package sandbox

import (
	"context"
	"fmt"

	"github.com/aos-ref/substrate/sandbox/seccomp"
)

// Taint é o nível de confiança de um resultado. No SBX só existe um valor
// possível: untrusted. O resultado de uma sandbox é SEMPRE untrusted (ADR-005) —
// prepara o taint tracking control/data-plane (AOS-069).
type Taint string

// TaintUntrusted é o único taint que um [ExecResult] pode carregar.
const TaintUntrusted Taint = "untrusted"

// DriverKind selecciona o runtime da sandbox por configuração. O contrato de
// execução é IDÊNTICO entre kinds (a mesma sequência create → exec → destroy e o
// mesmo [ExecResult]); só muda a fronteira de isolamento concreta.
type DriverKind string

const (
	// DriverFirecracker — microVM Firecracker (fronteira de virtualização de
	// hardware, mais forte). Skeleton documentado neste ambiente.
	DriverFirecracker DriverKind = "firecracker"
	// DriverGVisor — sandbox gVisor (interceptação de syscalls em espaço de
	// utilizador, mais leve). Skeleton documentado neste ambiente.
	DriverGVisor DriverKind = "gvisor"
	// DriverFake — driver de referência determinista in-process (testes). Modela
	// o jail e impõe as invariantes de isolamento; NUNCA usar em produção.
	DriverFake DriverKind = "fake"
)

// seccompEnforcementFor diz QUEM impõe, num dado driver, o perfil propagado em
// [Spec.Seccomp] (AOS-351). É a única fonte desta verdade: o [Launcher] usa-a para
// qualificar o hash em cada [LifecycleEvent], em vez de o selar como se fosse
// sempre imposição.
//
// Hoje só o [DriverFake] lê o perfil e o aplica no [SandboxDriver.Exec]. Os
// drivers reais recebem a [Spec] e ignoram-na (ver [Spec.Seccomp]), pelo que a
// resposta honesta é [SeccompEnforcedByNone] — fail-closed: um driver novo é
// «não impõe» até provar o contrário aqui.
func seccompEnforcementFor(kind DriverKind) SeccompEnforcement {
	if kind == DriverFake {
		return SeccompEnforcedByDriver
	}
	return SeccompEnforcedByNone
}

// Isolation descreve — e o [Launcher] IMPÕE fail-closed — as propriedades de
// isolamento ao nível do kernel da microVM (ADR-004). Em AOS-064 o foco é
// processo/FS/kernel; rede é AOS-067, overlay/seccomp é AOS-066.
type Isolation struct {
	// NoHostSocket: o guest não vê o socket de controlo/Docker do host.
	NoHostSocket bool
	// NoSharedNetNS: sem partilha do namespace de rede do host.
	NoSharedNetNS bool
	// NoSharedPIDNS: sem partilha do namespace de PID do host.
	NoSharedPIDNS bool
	// RootFSReadOnly: raiz de FS read-only (o overlay efémero é AOS-066).
	RootFSReadOnly bool
}

// HardenedIsolation é a isolação canónica exigida por AOS-064: sem socket do
// host, sem namespace de rede/PID partilhado, rootfs read-only. O [Launcher]
// rejeita fail-closed qualquer spec que a enfraqueça.
func HardenedIsolation() Isolation {
	return Isolation{
		NoHostSocket:   true,
		NoSharedNetNS:  true,
		NoSharedPIDNS:  true,
		RootFSReadOnly: true,
	}
}

// hardened indica se a isolação satisfaz o mínimo AOS-064 (fail-closed).
func (iso Isolation) hardened() bool {
	return iso.NoHostSocket && iso.NoSharedNetNS && iso.NoSharedPIDNS
}

