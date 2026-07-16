package sandbox

import (
	"context"
	"strconv"
	"sync/atomic"
)

// GVisorDriver é o skeleton do driver gVisor (sandbox de espaço de utilizador que
// intercepta syscalls, ADR-004). Neste ambiente documenta a integração real e é
// fail-closed sem um [GuestExecutor] injectado. O contrato create → exec → destroy
// e o [ExecResult] (untrusted) são IDÊNTICOS aos do Firecracker e do fake.
//
// # Integração real (documentação, não executada aqui)
//
// Cada execução corre num sandbox runsc dedicado:
//   - Sem acesso ao socket do host: o runsc não monta o Docker socket nem IPC do
//     host; o gofer media o FS numa árvore dedicada (rootfs read-only + overlay,
//     AOS-066).
//   - Sem partilha de namespace de rede/PID do host: netns próprio (rede
//     default-deny, AOS-067) e PID namespace isolado. O kernel do host é protegido
//     pela interposição de syscalls do runsc (perfil seccomp mínimo, AOS-066).
//   - O credentials_handle é resolvido/injectado server-side (ADR-006, AOS-070).
type GVisorDriver struct {
	exec GuestExecutor
	seq  atomic.Uint64
}

// GVisorOption configura o [GVisorDriver].
type GVisorOption func(*GVisorDriver)

// WithGVisorExecutor injecta o executor de guest (a integração real / um mock
// determinista de teste). Sem ele, Create devolve [ErrDriverUnavailable].
func WithGVisorExecutor(e GuestExecutor) GVisorOption {
	return func(d *GVisorDriver) { d.exec = e }
}

// NewGVisorDriver constrói o skeleton gVisor.
func NewGVisorDriver(opts ...GVisorOption) *GVisorDriver {
	d := &GVisorDriver{}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Kind implementa [SandboxDriver].
func (*GVisorDriver) Kind() DriverKind { return DriverGVisor }

// Create implementa [SandboxDriver]. Impõe as invariantes de isolamento e arranca
// o sandbox via o executor. Fail-closed sem executor.
func (d *GVisorDriver) Create(_ context.Context, cap capability, spec Spec) (Instance, error) {
	if !cap.sanctioned() {
		return Instance{}, ErrUnsanctionedCapability
	}
	if err := enforceIsolation(spec.Isolation); err != nil {
		return Instance{}, err
	}
	if d.exec == nil {
		return Instance{}, ErrDriverUnavailable
	}
	id := "gv-" + spec.RunID + "-" + spec.StepID + "-" + strconv.FormatUint(d.seq.Add(1), 10)
	return Instance{
		ID:            id,
		Kind:          DriverGVisor,
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
// contenção real é a interposição de syscalls do runsc, não este código; um
// [GuestExecutor] ingénuo NÃO recebe rede de segurança Go. A invariante 'escape
// bloqueado' pode ser modelada em teste injectando um executor que aplique as
// verificações (ver TestSecurity_SkeletonEscapeCanBeModeled).
func (d *GVisorDriver) Exec(ctx context.Context, cap capability, inst Instance, req ExecRequest) (ExecResult, error) {
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

// Destroy implementa [SandboxDriver]: termina o sandbox runsc (no-op idempotente
// no skeleton).
func (d *GVisorDriver) Destroy(_ context.Context, cap capability, _ Instance) error {
	if !cap.sanctioned() {
		return ErrUnsanctionedCapability
	}
	return nil
}
