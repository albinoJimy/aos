package tofu

import (
	"context"
	"errors"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
)

// DefaultPartition é a partição da hash-chain de audit onde as transições de
// confiança TOFU (first_seen/pinned/changed/re-aprovação) se selam por omissão. Uma
// cadeia dedicada mantém o rasto de confiança verificável de forma independente das
// decisões de admissão (registry.admission) e das mudanças de trust store.
const DefaultPartition = "registry.tofu"

// Atributos de span das transições TOFU (públicos por natureza — identidade,
// versão, digest e estado NÃO são segredos; nenhum valor de credencial entra num
// span). Reutilizam a porta Tracer zero-dep do Agent Runtime (AOS-013).
const (
	attrIdentity = agentruntime.AttrToolName
	attrVersion  = "aos.registry.version"
	attrDigest   = "aos.registry.digest"
	attrState    = "aos.registry.trust_state"
	attrDecision = "aos.registry.decision"
	attrReason   = "aos.registry.reason"
)

const (
	opObserve   = "registry.tofu.observe"
	opRatify    = "registry.tofu.ratify"
	opReapprove = "registry.tofu.reapprove"
)

// Observation é uma (re-)descoberta do manifesto de capabilities de um servidor
// MCP: a sua IDENTIDADE estável, a VERSÃO SemVer do artefacto e o DIGEST do
// manifesto (calculado por AOS-047 — [DigestManifest] é o atalho conveniente). O
// TOFU trata o digest como um OPACO: nunca interpreta o conteúdo do manifesto (que
// permanece untrusted, ADR-005) — dá confiança à ESTABILIDADE do schema, não ao seu
// conteúdo.
type Observation struct {
	// Identity é a identidade estável do servidor (ex.: "mcp://host", serverID). É a
	// chave TOFU: o que se regista na primeira ligação e por onde se detecta o drift.
	Identity string
	// Version é a versão SemVer pinada do manifesto/servidor.
	Version domain.Version
	// Digest é o digest do manifesto de capabilities (AOS-047).
	Digest string
}

func (o Observation) validate() error {
	if o.Identity == "" {
		return ErrEmptyIdentity
	}
	if o.Version.IsZero() {
		return ErrUnpinnedVersion
	}
	if o.Digest == "" {
		return ErrEmptyDigest
	}
	return nil
}

func (o Observation) ref() reference { return reference{Version: o.Version, Digest: o.Digest} }

// Outcome é o resultado observável de uma observação/re-aprovação: o estado de
// confiança resultante da identidade, a referência ancorada (versão + digest), se a
// utilização é ADMITIDA (só em pinned) e se ESTA observação detectou um drift. É
// composto apenas por metadados públicos.
type Outcome struct {
	Identity string
	State    TrustState
	Version  domain.Version
	Digest   string
	// Admitted é true SE E SÓ SE o estado é pinned (a utilização do artefacto é
	// admissível). first_seen e changed NÃO admitem (default-deny, ADR-002).
	Admitted bool
	// Drift é true quando ESTA observação classificou a identidade como changed
	// (divergência do digest pinado). Acompanha sempre ErrSchemaDrift em Observe.
	Drift bool
	// Reason é a justificação legível de uma não-admissão (vazia quando Admitted).
	Reason string
}

// DigestManifest é o atalho para calcular o digest de um manifesto de capabilities
// a partir da sua forma JSON — delega em [digest.DigestJSON] de AOS-047 (SHA-256
// sobre JSON canónico: ordem de chaves e whitespace irrelevantes). NÃO reimplementa
// o hashing; existe para que o chamador produza o mesmo digest que o TOFU compara.
// Fail-closed: JSON inválido devolve o erro de AOS-047 (não se pina conteúdo malformado).
func DigestManifest(manifestJSON []byte) (string, error) {
	return digest.DigestJSON(manifestJSON)
}

