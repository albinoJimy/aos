package revalidation

import (
	"context"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/signing"
	"github.com/aos-ref/platform/registry/toolset"
)

// DefaultPartition é a partição da hash-chain de audit onde as decisões de
// revalidação por chamada (despacho/bloqueio) se selam por omissão.
const DefaultPartition = "registry.revalidation"

// capRevalidate é a capability selada em cada decisão de revalidação (vocabulário
// estável, tamper-evident).
const capRevalidate = "registry.revalidation.check"

// opRevalidate é o nome do span OTel da revalidação por chamada.
const opRevalidate = "registry.revalidate_call"

// Atributos de span (públicos por natureza — id/version/digest/decisão NÃO são
// segredos; scopes, a assinatura e qualquer segredo de credencial NUNCA entram num
// span). Reutilizam a porta Tracer zero-dep do Agent Runtime (AOS-013) e as chaves
// estáveis do REG.
const (
	attrToolID   = agentruntime.AttrToolName
	attrRunID    = agentruntime.AttrRunID
	attrVersion  = "aos.registry.version"
	attrDigest   = "aos.registry.digest"
	attrDecision = "aos.registry.decision"
	attrStage    = "aos.registry.stage"
	attrReason   = "aos.registry.reason"
)

// Permit é a autorização de despacho NÃO-FORJÁVEL emitida pela revalidação quando
// TODOS os passos passam — à imagem do Permit do RM (AOS-003). É selada por um campo
// não exportado (granted) que só [Revalidator.Revalidate] põe a true: um Permit{}
// construído directamente por outro pacote tem granted=false, pelo que [Permit.
// Granted] o distingue de um genuíno. Sem este selo, um allow seria forjável por
// construção. Liga-se à identidade pinada (id, version, digest) revalidada.
type Permit struct {
	toolID  string
	version string
	digest  string
	granted bool
}

// Granted indica se o permit é GENUÍNO — emitido por uma revalidação bem-sucedida.
// Um Permit forjado directamente devolve false. O consumidor de despacho DEVE exigir
// Granted() (não basta inspeccionar os campos), pois só o selo não exportado é
// inforjável.
func (p Permit) Granted() bool { return p.granted }

// ToolID, Version, Digest expõem a identidade pinada que o permit autoriza.
func (p Permit) ToolID() string  { return p.toolID }
func (p Permit) Version() string { return p.version }
func (p Permit) Digest() string  { return p.digest }

// Decision é o resultado de uma revalidação por chamada. Allowed=true significa que
// os seis passos passaram e há um [Permit] genuíno; Allowed=false é um BLOQUEIO
// fail-closed com o Stage e a Reason que o produziram (públicos, para
// observabilidade).
type Decision struct {
	// Allowed é o veredicto: true só se LOOKUP→digest→assinatura→scope/egress→audit
	// passaram todos.
	Allowed bool
	// Stage é o passo em que a decisão foi selada.
	Stage Stage
	// Reason é o código estável da decisão.
	Reason Reason
	// ToolID, Version, Digest são a identidade pinada avaliada (a CONGELADA, quando
	// conhecida; senão a da definição actual).
	ToolID  string
	Version string
	Digest  string
	// permit é o token não-forjável; presente (granted) só quando Allowed.
	permit Permit
}

// Permit devolve o [Permit] não-forjável e um booleano de presença. Só num despacho
// autorizado (Allowed) o booleano é true e o permit é genuíno.
func (d Decision) Permit() (Permit, bool) {
	return d.permit, d.Allowed
}

