package scheduler

// breaker.go — Circuit breaker de ORÇAMENTO por árvore de run (AOS-029,
// concretização do ADR-008: "orçamento em tokens/$ com circuit breaker").
//
// Enquanto o admission control (AOS-027) governa a ENTRADA de trabalho, o breaker
// governa a CONTINUAÇÃO: interrompe de forma segura uma árvore que queima
// orçamento a uma velocidade anómala OU que esgota o orçamento atribuído, ANTES de
// provocar explosão de custo. Dispara (trip) por DOIS sinais independentes:
//
//	(a) VELOCIDADE — tokens/$ por unidade de tempo acima de um limiar, medida numa
//	    JANELA DESLIZANTE com relógio INJECTÁVEL (à imagem do refill temporizado do
//	    AOS-027: sem time.Now no caminho de decisão);
//	(b) ESGOTAMENTO — orçamento remanescente da árvore (lido de budget.Available,
//	    AOS-026/008 — NÃO reimplementado aqui) esgotado/abaixo de uma margem.
//
// MÁQUINA DE ESTADOS PRÓPRIA (declarativa, NÃO confundir com a máquina das TAREFAS
// do AOS-017): closed → open → half-open (→ closed | → open). Half-open permite a
// retoma CONTROLADA após reavaliação/reabastecimento de orçamento.
//
// TRIP FAIL-CLOSED PARA O CONSUMO: por omissão PÁRA o gasto — na dúvida, open.
// [Breaker.Allow] nega a continuação enquanto open; um erro de decisão degrada para
// negação (nunca se concede consumo por omissão). Ao disparar, as tarefas em curso
// transitam para um estado DURÁVEL seguro (paused/waiting_on_human ou terminal
// controlado) através da Machine do AOS-017 (porta [TaskParker]), de forma
// IDEMPOTENTE — uma tarefa já parada/terminal é no-op, SEM duplicar efeitos (ADR-001).
//
// RETOMA em half-open NÃO re-executa passos concluídos: o breaker apenas LIBERTA a
// continuação (Allow devolve true); a não-reexecução é garantida pelo ledger/replay
// determinístico já existente (AOS-014/017), fora deste âmbito — o breaker nunca
// reexecuta passos.
//
// AVISO ~80%: antes do hard-trip, sinaliza a aproximação do limite
// (budget.warning_80pct), integrando-se com a exaustão graciosa (UX).
//
// EVENTOS append-only (cada transição do breaker, com o MOTIVO velocidade-vs-
// esgotamento e o estado de orçamento no momento): budget.breaker_tripped,
// budget.breaker_half_open, budget.breaker_closed, budget.warning_80pct. O replay
// ([Breaker.Rebuild]) reconstrói o estado do breaker a partir destes eventos.
//
// DETERMINISMO: relógio/IDs injectáveis, serialização estável (structs, sem mapas),
// sem time.Now/rand no caminho de decisão. OTel: span com os sinais do breaker +
// custo por span, via a porta agentruntime.Tracer zero-dep.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aos-ref/control-plane/budget"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento append-only do breaker (AOS-029). São o contrato auditável do
// ticket; o replay reconstrói a sequência de transições do breaker. Seguem a
// convenção dotted do resto do plano de controlo (cf. admission.admit_granted,
// run.state.transition, subagent.budget_reserved).
const (
	// EventBudgetBreakerTripped — o breaker disparou (closed/half-open → open), com o
	// MOTIVO (velocidade vs esgotamento) e o estado de orçamento no momento.
	EventBudgetBreakerTripped = "budget.breaker_tripped"
	// EventBudgetBreakerHalfOpen — open → half-open após o cooldown (reavaliação/
	// reabastecimento): retoma controlada permitida.
	EventBudgetBreakerHalfOpen = "budget.breaker_half_open"
	// EventBudgetBreakerClosed — half-open → closed após um probe recuperado (sinais
	// normalizados / orçamento reabastecido).
	EventBudgetBreakerClosed = "budget.breaker_closed"
	// EventBudgetWarning80Pct — aviso de aproximação do limite (~80% consumido),
	// EMITIDO antes do hard-trip. Não muda o estado do breaker.
	EventBudgetWarning80Pct = "budget.warning_80pct"
)

// DefaultBreakerNHI é a NHI por omissão do breaker nos eventos que emite.
const DefaultBreakerNHI = "nhi:control-plane/scheduler/budget-breaker"

// breakerStreamPrefix é o prefixo do stream (por árvore de run) onde o breaker
// projecta as suas transições append-only.
const breakerStreamPrefix = "budget-breaker/"

// breakerStepPrefix namespaceia o step_id de cada evento do breaker ("brk-N"), para
// que a idempotency_key (tree_id:brk-N) seja distinta de qualquer outra no stream.
const breakerStepPrefix = "brk-"

// opBreaker é o nome de operação do span do breaker.
const opBreaker = "budget_breaker"

// Atributos de span (OTel GenAI-style; reutilizam a convenção zero-dep).
const (
	attrBreakerTree      = "aos.breaker.tree_id"
	attrBreakerClass     = "aos.breaker.class"
	attrBreakerTenant    = "aos.breaker.tenant"
	attrBreakerState     = "aos.breaker.state"
	attrBreakerReason    = "aos.breaker.reason"
	attrBreakerVelTokens = "aos.breaker.velocity_tokens"
	attrBreakerVelCost   = "aos.breaker.velocity_cost_usd"
	attrBreakerAvailTok  = "aos.breaker.available_tokens"
	attrBreakerAvailCost = "aos.breaker.available_cost_usd"
	attrBreakerSampleTok = "aos.breaker.sample_tokens"
	attrBreakerSampleUSD = "aos.breaker.cost_usd"
	attrBreakerConsumed  = "aos.breaker.consumed_bps"
	attrBreakerAllowed   = "aos.breaker.allowed"
)

