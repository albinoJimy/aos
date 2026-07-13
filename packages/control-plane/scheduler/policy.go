// policy.go — motor de POLÍTICA DECLARATIVA de degradação (AOS-030).
//
// Quando uma fila limitada satura (queue.go), é preciso decidir QUE acção de
// degradação aplicar. Essa decisão NÃO é imperativa espalhada pelo código: vive
// numa POLÍTICA DECLARATIVA versionada — um artefacto de configuração (JSON,
// stdlib, ZERO deps) que mapeia condições de saturação → acção nominal. O motor
// apenas SELECCIONA a acção (determinística: mesma condição → mesma acção) e
// emite degradation_policy_selected; a EXECUÇÃO das acções (shed/defer/downgrade/
// reject) é o AOS-031, fora deste âmbito.
//
// VERSIONAMENTO (à imagem do bundle assinado/versionado do PDP, AOS-004, mas com
// JSON stdlib em vez de Cedar): cada artefacto declara uma versão SemVer. O
// HOT-RELOAD troca o motor de decisão ATOMICAMENTE (atomic.Pointer) — as filas em
// curso NÃO são tocadas, logo nenhum trabalho se perde. Cada troca de versão
// regista um CHANGELOG no audit trail (evento append-only versão-antiga→nova). A
// validação é FAIL-CLOSED: uma configuração inválida (JSON malformado, acção
// desconhecida, SemVer inválido, ou versão não-monótona) é REJEITADA e mantém-se
// a política anterior — nunca se cai numa política vazia ou permissiva.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento append-only do motor de política (AOS-030). Contrato auditável.
const (
	// EventDegradationPolicySelected marca a SELECÇÃO de uma acção de degradação
	// para uma condição de saturação (a execução é AOS-031).
	EventDegradationPolicySelected = "backpressure.degradation_policy_selected"
	// EventPolicyReloaded é o CHANGELOG de versão no audit trail: versão antiga→nova.
	EventPolicyReloaded = "backpressure.policy_reloaded"
)

// DefaultPolicyNHI é a NHI por omissão do motor de política nos eventos que emite.
const DefaultPolicyNHI = "nhi:control-plane/scheduler/backpressure-policy"

// policyAuditStreamPrefix é o prefixo do stream de auditoria (changelog de versões).
const policyAuditStreamPrefix = "backpressure/policy-audit/"

// Atributos de span (OTel, porta zero-dep).
const (
	attrPolicyVersion  = "aos.backpressure.policy_version"
	attrPolicyAction   = "aos.backpressure.action"
	attrPolicyFrom     = "aos.backpressure.policy_from"
	attrPolicyPriority = "aos.backpressure.priority"
	attrPolicyFill     = "aos.backpressure.fill_ratio"
)

const opSelectPolicy = "backpressure_policy_select"

// ErrInvalidPolicy sinaliza uma configuração REJEITADA na validação (fail-closed).
// A política anterior mantém-se; nunca se adopta uma config inválida.
var ErrInvalidPolicy = errors.New("scheduler: política de degradação inválida")

// ErrStalePolicy sinaliza um hot-reload REJEITADO por a versão não ser mais
// recente que a corrente (SemVer não-monótona). Fail-closed: mantém a anterior.
var ErrStalePolicy = errors.New("scheduler: versão de política não é mais recente que a corrente")

// DegradationAction é o nome NOMINAL de uma acção de degradação. O motor apenas a
// SELECCIONA; a execução (shed→defer→downgrade→reject) é o AOS-031.
type DegradationAction string

const (
	// ActionShed — descartar trabalho de baixa prioridade (executado em AOS-031).
	ActionShed DegradationAction = "shed"
	// ActionDefer — adiar trabalho admissível (executado em AOS-031).
	ActionDefer DegradationAction = "defer"
	// ActionDowngrade — encaminhar para tier mais barato (executado em AOS-031).
	ActionDowngrade DegradationAction = "downgrade"
	// ActionReject — rejeitar como último recurso (executado em AOS-031).
	ActionReject DegradationAction = "reject"
)

// valid indica se a acção é uma das quatro acções nominais reconhecidas.
func (a DegradationAction) valid() bool {
	switch a {
	case ActionShed, ActionDefer, ActionDowngrade, ActionReject:
		return true
	default:
		return false
	}
}

