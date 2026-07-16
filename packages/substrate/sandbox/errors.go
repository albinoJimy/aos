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

	// ErrEmptyImageVersion — [NewSnapshot] sem versão de imagem (o eixo de
	// imutabilidade do snapshot base).
	ErrEmptyImageVersion = errors.New("sandbox: versao de imagem vazia")

	// ErrOverlayDiscarded — escrita/leitura sobre um [Overlay] já descartado. O
	// estado sujo de uma execução não ressuscita (AOS-065): fail-closed.
	ErrOverlayDiscarded = errors.New("sandbox: overlay efemero ja descartado")

	// ErrNilSnapshot — [NewPool] sem snapshot base.
	ErrNilSnapshot = errors.New("sandbox: snapshot nil")

	// ErrInvalidPoolSize — [NewPool] com tamanho de pré-aquecimento inválido (< 0).
	ErrInvalidPoolSize = errors.New("sandbox: tamanho de pool invalido")

	// ErrPoolExhausted — o pool não tem VM limpa disponível e a política de
	// degradação recusou/expirou (fail-closed). NUNCA se serve estado sujo em vez
	// de devolver este erro (AOS-065).
	ErrPoolExhausted = errors.New("sandbox: pool de microVMs esgotado (politica fail-closed)")

	// ErrPoolClosed — reserva sobre um [Pool] já fechado.
	ErrPoolClosed = errors.New("sandbox: pool fechado")

	// ErrReadOnlyRoot — tentativa de ESCRITA na raiz READ-ONLY do FS da microVM
	// (AOS-066). A raiz é o snapshot base imutável; toda a escrita TEM de ir para o
	// overlay efémero. Uma escrita fora do overlay (na raiz) falha de forma
	// controlada com este erro (nunca panic; o base NUNCA é mutado). Fail-closed.
	ErrReadOnlyRoot = errors.New("sandbox: escrita na raiz read-only rejeitada (use o overlay efemero)")

	// ErrNilOverlay — [MountReadOnly] sem overlay (AOS-065). Um [RootFS] sem a
	// camada de escrita efémera não pode existir (fail-closed).
	ErrNilOverlay = errors.New("sandbox: overlay efemero nil")

	// ErrReadOnlyRootRequired — a spec/isolação não exige raiz read-only (AOS-066).
	// Fail-closed: a microVM nunca corre com a raiz de FS escrevível.
	ErrReadOnlyRootRequired = errors.New("sandbox: raiz de FS read-only obrigatoria (AOS-066)")

	// ErrNilSeccompProfile — perfil seccomp nil onde é obrigatório (AOS-066). A
	// microVM nunca corre sem um perfil seccomp default-deny (fail-closed).
	ErrNilSeccompProfile = errors.New("sandbox: perfil seccomp nil (AOS-066)")

	// ErrSeccompDenied — o [Exec] tentou uma syscall FORA da allowlist do perfil
	// seccomp EFETIVAMENTE aplicado (AOS-066). Default-deny fail-closed: a syscall é
	// bloqueada e o efeito não ocorre. É a imposição, no caminho de execução, do
	// mesmo perfil cujo hash o manifesto atesta — o hash deixa de sobreviver a uma CI
	// verde sem imposição real.
	ErrSeccompDenied = errors.New("sandbox: syscall fora da allowlist seccomp bloqueada (default-deny, AOS-066)")
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