// BreakerState é um dos TRÊS estados do circuit breaker de orçamento. É uma string
// legível para que o valor persistido no evento de transição seja auto-descritivo no
// log append-only (ADR-001). NÃO confundir com os dez estados da máquina das TAREFAS
// (AOS-017): esta é a máquina PRÓPRIA do breaker.
type BreakerState string

const (
	// BreakerClosed — regime normal: o consumo é permitido ([Breaker.Allow] concede).
	BreakerClosed BreakerState = "closed"
	// BreakerOpen — disparado: o consumo é NEGADO (fail-closed, "na dúvida pára") até
	// ao cooldown. As tarefas em curso foram paradas em estado durável seguro.
	BreakerOpen BreakerState = "open"
	// BreakerHalfOpen — retoma controlada após o cooldown: permite um probe de
	// continuação; um sinal ainda anómalo re-dispara (→ open), um probe recuperado
	// fecha (→ closed).
	BreakerHalfOpen BreakerState = "half_open"
)

// TripReason é o MOTIVO de um disparo — o sinal que o causou. Gravado no evento
// budget.breaker_tripped para a auditoria distinguir velocidade de esgotamento.
type TripReason string

const (
	// ReasonVelocity — disparo por VELOCIDADE de consumo (tokens/$ por janela acima
	// do limiar).
	ReasonVelocity TripReason = "velocity"
	// ReasonExhaustion — disparo por ESGOTAMENTO do orçamento remanescente da árvore
	// (Available <= margem).
	ReasonExhaustion TripReason = "exhaustion"
	// ReasonReadError — disparo DEGRADADO fail-closed: a leitura do remanescente da
	// árvore (budget.Available) falhou, pelo que o breaker abre "na dúvida" (AOS-029-Q1).
	// O sinal de velocidade é independente da leitura; quando é ele a justificar o
	// disparo, o motivo gravado é [ReasonVelocity].
	ReasonReadError TripReason = "read_error"
)

// breakerTransition é um par ordenado (origem → destino) da tabela declarativa do
// breaker — a máquina é DADOS, não if/switch espalhado (testável por matriz,
// reconstruível por replay).
type breakerTransition struct {
	From BreakerState
	To   BreakerState
}

// validBreakerTransitions é a TABELA DECLARATIVA do breaker (4 pares):
//
//	closed    → open       (trip: velocidade OU esgotamento)
//	half_open → open       (re-trip: sinal ainda anómalo no probe)
//	open      → half_open  (cooldown decorrido: retoma controlada)
//	half_open → closed     (probe recuperado: sinais normalizados / reabastecido)
//
// Qualquer par ausente é INVÁLIDO (ex.: closed → half_open, open → closed directo).
var validBreakerTransitions = map[breakerTransition]struct{}{
	{BreakerClosed, BreakerOpen}:     {},
	{BreakerHalfOpen, BreakerOpen}:   {},
	{BreakerOpen, BreakerHalfOpen}:   {},
	{BreakerHalfOpen, BreakerClosed}: {},
}

// isValidBreakerTransition consulta a tabela declarativa.
func isValidBreakerTransition(from, to BreakerState) bool {
	_, ok := validBreakerTransitions[breakerTransition{from, to}]
	return ok
}

// Thresholds são os limiares CONFIGURÁVEIS do breaker (por classe/tenant — opções,
// nunca constantes hard-coded). Denominados nas duas dimensões que o AOS controla
// (tokens e custo em micro-USD), coerentes com budget.Amount.
type Thresholds struct {
	// VelocityTokens é o tecto de tokens consumidos por [Window]: ATINGIR o tecto já
	// dispara o trip por velocidade (comparação `>=`, fail-closed — na dúvida pára;
	// AOS-029-Q5). <=0 desliga o sinal de velocidade na dimensão tokens.
	VelocityTokens int64
	// VelocityCostMicroUSD é o tecto de custo (micro-USD) consumido por [Window]:
	// ATINGIR o tecto já dispara (comparação `>=`, fail-closed). <=0 desliga o sinal
	// de custo.
	VelocityCostMicroUSD int64
	// Window é a janela deslizante da medição de velocidade. <=0 desliga por completo
	// o sinal de velocidade (só esgotamento dispara).
	Window time.Duration
	// ExhaustionMargin é a margem de esgotamento: o breaker dispara quando o
	// remanescente da árvore (budget.Available) fica <= à margem em ALGUMA dimensão.
	// Zero ⇒ dispara só quando o remanescente chega a 0.
	ExhaustionMargin budget.Amount
	// WarnFraction é a fracção consumida (0..1) a que se emite o aviso ~80% ANTES do
	// hard-trip. <=0 ou >=1 desliga o aviso. Típico: 0.8.
	WarnFraction float64
	// Cooldown é o tempo (medido pelo relógio injectável a partir do trip) após o qual
	// open → half_open permite a retoma controlada. <=0 ⇒ nunca reabre automaticamente
	// (só reabre por reavaliação externa explícita).
	Cooldown time.Duration
}

// ThresholdProvider resolve os limiares por classe/tenant. É a PORTA que torna os
// limiares configuráveis (não constantes); o breaker recebe os limiares já
// resolvidos na construção. [StaticThresholdProvider] é a impl de referência.
type ThresholdProvider interface {
	// Thresholds devolve os limiares efectivos para uma classe/tenant.
	Thresholds(class, tenant string) Thresholds
}

// StaticThresholdProvider é a impl de referência determinística do
// [ThresholdProvider]: um default global com overrides por classe/tenant. Sem I/O
// nem relógio — segura para replay. À imagem do [StaticQuotaProvider] do AOS-027.
type StaticThresholdProvider struct {
	def      Thresholds
	byKey    map[string]Thresholds // chave = class + "\x00" + tenant
	byClass  map[string]Thresholds
	byTenant map[string]Thresholds
}

// NewStaticThresholdProvider constrói o provider com um default global.
func NewStaticThresholdProvider(def Thresholds) *StaticThresholdProvider {
	return &StaticThresholdProvider{
		def:      def,
		byKey:    make(map[string]Thresholds),
		byClass:  make(map[string]Thresholds),
		byTenant: make(map[string]Thresholds),
	}
}

