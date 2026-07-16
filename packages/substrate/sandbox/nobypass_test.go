package sandbox

import (
	"context"
	"errors"
	"testing"
)

// TestNoBypass_DenyNeverReachesDriver prova que, quando o RM NEGA, nenhum efeito de
// sandbox ocorre: o driver nunca é criado nem executado (no-bypass, ADR-002).
func TestNoBypass_DenyNeverReachesDriver(t *testing.T) {
	store := newStore(t)
	cd := &countingDriver{SandboxDriver: NewFakeDriver()}
	launcher, err := NewLauncher(cd, WithEventSink(NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	rm := newDenyMonitor(store)
	ml, err := NewMediatedLauncher(rm, launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	_, err = ml.Execute(context.Background(), defaultAuthz(), ExecRequest{
		RunID: "run-deny", StepID: "step-deny", Call: ToolCall{Command: "x"},
	})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v, esperado *DeniedError", err)
	}
	if cd.creates != 0 || cd.execs != 0 {
		t.Fatalf("driver alcancado sob deny: create=%d exec=%d (esperado 0/0)", cd.creates, cd.execs)
	}
}

// TestNoBypass_DriverReachedOnlyUnderPermit prova que o nº de criações do driver
// iguala o nº de permits do RM — o driver só é alcançado através da mediação.
func TestNoBypass_DriverReachedOnlyUnderPermit(t *testing.T) {
	store := newStore(t)
	cd := &countingDriver{SandboxDriver: NewFakeDriver()}
	launcher, err := NewLauncher(cd, WithEventSink(NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	rm := newPermitMonitor(store)
	ml, err := NewMediatedLauncher(rm, launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	const n = 3
	for i := 0; i < n; i++ {
		if _, err := ml.Execute(context.Background(), defaultAuthz(), ExecRequest{
			RunID: "run-p", StepID: "step-" + string(rune('a'+i)), Call: ToolCall{Command: "ok"},
		}); err != nil {
			t.Fatalf("Execute[%d]: %v", i, err)
		}
	}
	permits, _, _ := rm.Metrics().Snapshot()
	if permits != n {
		t.Fatalf("permits = %d, esperado %d", permits, n)
	}
	if cd.creates != n || cd.execs != n || cd.destroys != n {
		t.Fatalf("driver create/exec/destroy = %d/%d/%d, esperado %d cada", cd.creates, cd.execs, cd.destroys, n)
	}
}

// TestNoBypass_MediationEventPrecedesLifecycle prova que o evento de mediação do RM
// (tool.call.mediated) precede os eventos de ciclo de vida da sandbox — a sandbox
// só corre DEPOIS de o RM permitir.
func TestNoBypass_MediationEventPrecedesLifecycle(t *testing.T) {
	store := newStore(t)
	launcher, err := NewLauncher(NewFakeDriver(), WithEventSink(NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	rm := newPermitMonitor(store)
	ml, err := NewMediatedLauncher(rm, launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}
	if _, err := ml.Execute(context.Background(), defaultAuthz(), ExecRequest{
		RunID: "run-seq", StepID: "step-seq", Call: ToolCall{Command: "ok"},
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	evs := readEvents(t, store, "run-seq")
	mediated := eventsOfType(evs, "tool.call.mediated")
	created := eventsOfType(evs, EventInstanceCreated)
	if len(mediated) != 1 || len(created) != 1 {
		t.Fatalf("mediated=%d created=%d, esperado 1/1", len(mediated), len(created))
	}
	if mediated[0].Seq >= created[0].Seq {
		t.Fatalf("mediado seq %d nao precede created seq %d", mediated[0].Seq, created[0].Seq)
	}
}
