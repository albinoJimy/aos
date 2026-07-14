package procedural

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/schema"
)

// Stage é o estado de promoção de um artefacto de skill na máquina fail-closed,
// coerente com o fluxo Mermaid da Dim. 7 da fonte: staging → eval-gate → canary →
// ratificação assinada → produção, mais o estado terminal de rollback.
type Stage string

const (
	// StageStaging — recém-submetida; NUNCA executável em produção.
	StageStaging Stage = "staging"
	// StageEvalGate — passou o eval-gate (golden-set + trace-diffing). Ainda não prod.
	StageEvalGate Stage = "eval_gate"
	// StageCanary — passou o canary (success-rate + unsafe-action rate). Ainda não prod.
	StageCanary Stage = "canary"
	// StageRatified — recebeu ratificação humana assinada válida. Elegível para prod.
	StageRatified Stage = "ratified"
	// StageProduction — activa em produção (pipeline completo). Executável em prod.
	StageProduction Stage = "production"
	// StageRolledBack — revertida por regressão/rollback; NÃO executável em prod.
	StageRolledBack Stage = "rolled_back"
)

// attrEvalResult é o atributo OTel semconv do veredicto de avaliação, ligado ao
// trace (DoD AOS-040). Emitido nos spans de eval-gate e canary.
const attrEvalResult = "gen_ai.evaluation.result"

// span operations do pipeline procedural.
const (
	opSubmit    = "procedural.submit"
	opEvalGate  = "procedural.eval_gate"
	opCanary    = "procedural.canary"
	opRatify    = "procedural.ratify"
	opActivate  = "procedural.activate"
	opRollback  = "procedural.rollback"
	opExecCheck = "procedural.exec_check"
)

// Manifest é o MANIFESTO do artefacto comportamental (AOS-040): identidade SemVer
// mais proveniência de autoria (agente-autor, run_id de origem) e o pin de
// integridade (hash de conteúdo). É o que distingue uma skill aprendida de um
// blob anónimo: cada skill é atribuível a quem a produziu e a que execução.
type Manifest struct {
	// SkillName identifica a skill aprendida.
	SkillName string
	// Version é a versão SemVer do artefacto (reutiliza o SemVer de AOS-041).
	Version schema.Version
	// AuthorAgent é a NHI/agente que auto-escreveu a skill (accountability).
	AuthorAgent string
	// OriginRunID é o run de origem que produziu a skill (correlação forense).
	OriginRunID string
	// ContentHash é SHA-256 do conteúdo (pin de integridade). Tem de bater com o
	// conteúdo submetido (ErrContentHashMismatch).
	ContentHash []byte
}

// NewManifest constrói um manifesto completo com o ContentHash já calculado a
// partir do conteúdo. É a forma canónica de criar o manifesto (garante o pin).
func NewManifest(skillName string, version schema.Version, authorAgent, originRunID string, content []byte) Manifest {
	return Manifest{
		SkillName:   skillName,
		Version:     version,
		AuthorAgent: authorAgent,
		OriginRunID: originRunID,
		ContentHash: computeContentHash(content),
	}
}

// Validate impõe a completude do manifesto (fail-closed). A versão zero-value
// (0.0.0) é rejeitada: um artefacto comportamental tem de ter versão explícita.
func (m Manifest) Validate() error {
	if m.SkillName == "" || m.AuthorAgent == "" || m.OriginRunID == "" {
		return ErrInvalidManifest
	}
	if (m.Version == schema.Version{}) {
		return ErrInvalidManifest
	}
	if len(m.ContentHash) == 0 {
		return ErrInvalidManifest
	}
	return nil
}

// Skill é o artefacto comportamental versionado: manifesto + conteúdo opaco (a
// definição da skill; a sua semântica de execução está fora deste âmbito — aqui
// governa-se o CICLO DE VIDA, não a interpretação).
type Skill struct {
	Manifest Manifest
	Content  []byte
}

// ProceduralBody projecta a skill no corpo tipado da classe procedural de AOS-035
// (domain.ProceduralBody), a ponte para persistir a skill como registo de memória
// da classe ClassProcedural. O Stage é o estado corrente da máquina.
func (s Skill) ProceduralBody(stage Stage) domain.ProceduralBody {
	return domain.ProceduralBody{
		SkillName:      s.Manifest.SkillName,
		Version:        s.Manifest.Version.String(),
		DefinitionHash: base64.RawStdEncoding.EncodeToString(s.Manifest.ContentHash),
		Stage:          string(stage),
	}
}

