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
	"github.com/aos-ref/kernel/agent-runtime/state"
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

// storeQueRecusa recusa o Append dos tipos de evento dados — simula a morte do processo (ou a
// perda da posse) a meio da materialização, depois de parte das escritas ficar durável.
type storeQueRecusa struct {
	*eventstore.Store
	recusar map[string]bool
}

var errAppendRecusado = errors.New("append recusado pelo store")

func (s storeQueRecusa) Append(ctx context.Context, stream string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if s.recusar[in.Type] {
		return eventstore.AppendResult{}, errAppendRecusado
	}
	return s.Store.Append(ctx, stream, in, opts...)
}

// contarTipos conta os eventos de cada tipo no stream do run.
func contarTipos(t *testing.T, es *eventstore.Store, runID string) map[string]int {
	t.Helper()
	evs, err := es.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	n := map[string]int{}
	for _, e := range evs {
		n[e.Type]++
	}
	return n
}

func planoRecolhaAnalise() Request {
	return baseReq(
		node("recolha", []plan.ToolRef{tool("fs.read")}),
		node("analise", []plan.ToolRef{tool("fs.read")}, "recolha"),
	)
}

func novoES(t *testing.T) *eventstore.Store {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	return es
}

func builderDoLog(t *testing.T, es *eventstore.Store, nhi string) *orchestrator.GraphBuilder {
	t.Helper()
	g, err := orchestrator.NewGraphBuilderFromLog(context.Background(), es, "run-1", eventstore.Producer{NHIID: nhi})
	if err != nil {
		t.Fatalf("NewGraphBuilderFromLog: %v", err)
	}
	return g
}

func materializador(t *testing.T, g *orchestrator.GraphBuilder, rec MaterializeRecorder) *Materializer {
	t.Helper()
	m, err := NewMaterializer(&fakeAdmission{}, NewGraphLeafAdmitter(g), rec)
	if err != nil {
		t.Fatalf("NewMaterializer: %v", err)
	}
	return m
}

// TestAOS476_ArestaRecusadaPeloStorePropaga (B2 da revisão): pelo adaptador de PRODUÇÃO, um
// store que recusa o `task.edge.added` faz a materialização falhar com esse erro e sem
// `plan.materialized` — o adaptador não pode engolir a recusa.
func TestAOS476_ArestaRecusadaPeloStorePropaga(t *testing.T) {
	es := novoES(t)
	st := storeQueRecusa{Store: es, recusar: map[string]bool{contract.EventTaskEdgeAdded: true}}
	g, err := orchestrator.NewGraphBuilder(st, "run-1", eventstore.Producer{NHIID: "nhi:test"})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	rec := &fakeRecorder{}
	if _, err := materializador(t, g, rec).Materialize(context.Background(), planoRecolhaAnalise()); !errors.Is(err, errAppendRecusado) {
		t.Fatalf("a recusa do store tinha de propagar, got %v", err)
	}
	if len(rec.payloads) != 0 {
		t.Fatalf("plan.materialized apenso com a aresta recusada: %+v", rec.payloads)
	}
}

