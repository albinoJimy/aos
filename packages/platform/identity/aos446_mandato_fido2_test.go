package identity

// aos446_mandato_fido2_test.go — o mandato assinado por FIDO2, e a reconciliação com o v1 e o v2.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// pinoDeSoftware / pinosDeSoftware embrulham uma pubkey ed25519 no pino de software. Existem para
// os testes anteriores ao AOS-446 continuarem a dizer o que diziam, agora que o pino é um tipo.
func pinoDeSoftware(t *testing.T, pub ed25519.PublicKey) MandateSigner {
	t.Helper()
	s, err := RawMandateSigner(pub)
	if err != nil {
		t.Fatalf("pino de software: %v", err)
	}
	return s
}

// pinosRaw é a mesma conversão sem `t`, para os helpers de cenário que são métodos. Uma chave
// malformada é SALTADA — é o que o [WithMandatedIssuer] fazia antes, e há um teste
// (`…SemSignatariosNaoEConfiado`) que depende disso.
func pinosRaw(m map[string]ed25519.PublicKey) map[string][]MandateSigner {
	out := make(map[string][]MandateSigner, len(m))
	for k, v := range m {
		if s, err := RawMandateSigner(v); err == nil {
			out[k] = []MandateSigner{s}
		}
	}
	return out
}

// autenticadorFalso simula o que um autenticador FIDO2 faz: assina
// SHA-256(application) || flags || counter || SHA-256(signed-data).
//
// A FORMA É A DOS VECTORES GOLDEN, e é o [TestAOS446AutenticadorFalsoReproduzOVectorGolden] que o
// prova: dados os mesmos parâmetros, este helper tem de produzir, byte a byte, o envelope que o
// `ssh-keygen -Y verify` aceitou. Sem essa amarra, o helper e o verificador podiam derivar juntos
// para um formato que não é o do OpenSSH e todos os testes continuariam verdes.
type autenticadorFalso struct {
	priv        ed25519.PrivateKey
	application string
	flags       byte
	counter     uint32
	hashAlg     string
}

func novoAutenticador(t *testing.T, seedHex, application string, flags byte, counter uint32, hashAlg string) *autenticadorFalso {
	t.Helper()
	seed, err := hex.DecodeString(seedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("seed invalida: %v", err)
	}
	return &autenticadorFalso{priv: ed25519.NewKeyFromSeed(seed), application: application, flags: flags, counter: counter, hashAlg: hashAlg}
}

func (a *autenticadorFalso) chave(t *testing.T) SKPublicKey {
	t.Helper()
	var blob []byte
	blob = sshString(blob, []byte(SSHSigAlgSKEd25519))
	blob = sshString(blob, a.priv.Public().(ed25519.PublicKey))
	blob = sshString(blob, []byte(a.application))
	k, err := parseSKPublicKeyBlob(blob)
	if err != nil {
		t.Fatalf("chave do autenticador: %v", err)
	}
	return k
}

func (a *autenticadorFalso) assinar(t *testing.T, namespace string, mensagem []byte) []byte {
	t.Helper()
	mh, err := hashMensagem(a.hashAlg, mensagem)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	var sd []byte
	sd = append(sd, sshsigMagic...)
	sd = sshString(sd, []byte(namespace))
	sd = sshString(sd, nil)
	sd = sshString(sd, []byte(a.hashAlg))
	sd = sshString(sd, mh)

	apphash := sha256.Sum256([]byte(a.application))
	sdhash := sha256.Sum256(sd)
	var inner []byte
	inner = append(inner, apphash[:]...)
	inner = append(inner, a.flags)
	inner = binary.BigEndian.AppendUint32(inner, a.counter)
	inner = append(inner, sdhash[:]...)

	var sig []byte
	sig = sshString(sig, []byte(SSHSigAlgSKEd25519))
	sig = sshString(sig, ed25519.Sign(a.priv, inner))
	sig = append(sig, a.flags)
	sig = binary.BigEndian.AppendUint32(sig, a.counter)

	var env []byte
	env = append(env, sshsigMagic...)
	env = binary.BigEndian.AppendUint32(env, sshsigVersion)
	env = sshString(env, a.chave(t).Blob())
	env = sshString(env, []byte(namespace))
	env = sshString(env, nil)
	env = sshString(env, []byte(a.hashAlg))
	env = sshString(env, sig)
	return env
}

