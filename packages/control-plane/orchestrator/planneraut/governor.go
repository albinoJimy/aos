package planneraut

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// ---------------------------------------------------------------------------
// Portas (dependências de outros módulos entram por interface; o wiring liga-as).
// ---------------------------------------------------------------------------

// OverrideRateSource é a porta para a taxa de override humano AUTORITATIVA de
// AOS-095. O planeador NÃO a reporta: é medida a jusante do humano e servida por
// aqui. É o ancoramento NÃO-GAMEÁVEL da promoção (DoD) — contadores próprios
// perfeitos não bastam se esta fonte independente reportar overrides a mais. Um
// erro da fonte é fail-closed: propaga-se e a janela não é avaliada (nem promove).
type OverrideRateSource interface {
	OverrideRate(ctx context.Context, domain DomainKey) (float64, error)
}

// EvalRequest é o pedido ao eval-gate de decomposição (AOS-241).
type EvalRequest struct {
	PlanID string
	Domain DomainKey
	Level  Level
}

// EvalOutcome é o veredicto do eval-gate de decomposição.
type EvalOutcome struct {
	Passed bool
	Reason string
}

// DecompositionEvalGate é a porta para o eval-gate de decomposição (AOS-241) — o
// TRAVÃO DE RUNTIME independente do humano. É PRÉ-CONDIÇÃO de qualquer
// auto-aprovação e corre mesmo a L4/L5; se reprovar (ou erra), a auto-aprovação é
// travada e a revisão humana forçada, sem depender de um humano no laço.
type DecompositionEvalGate interface {
	Evaluate(ctx context.Context, req EvalRequest) (EvalOutcome, error)
}

// ---------------------------------------------------------------------------
// Erros sentinela (fail-closed).
// ---------------------------------------------------------------------------

var (
	// ErrNilPort — uma porta obrigatória é nil.
	ErrNilPort = errors.New("planneraut: porta obrigatoria nil")
	// ErrInvalidConfig — configuração inválida (limites <=0, nível fora de L0-L5,
	// envelope sem tecto de SLI).
	ErrInvalidConfig = errors.New("planneraut: configuracao invalida")
)

// ---------------------------------------------------------------------------
// Config e Governor.
// ---------------------------------------------------------------------------

// Config parametriza o Governor.
type Config struct {
	// Envelope são os limites sadios dos sinais (a base da promoção/demoção).
	Envelope Envelope
	// MinSample é o nº MÍNIMO de planos numa janela para render juízo. Abaixo disto a
	// janela é evidência insuficiente: não promove NEM demove. >0 obrigatório.
	MinSample int
	// MinRecurrence é o nº de janelas SÃS SUSTENTADAS que um domínio tem de acumular
	// para promover UM nível (AOS-014). >0 obrigatório. É o custo, em janelas, de cada
	// degrau de autonomia — a lentidão do lado "promover" da assimetria.
	MinRecurrence int
	// MaxLevel é o tecto de promoção (tipicamente L5). Válido L0–L5.
	MaxLevel Level
	// AutoApproveFrom é o nível A PARTIR do qual a auto-aprovação dentro do envelope é
	// possível (tipicamente L4). Válido L0–L5.
	AutoApproveFrom Level
	// SampleEveryN — amostragem post-hoc: 1 em cada N auto-aprovações é marcada para
	// escrutínio a posteriori (mesmo a L4/L5). >0 obrigatório.
	SampleEveryN int
}

func (c Config) valid() bool {
	return c.MinSample > 0 && c.MinRecurrence > 0 && c.SampleEveryN > 0 &&
		c.MaxLevel.Valid() && c.AutoApproveFrom.Valid() && c.Envelope.valid()
}

// domainState é o estado por-domínio: o nível corrente, a série sã sustentada e os
// últimos sinais observados. Protegido pelo mutex do Governor.
type domainState struct {
	key         DomainKey
	level       Level
	recurrence  int // janelas sãs consecutivas desde o último degrau/demoção
	lastSignals Signals
	hasSignals  bool
}

// Governor governa a autonomia L0–L5 por domínio: observa janelas (promove/demove),
// expõe os sinais/SLI e decide a auto-aprovação sobre o risco DERIVADO. Seguro para
// uso concorrente.
type Governor struct {
	cfg      Config
	override OverrideRateSource
	evalGate DecompositionEvalGate

	mu        sync.Mutex
	domains   map[DomainKey]*domainState
	sampleCtr uint64
}

