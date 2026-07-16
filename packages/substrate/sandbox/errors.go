package sandbox

import "errors"

var (
	// ErrDriverUnavailable — o driver real (Firecracker/gVisor) não está
	// disponível neste ambiente (sem KVM/host support). Os skeletons devolvem-no
	// no Create quando não têm um [GuestExecutor] injectado. Fail-closed: nunca
	// executam um efeito fora de uma microVM real.
	ErrDriverUnavailable = errors.New("sandbox: driver microVM nao disponivel neste ambiente (sem KVM/host support)")

	// ErrUnknownDriver — [DriverKind] não reconhecido em [NewDriver].
	ErrUnknownDriver = errors.New("sandbox: driver desconhecido")

	// ErrJailEscape — tentativa de escapar do jail (path traversal, symlink para
	// fora, metacaractere de shell) BLOQUEADA antes de tocar o host.
	ErrJailEscape = errors.New("sandbox: tentativa de escape do jail bloqueada")

	// ErrUnsanctionedCapability — uma operação de driver foi chamada com uma
	// [capability] que não foi mintada por um [Launcher] (launcher nil). É a
	// verificação de RUNTIME que torna a ligação capability→Launcher uma evidência
	// real (não só o selo do tipo não-exportado em compile-time), à imagem do
	// permitToken não-forjável validado pelo Reference Monitor (ADR-002).
	ErrUnsanctionedCapability = errors.New("sandbox: capability nao sancionada por um Launcher")

	// ErrHostSocketForbidden — a spec pediria acesso ao socket do host. Fail-closed:
	// a microVM nunca expõe o socket do host (ADR-004).
	ErrHostSocketForbidden = errors.New("sandbox: acesso ao socket do host proibido")

	// ErrSharedNamespaceForbidden — a spec pediria partilha do namespace de
	// rede/PID do host. Fail-closed: a microVM não partilha namespace do host.
	ErrSharedNamespaceForbidden = errors.New("sandbox: partilha de namespace de rede/PID do host proibida")

	// ErrEmptyRunID — run_id vazio (obrigatório para correlacionar no Event Store).
	ErrEmptyRunID = errors.New("sandbox: run_id vazio")

	// ErrEmptyStepID — step_id vazio (obrigatório para correlacionar no Event Store).
	ErrEmptyStepID = errors.New("sandbox: step_id vazio")

	// ErrNilDriver — [NewLauncher] sem driver.
	ErrNilDriver = errors.New("sandbox: driver nil")

	// ErrNilMonitor — [NewMediatedLauncher] sem Reference Monitor.
	ErrNilMonitor = errors.New("sandbox: reference monitor nil")

	// ErrEmptyToolID — [NewMediatedLauncher] sem tool id de registo.
	ErrEmptyToolID = errors.New("sandbox: tool id vazio")
)

// DeniedError reporta que o Reference Monitor NÃO permitiu a tool call: nenhum
// efeito de sandbox ocorreu. É devolvido por [MediatedLauncher.Execute] quando a
// decisão do RM não é permit (deny/escalate), preservando o código estável da
// decisão para o chamador ramificar sem fazer parse de texto.
type DeniedError struct {
	Effect string // "deny" | "escalate"
	Code   string // código estável do RM (ex.: "E_DENIED_BY_HOOK")
	Reason string
}

func (e *DeniedError) Error() string {
	return "sandbox: tool call nao permitida pelo reference monitor: " + e.Effect + " (" + e.Code + "): " + e.Reason
}
