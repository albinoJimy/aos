// degradation.go — EXECUTOR de degradação graciosa (AOS-031).
//
// A política declarativa de backpressure (AOS-030, policy.go) SELECCIONA uma
// acção de degradação quando uma fila satura; ESTE ficheiro EXECUTA a acção
// seleccionada. A cadeia canónica da fonte (ADR-008) é, por ORDEM DE PREFERÊNCIA:
//
//	shed → defer → downgrade → reject
//
// Cada degrau tem semântica precisa e é um EVENTO append-only (gatilho + acção +
// efeito), reconstruível por replay:
//
//   - SHED: descarta trabalho de BAIXA prioridade/opcional de forma controlada,
//     com RAZÃO. NUNCA descarta trabalho CRÍTICO silenciosamente — um shed de
//     trabalho crítico é ERRO fail-closed ([ErrCannotShedCritical]): o trabalho
//     NÃO é descartado. (work_shed)
//   - DEFER: adia trabalho admissível preservando-o (retry_after), integrando o
//     admission control (AOS-027) e as filas (AOS-030) — não perde o trabalho.
//     (work_deferred)
//   - DOWNGRADE: encaminha para um modelo/tier mais BARATO via a porta
//     [ModelTierRouter] (impl de referência [StaticModelTierRouter], à imagem do
//     QuotaProvider de AOS-027), registando o swap como VARIÂNCIA EXPLÍCITA
//     (tier_antigo→tier_novo) — NUNCA silencioso. A variância entra no log para o
//     replay ser fiel (ADR-010/AOS-016). (model_downgraded)
//     FRONTEIRA DE PORTA (AOS-016): model_downgraded é o registo do PORQUÊ do swap;
//     o swap torna-se facto do MANIFESTO quando o runtime passa a usar o novo tier,
//     e é aí que o motor de replay de AOS-016 o detecta como divergência de modelo
//     (Reason="model", via ModelConfig). Este executor NÃO reconcilia o manifesto
//     (é o Model Gateway/runtime que o faz) — apenas emite a variância auditável.
//   - REJECT: último recurso; devolve um erro CLARO e accionável
//     ([ErrWorkRejected]); FAIL-CLOSED para acções irreversíveis (o efeito
//     irreversível NÃO ocorre). (work_rejected)
//
// REVERSIBILIDADE: ao NORMALIZAR a carga (descer o sinal de saturação), as
// degradações REVERSÍVEIS revertem — o downgrade restaura o tier
// ([Degrader.Normalize] emite tier_restored). Shed e reject NÃO são reversíveis
// (o trabalho já foi descartado/recusado; fail-closed, documentado). A variância
// do downgrade (model_downgraded) permanece no log MESMO após a reversão — o
// swap é um facto do manifesto, para o replay ser fiel.
//
// ESTE ficheiro NÃO reimplementa a política (AOS-030) nem o admission (AOS-027);
// NÃO implementa o Model Gateway (EPIC-06 — é a porta [ModelTierRouter]).
//
// DETERMINISMO: relógio/IDs injectáveis, iteração ordenada, serialização estável
// (structs, sem mapas) — sem time.Now/rand nas decisões. OTel: um span por acção,
// via a porta agentruntime.Tracer zero-dep.
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

// Tipos de evento append-only da degradação graciosa (AOS-031). Cada acção da
// cadeia shed→defer→downgrade→reject é um evento observável; tier_restored regista
// a reversão ao normalizar a carga.
const (
	// EventWorkShed marca o descarte controlado de trabalho opcional/baixa
	// prioridade (com razão). Nunca emitido para trabalho crítico.
	EventWorkShed = "degradation.work_shed"
	// EventWorkDeferred marca o adiamento de trabalho admissível (retry_after),
	// preservando-o.
	EventWorkDeferred = "degradation.work_deferred"
	// EventModelDowngraded marca o swap para um tier mais barato como VARIÂNCIA
	// EXPLÍCITA (tier_antigo→tier_novo) — nunca silencioso; entra no log para o
	// replay ser fiel (ADR-010).
	EventModelDowngraded = "degradation.model_downgraded"
	// EventWorkRejected marca a rejeição de último recurso (erro accionável;
	// fail-closed para irreversíveis).
	EventWorkRejected = "degradation.work_rejected"
	// EventTierRestored marca a REVERSÃO de um downgrade ao normalizar a carga
	// (tier_novo→tier_antigo). A variância model_downgraded permanece no log.
	EventTierRestored = "degradation.tier_restored"
)

// DefaultDegraderNHI é a NHI por omissão do executor de degradação nos eventos que
// emite.
const DefaultDegraderNHI = "nhi:control-plane/scheduler/degradation"

// degradationStreamPrefix é o prefixo do stream de eventos de degradação (um por
// instância). Permite vários executores independentes no mesmo Event Store.
const degradationStreamPrefix = "degradation/"

// opDegradation é o nome de operação do span por acção de degradação.
const opDegradation = "degradation_execute"

// Atributos de span (OTel, porta zero-dep).
const (
	attrDegAction    = "aos.degradation.action"
	attrDegItem      = "aos.degradation.item_id"
	attrDegTenant    = "aos.degradation.tenant"
	attrDegPriority  = "aos.degradation.priority"
	attrDegCritical  = "aos.degradation.critical"
	attrDegApplied   = "aos.degradation.applied"
	attrDegReason    = "aos.degradation.reason"
	attrDegRetryMs   = "aos.degradation.retry_after_ms"
	attrDegFromTier  = "aos.degradation.from_tier"
	attrDegToTier    = "aos.degradation.to_tier"
	attrDegReversibl = "aos.degradation.reversible"
)

