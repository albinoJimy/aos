package natsjs

import (
	"bufio"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Testes da credencial nkey (AOS-470). A prova de que a codificação é a do `nkeys` oficial é
// feita contra um nats-server REAL (integracao_test.go, com o cluster de scripts/ci/
// nats-cluster.sh em modo autorizado): o servidor recusa arrancar com uma chave pública mal
// codificada e recusa o CONNECT com uma assinatura que não confere. Estes testes cobrem o que
// não precisa de servidor.

func TestNKey_CRC16EOXMODEM(t *testing.T) {
	// Valor de verificação canónico do CRC-16/XMODEM. Outro CRC-16 (ARC, CCITT-FALSE…) daria
	// chaves que o servidor rejeita por CRC.
	if got := crc16([]byte("123456789")); got != 0x31C3 {
		t.Fatalf("crc16 = %#04x, quer 0x31c3", got)
	}
}

func TestNKey_GerarELerSaoInversos(t *testing.T) {
	seed, pub, err := GerarNKeyUtilizador(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(seed, "SU") || len(seed) != 58 {
		t.Fatalf("seed %q: quer 58 caracteres a começar por SU", seed)
	}
	if !strings.HasPrefix(pub, "U") || len(pub) != 56 {
		t.Fatalf("pública %q: quer 56 caracteres a começar por U", pub)
	}
	k, err := ParseNKeySeed([]byte("  " + seed + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if k.Publica() != pub {
		t.Fatalf("pública derivada da seed = %q, quer %q", k.Publica(), pub)
	}

	// A assinatura do nonce verifica-se com a chave que a pública codifica.
	raw, err := descodificar(pub)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(k.assinar("nonce-do-servidor"))
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(ed25519.PublicKey(raw[1:]), []byte("nonce-do-servidor"), sig) {
		t.Fatal("assinatura do nonce não verifica com a chave pública anunciada")
	}
}

func TestNKey_RecusaMaterialQueNaoESeedDeUtilizador(t *testing.T) {
	seed, pub, err := GerarNKeyUtilizador(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Seed de CONTA (prefixo 0, «SA…»): bem formada, mas o servidor não a autentica como cliente.
	raw, _ := descodificar(seed)
	conta := append([]byte{prefixoSeed, 0}, raw[2:]...)
	seedConta := b32.EncodeToString(anexarCRC(conta))

	trocado := []byte(seed)
	if trocado[20] == 'A' {
		trocado[20] = 'B'
	} else {
		trocado[20] = 'A'
	}

	casos := map[string]string{
		"vazia":            "  \n",
		"chave pública":    pub,
		"carácter trocado": string(trocado),
		"truncada":         seed[:40],
		"seed de conta":    seedConta,
		"não é base32":     "SU!!!!",
		"palavra-passe":    "hunter2",
	}
	for nome, v := range casos {
		if _, err := ParseNKeySeed([]byte(v)); !errors.Is(err, ErrNKeyInvalida) {
			t.Errorf("%s: err = %v, quer ErrNKeyInvalida", nome, err)
		}
	}
}

func TestNKey_FicheiroAcessivelAOutrosERecusado(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("o modo POSIX não existe em Windows; a verificação só corre fora dele")
	}
	seed, _, err := GerarNKeyUtilizador(nil)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "aos.nk")
	if err := os.WriteFile(p, []byte(seed+"\n"), 0o604); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o604); err != nil {
		t.Fatal(err)
	}
	if _, err := LerNKeyFicheiro(p); !errors.Is(err, ErrNKeyInvalida) {
		t.Fatalf("seed legível por todos aceite: err = %v", err)
	}
	if err := os.Chmod(p, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := LerNKeyFicheiro(p); err != nil {
		t.Fatalf("seed 0400 recusada: %v", err)
	}
}

func TestNKey_FicheiroAusenteEErro(t *testing.T) {
	if _, err := LerNKeyFicheiro(filepath.Join(t.TempDir(), "nao-existe")); err == nil {
		t.Fatal("ficheiro inexistente aceite")
	}
}

// servidorDeHandshake aceita UMA ligação, envia `info`, lê o CONNECT e entrega-o ao teste
// por `connects`, e responde ao PING com `resposta`.
func servidorDeHandshake(t *testing.T, info, resposta string) (addr string, connects <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = io.WriteString(c, info)
		br := bufio.NewReader(c)
		linha, err := br.ReadString('\n')
		if err != nil {
			close(ch)
			return
		}
		ch <- linha
		if _, err := br.ReadString('\n'); err != nil { // PING
			return
		}
		_, _ = io.WriteString(c, resposta)
		_, _ = io.Copy(io.Discard, br)
	}()
	return ln.Addr().String(), ch
}

func TestHandshake_ComCredencialAssinaONonceDoINFO(t *testing.T) {
	seed, pub, _ := GerarNKeyUtilizador(nil)
	k, _ := ParseNKeySeed([]byte(seed))
	addr, connects := servidorDeHandshake(t,
		`INFO {"headers":true,"max_payload":1048576,"auth_required":true,"nonce":"abc123"}`+"\r\n", "PONG\r\n")

	cn, err := ConnectServersCom([]string{addr}, 3*time.Second, k)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = cn.Close() }()

	linha := <-connects
	var opts connectOpts
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(linha, "CONNECT "))), &opts); err != nil {
		t.Fatalf("CONNECT ilegível %q: %v", linha, err)
	}
	if opts.NKey != pub {
		t.Fatalf("CONNECT.nkey = %q, quer %q", opts.NKey, pub)
	}
	raw, _ := descodificar(pub)
	sig, err := base64.RawURLEncoding.DecodeString(opts.Sig)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(raw[1:]), []byte("abc123"), sig) {
		t.Fatalf("CONNECT.sig não é a assinatura do nonce do INFO (err=%v)", err)
	}
	if strings.Contains(linha, seed) {
		t.Fatal("a SEED atravessou o fio no CONNECT")
	}
	if !cn.Ligada() {
		t.Fatal("ligação aceite mas não marcada como ligada")
	}
}

