// routing.go — ROTEAMENTO LEAST-LOADED / TOKEN-AWARE (AOS-033).
//
// A fonte (ADR-008) substitui o "roteamento round-robin CEGO" por
// least-loaded/token-aware com cost-aware model tiering. O round-robin ignora a
// carga real e o consumo de tokens, agravando a saturação (hotspots); este
// componente encaminha trabalho para o destino (worker/tier) MENOS CARREGADO em
// função de carga real E consumo de tokens — NUNCA por rotação cega.
//
// COMPOSIÇÃO (não reimplementa nada — à imagem do Dispatcher de AOS-032):
//   - CARGA por destino entra por uma PORTA [LoadSource] (filas por worker,
//     tokens em voo, latência recente), à imagem do [QuotaProvider] de AOS-027.
//     A impl de referência determinística [StaticLoadSource] fecha o contrato
//     entretanto — o Model Gateway (EPIC-06) e a frota (EPIC-10) NÃO são
//     implementados aqui.
//   - TOKEN-AWARENESS em duas camadas coerentes: (1) headroom LOCAL por destino
//     (CapacityTokens − TokensInFlight) filtra à cabeça um destino sem margem
//     para o custo estimado — determinístico, sem reserva; (2) integração
//     OPCIONAL com o admission control global (AOS-027, porta [AdmissionGate]):
//     quando injectado, o router RESERVA no destino escolhido pelo Admit e um
//     destino sem headroom GLOBAL é preterido (reutiliza o Admit — NÃO o
//     reimplementa).
//   - COST-AWARE MODEL TIERING coerente com AOS-031: quando NENHUM destino do
//     tier corrente tem margem e o trabalho é elegível, o router desce a escada
//     pela MESMA porta [ModelTierRouter] (impl [StaticModelTierRouter]) que o
//     downgrade de AOS-031 usa — a mesma escada de tiers, sem a redefinir. O
//     swap de tier é registado como VARIÂNCIA EXPLÍCITA no evento (nunca
//     silencioso).
//
// DETERMINISMO / REPLAY (ADR-001/010): os destinos são iterados por ORDEM DE ID
// (nunca ordem de mapa Go); o custo é aritmética INTEIRA (sem float, sem relógio
// na decisão); o tie-break é ESTÁVEL (custo asc → id do destino asc, um total
// order porque o id é único). O relógio (carimbos) e o gerador de decision IDs
// são INJECTÁVEIS. Mesmos sinais ⇒ MESMO destino e MESMOS bytes de evento — o
// replay reconstrói as decisões.
//
// EVENTOS append-only: work_routed (destino escolhido + carga no momento + custo
// + custo estimado + eventual variância de tier) por decisão; work_route_deferred
// quando nenhum destino tem margem (observável, NUNCA um descarte silencioso). O
// replay (ReplayRouting) reconstrói a sequência de decisões.
//
// OTel: um span por decisão (destino/carga/custo) via a porta agentruntime.Tracer
// zero-dep. Sem novas deps.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento append-only do roteamento (AOS-033). Contrato observável.
const (
	// EventWorkRouted marca o encaminhamento de um trabalho para o destino
	// MENOS carregado, com a carga no momento, o custo do destino e o custo
	// estimado do trabalho (e a eventual variância de tier).
	EventWorkRouted = "routing.work_routed"
	// EventWorkRouteDeferred marca um adiamento por ausência de destino com
	// margem (observável, nunca silencioso).
	EventWorkRouteDeferred = "routing.work_route_deferred"
)

// DefaultRouterNHI é a NHI por omissão do router nos eventos que emite.
const DefaultRouterNHI = "nhi:control-plane/scheduler/routing"

// routingStreamPrefix é o prefixo do stream de eventos de roteamento (um por
// instância). Permite vários routers independentes no mesmo Event Store.
const routingStreamPrefix = "routing/"

// opRouting é o nome de operação do span por decisão de roteamento.
const opRouting = "routing_decision"

// Atributos de span (OTel, porta zero-dep).
const (
	attrRouteItem       = "aos.routing.item_id"
	attrRouteTenant     = "aos.routing.tenant"
	attrRouteDest       = "aos.routing.destination"
	attrRouteTier       = "aos.routing.tier"
	attrRouteModel      = "aos.routing.model"
	attrRouteCostScore  = "aos.routing.cost_score"
	attrRouteQueueDepth = "aos.routing.queue_depth"
	attrRouteInFlight   = "aos.routing.tokens_in_flight"
	attrRouteLatencyMs  = "aos.routing.recent_latency_ms"
	attrRouteEstTokens  = "aos.routing.estimated_tokens"
	attrRouteHeadroom   = "aos.routing.headroom_tokens"
	attrRouteRouted     = "aos.routing.routed"
	attrRouteDeferred   = "aos.routing.deferred"
	attrRouteDowngraded = "aos.routing.downgraded"
	attrRouteFromTier   = "aos.routing.from_tier"
	attrRouteToTier     = "aos.routing.to_tier"
)