// Sentinelas de erro do executor (comparáveis por errors.Is — fail-closed).
var (
	// ErrCannotShedCritical é devolvido quando a acção seleccionada é SHED mas o
	// trabalho é CRÍTICO. Fail-closed: o trabalho NÃO é descartado (nunca um
	// descarte silencioso de crítico). O chamador deve escalar para o degrau
	// seguinte da cadeia (defer/downgrade/reject) — ver [Degrader.ExecuteChain].
	ErrCannotShedCritical = errors.New("scheduler: shed recusado — trabalho crítico nunca é descartado (fail-closed)")
	// ErrWorkRejected é devolvido pela acção REJECT: um erro CLARO e accionável ao
	// chamador. Para trabalho IRREVERSÍVEL, a rejeição é fail-closed (terminal: o
	// efeito irreversível NÃO ocorre e o trabalho não é ressuscitado ao normalizar).
	ErrWorkRejected = errors.New("scheduler: trabalho rejeitado — sistema saturado, sem degrau de degradação aplicável")
	// ErrUnknownDegradationAction sinaliza uma acção não reconhecida (fail-closed:
	// na dúvida, não se executa nenhuma degradação).
	ErrUnknownDegradationAction = errors.New("scheduler: acção de degradação desconhecida")
	// ErrChainExhausted é devolvido por [Degrader.ExecuteChain] quando NENHUM
	// degrau da ordem de preferência foi aplicável (envolve ErrWorkRejected: o
	// resultado terminal da cadeia é sempre a rejeição fail-closed).
	ErrChainExhausted = errors.New("scheduler: cadeia de degradação esgotada sem degrau aplicável")
	// ErrCannotShedIrreversible é devolvido quando a acção seleccionada é SHED mas o
	// trabalho é IRREVERSÍVEL. Fail-closed: trabalho irreversível NUNCA é descartado
	// em silêncio — deve escalar para o degrau seguinte e, em último recurso, ser
	// REJEITADO (erro accionável), nunca perdido por shed.
	ErrCannotShedIrreversible = errors.New("scheduler: shed recusado — trabalho irreversível nunca é descartado (fail-closed)")
	// ErrCannotShedNonOptional é devolvido quando a acção seleccionada é SHED mas o
	// item NÃO está marcado como OPCIONAL ([DegradationItem.Optional]). O default é
	// FAIL-CLOSED: só é descartável trabalho PROVADAMENTE opcional — nunca se
	// descarta por omissão do flag (a ausência de Critical NÃO autoriza o descarte).
	ErrCannotShedNonOptional = errors.New("scheduler: shed recusado — trabalho não marcado como opcional (fail-closed)")
	// ErrMissingReason é devolvido por qualquer acção de degradação cujo gatilho não
	// traga RAZÃO ([DegradationTrigger.Reason] vazia). Fail-closed: sem razão não há
	// descarte/adiamento/downgrade/rejeição — nenhuma degradação entra no log sem
	// razão auditável (contrato gatilho+razão).
	ErrMissingReason = errors.New("scheduler: degradação recusada — gatilho sem razão (fail-closed)")
	// ErrDegradationNotApplied é devolvido por [Degrader.Execute] quando a acção
	// seleccionada pela política NÃO teve efeito (ex.: downgrade de item já no tier
	// mais barato, ou downgrade de irreversível): a carga NÃO foi aliviada. É um
	// sinal ACCIONÁVEL para o chamador ESCALAR (não um sucesso silencioso) — envolve
	// o resultado com Applied=false.
	ErrDegradationNotApplied = errors.New("scheduler: acção de degradação seleccionada não teve efeito — escalar")
)

// DefaultPreferenceOrder é a ORDEM DE PREFERÊNCIA canónica da fonte (ADR-008):
// shed → defer → downgrade → reject. É reutilizada por [Degrader.ExecuteChain]
// quando o chamador não fornece uma ordem própria. A ordem é CONFIGURÁVEL por
// classe/tenant (o chamador passa a sua), coerente com a política de AOS-030 — o
// executor NÃO redefine a política, apenas respeita a ordem que recebe.
var DefaultPreferenceOrder = []DegradationAction{ActionShed, ActionDefer, ActionDowngrade, ActionReject}

// DegradationItem é a unidade de trabalho sujeita a degradação. Estende o
// [WorkItem] das filas (AOS-030) com a classificação de que o executor precisa: a
// criticidade (guard do shed), a reversibilidade (guard do reject fail-closed) e o
// tier corrente (alvo do downgrade).
type DegradationItem struct {
	// ID identifica o trabalho (chave da reversibilidade dos downgrades activos).
	ID string
	// Tenant e Priority são as dimensões de partição (tenant:priority), coerentes
	// com as filas de AOS-030.
	Tenant   string
	Priority string
	// Class é a classe declarada do trabalho (para preferência configurável por
	// classe; opcional).
	Class string
	// Critical marca trabalho que NUNCA pode ser descartado por shed (guard
	// fail-closed). Trabalho crítico chegando a um shed é ErrCannotShedCritical.
	Critical bool
	// Optional marca EXPLICITAMENTE trabalho descartável por shed (opcional/baixa
	// prioridade). O guard do shed é FAIL-CLOSED: só é descartado trabalho com
	// Optional=true — a mera AUSÊNCIA de Critical NÃO autoriza o descarte (um item
	// de prioridade alta que se esqueça de marcar Critical continua protegido). Um
	// shed de item não-opcional é ErrCannotShedNonOptional (não descarta nada).
	Optional bool
	// Deferrable indica se o trabalho pode ser adiado (defer). Por omissão o zero
	// value é false; use [DegradationItem.Deferrable]=true para trabalho diferível.
	// Na cadeia de preferência, trabalho não-diferível salta o degrau defer.
	Deferrable bool
	// Irreversible marca trabalho cujo efeito, uma vez desencadeado, não se pode
	// desfazer. É um guard LOAD-BEARING: trabalho irreversível NUNCA é descartado
	// por shed (ErrCannotShedIrreversible) nem degradado silenciosamente por
	// downgrade (Downgrade devolve Applied=false) — escala para o REJECT fail-closed
	// (terminal): o executor garante que o efeito NÃO ocorre e que Normalize nunca o
	// ressuscita.
	Irreversible bool
	// CurrentTier e CurrentModel são o tier/modelo corrente (ponto de partida do
	// downgrade e alvo da restauração ao normalizar).
	CurrentTier  string
	CurrentModel string
	// Key é o contexto provider:model:region (passado à porta ModelTierRouter e
	// coerente com o admission control; opcional).
	Key ProviderKey
}

// sheddable decide, de forma pura, se o item é ELEGÍVEL a descarte por shed. O
// default é FAIL-CLOSED: só é descartável trabalho EXPLICITAMENTE opcional que não
// seja crítico nem irreversível. Usado pelo guard do [Degrader.Shed] e pelo salto
// do degrau shed em [Degrader.ExecuteChain] (coerentes entre si).
func (item DegradationItem) sheddable() bool {
	return item.Optional && !item.Critical && !item.Irreversible
}

// DegradationTrigger descreve o GATILHO da degradação (a condição de saturação
// que levou a política a seleccionar a acção). É registado no evento junto com a
// acção e o efeito, tornando cada decisão auditável. Deriva-se tipicamente de uma
// [SaturationCondition] via [TriggerFromCondition].
type DegradationTrigger struct {
	// Reason é a razão legível da degradação (obrigatória — sem razão não há shed).
	Reason string
	// PolicyVersion é a versão da política (AOS-030) que seleccionou a acção.
	PolicyVersion string
	// Partition é a partição tenant:priority saturada.
	Partition string
	// FillRatio é o enchimento da fila no momento (∈[0,1]).
	FillRatio float64
	// Depth e Capacity são a profundidade e o tecto da fila no momento.
	Depth    int
	Capacity int
}

