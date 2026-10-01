package natsjs

import (
	"crypto/ed25519"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// ErrAutenticacao — o servidor recusou a identidade deste cliente, ou a autenticação não pôde
// sequer ser tentada (servidor exige credencial e não há; há credencial e o servidor não a pede).
//
// É sentinela própria, e não [ErrProtocol], porque a acção é do OPERADOR — credencial em falta,
// chave pública que não está na `authorization` do servidor, servidor sem `authorization` — e
// não do programador. Também não é [ErrDesligado]: repetir não cura nada.
var ErrAutenticacao = errors.New("natsjs: autenticação recusada")

// ErrNKeyInvalida — o material de credencial não é uma seed nkey de UTILIZADOR válida.
var ErrNKeyInvalida = errors.New("natsjs: seed nkey inválida")

// NKey é a credencial de cliente NATS por nkey (AOS-470): um par ed25519 cuja chave pública
// figura na `authorization { users = [ {nkey: U…} ] }` do servidor.
//
// # Porque nkey, e não utilizador/palavra-passe ou token
//
// Com nkey o segredo NUNCA atravessa o fio: o servidor manda um nonce no INFO, o cliente
// devolve a assinatura ed25519 dele e a chave pública. O servidor guarda só a chave pública —
// a configuração do cluster pode ser lida por quem a administra sem que isso dê a ninguém o
// poder de se ligar. Um token ou uma palavra-passe viajariam em claro no CONNECT (este cliente
// não fala TLS) e viveriam em claro no ficheiro do servidor.
//
// E é ed25519 da biblioteca padrão — zero dependências, coerente com ADR-017. A codificação
// (base32 sem padding, byte de prefixo, CRC-16/XMODEM) é a do pacote `nkeys` da NATS,
// reimplementada aqui e provada contra um `nats-server` real (scripts/ci/nats-cluster.sh).
//
// # O que NÃO fecha
//
// Sem TLS, quem está NO CAMINHO de uma ligação já autenticada pode injectar comandos nela: a
// assinatura prova quem abriu a sessão, não protege o que corre dentro dela. Em produção o
// caminho é o túnel WireGuard (deploy/nats). E quem lê a seed — root no host do nó `aos` —
// é o nó: a nkey estreita «quem alcança a porta» para «quem tem a seed», não mais do que isso.
type NKey struct {
	publica string
	privada ed25519.PrivateKey
}

// Bytes de prefixo da codificação nkey (o primeiro carácter base32 que produzem: S e U).
const (
	prefixoSeed       byte = 18 << 3 // 'S'
	prefixoUtilizador byte = 20 << 3 // 'U'
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// ParseNKeySeed lê uma seed nkey de UTILIZADOR (`SU…`), tal como `nk -gen user` ou
// `aos nats-nkey gerar` a escrevem. Espaço em redor é ignorado; qualquer outra coisa é
// recusada — incluindo uma seed de OUTRO tipo (conta, operador, servidor), porque o servidor
// só aceita chaves de utilizador na `authorization` e o erro seria descoberto só no handshake.
func ParseNKeySeed(seed []byte) (*NKey, error) {
	txt := strings.TrimSpace(string(seed))
	if txt == "" {
		return nil, fmt.Errorf("%w: vazia", ErrNKeyInvalida)
	}
	raw, err := descodificar(txt)
	if err != nil {
		return nil, err
	}
	if len(raw) != 2+ed25519.SeedSize {
		return nil, fmt.Errorf("%w: comprimento %d", ErrNKeyInvalida, len(raw))
	}
	if raw[0]&0xF8 != prefixoSeed {
		return nil, fmt.Errorf("%w: não é uma seed (começa por %q) — a chave PÚBLICA vai para o servidor, a SEED fica com o cliente", ErrNKeyInvalida, txt[:1])
	}
	tipo := (raw[0]&0x07)<<5 | (raw[1]&0xF8)>>3
	if tipo != prefixoUtilizador {
		return nil, fmt.Errorf("%w: seed de tipo %q, e o servidor só autentica utilizadores (SU…)", ErrNKeyInvalida, txt[:2])
	}
	priv := ed25519.NewKeyFromSeed(raw[2:])
	pub, err := codificar(prefixoUtilizador, priv.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, err
	}
	return &NKey{publica: pub, privada: priv}, nil
}

// LerNKeyFicheiro lê a seed de um ficheiro montado — a única forma de a credencial chegar ao
// nó (nunca uma variável de ambiente com o valor, que acaba em `docker inspect` e em logs).
//
// Fora de Windows recusa um ficheiro acessível a OUTROS (bits `o+rwx`), como o ssh faz com uma
// chave privada: uma seed legível por todos os utilizadores do host é uma credencial publicada.
func LerNKeyFicheiro(caminho string) (*NKey, error) {
	f, err := os.Open(caminho)
	if err != nil {
		return nil, fmt.Errorf("natsjs: ler a seed nkey: %w", err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("natsjs: ler a seed nkey: %w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s não é um ficheiro regular", ErrNKeyInvalida, caminho)
	}
	if runtime.GOOS != "windows" && st.Mode().Perm()&0o007 != 0 {
		return nil, fmt.Errorf("%w: %s tem modo %v — acessível a outros utilizadores do host; use 0400 ou 0440", ErrNKeyInvalida, caminho, st.Mode().Perm())
	}
	// Uma seed tem 58 caracteres; o tecto só impede que um caminho errado leia um disco.
	conteudo, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return nil, fmt.Errorf("natsjs: ler a seed nkey: %w", err)
	}
	k, err := ParseNKeySeed(conteudo)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", caminho, err)
	}
	return k, nil
}

// GerarNKeyUtilizador cria uma nkey de utilizador nova e devolve a seed (`SU…`, segredo do
// cliente) e a chave pública (`U…`, vai para a `authorization` do servidor). `aleatorio` nil
// usa crypto/rand.
func GerarNKeyUtilizador(aleatorio io.Reader) (seed, publica string, err error) {
	pub, priv, err := ed25519.GenerateKey(aleatorio)
	if err != nil {
		return "", "", fmt.Errorf("natsjs: gerar nkey: %w", err)
	}
	raw := make([]byte, 0, 2+ed25519.SeedSize+2)
	raw = append(raw, prefixoSeed|prefixoUtilizador>>5, (prefixoUtilizador&0x1F)<<3)
	raw = append(raw, priv.Seed()...)
	publica, err = codificar(prefixoUtilizador, pub)
	if err != nil {
		return "", "", err
	}
	return b32.EncodeToString(anexarCRC(raw)), publica, nil
}

// Publica é a chave pública (`U…`) — o que se declara no servidor, e o que este cliente
// apresenta no CONNECT. Não é segredo.
func (k *NKey) Publica() string { return k.publica }

// assinar devolve a assinatura do nonce do INFO no formato que o servidor espera (base64 URL
// sem padding — o do cliente oficial).
func (k *NKey) assinar(nonce string) string {
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(k.privada, []byte(nonce)))
}