// TestAOS446AutenticadorFalsoReproduzOVectorGolden — a amarra entre o helper dos testes e os
// bytes que o OpenSSH validou.
func TestAOS446AutenticadorFalsoReproduzOVectorGolden(t *testing.T) {
	for _, nome := range []string{"sha512_up", "sha256_up_uv", "sem_toque"} {
		v := carregarVectores(t)[nome]
		t.Run(nome, func(t *testing.T) {
			a := novoAutenticador(t, v.SeedHex, v.Application, v.Flags, v.Counter, v.HashAlgorithm)
			got := base64.StdEncoding.EncodeToString(a.assinar(t, v.Namespace, v.mensagem(t)))
			if got != v.EnvelopeB64 {
				t.Fatalf("o helper NAO reproduz o envelope validado pelo ssh-keygen\n got=%s\nwant=%s", got, v.EnvelopeB64)
			}
		})
	}
}

// mandatoDeProva devolve um mandato v2 válido, sem assinatura.
func mandatoDeProva() Mandate {
	return Mandate{
		ID: "aaaaaaaaaaaaaaaaaaaaaa", Human: "alice", Board: "eu-west", AgentID: "agent:planner",
		AgentClass: "planner", PolicyRef: "policy://planner", Scope: []string{"cap:doc.read"},
		Issuer: "iss:aos-issuer-auto", MaxTTLSeconds: 2700, NotBefore: 1790000000, NotAfter: 1790086400,
		Requesters: []string{"sub-jimy"},
	}
}

// TestAOS446MandatoFIDO2VerificaEDistingueSe — um mandato assinado por hardware verifica contra o
// pino de hardware, e o campo `fmt` diz o que é.
func TestAOS446MandatoFIDO2VerificaEDistingueSe(t *testing.T) {
	a := novoAutenticador(t, "4141414141414141414141414141414141414141414141414141414141414141", MandateSSHSIGApplication, 0x01, 3, "sha512")
	pino, err := SKMandateSigner(a.chave(t))
	if err != nil {
		t.Fatal(err)
	}
	m := mandatoDeProva()
	sm, err := AttachSSHSIG(m, a.assinar(t, MandateSSHSIGNamespace, m.SigningInput()), pino)
	if err != nil {
		t.Fatalf("AttachSSHSIG: %v", err)
	}
	if sm.Format != MandateFormatSSHSIG {
		t.Fatalf("fmt=%q, esperado %q", sm.Format, MandateFormatSSHSIG)
	}
	if err := sm.VerifySignature(pino); err != nil {
		t.Fatalf("o mandato FIDO2 nao verifica: %v", err)
	}
	if !pino.Hardware() || pino.Kind() != MandateFormatSSHSIG {
		t.Fatal("o pino tem de se declarar hardware")
	}
	// O DOCUMENTO NÃO SE MEXE DEPOIS DE ASSINADO: a mesma amarra do v2.
	mexido := sm
	mexido.Mandate.Requesters = append(append([]string(nil), m.Requesters...), "sub-intruso")
	if err := mexido.VerifySignature(pino); !errors.Is(err, ErrMandateInvalid) {
		t.Fatalf("um requester acrescentado tinha de invalidar, veio %v", err)
	}
	// NAMESPACE PRÓPRIA: uma assinatura que o humano produziu para `git` não é um mandato.
	outra := AttachHelperErro(t, m, a.assinar(t, "git", m.SigningInput()), pino)
	if !errors.Is(outra, ErrMandateInvalid) || !strings.Contains(outra.Error(), "namespace") {
		t.Fatalf("uma assinatura de outra namespace tinha de ser recusada, veio %v", outra)
	}
}

// AttachHelperErro corre o [AttachSSHSIG] e devolve só o erro.
func AttachHelperErro(t *testing.T, m Mandate, env []byte, pino MandateSigner) error {
	t.Helper()
	_, err := AttachSSHSIG(m, env, pino)
	return err
}

