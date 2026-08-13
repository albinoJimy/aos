package weights_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/weights"
)

// testKey devolve um par ed25519 DETERMINISTA de teste (não é a chave de produção:
// o trust anchor pinado só é exigido por LoadTable, o carregador embebido).
func testKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	var seed [32]byte
	copy(seed[:], []byte("aos-269-weights-table-test-key!!"))
	priv := ed25519.NewKeyFromSeed(seed[:])
	return priv, base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
}

// sign assina o DIGEST canónico da tabela com a chave de teste.
func sign(t *testing.T, priv ed25519.PrivateKey, tableJSON string) string {
	t.Helper()
	dig, err := weights.Digest([]byte(tableJSON))
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(dig)))
}

const goodTable = `{"version":"t/v1","semver":"1.2.3","default_profile":"balanced","profiles":[` +
	`{"name":"balanced","weights":{"health":200,"headroom":150,"cost":200,"latency":150,"task_fit":200,"stability":100}},` +
	`{"name":"cheap","weights":{"health":100,"headroom":100,"cost":600,"latency":50,"task_fit":100,"stability":50}}]}`

// TestLoadTable_Embedded prova o caminho de PRODUÇÃO: a tabela embebida carrega
// com o trust anchor PINADO e a assinatura real, e expõe os perfis documentados.
func TestLoadTable_Embedded(t *testing.T) {
	tab, err := weights.LoadTable()
	if err != nil {
		t.Fatalf("LoadTable (artefacto embebido+assinado): %v", err)
	}
	if tab.Default() != "balanced" {
		t.Errorf("perfil por omissão deve ser balanced, obtive %q", tab.Default())
	}
	names := strings.Join(tab.Names(), ",")
	if names != "balanced,cheap,fast,quality" {
		t.Errorf("perfis esperados balanced,cheap,fast,quality; obtive %q", names)
	}
	// A versão é tamper-evident: "versão#digest12".
	if !strings.HasPrefix(tab.Version(), "gw-scoring-weights/v1#") || len(tab.Version()) != len("gw-scoring-weights/v1#")+12 {
		t.Errorf("versão tamper-evident inesperada: %q", tab.Version())
	}
	if tab.SemVer() != "1.0.0" {
		t.Errorf("semver inicial deve ser 1.0.0 (ADR-012), obtive %q", tab.SemVer())
	}
	// Todos os perfis somam > 0 (senão não ordenariam nada).
	for _, n := range tab.Names() {
		w, ok := tab.Lookup(n)
		if !ok || w.Sum() <= 0 {
			t.Errorf("perfil %q inválido (ok=%v soma=%d)", n, ok, w.Sum())
		}
	}
	if _, ok := tab.Lookup("nao-existe"); ok {
		t.Error("um perfil inexistente NÃO pode resolver (fail-closed)")
	}
}

// TestLoadSignedTable_TamperFailsClosed é a prova TAMPER-EVIDENT: mexer num único
// peso invalida a assinatura — a tabela adulterada NÃO carrega (fail-closed).
func TestLoadSignedTable_TamperFailsClosed(t *testing.T) {
	priv, pub := testKey(t)
	sig := sign(t, priv, goodTable)
	if _, err := weights.LoadSignedTable([]byte(goodTable), sig, pub); err != nil {
		t.Fatalf("a tabela íntegra devia carregar: %v", err)
	}
	tampered := strings.Replace(goodTable, `"cost":200`, `"cost":900`, 1)
	if tampered == goodTable {
		t.Fatal("fixture inválida: a adulteração não alterou o documento")
	}
	if _, err := weights.LoadSignedTable([]byte(tampered), sig, pub); !errors.Is(err, weights.ErrSignatureInvalid) {
		t.Fatalf("um peso adulterado devia falhar com ErrSignatureInvalid, obtive %v", err)
	}
}

// TestLoadSignedTable_ForeignKeyRejected prova que reassinar com OUTRA chave não
// chega: a verificação é contra a pública de confiança fornecida.
func TestLoadSignedTable_ForeignKeyRejected(t *testing.T) {
	_, pub := testKey(t)
	var seed [32]byte
	copy(seed[:], []byte("chave-do-atacante-nao-confiavel!"))
	foreign := ed25519.NewKeyFromSeed(seed[:])
	sig := sign(t, foreign, goodTable)
	if _, err := weights.LoadSignedTable([]byte(goodTable), sig, pub); !errors.Is(err, weights.ErrSignatureInvalid) {
		t.Fatalf("assinatura de chave estranha devia ser recusada, obtive %v", err)
	}
}