// codificar produz a forma textual de uma chave pública: prefixo, 32 bytes, CRC-16 LE, base32.
func codificar(prefixo byte, chave []byte) (string, error) {
	if len(chave) != ed25519.PublicKeySize {
		return "", fmt.Errorf("%w: chave de %d bytes", ErrNKeyInvalida, len(chave))
	}
	raw := make([]byte, 0, 1+len(chave)+2)
	raw = append(raw, prefixo)
	raw = append(raw, chave...)
	return b32.EncodeToString(anexarCRC(raw)), nil
}

// descodificar inverte a base32 e verifica o CRC — um carácter trocado ao copiar a seed é
// apanhado aqui, e não no handshake como «Authorization Violation» sem mais explicação.
func descodificar(txt string) ([]byte, error) {
	raw, err := b32.DecodeString(txt)
	if err != nil {
		return nil, fmt.Errorf("%w: base32: %v", ErrNKeyInvalida, err)
	}
	if len(raw) < 3 {
		return nil, fmt.Errorf("%w: curta demais", ErrNKeyInvalida)
	}
	corpo, crc := raw[:len(raw)-2], binary.LittleEndian.Uint16(raw[len(raw)-2:])
	if crc16(corpo) != crc {
		return nil, fmt.Errorf("%w: CRC não confere (seed truncada ou mal copiada)", ErrNKeyInvalida)
	}
	return corpo, nil
}

func anexarCRC(b []byte) []byte {
	return binary.LittleEndian.AppendUint16(b, crc16(b))
}

// crc16 é o CRC-16/XMODEM (polinómio 0x1021, valor inicial 0) que o formato nkey usa.
func crc16(b []byte) uint16 {
	var crc uint16
	for _, v := range b {
		crc ^= uint16(v) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