// ErrNoDestinations é devolvido quando a [LoadSource] não oferece nenhum destino
// candidato — sem destinos não há para onde rotear (fail-closed: erro explícito,
// não um destino inventado).
var ErrNoDestinations = errors.New("routing: sem destinos candidatos (LoadSource vazia)")

// ---------------------------------------------------------------------------
// Destination / DestinationLoad — o alvo do roteamento e a sua carga.
// ---------------------------------------------------------------------------

// Destination identifica um destino de roteamento (um worker/tier). O ID é a
// chave ESTÁVEL do tie-break determinístico (nunca a ordem de mapa Go). Tier e
// Model ligam-no à escada de tiers (coerente com AOS-031); Key liga-o ao
// admission control global por provider:model:region (AOS-027).
type Destination struct {
	// ID identifica o destino de forma única e estável (tie-break do least-loaded).
	ID string
	// Tier e Model são o degrau da escada de tiers e o modelo concreto (cost-aware
	// tiering coerente com AOS-031). Tier vazio ⇒ destino sem tiering.
	Tier  string
	Model string
	// Key é o provider:model:region do destino (headroom global via AOS-027 quando
	// há [AdmissionGate] injectado). Opcional.
	Key ProviderKey
}

// DestinationLoad são os sinais de carga de um destino num instante: a
// profundidade da fila, os tokens em voo (não consolidados), a latência recente e
// o tecto de tokens em voo. É a leitura da porta [LoadSource] que a função de
// custo consome. CapacityTokens dá a headroom LOCAL (token-aware sem reserva).
type DestinationLoad struct {
	// QueueDepth é o número de trabalhos em fila no destino (carga real).
	QueueDepth int64
	// TokensInFlight são os tokens já encaminhados e ainda em voo (consumo de
	// tokens) — o sinal central do token-aware.
	TokensInFlight int64
	// RecentLatencyMs é a latência recente observada no destino (ms).
	RecentLatencyMs int64
	// CapacityTokens é o tecto de tokens em voo do destino. > 0 activa a headroom
	// LOCAL: um destino cujo TokensInFlight + custo estimado excede a capacidade é
	// PRETERIDO (token-aware determinístico, sem reserva). 0 ⇒ sem tecto local (só
	// o admission global, se houver, limita).
	CapacityTokens int64
}

// headroomTokens devolve a margem LOCAL de tokens do destino (Capacity − InFlight),
// ou -1 quando não há tecto (CapacityTokens<=0: sem filtro local).
func (l DestinationLoad) headroomTokens() int64 {
	if l.CapacityTokens <= 0 {
		return -1
	}
	h := l.CapacityTokens - l.TokensInFlight
	if h < 0 {
		return 0
	}
	return h
}

// ---------------------------------------------------------------------------
// LoadSource / LoadReporter — as portas de carga.
// ---------------------------------------------------------------------------

// LoadSource é a PORTA que enumera os destinos candidatos e reporta a carga de
// cada um. O Model Gateway (EPIC-06) e a topologia da frota (EPIC-10) NÃO são
// implementados aqui; a impl de referência determinística [StaticLoadSource]
// fecha o contrato — à imagem do [QuotaProvider] de AOS-027.
type LoadSource interface {
	// Destinations enumera os destinos candidatos. A ordem é irrelevante: o router
	// ordena por ID (determinismo). Vazio ⇒ [ErrNoDestinations].
	Destinations(ctx context.Context) ([]Destination, error)
	// Load devolve a carga corrente de um destino. Um erro impede o roteamento para
	// esse destino (fail-closed: sem carga conhecida, não se escolhe às cegas).
	Load(ctx context.Context, destID string) (DestinationLoad, error)
}

// LoadReporter é a PORTA OPCIONAL por onde o router REFLECTE o trabalho
// encaminhado na carga do destino (tokens em voo) — para que decisões SUCESSIVAS
// vejam a carga ACTUALIZADA e o least-loaded equalize de facto a pressão (o que
// distingue o least-loaded do round-robin cego). [StaticLoadSource] implementa-a.
// Sem reporter, o router decide sobre a carga tal como a lê (útil em testes de
// decisão pura).
type LoadReporter interface {
	// Reserve marca `tokens` como em voo no destino (e incrementa a fila). Chamado
	// pelo router APÓS uma decisão de roteamento bem-sucedida.
	Reserve(ctx context.Context, destID string, tokens int64) error
}

// StaticLoadSource é a impl de referência determinística de [LoadSource] e
// [LoadReporter]: um mapa mutável de carga por destino, seguro para concorrência.
// Substitui o Model Gateway/frota em testes e no arranque. Sem I/O nem relógio —
// segura para replay. NÃO é o Model Gateway (EPIC-06) nem a frota (EPIC-10).
type StaticLoadSource struct {
	mu    sync.Mutex
	dests []Destination
	load  map[string]DestinationLoad
}

