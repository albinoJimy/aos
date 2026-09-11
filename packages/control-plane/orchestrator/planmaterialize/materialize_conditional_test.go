package planmaterialize

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// condEdge devolve a aresta condicional `verdict eq fail` sobre `from` — a forma do
// ramo de recuperação de ADR-022 §2.1 (o nó só despacha se a origem REPROVAR).
func condEdge(from string) plan.ConditionalEdge {
	return plan.ConditionalEdge{
		From: from,
		When: []plan.Predicate{{Subject: plan.SubjectVerdict, Op: plan.OpEq, Enum: plan.EnumFail}},
	}
}

// TestAOS389_PlanoCondicionalRecusadoSemEfeito sela o guard fail-closed: um plano
// APROVADO com uma aresta `conditional_on` (o cenário medido em 2026-09-10 — `B`
// condicional sobre `A`) é RECUSADO com [ErrConditionalNaoComposto] ANTES de qualquer
// admissão ou efeito. Reproduz o defeito fail-OPEN que o guard fecha: antes de AOS-389,
// `B` era materializado/spawnado sem a condição ser avaliada.
//
// A asserção central é a AUSÊNCIA de efeito: nenhuma chamada a Admit/AdmitLeaf/Spawn e
// nenhum `plan.materialized`. É o sensor de regressão — se algum caminho de
// materialização voltar a dar efeito a um nó condicional, este teste cai.
//
// SUPERADO POR AOS-390: quando o avaliador de ramos estiver composto, este teste
// inverte-se para assertar que `A` passa ⇒ `B` é podado (`branch_not_taken`), em vez de
// recusa do plano inteiro.
func TestAOS389_PlanoCondicionalRecusadoSemEfeito(t *testing.T) {
	h := newHarness(t, nil)

	a := node("a", []plan.ToolRef{tool("t")}) // produtor observado pela condição
	b := plan.Node{
		NodeID:        "b",
		Role:          "role-b",
		Objective:     "obj-b",
		Tools:         []plan.ToolRef{tool("t")},
		ConditionalOn: []plan.ConditionalEdge{condEdge("a")},
	}

	_, err := h.m.Materialize(context.Background(), baseReq(a, b))
	if !errors.Is(err, ErrConditionalNaoComposto) {
		t.Fatalf("Materialize err = %v; queria ErrConditionalNaoComposto", err)
	}

	// Fail-closed ANTES de qualquer efeito: nada admitido, nada materializado.
	if len(h.adm.calls) != 0 {
		t.Errorf("admissão chamada %d vez(es); esperado 0 (recusa antes da FASE 1): %v", len(h.adm.calls), h.adm.calls)
	}
	if len(h.lf.calls) != 0 {
		t.Errorf("AdmitLeaf chamada %d vez(es); esperado 0 (nenhum nó-folha materializado): %v", len(h.lf.calls), h.lf.calls)
	}
	if len(h.sp.calls) != 0 {
		t.Errorf("Spawn chamada %d vez(es); esperado 0 (nenhum papel spawnado): %v", len(h.sp.calls), h.sp.calls)
	}
	if len(h.rec.payloads) != 0 {
		t.Errorf("plan.materialized apenso %d vez(es); esperado 0", len(h.rec.payloads))
	}
}

// TestAOS389_CondicionalNumNoQualquerRecusa garante que o guard olha para TODOS os nós,
// não só o primeiro em ordem canónica: um plano cujo ÚNICO nó condicional é o último por
// node_id é recusado na mesma.
func TestAOS389_CondicionalNumNoQualquerRecusa(t *testing.T) {
	h := newHarness(t, nil)

	a := node("a", []plan.ToolRef{tool("t")})
	z := plan.Node{
		NodeID:        "z",
		Role:          "role-z",
		Objective:     "obj-z",
		Tools:         []plan.ToolRef{tool("t")},
		ConditionalOn: []plan.ConditionalEdge{condEdge("a")},
	}

	if _, err := h.m.Materialize(context.Background(), baseReq(a, z)); !errors.Is(err, ErrConditionalNaoComposto) {
		t.Fatalf("Materialize err = %v; queria ErrConditionalNaoComposto", err)
	}
	if len(h.adm.calls)+len(h.lf.calls)+len(h.sp.calls)+len(h.rec.payloads) != 0 {
		t.Errorf("houve efeito num plano condicional recusado (adm=%d leaf=%d spawn=%d rec=%d)",
			len(h.adm.calls), len(h.lf.calls), len(h.sp.calls), len(h.rec.payloads))
	}
}

// TestAOS389_NoCondicionalPapelNaoSpawna reproduz a forma MAIS AGUDA da violação medida:
// um nó condicional que o [DefaultClassifier] tornaria PAPEL-QUE-EXPANDE (porque outro nó
// dele depende) seria, sem o guard, spawnado como sub-agente real via [Spawner.Spawn]
// (Delegator.Spawn) — efeito imediato e irreversível — sem a condição ser avaliada. O
// guard corre ANTES da classificação, pelo que o plano é recusado e nenhum Spawn ocorre.
func TestAOS389_NoCondicionalPapelNaoSpawna(t *testing.T) {
	h := newHarness(t, nil)

	a := node("a", []plan.ToolRef{tool("t")})
	// `b` é condicional sobre `a` E tem um dependente (`c`) ⇒ DefaultClassifier di-lo PAPEL.
	b := plan.Node{
		NodeID:        "b",
		Role:          "role-b",
		Objective:     "obj-b",
		Tools:         []plan.ToolRef{tool("t")},
		ConditionalOn: []plan.ConditionalEdge{condEdge("a")},
	}
	c := node("c", []plan.ToolRef{tool("t")}, "b") // depende de b ⇒ b é papel-que-expande

	if _, err := h.m.Materialize(context.Background(), baseReq(a, b, c)); !errors.Is(err, ErrConditionalNaoComposto) {
		t.Fatalf("Materialize err = %v; queria ErrConditionalNaoComposto", err)
	}
	if len(h.sp.calls) != 0 {
		t.Fatalf("Spawn (Delegator) chamado %d vez(es) para um nó condicional-papel; esperado 0: %v", len(h.sp.calls), h.sp.calls)
	}
	if len(h.adm.calls)+len(h.lf.calls)+len(h.rec.payloads) != 0 {
		t.Errorf("houve outro efeito (adm=%d leaf=%d rec=%d)", len(h.adm.calls), len(h.lf.calls), len(h.rec.payloads))
	}
}

// TestAOS389_PlanoSemCondicionaisMaterializaComoAntes é a não-regressão: um plano com
// `depends_on` mas SEM `conditional_on` continua a materializar exactamente como antes do
// guard — o guard distingue, não rejeita tudo.
func TestAOS389_PlanoSemCondicionaisMaterializaComoAntes(t *testing.T) {
	h := newHarness(t, nil)

	// a (papel, tem dependente) → b (folha). Nenhuma aresta condicional.
	a := node("a", []plan.ToolRef{tool("t")})
	b := node("b", []plan.ToolRef{tool("t")}, "a")

	if _, err := h.m.Materialize(context.Background(), baseReq(a, b)); err != nil {
		t.Fatalf("Materialize de plano não-condicional falhou: %v", err)
	}
	if len(h.adm.calls) != 2 {
		t.Errorf("admissão chamada %d vez(es); esperado 2 (ambos os nós admitidos)", len(h.adm.calls))
	}
	if len(h.rec.payloads) != 1 {
		t.Errorf("plan.materialized apenso %d vez(es); esperado 1", len(h.rec.payloads))
	}
}