// SaturationCondition é a CONDIÇÃO observável avaliada contra as regras da
// política. É pura (sem relógio/estado): a idade já vem resolvida como duração.
type SaturationCondition struct {
	// Tenant e Priority são as dimensões de partição (tenant:priority).
	Tenant   string
	Priority string
	// Depth é a profundidade corrente da fila; Capacity é o limite (MaxLen).
	Depth    int
	Capacity int
	// FillRatio = Depth/Capacity ∈ [0,1] (0 se Capacity<=0).
	FillRatio float64
	// OldestAge é a idade do item mais antigo em fila.
	OldestAge time.Duration
	// Saturated é o estado latched de histerese da partição no momento.
	Saturated bool
}

// PolicyRule é uma regra declarativa: se a condição casar (prioridade + limiar de
// enchimento + limiar de idade), selecciona-se Action. A ordem de declaração é a
// ordem de avaliação (primeira que casa vence) — DETERMINÍSTICO.
type PolicyRule struct {
	// Priority restringe a regra a uma classe ("" = qualquer prioridade).
	Priority string `json:"priority,omitempty"`
	// MinFillRatio é o enchimento mínimo (Depth/Capacity) para a regra casar (∈[0,1]).
	MinFillRatio float64 `json:"min_fill_ratio,omitempty"`
	// MinAgeMs é a idade mínima (ms) do item mais antigo para a regra casar.
	MinAgeMs int64 `json:"min_age_ms,omitempty"`
	// Action é a acção nominal seleccionada quando a regra casa.
	Action DegradationAction `json:"action"`
}

// matches decide, de forma pura e determinística, se a regra casa a condição.
func (r PolicyRule) matches(c SaturationCondition) bool {
	if r.Priority != "" && r.Priority != c.Priority {
		return false
	}
	if c.FillRatio < r.MinFillRatio {
		return false
	}
	if r.MinAgeMs > 0 && c.OldestAge < time.Duration(r.MinAgeMs)*time.Millisecond {
		return false
	}
	return true
}

// PolicyDoc é o ARTEFACTO de política versionado (serializado em JSON). Version é
// SemVer; Rules são avaliadas por ordem; DefaultAction é a rede de segurança
// (aplicada quando nenhuma regra casa — nunca "sem acção").
type PolicyDoc struct {
	Version       string            `json:"version"`
	Rules         []PolicyRule      `json:"rules"`
	DefaultAction DegradationAction `json:"default_action"`
}

// validate impõe as invariantes fail-closed do artefacto. Uma config que não
// valide é rejeitada e a política anterior mantém-se.
func (d PolicyDoc) validate() error {
	if _, ok := parseSemVer(d.Version); !ok {
		return fmt.Errorf("%w: versão %q não é SemVer MAJOR.MINOR.PATCH", ErrInvalidPolicy, d.Version)
	}
	if !d.DefaultAction.valid() {
		return fmt.Errorf("%w: default_action %q desconhecida (esperado shed|defer|downgrade|reject)", ErrInvalidPolicy, d.DefaultAction)
	}
	for i, r := range d.Rules {
		if !r.Action.valid() {
			return fmt.Errorf("%w: regra %d com acção %q desconhecida", ErrInvalidPolicy, i, r.Action)
		}
		if r.MinFillRatio < 0 || r.MinFillRatio > 1 {
			return fmt.Errorf("%w: regra %d com min_fill_ratio %.3f fora de [0,1]", ErrInvalidPolicy, i, r.MinFillRatio)
		}
		if r.MinAgeMs < 0 {
			return fmt.Errorf("%w: regra %d com min_age_ms negativo (%d)", ErrInvalidPolicy, i, r.MinAgeMs)
		}
	}
	return nil
}

// Select escolhe a acção para a condição: primeira regra (por ordem) que casa
// vence; se nenhuma casar, DefaultAction. Determinístico e puro.
func (d PolicyDoc) Select(c SaturationCondition) DegradationAction {
	for _, r := range d.Rules {
		if r.matches(c) {
			return r.Action
		}
	}
	return d.DefaultAction
}