// NewStaticLoadSource constrói a impl de referência com os destinos dados. A
// carga inicial de cada destino é o zero value (ajuste com [StaticLoadSource.SetLoad]).
func NewStaticLoadSource(dests ...Destination) *StaticLoadSource {
	s := &StaticLoadSource{load: make(map[string]DestinationLoad, len(dests))}
	s.dests = make([]Destination, len(dests))
	copy(s.dests, dests)
	for _, d := range dests {
		if _, ok := s.load[d.ID]; !ok {
			s.load[d.ID] = DestinationLoad{}
		}
	}
	return s
}

// SetLoad fixa a carga de um destino (para preparar cenários de teste).
func (s *StaticLoadSource) SetLoad(destID string, load DestinationLoad) *StaticLoadSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.load[destID] = load
	return s
}

// Destinations implementa [LoadSource]: devolve uma cópia da lista de destinos.
func (s *StaticLoadSource) Destinations(_ context.Context) ([]Destination, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Destination, len(s.dests))
	copy(out, s.dests)
	return out, nil
}

// Load implementa [LoadSource].
func (s *StaticLoadSource) Load(_ context.Context, destID string) (DestinationLoad, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load[destID], nil
}

// Reserve implementa [LoadReporter]: reflecte o trabalho encaminhado (incrementa
// os tokens em voo e a profundidade da fila). É o mecanismo que faz o least-loaded
// EQUALIZAR a carga entre decisões sucessivas.
func (s *StaticLoadSource) Reserve(_ context.Context, destID string, tokens int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.load[destID]
	l.TokensInFlight += tokens
	l.QueueDepth++
	s.load[destID] = l
	return nil
}

// ---------------------------------------------------------------------------
// Função de custo — como a carga se colapsa num escalar comparável.
// ---------------------------------------------------------------------------

// RouteWeights pondera os sinais de carga na função de custo por omissão. Inteiros
// (determinismo/replay: sem float). Por omissão pesa a fila e os tokens em voo
// (least-loaded por carga real E consumo de tokens); a latência é opt-in.
type RouteWeights struct {
	// Queue pondera a profundidade da fila (carga real).
	Queue int64
	// Token pondera os tokens em voo (consumo de tokens) — o eixo do token-aware.
	Token int64
	// Latency pondera a latência recente (ms). 0 por omissão.
	Latency int64
}

// DefaultRouteWeights é a ponderação por omissão: fila e tokens contam por igual;
// a latência não conta salvo opt-in. É o "least-loaded por carga real e consumo de
// tokens" da fonte.
var DefaultRouteWeights = RouteWeights{Queue: 1, Token: 1, Latency: 0}

// LoadCost colapsa a carga de um destino num escalar comparável (menor = menos
// carregado). Determinística (inteiros, sem relógio). Injectável via [WithLoadCost];
// por omissão é a soma ponderada por [DefaultRouteWeights].
type LoadCost func(DestinationLoad) int64

// weightedCost devolve a função de custo por omissão para as ponderações dadas.
func weightedCost(w RouteWeights) LoadCost {
	return func(l DestinationLoad) int64 {
		return w.Queue*l.QueueDepth + w.Token*l.TokensInFlight + w.Latency*l.RecentLatencyMs
	}
}

// ---------------------------------------------------------------------------
// WorkRequest / RouteDecision — o pedido e o veredicto.
// ---------------------------------------------------------------------------

// WorkRequest é o trabalho a rotear. EstimatedTokens torna a decisão token-aware
// (o custo estimado é confrontado com a headroom por destino). CurrentTier/Model e
// TierEligible governam o cost-aware model tiering (coerente com AOS-031).
type WorkRequest struct {
	// ID identifica o trabalho (aparece no evento e no tie-break dos IDs de decisão).
	ID string
	// Tenant é a dimensão de partição (propagada ao Admit quando há AdmissionGate).
	Tenant string
	// EstimatedTokens é o custo/consumo estimado do trabalho (token-aware). < 1 é
	// promovido a 1.
	EstimatedTokens int64
	// CurrentTier e CurrentModel são o tier/modelo de partida (ponto de entrada do
	// cost-aware tiering). Vazio ⇒ roteamento PLANO (least-loaded sobre TODOS os
	// destinos, sem tiering).
	CurrentTier  string
	CurrentModel string
	// TierEligible autoriza o cost-aware tiering a DESCER de tier quando o tier
	// corrente não tem destino com margem. Coerente com o downgrade de AOS-031
	// (mesma escada/porta ModelTierRouter). Sem elegibilidade, o trabalho fica no
	// tier corrente (e adia se não houver margem).
	TierEligible bool
	// Class é a classe do trabalho (roteamento por classe na impl de produção;
	// opcional).
	Class string
	// DecisionID torna a decisão idempotente/reproduzível: se vazio, é gerado pelo
	// idGen injectável. Também é o RequestID passado ao Admit (AOS-027).
	DecisionID string
}

