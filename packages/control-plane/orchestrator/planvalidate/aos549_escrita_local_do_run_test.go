package planvalidate

import (
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// AOS-549 (emenda de 2026-10-22 ao ADR-034): a escrita local do run MUTA — conta como efeito e um
// verificador não a pode pinar — mas não torna o nó irreversível: um nó que só grava no armazém do
// próprio run não chega ao humano por isso.
func TestAOS549_EscritaLocalDoRunMutaSemSerIrreversivel(t *testing.T) {
	t.Parallel()
	m := MutationRunArtifact
	if !m.Mutates() || m.Irreversible() || m.String() != "run_artifact" {
		t.Fatalf("Mutates=%v Irreversible=%v String=%q", m.Mutates(), m.Irreversible(), m.String())
	}
	for _, outra := range []Mutation{MutationMutates, MutationUnknown, Mutation(200)} {
		if !outra.Irreversible() {
			t.Fatalf("%v deixou de ser irreversivel", outra)
		}
	}
	if MutationNone.Irreversible() {
		t.Fatal("a leitura passou a irreversivel")
	}
	c := safeCap("artifact_write")
	c.Mutation = MutationRunArtifact
	if !IsEffectTool(c) {
		t.Fatal("a escrita local do run tem de contar como efeito (um verificador nao a pode pinar)")
	}
	doc := baseDoc()
	doc.Nodes = []plan.Node{{NodeID: "grava", Role: "r", Objective: "o",
		Tools: []plan.ToolRef{{Name: "artifact_write", Version: "1.0.0", Digest: "sha256:artifact_write"}}}}
	nr := ResolveRisks(doc, Snapshot{Hash: capHash, Tools: []Capability{c}}, nil)["grava"]
	// gray, e não safe: a sensibilidade declarada não se aplica a uma tool que escreve (a regra do nó).
	if nr.Classification.Irreversible() || nr.Derived != plan.RiskGray {
		t.Fatalf("o no que so grava no armazem do run derivou %q (irreversivel=%v)", nr.Derived, nr.Classification.Irreversible())
	}
}
