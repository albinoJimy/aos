package securitytests

import (
	"context"
	"errors"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/sandbox"
	"github.com/aos-ref/substrate/sandbox/seccomp"
)

// ===========================================================================
// CENÁRIO 4 — ISOLAMENTO (AOS-066, ADR-004)
//
// A microVM só é fronteira se o interior for hostil à persistência e à evasão: FS
// read-only + overlay efémero (nada persiste entre execuções), seccomp mínimo (syscall
// fora da allowlist bloqueada) e sem socket do host. Estes testes exercitam o CAMINHO DE
// EXECUÇÃO REAL (MediatedLauncher.Execute, mediado pelo RM) e os controlos reais; não os
// reimplementam.
// ===========================================================================

// newPermitMonitor constrói um RM que PERMITE (cadeia de stubs neutros) e sela as
// mediações no store dado — o caminho pelo qual a sandbox é sempre invocada (ADR-002).
func newPermitMonitor(store *eventstore.Store) *referencemonitor.Monitor {
	return referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.DefaultHooks()...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)),
	)
}

// isoAuthz é uma autorização mínima válida para a sandbox.
func isoAuthz() sandbox.Authorization {
	return sandbox.Authorization{
		Principal:  referencemonitor.Principal{NHIID: "nhi-test", AgentID: "agent-1", AgentClass: "class-a"},
		Capability: "cap:test.exec",
		Resource:   referencemonitor.Resource{Type: "vm", Value: "sandbox"},
		Credential: "tok-test",
	}
}

// isolationSnapshot constrói um snapshot base determinista com um ficheiro read-only,
// para ligar o RootFS (raiz read-only + overlay efémero) ao caminho de execução.
func isolationSnapshot(t *testing.T) *sandbox.Snapshot {
	t.Helper()
	snap, err := sandbox.NewSnapshot("img/aos075", map[string][]byte{
		"etc/config": []byte("base-config"),
	})
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	return snap
}

