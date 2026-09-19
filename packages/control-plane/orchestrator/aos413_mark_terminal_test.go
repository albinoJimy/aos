package orchestrator_test

// AOS-413 (ADR-027) — a conclusão de um nó do plano é um facto durável: sem ela o despacho nunca
// via uma dependência cumprida e o organigrama parava no primeiro nó.

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/contract"
	arstate "github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

func TestAOS413_MarkTerminalFicaDuravelESobreviveAoReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore(t)
	gb, err := orchestrator.NewGraphBuilder(store, "run-413", eventstore.Producer{})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if err := gb.AddNode(ctx, orchestrator.NodeSpec{TaskID: id}); err != nil {
			t.Fatalf("AddNode %q: %v", id, err)
		}
		if err := gb.MarkRunning(ctx, id); err != nil {
			t.Fatalf("MarkRunning %q: %v", id, err)
		}
	}
	if err := gb.MarkTerminal(ctx, "a", arstate.Complete); err != nil {
		t.Fatalf("MarkTerminal a→complete: %v", err)
	}
	if err := gb.MarkTerminal(ctx, "b", arstate.Failed); err != nil {
		t.Fatalf("MarkTerminal b→failed: %v", err)
	}

	// O replay reconstitui a conclusão: é o que um `serve` retomado vê.
	dag, err := orchestrator.RebuildDAG(ctx, store, "run-413")
	if err != nil {
		t.Fatalf("RebuildDAG: %v", err)
	}
	if st, _ := dag.State("a"); st != arstate.Complete {
		t.Fatalf("a depois do replay = %s, quero complete", st)
	}
	if st, _ := dag.State("b"); st != arstate.Failed {
		t.Fatalf("b depois do replay = %s, quero failed", st)
	}
}

func TestAOS413_MarkTerminalRecusaOQueNaoEConclusao(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	gb, err := orchestrator.NewGraphBuilder(newStore(t), "run-413-recusa", eventstore.Producer{})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	if err := gb.AddNode(ctx, orchestrator.NodeSpec{TaskID: "a"}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	// Um nó que não arrancou não conclui: a tabela de AOS-017 recusa ready→complete.
	if err := gb.MarkTerminal(ctx, "a", arstate.Complete); !errors.Is(err, arstate.ErrInvalidTransition) {
		t.Fatalf("ready→complete tinha de ser recusado, veio %v", err)
	}
	if err := gb.MarkRunning(ctx, "a"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	// killed/timed_out têm autores próprios — não são «o nó acabou o seu trabalho».
	if err := gb.MarkTerminal(ctx, "a", arstate.Killed); !errors.Is(err, arstate.ErrInvalidTransition) {
		t.Fatalf("MarkTerminal para killed tinha de ser recusado, veio %v", err)
	}
	if st, _ := gb.DAG().State("a"); st != arstate.Running {
		t.Fatalf("uma recusa não pode mexer no estado: %s", st)
	}
}

func TestAOS413_MarkTerminalRevertidoSeOAppendFalha(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fs := &flakyStore{inner: newStore(t), failType: contract.EventTaskNodeStateChanged}
	gb, err := orchestrator.NewGraphBuilder(fs, "run-413-revert", eventstore.Producer{})
	if err != nil {
		t.Fatalf("NewGraphBuilder: %v", err)
	}
	if err := gb.AddNode(ctx, orchestrator.NodeSpec{TaskID: "a"}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	if err := gb.MarkRunning(ctx, "a"); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	fs.remain = 1
	if err := gb.MarkTerminal(ctx, "a", arstate.Complete); err == nil {
		t.Fatal("MarkTerminal devia falhar quando o Append falha")
	}
	if st, _ := gb.DAG().State("a"); st != arstate.Running {
		t.Fatalf("estado de 'a'=%s após falha, quero running (revert)", st)
	}
}
