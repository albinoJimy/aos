package activity_test

import (
	"context"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/activity"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// AOS-454 — o passo pai atravessa a Activity até ao Call que o RM medeia, e não entra na
// identidade da activity.

// TestAOS454_ParentStepIDChegaAoEventoDeMediacao: o parent_step_id da Activity aparece no evento
// tool.call.mediated. Até AOS-454 `toCall` não o tinha por onde levar e o evento saía sem ele.
func TestAOS454_ParentStepIDChegaAoEventoDeMediacao(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	d, err := activity.NewDispatcher(h.rm, h.ledger)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	act := baseActivity()
	act.ParentStepID = "step-000001"
	if _, err := d.Dispatch(context.Background(), act); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	evs, err := h.store.Read(context.Background(), testRun, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var pais []string
	for _, ev := range evs {
		if ev.Type == referencemonitor.EventTypeMediated {
			pais = append(pais, ev.ParentStepID)
		}
	}
	if len(pais) != 1 || pais[0] != "step-000001" {
		t.Fatalf("o evento de mediação devia levar parent_step_id=step-000001, levou %q", pais)
	}
}

// TestAOS454_ParentStepIDForaDaChaveDeIdempotencia: o mesmo (RunID, StepID) com outro pai é o
// MESMO efeito lógico — deduplica, não re-executa, e o ledger não o recusa como acção diferente.
// Se o pai entrasse na chave, uma retoma que o reconstruísse de outra forma duplicaria o efeito.
func TestAOS454_ParentStepIDForaDaChaveDeIdempotencia(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	d, err := activity.NewDispatcher(h.rm, h.ledger)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	ctx := context.Background()
	primeira := baseActivity()
	primeira.ParentStepID = "step-000001"
	if _, err := d.Dispatch(ctx, primeira); err != nil {
		t.Fatalf("Dispatch #1: %v", err)
	}
	segunda := baseActivity()
	segunda.ParentStepID = "outro-pai"
	res, err := d.Dispatch(ctx, segunda)
	if err != nil {
		t.Fatalf("Dispatch #2 (mesmo passo, outro pai): %v", err)
	}
	if !res.Deduplicated {
		t.Fatalf("mesmo (RunID, StepID) com outro pai devia deduplicar: %+v", res)
	}
	if got := h.spy.calls.Load(); got != 1 {
		t.Fatalf("o efeito devia correr 1x, correu %d", got)
	}
}
