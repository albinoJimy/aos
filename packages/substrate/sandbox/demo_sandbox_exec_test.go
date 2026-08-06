package sandbox

import (
	"context"
	"errors"
	"testing"
)

// TestDemo_SandboxExecEndToEnd demonstra a EXECUÇÃO CANÓNICA de uma tool call no AOS: pelo
// [MediatedLauncher] (regista o dispatch no RM; no-bypass estrutural), o RM MEDEIA e, no permit,
// o [Launcher] corre o ciclo de vida da microVM (create → exec → destroy, event-sealed), devolvendo
// um [ExecResult] SEMPRE untrusted. Mostra o INPUT (a tool call), o OUTPUT (conteúdo real lido do
// jail), o isolamento (escape bloqueado), e a SELEÇÃO DE DRIVER — fake (dev) vs firecracker
// (produção, microVM real). NÃO reinventa nada: usa o subsistema sandbox tal-qual.
func TestDemo_SandboxExecEndToEnd(t *testing.T) {
	ctx := context.Background()
	content := []byte("Reuniao 3a: rever o plano de migracao. Owner: alice.")
	// Semeia o RootFS BASE read-only com o documento (AOS-066): a leitura cai no base.
	snap, err := NewSnapshot("img/doc-v1", map[string][]byte{"notes": content})
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}

	// ---------- DEV: driver FAKE (jail funcional in-process, isolamento modelado) ----------
	store := newStore(t)
	driver, err := NewDriver(DriverFake)
	if err != nil {
		t.Fatalf("NewDriver(fake): %v", err)
	}
	launcher, err := NewLauncher(driver, WithEventSink(NewEventStoreSink(store)), WithSnapshot(snap))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	rm := newPermitMonitor(store)
	ml, err := NewMediatedLauncher(rm, launcher, "doc_read")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	// EXECUÇÃO: pelo MediatedLauncher → RM.Mediate (permit) → sandbox (create→exec→destroy).
	res, err := ml.Execute(ctx, defaultAuthz(), ExecRequest{
		RunID: "run-sbx", StepID: "step-1",
		Call: ToolCall{ToolID: "doc_read", Command: "read", Path: "notes"},
	})
	if err != nil {
		t.Fatalf("Execute (permit devia executar): %v", err)
	}
	if string(res.Stdout) != string(content) {
		t.Fatalf("output != conteudo semeado: %q", res.Stdout)
	}
	if res.Taint() != TaintUntrusted {
		t.Fatalf("ExecResult devia ser untrusted por tipo, veio %v", res.Taint())
	}

	// ISOLAMENTO: uma tentativa de escape (path traversal) NÃO lê o host — é bloqueada com
	// ErrJailEscape, SEM tocar o host.
	_, escErr := ml.Execute(ctx, defaultAuthz(), ExecRequest{
		RunID: "run-esc", StepID: "s",
		Call: ToolCall{ToolID: "doc_read", Command: "read", Path: "../../etc/passwd"},
	})
	if !errors.Is(escErr, ErrJailEscape) {
		t.Fatalf("escape devia ser bloqueado com ErrJailEscape, veio: %v", escErr)
	}

	t.Logf("\n"+
		"  ADAPTADOR    : MediatedLauncher (dispatch registado no RM; no-bypass estrutural)\n"+
		"  DRIVER (dev) : fake — jail funcional (FS overlay, seccomp, escape-block, host nunca tocado)\n"+
		"  INPUT        : ToolCall{tool=doc_read, cmd=read, path=notes}\n"+
		"  CADEIA       : Execute -> RM.Mediate (permit) -> Launcher: create -> exec -> destroy (event-sealed)\n"+
		"  OUTPUT       : %s\n"+
		"  TAINT        : %v (untrusted por TIPO — ExecResult.Taint)\n"+
		"  ISOLAMENTO   : path traversal '../../etc/passwd' -> BLOQUEADO (%v, host intacto)",
		res.Stdout, res.Taint(), escErr)

	// ---------- PRODUÇÃO: driver FIRECRACKER (microVM real) ----------
	// MESMO código (MediatedLauncher + Launcher + Execute), só o driver muda. Sem KVM (este
	// ambiente) o driver de produção devolve ErrDriverUnavailable — prova que o caminho de
	// produção está WIRED e só precisa de um host com KVM (infra do dono), não de código novo.
	fcDriver, err := NewDriver(DriverFirecracker)
	if err != nil {
		t.Fatalf("NewDriver(firecracker): %v", err)
	}
	fcStore := newStore(t)
	fcLauncher, err := NewLauncher(fcDriver, WithEventSink(NewEventStoreSink(fcStore)), WithSnapshot(snap))
	if err != nil {
		t.Fatalf("NewLauncher(firecracker): %v", err)
	}
	fcMl, err := NewMediatedLauncher(newPermitMonitor(fcStore), fcLauncher, "doc_read")
	if err != nil {
		t.Fatalf("NewMediatedLauncher(firecracker): %v", err)
	}
	_, fcErr := fcMl.Execute(ctx, defaultAuthz(), ExecRequest{
		RunID: "run-fc", StepID: "s", Call: ToolCall{ToolID: "doc_read", Command: "read", Path: "notes"},
	})
	if !errors.Is(fcErr, ErrDriverUnavailable) {
		t.Fatalf("firecracker sem KVM devia dar ErrDriverUnavailable, veio: %v", fcErr)
	}
	t.Logf("\n"+
		"  DRIVER (prod): firecracker — MESMO codigo, so o driver muda\n"+
		"  RESULTADO    : %v (microVM real precisa de host com KVM — infra do dono, nao codigo)",
		fcErr)
}

