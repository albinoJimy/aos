package sandbox

import (
	"context"
	"strconv"
	"sync/atomic"
)

// GuestExecutor abstrai a execução de uma [ToolCall] DENTRO de um guest já
// arrancado e isolado (microVM/gVisor). A implementação REAL conduz o binário
// externo (a API socket do Firecracker — do lado do HOST apenas — via jailer, ou
// o runsc do gVisor) e é infra (exige KVM/host support), fora deste módulo e das
// suas dependências Go. Injectar um executor permite que os skeletons satisfaçam o
// contrato IDÊNTICO de forma determinista (testes); sem executor, o driver é
// [ErrDriverUnavailable] neste ambiente. O executor NUNCA recebe um handle do host
// (socket/namespace) — só a instância isolada e a tool call.
type GuestExecutor interface {
	RunInGuest(ctx context.Context, inst Instance, call ToolCall) (stdout []byte, artifacts []Artifact, exitCode int, err error)
}

// FirecrackerDriver é o skeleton do driver Firecracker (microVM, ADR-004). Neste
// ambiente (Windows, sem KVM) documenta a integração real e é fail-closed sem um
// [GuestExecutor] injectado.
//
// # Integração real (documentação, não executada aqui)
//
// Cada execução corre numa microVM DEDICADA arrancada via jailer:
//   - A API socket de controlo do Firecracker vive do lado do HOST apenas
//     (--api-sock num diretório do jailer); NUNCA é mapeada para dentro do guest.
//     Não há Docker socket nem IPC do host visível ao guest (ADR-004).
//   - Sem partilha de namespace de rede/PID do host: a fronteira é de virtualização
//     de hardware (kernel do guest separado); a rede é default-deny (AOS-067, tap
//     dedicado sem egress). O PID do host é invisível ao guest.
//   - rootfs read-only + overlay efémero descartado no destroy (AOS-066).
//   - O credentials_handle é resolvido/injectado server-side (ADR-006, AOS-070);
//     o segredo nunca entra no contexto do agente nem no guest em claro por esta via.
//
// O contrato create → exec → destroy e o [ExecResult] (untrusted) são IDÊNTICOS
// aos do [FakeDriver] e do [GVisorDriver]: só muda a fronteira concreta.
type FirecrackerDriver struct {
	exec GuestExecutor
	seq  atomic.Uint64
}

// FirecrackerOption configura o [FirecrackerDriver].
type FirecrackerOption func(*FirecrackerDriver)

// WithFirecrackerExecutor injecta o executor de guest (a integração real / um mock
// determinista de teste). Sem ele, Create devolve [ErrDriverUnavailable].
func WithFirecrackerExecutor(e GuestExecutor) FirecrackerOption {
	return func(d *FirecrackerDriver) { d.exec = e }
}

// NewFirecrackerDriver constrói o skeleton Firecracker.
func NewFirecrackerDriver(opts ...FirecrackerOption) *FirecrackerDriver {
	d := &FirecrackerDriver{}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Kind implementa [SandboxDriver].
func (*FirecrackerDriver) Kind() DriverKind { return DriverFirecracker }

// Create implementa [SandboxDriver]. Impõe as invariantes de isolamento e arranca
// a microVM via o executor. Fail-closed sem executor (sem KVM neste ambiente).
func (d *FirecrackerDriver) Create(_ context.Context, cap capability, spec Spec) (Instance, error) {
	if !cap.sanctioned() {
		return Instance{}, ErrUnsanctionedCapability
	}
	if err := enforceIsolation(spec.Isolation); err != nil {
		return Instance{}, err
	}
	if d.exec == nil {
		return Instance{}, ErrDriverUnavailable
	}
	id := "fc-" + spec.RunID + "-" + spec.StepID + "-" + strconv.FormatUint(d.seq.Add(1), 10)
	return Instance{
		ID:            id,
		Kind:          DriverFirecracker,
		NoHostSocket:  true,
		NoSharedNetNS: true,
		NoSharedPIDNS: true,
	}, nil
}

// Exec implementa [SandboxDriver]: delega no executor de guest e força o taint
// untrusted no resultado.
//
// CONTENÇÃO: o skeleton NÃO faz qualquer verificação de escape ao nível Go
// (traversal/symlink/metacaractere) — ao contrário do [FakeDriver]. Por desenho a
// contenção real é a fronteira microVM/jailer, não este código; um [GuestExecutor]
// ingénuo NÃO recebe rede de segurança Go. A invariante 'escape bloqueado' pode ser
// modelada em teste injectando um executor que aplique as verificações (ver
// TestSecurity_SkeletonEscapeCanBeModeled).
func (d *FirecrackerDriver) Exec(ctx context.Context, cap capability, inst Instance, req ExecRequest) (ExecResult, error) {
	if !cap.sanctioned() {
		return ExecResult{}, ErrUnsanctionedCapability
	}
	if d.exec == nil {
		return ExecResult{}, ErrDriverUnavailable
	}
	stdout, arts, exit, err := d.exec.RunInGuest(ctx, inst, req.Call)
	if err != nil {
		return ExecResult{}, err
	}
	return newResult(stdout, arts, exit), nil
}

// Destroy implementa [SandboxDriver]: termina a microVM (o real descarta o overlay
// e mata o jailer; o skeleton é no-op idempotente).
func (d *FirecrackerDriver) Destroy(_ context.Context, cap capability, _ Instance) error {
	if !cap.sanctioned() {
		return ErrUnsanctionedCapability
	}
	return nil
}

// enforceIsolation rejeita fail-closed uma spec que enfraqueça a fronteira (sem
// socket do host, sem namespace de rede/PID partilhado). Partilhado pelos
// skeletons Firecracker/gVisor.
func enforceIsolation(iso Isolation) error {
	if !iso.NoHostSocket {
		return ErrHostSocketForbidden
	}
	if !iso.NoSharedNetNS || !iso.NoSharedPIDNS {
		return ErrSharedNamespaceForbidden
	}
	return nil
}
