package promotion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/semver"
	"github.com/aos-ref/substrate/eventstore"
)

// DefaultPromotionPartition é a partição da hash-chain de audit onde as transições
// do ciclo de promoção se selam por omissão (WORM, AOS-011).
const DefaultPromotionPartition = "registry.promotion"

// capPromotionPrefix é o prefixo do vocabulário de capability selado em cada
// transição (estável, tamper-evident): "registry.promotion.<stage>".
const capPromotionPrefix = "registry.promotion."

// Estágios (transições) do ciclo — vocabulário estável selado no audit e usado no
// stepID. Cada um é uma aresta observável do fluxo de ADR-012.
const (
	stagePublished          = "published"
	stageIntegrityVerified  = "integrity_verified"
	stageIntegrityRejected  = "integrity_rejected"
	stageEvalPassed         = "eval_passed"
	stageEvalRejected       = "eval_rejected"
	stageRatified           = "ratified"
	stageRatificationRefuse = "ratification_refused"
	stagePromoteIntent      = "promote_intent"
	stagePromoted           = "promoted"
	stagePromoteFailed      = "promote_failed"
	stageDeprecateIntent    = "deprecate_intent"
	stageDeprecated         = "deprecated"
	stageRevokeIntent       = "revoke_intent"
	stageRevoked            = "revoked"
	stageRollbackIntent     = "rollback_intent"
	stageRolledBack         = "rolled_back"
)

// Estágios "_intent" (AOS-053 Q3): a INTENÇÃO de uma transição de estado é selada no
// WORM ANTES de o estado ser comprometido na fonte de verdade (o Registry). Assim, um
// audit indisponível falha ANTES da mutação (fail-closed) — nunca fica um artefacto
// no novo estado sem a transição na hash-chain tamper-evident. Se a selagem de
// CONFIRMAÇÃO posterior falhar, a intenção já sela a aresta na cadeia (a confirmação
// em falta sinaliza o não-fecho), em vez de a transição efectiva ficar totalmente
// ausente do WORM.

// Operações de span do ciclo (públicas: id/version/estado não são segredos).
const (
	opPublish   = "registry.promotion.publish"
	opPromote   = "registry.promotion.promote"
	opDeprecate = "registry.promotion.deprecate"
	opRevoke    = "registry.promotion.revoke"
	opRollback  = "registry.promotion.rollback"
)

// AttrEvalResult — gen_ai.evaluation.result: o veredicto do eval-gate emitido no
// span de promoção de uma skill auto-escrita ("passed"|"failed"). Segue a semconv
// GenAI de avaliação (o adaptador OTel real é EPIC-08).
const AttrEvalResult = "gen_ai.evaluation.result"

// AttrRejectReason — razão granular da rejeição de integridade (hash/contract/bump/
// signature), para diagnóstico. Nunca contém segredos.
const AttrRejectReason = "aos.registry.reject_reason"

// GovernedRegistry constrói um Registry cujo AdmissionVerifier é o [CompositeVerifier]
// (integridade + aprovação de governação). É o wiring CANÓNICO de AOS-053: garante
// que o gate estrutural do Registry e o Pipeline partilham o MESMO ledger e o MESMO
// verificador de integridade, pelo que nenhuma promoção a active escapa aos gates —
// nem sequer uma chamada directa a SetStatus. classifier nil usa o default.
func GovernedRegistry(store eventstore.EventStore, integrity registry.AdmissionVerifier, ledger *ApprovalLedger, classifier SelfAuthoredClassifier, regOpts ...registry.Option) (*registry.Registry, error) {
	if integrity == nil || ledger == nil {
		return nil, ErrNilIntegrity
	}
	cv := NewCompositeVerifier(integrity, ledger, classifier)
	opts := append([]registry.Option{registry.WithAdmissionVerifier(cv)}, regOpts...)
	return registry.New(store, opts...)
}

// Pipeline é o orquestrador do ciclo de publicação/promoção (AOS-053). Não detém
// estado autoritativo do catálogo (a fonte de verdade é o Registry sobre o Event
// Store); mantém apenas os Lifecycles por linha de versões (projecção operável para
// o rollback atómico de AOS-052) e o ApprovalLedger da governação. Seguro para
// concorrência. Construir com [NewPipeline].
type Pipeline struct {
	reg        *registry.Registry
	integrity  registry.AdmissionVerifier
	ledger     *ApprovalLedger
	classifier SelfAuthoredClassifier
	evalGate   EvalGate
	ratifiers  *RatifierStore
	digester   domain.Digester
	audit      audit.Store
	partition  string
	tracer     agentruntime.Tracer
	now        func() time.Time

	mu         sync.Mutex
	lifecycles map[string]*registry.Lifecycle
}

