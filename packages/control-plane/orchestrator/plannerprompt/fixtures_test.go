package plannerprompt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
)

// testSnapshot é o snapshot de capabilities PINADO das fixtures: uma única tool
// admissível `search@1.0.0`. Um candidato que refira outra tool (ex.: `exfiltrate`)
// resolve para rejeição no validador de AOS-231 — é a base da asserção de segurança.
func testSnapshot() planvalidate.Snapshot {
	return planvalidate.Snapshot{
		Hash: "snap-v1",
		Tools: []planvalidate.Capability{
			{Name: "search", Version: "1.0.0", Digest: "sha256:search", Admissible: true},
		},
	}
}

// testCeilings são tectos generosos: não é o tecto que rejeita nas fixtures boas.
func testCeilings() planvalidate.Ceilings {
	return planvalidate.Ceilings{MaxNodes: 10, MaxDepth: 5, MaxFanout: 5}
}

// loadCandidate desserializa UMA fixture de plano via [plan.Decode] (o desserializador
// sancionado, fail-closed). Falha o teste se a fixture não parsear — mantém as fixtures
// honestas (um JSON malformado não passa despercebido como "candidato").
func loadCandidate(t *testing.T, name string) plan.PlanDocument {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "search-summarize", name))
	if err != nil {
		t.Fatalf("ler fixture %q: %v", name, err)
	}
	doc, err := plan.Decode(raw)
	if err != nil {
		t.Fatalf("decode fixture %q: %v", name, err)
	}
	return doc
}

// atLeastTwoNodes é a rubrica SEMÂNTICA de qualidade das fixtures: "o objectivo foi
// decomposto em >= 2 nós" (uma decomposição de 1 nó é admissível mas de qualidade
// inferior). Predicado puro e declarado.
func atLeastTwoNodes(doc plan.PlanDocument) bool { return len(doc.Nodes) >= 2 }
