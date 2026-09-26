package activity_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/activity"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-069/ADR-034 — o taint da AUTORIZAÇÃO atravessa o contrato de activity.
//
// Até à fase 1 em produção (2026-09-26) o `toCall` fixava untrusted: com o TaintGate armado
// para a capability, NENHUMA activity passava — nem a de um contexto só com o objectivo. O
// rótulo passa a ser o [activity.Activity.AuthorizationTaint], cujo valor-zero continua a ser
// untrusted (fail-closed para quem não o conhece).
func TestDispatch_AuthorizationTaintChegaAoTaintGate(t *testing.T) {
	casos := []struct {
		nome   string
		rotulo taint.Label
		permit bool
	}{
		{"trusted passa o TaintGate", taint.Trusted, true},
		{"untrusted e negado pelo TaintGate", taint.Untrusted, false},
	}
	for i, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			store, err := eventstore.New()
			if err != nil {
				t.Fatalf("eventstore.New: %v", err)
			}
			defer store.Close()
			spy := &spyTool{}
			rm := referencemonitor.New(
				referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)),
				referencemonitor.WithHooks(referencemonitor.NewTaintGate(referencemonitor.NewStaticPrivilegedSet("cap:spy"))),
			)
			if err := rm.Register(testTool, spy.fn); err != nil {
				t.Fatalf("Register: %v", err)
			}
			ledger, err := durable.NewStepLedger(store)
			if err != nil {
				t.Fatalf("NewStepLedger: %v", err)
			}
			d, err := activity.NewDispatcher(rm, ledger)
			if err != nil {
				t.Fatalf("NewDispatcher: %v", err)
			}
			act := baseActivity()
			act.StepID = testStep + "-" + string(rune('a'+i))
			act.AuthorizationTaint = c.rotulo
			_, err = d.Dispatch(context.Background(), act)
			if c.permit {
				if err != nil || spy.calls.Load() != 1 {
					t.Fatalf("activity trusted devia passar e executar: err=%v execs=%d", err, spy.calls.Load())
				}
				return
			}
			var md *activity.MediationDenial
			if !errors.As(err, &md) || md.DeniedBy != "taint" || spy.calls.Load() != 0 {
				t.Fatalf("activity untrusted devia ser negada pelo taint sem executar: err=%v execs=%d", err, spy.calls.Load())
			}
		})
	}
}

// TestActivity_AuthorizationTaintValorZeroEUntrusted fixa o fail-closed do campo novo.
func TestActivity_AuthorizationTaintValorZeroEUntrusted(t *testing.T) {
	if (activity.Activity{}).AuthorizationTaint != taint.Untrusted {
		t.Fatal("o valor-zero de AuthorizationTaint tem de ser untrusted (fail-closed)")
	}
}