// SetClassTenant fixa limiares para o par exacto (classe, tenant) — a chave mais
// específica.
func (p *StaticThresholdProvider) SetClassTenant(class, tenant string, t Thresholds) *StaticThresholdProvider {
	p.byKey[class+"\x00"+tenant] = t
	return p
}

// SetClass fixa limiares para uma classe (qualquer tenant sem override próprio).
func (p *StaticThresholdProvider) SetClass(class string, t Thresholds) *StaticThresholdProvider {
	p.byClass[class] = t
	return p
}

// SetTenant fixa limiares para um tenant (qualquer classe sem override próprio).
func (p *StaticThresholdProvider) SetTenant(tenant string, t Thresholds) *StaticThresholdProvider {
	p.byTenant[tenant] = t
	return p
}

// Thresholds resolve os limiares por especificidade: (classe,tenant) > classe >
// tenant > default.
func (p *StaticThresholdProvider) Thresholds(class, tenant string) Thresholds {
	if t, ok := p.byKey[class+"\x00"+tenant]; ok {
		return t
	}
	if t, ok := p.byClass[class]; ok {
		return t
	}
	if t, ok := p.byTenant[tenant]; ok {
		return t
	}
	return p.def
}

// TreeBudgetReader é o seam de LEITURA do orçamento hierárquico da árvore (AOS-026/
// 008). O breaker apenas LÊ o remanescente (Available) e o limite (Snapshot) do nó
// raiz — NUNCA reserva/consome nem reimplementa o orçamento. *budget.Budget
// satisfá-lo directamente.
type TreeBudgetReader interface {
	// Available devolve o remanescente do nó (Limit − Reserved − Committed).
	Available(nodeID string) (budget.Amount, error)
	// Snapshot devolve os contadores de cada nó (para ler o Limit do nó raiz — base
	// da fracção consumida do aviso ~80%).
	Snapshot() map[string]budget.NodeState
}

// Asserção de compatibilidade: o Budget real (AOS-026/008) satisfaz o seam de
// leitura sem adaptador.
var _ TreeBudgetReader = (*budget.Budget)(nil)

// TaskParker transita as tarefas em curso de uma árvore para um estado DURÁVEL
// seguro quando o breaker dispara. É a PORTA de integração com a máquina de estados
// do AOS-017 (a implementação canónica, [MachineParker], usa Machine.Pause). DEVE
// ser IDEMPOTENTE: parar uma árvore já parada/terminal é no-op — SEM duplicar
// efeitos (ADR-001). O breaker NÃO reimplementa a máquina das tarefas.
type TaskParker interface {
	// ParkTree transita as tarefas em curso da árvore para um estado durável seguro
	// (paused/waiting_on_human ou terminal controlado). Idempotente.
	ParkTree(ctx context.Context, treeID string) error
}

// Sentinelas de erro do breaker (comparáveis por errors.Is — fail-closed).
var (
	// ErrBreakerDeps — dependências obrigatórias em falta (log ou budget reader).
	ErrBreakerDeps = errors.New("scheduler: dependências do Breaker em falta (event log / budget reader)")
	// ErrBreakerEmptyTree — tree_id vazio (é o stream_id do breaker no Event Store).
	ErrBreakerEmptyTree = errors.New("scheduler: tree_id do Breaker vazio")
	// ErrBreakerUnknownState — o Rebuild leu um estado não canónico (log corrompido).
	ErrBreakerUnknownState = errors.New("scheduler: estado de breaker desconhecido no log")
)

// sample é uma observação de consumo na janela deslizante de velocidade.
type sample struct {
	tsNano int64
	amount budget.Amount
}

// Sample é o consumo observado desde a última observação (tokens e custo), a
// contabilizar na janela deslizante de velocidade.
type Sample = budget.Amount

// Breaker é o circuit breaker de orçamento de UMA árvore de run. É seguro para uso
// concorrente (um mutex serializa observação/decisão/transição, à imagem da Machine
// do AOS-017 — dono único, sem CAS distribuído). Construir com [NewBreaker].
type Breaker struct {
	log    EventLog
	budget TreeBudgetReader
	parker TaskParker

	treeID string
	node   string
	class  string
	tenant string
	th     Thresholds

	now      func() time.Time
	tracer   agentruntime.Tracer
	producer eventstore.Producer

	mu          sync.Mutex
	state       BreakerState
	trippedAt   time.Time
	warned      bool     // aviso ~80% já emitido no ciclo closed corrente (reset ao fechar)
	parkPending bool     // ParkTree falhou no trip: re-tentar enquanto open (AOS-029-Q3)
	window      []sample // janela deslizante de velocidade (transiente; não persistida)
	nEvents     uint64   // nº de eventos do breaker já persistidos (base do step_id "brk-N")
}

// BreakerOption configura o [Breaker].
type BreakerOption func(*Breaker)

// WithBreakerNode define o id do nó de orçamento (raiz da árvore) que o breaker lê
// para o sinal de esgotamento. Por omissão é o próprio tree_id.
func WithBreakerNode(nodeID string) BreakerOption {
	return func(b *Breaker) {
		if nodeID != "" {
			b.node = nodeID
		}
	}
}

// WithBreakerClassTenant define a classe/tenant do breaker (rótulos de auditoria e
// dimensão dos limiares configuráveis).
func WithBreakerClassTenant(class, tenant string) BreakerOption {
	return func(b *Breaker) {
		b.class = class
		b.tenant = tenant
	}
}

// WithBreakerParker liga a porta de paragem das tarefas (AOS-017). Sem ela, um trip
// muda o estado do breaker e nega o consumo, mas NÃO transita as tarefas (usar só
// quando a paragem é orquestrada por outra via).
func WithBreakerParker(p TaskParker) BreakerOption {
	return func(b *Breaker) {
		if p != nil {
			b.parker = p
		}
	}
}

