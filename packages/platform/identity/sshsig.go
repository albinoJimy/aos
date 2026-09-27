package identity

// sshsig.go — VERIFICAÇÃO DE ASSINATURAS FIDO2 `sk-ssh-ed25519@openssh.com` NO FORMATO SSHSIG
// (AOS-446 fase 1, ADR-033 §6.5).
//
// # PORQUE EXISTE
//
// A chave que assina os mandatos era uma seed ed25519 num ficheiro, em hex e em claro, na máquina
// do humano (ADR-033 §6.1). Quem copie esse ficheiro assina mandatos para sempre, sem o humano dar
// por isso: o mandato — o artefacto que autoriza a cunhagem sem operador — depende de um segredo
// que se copia com um `cat`. Uma chave FIDO2 residente num autenticador não se copia: a assinatura
// só existe se alguém tocar no dispositivo, e o `flags` do próprio blob prova-o.
//
// # PORQUE EM STDLIB, E PORQUE SÓ VERIFICAÇÃO
//
// O ambiente de build é OFFLINE (`GOPROXY=off`): não se acrescentam módulos. O formato, ao
// contrário do que a palavra «SSH» sugere, é um punhado de strings com prefixo de comprimento —
// `encoding/binary`, `crypto/sha256`, `crypto/sha512` e `crypto/ed25519` chegam. ASSINAR exigiria
// falar CTAP2/USB-HID com o autenticador, que é exactamente o que o `ssh-keygen -Y sign` já faz:
// o humano assina com a ferramenta dele, e o AOS verifica. É a mesma divisão do resto do
// repositório — o nó CONSOME âncoras que assinam fora dele.
//
// # A FORMA, E COMO FOI PROVADA
//
// Não foi lida de memória. Construíram-se vectores com esta mesma serialização e entregaram-se ao
// `ssh-keygen -Y verify` do OpenSSH 10.3p1, que os aceitou; mutar um byte do contador, trocar a
// namespace ou trocar a mensagem fá-lo recusar os três. Os vectores ficaram congelados em
// `testdata/` e o teste `TestAOS446SSHSIGVectoresGolden` corre contra eles.
//
// Contentor SSHSIG (PROTOCOL.sshsig), dentro do armor `-----BEGIN SSH SIGNATURE-----`:
//
//	byte[6]  "SSHSIG"
//	uint32   versao (1)
//	string   publickey   (blob da chave pública)
//	string   namespace
//	string   reserved    (vazio)
//	string   hash_algorithm ("sha256" | "sha512")
//	string   signature   (blob da assinatura)
//
// Bytes assinados (o «signed-data»), com H = o hash_algorithm acima:
//
//	byte[6]  "SSHSIG"
//	string   namespace
//	string   reserved
//	string   hash_algorithm
//	string   H(mensagem)
//
// Blob da chave `sk-ssh-ed25519@openssh.com`:
//
//	string   "sk-ssh-ed25519@openssh.com"
//	string   pubkey ed25519 (32 bytes)
//	string   application    (ex.: "ssh:aos-mandate")
//
// Blob da assinatura:
//
//	string   "sk-ssh-ed25519@openssh.com"
//	string   assinatura ed25519 crua (64 bytes)
//	byte     flags
//	uint32   counter
//
// E o que o AUTENTICADOR assinou não é o signed-data: é a adaptação U2F/WebAuthn que o OpenSSH
// usa, com o hash da `application` no papel do rpIdHash e o hash do signed-data no papel do
// desafio —
//
//	SHA-256(application) || flags || counter (big-endian) || SHA-256(signed-data)
//
// — sempre SHA-256 nestes dois, independentemente do `hash_algorithm` do contentor (esse só cobre
// a mensagem). Confirmado empiricamente: um vector com `hash_algorithm=sha512` e estes dois em
// SHA-256 é aceite pelo `ssh-keygen`.
//
// # ONDE SOMOS MAIS ESTRITOS DO QUE O OpenSSH, E PORQUÊ
//
// MEDIDO, não suposto: um vector com `flags = 0x00` — nenhuma presença de utilizador — é aceite
// pelo `ssh-keygen -Y verify` sem uma palavra. Para uma chave de host isso é razoável; para um
// MANDATO é o contrário de tudo o que ele existe para provar. Se um autenticador comprometido (ou
// um `ssh-agent` a quem se pediu a chave) produzir uma assinatura sem toque, o mandato vale o
// mesmo que a seed em ficheiro que isto veio substituir. Por isso [VerifySSHSIG] EXIGE
// `flags & 0x01` e recusa fail-closed quem não o traga.

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const (
	// SSHSigAlgSKEd25519 é o ÚNICO algoritmo que se aceita: uma chave FIDO2 ed25519 residente
	// num autenticador. `ssh-ed25519` (software) é RECUSADO de propósito — ver
	// [ErrSSHSigAlgo].
	SSHSigAlgSKEd25519 = "sk-ssh-ed25519@openssh.com"
	// sshsigMagic é o preâmbulo de 6 bytes, cru (NÃO é uma string com comprimento).
	sshsigMagic = "SSHSIG"
	// sshsigVersion é a única versão do contentor que existe.
	sshsigVersion = 1
	// sshsigUserPresence é o bit que o autenticador põe quando alguém lhe tocou (`UP`, 0x01 —
	// o mesmo bit do `authenticatorData` do WebAuthn).
	sshsigUserPresence = 0x01
	// sshsigUserVerified é o bit de verificação do utilizador (PIN/biometria, `UV`, 0x04).
	// Não é exigido: nem todos os autenticadores o fazem, e o que o mandato precisa de provar é
	// presença. Vai para o diagnóstico.
	sshsigUserVerified = 0x04
	// sshsigArmorInicio/Fim delimitam o armor do `ssh-keygen -Y sign`.
	sshsigArmorInicio = "-----BEGIN SSH SIGNATURE-----"
	sshsigArmorFim    = "-----END SSH SIGNATURE-----"
	// sshsigMaxBytes é o tecto do blob descodificado. Um mandato assinado viaja EMBEBIDO em cada
	// token; sem tecto, um contentor de megabytes passaria pelo parser antes de a assinatura o
	// recusar. 8 KiB é ordens de grandeza acima do que um SSHSIG legítimo ocupa (~400 bytes).
	sshsigMaxBytes = 8 << 10
	// bomUTF8Rune é o BOM UTF-8, escrito como escape e não como o caractere (um BOM literal a
	// meio de um ficheiro Go é um erro de compilação — e foi assim que apareceu).
	bomUTF8Rune = "\xef\xbb\xbf"
)

