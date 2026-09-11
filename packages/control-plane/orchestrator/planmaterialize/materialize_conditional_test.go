package planmaterialize

import (
	"context"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// condEdge devolve a aresta condicional `verdict eq fail` sobre `from` — a forma do
// ramo de recuperação de ADR-022 §2.1 (o nó só despacha se a origem REPROVAR).
func condEdge(from string) plan.ConditionalEdge {
	return plan.ConditionalEdge{
		From: from,
		When: []plan.Predicate{{Subject: plan.SubjectVerdict, Op: plan.OpEq, Enum: plan.EnumFail}},
	}
}

// TestAOS390_MaterializeAdmiteCondicionalSemEfeito — pós ADR-024, a materialização é
// ADMISSÃO-PURA: um plano APROVADO com `conditional_on` é admitido (nós pendentes no
// DAG + `plan.materialized` apenso) SEM produzir efeito. A avaliação da condição e a
// poda de `branch_not_taken` são do despacho governado (plandispatch.Dispatcher), não da
// materialização.
//
// Isto INVERTE o guard interino de AOS-389 (que recusava planos condicionais): o guard
// deixou de ser necessário porque ADMITIR um nó condicional no DAG não é EXECUTÁ-LO — o
// efeito só nasce no sink, a jusante da avaliação da condição. A prova da PODA vive numa
// composição real com o Dispatcher (ver runlifecycle/derivacao_condicional_test.go).
func TestAOS390_MaterializeAdmiteCondicionalSemEfeito(t *testing.T) {
	h := newHarness(t, nil)

	a := node("a", []plan.ToolRef{tool("t")}) // produtor observado pela condição
	b := plan.Node{
		NodeID:        "b",
		Role:          "role-b",
		Objective:     "obj-b",
		Tools:         []plan.ToolRef{tool("t")},
		ConditionalOn: []plan.ConditionalEdge{condEdge("a")},
	}

	p, err := h.m.Materialize(context.Background(), baseReq(a, b))
	if err != nil {
		t.Fatalf("Materialize de plano condicional falhou (devia ADMITIR, não recusar): %v", err)
	}

	// Ambos os nós ADMITIDOS no DAG (a como folha; b, com dependente nenhum mas
	// condicional, é folha pelo DefaultClassifier) e `plan.materialized` apenso.
	if len(h.rec.payloads) != 1 {
		t.Fatalf("esperava 1 plan.materialized, got %d", len(h.rec.payloads))
	}
	if len(h.lf.calls) != 2 {
		t.Errorf("esperava 2 nós admitidos (a,b), got %d: %+v", len(h.lf.calls), h.lf.calls)
	}
	// O payload regista os dois nós — o `conditional_on` não bloqueia a admissão.
	if len(p.Nodes) != 2 {
		t.Errorf("payload devia ter 2 nós, got %d", len(p.Nodes))
	}
}

// TestAOS390_PapelCondicionalAdmitidoSemTool reproduz a forma mais aguda do antigo
// fail-open (um nó condicional que o DefaultClassifier torna PAPEL, porque outro dele
// depende): antes seria spawnado como sub-agente sem avaliar a condição. Pós ADR-024, é
// ADMITIDO no DAG como nó PENDENTE SEM TOOL — nenhum spawn acontece na materialização.
func TestAOS390_PapelCondicionalAdmitidoSemTool(t *testing.T) {
	h := newHarness(t, nil)

	a := node("a", []plan.ToolRef{tool("t")})
	b := plan.Node{
		NodeID:        "b",
		Role:          "role-b",
		Objective:     "obj-b",
		Tools:         []plan.ToolRef{tool("t")},
		ConditionalOn: []plan.ConditionalEdge{condEdge("a")},
	}
	c := node("c", []plan.ToolRef{tool("t")}, "b") // depende de b ⇒ b é papel-que-expande

	p, err := h.m.Materialize(context.Background(), baseReq(a, b, c))
	if err != nil {
		t.Fatalf("Materialize falhou: %v", err)
	}

	// b é papel no payload, mas foi ADMITIDO (não spawnado) — aparece em lf.calls SEM tool.
	var bKind plannerevents.SpawnKind
	for _, n := range p.Nodes {
		if n.NodeID == "b" {
			bKind = n.Kind
		}
	}
	if bKind != plannerevents.SpawnRole {
		t.Errorf("b devia ser classificado SpawnRole (tem dependente), got %q", bKind)
	}
	var bAdmitido bool
	for _, lc := range h.lf.calls {
		if lc.nodeID == "b" {
			bAdmitido = true
			if lc.toolID != "" {
				t.Errorf("papel 'b' devia ser admitido SEM tool (placeholder), got toolID=%q", lc.toolID)
			}
		}
	}
	if !bAdmitido {
		t.Errorf("papel condicional 'b' não foi admitido no DAG: %+v", h.lf.calls)
	}
}
