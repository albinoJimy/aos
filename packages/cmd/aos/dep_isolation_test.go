package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// AOS-177 — ISOLAMENTO DA DEPENDÊNCIA CBOR/WebAuthn, tornado EXECUTÁVEL.
//
// A Carta (emenda 1.3) abre uma exceção ESCOPADA ao ADR-017: a lib CBOR necessária à
// verificação de attestation WebAuthn é permitida APENAS no componente de autoridade de
// identidade EXTERNO (packages/platform/attestation, módulo próprio). O BINÁRIO DO NÓ
// mantém-se zero-dep (stdlib + cedar-go).
//
// "Mantém-se zero-dep" é uma afirmação verificável — e é isto que a verifica. Sem este
// guarda, a exceção seria uma promessa: bastaria um import distraído em `integration` (que o
// nó importa) para o cbor entrar no binário do nó sem ninguém dar por isso, e a emenda 1.3
// passaria a ficção. Segue o molde de boundary_orq_sch_test.go (ADR-018 §5): interroga o
// GRAFO DE BUILD EFECTIVO (`go list -deps ./...`), não apenas os imports directos deste
// comando — a invariante é sobre o PROCESSO do nó, e uma dep arrastada transitivamente conta
// exactamente na mesma.

// forbiddenAttestationDeps — fragmentos de caminho de pacote que NÃO podem existir no fecho
// transitivo do nó. Incluem-se a lib CBOR, a sua dep transitiva, o próprio módulo de
// attestation e qualquer framework WebAuthn (nenhum é usado, e um dia que alguém tente, o
// guarda apanha-o).
var forbiddenAttestationDeps = []string{
	"github.com/fxamacker/cbor",
	"github.com/x448/float16",
	"github.com/aos-ref/platform/attestation",
	"webauthn",
}

// matchesForbiddenDep devolve o fragmento proibido que o caminho contém (ou "").
func matchesForbiddenDep(p string) string {
	lower := strings.ToLower(p)
	for _, bad := range forbiddenAttestationDeps {
		if strings.Contains(lower, bad) {
			return bad
		}
	}
	return ""
}

func TestDepIsolation_NodeBuildGraphExcludesAttestationDeps(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		// Sem toolchain a verificação transitiva não é possível; o guarda do go.mod (abaixo)
		// mantém-se activo. Sinaliza-se o skip em vez de um falso-verde.
		t.Skipf("toolchain `go` indisponível — guarda do grafo transitivo saltado: %v", err)
	}

	out, err := exec.Command(goBin, "list", "-deps", "./...").CombinedOutput()
	if err != nil {
		t.Fatalf("`go list -deps ./...` no módulo do nó falhou: %v\n%s", err, out)
	}

	seen := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		seen++
		if bad := matchesForbiddenDep(p); bad != "" {
			t.Errorf("o grafo de build do nó inclui %q (contém %q) — viola a Carta emenda 1.3: a dep CBOR/WebAuthn é EXCLUSIVA do componente de autoridade externo; o binário do nó fica zero-dep", p, bad)
		}
	}
	if seen == 0 {
		t.Fatal("`go list -deps ./...` não devolveu pacotes — o guarda ficou vacuoso")
	}

	// NÃO-VACUOSIDADE: o guarda tem de estar realmente a inspeccionar um grafo GRANDE (a
	// raiz de composição `integration` e tudo o que ela arrasta). Um grafo minúsculo
	// significaria que se estava a verificar outra coisa.
	if seen < 50 {
		t.Fatalf("apenas %d pacotes no grafo do nó — suspeito: o guarda pode estar a olhar para o alvo errado", seen)
	}
	if !strings.Contains(string(out), "github.com/aos-ref/integration") {
		t.Fatal("o grafo do nó não inclui `integration` — o guarda não está a ver a raiz de composição real")
	}
}

// TestDepIsolation_NodeGoModHasNoAttestationDeps é o guarda que NÃO precisa de toolchain: as
// deps proibidas também não podem aparecer no go.mod do nó (nem como indirectas). É barato,
// determinista, e apanha o caso em que alguém adiciona a dep "para experimentar".
func TestDepIsolation_NodeGoModHasNoAttestationDeps(t *testing.T) {
	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("ler go.mod do nó: %v", err)
	}
	text := string(raw)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		// As directivas `replace` mapeiam TODOS os módulos do monorepo por convenção
		// (incluindo os que este módulo não requer); o que interessa é o que é REQUERIDO.
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "replace ") {
			continue
		}
		if bad := matchesForbiddenDep(trimmed); bad != "" {
			t.Errorf("go.mod do nó menciona %q na linha %q — a dep CBOR/WebAuthn não pode entrar no módulo do nó (Carta emenda 1.3)", bad, trimmed)
		}
	}
}