// TestParse_MalformedFailsClosed cobre a validação fail-closed do schema: sem
// versão, SemVer inválido (ADR-012), sem perfis, perfil duplicado, peso fora de
// intervalo, perfil de soma zero, default inexistente e campo desconhecido.
func TestParse_MalformedFailsClosed(t *testing.T) {
	priv, pub := testKey(t)
	cases := []struct{ name, doc string }{
		{"sem versao", `{"version":"","semver":"1.0.0","default_profile":"a","profiles":[{"name":"a","weights":{"cost":1}}]}`},
		{"semver invalido", `{"version":"t","semver":"1.0","default_profile":"a","profiles":[{"name":"a","weights":{"cost":1}}]}`},
		{"semver com zero a esquerda", `{"version":"t","semver":"1.0.01","default_profile":"a","profiles":[{"name":"a","weights":{"cost":1}}]}`},
		{"sem perfis", `{"version":"t","semver":"1.0.0","default_profile":"a","profiles":[]}`},
		{"perfil sem nome", `{"version":"t","semver":"1.0.0","default_profile":"a","profiles":[{"name":"","weights":{"cost":1}}]}`},
		{"perfil duplicado", `{"version":"t","semver":"1.0.0","default_profile":"a","profiles":[{"name":"a","weights":{"cost":1}},{"name":"a","weights":{"cost":2}}]}`},
		{"peso negativo", `{"version":"t","semver":"1.0.0","default_profile":"a","profiles":[{"name":"a","weights":{"cost":-1,"health":5}}]}`},
		{"peso acima do tecto", `{"version":"t","semver":"1.0.0","default_profile":"a","profiles":[{"name":"a","weights":{"cost":1001}}]}`},
		{"soma zero", `{"version":"t","semver":"1.0.0","default_profile":"a","profiles":[{"name":"a","weights":{}}]}`},
		{"default inexistente", `{"version":"t","semver":"1.0.0","default_profile":"z","profiles":[{"name":"a","weights":{"cost":1}}]}`},
		{"campo desconhecido", `{"version":"t","semver":"1.0.0","default_profile":"a","bandit":true,"profiles":[{"name":"a","weights":{"cost":1}}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Mesmo ASSINADA (o atacante controla o documento e a sua chave), uma tabela
			// malformada não carrega: a validação estrutural é independente da assinatura.
			dig, derr := weights.Digest([]byte(c.doc))
			var sig string
			if derr == nil {
				sig = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(dig)))
			}
			if _, err := weights.LoadSignedTable([]byte(c.doc), sig, pub); !errors.Is(err, weights.ErrTableMalformed) {
				t.Fatalf("%s: esperava ErrTableMalformed, obtive %v", c.name, err)
			}
		})
	}
}

// TestLoadSignedTable_PubKeyInvalid cobre a chave pública inválida (fail-closed).
func TestLoadSignedTable_PubKeyInvalid(t *testing.T) {
	priv, _ := testKey(t)
	sig := sign(t, priv, goodTable)
	if _, err := weights.LoadSignedTable([]byte(goodTable), sig, "nao-base64!!"); !errors.Is(err, weights.ErrPubKeyInvalid) {
		t.Fatalf("pubkey não-base64 devia falhar com ErrPubKeyInvalid, obtive %v", err)
	}
	if _, err := weights.LoadSignedTable([]byte(goodTable), sig, base64.StdEncoding.EncodeToString([]byte("curta"))); !errors.Is(err, weights.ErrPubKeyInvalid) {
		t.Fatal("pubkey de tamanho errado devia falhar com ErrPubKeyInvalid")
	}
	if _, err := weights.LoadSignedTable([]byte(goodTable), "nao-base64!!", func() string { _, p := testKey(t); return p }()); !errors.Is(err, weights.ErrSignatureInvalid) {
		t.Fatal("assinatura não-base64 devia falhar com ErrSignatureInvalid")
	}
}

// TestLoadSignedTableFromDir prova a via EXTERNA (bundle montado, anchor
// out-of-band): mesma governação, proveniência diferente. Fail-closed em ausência.
func TestLoadSignedTableFromDir(t *testing.T) {
	priv, pub := testKey(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, weights.BundleTableFile), []byte(goodTable), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, weights.BundleSigFile), []byte(sign(t, priv, goodTable)), 0o600); err != nil {
		t.Fatal(err)
	}
	tab, err := weights.LoadSignedTableFromDir(dir, pub)
	if err != nil {
		t.Fatalf("bundle externo assinado devia carregar: %v", err)
	}
	if tab.Default() != "balanced" {
		t.Errorf("default do bundle: %q", tab.Default())
	}
	if _, err := weights.LoadSignedTableFromDir(t.TempDir(), pub); err == nil {
		t.Error("bundle ausente devia falhar-fechar")
	}
	// Assinatura correcta mas anchor errado (a pública do atacante) ⇒ recusa.
	var seed [32]byte
	copy(seed[:], []byte("outra-chave-para-o-anchor-errado"))
	other := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey))
	if _, err := weights.LoadSignedTableFromDir(dir, other); !errors.Is(err, weights.ErrSignatureInvalid) {
		t.Fatalf("anchor errado devia recusar, obtive %v", err)
	}
}

// TestDigest_CanonicalIndependenteDaOrdem prova que o digest (material assinado) é
// canónico: reordenar os perfis ou mudar espaços NÃO muda o digest — a assinatura
// cobre CONTEÚDO, não formatação.
func TestDigest_CanonicalIndependenteDaOrdem(t *testing.T) {
	reordered := `{"version":"t/v1","semver":"1.2.3","default_profile":"balanced","profiles":[` +
		`{"name":"cheap","weights":{"health":100,"headroom":100,"cost":600,"latency":50,"task_fit":100,"stability":50}},` +
		`  {"name":"balanced","weights":{"health":200,"headroom":150,"cost":200,"latency":150,"task_fit":200,"stability":100}}]}`
	a, err := weights.Digest([]byte(goodTable))
	if err != nil {
		t.Fatal(err)
	}
	b, err := weights.Digest([]byte(reordered))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("o digest tem de ser canónico (independente de ordem/espaços): %s != %s", a, b)
	}
	// E MUDAR um peso tem de mudar o digest (tamper-evident).
	c, err := weights.Digest([]byte(strings.Replace(goodTable, `"cost":200`, `"cost":201`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if c == a {
		t.Error("mudar um peso TEM de mudar o digest")
	}
}
