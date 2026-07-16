package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/sandbox/seccomp"
)

// wiredSnapshot constrói um snapshot base determinista com um ficheiro read-only,
// para ligar o RootFS (raiz read-only + overlay efémero) ao caminho de execução.
func wiredSnapshot(t *testing.T) *Snapshot {
	t.Helper()
	snap, err := NewSnapshot("img/wired", map[string][]byte{
		"etc/config": []byte("base-config"),
	})
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	return snap
}

// execOnce corre uma tool call ATRAVÉS do Reference Monitor (MediatedLauncher.Execute)
// — o caminho de execução REAL, mediado — e devolve o resultado e o erro de execução.
func execOnce(t *testing.T, ml *MediatedLauncher, runID, stepID string, call ToolCall) (ExecResult, error) {
	t.Helper()
	return ml.Execute(context.Background(), defaultAuthz(), ExecRequest{
		RunID: runID, StepID: stepID, Call: call,
	})
}

// TestWiring_NPlusOneDoesNotSeeN_ThroughExecute fecha a lacuna do finding de
// test-coverage: a persistência N->N+1 é REFUTADA no caminho de execução REAL
// (MediatedLauncher.Execute duas vezes no mesmo driver), não só no modelo Overlay
// isolado. Uma escrita na execução N não sobrevive à execução N+1 (overlay novo do
// mesmo base imutável, descartado no destroy).
func TestWiring_NPlusOneDoesNotSeeN_ThroughExecute(t *testing.T) {
	store := newStore(t)
	snap := wiredSnapshot(t)
	launcher, err := NewLauncher(NewFakeDriver(),
		WithEventSink(NewEventStoreSink(store)),
		WithSnapshot(snap),
	)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	ml, err := NewMediatedLauncher(newPermitMonitor(store), launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	// Execução N: escreve um ficheiro no overlay efémero (copy-up).
	resN, err := execOnce(t, ml, "run-n", "step-n", ToolCall{Command: "write", Path: "run/secret", Write: []byte("data-N")})
	if err != nil {
		t.Fatalf("Execute N (write): %v", err)
	}
	if string(resN.Stdout) != "wrote 6 bytes to run/secret" {
		t.Fatalf("N stdout = %q, quero confirmação de escrita no overlay", resN.Stdout)
	}

	// Execução N+1: lê o MESMO caminho — tem de estar AUSENTE (overlay de N descartado).
	resN1, err := execOnce(t, ml, "run-n1", "step-n1", ToolCall{Command: "read", Path: "run/secret"})
	if err != nil {
		t.Fatalf("Execute N+1 (read ausente): %v", err)
	}
	if resN1.ExitCode == 0 {
		t.Fatalf("ISOLAMENTO QUEBRADO: N+1 observa o ficheiro escrito por N (exit=%d, stdout=%q)", resN1.ExitCode, resN1.Stdout)
	}

	// Refutação: a raiz read-only continua legível em N+1 — a ausência acima é REAL
	// (o mecanismo de leitura funciona), não um falso negativo por leitura sempre-vazia.
	resBase, err := execOnce(t, ml, "run-n2", "step-n2", ToolCall{Command: "read", Path: "etc/config"})
	if err != nil {
		t.Fatalf("Execute N+2 (read base): %v", err)
	}
	if resBase.ExitCode != 0 || string(resBase.Stdout) != "base-config" {
		t.Fatalf("N+2 leitura da raiz = (%q, exit=%d), quero (base-config, 0)", resBase.Stdout, resBase.ExitCode)
	}

	// O manifesto atesta o rootfs EFETIVAMENTE montado: base digest == digest do
	// snapshot, e os overlays de N e N+1 são DISTINTOS (nunca reciclados).
	ovN := overlayIDFromExec(t, store, "run-n")
	ovN1 := overlayIDFromExec(t, store, "run-n1")
	if ovN == "" || ovN1 == "" {
		t.Fatalf("overlay id ausente no manifesto: N=%q N+1=%q", ovN, ovN1)
	}
	if ovN == ovN1 {
		t.Fatalf("overlay de N+1 reutilizou o id de N (%q): estado reciclado", ovN)
	}
	if bd := baseDigestFromExec(t, store, "run-n"); bd != snap.Digest() {
		t.Fatalf("manifesto rootfs_base_digest = %q, quero o do snapshot %q", bd, snap.Digest())
	}
}

// TestWiring_ReadOnlyRootImmutableThroughExecute fecha o finding model-limitation: no
// caminho de execução REAL a raiz é read-only. Uma escrita que SOMBREIA um ficheiro
// base faz copy-up para o overlay efémero (funciona nessa execução) mas NUNCA muta o
// base — a execução seguinte volta a ler o valor base ORIGINAL, e o digest do base é
// estável (imutabilidade estrutural).
func TestWiring_ReadOnlyRootImmutableThroughExecute(t *testing.T) {
	store := newStore(t)
	snap := wiredSnapshot(t)
	digestBefore := snap.Digest()
	launcher, err := NewLauncher(NewFakeDriver(),
		WithEventSink(NewEventStoreSink(store)),
		WithSnapshot(snap),
	)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	ml, err := NewMediatedLauncher(newPermitMonitor(store), launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	// Execução N: escreve POR CIMA de um caminho da raiz base (copy-up para o overlay).
	if _, err := execOnce(t, ml, "run-w", "step-w", ToolCall{Command: "write", Path: "etc/config", Write: []byte("tampered")}); err != nil {
		t.Fatalf("Execute (write over base): %v", err)
	}
	// O base NUNCA foi mutado: o digest do snapshot é estável.
	if snap.Digest() != digestBefore {
		t.Fatal("digest do base mudou: a raiz read-only foi mutada por uma escrita")
	}
	// Execução N+1: relê o MESMO caminho — devolve o valor BASE original (o copy-up de
	// N foi descartado com o overlay; a raiz read-only impôs-se no caminho executado).
	res, err := execOnce(t, ml, "run-r", "step-r", ToolCall{Command: "read", Path: "etc/config"})
	if err != nil {
		t.Fatalf("Execute (read after tamper): %v", err)
	}
	if res.ExitCode != 0 || string(res.Stdout) != "base-config" {
		t.Fatalf("leitura pós-tamper = (%q, exit=%d), quero (base-config, 0): a raiz não é read-only", res.Stdout, res.ExitCode)
	}
}

// TestWiring_SeccompDefaultDenyOnExecPath prova que o perfil seccomp está LIGADO ao
// caminho de execução (finding attestation-enforcement-decoupling): uma syscall fora
// da allowlist do perfil EFETIVAMENTE aplicado é bloqueada no Exec (default-deny), e
// o HASH gravado no manifesto é o desse MESMO perfil que bloqueou — o hash tem poder
// de refutação, não sobrevive a uma CI verde sem imposição.
func TestWiring_SeccompDefaultDenyOnExecPath(t *testing.T) {
	store := newStore(t)
	// Perfil restritivo: só permite "read". Uma escrita (que exige "write") é negada.
	restrictive, err := seccomp.Parse([]byte(`{"version":"restrictive/v1","default_action":"deny","allowed_syscalls":["read"]}`))
	if err != nil {
		t.Fatalf("seccomp.Parse: %v", err)
	}
	launcher, err := NewLauncher(NewFakeDriver(),
		WithEventSink(NewEventStoreSink(store)),
		WithSnapshot(wiredSnapshot(t)),
		WithSeccompProfile(restrictive),
	)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	ml, err := NewMediatedLauncher(newPermitMonitor(store), launcher, "sandbox.exec")
	if err != nil {
		t.Fatalf("NewMediatedLauncher: %v", err)
	}

	// Uma leitura (syscall "read", na allowlist) é PERMITIDA — o gate não é tautológico.
	if _, err := execOnce(t, ml, "run-ok", "step-ok", ToolCall{Command: "read", Path: "etc/config"}); err != nil {
		t.Fatalf("Execute read (allowlisted) = %v, quero permitido", err)
	}
	// Uma escrita (syscall "write", FORA da allowlist) é BLOQUEADA default-deny.
	_, err = execOnce(t, ml, "run-deny", "step-deny", ToolCall{Command: "write", Path: "etc/config", Write: []byte("x")})
	if !errors.Is(err, ErrSeccompDenied) {
		t.Fatalf("Execute write sob perfil sem 'write' = %v, quero ErrSeccompDenied", err)
	}

	// O hash no manifesto é o do perfil restritivo que EFETIVAMENTE bloqueou (não o
	// default) — o hash atesta o perfil aplicado.
	evs := eventsOfType(readEvents(t, store, "run-deny"), EventExecCompleted)
	if len(evs) == 0 {
		t.Fatal("sem evento exec para run-deny")
	}
	var p lifecyclePayload
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.SeccompProfileHash != restrictive.Hash() {
		t.Fatalf("manifesto seccomp_profile_hash = %q, quero o do perfil aplicado %q", p.SeccompProfileHash, restrictive.Hash())
	}
}

// decodeExecPayload descodifica o payload do evento de exec (manifesto) do run dado.
func decodeExecPayload(t *testing.T, store *eventstore.Store, runID string) lifecyclePayload {
	t.Helper()
	evs := eventsOfType(readEvents(t, store, runID), EventExecCompleted)
	if len(evs) == 0 {
		t.Fatalf("sem evento exec para %q", runID)
	}
	var p lifecyclePayload
	if err := json.Unmarshal(evs[0].Payload, &p); err != nil {
		t.Fatalf("unmarshal payload %q: %v", runID, err)
	}
	return p
}

// overlayIDFromExec extrai o overlay_id do manifesto (evento exec) do run dado.
func overlayIDFromExec(t *testing.T, store *eventstore.Store, runID string) string {
	t.Helper()
	return decodeExecPayload(t, store, runID).OverlayID
}

// baseDigestFromExec extrai o rootfs_base_digest do manifesto (evento exec).
func baseDigestFromExec(t *testing.T, store *eventstore.Store, runID string) string {
	t.Helper()
	return decodeExecPayload(t, store, runID).RootFSBaseDigest
}
