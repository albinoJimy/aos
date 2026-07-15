// Package router é o ROUTER cost/load-aware de PRODUÇÃO do Model Gateway
// (AOS-059, tecnica/06 §6, ADR-008/ADR-010). Decide o destino de cada model call
// a partir de quatro sinais — CARGA, CUSTO/CAPACIDADE, LATÊNCIA/PRIORIDADE e
// ORÇAMENTO/rate-limit — SEMPRE dentro da fronteira de soberania (AOS-058) e
// coordenado com o admission control GLOBAL (EPIC-03).
//
// # O que este router COMPÕE (não reimplementa)
//
//   - CARGA — o endpoint MENOS CARREGADO por headroom real de TPM/RPM. Compõe o
//     keypool (AOS-057, routing/keypool) para a selecção de conta e a porta
//     [LoadProvider] para o ranking de endpoints/regiões. NÃO reimplementa o
//     token-bucket.
//   - SOBERANIA — compõe a guarda de fronteira (AOS-058, routing/sovereignty): os
//     candidatos cross-border são DESCARTADOS ANTES de qualquer escolha; toda a
//     decisão ocorre entre sobreviventes intra-fronteira. Compõe também a
//     allowlist regional (porta [Allowlist]): um tier/modelo fora da allowlist do
//     board NUNCA é escolhido nem oferecido em degradação.
//   - CUSTO/CAPACIDADE + LATÊNCIA — compõe routing/tiering: o tier mais barato que
//     satisfaz a capacidade; interactivo favorece latência, batch tolera lento/barato.
//   - ADMISSION GLOBAL — coordena com a porta [AdmissionCoordinator] (ADR-008): NÃO
//     despacha sem débito reservado a montante (evita o colapso agregado — vários
//     boards, cada um ok, saturam o limite partilhado). NÃO reimplementa o bucket.
//   - ORÇAMENTO — compõe routing/degradation: a ~80% do orçamento OFERECE degradar
//     para tier mais barato (exaustão graciosa) em vez de hard-stop cego.
//
// A CADEIA shed→defer→degradar→rejeitar é DO ESCALONADOR (AOS-031); este router dá
// a ESCOLHA de tier e a OFERTA de degradação, mapeando o seu resultado para os
// degraus da cadeia — sem os executar. Cada decisão regista MODELO, TIER e RAZÃO
// (span OTel + porta [DecisionSink]) para análise de custo post-hoc (ADR-010).
//
// # Determinismo
//
// Sem relógio nem aleatoriedade na decisão: carga, orçamento e admissão são
// injectados por porta. A selecção é determinística (desempate estável).
package router