// SignedTransition é uma transição de estado SELADA e ASSINADA: o registo de
// audit tamper-evident (hash-chain AOS-011) MAIS a assinatura ed25519 sobre o
// payload canónico da transição. Prova, verificável offline, de que a transição
// ocorreu, atribuível à chave do assinador.
type SignedTransition struct {
	// Record é o registo de audit selado na hash-chain (AuditSeq/PrevHash/EntryHash).
	Record audit.AuditRecord
	// From e To são os estados da transição.
	From Stage
	To   Stage
	// Payload é a mensagem canónica assinada (determinística).
	Payload []byte
	// Signature é a assinatura ed25519 sobre Payload.
	Signature []byte
	// SignerKID identifica a chave que assinou.
	SignerKID string
}

// skillState é o estado interno mutável de uma (skill,versão) na máquina.
type skillState struct {
	skill        Skill
	stage        Stage
	pin          PinRef
	evalPassed   bool
	canaryPassed bool
	ratified     bool
	evalResult   EvalResult
	canaryResult CanaryResult
	ratifierID   string
}

// SkillMemory é a memória procedural: governa o ciclo de vida das skills
// aprendidas através do pipeline de promoção estagiada fail-closed, mantendo a
// allowlist de execução em produção e o rollback atómico. É segura para
// concorrência.
type SkillMemory struct {
	mu         sync.RWMutex
	store      audit.Store
	signer     Signer
	registry   SkillRegistry
	evalGate   EvalGate
	canaryGate CanaryGate
	ratifiers  map[string]ed25519.PublicKey
	now        func() time.Time
	tracer     agentruntime.Tracer

	// states indexa (name@version) → estado da máquina.
	states map[string]*skillState
	// prodActive indexa name → versão actualmente activa em PRODUÇÃO. É A ALLOWLIST
	// de execução: uma (name,version) só é executável em prod se prodActive[name]
	// == version. Skills em staging/canary são estruturalmente excluídas.
	prodActive map[string]schema.Version
	// prodPrevious indexa name → versão prod ANTERIOR pinada, alvo do rollback
	// atómico (a versão para a qual se reverte sem downtime em regressão).
	prodPrevious map[string]schema.Version
}

// Option configura o [SkillMemory].
type Option func(*SkillMemory)

// WithClock injecta o relógio observacional do audit (default time.Now).
// Determinismo: injectar um relógio fixo torna as transições reproduzíveis.
func WithClock(now func() time.Time) Option {
	return func(m *SkillMemory) {
		if now != nil {
			m.now = now
		}
	}
}

// WithTracer injecta o Tracer dos spans OTel (default NoopTracer).
func WithTracer(tr agentruntime.Tracer) Option {
	return func(m *SkillMemory) {
		if tr != nil {
			m.tracer = tr
		}
	}
}

// WithRatifier adiciona uma chave pública de ratificador humano AUTORIZADO à
// allowlist de ratificadores. Só assinaturas de ratificadores registados são
// aceites na ratificação (fail-closed).
func WithRatifier(ratifierID string, pub ed25519.PublicKey) Option {
	return func(m *SkillMemory) {
		if ratifierID != "" && len(pub) == ed25519.PublicKeySize {
			m.ratifiers[ratifierID] = pub
		}
	}
}

// NewSkillMemory constrói a memória procedural. Fail-closed na construção: todas
// as portas obrigatórias (audit store, signer, registry, eval-gate, canary-gate)
// têm de estar presentes — sem qualquer uma não há pipeline governável.
func NewSkillMemory(store audit.Store, signer Signer, registry SkillRegistry, evalGate EvalGate, canaryGate CanaryGate, opts ...Option) (*SkillMemory, error) {
	if store == nil {
		return nil, ErrNilAuditStore
	}
	if signer == nil {
		return nil, ErrNilSigner
	}
	if registry == nil {
		return nil, ErrNilRegistry
	}
	if evalGate == nil {
		return nil, ErrNilEvalGate
	}
	if canaryGate == nil {
		return nil, ErrNilCanaryGate
	}
	m := &SkillMemory{
		store:        store,
		signer:       signer,
		registry:     registry,
		evalGate:     evalGate,
		canaryGate:   canaryGate,
		ratifiers:    make(map[string]ed25519.PublicKey),
		now:          time.Now,
		tracer:       agentruntime.NoopTracer{},
		states:       make(map[string]*skillState),
		prodActive:   make(map[string]schema.Version),
		prodPrevious: make(map[string]schema.Version),
	}
	for _, o := range opts {
		o(m)
	}
	return m, nil
}

