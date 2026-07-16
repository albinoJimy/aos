package sandbox

import (
	"context"
	"errors"
	"testing"
)

// echoExecutor é um GuestExecutor determinista que modela a execução real dentro do
// guest — usado para exercitar os skeletons Firecracker/gVisor com contrato idêntico.
type echoExecutor struct{}

func (echoExecutor) RunInGuest(_ context.Context, _ Instance, call ToolCall) ([]byte, []Artifact, int, error) {
	out := call.Command
	for _, a := range call.Args {
		out += " " + a
	}
	return []byte(out), nil, 0, nil
}

// TestDriver_SelectionByConfig prova que [NewDriver] selecciona por config e que um
// kind desconhecido falha.
func TestDriver_SelectionByConfig(t *testing.T) {
	for _, kind := range []DriverKind{DriverFirecracker, DriverGVisor, DriverFake} {
		d, err := NewDriver(kind)
		if err != nil {
			t.Fatalf("NewDriver(%q): %v", kind, err)
		}
		if d.Kind() != kind {
			t.Fatalf("Kind() = %q, esperado %q", d.Kind(), kind)
		}
	}
	if _, err := NewDriver("qcoisa"); !errors.Is(err, ErrUnknownDriver) {
		t.Fatalf("err = %v, esperado ErrUnknownDriver", err)
	}
}

// TestDriver_SkeletonsUnavailableWithoutExecutor prova que os skeletons são
// fail-closed neste ambiente (sem KVM/host support) quando não têm executor.
func TestDriver_SkeletonsUnavailableWithoutExecutor(t *testing.T) {
	for _, d := range []SandboxDriver{NewFirecrackerDriver(), NewGVisorDriver()} {
		launcher, err := NewLauncher(d)
		if err != nil {
			t.Fatalf("NewLauncher: %v", err)
		}
		_, err = launcher.run(context.Background(), ExecRequest{RunID: "r", StepID: "s", Call: ToolCall{Command: "x"}})
		if !errors.Is(err, ErrDriverUnavailable) {
			t.Fatalf("driver %q: err = %v, esperado ErrDriverUnavailable", d.Kind(), err)
		}
	}
}

// TestDriver_IdenticalContractAcrossKinds prova o critério: a selecção
// firecracker|gvisor|fake tem contrato IDÊNTICO — a mesma sequência create → exec →
// destroy e o mesmo [ExecResult] (mesmo stdout, mesmo taint untrusted).
func TestDriver_IdenticalContractAcrossKinds(t *testing.T) {
	drivers := map[DriverKind]SandboxDriver{
		DriverFake:        NewFakeDriver(),
		DriverFirecracker: NewFirecrackerDriver(WithFirecrackerExecutor(echoExecutor{})),
		DriverGVisor:      NewGVisorDriver(WithGVisorExecutor(echoExecutor{})),
	}
	call := ToolCall{ToolID: "echo", Command: "echo", Args: []string{"same", "contract"}}
	const wantStdout = "echo same contract"

	var firstArtifacts int
	first := true
	for kind, d := range drivers {
		store := newStore(t)
		launcher, err := NewLauncher(d, WithEventSink(NewEventStoreSink(store)))
		if err != nil {
			t.Fatalf("NewLauncher(%q): %v", kind, err)
		}
		res, err := launcher.run(context.Background(), ExecRequest{RunID: "r", StepID: "s", Call: call})
		if err != nil {
			t.Fatalf("run(%q): %v", kind, err)
		}
		if string(res.Stdout) != wantStdout {
			t.Fatalf("driver %q: stdout = %q, esperado %q", kind, res.Stdout, wantStdout)
		}
		if res.Taint() != TaintUntrusted {
			t.Fatalf("driver %q: taint = %q, esperado untrusted", kind, res.Taint())
		}
		// Todos selam create/exec/destroy no Event Store (mesmo contrato observável).
		evs := readEvents(t, store, "r")
		for _, typ := range []string{EventInstanceCreated, EventExecCompleted, EventInstanceDestroyed} {
			if len(eventsOfType(evs, typ)) != 1 {
				t.Fatalf("driver %q: evento %q ausente/duplicado", kind, typ)
			}
		}
		if first {
			firstArtifacts = len(res.Artifacts)
			first = false
		} else if len(res.Artifacts) != firstArtifacts {
			t.Fatalf("driver %q: nº de artefactos diverge do contrato (%d vs %d)", kind, len(res.Artifacts), firstArtifacts)
		}
	}
}

// TestDriver_IsolationInvariantsEnforced prova que todos os drivers impõem as
// invariantes de isolamento na instância (sem socket/namespace do host).
func TestDriver_IsolationInvariantsEnforced(t *testing.T) {
	drivers := []SandboxDriver{
		NewFakeDriver(),
		NewFirecrackerDriver(WithFirecrackerExecutor(echoExecutor{})),
		NewGVisorDriver(WithGVisorExecutor(echoExecutor{})),
	}
	launcher := &Launcher{} // só para mintar a capability de teste
	cap := capability{launcher: launcher}
	for _, d := range drivers {
		inst, err := d.Create(context.Background(), cap, Spec{RunID: "r", StepID: "s", Kind: d.Kind(), Isolation: HardenedIsolation()})
		if err != nil {
			t.Fatalf("driver %q Create: %v", d.Kind(), err)
		}
		if !inst.NoHostSocket || !inst.NoSharedNetNS || !inst.NoSharedPIDNS {
			t.Fatalf("driver %q: instancia sem invariantes de isolamento: %+v", d.Kind(), inst)
		}
		_ = d.Destroy(context.Background(), cap, inst)
	}
}

