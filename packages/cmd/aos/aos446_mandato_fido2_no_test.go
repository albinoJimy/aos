package main

// aos446_mandato_fido2_no_test.go — O CAMINHO INTEIRO, VISTO DO NÓ (AOS-446 fase 1):
// um mandato assinado por FIDO2 cunha um token, o nó aceita-o, e a decisão selada no WORM v5 diz
// SOB QUE CHAVE o mandato foi verificado.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
)

// ---------------------------------------------------------------------------------------------
// O AUTENTICADOR SIMULADO, E PORQUE EXISTE UMA SEGUNDA CÓPIA DELE
//
// A primeira vive em `platform/identity` (onde o verificador vive). Esta é do módulo do NÓ, que
// não depende do testkit — nenhum módulo de produção depende, por decisão do AOS-109 — e não
// importa código de teste de outro módulo (Go não o permite).
//
// A duplicação NÃO é livre: as duas cópias são amarradas aos MESMOS vectores golden, os que o
// `ssh-keygen -Y verify` do OpenSSH_10.3p1 aceitou. O
// [TestAOS446AutenticadorDoNoReproduzOVectorGolden] lê o ficheiro do outro módulo por caminho
// relativo — o mesmo padrão com que estes testes já abrem o bundle do PDP — e exige o envelope
// byte a byte. Se esta cópia derivar, avermelha aqui.
// ---------------------------------------------------------------------------------------------

const aos446VectoresGolden = "../../platform/identity/testdata/aos446_sshsig_sk_vectores.json"

type aos446Autenticador struct {
	priv        ed25519.PrivateKey
	application string
	flags       byte
	counter     uint32
	hashAlg     string
}

func aos446NovoAutenticador(t *testing.T, seedHex, application string, flags byte, counter uint32, hashAlg string) *aos446Autenticador {
	t.Helper()
	seed, err := hex.DecodeString(seedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("seed invalida: %v", err)
	}
	return &aos446Autenticador{priv: ed25519.NewKeyFromSeed(seed), application: application, flags: flags, counter: counter, hashAlg: hashAlg}
}

func aos446SSHString(b, s []byte) []byte {
	return append(binary.BigEndian.AppendUint32(b, uint32(len(s))), s...)
}

func (a *aos446Autenticador) blob() []byte {
	var b []byte
	b = aos446SSHString(b, []byte(identity.SSHSigAlgSKEd25519))
	b = aos446SSHString(b, a.priv.Public().(ed25519.PublicKey))
	return aos446SSHString(b, []byte(a.application))
}

func (a *aos446Autenticador) pino(t *testing.T) identity.MandateSigner {
	t.Helper()
	s, err := identity.ParseMandateSigner(identity.SSHSigAlgSKEd25519 + " " + base64.StdEncoding.EncodeToString(a.blob()))
	if err != nil {
		t.Fatalf("pino FIDO2: %v", err)
	}
	return s
}

func (a *aos446Autenticador) envelope(t *testing.T, namespace string, mensagem []byte) []byte {
	t.Helper()
	var mh []byte
	switch a.hashAlg {
	case "sha512":
		h := sha512.Sum512(mensagem)
		mh = h[:]
	case "sha256":
		h := sha256.Sum256(mensagem)
		mh = h[:]
	default:
		t.Fatalf("hash %q", a.hashAlg)
	}
	var sd []byte
	sd = append(sd, "SSHSIG"...)
	sd = aos446SSHString(sd, []byte(namespace))
	sd = aos446SSHString(sd, nil)
	sd = aos446SSHString(sd, []byte(a.hashAlg))
	sd = aos446SSHString(sd, mh)

	apphash := sha256.Sum256([]byte(a.application))
	sdhash := sha256.Sum256(sd)
	var inner []byte
	inner = append(inner, apphash[:]...)
	inner = append(inner, a.flags)
	inner = binary.BigEndian.AppendUint32(inner, a.counter)
	inner = append(inner, sdhash[:]...)

	var sig []byte
	sig = aos446SSHString(sig, []byte(identity.SSHSigAlgSKEd25519))
	sig = aos446SSHString(sig, ed25519.Sign(a.priv, inner))
	sig = append(sig, a.flags)
	sig = binary.BigEndian.AppendUint32(sig, a.counter)

	var env []byte
	env = append(env, "SSHSIG"...)
	env = binary.BigEndian.AppendUint32(env, 1)
	env = aos446SSHString(env, a.blob())
	env = aos446SSHString(env, []byte(namespace))
	env = aos446SSHString(env, nil)
	env = aos446SSHString(env, []byte(a.hashAlg))
	return aos446SSHString(env, sig)
}