// TriggerFromCondition constrói um [DegradationTrigger] a partir da condição de
// saturação de AOS-030 e da versão de política que seleccionou a acção. Reutiliza
// o contrato de AOS-030 (não o redefine).
func TriggerFromCondition(c SaturationCondition, policyVersion, reason string) DegradationTrigger {
	return DegradationTrigger{
		Reason:        reason,
		PolicyVersion: policyVersion,
		Partition:     Partition{Tenant: c.Tenant, Priority: c.Priority}.String(),
		FillRatio:     c.FillRatio,
		Depth:         c.Depth,
		Capacity:      c.Capacity,
	}
}

// DegradationResult é o veredicto de uma acção de degradação executada.
type DegradationResult struct {
	// Action é a acção executada (shed/defer/downgrade/reject).
	Action DegradationAction
	// ItemID identifica o trabalho degradado.
	ItemID string
	// Applied indica se a acção teve efeito. Um downgrade sem tier mais barato
	// disponível devolve Applied=false (não houve para onde descer); um shed
	// controlado, um defer preservado e um reject devolvem Applied=true.
	Applied bool
	// RetryAfter (defer) é o adiamento aconselhado até haver headroom.
	RetryAfter time.Duration
	// FromTier/ToTier/FromModel/ToModel (downgrade) descrevem o swap de variância.
	FromTier  string
	ToTier    string
	FromModel string
	ToModel   string
	// Reversible indica se esta degradação reverte ao normalizar (downgrade=sim;
	// shed/defer/reject=não).
	Reversible bool
	// Reason ecoa a razão do gatilho.
	Reason string
}

// ---------------------------------------------------------------------------
// ModelTierRouter — a PORTA de cost-aware model tiering (o Model Gateway de
// EPIC-06 implementá-la-á; aqui só a porta + impl de referência).
// ---------------------------------------------------------------------------

// ModelTier é um degrau da escada de tiers: um nome lógico de tier, o modelo
// concreto e o CostRank (menor = mais barato). A escada ordena-se por CostRank.
type ModelTier struct {
	// Tier é o nome lógico do tier (ex.: "premium", "standard", "economy").
	Tier string
	// Model é o identificador do modelo concreto do tier.
	Model string
	// CostRank ordena a escada por custo: quanto MENOR, mais barato. O downgrade
	// desce para o CostRank imediatamente inferior ao corrente.
	CostRank int
}

// TierRouteRequest é o pedido à porta [ModelTierRouter]: dado o tier corrente,
// devolver o tier imediatamente mais barato. Class/Tenant permitem roteamento por
// classe/tenant na impl de produção (o Model Gateway); a impl de referência
// usa uma única escada.
type TierRouteRequest struct {
	Key          ProviderKey
	Tenant       string
	Class        string
	CurrentTier  string
	CurrentModel string
}

// TierRouteDecision é a resposta da porta: se há um tier mais barato e qual.
type TierRouteDecision struct {
	// Downgraded indica se existe um tier mais barato para onde descer. false
	// quando o corrente já é o mais barato (não há para onde degradar).
	Downgraded bool
	FromTier   string
	ToTier     string
	FromModel  string
	ToModel    string
}

// ModelTierRouter é a PORTA que encaminha trabalho para um tier de modelo mais
// barato (cost-aware model tiering). O Model Gateway (EPIC-06) é o implementador
// de produção; NÃO é implementado aqui. A impl de referência determinística
// ([StaticModelTierRouter]) fecha o contrato entretanto — à imagem do
// [QuotaProvider] de AOS-027 e do ModelClient de AOS-013.
type ModelTierRouter interface {
	// Cheaper devolve o tier imediatamente mais barato que o corrente, ou
	// Downgraded=false se o corrente já é o mais barato. Determinística.
	Cheaper(ctx context.Context, req TierRouteRequest) (TierRouteDecision, error)
}

// StaticModelTierRouter é a impl de referência determinística do
// [ModelTierRouter]: uma escada FIXA de tiers ordenada por CostRank. Sem I/O nem
// relógio — segura para replay. NÃO é o Model Gateway (EPIC-06); é o análogo do
// StaticQuotaProvider (AOS-027) que fecha o contrato da porta.
type StaticModelTierRouter struct {
	// ladder é a escada ordenada por CostRank ASCENDENTE (índice 0 = mais barato).
	ladder []ModelTier
	// idx mapeia nome de tier → índice na escada (para localizar o corrente).
	idx map[string]int
}

// NewStaticModelTierRouter constrói a impl de referência com a escada dada. Os
// tiers são ordenados por CostRank ascendente (desempate estável por nome), para
// que Cheaper seja determinística. Tiers com nomes duplicados: o primeiro na
// ordenação vence o índice (a escada mantém todos, mas a localização usa o
// primeiro) — evite nomes duplicados na configuração.
func NewStaticModelTierRouter(tiers ...ModelTier) *StaticModelTierRouter {
	ladder := make([]ModelTier, len(tiers))
	copy(ladder, tiers)
	sort.SliceStable(ladder, func(i, j int) bool {
		if ladder[i].CostRank != ladder[j].CostRank {
			return ladder[i].CostRank < ladder[j].CostRank
		}
		return ladder[i].Tier < ladder[j].Tier
	})
	idx := make(map[string]int, len(ladder))
	for i, t := range ladder {
		if _, ok := idx[t.Tier]; !ok {
			idx[t.Tier] = i
		}
	}
	return &StaticModelTierRouter{ladder: ladder, idx: idx}
}

// Cheaper implementa [ModelTierRouter]: localiza o tier corrente na escada e
// devolve o degrau imediatamente mais barato (índice − 1 na ordenação
// ascendente). Se o corrente é o mais barato (índice 0) ou desconhecido, devolve
// Downgraded=false — não há para onde descer (nunca um "upgrade" acidental).
func (r *StaticModelTierRouter) Cheaper(_ context.Context, req TierRouteRequest) (TierRouteDecision, error) {
	i, ok := r.idx[req.CurrentTier]
	if !ok || i <= 0 {
		// Desconhecido ou já no mais barato: não degrada.
		return TierRouteDecision{Downgraded: false, FromTier: req.CurrentTier, FromModel: req.CurrentModel}, nil
	}
	target := r.ladder[i-1]
	return TierRouteDecision{
		Downgraded: true,
		FromTier:   req.CurrentTier,
		ToTier:     target.Tier,
		FromModel:  req.CurrentModel,
		ToModel:    target.Model,
	}, nil
}