// WithBreakerClock injecta o relógio de decisão (determinismo/replay: sem time.Now
// no caminho de decisão — à imagem do refill do AOS-027).
//
// COERÊNCIA ENTRE DONOS (AOS-029-Q6): o cooldown persiste o instante do trip como
// nanos ABSOLUTOS ([breakerPayload.TSUnixNano]) e [Breaker.Rebuild] deriva o
// trippedAt desse valor. Um dono NOVO (após crash) DEVE injectar um relógio na MESMA
// base temporal (wall-clock coerente) de quem escreveu o evento; um relógio
// puramente lógico/monotónico que reinicie entre donos torna a aritmética do cooldown
// incoerente (reabre cedo demais ou nunca). Em produção use time.Now (default) ou uma
// fonte NTP-sincronizada partilhada; relógios lógicos só são seguros dentro do MESMO
// dono/processo.
func WithBreakerClock(now func() time.Time) BreakerOption {
	return func(b *Breaker) {
		if now != nil {
			b.now = now
		}
	}
}

// WithBreakerTracer injecta a porta OTel (span com os sinais do breaker + custo por
// span). Zero-dep (reutiliza agentruntime.Tracer).
func WithBreakerTracer(t agentruntime.Tracer) BreakerOption {
	return func(b *Breaker) {
		if t != nil {
			b.tracer = t
		}
	}
}

// WithBreakerProducer injecta a identidade emissora (NHI) dos eventos.
func WithBreakerProducer(p eventstore.Producer) BreakerOption {
	return func(b *Breaker) {
		if p.NHIID != "" {
			b.producer = p
		}
	}
}