// TestAOS446TabelaPinoVersusFmt — as quatro combinações de (pino, fmt), uma a uma. É a tabela do
// [MandateSigner.Verify], e é o que torna o campo `fmt` inofensivo.
func TestAOS446TabelaPinoVersusFmt(t *testing.T) {
	humano := ed25519.NewKeyFromSeed(make([]byte, 32))
	pinoSW := pinoDeSoftware(t, humano.Public().(ed25519.PublicKey))
	a := novoAutenticador(t, "4141414141414141414141414141414141414141414141414141414141414141", MandateSSHSIGApplication, 0x01, 3, "sha512")
	pinoHW, err := SKMandateSigner(a.chave(t))
	if err != nil {
		t.Fatal(err)
	}
	m := mandatoDeProva()
	assinadoSW, err := SignMandate(humano, m)
	if err != nil {
		t.Fatal(err)
	}
	assinadoHW, err := AttachSSHSIG(m, a.assinar(t, MandateSSHSIGNamespace, m.SigningInput()), pinoHW)
	if err != nil {
		t.Fatal(err)
	}

	casos := []struct {
		nome   string
		sm     SignedMandate
		pino   MandateSigner
		aceita bool
		contem string
	}{
		{"software + fmt ausente", assinadoSW, pinoSW, true, ""},
		{"software + fmt=ed25519 explicito", comFormato(assinadoSW, MandateFormatEd25519), pinoSW, true, ""},
		{"software + fmt=sshsig", comFormato(assinadoSW, MandateFormatSSHSIG), pinoSW, false, "chave de SOFTWARE"},
		{"hardware + fmt=sshsig", assinadoHW, pinoHW, true, ""},
		{"hardware + fmt ausente", comFormato(assinadoHW, ""), pinoHW, false, "so aceita"},
		{"hardware + fmt=ed25519", comFormato(assinadoHW, MandateFormatEd25519), pinoHW, false, "so aceita"},
		{"assinatura de software contra pino de hardware", assinadoSW, pinoHW, false, "so aceita"},
		{"envelope de hardware contra pino de software", assinadoHW, pinoSW, false, "chave de SOFTWARE"},
		{"fmt desconhecido", comFormato(assinadoSW, "pgp"), pinoSW, false, "fmt=\"pgp\""},
		// A5: o `fmt` é uma enumeração fechada escrita por uma ferramenta, não texto de um
		// humano. Aparava-se, e `" sshsig "` passava.
		{"fmt com espacos a volta", comFormato(assinadoHW, " sshsig "), pinoHW, false, "nao e um formato conhecido"},
		{"fmt ed25519 com espaco", comFormato(assinadoSW, "ed25519 "), pinoSW, false, "nao e um formato conhecido"},
		{"fmt em maiusculas", comFormato(assinadoSW, "ED25519"), pinoSW, false, "nao e um formato conhecido"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			err := c.sm.VerifySignature(c.pino)
			if c.aceita {
				if err != nil {
					t.Fatalf("tinha de aceitar: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("ACEITE — tinha de recusar")
			}
			if !errors.Is(err, ErrMandateInvalid) {
				t.Fatalf("recusado fora de ErrMandateInvalid: %v", err)
			}
			if c.contem != "" && !strings.Contains(err.Error(), c.contem) {
				t.Fatalf("recusado pela razao errada: %v", err)
			}
		})
	}
}

func comFormato(sm SignedMandate, f string) SignedMandate {
	sm.Format = f
	return sm
}

// TestAOS446FmtNaoMudaOsBytesAssinados — a reconciliação com o v1 (que corre em produção) e com o
// v2 (que acabou de entrar). O campo `fmt` NÃO entra no [Mandate.SigningInput], pelo que os bytes
// que qualquer um deles assinou são exactamente os de antes deste ticket.
func TestAOS446FmtNaoMudaOsBytesAssinados(t *testing.T) {
	m := mandatoDeProva()
	v2 := string(m.SigningInput())
	if !strings.Contains(v2, mandateDomainV2) {
		t.Fatalf("o mandato de prova tinha de ser v2: %q", v2[:60])
	}
	v1doc := m
	v1doc.Requesters = nil
	v1 := string(v1doc.SigningInput())
	if !strings.Contains(v1, mandateDomain) || strings.Contains(v1, mandateDomainV2) {
		t.Fatal("sem requesters, o SigningInput tem de ser o do dominio v1")
	}
	// O `fmt` vive no ENVELOPE. Mudá-lo não pode tocar nos bytes.
	humano := ed25519.NewKeyFromSeed(make([]byte, 32))
	sm, err := SignMandate(humano, m)
	if err != nil {
		t.Fatal(err)
	}
	if sm.Format != "" {
		t.Fatalf("SignMandate nao pode escrever fmt (um binario anterior recusa-o com DisallowUnknownFields): %q", sm.Format)
	}
	if string(sm.Mandate.SigningInput()) != v2 || string(comFormato(sm, MandateFormatSSHSIG).Mandate.SigningInput()) != v2 {
		t.Fatal("mudar o fmt mudou os bytes assinados")
	}
	// E um mandato SEM `fmt` serializa hoje EXACTAMENTE como serializava antes do ticket.
	corpo, err := json.Marshal(sm)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(corpo), "\"fmt\"") {
		t.Fatalf("um mandato de software nao pode ganhar o campo fmt no JSON: %s", corpo)
	}
}