// ErrSSHSig — o envelope SSHSIG está malformado, é de outra namespace, de outra chave, ou a
// assinatura não verifica. Uma só sentinela para toda a família: o chamador embrulha-a em
// [ErrMandateInvalid], e a mensagem diz qual dos casos foi.
var ErrSSHSig = errors.New("assinatura SSHSIG invalida")

// ErrSSHSigAlgo — o SSHSIG não é de uma chave FIDO2. RECUSADO, e é uma decisão e não uma
// limitação: aceitar `ssh-ed25519` deixaria uma chave de SOFTWARE assinar um mandato pelo caminho
// que existe para exigir HARDWARE, e o campo `fmt` do mandato passaria a dizer «FIDO2» sobre uma
// seed em ficheiro. Um humano que queira assinar por software já tem o caminho ed25519 cru.
var ErrSSHSigAlgo = fmt.Errorf("%w: so se aceita %s (uma chave FIDO2 residente); `ssh-ed25519` de software e recusado de proposito", ErrSSHSig, SSHSigAlgSKEd25519)

// ErrSSHSigSemToque — a assinatura não traz a presença de utilizador. Ver o bloco «ONDE SOMOS
// MAIS ESTRITOS DO QUE O OpenSSH» no topo do ficheiro.
var ErrSSHSigSemToque = fmt.Errorf("%w: flags sem o bit de PRESENCA DE UTILIZADOR (0x01) — o mandato existe para provar que um humano TOCOU na chave, e o `ssh-keygen -Y verify` aceita isto sem o exigir", ErrSSHSig)

