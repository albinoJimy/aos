package runlifecycle_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// graph_test.go — A PROVA DA RE-HIDRATAÇÃO (AOS-281 AC4, ADR-023 §2.3).
//
// «Um componente que tome posse a meio RE-HIDRATA o grafo a partir do log em vez de
// começar vazio; não existe caminho que admita arestas cegas às já duráveis.»

// seedGraph constrói, sob a posse de A, um grafo `a→b→c` com `a` já em running.
// Devolve a ordem topológica que ficou DURÁVEL.
func seedGraph(ctx context.Context, t *testing.T, ten *runlifecycle.Tenure) []string {
	t.Helper()
	g, err := ten.Graph(ctx, eventstore.Producer{})
	if err != nil {
		t.Fatalf("Graph do primeiro dono: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := g.AddNode(ctx, orchestrator.NodeSpec{TaskID: id}); err != nil {
			t.Fatalf("AddNode(%s): %v", id, err)
		}
	}
	if err := g.AddEdge(ctx, "a", "b"); err != nil {
		t.Fatalf("AddEdge(a→b): %v", err)
	}
	if err := g.AddEdge(ctx, "b", "c"); err != nil {
		t.Fatalf("AddEdge(b→c): %v", err)
	}
	if err := g.MarkRunning(ctx, "a"); err != nil {
		t.Fatalf("MarkRunning(a): %v", err)
	}
	order, err := g.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder do primeiro dono: %v", err)
	}
	return order
}

// ---------------------------------------------------------------------------
// TESTE 1 — TOMADA DE POSSE A MEIO: o grafo vem do LOG, não vazio.
//
// Falha-antes: com `NewGraphBuilder` (DAG vazio), o segundo dono veria `Len()==0` e
// uma TopoOrder vazia sobre um run que tem três nós duráveis.
// ---------------------------------------------------------------------------

func TestRehidratacao_PosseAMeio_GrafoVemDoLog(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	clk := newClock()
	const runID = "run-rehidrata"

	a := replica(t, store, clk, "proc-a")
	ta, err := runlifecycle.Claim(ctx, store, a, runID)
	if err != nil {
		t.Fatalf("claim de A: %v", err)
	}
	ordemDuravel := seedGraph(ctx, t, ta)

	// A larga; B toma posse A MEIO do run.
	if err := ta.Release(ctx); err != nil {
		t.Fatalf("release de A: %v", err)
	}
	b := replica(t, store, clk, "proc-b")
	tb, err := runlifecycle.Claim(ctx, store, b, runID)
	if err != nil {
		t.Fatalf("claim de B: %v", err)
	}

	gb, err := tb.Graph(ctx, eventstore.Producer{})
	if err != nil {
		t.Fatalf("Graph de B: %v", err)
	}

	// (a) A TOPOLOGIA veio do log.
	if got := gb.DAG().Len(); got != 3 {
		t.Fatalf("nós no grafo de B = %d, quer 3 — B começou VAZIO sobre um run com três nós duráveis", got)
	}
	ordemB, err := gb.TopoOrder()
	if err != nil {
		t.Fatalf("TopoOrder de B: %v", err)
	}
	if !reflect.DeepEqual(ordemB, ordemDuravel) {
		t.Fatalf("TopoOrder de B = %v, quer %v (idêntica — ADR-010)", ordemB, ordemDuravel)
	}

	// (b) O ESTADO POR-NÓ veio do log: `a` continua running, não voltou a ready.
	if st, ok := gb.DAG().State("a"); !ok || st != state.Running {
		t.Fatalf("estado de `a` em B = (%v, %v), quer (running, true) — a transição durável perdeu-se na retoma", st, ok)
	}

	// (c) AS ARESTAS NÃO SÃO CEGAS: a inversa fecharia um ciclo e é REJEITADA.
	//     Este é o coração do AC4. Um builder cego admitiria `c→a` (a memória vazia não
	//     vê `a→b→c`), o log ficaria com o ciclo, e o RebuildDAG falharia PARA SEMPRE.
	err = gb.AddEdge(ctx, "c", "a")
	if !errors.Is(err, orchestrator.ErrEdgeClosesCycle) {
		t.Fatalf("AddEdge(c→a) num grafo re-hidratado = %v, quer ErrEdgeClosesCycle — B admitiu uma aresta CEGA às que já estavam duráveis", err)
	}

	// (d) E o log continua a sustentar um DAG válido depois de tudo isto.
	if _, err := orchestrator.RebuildDAG(ctx, storeFor(store), runID); err != nil {
		t.Fatalf("RebuildDAG após a retoma: %v — o log deixou de sustentar um grafo válido", err)
	}
}

