package sandbox_test

// Este teste corre num pacote EXTERNO (sandbox_test) — só vê a superfície
// EXPORTADA. Prova, na perspectiva de um consumidor, que a ÚNICA via de execução
// da sandbox é através do Reference Monitor.
//
// Prova estrutural (compile-time, documentada): um consumidor externo NÃO consegue
// invocar a sandbox directamente porque:
//   - o ciclo de vida (Launcher.run) é não-exportado;
//   - as operações do driver (Create/Exec/Destroy) exigem o tipo NÃO-EXPORTADO
//     `capability`, que um pacote externo não consegue nomear nem construir — logo
//     não pode chamar um driver NEM implementar SandboxDriver;
//   - o único adaptador exportado que corre a sandbox (MediatedLauncher) regista-se
//     como ToolFunc no RM e a sua superfície pública (Execute) só chama rm.Mediate.
//
// As linhas comentadas abaixo NÃO COMPILARIAM (deixadas como evidência):
//   d, _ := sandbox.NewDriver(sandbox.DriverFake)
//   d.Exec(ctx, /* capability */, inst, req) // impossivel: capability e' nao-exportado
//   launcher.run(ctx, req)                    // impossivel: run e' nao-exportado

import (
	"context"
	"errors"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/sandbox"
)

func newStoreExt(t *testing.T) *eventstore.Store {
	t.Helper()
	store, err := eventstore.New(eventstore.WithReplicas(3))
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func extAuthz() sandbox.Authorization {
	return sandbox.Authorization{
		Principal:  referencemonitor.Principal{NHIID: "nhi", AgentID: "a", AgentClass: "c"},
		Capability: "cap:exec",
		Resource:   referencemonitor.Resource{Type: "vm", Value: "sbx"},
		Credential: "tok",
	}
}

// TestExt_OnlyMediatedPathRunsSandbox prova, de fora, que Execute encaminha pelo RM:
// sob permit corre; sob deny não há efeito. Não existe outra superfície pública.
func TestExt_OnlyMediatedPathRunsSandbox(t *testing.T) {
	// Permit.
	store := newStoreExt(t)
	launcher, err := sandbox.NewLauncher(sandbox.NewFakeDriver(), sandbox.WithEventSink(sandbox.NewEventStoreSink(store)))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	rm := referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.DefaultHooks()...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)),
	)
	ml, err := sandbox.NewMediatedLauncher(rm, launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}
	res, err := ml.Execute(context.Background(), extAuthz(), sandbox.ExecRequest{
		RunID: "run-x", StepID: "step-x", Call: sandbox.ToolCall{Command: "echo", Args: []string{"hi"}},
	})
	if err != nil {
		t.Fatalf("Execute (permit): %v", err)
	}
	if res.Taint() != sandbox.TaintUntrusted {
		t.Fatalf("resultado nao untrusted")
	}

	// Deny: a mesma superfície pública, mas o RM nega — nenhum efeito.
	denyStore := newStoreExt(t)
	denyLauncher, _ := sandbox.NewLauncher(sandbox.NewFakeDriver(), sandbox.WithEventSink(sandbox.NewEventStoreSink(denyStore)))
	denyRM := referencemonitor.New(
		referencemonitor.WithHooks(denyAllHook{}),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(denyStore)),
	)
	denyML, err := sandbox.NewMediatedLauncher(denyRM, denyLauncher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher (deny): %v", err)
	}
	_, err = denyML.Execute(context.Background(), extAuthz(), sandbox.ExecRequest{
		RunID: "run-d", StepID: "step-d", Call: sandbox.ToolCall{Command: "x"},
	})
	var denied *sandbox.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v, esperado *sandbox.DeniedError", err)
	}
}

type denyAllHook struct{}

func (denyAllHook) Name() string { return "deny-all" }
func (denyAllHook) Evaluate(context.Context, *referencemonitor.Call) (referencemonitor.HookResult, error) {
	return referencemonitor.HookResult{Decision: referencemonitor.HookDeny}, nil
}