// newEventStore constrói um Event Store de referência para os testes de isolamento.
func newEventStore(t *testing.T) *eventstore.Store {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// execOnce corre uma tool call ATRAVÉS do Reference Monitor (MediatedLauncher.Execute).
func execOnce(t *testing.T, ml *sandbox.MediatedLauncher, runID, stepID string, call sandbox.ToolCall) (sandbox.ExecResult, error) {
	t.Helper()
	return ml.Execute(context.Background(), isoAuthz(), sandbox.ExecRequest{RunID: runID, StepID: stepID, Call: call})
}

// TestIsolation_OverlayDoesNotPersist prova, no caminho de execução REAL (Execute duas
// vezes no mesmo launcher), que uma escrita da execução N não sobrevive à execução N+1:
// cada execução recebe um overlay NOVO restaurado do mesmo base imutável, descartado no
// destroy. Refutação: a raiz read-only continua legível em N+1 (a ausência é REAL).
func TestIsolation_OverlayDoesNotPersist(t *testing.T) {
	t.Parallel()
	store := newEventStore(t)
	launcher, err := sandbox.NewLauncher(sandbox.NewFakeDriver(),
		sandbox.WithEventSink(sandbox.NewEventStoreSink(store)),
		sandbox.WithSnapshot(isolationSnapshot(t)),
	)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	ml, err := sandbox.NewMediatedLauncher(newPermitMonitor(store), launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	// Execução N: escreve no overlay efémero (copy-up).
	if _, err := execOnce(t, ml, "run-n", "step-n", sandbox.ToolCall{Command: "write", Path: "run/secret", Write: []byte("data-N")}); err != nil {
		t.Fatalf("Execute N (write): %v", err)
	}
	// Execução N+1: lê o MESMO caminho — tem de estar AUSENTE (overlay de N descartado).
	resN1, err := execOnce(t, ml, "run-n1", "step-n1", sandbox.ToolCall{Command: "read", Path: "run/secret"})
	if err != nil {
		t.Fatalf("Execute N+1 (read ausente): %v", err)
	}
	if resN1.ExitCode == 0 {
		t.Fatalf("ISOLAMENTO QUEBRADO: N+1 observa o ficheiro escrito por N (exit=%d, stdout=%q)", resN1.ExitCode, resN1.Stdout)
	}
	// Refutação: a raiz read-only continua legível — a ausência acima é REAL.
	resBase, err := execOnce(t, ml, "run-n2", "step-n2", sandbox.ToolCall{Command: "read", Path: "etc/config"})
	if err != nil {
		t.Fatalf("Execute N+2 (read base): %v", err)
	}
	if resBase.ExitCode != 0 || string(resBase.Stdout) != "base-config" {
		t.Fatalf("N+2 leitura da raiz = (%q, exit=%d), quer (base-config, 0)", resBase.Stdout, resBase.ExitCode)
	}

	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "isolation_overlay", "sandbox.exec", "overlay não persiste N->N+1")
	verifyWORM(t, ledger, suiteLedgerPartition)
}

// TestIsolation_SeccompBlocksOutsideAllowlist prova, no caminho de execução REAL, que uma
// syscall FORA do perfil seccomp efectivamente aplicado é bloqueada default-deny — e que
// uma syscall NA allowlist é permitida (gate não-tautológico).
func TestIsolation_SeccompBlocksOutsideAllowlist(t *testing.T) {
	t.Parallel()
	store := newEventStore(t)
	// Perfil restritivo: só permite "read". Uma escrita (que exige "write") é negada.
	restrictive, err := seccomp.Parse([]byte(`{"version":"aos075-restrictive/v1","default_action":"deny","allowed_syscalls":["read"]}`))
	if err != nil {
		t.Fatalf("seccomp.Parse: %v", err)
	}
	launcher, err := sandbox.NewLauncher(sandbox.NewFakeDriver(),
		sandbox.WithEventSink(sandbox.NewEventStoreSink(store)),
		sandbox.WithSnapshot(isolationSnapshot(t)),
		sandbox.WithSeccompProfile(restrictive),
	)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	ml, err := sandbox.NewMediatedLauncher(newPermitMonitor(store), launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	// "read" está na allowlist → PERMITIDO (gate não-tautológico).
	if _, err := execOnce(t, ml, "run-ok", "step-ok", sandbox.ToolCall{Command: "read", Path: "etc/config"}); err != nil {
		t.Fatalf("Execute read (allowlisted) = %v, quer permitido", err)
	}
	// "write" está FORA da allowlist → BLOQUEADO default-deny.
	_, err = execOnce(t, ml, "run-deny", "step-deny", sandbox.ToolCall{Command: "write", Path: "etc/config", Write: []byte("x")})
	if !errors.Is(err, sandbox.ErrSeccompDenied) {
		t.Fatalf("Execute write sob perfil sem 'write' = %v, quer ErrSeccompDenied", err)
	}

	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "isolation_seccomp", "sandbox.exec", sandbox.ErrSeccompDenied.Error())
	verifyWORM(t, ledger, suiteLedgerPartition)
}

// TestIsolation_NoHostSocket prova as duas faces da fronteira sem-socket-do-host: (a) no
// caminho hardened (default) uma execução corre sem NUNCA tocar o host nem o seu socket
// (funnel de refutação do FakeDriver); e (b) uma configuração que ENFRAQUECE a fronteira
// é RECUSADA fail-closed (a sandbox não corre sem a garantia hardened, ADR-004).
func TestIsolation_NoHostSocket(t *testing.T) {
	t.Parallel()
	store := newEventStore(t)

	// (a) Caminho hardened: exec corre e NÃO acede ao host/socket do host.
	driver := sandbox.NewFakeDriver()
	launcher, err := sandbox.NewLauncher(driver,
		sandbox.WithEventSink(sandbox.NewEventStoreSink(store)),
		sandbox.WithSnapshot(isolationSnapshot(t)),
	)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	ml, err := sandbox.NewMediatedLauncher(newPermitMonitor(store), launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}
	res, err := execOnce(t, ml, "run-iso", "step-iso", sandbox.ToolCall{Command: "read", Path: "etc/config"})
	if err != nil {
		t.Fatalf("Execute hardened: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exec hardened exit=%d, quer 0 (a execução tem de correr para a asserção ter poder)", res.ExitCode)
	}
	if driver.HostSocketAccessed() {
		t.Fatal("a microVM acedeu ao socket do host (ADR-004 violado)")
	}
	if driver.HostTouches() != 0 {
		t.Fatalf("a microVM tocou o host %d vez(es) (isolamento violado)", driver.HostTouches())
	}

	// (b) Fronteira enfraquecida (NoHostSocket=false) é RECUSADA fail-closed. O guard
	//     hardened bundla NoHostSocket com os namespaces, logo devolve
	//     ErrSharedNamespaceForbidden — a sandbox NÃO corre sem a garantia.
	weakStore := newEventStore(t)
	weakLauncher, err := sandbox.NewLauncher(sandbox.NewFakeDriver(),
		sandbox.WithEventSink(sandbox.NewEventStoreSink(weakStore)),
		sandbox.WithSnapshot(isolationSnapshot(t)),
		sandbox.WithIsolation(sandbox.Isolation{NoHostSocket: false, NoSharedNetNS: true, NoSharedPIDNS: true, RootFSReadOnly: true}),
	)
	if err != nil {
		t.Fatalf("NewLauncher (weak): %v", err)
	}
	weakML, err := sandbox.NewMediatedLauncher(newPermitMonitor(weakStore), weakLauncher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher (weak): %v", err)
	}
	_, err = execOnce(t, weakML, "run-weak", "step-weak", sandbox.ToolCall{Command: "read", Path: "etc/config"})
	if !errors.Is(err, sandbox.ErrSharedNamespaceForbidden) {
		t.Fatalf("exec sem NoHostSocket = %v, quer ErrSharedNamespaceForbidden (fail-closed)", err)
	}

	ledger := audit.NewMemStore()
	attestBlock(t, ledger, "isolation_no_host_socket", "sandbox.exec", sandbox.ErrSharedNamespaceForbidden.Error())
	verifyWORM(t, ledger, suiteLedgerPartition)
}
