package orchestrator_test

// O BUILDER CEGO — duas guardas contra o run permanentemente irreconstruível.
//
// Vêm de uma auditoria adversarial multi-agente de 2026-08-30, e as duas conclusões
// foram MEDIDAS com o Event Store REAL, sem mocks. Ambas terminam no mesmo sítio: o log
// com `a→b` E `b→a`, e o [orchestrator.RebuildDAG] a falhar PARA SEMPRE — é função pura
// do log desde o seq 1, o log é append-only, e o vocabulário de eventos não tem
// `task.edge.removed` nem compensação. Não há reparação em banda.
//
// # (1) O REVERT DE UMA MUTAÇÃO QUE NÃO HOUVE
//
// [orchestrator.DAG.AddEdge] devolve nil em DOIS casos: quando adicionou a aresta, e
// quando ela JÁ LÁ ESTAVA (curto-circuito idempotente, que não muta). O builder tratava
// os dois como um só e, em erro do `emit`, revertia sempre — removendo da memória uma
// aresta DURÁVEL. A inversa deixava então de parecer um ciclo, era admitida, e o log
// ficava com as duas.
//
// # (2) O `StatusDuplicate` DESCARTADO
//
// O `emit` deitava fora o [eventstore.AppendResult]. Como o duplicado vem com erro NIL,
// era indistinguível de um commit fresco: um builder «retomado» sobre um run existente
// fazia `AddNode`+`MarkRunning`, ambos devolviam nil, ZERO eventos novos entravam no
// stream, e o chamador ficava com a ILUSÃO de retoma segura — para depois admitir
// arestas cegas às que já estavam duráveis.
//
// # PORQUE O `ctx` CANCELADO É A INJECÇÃO FIEL
//
// O gatilho NÃO é uma falha genérica de Append: o store deduplica por (run_id, step_id)
// ANTES de tudo e devolve `StatusDuplicate` com erro nil — o caminho feliz é são. São as
// verificações que correm ANTES da dedup que falham: `ctx.Err()`, store fechado, sem
// líder. Por isso estes testes usam o store REAL com um ctx cancelado, e não um mock que
// falha o Append — um mock provaria uma coisa que o store não faz.

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/substrate/eventstore"
)

func builderCom(t *testing.T, runID string) (*orchestrator.GraphBuilder, *eventstore.Store) {
	t.Helper()
	es := newStore(t)
	b, err := orchestrator.NewGraphBuilder(es, runID, eventstore.Producer{NHIID: "nhi:teste"})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	return b, es
}

// TestBuilderCego_ReemissaoFalhadaNaoApagaArestaDuravel é o teste que nasceu VERMELHO.
func TestBuilderCego_ReemissaoFalhadaNaoApagaArestaDuravel(t *testing.T) {
	ctx := context.Background()
	b, es := builderCom(t, "run-reemissao")

	for _, id := range []string{"a", "b"} {
		if err := b.AddNode(ctx, orchestrator.NodeSpec{TaskID: id}); err != nil {
			t.Fatalf("AddNode %q: %v", id, err)
		}
	}
	if err := b.AddEdge(ctx, "a", "b"); err != nil {
		t.Fatalf("AddEdge a→b: %v", err)
	}

	// Reemissão da MESMA aresta com o ctx morto — um retry que reutiliza um contexto já
	// cancelado. O `ctx.Err()` do store dispara ANTES da deduplicação.
	morto, cancel := context.WithCancel(ctx)
	cancel()
	if err := b.AddEdge(morto, "a", "b"); err == nil {
		t.Fatal("esperava erro do append com ctx cancelado")
	}

	// A INVARIANTE: a aresta estava DURÁVEL e não podia sair da memória.
	if !b.DAG().Reachable("a", "b") {
		t.Fatal("a aresta a→b DESAPARECEU da memória — revert de uma mutação que não houve")
	}
	// E a consequência que isso destrancava: com a→b fora da memória, a inversa deixaria
	// de parecer um ciclo e seria admitida E PERSISTIDA.
	if err := b.AddEdge(ctx, "b", "a"); !errors.Is(err, orchestrator.ErrEdgeClosesCycle) {
		t.Fatalf("b→a devia ser rejeitada por ciclo, obtive: %v", err)
	}
	// O log tem de continuar reconstruível. Era aqui que o run morria para sempre.
	if _, err := orchestrator.RebuildDAG(ctx, es, "run-reemissao"); err != nil {
		t.Fatalf("o run ficou IRRECONSTRUÍVEL: %v", err)
	}
}