// TestAOS446ParseMandateSignersUnificado — a gramática de AOS_MANDATE_SIGNERS num só parser, com
// as duas formas de pino, e as recusas que o nó já impunha (e o emissor não).
func TestAOS446ParseMandateSignersUnificado(t *testing.T) {
	hexA := strings.Repeat("aa", 32)
	hexB := strings.Repeat("bb", 32)
	sk := carregarVectores(t)["sha512_up"].ChaveLinha

	bons := map[string]string{
		"so software":     "alice=" + hexA,
		"so hardware":     "alice=" + sk,
		"os dois":         "alice=" + hexA + ",bob=" + sk,
		"espacos a volta": "  alice = " + hexA + " , bob = " + hexB + " ",
		// A7: a JANELA DE ROTAÇÃO. O mesmo humano com o pino de software e o de hardware.
		"dois pinos para o mesmo humano": "alice=" + hexA + ",alice=" + sk,
	}
	for nome, lista := range bons {
		t.Run(nome, func(t *testing.T) {
			m, err := ParseMandateSigners(lista)
			if err != nil {
				t.Fatalf("recusado: %v", err)
			}
			for humano, ps := range m {
				if len(ps) == 0 {
					t.Fatalf("humano %q sem pinos", humano)
				}
				for _, s := range ps {
					if !s.Valid() || s.Fingerprint() == "" {
						t.Fatalf("pino de %q mal formado", humano)
					}
				}
			}
		})
	}

	maus := map[string]string{
		"vazia":                  "",
		"sem igual":              "alice",
		"user vazio":             "=" + hexA,
		"prefixo human:":         "human:alice=" + hexA,
		"mesmo pino repetido":    "alice=" + hexA + ",alice=" + hexA,
		"tres pinos para um":     "alice=" + hexA + ",alice=" + hexB + ",alice=" + strings.Repeat("cc", 32),
		"mesma chave dois nomes": "alice=" + hexA + ",bob=" + hexA,
		"mesma chave sk":         "alice=" + sk + ",bob=" + sk,
		"hex curto":              "alice=" + strings.Repeat("aa", 31),
		"nao hex":                "alice=zz" + strings.Repeat("aa", 31),
		"sk de outro tipo":       "alice=sk-ecdsa-sha2-nistp256@openssh.com AAAA",
		"ssh-ed25519 software":   "alice=ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAA",
		"sk com base64 partido":  "alice=sk-ssh-ed25519@openssh.com nao-e-base64!!",
		// A5: a `application` tem de ser a deste uso. Uma chave gerada sem `-O application=`
		// serve para SSH interactivo — e o §6.4 do ADR-033 manda criar uma dessas na MESMA
		// máquina.
		"sk com outra application": "alice=" + carregarVectores(t)["outra_application"].ChaveLinha,
	}
	// A razão da recusa importa: um `.env` com o MESMO pino duas vezes e um com a mesma chave sob
	// DOIS nomes são erros diferentes — o primeiro é um engano de quem editou, o segundo destrói
	// a atribuição — e quem lê a mensagem tem de saber qual dos dois cometeu.
	razoes := map[string]string{
		"mesmo pino repetido":    "o MESMO pino duas vezes",
		"mesma chave dois nomes": "partilham a mesma chave",
		"tres pinos para um":     "mais de 2 pinos",
	}
	for nome, lista := range maus {
		t.Run(nome, func(t *testing.T) {
			err := erroDoParser(lista)
			if err == nil {
				t.Fatalf("%q foi ACEITE", lista)
			}
			if r, tem := razoes[nome]; tem && !strings.Contains(err.Error(), r) {
				t.Fatalf("recusado pela razao errada (esperava %q): %v", r, err)
			}
		})
	}
}

// TestAOS446ImpressaoDigitalNaoColide — as duas famílias de pino nunca produzem a mesma
// impressão, e a de hardware é a que o `ssh-keygen -lf` imprime.
func TestAOS446ImpressaoDigitalNaoColide(t *testing.T) {
	v := carregarVectores(t)["sha512_up"]
	hw, err := ParseMandateSigner(v.ChaveLinha)
	if err != nil {
		t.Fatal(err)
	}
	if hw.Fingerprint() != v.SSHFingerprint {
		t.Fatalf("impressao %q, esperada a do ssh-keygen %q", hw.Fingerprint(), v.SSHFingerprint)
	}
	sw, err := ParseMandateSigner(strings.Repeat("aa", 32))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sw.Fingerprint(), "ed25519:") || strings.HasPrefix(hw.Fingerprint(), "ed25519:") {
		t.Fatalf("os prefixos tem de separar as familias: %q / %q", sw.Fingerprint(), hw.Fingerprint())
	}
	// A MESMA chave ed25519 crua e a MESMA chave dentro de um blob sk NÃO podem ter a mesma
	// impressão: se tivessem, trocar um pino de software pelo de hardware (ou o contrário)
	// passaria despercebido ao registo das âncoras.
	mesma, err := RawMandateSigner(hw.sk.Key)
	if err != nil {
		t.Fatal(err)
	}
	if mesma.Fingerprint() == hw.Fingerprint() {
		t.Fatal("a mesma chave crua e dentro de um blob sk deram a mesma impressao")
	}
	var vazio MandateSigner
	if vazio.Fingerprint() != "" || vazio.Kind() != "" || vazio.Valid() {
		t.Fatal("um pino nao formado nao pode parecer valido")
	}
}