// SKPublicKey é uma chave pública FIDO2 `sk-ssh-ed25519@openssh.com`: a chave ed25519 do
// autenticador e a `application` a que está presa. As duas são autoridade: a `application` entra
// HASHADA no que o autenticador assinou, pelo que uma assinatura produzida para outra application
// (por exemplo `ssh:` genérico do `ssh-keygen -t ed25519-sk` sem `-O application=`) não verifica
// contra um pino que fixe `ssh:aos-mandate`.
type SKPublicKey struct {
	Key         ed25519.PublicKey
	Application string
	// blob é o blob canónico da chave, tal como aparece numa linha de `authorized_keys` e dentro
	// do contentor SSHSIG. Guarda-se para a comparação ser de BYTES: o contentor traz a sua
	// própria cópia da chave pública, e é preciso exigir que seja EXACTAMENTE a pinada — comparar
	// campo a campo deixaria passar uma codificação diferente dos mesmos valores.
	blob []byte
}

// Blob devolve o blob canónico da chave (o que vai em base64 numa linha de `authorized_keys`).
func (k SKPublicKey) Blob() []byte { return append([]byte(nil), k.blob...) }

// SSHFingerprint devolve a impressão digital no formato do OpenSSH — `SHA256:<base64>` —, a MESMA
// que `ssh-keygen -lf chave.pub` imprime. É deliberado que coincida: o operador confere o pino do
// nó contra o que a ferramenta dele mostra, sem converter nada.
func (k SKPublicKey) SSHFingerprint() string {
	sum := sha256.Sum256(k.blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// ParseSKPublicKey lê uma chave pública `sk-ssh-ed25519@openssh.com` na forma de `authorized_keys`
// (`sk-ssh-ed25519@openssh.com AAAA…` com um comentário opcional) ou só o base64 do blob.
func ParseSKPublicKey(texto string) (SKPublicKey, error) {
	campos := strings.Fields(strings.TrimSpace(texto))
	if len(campos) == 0 {
		return SKPublicKey{}, fmt.Errorf("%w: chave publica vazia", ErrSSHSig)
	}
	b64 := campos[0]
	if len(campos) >= 2 {
		if campos[0] != SSHSigAlgSKEd25519 {
			return SKPublicKey{}, fmt.Errorf("%w: tipo %q", ErrSSHSigAlgo, campos[0])
		}
		b64 = campos[1]
	}
	blob, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return SKPublicKey{}, fmt.Errorf("%w: base64 da chave publica ilegivel", ErrSSHSig)
	}
	return parseSKPublicKeyBlob(blob)
}

// parseSKPublicKeyBlob lê o blob binário da chave.
func parseSKPublicKeyBlob(blob []byte) (SKPublicKey, error) {
	if len(blob) > sshsigMaxBytes {
		return SKPublicKey{}, fmt.Errorf("%w: blob da chave publica com %d bytes (tecto %d)", ErrSSHSig, len(blob), sshsigMaxBytes)
	}
	r := &sshReader{b: blob}
	alg, err := r.str()
	if err != nil {
		return SKPublicKey{}, fmt.Errorf("%w: tipo da chave: %v", ErrSSHSig, err)
	}
	if string(alg) != SSHSigAlgSKEd25519 {
		return SKPublicKey{}, fmt.Errorf("%w: tipo %q", ErrSSHSigAlgo, string(alg))
	}
	pub, err := r.str()
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return SKPublicKey{}, fmt.Errorf("%w: pubkey ed25519 de %d bytes (esperados %d)", ErrSSHSig, len(pub), ed25519.PublicKeySize)
	}
	app, err := r.str()
	if err != nil {
		return SKPublicKey{}, fmt.Errorf("%w: application: %v", ErrSSHSig, err)
	}
	if len(app) == 0 {
		return SKPublicKey{}, fmt.Errorf("%w: application vazia — e ela que prende a assinatura a este uso", ErrSSHSig)
	}
	// SOBRAS SÃO RECUSA. Um blob com bytes a mais é um blob que duas implementações leem de
	// maneiras diferentes, e o pino de uma âncora de confiança não é sítio para tolerância.
	if !r.fim() {
		return SKPublicKey{}, fmt.Errorf("%w: %d bytes a mais depois da application", ErrSSHSig, r.resto())
	}
	return SKPublicKey{
		Key:         ed25519.PublicKey(append([]byte(nil), pub...)),
		Application: string(app),
		blob:        append([]byte(nil), blob...),
	}, nil
}