// RouteDecision é o veredicto de [Router.Route].
type RouteDecision struct {
	// Routed indica que o trabalho foi encaminhado para um destino.
	Routed bool
	// Deferred indica que NENHUM destino tinha margem (nem tier mais barato): o
	// trabalho é ADIADO (observável), NUNCA descartado silenciosamente.
	Deferred bool
	// DecisionID identifica a decisão (idempotência/replay; RequestID do Admit).
	DecisionID string
	// ItemID ecoa o WorkRequest.ID.
	ItemID string
	// Destination é o destino escolhido (zero value quando Deferred).
	Destination Destination
	// Load é a carga do destino escolhido no momento da decisão (observabilidade).
	Load DestinationLoad
	// CostScore é o custo do destino escolhido (o mínimo entre os elegíveis).
	CostScore int64
	// EstimatedTokens é o custo estimado do trabalho considerado.
	EstimatedTokens int64
	// HeadroomTokens é a margem LOCAL do destino escolhido (-1 se sem tecto local).
	HeadroomTokens int64
	// Downgraded/FromTier/ToTier/FromModel/ToModel descrevem a VARIÂNCIA de tier
	// (cost-aware tiering desceu de tier). Downgraded=false quando ficou no tier
	// corrente.
	Downgraded bool
	FromTier   string
	ToTier     string
	FromModel  string
	ToModel    string
	// ReservationID é a reserva de headroom global (AOS-027) quando há AdmissionGate;
	// vazio caso contrário.
	ReservationID string
	// RetryAfter, quando Deferred, é o adiamento aconselhado (do Admit, se houver).
	RetryAfter time.Duration
}

// ---------------------------------------------------------------------------
// Router — o selector least-loaded / token-aware.
// ---------------------------------------------------------------------------

// Router encaminha trabalho para o destino MENOS carregado (carga real + tokens),
// token-aware (custo estimado + headroom por destino) e cost-aware por tier
// (coerente com AOS-031), com decisões observáveis e reproduzíveis em replay.
// Seguro para concorrência (um mutex protege o contador de eventos). Construir com
// [NewRouter].
type Router struct {
	src        LoadSource
	reporter   LoadReporter
	tierRouter ModelTierRouter
	adm        AdmissionGate
	releaser   reservationReleaser
	cost       LoadCost
	now        func() time.Time
	idGen      func() string
	tracer     agentruntime.Tracer
	log        EventLog
	producer   eventstore.Producer
	name       string

	mu      sync.Mutex
	nEvents uint64
}

// RouterOption configura o [Router].
type RouterOption func(*Router)

// WithLoadReporter ACOPLA a porta que reflecte o trabalho encaminhado na carga do
// destino (tokens em voo), para que decisões sucessivas equalizem a carga (o que
// distingue o least-loaded do round-robin). Sem ela, o router decide sobre a carga
// tal como a lê.
func WithLoadReporter(r LoadReporter) RouterOption {
	return func(rt *Router) {
		if r != nil {
			rt.reporter = r
		}
	}
}

// WithTierRouter ACOPLA a porta de cost-aware model tiering (AOS-031, [ModelTierRouter]).
// Sem ela, o router não desce de tier (roteia no tier corrente ou adia). É a MESMA
// porta/escada do downgrade de AOS-031 — coerência garantida por reutilização.
func WithTierRouter(tr ModelTierRouter) RouterOption {
	return func(rt *Router) {
		if tr != nil {
			rt.tierRouter = tr
		}
	}
}

// WithRouteAdmission ACOPLA o admission control global (AOS-027, [AdmissionGate]):
// o router RESERVA no destino escolhido pelo Admit; um destino sem headroom GLOBAL
// é PRETERIDO. Reutiliza o Admit — NÃO o reimplementa. Se o gate também satisfizer
// [reservationReleaser] (o *Admission satisfá-lo), a reserva é LIBERTADA quando um
// erro ocorre após a concessão (sem fuga de headroom).
func WithRouteAdmission(gate AdmissionGate) RouterOption {
	return func(rt *Router) {
		if gate != nil {
			rt.adm = gate
			if rel, ok := gate.(reservationReleaser); ok {
				rt.releaser = rel
			}
		}
	}
}

// WithRouteWeights afina a ponderação da função de custo por omissão (fila, tokens,
// latência). Ignorada se [WithLoadCost] for usada.
func WithRouteWeights(w RouteWeights) RouterOption {
	return func(rt *Router) { rt.cost = weightedCost(w) }
}

// WithLoadCost injecta uma função de custo própria (determinística, inteira).
func WithLoadCost(c LoadCost) RouterOption {
	return func(rt *Router) {
		if c != nil {
			rt.cost = c
		}
	}
}

// WithRouteClock injecta o relógio dos carimbos dos eventos (determinismo/replay:
// sem time.Now na decisão).
func WithRouteClock(now func() time.Time) RouterOption {
	return func(rt *Router) {
		if now != nil {
			rt.now = now
		}
	}
}