// jailCheckingExecutor é um GuestExecutor de REFERÊNCIA de teste que aplica as
// mesmas verificações de escape do FakeDriver (metacaractere/traversal) ANTES de
// "executar" — para exercitar a invariante 'escape bloqueado' também nos caminhos
// fc/gv. NOTA: na infra real a contenção é do microVM/runsc, não deste código Go.
type jailCheckingExecutor struct{}

func (jailCheckingExecutor) RunInGuest(_ context.Context, _ Instance, call ToolCall) ([]byte, []Artifact, int, error) {
	if hasShellMetachar(call.Command) {
		return nil, nil, 0, ErrJailEscape
	}
	for _, a := range call.Args {
		if hasShellMetachar(a) {
			return nil, nil, 0, ErrJailEscape
		}
	}
	if call.Path != "" {
		if _, err := jailClean(call.Path); err != nil {
			return nil, nil, 0, ErrJailEscape
		}
	}
	return []byte(call.Command), nil, 0, nil
}

// TestSecurity_SkeletonEscapeCanBeModeled prova que a invariante 'escape bloqueado'
// é exercitável também nos skeletons fc/gv quando o GuestExecutor injectado aplica
// as verificações — documentando (via teste) que a contenção Go NÃO está nos
// skeletons (é do microVM/runsc real) mas o contrato de escape pode ser modelado
// com resultado idêntico ([ErrJailEscape]) ao do FakeDriver.
func TestSecurity_SkeletonEscapeCanBeModeled(t *testing.T) {
	drivers := []SandboxDriver{
		NewFirecrackerDriver(WithFirecrackerExecutor(jailCheckingExecutor{})),
		NewGVisorDriver(WithGVisorExecutor(jailCheckingExecutor{})),
	}
	escapes := []ToolCall{
		{Command: "cat", Path: "../../etc/passwd"},
		{Command: "echo", Args: []string{"$(whoami)"}},
	}
	for _, d := range drivers {
		launcher, err := NewLauncher(d)
		if err != nil {
			t.Fatalf("NewLauncher(%q): %v", d.Kind(), err)
		}
		for _, call := range escapes {
			_, err := launcher.run(context.Background(), ExecRequest{RunID: "r", StepID: "s", Call: call})
			if !errors.Is(err, ErrJailEscape) {
				t.Fatalf("driver %q call %+v: err = %v, esperado ErrJailEscape", d.Kind(), call, err)
			}
		}
	}
}

// TestNoBypass_UnsanctionedCapabilityRejected prova que a ligação capability→Launcher
// é 'evidência' REAL em runtime: um capability zero-value (launcher nil) é recusado
// por Create/Exec/Destroy de cada driver — não é apenas o selo do tipo não-exportado
// (compile-time).
func TestNoBypass_UnsanctionedCapabilityRejected(t *testing.T) {
	var zero capability // launcher == nil
	spec := Spec{RunID: "r", StepID: "s", Isolation: HardenedIsolation()}
	drivers := []SandboxDriver{
		NewFakeDriver(),
		NewFirecrackerDriver(WithFirecrackerExecutor(echoExecutor{})),
		NewGVisorDriver(WithGVisorExecutor(echoExecutor{})),
	}
	for _, d := range drivers {
		if _, err := d.Create(context.Background(), zero, spec); !errors.Is(err, ErrUnsanctionedCapability) {
			t.Fatalf("driver %q Create(zero-cap): err = %v, esperado ErrUnsanctionedCapability", d.Kind(), err)
		}
		if _, err := d.Exec(context.Background(), zero, Instance{}, ExecRequest{RunID: "r", StepID: "s", Call: ToolCall{Command: "ok"}}); !errors.Is(err, ErrUnsanctionedCapability) {
			t.Fatalf("driver %q Exec(zero-cap): err = %v, esperado ErrUnsanctionedCapability", d.Kind(), err)
		}
		if err := d.Destroy(context.Background(), zero, Instance{}); !errors.Is(err, ErrUnsanctionedCapability) {
			t.Fatalf("driver %q Destroy(zero-cap): err = %v, esperado ErrUnsanctionedCapability", d.Kind(), err)
		}
	}
	// Sancionada (launcher não-nil): a mesma operação passa a barreira de runtime.
	launcher := &Launcher{}
	sane := capability{launcher: launcher}
	fake := NewFakeDriver()
	if _, err := fake.Create(context.Background(), sane, spec); err != nil {
		t.Fatalf("Create sancionado: %v", err)
	}
}

// TestDriver_CreateRejectsWeakIsolation prova o fail-closed no Create de cada driver.
func TestDriver_CreateRejectsWeakIsolation(t *testing.T) {
	launcher := &Launcher{}
	cap := capability{launcher: launcher}
	weak := Isolation{NoHostSocket: false, NoSharedNetNS: true, NoSharedPIDNS: true}
	drivers := []SandboxDriver{
		NewFakeDriver(),
		NewFirecrackerDriver(WithFirecrackerExecutor(echoExecutor{})),
		NewGVisorDriver(WithGVisorExecutor(echoExecutor{})),
	}
	for _, d := range drivers {
		if _, err := d.Create(context.Background(), cap, Spec{RunID: "r", StepID: "s", Isolation: weak}); !errors.Is(err, ErrHostSocketForbidden) {
			t.Fatalf("driver %q: err = %v, esperado ErrHostSocketForbidden", d.Kind(), err)
		}
	}
}
