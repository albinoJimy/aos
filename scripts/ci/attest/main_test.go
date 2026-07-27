// Testes do verificador de atestação (AOS-207).
//
// PORQUÊ ESTES TESTES SÃO O AUTO-TESTE DO MECANISMO: o modo de falha mais perigoso de um
// verificador não é recusar de mais — é deixar de recusar. Um `verify` que devolvesse 0 para
// tudo produziria uma entrega «verificada» indistinguível de uma verdadeira. Por isso cada
// teste aqui é uma NEGAÇÃO nomeada: payload adulterado, keyid desconhecido, chave revogada,
// keyid incoerente com o material, assinatura de outra chave, janela expirada.
//
// TODO o material de chave destes testes é EFÉMERO (gerado em runtime, em t.TempDir()) — a
// mesma regra que `scripts/ci/secrets.sh` impõe ao repositório inteiro: nunca há chave
// privada committada, nem sequer em fixtures de teste.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const stmt = `{"_type":"https://in-toto.io/Statement/v1","subject":[{"name":"img","digest":{"sha256":"aa"}}],"predicateType":"x","predicate":{}}`

// newSigner cria um par EFÉMERO e devolve (caminho da seed, entrada de registo).
func newSigner(t *testing.T, dir string) (string, rosterKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gerar par: %v", err)
	}
	seedPath := filepath.Join(dir, "seed.hex")
	if err := os.WriteFile(seedPath, []byte(hex.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		t.Fatalf("escrever seed: %v", err)
	}
	return seedPath, rosterKey{
		KeyID:     keyIDOf(pub),
		Algorithm: algEd25519,
		PublicKey: hex.EncodeToString(pub),
		Holder:    "teste efémero",
		Custody:   "t.TempDir()",
		Status:    "active",
		NotBefore: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	}
}

func writeRoster(t *testing.T, path string, keys ...rosterKey) string {
	t.Helper()
	blob, err := json.MarshalIndent(roster{Format: rosterFormat, Note: "teste", Keys: keys}, "", "  ")
	if err != nil {
		t.Fatalf("serializar registo: %v", err)
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatalf("escrever registo: %v", err)
	}
	return path
}

// signFixture assina `stmt` e devolve o caminho do envelope.
func signFixture(t *testing.T, dir, seedPath string) string {
	t.Helper()
	payload := filepath.Join(dir, "statement.json")
	if err := os.WriteFile(payload, []byte(stmt), 0o600); err != nil {
		t.Fatalf("escrever payload: %v", err)
	}
	envPath := filepath.Join(dir, "attestation.dsse.json")
	if err := cmdSign([]string{"-key", seedPath, "-payload", payload, "-out", envPath}); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return envPath
}

func readEnv(t *testing.T, path string) envelope {
	t.Helper()
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ler envelope: %v", err)
	}
	var e envelope
	if err := json.Unmarshal(blob, &e); err != nil {
		t.Fatalf("envelope malformado: %v", err)
	}
	return e
}

func writeEnv(t *testing.T, path string, e envelope) {
	t.Helper()
	blob, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		t.Fatalf("serializar envelope: %v", err)
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatalf("escrever envelope: %v", err)
	}
}

// TestAOS207AssinaturaValidaEAceite — o caminho POSITIVO. Sem ele, todas as negações abaixo
// seriam satisfeitas por um verificador que recusa sempre.
func TestAOS207AssinaturaValidaEAceite(t *testing.T) {
	dir := t.TempDir()
	seed, key := newSigner(t, dir)
	envPath := signFixture(t, dir, seed)
	rosterPath := writeRoster(t, filepath.Join(dir, "pubkeys.json"), key)
	payloadOut := filepath.Join(dir, "payload.json")
	if err := cmdVerify([]string{"-envelope", envPath, "-roster", rosterPath, "-payload-out", payloadOut}); err != nil {
		t.Fatalf("assinatura legítima RECUSADA: %v", err)
	}
	got, err := os.ReadFile(payloadOut)
	if err != nil {
		t.Fatalf("ler payload autenticado: %v", err)
	}
	if string(got) != stmt {
		t.Fatalf("payload autenticado difere do assinado")
	}
}