// PipelineOption configura o Pipeline.
type PipelineOption func(*Pipeline)

// WithEvalGate injecta o eval-gate (harness de EPIC-11) para skills auto-escritas.
// Por omissão nil: a promoção de uma skill auto-escrita sem eval-gate é RECUSADA
// (ErrNoEvalGate) — fail-closed, nunca admitida por omissão de wiring.
func WithEvalGate(g EvalGate) PipelineOption {
	return func(p *Pipeline) {
		if g != nil {
			p.evalGate = g
		}
	}
}

// WithRatifiers injecta a allowlist de ratificadores humanos. Por omissão nil: a
// promoção de uma skill auto-escrita sem allowlist é RECUSADA (ErrNoRatifiers).
func WithRatifiers(s *RatifierStore) PipelineOption {
	return func(p *Pipeline) {
		if s != nil {
			p.ratifiers = s
		}
	}
}

// WithClassifier injecta a classificação de skill auto-escrita. Por omissão
// [DefaultSelfAuthoredClassifier]. DEVE coincidir com o classifier do
// CompositeVerifier instalado no Registry (usar [GovernedRegistry] garante-o).
func WithClassifier(c SelfAuthoredClassifier) PipelineOption {
	return func(p *Pipeline) {
		if c != nil {
			p.classifier = c
		}
	}
}

// WithDigester injecta o Digester da verificação de hash. Por omissão o SHA-256
// canonicalizado de AOS-047 (deve coincidir com o do Registry).
func WithDigester(d domain.Digester) PipelineOption {
	return func(p *Pipeline) {
		if d != nil {
			p.digester = d
		}
	}
}

// WithPartition define a partição de audit das transições. Por omissão
// [DefaultPromotionPartition].
func WithPartition(part string) PipelineOption {
	return func(p *Pipeline) {
		if part != "" {
			p.partition = part
		}
	}
}

// WithTracer injecta a porta de observabilidade (spans OTel GenAI). Por omissão
// NoopTracer. Nenhum segredo entra num span.
func WithTracer(t agentruntime.Tracer) PipelineOption {
	return func(p *Pipeline) {
		if t != nil {
			p.tracer = t
		}
	}
}

// WithClock injecta o relógio (determinismo; só timestamps de audit — nunca uma
// decisão). Por omissão time.Now.
func WithClock(f func() time.Time) PipelineOption {
	return func(p *Pipeline) {
		if f != nil {
			p.now = f
		}
	}
}