// SSHSigDetalhe é o que a verificação apurou sobre o gesto físico: os bits do autenticador e o
// contador que ele incrementa a cada assinatura.
type SSHSigDetalhe struct {
	Flags byte
	// Counter é o contador que o autenticador incrementa a cada assinatura.
	//
	// RESÍDUO DECLARADO (achado A5 da revisão adversarial, 2026-09-27): aceita-se QUALQUER
	// valor, incluindo um MENOR do que o de uma assinatura anterior da mesma chave. O contador
	// existe, no WebAuthn, para detectar um autenticador CLONADO — e detectá-lo exige guardar o
	// último valor visto por chave e recusar um recuo, que é estado persistente que este
	// verificador não tem (nem o `ssh-keygen -Y verify`, que também o ignora).
	//
	// O impacto aqui é nulo por uma razão concreta e não por optimismo: um mandato é assinado
	// UMA vez e verificado muitas, sempre sobre os MESMOS bytes — não há um segundo mandato
	// legítimo do mesmo humano cuja ordem importe, e um clone da chave física assina um mandato
	// novo com um contador qualquer, que o registo das âncoras não distingue do original porque
	// a chave pública é a mesma. Quem fecha o clone é a custódia do autenticador, não o contador.
	Counter uint32
	// HashAlgorithm é o hash com que a MENSAGEM foi resumida ("sha256" ou "sha512").
	HashAlgorithm string
}

// UserVerified diz se o autenticador afirmou ter verificado o utilizador (PIN/biometria).
func (d SSHSigDetalhe) UserVerified() bool { return d.Flags&sshsigUserVerified != 0 }