// ---------------------------------------------------------------------------
// DeferSink — a PORTA opcional que PRESERVA o trabalho adiado (defer).
// ---------------------------------------------------------------------------

// DeferSink é a PORTA por onde o defer PRESERVA o trabalho adiado (ex.:
// reenfileirar nas filas de AOS-030). É OPCIONAL: sem sink, o defer apenas
// devolve o retry_after no resultado/evento (o chamador reagenda). Injectar um
// sink integra o defer com a fila/admission (AOS-027/030), garantindo que o
// trabalho NÃO se perde.
type DeferSink interface {
	// Defer preserva o item para nova tentativa após retryAfter. Um erro é
	// PROPAGADO (fail-closed: se não se conseguiu preservar, não se afirma o defer).
	Defer(ctx context.Context, item DegradationItem, retryAfter time.Duration) error
}

// ---------------------------------------------------------------------------
// Degrader — o EXECUTOR das quatro acções.
// ---------------------------------------------------------------------------

// activeDowngrade regista um downgrade REVERSÍVEL em curso (para a restauração ao
// normalizar). Guardado no mapa `active` do [Degrader], sob mutex.
type activeDowngrade struct {
	item      DegradationItem
	fromTier  string
	toTier    string
	fromModel string
	toModel   string
}

// Degrader executa as acções de degradação seleccionadas pela política (AOS-030),
// na ordem de preferência shed→defer→downgrade→reject, com eventos append-only e
// reversibilidade dos downgrades ao normalizar. Construir com [NewDegrader]. É
// seguro para uso concorrente (um mutex protege o registo de downgrades activos e
// o contador de eventos).
type Degrader struct {
	router    ModelTierRouter
	deferSink DeferSink
	log       EventLog
	now       func() time.Time
	tracer    agentruntime.Tracer
	producer  eventstore.Producer
	name      string
	// deferRetry é o retry_after por omissão do defer (0 = deixa o chamador
	// derivar do refill/backpressure a montante). Injectável via WithDeferRetry.
	deferRetry time.Duration

	mu sync.Mutex
	// active mapeia item ID → downgrade reversível em curso. Reverte ao normalizar.
	active map[string]*activeDowngrade
	// nEvents gera o step_id monotónico dos eventos de degradação (determinístico
	// dada a sequência de chamadas; a dedup do Event Store por step_id protege
	// contra duplicados de re-entrega).
	nEvents uint64
}

// DegraderOption configura o [Degrader].
type DegraderOption func(*Degrader)

// WithDegradationLog injecta o Event Store para os eventos de degradação
// (append-only, observáveis, replay-fiéis). Sem log, o executor executa na mesma
// (útil em testes puros), mas não deixa rasto auditável.
func WithDegradationLog(log EventLog) DegraderOption {
	return func(d *Degrader) {
		if log != nil {
			d.log = log
		}
	}
}

// WithDeferSink injecta a porta que preserva o trabalho adiado (ex.: as filas de
// AOS-030). Sem ela, o defer só devolve o retry_after (o chamador reagenda).
func WithDeferSink(sink DeferSink) DegraderOption {
	return func(d *Degrader) {
		if sink != nil {
			d.deferSink = sink
		}
	}
}

// WithDeferRetry fixa o retry_after por omissão do defer (0 = derivado a
// montante). Determinístico (sem time.Now na decisão).
func WithDeferRetry(after time.Duration) DegraderOption {
	return func(d *Degrader) {
		if after > 0 {
			d.deferRetry = after
		}
	}
}

// WithDegradationClock injecta o relógio dos carimbos dos eventos
// (determinismo/replay: sem time.Now embutido na decisão).
func WithDegradationClock(now func() time.Time) DegraderOption {
	return func(d *Degrader) {
		if now != nil {
			d.now = now
		}
	}
}

// WithDegradationTracer injecta a porta OTel (span por acção). Zero-dep.
func WithDegradationTracer(t agentruntime.Tracer) DegraderOption {
	return func(d *Degrader) {
		if t != nil {
			d.tracer = t
		}
	}
}

// WithDegradationProducer injecta a NHI emissora dos eventos.
func WithDegradationProducer(p eventstore.Producer) DegraderOption {
	return func(d *Degrader) {
		if p.NHIID != "" {
			d.producer = p
		}
	}
}

// WithDegradationName nomeia a instância (usada no stream de eventos). Permite
// vários executores independentes no mesmo Event Store.
func WithDegradationName(name string) DegraderOption {
	return func(d *Degrader) {
		if name != "" {
			d.name = name
		}
	}
}

// NewDegrader constrói o executor. router (a porta de tiering) é OBRIGATÓRIO — sem
// ele não há downgrade possível (fail-closed). Os restantes colaboradores são
// opcionais.
func NewDegrader(router ModelTierRouter, opts ...DegraderOption) (*Degrader, error) {
	if router == nil {
		return nil, fmt.Errorf("scheduler: ModelTierRouter nil (downgrade sem porta de tiering)")
	}
	d := &Degrader{
		router:   router,
		now:      time.Now,
		tracer:   agentruntime.NoopTracer{},
		producer: eventstore.Producer{NHIID: DefaultDegraderNHI},
		name:     "default",
		active:   make(map[string]*activeDowngrade),
	}
	for _, opt := range opts {
		opt(d)
	}
	return d, nil
}

// Execute despacha a acção SELECCIONADA pela política (AOS-030) para o handler
// correspondente. É o ponto de entrada quando o chamador já tem uma acção
// (tipicamente de [PolicyEngine.Select]). Para a cadeia com fallback pela ordem de
// preferência, ver [Degrader.ExecuteChain].
//
// ESCALADA NÃO-SILENCIOSA: se a acção seleccionada NÃO teve efeito (Applied=false,
// ex.: downgrade de item já no tier mais barato ou de trabalho irreversível), a
// carga NÃO foi aliviada — Execute devolve [ErrDegradationNotApplied] (envolvendo o
// resultado) para o chamador ESCALAR ao degrau seguinte, em vez de julgar
// (silenciosamente) ter degradado. O caminho com fallback embutido é [ExecuteChain].
func (d *Degrader) Execute(ctx context.Context, action DegradationAction, item DegradationItem, trigger DegradationTrigger) (DegradationResult, error) {
	var (
		res DegradationResult
		err error
	)
	switch action {
	case ActionShed:
		res, err = d.Shed(ctx, item, trigger)
	case ActionDefer:
		res, err = d.Defer(ctx, item, trigger)
	case ActionDowngrade:
		res, err = d.Downgrade(ctx, item, trigger)
	case ActionReject:
		res, err = d.Reject(ctx, item, trigger)
	default:
		return DegradationResult{}, fmt.Errorf("%w: %q", ErrUnknownDegradationAction, action)
	}
	if err != nil {
		return res, err
	}
	if !res.Applied {
		// A acção seleccionada não aliviou a carga: sinal accionável para escalar
		// (nunca um no-op silencioso que deixa a pressão sem alívio).
		return res, fmt.Errorf("%w: acção=%q item=%q", ErrDegradationNotApplied, action, item.ID)
	}
	return res, nil
}