// WithRouteIDGen injecta o gerador de decision IDs (determinismo/replay).
func WithRouteIDGen(gen func() string) RouterOption {
	return func(rt *Router) {
		if gen != nil {
			rt.idGen = gen
		}
	}
}

// WithRouteTracer injecta a porta OTel (span por decisão). Zero-dep.
func WithRouteTracer(t agentruntime.Tracer) RouterOption {
	return func(rt *Router) {
		if t != nil {
			rt.tracer = t
		}
	}
}

// WithRouteLog injecta o Event Store para os eventos de roteamento (append-only,
// observáveis, replay-fiéis). Sem log, o router decide na mesma mas não deixa
// rasto auditável.
func WithRouteLog(log EventLog) RouterOption {
	return func(rt *Router) {
		if log != nil {
			rt.log = log
		}
	}
}

// WithRouteProducer injecta a NHI emissora dos eventos.
func WithRouteProducer(p eventstore.Producer) RouterOption {
	return func(rt *Router) {
		if p.NHIID != "" {
			rt.producer = p
		}
	}
}

// WithRouteName nomeia a instância (usada no stream de eventos). Permite vários
// routers independentes no mesmo Event Store.
func WithRouteName(name string) RouterOption {
	return func(rt *Router) {
		if name != "" {
			rt.name = name
		}
	}
}

// NewRouter constrói o router. src (a porta de carga) é OBRIGATÓRIA — sem carga
// não há least-loaded (fail-closed). Os restantes colaboradores são opcionais.
//
// CONTRATO OPERACIONAL (produção) — duas configurações que NÃO são impostas na
// construção (para permitir testes de decisão pura e roteamento plano) mas que a
// frota de produção DEVE satisfazer, sob pena de invariantes silenciosamente
// desligadas:
//
//   - TOKEN-AWARE (porta local): a headroom LOCAL só filtra destinos quando
//     DestinationLoad.CapacityTokens > 0 (ver [DestinationLoad.headroomTokens]).
//     Sem qualquer tecto (CapacityTokens<=0) E sem [WithRouteAdmission], o custo
//     estimado alimenta apenas o Reserve/evento — NUNCA a elegibilidade: o router
//     encaminha independentemente do headroom. A invariante "destino sem headroom
//     para o custo estimado é preterido" exige CapacityTokens>0 OU o AdmissionGate.
//     A produção DEVE fornecer um dos dois.
//   - EQUALIZAÇÃO (feedback vivo): sem [WithLoadReporter] (nem uma LoadSource com
//     feedback externo a actualizar a carga entre decisões), a carga não evolui e
//     sinais idênticos encaminham TODOS os trabalhos para o destino de menor id
//     (hotspot), apesar dos restantes estarem livres. A equalização que distingue o
//     least-loaded do round-robin cego EXIGE feedback vivo — em produção, injecte
//     [WithLoadReporter] ou garanta uma LoadSource com carga viva.
func NewRouter(src LoadSource, opts ...RouterOption) (*Router, error) {
	if src == nil {
		return nil, fmt.Errorf("routing: LoadSource nil (least-loaded sem sinais de carga)")
	}
	rt := &Router{
		src:      src,
		cost:     weightedCost(DefaultRouteWeights),
		now:      time.Now,
		tracer:   agentruntime.NoopTracer{},
		producer: eventstore.Producer{NHIID: DefaultRouterNHI},
		name:     "default",
	}
	for _, opt := range opts {
		opt(rt)
	}
	if rt.idGen == nil {
		rt.idGen = defaultIDGen()
	}
	return rt, nil
}

// scored é um destino candidato com o seu custo (para ordenar por least-loaded).
type scored struct {
	dest Destination
	load DestinationLoad
	cost int64
}