// Spec descreve a microVM a criar. É construída pelo [Launcher] a partir do
// [ExecRequest] — o chamador não a fabrica directamente.
type Spec struct {
	RunID     string
	StepID    string
	Kind      DriverKind
	Isolation Isolation
	// Seccomp é o perfil seccomp default-deny (AOS-066) que o [Launcher] propaga ao
	// driver. QUEM o impõe depende do driver — e o manifesto tem de o dizer (AOS-351):
	//
	//   - [FakeDriver]: IMPÕE-o no [SandboxDriver.Exec] (default-deny: uma syscall
	//     fora da allowlist devolve [ErrSeccompDenied]). Só aqui o
	//     [seccomp.Profile.Hash] gravado no manifesto atesta o perfil REALMENTE
	//     aplicado, por ser o MESMO objecto que o Exec consulta.
	//   - [FirecrackerDriver]/[GVisorDriver]: NÃO lêem este campo, e o
	//     [GuestExecutor] nem sequer o transporta (o wire host→guest leva a
	//     [ToolCall] e mais nada). Nenhum byte deste perfil chega ao guest: para
	//     estes drivers o hash é uma DECLARAÇÃO de configuração, não uma atestação
	//     de imposição. A allowlist de syscalls do guest, se existir, é a que a
	//     imagem/o runtime aplicarem — não esta.
	//
	// O evento de ciclo de vida qualifica-o em [LifecycleEvent.SeccompEnforcedBy],
	// derivado por [seccompEnforcementFor]. Nil só nos testes que invocam o driver
	// directamente (gate ignorado).
	Seccomp *seccomp.Profile
	// RootFS é a montagem raiz-read-only + overlay efémero (AOS-066 sobre AOS-065)
	// desta execução. Quando não-nil, o driver roteia as ESCRITAS para o overlay
	// (copy-up) e as LEITURAS caem no base read-only; o base NUNCA é mutado e o
	// overlay é descartado no destroy (a execução N+1 não observa a de N). Nil mantém
	// o jail in-memory legado (sem camada base read-only) — os drivers directos.
	RootFS *RootFS
}

// Instance é o handle de uma microVM criada. Os campos de isolamento são a prova
// observável (para testes/audit) de que as invariantes foram impostas. O estado
// privado do driver vive em [Instance.handle] (nunca um handle do host).
type Instance struct {
	// ID é o identificador efémero da microVM (único por execução).
	ID string
	// Kind é o driver que a criou.
	Kind DriverKind
	// NoHostSocket/NoSharedNetNS/NoSharedPIDNS são as invariantes impostas (ADR-004).
	NoHostSocket  bool
	NoSharedNetNS bool
	NoSharedPIDNS bool
	// handle é estado privado do driver (ex.: o jail in-memory do fake). Nunca é
	// um handle do host. Não-exportado: opaco ao exterior.
	handle any
}

// ToolCall é a descrição do efeito a correr DENTRO da microVM. É dados inertes
// para a sandbox: um comando lógico, argumentos e, opcionalmente, um caminho no
// jail a ler/escrever. NÃO contém segredos.
type ToolCall struct {
	// ToolID identifica a tool (correlação/observabilidade).
	ToolID string
	// Command é o comando lógico a executar no guest.
	Command string
	// Args são os argumentos do comando.
	Args []string
	// Path é um caminho RELATIVO ao jail que a tool lê/escreve (opcional).
	Path string
	// Write, se não-nil, é o conteúdo a escrever em Path dentro do jail.
	Write []byte
}

// ExecRequest é o pedido de execução entregue ao driver: identidade de execução
// (run_id/step_id), a tool call e o handle de credencial OPACO (ADR-006). Não
// transporta tipos do Reference Monitor — a autorização é resolvida a montante,
// pelo RM, antes de o efeito chegar aqui.
type ExecRequest struct {
	RunID  string
	StepID string
	Call   ToolCall
	// CredentialsHandle é um id OPACO e NÃO-SECRETO (ADR-006). A sandbox nunca vê
	// o segredo em claro; a resolução server-side é do broker (AOS-070).
	CredentialsHandle string
}