// TestDemo_ArgsToExecRequestChain fecha a última peça: os args OPACOS que o modelo produz por tool
// call → [BuildExecRequest] (binding TRUSTED) → [ExecRequest] → MediatedLauncher → sandbox →
// conteúdo real. É o que ligaria o loop live (Kimi) à execução em microVM.
func TestDemo_ArgsToExecRequestChain(t *testing.T) {
	ctx := context.Background()
	content := []byte("Reuniao 3a: rever o plano de migracao. Owner: alice.")
	snap, err := NewSnapshot("img/doc-v1", map[string][]byte{"notes": content})
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	store := newStore(t)
	driver, _ := NewDriver(DriverFake)
	launcher, err := NewLauncher(driver, WithEventSink(NewEventStoreSink(store)), WithSnapshot(snap))
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	ml, err := NewMediatedLauncher(newPermitMonitor(store), launcher, "doc_read")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	// O modelo (Kimi) produz ESTES args opacos. A binding é TRUSTED (config): Command fixo "read",
	// o Path vem do arg doc_id. O modelo NÃO escolhe o comando.
	modelArgs := []byte(`{"doc_id":"notes"}`)
	binding := SandboxBinding{Command: "read", PathArg: "doc_id"}
	req, err := BuildExecRequest("run-adapt", "step-1", "doc_read", modelArgs, binding)
	if err != nil {
		t.Fatalf("BuildExecRequest: %v", err)
	}
	res, err := ml.Execute(ctx, defaultAuthz(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if string(res.Stdout) != string(content) {
		t.Fatalf("output != conteudo: %q", res.Stdout)
	}

	// SEGURANÇA: os args untrusted só preenchem VALORES; o Command vem da binding. Um arg que tenta
	// escapar (path traversal) é bloqueado pelo jail — não escolhe comando nem lê o host.
	evilReq, err := BuildExecRequest("run-evil", "s", "doc_read", []byte(`{"doc_id":"../../etc/passwd"}`), binding)
	if err != nil {
		t.Fatalf("BuildExecRequest(evil): %v", err)
	}
	_, evilErr := ml.Execute(ctx, defaultAuthz(), evilReq)
	if !errors.Is(evilErr, ErrJailEscape) {
		t.Fatalf("escape via args devia ser bloqueado, veio: %v", evilErr)
	}

	t.Logf("\n"+
		"  ARGS DO MODELO : %s  (opacos, untrusted)\n"+
		"  BINDING (trust): Command=read (FIXO), Path<-doc_id\n"+
		"  EXECREQUEST    : ToolCall{tool=doc_read, cmd=read, path=notes}\n"+
		"  -> MediatedLauncher -> sandbox -> OUTPUT: %s\n"+
		"  SEGURANCA      : args '../../etc/passwd' -> BLOQUEADO (%v); o modelo nao escolhe o Command",
		modelArgs, res.Stdout, evilErr)
}
