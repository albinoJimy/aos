package sandbox

import "context"

// OpExecuteTool é o nome do span que cobre o ciclo de vida da sandbox (ADR-010,
// OTel GenAI). O SBX não puxa o SDK OTel (isso é EPIC-08); expõe uma porta mínima.
const OpExecuteTool = "execute_tool"

// OpProvisionSandbox é o nome do span que cobre a PROVISÃO de uma sandbox pelo pool
// (AOS-065): a reserva de uma VM limpa (warm hit / expansão / wait). É o span que
// transporta o custo de cold-start por span (cold_start_ms/p95_ms) exigido pelo DoD,
// distinto do span [OpExecuteTool] que cobre a EXECUÇÃO mediada (AOS-064). Sem
// segredos.
const OpProvisionSandbox = "provision_sandbox"

// Atributos de span canónicos. NENHUM transporta segredo (ADR-006): o
// credentials_handle é opaco e o segredo nunca chega aqui.
const (
	AttrOperationName = "gen_ai.operation.name"
	AttrRunID         = "aos.run_id"
	AttrStepID        = "aos.step_id"
	AttrToolName      = "gen_ai.tool.name"
	AttrDriver        = "aos.sandbox.driver"
	AttrInstanceID    = "aos.sandbox.instance_id"
	AttrExitCode      = "aos.sandbox.exit_code"
	AttrCostUSD       = "gen_ai.usage.cost"
	AttrTaint         = "aos.taint"
	AttrCredHandle    = "aos.credentials_handle" // id OPACO, nunca o segredo

	// AttrRootFSReadOnly — a raiz de FS da microVM é read-only (AOS-066). Não é
	// segredo. (A versão de imagem usa [AttrImageVersion], definido em metrics.go.)
	AttrRootFSReadOnly = "aos.sandbox.rootfs_read_only"
	// AttrSeccompHash — HASH (sha256) do perfil seccomp aplicado (AOS-066), gravado
	// no manifesto da execução. O perfil/hash NÃO são segredos (ADR-006).
	AttrSeccompHash = "aos.sandbox.seccomp_profile_hash"
	// AttrSeccompVersion — versão tamper-evident ("tag#digest12") do perfil seccomp.
	AttrSeccompVersion = "aos.sandbox.seccomp_profile_version"
	// AttrRootFSBaseDigest — digest do snapshot base read-only EFETIVAMENTE montado
	// (AOS-066). Prova, no span, de que o rootfs foi montado (não só declarado). Não
	// é segredo. Só presente quando o overlay read-only é montado (WithSnapshot).
	AttrRootFSBaseDigest = "aos.sandbox.rootfs_base_digest"
	// AttrOverlayID — id do overlay efémero desta execução (AOS-065/066). Único por
	// restore. Não é segredo. Só presente quando o overlay read-only é montado.
	AttrOverlayID = "aos.sandbox.overlay_id"
)

// Span é uma unidade de trabalho observável mínima (porta; ver [NoopTracer]).
type Span interface {
	SetAttribute(key string, value any)
	End()
}

// Tracer abre spans. O default do [Launcher] é [NoopTracer].
type Tracer interface {
	StartSpan(ctx context.Context, name string) (context.Context, Span)
}

// NoopTracer descarta os spans (default).
type NoopTracer struct{}

// StartSpan implementa [Tracer].
func (NoopTracer) StartSpan(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopSpan struct{}

func (noopSpan) SetAttribute(string, any) {}
func (noopSpan) End()                     {}
