package main

// aos446_fase1_test.go — a cerimónia FIDO2 e a verificação das âncoras no selador (AOS-446 fase 1).

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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	audit "github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
)

// pinosDoEmissor embrulha pubkeys ed25519 em pinos, para os testes anteriores ao AOS-446
// continuarem a compor o verificador como compunham.
func pinosDoEmissor(t *testing.T, m map[string]ed25519.PublicKey) map[string][]identity.MandateSigner {
	t.Helper()
	out := make(map[string][]identity.MandateSigner, len(m))
	for k, v := range m {
		s, err := identity.RawMandateSigner(v)
		if err != nil {
			t.Fatalf("pino de %q: %v", k, err)
		}
		out[k] = []identity.MandateSigner{s}
	}
	return out
}

// ---------------------------------------------------------------------------------------------
// O AUTENTICADOR SIMULADO. Terceira cópia (identity, cmd/aos, aqui) e pela mesma razão: são três
// MÓDULOS Go, e o Go não partilha código de teste entre módulos. As três estão amarradas aos
// MESMOS vectores que o `ssh-keygen -Y verify` do OpenSSH_10.3p1 aceitou, e esta tem ainda uma
// amarra a mais: quando existe um `ssh-keygen` no PATH,
// [TestAOS446SSHKeygenRealAceitaOQueProduzimos] entrega-lhe o que o teste produziu e exige «Good».
// ---------------------------------------------------------------------------------------------

const aos446Vectores = "../../platform/identity/testdata/aos446_sshsig_sk_vectores.json"

type aos446Auth struct {
	priv        ed25519.PrivateKey
	application string
	flags       byte
	counter     uint32
	hashAlg     string
}

func aos446NovoAuth(t *testing.T, seedHex, app string, flags byte, counter uint32, hashAlg string) *aos446Auth {
	t.Helper()
	seed, err := hex.DecodeString(seedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("seed: %v", err)
	}
	return &aos446Auth{priv: ed25519.NewKeyFromSeed(seed), application: app, flags: flags, counter: counter, hashAlg: hashAlg}
}

func aos446Str(b, s []byte) []byte {
	return append(binary.BigEndian.AppendUint32(b, uint32(len(s))), s...)
}

func (a *aos446Auth) blob() []byte {
	var b []byte
	b = aos446Str(b, []byte(identity.SSHSigAlgSKEd25519))
	b = aos446Str(b, a.priv.Public().(ed25519.PublicKey))
	return aos446Str(b, []byte(a.application))
}

func (a *aos446Auth) linha() string {
	return identity.SSHSigAlgSKEd25519 + " " + base64.StdEncoding.EncodeToString(a.blob())
}

func (a *aos446Auth) envelope(t *testing.T, namespace string, mensagem []byte) []byte {
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
	sd = aos446Str(sd, []byte(namespace))
	sd = aos446Str(sd, nil)
	sd = aos446Str(sd, []byte(a.hashAlg))
	sd = aos446Str(sd, mh)

	apphash := sha256.Sum256([]byte(a.application))
	sdhash := sha256.Sum256(sd)
	var inner []byte
	inner = append(inner, apphash[:]...)
	inner = append(inner, a.flags)
	inner = binary.BigEndian.AppendUint32(inner, a.counter)
	inner = append(inner, sdhash[:]...)

	var sig []byte
	sig = aos446Str(sig, []byte(identity.SSHSigAlgSKEd25519))
	sig = aos446Str(sig, ed25519.Sign(a.priv, inner))
	sig = append(sig, a.flags)
	sig = binary.BigEndian.AppendUint32(sig, a.counter)

	var env []byte
	env = append(env, "SSHSIG"...)
	env = binary.BigEndian.AppendUint32(env, 1)
	env = aos446Str(env, a.blob())
	env = aos446Str(env, []byte(namespace))
	env = aos446Str(env, nil)
	env = aos446Str(env, []byte(a.hashAlg))
	return aos446Str(env, sig)
}

// TestAOS446AuthDoEmissorReproduzOVectorGolden — a amarra desta cópia.
func TestAOS446AuthDoEmissorReproduzOVectorGolden(t *testing.T) {
	raw, err := os.ReadFile(aos446Vectores)
	if err != nil {
		t.Fatalf("ler os vectores: %v", err)
	}
	var vs []struct {
		Nome, SeedHex, Application, Namespace, HashAlgorithm string
		MensagemB64, EnvelopeB64, OpenSSHVeredicto           string
		Flags                                                byte
		Counter                                              uint32
	}
	// Os nomes JSON são minúsculas com underscore; decodifica-se por um mapa intermédio para o
	// teste não repetir as tags.
	var cru []map[string]any
	if err := json.Unmarshal(raw, &cru); err != nil {
		t.Fatal(err)
	}
	for _, m := range cru {
		vs = append(vs, struct {
			Nome, SeedHex, Application, Namespace, HashAlgorithm string
			MensagemB64, EnvelopeB64, OpenSSHVeredicto           string
			Flags                                                byte
			Counter                                              uint32
		}{
			Nome: m["nome"].(string), SeedHex: m["seed_hex"].(string), Application: m["application"].(string),
			Namespace: m["namespace"].(string), HashAlgorithm: m["hash_algorithm"].(string),
			MensagemB64: m["mensagem_b64"].(string), EnvelopeB64: m["envelope_b64"].(string),
			OpenSSHVeredicto: m["openssh_veredicto"].(string),
			Flags:            byte(m["flags"].(float64)), Counter: uint32(m["counter"].(float64)),
		})
	}
	if len(vs) == 0 {
		t.Fatal("sem vectores")
	}
	for _, v := range vs {
		if !strings.Contains(v.OpenSSHVeredicto, "OpenSSH") {
			t.Fatalf("o vector %q nao regista veredicto externo", v.Nome)
		}
		msg, _ := base64.StdEncoding.DecodeString(v.MensagemB64)
		a := aos446NovoAuth(t, v.SeedHex, v.Application, v.Flags, v.Counter, v.HashAlgorithm)
		if got := base64.StdEncoding.EncodeToString(a.envelope(t, v.Namespace, msg)); got != v.EnvelopeB64 {
			t.Fatalf("o autenticador do emissor NAO reproduz o vector %q", v.Nome)
		}
	}
}

