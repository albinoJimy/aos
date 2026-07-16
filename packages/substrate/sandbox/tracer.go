package sandbox

import "context"

// OpExecuteTool é o nome do span que cobre o ciclo de vida da sandbox (ADR-010,
// OTel GenAI). O SBX não puxa o SDK OTel (isso é EPIC-08); expõe uma porta mínima.
const OpExecuteTool = "execute_tool"

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