// Shed descarta trabalho opcional/baixa prioridade de forma CONTROLADA, com razão.
// GUARDS FAIL-CLOSED (nenhum emite work_shed nem descarta nada — o chamador escala,
// ver [Degrader.ExecuteChain]):
//   - sem RAZÃO no gatilho ⇒ [ErrMissingReason] (sem razão não há shed);
//   - trabalho CRÍTICO ⇒ [ErrCannotShedCritical];
//   - trabalho IRREVERSÍVEL ⇒ [ErrCannotShedIrreversible] (nunca perdido por shed);
//   - trabalho NÃO marcado OPCIONAL ⇒ [ErrCannotShedNonOptional] (default protege:
//     só é descartável trabalho PROVADAMENTE opcional; a ausência de Critical não
//     autoriza o descarte).
func (d *Degrader) Shed(ctx context.Context, item DegradationItem, trigger DegradationTrigger) (DegradationResult, error) {
	ctx, span := d.startSpan(ctx, ActionShed, item)
	defer span.End()

	refuse := func(err error) (DegradationResult, error) {
		// Fail-closed: sem evento nem efeito; devolve o resultado não-aplicado + erro.
		span.SetAttribute(attrDegApplied, false)
		return DegradationResult{Action: ActionShed, ItemID: item.ID, Applied: false, Reason: trigger.Reason}, err
	}
	if trigger.Reason == "" {
		return refuse(fmt.Errorf("%w: item=%q acção=shed", ErrMissingReason, item.ID))
	}
	if item.Critical {
		// NUNCA descartar crítico silenciosamente: fail-closed, sem evento nem efeito.
		return refuse(fmt.Errorf("%w: item=%q", ErrCannotShedCritical, item.ID))
	}
	if item.Irreversible {
		// Irreversível nunca é perdido por shed: escala para o reject fail-closed.
		return refuse(fmt.Errorf("%w: item=%q", ErrCannotShedIrreversible, item.ID))
	}
	if !item.Optional {
		// Default fail-closed: só se descarta trabalho explicitamente opcional.
		return refuse(fmt.Errorf("%w: item=%q", ErrCannotShedNonOptional, item.ID))
	}

	span.SetAttribute(attrDegApplied, true)
	span.SetAttribute(attrDegReason, trigger.Reason)
	if err := d.emit(ctx, EventWorkShed, item, trigger, func(pl *degradationPayload) {
		pl.Action = string(ActionShed)
	}); err != nil {
		return DegradationResult{}, err
	}
	return DegradationResult{Action: ActionShed, ItemID: item.ID, Applied: true, Reason: trigger.Reason}, nil
}

// Defer adia trabalho admissível PRESERVANDO-O (retry_after). Se houver um
// [DeferSink] injectado, reenfileira por ele (integra AOS-027/030; não perde o
// trabalho) — um erro do sink é PROPAGADO (fail-closed: não se afirma um defer que
// não preservou). Emite work_deferred com o retry_after.
func (d *Degrader) Defer(ctx context.Context, item DegradationItem, trigger DegradationTrigger) (DegradationResult, error) {
	ctx, span := d.startSpan(ctx, ActionDefer, item)
	defer span.End()

	if trigger.Reason == "" {
		// Fail-closed: nenhuma degradação entra no log sem razão auditável.
		span.SetAttribute(attrDegApplied, false)
		return DegradationResult{Action: ActionDefer, ItemID: item.ID, Applied: false},
			fmt.Errorf("%w: item=%q acção=defer", ErrMissingReason, item.ID)
	}

	retry := d.deferRetry
	span.SetAttribute(attrDegRetryMs, retry.Milliseconds())

	// Preserva o trabalho pela porta (se injectada) ANTES de afirmar o defer.
	if d.deferSink != nil {
		if err := d.deferSink.Defer(ctx, item, retry); err != nil {
			span.SetAttribute(attrDegApplied, false)
			return DegradationResult{}, fmt.Errorf("scheduler: preservar trabalho adiado: %w", err)
		}
	}

	span.SetAttribute(attrDegApplied, true)
	if err := d.emit(ctx, EventWorkDeferred, item, trigger, func(pl *degradationPayload) {
		pl.Action = string(ActionDefer)
		pl.RetryAfterMs = retry.Milliseconds()
	}); err != nil {
		return DegradationResult{}, err
	}
	return DegradationResult{Action: ActionDefer, ItemID: item.ID, Applied: true, RetryAfter: retry, Reason: trigger.Reason}, nil
}

