package orchestrator_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/contract"
	"github.com/aos-ref/substrate/eventstore"
)

func newStore(t *testing.T) *eventstore.Store {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	return es
}

// buildDAG admite os nós dados (task_ids) num DAG puro.
func buildDAG(t *testing.T, runID string, tasks ...string) *orchestrator.DAG {
	t.Helper()
	d := orchestrator.NewDAG(runID)
	for _, id := range tasks {
		if err := d.AddNode(orchestrator.NodeSpec{TaskID: id}); err != nil {
			t.Fatalf("AddNode %q: %v", id, err)
		}
	}
	return d
}

// TestAddEdgeRejectsCycle prova a aciclicidade FAIL-CLOSED: uma aresta que fecha
// um ciclo é rejeitada na admissão com ErrEdgeClosesCycle e NÃO altera o grafo.
func TestAddEdgeRejectsCycle(t *testing.T) {
	t.Parallel()
	d := buildDAG(t, "run", "a", "b", "c")
	// a→b→c: cadeia acíclica.
	if err := d.AddEdge("a", "b"); err != nil {
		t.Fatalf("a→b: %v", err)
	}
	if err := d.AddEdge("b", "c"); err != nil {
		t.Fatalf("b→c: %v", err)
	}
	// c→a fecharia o ciclo a→b→c→a: tem de ser rejeitada.
	if err := d.AddEdge("c", "a"); !errors.Is(err, orchestrator.ErrEdgeClosesCycle) {
		t.Fatalf("c→a devia ser rejeitada com ErrEdgeClosesCycle, got %v", err)
	}
	// Auto-laço é sempre ciclo.
	if err := d.AddEdge("a", "a"); !errors.Is(err, orchestrator.ErrEdgeClosesCycle) {
		t.Fatalf("auto-laço devia ser rejeitado, got %v", err)
	}
	// O grafo continua ordenável (a rejeição não o corrompeu).
	if _, err := d.TopoOrder(); err != nil {
		t.Fatalf("TopoOrder após rejeição: %v", err)
	}
}

// TestTopoOrderStableAcrossInsertionOrder prova que a ordenação topológica é
// determinística (desempate por task_id lexicográfico) e IDÊNTICA independente da
// ordem de admissão das arestas — a base do replay reprodutível.
func TestTopoOrderStableAcrossInsertionOrder(t *testing.T) {
	t.Parallel()
	// DAG: a→c, b→c, a→d ; nós sem deps: a, b (b<... desempate lexicográfico).
	// Ordem esperada por Kahn com desempate lexicográfico:
	//   prontos {a,b} → a ; a liberta c,d parcialmente (c ainda depende de b).
	//   prontos {b,d} → b ; b liberta c → prontos {c,d}
	//   → c ? d? lexicográfico: c<d → c, depois d.
	// Resultado: a, b, c, d.
	want := []string{"a", "b", "c", "d"}

	mk := func(order func(d *orchestrator.DAG)) []string {
		d := buildDAG(t, "run", "a", "b", "c", "d")
		order(d)
		got, err := d.TopoOrder()
		if err != nil {
			t.Fatalf("TopoOrder: %v", err)
		}
		return got
	}

	got1 := mk(func(d *orchestrator.DAG) {
		_ = d.AddEdge("a", "c")
		_ = d.AddEdge("b", "c")
		_ = d.AddEdge("a", "d")
	})
	got2 := mk(func(d *orchestrator.DAG) {
		// ordem de admissão diferente
		_ = d.AddEdge("a", "d")
		_ = d.AddEdge("b", "c")
		_ = d.AddEdge("a", "c")
	})
	if !reflect.DeepEqual(got1, want) {
		t.Fatalf("topo got1=%v, quero %v", got1, want)
	}
	if !reflect.DeepEqual(got1, got2) {
		t.Fatalf("topo instável: got1=%v got2=%v", got1, got2)
	}
}

