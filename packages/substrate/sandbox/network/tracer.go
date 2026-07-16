package network

import "context"

// OpEgressDecision é o nome do span que cobre uma decisão de egress (ADR-010). O
// SBX não puxa o SDK OTel (isso é EPIC-08); expõe uma porta mínima.
const OpEgressDecision = "egress_decision"

// Atributos de span canónicos da decisão de egress. NENHUM transporta segredo
// (ADR-006): o destino e a decisão NÃO são segredos; nenhuma chave/token entra aqui.
const (
	AttrOperationName  = "gen_ai.operation.name"
	AttrPrincipalNHI   = "aos.principal.nhi_id"
	AttrPrincipalClass = "aos.principal.agent_class"
	AttrEgressDest     = "aos.egress.destination" // "host:port"/"ip:port", não-secreto
	AttrEgressAllowed  = "aos.egress.allowed"
	AttrEgressReason   = "aos.egress.reason"
	AttrPolicyVersion  = "aos.egress.policy_version"
)

// Span é uma unidade de trabalho observável mínima (porta; ver [NoopTracer]).
type Span interface {
	SetAttribute(key string, value any)
	End()
}

// Tracer abre spans. O default do [EgressFilter] é [NoopTracer].
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
