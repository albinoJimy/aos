package orchestrator_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/contract"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	arstate "github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// flakyStore embrulha um *eventstore.Store e faz FALHAR o Append de eventos de um
// dado tipo (contando quantas falhas restantes). Serve para provar a durabilidade
// fail-closed: se o log não aceita o facto, o grafo em memória não deve reter a
// mutação.
type flakyStore struct {
	inner    *eventstore.Store
	failType string
	remain   int
}

func (f *flakyStore) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if f.failType != "" && in.Type == f.failType && f.remain > 0 {
		f.remain--
		return eventstore.AppendResult{}, errors.New("flaky: append rejeitado")
	}
	return f.inner.Append(ctx, streamID, in, opts...)
}

func (f *flakyStore) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	return f.inner.Read(ctx, streamID, fromSeq)
}

// TestReplayReconstructsNodeExecutionState prova (findings 1/2/9) que RebuildDAG
// reconstrói o ESTADO DE EXECUÇÃO por-nó, não só a topologia: as transições
// ready→running (task.node.state_changed) e a resolução running→failed da vítima
// (deadlock.resolved) sobrevivem ao replay. Sem isto, um crash entre o commit da
// resolução e a aplicação dos efeitos perderia o abort.
func TestReplayReconstructsNodeExecutionState(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	ctx := context.Background()

	gb, err := orchestrator.NewGraphBuilder(es, "run-state", eventstore.Producer{NHIID: "nhi:orq"})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	if err := gb.AddNode(ctx, orchestrator.NodeSpec{TaskID: "t1", Priority: 10}); err != nil {
		t.Fatalf("AddNode t1: %v", err)
	}
	if err := gb.AddNode(ctx, orchestrator.NodeSpec{TaskID: "t2", Priority: 5}); err != nil {
		t.Fatalf("AddNode t2: %v", err)
	}
	// Claim DURÁVEL de ambos os nós (persiste task.node.state_changed).
	if err := gb.MarkRunning(ctx, "t1"); err != nil {
		t.Fatalf("MarkRunning t1: %v", err)
	}
	if err := gb.MarkRunning(ctx, "t2"); err != nil {
		t.Fatalf("MarkRunning t2: %v", err)
	}

	// Espera circular sobre dois leases; t2 (menor prioridade) é a vítima.
	l := orchestrator.NewResourceLedger()
	l.Acquire("t1", "lease:L1")
	l.Acquire("t2", "lease:L2")
	l.Wait("t1", "lease:L2")
	l.Wait("t2", "lease:L1")

	dd, err := orchestrator.NewDeadlockDetector(gb.DAG(), l, es, eventstore.Producer{NHIID: "nhi:orq"})
	if err != nil {
		t.Fatalf("NewDeadlockDetector: %v", err)
	}
	res, err := dd.DetectAndResolve(ctx)
	if err != nil || res == nil || !res.Applied || res.Victim != "t2" {
		t.Fatalf("resolução inesperada: res=%+v err=%v", res, err)
	}

	// Estado VIVO: t1 running, t2 failed.
	if st, _ := gb.DAG().State("t1"); st != arstate.Running {
		t.Fatalf("vivo t1=%s, quero running", st)
	}
	if st, _ := gb.DAG().State("t2"); st != arstate.Failed {
		t.Fatalf("vivo t2=%s, quero failed", st)
	}

	// Replay SÓ a partir do log: o estado por-nó tem de ser reconstruído.
	rebuilt, err := orchestrator.RebuildDAG(ctx, es, "run-state")
	if err != nil {
		t.Fatalf("RebuildDAG: %v", err)
	}
	if st, _ := rebuilt.State("t1"); st != arstate.Running {
		t.Fatalf("replay t1=%s, quero running (estado de execução perdido)", st)
	}
	if st, _ := rebuilt.State("t2"); st != arstate.Failed {
		t.Fatalf("replay t2=%s, quero failed (resolução de deadlock perdida)", st)
	}
}