// NewGovernor constrói um Governor. Todas as portas são OBRIGATÓRIAS (nil ⇒
// [ErrNilPort]) e a Config é validada ([ErrInvalidConfig]) — fail-closed, sem no-op
// silencioso.
func NewGovernor(cfg Config, override OverrideRateSource, evalGate DecompositionEvalGate) (*Governor, error) {
	if override == nil || evalGate == nil {
		return nil, ErrNilPort
	}
	if !cfg.valid() {
		return nil, ErrInvalidConfig
	}
	return &Governor{
		cfg:      cfg,
		override: override,
		evalGate: evalGate,
		domains:  make(map[DomainKey]*domainState),
	}, nil
}

// ---------------------------------------------------------------------------
// Observação de janela: promoção lenta (recorrência) / demoção imediata (anomalia).
// ---------------------------------------------------------------------------

// WindowOutcome é o desfecho de observar uma janela para um domínio.
type WindowOutcome struct {
	Domain     DomainKey
	Signals    Signals
	Level      Level    // nível APÓS esta janela
	Promoted   bool     // subiu um degrau nesta janela
	Demoted    bool     // baixou (a L0) nesta janela
	Breaches   []Breach // anomalias que causaram a demoção (vazio se sã)
	Recurrence int      // série sã sustentada após esta janela
	Evaluated  bool     // false se amostra < MinSample (sem juízo)
}

// ObserveWindow observa uma janela de contadores para um domínio e actualiza o nível.
// Busca a taxa de override AUTORITATIVA (AOS-095, porta) ANTES de derivar os sinais —
// é o input não-gameável. Regras:
//
//   - amostra < MinSample ⇒ evidência insuficiente: nem promove nem demove;
//   - QUALQUER sinal fora do envelope ⇒ DEMOÇÃO a L0 e reset da série (o lado rápido
//     da assimetria — uma anomalia chega para perder toda a autonomia);
//   - janela sã ⇒ +1 na série; ao atingir MinRecurrence sobe UM degrau (até MaxLevel)
//     e reinicia a série (cada degrau custa MinRecurrence janelas sustentadas — um
//     domínio AD-HOC, observado uma vez, nunca acumula a série e fica em L0).
//
// Fail-closed: um erro da fonte de override propaga-se e a janela não altera o nível.
func (g *Governor) ObserveWindow(ctx context.Context, domain DomainKey, c Counters) (WindowOutcome, error) {
	orate, err := g.override.OverrideRate(ctx, domain)
	if err != nil {
		return WindowOutcome{}, fmt.Errorf("planneraut: fonte de override (AOS-095): %w", err)
	}
	sig := ComputeSignals(c, orate)

	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.domains[domain]
	if st == nil {
		st = &domainState{key: domain, level: L0}
		g.domains[domain] = st
	}
	st.lastSignals = sig
	st.hasSignals = true

	out := WindowOutcome{Domain: domain, Signals: sig}

	// Evidência insuficiente: não julga (nem promove nem demove, nem toca na série).
	if c.Plans < int64(g.cfg.MinSample) {
		out.Level = st.level
		out.Recurrence = st.recurrence
		return out, nil
	}
	out.Evaluated = true

	// Anomalia: demoção imediata a L0 (lado rápido da assimetria).
	if breaches := g.cfg.Envelope.Evaluate(sig); len(breaches) > 0 {
		out.Demoted = st.level != L0
		st.level = L0
		st.recurrence = 0
		out.Level = L0
		out.Breaches = breaches
		out.Recurrence = 0
		return out, nil
	}

	// Janela sã: acumula recorrência; promove um degrau ao atingir o limiar.
	st.recurrence++
	if st.recurrence >= g.cfg.MinRecurrence && st.level < g.cfg.MaxLevel {
		st.level++
		st.recurrence = 0
		out.Promoted = true
	}
	out.Level = st.level
	out.Recurrence = st.recurrence
	return out, nil
}

// ---------------------------------------------------------------------------
// Auto-aprovação sobre RISCO DERIVADO, com travão de eval-gate.
// ---------------------------------------------------------------------------

// Códigos de razão ESTÁVEIS da decisão de auto-aprovação (content-free, auditáveis).
const (
	ReasonDangerForcesReview        = "derived_danger_forces_review"
	ReasonNonSafeForcesReview       = "derived_risk_not_safe_forces_review"
	ReasonCapabilityGapForcesReview = "capability_gap_forces_review"
	ReasonBelowAutoApprove          = "level_below_autoapprove_envelope"
	ReasonEvalGateBrake             = "decomposition_eval_gate_failed"
	ReasonAutoApproved              = "auto_approved_within_envelope"
)