// Route escolhe o destino MENOS carregado (carga real + tokens) com margem para o
// custo estimado (token-aware), descendo de tier quando elegível e necessário
// (cost-aware tiering coerente com AOS-031), emite work_routed (ou
// work_route_deferred) e reflecte a carga pela porta [LoadReporter] (se houver).
//
// Fluxo:
//  1. estima o custo (>=1); enumera e ORDENA os destinos por ID (determinismo);
//  2. selecciona no tier corrente o de MENOR custo com headroom LOCAL e, se há
//     [AdmissionGate], com headroom GLOBAL (reserva no Admit); tie-break estável;
//  3. se nenhum tem margem e o trabalho é elegível e há [ModelTierRouter], DESCE
//     um degrau da escada (variância explícita) e repete;
//  4. sem destino e sem tier mais barato ⇒ ADIA (observável), nunca descarta.
func (r *Router) Route(ctx context.Context, req WorkRequest) (RouteDecision, error) {
	ctx, span := r.tracer.StartSpan(ctx, opRouting)
	defer span.End()

	cost := req.EstimatedTokens
	if cost < 1 {
		cost = 1
	}
	decID := req.DecisionID
	if decID == "" {
		decID = r.idGen()
	}
	span.SetAttribute(attrRouteItem, req.ID)
	span.SetAttribute(attrRouteTenant, req.Tenant)
	span.SetAttribute(attrRouteEstTokens, cost)

	all, err := r.src.Destinations(ctx)
	if err != nil {
		return RouteDecision{}, fmt.Errorf("routing: enumerar destinos: %w", err)
	}
	if len(all) == 0 {
		return RouteDecision{}, ErrNoDestinations
	}
	// Ordena os destinos por ID: base do tie-break estável (nunca ordem de mapa Go).
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })

	// Escada de tiers: começa no tier corrente e desce enquanto elegível/necessário.
	targetTier := req.CurrentTier
	fromTier, fromModel := req.CurrentTier, req.CurrentModel
	toModel := req.CurrentModel
	downgraded := false
	var lastRetry time.Duration

	for {
		chosen, res, retry, cerr := r.selectInTier(ctx, all, targetTier, cost, req, decID)
		if cerr != nil {
			return RouteDecision{}, cerr
		}
		if chosen != nil {
			dec := RouteDecision{
				Routed:          true,
				DecisionID:      decID,
				ItemID:          req.ID,
				Destination:     chosen.dest,
				Load:            chosen.load,
				CostScore:       chosen.cost,
				EstimatedTokens: cost,
				HeadroomTokens:  chosen.load.headroomTokens(),
				Downgraded:      downgraded,
				ReservationID:   res,
			}
			if downgraded {
				dec.FromTier = fromTier
				dec.ToTier = targetTier
				dec.FromModel = fromModel
				dec.ToModel = chosen.dest.Model
			}
			if err := r.finishRouted(ctx, span, req, cost, dec); err != nil {
				// Erro após a decisão: liberta a reserva global (se houver) para não
				// vazar headroom, e propaga.
				if res != "" && r.releaser != nil {
					_ = r.releaser.Release(ctx, chosen.dest.Key, res, cost, 1)
				}
				return RouteDecision{}, err
			}
			return dec, nil
		}

		// Nenhum destino com margem no tier corrente. Se elegível e há porta de
		// tiering, DESCE um degrau (coerente com AOS-031) e tenta o tier mais barato.
		if retry > lastRetry {
			lastRetry = retry
		}
		if req.TierEligible && r.tierRouter != nil && targetTier != "" {
			td, terr := r.tierRouter.Cheaper(ctx, TierRouteRequest{
				Tenant:       req.Tenant,
				Class:        req.Class,
				CurrentTier:  targetTier,
				CurrentModel: toModel,
			})
			if terr != nil {
				return RouteDecision{}, fmt.Errorf("routing: descer de tier: %w", terr)
			}
			if td.Downgraded {
				downgraded = true
				targetTier = td.ToTier
				toModel = td.ToModel
				continue
			}
		}
		// Sem destino e sem tier mais barato: ADIA (observável, nunca silencioso).
		dec := RouteDecision{
			Deferred:        true,
			DecisionID:      decID,
			ItemID:          req.ID,
			EstimatedTokens: cost,
			Downgraded:      downgraded,
			RetryAfter:      lastRetry,
		}
		if downgraded {
			dec.FromTier = fromTier
			dec.ToTier = targetTier
			dec.FromModel = fromModel
		}
		if err := r.emitDeferred(ctx, span, req, cost, dec); err != nil {
			return RouteDecision{}, err
		}
		return dec, nil
	}
}

// selectInTier escolhe, no tier dado (ou em TODOS se tier==""), o destino de MENOR
// custo com headroom LOCAL para `cost` e — se há [AdmissionGate] — com headroom
// GLOBAL (reservando no Admit). Devolve o escolhido (ou nil), o reservationID
// (vazio sem admission), e o retry_after aconselhado quando nada coube. Tie-break
// estável: custo asc → id asc (os destinos já vêm ordenados por id).
func (r *Router) selectInTier(ctx context.Context, all []Destination, tier string, cost int64, req WorkRequest, decID string) (*scored, string, time.Duration, error) {
	// Candidatos elegíveis pela headroom LOCAL, no tier pedido.
	cands := make([]scored, 0, len(all))
	for _, d := range all {
		if tier != "" && d.Tier != tier {
			continue
		}
		load, err := r.src.Load(ctx, d.ID)
		if err != nil {
			return nil, "", 0, fmt.Errorf("routing: carga do destino %q: %w", d.ID, err)
		}
		// Token-aware LOCAL: headroom = Capacity − InFlight; sem margem ⇒ preterido.
		if h := load.headroomTokens(); h >= 0 && cost > h {
			continue
		}
		cands = append(cands, scored{dest: d, load: load, cost: r.cost(load)})
	}
	if len(cands) == 0 {
		return nil, "", 0, nil
	}
	// Ordena por custo asc; empate desfeito pelo id do destino (asc) — total order
	// estável (a lista já está por id, mas fixamo-lo explicitamente no comparador).
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].cost != cands[j].cost {
			return cands[i].cost < cands[j].cost
		}
		return cands[i].dest.ID < cands[j].dest.ID
	})

	// Sem admission global: o de menor custo (com headroom local) vence.
	if r.adm == nil {
		best := cands[0]
		return &best, "", 0, nil
	}

	// Com admission (AOS-027): tenta reservar por ordem de menor custo; o primeiro
	// que o Admit CONCEDE vence (headroom global). Um defer/reject ⇒ destino
	// preterido, tenta o seguinte. O Admit não reserva num defer, logo não há fuga.
	var maxRetry time.Duration
	for i := range cands {
		c := cands[i]
		ar, err := r.adm.Admit(ctx, AdmitRequest{
			Key:             c.dest.Key,
			Tenant:          req.Tenant,
			EstimatedTokens: cost,
			RequestID:       decID,
		})
		if err != nil {
			return nil, "", 0, fmt.Errorf("routing: admissão do destino %q: %w", c.dest.ID, err)
		}
		if ar.Granted {
			return &c, ar.ReservationID, 0, nil
		}
		if ar.RetryAfter > maxRetry {
			maxRetry = ar.RetryAfter
		}
	}
	return nil, "", maxRetry, nil
}