// TestAOS446AutenticadorDoNoReproduzOVectorGolden — a amarra desta cópia aos bytes que o OpenSSH
// validou. Sem ela, a cópia do nó e a do `identity` podiam derivar juntas para um formato que não
// é o do OpenSSH, e todos os testes das duas continuariam verdes.
func TestAOS446AutenticadorDoNoReproduzOVectorGolden(t *testing.T) {
	raw, err := os.ReadFile(aos446VectoresGolden)
	if err != nil {
		t.Fatalf("ler os vectores golden: %v", err)
	}
	var vs []struct {
		Nome             string `json:"nome"`
		SeedHex          string `json:"seed_hex"`
		Application      string `json:"application"`
		Namespace        string `json:"namespace"`
		HashAlgorithm    string `json:"hash_algorithm"`
		Flags            byte   `json:"flags"`
		Counter          uint32 `json:"counter"`
		MensagemB64      string `json:"mensagem_b64"`
		EnvelopeB64      string `json:"envelope_b64"`
		OpenSSHVeredicto string `json:"openssh_veredicto"`
	}
	if err := json.Unmarshal(raw, &vs); err != nil {
		t.Fatalf("vectores ilegiveis: %v", err)
	}
	if len(vs) == 0 {
		t.Fatal("sem vectores")
	}
	for _, v := range vs {
		if !strings.Contains(v.OpenSSHVeredicto, "OpenSSH") {
			t.Fatalf("o vector %q nao regista o veredicto de uma implementacao externa", v.Nome)
		}
		msg, _ := base64.StdEncoding.DecodeString(v.MensagemB64)
		a := aos446NovoAutenticador(t, v.SeedHex, v.Application, v.Flags, v.Counter, v.HashAlgorithm)
		if got := base64.StdEncoding.EncodeToString(a.envelope(t, v.Namespace, msg)); got != v.EnvelopeB64 {
			t.Fatalf("o autenticador do no NAO reproduz o vector %q validado pelo ssh-keygen", v.Nome)
		}
	}
}

// tokenDoMandatoFIDO2 cunha o NHI do run sob um mandato assinado em SSHSIG pelo autenticador da
// fixture — o que o humano produz com `ssh-keygen -Y sign` e a chave de hardware dele.
func (f *aos439Fixture) tokenDoMandatoFIDO2(t *testing.T, requesters ...string) string {
	t.Helper()
	if f.sk == nil {
		t.Fatal("a fixture nao foi montada com autenticador FIDO2")
	}
	agora := tnClock()()
	m := identity.Mandate{
		ID: aos439Mandato, Human: tnHuman, Board: govBoard, AgentID: durAgent, AgentClass: durClass,
		PolicyRef: "policy://" + durClass, Scope: []string{durCap}, Issuer: issAutoDeTeste, MaxTTLSeconds: 2700,
		NotBefore: agora.Add(-time.Hour).Unix(), NotAfter: agora.Add(24 * time.Hour).Unix(),
		Requesters: requesters,
	}
	sm, err := identity.AttachSSHSIG(m, f.sk.envelope(t, identity.MandateSSHSIGNamespace, m.SigningInput()), f.sk.pino(t))
	if err != nil {
		t.Fatalf("juntar a assinatura FIDO2 ao mandato: %v", err)
	}
	iss, err := identity.NewIssuer(issAutoDeTeste, f.auto, map[string]identity.ClassPolicy{
		durClass: {TTL: 15 * time.Minute, Scope: []string{durCap}},
	}, identity.WithIssuerClock(tnClock()))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := iss.Issue(context.Background(), identity.IssueRequest{
		UserID: tnHuman, AgentID: durAgent, AgentClass: durClass, PolicyRef: "policy://" + durClass,
		Board: govBoard, UserAuthority: []string{durCap}, Mandate: &sm,
	})
	if err != nil {
		t.Fatalf("cunhar sob o mandato FIDO2: %v", err)
	}
	return tok.Compact
}