// TestAOS446DigestDasAncorasEDeterminista — o digest do conjunto de pinos não depende da ordem de
// iteração do mapa (senão o registo das âncoras acusava uma troca a cada arranque).
func TestAOS446DigestDasAncorasEDeterminista(t *testing.T) {
	a := pinoDeSoftware(t, ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey))
	seedB := make([]byte, 32)
	seedB[0] = 9
	b := pinoDeSoftware(t, ed25519.NewKeyFromSeed(seedB).Public().(ed25519.PublicKey))
	um1 := map[string][]MandateSigner{"alice": {a}, "bob": {b}}
	um := MandateSignersDigest(um1)
	for i := 0; i < 32; i++ {
		if MandateSignersDigest(map[string][]MandateSigner{"bob": {b}, "alice": {a}}) != um {
			t.Fatal("o digest depende da ordem de iteracao do mapa")
		}
	}
	if MandateSignersDigest(map[string][]MandateSigner{"alice": {a}}) == um {
		t.Fatal("tirar um assinante tinha de mudar o digest")
	}
	if MandateSignersDigest(map[string][]MandateSigner{"alice": {b}, "bob": {a}}) == um {
		t.Fatal("trocar as chaves entre os nomes tinha de mudar o digest")
	}
	// UM SEGUNDO PINO É UMA ÂNCORA A MAIS, e tem de mudar o digest — senão abrir uma janela de
	// rotação seria invisível ao registo das âncoras.
	if MandateSignersDigest(map[string][]MandateSigner{"alice": {a, b}, "bob": {b}}) == um {
		t.Fatal("acrescentar um segundo pino a um humano tinha de mudar o digest")
	}
	// Mas a ORDEM dos dois pinos do mesmo humano no `.env` não é autoridade nenhuma.
	if MandateSignersDigest(map[string][]MandateSigner{"alice": {a, b}}) != MandateSignersDigest(map[string][]MandateSigner{"alice": {b, a}}) {
		t.Fatal("reordenar os dois pinos de uma rotacao nao pode mudar o digest")
	}
	// RENOMEAR UM ASSINANTE, COM A MESMA CHAVE, TAMBÉM É UMA TROCA DE ÂNCORA — e esta linha
	// existe porque a varredura de mutações a encontrou em falta: tirar o NOME do digest passava
	// em todos os outros casos. O nome é autoridade: o `human` do mandato tem de bater com ele,
	// pelo que `alice=<K>` e `alicia=<K>` autorizam humanos DIFERENTES com a mesma chave.
	if MandateSignersDigest(map[string][]MandateSigner{"alicia": {a}, "bob": {b}}) == um {
		t.Fatal("renomear um assinante com a MESMA chave tinha de mudar o digest")
	}
	if MandateSignersDigest(nil) == "" {
		t.Fatal("o digest de um conjunto vazio tem de ser um valor, nao vazio")
	}
}