// ParsePolicy desserializa e VALIDA um artefacto JSON (fail-closed). Campos
// desconhecidos são rejeitados (DisallowUnknownFields) para apanhar drift de
// schema em vez de o ignorar silenciosamente.
func ParsePolicy(raw []byte) (PolicyDoc, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var d PolicyDoc
	if err := dec.Decode(&d); err != nil {
		return PolicyDoc{}, fmt.Errorf("%w: JSON malformado: %v", ErrInvalidPolicy, err)
	}
	if err := d.validate(); err != nil {
		return PolicyDoc{}, err
	}
	return d, nil
}

// PolicyEngine detém a política CORRENTE e permite hot-reload atómico versionado.
// As leituras (Select) são lock-free (atomic.Pointer); os reloads são
// serializados (mu). Construir com [NewPolicyEngine].
type PolicyEngine struct {
	current  atomic.Pointer[PolicyDoc]
	mu       sync.Mutex // serializa reloads (só um vencedor troca a versão)
	log      EventLog
	now      func() time.Time
	tracer   agentruntime.Tracer
	producer eventstore.Producer
	name     string
	// nEvents gera o step_id monotónico da auditoria. É atómico porque Select() é
	// lock-free (não segura e.mu): Select||Select e Select||Reload incrementam-no
	// concorrentemente, logo a mutação TEM de ser serializada por si só, sob pena
	// de step_ids torn/duplicados corromperem a dedup append-only do Event Store.
	nEvents atomic.Uint64
}

// PolicyEngineOption configura o [PolicyEngine].
type PolicyEngineOption func(*PolicyEngine)

// WithPolicyLog injecta o Event Store para o changelog de versões e a emissão de
// degradation_policy_selected. Sem log, o motor selecciona na mesma (útil em
// testes puros), mas não deixa rasto auditável.
func WithPolicyLog(log EventLog) PolicyEngineOption {
	return func(e *PolicyEngine) {
		if log != nil {
			e.log = log
		}
	}
}

// WithPolicyClock injecta o relógio (determinismo/replay: sem time.Now embutido).
func WithPolicyClock(now func() time.Time) PolicyEngineOption {
	return func(e *PolicyEngine) {
		if now != nil {
			e.now = now
		}
	}
}

// WithPolicyTracer injecta a porta OTel (spans de selecção). Zero-dep.
func WithPolicyTracer(t agentruntime.Tracer) PolicyEngineOption {
	return func(e *PolicyEngine) {
		if t != nil {
			e.tracer = t
		}
	}
}

// WithPolicyProducer injecta a NHI emissora dos eventos.
func WithPolicyProducer(p eventstore.Producer) PolicyEngineOption {
	return func(e *PolicyEngine) {
		if p.NHIID != "" {
			e.producer = p
		}
	}
}

// WithPolicyName nomeia o artefacto (usado no stream de auditoria). Permite vários
// motores independentes no mesmo Event Store.
func WithPolicyName(name string) PolicyEngineOption {
	return func(e *PolicyEngine) {
		if name != "" {
			e.name = name
		}
	}
}

