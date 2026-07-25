package integration

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// AOS-177 — o MESMO guarda do nó, aplicado à RAIZ DE COMPOSIÇÃO.
//
// `integration` é o ápice que o nó importa: se a dep CBOR entrasse AQUI, entrava no binário
// do nó por transitividade. O guarda do módulo do nó (packages/cmd/aos/dep_isolation_test.go)
// já apanharia isso, mas só depois do facto e com uma mensagem que aponta para o sítio
// errado. Este falha NO MÓDULO ONDE O ERRO SERIA COMETIDO — quem adicionar o import vê a
// razão imediatamente, e a fronteira fica afirmada onde é decidida.
//
// A porta [DeviceAttestationVerifier] vive neste módulo e é STDLIB PURA por desenho; a
// implementação vive em packages/platform/attestation, que este módulo NÃO importa (a
// satisfação da interface é ESTRUTURAL — não precisa de aresta de importação).

// forbiddenAttestationDeps — fragmentos de caminho proibidos no fecho transitivo deste módulo.
var forbiddenAttestationDeps = []string{
	"github.com/fxamacker/cbor",
	"github.com/x448/float16",
	"github.com/aos-ref/platform/attestation",
	"webauthn",
}

func matchesForbiddenAttestationDep(p string) string {
	lower := strings.ToLower(p)
	for _, bad := range forbiddenAttestationDeps {
		if strings.Contains(lower, bad) {
			return bad
		}
	}
	return ""
}

func TestDepIsolation_IntegrationBuildGraphExcludesAttestationDeps(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("toolchain `go` indisponível — guarda do grafo transitivo saltado: %v", err)
	}

	out, err := exec.Command(goBin, "list", "-deps", "./...").CombinedOutput()
	if err != nil {
		t.Fatalf("`go list -deps ./...` em integration falhou: %v\n%s", err, out)
	}

	seen := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		seen++
		if bad := matchesForbiddenAttestationDep(p); bad != "" {
			t.Errorf("o grafo de build de `integration` inclui %q (contém %q) — a raiz de composição que o nó importa NÃO pode ganhar a dep CBOR/WebAuthn (Carta emenda 1.3)", p, bad)
		}
	}
	if seen < 50 {
		t.Fatalf("apenas %d pacotes no grafo de integration — o guarda ficou vacuoso", seen)
	}
}

// O go.mod da raiz de composição também não pode REQUERER a dep (guarda sem toolchain).
func TestDepIsolation_IntegrationGoModHasNoAttestationDeps(t *testing.T) {
	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("ler go.mod de integration: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "replace ") {
			continue
		}
		if bad := matchesForbiddenAttestationDep(trimmed); bad != "" {
			t.Errorf("go.mod de integration menciona %q na linha %q — a dep CBOR/WebAuthn não pode entrar aqui", bad, trimmed)
		}
	}
}