// Request é o pedido de revalidação de UMA tool call. Reúne a EXPECTATIVA (o
// conjunto congelado do run), a REALIDADE (a definição actual em backing store,
// possivelmente mutada por um servidor MCP a meio do run) e a POLÍTICA de scopes/
// egress do run.
type Request struct {
	// RunID e StepID correlacionam a decisão com a trajectória (selados no audit).
	RunID  string
	StepID string
	// ToolID é a tool a revalidar (a chave de LOOKUP no conjunto congelado).
	ToolID string
	// Current é a definição ACTUAL da tool em backing store — o que EXECUTARIA se
	// não fosse revalidada. É sobre ESTA que o digest é recalculado e a assinatura
	// revalidada. Um servidor MCP que mutou o schema entrega aqui a definição mutada.
	Current domain.Entry
	// Frozen é o conjunto congelado do run (AOS-050) — a EXPECTATIVA imutável contra
	// a qual Current é verificada. Nil é tratado como default-deny (sem expectativa,
	// nada executa).
	Frozen *toolset.FrozenToolSet
	// Policy é a fronteira de scopes/egress permitida no run (passo 4).
	Policy Policy
	// EgressHost é o host concreto que a chamada visaria (opcional). Se dado e a
	// classe de egress não for "none", é verificado contra a [EgressAllowlist].
	EgressHost string
}

// Revalidator é o REVALIDADOR POR CHAMADA (AOS-051): compõe digest (AOS-047),
// assinatura (AOS-048), conjunto congelado (AOS-050), quarentena (AOS-042), egress
// (EPIC-07) e audit WORM (AOS-011) na sequência fail-closed de tecnica/05 §5. É
// seguro para concorrência e reutilizável entre chamadas de vários runs. Construir
// com [New].
type Revalidator struct {
	digester   domain.Digester
	trust      TrustStore
	audit      audit.Store
	quarantine Quarantiner
	alerter    Alerter
	egress     EgressAllowlist
	tracer     agentruntime.Tracer
	partition  string
	now        func() time.Time
}

// Option configura o [Revalidator].
type Option func(*Revalidator)

// WithDigester injecta o Digester usado para RECALCULAR o digest da definição
// actual. Por omissão [digest.SHA256Digester] (AOS-047) — TEM de ser o mesmo
// algoritmo que produziu o digest congelado, senão toda a comparação diverge. Um
// valor nil é ignorado.
func WithDigester(d domain.Digester) Option {
	return func(r *Revalidator) {
		if d != nil {
			r.digester = d
		}
	}
}

// WithQuarantiner injecta a máquina de quarentena (AOS-042). Por omissão
// [NoopQuarantiner] (produção DEVE ligar a real). Um valor nil é ignorado.
func WithQuarantiner(q Quarantiner) Option {
	return func(r *Revalidator) {
		if q != nil {
			r.quarantine = q
		}
	}
}

// WithAlerter injecta o pipeline de alertas. Por omissão [NoopAlerter]. Um valor
// nil é ignorado.
func WithAlerter(a Alerter) Option {
	return func(r *Revalidator) {
		if a != nil {
			r.alerter = a
		}
	}
}

// WithEgressAllowlist injecta a allowlist de egress por host (EPIC-07). Por omissão
// nenhuma (só a classe é verificada). Um valor nil é ignorado.
func WithEgressAllowlist(a EgressAllowlist) Option {
	return func(r *Revalidator) {
		if a != nil {
			r.egress = a
		}
	}
}

// WithTracer injecta a porta de observabilidade (spans OTel). Por omissão
// NoopTracer. Os spans levam id/version/digest/decisão — nunca scopes nem segredos.
func WithTracer(t agentruntime.Tracer) Option {
	return func(r *Revalidator) {
		if t != nil {
			r.tracer = t
		}
	}
}

// WithPartition define a partição de audit das decisões. Por omissão
// [DefaultPartition].
func WithPartition(p string) Option {
	return func(r *Revalidator) {
		if p != "" {
			r.partition = p
		}
	}
}

// WithClock injecta o relógio (determinismo em testes; SÓ para os timestamps
// observacionais do audit — nunca numa decisão). Um valor nil é ignorado.
func WithClock(now func() time.Time) Option {
	return func(r *Revalidator) {
		if now != nil {
			r.now = now
		}
	}
}