// TestAOS446MandatoFIDO2CorreNoNoEOSeloDizAChave — o critério inteiro, num run real.
func TestAOS446MandatoFIDO2CorreNoNoEOSeloDizAChave(t *testing.T) {
	ctx := context.Background()
	sk := aos446NovoAutenticador(t, "4141414141414141414141414141414141414141414141414141414141414141", "ssh:aos-mandate", 0x01, 11, "sha512")
	f := noAOS439Com(t, audit.SchemaV5, sk)
	const plano = "plano-446"
	const filho = plano + separadorDoRunFilho + "n1"
	ger := f.pedirEReclamar(t, plano, aos439Alice)

	tok := f.tokenDoMandatoFIDO2(t, aos439Alice)
	if r := f.submeterFilho(t, filho, tok, &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Drenador); r.code != http.StatusCreated {
		t.Fatalf("o run sob o mandato FIDO2 tinha de ser aceite (201), veio %d %s", r.code, r.body)
	}
	f.esperar(t, filho)

	dec := decisaoDaTool(t, f.node, filho)
	if dec.Decision != audit.DecisionAllow {
		t.Fatalf("a tool call tinha de ser permitida para o teste medir o selo: %+v", dec)
	}
	if dec.SchemaVersion != audit.SchemaV5 {
		t.Fatalf("a decisao tinha de ser selada em v5, veio %d", dec.SchemaVersion)
	}
	esperado := sk.pino(t).Fingerprint()
	if dec.Principal.MandateSigner != esperado {
		t.Fatalf("o selo tem de nomear a CHAVE que verificou o mandato (%q), veio %q", esperado, dec.Principal.MandateSigner)
	}
	if !strings.HasPrefix(dec.Principal.MandateSigner, "SHA256:") {
		t.Fatalf("a impressao de uma chave FIDO2 e a do `ssh-keygen -lf`: %q", dec.Principal.MandateSigner)
	}
	// O resto do selo continua a dizer o que a onda B1 pôs lá.
	if dec.Principal.MandateID != aos439Mandato || dec.Principal.RequestedBy != aos439Alice {
		t.Fatalf("o v5 nao pode perder o que o v4 selava: %+v", dec.Principal)
	}
	if err := audit.Verify(ctx, f.node.WORM, filho, 1, dec.AuditSeq); err != nil {
		t.Fatalf("a cadeia do run tem de verificar: %v", err)
	}
	// E O REGISTO DAS ÂNCORAS DO ARRANQUE DIZ A MESMA CHAVE. As duas metades do argumento têm de
	// bater: uma diz que chaves o PROCESSO tinha, a outra sob que chave CADA DECISÃO correu.
	anc, err := f.node.WORM.Read(ctx, TrustAnchorsPartition, 1, 10)
	if err != nil || len(anc) == 0 {
		t.Fatalf("o arranque tinha de selar as ancoras: %v (%d registos)", err, len(anc))
	}
	lido, _ := trustAnchorsFromParams(anc[0].Obligations[0].Params)
	if lido["mandate_signers"] == ancoraAusente || lido["mandate_signers"] == "" {
		t.Fatalf("o registo das ancoras tem de resumir os assinantes: %+v", lido)
	}
}

// TestAOS446UmaChaveDeSoftwareNaoPassaPorUmPinoDeHardware — a mutação da guarda, no nó: com o
// humano pinado em hardware, o mandato assinado com a seed de software dele é RECUSADO, e o run
// não corre. É o que dá valor ao pino FIDO2 — se o caminho de software continuasse a funcionar,
// trocar o pino não mudava nada.
func TestAOS446UmaChaveDeSoftwareNaoPassaPorUmPinoDeHardware(t *testing.T) {
	sk := aos446NovoAutenticador(t, "4141414141414141414141414141414141414141414141414141414141414141", "ssh:aos-mandate", 0x01, 12, "sha512")
	f := noAOS439Com(t, audit.SchemaV5, sk)
	const plano = "plano-446-sw"
	const filho = plano + separadorDoRunFilho + "n1"
	ger := f.pedirEReclamar(t, plano, aos439Alice)

	// A MESMA fixture, mas com o token cunhado sob um mandato assinado pela SEED de software.
	tok := f.tokenDoMandato(t, aos439Alice)
	r := f.submeterFilho(t, filho, tok, &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Drenador)
	if r.code == http.StatusCreated {
		t.Fatalf("um mandato assinado por software tinha de ser RECUSADO sob um pino FIDO2, veio 201")
	}
}

