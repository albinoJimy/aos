package runlifecycle_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planmaterialize"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/substrate/eventstore"
)

// aos476_arestas_fenced_test.go — as ARESTAS que a materialização escreve (AOS-476) passam pelo
// fencing da posse, como os nós (ADR-023 §2.4). A guarda estática apanha uma chamada directa ao
// store; isto apanha o comportamento: um dono superado não consegue pôr uma aresta no log, nem
// pelo seu GraphBuilder nem pela materialização.

// TestAOS476_ArestaDeDonoSuperadoRecusada: A admite os dois nós enquanto é dono; B supera-o; A,
// com o seu GraphBuilder em mãos, tenta escrever a aresta — directamente e pela materialização
// retomada (os nós já lá estão e coincidem, pelo que a primeira escrita da materialização é a
// aresta). Nenhuma das duas chega ao log.
func TestAOS476_ArestaDeDonoSuperadoRecusada(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	clk := newClock()
	const runID = "run-aresta-fenced"

	a := replica(t, store, clk, "proc-a")
	ta, err := runlifecycle.Claim(ctx, store, a, runID)
	if err != nil {
		t.Fatalf("claim de A: %v", err)
	}
	ga, err := ta.Graph(ctx, eventstore.Producer{})
	if err != nil {
		t.Fatalf("Graph de A: %v", err)
	}
	// Os nós com a especificação que a materialização escreveria: `recolha` tem um dependente
	// (papel, sem tool); `analise` é folha com a sua tool.
	if err := ga.AddNode(ctx, orchestrator.NodeSpec{TaskID: "recolha"}); err != nil {
		t.Fatalf("AddNode recolha: %v", err)
	}
	analise := orchestrator.NodeSpec{TaskID: "analise"}
	analise.Task.ToolID = "fs.read"
	analise.Task.Capability = planmaterialize.DefaultCapabilityMapper(plan.ToolRef{Name: "fs.read"})
	if err := ga.AddNode(ctx, analise); err != nil {
		t.Fatalf("AddNode analise: %v", err)
	}

	clk.advance(testTTL + 1)
	b := replica(t, store, clk, "proc-b")
	if _, err := runlifecycle.Claim(ctx, store, b, runID); err != nil {
		t.Fatalf("claim de B: %v", err)
	}

	before := streamLen(t, store, runID)
	if err := ga.AddEdge(ctx, "recolha", "analise"); err == nil {
		t.Fatal("o dono SUPERADO escreveu uma aresta pelo seu GraphBuilder — task.edge.added escapa ao fencing")
	}
	if after := streamLen(t, store, runID); after != before {
		t.Fatalf("o stream cresceu de %d para %d — a aresta do dono superado CHEGOU ao log", before, after)
	}

	rec := &gravadorEmMemoria{}
	m, err := planmaterialize.NewMaterializer(admissaoSempreOK{}, planmaterialize.NewGraphLeafAdmitter(ga), rec)
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}
	tool := plan.ToolRef{Name: "fs.read", Version: "1.0.0", Digest: "sha256:aaa"}
	_, err = m.Materialize(ctx, planmaterialize.Request{
		RunID: runID, PlanID: runID + "-plan",
		Doc: plan.PlanDocument{Objective: "o", Nodes: []plan.Node{
			{NodeID: "recolha", Role: "worker", Objective: "r", Tools: []plan.ToolRef{tool}},
			{NodeID: "analise", Role: "worker", Objective: "a", Tools: []plan.ToolRef{tool}, DependsOn: []string{"recolha"}},
		}},
	})
	if err == nil {
		t.Fatal("a materialização de um dono SUPERADO passou — a aresta escapou ao fencing")
	}
	// A recusa tem de vir da ARESTA: os nós já duráveis coincidem e são aceites sem escrita, pelo
	// que é o task.edge.added a primeira escrita que o fencing apanha.
	if !strings.Contains(err.Error(), "admitir aresta recolha→analise") {
		t.Fatalf("a materialização falhou antes da aresta, o teste não prova o fencing dela: %v", err)
	}
	if after := streamLen(t, store, runID); after != before {
		t.Fatalf("o stream cresceu de %d para %d pela materialização do dono superado", before, after)
	}
	if rec.ultimo.PlanID != "" {
		t.Fatalf("plan.materialized apenso por um dono superado: %+v", rec.ultimo)
	}
}
