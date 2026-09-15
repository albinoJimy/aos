package planmaterialize

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/substrate/eventstore"
)

// materialize_edges_test.go — as arestas do plano aprovado ficam DURÁVEIS no DAG.
//
// Medido no E2E manual de 2026-09-15: o `--goal` materializava `recolha` e `analise`
// (`analise` depende de `recolha`) e o log ficava com dois `task.node.created` e ZERO
// `task.edge.added`. O despacho respeitava a dependência só porque a lia do documento EM
// MEMÓRIA; o grafo durável — a única topologia que um dono seguinte re-hidrata, e aquela
// que o contrato declara como a fonte das dependências (`TaskNodeCreatedPayload` não tem
// `Deps` de propósito) — dizia que os dois nós eram independentes, e o `inspect`
// imprimia `ordem=analise,recolha`.

// TestArestasDoPlanoAdmitidasNoDAG: cada aresta de entrada (depends_on E conditional_on)
// é admitida DEPOIS de todos os nós, em ordem canónica, sem duplicados.
func TestArestasDoPlanoAdmitidasNoDAG(t *testing.T) {
	h := newHarness(t, nil)
	c := node("c", []plan.ToolRef{tool("t")}, "b", "a", "a") // dep duplicada de propósito
	c.ConditionalOn = []plan.ConditionalEdge{condEdge("d")}
	req := baseReq(
		c,
		node("b", []plan.ToolRef{tool("t")}, "a"),
		node("a", []plan.ToolRef{tool("t")}),
		node("d", []plan.ToolRef{tool("t")}),
	)
	if _, err := h.m.Materialize(context.Background(), req); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := []string{
		"node:a", "node:b", "node:c", "node:d",
		"edge:a->b",
		"edge:a->c", "edge:b->c", "edge:d->c",
	}
	if !reflect.DeepEqual(h.lf.ops, want) {
		t.Fatalf("admissões no DAG = %v\nquer %v\n(sem as arestas, o grafo durável diz que os nós são independentes)", h.lf.ops, want)
	}
}

// TestArestaQueFechaCicloAbortaSemEfeito: um documento cíclico que chegue por outra
// porta (replan, migração, edição no gate — a admissão AOS-231 já o recusa) aborta ANTES
// da admissão global e de qualquer nó, com o sentinela do DAG preservado.
func TestArestaQueFechaCicloAbortaSemEfeito(t *testing.T) {
	h := newHarness(t, nil)
	req := baseReq(
		node("a", []plan.ToolRef{tool("t")}, "b"),
		node("b", []plan.ToolRef{tool("t")}, "a"),
	)
	_, err := h.m.Materialize(context.Background(), req)
	if !errors.Is(err, ErrInvalidRequest) || !errors.Is(err, orchestrator.ErrEdgeClosesCycle) {
		t.Fatalf("plano cíclico devia dar ErrInvalidRequest+ErrEdgeClosesCycle, got %v", err)
	}
	if len(h.adm.calls) != 0 || len(h.lf.ops) != 0 || len(h.rec.payloads) != 0 {
		t.Fatalf("efeito parcial num plano cíclico: admissões=%v dag=%v rec=%d", h.adm.calls, h.lf.ops, len(h.rec.payloads))
	}
}

// TestArestaParaForaDoPlanoAbortaSemEfeito: uma dependência sobre um nó que o documento
// não tem não se admite como «nó independente» — aborta sem efeito.
func TestArestaParaForaDoPlanoAbortaSemEfeito(t *testing.T) {
	h := newHarness(t, nil)
	req := baseReq(node("a", []plan.ToolRef{tool("t")}, "fantasma"))
	if _, err := h.m.Materialize(context.Background(), req); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("dependência fora do plano devia dar ErrInvalidRequest, got %v", err)
	}
	if len(h.adm.calls) != 0 || len(h.lf.ops) != 0 || len(h.rec.payloads) != 0 {
		t.Fatalf("efeito parcial: admissões=%v dag=%v rec=%d", h.adm.calls, h.lf.ops, len(h.rec.payloads))
	}
}

// TestArestasSobrevivemAoReplay: pelo adaptador de produção sobre um Event Store REAL, o
// DAG que [orchestrator.RebuildDAG] reconstrói tem a dependência e ordena-a — é o grafo
// que um segundo dono re-hidrata.
func TestArestasSobrevivemAoReplay(t *testing.T) {
	ctx := context.Background()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	g, err := orchestrator.NewGraphBuilder(es, "run-1", eventstore.Producer{NHIID: "nhi:test"})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	m, err := NewMaterializer(&fakeAdmission{}, NewGraphLeafAdmitter(g), &fakeRecorder{})
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}
	req := baseReq(
		node("recolha", []plan.ToolRef{tool("fs.read")}),
		node("analise", []plan.ToolRef{tool("fs.read")}, "recolha"),
	)
	if _, err := m.Materialize(ctx, req); err != nil {
		t.Fatalf("Materialize: %v", err)
	}

	dag, err := orchestrator.RebuildDAG(ctx, es, "run-1")
	if err != nil {
		t.Fatalf("RebuildDAG: %v", err)
	}
	if !dag.HasEdge("recolha", "analise") {
		t.Fatal("o DAG re-hidratado não tem recolha→analise — a dependência só existia em memória")
	}
	ordem, err := dag.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder: %v", err)
	}
	if want := []string{"recolha", "analise"}; !reflect.DeepEqual(ordem, want) {
		t.Fatalf("ordem re-hidratada = %v, quer %v", ordem, want)
	}
}
