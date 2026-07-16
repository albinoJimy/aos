package messaging

import "context"

// OpMessageVerify é o nome do span que cobre uma decisão de verificação de
// mensagem inter-agente (AOS-073, DoD "Spans OTel"; ADR-010). O módulo NÃO puxa o
// SDK OTel (isso é EPIC-08) — expõe, à imagem de substrate/sandbox/network, uma
// porta mínima [Tracer]/[Span] que o wiring liga a um exportador real.
const OpMessageVerify = "msg_verify"

// Atributos de span canónicos da decisão de verificação. NENHUM transporta
// segredo (ADR-006): a origem CLAMADA, a acção, a referência (id), a decisão e o
// motivo NÃO são segredos; o PAYLOAD e qualquer chave NUNCA entram num span.
const (
	AttrOperationName       = "gen_ai.operation.name"
	AttrClaimedOrigin       = "aos.msg.claimed_origin"       // origem CLAMADA (spoofável), não autenticada
	AttrAction              = "aos.msg.action"               // capability pedida
	AttrReference           = "aos.msg.reference_id"         // id da referência (não-secreto)
	AttrDecision            = "aos.msg.decision"             // "allow" | "deny"
	AttrRejectReason        = "aos.msg.reject_reason"        // Reason* em caso de deny
	AttrOriginAuthenticated = "aos.msg.origin_authenticated" // origem criptograficamente comprovada?
	AttrPolicyVersion       = "aos.msg.policy_version"
)

// Valores estáveis do atributo de decisão.
const (
	decisionAllow = "allow"
	decisionDeny  = "deny"
)

// Span é uma unidade de trabalho observável mínima (porta; ver [NoopTracer]).
type Span interface {
	SetAttribute(key string, value any)
	End()
}

// Tracer abre spans. O default do [Verifier] é [NoopTracer] (injectável via
// [WithTracer]).
type Tracer interface {
	StartSpan(ctx context.Context, name string) (context.Context, Span)
}

// NoopTracer descarta os spans (default). Mantém o módulo zero-dep: sem tracer
// injectado, a verificação não emite nada e não incorre custo.
type NoopTracer struct{}

// StartSpan implementa [Tracer].
func (NoopTracer) StartSpan(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopSpan struct{}

func (noopSpan) SetAttribute(string, any) {}
func (noopSpan) End()                     {}