func (r ExecRequest) validate() error {
	if r.RunID == "" {
		return ErrEmptyRunID
	}
	if r.StepID == "" {
		return ErrEmptyStepID
	}
	return nil
}

// Artifact é um artefacto produzido pela execução (ficheiro de saída, etc.).
type Artifact struct {
	Name string
	Data []byte
}

// ExecResult é o resultado da execução na microVM. O taint é SEMPRE untrusted:
// [ExecResult.Taint] devolve [TaintUntrusted] independentemente de como o valor
// foi construído (um ExecResult{} zero também é untrusted) — a garantia é do TIPO,
// não de um campo mutável (ADR-005, prepara AOS-069).
type ExecResult struct {
	Stdout    []byte
	Artifacts []Artifact
	ExitCode  int
}

// Taint devolve SEMPRE [TaintUntrusted]. Não há forma de produzir um resultado de
// sandbox trusted.
func (ExecResult) Taint() Taint { return TaintUntrusted }

// IsUntrusted é sempre verdadeiro (ver [ExecResult.Taint]).
func (ExecResult) IsUntrusted() bool { return true }

// capability é a testemunha NÃO-EXPORTADA que autoriza uma operação de driver.
// Só o [Launcher] a minta (dentro do ciclo de vida invocado sob despacho do RM).
// Como o tipo é não-exportado, nenhum pacote externo consegue nomeá-lo: não pode
// chamar as operações do driver NEM implementar [SandboxDriver]. É o coração do
// no-bypass estrutural (ADR-002), à imagem do permitToken não-forjável do RM.
type capability struct {
	// launcher liga a capability ao Launcher que a mintou. Não é decorativo: cada
	// operação de driver VALIDA-O em runtime via [capability.sanctioned] e recusa
	// [ErrUnsanctionedCapability] se for nil (um zero-value capability{} mintado por
	// engano dentro do pacote). Assim a evidência é REAL em runtime — não apenas o
	// selo do tipo não-exportado (compile-time) — alinhando com o permitToken
	// não-forjável validado em runtime pelo Reference Monitor (monitor.go).
	launcher *Launcher
}

// sanctioned reporta se a capability foi mintada por um [Launcher] (o único que a
// pode ligar a si próprio em [Launcher.run]). É a verificação de runtime do
// no-bypass estrutural: uma capability zero-value não é sancionada.
func (c capability) sanctioned() bool { return c.launcher != nil }

// SandboxDriver é o contrato do runtime de isolamento: criar → executar →
// destruir. As três operações exigem uma [capability] (não-exportada), pelo que
// a interface é SELADA — só os drivers first-party deste pacote a implementam e
// só o [Launcher] as invoca (no-bypass estrutural).
type SandboxDriver interface {
	// Create arranca uma microVM segundo a spec e devolve o handle. Impõe as
	// invariantes de isolamento (sem socket/namespace do host).
	Create(ctx context.Context, cap capability, spec Spec) (Instance, error)
	// Exec corre a tool call DENTRO da microVM e devolve o resultado (untrusted).
	Exec(ctx context.Context, cap capability, inst Instance, req ExecRequest) (ExecResult, error)
	// Destroy termina a microVM e liberta os recursos (idempotente).
	Destroy(ctx context.Context, cap capability, inst Instance) error
	// Kind identifica o driver. Não exige capability (é metadado, não um efeito).
	Kind() DriverKind
}

// NewDriver selecciona um driver por [DriverKind] com contrato IDÊNTICO. O driver
// devolvido destina-se a ser COMPOSTO num [Launcher]; as suas operações de ciclo
// de vida são gated por capability e só o Launcher as invoca (ADR-002).
func NewDriver(kind DriverKind) (SandboxDriver, error) {
	switch kind {
	case DriverFirecracker:
		return NewFirecrackerDriver(), nil
	case DriverGVisor:
		return NewGVisorDriver(), nil
	case DriverFake:
		return NewFakeDriver(), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownDriver, kind)
	}
}