// VerifySSHSIG verifica uma assinatura SSHSIG `sk-ssh-ed25519@openssh.com` sobre `mensagem`.
//
// `envelope` é o blob do contentor (já sem armor — ver [DecodeSSHSIGArmor]), `pino` é a chave que
// se ACEITA, e `namespace` a que se EXIGE. Devolve o detalhe do gesto quando aceita.
//
// A ordem das verificações é deliberada, e é a mesma disciplina do [Verifier]: primeiro o que
// identifica (chave e namespace), depois a criptografia, e a presença de utilizador no fim — o
// bit de `flags` viaja DENTRO do que foi assinado, pelo que só se lê depois de a assinatura o
// provar. Lê-lo antes seria acreditar num byte que o atacante escolhe.
func VerifySSHSIG(envelope []byte, pino SKPublicKey, namespace string, mensagem []byte) (SSHSigDetalhe, error) {
	var det SSHSigDetalhe
	if len(pino.blob) == 0 || len(pino.Key) != ed25519.PublicKeySize {
		return det, fmt.Errorf("%w: pino nao e uma chave sk-ssh-ed25519 valida", ErrSSHSig)
	}
	if strings.TrimSpace(namespace) == "" {
		return det, fmt.Errorf("%w: namespace exigida vazia", ErrSSHSig)
	}
	if len(envelope) > sshsigMaxBytes {
		return det, fmt.Errorf("%w: envelope com %d bytes (tecto %d)", ErrSSHSig, len(envelope), sshsigMaxBytes)
	}
	if !bytes.HasPrefix(envelope, []byte(sshsigMagic)) {
		return det, fmt.Errorf("%w: sem o preambulo %q", ErrSSHSig, sshsigMagic)
	}
	r := &sshReader{b: envelope[len(sshsigMagic):]}
	ver, err := r.u32()
	if err != nil {
		return det, fmt.Errorf("%w: versao: %v", ErrSSHSig, err)
	}
	if ver != sshsigVersion {
		return det, fmt.Errorf("%w: versao %d (so a %d existe)", ErrSSHSig, ver, sshsigVersion)
	}
	pkBlob, err := r.str()
	if err != nil {
		return det, fmt.Errorf("%w: chave publica do envelope: %v", ErrSSHSig, err)
	}
	// A CHAVE DO ENVELOPE TEM DE SER, BYTE A BYTE, A PINADA. É esta linha que impede que quem
	// entrega o mandato traga a chave dele lá dentro e a assinatura verifique contra ela.
	if !bytes.Equal(pkBlob, pino.blob) {
		got, gerr := parseSKPublicKeyBlob(pkBlob)
		quem := "ilegivel"
		if gerr == nil {
			quem = got.SSHFingerprint()
		}
		return det, fmt.Errorf("%w: o envelope traz a chave %s e o pino e %s", ErrSSHSig, quem, pino.SSHFingerprint())
	}
	ns, err := r.str()
	if err != nil {
		return det, fmt.Errorf("%w: namespace: %v", ErrSSHSig, err)
	}
	if string(ns) != namespace {
		return det, fmt.Errorf("%w: namespace %q, esperada %q — uma assinatura de outro uso nao vale aqui", ErrSSHSig, string(ns), namespace)
	}
	reserved, err := r.str()
	if err != nil {
		return det, fmt.Errorf("%w: reserved: %v", ErrSSHSig, err)
	}
	if len(reserved) != 0 {
		return det, fmt.Errorf("%w: campo reserved nao-vazio (%d bytes)", ErrSSHSig, len(reserved))
	}
	hashAlg, err := r.str()
	if err != nil {
		return det, fmt.Errorf("%w: hash_algorithm: %v", ErrSSHSig, err)
	}
	mh, err := hashMensagem(string(hashAlg), mensagem)
	if err != nil {
		return det, err
	}
	sigBlob, err := r.str()
	if err != nil {
		return det, fmt.Errorf("%w: blob da assinatura: %v", ErrSSHSig, err)
	}
	if !r.fim() {
		return det, fmt.Errorf("%w: %d bytes a mais depois da assinatura", ErrSSHSig, r.resto())
	}

	raw, flags, counter, err := parseSKSigBlob(sigBlob)
	if err != nil {
		return det, err
	}
	det = SSHSigDetalhe{Flags: flags, Counter: counter, HashAlgorithm: string(hashAlg)}

	// O «signed-data»: o que o `ssh-keygen` considera assinado.
	var sd []byte
	sd = append(sd, sshsigMagic...)
	sd = sshString(sd, []byte(namespace))
	sd = sshString(sd, nil)
	sd = sshString(sd, hashAlg)
	sd = sshString(sd, mh)

	// E o que o AUTENTICADOR assinou: a adaptação U2F.
	apphash := sha256.Sum256([]byte(pino.Application))
	sdhash := sha256.Sum256(sd)
	inner := make([]byte, 0, len(apphash)+1+4+len(sdhash))
	inner = append(inner, apphash[:]...)
	inner = append(inner, flags)
	inner = sshUint32(inner, counter)
	inner = append(inner, sdhash[:]...)

	if !ed25519.Verify(pino.Key, inner, raw) {
		return det, fmt.Errorf("%w: a assinatura nao verifica contra %s", ErrSSHSig, pino.SSHFingerprint())
	}
	if flags&sshsigUserPresence == 0 {
		return det, ErrSSHSigSemToque
	}
	return det, nil
}