// TestAddNodeRevertsOnAppendFailure prova (finding 4) que uma falha de Append na
// admissão de um nó NÃO deixa o nó em memória (fail-closed: DAG↔log consistente).
func TestAddNodeRevertsOnAppendFailure(t *testing.T) {
	t.Parallel()
	fs := &flakyStore{inner: newStore(t), failType: contract.EventTaskNodeCreated, remain: 1}
	ctx := context.Background()
	gb, err := orchestrator.NewGraphBuilder(fs, "run-revn", eventstore.Producer{})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	if err := gb.AddNode(ctx, orchestrator.NodeSpec{TaskID: "a"}); err == nil {
		t.Fatal("AddNode devia falhar quando o Append falha")
	}
	if gb.DAG().Has("a") {
		t.Fatal("nó 'a' não devia permanecer no DAG após falha do Append (revert)")
	}
	// A falha esgotou-se; re-admitir agora tem de funcionar (contador de seq intacto).
	if err := gb.AddNode(ctx, orchestrator.NodeSpec{TaskID: "a"}); err != nil {
		t.Fatalf("re-AddNode 'a' após revert: %v", err)
	}
	if !gb.DAG().Has("a") {
		t.Fatal("nó 'a' devia existir após re-admissão bem-sucedida")
	}
}

// TestAddEdgeRevertsOnAppendFailure prova (finding 4) que uma falha de Append na
// admissão de uma aresta NÃO deixa a aresta em memória: após o revert, a aresta
// inversa passa a ser admissível (não haveria ciclo).
func TestAddEdgeRevertsOnAppendFailure(t *testing.T) {
	t.Parallel()
	fs := &flakyStore{inner: newStore(t), failType: contract.EventTaskEdgeAdded, remain: 1}
	ctx := context.Background()
	gb, err := orchestrator.NewGraphBuilder(fs, "run-reve", eventstore.Producer{})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if err := gb.AddNode(ctx, orchestrator.NodeSpec{TaskID: id}); err != nil {
			t.Fatalf("AddNode %q: %v", id, err)
		}
	}
	if err := gb.AddEdge(ctx, "a", "b"); err == nil {
		t.Fatal("AddEdge a→b devia falhar quando o Append falha")
	}
	// Se a→b tivesse ficado em memória, b→a seria rejeitada como ciclo. Como foi
	// revertida, b→a é admissível (a falha já se esgotou).
	if err := gb.AddEdge(ctx, "b", "a"); err != nil {
		t.Fatalf("b→a devia ser admissível após revert de a→b, got %v", err)
	}
}

// TestMarkRunningRevertsOnAppendFailure prova (finding 4) que uma falha de Append
// no claim ready→running NÃO deixa o nó em running em memória: a transição é
// revertida (o estado por-nó nunca fica durável só em memória, divergindo do log).
func TestMarkRunningRevertsOnAppendFailure(t *testing.T) {
	t.Parallel()
	fs := &flakyStore{inner: newStore(t), failType: contract.EventTaskNodeStateChanged, remain: 1}
	ctx := context.Background()
	gb, err := orchestrator.NewGraphBuilder(fs, "run-revr", eventstore.Producer{})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	if err := gb.AddNode(ctx, orchestrator.NodeSpec{TaskID: "a"}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if err := gb.MarkRunning(ctx, "a"); err == nil {
		t.Fatal("MarkRunning devia falhar quando o Append falha")
	}
	if st, _ := gb.DAG().State("a"); st != arstate.Ready {
		t.Fatalf("estado de 'a'=%s após falha, quero ready (revert)", st)
	}
	// A falha esgotou-se; o claim agora tem de funcionar.
	if err := gb.MarkRunning(ctx, "a"); err != nil {
		t.Fatalf("MarkRunning após revert: %v", err)
	}
	if st, _ := gb.DAG().State("a"); st != arstate.Running {
		t.Fatalf("estado de 'a'=%s, quero running", st)
	}
}

// TestAddEdgeRejectionPreservesCycleSentinel prova (finding 5) que, quando o
// Append da rejeição de ciclo FALHA, AddEdge ainda expõe ErrEdgeClosesCycle
// (errors.Is), sem mascará-lo com o erro de infra.
func TestAddEdgeRejectionPreservesCycleSentinel(t *testing.T) {
	t.Parallel()
	fs := &flakyStore{inner: newStore(t), failType: contract.EventEdgeRejectedCycle, remain: 1}
	ctx := context.Background()
	gb, err := orchestrator.NewGraphBuilder(fs, "run-sent", eventstore.Producer{})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if err := gb.AddNode(ctx, orchestrator.NodeSpec{TaskID: id}); err != nil {
			t.Fatalf("AddNode %q: %v", id, err)
		}
	}
	if err := gb.AddEdge(ctx, "a", "b"); err != nil {
		t.Fatalf("AddEdge a→b: %v", err)
	}
	// b→a fecha ciclo; o registo da rejeição falha, mas o sentinel tem de persistir.
	err = gb.AddEdge(ctx, "b", "a")
	if !errors.Is(err, orchestrator.ErrEdgeClosesCycle) {
		t.Fatalf("b→a: sentinel de ciclo mascarado, got %v", err)
	}
}