// TestAOS446SSHKeygenRealAceitaOQueProduzimos — quando há `ssh-keygen` no PATH, a prova deixa de
// ser um ficheiro congelado e passa a ser a ferramenta real, AQUI, sobre bytes gerados AGORA.
//
// SALTA-SE sem `ssh-keygen` (o CI do repositório é offline e pode não o ter), e é por isso que os
// vectores congelados existem: eles carregam o veredicto de uma corrida em que a ferramenta
// ESTAVA presente. Este teste é o reforço, não a única prova.
func TestAOS446SSHKeygenRealAceitaOQueProduzimos(t *testing.T) {
	bin, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("sem ssh-keygen no PATH — a prova externa fica nos vectores golden")
	}
	dir := t.TempDir()
	a := aos446NovoAuth(t, strings.Repeat("7", 64), identity.MandateSSHSIGApplication, 0x01, 42, "sha512")
	msg := []byte("bytes gerados por TestAOS446SSHKeygenRealAceitaOQueProduzimos\n")
	env := a.envelope(t, identity.MandateSSHSIGNamespace, msg)

	msgPath := filepath.Join(dir, "m.txt")
	sigPath := filepath.Join(dir, "m.txt.sig")
	allowed := filepath.Join(dir, "allowed_signers")
	if err := os.WriteFile(msgPath, msg, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sigPath, []byte(identity.EncodeSSHSIGArmor(env)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(allowed, []byte("humano@aos "+a.linha()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(msgPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command(bin, "-Y", "verify", "-f", allowed, "-I", "humano@aos",
		"-n", identity.MandateSSHSIGNamespace, "-s", sigPath)
	cmd.Stdin = f
	saida, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("o ssh-keygen REAL recusou o que produzimos: %v\n%s", err, saida)
	}
	if !strings.Contains(string(saida), "Good") {
		t.Fatalf("veredicto inesperado do ssh-keygen: %s", saida)
	}
	// E O NOSSO VERIFICADOR ACEITA OS MESMOS BYTES.
	pino, err := identity.ParseMandateSigner(a.linha())
	if err != nil {
		t.Fatal(err)
	}
	if err := pino.Verify(identity.MandateFormatSSHSIG, env, msg); err != nil {
		t.Fatalf("o ssh-keygen aceitou e nos recusamos: %v", err)
	}
}

// TestAOS446CerimoniaFIDO2DeCaboARabo — `mandate-prepare` → assinatura → `mandate-attach` →
// `mint-mandated` → o nó aceita o token. É o caminho que o dono vai correr com a chave física.
func TestAOS446CerimoniaFIDO2DeCaboARabo(t *testing.T) {
	dir := t.TempDir()
	prefixo := filepath.Join(dir, "mandato")
	var out, diag bytes.Buffer
	err := run([]string{"mandate-prepare", "--human", "alice", "--board", "board-eu",
		"--agent", "agent:aos-orq", "--class", "orq", "--caps", "cap:doc.read",
		"--requesters", "sub-bob,sub-carla", "--out", prefixo}, &out, &diag)
	if err != nil {
		t.Fatalf("mandate-prepare: %v", err)
	}
	if !strings.Contains(diag.String(), "ssh-keygen -Y sign") || !strings.Contains(diag.String(), identity.MandateSSHSIGNamespace) {
		t.Fatalf("o prepare tem de imprimir o comando exacto do passo 2: %s", diag.String())
	}
	bytesPath := prefixo + sufixoDosBytes
	docPath := prefixo + sufixoDoDocumento
	assinar, err := os.ReadFile(bytesPath)
	if err != nil {
		t.Fatalf("o prepare tinha de escrever os bytes a assinar: %v", err)
	}
	// O DOCUMENTO E OS BYTES TÊM DE CONCORDAR — senão o humano assina uma coisa e entrega outra.
	docRaw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	var m identity.Mandate
	if err := json.Unmarshal(docRaw, &m); err != nil {
		t.Fatal(err)
	}
	if string(m.SigningInput()) != string(assinar) {
		t.Fatal("os bytes a assinar nao sao os do documento escrito")
	}

	// O humano toca na chave.
	a := aos446NovoAuth(t, strings.Repeat("3", 64), identity.MandateSSHSIGApplication, 0x01, 5, "sha512")
	sigPath := bytesPath + ".sig"
	if err := os.WriteFile(sigPath, []byte(identity.EncodeSSHSIGArmor(a.envelope(t, identity.MandateSSHSIGNamespace, assinar))), 0o600); err != nil {
		t.Fatal(err)
	}

	assinado := filepath.Join(dir, "mandato.json")
	out.Reset()
	diag.Reset()
	if err := run([]string{"mandate-attach", "--mandate", docPath, "--sig", sigPath,
		"--signer", a.linha(), "--out", assinado}, &out, &diag); err != nil {
		t.Fatalf("mandate-attach: %v", err)
	}
	if !strings.Contains(diag.String(), "HARDWARE") || !strings.Contains(diag.String(), "AOS_MANDATE_SIGNERS") {
		t.Fatalf("o attach tem de dizer o que pinar no no: %s", diag.String())
	}
	var sm identity.SignedMandate
	corpo, err := os.ReadFile(assinado)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(corpo, &sm); err != nil {
		t.Fatal(err)
	}
	if sm.Format != identity.MandateFormatSSHSIG {
		t.Fatalf("o mandato tinha de sair com fmt=%q, veio %q", identity.MandateFormatSSHSIG, sm.Format)
	}

	// E CUNHA-SE SOB ELE, com o pino no formato do `.env` do nó.
	emissor := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{5}, 32))
	chaveEmissor := filepath.Join(dir, "issuer-auto.key")
	if err := os.WriteFile(chaveEmissor, []byte(hex.EncodeToString(emissor.Seed())), 0o600); err != nil {
		t.Fatal(err)
	}
	destino := filepath.Join(dir, "nhi.jwt")
	out.Reset()
	if err := run([]string{"mint-mandated", "--mandate", assinado, "--key-file", chaveEmissor,
		"--signers", "alice=" + a.linha(), "--out", destino}, &out, &diag); err != nil {
		t.Fatalf("mint-mandated sob um mandato FIDO2: %v", err)
	}
	tok, err := os.ReadFile(destino)
	if err != nil {
		t.Fatal(err)
	}
	pino, err := identity.ParseMandateSigner(a.linha())
	if err != nil {
		t.Fatal(err)
	}
	v := identity.NewVerifier(identity.WithMandatedIssuer("iss:aos-issuer-auto",
		emissor.Public().(ed25519.PublicKey), map[string][]identity.MandateSigner{"alice": {pino}}))
	p, err := v.Verify(context.Background(), strings.TrimSpace(string(tok)))
	if err != nil {
		t.Fatalf("o no tem de aceitar o token cunhado sob o mandato FIDO2: %v", err)
	}
	if p.MandateSigner != pino.Fingerprint() || p.MandateSignerKind != identity.MandateFormatSSHSIG {
		t.Fatalf("o Principal tem de trazer a impressao do pino de hardware: %q / %q", p.MandateSigner, p.MandateSignerKind)
	}
}

// TestAOS446AttachRecusaOQueNaoDeve — as mutações da guarda do `mandate-attach`.
func TestAOS446AttachRecusaOQueNaoDeve(t *testing.T) {
	dir := t.TempDir()
	prefixo := filepath.Join(dir, "m")
	var out, diag bytes.Buffer
	if err := run([]string{"mandate-prepare", "--human", "alice", "--board", "board-eu",
		"--agent", "agent:aos-orq", "--class", "orq", "--caps", "cap:doc.read",
		"--requesters", "sub-bob", "--out", prefixo}, &out, &diag); err != nil {
		t.Fatal(err)
	}
	docPath, bytesPath := prefixo+sufixoDoDocumento, prefixo+sufixoDosBytes
	assinar, _ := os.ReadFile(bytesPath)
	bom := aos446NovoAuth(t, strings.Repeat("3", 64), identity.MandateSSHSIGApplication, 0x01, 5, "sha512")
	outro := aos446NovoAuth(t, strings.Repeat("4", 64), identity.MandateSSHSIGApplication, 0x01, 5, "sha512")
	semToque := aos446NovoAuth(t, strings.Repeat("3", 64), identity.MandateSSHSIGApplication, 0x00, 5, "sha512")
	outraApp := aos446NovoAuth(t, strings.Repeat("3", 64), "ssh:outra-application-qualquer", 0x01, 5, "sha512")

	escrever := func(nome string, env []byte) string {
		p := filepath.Join(dir, nome)
		if err := os.WriteFile(p, []byte(identity.EncodeSSHSIGArmor(env)), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	casos := []struct{ nome, sig, signer string }{
		{"assinatura de outra chave", escrever("outra.sig", outro.envelope(t, identity.MandateSSHSIGNamespace, assinar)), bom.linha()},
		{"assinatura de outra namespace", escrever("ns.sig", bom.envelope(t, "git", assinar)), bom.linha()},
		{"assinatura de outra mensagem", escrever("msg.sig", bom.envelope(t, identity.MandateSSHSIGNamespace, []byte("outra coisa"))), bom.linha()},
		{"sem presenca de utilizador", escrever("toque.sig", semToque.envelope(t, identity.MandateSSHSIGNamespace, assinar)), semToque.linha()},
		{"application diferente do pino", escrever("app.sig", outraApp.envelope(t, identity.MandateSSHSIGNamespace, assinar)), bom.linha()},
		{"pino de software", escrever("sw.sig", bom.envelope(t, identity.MandateSSHSIGNamespace, assinar)), strings.Repeat("aa", 32)},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			var o, d bytes.Buffer
			err := run([]string{"mandate-attach", "--mandate", docPath, "--sig", c.sig, "--signer", c.signer}, &o, &d)
			if err == nil {
				t.Fatalf("ACEITE — tinha de recusar (saida: %s)", o.String())
			}
		})
	}
	// O CAMINHO BOM continua a funcionar — senão o teste acima passava com um attach que recusa tudo.
	var o, d bytes.Buffer
	if err := run([]string{"mandate-attach", "--mandate", docPath,
		"--sig", escrever("bom.sig", bom.envelope(t, identity.MandateSSHSIGNamespace, assinar)),
		"--signer", bom.linha()}, &o, &d); err != nil {
		t.Fatalf("o caminho bom tinha de passar: %v", err)
	}
}

// ---------------------------------------------------------------------------------------------
// A VERIFICAÇÃO DAS ÂNCORAS NO SELADOR
// ---------------------------------------------------------------------------------------------

// wormComAncoras escreve um WORM com um registo de âncoras por retrato dado.
func wormComAncoras(t *testing.T, retratos ...audit.TrustAnchors) string {
	t.Helper()
	caminho := filepath.Join(t.TempDir(), "worm.wal")
	store, err := audit.OpenFileStore(caminho)
	if err != nil {
		t.Fatal(err)
	}
	for i, r := range retratos {
		tipo := audit.TrustAnchorsChangedEventType
		if i > 0 && r.Digest() == retratos[i-1].Digest() {
			tipo = audit.TrustAnchorsActiveEventType
		}
		if _, err := store.Append(context.Background(), audit.AuditRecord{
			Partition: audit.TrustAnchorsPartition, Timestamp: time.Unix(int64(i+1), 0).UTC(),
			Decision: audit.DecisionAllow, Principal: audit.Principal{NHIID: "config:node"},
			Resource:    audit.Resource{Type: tipo, Value: r.Digest()},
			Obligations: []audit.Obligation{{Type: tipo, Params: r.Params("config:node")}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return caminho
}

func selarPara(t *testing.T, worm, chave, anterior, aceitar string) (string, error) {
	t.Helper()
	args := []string{"worm-seal", "--worm", worm, "--key-file", chave}
	if anterior != "" {
		args = append(args, "--anterior", anterior)
	}
	if aceitar != "" {
		args = append(args, "--aceitar-ancoras", aceitar)
	}
	var out bytes.Buffer
	err := runWormSeal(args[1:], &out)
	return out.String(), err
}

// TestAOS446SeladorDenunciaAncoraTrocada — o critério: o selo diário, FORA do host, apanha um
// `AOS_MANDATE_SIGNERS` trocado por quem tem root no host.
func TestAOS446SeladorDenunciaAncoraTrocada(t *testing.T) {
	dir := t.TempDir()
	chave := filepath.Join(dir, "sel.key")
	seed := bytes.Repeat([]byte{7}, 32)
	if err := os.WriteFile(chave, []byte(hex.EncodeToString(seed)), 0o600); err != nil {
		t.Fatal(err)
	}
	antes := audit.TrustAnchors{"mandate_signers": "ed25519:aaaa", "issuer_pubkey": "ed25519:bbbb"}
	depois := audit.TrustAnchors{"mandate_signers": "ed25519:TROCADA", "issuer_pubkey": "ed25519:bbbb"}

	// (1) SELAGEM DE BASE: um só retrato, nada com que comparar.
	worm := wormComAncoras(t, antes)
	saida, err := selarPara(t, worm, chave, "", "")
	if err != nil {
		t.Fatalf("a primeira selagem tinha de passar: %v", err)
	}
	cpPath := filepath.Join(dir, "checkpoints.json")
	if err := os.WriteFile(cpPath, []byte(saida), 0o600); err != nil {
		t.Fatal(err)
	}

	// (2) O NÓ REINICIA SEM TROCA: sela, e a verificação diz «inalteradas».
	worm2 := wormComAncoras(t, antes, antes)
	if _, err := selarPara(t, worm2, chave, cpPath, ""); err != nil {
		t.Fatalf("um reinicio sem troca tinha de selar: %v", err)
	}

	// (3) ROOT TROCA A CHAVE E REINICIA: a selagem RECUSA, e nomeia a âncora.
	worm3 := wormComAncoras(t, antes, depois)
	_, err = selarPara(t, worm3, chave, cpPath, "")
	if !errors.Is(err, ErrWormSealAncorasTrocadas) {
		t.Fatalf("a troca tinha de recusar a selagem, veio %v", err)
	}
	if !strings.Contains(err.Error(), "mandate_signers") || !strings.Contains(err.Error(), "--aceitar-ancoras") {
		t.Fatalf("a recusa tem de nomear a ancora e dizer como aceitar: %v", err)
	}

	// (4) O OPERADOR DECLARA A ROTAÇÃO: com o digest EXACTO, sela.
	if _, err := selarPara(t, worm3, chave, cpPath, depois.Digest()); err != nil {
		t.Fatalf("com --aceitar-ancoras a selagem tinha de passar: %v", err)
	}
	// E um digest ERRADO não serve — senão o flag seria um `--sim`.
	if _, err := selarPara(t, worm3, chave, cpPath, antes.Digest()); !errors.Is(err, ErrWormSealAncorasTrocadas) {
		t.Fatalf("um digest errado tinha de recusar na mesma, veio %v", err)
	}
}

// TestAOS446SeladorSemBaseDeclaraQueNaoVerifica — honestidade: sem `--anterior`, ou sem a partição
// coberta pela selagem anterior, a verificação NÃO corre, e o comando di-lo em vez de dar a
// impressão de a ter corrido.
func TestAOS446SeladorSemBaseDeclaraQueNaoVerifica(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	antes := audit.TrustAnchors{"mandate_signers": "ed25519:aaaa"}
	worm := wormComAncoras(t, antes)
	store, err := audit.OpenFileStoreReadOnly(worm)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Sem nenhum checkpoint da partição das âncoras: avisa e deixa passar.
	var diag bytes.Buffer
	if err := exigirAncorasIguais(ctx, store, []audit.Checkpoint{{Partition: "outra", AuditSeq: 1}}, "", &diag); err != nil {
		t.Fatalf("sem base a verificacao nao pode RECUSAR: %v", err)
	}
	if !strings.Contains(diag.String(), "nao ha base") {
		t.Fatalf("tinha de declarar que nao ha base: %q", diag.String())
	}
	_ = dir
}

// TestAOS446PinoDoHumanoEUnificado — o leitor do emissor passou a ser o do nó: o que um recusa, o
// outro recusa. Era a divergência silenciosa entre `pubkeyDoHumano` e `parseMandateSigners`.
func TestAOS446PinoDoHumanoEUnificado(t *testing.T) {
	hexA, hexB := strings.Repeat("aa", 32), strings.Repeat("bb", 32)
	if _, err := pinosDoHumano("", "alice="+hexA, "alice"); err != nil {
		t.Fatalf("o caminho bom tinha de passar: %v", err)
	}
	if ps, err := pinosDoHumano(hexA, "", "alice"); err != nil || len(ps) != 1 || ps[0].Fingerprint() == "" {
		t.Fatalf("--signer-pubkey avulso tinha de passar: %v", err)
	}
	maus := map[string]string{
		// A LISTA INTEIRA é validada, e não só a entrada procurada: o `pubkeyDoHumano` antigo
		// devolvia a chave da alice sem olhar para o resto, e um `.env` que o NÓ recusa cunhava
		// aqui na mesma.
		"chave partilhada por dois nomes": "alice=" + hexA + ",bob=" + hexA,
		"chave de bob tambem em alice":    "alice=" + hexA + ",bob=" + hexB + ",bob=" + hexA,
		"entrada malformada":              "alice=" + hexA + ",lixo",
		"pubkey curta noutra entrada":     "alice=" + hexA + ",bob=aa",
	}
	for nome, lista := range maus {
		t.Run(nome, func(t *testing.T) {
			if _, err := pinosDoHumano("", lista, "alice"); err == nil {
				t.Fatalf("%q foi ACEITE", lista)
			}
		})
	}
	if _, err := pinosDoHumano("", "bob="+hexB, "alice"); err == nil {
		t.Fatal("um humano ausente da lista tinha de recusar")
	}
	// E o pino FIDO2 atravessa a lista (tem espacos, e a virgula separa).
	a := aos446NovoAuth(t, strings.Repeat("3", 64), identity.MandateSSHSIGApplication, 0x01, 1, "sha512")
	ps, err := pinosDoHumano("", "alice="+a.linha()+",bob="+hexB, "alice")
	if err != nil || len(ps) != 1 || !ps[0].Hardware() {
		t.Fatalf("um pino FIDO2 tinha de atravessar a lista: %v", err)
	}
	// A7: durante a janela de rotação o humano tem DOIS, e o emissor devolve os dois.
	dois, err := pinosDoHumano("", "alice="+hexA+",alice="+a.linha(), "alice")
	if err != nil || len(dois) != 2 {
		t.Fatalf("os dois pinos da rotacao tinham de chegar ao emissor: %v (%d)", err, len(dois))
	}
}

var _ = io.Discard

// TestAOS446SeladorVarreTodosOsRegistosDoIntervalo — O ACHADO A1 DA REVISÃO ADVERSARIAL
// (2026-09-27), reproduzido byte a byte.
//
// O denunciante comparava o ÚLTIMO registo ancorado com o ÚLTIMO registo do store, e concluía
// sobre o intervalo inteiro a partir de duas leituras. Derrotá-lo não exigia apagar nada: root
// troca o pino e reinicia (o nó sela HONESTAMENTE a troca), DEIXA a troca em vigor, e acrescenta
// um registo com os parâmetros ANTIGOS. A sequência é [antes, DEPOIS, antes]: a selagem lia o
// primeiro e o terceiro, dizia «INALTERADAS» e selava.
//
// Sem a varredura este teste fica VERDE no caminho errado — é o controlo negativo da correcção.
func TestAOS446SeladorVarreTodosOsRegistosDoIntervalo(t *testing.T) {
	dir := t.TempDir()
	chave := filepath.Join(dir, "sel.key")
	if err := os.WriteFile(chave, []byte(hex.EncodeToString(bytes.Repeat([]byte{7}, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
	antes := audit.TrustAnchors{"mandate_signers": "ed25519:aaaa", "issuer_pubkey": "ed25519:bbbb"}
	depois := audit.TrustAnchors{"mandate_signers": "ed25519:DO-ATACANTE", "issuer_pubkey": "ed25519:bbbb"}

	// (1) SELAGEM DE BASE sobre o retrato honesto.
	base := wormComAncoras(t, antes)
	saida, err := selarPara(t, base, chave, "", "")
	if err != nil {
		t.Fatalf("a selagem de base tinha de passar: %v", err)
	}
	cpPath := filepath.Join(dir, "checkpoints.json")
	if err := os.WriteFile(cpPath, []byte(saida), 0o600); err != nil {
		t.Fatal(err)
	}

	// (2) O ATAQUE: troca honestamente selada no meio, e um registo do atacante a repor o
	// retrato antigo no fim. Nada foi apagado — o `audit_seq` continua gapless.
	atacado := wormComAncoras(t, antes, depois, antes)
	_, err = selarPara(t, atacado, chave, cpPath, "")
	if !errors.Is(err, ErrWormSealAncorasTrocadas) {
		t.Fatalf("o registo do MEIO tinha de denunciar a troca (A1), veio %v", err)
	}
	if !strings.Contains(err.Error(), "seq 2") || !strings.Contains(err.Error(), "mandate_signers") {
		t.Fatalf("a recusa tem de nomear o AuditSeq do registo divergente e a ancora: %v", err)
	}

	// (3) E O CONTROLO POSITIVO: a MESMA forma, mas sem troca nenhuma no meio, continua a selar.
	limpo := wormComAncoras(t, antes, antes, antes)
	if _, err := selarPara(t, limpo, chave, cpPath, ""); err != nil {
		t.Fatalf("tres registos iguais tinham de selar: %v", err)
	}

	// (4) O OPERADOR DECLARA a rotação que fez a meio, e a selagem passa.
	if _, err := selarPara(t, atacado, chave, cpPath, depois.Digest()); err != nil {
		t.Fatalf("com o digest do retrato do meio declarado, a selagem tinha de passar: %v", err)
	}
	// Mas declarar o digest ERRADO não serve.
	if _, err := selarPara(t, atacado, chave, cpPath, antes.Digest()); !errors.Is(err, ErrWormSealAncorasTrocadas) {
		t.Fatalf("declarar o digest ja ancorado nao pode aceitar a troca do meio, veio %v", err)
	}
}

// TestAOS446SeladorContaOsRegistosQueVarreu — o achado A2: a linha do diagnóstico afirmava uma
// coisa sobre o intervalo inteiro a partir de duas leituras. Passa a nomear quantos varreu.
func TestAOS446SeladorContaOsRegistosQueVarreu(t *testing.T) {
	ctx := context.Background()
	antes := audit.TrustAnchors{"mandate_signers": "ed25519:aaaa"}
	worm := wormComAncoras(t, antes, antes, antes)
	store, err := audit.OpenFileStoreReadOnly(worm)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var diag bytes.Buffer
	if err := exigirAncorasIguais(ctx, store, []audit.Checkpoint{{Partition: audit.TrustAnchorsPartition, AuditSeq: 1}}, "", &diag); err != nil {
		t.Fatalf("tres registos iguais tinham de passar: %v", err)
	}
	if !strings.Contains(diag.String(), "TODOS os 2 registo(s)") {
		t.Fatalf("a linha tem de dizer QUANTOS registos foram varridos: %q", diag.String())
	}
	// E sem registos novos desde a selagem anterior, di-lo em vez de afirmar «inalteradas».
	diag.Reset()
	if err := exigirAncorasIguais(ctx, store, []audit.Checkpoint{{Partition: audit.TrustAnchorsPartition, AuditSeq: 3}}, "", &diag); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diag.String(), "nenhum registo de ancoras novo") {
		t.Fatalf("sem registos novos a linha nao pode afirmar uma varredura: %q", diag.String())
	}
}

// TestAOS446EmissorCunhaComQualquerUmDosDoisPinos — o achado A7 do lado do emissor: se ele
// escolhesse um pino, recusaria com «assinatura inválida» o mandato que o nó aceitaria.
func TestAOS446EmissorCunhaComQualquerUmDosDoisPinos(t *testing.T) {
	dir := t.TempDir()
	humano := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{11}, 32))
	pinoSW := hex.EncodeToString(humano.Public().(ed25519.PublicKey))
	a := aos446NovoAuth(t, strings.Repeat("3", 64), identity.MandateSSHSIGApplication, 0x01, 5, "sha512")
	lista := "alice=" + pinoSW + ",alice=" + a.linha()

	// O mandato de SOFTWARE, assinado pela seed antiga.
	chaveHumano := filepath.Join(dir, "humano.key")
	if err := os.WriteFile(chaveHumano, []byte(hex.EncodeToString(humano.Seed())), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, diag bytes.Buffer
	swPath := filepath.Join(dir, "sw.json")
	if err := run([]string{"mandate-sign", "--key-file", chaveHumano, "--human", "alice",
		"--board", "board-eu", "--agent", "agent:aos-orq", "--class", "orq", "--caps", "cap:doc.read",
		"--requesters", "sub-bob", "--out", swPath}, &out, &diag); err != nil {
		t.Fatalf("mandate-sign: %v", err)
	}

	// O mandato FIDO2, pela cerimónia.
	prefixo := filepath.Join(dir, "hw")
	out.Reset()
	diag.Reset()
	if err := run([]string{"mandate-prepare", "--human", "alice", "--board", "board-eu",
		"--agent", "agent:aos-orq", "--class", "orq", "--caps", "cap:doc.read",
		"--requesters", "sub-bob", "--out", prefixo}, &out, &diag); err != nil {
		t.Fatal(err)
	}
	assinar, _ := os.ReadFile(prefixo + sufixoDosBytes)
	sigPath := prefixo + sufixoDosBytes + ".sig"
	if err := os.WriteFile(sigPath, []byte(identity.EncodeSSHSIGArmor(a.envelope(t, identity.MandateSSHSIGNamespace, assinar))), 0o600); err != nil {
		t.Fatal(err)
	}
	hwPath := filepath.Join(dir, "hw.json")
	if err := run([]string{"mandate-attach", "--mandate", prefixo + sufixoDoDocumento,
		"--sig", sigPath, "--signer", a.linha(), "--out", hwPath}, &out, &diag); err != nil {
		t.Fatalf("mandate-attach: %v", err)
	}

	// OS DOIS CUNHAM com a MESMA lista de pinos — é isto que faz a rotação não parar nada.
	emissor := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{5}, 32))
	chaveEmissor := filepath.Join(dir, "issuer-auto.key")
	if err := os.WriteFile(chaveEmissor, []byte(hex.EncodeToString(emissor.Seed())), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ nome, mandato string }{
		{"mandato de software", swPath},
		{"mandato FIDO2", hwPath},
	} {
		destino := filepath.Join(dir, "nhi-"+c.nome+".jwt")
		out.Reset()
		diag.Reset()
		if err := run([]string{"mint-mandated", "--mandate", c.mandato, "--key-file", chaveEmissor,
			"--signers", lista, "--out", destino}, &out, &diag); err != nil {
			t.Fatalf("%s: tinha de cunhar com os dois pinos em vigor: %v", c.nome, err)
		}
	}

	// E um mandato assinado por uma chave que NÃO está em nenhum dos dois pinos é recusado.
	outro := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{99}, 32))
	chaveOutro := filepath.Join(dir, "outro.key")
	if err := os.WriteFile(chaveOutro, []byte(hex.EncodeToString(outro.Seed())), 0o600); err != nil {
		t.Fatal(err)
	}
	intrusoPath := filepath.Join(dir, "intruso.json")
	out.Reset()
	diag.Reset()
	if err := run([]string{"mandate-sign", "--key-file", chaveOutro, "--human", "alice",
		"--board", "board-eu", "--agent", "agent:aos-orq", "--class", "orq", "--caps", "cap:doc.read",
		"--requesters", "sub-bob", "--out", intrusoPath}, &out, &diag); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"mint-mandated", "--mandate", intrusoPath, "--key-file", chaveEmissor,
		"--signers", lista, "--out", filepath.Join(dir, "nao.jwt")}, &out, &diag)
	if err == nil {
		t.Fatal("um mandato de uma chave fora dos dois pinos tinha de ser RECUSADO")
	}
	if !strings.Contains(err.Error(), "nenhum dos 2 pino(s)") {
		t.Fatalf("a recusa tem de dizer que tentou os dois: %v", err)
	}
}

// TestAOS446AceitarAncorasEUmaLista — O ACHADO B1 DA 2.ª RONDA, reproduzido.
//
// O procedimento de rotação de pinos produz DUAS mudanças do retrato: o passo que ABRE a janela
// (o humano passa a ter dois pinos) e o que a FECHA (volta a um). Se as duas caírem entre dois
// selos diários, o flag de valor único não tinha COMO ser invocado — e a pressão que isso põe no
// operador é largar o `--anterior`, que é o que faz a verificação inteira nem correr.
func TestAOS446AceitarAncorasEUmaLista(t *testing.T) {
	dir := t.TempDir()
	chave := filepath.Join(dir, "sel.key")
	if err := os.WriteFile(chave, []byte(hex.EncodeToString(bytes.Repeat([]byte{7}, 32))), 0o600); err != nil {
		t.Fatal(err)
	}
	// [a] o estado inicial, [b] a janela ABERTA (dois pinos), [c] a janela FECHADA (um pino novo).
	a := audit.TrustAnchors{"mandate_signers": "ed25519:AAAA", "mandate_dual_pin_until": "(fechada)"}
	b := audit.TrustAnchors{"mandate_signers": "ed25519:AAAA+SHA256:BBBB", "mandate_dual_pin_until": "2026-10-10T23:59:59Z"}
	c := audit.TrustAnchors{"mandate_signers": "SHA256:BBBB", "mandate_dual_pin_until": "(fechada)"}

	base := wormComAncoras(t, a)
	saida, err := selarPara(t, base, chave, "", "")
	if err != nil {
		t.Fatalf("selagem de base: %v", err)
	}
	cpPath := filepath.Join(dir, "checkpoints.json")
	if err := os.WriteFile(cpPath, []byte(saida), 0o600); err != nil {
		t.Fatal(err)
	}

	rotacao := wormComAncoras(t, a, b, c) // as DUAS mudancas entre dois selos
	// Sem declarar nada, ou declarando SÓ um dos dois, continua a recusar.
	for _, so := range []string{"", b.Digest(), c.Digest()} {
		if _, err := selarPara(t, rotacao, chave, cpPath, so); !errors.Is(err, ErrWormSealAncorasTrocadas) {
			t.Fatalf("--aceitar-ancoras=%q tinha de recusar (falta o outro retrato), veio %v", so, err)
		}
	}
	// COM A LISTA, sela — e o diagnóstico diz quais dos declarados foram usados.
	for _, lista := range []string{
		b.Digest() + "," + c.Digest(),
		c.Digest() + "," + b.Digest(),
		" " + b.Digest() + " , " + c.Digest() + " ",
	} {
		if _, err := selarPara(t, rotacao, chave, cpPath, lista); err != nil {
			t.Fatalf("--aceitar-ancoras=%q tinha de selar: %v", lista, err)
		}
	}

	// CONTROLO NEGATIVO: um retrato NÃO declarado no meio da lista continua a recusar — senão a
	// lista seria um `--sim` com passos extra.
	intruso := audit.TrustAnchors{"mandate_signers": "ed25519:DO-ATACANTE"}
	comIntruso := wormComAncoras(t, a, b, intruso, c)
	_, err = selarPara(t, comIntruso, chave, cpPath, b.Digest()+","+c.Digest())
	if !errors.Is(err, ErrWormSealAncorasTrocadas) {
		t.Fatalf("um retrato nao declarado no meio tinha de recusar, veio %v", err)
	}
	if !strings.Contains(err.Error(), "seq 3") {
		t.Fatalf("a recusa tem de nomear o seq do intruso: %v", err)
	}

	// E o diagnóstico nomeia os digests usados, e o declarado que não apareceu.
	store, err := audit.OpenFileStoreReadOnly(rotacao)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var diag bytes.Buffer
	if err := exigirAncorasIguais(context.Background(), store,
		[]audit.Checkpoint{{Partition: audit.TrustAnchorsPartition, AuditSeq: 1}},
		b.Digest()+","+c.Digest()+",um-digest-que-nao-existe", &diag); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diag.String(), "2 de 3 digest(s)") {
		t.Fatalf("o diagnostico tem de dizer quantos dos declarados foram usados: %q", diag.String())
	}
	if !strings.Contains(diag.String(), "um-digest-que-nao-existe") {
		t.Fatalf("o diagnostico tem de nomear o declarado que nao apareceu: %q", diag.String())
	}
}