// TestAOS476_MaterializacaoRetomaDepoisDosNos (MÉDIO-1 da revisão): a primeira tentativa morre
// depois dos `task.node.created` e antes da aresta e do `plan.materialized`. Um dono seguinte,
// com o grafo RE-HIDRATADO, volta a materializar: os nós que coincidem são aceites sem segunda
// escrita, a aresta é escrita e o `plan.materialized` é apenso. Antes, a readmissão do primeiro
// nó falhava com «nó já existe no grafo» e o run ficava irrecuperável.
func TestAOS476_MaterializacaoRetomaDepoisDosNos(t *testing.T) {
	ctx := context.Background()
	es := novoES(t)
	g1, err := orchestrator.NewGraphBuilder(storeQueRecusa{Store: es, recusar: map[string]bool{contract.EventTaskEdgeAdded: true}},
		"run-1", eventstore.Producer{NHIID: "nhi:p1"})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	if _, err := materializador(t, g1, &fakeRecorder{}).Materialize(ctx, planoRecolhaAnalise()); err == nil {
		t.Fatal("a primeira tentativa tinha de morrer na aresta")
	}
	if n := contarTipos(t, es, "run-1"); n[contract.EventTaskNodeCreated] != 2 || n[contract.EventTaskEdgeAdded] != 0 {
		t.Fatalf("estado depois da morte = %v, quer 2 nós e 0 arestas", n)
	}

	rec := &fakeRecorder{}
	if _, err := materializador(t, builderDoLog(t, es, "nhi:p2"), rec).Materialize(ctx, planoRecolhaAnalise()); err != nil {
		t.Fatalf("a retoma da materialização falhou: %v", err)
	}
	if n := contarTipos(t, es, "run-1"); n[contract.EventTaskNodeCreated] != 2 || n[contract.EventTaskEdgeAdded] != 1 {
		t.Fatalf("depois da retoma = %v, quer 2 nós (sem reescrita) e 1 aresta", n)
	}
	if len(rec.payloads) != 1 {
		t.Fatalf("plan.materialized apenso %d vezes na retoma, quer 1", len(rec.payloads))
	}

	// Uma terceira passagem, com a aresta já durável, também passa e não a reescreve.
	if _, err := materializador(t, builderDoLog(t, es, "nhi:p3"), &fakeRecorder{}).Materialize(ctx, planoRecolhaAnalise()); err != nil {
		t.Fatalf("a retoma com a aresta já durável falhou: %v", err)
	}
	if n := contarTipos(t, es, "run-1"); n[contract.EventTaskEdgeAdded] != 1 {
		t.Fatalf("a aresta já durável foi reescrita: %v", n)
	}
}

// TestAOS476_RetomaComNoDivergenteRecusa: um nó já durável com outra tool call não é o nó do
// plano. A retoma recusa com [ErrNodeDiverges], sem escrever nada e sem `plan.materialized`.
func TestAOS476_RetomaComNoDivergenteRecusa(t *testing.T) {
	ctx := context.Background()
	es := novoES(t)
	outro := orchestrator.NodeSpec{TaskID: "analise"}
	outro.Task.ToolID = "http.post"
	outro.Task.Capability = "cap:tool:http.post"
	if err := builderDoLog(t, es, "nhi:p1").AddNode(ctx, outro); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	antes := contarTipos(t, es, "run-1")

	rec := &fakeRecorder{}
	_, err := materializador(t, builderDoLog(t, es, "nhi:p2"), rec).Materialize(ctx, planoRecolhaAnalise())
	if !errors.Is(err, ErrNodeDiverges) {
		t.Fatalf("nó divergente devia dar ErrNodeDiverges, got %v", err)
	}
	if depois := contarTipos(t, es, "run-1"); !reflect.DeepEqual(antes, depois) {
		t.Fatalf("a retoma recusada escreveu: antes=%v depois=%v", antes, depois)
	}
	if len(rec.payloads) != 0 {
		t.Fatalf("plan.materialized apenso sobre um nó divergente: %+v", rec.payloads)
	}
}

