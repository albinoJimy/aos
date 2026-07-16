package sandbox

import (
	"context"
	"errors"
	"testing"
)

// countingDriver embrulha um driver e conta create/exec/destroy (asserção do ciclo
// de vida, incl. destroy garantido).
type countingDriver struct {
	SandboxDriver
	creates  int
	execs    int
	destroys int
	failExec error
}

func (d *countingDriver) Create(ctx context.Context, cap capability, spec Spec) (Instance, error) {
	d.creates++
	return d.SandboxDriver.Create(ctx, cap, spec)
}

func (d *countingDriver) Exec(ctx context.Context, cap capability, inst Instance, req ExecRequest) (ExecResult, error) {
	d.execs++
	if d.failExec != nil {
		return ExecResult{}, d.failExec
	}
	return d.SandboxDriver.Exec(ctx, cap, inst, req)
}

func (d *countingDriver) Destroy(ctx context.Context, cap capability, inst Instance) error {
	d.destroys++
	return d.SandboxDriver.Destroy(ctx, cap, inst)
}

// TestIntegration_ToolCallRunsInMicroVM prova o critério de integração: uma tool
// call executa na microVM (fake) e devolve resultado; verifica a AUSÊNCIA de acesso
// ao socket do host e as invariantes de isolamento.
func TestIntegration_ToolCallRunsInMicroVM(t *testing.T) {
	store := newStore(t)
	fake := NewFakeDriver()
	launcher, err := NewLauncher(fake, WithEventSink(NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	rm := newPermitMonitor(store)
	ml, err := NewMediatedLauncher(rm, launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	req := ExecRequest{
		RunID:  "run-1",
		StepID: "step-1",
		Call:   ToolCall{ToolID: "echo", Command: "echo", Args: []string{"hello", "world"}},
	}
	res, err := ml.Execute(context.Background(), defaultAuthz(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := string(res.Stdout), "echo hello world"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if res.Taint() != TaintUntrusted || !res.IsUntrusted() {
		t.Fatalf("resultado nao untrusted: %v", res.Taint())
	}
	// Ausência de acesso ao socket do host (ADR-004).
	if fake.HostSocketAccessed() {
		t.Fatal("driver acedeu ao socket do host")
	}
	if fake.HostTouches() != 0 {
		t.Fatalf("host tocado %d vezes (esperado 0)", fake.HostTouches())
	}
}

// TestLifecycle_CreateExecDestroyOrder prova que o ciclo é create → exec → destroy e
// que o destroy corre exactamente uma vez.
func TestLifecycle_CreateExecDestroyOrder(t *testing.T) {
	store := newStore(t)
	cd := &countingDriver{SandboxDriver: NewFakeDriver()}
	launcher, err := NewLauncher(cd, WithEventSink(NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	if _, err := launcher.run(context.Background(), ExecRequest{
		RunID: "run-1", StepID: "step-1", Call: ToolCall{Command: "ok"},
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if cd.creates != 1 || cd.execs != 1 || cd.destroys != 1 {
		t.Fatalf("ciclo = create:%d exec:%d destroy:%d, esperado 1/1/1", cd.creates, cd.execs, cd.destroys)
	}
}

// TestLifecycle_DestroyGuaranteedOnExecError prova que o destroy corre mesmo quando
// o exec falha — sem microVMs órfãs.
func TestLifecycle_DestroyGuaranteedOnExecError(t *testing.T) {
	store := newStore(t)
	sentinel := errors.New("exec falhou")
	cd := &countingDriver{SandboxDriver: NewFakeDriver(), failExec: sentinel}
	launcher, err := NewLauncher(cd, WithEventSink(NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	_, err = launcher.run(context.Background(), ExecRequest{
		RunID: "run-1", StepID: "step-1", Call: ToolCall{Command: "boom"},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("run err = %v, esperado %v", err, sentinel)
	}
	if cd.destroys != 1 {
		t.Fatalf("destroy correu %d vezes (esperado 1 mesmo em erro)", cd.destroys)
	}
	// Mesmo em erro, o evento destroyed foi selado.
	destroyed := eventsOfType(readEvents(t, store, "run-1"), EventInstanceDestroyed)
	if len(destroyed) != 1 {
		t.Fatalf("eventos destroyed = %d, esperado 1", len(destroyed))
	}
}

// panicExecDriver embrulha um driver e faz PANIC no Exec — para provar que o
// destroy (defer) corre mesmo em panic (sem microVMs órfãs), como a documentação
// afirma.
type panicExecDriver struct {
	SandboxDriver
	destroys int
}

func (d *panicExecDriver) Exec(context.Context, capability, Instance, ExecRequest) (ExecResult, error) {
	panic("driver exec explodiu")
}

func (d *panicExecDriver) Destroy(ctx context.Context, cap capability, inst Instance) error {
	d.destroys++
	return d.SandboxDriver.Destroy(ctx, cap, inst)
}

// TestLifecycle_DestroyGuaranteedOnPanic prova a garantia documentada 'destroy
// mesmo em panic': quando o Exec do driver faz panic, o defer de destroy corre na
// desenrolagem (destroys==1 e evento destroyed selado) e o panic PROPAGA ao chamador
// (o loop/RM decide a política de recover — fora de escopo AOS-064).
func TestLifecycle_DestroyGuaranteedOnPanic(t *testing.T) {
	store := newStore(t)
	pd := &panicExecDriver{SandboxDriver: NewFakeDriver()}
	launcher, err := NewLauncher(pd, WithEventSink(NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("esperado panic propagado do Exec do driver")
			}
		}()
		_, _ = launcher.run(context.Background(), ExecRequest{
			RunID: "run-panic", StepID: "step-panic", Call: ToolCall{Command: "boom"},
		})
	}()
	if pd.destroys != 1 {
		t.Fatalf("destroy correu %d vezes (esperado 1 mesmo em panic)", pd.destroys)
	}
	destroyed := eventsOfType(readEvents(t, store, "run-panic"), EventInstanceDestroyed)
	if len(destroyed) != 1 {
		t.Fatalf("eventos destroyed = %d, esperado 1 (selado no defer mesmo em panic)", len(destroyed))
	}
}

// TestLifecycle_DestroyRunsAfterContextCancel prova que o destroy (cleanup) corre
// mesmo com o contexto de execução cancelado.
func TestLifecycle_DestroyRunsAfterContextCancel(t *testing.T) {
	store := newStore(t)
	cd := &countingDriver{SandboxDriver: NewFakeDriver()}
	launcher, err := NewLauncher(cd, WithEventSink(NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelado à partida
	// O create do fake não observa o ctx; o objectivo é provar que o defer de destroy
	// usa um contexto sem cancelamento e sela o evento na mesma.
	_, _ = launcher.run(ctx, ExecRequest{RunID: "run-c", StepID: "step-c", Call: ToolCall{Command: "x"}})
	if cd.destroys != 1 {
		t.Fatalf("destroy correu %d vezes (esperado 1)", cd.destroys)
	}
}

// TestLifecycle_EmptyRunOrStepRejected prova a validação fail-closed.
func TestLifecycle_EmptyRunOrStepRejected(t *testing.T) {
	launcher, _ := NewLauncher(NewFakeDriver())
	cases := []struct {
		name string
		req  ExecRequest
		want error
	}{
		{"sem run", ExecRequest{StepID: "s"}, ErrEmptyRunID},
		{"sem step", ExecRequest{RunID: "r"}, ErrEmptyStepID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := launcher.run(context.Background(), tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, esperado %v", err, tc.want)
			}
		})
	}
}

// TestLifecycle_WeakIsolationRejected prova o fail-closed de isolamento.
func TestLifecycle_WeakIsolationRejected(t *testing.T) {
	launcher, err := NewLauncher(NewFakeDriver(), WithIsolation(Isolation{NoHostSocket: false}))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	_, err = launcher.run(context.Background(), ExecRequest{RunID: "r", StepID: "s", Call: ToolCall{Command: "x"}})
	if !errors.Is(err, ErrSharedNamespaceForbidden) && !errors.Is(err, ErrHostSocketForbidden) {
		t.Fatalf("err = %v, esperado recusa de isolamento fraca", err)
	}
}