// TestAOS446RegistoEstranhoNaParticaoDasAncoras — achado BAIXO 5: um registo que não se reconhece
// era saltado em silêncio, e o diagnóstico chegava a dizer «nenhum registo novo» sobre uma
// partição onde alguém tinha escrito.
func TestAOS446RegistoEstranhoNaParticaoDasAncoras(t *testing.T) {
	ctx := context.Background()
	caminho := filepath.Join(t.TempDir(), "worm.wal")
	store, err := audit.OpenFileStore(caminho)
	if err != nil {
		t.Fatal(err)
	}
	a := audit.TrustAnchors{"mandate_signers": "ed25519:AAAA"}
	if _, err := store.Append(ctx, audit.AuditRecord{
		Partition: audit.TrustAnchorsPartition, Timestamp: time.Unix(1, 0).UTC(), Decision: audit.DecisionAllow,
		Resource:    audit.Resource{Type: audit.TrustAnchorsChangedEventType, Value: a.Digest()},
		Obligations: []audit.Obligation{{Type: audit.TrustAnchorsChangedEventType, Params: a.Params("config:node")}},
	}); err != nil {
		t.Fatal(err)
	}
	// Um registo que NÃO é de âncoras, apendido na partição delas.
	if _, err := store.Append(ctx, audit.AuditRecord{
		Partition: audit.TrustAnchorsPartition, Timestamp: time.Unix(2, 0).UTC(), Decision: audit.DecisionAllow,
		Resource: audit.Resource{Type: "coisa.estranha"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := audit.OpenFileStoreReadOnly(caminho)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()

	var diag bytes.Buffer
	if err := exigirAncorasIguais(ctx, ro,
		[]audit.Checkpoint{{Partition: audit.TrustAnchorsPartition, AuditSeq: 1}}, "", &diag); err != nil {
		t.Fatalf("um registo estranho nao pode RECUSAR a selagem: %v", err)
	}
	if !strings.Contains(diag.String(), "sem retrato de ancoras legivel") || !strings.Contains(diag.String(), "seq 2") {
		t.Fatalf("o registo estranho tem de ser contado e nomeado: %q", diag.String())
	}
	if strings.Contains(diag.String(), "nenhum registo de ancoras novo") {
		t.Fatalf("nao pode dizer que nao havia nada quando havia um registo: %q", diag.String())
	}
}