// TestAOS476_RetomaComNoJaEmCursoRecusa: sem `plan.materialized` nada pode ter despachado um
// nó. Um nó do plano que já não está `ready` não se aceita como «o mesmo nó por admitir».
func TestAOS476_RetomaComNoJaEmCursoRecusa(t *testing.T) {
	ctx := context.Background()
	es := novoES(t)
	g1 := builderDoLog(t, es, "nhi:p1")
	// `recolha` tem um dependente no plano ⇒ papel, admitido sem tool: a especificação coincide.
	if err := g1.AddNode(ctx, orchestrator.NodeSpec{TaskID: "recolha"}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if err := g1.MarkRunning(ctx, "recolha"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if _, err := materializador(t, builderDoLog(t, es, "nhi:p2"), &fakeRecorder{}).Materialize(ctx, planoRecolhaAnalise()); !errors.Is(err, ErrNodeDiverges) {
		t.Fatalf("nó já em curso devia dar ErrNodeDiverges, got %v", err)
	}
}

// TestAOS476_RetomaSobreGrafoQueNaoEODoPlanoRecusa (revisão da ronda 2, B2 e B4): cada forma de
// o grafo durável não ser o do plano recusa ANTES de escrever — nem nó, nem aresta, nem
// `task.edge.rejected_cycle`, nem `plan.materialized`. Os três primeiros casos são campos que o
// `task.node.created` transporta e que uma comparação só da tool deixaria passar; o quarto é um
// estado terminal (não só `running`); os dois últimos são topologia que o plano não declara.
func TestAOS476_RetomaSobreGrafoQueNaoEODoPlanoRecusa(t *testing.T) {
	recolha := orchestrator.NodeSpec{TaskID: "recolha"} // papel do plano: sem tool
	casos := []struct {
		nome   string
		quer   error
		semear func(t *testing.T, g *orchestrator.GraphBuilder)
	}{
		{"prioridade", ErrNodeDiverges, func(t *testing.T, g *orchestrator.GraphBuilder) {
			n := recolha
			n.Priority = 7
			deve(t, g.AddNode(context.Background(), n))
		}},
		{"identidade", ErrNodeDiverges, func(t *testing.T, g *orchestrator.GraphBuilder) {
			n := recolha
			n.Agent = contract.AgentIdentity{NHIID: "nhi:outro"}
			deve(t, g.AddNode(context.Background(), n))
		}},
		{"capability", ErrNodeDiverges, func(t *testing.T, g *orchestrator.GraphBuilder) {
			// a MESMA tool, com outra capability: a autoridade da folha não é a do plano.
			n := orchestrator.NodeSpec{TaskID: "analise"}
			n.Task.ToolID = "fs.read"
			n.Task.Capability = "cap:tool:http.post"
			deve(t, g.AddNode(context.Background(), n))
		}},
		{"terminal", ErrNodeDiverges, func(t *testing.T, g *orchestrator.GraphBuilder) {
			deve(t, g.AddNode(context.Background(), recolha))
			deve(t, g.MarkRunning(context.Background(), "recolha"))
			deve(t, g.MarkTerminal(context.Background(), "recolha", state.Complete))
		}},
		{"no-a-mais", ErrNodeDiverges, func(t *testing.T, g *orchestrator.GraphBuilder) {
			deve(t, g.AddNode(context.Background(), recolha))
			deve(t, g.AddNode(context.Background(), orchestrator.NodeSpec{TaskID: "fantasma"}))
		}},
		{"aresta-invertida", ErrEdgeDiverges, func(t *testing.T, g *orchestrator.GraphBuilder) {
			analise := orchestrator.NodeSpec{TaskID: "analise"}
			analise.Task.ToolID = "fs.read"
			analise.Task.Capability = DefaultCapabilityMapper(tool("fs.read"))
			deve(t, g.AddNode(context.Background(), analise))
			deve(t, g.AddNode(context.Background(), recolha))
			deve(t, g.AddEdge(context.Background(), "analise", "recolha"))
		}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			es := novoES(t)
			c.semear(t, builderDoLog(t, es, "nhi:p1"))
			antes := contarTipos(t, es, "run-1")
			rec := &fakeRecorder{}
			_, err := materializador(t, builderDoLog(t, es, "nhi:p2"), rec).Materialize(context.Background(), planoRecolhaAnalise())
			if !errors.Is(err, c.quer) || !errors.Is(err, ErrGraphDiverges) {
				t.Fatalf("devia dar %v (e ErrGraphDiverges), got %v", c.quer, err)
			}
			if depois := contarTipos(t, es, "run-1"); !reflect.DeepEqual(antes, depois) {
				t.Fatalf("a retoma recusada escreveu: antes=%v depois=%v", antes, depois)
			}
			if len(rec.payloads) != 0 {
				t.Fatalf("plan.materialized apenso sobre um grafo divergente: %+v", rec.payloads)
			}
		})
	}
}

func deve(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