// Downgrade encaminha o trabalho para um tier de modelo mais BARATO via a porta
// [ModelTierRouter], registando o swap como VARIÂNCIA EXPLÍCITA
// (tier_antigo→tier_novo) no evento model_downgraded — NUNCA silencioso. A
// variância entra no log para o replay ser fiel (ADR-010/AOS-016) e o downgrade é
// registado como REVERSÍVEL (reverte ao normalizar via [Degrader.Normalize]). Se
// não há tier mais barato, devolve Applied=false SEM emitir (não houve variância).
func (d *Degrader) Downgrade(ctx context.Context, item DegradationItem, trigger DegradationTrigger) (DegradationResult, error) {
	ctx, span := d.startSpan(ctx, ActionDowngrade, item)
	defer span.End()

	if trigger.Reason == "" {
		// Fail-closed: sem razão não se regista variância no log.
		span.SetAttribute(attrDegApplied, false)
		return DegradationResult{Action: ActionDowngrade, ItemID: item.ID, Applied: false},
			fmt.Errorf("%w: item=%q acção=downgrade", ErrMissingReason, item.ID)
	}
	if item.Irreversible {
		// Trabalho irreversível NÃO é degradado silenciosamente: Applied=false SEM
		// variância, para a cadeia escalar ao reject fail-closed (nunca um swap de
		// modelo escondido num efeito que não se pode desfazer).
		span.SetAttribute(attrDegApplied, false)
		return DegradationResult{Action: ActionDowngrade, ItemID: item.ID, Applied: false, Reason: trigger.Reason}, nil
	}

	dec, err := d.router.Cheaper(ctx, TierRouteRequest{
		Key:          item.Key,
		Tenant:       item.Tenant,
		Class:        item.Class,
		CurrentTier:  item.CurrentTier,
		CurrentModel: item.CurrentModel,
	})
	if err != nil {
		return DegradationResult{}, fmt.Errorf("scheduler: rotear tier mais barato: %w", err)
	}
	if !dec.Downgraded {
		// Já no tier mais barato: não há para onde degradar, nenhuma variância.
		span.SetAttribute(attrDegApplied, false)
		return DegradationResult{Action: ActionDowngrade, ItemID: item.ID, Applied: false, Reason: trigger.Reason}, nil
	}

	span.SetAttribute(attrDegApplied, true)
	span.SetAttribute(attrDegFromTier, dec.FromTier)
	span.SetAttribute(attrDegToTier, dec.ToTier)
	span.SetAttribute(attrDegReversibl, true)

	// Regista a VARIÂNCIA EXPLÍCITA (nunca silenciosa): tier_antigo→tier_novo. É um
	// facto do manifesto/log — o replay reconstrói o swap fielmente.
	if err := d.emit(ctx, EventModelDowngraded, item, trigger, func(pl *degradationPayload) {
		pl.Action = string(ActionDowngrade)
		pl.FromTier = dec.FromTier
		pl.ToTier = dec.ToTier
		pl.FromModel = dec.FromModel
		pl.ToModel = dec.ToModel
		pl.Reversible = true
	}); err != nil {
		return DegradationResult{}, err
	}

	// Regista o downgrade como REVERSÍVEL (para restaurar o tier ao normalizar). Um
	// segundo downgrade do mesmo item substitui o registo (o fromTier original
	// mantém-se se ainda activo — ver abaixo).
	d.mu.Lock()
	if prev, ok := d.active[item.ID]; ok {
		// Downgrade em cascata (premium→standard→economy): preserva o tier ORIGINAL
		// (o primeiro fromTier) para a restauração devolver ao topo, não ao degrau
		// intermédio.
		d.active[item.ID] = &activeDowngrade{
			item:      item,
			fromTier:  prev.fromTier,
			toTier:    dec.ToTier,
			fromModel: prev.fromModel,
			toModel:   dec.ToModel,
		}
	} else {
		d.active[item.ID] = &activeDowngrade{
			item:      item,
			fromTier:  dec.FromTier,
			toTier:    dec.ToTier,
			fromModel: dec.FromModel,
			toModel:   dec.ToModel,
		}
	}
	d.mu.Unlock()

	return DegradationResult{
		Action:     ActionDowngrade,
		ItemID:     item.ID,
		Applied:    true,
		FromTier:   dec.FromTier,
		ToTier:     dec.ToTier,
		FromModel:  dec.FromModel,
		ToModel:    dec.ToModel,
		Reversible: true,
		Reason:     trigger.Reason,
	}, nil
}

// Reject rejeita o trabalho como ÚLTIMO RECURSO, devolvendo um erro CLARO e
// accionável ([ErrWorkRejected]) e emitindo work_rejected. FAIL-CLOSED para
// trabalho IRREVERSÍVEL: a rejeição é terminal — o efeito irreversível NÃO ocorre
// (o executor nunca o desencadeia) e Normalize nunca ressuscita trabalho
// rejeitado. Applied=true (a rejeição É a acção aplicada), mas devolve sempre o
// erro sentinela.
func (d *Degrader) Reject(ctx context.Context, item DegradationItem, trigger DegradationTrigger) (DegradationResult, error) {
	ctx, span := d.startSpan(ctx, ActionReject, item)
	defer span.End()

	if trigger.Reason == "" {
		// Fail-closed: mesmo a rejeição de último recurso exige razão auditável.
		span.SetAttribute(attrDegApplied, false)
		return DegradationResult{Action: ActionReject, ItemID: item.ID, Applied: false, Reversible: false},
			fmt.Errorf("%w: item=%q acção=reject", ErrMissingReason, item.ID)
	}

	span.SetAttribute(attrDegApplied, true)
	span.SetAttribute(attrDegReason, trigger.Reason)
	span.SetAttribute(attrDegReversibl, false)

	if err := d.emit(ctx, EventWorkRejected, item, trigger, func(pl *degradationPayload) {
		pl.Action = string(ActionReject)
	}); err != nil {
		return DegradationResult{}, err
	}
	res := DegradationResult{Action: ActionReject, ItemID: item.ID, Applied: true, Reversible: false, Reason: trigger.Reason}
	// Erro accionável: o chamador sabe que o trabalho foi recusado e porquê.
	return res, fmt.Errorf("%w: item=%q partição=%q razão=%q", ErrWorkRejected, item.ID, trigger.Partition, trigger.Reason)
}

// ExecuteChain percorre a ORDEM DE PREFERÊNCIA (por omissão
// [DefaultPreferenceOrder]: shed→defer→downgrade→reject) e aplica o PRIMEIRO
// degrau APLICÁVEL ao item, escalando quando um degrau não se aplica:
//
//   - shed é aplicável se o item é OPCIONAL e nem crítico nem irreversível
//     (default fail-closed: só se descarta trabalho provadamente opcional);
//   - defer é aplicável se o item é diferível (Deferrable);
//   - downgrade é aplicável se houver um tier mais barato E o item for reversível
//     (irreversível não é degradado silenciosamente — a porta/guard não concede);
//   - reject é o degrau terminal (sempre aplicável, fail-closed).
//
// A ordem é CONFIGURÁVEL por classe/tenant (passe uma `order` própria, coerente
// com a política de AOS-030). Devolve o resultado do degrau aplicado. Se a ordem
// se esgotar sem reject (ordem mal formada), devolve [ErrChainExhausted] envolvendo
// [ErrWorkRejected] — a rede de segurança é sempre a rejeição fail-closed.
//
// NOTA: este helper demonstra a PREFERÊNCIA com FALLBACK (crítico salta o shed).
// A ESCALADA POR PRESSÃO CRESCENTE é conduzida pela política de AOS-030, que
// selecciona a acção conforme o enchimento — o chamador executa cada selecção com
// [Degrader.Execute]. Ambos os caminhos respeitam a mesma ordem canónica.
func (d *Degrader) ExecuteChain(ctx context.Context, item DegradationItem, trigger DegradationTrigger, order []DegradationAction) (DegradationResult, error) {
	if len(order) == 0 {
		order = DefaultPreferenceOrder
	}
	for _, action := range order {
		switch action {
		case ActionShed:
			if !item.sheddable() {
				continue // não descartável (crítico/irreversível/não-opcional): escala
			}
			return d.Shed(ctx, item, trigger)
		case ActionDefer:
			if !item.Deferrable {
				continue // não-diferível: escala
			}
			return d.Defer(ctx, item, trigger)
		case ActionDowngrade:
			res, err := d.Downgrade(ctx, item, trigger)
			if err != nil {
				return res, err
			}
			if !res.Applied {
				continue // sem tier mais barato: escala para reject
			}
			return res, nil
		case ActionReject:
			return d.Reject(ctx, item, trigger)
		default:
			return DegradationResult{}, fmt.Errorf("%w: %q", ErrUnknownDegradationAction, action)
		}
	}
	// Ordem esgotada sem um degrau terminal: rede de segurança fail-closed.
	res, _ := d.Reject(ctx, item, trigger)
	return res, fmt.Errorf("%w: %w", ErrChainExhausted, ErrWorkRejected)
}

