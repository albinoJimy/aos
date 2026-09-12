package planmigrate

import (
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	pe "github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// TestClassifyKind_ConsistenteComProducao fixa a política de folha-vs-papel replicada de
// planmaterialize (AOS-390): papel sse tem dependente; verificador SEMPRE folha. Antes
// classificava tudo folha — quebrava o replay byte-a-byte de um plano com papéis.
func TestClassifyKind_ConsistenteComProducao(t *testing.T) {
	doc := plan.PlanDocument{Nodes: []plan.Node{
		{NodeID: "arch", Role: "worker"},                                      // tem dependente ⇒ papel
		{NodeID: "impl", Role: "worker", DependsOn: []string{"arch"}},         // sumidouro ⇒ folha
		{NodeID: "rev", Role: plan.RoleVerifier, DependsOn: []string{"arch"}}, // verificador ⇒ folha (mesmo com/sem dependentes)
	}}

	casos := map[string]pe.SpawnKind{
		"arch": pe.SpawnRole,
		"impl": pe.SpawnLeaf,
		"rev":  pe.SpawnLeaf,
	}
	for _, n := range doc.Nodes {
		if got := classifyKind(n, doc); got != casos[n.NodeID] {
			t.Errorf("classifyKind(%q) = %q, quer %q", n.NodeID, got, casos[n.NodeID])
		}
	}
}

// TestClassifyKind_VerificadorComDependenteContinuaFolha — o forço verificador→folha
// (ADR-022 §2.2) vence a topologia: um verificador de que OUTRO nó depende continua folha,
// não vira papel-que-expande (um verificador não delega).
func TestClassifyKind_VerificadorComDependenteContinuaFolha(t *testing.T) {
	doc := plan.PlanDocument{Nodes: []plan.Node{
		{NodeID: "rev", Role: plan.RoleVerifier},
		{NodeID: "consumidor", Role: "worker", DependsOn: []string{"rev"}},
	}}
	if got := classifyKind(doc.Nodes[0], doc); got != pe.SpawnLeaf {
		t.Errorf("verificador com dependente = %q, quer SpawnLeaf (não delega)", got)
	}
}