// AutoApproveInput é o pedido de decisão de auto-aprovação de UM plano.
type AutoApproveInput struct {
	PlanID string
	Domain DomainKey
	// Derived é o RISCO DERIVADO das ferramentas pinadas (AOS-232) — AUTORITATIVO. É
	// o piso; a decisão avalia-se sobre ele.
	Derived plan.RiskClass
	// Declared é o rótulo `risk_class` ADVISORY do LLM (untrusted, ADR-005). Só pode
	// ELEVAR o piso derivado, NUNCA baixá-lo (ver [elevate]). Um `safe` declarado
	// jamais esconde um `danger` derivado.
	Declared plan.RiskClass
	// HasCapabilityGap indica que algum nó abriu um `capability_gap` (AOS-113).
	HasCapabilityGap bool
}

// AutoApproveDecision é o desfecho da decisão.
type AutoApproveDecision struct {
	// Level é o nível corrente do domínio (L0 se desconhecido/ad-hoc).
	Level Level
	// AutoApproved — o plano pode dispensar a aprovação humana.
	AutoApproved bool
	// RequireHuman — a revisão humana é forçada.
	RequireHuman bool
	// EvalSampled — esta auto-aprovação foi marcada para amostragem post-hoc.
	EvalSampled bool
	// Reason é o código estável que justifica a decisão.
	Reason string
}

// AuthorizeAutoApproval decide se um plano auto-aprova, pela ordem fail-closed:
//
//  1. risco DERIVADO (elevado pelo advisory, nunca baixado): SÓ `safe` é
//     auto-aprovável. `danger` ⇒ revisão humana SEMPRE (mesmo a L5); `gray`
//     (revisão item-a-item no gate, ver [plan.RiskGray]) e `unset` (risco
//     desconhecido) ⇒ revisão humana também — só o piso SAFE dispensa humano;
//  2. capability_gap ⇒ revisão humana SEMPRE, mesmo a L5;
//  3. nível do domínio < AutoApproveFrom ⇒ revisão humana (fora do envelope de
//     autonomia);
//  4. TRAVÃO: eval-gate de decomposição (AOS-241, porta) como pré-condição — corre
//     mesmo a L4/L5; reprovação/erro ⇒ revisão humana (independente do humano);
//  5. auto-aprova dentro do envelope; amostragem post-hoc marcada mesmo a L4/L5.
//
// A decisão NUNCA lê o rótulo do LLM isoladamente: [elevate] garante que o advisory
// só sobe o piso derivado. Assim, Derived=danger + Declared=safe a L5 ⇒ revisão.
func (g *Governor) AuthorizeAutoApproval(ctx context.Context, in AutoApproveInput) (AutoApproveDecision, error) {
	g.mu.Lock()
	lvl := L0
	if st := g.domains[in.Domain]; st != nil {
		lvl = st.level
	}
	g.mu.Unlock()

	d := AutoApproveDecision{Level: lvl}

	// (1) O risco DERIVADO (AOS-232) é AUTORITATIVO e é o PISO: só um derived `safe`
	//     é auto-aprovável. O advisory untrusted do LLM ([elevate]) só pode ELEVAR
	//     esse piso para forçar revisão, NUNCA rebaixá-lo — em particular, um `unset`
	//     (risco derivado DESCONHECIDO, fail-closed) jamais é promovido a `safe` por
	//     um rótulo `safe` do modelo (elevate(unset,safe)==safe, pelo que testar só o
	//     elevado deixaria passar o desconhecido — por isso testamos o DERIVADO cru).
	//     `danger` (derivado ou elevado) ⇒ [ReasonDangerForcesReview]; `gray`/`unset`
	//     e qualquer outro não-`safe` ⇒ [ReasonNonSafeForcesReview].
	if effective := elevate(in.Derived, in.Declared); in.Derived != plan.RiskSafe || effective != plan.RiskSafe {
		d.RequireHuman = true
		if in.Derived == plan.RiskDanger || effective == plan.RiskDanger {
			d.Reason = ReasonDangerForcesReview
		} else {
			d.Reason = ReasonNonSafeForcesReview
		}
		return d, nil
	}
	// (2) capability_gap ⇒ revisão sempre (mesmo a L5).
	if in.HasCapabilityGap {
		d.RequireHuman = true
		d.Reason = ReasonCapabilityGapForcesReview
		return d, nil
	}
	// (3) Fora do envelope de autonomia.
	if lvl < g.cfg.AutoApproveFrom {
		d.RequireHuman = true
		d.Reason = ReasonBelowAutoApprove
		return d, nil
	}
	// (4) Travão de runtime: pré-condição do eval-gate (corre mesmo a L4/L5). A porta
	//     é chamada FORA do lock (chamada externa não deve reter o mutex).
	out, err := g.evalGate.Evaluate(ctx, EvalRequest{PlanID: in.PlanID, Domain: in.Domain, Level: lvl})
	if err != nil || !out.Passed {
		d.RequireHuman = true
		d.Reason = ReasonEvalGateBrake
		return d, nil
	}
	// (5) Auto-aprovado dentro do envelope; amostragem post-hoc mesmo a L4/L5.
	d.AutoApproved = true
	d.EvalSampled = g.nextSample()
	d.Reason = ReasonAutoApproved
	return d, nil
}