// Normalize REVERTE todas as degradações REVERSÍVEIS (downgrades activos) ao
// NORMALIZAR a carga — o sinal de saturação desceu (ex.: backpressure_cleared de
// AOS-030). Restaura o tier de cada downgrade activo (emite tier_restored,
// tier_novo→tier_antigo) por ORDEM de ID (determinismo) e esvazia o registo. É
// IDEMPOTENTE: uma segunda Normalize sem novos downgrades é no-op (nada activo).
//
// A variância model_downgraded PERMANECE no log — o swap foi um facto e o replay
// tem de o reconstruir mesmo após a reversão (ADR-010). Shed e reject NÃO são
// reversíveis (o trabalho já foi descartado/recusado) e não são tocados aqui.
// Devolve os itens restaurados.
func (d *Degrader) Normalize(ctx context.Context, reason string) ([]DegradationResult, error) {
	// Snapshot ordenado dos IDs activos (determinismo). NÃO se esvazia o registo já:
	// cada item só sai de `active` DEPOIS de o seu tier_restored ter sido persistido,
	// para que um erro do Event Store a meio NÃO perca as reversões restantes — a
	// próxima Normalize reprocessa-as (nunca uma reversão pendente fica perdida, ao
	// contrário do model_downgraded, que permanece no log).
	d.mu.Lock()
	ids := make([]string, 0, len(d.active))
	for id := range d.active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	d.mu.Unlock()

	out := make([]DegradationResult, 0, len(ids))
	for _, id := range ids {
		d.mu.Lock()
		ad, ok := d.active[id]
		d.mu.Unlock()
		if !ok {
			continue // restaurado/removido concorrentemente: nada a fazer
		}

		trigger := DegradationTrigger{Reason: reason}
		if err := d.emit(ctx, EventTierRestored, ad.item, trigger, func(pl *degradationPayload) {
			pl.Action = "tier_restored"
			// Restauração: o tier_novo (degradado) volta ao tier_antigo (original).
			pl.FromTier = ad.toTier
			pl.ToTier = ad.fromTier
			pl.FromModel = ad.toModel
			pl.ToModel = ad.fromModel
			pl.Reversible = false // a própria restauração é terminal
		}); err != nil {
			// Erro parcial: os itens ainda NÃO restaurados permanecem em `active`
			// (este e os seguintes) para a próxima Normalize os reprocessar.
			return out, err
		}

		// Só agora, com o tier_restored já no log, se remove o item do registo.
		d.mu.Lock()
		delete(d.active, id)
		d.mu.Unlock()

		out = append(out, DegradationResult{
			Action:    ActionDowngrade, // reversão de um downgrade
			ItemID:    ad.item.ID,
			Applied:   true,
			FromTier:  ad.toTier,   // de onde reverte
			ToTier:    ad.fromTier, // para o tier original
			FromModel: ad.toModel,
			ToModel:   ad.fromModel,
			Reason:    reason,
		})
	}
	return out, nil
}

// ActiveDowngrades devolve os downgrades REVERSÍVEIS actualmente em curso (por
// ordem de ID), para observabilidade/teste. Um snapshot: o tier corrente (toTier)
// e o original a restaurar (fromTier).
func (d *Degrader) ActiveDowngrades() []DegradationResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := make([]string, 0, len(d.active))
	for id := range d.active {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]DegradationResult, 0, len(ids))
	for _, id := range ids {
		ad := d.active[id]
		out = append(out, DegradationResult{
			Action:     ActionDowngrade,
			ItemID:     ad.item.ID,
			Applied:    true,
			FromTier:   ad.fromTier,
			ToTier:     ad.toTier,
			FromModel:  ad.fromModel,
			ToModel:    ad.toModel,
			Reversible: true,
		})
	}
	return out
}

// startSpan abre o span da acção com os atributos comuns.
func (d *Degrader) startSpan(ctx context.Context, action DegradationAction, item DegradationItem) (context.Context, agentruntime.Span) {
	ctx, span := d.tracer.StartSpan(ctx, opDegradation)
	span.SetAttribute(attrDegAction, string(action))
	span.SetAttribute(attrDegItem, item.ID)
	span.SetAttribute(attrDegTenant, item.Tenant)
	span.SetAttribute(attrDegPriority, item.Priority)
	span.SetAttribute(attrDegCritical, item.Critical)
	return ctx, span
}

// degradationPayload é o corpo serializado (estável, sem mapas) dos eventos de
// degradação — determinismo/replay. Cada evento carrega o GATILHO (razão, versão
// de política, partição, enchimento), a ACÇÃO e o EFEITO (retry_after, swap de
// tier).
type degradationPayload struct {
	Type          string  `json:"type"`
	Action        string  `json:"action"`
	ItemID        string  `json:"item_id"`
	Tenant        string  `json:"tenant,omitempty"`
	Priority      string  `json:"priority,omitempty"`
	Class         string  `json:"class,omitempty"`
	Critical      bool    `json:"critical,omitempty"`
	Reason        string  `json:"reason"`
	PolicyVersion string  `json:"policy_version,omitempty"`
	Partition     string  `json:"partition,omitempty"`
	FillRatio     float64 `json:"fill_ratio,omitempty"`
	Depth         int     `json:"depth,omitempty"`
	Capacity      int     `json:"capacity,omitempty"`
	RetryAfterMs  int64   `json:"retry_after_ms,omitempty"`
	FromTier      string  `json:"from_tier,omitempty"`
	ToTier        string  `json:"to_tier,omitempty"`
	FromModel     string  `json:"from_model,omitempty"`
	ToModel       string  `json:"to_model,omitempty"`
	Reversible    bool    `json:"reversible,omitempty"`
	TSUnixNano    int64   `json:"ts_unix_nano"`
}