// NewBreaker constrói o breaker de uma árvore. log (Event Store, para as transições
// append-only) e treeBudget (leitura do remanescente/limite da árvore) são
// OBRIGATÓRIOS — a sua ausência é fail-closed. Os limiares vêm já resolvidos (use um
// [ThresholdProvider] para os derivar por classe/tenant). Começa em [BreakerClosed]
// (regime normal); para reconstruir após crash, chame [Breaker.Rebuild].
func NewBreaker(log EventLog, treeBudget TreeBudgetReader, treeID string, th Thresholds, opts ...BreakerOption) (*Breaker, error) {
	if log == nil || treeBudget == nil {
		return nil, ErrBreakerDeps
	}
	if treeID == "" {
		return nil, ErrBreakerEmptyTree
	}
	b := &Breaker{
		log:      log,
		budget:   treeBudget,
		treeID:   treeID,
		node:     treeID,
		th:       th,
		now:      time.Now,
		tracer:   agentruntime.NoopTracer{},
		producer: eventstore.Producer{NHIID: DefaultBreakerNHI},
		state:    BreakerClosed,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b, nil
}

// State devolve o estado corrente do breaker.
func (b *Breaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// TreeID devolve a árvore de run governada por este breaker (stream_id no ES).
func (b *Breaker) TreeID() string { return b.treeID }

// Allow é a decisão de CONTINUAÇÃO — o par fail-closed do consumo. Consultada ANTES
// de a árvore gastar orçamento:
//
//   - closed    → concede (true);
//   - half_open → concede um probe de continuação (true) — retoma controlada;
//   - open      → se o cooldown decorreu, transita open → half_open (retoma
//     controlada) e concede o probe; senão NEGA (false) — fail-closed, "na dúvida
//     pára". A não-reexecução de passos concluídos é do ledger/replay (o breaker só
//     liberta a continuação; NUNCA reexecuta).
//
// Um erro (ex.: Event Store recusa a transição open → half_open) degrada para NEGAÇÃO
// (false) — nunca se concede consumo por omissão.
func (b *Breaker) Allow(ctx context.Context) (bool, BreakerState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case BreakerClosed, BreakerHalfOpen:
		return true, b.state, nil
	case BreakerOpen:
		// Reconciliação de paragem (AOS-029-Q3): se um trip anterior não conseguiu parar
		// as tarefas (ParkTree falhou), re-tenta agora (idempotente). Enquanto não
		// parar, mantém-se open e NEGA — fail-closed, sem avançar para half_open.
		if err := b.reconcilePark(ctx); err != nil {
			return false, BreakerOpen, err
		}
		// Cooldown decorrido (relógio injectável, a contar do trip) ⇒ retoma controlada.
		if b.th.Cooldown > 0 && !b.now().Before(b.trippedAt.Add(b.th.Cooldown)) {
			// Span da transição open → half_open (AOS-029-C4): a reabertura por cooldown
			// também é uma transição de estado do breaker e leva os sinais no span.
			ctx, span := b.tracer.StartSpan(ctx, opBreaker)
			defer span.End()
			avail, _ := b.budget.Available(b.node) // best-effort para o payload
			limit := b.limitOf()
			span.SetAttribute(attrBreakerTree, b.treeID)
			span.SetAttribute(attrBreakerClass, b.class)
			span.SetAttribute(attrBreakerTenant, b.tenant)
			span.SetAttribute(attrBreakerReason, string(ReasonCooldown))
			span.SetAttribute(attrBreakerAvailTok, avail.Tokens)
			span.SetAttribute(attrBreakerAvailCost, microUSD(avail.CostMicroUSD))
			if err := b.emitTransition(ctx, BreakerOpen, BreakerHalfOpen, string(ReasonCooldown), avail, limit, budget.Amount{}); err != nil {
				span.SetAttribute(attrBreakerState, string(BreakerOpen))
				return false, BreakerOpen, err // fail-closed: não conseguiu abrir → nega
			}
			b.state = BreakerHalfOpen
			span.SetAttribute(attrBreakerState, string(BreakerHalfOpen))
			return true, BreakerHalfOpen, nil
		}
		return false, BreakerOpen, nil
	default:
		// Estado desconhecido (não deveria ocorrer): fail-closed.
		return false, b.state, nil
	}
}

// ReasonCooldown/ReasonRecovered são motivos das transições NÃO-trip (reabertura por
// cooldown e fecho por recuperação), gravados no evento para auditoria.
const (
	// ReasonCooldown — open → half_open por o cooldown ter decorrido.
	ReasonCooldown TripReason = "cooldown"
	// ReasonRecovered — half_open → closed por probe recuperado (sinais normais).
	ReasonRecovered TripReason = "recovered"
)

// Observe contabiliza o consumo `s` (tokens/$ desde a última observação) na janela
// deslizante, reavalia os DOIS sinais (velocidade E esgotamento) e conduz a máquina
// do breaker:
//
//   - closed/half_open + sinal anómalo ⇒ TRIP (→ open): emite budget.breaker_tripped
//     com o motivo, para as tarefas em estado durável seguro (idempotente) e passa a
//     NEGAR o consumo (fail-closed);
//   - half_open + sinais normais ⇒ RECUPERA (→ closed): probe bem-sucedido;
//   - closed + fracção consumida >= WarnFraction (e ainda sem trip) ⇒ AVISO ~80%
//     (budget.warning_80pct), UMA vez por ciclo, ANTES do hard-trip;
//   - open ⇒ apenas contabiliza a amostra (a reabertura é do [Breaker.Allow] por
//     cooldown).
//
// Devolve o estado resultante e, se disparou, o motivo. Determinística: relógio
// injectável, serialização estável.
func (b *Breaker) Observe(ctx context.Context, s Sample) (BreakerState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	ctx, span := b.tracer.StartSpan(ctx, opBreaker)
	defer span.End()

	now := b.now()
	nowNano := now.UnixNano()

	// Janela deslizante: acrescenta a amostra e poda as mais velhas do que Window.
	if !s.IsZero() {
		b.window = append(b.window, sample{tsNano: nowNano, amount: s})
	}
	velTokens, velCost := b.foldWindow(nowNano)

	avail, err := b.budget.Available(b.node)
	if err != nil {
		// Fail-closed (AOS-029-Q1): sem leitura do remanescente, na dúvida ABRE. O sinal
		// de VELOCIDADE é independente da leitura (já calculado acima) e pode ele próprio
		// justificar o disparo; caso contrário grava-se [ReasonReadError]. Um reader
		// persistentemente indisponível deixa de poder "cegar" o breaker.
		span.SetAttribute(attrBreakerState, string(b.state))
		switch b.state {
		case BreakerClosed, BreakerHalfOpen:
			reason := ReasonReadError
			if b.velocityTrips(velTokens, velCost) {
				reason = ReasonVelocity
			}
			if terr := b.trip(ctx, reason, budget.Amount{}, b.limitOf(), velTokens, velCost); terr != nil {
				return b.state, terr
			}
			span.SetAttribute(attrBreakerReason, string(reason))
			span.SetAttribute(attrBreakerState, string(BreakerOpen))
			return BreakerOpen, fmt.Errorf("scheduler: breaker lê remanescente da árvore (fail-closed, disparou): %w", err)
		case BreakerOpen:
			// Já open: reconcilia a paragem pendente (idempotente) e mantém-se open.
			if perr := b.reconcilePark(ctx); perr != nil {
				return BreakerOpen, perr
			}
			return BreakerOpen, fmt.Errorf("scheduler: breaker lê remanescente da árvore: %w", err)
		default:
			return b.state, fmt.Errorf("scheduler: breaker lê remanescente da árvore: %w", err)
		}
	}
	limit := b.limitOf()

	span.SetAttribute(attrBreakerTree, b.treeID)
	span.SetAttribute(attrBreakerClass, b.class)
	span.SetAttribute(attrBreakerTenant, b.tenant)
	span.SetAttribute(attrBreakerVelTokens, velTokens)
	span.SetAttribute(attrBreakerVelCost, microUSD(velCost))
	span.SetAttribute(attrBreakerAvailTok, avail.Tokens)
	span.SetAttribute(attrBreakerAvailCost, microUSD(avail.CostMicroUSD))
	span.SetAttribute(attrBreakerSampleTok, s.Tokens)
	span.SetAttribute(attrBreakerSampleUSD, microUSD(s.CostMicroUSD))

	reason, tripped := b.evaluateTrip(velTokens, velCost, avail, limit)

	switch b.state {
	case BreakerClosed, BreakerHalfOpen:
		// Aviso ~80% ANTES do hard-trip (AOS-029-Q2): avaliado INDEPENDENTEMENTE da
		// precedência do esgotamento e ANTES do trip, para PRECEDER sempre o disparo no
		// log append-only (warnSeq < tripSeq) mesmo na Observe em que ambos ocorrem, e
		// mesmo quando a margem de esgotamento é maior que (1-WarnFraction)*Limit. Uma
		// vez por ciclo closed (o `warned` reinicia ao fechar).
		if b.state == BreakerClosed && !b.warned {
			if bps, warn := b.warnFraction(avail, limit); warn {
				span.SetAttribute(attrBreakerConsumed, bps)
				if err := b.emitWarning(ctx, avail, limit, velTokens, velCost, bps); err != nil {
					span.SetAttribute(attrBreakerState, string(b.state))
					return b.state, err
				}
				b.warned = true
			}
		}
		if tripped {
			if err := b.trip(ctx, reason, avail, limit, velTokens, velCost); err != nil {
				span.SetAttribute(attrBreakerState, string(b.state))
				return b.state, err
			}
			span.SetAttribute(attrBreakerReason, string(reason))
			span.SetAttribute(attrBreakerState, string(BreakerOpen))
			return BreakerOpen, nil
		}
		if b.state == BreakerHalfOpen {
			// Probe recuperado (sem trip): fecha o breaker (retoma o regime normal).
			if err := b.emitTransition(ctx, BreakerHalfOpen, BreakerClosed, string(ReasonRecovered), avail, limit, velAmount(velTokens, velCost)); err != nil {
				span.SetAttribute(attrBreakerState, string(b.state))
				return b.state, err
			}
			b.state = BreakerClosed
			b.warned = false
			b.trippedAt = time.Time{}
			span.SetAttribute(attrBreakerState, string(BreakerClosed))
			return BreakerClosed, nil
		}
		span.SetAttribute(attrBreakerState, string(BreakerClosed))
		return BreakerClosed, nil
	case BreakerOpen:
		// Aberto: reconcilia a paragem pendente (AOS-029-Q3, idempotente) e contabiliza a
		// amostra; a reabertura é do Allow (cooldown).
		if err := b.reconcilePark(ctx); err != nil {
			span.SetAttribute(attrBreakerState, string(BreakerOpen))
			return BreakerOpen, err
		}
		span.SetAttribute(attrBreakerState, string(BreakerOpen))
		return BreakerOpen, nil
	default:
		span.SetAttribute(attrBreakerState, string(b.state))
		return b.state, nil
	}
}

// evaluateTrip decide se ALGUM dos dois sinais dispara e devolve o motivo. Ordem:
// ESGOTAMENTO tem precedência (é a condição mais grave — orçamento a zero), depois
// VELOCIDADE. Determinística (sem relógio; o nowNano da janela já foi aplicado).
func (b *Breaker) evaluateTrip(velTokens, velCost int64, avail, limit budget.Amount) (TripReason, bool) {
	if b.exhausted(avail, limit) {
		return ReasonExhaustion, true
	}
	if b.velocityTrips(velTokens, velCost) {
		return ReasonVelocity, true
	}
	return "", false
}

// exhausted indica esgotamento: remanescente <= margem em ALGUMA dimensão FINANCIADA.
// Uma dimensão cujo Limit do nó raiz é <=0 está INACTIVA (não financiada) e é IGNORADA
// (AOS-029-Q4/C1) — Available=0 numa dimensão não financiada é "zero permitido", não
// esgotamento, pelo que um orçamento de dimensão única (ex.: só tokens) não dispara
// esgotamento espúrio na 1ª Observe. Margem por omissão zero ⇒ dispara ao chegar a 0.
func (b *Breaker) exhausted(avail, limit budget.Amount) bool {
	m := b.th.ExhaustionMargin
	if limit.Tokens > 0 && avail.Tokens <= m.Tokens {
		return true
	}
	if limit.CostMicroUSD > 0 && avail.CostMicroUSD <= m.CostMicroUSD {
		return true
	}
	return false
}

// velocityTrips indica trip por VELOCIDADE: consumo por janela que ATINGE o limiar em
// ALGUMA dimensão (comparação `>=`, fail-closed — AOS-029-Q5). Independente da leitura
// de orçamento, pelo que um burst dispara mesmo com o reader indisponível (AOS-029-Q1).
// Limiar <=0 desliga essa dimensão; Window <=0 desliga o sinal por completo.
func (b *Breaker) velocityTrips(velTokens, velCost int64) bool {
	if b.th.Window <= 0 {
		return false
	}
	if b.th.VelocityTokens > 0 && velTokens >= b.th.VelocityTokens {
		return true
	}
	if b.th.VelocityCostMicroUSD > 0 && velCost >= b.th.VelocityCostMicroUSD {
		return true
	}
	return false
}

// warnFraction devolve a fracção consumida em basis points (0..10000) e se atinge o
// limiar de aviso ~80%. Consumido = Limit − Available; fracção = max entre as duas
// dimensões (a mais avançada). WarnFraction <=0 ou >=1 desliga o aviso.
func (b *Breaker) warnFraction(avail, limit budget.Amount) (int64, bool) {
	wf := b.th.WarnFraction
	if wf <= 0 || wf >= 1 {
		return 0, false
	}
	fracTok := dimFraction(limit.Tokens-avail.Tokens, limit.Tokens)
	fracCost := dimFraction(limit.CostMicroUSD-avail.CostMicroUSD, limit.CostMicroUSD)
	frac := fracTok
	if fracCost > frac {
		frac = fracCost
	}
	bps := int64(frac * 10000)
	return bps, frac >= wf
}

// dimFraction devolve consumed/limit clampado a [0,1]; limit<=0 ⇒ 0 (sem base).
func dimFraction(consumed, limit int64) float64 {
	if limit <= 0 {
		return 0
	}
	if consumed < 0 {
		consumed = 0
	}
	f := float64(consumed) / float64(limit)
	if f > 1 {
		f = 1
	}
	return f
}

// trip dispara o breaker: emite budget.breaker_tripped (durável, ANTES de mutar o
// estado — ordem de não-corrupção da Machine do AOS-017), passa a open, regista o
// instante do trip (base do cooldown) e transita as tarefas para estado durável
// seguro (idempotente, via [TaskParker]). Uma falha de emissão NÃO muda o estado
// (fail-closed: o chamador trata o erro como "parar"); uma falha de PARAGEM propaga o
// erro mas o breaker já está open (consumo negado) E marca [parkPending] para que
// [reconcilePark] re-tente a paragem (idempotentemente) em cada Allow/Observe/Reevaluate
// enquanto open (AOS-029-Q3) — as tarefas acabam paradas sem transições duplicadas.
func (b *Breaker) trip(ctx context.Context, reason TripReason, avail, limit budget.Amount, velTokens, velCost int64) error {
	from := b.state
	if err := b.emitTransition(ctx, from, BreakerOpen, string(reason), avail, limit, velAmount(velTokens, velCost)); err != nil {
		return err
	}
	b.state = BreakerOpen
	b.trippedAt = b.now()
	// Transita as tarefas em curso para estado durável seguro (AOS-017), idempotente.
	if b.parker != nil {
		if err := b.parker.ParkTree(ctx, b.treeID); err != nil {
			b.parkPending = true // fica pendente: reconcilePark re-tenta enquanto open
			return fmt.Errorf("scheduler: breaker parar tarefas da árvore %s: %w", b.treeID, err)
		}
		b.parkPending = false
	}
	return nil
}

// reconcilePark re-tenta a paragem das tarefas quando um trip anterior falhou o
// ParkTree ([parkPending]). Idempotente — ParkTree sobre tarefas já paradas/terminais
// é no-op (ADR-001), pelo que a re-tentativa NÃO duplica transições. Chamada sob b.mu
// nos caminhos que correm com o breaker open (Allow/Observe). Fecha o buraco de
// fail-open em que as tarefas ficariam em running apesar de o breaker estar open
// (AOS-029-Q3/C2).
func (b *Breaker) reconcilePark(ctx context.Context) error {
	if !b.parkPending || b.parker == nil {
		return nil
	}
	if err := b.parker.ParkTree(ctx, b.treeID); err != nil {
		return fmt.Errorf("scheduler: breaker re-parar tarefas da árvore %s: %w", b.treeID, err)
	}
	b.parkPending = false
	return nil
}

// Reevaluate força uma reavaliação da reabertura por cooldown SEM uma amostra de
// consumo (útil quando o dono do breaker faz poll periódico). Transita open →
// half_open se o cooldown decorreu; caso contrário no-op. É idempotente e
// fail-closed (um erro de emissão mantém open).
func (b *Breaker) Reevaluate(ctx context.Context) (BreakerState, error) {
	allowed, st, err := b.Allow(ctx)
	_ = allowed
	return st, err
}

// limitOf lê o Limit do nó raiz da árvore do Snapshot do orçamento (base da fracção
// consumida do aviso ~80%). Nó ausente ⇒ Amount zero (fracção indefinida ⇒ 0).
func (b *Breaker) limitOf() budget.Amount {
	snap := b.budget.Snapshot()
	if ns, ok := snap[b.node]; ok {
		return ns.Limit
	}
	return budget.Amount{}
}

// foldWindow poda a janela às amostras com idade < Window e soma o consumo activo
// nas duas dimensões (a velocidade por janela). Determinística: usa o nowNano dado.
func (b *Breaker) foldWindow(nowNano int64) (int64, int64) {
	if b.th.Window <= 0 {
		b.window = b.window[:0]
		return 0, 0
	}
	windowNano := b.th.Window.Nanoseconds()
	kept := b.window[:0]
	var tok, cost int64
	for _, s := range b.window {
		if nowNano-s.tsNano >= windowNano {
			continue // expirou a janela deslizante
		}
		kept = append(kept, s)
		tok += s.amount.Tokens
		cost += s.amount.CostMicroUSD
	}
	b.window = kept
	return tok, cost
}

// breakerPayload é o corpo serializado (estável, sem mapas) de cada evento do
// breaker — determinismo/replay. Inclui o MOTIVO e o estado de orçamento no momento.
type breakerPayload struct {
	Type              string `json:"type"`
	TreeID            string `json:"tree_id"`
	Node              string `json:"node,omitempty"`
	Class             string `json:"class,omitempty"`
	Tenant            string `json:"tenant,omitempty"`
	From              string `json:"from,omitempty"`
	To                string `json:"to,omitempty"`
	Reason            string `json:"reason,omitempty"`
	AvailTokens       int64  `json:"avail_tokens"`
	AvailCostMicroUSD int64  `json:"avail_cost_micro_usd"`
	LimitTokens       int64  `json:"limit_tokens"`
	LimitCostMicroUSD int64  `json:"limit_cost_micro_usd"`
	VelTokens         int64  `json:"vel_tokens,omitempty"`
	VelCostMicroUSD   int64  `json:"vel_cost_micro_usd,omitempty"`
	ConsumedBps       int64  `json:"consumed_bps,omitempty"`
	TSUnixNano        int64  `json:"ts_unix_nano"`
}

// emitTransition persiste uma transição do breaker (tripped/half_open/closed) como
// evento append-only ANTES de o chamador mutar o estado in-memory. O step_id
// "brk-N" é único por árvore (namespaced), pelo que a dedup global do ES não colide
// com outros streams. Devolve erro se o Event Store recusar (fail-closed).
func (b *Breaker) emitTransition(ctx context.Context, from, to BreakerState, reason string, avail, limit, vel budget.Amount) error {
	evType := transitionEventType(to)
	pl := breakerPayload{
		Type:              evType,
		TreeID:            b.treeID,
		Node:              b.node,
		Class:             b.class,
		Tenant:            b.tenant,
		From:              string(from),
		To:                string(to),
		Reason:            reason,
		AvailTokens:       avail.Tokens,
		AvailCostMicroUSD: avail.CostMicroUSD,
		LimitTokens:       limit.Tokens,
		LimitCostMicroUSD: limit.CostMicroUSD,
		VelTokens:         vel.Tokens,
		VelCostMicroUSD:   vel.CostMicroUSD,
		TSUnixNano:        b.now().UnixNano(),
	}
	return b.append(ctx, evType, pl)
}

// emitWarning persiste o aviso ~80% (budget.warning_80pct). Não muda o estado do
// breaker — precede o hard-trip.
func (b *Breaker) emitWarning(ctx context.Context, avail, limit budget.Amount, velTokens, velCost, bps int64) error {
	pl := breakerPayload{
		Type:              EventBudgetWarning80Pct,
		TreeID:            b.treeID,
		Node:              b.node,
		Class:             b.class,
		Tenant:            b.tenant,
		Reason:            "approaching_limit",
		AvailTokens:       avail.Tokens,
		AvailCostMicroUSD: avail.CostMicroUSD,
		LimitTokens:       limit.Tokens,
		LimitCostMicroUSD: limit.CostMicroUSD,
		VelTokens:         velTokens,
		VelCostMicroUSD:   velCost,
		ConsumedBps:       bps,
		TSUnixNano:        b.now().UnixNano(),
	}
	return b.append(ctx, EventBudgetWarning80Pct, pl)
}

// append serializa e escreve um evento do breaker no stream da árvore, com um
// step_id "brk-N" monotónico (idempotente por (tree_id, step_id) na dedup do ES).
func (b *Breaker) append(ctx context.Context, evType string, pl breakerPayload) error {
	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	streamID := breakerStreamPrefix + b.treeID
	stepID := breakerStepPrefix + strconv.FormatUint(b.nEvents+1, 10)
	if _, err := b.log.Append(ctx, streamID, eventstore.EventInput{
		Type:     evType,
		Payload:  raw,
		RunID:    streamID,
		StepID:   stepID,
		Producer: b.producer,
	}); err != nil {
		return err
	}
	b.nEvents++
	return nil
}

// transitionEventType mapeia o estado de destino para o tipo de evento append-only.
func transitionEventType(to BreakerState) string {
	switch to {
	case BreakerOpen:
		return EventBudgetBreakerTripped
	case BreakerHalfOpen:
		return EventBudgetBreakerHalfOpen
	case BreakerClosed:
		return EventBudgetBreakerClosed
	default:
		return "budget.breaker_" + string(to)
	}
}

// velAmount empacota a velocidade nas duas dimensões num budget.Amount (para o
// payload). Puro.
func velAmount(tok, cost int64) budget.Amount {
	return budget.Amount{Tokens: tok, CostMicroUSD: cost}
}

// microUSD converte micro-USD inteiro para USD (float, só para o atributo de span).
func microUSD(v int64) float64 { return float64(v) / 1_000_000.0 }

// BreakerRecord é uma transição do breaker reconstruída do log (para replay).
type BreakerRecord struct {
	Type        string
	From        BreakerState
	To          BreakerState
	Reason      string
	AvailTokens int64
	AvailCost   int64
	LimitTokens int64
	LimitCost   int64
	VelTokens   int64
	VelCost     int64
	ConsumedBps int64
	TSUnixNano  int64
	Seq         uint64
}

// Replay reconstrói fielmente a sequência de eventos do breaker da árvore (por ordem
// de seq). Prova de que a sequência se reconstrói do Event Store (determinismo/
// ADR-001).
func (b *Breaker) Replay(ctx context.Context) ([]BreakerRecord, error) {
	streamID := breakerStreamPrefix + b.treeID
	evs, err := b.log.Read(ctx, streamID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]BreakerRecord, 0, len(evs))
	for _, ev := range evs {
		var pl breakerPayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return nil, fmt.Errorf("scheduler: payload de breaker corrompido no seq %d: %w", ev.Seq, err)
		}
		out = append(out, BreakerRecord{
			Type:        pl.Type,
			From:        BreakerState(pl.From),
			To:          BreakerState(pl.To),
			Reason:      pl.Reason,
			AvailTokens: pl.AvailTokens,
			AvailCost:   pl.AvailCostMicroUSD,
			LimitTokens: pl.LimitTokens,
			LimitCost:   pl.LimitCostMicroUSD,
			VelTokens:   pl.VelTokens,
			VelCost:     pl.VelCostMicroUSD,
			ConsumedBps: pl.ConsumedBps,
			TSUnixNano:  pl.TSUnixNano,
			Seq:         ev.Seq,
		})
	}
	return out, nil
}