// TestAOS446SemPresencaDeUtilizadorONoRecusa — a divergência deliberada face ao OpenSSH, medida
// no nó e não só na biblioteca.
func TestAOS446SemPresencaDeUtilizadorONoRecusa(t *testing.T) {
	semToque := aos446NovoAutenticador(t, "4141414141414141414141414141414141414141414141414141414141414141", "ssh:aos-mandate", 0x00, 13, "sha512")
	agora := tnClock()()
	m := identity.Mandate{
		ID: aos439Mandato, Human: tnHuman, Board: govBoard, AgentID: durAgent, AgentClass: durClass,
		PolicyRef: "policy://" + durClass, Scope: []string{durCap}, Issuer: issAutoDeTeste, MaxTTLSeconds: 2700,
		NotBefore: agora.Add(-time.Hour).Unix(), NotAfter: agora.Add(24 * time.Hour).Unix(),
		Requesters: []string{aos439Alice},
	}
	_, err := identity.AttachSSHSIG(m, semToque.envelope(t, identity.MandateSSHSIGNamespace, m.SigningInput()), semToque.pino(t))
	if err == nil {
		t.Fatal("um mandato assinado sem presenca de utilizador tinha de ser recusado")
	}
	if !strings.Contains(err.Error(), "PRESENCA DE UTILIZADOR") {
		t.Fatalf("recusado pela razao errada: %v", err)
	}
}

// TestAOS446ResumeExigeAMesmaChave — A GUARDA DA RETOMA, medida no vector real: um REINÍCIO com o
// pino trocado.
//
// Não se consegue provar dentro de um só processo, e isso é a própria razão de ser da guarda: o
// mapa `AOS_MANDATE_SIGNERS` tem UMA chave por humano, pelo que um token verificado sob outra
// chave só existe depois de alguém reescrever o `.env` e reiniciar — que é exactamente o atacante
// do ADR-033 §6.1. O teste faz isso: corre um run, suspende-o, fecha o nó, e volta a abrir sobre
// O MESMO directório com o `tnHuman` pinado noutra chave, apresentando um mandato com o MESMO id
// (o id é escolhido por quem assina, e é por isso que o `mandate_id` sozinho não chega).
func TestAOS446ResumeExigeAMesmaChave(t *testing.T) {
	dir := t.TempDir()
	kek := audit.NewInMemoryKeyVault(nil) // a MESMA custodia nos dois arranques — ver noAOS439Em
	f := noAOS439Em(t, dir, audit.SchemaV5, nil, nil, kek)
	const plano = "plano-446-ret"
	const filho = plano + separadorDoRunFilho + "n1"
	ger := f.pedirEReclamar(t, plano, aos439Alice)
	if r := f.submeterFilho(t, filho, f.tokenDoMandato(t, aos439Alice), &vinculoAoPedido{RunID: plano, Geracao: ger}, aos439Drenador); r.code != http.StatusCreated {
		t.Fatalf("submit: %d %s", r.code, r.body)
	}
	f.esperar(t, filho)
	rec, ok, err := f.node.ResumeRecords.Get(context.Background(), filho)
	if err != nil || !ok || rec.Principal.MandateSigner == "" {
		t.Fatalf("o registo de retoma tem de guardar a impressao do pino: %q ok=%t %v", rec.Principal.MandateSigner, ok, err)
	}
	impressaoOriginal := rec.Principal.MandateSigner
	if err := f.node.Close(); err != nil {
		t.Fatalf("fechar o no: %v", err)
	}

	// ROOT TROCA O PINO E REINICIA. Outra chave de software para o MESMO humano.
	intruso := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{77}, ed25519.SeedSize))
	pinoIntruso, err := identity.RawMandateSigner(intruso.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	g := noAOS439Em(t, dir, audit.SchemaV5, nil, &pinoIntruso, kek)
	rec2, ok, err := g.node.ResumeRecords.Get(context.Background(), filho)
	if err != nil || !ok {
		t.Fatalf("o registo de retoma tem de sobreviver ao reinicio: ok=%t %v", ok, err)
	}
	if rec2.Principal.MandateSigner != impressaoOriginal {
		t.Fatalf("o registo mudou com o reinicio: %q -> %q", impressaoOriginal, rec2.Principal.MandateSigner)
	}
	g.svc.mu.Lock()
	g.svc.suspended[filho] = &runState{runID: filho, suspended: true, done: make(chan struct{})}
	g.svc.mu.Unlock()

	// O MESMO agente, o MESMO humano e o MESMO id de mandato — só a CHAVE é outra.
	tok := g.tokenDoMandatoDe(t, intruso, tnHuman, aos439Mandato, aos439Alice)
	err = g.svc.Resume(context.Background(), filho, tok)
	if !errors.Is(err, ErrResumePrincipalMismatch) {
		t.Fatalf("a retoma sob outra CHAVE tinha de dar ErrResumePrincipalMismatch, veio %v", err)
	}
	if !strings.Contains(err.Error(), impressaoOriginal) {
		t.Fatalf("a recusa tem de nomear a chave que autorizou o run: %v", err)
	}
}