// NewPolicyEngine constrói o motor com uma política INICIAL (validada fail-closed).
// Se um log for injectado, regista o carregamento inicial ("" → versão) no audit
// trail, para que o replay reconstrua a linhagem de versões desde o arranque.
func NewPolicyEngine(initial PolicyDoc, opts ...PolicyEngineOption) (*PolicyEngine, error) {
	if err := initial.validate(); err != nil {
		return nil, err
	}
	e := &PolicyEngine{
		now:      time.Now,
		tracer:   agentruntime.NoopTracer{},
		producer: eventstore.Producer{NHIID: DefaultPolicyNHI},
		name:     "default",
	}
	for _, opt := range opts {
		opt(e)
	}
	doc := initial
	e.current.Store(&doc)
	if e.log != nil {
		if err := e.appendReload(context.Background(), "", doc.Version, len(doc.Rules)); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// Current devolve a política corrente (cópia por valor do ponteiro atómico).
func (e *PolicyEngine) Current() PolicyDoc {
	return *e.current.Load()
}

// Version devolve a versão SemVer corrente.
func (e *PolicyEngine) Version() string {
	return e.current.Load().Version
}

// Select resolve a acção para uma condição de saturação e, se houver log, emite
// degradation_policy_selected (append-only, observável). A decisão é sempre
// tomada sobre a política corrente (ponteiro atómico) — um hot-reload concorrente
// nunca deixa a selecção a meio.
func (e *PolicyEngine) Select(ctx context.Context, c SaturationCondition) (DegradationAction, string, error) {
	_, span := e.tracer.StartSpan(ctx, opSelectPolicy)
	defer span.End()

	doc := e.current.Load()
	action := doc.Select(c)

	span.SetAttribute(attrPolicyVersion, doc.Version)
	span.SetAttribute(attrPolicyAction, string(action))
	span.SetAttribute(attrPolicyPriority, c.Priority)
	span.SetAttribute(attrPolicyFill, c.FillRatio)

	if e.log != nil {
		if err := e.appendSelected(ctx, doc.Version, action, c); err != nil {
			return action, doc.Version, err
		}
	}
	return action, doc.Version, nil
}

// Reload troca a política ATOMICAMENTE por uma nova versão. Fluxo fail-closed:
//  1. ParsePolicy valida a config (JSON, acções, SemVer); inválida ⇒ mantém a
//     anterior e devolve ErrInvalidPolicy;
//  2. a nova versão TEM de ser estritamente mais recente que a corrente (SemVer
//     monótono); caso contrário ⇒ mantém a anterior e devolve ErrStalePolicy;
//  3. regista o changelog (versão-antiga→nova) no audit trail ANTES de trocar; se
//     o Event Store recusar, mantém a anterior (fail-closed);
//  4. troca o ponteiro atómico — as filas em curso NÃO são tocadas (nenhum
//     trabalho se perde).
func (e *PolicyEngine) Reload(ctx context.Context, raw []byte) (PolicyDoc, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	cur := e.current.Load()
	next, err := ParsePolicy(raw)
	if err != nil {
		return *cur, err // fail-closed: mantém a corrente
	}
	if compareSemVer(next.Version, cur.Version) <= 0 {
		return *cur, fmt.Errorf("%w: corrente=%s, oferecida=%s", ErrStalePolicy, cur.Version, next.Version)
	}
	// Changelog no audit trail ANTES da troca (fail-closed: se falhar, não troca).
	if e.log != nil {
		if err := e.appendReload(ctx, cur.Version, next.Version, len(next.Rules)); err != nil {
			return *cur, err
		}
	}
	e.current.Store(&next)
	return next, nil
}

// appendSelected persiste degradation_policy_selected no stream de auditoria.
func (e *PolicyEngine) appendSelected(ctx context.Context, version string, action DegradationAction, c SaturationCondition) error {
	pl := policyEventPayload{
		Type:          EventDegradationPolicySelected,
		PolicyVersion: version,
		Action:        string(action),
		Tenant:        c.Tenant,
		Priority:      c.Priority,
		Depth:         c.Depth,
		Capacity:      c.Capacity,
		FillRatio:     c.FillRatio,
		OldestAgeMs:   c.OldestAge.Milliseconds(),
		TSUnixNano:    e.now().UnixNano(),
	}
	return e.appendEvent(ctx, EventDegradationPolicySelected, pl)
}

// appendReload persiste o changelog de versão (versão-antiga→nova) no audit trail.
func (e *PolicyEngine) appendReload(ctx context.Context, from, to string, ruleCount int) error {
	pl := policyEventPayload{
		Type:          EventPolicyReloaded,
		PolicyVersion: to,
		FromVersion:   from,
		ToVersion:     to,
		RuleCount:     ruleCount,
		TSUnixNano:    e.now().UnixNano(),
	}
	return e.appendEvent(ctx, EventPolicyReloaded, pl)
}

// appendEvent serializa e escreve um evento de política no stream de auditoria,
// com step_id "pol-N" monotónico (idempotente por (run_id, step_id) na dedup do
// Event Store). Fail-closed: um erro do store propaga.
func (e *PolicyEngine) appendEvent(ctx context.Context, evType string, pl policyEventPayload) error {
	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	streamID := policyAuditStreamPrefix + e.name
	stepID := "pol-" + strconv.FormatUint(e.nEvents.Add(1), 10)
	_, err = e.log.Append(ctx, streamID, eventstore.EventInput{
		Type:     evType,
		Payload:  raw,
		RunID:    streamID,
		StepID:   stepID,
		Producer: e.producer,
	})
	return err
}

// policyEventPayload é o corpo serializado (estável, sem mapas) dos eventos do
// motor de política — determinismo/replay.
type policyEventPayload struct {
	Type          string  `json:"type"`
	PolicyVersion string  `json:"policy_version"`
	Action        string  `json:"action,omitempty"`
	FromVersion   string  `json:"from_version,omitempty"`
	ToVersion     string  `json:"to_version,omitempty"`
	RuleCount     int     `json:"rule_count,omitempty"`
	Tenant        string  `json:"tenant,omitempty"`
	Priority      string  `json:"priority,omitempty"`
	Depth         int     `json:"depth,omitempty"`
	Capacity      int     `json:"capacity,omitempty"`
	FillRatio     float64 `json:"fill_ratio,omitempty"`
	OldestAgeMs   int64   `json:"oldest_age_ms,omitempty"`
	TSUnixNano    int64   `json:"ts_unix_nano"`
}

// PolicyVersionChange é uma transição de versão reconstruída do audit trail.
type PolicyVersionChange struct {
	From      string
	To        string
	RuleCount int
	Seq       uint64
}

// ReplayVersions reconstrói a LINHAGEM de versões da política a partir do audit
// trail (append-only, por ordem de seq). É a prova de que o versionamento e o
// changelog se reconstroem do Event Store (ADR-001/010).
func (e *PolicyEngine) ReplayVersions(ctx context.Context) ([]PolicyVersionChange, error) {
	if e.log == nil {
		return nil, nil
	}
	streamID := policyAuditStreamPrefix + e.name
	evs, err := e.log.Read(ctx, streamID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var out []PolicyVersionChange
	for _, ev := range evs {
		if ev.Type != EventPolicyReloaded {
			continue
		}
		var pl policyEventPayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return nil, err
		}
		out = append(out, PolicyVersionChange{
			From:      pl.FromVersion,
			To:        pl.ToVersion,
			RuleCount: pl.RuleCount,
			Seq:       ev.Seq,
		})
	}
	return out, nil
}

// PolicySelection é uma DECISÃO de política (degradation_policy_selected)
// reconstruída do audit trail.
type PolicySelection struct {
	Version  string
	Action   DegradationAction
	Tenant   string
	Priority string
	Seq      uint64
}

// ReplaySelections reconstrói as SELECÇÕES de acção (degradation_policy_selected)
// a partir do audit trail (append-only, por ordem de seq). Complementa
// [ReplayVersions] (que cobre só a linhagem de versões): é a prova de que as
// DECISÕES de política — não apenas os estados de fila — são eventos observáveis
// que se reconstroem do Event Store (critério de aceitação 6).
func (e *PolicyEngine) ReplaySelections(ctx context.Context) ([]PolicySelection, error) {
	if e.log == nil {
		return nil, nil
	}
	streamID := policyAuditStreamPrefix + e.name
	evs, err := e.log.Read(ctx, streamID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var out []PolicySelection
	for _, ev := range evs {
		if ev.Type != EventDegradationPolicySelected {
			continue
		}
		var pl policyEventPayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return nil, err
		}
		out = append(out, PolicySelection{
			Version:  pl.PolicyVersion,
			Action:   DegradationAction(pl.Action),
			Tenant:   pl.Tenant,
			Priority: pl.Priority,
			Seq:      ev.Seq,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// SemVer mínimo (MAJOR.MINOR.PATCH numérico) — zero-dep. Suficiente para ordenar
// versões de artefacto; pré-release/build não são usados nas políticas do AOS.
// ---------------------------------------------------------------------------

type semver struct{ major, minor, patch int }

// parseSemVer aceita estritamente "X.Y.Z" com X,Y,Z inteiros não-negativos.
func parseSemVer(s string) (semver, bool) {
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var v semver
	dst := []*int{&v.major, &v.minor, &v.patch}
	for i, p := range parts {
		if p == "" {
			return semver{}, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return semver{}, false
		}
		*dst[i] = n
	}
	return v, true
}

// compareSemVer devolve -1/0/+1 comparando a<b/a==b/a>b. Versões inválidas
// ordenam-se antes das válidas (defensivo; a validação já as rejeita a montante).
func compareSemVer(a, b string) int {
	va, oka := parseSemVer(a)
	vb, okb := parseSemVer(b)
	switch {
	case !oka && !okb:
		return 0
	case !oka:
		return -1
	case !okb:
		return 1
	}
	if va.major != vb.major {
		return cmpInt(va.major, vb.major)
	}
	if va.minor != vb.minor {
		return cmpInt(va.minor, vb.minor)
	}
	return cmpInt(va.patch, vb.patch)
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