// New constrói um revalidador sobre um trust store (AOS-048) e um audit store
// (AOS-011). Fail-closed: trust nil ou audit nil devolvem erro — sem chaves de
// confiança nenhuma assinatura é revalidável, e sem audit nenhuma decisão é selável.
// Por omissão: Digester SHA-256, quarentena/alerta no-op, sem allowlist de host,
// NoopTracer, partição [DefaultPartition], relógio time.Now.
func New(trust TrustStore, auditStore audit.Store, opts ...Option) (*Revalidator, error) {
	if trust == nil {
		return nil, ErrNoTrustStore
	}
	if auditStore == nil {
		return nil, ErrNoAuditStore
	}
	r := &Revalidator{
		digester:   digest.SHA256Digester{},
		trust:      trust,
		audit:      auditStore,
		quarantine: NoopQuarantiner{},
		alerter:    NoopAlerter{},
		tracer:     agentruntime.NoopTracer{},
		partition:  DefaultPartition,
		now:        time.Now,
	}
	for _, o := range opts {
		o(r)
	}
	return r, nil
}

// Revalidate executa a sequência FAIL-CLOSED de revalidação de UMA tool call
// (LOOKUP → digest → assinatura → scope/egress → EXEC → AUDIT) e devolve a
// [Decision]. É a SUPERFÍCIE ÚNICA da revalidação — o RM invoca-a antes de mintar o
// seu próprio permit e despachar; nenhuma execução directa (ADR-002).
//
// Garantias:
//   - qualquer passo que falhe → Allowed=false com Stage/Reason específicos; nos
//     passos de DIVERGÊNCIA (identidade/digest/assinatura/scope/egress) o artefacto
//     é colocado em QUARENTENA e é emitido um ALERTA;
//   - cada decisão (despacho OU bloqueio) é selada no audit WORM com id/version/
//     digest/resultado; uma AUTORIZAÇÃO não-auditável degrada para bloqueio;
//   - a DECISÃO é pura: o mesmo (expectativa, definição, política) produz sempre o
//     mesmo veredicto (o relógio só data o audit).
//
// O error devolvido é reservado a cancelamento de contexto; os bloqueios de política
// são comunicados via Decision.Allowed/Reason, nunca via error.
func (r *Revalidator) Revalidate(ctx context.Context, req Request) (Decision, error) {
	ctx, span := r.tracer.StartSpan(ctx, opRevalidate)
	defer span.End()
	span.SetAttribute(attrToolID, req.ToolID)
	span.SetAttribute(attrRunID, req.RunID)

	// Contexto já cancelado: fail-closed sem avaliar nem auditar (gravar exigiria o
	// mesmo contexto já cancelado). É o único caminho de bloqueio sem registo.
	if err := ctx.Err(); err != nil {
		d := Decision{Allowed: false, Stage: StageLookup, Reason: ReasonContextCanceled, ToolID: req.ToolID}
		r.setSpanDeny(span, d)
		return d, err
	}

	// (1) LOOKUP: a tool tem de estar no conjunto congelado do run.
	if req.Frozen == nil {
		return r.block(ctx, span, req, StageLookup, ReasonNotFrozen, req.ToolID, "", ""), nil
	}
	exp, ok := req.Frozen.Expectation(req.ToolID)
	if !ok {
		return r.block(ctx, span, req, StageLookup, ReasonNotFrozen, req.ToolID, "", ""), nil
	}
	expVer := exp.Version.String()
	span.SetAttribute(attrVersion, expVer)
	span.SetAttribute(attrDigest, exp.Digest)

	// Identidade pinada: a definição actual TEM de ser a mesma (id, version) que a
	// congelada. Um swap de versão a meio do run é drift (mesmo que o digest do
	// contrato coincidisse). É a primeira metade do passo (2).
	if req.Current.ID != exp.ID || !req.Current.Version.Equal(exp.Version) {
		return r.divergence(ctx, span, req, StageDigest, ReasonIdentityDrift, exp), nil
	}

	// (2) DIGEST: recalcula SEMPRE o digest SHA-256 da definição ACTUAL (sobre os
	// bytes reais em backing store) e compara com o congelado. O digest recalculado é
	// o ÚNICO discriminador que liga os bytes reais de Current à expectativa; nada o
	// pode substituir. Não há cache: um fingerprint não-criptográfico poderia sofrer
	// segunda-preimagem adversarial (o atacante controla Current) e mascarar o drift,
	// pelo que o SHA-256 colision-resistant é recalculado a cada chamada. O custo é de
	// poucos µs sobre um contrato de poucos KB — o Ed25519 (passo 3) domina o
	// orçamento de mediação (ADR-002, p95 < 15 ms).
	computed := r.digester.Digest(req.Current.Kind, req.Current.Contract)
	if err := digest.Compare(exp.Digest, computed); err != nil {
		return r.divergence(ctx, span, req, StageDigest, ReasonDigestMismatch, exp), nil
	}

	// (3) ASSINATURA: revalida a assinatura sobre (id, version, digest) CONGELADOS
	// com a chave pública do publicador confiável. Reutiliza signing.Verify (AOS-048).
	if req.Current.Signature == "" {
		return r.divergence(ctx, span, req, StageSignature, ReasonSignatureMissing, exp), nil
	}
	pub, ok := r.trust.Lookup(req.Current.Provenance.Publisher)
	if !ok {
		return r.divergence(ctx, span, req, StageSignature, ReasonUntrustedKey, exp), nil
	}
	if err := signing.Verify(pub, exp.ID, exp.Version, exp.Digest, req.Current.Signature); err != nil {
		return r.divergence(ctx, span, req, StageSignature, ReasonSignatureInvalid, exp), nil
	}

	// (4) SCOPE/EGRESS: scopes declarados ⊆ permitidos e classe de egress ≤ tecto
	// (ADR-006 + EPIC-07). Os scopes NUNCA entram em spans/audit — só o veredicto.
	if !scopesWithin(req.Current.Contract.CredentialScopes, req.Policy.AllowedScopes) {
		return r.divergence(ctx, span, req, StageScopeEgress, ReasonScopeDenied, exp), nil
	}
	if egressRank(req.Current.Contract.Egress) > egressRank(req.Policy.MaxEgress) {
		return r.divergence(ctx, span, req, StageScopeEgress, ReasonEgressDenied, exp), nil
	}
	if r.egress != nil && req.Current.Contract.Egress != domain.EgressNone {
		// Com allowlist activa e egress não-none, o host concreto é OBRIGATÓRIO:
		// um EgressHost ausente NÃO pode saltar a verificação por-host (seria um
		// fail-OPEN por omissão de argumento no caminho quente). Fail-closed — host
		// vazio ou fora da allowlist → bloqueio + quarentena + alerta.
		if req.EgressHost == "" || !r.egress.Allowed(req.EgressHost) {
			return r.divergence(ctx, span, req, StageScopeEgress, ReasonEgressHostDenied, exp), nil
		}
	}

	// (5)+(6) EXEC + AUDIT: tudo passou. Sela a decisão de DESPACHO no audit ANTES de
	// devolver o permit. Uma autorização não-auditável degrada para bloqueio.
	if err := r.record(ctx, req, exp, audit.DecisionAllow); err != nil {
		d := Decision{Allowed: false, Stage: StageExec, Reason: ReasonAuditFailed, ToolID: exp.ID, Version: expVer, Digest: exp.Digest}
		r.setSpanDeny(span, d)
		// Uma autorização que nem sequer se conseguiu auditar é um incidente:
		// alerta (best-effort), mas NÃO quarentena — o artefacto não é divergente,
		// falhou a infra de audit.
		r.alerter.Alert(ctx, Alert{ToolID: exp.ID, Version: expVer, Digest: exp.Digest, Stage: StageExec, Reason: ReasonAuditFailed})
		return d, nil
	}

	d := Decision{
		Allowed: true, Stage: StageExec, Reason: ReasonPermitted,
		ToolID: exp.ID, Version: expVer, Digest: exp.Digest,
		permit: Permit{toolID: exp.ID, version: expVer, digest: exp.Digest, granted: true},
	}
	span.SetAttribute(attrDecision, "permitted")
	span.SetAttribute(attrStage, string(StageExec))
	span.SetAttribute(attrReason, string(ReasonPermitted))
	return d, nil
}

