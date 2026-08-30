package runlifecycle_test

import (
	"context"
	"sync"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/plandispatch"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/substrate/eventstore"
)

// derivacao_test.go — «O OUTRO DERIVA DO LOG EM VEZ DE ESCREVER» (AOS-281 AC3).
//
// O ADR-023 §2.2 DECLARA que o Escalonador deriva. Este ficheiro DEMONSTRA-O com o
// despachante REAL — não com um duplo — sobre vistas construídas a partir do log de um
// run possuído, e verifica a propriedade que interessa: depois de uma passagem
// completa de despacho, o stream do run NÃO ganhou um único facto.
//
// É esta a diferença entre a declaração e a prova. Uma porta chamada `LifecycleView`
// pode ser implementada por algo que escreva; o que este teste mede não é o nome da
// porta, é o log.

// ---- Colaboradores mínimos do despachante ---------------------------------
//
// Gate, Headroom, CardOracle e DispatchSink são portas do EPIC-03 que este ticket NÃO
// compõe (não são autoridade de ciclo de vida — ver o relatório de AOS-281). Aqui são
// duplos triviais, de propósito: o que está sob teste é a DERIVAÇÃO do estado, e
// qualquer coisa a mais nestes duplos seria ruído a competir com a asserção.

type gateAberto struct{}

func (gateAberto) Materialized(context.Context, string) (bool, error) { return true, nil }

type semTecto struct{}

func (semTecto) Available(context.Context) (int, error) { return 100, nil }
func (semTecto) Acquire(context.Context) (bool, error)  { return true, nil }
func (semTecto) Release(context.Context) error          { return nil }

type cartoesLimpos struct{}

func (cartoesLimpos) Cleared(context.Context, string, string) (bool, error) { return true, nil }

// sinkRegistador anota os nós entregues, sem efeito nenhum.
type sinkRegistador struct {
	mu          sync.Mutex
	despachados []string
}

func (s *sinkRegistador) Dispatch(_ context.Context, n plandispatch.Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.despachados = append(s.despachados, n.NodeID)
	return nil
}

func (s *sinkRegistador) lista() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.despachados...)
}

// ---------------------------------------------------------------------------
// TESTE — O DESPACHANTE DECIDE A PARTIR DO LOG, E NÃO ESCREVE NO STREAM DO RUN.
//
// Cenário: grafo `a→b`. O dono (o ORQ, sob posse) marca `a` como running. O
// despachante corre uma passagem sobre a vista DERIVADA: `a` está em voo (não
// re-despacha), `b` depende de `a` que não está complete (não despacha). Zero
// despachos, e — o que interessa — zero escritas no run.
// ---------------------------------------------------------------------------

