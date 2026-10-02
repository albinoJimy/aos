package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// TestAOS478_PromptDeExaustaoLevaIdentidadeDeComponente (AOS-478): o pendente de exaustão
// (`approval.pending` com `kind=exhaustion`) é levantado pelo NÓ, e o envelope di-lo com
// `nhi:aos-node/exhaustion-prompt` em vez de vir vazio. Mesma composição real do AOS-263
// (ledger de turnos → burn-down → prompt → pendentes + gate de estado), com o Event Store à mão.
func TestAOS478_PromptDeExaustaoLevaIdentidadeDeComponente(t *testing.T) {
	ctx := context.Background()
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = es.Close() })
	rb, err := integration.NewRunBudget(1000)
	if err != nil {
		t.Fatal(err)
	}
	pend, err := integration.NewPendingApprovals(es)
	if err != nil {
		t.Fatal(err)
	}
	resumeRecords, err := integration.NewResumeRecords(es, nil)
	if err != nil {
		t.Fatal(err)
	}
	log := func(string, ...any) {}
	gates := newRunStateGates(es, nil, 0)
	auth, worm := aos263RotaDeDecisao(t, es)
	prompt, err := newExhaustionPrompt(gates, pend, resumeRecords, auth, worm, log)
	if err != nil || prompt == nil {
		t.Fatalf("newExhaustionPrompt: %v (nil=%t)", err, prompt == nil)
	}
	prog := newRunProgress(gates, rb, newTurnLedgerBurndown(es), otelgenai.NoopTracer{}, 0.80, log, prompt)
	rec := agentruntime.NewTurnRecorder(es)

	const run = "run-aos478-exaustao"
	if err := gates.Open(ctx, run, state.Uint64Token(1)); err != nil {
		t.Fatal(err)
	}
	if err := gates.resolveGate(run).claimRunning(ctx); err != nil {
		t.Fatal(err)
	}
	gravaTurno(t, rec, run, fmt.Sprintf("step-%d", 1), 1, 900, 0, 0)
	if err := prog.ObserveProgress(ctx, run, 1); !errors.Is(err, errExhaustionSuspended) {
		t.Fatalf("o limiar devia suspender o run; deu %v", err)
	}

	streams, err := es.Streams()
	if err != nil {
		t.Fatal(err)
	}
	vistos := 0
	for _, s := range streams {
		evs, _ := es.Read(ctx, s, 1)
		for _, ev := range evs {
			if ev.Type != "approval.pending" {
				continue
			}
			vistos++
			if ev.Producer.NHIID != exhaustionPromptNHI {
				t.Errorf("approval.pending (exaustão) producer=%+v, quer %q", ev.Producer, exhaustionPromptNHI)
			}
		}
	}
	if vistos != 1 {
		t.Fatalf("pendentes de exaustão vistos=%d, quer 1", vistos)
	}
}