func stateKey(name string, v schema.Version) string { return name + "@" + v.String() }

func auditPartition(name string) string { return "procedural:" + name }

// Submit regista uma skill APRENDIDA em STAGING. Passos (fail-closed):
//  1. valida o manifesto e o pin de integridade (hash do conteúdo);
//  2. rejeita versões duplicadas (imutabilidade SemVer);
//  3. pina o artefacto no Skill Registry (EPIC-05: pin+hash+assinatura);
//  4. sela a transição (∅→staging) no audit trail assinado.
//
// A skill entra em staging: NUNCA executável em produção até ao pipeline completo.
func (m *SkillMemory) Submit(ctx context.Context, manifest Manifest, content []byte) (SignedTransition, error) {
	ctx, span := m.tracer.StartSpan(ctx, opSubmit)
	defer span.End()
	span.SetAttribute(agentruntime.AttrOperationName, opSubmit)
	span.SetAttribute("aos.skill.name", manifest.SkillName)

	if err := manifest.Validate(); err != nil {
		span.SetAttribute("aos.result", "rejected")
		return SignedTransition{}, err
	}
	span.SetAttribute("aos.skill.version", manifest.Version.String())
	if !bytes.Equal(computeContentHash(content), manifest.ContentHash) {
		span.SetAttribute("aos.result", "rejected")
		return SignedTransition{}, ErrContentHashMismatch
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	k := stateKey(manifest.SkillName, manifest.Version)
	if _, ok := m.states[k]; ok {
		span.SetAttribute("aos.result", "rejected")
		return SignedTransition{}, ErrDuplicateVersion
	}

	// Assina o manifesto (autor/run_id/hash) e pina-o no Registry (EPIC-05).
	manifestMsg := canonicalManifest(manifest)
	manifestSig := m.signer.Sign(manifestMsg)
	pin, err := m.registry.Register(ctx, RegistrationRequest{
		SkillName:   manifest.SkillName,
		Version:     manifest.Version,
		ContentHash: manifest.ContentHash,
		AuthorAgent: manifest.AuthorAgent,
		OriginRunID: manifest.OriginRunID,
		Signature:   manifestSig,
		SignerKID:   m.signer.KID(),
	})
	if err != nil {
		span.SetAttribute("aos.result", "rejected")
		return SignedTransition{}, err
	}

	st := &skillState{
		skill: Skill{Manifest: manifest, Content: cloneBytes(content)},
		stage: StageStaging,
		pin:   pin,
	}
	m.states[k] = st

	tr, err := m.appendTransitionLocked(ctx, manifest.SkillName, manifest.Version, "", StageStaging, audit.DecisionAllow, manifest.AuthorAgent, "memory:procedural:submit", map[string]string{
		"author_agent":  manifest.AuthorAgent,
		"origin_run_id": manifest.OriginRunID,
		"content_hash":  base64.RawStdEncoding.EncodeToString(manifest.ContentHash),
		"registry_pin":  pin.Ref,
	}, nil)
	if err != nil {
		// Rollback do estado in-memory se a selagem falhar (nada meio-selado).
		delete(m.states, k)
		span.SetAttribute("aos.result", "error")
		return SignedTransition{}, err
	}
	span.SetAttribute("aos.result", "staging")
	return tr, nil
}

// RunEvalGate submete a (skill,versão) em staging ao eval-gate (golden-set +
// trace-diffing vs a baseline = versão prod actual). Fail-closed: se o eval-gate
// NÃO ficar verde, marca deny no audit e devolve ErrEvalGateNotPassed — o canary
// e a activação ficam bloqueados. Só de staging se corre o eval-gate.
func (m *SkillMemory) RunEvalGate(ctx context.Context, name string, version schema.Version) (EvalResult, SignedTransition, error) {
	ctx, span := m.tracer.StartSpan(ctx, opEvalGate)
	defer span.End()
	span.SetAttribute(agentruntime.AttrOperationName, opEvalGate)
	span.SetAttribute("aos.skill.name", name)
	span.SetAttribute("aos.skill.version", version.String())

	m.mu.Lock()
	defer m.mu.Unlock()

	st, ok := m.states[stateKey(name, version)]
	if !ok {
		return EvalResult{}, SignedTransition{}, ErrSkillNotFound
	}
	if st.stage != StageStaging {
		return EvalResult{}, SignedTransition{}, ErrInvalidStageTransition
	}

	baseline := m.prodActive[name] // zero-value se não houver prod
	res, err := m.evalGate.Evaluate(ctx, EvalRequest{SkillName: name, Version: version, BaselineVersion: baseline})
	if err != nil {
		return EvalResult{}, SignedTransition{}, err
	}
	st.evalResult = res
	extra := map[string]string{
		"golden_set_score":       ftoa(res.GoldenSetScore),
		"trace_diff_regressions": itoa(res.TraceDiffRegressions),
		"baseline_version":       baseline.String(),
	}
	if !res.Passed {
		span.SetAttribute(attrEvalResult, "fail")
		tr, aerr := m.appendTransitionLocked(ctx, name, version, StageStaging, StageStaging, audit.DecisionDeny, name, "memory:procedural:eval_gate", extra, nil)
		if aerr != nil {
			return res, SignedTransition{}, aerr
		}
		return res, tr, ErrEvalGateNotPassed
	}
	// AUDIT-BEFORE-EFFECT: sela a transição ANTES de avançar a máquina. Se o Append
	// falhar (audit indisponível) a (skill,versão) NÃO avança de staging — nenhum
	// estado durável fica à frente da cadeia de audit assinada (fail-closed).
	span.SetAttribute(attrEvalResult, "pass")
	tr, aerr := m.appendTransitionLocked(ctx, name, version, StageStaging, StageEvalGate, audit.DecisionAllow, name, "memory:procedural:eval_gate", extra, nil)
	if aerr != nil {
		return res, SignedTransition{}, aerr
	}
	st.evalPassed = true
	st.stage = StageEvalGate
	return res, tr, nil
}

// RunCanary submete a (skill,versão) ao canary (success-rate + unsafe-action
// rate). Fail-closed: requer eval-gate VERDE primeiro (ErrEvalGateNotPassed); se
// o canary não ficar verde, marca deny e devolve ErrCanaryNotPassed.
func (m *SkillMemory) RunCanary(ctx context.Context, name string, version schema.Version) (CanaryResult, SignedTransition, error) {
	ctx, span := m.tracer.StartSpan(ctx, opCanary)
	defer span.End()
	span.SetAttribute(agentruntime.AttrOperationName, opCanary)
	span.SetAttribute("aos.skill.name", name)
	span.SetAttribute("aos.skill.version", version.String())

	m.mu.Lock()
	defer m.mu.Unlock()

	st, ok := m.states[stateKey(name, version)]
	if !ok {
		return CanaryResult{}, SignedTransition{}, ErrSkillNotFound
	}
	if !st.evalPassed || st.stage != StageEvalGate {
		return CanaryResult{}, SignedTransition{}, ErrEvalGateNotPassed
	}

	res, err := m.canaryGate.Evaluate(ctx, CanaryRequest{SkillName: name, Version: version})
	if err != nil {
		return CanaryResult{}, SignedTransition{}, err
	}
	st.canaryResult = res
	extra := map[string]string{
		"success_rate":       ftoa(res.SuccessRate),
		"unsafe_action_rate": ftoa(res.UnsafeActionRate),
	}
	if !res.Passed {
		span.SetAttribute(attrEvalResult, "fail")
		tr, aerr := m.appendTransitionLocked(ctx, name, version, StageEvalGate, StageEvalGate, audit.DecisionDeny, name, "memory:procedural:canary", extra, nil)
		if aerr != nil {
			return res, SignedTransition{}, aerr
		}
		return res, tr, ErrCanaryNotPassed
	}
	// AUDIT-BEFORE-EFFECT: sela a transição ANTES de avançar a máquina. Se o Append
	// falhar a (skill,versão) NÃO avança de eval-gate — a cadeia assinada nunca fica
	// atrás do estado durável (fail-closed).
	span.SetAttribute(attrEvalResult, "pass")
	tr, aerr := m.appendTransitionLocked(ctx, name, version, StageEvalGate, StageCanary, audit.DecisionAllow, name, "memory:procedural:canary", extra, nil)
	if aerr != nil {
		return res, SignedTransition{}, aerr
	}
	st.canaryPassed = true
	st.stage = StageCanary
	return res, tr, nil
}

// Ratify aplica a ratificação HUMANA assinada (ed25519). Fail-closed:
//   - requer eval-gate + canary VERDES (senão ErrActivationRefused);
//   - o ratificador tem de estar na allowlist (ErrRatifierNotAuthorized);
//   - a assinatura tem de ser válida para (skill,versão) (ErrRatificationInvalid).
//
// A ratificação é ela própria um evento ASSINADO: a assinatura humana é selada
// no audit trail (base64) e a transição é adicionalmente assinada pelo sistema.
func (m *SkillMemory) Ratify(ctx context.Context, r Ratification) (SignedTransition, error) {
	ctx, span := m.tracer.StartSpan(ctx, opRatify)
	defer span.End()
	span.SetAttribute(agentruntime.AttrOperationName, opRatify)
	span.SetAttribute("aos.skill.name", r.SkillName)
	span.SetAttribute("aos.skill.version", r.Version.String())

	m.mu.Lock()
	defer m.mu.Unlock()

	st, ok := m.states[stateKey(r.SkillName, r.Version)]
	if !ok {
		return SignedTransition{}, ErrSkillNotFound
	}
	if !st.evalPassed || !st.canaryPassed || st.stage != StageCanary {
		span.SetAttribute("aos.result", "refused")
		return m.denyRatifyLocked(ctx, r, st.stage, "gates_incomplete", ErrActivationRefused)
	}
	pub, ok := m.ratifiers[r.RatifierID]
	if !ok {
		// Evento de SEGURANÇA: tentativa de ratificação por chave FORA da allowlist.
		// É selada como deny no audit assinado (não fica sem rasto forense).
		span.SetAttribute("aos.result", "refused")
		return m.denyRatifyLocked(ctx, r, st.stage, "ratifier_not_authorized", ErrRatifierNotAuthorized)
	}
	msg := CanonicalRatification(r.RatifierID, r.SkillName, r.Version, st.skill.Manifest.ContentHash)
	if len(r.Signature) != ed25519.SignatureSize || !ed25519.Verify(pub, msg, r.Signature) {
		// Evento de SEGURANÇA: assinatura humana ausente/inválida para o alvo. Deny selado.
		span.SetAttribute("aos.result", "refused")
		return m.denyRatifyLocked(ctx, r, st.stage, "ratification_signature_invalid", ErrRatificationInvalid)
	}
	// AUDIT-BEFORE-EFFECT: sela a ratificação (com a assinatura humana) ANTES de
	// marcar o estado ratificado. Se o Append falhar, a (skill,versão) NÃO fica
	// ratificada — a elegibilidade para produção nunca precede a cadeia assinada.
	span.SetAttribute("aos.result", "ratified")
	tr, err := m.appendTransitionLocked(ctx, r.SkillName, r.Version, StageCanary, StageRatified, audit.DecisionEscalate, r.RatifierID, "memory:procedural:ratify", map[string]string{
		"ratifier_id":      r.RatifierID,
		"ratification_sig": base64.RawStdEncoding.EncodeToString(r.Signature),
		"ratification_alg": "ed25519",
	}, r.Signature)
	if err != nil {
		return SignedTransition{}, err
	}
	st.ratified = true
	st.ratifierID = r.RatifierID
	st.stage = StageRatified
	return tr, nil
}

// denyRatifyLocked sela uma entrada de audit assinada DecisionDeny para uma
// tentativa de ratificação RECUSADA (ratificador não autorizado, assinatura
// inválida ou gates incompletos) e devolve o erro de domínio original. Uma
// tentativa de ratificação recusada é um evento de segurança e NÃO deve ficar
// sem rasto no hash-chain assinado. Se a própria selagem falhar, propaga o erro
// de audit (fail-closed sobre a porta de audit). DEVE ser chamada com m.mu retido.
func (m *SkillMemory) denyRatifyLocked(ctx context.Context, r Ratification, stage Stage, reason string, refusal error) (SignedTransition, error) {
	if _, aerr := m.appendTransitionLocked(ctx, r.SkillName, r.Version, stage, stage, audit.DecisionDeny, r.RatifierID, "memory:procedural:ratify", map[string]string{
		"ratifier_id":    r.RatifierID,
		"refused_reason": reason,
	}, nil); aerr != nil {
		return SignedTransition{}, aerr
	}
	return SignedTransition{}, refusal
}

// Activate ACTIVA a (skill,versão) em PRODUÇÃO. É a barreira central fail-closed:
// a activação é RECUSADA (ErrActivationRefused) a menos que TODOS os três gates
// estejam verdes — eval-gate + canary + ratificação assinada. Reverifica também
// o pin do Registry (integridade supply-chain). Em sucesso, faz o swap ATÓMICO da
// allowlist de produção: a versão prod anterior fica PINADA para rollback, a nova
// entra na allowlist. Não há caminho de auto-promoção — os gates são externos.
func (m *SkillMemory) Activate(ctx context.Context, name string, version schema.Version) (SignedTransition, error) {
	ctx, span := m.tracer.StartSpan(ctx, opActivate)
	defer span.End()
	span.SetAttribute(agentruntime.AttrOperationName, opActivate)
	span.SetAttribute("aos.skill.name", name)
	span.SetAttribute("aos.skill.version", version.String())

	m.mu.Lock()
	defer m.mu.Unlock()

	st, ok := m.states[stateKey(name, version)]
	if !ok {
		return SignedTransition{}, ErrSkillNotFound
	}
	// FAIL-CLOSED: os três gates. Falta de QUALQUER um → RECUSADA.
	if !st.evalPassed || !st.canaryPassed || !st.ratified || st.stage != StageRatified {
		// Recusa de activação por gates incompletos é um evento de segurança: selada
		// como deny no audit assinado (consistente com as falhas de eval/canary).
		span.SetAttribute("aos.result", "refused")
		return m.denyActivateLocked(ctx, name, version, st.stage, "gates_incomplete", ErrActivationRefused)
	}
	// Reverifica o pin de integridade no Registry (EPIC-05) antes de activar.
	pin, found, err := m.registry.Resolve(ctx, name, version)
	if err != nil {
		return SignedTransition{}, err
	}
	if !found || !bytes.Equal(pin.ContentHash, st.skill.Manifest.ContentHash) {
		// Falha de integridade supply-chain na activação: selada como deny.
		span.SetAttribute("aos.result", "refused")
		return m.denyActivateLocked(ctx, name, version, st.stage, "content_hash_mismatch", ErrContentHashMismatch)
	}

	// AUDIT-BEFORE-EFFECT: sela a transição para produção ANTES de mutar a allowlist
	// de execução. Se o Append falhar (audit indisponível) a skill NÃO se torna
	// executável em prod — a porta de audit é fail-closed: nenhuma activação sem
	// trilho forense assinado (espelha o MediationSink e o RM, ADR-002/010).
	prev, hasPrev := m.prodActive[name]
	extra := map[string]string{"registry_pin": pin.Ref}
	if hasPrev {
		extra["previous_version"] = prev.String()
	}
	tr, err := m.appendTransitionLocked(ctx, name, version, StageRatified, StageProduction, audit.DecisionAllow, name, "memory:procedural:activate", extra, nil)
	if err != nil {
		span.SetAttribute("aos.result", "error")
		return SignedTransition{}, err
	}

	// SWAP ATÓMICO da allowlist (SÓ após o audit selado): a versão prod anterior fica
	// pinada (alvo de rollback), a nova torna-se a activa. Sob Lock: um leitor sob
	// RLock vê sempre uma versão prod válida (a antiga OU a nova), nunca um vazio.
	if hasPrev {
		m.prodPrevious[name] = prev
		if prevSt, ok := m.states[stateKey(name, prev)]; ok {
			prevSt.stage = StageRatified // a anterior sai de prod mas permanece elegível
		}
	}
	m.prodActive[name] = version
	st.stage = StageProduction
	span.SetAttribute("aos.result", "production")
	return tr, nil
}

// denyActivateLocked sela uma entrada de audit assinada DecisionDeny para uma
// activação RECUSADA (gates incompletos ou falha de integridade do pin) e devolve
// o erro de domínio original. Uma activação recusada é um evento de segurança e
// NÃO deve ficar sem rasto no hash-chain assinado. Se a selagem falhar, propaga o
// erro de audit (fail-closed). DEVE ser chamada com m.mu retido.
func (m *SkillMemory) denyActivateLocked(ctx context.Context, name string, version schema.Version, stage Stage, reason string, refusal error) (SignedTransition, error) {
	if _, aerr := m.appendTransitionLocked(ctx, name, version, stage, stage, audit.DecisionDeny, name, "memory:procedural:activate", map[string]string{
		"refused_reason": reason,
	}, nil); aerr != nil {
		return SignedTransition{}, aerr
	}
	return SignedTransition{}, refusal
}

// RegressionSignal é o sinal de regressão observado sobre a versão em produção
// (ex.: queda de success-rate, subida de unsafe-action rate no pós-activação).
type RegressionSignal struct {
	// Regressed indica se foi detectada regressão (dispara o rollback atómico).
	Regressed bool
	// Reason é a justificação selada no audit (ex.: "success_rate<0.9").
	Reason string
}

// HandleRegression executa o ROLLBACK ATÓMICO AUTOMÁTICO em regressão detectada:
// se signal.Regressed, reverte a versão prod para a anterior pinada, sem
// downtime (swap único sob Lock). Se não houver versão anterior, DESACTIVA a
// skill de produção (fail-closed: nenhuma versão é melhor que uma regredida) e
// devolve ErrNoPreviousVersion. Sela a transição (production→rolled_back) no
// audit trail assinado.
func (m *SkillMemory) HandleRegression(ctx context.Context, name string, signal RegressionSignal) (SignedTransition, error) {
	ctx, span := m.tracer.StartSpan(ctx, opRollback)
	defer span.End()
	span.SetAttribute(agentruntime.AttrOperationName, opRollback)
	span.SetAttribute("aos.skill.name", name)

	if !signal.Regressed {
		// Sem regressão não há rollback (no-op explícito, sem tocar no audit).
		span.SetAttribute("aos.result", "no_regression")
		return SignedTransition{}, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	current, ok := m.prodActive[name]
	if !ok {
		span.SetAttribute("aos.result", "no_prod")
		return SignedTransition{}, ErrSkillNotFound
	}
	if curSt, ok := m.states[stateKey(name, current)]; ok {
		curSt.stage = StageRolledBack
	}

	prev, hasPrev := m.prodPrevious[name]
	reason := signal.Reason
	if reason == "" {
		reason = "regression_detected"
	}
	extra := map[string]string{
		"rolled_back_version": current.String(),
		"reason":              reason,
	}

	if !hasPrev {
		// Sem alvo de reversão: DESACTIVA (allowlist de prod deixa de ter esta skill).
		delete(m.prodActive, name)
		span.SetAttribute("aos.result", "deactivated")
		tr, aerr := m.appendTransitionLocked(ctx, name, current, StageProduction, StageRolledBack, audit.DecisionDeny, name, "memory:procedural:rollback", extra, nil)
		if aerr != nil {
			return SignedTransition{}, aerr
		}
		return tr, ErrNoPreviousVersion
	}

	// SWAP ATÓMICO de reversão: a versão anterior volta a ser a activa. Sob Lock,
	// prodActive[name] passa de current para prev numa única atribuição — o leitor
	// vê sempre uma versão prod válida.
	m.prodActive[name] = prev
	delete(m.prodPrevious, name)
	if prevSt, ok := m.states[stateKey(name, prev)]; ok {
		prevSt.stage = StageProduction // a anterior volta a produção
	}
	extra["restored_version"] = prev.String()
	span.SetAttribute("aos.result", "rolled_back")
	return m.appendTransitionLocked(ctx, name, current, StageProduction, StageRolledBack, audit.DecisionAllow, name, "memory:procedural:rollback", extra, nil)
}

// IsExecutableInProd é a ALLOWLIST fail-closed: devolve true SÓ se a (name,
// version) exacta é a versão actualmente activa em produção. Uma skill em
// staging/canary/ratified (ou já revertida) é estruturalmente excluída.
func (m *SkillMemory) IsExecutableInProd(name string, version schema.Version) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	active, ok := m.prodActive[name]
	return ok && active.Equal(version)
}

// ExecuteInProd é o GATE de execução em produção: devolve nil se a (name,version)
// está na allowlist de produção, senão ErrNotExecutableInProd (fail-closed). A
// execução concreta da skill está fora deste âmbito — esta é a barreira estrutural
// que garante que staging nunca corre em prod.
func (m *SkillMemory) ExecuteInProd(ctx context.Context, name string, version schema.Version) error {
	_, span := m.tracer.StartSpan(ctx, opExecCheck)
	defer span.End()
	span.SetAttribute(agentruntime.AttrOperationName, opExecCheck)
	span.SetAttribute("aos.skill.name", name)
	span.SetAttribute("aos.skill.version", version.String())
	if !m.IsExecutableInProd(name, version) {
		span.SetAttribute("aos.result", "denied")
		return ErrNotExecutableInProd
	}
	span.SetAttribute("aos.result", "allowed")
	return nil
}

// ProdVersion devolve a versão activa em produção de name e se existe.
func (m *SkillMemory) ProdVersion(name string) (schema.Version, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.prodActive[name]
	return v, ok
}

// StageOf devolve o estado corrente de (name,version) e se existe.
func (m *SkillMemory) StageOf(name string, version schema.Version) (Stage, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, ok := m.states[stateKey(name, version)]
	if !ok {
		return "", false
	}
	return st.stage, true
}

// appendTransitionLocked sela uma transição no audit trail e assina-a. DEVE ser
// chamada com m.mu retido. A assinatura é sobre o payload canónico da transição
// (determinístico); a base64 da assinatura entra nas obligations, pelo que a
// hash-chain sela também a própria assinatura (tamper-evident + assinado).
func (m *SkillMemory) appendTransitionLocked(ctx context.Context, name string, version schema.Version, from, to Stage, decision audit.Decision, actor, capability string, extra map[string]string, humanSig []byte) (SignedTransition, error) {
	// Deriva o OriginRunID do estado com verificação de presença: uma transição
	// selada para uma (name,version) sem estado NÃO deve entrar em panic no caminho
	// de audit (robustez/disponibilidade). Ausente ⇒ RunID vazio (fallback).
	var runID string
	if st, ok := m.states[stateKey(name, version)]; ok {
		runID = st.skill.Manifest.OriginRunID
	}

	ts := m.now().UTC()
	payload := canonicalTransition(name, version, from, to, actor, ts.UnixNano(), extra)
	sig := m.signer.Sign(payload)

	params := map[string]string{
		"from":       string(from),
		"to":         string(to),
		"actor":      actor,
		"signer_kid": m.signer.KID(),
		"signature":  base64.RawStdEncoding.EncodeToString(sig),
		"sig_alg":    "ed25519",
	}
	for k, v := range extra {
		params[k] = v
	}
	if humanSig != nil {
		params["human_signature"] = base64.RawStdEncoding.EncodeToString(humanSig)
	}

	rec := audit.AuditRecord{
		Partition:  auditPartition(name),
		Timestamp:  ts,
		Decision:   decision,
		Principal:  audit.Principal{NHIID: actor},
		Capability: capability,
		RunID:      runID,
		ToolID:     "memory.procedural." + string(to),
		Resource: audit.Resource{
			Type:  "skill",
			Value: name + "@" + version.String(),
		},
		Context: audit.CallContext{
			Taint:         "procedural",
			Reversibility: "reversible",
		},
		Obligations: []audit.Obligation{{
			Type:   "procedural_transition",
			Params: params,
		}},
	}
	sealed, err := m.store.Append(ctx, rec)
	if err != nil {
		return SignedTransition{}, err
	}
	return SignedTransition{
		Record:    sealed,
		From:      from,
		To:        to,
		Payload:   payload,
		Signature: sig,
		SignerKID: m.signer.KID(),
	}, nil
}

// VerifySignedTransition verifica a assinatura ed25519 de uma transição selada
// contra uma chave pública. Prova, offline, que a transição foi assinada por
// quem detém a chave correspondente.
func VerifySignedTransition(pub ed25519.PublicKey, tr SignedTransition) bool {
	if len(pub) != ed25519.PublicKeySize || len(tr.Signature) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, tr.Payload, tr.Signature)
}