// TestTopoOrderRespectsDependencies prova que cada dependência precede o dependente.
func TestTopoOrderRespectsDependencies(t *testing.T) {
	t.Parallel()
	d := buildDAG(t, "run", "build", "test", "deploy", "lint")
	// deploy depende de test; test depende de build; lint depende de build.
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(d.AddEdge("build", "test"))
	must(d.AddEdge("test", "deploy"))
	must(d.AddEdge("build", "lint"))
	order, err := d.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder: %v", err)
	}
	pos := map[string]int{}
	for i, id := range order {
		pos[id] = i
	}
	for _, dep := range [][2]string{{"build", "test"}, {"test", "deploy"}, {"build", "lint"}} {
		if pos[dep[0]] >= pos[dep[1]] {
			t.Fatalf("dependência violada: %s deve preceder %s em %v", dep[0], dep[1], order)
		}
	}
}

// TestGraphBuilderPersistsEvents prova que a construção do DAG persiste
// task.node.created e task.edge.added como eventos append-only no stream=run_id,
// e que uma aresta que fecha ciclo emite task.edge.rejected_cycle com razão.
func TestGraphBuilderPersistsEvents(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	ctx := context.Background()
	gb, err := orchestrator.NewGraphBuilder(es, "run-1", eventstore.Producer{NHIID: "nhi:orq"})
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
	// b→a fecha ciclo → rejeitada + evento de rejeição.
	if err := gb.AddEdge(ctx, "b", "a"); !errors.Is(err, orchestrator.ErrEdgeClosesCycle) {
		t.Fatalf("b→a devia ser rejeitada, got %v", err)
	}

	evs, err := es.Read(ctx, "run-1", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	wantTypes := []string{
		contract.EventTaskNodeCreated,
		contract.EventTaskNodeCreated,
		contract.EventTaskEdgeAdded,
		contract.EventEdgeRejectedCycle,
	}
	if len(evs) != len(wantTypes) {
		t.Fatalf("stream tem %d eventos, quero %d: %+v", len(evs), len(wantTypes), evs)
	}
	for i, want := range wantTypes {
		if evs[i].Type != want {
			t.Fatalf("evento %d = %s, quero %s", i, evs[i].Type, want)
		}
	}
	// A rejeição carrega razão explícita.
	var rej contract.EdgeRejectedCyclePayload
	if err := json.Unmarshal(evs[3].Payload, &rej); err != nil {
		t.Fatalf("unmarshal rejeição: %v", err)
	}
	if rej.From != "b" || rej.To != "a" || rej.Reason == "" {
		t.Fatalf("rejeição sem razão explícita: %+v", rej)
	}
}

// TestReplayReconstructsIdenticalOrder é o teste de INTEGRAÇÃO do replay: um DAG
// persistido reconstrói-se a partir dos eventos do Event Store e produz uma
// ordenação topológica IDÊNTICA à original (ADR-010).
func TestReplayReconstructsIdenticalOrder(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	ctx := context.Background()
	gb, err := orchestrator.NewGraphBuilder(es, "run-replay", eventstore.Producer{NHIID: "nhi:orq"})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	nodes := []string{"deploy", "build", "test", "lint", "package"}
	for _, id := range nodes {
		if err := gb.AddNode(ctx, orchestrator.NodeSpec{TaskID: id}); err != nil {
			t.Fatalf("AddNode %q: %v", id, err)
		}
	}
	edges := [][2]string{
		{"build", "test"},
		{"build", "lint"},
		{"test", "package"},
		{"package", "deploy"},
	}
	for _, e := range edges {
		if err := gb.AddEdge(ctx, e[0], e[1]); err != nil {
			t.Fatalf("AddEdge %s→%s: %v", e[0], e[1], err)
		}
	}
	original, err := gb.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder original: %v", err)
	}

	// Reconstrói o DAG SÓ a partir dos eventos (replay resume-from-step).
	rebuilt, err := orchestrator.RebuildDAG(ctx, es, "run-replay")
	if err != nil {
		t.Fatalf("RebuildDAG: %v", err)
	}
	replayed, err := rebuilt.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder reconstruído: %v", err)
	}
	if !reflect.DeepEqual(original, replayed) {
		t.Fatalf("replay não é determinístico:\n original=%v\n replayed=%v", original, replayed)
	}
	if rebuilt.Len() != len(nodes) {
		t.Fatalf("replay reconstruiu %d nós, quero %d", rebuilt.Len(), len(nodes))
	}
}

