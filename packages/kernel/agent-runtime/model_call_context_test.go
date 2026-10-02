package agentruntime_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// TestAOS394_ModelCallFromContext_SemAnexoDevolveVazio: sem anexo não há correlação, e o leitor
// não recebe um valor inventado.
func TestAOS394_ModelCallFromContext_SemAnexoDevolveVazio(t *testing.T) {
	t.Parallel()
	run, step, ok := agentruntime.ModelCallFromContext(context.Background())
	if ok || run != "" || step != "" {
		t.Fatalf("sem anexo esperava (\"\", \"\", false), veio (%q, %q, %v)", run, step, ok)
	}
	ctx := agentruntime.ContextWithModelCall(context.Background(), "run-x", "step-000009")
	if run, step, ok = agentruntime.ModelCallFromContext(ctx); !ok || run != "run-x" || step != "step-000009" {
		t.Fatalf("anexo lido = (%q, %q, %v), esperava (run-x, step-000009, true)", run, step, ok)
	}
	// O `ok` é sobre o PAR: um anexo com o run vazio continua a ser um anexo, e quem lê não pode
	// completá-lo com um run de outra fonte.
	vazio := agentruntime.ContextWithModelCall(context.Background(), "", "step-000010")
	if run, step, ok = agentruntime.ModelCallFromContext(vazio); !ok || run != "" || step != "step-000010" {
		t.Fatalf("anexo com run vazio = (%q, %q, %v), esperava (\"\", step-000010, true)", run, step, ok)
	}
}

// TestAOS394_CallModel_AnexaRunEPassoDeCadaTurno: o ModelClient recebe, em cada turno, o run do
// goal e o step_id desse turno — o mesmo que o turn.recorded grava. É o que o adaptador do Model
// Gateway lê para selar a chamada.
func TestAOS394_CallModel_AnexaRunEPassoDeCadaTurno(t *testing.T) {
	t.Parallel()
	const runID = "run-aos394"
	type visto struct{ run, step string }
	var vistos []visto
	mc := agentruntime.ModelClientFunc(func(ctx context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		run, step, ok := agentruntime.ModelCallFromContext(ctx)
		if !ok {
			t.Errorf("turno %d: o ModelClient nao recebeu anexo de correlacao", view.Turn)
		}
		vistos = append(vistos, visto{run, step})
		return agentruntime.ModelResponse{Text: "ok", Final: view.Turn >= 2}, nil
	})

	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rt := agentruntime.New(mc, referencemonitor.New(), agentruntime.NewTurnRecorder(store))
	res, err := rt.Run(context.Background(), agentruntime.Goal{
		RunID:     runID,
		Principal: referencemonitor.Principal{NHIID: "nhi:teste"},
		System:    "sistema",
		Objective: "dois turnos",
		MaxTurns:  3,
	})
	if err != nil && !errors.Is(err, agentruntime.ErrMaxTurnsExceeded) {
		t.Fatalf("Run: %v", err)
	}
	if len(vistos) == 0 || len(vistos) != res.Turns {
		t.Fatalf("chamadas ao modelo = %d, turnos = %d", len(vistos), res.Turns)
	}
	for i, v := range vistos {
		quer := fmt.Sprintf("step-%06d", i+1)
		if v.run != runID || v.step != quer {
			t.Fatalf("turno %d: o ModelClient viu (%q, %q), esperava (%q, %q)", i+1, v.run, v.step, runID, quer)
		}
	}

	// O passo anexado é o MESMO que o evento durável do turno grava.
	eventos, err := store.Read(context.Background(), runID, 0)
	if err != nil {
		t.Fatalf("store.Read: %v", err)
	}
	gravados := 0
	for _, ev := range eventos {
		if ev.Type != "turn.recorded" {
			continue
		}
		if ev.StepID != vistos[gravados].step {
			t.Fatalf("turn.recorded #%d tem step_id %q, o ModelClient viu %q", gravados+1, ev.StepID, vistos[gravados].step)
		}
		gravados++
	}
	if gravados != len(vistos) {
		t.Fatalf("turn.recorded = %d, chamadas ao modelo = %d", gravados, len(vistos))
	}
}