// parseSKSigBlob lê o blob da assinatura `sk-ssh-ed25519@openssh.com`.
func parseSKSigBlob(blob []byte) (raw []byte, flags byte, counter uint32, err error) {
	r := &sshReader{b: blob}
	alg, err := r.str()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%w: tipo da assinatura: %v", ErrSSHSig, err)
	}
	if string(alg) != SSHSigAlgSKEd25519 {
		return nil, 0, 0, fmt.Errorf("%w: tipo de assinatura %q", ErrSSHSigAlgo, string(alg))
	}
	raw, err = r.str()
	if err != nil || len(raw) != ed25519.SignatureSize {
		return nil, 0, 0, fmt.Errorf("%w: assinatura ed25519 de %d bytes (esperados %d)", ErrSSHSig, len(raw), ed25519.SignatureSize)
	}
	flags, err = r.u8()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%w: flags: %v", ErrSSHSig, err)
	}
	counter, err = r.u32()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("%w: counter: %v", ErrSSHSig, err)
	}
	if !r.fim() {
		return nil, 0, 0, fmt.Errorf("%w: %d bytes a mais no blob da assinatura", ErrSSHSig, r.resto())
	}
	return raw, flags, counter, nil
}

// hashMensagem resume a mensagem com o algoritmo que o contentor declara. Um algoritmo
// desconhecido NÃO se adivinha: recusa-se.
func hashMensagem(alg string, msg []byte) ([]byte, error) {
	switch alg {
	case "sha512":
		h := sha512.Sum512(msg)
		return h[:], nil
	case "sha256":
		h := sha256.Sum256(msg)
		return h[:], nil
	default:
		return nil, fmt.Errorf("%w: hash_algorithm %q (so sha256 e sha512)", ErrSSHSig, alg)
	}
}

// DecodeSSHSIGArmor retira o armor `-----BEGIN SSH SIGNATURE-----` e devolve o blob do contentor.
//
// EXIGE O ARMOR, E EXIGE QUE SEJA UM SÓ (achado A5 da revisão adversarial, 2026-09-27). A versão
// anterior era tolerante em três eixos, e nenhum deles servia ninguém:
//
//   - aceitava base64 CRU sem armor — o `ssh-keygen -Y sign` produz sempre armor, e o que entra
//     por aqui é o ficheiro que ele escreveu;
//   - ignorava tudo o que viesse DEPOIS do `END` — um ficheiro com uma assinatura e lixo a
//     seguir lia-se como se estivesse limpo;
//   - com DOIS blocos, usava o primeiro em silêncio — e um ficheiro com duas assinaturas é
//     exactamente a forma de fazer duas ferramentas lerem coisas diferentes do mesmo byte-stream.
//
// O BOM inicial continua a ser tolerado, e só ele: é a mesma razão de [limparSeedHex] no emissor
// (estes ficheiros nascem em Windows, e o `>` do PowerShell escreve BOM).
func DecodeSSHSIGArmor(texto string) ([]byte, error) {
	t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(texto), bomUTF8Rune))
	if strings.Count(t, sshsigArmorInicio) != 1 || strings.Count(t, sshsigArmorFim) != 1 {
		return nil, fmt.Errorf("%w: esperado exactamente UM bloco %q…%q (encontrados %d inicio(s) e %d fim(ns))",
			ErrSSHSig, sshsigArmorInicio, sshsigArmorFim, strings.Count(t, sshsigArmorInicio), strings.Count(t, sshsigArmorFim))
	}
	if !strings.HasPrefix(t, sshsigArmorInicio) {
		return nil, fmt.Errorf("%w: ha texto antes de %q", ErrSSHSig, sshsigArmorInicio)
	}
	if !strings.HasSuffix(t, sshsigArmorFim) {
		return nil, fmt.Errorf("%w: ha texto depois de %q", ErrSSHSig, sshsigArmorFim)
	}
	corpo := t[len(sshsigArmorInicio) : len(t)-len(sshsigArmorFim)]
	limpo := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, corpo)
	if limpo == "" {
		return nil, fmt.Errorf("%w: assinatura vazia", ErrSSHSig)
	}
	blob, err := base64.StdEncoding.DecodeString(limpo)
	if err != nil {
		return nil, fmt.Errorf("%w: base64 do envelope ilegivel: %v", ErrSSHSig, err)
	}
	return blob, nil
}