// divergence é o caminho de BLOQUEIO POR DIVERGÊNCIA (identidade/digest/assinatura/
// scope/egress): coloca o artefacto em QUARENTENA (AOS-042), emite um ALERTA e sela
// a decisão de bloqueio no audit. Quarentena e alerta são BEST-EFFORT do ponto de
// vista do bloqueio — nunca o desfazem —, mas uma quarentena falhada agrava o alerta.
func (r *Revalidator) divergence(ctx context.Context, span agentruntime.Span, req Request, stage Stage, reason Reason, exp toolset.Expectation) Decision {
	ver := exp.Version.String()
	art := Artifact{ID: exp.ID, Version: ver, Digest: exp.Digest, Reason: reason}
	qErr := r.quarantine.Quarantine(ctx, art)
	r.alerter.Alert(ctx, Alert{ToolID: exp.ID, Version: ver, Digest: exp.Digest, Stage: stage, Reason: reason, QuarantineErr: qErr})
	return r.block(ctx, span, req, stage, reason, exp.ID, ver, exp.Digest)
}

// block sela uma decisão de BLOQUEIO no audit (best-effort — o bloqueio já é
// fail-closed) e devolve a Decision. Serve tanto o default-deny do LOOKUP (sem
// quarentena) como o epílogo de [divergence].
func (r *Revalidator) block(ctx context.Context, span agentruntime.Span, req Request, stage Stage, reason Reason, id, ver, dig string) Decision {
	d := Decision{Allowed: false, Stage: stage, Reason: reason, ToolID: id, Version: ver, Digest: dig}
	// Bloqueio auditado best-effort: a decisão já está fail-closed, pelo que uma
	// falha de audit não a altera (contrasta com o permit, mandatoriamente auditado).
	_ = r.recordRaw(ctx, req, id, ver, dig, audit.DecisionDeny)
	r.setSpanDeny(span, d)
	return d
}

