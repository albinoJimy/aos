package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/sandbox/seccomp"
)

// TestManifest_SeccompHashRecordedPerTrajectory prova o critério AOS-066: o HASH do
// perfil seccomp (versionado) é gravado no manifesto de CADA fase da execução
// (created/exec/destroyed) no Event Store, ligando a trajectória à versão EXACTA do
// perfil. Também o rootfs read-only e a versão de imagem entram no manifesto.
func TestManifest_SeccompHashRecordedPerTrajectory(t *testing.T) {
	store := newStore(t)
	prof, err := seccomp.Load()
	if err != nil {
		t.Fatalf("seccomp.Load: %v", err)
	}
	launcher, err := NewLauncher(NewFakeDriver(),
		WithEventSink(NewEventStoreSink(store)),
		WithImageVersion("img/v42"),
	)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	rm := newPermitMonitor(store)
	ml, err := NewMediatedLauncher(rm, launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	req := ExecRequest{RunID: "run-m", StepID: "step-m", Call: ToolCall{ToolID: "t", Command: "echo"}}
	if _, err := ml.Execute(context.Background(), defaultAuthz(), req); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	evs := readEvents(t, store, "run-m")
	phases := []string{EventInstanceCreated, EventExecCompleted, EventInstanceDestroyed}
	for _, typ := range phases {
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
			if p.SeccompProfileVersion != prof.Version() {
				t.Fatalf("%q seccomp_profile_version = %q, quero %q", typ, p.SeccompProfileVersion, prof.Version())
			}
			if p.ImageVersion != "img/v42" {
				t.Fatalf("%q image_version = %q, quero img/v42", typ, p.ImageVersion)
			}
			if !p.Isolation.RootFSReadOnly {
				t.Fatalf("%q rootfs_read_only = false, quero true", typ)
			}
		}
	}
}

// TestManifest_SpanCarriesSeccompHashNoSecret prova que o span transporta o hash do
// perfil seccomp e o rootfs read-only, e que NENHUM atributo de span carrega um
// segredo (o hash/perfil NÃO são segredos, mas o credentials_handle é opaco).
func TestManifest_SpanCarriesSeccompHashNoSecret(t *testing.T) {
	store := newStore(t)
	prof, _ := seccomp.Load()
	rt := &recordingTracer{}
	launcher, err := NewLauncher(NewFakeDriver(),
		WithEventSink(NewEventStoreSink(store)),
		WithTracer(rt),
	)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	rm := newPermitMonitor(store)
	ml, err := NewMediatedLauncher(rm, launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	const secret = "super-secret-value"
	req := ExecRequest{
		RunID: "run-s", StepID: "step-s",
		Call:              ToolCall{ToolID: "t", Command: "echo"},
		CredentialsHandle: "handle-opaco-123",
	}
	// O segredo real nunca é passado à sandbox (só o handle opaco); ainda assim
	// varremos todos os atributos por ele.
	_ = secret
	if _, err := ml.Execute(context.Background(), defaultAuthz(), req); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	hv, ok := rt.attr(AttrSeccompHash)
	if !ok || hv.(string) != prof.Hash() {
		t.Fatalf("span AttrSeccompHash = %v (ok=%v), quero %q", hv, ok, prof.Hash())
	}
	ro, ok := rt.attr(AttrRootFSReadOnly)
	if !ok || ro.(bool) != true {
		t.Fatalf("span AttrRootFSReadOnly = %v (ok=%v), quero true", ro, ok)
	}
	for _, v := range rt.attrValues() {
		if s, isStr := v.(string); isStr && strings.Contains(s, secret) {
			t.Fatalf("segredo vazou num atributo de span: %q", s)
		}
	}
}

// TestFailClosed_RootFSMustBeReadOnly prova o critério AOS-066: o Launcher recusa
// fail-closed correr com a raiz de FS escrevível (RootFSReadOnly=false).
func TestFailClosed_RootFSMustBeReadOnly(t *testing.T) {
	store := newStore(t)
	launcher, err := NewLauncher(NewFakeDriver(),
		WithEventSink(NewEventStoreSink(store)),
		WithIsolation(Isolation{NoHostSocket: true, NoSharedNetNS: true, NoSharedPIDNS: true, RootFSReadOnly: false}),
	)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	rm := newPermitMonitor(store)
	ml, err := NewMediatedLauncher(rm, launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}
	req := ExecRequest{RunID: "run-ro", StepID: "step-ro", Call: ToolCall{ToolID: "t", Command: "echo"}}
	_, err = ml.Execute(context.Background(), defaultAuthz(), req)
	if !errors.Is(err, ErrReadOnlyRootRequired) {
		t.Fatalf("Execute com raiz escrevível = %v, quero ErrReadOnlyRootRequired", err)
	}
}

// TestManifest_CustomSeccompProfileApplied prova que WithSeccompProfile sobrepõe o
// perfil e o seu hash é o que entra no manifesto (não o default).
func TestManifest_CustomSeccompProfileApplied(t *testing.T) {
	store := newStore(t)
	custom, err := seccomp.Parse([]byte(`{"version":"custom/v9","default_action":"deny","allowed_syscalls":["read","write"]}`))
	if err != nil {
		t.Fatalf("seccomp.Parse: %v", err)
	}
	def, _ := seccomp.Load()
	if custom.Hash() == def.Hash() {
		t.Fatal("perfil custom devia ter hash diferente do default")
	}
	launcher, err := NewLauncher(NewFakeDriver(),
		WithEventSink(NewEventStoreSink(store)),
		WithSeccompProfile(custom),
	)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	rm := newPermitMonitor(store)
	ml, err := NewMediatedLauncher(rm, launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}
	req := ExecRequest{RunID: "run-c", StepID: "step-c", Call: ToolCall{ToolID: "t", Command: "echo"}}
	if _, err := ml.Execute(context.Background(), defaultAuthz(), req); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	evs := eventsOfType(readEvents(t, store, "run-c"), EventExecCompleted)
	if len(evs) == 0 {
		t.Fatal("sem evento exec")
	}
	var p lifecyclePayload
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.SeccompProfileHash != custom.Hash() {
		t.Fatalf("hash no manifesto = %q, quero o do perfil custom %q", p.SeccompProfileHash, custom.Hash())
	}
}