// emit serializa e escreve um evento de degradação no stream da instância, com
// step_id "deg-N" monotónico (idempotente por (run_id, step_id) na dedup do Event
// Store). O `mut` preenche os campos específicos da acção. No-op sem log (caminho
// puramente in-memory). Fail-closed: um erro do store propaga.
func (d *Degrader) emit(ctx context.Context, evType string, item DegradationItem, trigger DegradationTrigger, mut func(*degradationPayload)) error {
	if d.log == nil {
		return nil
	}
	pl := degradationPayload{
		Type:          evType,
		ItemID:        item.ID,
		Tenant:        item.Tenant,
		Priority:      item.Priority,
		Class:         item.Class,
		Critical:      item.Critical,
		Reason:        trigger.Reason,
		PolicyVersion: trigger.PolicyVersion,
		Partition:     trigger.Partition,
		FillRatio:     trigger.FillRatio,
		Depth:         trigger.Depth,
		Capacity:      trigger.Capacity,
		TSUnixNano:    d.now().UnixNano(),
	}
	if mut != nil {
		mut(&pl)
	}
	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.nEvents++
	stepID := "deg-" + strconv.FormatUint(d.nEvents, 10)
	d.mu.Unlock()

	streamID := degradationStreamPrefix + d.name
	_, err = d.log.Append(ctx, streamID, eventstore.EventInput{
		Type:     evType,
		Payload:  raw,
		RunID:    streamID,
		StepID:   stepID,
		Producer: d.producer,
	})
	return err
}

// DegradationRecord é uma acção de degradação reconstruída do log (para replay).
// Inclui a variância do downgrade (from_tier→to_tier), que o replay usa para ser
// fiel ao swap de modelo (ADR-010).
type DegradationRecord struct {
	Type       string
	Action     string
	ItemID     string
	Tenant     string
	Reason     string
	RetryAfter time.Duration
	FromTier   string
	ToTier     string
	FromModel  string
	ToModel    string
	Reversible bool
	Seq        uint64
}

// ReplayDegradation reconstrói fielmente a sequência de acções de degradação da
// instância a partir do Event Store (append-only, por ordem de seq). É a prova de
// que cada acção — incluindo a VARIÂNCIA do downgrade — se reconstrói do log
// (determinismo/ADR-001/010). Sem log, devolve nil.
func (d *Degrader) ReplayDegradation(ctx context.Context) ([]DegradationRecord, error) {
	if d.log == nil {
		return nil, nil
	}
	streamID := degradationStreamPrefix + d.name
	evs, err := d.log.Read(ctx, streamID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]DegradationRecord, 0, len(evs))
	for _, ev := range evs {
		var pl degradationPayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return nil, fmt.Errorf("scheduler: payload de degradação corrompido no seq %d: %w", ev.Seq, err)
		}
		out = append(out, DegradationRecord{
			Type:       pl.Type,
			Action:     pl.Action,
			ItemID:     pl.ItemID,
			Tenant:     pl.Tenant,
			Reason:     pl.Reason,
			RetryAfter: time.Duration(pl.RetryAfterMs) * time.Millisecond,
			FromTier:   pl.FromTier,
			ToTier:     pl.ToTier,
			FromModel:  pl.FromModel,
			ToModel:    pl.ToModel,
			Reversible: pl.Reversible,
			Seq:        ev.Seq,
		})
	}
	return out, nil
}

// RehydrateActive RECONSTRÓI o registo de downgrades REVERSÍVEIS activos a partir do
// log (Event Store), para que [Degrader.Normalize] restaure fielmente os tiers APÓS
// UM RESTART. Sem isto, o mapa `active` (só em memória) fica vazio no arranque e um
// downgrade a meio da degradação nunca se reverteria — o log teria model_downgraded
// sem o tier_restored correspondente e o estado divergiria do pretendido.
//
// Reconstrói por replay determinístico (por ordem de seq): cada model_downgraded
// (re)regista o item (preservando o tier ORIGINAL em cascata, como o caminho vivo);
// cada tier_restored remove-o (já revertido). O que sobra são os downgrades ainda
// activos. SUBSTITUI o registo em memória — deve ser chamado no arranque, ANTES de
// executar novas acções. Sem log, é no-op. Devolve o número de downgrades activos.
func (d *Degrader) RehydrateActive(ctx context.Context) (int, error) {
	if d.log == nil {
		return 0, nil
	}
	streamID := degradationStreamPrefix + d.name
	evs, err := d.log.Read(ctx, streamID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return 0, nil
		}
		return 0, err
	}
	rebuilt := make(map[string]*activeDowngrade)
	for _, ev := range evs {
		var pl degradationPayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return 0, fmt.Errorf("scheduler: payload de degradação corrompido no seq %d: %w", ev.Seq, err)
		}
		switch pl.Type {
		case EventModelDowngraded:
			// Item reconstruído dos campos persistidos (suficiente para o tier_restored).
			item := DegradationItem{
				ID:       pl.ItemID,
				Tenant:   pl.Tenant,
				Priority: pl.Priority,
				Class:    pl.Class,
				Critical: pl.Critical,
			}
			if prev, ok := rebuilt[pl.ItemID]; ok {
				// Cascata: preserva o tier/modelo ORIGINAL (primeiro fromTier).
				rebuilt[pl.ItemID] = &activeDowngrade{
					item:      item,
					fromTier:  prev.fromTier,
					toTier:    pl.ToTier,
					fromModel: prev.fromModel,
					toModel:   pl.ToModel,
				}
			} else {
				rebuilt[pl.ItemID] = &activeDowngrade{
					item:      item,
					fromTier:  pl.FromTier,
					toTier:    pl.ToTier,
					fromModel: pl.FromModel,
					toModel:   pl.ToModel,
				}
			}
		case EventTierRestored:
			// Já revertido: sai do registo de activos.
			delete(rebuilt, pl.ItemID)
		}
	}
	d.mu.Lock()
	d.active = rebuilt
	// Retoma o contador de step_id a partir do que já está no stream: sem isto, o
	// contador in-memory reiniciaria a 0 e os step_ids pós-restart ("deg-1"...)
	// COLIDIRIAM com os já persistidos, sendo DEDUPLICADOS (silenciosamente
	// descartados) pelo Event Store — a reversão nunca entraria no log. Cada emit
	// gera exactamente um evento, logo o próximo step_id é len(evs)+1.
	d.nEvents = uint64(len(evs))
	d.mu.Unlock()
	return len(rebuilt), nil
}

// Verificação estática: a impl de referência satisfaz a porta.
var _ ModelTierRouter = (*StaticModelTierRouter)(nil)