import (
	"context"
	"sort"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/routing/degradation"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// Atributos de span do registo por decisão (modelo/tier/razão) — análise de custo
// post-hoc e calibração da política (ADR-010).
const (
	AttrRoutingModel     = "aos.routing.model"
	AttrRoutingTier      = "aos.routing.tier"
	AttrRoutingReason    = "aos.routing.reason"
	AttrRoutingRegion    = "aos.routing.region"
	AttrRoutingOutcome   = "aos.routing.outcome"
	AttrRoutingDegraded  = "aos.routing.degraded"
	AttrRoutingFromTier  = "aos.routing.from_tier"
	AttrRoutingToTier    = "aos.routing.to_tier"
	AttrRoutingKeyID     = "aos.routing.key_id"
	AttrRoutingExhausted = "aos.routing.budget_exhausted"
)

// opRouting é o nome de operação do span por decisão de roteamento.
const opRouting = "model_routing"

// Outcome mapeia o resultado do router para os degraus da cadeia do Escalonador
// (shed→defer→degradar→rejeitar) SEM os executar — a cadeia é do Escalonador.
type Outcome string

const (
	// OutcomeRouted — rota escolhida no tier que satisfaz a capacidade (sem pressão).
	OutcomeRouted Outcome = "routed"
	// OutcomeDegraded — rota escolhida num tier MAIS BARATO por exaustão graciosa
	// (orçamento >= limiar) ou pressão — a OFERTA de degradação do GW. O degrau
	// "degradar" da cadeia; a variância model_downgraded é selada pelo Escalonador.
	OutcomeDegraded Outcome = "degraded"
	// OutcomeDeferred — sem headroom GLOBAL de admissão (ou pool saturado): ADIA com
	// retry_after, NUNCA despacha sem débito reservado. O degrau "defer" da cadeia.
	OutcomeDeferred Outcome = "deferred"
	// OutcomeRejected — sem capacidade intra-fronteira, sem tier elegível dentro da
	// allowlist, ou rejeição permanente do admission. O degrau "reject" (fail-closed).
	OutcomeRejected Outcome = "rejected"
)

// Allowlist é a PORTA da allowlist regional (AOS-058) que o router compõe: dado
// (board, modelo, região), diz se está DENTRO da fronteira de soberania do board.
// O router NUNCA escolhe nem degrada para um par fora desta allowlist. *Policy de
// policy/allowlist satisfá-la via um adaptador fino (ver o wiring do estágio).
type Allowlist interface {
	// Allows reporta se o triplo (board, modelo, região) está na allowlist regional
	// (default-deny). Determinística.
	Allows(board, model, region string) bool
}

// allowNone é o fallback fail-CLOSED (default-deny) usado quando NENHUMA allowlist é
// injectada: nada é elegível. A postura fail-closed não depende de LEMBRAR de ligar
// a allowlist — está alinhada com routingstage.AllowlistFrom(nil), também default-
// deny. Produção liga sempre a allowlist regional real (AOS-058); um teste que
// queira fail-open injecta EXPLICITAMENTE uma allowlist que permita tudo.
type allowNone struct{}

func (allowNone) Allows(_, _, _ string) bool { return false }

// Headroom é o headroom de throughput de um endpoint candidato (folga real de
// TPM/RPM), como racional inteiro do PIOR eixo (à imagem do keypool). Menor
// utilização = mais folga = preferido (menos carregado). Sem floats (determinismo).
type Headroom struct {
	// WorstUsed / WorstLimit é a utilização do eixo MAIS carregado (RPM ou TPM).
	// WorstLimit > 0 (um limite <=0 deve ser normalizado a montante).
	WorstUsed  int64
	WorstLimit int64
	// Saturated marca ausência total de capacidade (o endpoint é descartado).
	Saturated bool
}

// lessLoaded reporta se h tem ESTRITAMENTE mais folga que o (utilização menor),
// por multiplicação cruzada inteira (sem floats). Ambos com WorstLimit > 0.
func (h Headroom) lessLoaded(o Headroom) bool {
	lh, lo := h.WorstLimit, o.WorstLimit
	if lh <= 0 {
		lh = 1
	}
	if lo <= 0 {
		lo = 1
	}
	return h.WorstUsed*lo < o.WorstUsed*lh
}

// LoadProvider é a PORTA de sinal de CARGA por endpoint (provider, região): o
// headroom real de TPM/RPM. Reutiliza o conceito de worstUtil do keypool (AOS-057)
// para o ranking de endpoints/regiões. A impl de referência é [StaticLoadProvider];
// produção liga-o ao estado real de carga.
type LoadProvider interface {
	// Load devolve o headroom do endpoint. Um erro exclui o candidato (fail-closed:
	// sem sinal de carga não se escolhe às cegas).
	Load(ctx context.Context, provider, region string) (Headroom, error)
}

// AdmissionRequest é o pedido de reserva de débito ao admission GLOBAL (ADR-008).
type AdmissionRequest struct {
	Provider        string
	Model           string
	Region          string
	Tenant          string
	Board           string
	EstimatedTokens int64
}

// AdmissionOutcome é a resposta do admission GLOBAL: concedido (débito reservado),
// adiado (sem headroom) ou rejeitado (permanente). O router NUNCA despacha sem
// Granted=true.
type AdmissionOutcome struct {
	Granted          bool
	Rejected         bool
	ReservationID    string
	RetryAfter       time.Duration
	HeadroomTokens   int64
	HeadroomRequests int64
}

// AdmissionCoordinator é a PORTA do admission control GLOBAL (EPIC-03, ADR-008): o
// router COORDENA com o token-bucket distribuído consumindo o headroom por esta
// porta — NÃO o reimplementa. Reservar a montante evita o colapso agregado. O
// adaptador de produção envolve *scheduler.Admission (ver routing/tieradapter).
type AdmissionCoordinator interface {
	// Reserve reserva débito para a chamada. Granted=false com RetryAfter adia;
	// Rejected=true é permanente. Um erro propaga fail-closed (não se despacha).
	Reserve(ctx context.Context, req AdmissionRequest) (AdmissionOutcome, error)
}

// KeyPool é a PORTA de selecção de chave de infra pooled por THROUGHPUT (AOS-057,
// routing/keypool): recebe APENAS (provider, região) — NUNCA a identidade. O
// router compõe-a para escolher a conta MENOS CARREGADA dentro da região escolhida.
// *keypool.Registry satisfá-la.
type KeyPool interface {
	Select(provider, region string) (string, error)
}

// DecisionSink recebe cada [Decision] de roteamento para análise de custo
// POST-HOC e calibração da política (modelo/tier/razão). Opcional; nil = sem sink
// (o span é sempre emitido). É a prova de que cada decisão fica registada.
type DecisionSink interface {
	Record(ctx context.Context, d Decision)
}

// Request é o pedido de roteamento de uma model call.
type Request struct {
	// Board e Tenant são as dimensões de soberania/quota.
	Board  string
	Tenant string
	// Provider é o provedor lógico (ex.: "openai"); a região é a PEDIDA.
	Provider string
	Region   string
	// Capability é a capacidade exigida pela tarefa (frontier p/ raciocínio,
	// básico p/ classificação/extracção).
	Capability tiering.Capability
	// Class é a classe latência/prioridade (interactiva vs batch).
	Class tiering.Class
	// EstimatedTokens é o custo estimado (alimenta a reserva de admissão).
	EstimatedTokens int64
	// Candidates são os endpoints candidatos (KeyID + região) para a selecção
	// menos-carregado e a filtragem de soberania. Vazio ⇒ usa só a região pedida.
	Candidates []sovereignty.Endpoint
}

// Decision é o veredicto do router: o modelo/tier/região/conta escolhidos, o
// resultado (mapeado à cadeia) e a RAZÃO (registada por decisão).
type Decision struct {
	Outcome  Outcome
	Board    string
	Tenant   string
	Provider string
	// Model/Tier/Region/KeyID são a rota escolhida (dentro da fronteira).
	Model  string
	Tier   string
	Region string
	KeyID  string
	// Degraded marca uma rota num tier mais barato (exaustão graciosa/pressão);
	// FromTier/ToTier descrevem a descida (a variância que o Escalonador sela).
	Degraded bool
	FromTier string
	ToTier   string
	// BudgetExhausted marca que o orçamento está ESGOTADO (>=100%) — propagado mesmo
	// quando NÃO há tier mais barato CAPAZ para onde degradar ("exhausted-no-cheaper"),
	// para que a cadeia do Escalonador/chamador possa rejeitar de forma INFORMADA em
	// vez de o GW continuar a gastar em silêncio. É a observabilidade fiel da
	// variância de orçamento (nunca hard-stop cego no router).
	BudgetExhausted bool
	// RetryAfter (defer) é o adiamento aconselhado quando não há headroom global.
	RetryAfter time.Duration
	// ReservationID é a reserva de débito concedida pelo admission (quando Granted).
	ReservationID string
	// Dropped são os candidatos cross-border descartados pela guarda de soberania
	// (a prova estrutural: nunca elegíveis).
	Dropped []sovereignty.Endpoint
	// HeadroomTokens/Requests é o headroom global observado (admissão).
	HeadroomTokens   int64
	HeadroomRequests int64
	// Reason é a razão legível da decisão (registada para análise post-hoc).
	Reason string
}

// Router é o router de produção. Construir com [New]. Stateless (o estado de
// carga/orçamento/admissão vive nas portas); seguro para uso concorrente.
type Router struct {
	ladder    *tiering.Ladder
	guard     *sovereignty.Guard
	allowlist Allowlist
	load      LoadProvider
	admission AdmissionCoordinator
	budget    degradation.BudgetProvider
	policy    degradation.Policy
	keypool   KeyPool
	tracer    agentruntime.Tracer
	sink      DecisionSink
}

// Option configura o [Router].
type Option func(*Router)

// WithGuard injecta a guarda de soberania (AOS-058). Sem ela, a fronteira de cada
// região é a própria região (failover só na mesma região).
func WithGuard(g *sovereignty.Guard) Option {
	return func(r *Router) {
		if g != nil {
			r.guard = g
		}
	}
}

// WithAllowlist injecta a allowlist regional (AOS-058). Sem ela, o router é
// fail-CLOSED (default-deny, alinhado com AllowlistFrom(nil)): nenhum tier/modelo é
// elegível — produção liga SEMPRE a allowlist regional real.
func WithAllowlist(a Allowlist) Option {
	return func(r *Router) {
		if a != nil {
			r.allowlist = a
		}
	}
}

// WithLoadProvider injecta o sinal de carga por endpoint (headroom TPM/RPM).
func WithLoadProvider(l LoadProvider) Option {
	return func(r *Router) {
		if l != nil {
			r.load = l
		}
	}
}

// WithAdmission injecta o coordenador do admission GLOBAL (ADR-008).
func WithAdmission(a AdmissionCoordinator) Option {
	return func(r *Router) {
		if a != nil {
			r.admission = a
		}
	}
}

// WithBudget injecta a porta de orçamento (exaustão graciosa a ~80%).
func WithBudget(b degradation.BudgetProvider) Option {
	return func(r *Router) {
		if b != nil {
			r.budget = b
		}
	}
}

// WithPolicy injecta a política declarativa de degradação (ordem + limiar).
// Default: [degradation.DefaultPolicy].
func WithPolicy(p degradation.Policy) Option {
	return func(r *Router) { r.policy = degradation.NewPolicy(p.Order, p.DegradeThresholdPct) }
}

// WithKeyPool injecta o selector de chave pooled por throughput (AOS-057).
func WithKeyPool(kp KeyPool) Option {
	return func(r *Router) {
		if kp != nil {
			r.keypool = kp
		}
	}
}

// WithTracer injecta a porta OTel (span por decisão). Default: NoopTracer.
func WithTracer(t agentruntime.Tracer) Option {
	return func(r *Router) {
		if t != nil {
			r.tracer = t
		}
	}
}

// WithDecisionSink injecta o sink de decisões (registo post-hoc modelo/tier/razão).
func WithDecisionSink(s DecisionSink) Option {
	return func(r *Router) {
		if s != nil {
			r.sink = s
		}
	}
}

// New constrói o router sobre a escada de tiers (obrigatória). Sem escada não há
// escolha de tier possível.
func New(ladder *tiering.Ladder, opts ...Option) *Router {
	r := &Router{
		ladder:    ladder,
		guard:     sovereignty.NewGuard(),
		allowlist: allowNone{},
		policy:    degradation.DefaultPolicy(),
		tracer:    agentruntime.NoopTracer{},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Route decide o destino da model call. Fluxo determinístico:
//
//  1. SOBERANIA: parte os candidatos em intra-fronteira e cross-border (a guarda
//     descarta os cross-border ANTES de escolher). Sem sobreviventes intra ⇒
//     REJECT (nunca cross-border).
//  2. CARGA: escolhe a região/endpoint MENOS CARREGADO entre os sobreviventes
//     (headroom TPM/RPM via porta) — determinístico, desempate por KeyID.
//  3. TIER (custo/capacidade + latência): o tier mais barato que satisfaz a
//     capacidade, DENTRO da allowlist da região escolhida (o filtro descarta
//     modelos fora da fronteira). Sem tier elegível ⇒ REJECT fail-closed.
//  4. ORÇAMENTO: a ~80% (porta budget) OFERECE degradar para tier mais barato que
//     AINDA satisfaz a capacidade (exaustão graciosa) — nunca hard-stop cego, nunca
//     abaixo da capacidade exigida; a descida respeita a allowlist.
//  5. CONTA: escolhe a chave pooled menos-carregada da região (keypool, AOS-057)
//     ANTES da reserva de admissão — se o pool saturar, ADIA sem ter reservado
//     débito global (a porta de admissão não tem Release; a ordem é a garantia de
//     não haver FUGA de reserva que sature o tecto partilhado — o colapso agregado).
//  6. ADMISSION GLOBAL: reserva débito a montante (ADR-008). Sem headroom ⇒ DEFER
//     (retry_after), nunca despacha. Rejeição permanente ⇒ REJECT.
//  7. REGISTA modelo/tier/razão (span + sink) e devolve a [Decision].
func (r *Router) Route(ctx context.Context, req Request) (Decision, error) {
	ctx, span := r.tracer.StartSpan(ctx, opRouting)
	defer span.End()

	d := Decision{
		Board:    req.Board,
		Tenant:   req.Tenant,
		Provider: req.Provider,
		Region:   req.Region,
	}

	// (1) SOBERANIA — sobreviventes intra-fronteira; cross-border descartados.
	region, dropped, ok := r.chooseRegion(ctx, req)
	d.Dropped = dropped
	if !ok {
		d.Outcome = OutcomeRejected
		if len(dropped) > 0 {
			d.Reason = "rejeitado: capacidade apenas cross-border (soberania) — sem failover fora da fronteira"
		} else {
			d.Reason = "rejeitado: sem endpoint intra-fronteira com capacidade"
		}
		return r.finish(ctx, span, d), nil
	}
	d.Region = region

	// (3) TIER dentro da allowlist da região escolhida (custo/capacidade + latência).
	filter := r.allowlistFilter(req.Board, region)
	tier, ok := r.ladder.Select(tiering.Request{Capability: req.Capability, Class: req.Class}, filter)
	if !ok {
		d.Outcome = OutcomeRejected
		d.Reason = "rejeitado: nenhum tier dentro da allowlist regional satisfaz a capacidade da tarefa"
		return r.finish(ctx, span, d), nil
	}
	d.Model, d.Tier = tier.Model, tier.Name
	d.Reason = "tier mais barato que satisfaz a capacidade (" + classReason(req.Class) + ")"

	// (4) ORÇAMENTO — exaustão graciosa a ~80%: oferece degradar (nunca hard-stop).
	if r.budget != nil {
		st, err := r.budget.Budget(ctx, degradation.BudgetKey{Board: req.Board, Tenant: req.Tenant})
		if err == nil {
			if offer := r.policy.OfferFor(st); offer.Degrade {
				d.BudgetExhausted = offer.Exhausted
				// A degradação NUNCA desce abaixo da capacidade EXIGIDA: o filtro de
				// degradação compõe a allowlist regional com um PISO DE CAPACIDADE
				// (t.Capability >= req.Capability). Como Select já escolheu o tier mais
				// barato que satisfaz a capacidade, só há degrau elegível se existir
				// OUTRO tier igualmente capaz mais barato (ex.: um Fast caro vs. um
				// lento barato na mesma capacidade). Sem degrau CAPAZ mais barato NÃO
				// se degrada — uma tarefa de raciocínio (Frontier) nunca é servida por
				// um tier incapaz.
				if cheaper, ok := r.ladder.Cheaper(tier.Name, r.capableAllowlistFilter(req.Board, region, req.Capability)); ok {
					d.Degraded = true
					d.FromTier, d.ToTier = tier.Name, cheaper.Name
					tier = cheaper
					d.Model, d.Tier = tier.Model, tier.Name
					d.Reason = offer.Reason
				} else if offer.Exhausted {
					// Esgotado (>=100%) e SEM degrau capaz mais barato: propaga o sinal
					// "exhausted-no-cheaper" (distinto de "routed") para o Escalonador
					// poder rejeitar de forma informada — nunca hard-stop cego aqui, mas
					// também nunca um gasto silencioso sem sinal (observabilidade fiel).
					d.Reason = "orcamento esgotado sem tier mais barato capaz (exhausted-no-cheaper): sinal para a cadeia do Escalonador rejeitar de forma informada"
				}
				// Acima do limiar mas sem degrau capaz e não esgotado: mantém o tier
				// capaz (a cadeia do Escalonador decide — nunca hard-stop cego aqui).
			}
		}
	}

	// (5) CONTA — chave pooled menos-carregada da região (keypool, AOS-057).
	// Seleccionada ANTES de reservar o débito de admissão: se o pool saturar, ADIA
	// sem ter reservado qualquer débito global. A porta [AdmissionCoordinator] só tem
	// Reserve (sem Release/Rollback), pelo que o router não conseguiria estruturalmente
	// reverter uma reserva já feita; esta ORDEM é a garantia de que uma saturação
	// (recorrente) do keypool NUNCA deixa reservas-fantasma a esgotar o tecto global
	// partilhado — o colapso agregado que o desenho (ADR-008) previne.
	if r.keypool != nil {
		keyID, err := r.keypool.Select(req.Provider, region)
		if err != nil {
			// Pool saturado/ausente: adia (nunca despacha acima do throughput).
			d.Outcome = OutcomeDeferred
			d.Reason = "adiado: pool de chaves saturado/ausente na regiao (keypool AOS-057)"
			return r.finish(ctx, span, d), nil
		}
		d.KeyID = keyID
	} else if kid := endpointKeyFor(req.Candidates, region); kid != "" {
		d.KeyID = kid
	}

	// (6) ADMISSION GLOBAL — reserva débito a montante (ADR-008): nunca despacha sem.
	if r.admission != nil {
		out, err := r.admission.Reserve(ctx, AdmissionRequest{
			Provider:        req.Provider,
			Model:           tier.Model,
			Region:          region,
			Tenant:          req.Tenant,
			Board:           req.Board,
			EstimatedTokens: req.EstimatedTokens,
		})
		if err != nil {
			return Decision{}, err
		}
		d.HeadroomTokens, d.HeadroomRequests = out.HeadroomTokens, out.HeadroomRequests
		switch {
		case out.Rejected:
			d.Outcome = OutcomeRejected
			d.Reason = "rejeitado: admission global permanente (custo excede o tecto TPM/RPM) — ADR-008"
			return r.finish(ctx, span, d), nil
		case !out.Granted:
			d.Outcome = OutcomeDeferred
			d.RetryAfter = out.RetryAfter
			d.Reason = "adiado: sem headroom global de admissao (coordenacao ADR-008, sem colapso agregado)"
			return r.finish(ctx, span, d), nil
		default:
			d.ReservationID = out.ReservationID
		}
	}

	if d.Degraded {
		d.Outcome = OutcomeDegraded
	} else {
		d.Outcome = OutcomeRouted
	}
	return r.finish(ctx, span, d), nil
}

// chooseRegion aplica a guarda de soberania e escolhe a região/endpoint MENOS
// CARREGADO entre os sobreviventes intra-fronteira. Os cross-border são
// DESCARTADOS (nunca elegíveis — a prova estrutural). Sem candidatos, cai na
// região pedida (fronteira de si própria). Devolve a região escolhida, os
// descartados cross-border e ok=false se não há sobrevivente.
func (r *Router) chooseRegion(ctx context.Context, req Request) (string, []sovereignty.Endpoint, bool) {
	if req.Region == "" {
		return "", nil, false // jurisdição indefinida: fail-closed
	}
	if len(req.Candidates) == 0 {
		// Sem candidatos explícitos: a própria região pedida é a rota (dentro da sua
		// fronteira por definição).
		return req.Region, nil, true
	}
	// Particiona por fronteira usando a API pública da guarda (SameBoundary): os
	// cross-border são descartados ANTES de qualquer ranking por carga.
	var intra []sovereignty.Endpoint
	var cross []sovereignty.Endpoint
	for _, c := range req.Candidates {
		if c.KeyID == "" || c.Region == "" {
			continue // jurisdição/chave indefinida: fail-closed
		}
		if r.guard.SameBoundary(req.Region, c.Region) {
			intra = append(intra, c)
		} else {
			cross = append(cross, c)
		}
	}
	if len(intra) == 0 {
		return "", cross, false
	}
	// Ranking determinístico por CARGA (menos carregado) entre os intra-fronteira,
	// desempate estável por KeyID; se não houver LoadProvider, desempate por KeyID.
	sort.SliceStable(intra, func(i, j int) bool { return intra[i].KeyID < intra[j].KeyID })
	best := intra[0]
	if r.load != nil {
		bestLoad, bestOK := r.loadOf(ctx, req.Provider, best.Region)
		for _, c := range intra[1:] {
			cl, ok := r.loadOf(ctx, req.Provider, c.Region)
			if !ok {
				continue
			}
			if !bestOK || cl.lessLoaded(bestLoad) {
				best, bestLoad, bestOK = c, cl, true
			}
		}
	}
	return best.Region, cross, true
}

// loadOf lê o headroom de um endpoint pela porta, excluindo saturados.
func (r *Router) loadOf(ctx context.Context, provider, region string) (Headroom, bool) {
	h, err := r.load.Load(ctx, provider, region)
	if err != nil || h.Saturated {
		return Headroom{}, false
	}
	return h, true
}

// allowlistFilter constrói o filtro de tiering que descarta qualquer tier cujo
// MODELO não esteja na allowlist regional do board para a região escolhida — a
// prova estrutural de que o router NUNCA escolhe/degrada para fora da fronteira.
func (r *Router) allowlistFilter(board, region string) tiering.Filter {
	return func(t tiering.Tier) bool {
		return r.allowlist.Allows(board, t.Model, region)
	}
}

// capableAllowlistFilter é o filtro da DEGRADAÇÃO por orçamento: além da allowlist
// regional, impõe um PISO DE CAPACIDADE (t.Capability >= capability exigida). Assim
// a exaustão graciosa NUNCA desce abaixo da capacidade que a tarefa exige — um
// degrau mais barato mas INCAPAZ não é elegível. Contrasta com [allowlistFilter]
// (usado na selecção inicial, onde a capacidade já é imposta por Ladder.Select):
// aqui o piso é RE-aplicado porque Cheaper desce por CUSTO sem verificar capacidade.
func (r *Router) capableAllowlistFilter(board, region string, capability tiering.Capability) tiering.Filter {
	return func(t tiering.Tier) bool {
		return t.Capability >= capability && r.allowlist.Allows(board, t.Model, region)
	}
}

// finish emite o span (modelo/tier/razão) e o sink por decisão (registo post-hoc).
func (r *Router) finish(ctx context.Context, span agentruntime.Span, d Decision) Decision {
	span.SetAttribute(AttrRoutingOutcome, string(d.Outcome))
	span.SetAttribute(AttrRoutingModel, d.Model)
	span.SetAttribute(AttrRoutingTier, d.Tier)
	span.SetAttribute(AttrRoutingRegion, d.Region)
	span.SetAttribute(AttrRoutingReason, d.Reason)
	span.SetAttribute(AttrRoutingDegraded, d.Degraded)
	span.SetAttribute(AttrRoutingExhausted, d.BudgetExhausted)
	if d.Degraded {
		span.SetAttribute(AttrRoutingFromTier, d.FromTier)
		span.SetAttribute(AttrRoutingToTier, d.ToTier)
	}
	if d.KeyID != "" {
		span.SetAttribute(AttrRoutingKeyID, d.KeyID)
	}
	if r.sink != nil {
		r.sink.Record(ctx, d)
	}
	return d
}

// classReason descreve o ramo latência-vs-batch na razão registada.
func classReason(c tiering.Class) string {
	if c == tiering.ClassInteractive {
		return "interactivo favorece latencia"
	}
	return "batch tolera tiers lentos/baratos"
}

// endpointKeyFor devolve o KeyID do primeiro candidato da região dada (desempate
// estável), quando não há keypool a escolher a conta.
func endpointKeyFor(candidates []sovereignty.Endpoint, region string) string {
	best := ""
	for _, c := range candidates {
		if c.Region != region || c.KeyID == "" {
			continue
		}
		if best == "" || c.KeyID < best {
			best = c.KeyID
		}
	}
	return best
}