// TestAOS207PayloadAdulteradoERecusado — é a propriedade central: mexer num digest DENTRO do
// payload assinado invalida a assinatura.
func TestAOS207PayloadAdulteradoERecusado(t *testing.T) {
	dir := t.TempDir()
	seed, key := newSigner(t, dir)
	envPath := signFixture(t, dir, seed)
	rosterPath := writeRoster(t, filepath.Join(dir, "pubkeys.json"), key)

	e := readEnv(t, envPath)
	raw, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		t.Fatalf("descodificar payload: %v", err)
	}
	e.Payload = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(raw), `"sha256":"aa"`, `"sha256":"bb"`, 1)))
	writeEnv(t, envPath, e)

	err = cmdVerify([]string{"-envelope", envPath, "-roster", rosterPath})
	if err == nil {
		t.Fatal("payload adulterado foi ACEITE — o verificador não impõe nada")
	}
	if !strings.Contains(err.Error(), "ASSINATURA INVÁLIDA") {
		t.Fatalf("recusa pelo motivo errado: %v", err)
	}
}

// TestAOS207ChaveDesconhecidaERecusada — assinatura válida, signatário não declarado.
func TestAOS207ChaveDesconhecidaERecusada(t *testing.T) {
	dir := t.TempDir()
	seed, _ := newSigner(t, dir)
	envPath := signFixture(t, dir, seed)
	_, outra := newSigner(t, t.TempDir())
	rosterPath := writeRoster(t, filepath.Join(dir, "pubkeys.json"), outra)

	err := cmdVerify([]string{"-envelope", envPath, "-roster", rosterPath})
	if err == nil {
		t.Fatal("assinatura de chave DESCONHECIDA foi aceite")
	}
	if !strings.Contains(err.Error(), "DESCONHECIDO") {
		t.Fatalf("recusa pelo motivo errado: %v", err)
	}
}

// TestAOS207ChaveRevogadaERecusada — a revogação tem de vencer a validade criptográfica.
func TestAOS207ChaveRevogadaERecusada(t *testing.T) {
	dir := t.TempDir()
	seed, key := newSigner(t, dir)
	envPath := signFixture(t, dir, seed)
	key.Status = "revoked"
	rosterPath := writeRoster(t, filepath.Join(dir, "pubkeys.json"), key)

	err := cmdVerify([]string{"-envelope", envPath, "-roster", rosterPath})
	if err == nil {
		t.Fatal("assinatura de chave REVOGADA foi aceite")
	}
	if !strings.Contains(err.Error(), "REVOGADA") {
		t.Fatalf("recusa pelo motivo errado: %v", err)
	}
}

// TestAOS207KeyIDIncoerenteERecusado — o registo declara um keyid e traz outro material.
// Sem esta verificação, a confiança seria no RÓTULO e não na chave.
func TestAOS207KeyIDIncoerenteERecusado(t *testing.T) {
	dir := t.TempDir()
	seed, key := newSigner(t, dir)
	envPath := signFixture(t, dir, seed)
	outroPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gerar par: %v", err)
	}
	key.PublicKey = hex.EncodeToString(outroPub) // keyid do signatário, material de outra chave
	rosterPath := writeRoster(t, filepath.Join(dir, "pubkeys.json"), key)

	err = cmdVerify([]string{"-envelope", envPath, "-roster", rosterPath})
	if err == nil {
		t.Fatal("entrada de registo INCOERENTE (keyid≠sha256(publicKey)) foi aceite")
	}
	if !strings.Contains(err.Error(), "INCOERENTE") {
		t.Fatalf("recusa pelo motivo errado: %v", err)
	}
}

// TestAOS207JanelaExpiradaERecusada — chave rodada fora da janela.
func TestAOS207JanelaExpiradaERecusada(t *testing.T) {
	dir := t.TempDir()
	seed, key := newSigner(t, dir)
	envPath := signFixture(t, dir, seed)
	key.Status = "rotated"
	key.NotAfter = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	rosterPath := writeRoster(t, filepath.Join(dir, "pubkeys.json"), key)

	err := cmdVerify([]string{"-envelope", envPath, "-roster", rosterPath})
	if err == nil {
		t.Fatal("assinatura fora da janela de validade foi aceite")
	}
	if !strings.Contains(err.Error(), "notAfter") {
		t.Fatalf("recusa pelo motivo errado: %v", err)
	}
}

