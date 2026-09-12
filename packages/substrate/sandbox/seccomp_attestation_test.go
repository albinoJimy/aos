package sandbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/sandbox/seccomp"
)

// TestManifest_SeccompEnforcementQualifiedPerDriver é o teste de AOS-351: o evento
// selado no WORM não pode AFIRMAR uma imposição que não houve.
//
// O [Launcher] propaga o MESMO perfil a todos os drivers, mas só o [FakeDriver] o
// lê e o aplica no Exec; o [FirecrackerDriver]/[GVisorDriver] recebem a [Spec] e
// ignoram [Spec.Seccomp] — o [GuestExecutor] nem sequer o transporta. O manifesto
// tem, por isso, de distinguir imposição ("driver") de mera declaração de config
// ("none") em CADA uma das três fases do ciclo de vida.
func TestManifest_SeccompEnforcementQualifiedPerDriver(t *testing.T) {
	cases := []struct {
		name   string
		driver SandboxDriver
		runID  string
		want   SeccompEnforcement
	}{
		{
			name:   "firecracker não impõe o perfil (só o declara)",
			driver: NewFirecrackerDriver(WithFirecrackerExecutor(echoExecutor{})),
			runID:  "run-351-fc",
			want:   SeccompEnforcedByNone,
		},
		{
			name:   "gvisor não impõe o perfil (só o declara)",
			driver: NewGVisorDriver(WithGVisorExecutor(echoExecutor{})),
			runID:  "run-351-gv",
			want:   SeccompEnforcedByNone,
		},
		{
			name:   "fake impõe o perfil no Exec",
			driver: NewFakeDriver(),
			runID:  "run-351-fake",
			want:   SeccompEnforcedByDriver,
		},
	}

	prof, err := seccomp.Load()
	if err != nil {
		t.Fatalf("seccomp.Load: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newStore(t)
			rt := &recordingTracer{}
			launcher, err := NewLauncher(tc.driver,
				WithEventSink(NewEventStoreSink(store)),
				WithTracer(rt),
			)
			if err != nil {
				t.Fatalf("NewLauncher: %v", err)
			}
			ml, err := NewMediatedLauncher(newPermitMonitor(store), launcher, "sandbox.exec")
			if err != nil {
				t.Fatalf("NewMediatedLauncher: %v", err)
			}
			req := ExecRequest{RunID: tc.runID, StepID: "step-351", Call: ToolCall{ToolID: "t", Command: "echo"}}
			if _, err := ml.Execute(context.Background(), defaultAuthz(), req); err != nil {
				t.Fatalf("Execute: %v", err)
			}

			evs := readEvents(t, store, tc.runID)
			for _, typ := range []string{EventInstanceCreated, EventExecCompleted, EventInstanceDestroyed} {
				matched := eventsOfType(evs, typ)
				if len(matched) == 0 {
					t.Fatalf("nenhum evento do tipo %q", typ)
				}
				for _, e := range matched {
					var p lifecyclePayload
					if err := json.Unmarshal(e.Payload, &p); err != nil {
						t.Fatalf("unmarshal payload %q: %v", typ, err)
					}
					if p.SeccompProfileHash != prof.Hash() {
						t.Fatalf("%q seccomp_profile_hash = %q, quero %q", typ, p.SeccompProfileHash, prof.Hash())
					}
					if p.SeccompEnforcedBy != string(tc.want) {
						t.Fatalf("%q seccomp_enforced_by = %q, quero %q — o evento afirma uma imposição diferente da real",
							typ, p.SeccompEnforcedBy, tc.want)
					}
					// A qualificação tem de estar no JSON selado, não só no struct:
					// é o que um leitor do WORM lê.
					if !strings.Contains(string(e.Payload), `"seccomp_enforced_by":"`+string(tc.want)+`"`) {
						t.Fatalf("%q payload selado sem seccomp_enforced_by=%q: %s", typ, tc.want, e.Payload)
					}
				}
			}

			// O span faz a MESMA afirmação que o evento — um hash nu no span seria
			// lido como imposição tal como no WORM.
			v, ok := rt.attr(AttrSeccompEnforcedBy)
			if !ok {
				t.Fatal("span sem AttrSeccompEnforcedBy")
			}
			if v.(string) != string(tc.want) {
				t.Fatalf("span AttrSeccompEnforcedBy = %v, quero %q", v, tc.want)
			}
		})
	}
}

// TestManifest_SeccompHashNeverSealedUnqualified prova a invariante estrutural que
// sustenta AOS-351 no sink: onde há hash no payload, há SEMPRE um
// seccomp_enforced_by explícito. Um hash nu é indistinguível de uma atestação.
func TestManifest_SeccompHashNeverSealedUnqualified(t *testing.T) {
	cases := []struct {
		name  string
		ev    LifecycleEvent
		want  string // valor esperado de seccomp_enforced_by ("" = campo ausente)
		runID string
	}{
		{
			name:  "hash sem qualificação degrada para none (fail-closed)",
			ev:    LifecycleEvent{Phase: PhaseExec, SeccompProfileHash: "sha256:abc"},
			want:  string(SeccompEnforcedByNone),
			runID: "run-351-q1",
		},
		{
			name:  "valor desconhecido degrada para none (fail-closed)",
			ev:    LifecycleEvent{Phase: PhaseExec, SeccompProfileHash: "sha256:abc", SeccompEnforcedBy: SeccompEnforcement("guest")},
			want:  string(SeccompEnforcedByNone),
			runID: "run-351-q2",
		},
		{
			name:  "imposição pelo driver é preservada",
			ev:    LifecycleEvent{Phase: PhaseExec, SeccompProfileHash: "sha256:abc", SeccompEnforcedBy: SeccompEnforcedByDriver},
			want:  string(SeccompEnforcedByDriver),
			runID: "run-351-q3",
		},
		{
			name:  "sem hash não há nada a qualificar",
			ev:    LifecycleEvent{Phase: PhaseExec, SeccompEnforcedBy: SeccompEnforcedByDriver},
			want:  "",
			runID: "run-351-q4",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newStore(t)
			sink := NewEventStoreSink(store)
			ev := tc.ev
			ev.RunID, ev.StepID = tc.runID, "step-q"
			if _, err := sink.RecordLifecycle(context.Background(), ev); err != nil {
				t.Fatalf("RecordLifecycle: %v", err)
			}
			evs := readEvents(t, store, tc.runID)
			if len(evs) != 1 {
				t.Fatalf("eventos = %d, quero 1", len(evs))
			}
			var p lifecyclePayload
			if err := json.Unmarshal(evs[0].Payload, &p); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if p.SeccompEnforcedBy != tc.want {
				t.Fatalf("seccomp_enforced_by = %q, quero %q", p.SeccompEnforcedBy, tc.want)
			}
			raw := string(evs[0].Payload)
			if strings.Contains(raw, "seccomp_profile_hash") && !strings.Contains(raw, "seccomp_enforced_by") {
				t.Fatalf("hash selado sem qualificação: %s", raw)
			}
		})
	}
}