// TestAOS446JanelaDeRotacaoDeDoisPinos — o achado A7 / decisão do dono de 2026-09-27.
//
// Trocar o pino de um humano do software para o FIDO2 invalida, no MESMO instante, todos os
// mandatos dele: a verificação da assinatura corre antes de tudo o resto. A janela existe para
// que a rotação não pare a cunhagem — os dois pinos aceitam, e o selo diz QUAL verificou, que é
// como o operador confirma que já pode remover o antigo.
func TestAOS446JanelaDeRotacaoDeDoisPinos(t *testing.T) {
	ctx := context.Background()
	c := novoCenario(t)
	agora := t0.Add(time.Hour)

	// O MESMO humano, com a chave de software do cenário e uma FIDO2.
	pinoSW := pinoDeSoftware(t, c.humano.Public().(ed25519.PublicKey))
	a := novoAutenticador(t, "4141414141414141414141414141414141414141414141414141414141414141", MandateSSHSIGApplication, 0x01, 3, "sha512")
	pinoHW, err := SKMandateSigner(a.chave(t))
	if err != nil {
		t.Fatal(err)
	}
	mHW := c.mandato.Mandate
	mHW.ID = "m-fido2"
	smHW, err := AttachSSHSIG(mHW, a.assinar(t, MandateSSHSIGNamespace, mHW.SigningInput()), pinoHW)
	if err != nil {
		t.Fatal(err)
	}
	tokSW := c.cunharHonesto(t, &c.mandato)
	tokHW := c.cunharHonesto(t, &smHW)

	verificador := func(ate time.Time, pinos map[string][]MandateSigner) *Verifier {
		return NewVerifier(
			WithMandatedIssuer(issAuto, c.emissor.Public().(ed25519.PublicKey), pinos),
			WithMandateDualPinUntil(ate),
			WithVerifierClock(func() time.Time { return agora }),
			WithVerifierLeeway(0),
		)
	}
	dois := map[string][]MandateSigner{"alice": {pinoSW, pinoHW}}

	// (1) JANELA ABERTA: os dois verificam, e o Principal diz QUAL pino serviu.
	v := verificador(agora.Add(time.Hour), dois)
	for _, cc := range []struct {
		nome, tok string
		quer      MandateSigner
		kind      string
	}{
		{"mandato de software", tokSW.Compact, pinoSW, MandateFormatEd25519},
		{"mandato FIDO2", tokHW.Compact, pinoHW, MandateFormatSSHSIG},
	} {
		p, err := v.Verify(ctx, cc.tok)
		if err != nil {
			t.Fatalf("%s: tinha de verificar dentro da janela: %v", cc.nome, err)
		}
		if p.MandateSigner != cc.quer.Fingerprint() {
			t.Fatalf("%s: o selo tem de dizer QUAL pino verificou — %q, esperado %q", cc.nome, p.MandateSigner, cc.quer.Fingerprint())
		}
		if p.MandateSignerKind != cc.kind {
			t.Fatalf("%s: kind %q, esperado %q", cc.nome, p.MandateSignerKind, cc.kind)
		}
	}
	// As duas impressões são DIFERENTES — é isso que torna o selo útil ao operador.
	if pinoSW.Fingerprint() == pinoHW.Fingerprint() {
		t.Fatal("os dois pinos tinham de ter impressoes distintas")
	}

	// (2) JANELA FECHADA (ausente, ou já passada): dois pinos são recusados, e os DOIS mandatos.
	for _, ate := range []time.Time{{}, agora.Add(-time.Second)} {
		vv := verificador(ate, dois)
		for _, tok := range []string{tokSW.Compact, tokHW.Compact} {
			if _, err := vv.Verify(ctx, tok); !errors.Is(err, ErrMandateDualPinClosed) {
				t.Fatalf("fora da janela, dois pinos tinham de dar ErrMandateDualPinClosed, veio %v", err)
			}
		}
	}

	// (3) UM SÓ PINO: a janela é irrelevante, e a tabela pino×fmt continua a valer POR PINO.
	so := map[string][]MandateSigner{"alice": {pinoHW}}
	vv := verificador(time.Time{}, so)
	if _, err := vv.Verify(ctx, tokHW.Compact); err != nil {
		t.Fatalf("um so pino verifica com a janela fechada: %v", err)
	}
	if _, err := vv.Verify(ctx, tokSW.Compact); !errors.Is(err, ErrMandateInvalid) {
		t.Fatalf("com o pino so em hardware, o mandato de software tinha de ser recusado, veio %v", err)
	}
	// E o simétrico: só o pino de software recusa o mandato FIDO2.
	vv = verificador(time.Time{}, map[string][]MandateSigner{"alice": {pinoSW}})
	if _, err := vv.Verify(ctx, tokHW.Compact); !errors.Is(err, ErrMandateInvalid) {
		t.Fatalf("com o pino so em software, o mandato FIDO2 tinha de ser recusado, veio %v", err)
	}
}