// record sela uma decisão sobre a expectativa congelada. Ver [recordRaw].
func (r *Revalidator) record(ctx context.Context, req Request, exp toolset.Expectation, decision audit.Decision) error {
	return r.recordRaw(ctx, req, exp.ID, exp.Version.String(), exp.Digest, decision)
}

// recordRaw sela uma decisão de revalidação na hash-chain de audit com o tuplo
// exigido por AOS-051: id (ToolID), version (PolicyVersion), digest (Resource.Value)
// e resultado (Decision allow/deny). A Partition mantém-se como fronteira DEDICADA de
// encadeamento das decisões de revalidação; RunID/StepID são os do PEDIDO concreto
// (req.RunID/req.StepID) — selados no conteúdo para que a correlação da decisão com a
// trajectória seja ela própria tamper-evident (independente da Partition). NENHUM
// scope nem segredo entra no registo. Fail-closed no caminho de permit (o chamador
// trata o erro); best-effort no de deny.
func (r *Revalidator) recordRaw(ctx context.Context, req Request, id, ver, dig string, decision audit.Decision) error {
	rec := audit.AuditRecord{
		Partition:     r.partition,
		Timestamp:     r.now(),
		Decision:      decision,
		Capability:    capRevalidate,
		ToolID:        id,
		PolicyVersion: ver,
		Resource:      audit.Resource{Type: "artifact.digest", Value: dig},
		RunID:         req.RunID,
		StepID:        req.StepID,
	}
	_, err := r.audit.Append(ctx, rec)
	return err
}

// setSpanDeny anota o span de um bloqueio (decisão pública, sem segredos).
func (r *Revalidator) setSpanDeny(span agentruntime.Span, d Decision) {
	span.SetAttribute(attrDecision, "denied")
	span.SetAttribute(attrStage, string(d.Stage))
	span.SetAttribute(attrReason, string(d.Reason))
}

// scopesWithin reporta se TODOS os scopes declarados estão dentro do conjunto
// permitido. Sem scopes declarados → sempre dentro (uma tool que não pede
// credenciais não viola a fronteira). Um scope declarado fora do permitido → false
// (fail-closed): um conjunto permitido vazio bloqueia qualquer tool que peça scopes.
func scopesWithin(declared, allowed []string) bool {
	if len(declared) == 0 {
		return true
	}
	allow := make(map[string]struct{}, len(allowed))
	for _, s := range allowed {
		allow[s] = struct{}{}
	}
	for _, s := range declared {
		if _, ok := allow[s]; !ok {
			return false
		}
	}
	return true
}