// finishRouted preenche o span, emite work_routed e SÓ ENTÃO reflecte a carga
// (LoadReporter).
//
// ORDEM (emit → reflect): o evento é escrito ANTES de a carga LOCAL ser reflectida.
// Assim, se o emit falhar (erro do store), a reserva local NÃO chega a acontecer e
// o ramo de erro de Route liberta a reserva GLOBAL — rollback SIMÉTRICO, sem carga
// fantasma. Reflectir a carga primeiro deixaria +cost tokens/+1 fila permanentes num
// destino quando o emit falhasse (a admissão era libertada mas o Reserve local não),
// enviesando o least-loaded seguinte. Note-se que dec.Load é a carga NO MOMENTO da
// decisão (capturada antes de qualquer Reserve), pelo que a ordem não altera os bytes
// do evento — o determinismo/replay mantém-se.
func (r *Router) finishRouted(ctx context.Context, span agentruntime.Span, req WorkRequest, cost int64, dec RouteDecision) error {
	span.SetAttribute(attrRouteRouted, true)
	span.SetAttribute(attrRouteDest, dec.Destination.ID)
	span.SetAttribute(attrRouteTier, dec.Destination.Tier)
	span.SetAttribute(attrRouteModel, dec.Destination.Model)
	span.SetAttribute(attrRouteCostScore, dec.CostScore)
	span.SetAttribute(attrRouteQueueDepth, dec.Load.QueueDepth)
	span.SetAttribute(attrRouteInFlight, dec.Load.TokensInFlight)
	span.SetAttribute(attrRouteLatencyMs, dec.Load.RecentLatencyMs)
	span.SetAttribute(attrRouteHeadroom, dec.HeadroomTokens)
	span.SetAttribute(attrRouteDowngraded, dec.Downgraded)
	if dec.Downgraded {
		span.SetAttribute(attrRouteFromTier, dec.FromTier)
		span.SetAttribute(attrRouteToTier, dec.ToTier)
	}
	if err := r.emit(ctx, EventWorkRouted, req, cost, dec); err != nil {
		return err
	}
	// Só depois de o evento estar durável reflectimos o trabalho encaminhado na carga
	// do destino (tokens em voo), para que a PRÓXIMA decisão veja a carga actualizada
	// — o que equaliza a pressão.
	if r.reporter != nil {
		if err := r.reporter.Reserve(ctx, dec.Destination.ID, cost); err != nil {
			return fmt.Errorf("routing: reflectir carga no destino %q: %w", dec.Destination.ID, err)
		}
	}
	return nil
}

// emitDeferred preenche o span e emite work_route_deferred (adiamento observável).
func (r *Router) emitDeferred(ctx context.Context, span agentruntime.Span, req WorkRequest, cost int64, dec RouteDecision) error {
	span.SetAttribute(attrRouteRouted, false)
	span.SetAttribute(attrRouteDeferred, true)
	span.SetAttribute(attrRouteEstTokens, cost)
	return r.emit(ctx, EventWorkRouteDeferred, req, cost, dec)
}

// routingPayload é o corpo serializado (estável, sem mapas) dos eventos de
// roteamento — determinismo/replay. Carrega o destino escolhido, a CARGA no
// momento, o CUSTO do destino e o custo estimado do trabalho, e a eventual
// variância de tier.
type routingPayload struct {
	Type            string `json:"type"`
	DecisionID      string `json:"decision_id"`
	ItemID          string `json:"item_id"`
	Tenant          string `json:"tenant,omitempty"`
	Class           string `json:"class,omitempty"`
	Destination     string `json:"destination,omitempty"`
	Tier            string `json:"tier,omitempty"`
	Model           string `json:"model,omitempty"`
	QueueDepth      int64  `json:"queue_depth"`
	TokensInFlight  int64  `json:"tokens_in_flight"`
	RecentLatencyMs int64  `json:"recent_latency_ms,omitempty"`
	CostScore       int64  `json:"cost_score"`
	EstimatedTokens int64  `json:"estimated_tokens"`
	HeadroomTokens  int64  `json:"headroom_tokens"`
	Deferred        bool   `json:"deferred,omitempty"`
	Downgraded      bool   `json:"downgraded,omitempty"`
	FromTier        string `json:"from_tier,omitempty"`
	ToTier          string `json:"to_tier,omitempty"`
	FromModel       string `json:"from_model,omitempty"`
	ToModel         string `json:"to_model,omitempty"`
	ReservationID   string `json:"reservation_id,omitempty"`
	RetryAfterMs    int64  `json:"retry_after_ms,omitempty"`
	TSUnixNano      int64  `json:"ts_unix_nano"`
}

