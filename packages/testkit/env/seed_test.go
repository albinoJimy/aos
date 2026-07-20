package env_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/testkit/env"
)

// TestSeedTrajectory_KnownAndDeterministic cobre AC5: o seed popula o Event Store
// com uma trajectória CONHECIDA (2 eventos por turno) e os payloads são
// deterministas (prompt_hash/model/seed derivados de (run_id, turn)).
func TestSeedTrajectory_KnownAndDeterministic(t *testing.T) {
	t.Parallel()
	e := env.New(t, env.WithEventStore())
	steps := e.SeedTrajectory("run-seed", 3)

	if len(steps) != 6 {
		t.Fatalf("esperava 6 passos (3 turnos x 2 eventos), obtive %d", len(steps))
	}
	// Ordem e tipos: recorded, replay, recorded, replay, ...
	wantTypes := []string{"turn.recorded", "replay.captured"}
	for i, s := range steps {
		if s.EventType != wantTypes[i%2] {
			t.Fatalf("passo %d: tipo %s, esperava %s", i, s.EventType, wantTypes[i%2])
		}
		if s.AppendResult.Status != eventstore.StatusCommitted {
			t.Fatalf("passo %d devia commit-ar, obtive %s", i, s.AppendResult.Status)
		}
	}
	// Payload determinista do primeiro turno.
	got, err := e.EventStore.Read(context.Background(), "run-seed", 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var p env.TurnPayload
	if err := json.Unmarshal(got[0].Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Turn != 1 || p.Model != env.ModelRef || p.Seed != 1 || p.PromptHash != "ph-run-seed-001" {
		t.Fatalf("payload determinista inesperado: %+v", p)
	}
}

// TestSeedTrajectory_ReplayDeduplicates cobre a base de AOS-111/112: re-semear a
// MESMA trajectória reproduz as MESMAS idempotency keys e o Event Store deduplica
// (StatusDuplicate com o seq original) — sem duplicar história.
func TestSeedTrajectory_ReplayDeduplicates(t *testing.T) {
	t.Parallel()
	e := env.New(t, env.WithEventStore())

	first := e.SeedTrajectory("run-replay", 3)
	replay := e.SeedTrajectory("run-replay", 3) // MESMA trajectória, re-aplicada

	if len(first) != len(replay) {
		t.Fatalf("comprimentos divergem: %d != %d", len(first), len(replay))
	}
	for i := range first {
		if replay[i].AppendResult.Status != eventstore.StatusDuplicate {
			t.Fatalf("replay do passo %d devia deduplicar, obtive %s", i, replay[i].AppendResult.Status)
		}
		if replay[i].AppendResult.Seq != first[i].AppendResult.Seq {
			t.Fatalf("replay do passo %d: seq %d != original %d", i, replay[i].AppendResult.Seq, first[i].AppendResult.Seq)
		}
		if replay[i].StepID != first[i].StepID {
			t.Fatalf("replay do passo %d: step_id %q != original %q", i, replay[i].StepID, first[i].StepID)
		}
	}
	// O stream continua com exactamente 6 eventos (o replay não acrescentou).
	got, err := e.EventStore.Read(context.Background(), "run-replay", 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("replay duplicou historia: stream tem %d eventos (esperava 6)", len(got))
	}
}

// TestSeedTrajectory_PublishedOnBus confirma que a trajectória semeada flui pelo
// transporte PUSH: o TAP do bus captura todos os eventos committed.
func TestSeedTrajectory_PublishedOnBus(t *testing.T) {
	t.Parallel()
	e := env.New(t, env.WithBus())
	e.SeedTrajectory("run-bus", 2)

	// Espera determinista: o push é assíncrono; aguarda a entrega dos 4 eventos.
	waitFor(t, func() bool { return e.Bus.Count() >= 4 })
	if got := e.Bus.Count(); got != 4 {
		t.Fatalf("TAP do bus devia capturar 4 eventos, capturou %d", got)
	}
	for _, ev := range e.Bus.Received() {
		if ev.Type != "turn.recorded" && ev.Type != "replay.captured" {
			t.Fatalf("evento inesperado no bus: %s", ev.Type)
		}
	}
}

// TestSeedEvent_LowLevel cobre o helper de baixo nível SeedEvent.
func TestSeedEvent_LowLevel(t *testing.T) {
	t.Parallel()
	e := env.New(t, env.WithEventStore())
	res := e.SeedEvent("run-le", "custom.event", 1, map[string]int{"k": 7})
	if res.Status != eventstore.StatusCommitted {
		t.Fatalf("esperava committed, obtive %s", res.Status)
	}
	// Payload nil também é aceite.
	res2 := e.SeedEvent("run-le", "custom.event2", 2, nil)
	if res2.Status != eventstore.StatusCommitted {
		t.Fatalf("payload nil devia commit-ar, obtive %s", res2.Status)
	}
}