// TestDeadlockNoAbortableVictimNoOrphan prova (finding 6) que, se nenhuma tarefa
// do ciclo puder abortar (nenhuma em running), DetectAndResolve falha SEM emitir
// deadlock.detected órfão.
func TestDeadlockNoAbortableVictimNoOrphan(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	ctx := context.Background()
	d := orchestrator.NewDAG("run-noabort")
	// Nós ficam em ready (nunca MarkRunning): ready→failed é inválido.
	if err := d.AddNode(orchestrator.NodeSpec{TaskID: "t1", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	if err := d.AddNode(orchestrator.NodeSpec{TaskID: "t2", Priority: 1}); err != nil {
		t.Fatal(err)
	}
	l := orchestrator.NewResourceLedger()
	l.Acquire("t1", "L1")
	l.Acquire("t2", "L2")
	l.Wait("t1", "L2")
	l.Wait("t2", "L1")

	dd, err := orchestrator.NewDeadlockDetector(d, l, es, eventstore.Producer{})
	if err != nil {
		t.Fatalf("NewDeadlockDetector: %v", err)
	}
	if _, err := dd.DetectAndResolve(ctx); err == nil {
		t.Fatal("esperava erro: sem vítima abortável")
	}
	// Nenhum evento — nem deadlock.detected órfão.
	if _, err := es.Read(ctx, "run-noabort", 1); err == nil {
		t.Fatal("não devia haver eventos (detected órfão)")
	}
}

// TestDetectAndResolveAllResolvesDisjointCycles prova (finding 3) que
// DetectAndResolveAll resolve TODOS os deadlocks disjuntos numa invocação,
// enquanto DetectAndResolve resolve apenas um.
func TestDetectAndResolveAllResolvesDisjointCycles(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	ctx := context.Background()
	d := orchestrator.NewDAG("run-multi")
	for _, id := range []string{"a1", "a2", "b1", "b2"} {
		if err := d.AddNode(orchestrator.NodeSpec{TaskID: id, Priority: 1}); err != nil {
			t.Fatal(err)
		}
		if err := d.MarkRunning(id); err != nil {
			t.Fatal(err)
		}
	}
	l := orchestrator.NewResourceLedger()
	// Ciclo A: a1↔a2.
	l.Acquire("a1", "LA1")
	l.Acquire("a2", "LA2")
	l.Wait("a1", "LA2")
	l.Wait("a2", "LA1")
	// Ciclo B: b1↔b2 (disjunto de A).
	l.Acquire("b1", "LB1")
	l.Acquire("b2", "LB2")
	l.Wait("b1", "LB2")
	l.Wait("b2", "LB1")

	dd, err := orchestrator.NewDeadlockDetector(d, l, es, eventstore.Producer{})
	if err != nil {
		t.Fatalf("NewDeadlockDetector: %v", err)
	}
	all, err := dd.DetectAndResolveAll(ctx)
	if err != nil {
		t.Fatalf("DetectAndResolveAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("DetectAndResolveAll resolveu %d ciclos, quero 2", len(all))
	}
	for _, r := range all {
		if !r.Applied {
			t.Fatalf("resolução não aplicada: %+v", r)
		}
	}
	// Já não há deadlock.
	if left, err := dd.DetectAndResolve(ctx); err != nil || left != nil {
		t.Fatalf("após resolver tudo não devia haver ciclo: left=%+v err=%v", left, err)
	}
}

// TestAgentIdentitySurvivesReplayAndIdempotencyKeyStable prova (finding 8, critério
// de aceitação 6) que a AgentIdentity (NHI + cadeia de delegação) sobrevive a
// persist+replay e que a idempotency key do nó (StepNodeCreated) NÃO depende do
// agente (ADR-001).
func TestAgentIdentitySurvivesReplayAndIdempotencyKeyStable(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	ctx := context.Background()
	gb, err := orchestrator.NewGraphBuilder(es, "run-nhi", eventstore.Producer{NHIID: "nhi:orq"})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	agent := contract.AgentIdentity{
		NHIID: "nhi:agent/planner",
		DelegationChain: []contract.DelegationHop{
			{Sub: "nhi:agent/planner", ActAs: "nhi:human/alice"},
		},
	}
	if err := gb.AddNode(ctx, orchestrator.NodeSpec{TaskID: "n1", Priority: 3, Agent: agent}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	// A identidade sobrevive ao replay.
	rebuilt, err := orchestrator.RebuildDAG(ctx, es, "run-nhi")
	if err != nil {
		t.Fatalf("RebuildDAG: %v", err)
	}
	got, ok := rebuilt.Agent("n1")
	if !ok {
		t.Fatal("nó n1 ausente após replay")
	}
	if !reflect.DeepEqual(got, agent) {
		t.Fatalf("AgentIdentity não sobreviveu ao replay: got %+v, quero %+v", got, agent)
	}

	// A idempotency key do nó é independente do agente (ADR-001).
	if contract.StepNodeCreated("n1") != "node:n1" {
		t.Fatalf("StepNodeCreated instável/dependente do agente: %q", contract.StepNodeCreated("n1"))
	}
}

// TestObservabilitySpansEmitted prova (finding 7) que a construção do grafo e a
// resolução de deadlock emitem spans OTel GenAI: invoke_agent por nó (com
// atributos de decomposição e custo por span) e um span de resolução de deadlock.
func TestObservabilitySpansEmitted(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	ctx := context.Background()
	tr := &agentruntime.RecordingTracer{}

	gb, err := orchestrator.NewGraphBuilder(es, "run-otel", eventstore.Producer{NHIID: "nhi:orq"},
		orchestrator.WithGraphTracer(tr))
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	for _, id := range []string{"t1", "t2"} {
		if err := gb.AddNode(ctx, orchestrator.NodeSpec{
			TaskID:   id,
			Priority: 5,
			Agent:    contract.AgentIdentity{NHIID: "nhi:agent/" + id},
			Task:     contract.TaskSpec{ToolID: "tool:x", Capability: "cap:x"},
		}); err != nil {
			t.Fatalf("AddNode %q: %v", id, err)
		}
		if err := gb.MarkRunning(ctx, id); err != nil {
			t.Fatalf("MarkRunning %q: %v", id, err)
		}
	}

	invoke := tr.SpansByOperation(agentruntime.OpInvokeAgent)
	if len(invoke) != 2 {
		t.Fatalf("spans invoke_agent=%d, quero 2 (um por nó)", len(invoke))
	}
	for _, s := range invoke {
		if !s.Ended {
			t.Fatalf("span invoke_agent não foi fechado: %+v", s)
		}
		if _, ok := s.Attributes[agentruntime.AttrCostUSD]; !ok {
			t.Fatalf("span invoke_agent sem custo por span: %+v", s.Attributes)
		}
		if _, ok := s.Attributes["aos.node.task_id"]; !ok {
			t.Fatalf("span invoke_agent sem atributo de decomposição task_id: %+v", s.Attributes)
		}
		if _, ok := s.Attributes["aos.node.agent_nhi"]; !ok {
			t.Fatalf("span invoke_agent sem NHI do agente: %+v", s.Attributes)
		}
	}

	// Span da resolução de deadlock.
	l := orchestrator.NewResourceLedger()
	l.Acquire("t1", "L1")
	l.Acquire("t2", "L2")
	l.Wait("t1", "L2")
	l.Wait("t2", "L1")
	trd := &agentruntime.RecordingTracer{}
	dd, err := orchestrator.NewDeadlockDetector(gb.DAG(), l, es, eventstore.Producer{NHIID: "nhi:orq"},
		orchestrator.WithDetectorTracer(trd))
	if err != nil {
		t.Fatalf("NewDeadlockDetector: %v", err)
	}
	if _, err := dd.DetectAndResolve(ctx); err != nil {
		t.Fatalf("DetectAndResolve: %v", err)
	}
	dlSpans := trd.Spans()
	if len(dlSpans) != 1 {
		t.Fatalf("spans de resolução=%d, quero 1", len(dlSpans))
	}
	if _, ok := dlSpans[0].Attributes[agentruntime.AttrCostUSD]; !ok {
		t.Fatalf("span de resolução sem custo por span: %+v", dlSpans[0].Attributes)
	}
	if v, ok := dlSpans[0].Attributes["aos.deadlock.victim"]; !ok || v != "t2" {
		t.Fatalf("span de resolução sem vítima esperada: %+v", dlSpans[0].Attributes)
	}
}