func TestDerivacao_DespachanteDecideDoLogSemEscrever(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	lm := replica(t, store, newClock(), "proc-orq")
	const derivaRun = "run-deriva"
	const derivaPlan = "plan-deriva"

	ten, err := runlifecycle.Claim(ctx, store, lm, derivaRun)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// O ORQ, SOB POSSE, escreve a topologia e o estado. É o único escritor.
	g, err := ten.Graph(ctx, eventstore.Producer{})
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if err := g.AddNode(ctx, orchestrator.NodeSpec{TaskID: id}); err != nil {
			t.Fatalf("AddNode(%s): %v", id, err)
		}
	}
	if err := g.AddEdge(ctx, "a", "b"); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	if err := g.MarkRunning(ctx, "a"); err != nil {
		t.Fatalf("MarkRunning(a): %v", err)
	}

	antes := streamLen(t, store, derivaRun)

	// O SCH deriva: a vista vem do log, por RebuildDAG.
	lr, err := runlifecycle.NewLifecycleReader(store, derivaPlan, derivaRun)
	if err != nil {
		t.Fatalf("NewLifecycleReader: %v", err)
	}
	vista, err := lr.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// A derivação é fiel, nó a nó.
	if st, _ := vista.State(ctx, derivaPlan, "a"); st != plandispatch.NodeRunning {
		t.Fatalf("estado derivado de `a` = %v, quer NodeRunning — o log diz running", st)
	}
	if st, _ := vista.State(ctx, derivaPlan, "b"); st != plandispatch.NodePending {
		t.Fatalf("estado derivado de `b` = %v, quer NodePending", st)
	}
	// Fail-closed: um nó que o log não conhece é NodeUnknown, nunca um palpite.
	if st, _ := vista.State(ctx, derivaPlan, "inexistente"); st != plandispatch.NodeUnknown {
		t.Fatalf("estado de um nó ausente = %v, quer NodeUnknown", st)
	}

	// O despachante REAL corre sobre essa vista.
	sink := &sinkRegistador{}
	d, err := plandispatch.NewDispatcher(gateAberto{}, vista, semTecto{}, cartoesLimpos{}, sink)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	res, err := d.Dispatch(ctx, plandispatch.Plan{
		PlanID: derivaPlan,
		Nodes: []plandispatch.Node{
			{NodeID: "a"},
			{NodeID: "b", DependsOn: []string{"a"}},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res.Dispatched != 0 {
		t.Fatalf("despachados = %d, quer 0 (`a` está em voo, `b` espera por `a`) — despachados: %v", res.Dispatched, sink.lista())
	}

	// A ASSERÇÃO QUE VALE: o stream do run não ganhou nada com a passagem.
	if depois := streamLen(t, store, derivaRun); depois != antes {
		t.Fatalf("o stream do run cresceu de %d para %d durante uma passagem de despacho — o SCH ESCREVEU ciclo de vida, contra ADR-023 §2.2", antes, depois)
	}
}

// ---------------------------------------------------------------------------
// TESTE — NÃO-VACUIDADE: quando o log diz `complete`, o dependente DESPACHA.
//
// Sem isto, o teste anterior passaria com uma vista que devolvesse sempre
// NodeUnknown — «zero despachos» é fácil de obter por avaria. Aqui o dono avança o
// estado no log e a MESMA derivação passa a libertar o dependente: prova que a vista
// segue o log, e não que está simplesmente morta.
// ---------------------------------------------------------------------------

func TestDerivacao_NaoVacuidade_LogCompleteLibertaDependente(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	lm := replica(t, store, newClock(), "proc-orq")
	const runID2 = "run-deriva-2"
	const planID2 = "plan-deriva-2"

	ten, err := runlifecycle.Claim(ctx, store, lm, runID2)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	g, err := ten.Graph(ctx, eventstore.Producer{})
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if err := g.AddNode(ctx, orchestrator.NodeSpec{TaskID: id}); err != nil {
			t.Fatalf("AddNode(%s): %v", id, err)
		}
	}
	if err := g.AddEdge(ctx, "a", "b"); err != nil {
		t.Fatalf("AddEdge: %v", err)
	}
	// `a` percorre ready→running→complete, tudo escrito pelo DONO.
	if err := g.MarkRunning(ctx, "a"); err != nil {
		t.Fatalf("MarkRunning(a): %v", err)
	}
	if err := marcarCompleto(ctx, ten, runID2, "a"); err != nil {
		t.Fatalf("transição a→complete: %v", err)
	}

	lr, err := runlifecycle.NewLifecycleReader(store, planID2, runID2)
	if err != nil {
		t.Fatalf("NewLifecycleReader: %v", err)
	}
	vista, err := lr.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if st, _ := vista.State(ctx, planID2, "a"); st != plandispatch.NodeComplete {
		t.Fatalf("estado derivado de `a` = %v, quer NodeComplete — a derivação não seguiu o log", st)
	}

	sink := &sinkRegistador{}
	d, err := plandispatch.NewDispatcher(gateAberto{}, vista, semTecto{}, cartoesLimpos{}, sink)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	res, err := d.Dispatch(ctx, plandispatch.Plan{
		PlanID: planID2,
		Nodes: []plandispatch.Node{
			{NodeID: "a"},
			{NodeID: "b", DependsOn: []string{"a"}},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res.Dispatched != 1 {
		t.Fatalf("despachados = %d, quer 1 (`b` libertado por `a` complete) — %v", res.Dispatched, sink.lista())
	}
	if got := sink.lista(); len(got) != 1 || got[0] != "b" {
		t.Fatalf("despachados = %v, quer [b]", got)
	}
}

// marcarCompleto escreve a transição running→complete de um nó pela via da posse — o
// facto `task.node.state_changed` que o [orchestrator.RebuildDAG] reproduz. Vive no
// teste porque o `GraphBuilder` só expõe o `MarkRunning`; escrever o facto à mão aqui
// é honesto (é exactamente o que o builder escreveria) e mantém o teste a exercer a
// DERIVAÇÃO, que é o que está sob prova.
func marcarCompleto(ctx context.Context, ten *runlifecycle.Tenure, runID, taskID string) error {
	_, err := ten.Append(ctx, eventstore.EventInput{
		Type:    "task.node.state_changed",
		Payload: []byte(`{"run_id":"` + runID + `","task_id":"` + taskID + `","from":"running","to":"complete"}`),
		RunID:   runID,
		StepID:  "nodestate:" + taskID + ":complete",
	})
	return err
}