// TestAOS207RotatedSemNotAfterERecusada — `rotated` sem fim de janela é `active` disfarçado.
func TestAOS207RotatedSemNotAfterERecusada(t *testing.T) {
	dir := t.TempDir()
	seed, key := newSigner(t, dir)
	envPath := signFixture(t, dir, seed)
	key.Status = "rotated"
	rosterPath := writeRoster(t, filepath.Join(dir, "pubkeys.json"), key)

	if err := cmdVerify([]string{"-envelope", envPath, "-roster", rosterPath}); err == nil {
		t.Fatal("`rotated` sem notAfter foi aceite")
	}
}

// TestAOS207RegistoVazioRecusa — fail-closed por omissão: sem chave de release provisionada,
// nada é verificável. É o estado em que o repositório é entregue.
func TestAOS207RegistoVazioRecusa(t *testing.T) {
	dir := t.TempDir()
	seed, _ := newSigner(t, dir)
	envPath := signFixture(t, dir, seed)
	rosterPath := writeRoster(t, filepath.Join(dir, "pubkeys.json"))

	err := cmdVerify([]string{"-envelope", envPath, "-roster", rosterPath})
	if err == nil {
		t.Fatal("registo de chaves VAZIO produziu verificação verde")
	}
	if !strings.Contains(err.Error(), "VAZIO") {
		t.Fatalf("recusa pelo motivo errado: %v", err)
	}
}

// TestAOS207PayloadTypeConfusaoERecusada — o PAE liga a assinatura ao payloadType; trocar o
// tipo tem de invalidar, não apenas mudar a interpretação.
func TestAOS207PayloadTypeConfusaoERecusada(t *testing.T) {
	dir := t.TempDir()
	seed, key := newSigner(t, dir)
	envPath := signFixture(t, dir, seed)
	rosterPath := writeRoster(t, filepath.Join(dir, "pubkeys.json"), key)

	e := readEnv(t, envPath)
	e.PayloadType = "application/json"
	writeEnv(t, envPath, e)

	if err := cmdVerify([]string{"-envelope", envPath, "-roster", rosterPath}); err == nil {
		t.Fatal("payloadType trocado foi aceite")
	}
}

// TestAOS207KeygenRecusaDentroDoRepo — a invariante «nenhuma chave privada no repositório» é
// IMPOSTA pela ferramenta, não apenas recomendada no README.
func TestAOS207KeygenRecusaDentroDoRepo(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatalf("simular árvore git: %v", err)
	}
	alvo := filepath.Join(dir, "sub", "release.seed")
	if err := os.MkdirAll(filepath.Dir(alvo), 0o750); err != nil {
		t.Fatalf("criar subdirectório: %v", err)
	}
	err := cmdKeygen([]string{"-out", alvo})
	if err == nil {
		t.Fatal("keygen escreveu material privado DENTRO de uma árvore git")
	}
	if !strings.Contains(err.Error(), "RECUSADO") {
		t.Fatalf("recusa pelo motivo errado: %v", err)
	}
	if _, statErr := os.Stat(alvo); statErr == nil {
		t.Fatal("keygen recusou mas DEIXOU o ficheiro de chave criado")
	}
}

// TestAOS207SignRecusaPayloadNaoInToto — assinar bytes opacos e chamar-lhes atestação
// produziria uma garantia que nenhum verificador consegue interpretar.
func TestAOS207SignRecusaPayloadNaoInToto(t *testing.T) {
	dir := t.TempDir()
	seed, _ := newSigner(t, dir)
	payload := filepath.Join(dir, "x.json")
	if err := os.WriteFile(payload, []byte(`{"qualquer":"coisa"}`), 0o600); err != nil {
		t.Fatalf("escrever payload: %v", err)
	}
	err := cmdSign([]string{"-key", seed, "-payload", payload, "-out", filepath.Join(dir, "e.json")})
	if err == nil {
		t.Fatal("sign aceitou um payload que não é in-toto Statement v1")
	}
}

// TestAOS207PAEConformeDSSE — vector fixo do PAE. Se esta codificação derivar, envelopes
// assinados por versões diferentes deixam de verificar sem que nada o diga.
func TestAOS207PAEConformeDSSE(t *testing.T) {
	got := string(pae("application/vnd.in-toto+json", []byte("hello")))
	want := "DSSEv1 28 application/vnd.in-toto+json 5 hello"
	if got != want {
		t.Fatalf("PAE = %q, esperado %q", got, want)
	}
}