// ---------------------------------------------------------------------------
// TESTE 2 — NÃO-VACUIDADE DO TESTE 1: um builder CEGO admite mesmo a aresta.
//
// Sem este teste, o (c) acima poderia estar a passar porque `AddEdge(c→a)` falha
// sempre por outra razão qualquer. Aqui prova-se que o construtor cego — o que este
// pacote NÃO usa — aceita exactamente a aresta que o re-hidratado recusa, e que o log
// fica com o ciclo. É o defeito, reproduzido, para que o guarda tenha sentido.
// ---------------------------------------------------------------------------

func TestRehidratacao_NaoVacuidade_BuilderCegoAdmiteAresta(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	clk := newClock()
	const runID = "run-cego"

	a := replica(t, store, clk, "proc-a")
	ta, err := runlifecycle.Claim(ctx, store, a, runID)
	if err != nil {
		t.Fatalf("claim de A: %v", err)
	}
	seedGraph(ctx, t, ta)

	// O construtor CEGO, sobre o MESMO run já povoado.
	cego, err := orchestrator.NewGraphBuilder(storeFor(store), runID, eventstore.Producer{})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	if got := cego.DAG().Len(); got != 0 {
		t.Fatalf("o builder cego devia começar VAZIO, tem %d nós — o teste deixou de reproduzir o defeito", got)
	}
	// Ele nem conhece `c` nem `a`, pelo que a aresta não fecha ciclo nenhum NA SUA
	// MEMÓRIA — e é isso que o faz aceitá-la onde o re-hidratado a recusa. Falha por
	// nó inexistente, que é o mais longe que ele chega: já não é o mesmo `AddEdge`.
	err = cego.AddEdge(ctx, "c", "a")
	if errors.Is(err, orchestrator.ErrEdgeClosesCycle) {
		t.Fatal("o builder CEGO detectou o ciclo — impossível: ele não tem as arestas duráveis em memória. O teste 1(c) não está a provar o que diz")
	}
}

// ---------------------------------------------------------------------------
// TESTE 3 — RUN NOVO: a via re-hidratada é equivalente à cega.
//
// A propriedade que torna [Tenure.Graph] usável SEMPRE: quem toma posse não precisa de
// saber, à partida, se o run é novo — pergunta que o chamador tipicamente não sabe
// responder e que era a origem do erro.
// ---------------------------------------------------------------------------

func TestRehidratacao_RunNovo_EquivalenteAoBuilderVazio(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	clk := newClock()
	const runID = "run-novo"

	lm := replica(t, store, clk, "proc-a")
	ten, err := runlifecycle.Claim(ctx, store, lm, runID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	g, err := ten.Graph(ctx, eventstore.Producer{})
	if err != nil {
		t.Fatalf("Graph num run novo (stream inexistente): %v", err)
	}
	if got := g.DAG().Len(); got != 0 {
		t.Fatalf("grafo de um run novo tem %d nós, quer 0", got)
	}
	if err := g.AddNode(ctx, orchestrator.NodeSpec{TaskID: "x"}); err != nil {
		t.Fatalf("AddNode num run novo: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TESTE 4 — AS ESCRITAS DO GRAFO SÃO FENCED (ADR-023 §2.4).
//
// A re-hidratação não valeria nada se as escritas do grafo escapassem ao fencing: o
// dono superado continuaria a escrever topologia. Aqui B supera A, e A tenta escrever
// PELO SEU GRAFO — não pela via directa.
// ---------------------------------------------------------------------------

func TestGrafo_EscritaDeDonoSuperado_Recusada(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	clk := newClock()
	const runID = "run-grafo-fenced"

	a := replica(t, store, clk, "proc-a")
	ta, err := runlifecycle.Claim(ctx, store, a, runID)
	if err != nil {
		t.Fatalf("claim de A: %v", err)
	}
	ga, err := ta.Graph(ctx, eventstore.Producer{})
	if err != nil {
		t.Fatalf("Graph de A: %v", err)
	}
	if err := ga.AddNode(ctx, orchestrator.NodeSpec{TaskID: "a"}); err != nil {
		t.Fatalf("AddNode de A enquanto dono: %v", err)
	}

	// A expira; B supera-o.
	clk.advance(testTTL + 1)
	b := replica(t, store, clk, "proc-b")
	if _, err := runlifecycle.Claim(ctx, store, b, runID); err != nil {
		t.Fatalf("claim de B: %v", err)
	}

	// A continua com o SEU GraphBuilder em mãos e tenta escrever topologia nova.
	before := streamLen(t, store, runID)
	err = ga.AddNode(ctx, orchestrator.NodeSpec{TaskID: "zombie"})
	if err == nil {
		t.Fatal("o dono SUPERADO escreveu um nó novo pelo seu GraphBuilder — as escritas do grafo escapam ao fencing")
	}
	if after := streamLen(t, store, runID); after != before {
		t.Fatalf("o stream cresceu de %d para %d — a escrita do dono superado CHEGOU ao log", before, after)
	}
}

// storeFor adapta o *eventstore.Store à interface que o orquestrador declara.
func storeFor(s *eventstore.Store) orchestrator.EventStore { return s }