// EncodeSSHSIGArmor é o inverso de [DecodeSSHSIGArmor]. Serve os testes e o `aos-issuer`, que
// re-emite o envelope para dentro do mandato.
func EncodeSSHSIGArmor(blob []byte) string {
	b64 := base64.StdEncoding.EncodeToString(blob)
	var b strings.Builder
	b.WriteString(sshsigArmorInicio)
	b.WriteByte('\n')
	for i := 0; i < len(b64); i += 70 {
		j := i + 70
		if j > len(b64) {
			j = len(b64)
		}
		b.WriteString(b64[i:j])
		b.WriteByte('\n')
	}
	b.WriteString(sshsigArmorFim)
	b.WriteByte('\n')
	return b.String()
}

// ------------------------------------------------------------------------------------------
// Leitor do formato de fio do SSH: inteiros big-endian e strings com prefixo de comprimento.
// ------------------------------------------------------------------------------------------

type sshReader struct {
	b []byte
	i int
}

func (r *sshReader) fim() bool  { return r.i >= len(r.b) }
func (r *sshReader) resto() int { return len(r.b) - r.i }

func (r *sshReader) u8() (byte, error) {
	if r.i+1 > len(r.b) {
		return 0, errors.New("fim prematuro")
	}
	v := r.b[r.i]
	r.i++
	return v, nil
}

func (r *sshReader) u32() (uint32, error) {
	if r.i+4 > len(r.b) {
		return 0, errors.New("fim prematuro")
	}
	v := binary.BigEndian.Uint32(r.b[r.i : r.i+4])
	r.i += 4
	return v, nil
}

func (r *sshReader) str() ([]byte, error) {
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	// O COMPRIMENTO VEM DO ATACANTE. Compara-se contra o que RESTA — e não se converte para int
	// antes de o validar: num alvo de 32 bits um n perto de 2^32 daria a volta e passaria a um
	// índice pequeno e válido.
	restam := len(r.b) - r.i
	if restam < 0 {
		// INALCANÇÁVEL pela invariante do leitor (`r.i` nunca passa `len(r.b)`), e escrito na
		// mesma: é o que torna a conversão abaixo provavelmente segura em vez de só provavelmente
		// segura, e o que um leitor futuro precisa de ver antes de mexer no índice.
		return nil, errors.New("leitor fora de limites")
	}
	if uint64(n) > uint64(restam) { // #nosec G115 -- restam >= 0, garantido na linha acima
		return nil, fmt.Errorf("comprimento %d acima dos %d bytes que restam", n, restam)
	}
	s := r.b[r.i : r.i+int(n)]
	r.i += int(n)
	return s, nil
}

func sshString(b []byte, s []byte) []byte {
	// O comprimento de uma string SSH é um uint32, e tudo o que este pacote serializa vem de um
	// envelope já limitado a [sshsigMaxBytes] ou é um hash de tamanho fixo. O corte torna isso
	// uma garantia em vez de um pressuposto — e um blob cortado não verifica, que é a saída
	// segura.
	if len(s) > sshsigMaxBytes {
		s = s[:sshsigMaxBytes]
	}
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(s))) // #nosec G115 -- len(s) <= sshsigMaxBytes, garantido acima
	b = append(b, l[:]...)
	return append(b, s...)
}

func sshUint32(b []byte, v uint32) []byte {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], v)
	return append(b, l[:]...)
}