// TestBuilderCego_RetomaSobreRunExistenteEDenunciada cobre a segunda metade: um builder
// novo sobre um run que já existe no log deixa de devolver nil em silêncio.
func TestBuilderCego_RetomaSobreRunExistenteEDenunciada(t *testing.T) {
	ctx := context.Background()
	const runID = "run-retoma"
	b1, es := builderCom(t, runID)

	if err := b1.AddNode(ctx, orchestrator.NodeSpec{TaskID: "a"}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if err := b1.MarkRunning(ctx, "a"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}

	// Retoma após crash: builder NOVO sobre o MESMO run. O DAG nasce vazio e cego.
	b2, err := orchestrator.NewGraphBuilder(es, runID, eventstore.Producer{NHIID: "nhi:teste"})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	if err := b2.AddNode(ctx, orchestrator.NodeSpec{TaskID: "a"}); !errors.Is(err, orchestrator.ErrLogAhead) {
		t.Fatalf("AddNode num run existente devia denunciar ErrLogAhead, obtive: %v", err)
	}
	if err := b2.MarkRunning(ctx, "a"); !errors.Is(err, orchestrator.ErrLogAhead) {
		t.Fatalf("MarkRunning num run existente devia denunciar ErrLogAhead, obtive: %v", err)
	}
}

// TestBuilderCego_ReemissaoIdempotenteContinuaAPassar é a metade que impede a guarda de
// virar um falso positivo. Uma reemissão com a aresta JÁ em memória é retry legítimo — o
// `StatusDuplicate` é esperado e não é anomalia nenhuma.
func TestBuilderCego_ReemissaoIdempotenteContinuaAPassar(t *testing.T) {
	ctx := context.Background()
	b, _ := builderCom(t, "run-idempotente")

	for _, id := range []string{"a", "b"} {
		if err := b.AddNode(ctx, orchestrator.NodeSpec{TaskID: id}); err != nil {
			t.Fatalf("AddNode %q: %v", id, err)
		}
	}
	if err := b.AddEdge(ctx, "a", "b"); err != nil {
		t.Fatalf("1ª AddEdge: %v", err)
	}
	if err := b.AddEdge(ctx, "a", "b"); err != nil {
		t.Fatalf("a reemissão idempotente da MESMA aresta devia passar: %v", err)
	}
	if !b.DAG().Reachable("a", "b") {
		t.Fatal("a aresta desapareceu numa reemissão bem-sucedida")
	}
}

// TestBuilderCego_ArestaNovaFalhadaERevertida é a metade SIMÉTRICA, e sem ela a guarda
// não estava provada: uma mutação que fizesse o `HasEdge` devolver sempre `true`
// passava em todos os outros testes deste ficheiro.
//
// Se a aresta é NOVA e o emit falha, ela TEM de sair da memória — senão o DAG vivo
// afirma uma dependência que o log não tem, e a divergência é na outra direcção. O
// revert está certo; o que estava errado era corrê-lo também sobre arestas que já eram
// duráveis.
func TestBuilderCego_ArestaNovaFalhadaERevertida(t *testing.T) {
	ctx := context.Background()
	b, es := builderCom(t, "run-aresta-nova")

	for _, id := range []string{"a", "b", "c"} {
		if err := b.AddNode(ctx, orchestrator.NodeSpec{TaskID: id}); err != nil {
			t.Fatalf("AddNode %q: %v", id, err)
		}
	}
	if err := b.AddEdge(ctx, "a", "b"); err != nil {
		t.Fatalf("AddEdge a→b: %v", err)
	}

	morto, cancel := context.WithCancel(ctx)
	cancel()
	if err := b.AddEdge(morto, "b", "c"); err == nil {
		t.Fatal("esperava erro do append com ctx cancelado")
	}
	if b.DAG().Reachable("b", "c") {
		t.Fatal("a aresta NOVA b→c ficou na memória sem estar no log — o DAG vivo afirma uma dependência que o log não tem")
	}
	// E o log não pode ter ganho nada.
	d, err := orchestrator.RebuildDAG(ctx, es, "run-aresta-nova")
	if err != nil {
		t.Fatalf("RebuildDAG: %v", err)
	}
	if d.Reachable("b", "c") {
		t.Fatal("a aresta b→c chegou ao log apesar de o Append ter falhado")
	}
}
