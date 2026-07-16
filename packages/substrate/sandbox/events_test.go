package sandbox

import (
	"context"
	"encoding/json"
	"testing"
)

// TestEvents_LifecycleRecordedWithRunStep prova o critério de evento: o ciclo de
// vida create/exec/destroy é gravado no Event Store com run_id/step_id.
func TestEvents_LifecycleRecordedWithRunStep(t *testing.T) {
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

	const runID, stepID = "run-ev", "step-ev"
	if _, err := ml.Execute(context.Background(), defaultAuthz(), ExecRequest{
		RunID: runID, StepID: stepID, Call: ToolCall{ToolID: "t", Command: "go"},
		CredentialsHandle: "cred-handle-xyz",
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	evs := readEvents(t, store, runID)

	// Uma transição de cada fase, na ordem create → exec → destroy.
	want := []string{EventInstanceCreated, EventExecCompleted, EventInstanceDestroyed}
	var order []string
	for _, e := range evs {
		for _, w := range want {
			if e.Type == w {
				order = append(order, e.Type)
			}
		}
	}
	if len(order) != 3 || order[0] != want[0] || order[1] != want[1] || order[2] != want[2] {
		t.Fatalf("ordem do ciclo de vida = %v, esperado %v", order, want)
	}

	// Cada evento do ciclo carrega run_id/step_id canónicos no payload e o envelope
	// usa run_id como stream e um step_id distinto por fase (sem dedup).
	for _, e := range evs {
		if e.Type != EventInstanceCreated && e.Type != EventExecCompleted && e.Type != EventInstanceDestroyed {
			continue
		}
		if e.RunID != runID {
			t.Fatalf("evento %s: envelope run_id = %q, esperado %q", e.Type, e.RunID, runID)
		}
		if e.StepID == stepID {
			t.Fatalf("evento %s: step_id de envelope deveria ser distinto por fase, foi %q (colidiria no dedup)", e.Type, e.StepID)
		}
		if e.ParentStepID != stepID {
			t.Fatalf("evento %s: parent_step_id = %q, esperado %q", e.Type, e.ParentStepID, stepID)
		}
		var p lifecyclePayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("payload invalido: %v", err)
		}
		if p.RunID != runID || p.StepID != stepID {
			t.Fatalf("payload run/step = %q/%q, esperado %q/%q", p.RunID, p.StepID, runID, stepID)
		}
		if p.Taint != string(TaintUntrusted) {
			t.Fatalf("payload taint = %q, esperado untrusted", p.Taint)
		}
		if !p.Isolation.NoHostSocket || !p.Isolation.NoSharedNetNS || !p.Isolation.NoSharedPIDNS {
			t.Fatalf("payload isolamento nao hardened: %+v", p.Isolation)
		}
	}
}

// TestEvents_CreatedSealedBeforeExec prova audit-before-effect: o evento created é
// selado (seq menor) antes do evento de exec.
func TestEvents_CreatedSealedBeforeExec(t *testing.T) {
	store := newStore(t)
	launcher, err := NewLauncher(NewFakeDriver(), WithEventSink(NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	if _, err := launcher.run(context.Background(), ExecRequest{
		RunID: "run-ord", StepID: "step-ord", Call: ToolCall{Command: "x"},
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	evs := readEvents(t, store, "run-ord")
	created := eventsOfType(evs, EventInstanceCreated)
	execed := eventsOfType(evs, EventExecCompleted)
	if len(created) != 1 || len(execed) != 1 {
		t.Fatalf("created=%d exec=%d, esperado 1/1", len(created), len(execed))
	}
	if created[0].Seq >= execed[0].Seq {
		t.Fatalf("created seq %d nao precede exec seq %d (audit-before-effect)", created[0].Seq, execed[0].Seq)
	}
}

// failingSink devolve erro no create (fail-closed de auditoria).
type failingSink struct{ failOn LifecyclePhase }

func (s failingSink) RecordLifecycle(_ context.Context, ev LifecycleEvent) (uint64, error) {
	if ev.Phase == s.failOn {
		return 0, context.DeadlineExceeded
	}
	return 1, nil
}

// TestEvents_AuditFailClosedOnCreate prova que, se o evento created não puder ser
// selado, o exec NÃO corre (fail-closed) mas o destroy é garantido.
func TestEvents_AuditFailClosedOnCreate(t *testing.T) {
	cd := &countingDriver{SandboxDriver: NewFakeDriver()}
	launcher, err := NewLauncher(cd, WithEventSink(failingSink{failOn: PhaseCreated}))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	if _, err := launcher.run(context.Background(), ExecRequest{
		RunID: "r", StepID: "s", Call: ToolCall{Command: "x"},
	}); err == nil {
		t.Fatal("esperado erro fail-closed de auditoria no create")
	}
	if cd.execs != 0 {
		t.Fatalf("exec correu %d vezes apesar da falha de auditoria (esperado 0)", cd.execs)
	}
	if cd.destroys != 1 {
		t.Fatalf("destroy correu %d vezes (esperado 1, garantido)", cd.destroys)
	}
}