// Rebuild reconstrói o estado corrente do breaker RELENDO os seus eventos do Event
// Store: o estado é o `To` da última TRANSIÇÃO (o aviso ~80% não muda o estado). É a
// materialização de "o replay reconstrói o breaker" e o que o faz SOBREVIVER A CRASH
// — um dono novo constrói um Breaker sobre o mesmo cluster, chama Rebuild e continua
// a governar a continuação de onde o anterior parou (o estado durável open mantém o
// fail-closed do consumo). O `warned` reinicia a cada entrada em closed; o trippedAt
// (base do cooldown) vem do último tripped quando o estado corrente é open.
//
// Fail-closed: um evento com `To` fora dos três estados canónicos (log corrompido)
// aborta com [ErrBreakerUnknownState] em vez de adoptar um estado desconhecido.
func (b *Breaker) Rebuild(ctx context.Context) (BreakerState, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	streamID := breakerStreamPrefix + b.treeID
	evs, err := b.log.Read(ctx, streamID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			b.state = BreakerClosed
			b.warned = false
			b.trippedAt = time.Time{}
			b.nEvents = 0
			return BreakerClosed, nil
		}
		return "", err
	}

	state := BreakerClosed
	warned := false
	var trippedAt time.Time
	var count, maxN uint64
	for _, ev := range evs {
		if n, ok := parseBreakerStepID(ev.StepID); ok && n > maxN {
			maxN = n
		}
		count++
		var pl breakerPayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return "", fmt.Errorf("scheduler: rebuild de breaker descodifica seq=%d: %w", ev.Seq, err)
		}
		switch pl.Type {
		case EventBudgetWarning80Pct:
			warned = true // aviso do ciclo closed corrente
		case EventBudgetBreakerTripped:
			if !isKnownBreakerState(BreakerState(pl.To)) {
				return "", fmt.Errorf("%w: %q (seq=%d)", ErrBreakerUnknownState, pl.To, ev.Seq)
			}
			state = BreakerOpen
			warned = false
			trippedAt = time.Unix(0, pl.TSUnixNano)
		case EventBudgetBreakerHalfOpen:
			state = BreakerHalfOpen
		case EventBudgetBreakerClosed:
			state = BreakerClosed
			warned = false
			trippedAt = time.Time{}
		default:
			// Evento estranho ao contrato do breaker: ignora (tolerante a co-tenancy do
			// stream), mas ainda conta para o step_id monotónico.
		}
	}

	b.state = state
	b.warned = warned
	b.trippedAt = trippedAt
	b.nEvents = count
	if maxN > b.nEvents {
		b.nEvents = maxN
	}
	return b.state, nil
}

// parseBreakerStepID extrai o N de um step_id "brk-N".
func parseBreakerStepID(stepID string) (uint64, bool) {
	if !strings.HasPrefix(stepID, breakerStepPrefix) {
		return 0, false
	}
	n, err := strconv.ParseUint(stepID[len(breakerStepPrefix):], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// isKnownBreakerState defende o Rebuild contra estados forjados de um log corrompido.
func isKnownBreakerState(s BreakerState) bool {
	switch s {
	case BreakerClosed, BreakerOpen, BreakerHalfOpen:
		return true
	default:
		return false
	}
}