// TestAOS446ArmorRecusaOQueNaoSejaUmSoBloco — o achado A5 sobre o [DecodeSSHSIGArmor].
func TestAOS446ArmorRecusaOQueNaoSejaUmSoBloco(t *testing.T) {
	v := carregarVectores(t)["sha512_up"]
	bom := EncodeSSHSIGArmor(v.envelope(t))
	outro := EncodeSSHSIGArmor(carregarVectores(t)["outra_application"].envelope(t))

	if _, err := DecodeSSHSIGArmor(bom); err != nil {
		t.Fatalf("o caminho bom tinha de passar: %v", err)
	}
	if _, err := DecodeSSHSIGArmor(bomUTF8Rune + "  \n" + bom + "\n "); err != nil {
		t.Fatalf("BOM e espaco a volta continuam tolerados: %v", err)
	}
	// O CASO QUE A VERSÃO ANTERIOR ACEITAVA EM SILÊNCIO, e é o que dá valor a este aperto: ela
	// fazia `Index(BEGIN)` e depois `Index(END)`, ficava com o PRIMEIRO bloco e ignorava o resto.
	// Um ficheiro com a assinatura do operador seguida de OUTRA lia-se como se estivesse limpo —
	// duas ferramentas a ler coisas diferentes do mesmo byte-stream. Estes três decodificavam
	// SEM ERRO na versão anterior (o base64 do primeiro bloco é válido), e é por isso que são o
	// controlo: os outros casos abaixo falhariam de qualquer maneira no decode.
	for nome, aceite := range map[string]string{
		"dois blocos, o segundo ignorado": bom + outro,
		"bloco bom seguido de lixo":       bom + "nota do operador\n",
		"dois blocos com texto no meio":   bom + "\n# rodado em 2026-10-01\n" + outro,
	} {
		t.Run(nome, func(t *testing.T) {
			if _, err := DecodeSSHSIGArmor(aceite); err == nil {
				t.Fatal("ACEITE — a versao anterior lia o primeiro bloco e ignorava o resto")
			}
		})
	}

	maus := map[string]string{
		"base64 cru sem armor": v.EnvelopeB64,
		"lixo depois do END":   bom + "aqui vai outra coisa\n",
		"lixo antes do BEGIN":  "nota do operador\n" + bom,
		"dois blocos":          bom + outro,
		"so o BEGIN":           "-----BEGIN SSH SIGNATURE-----\nAAAA\n",
		"so o END":             "AAAA\n-----END SSH SIGNATURE-----\n",
		"vazio":                "",
		"armor sem corpo":      "-----BEGIN SSH SIGNATURE-----\n-----END SSH SIGNATURE-----\n",
	}
	for nome, mau := range maus {
		t.Run(nome, func(t *testing.T) {
			if _, err := DecodeSSHSIGArmor(mau); err == nil {
				t.Fatalf("%q foi ACEITE", nome)
			}
		})
	}
}

// TestAOS446ApplicationDoPinoEExigida — o achado A5 sobre [ParseMandateSigner]. Uma chave FIDO2
// gerada sem `-O application=` serve para SSH interactivo, e o ADR-033 §6.4 manda criar uma
// dessas na MESMA máquina: pinada aqui, cada login passaria a produzir assinaturas sob a mesma
// application.
func TestAOS446ApplicationDoPinoEExigida(t *testing.T) {
	vs := carregarVectores(t)
	if _, err := ParseMandateSigner(vs["sha512_up"].ChaveLinha); err != nil {
		t.Fatalf("a application certa tinha de passar: %v", err)
	}
	outra := vs["outra_application"]
	if outra.Application == MandateSSHSIGApplication {
		t.Fatal("o vector `outra_application` tem de ter OUTRA application para este teste valer")
	}
	s, err := ParseMandateSigner(outra.ChaveLinha)
	if err == nil {
		t.Fatalf("uma chave com application %q foi ACEITE como pino de mandato (%s)", outra.Application, s.Fingerprint())
	}
	if !strings.Contains(err.Error(), MandateSSHSIGApplication) {
		t.Fatalf("a recusa tem de dizer qual a application exigida: %v", err)
	}
}

// erroDoParser corre o parser e devolve só o erro.
func erroDoParser(lista string) error {
	_, err := ParseMandateSigners(lista)
	return err
}

