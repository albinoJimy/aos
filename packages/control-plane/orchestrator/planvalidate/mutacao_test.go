package planvalidate

import (
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// mutacao_test.go — O 4.º EIXO NO RISCO (AOS-409, decisão R1). O critério de efeito
// (V3/P4) prova-se na tabela de [TestIsEffectToolCriterion] e nos testes de (V3) e (P4);
// aqui prova-se a outra metade da «uma definição, duas perguntas»: a regra 6.

// mutatorCap é uma capability PINADA que escreve localmente e se desfaz — `EgressNone` +
// `Reversible` + [MutationMutates]. Antes do AOS-409 derivava `safe` (auto-aprovável).
func mutatorCap(name string) Capability {
	c := safeCap(name)
	c.Mutation = MutationMutates
	return c
}

// TestMutadorReversivelSemEgressDerivaDanger — R1: uma tool que muta estado é
// apresentada ao classificador SA-ROC como IRREVERSÍVEL, e o nó deriva `danger` com
// approval-card humano. Passa pelo ponto de entrada que o `aos-orq` usa ([ResolveRisks]).
//
// FALHA-ANTES: o nó derivava `safe` — os três eixos do classificador eram os de uma
// leitura — e o gate do AOS-408 auto-aprovava uma escrita sem humano nenhum.
func TestMutadorReversivelSemEgressDerivaDanger(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "grava", Role: "r", Objective: "o",
			Tools:     []plan.ToolRef{{Name: "edit", Version: "1.0.0", Digest: "sha256:edit"}},
			RiskClass: plan.RiskSafe, // o rótulo do LLM não baixa o piso
		},
	}
	snap := Snapshot{Hash: capHash, Tools: []Capability{mutatorCap("edit")}}

	nr := ResolveRisks(doc, snap, nil)["grava"]
	if nr.Derived != plan.RiskDanger || nr.Resolved != plan.RiskDanger {
		t.Fatalf("tool mutadora (sem egress, reversível) devia derivar danger, veio derived=%q resolved=%q", nr.Derived, nr.Resolved)
	}
	if nr.AutoApprovable() {
		t.Fatal("um nó que escreve não pode ser auto-aprovável — tem de chegar ao humano com approval-card")
	}
	if !nr.Classification.Irreversible() {
		t.Fatal("R1: a mutação entra no classificador como irreversível")
	}
	if nr.Classification.Egress != risk.EgressNone {
		t.Fatalf("a mutação não pode inventar egress: veio %v", nr.Classification.Egress)
	}
}

// TestMutacaoPorDeclararDerivaDanger — o valor-zero do eixo (um literal Go que não o
// declara) é mutador: fail-closed pelo tipo, sem uma linha para isso.
func TestMutacaoPorDeclararDerivaDanger(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "n", Role: "r", Objective: "o",
			Tools: []plan.ToolRef{{Name: "x", Version: "1.0.0", Digest: "sha256:x"}}},
	}
	c := safeCap("x")
	c.Mutation = MutationUnknown
	nr := ResolveRisks(doc, Snapshot{Hash: capHash, Tools: []Capability{c}}, nil)["n"]
	if nr.Resolved != plan.RiskDanger {
		t.Fatalf("mutação por declarar devia derivar danger, veio %q", nr.Resolved)
	}
}

// TestLeituraDeclaradaContinuaSafe — NÃO-VACUIDADE: a leitura declarada (`MutationNone`)
// continua `safe`. Se o eixo tornasse tudo `danger`, o gate pedia humano para ler.
func TestLeituraDeclaradaContinuaSafe(t *testing.T) {
	doc := baseDoc()
	doc.Nodes = []plan.Node{
		{NodeID: "le", Role: "r", Objective: "o",
			Tools: []plan.ToolRef{{Name: "read", Version: "1.0.0", Digest: "sha256:read"}}},
	}
	nr := ResolveRisks(doc, Snapshot{Hash: capHash, Tools: []Capability{safeCap("read")}}, nil)["le"]
	if nr.Resolved != plan.RiskSafe || !nr.AutoApprovable() {
		t.Fatalf("leitura declarada devia derivar safe auto-aprovável, veio %q", nr.Resolved)
	}
}

// TestMutationString — o vocabulário do eixo é o do ficheiro do snapshot pinado.
func TestMutationString(t *testing.T) {
	for m, want := range map[Mutation]string{
		MutationNone: "none", MutationMutates: "mutates", MutationUnknown: "unknown", Mutation(99): "unknown",
	} {
		if got := m.String(); got != want {
			t.Fatalf("Mutation(%d).String() = %q; queria %q", m, got, want)
		}
	}
}