// Monitor é a máquina TOFU (AOS-049): mantém, por identidade de servidor MCP, o
// estado de confiança (first_seen → pinned → changed), DETECTA a divergência do
// digest do manifesto (schema drift) contra a referência pinada, BLOQUEIA a
// utilização em changed até re-aprovação explícita com uma NOVA versão SemVer, e
// SELA cada transição no audit hash-chain WORM (AOS-011). Os schemas/descrições
// permanecem UNTRUSTED durante todo o processo (ADR-005): o Monitor só manipula
// identidade + digest, nunca o conteúdo. Seguro para concorrência. Construir com
// [NewMonitor].
type Monitor struct {
	mu        sync.RWMutex
	records   map[string]record
	audit     audit.Store
	partition string
	tracer    agentruntime.Tracer
	now       func() time.Time
}

// Option configura o Monitor.
type Option func(*Monitor)

// WithPartition define a partição de audit das transições TOFU. Por omissão
// [DefaultPartition]. Um valor vazio é ignorado (mantém o default).
func WithPartition(p string) Option {
	return func(m *Monitor) {
		if p != "" {
			m.partition = p
		}
	}
}

// WithTracer injecta a porta de observabilidade (spans OTel GenAI). Por omissão
// NoopTracer. Os spans levam identidade/versão/digest/estado — nunca um segredo.
func WithTracer(t agentruntime.Tracer) Option {
	return func(m *Monitor) {
		if t != nil {
			m.tracer = t
		}
	}
}

// WithClock injecta o relógio (determinismo em testes; só para timestamps
// observacionais de audit — NUNCA numa decisão de confiança, que é pura).
func WithClock(f func() time.Time) Option {
	return func(m *Monitor) {
		if f != nil {
			m.now = f
		}
	}
}

// NewMonitor constrói a máquina TOFU sobre um audit store. Fail-closed: um audit
// store nil devolve [ErrNoAuditStore] — sem cadeia WORM nenhuma transição de
// confiança é selável, e uma máquina TOFU não-auditável não é admissível (ADR-010).
func NewMonitor(auditStore audit.Store, opts ...Option) (*Monitor, error) {
	if auditStore == nil {
		return nil, ErrNoAuditStore
	}
	m := &Monitor{
		records:   make(map[string]record),
		audit:     auditStore,
		partition: DefaultPartition,
		tracer:    agentruntime.NoopTracer{},
		now:       time.Now,
	}
	for _, o := range opts {
		o(m)
	}
	return m, nil
}

