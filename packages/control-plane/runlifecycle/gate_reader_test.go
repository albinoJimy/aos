package runlifecycle_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/control-plane/runlifecycle"
	"github.com/aos-ref/substrate/eventstore"
)

// TestGateReader_MaterializadoDerivaDoLog prova que a porta Gate de produção deriva o
// estado do log: fechada até `plan.materialized` ser apenso, aberta depois. É a
// não-vacuidade — o gate abre por causa do facto, não por omissão.
func TestGateReader_MaterializadoDerivaDoLog(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	const plano = "plan-gate"

	gr, err := runlifecycle.NewGateReader(store, plano)
	if err != nil {
		t.Fatalf("NewGateReader: %v", err)
	}

	// Antes de materializar: fechado.
	snap, err := gr.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if ok, err := snap.Materialized(ctx, plano); err != nil || ok {
		t.Fatalf("gate reportou materializado sem plan.materialized (ok=%v err=%v)", ok, err)
	}

	// Apensa `plan.materialized` ao stream do plano.
	if _, err := store.Append(ctx, plano, eventstore.EventInput{
		Type:     plannerevents.EventMaterialized,
		RunID:    "run-gate",
		StepID:   "planstep:materialized",
		Payload:  json.RawMessage(`{"plan_id":"plan-gate"}`),
		Producer: eventstore.Producer{NHIID: "nhi:orq"},
	}); err != nil {
		t.Fatalf("append plan.materialized: %v", err)
	}

	// Snapshot NOVO (retrato por passagem): agora abre.
	snap2, err := gr.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot 2: %v", err)
	}
	if ok, err := snap2.Materialized(ctx, plano); err != nil || !ok {
		t.Fatalf("gate não abriu após plan.materialized (ok=%v err=%v)", ok, err)
	}
}

// TestGateReader_PlanoEstrangeiroFailClosed — um leitor amarrado a um plano recusa
// responder por outro (nunca um false silencioso que faria um plano materializado
// noutro stream parecer por-materializar).
func TestGateReader_PlanoEstrangeiroFailClosed(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	gr, err := runlifecycle.NewGateReader(store, "plan-a")
	if err != nil {
		t.Fatalf("NewGateReader: %v", err)
	}
	snap, err := gr.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if _, err := snap.Materialized(ctx, "plan-b"); !errors.Is(err, runlifecycle.ErrForeignPlan) {
		t.Fatalf("esperava ErrForeignPlan para plano estrangeiro, veio %v", err)
	}
}