// TestRebuildEmptyStream: reconstruir um run sem eventos devolve um DAG vazio.
func TestRebuildEmptyStream(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	d, err := orchestrator.RebuildDAG(context.Background(), es, "vazio")
	if err != nil {
		t.Fatalf("RebuildDAG: %v", err)
	}
	if d.Len() != 0 {
		t.Fatalf("DAG reconstruído de stream vazio tem %d nós, quero 0", d.Len())
	}
}

// TestDAGAccessors cobre os acessores públicos do DAG e do GraphBuilder.
func TestDAGAccessors(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	gb, err := orchestrator.NewGraphBuilder(es, "run-acc", eventstore.Producer{})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	if err := gb.AddNode(context.Background(), orchestrator.NodeSpec{TaskID: "a"}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	d := gb.DAG()
	if d.RunID() != "run-acc" {
		t.Fatalf("RunID=%q, quero run-acc", d.RunID())
	}
	if !d.Has("a") || d.Has("z") {
		t.Fatalf("Has incorrecto: a=%v z=%v", d.Has("a"), d.Has("z"))
	}
	if st, ok := d.State("a"); !ok || st != "ready" {
		t.Fatalf("State(a)=%s,%v, quero ready,true", st, ok)
	}
	if _, ok := d.State("z"); ok {
		t.Fatal("State de nó inexistente devia devolver ok=false")
	}
}

// TestNewGraphBuilderValidation cobre os erros de construção.
func TestNewGraphBuilderValidation(t *testing.T) {
	t.Parallel()
	es := newStore(t)
	if _, err := orchestrator.NewGraphBuilder(nil, "r", eventstore.Producer{}); err == nil {
		t.Fatal("store nil devia falhar")
	}
	if _, err := orchestrator.NewGraphBuilder(es, "", eventstore.Producer{}); err == nil {
		t.Fatal("run_id vazio devia falhar")
	}
	if _, err := orchestrator.RebuildDAG(context.Background(), nil, "r"); err == nil {
		t.Fatal("RebuildDAG com store nil devia falhar")
	}
	if _, err := orchestrator.NewDeadlockDetector(nil, nil, nil, eventstore.Producer{}); err == nil {
		t.Fatal("detector sem dependências devia falhar")
	}
}

// TestAddNodeErrors cobre os caminhos de erro da admissão de nós/arestas.
func TestAddNodeErrors(t *testing.T) {
	t.Parallel()
	d := orchestrator.NewDAG("run")
	if err := d.AddNode(orchestrator.NodeSpec{TaskID: ""}); !errors.Is(err, orchestrator.ErrEmptyTaskID) {
		t.Fatalf("task_id vazio devia falhar, got %v", err)
	}
	if err := d.AddNode(orchestrator.NodeSpec{TaskID: "a"}); err != nil {
		t.Fatalf("AddNode a: %v", err)
	}
	if err := d.AddNode(orchestrator.NodeSpec{TaskID: "a"}); !errors.Is(err, orchestrator.ErrNodeExists) {
		t.Fatalf("nó duplicado devia falhar, got %v", err)
	}
	if err := d.AddEdge("a", "x"); !errors.Is(err, orchestrator.ErrNodeNotFound) {
		t.Fatalf("aresta para nó inexistente devia falhar, got %v", err)
	}
}