// Observe processa uma (re-)descoberta do manifesto de um servidor: regista
// first_seen na primeira ligação e, nas subsequentes, DETECTA o drift do digest
// contra a referência pinada. Devolve o [Outcome] e, quando a identidade está (ou
// passa a estar) em changed, [ErrSchemaDrift] — fail-closed: um chamador que trate o
// erro recusa a utilização, e um chamador que inspeccione o Outcome vê Admitted=false.
//
// Uma transição real (first_seen inicial, drift para changed) é SELADA no audit WORM
// ANTES de tomar efeito; uma falha de audit devolve [ErrAuditFailed] e a transição
// NÃO se aplica (fail-closed). Uma re-observação idêntica (pinned que se mantém) não
// é transição e não gera registo.
func (m *Monitor) Observe(ctx context.Context, obs Observation) (Outcome, error) {
	ctx, span := m.tracer.StartSpan(ctx, opObserve)
	defer span.End()
	span.SetAttribute(attrIdentity, obs.Identity)
	span.SetAttribute(attrVersion, obs.Version.String())
	span.SetAttribute(attrDigest, obs.Digest)

	if err := obs.validate(); err != nil {
		span.SetAttribute(attrDecision, "invalid")
		return Outcome{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	prev, ok := m.records[obs.Identity]
	var prevPtr *record
	if ok {
		prevPtr = &prev
	}
	tr := onObserve(prevPtr, obs.ref())
	return m.applyLocked(ctx, span, obs.Identity, tr)
}

// Ratify promove uma identidade de first_seen para pinned — a ratificação EXPLÍCITA
// do operador (só depois disto o artefacto é de confiança TOFU). Exige que a
// (versão, digest) ratificada coincida EXACTAMENTE com a observada em first_seen. A
// transição é selada no audit WORM antes de tomar efeito. Fail-closed: estado errado
// → [ErrNotFirstSeen]; par divergente → [ErrRatifyMismatch]; audit falha →
// [ErrAuditFailed] (sem efeito).
func (m *Monitor) Ratify(ctx context.Context, identity string, version domain.Version, dgst string) error {
	ctx, span := m.tracer.StartSpan(ctx, opRatify)
	defer span.End()
	span.SetAttribute(attrIdentity, identity)
	span.SetAttribute(attrVersion, version.String())
	span.SetAttribute(attrDigest, dgst)

	if identity == "" {
		span.SetAttribute(attrDecision, "invalid")
		return ErrEmptyIdentity
	}
	if version.IsZero() {
		span.SetAttribute(attrDecision, "invalid")
		return ErrUnpinnedVersion
	}
	if dgst == "" {
		span.SetAttribute(attrDecision, "invalid")
		return ErrEmptyDigest
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	prev, ok := m.records[identity]
	var prevPtr *record
	if ok {
		prevPtr = &prev
	}
	tr := onRatify(prevPtr, reference{Version: version, Digest: dgst})
	_, err := m.applyLocked(ctx, span, identity, tr)
	return err
}

// Reapprove recupera uma identidade de changed para pinned APÓS um incidente de
// drift — a re-aprovação explícita que EXIGE uma nova versão SemVer (ADR-012). A
// mesma versão (ainda que com digest diferente) é RECUSADA ([ErrInBandReapproval]);
// uma versão inferior é recusada ([ErrVersionRegression]). Em sucesso, a nova
// (versão, digest) passa a referência de confiança. A transição é selada no audit
// WORM antes de tomar efeito. Fail-closed: estado errado → [ErrNotChanged].
func (m *Monitor) Reapprove(ctx context.Context, obs Observation) (Outcome, error) {
	ctx, span := m.tracer.StartSpan(ctx, opReapprove)
	defer span.End()
	span.SetAttribute(attrIdentity, obs.Identity)
	span.SetAttribute(attrVersion, obs.Version.String())
	span.SetAttribute(attrDigest, obs.Digest)

	if err := obs.validate(); err != nil {
		span.SetAttribute(attrDecision, "invalid")
		return Outcome{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	prev, ok := m.records[obs.Identity]
	var prevPtr *record
	if ok {
		prevPtr = &prev
	}
	tr := onReapprove(prevPtr, obs.ref())
	return m.applyLocked(ctx, span, obs.Identity, tr)
}

// applyLocked traduz uma decisão pura ([transition]) em efeitos: sela a transição no
// audit (quando há mudança de estado) ANTES de mutar o mapa, actualiza o estado e
// produz o [Outcome]. Deve ser chamada com m.mu retido. Fail-closed em todas as
// arestas: uma tentativa recusada (tr.Err sem Changed) devolve o erro sem mutar; um
// drift (tr.Err com Changed) MUTA para changed e devolve ErrSchemaDrift; uma falha
// de audit numa transição impede a mutação (ErrAuditFailed).
func (m *Monitor) applyLocked(ctx context.Context, span agentruntime.Span, identity string, tr transition) (Outcome, error) {
	// Tentativa RECUSADA sem mudança de estado (ex.: ratify em estado errado,
	// re-aprovação in-band): audita a tentativa como deny (o rasto importa) e recusa.
	if tr.Err != nil && !tr.Changed {
		// O estado corrente (se existir) é preservado; auditamos a tentativa recusada
		// contra a referência corrente quando a há.
		cur, ok := m.records[identity]
		ref := cur.Ref
		if !ok {
			ref = reference{}
		}
		m.auditAttempt(ctx, identity, ref, tr.Err)
		if ok {
			ok2, reason := admits(cur.State)
			m.annotate(span, cur.State, ref, "denied", tr.Err.Error())
			return Outcome{
				Identity: identity, State: cur.State, Version: ref.Version, Digest: ref.Digest,
				Admitted: ok2, Drift: cur.State == StateChanged, Reason: reason,
			}, tr.Err
		}
		m.annotate(span, "", ref, "denied", tr.Err.Error())
		return Outcome{Identity: identity}, tr.Err
	}

	// Sem mudança de estado e sem erro: re-observação idêntica (pinned/first_seen que
	// se mantém). Não há transição a selar.
	if !tr.Changed {
		ok, reason := admits(tr.Next.State)
		decision := "passed"
		if !ok {
			decision = "pending"
		}
		m.annotate(span, tr.Next.State, tr.Next.Ref, decision, reason)
		out := m.outcome(identity, tr.Next, ok, false, reason)
		return out, tr.Err // tr.Err é ErrSchemaDrift no caso changed idempotente; nil caso contrário
	}

	// Mudança de estado: SELA a transição no audit ANTES de a aplicar. Uma transição
	// não-auditável não toma efeito (fail-closed).
	if err := m.record(ctx, tr.Cap, identity, tr.Next.Ref, tr.Decision); err != nil {
		m.annotate(span, tr.Next.State, tr.Next.Ref, "error", "audit_failed")
		return Outcome{}, err
	}
	m.records[identity] = tr.Next

	drift := tr.Cap == capChanged
	admitted, reason := admits(tr.Next.State)
	decision := string(tr.Next.State)
	if drift {
		decision = "drift"
	}
	m.annotate(span, tr.Next.State, tr.Next.Ref, decision, reason)
	return m.outcome(identity, tr.Next, admitted, drift, reason), tr.Err
}

// outcome monta o Outcome a partir do record resultante.
func (m *Monitor) outcome(identity string, r record, admitted, drift bool, reason string) Outcome {
	return Outcome{
		Identity: identity,
		State:    r.State,
		Version:  r.Ref.Version,
		Digest:   r.Ref.Digest,
		Admitted: admitted,
		Drift:    drift,
		Reason:   reason,
	}
}

// annotate preenche os atributos finais do span (estado/decisão/razão públicos).
func (m *Monitor) annotate(span agentruntime.Span, state TrustState, ref reference, decision, reason string) {
	if state != "" {
		span.SetAttribute(attrState, string(state))
	}
	if ref.Digest != "" {
		span.SetAttribute(attrDigest, ref.Digest)
	}
	span.SetAttribute(attrDecision, decision)
	if reason != "" {
		span.SetAttribute(attrReason, reason)
	}
}

// Admits é o ponto de consulta do BLOQUEIO TOFU (default-deny, ADR-002): a
// utilização de um artefacto de uma identidade só é admissível se o seu estado for
// pinned. Uma identidade em first_seen (não ratificada), em changed (incidente de
// drift) ou DESCONHECIDA (nunca observada) NÃO é admitida. Devolve o veredicto, o
// estado corrente e a razão legível. É read-only (não audita nem transita).
func (m *Monitor) Admits(identity string) (bool, TrustState, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.records[identity]
	if !ok {
		return false, "", "identidade desconhecida (nunca observada; default-deny)"
	}
	admitted, reason := admits(r.State)
	return admitted, r.State, reason
}

// State devolve o estado de confiança corrente de uma identidade e se ela é
// conhecida. Read-only — para observabilidade e testes.
func (m *Monitor) State(identity string) (TrustState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.records[identity]
	if !ok {
		return "", false
	}
	return r.State, true
}

// record sela uma transição de confiança na hash-chain de audit com a identidade
// (ToolID), a versão (PolicyVersion), o digest (Resource.Value) e o veredicto
// (Decision). Fail-closed: um erro do store é embrulhado em [ErrAuditFailed] para
// que o chamador recuse a transição.
func (m *Monitor) record(ctx context.Context, capability, identity string, ref reference, decision transitionDecision) error {
	rec := audit.AuditRecord{
		Partition:     m.partition,
		Timestamp:     m.now(),
		Decision:      auditDecision(decision),
		Capability:    capability,
		ToolID:        identity,
		PolicyVersion: ref.Version.String(),
		Resource:      audit.Resource{Type: "mcp.manifest.digest", Value: ref.Digest},
		Context:       audit.CallContext{Taint: "untrusted"},
		RunID:         m.partition,
		StepID:        identity + "@" + ref.Version.String(),
	}
	if _, err := m.audit.Append(ctx, rec); err != nil {
		return ErrAuditFailed
	}
	return nil
}

// auditAttempt sela uma TENTATIVA recusada (ratify/reapprove inválidos, ou um re-hit
// de drift numa identidade já em changed) como deny, preservando o rasto da
// tentativa. Best-effort no rasto: uma falha do próprio audit não altera o veredicto
// de recusa já decidido (a tentativa é recusada de qualquer modo), pelo que o erro de
// audit é deliberadamente descartado aqui — a acção principal (a recusa) já é
// fail-closed. A Capability é DERIVADA da causa (via [capForCause]) para que uma
// consulta forense do WORM filtrada por Capability distinga um re-hit de drift de uma
// verdadeira tentativa de re-aprovação — a fidelidade do rasto de um controlo
// tamper-evident depende deste rótulo ser fiel à operação recusada.
func (m *Monitor) auditAttempt(ctx context.Context, identity string, ref reference, cause error) {
	code := "E_TOFU_DENIED"
	if te, ok := cause.(*TofuError); ok {
		code = te.Code
	}
	rec := audit.AuditRecord{
		Partition:     m.partition,
		Timestamp:     m.now(),
		Decision:      audit.DecisionDeny,
		Capability:    capForCause(cause),
		ToolID:        identity,
		PolicyVersion: ref.Version.String(),
		Resource:      audit.Resource{Type: "mcp.manifest.digest", Value: ref.Digest},
		Context:       audit.CallContext{Taint: "untrusted"},
		RunID:         m.partition,
		StepID:        identity + ":denied:" + code,
	}
	_, _ = m.audit.Append(ctx, rec)
}

// capForCause deriva a capability de audit de uma TENTATIVA recusada a partir da sua
// causa, para que o registo WORM rotule a operação REALMENTE recusada em vez de
// assumir sempre re-aprovação. Sem esta derivação, um re-hit de drift (nova
// observação de uma identidade já em changed) seria selado como
// "registry.tofu.reapproved" — indistinguível de uma tentativa de re-aprovação numa
// consulta forense filtrada por Capability. O mapeamento é a capability da operação
// tentada:
//
//   - ErrSchemaDrift (re-hit de drift em changed) → capChanged;
//   - ErrNotFirstSeen / ErrRatifyMismatch (ratify inválido) → capPinned;
//   - ErrNotChanged / ErrInBandReapproval / ErrVersionRegression (reapprove inválido)
//     e qualquer outra causa → capReapproved.
func capForCause(cause error) string {
	switch {
	case errors.Is(cause, ErrSchemaDrift):
		return capChanged
	case errors.Is(cause, ErrNotFirstSeen), errors.Is(cause, ErrRatifyMismatch):
		return capPinned
	default: // ErrNotChanged, ErrInBandReapproval, ErrVersionRegression
		return capReapproved
	}
}

// auditDecision traduz o veredicto puro no vocabulário do pacote audit.
func auditDecision(d transitionDecision) audit.Decision {
	if d == decisionDeny {
		return audit.DecisionDeny
	}
	return audit.DecisionAllow
}