func TestHandshake_RecusaDoServidorEErrAutenticacaoNoConnect(t *testing.T) {
	// O defeito que o PING do handshake fecha: sem ele, Connect devolvia SUCESSO e a recusa
	// chegava depois, ao leitor, como uma ligação partida — e o cliente reconectava para sempre.
	seed, _, _ := GerarNKeyUtilizador(nil)
	k, _ := ParseNKeySeed([]byte(seed))
	addr, _ := servidorDeHandshake(t,
		`INFO {"headers":true,"auth_required":true,"nonce":"n"}`+"\r\n", "-ERR 'Authorization Violation'\r\n")
	_, err := ConnectServersCom([]string{addr}, 3*time.Second, k)
	if !errors.Is(err, ErrAutenticacao) {
		t.Fatalf("err = %v, quer ErrAutenticacao", err)
	}
}

func TestHandshake_SemCredencialContraServidorQueExigeERecusadoAntesDoCONNECT(t *testing.T) {
	addr, connects := servidorDeHandshake(t,
		`INFO {"headers":true,"auth_required":true,"nonce":"n"}`+"\r\n", "PONG\r\n")
	_, err := ConnectServersCom([]string{addr}, 3*time.Second, nil)
	if !errors.Is(err, ErrAutenticacao) {
		t.Fatalf("err = %v, quer ErrAutenticacao", err)
	}
	if l, ok := <-connects; ok {
		t.Fatalf("CONNECT enviado sem credencial a um servidor que a exige: %q", l)
	}
}

func TestHandshake_ComCredencialContraServidorSemAuthorizationERecusado(t *testing.T) {
	// Fail-closed: credencial configurada e servidor sem `authorization` é um cluster onde
	// qualquer um escreve. Ligar em silêncio desfazia o que a credencial veio garantir.
	seed, _, _ := GerarNKeyUtilizador(nil)
	k, _ := ParseNKeySeed([]byte(seed))
	addr, _ := servidorDeHandshake(t, `INFO {"headers":true,"max_payload":1048576}`+"\r\n", "PONG\r\n")
	_, err := ConnectServersCom([]string{addr}, 3*time.Second, k)
	if !errors.Is(err, ErrAutenticacao) {
		t.Fatalf("err = %v, quer ErrAutenticacao", err)
	}
}

func TestHandshake_SemPONGNoPrazoFalha(t *testing.T) {
	addr, _ := servidorDeHandshake(t, `INFO {"headers":true}`+"\r\n", "")
	if _, err := ConnectServersCom([]string{addr}, 300*time.Millisecond, nil); err == nil {
		t.Fatal("handshake sem PONG aceite")
	}
}