// nextSample avança o contador determinístico de amostragem post-hoc e devolve se
// esta auto-aprovação cai na amostra (1 em cada SampleEveryN). Sob lock.
func (g *Governor) nextSample() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sampleCtr++
	// SampleEveryN > 0 é invariante do construtor (Config.valid()); a guarda torna-o
	// explícito e fail-safe (um N <= 0 desliga a amostragem em vez de dividir por zero
	// ou de converter um negativo para um uint64 gigante).
	n := g.cfg.SampleEveryN
	if n <= 0 {
		return false
	}
	return g.sampleCtr%uint64(n) == 0
}

// ---------------------------------------------------------------------------
// Inspecção / SLI (o nível, os sinais e o SLI de planeamento são visíveis).
// ---------------------------------------------------------------------------

// Level devolve o nível de autonomia corrente de um domínio (L0 se desconhecido —
// um domínio ad-hoc nunca observado está no piso).
func (g *Governor) Level(domain DomainKey) Level {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st := g.domains[domain]; st != nil {
		return st.level
	}
	return L0
}

// Recurrence devolve a série sã sustentada corrente de um domínio (0 se desconhecido).
func (g *Governor) Recurrence(domain DomainKey) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st := g.domains[domain]; st != nil {
		return st.recurrence
	}
	return 0
}

// LastSignals devolve os últimos sinais observados de um domínio e se existem.
func (g *Governor) LastSignals(domain DomainKey) (Signals, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st := g.domains[domain]; st != nil && st.hasSignals {
		return st.lastSignals, true
	}
	return Signals{}, false
}

// PlanningFractionSLI expõe o SLI de fracção de planeamento de um domínio: a fracção
// corrente, se respeita o tecto do envelope, e se há sinais. É a superfície de
// métrica do SLI de 5% (AOS-242).
func (g *Governor) PlanningFractionSLI(domain DomainKey) (fraction float64, withinSLI, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.domains[domain]
	if st == nil || !st.hasSignals {
		return 0, false, false
	}
	f := st.lastSignals.PlanningFraction
	return f, f <= g.cfg.Envelope.MaxPlanningFraction, true
}

// ---------------------------------------------------------------------------
// Regra «só eleva» do risco (local — reflecte AOS-232/§3.3 regra 6).
// ---------------------------------------------------------------------------

// riskRank é a ordem SEMÂNTICA do enum de risco (unset < safe < gray < danger). O
// rótulo ausente fica no fundo — um advisory omitido nunca eleva nada.
func riskRank(r plan.RiskClass) int {
	switch r {
	case plan.RiskSafe:
		return 1
	case plan.RiskGray:
		return 2
	case plan.RiskDanger:
		return 3
	default:
		return 0 // RiskUnset / desconhecido
	}
}

// elevate aplica a regra advisory: o rótulo DECLARADO do LLM só é aceite se ELEVAR o
// piso DERIVADO; um downgrade é ignorado (devolve o derivado). O rótulo do modelo só
// SOBE o risco, nunca o baixa — é isto que impede um `safe` declarado de esconder um
// `danger` derivado. Puro.
func elevate(derived, declared plan.RiskClass) plan.RiskClass {
	if riskRank(declared) > riskRank(derived) {
		return declared
	}
	return derived
}