// TestAOS446MesmaChaveEmDuasFormasNaoSePinaEmDoisNomes — O ACHADO B3 DA 2.ª RONDA.
//
// A guarda de «a mesma chave sob dois nomes» indexava por [MandateSigner.Fingerprint], que é
// `ed25519:<hash>` num pino de software e `SHA256:<b64>` num FIDO2 — dois valores para a MESMA
// chave ed25519. Medido: `alice=<hex K>,bob=<sk-ssh com K>` era ACEITE, e quem detivesse K
// assinava mandatos em nome da alice E do bob, que é exactamente o que a guarda existe para
// recusar.
func TestAOS446MesmaChaveEmDuasFormasNaoSePinaEmDoisNomes(t *testing.T) {
	v := carregarVectores(t)["sha512_up"]
	skPino, err := ParseMandateSigner(v.ChaveLinha)
	if err != nil {
		t.Fatal(err)
	}
	// A MESMA chave ed25519, agora em hex — é a que está dentro do blob do vector.
	mesmaEmHex := hex.EncodeToString(skPino.chaveCrua())
	if len(skPino.chaveCrua()) != ed25519.PublicKeySize {
		t.Fatal("a chave crua do pino FIDO2 tinha de ser uma pubkey ed25519")
	}

	// (1) NOMES DIFERENTES com a mesma chave crua: ABORTA, nas duas ordens.
	for _, lista := range []string{
		"alice=" + mesmaEmHex + ",bob=" + v.ChaveLinha,
		"bob=" + v.ChaveLinha + ",alice=" + mesmaEmHex,
	} {
		err := erroDoParser(lista)
		if err == nil {
			t.Fatalf("ACEITE — a mesma chave em duas formas sob dois nomes: %q", lista)
		}
		if !strings.Contains(err.Error(), "MESMA chave ed25519 em formas diferentes") {
			t.Fatalf("recusado pela razao errada: %v", err)
		}
	}
	// As impressões SÃO diferentes — é por isso que a guarda da impressão sozinha não bastava.
	swPino, err := ParseMandateSigner(mesmaEmHex)
	if err != nil {
		t.Fatal(err)
	}
	if swPino.Fingerprint() == skPino.Fingerprint() {
		t.Fatal("este teste so tem valor se as duas impressoes forem DIFERENTES")
	}

	// (2) O MESMO nome nas duas formas continua a passar: o detentor da chave é o mesmo, e é a
	// forma que uma rotação pode tomar. Declarado: não é uma rotação de MATERIAL — é a mesma
	// chave a ser apresentada de duas maneiras —, mas não acrescenta ninguém à autoridade.
	if err := erroDoParser("alice=" + mesmaEmHex + ",alice=" + v.ChaveLinha); err != nil {
		t.Fatalf("o MESMO humano nas duas formas tinha de passar: %v", err)
	}

	// (3) Chaves DIFERENTES em nomes diferentes continuam a passar, obviamente.
	if err := erroDoParser("alice=" + strings.Repeat("aa", 32) + ",bob=" + v.ChaveLinha); err != nil {
		t.Fatalf("chaves diferentes em nomes diferentes tinham de passar: %v", err)
	}
}

// TestAOS446TectoDePinosNoVerificador — achado BAIXO 3: o tecto era imposto só no parser, e uma
// Config montada em código chegava ao verificador sem passar por ele. Medido: TRÊS pinos
// verificavam.
func TestAOS446TectoDePinosNoVerificador(t *testing.T) {
	c := novoCenario(t)
	pino := pinoDeSoftware(t, c.humano.Public().(ed25519.PublicKey))
	var extra []MandateSigner
	for i := 0; i < 4; i++ {
		seed := make([]byte, ed25519.SeedSize)
		seed[0] = byte(100 + i)
		extra = append(extra, pinoDeSoftware(t, ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)))
	}
	// O pino BOM em último: se o tecto não morder, o mandato verifica; se morder, é descartado.
	demais := append(append([]MandateSigner(nil), extra...), pino)
	v := NewVerifier(
		WithMandatedIssuer(issAuto, c.emissor.Public().(ed25519.PublicKey), map[string][]MandateSigner{"alice": demais}),
		WithMandateDualPinUntil(t0.Add(48*time.Hour)),
		WithVerifierClock(func() time.Time { return t0.Add(time.Hour) }),
		WithVerifierLeeway(0),
	)
	if n := len(v.mandateSigners["alice"]); n > MandatePinosMaximoPorHumano {
		t.Fatalf("o verificador aceitou %d pinos, acima do tecto de %d", n, MandatePinosMaximoPorHumano)
	}
	if _, err := v.Verify(context.Background(), c.cunharHonesto(t, &c.mandato).Compact); err == nil {
		t.Fatal("o pino bom estava acima do tecto e tinha de ter sido DESCARTADO")
	}
	// Controlo: dentro do tecto, o mesmo pino verifica.
	ok := NewVerifier(
		WithMandatedIssuer(issAuto, c.emissor.Public().(ed25519.PublicKey), map[string][]MandateSigner{"alice": {extra[0], pino}}),
		WithMandateDualPinUntil(t0.Add(48*time.Hour)),
		WithVerifierClock(func() time.Time { return t0.Add(time.Hour) }),
		WithVerifierLeeway(0),
	)
	if _, err := ok.Verify(context.Background(), c.cunharHonesto(t, &c.mandato).Compact); err != nil {
		t.Fatalf("dentro do tecto, o segundo pino tinha de verificar: %v", err)
	}
}
