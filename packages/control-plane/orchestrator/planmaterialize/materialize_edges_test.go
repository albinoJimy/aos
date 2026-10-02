package planmaterialize

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/contract"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/substrate/eventstore"
)

// materialize_edges_test.go — as arestas do plano aprovado ficam DURÁVEIS no DAG (AOS-476).
//
// Medido três vezes (2026-09-15 local, 2026-10-01 local e no `consume.wal` de produção): a
// materialização deixava `task.node.created` por nó e ZERO `task.edge.added`. O despacho
// respeitava `depends_on` só porque o lia do documento EM MEMÓRIA; o grafo durável — a
// única topologia que um dono seguinte re-hidrata, e aquela que o contrato declara como a
// fonte das dependências (`TaskNodeCreatedPayload` não tem `Deps` de propósito) — dizia
// que os nós eram independentes, e o `inspect` imprimia `ordem=analise,recolha`.

// recorderNoMesmoLog apensa o marcador "materialized" ao MESMO registo de ordem que o
// [fakeLeaf] usa, para se poder afirmar a ordem relativa nós → arestas → plan.materialized.
type recorderNoMesmoLog struct{ lf *fakeLeaf }

func (r recorderNoMesmoLog) RecordMaterialized(context.Context, plannerevents.MaterializedPayload) (uint64, error) {
	r.lf.ops = append(r.lf.ops, "materialized")
	return 1, nil
}

// TestAOS476_ArestasDepoisDosNosAntesDoMaterialized: cada aresta de entrada (depends_on E
// origem de conditional_on) é admitida DEPOIS de todos os nós, em ordem canónica e sem
// duplicados, e ANTES de `plan.materialized`.
func TestAOS476_ArestasDepoisDosNosAntesDoMaterialized(t *testing.T) {
	lf := &fakeLeaf{}
	m, err := NewMaterializer(&fakeAdmission{}, lf, recorderNoMesmoLog{lf: lf})
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}
	c := node("c", []plan.ToolRef{tool("t")}, "b", "a", "a") // dependência duplicada de propósito
	c.ConditionalOn = []plan.ConditionalEdge{condEdge("d")}
	req := baseReq(
		c,
		node("b", []plan.ToolRef{tool("t")}, "a"),
		node("a", []plan.ToolRef{tool("t")}),
		node("d", []plan.ToolRef{tool("t")}),
	)
	if _, err := m.Materialize(context.Background(), req); err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	want := []string{
		"node:a", "node:b", "node:c", "node:d",
		"edge:a->b",
		"edge:a->c", "edge:b->c", "edge:d->c",
		"materialized",
	}
	if !reflect.DeepEqual(lf.ops, want) {
		t.Fatalf("admissões no DAG = %v\nquer %v\n(sem as arestas, o grafo durável diz que os nós são independentes)", lf.ops, want)
	}
}

// TestAOS476_CicloAbortaSemNenhumNo: um documento cíclico que chegue por outra porta
// (replan, migração, edição no gate — a admissão AOS-231 já o recusa) aborta ANTES da
// admissão global e de qualquer nó, com o sentinela do DAG preservado.
func TestAOS476_CicloAbortaSemNenhumNo(t *testing.T) {
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

// TestAOS476_OrigemForaDoPlanoAbortaSemNenhumNo: uma dependência — por `depends_on` ou por
// `conditional_on` — sobre um nó que o documento não tem não se admite como «nó
// independente»: aborta sem escrever nada.
func TestAOS476_OrigemForaDoPlanoAbortaSemNenhumNo(t *testing.T) {
	condicional := node("a", []plan.ToolRef{tool("t")})
	condicional.ConditionalOn = []plan.ConditionalEdge{condEdge("fantasma")}
	for nome, n := range map[string]plan.Node{
		"depends_on":     node("a", []plan.ToolRef{tool("t")}, "fantasma"),
		"conditional_on": condicional,
	} {
		t.Run(nome, func(t *testing.T) {
			h := newHarness(t, nil)
			_, err := h.m.Materialize(context.Background(), baseReq(n, node("b", []plan.ToolRef{tool("t")})))
			if !errors.Is(err, ErrInvalidRequest) || !errors.Is(err, orchestrator.ErrNodeNotFound) {
				t.Fatalf("origem fora do plano devia dar ErrInvalidRequest+ErrNodeNotFound, got %v", err)
			}
			if len(h.adm.calls) != 0 || len(h.lf.ops) != 0 || len(h.rec.payloads) != 0 {
				t.Fatalf("efeito parcial: admissões=%v dag=%v rec=%d", h.adm.calls, h.lf.ops, len(h.rec.payloads))
			}
		})
	}
}

// arestaRecusada recusa a admissão de qualquer aresta — simula a porta de produção a
// recusar (ciclo contra o grafo durável, posse perdida, store fechado).
type arestaRecusada struct{ fakeLeaf }

var errArestaRecusada = errors.New("aresta recusada pela porta")

func (*arestaRecusada) AdmitEdge(context.Context, string, string) error { return errArestaRecusada }

// TestAOS476_ArestaRecusadaNaoApensaMaterialized: se a porta recusa uma aresta, o
// `plan.materialized` NÃO é apenso — o plano não se declara materializado com uma
// topologia que o log não tem.
func TestAOS476_ArestaRecusadaNaoApensaMaterialized(t *testing.T) {
	lf := &arestaRecusada{}
	rec := &fakeRecorder{}
	m, err := NewMaterializer(&fakeAdmission{}, lf, rec)
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}
	req := baseReq(
		node("recolha", []plan.ToolRef{tool("t")}),
		node("analise", []plan.ToolRef{tool("t")}, "recolha"),
	)
	if _, err := m.Materialize(context.Background(), req); !errors.Is(err, errArestaRecusada) {
		t.Fatalf("a recusa da aresta devia propagar, got %v", err)
	}
	if len(rec.payloads) != 0 {
		t.Fatalf("plan.materialized apenso apesar da aresta recusada: %+v", rec.payloads)
	}
}

// recorderQueContaArestas, no momento em que lhe pedem `plan.materialized`, conta as
// `task.edge.added` já DURÁVEIS no stream do run — a ordem verifica-se no log, não num
// registo do teste.
type recorderQueContaArestas struct {
	es           *eventstore.Store
	runID        string
	arestasAntes int
}

func (r *recorderQueContaArestas) RecordMaterialized(ctx context.Context, _ plannerevents.MaterializedPayload) (uint64, error) {
	evs, err := r.es.Read(ctx, r.runID, 1)
	if err != nil {
		return 0, err
	}
	for _, e := range evs {
		if e.Type == contract.EventTaskEdgeAdded {
			r.arestasAntes++
		}
	}
	return 1, nil
}

// TestAOS476_ArestasSobrevivemAoReplay: pelo adaptador de produção sobre um Event Store
// REAL, a aresta está durável ANTES de `plan.materialized`, e o DAG que
// [orchestrator.RebuildDAG] reconstrói tem a dependência e ordena-a — é o grafo que um
// segundo dono re-hidrata.
func TestAOS476_ArestasSobrevivemAoReplay(t *testing.T) {
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
	rec := &recorderQueContaArestas{es: es, runID: "run-1"}
	m, err := NewMaterializer(&fakeAdmission{}, NewGraphLeafAdmitter(g), rec)
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
	if rec.arestasAntes != 1 {
		t.Fatalf("task.edge.added duráveis quando plan.materialized foi pedido = %d, quer 1", rec.arestasAntes)
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
