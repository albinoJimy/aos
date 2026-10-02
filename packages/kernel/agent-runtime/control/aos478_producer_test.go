package control_test

import (
	"context"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/control"
)

// TestAOS478_ResumeLevaOEmissor (AOS-478): `control.resume` é um acto de operador como a
// pausa e o steer — o envelope leva o emissor autenticado, o mesmo `emitter_id` do payload.
func TestAOS478_ResumeLevaOEmissor(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	ch := newChannel(t, st, a)
	runID := "run-aos478-resume"
	_, gate := runningMachine(t, st, runID)
	if err := ch.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.GracefulPause(ctx, runID, gate); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Resume(ctx, runID, signed(t, a, runID, control.SignalResume, nil), gate); err != nil {
		t.Fatal(err)
	}
	evs, err := st.Read(ctx, runID, 1)
	if err != nil {
		t.Fatal(err)
	}
	var vistos int
	for _, ev := range evs {
		if ev.Type != control.EventTypeControlResume {
			continue
		}
		vistos++
		if ev.Producer.NHIID != testEmitter {
			t.Fatalf("control.resume producer=%+v, quer o emissor %q", ev.Producer, testEmitter)
		}
	}
	if vistos != 1 {
		t.Fatalf("control.resume vistos=%d, quer 1", vistos)
	}
}