// emit serializa e escreve um evento de roteamento no stream da instância, com
// step_id "route-N" monotónico (idempotente por (run_id, step_id) na dedup do
// Event Store). No-op sem log. Fail-closed: um erro do store propaga.
func (r *Router) emit(ctx context.Context, evType string, req WorkRequest, cost int64, dec RouteDecision) error {
	if r.log == nil {
		return nil
	}
	pl := routingPayload{
		Type:            evType,
		DecisionID:      dec.DecisionID,
		ItemID:          req.ID,
		Tenant:          req.Tenant,
		Class:           req.Class,
		Destination:     dec.Destination.ID,
		Tier:            dec.Destination.Tier,
		Model:           dec.Destination.Model,
		QueueDepth:      dec.Load.QueueDepth,
		TokensInFlight:  dec.Load.TokensInFlight,
		RecentLatencyMs: dec.Load.RecentLatencyMs,
		CostScore:       dec.CostScore,
		EstimatedTokens: cost,
		HeadroomTokens:  dec.HeadroomTokens,
		Deferred:        dec.Deferred,
		Downgraded:      dec.Downgraded,
		FromTier:        dec.FromTier,
		ToTier:          dec.ToTier,
		FromModel:       dec.FromModel,
		ToModel:         dec.ToModel,
		ReservationID:   dec.ReservationID,
		RetryAfterMs:    dec.RetryAfter.Milliseconds(),
		TSUnixNano:      r.now().UnixNano(),
	}
	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.nEvents++
	stepID := "route-" + strconv.FormatUint(r.nEvents, 10)
	r.mu.Unlock()

	streamID := r.RoutingStreamID()
	_, err = r.log.Append(ctx, streamID, eventstore.EventInput{
		Type:     evType,
		Payload:  raw,
		RunID:    streamID,
		StepID:   stepID,
		Producer: r.producer,
	})
	return err
}

// RoutingStreamID é o stream de eventos de roteamento desta instância.
func (r *Router) RoutingStreamID() string { return routingStreamPrefix + r.name }

// RouteRecord é uma decisão de roteamento reconstruída do log (para replay).
// Inclui a carga no momento e a variância de tier — a prova de que cada decisão se
// reconstrói do Event Store (determinismo/ADR-001/010).
type RouteRecord struct {
	Type            string
	DecisionID      string
	ItemID          string
	Tenant          string
	Destination     string
	Tier            string
	Model           string
	QueueDepth      int64
	TokensInFlight  int64
	CostScore       int64
	EstimatedTokens int64
	Deferred        bool
	Downgraded      bool
	FromTier        string
	ToTier          string
	Seq             uint64
}

// ReplayRouting reconstrói fielmente a sequência de decisões de roteamento da
// instância a partir do Event Store (append-only, por ordem de seq). Sem log,
// devolve nil.
func (r *Router) ReplayRouting(ctx context.Context) ([]RouteRecord, error) {
	if r.log == nil {
		return nil, nil
	}
	streamID := r.RoutingStreamID()
	evs, err := r.log.Read(ctx, streamID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]RouteRecord, 0, len(evs))
	for _, ev := range evs {
		var pl routingPayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return nil, fmt.Errorf("routing: payload de roteamento corrompido no seq %d: %w", ev.Seq, err)
		}
		out = append(out, RouteRecord{
			Type:            pl.Type,
			DecisionID:      pl.DecisionID,
			ItemID:          pl.ItemID,
			Tenant:          pl.Tenant,
			Destination:     pl.Destination,
			Tier:            pl.Tier,
			Model:           pl.Model,
			QueueDepth:      pl.QueueDepth,
			TokensInFlight:  pl.TokensInFlight,
			CostScore:       pl.CostScore,
			EstimatedTokens: pl.EstimatedTokens,
			Deferred:        pl.Deferred,
			Downgraded:      pl.Downgraded,
			FromTier:        pl.FromTier,
			ToTier:          pl.ToTier,
			Seq:             ev.Seq,
		})
	}
	return out, nil
}

// Verificação estática: a impl de referência satisfaz as portas.
var (
	_ LoadSource   = (*StaticLoadSource)(nil)
	_ LoadReporter = (*StaticLoadSource)(nil)
)
