package network

import (
	"context"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// Razões estáveis de uma decisão de egress (para o chamador ramificar sem parse de
// texto, e para o audit/span).
const (
	ReasonAllowed     = "egress permitido pela allowlist do principal"
	ReasonNotInList   = "destino fora da allowlist (default-deny)"
	ReasonNoPolicy    = "sem allowlist para o principal (fail-closed)"
	ReasonInvalidDest = "destino de egress invalido (fail-closed)"
	ReasonAuditFailed = "audit de egress indisponivel (fail-closed)"
	// ReasonUnverifiableEgress — o call declara uma capability de rede (cap:http.*,
	// cap:net.*) mas o Resource.Type não é de rede, logo o destino não é derivável nem
	// verificável contra a allowlist. Fail-closed: nega-se em vez de abster (fecha o
	// vector de exfiltração via tool com tipo mislabelado).
	ReasonUnverifiableEgress = "egress com capability de rede e recurso nao-rede (fail-closed)"
)

// Decision é o veredicto de uma decisão de egress. É o que o Reference Monitor
// APLICA (via [EgressHook]) na cadeia de mediação — o filtro apenas DECIDE.
type Decision struct {
	// Allow é o veredicto: true só se a allowlist do principal permitir o destino.
	Allow bool
	// Reason é a razão estável (ver Reason*).
	Reason string
	// PolicyVersion é a versão tamper-evident da allowlist consultada (vazia quando
	// não houve allowlist resolúvel).
	PolicyVersion string
}

// EgressFilter é o núcleo de decisão default-deny (AOS-067): consulta a allowlist do
// principal (via [EgressPolicyResolver]), decide allow/deny por (principal, destino)
// e, num BLOQUEIO, sela um evento de segurança no audit WORM. FAIL-CLOSED em toda a
// borda — destino inválido, allowlist ausente/malformada ou audit indisponível
// resultam em DENY, nunca em bypass.
type EgressFilter struct {
	resolver   EgressPolicyResolver
	sink       SecurityAuditSink
	tracer     Tracer
	sealAllows bool
	now        func() time.Time
}

// EgressFilterOption configura o [EgressFilter].
type EgressFilterOption func(*EgressFilter)

// WithSecurityAuditSink injecta o sink WORM que sela os eventos de segurança (egress
// bloqueado). É OBRIGATÓRIO: [NewEgressFilter] recusa ([ErrNilSink]) sem ele, pois um
// bloqueio de egress é um evento de segurança que TEM de ser selado no WORM
// tamper-evident (AOS-067/AOS-072) — nunca um deny silencioso não-auditado.
func WithSecurityAuditSink(s SecurityAuditSink) EgressFilterOption {
	return func(f *EgressFilter) { f.sink = s }
}

// WithTracer injecta a porta de observabilidade (default [NoopTracer]).
func WithTracer(t Tracer) EgressFilterOption { return func(f *EgressFilter) { f.tracer = t } }

// WithAuditAllows liga o registo dos egress PERMITIDOS no WORM (observabilidade).
// Por omissão só os BLOQUEIOS (eventos de segurança) são selados. Com esta opção, um
// allow cujo registo falhe degrada para DENY (audit-before-effect).
func WithAuditAllows() EgressFilterOption { return func(f *EgressFilter) { f.sealAllows = true } }

// withClock injecta o relógio observacional (uso interno/testes). O timestamp NÃO
// entra na decisão (determinismo) — só no registo observacional.
func withClock(fn func() time.Time) EgressFilterOption {
	return func(f *EgressFilter) { f.now = fn }
}

// NewEgressFilter constrói o filtro sobre um resolver (obrigatório) e um sink de
// segurança WORM (obrigatório, via [WithSecurityAuditSink]). Sem resolver não há forma
// de resolver uma allowlist ([ErrNilResolver]); sem sink um bloqueio ficaria por selar
// silenciosamente no WORM ([ErrNilSink]). Ambos são fail-closed no arranque: é
// impossível compor um filtro de enforcement sem allowlist nem sem audit.
func NewEgressFilter(resolver EgressPolicyResolver, opts ...EgressFilterOption) (*EgressFilter, error) {
	if resolver == nil {
		return nil, ErrNilResolver
	}
	f := &EgressFilter{
		resolver: resolver,
		tracer:   NoopTracer{},
		now:      time.Now,
	}
	for _, o := range opts {
		o(f)
	}
	// FAIL-CLOSED: o sink WORM é obrigatório — um bloqueio TEM de ser selável.
	if f.sink == nil {
		return nil, ErrNilSink
	}
	if f.tracer == nil {
		f.tracer = NoopTracer{}
	}
	if f.now == nil {
		f.now = time.Now
	}
	return f, nil
}

// Decide é a DECISÃO de egress default-deny para (principal, destino). Devolve a
// [Decision] (Allow/Reason/PolicyVersion) e, separadamente, um erro operacional APENAS
// quando a selagem no WORM falha — caso em que a decisão é FORÇADA a deny
// (audit-before-effect: um egress não-auditável não é permitido). Em toda a outra
// borda a decisão é deny fail-closed sem erro. NUNCA devolve Allow=true sem uma regra
// explícita da allowlist do principal a casar o destino.
func (f *EgressFilter) Decide(ctx context.Context, principal referencemonitor.Principal, dest Destination) (Decision, error) {
	ctx, span := f.tracer.StartSpan(ctx, OpEgressDecision)
	defer span.End()
	span.SetAttribute(AttrOperationName, OpEgressDecision)
	span.SetAttribute(AttrPrincipalNHI, principal.NHIID)
	span.SetAttribute(AttrPrincipalClass, principal.AgentClass)
	span.SetAttribute(AttrEgressDest, dest.String())

	// (1) FAIL-CLOSED: destino inválido (sem porta/localizador) não é avaliável.
	if !dest.valid() {
		return f.deny(ctx, span, principal, dest, ReasonInvalidDest, "")
	}

	// (2) Resolve a allowlist do principal. Ausente/erro ⇒ default-deny total.
	policy, err := f.resolver.Resolve(ctx, principal)
	if err != nil || policy == nil {
		return f.deny(ctx, span, principal, dest, ReasonNoPolicy, "")
	}
	version := policy.Version()

	// (3) Avalia default-deny (escopado ao principal/classe).
	if policy.Evaluate(principal, dest) != EffectAllow {
		return f.deny(ctx, span, principal, dest, ReasonNotInList, version)
	}

	// (4) ALLOW. Regista opcionalmente (observabilidade); um allow não-auditável
	// degrada para deny quando o registo de allows está ligado (audit-before-effect).
	span.SetAttribute(AttrEgressAllowed, true)
	span.SetAttribute(AttrEgressReason, ReasonAllowed)
	span.SetAttribute(AttrPolicyVersion, version)
	if f.sealAllows {
		if err := f.seal(ctx, principal, dest, SecurityAllowed, ReasonAllowed, version); err != nil {
			return f.deny(ctx, span, principal, dest, ReasonAuditFailed, version)
		}
	}
	return Decision{Allow: true, Reason: ReasonAllowed, PolicyVersion: version}, nil
}

// deny materializa uma decisão de bloqueio: anota o span, SELA o evento de segurança
// no WORM e devolve Decision{Allow:false}. Se a selagem falhar, a decisão permanece
// deny (nunca abre) e o erro é surfaçado (o WORM não registou o bloqueio).
func (f *EgressFilter) deny(ctx context.Context, span Span, principal referencemonitor.Principal, dest Destination, reason, version string) (Decision, error) {
	span.SetAttribute(AttrEgressAllowed, false)
	span.SetAttribute(AttrEgressReason, reason)
	if version != "" {
		span.SetAttribute(AttrPolicyVersion, version)
	}
	// O sink é garantido não-nil por NewEgressFilter: um bloqueio é SEMPRE selado no
	// WORM (audit-before-effect). Se a selagem falhar, a decisão permanece deny e o
	// erro é surfaçado.
	sealErr := f.seal(ctx, principal, dest, SecurityBlocked, reason, version)
	return Decision{Allow: false, Reason: reason, PolicyVersion: version}, sealErr
}

// seal sela um evento de segurança no WORM com o timestamp observacional e a
// correlação (run_id/step_id) que o contexto transporte ([WithCorrelation]).
func (f *EgressFilter) seal(ctx context.Context, principal referencemonitor.Principal, dest Destination, decision SecurityDecision, reason, version string) error {
	corr := correlationFrom(ctx)
	return f.sink.Seal(ctx, SecurityEvent{
		Principal:     principal,
		Destination:   dest,
		Decision:      decision,
		Reason:        reason,
		PolicyVersion: version,
		RunID:         corr.runID,
		StepID:        corr.stepID,
		Timestamp:     f.now(),
	})
}