// NewPipeline constrói o orquestrador. reg, integrity, ledger e auditStore são
// obrigatórios (fail-closed). integrity e ledger DEVEM ser os mesmos usados na
// construção do CompositeVerifier do reg (ver [GovernedRegistry]).
func NewPipeline(reg *registry.Registry, integrity registry.AdmissionVerifier, ledger *ApprovalLedger, auditStore audit.Store, opts ...PipelineOption) (*Pipeline, error) {
	if reg == nil {
		return nil, ErrNilRegistry
	}
	if integrity == nil || ledger == nil {
		return nil, ErrNilIntegrity
	}
	if auditStore == nil {
		return nil, ErrNoAudit
	}
	p := &Pipeline{
		reg:        reg,
		integrity:  integrity,
		ledger:     ledger,
		classifier: DefaultSelfAuthoredClassifier,
		digester:   digest.SHA256Digester{},
		audit:      auditStore,
		partition:  DefaultPromotionPartition,
		tracer:     agentruntime.NoopTracer{},
		now:        time.Now,
		lifecycles: make(map[string]*registry.Lifecycle),
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// PromoteRequest descreve o pedido de promoção de uma versão staging a active.
type PromoteRequest struct {
	// ID e Version identificam a versão staging a promover.
	ID      string
	Version domain.Version
	// Ratification é a ratificação humana assinada. OBRIGATÓRIA para skills
	// auto-escritas; ignorada para artefactos de terceiros.
	Ratification *Ratification
	// SemanticsBroken declara uma quebra de comportamento não derivável do contrato
	// estrutural (alimenta o ValidateBump de AOS-052).
	SemanticsBroken bool
}

// PromoteResult é o desfecho de uma promoção bem-sucedida.
type PromoteResult struct {
	// Version é a versão promovida a active — a SemVer ATRIBUÍDA na promoção.
	Version domain.Version
	// SelfAuthored indica se o artefacto foi tratado como skill auto-escrita
	// (eval-gate + ratificação) ou de terceiros (só verificação).
	SelfAuthored bool
	// Baseline é a versão active anterior contra a qual se validou o bump e se fez o
	// trace-diffing (IsZero se primeira promoção).
	Baseline domain.Version
	// Eval é o veredicto do eval-gate (zero-value se não-aplicável a terceiros).
	Eval EvalResult
}

// Publish admite um artefacto no catálogo em STAGING (delega em Registry.Publish) e
// sela a transição no audit WORM. Nunca coloca em active (o Registry impõe staging).
func (p *Pipeline) Publish(ctx context.Context, req registry.PublishRequest) (domain.Entry, error) {
	ctx, span := p.tracer.StartSpan(ctx, opPublish)
	defer span.End()
	span.SetAttribute(registry.AttrArtifactID, req.ID)
	span.SetAttribute(registry.AttrArtifactVersion, req.Version.String())

	e, err := p.reg.Publish(ctx, req)
	if err != nil {
		span.SetAttribute(registry.AttrDecision, "denied")
		return domain.Entry{}, err
	}
	if serr := p.seal(ctx, e.ID, e.Version, e.Digest, stagePublished, audit.DecisionAllow); serr != nil {
		return domain.Entry{}, serr
	}
	span.SetAttribute(registry.AttrArtifactKind, string(e.Kind))
	span.SetAttribute(registry.AttrArtifactDigest, e.Digest)
	span.SetAttribute(registry.AttrDecision, "staged")
	return e, nil
}

// Promote executa o ciclo fail-closed de promoção staging→active:
//
//  1. resolve a versão staging (a resolução re-verifica o digest — hash de AOS-047);
//  2. PRÉ-CONDIÇÃO de integridade: hash + contrato + ValidateBump (AOS-052) +
//     assinatura (AOS-048). Qualquer falha → REJEITADO (permanece em staging);
//  3. se skill AUTO-ESCRITA: eval-gate (golden-set + trace-diffing) — falha →
//     REJEITADA; e ratificação humana assinada — ausente/inválida → RECUSA;
//  4. active com a SemVer atribuída (Registry.SetStatus re-atravessa o gate
//     composto). Cada transição é selada no audit WORM.
//
// NENHUM salto para active: cada gate aplicável tem de passar. A distinção
// tools/skills é estrutural (classifier por kind+origem).
func (p *Pipeline) Promote(ctx context.Context, req PromoteRequest) (PromoteResult, error) {
	ctx, span := p.tracer.StartSpan(ctx, opPromote)
	defer span.End()
	span.SetAttribute(registry.AttrArtifactID, req.ID)
	span.SetAttribute(registry.AttrArtifactVersion, req.Version.String())

	if req.ID == "" || req.Version.IsZero() {
		span.SetAttribute(registry.AttrDecision, "denied")
		return PromoteResult{}, registry.ErrInvalidRequest
	}

	// (1) Resolução da versão staging. Resolve re-verifica o digest (fail-closed):
	// um digest que não coincida com o conteúdo é já uma falha de integridade.
	e, err := p.reg.Resolve(ctx, req.ID, req.Version)
	if err != nil {
		if errors.Is(err, registry.ErrDigestMismatch) {
			return p.rejectIntegrity(ctx, span, req.ID, req.Version, "", "hash", err)
		}
		span.SetAttribute(registry.AttrDecision, "denied")
		return PromoteResult{}, err
	}
	span.SetAttribute(registry.AttrArtifactKind, string(e.Kind))
	span.SetAttribute(registry.AttrArtifactDigest, e.Digest)
	if e.Status != domain.StatusStaging {
		span.SetAttribute(registry.AttrDecision, "denied")
		return PromoteResult{}, ErrNotStaging
	}

	// (2) PRÉ-CONDIÇÃO de integridade — ordem: hash, contrato, bump, assinatura.
	if cerr := digest.Compare(e.Digest, p.digester.Digest(e.Kind, e.Contract)); cerr != nil {
		return p.rejectIntegrity(ctx, span, e.ID, e.Version, e.Digest, "hash", cerr)
	}
	if verr := validateContract(e.Contract); verr != nil {
		return p.rejectIntegrity(ctx, span, e.ID, e.Version, e.Digest, "contract", verr)
	}
	baseline, hasBaseline, berr := p.currentActive(ctx, e.ID)
	if berr != nil {
		span.SetAttribute(registry.AttrDecision, "error")
		return PromoteResult{}, berr
	}
	if hasBaseline {
		if _, bverr := semver.ValidateBump(semver.ChangeRequest{
			Kind:            e.Kind,
			From:            baseline.Version,
			To:              e.Version,
			OldContract:     baseline.Contract,
			NewContract:     e.Contract,
			SemanticsBroken: req.SemanticsBroken,
		}); bverr != nil {
			return p.rejectIntegrity(ctx, span, e.ID, e.Version, e.Digest, "bump", bverr)
		}
	}
	if sverr := p.integrity.Verify(ctx, e); sverr != nil {
		return p.rejectIntegrity(ctx, span, e.ID, e.Version, e.Digest, "signature", sverr)
	}
	if serr := p.seal(ctx, e.ID, e.Version, e.Digest, stageIntegrityVerified, audit.DecisionAllow); serr != nil {
		return PromoteResult{}, serr
	}

	// (3) Governação das skills auto-escritas: eval-gate + ratificação assinada.
	selfAuthored := p.classifier(e)
	var evalRes EvalResult
	if selfAuthored {
		res, gerr := p.runEvalGate(ctx, span, e, baseline)
		if gerr != nil {
			return PromoteResult{}, gerr
		}
		evalRes = res
		if rerr := p.verifyRatification(ctx, e, req.Ratification); rerr != nil {
			return PromoteResult{}, rerr
		}
		// Aprovação de governação: eval-gate PASSOU + ratificação VÁLIDA. É o que o
		// CompositeVerifier exige para deixar a skill auto-escrita chegar a active.
		p.ledger.Approve(e.ID, e.Version, e.Digest)
	}

	// (4) Promoção a active. Selamos a INTENÇÃO da transição no WORM ANTES de
	// comprometer o estado active na fonte de verdade (AOS-053 Q3): se o audit
	// estiver indisponível, falhamos AQUI (fail-closed) e o estado NUNCA muda — nunca
	// fica um artefacto active sem a transição na hash-chain tamper-evident. O
	// Registry re-atravessa o gate composto (assinatura + aprovação) — defesa em
	// profundidade; a SemVer atribuída é a versão pinada.
	if serr := p.seal(ctx, e.ID, e.Version, e.Digest, stagePromoteIntent, audit.DecisionAllow); serr != nil {
		span.SetAttribute(registry.AttrDecision, "denied")
		return PromoteResult{}, serr
	}
	if _, perr := p.reg.SetStatus(ctx, e.ID, e.Version, domain.StatusActive); perr != nil {
		_ = p.seal(ctx, e.ID, e.Version, e.Digest, stagePromoteFailed, audit.DecisionDeny)
		span.SetAttribute(registry.AttrDecision, "denied")
		return PromoteResult{}, perr
	}
	p.track(e.ID, e.Version, domain.StatusActive)
	if serr := p.seal(ctx, e.ID, e.Version, e.Digest, stagePromoted, audit.DecisionAllow); serr != nil {
		return PromoteResult{}, serr
	}
	span.SetAttribute(registry.AttrDecision, "promoted")
	return PromoteResult{Version: e.Version, SelfAuthored: selfAuthored, Baseline: baselineVersion(baseline, hasBaseline), Eval: evalRes}, nil
}

// runEvalGate corre o eval-gate de uma skill auto-escrita e emite o veredicto no
// span (gen_ai.evaluation.result). Fail-closed: sem gate configurado, ou eval que
// falhe/erre, REJEITA (a skill não vai a produção).
func (p *Pipeline) runEvalGate(ctx context.Context, span agentruntime.Span, e domain.Entry, baseline domain.Entry) (EvalResult, error) {
	if p.evalGate == nil {
		span.SetAttribute(AttrEvalResult, "failed")
		_ = p.seal(ctx, e.ID, e.Version, e.Digest, stageEvalRejected, audit.DecisionDeny)
		span.SetAttribute(registry.AttrDecision, "denied")
		return EvalResult{}, ErrNoEvalGate
	}
	res, err := p.evalGate.Evaluate(ctx, EvalRequest{ID: e.ID, Version: e.Version, Baseline: baseline.Version, Digest: e.Digest})
	if err != nil || !res.Passed {
		span.SetAttribute(AttrEvalResult, "failed")
		_ = p.seal(ctx, e.ID, e.Version, e.Digest, stageEvalRejected, audit.DecisionDeny)
		span.SetAttribute(registry.AttrDecision, "denied")
		if err != nil {
			return EvalResult{}, fmt.Errorf("%w: %v", ErrEvalGateRejected, err)
		}
		return EvalResult{}, ErrEvalGateRejected
	}
	span.SetAttribute(AttrEvalResult, "passed")
	if serr := p.seal(ctx, e.ID, e.Version, e.Digest, stageEvalPassed, audit.DecisionAllow); serr != nil {
		return EvalResult{}, serr
	}
	return res, nil
}

// verifyRatification impõe a ratificação humana assinada de uma skill auto-escrita.
// Fail-closed: sem allowlist → ErrNoRatifiers; sem ratificação → ErrRatificationRequired;
// inválida/não-autorizada → ErrRatificationInvalid. Cada recusa é selada.
func (p *Pipeline) verifyRatification(ctx context.Context, e domain.Entry, rat *Ratification) error {
	if p.ratifiers == nil {
		_ = p.seal(ctx, e.ID, e.Version, e.Digest, stageRatificationRefuse, audit.DecisionDeny)
		return ErrNoRatifiers
	}
	if rat == nil {
		_ = p.seal(ctx, e.ID, e.Version, e.Digest, stageRatificationRefuse, audit.DecisionDeny)
		return ErrRatificationRequired
	}
	if err := p.ratifiers.Verify(*rat, e.ID, e.Version, e.Digest); err != nil {
		_ = p.seal(ctx, e.ID, e.Version, e.Digest, stageRatificationRefuse, audit.DecisionDeny)
		return err
	}
	return p.seal(ctx, e.ID, e.Version, e.Digest, stageRatified, audit.DecisionAllow)
}

// Deprecate marca uma versão active como deprecated (deprecação formal antes de
// qualquer retirada — AOS-052) e sela a transição. Espelha no Lifecycle.
func (p *Pipeline) Deprecate(ctx context.Context, id string, v domain.Version) (domain.Entry, error) {
	ctx, span := p.tracer.StartSpan(ctx, opDeprecate)
	defer span.End()
	span.SetAttribute(registry.AttrArtifactID, id)
	span.SetAttribute(registry.AttrArtifactVersion, v.String())

	// Intenção selada ANTES da mutação (AOS-053 Q3): audit indisponível falha aqui,
	// sem deprecar sem selo. O digest confirmado entra no selo de confirmação abaixo.
	if serr := p.seal(ctx, id, v, "", stageDeprecateIntent, audit.DecisionAllow); serr != nil {
		span.SetAttribute(registry.AttrDecision, "denied")
		return domain.Entry{}, serr
	}
	e, err := p.reg.SetStatus(ctx, id, v, domain.StatusDeprecated)
	if err != nil {
		span.SetAttribute(registry.AttrDecision, "denied")
		return domain.Entry{}, err
	}
	p.track(id, v, domain.StatusDeprecated)
	if serr := p.seal(ctx, id, v, e.Digest, stageDeprecated, audit.DecisionAllow); serr != nil {
		return domain.Entry{}, serr
	}
	span.SetAttribute(registry.AttrDecision, "deprecated")
	return e, nil
}

// Revoke é a REVOGAÇÃO DE EMERGÊNCIA: transita a versão para revoked (a partir de
// QUALQUER estado, transição terminal) e sela a transição. Bloqueia IMEDIATAMENTE o
// artefacto no RM — a partir daqui Registry.IsAdmissible devolve false (default-deny
// por estado) e a revalidação por chamada (AOS-051) recusa o despacho.
func (p *Pipeline) Revoke(ctx context.Context, id string, v domain.Version) (domain.Entry, error) {
	ctx, span := p.tracer.StartSpan(ctx, opRevoke)
	defer span.End()
	span.SetAttribute(registry.AttrArtifactID, id)
	span.SetAttribute(registry.AttrArtifactVersion, v.String())

	// Intenção selada ANTES da mutação (AOS-053 Q3): a revogação de emergência entra
	// no WORM antes de comprometer o estado revoked; audit indisponível falha aqui.
	if serr := p.seal(ctx, id, v, "", stageRevokeIntent, audit.DecisionDeny); serr != nil {
		span.SetAttribute(registry.AttrDecision, "denied")
		return domain.Entry{}, serr
	}
	e, err := p.reg.SetStatus(ctx, id, v, domain.StatusRevoked)
	if err != nil {
		span.SetAttribute(registry.AttrDecision, "denied")
		return domain.Entry{}, err
	}
	p.track(id, v, domain.StatusRevoked)
	if serr := p.seal(ctx, id, v, e.Digest, stageRevoked, audit.DecisionDeny); serr != nil {
		return domain.Entry{}, serr
	}
	span.SetAttribute(registry.AttrDecision, "revoked")
	return e, nil
}

// Rollback repõe atomicamente uma versão anterior (deprecated e verificada) como
// active, delegando o SWAP ATÓMICO no Lifecycle de AOS-052 (a active corrente passa
// a deprecated e o alvo a active sob um único lock — sem estado híbrido observável).
// A operação é REFLECTIDA no Registry (a fonte de verdade), com a reactivação a
// re-atravessar o gate composto (assinatura + aprovação persistida): a confiança da
// primeira promoção NÃO é herdada (AOS-048 Q1). O rollback re-promove sempre uma
// versão FORMALMENTE deprecated, nunca uma staging (fecha o bypass staging→active).
func (p *Pipeline) Rollback(ctx context.Context, id string, target domain.Version) (domain.Entry, error) {
	ctx, span := p.tracer.StartSpan(ctx, opRollback)
	defer span.End()
	span.SetAttribute(registry.AttrArtifactID, id)
	span.SetAttribute(registry.AttrArtifactVersion, target.String())

	lc := p.lifecycle(id)
	prevActive, hadActive := lc.Active()

	// Pré-verificação de integridade do alvo (a reactivação não herda confiança).
	tEntry, err := p.reg.Resolve(ctx, id, target)
	if err != nil {
		span.SetAttribute(registry.AttrDecision, "denied")
		return domain.Entry{}, err
	}
	if verr := p.integrity.Verify(ctx, tEntry); verr != nil {
		span.SetAttribute(registry.AttrDecision, "denied")
		return domain.Entry{}, fmt.Errorf("%w: %v", registry.ErrAdmissionDenied, verr)
	}

	// Intenção selada ANTES de qualquer mutação de estado — Lifecycle E Registry
	// (AOS-053 Q3): audit indisponível falha aqui, sem swap parcial nem re-activação
	// do alvo sem selo na hash-chain.
	if serr := p.seal(ctx, id, target, tEntry.Digest, stageRollbackIntent, audit.DecisionAllow); serr != nil {
		span.SetAttribute(registry.AttrDecision, "denied")
		return domain.Entry{}, serr
	}

	// SWAP ATÓMICO na projecção operável (AOS-052).
	if serr := lc.Rollback(ctx, target); serr != nil {
		span.SetAttribute(registry.AttrDecision, "denied")
		return domain.Entry{}, fmt.Errorf("%w: %v", ErrRollbackTarget, serr)
	}

	// Reflexão no Registry (fonte de verdade). Deprecate-first evita duas versões
	// active simultâneas; a pré-verificação garante que a reactivação do alvo passa.
	if hadActive && !prevActive.Equal(target) {
		if _, derr := p.reg.SetStatus(ctx, id, prevActive, domain.StatusDeprecated); derr != nil {
			span.SetAttribute(registry.AttrDecision, "error")
			return domain.Entry{}, derr
		}
		p.track(id, prevActive, domain.StatusDeprecated)
	}
	e, aerr := p.reg.SetStatus(ctx, id, target, domain.StatusActive)
	if aerr != nil {
		span.SetAttribute(registry.AttrDecision, "denied")
		return domain.Entry{}, aerr
	}
	p.track(id, target, domain.StatusActive)
	if serr := p.seal(ctx, id, target, e.Digest, stageRolledBack, audit.DecisionAllow); serr != nil {
		return domain.Entry{}, serr
	}
	span.SetAttribute(registry.AttrDecision, "rolled_back")
	return e, nil
}

// rejectIntegrity sela a rejeição de integridade e devolve ErrIntegrityRejected
// embrulhando a causa. O artefacto NÃO é promovido (permanece em staging).
func (p *Pipeline) rejectIntegrity(ctx context.Context, span agentruntime.Span, id string, v domain.Version, digestVal, reason string, cause error) (PromoteResult, error) {
	span.SetAttribute(AttrRejectReason, reason)
	span.SetAttribute(registry.AttrDecision, "rejected")
	if serr := p.seal(ctx, id, v, digestVal, stageIntegrityRejected, audit.DecisionDeny); serr != nil {
		return PromoteResult{}, serr
	}
	return PromoteResult{}, fmt.Errorf("%w (%s): %v", ErrIntegrityRejected, reason, cause)
}

// currentActive devolve a versão active corrente de MAIOR SemVer da linha de
// versões id (a baseline do ValidateBump e do trace-diffing). Um catálogo sem
// versão active da linha devolve hasBaseline=false (primeira promoção).
func (p *Pipeline) currentActive(ctx context.Context, id string) (domain.Entry, bool, error) {
	actives, err := p.reg.ActiveEntries(ctx)
	if err != nil {
		return domain.Entry{}, false, err
	}
	var best domain.Entry
	found := false
	for _, a := range actives {
		if a.ID != id {
			continue
		}
		if !found || best.Version.Less(a.Version) {
			best = a
			found = true
		}
	}
	return best, found, nil
}

// baselineVersion devolve a versão da baseline ou o zero-value se não houver.
func baselineVersion(baseline domain.Entry, has bool) domain.Version {
	if !has {
		return domain.Version{}
	}
	return baseline.Version
}

// lifecycle devolve (ou cria) o Lifecycle da linha de versões id.
func (p *Pipeline) lifecycle(id string) *registry.Lifecycle {
	p.mu.Lock()
	defer p.mu.Unlock()
	lc, ok := p.lifecycles[id]
	if !ok {
		lc = registry.NewLifecycle(id, registry.WithLifecycleTracer(p.tracer))
		p.lifecycles[id] = lc
	}
	return lc
}

// track espelha o estado de uma versão no Lifecycle da sua linha (projecção
// operável para o rollback). Erros de tracking são não-fatais para a transição já
// selada na fonte de verdade (o Lifecycle é uma projecção, não a autoridade).
func (p *Pipeline) track(id string, v domain.Version, status domain.Status) {
	_ = p.lifecycle(id).Track(v, status)
}

// seal sela uma transição do ciclo na hash-chain de audit (WORM, AOS-011) com o
// tuplo (id, version, digest) e o veredicto. Fail-closed: um erro do store →
// ErrAuditFailed (uma transição não-auditável não é admissível).
func (p *Pipeline) seal(ctx context.Context, id string, v domain.Version, digestVal, stage string, decision audit.Decision) error {
	rec := audit.AuditRecord{
		Partition:     p.partition,
		Timestamp:     p.now(),
		Decision:      decision,
		Capability:    capPromotionPrefix + stage,
		ToolID:        id,
		PolicyVersion: v.String(),
		Resource:      audit.Resource{Type: "artifact.digest", Value: digestVal},
		RunID:         p.partition,
		StepID:        id + "@" + v.String() + ":" + stage,
	}
	if _, err := p.audit.Append(ctx, rec); err != nil {
		return ErrAuditFailed
	}
	return nil
}

// validateContract impõe (fail-closed) que os schemas de I/O do contrato são JSON
// canonicalizável e que a classe de egress é válida — a dimensão "contrato" da
// verificação de integridade. Reutiliza a canonicalização de AOS-047.
func validateContract(c domain.Contract) error {
	if _, err := digest.CanonicalJSON(c.InputSchema); err != nil {
		return fmt.Errorf("input_schema: %w", err)
	}
	if _, err := digest.CanonicalJSON(c.OutputSchema); err != nil {
		return fmt.Errorf("output_schema: %w", err)
	}
	if !c.Egress.Valid() {
		return domain.ErrInvalidEgress
	}
	return nil
}
