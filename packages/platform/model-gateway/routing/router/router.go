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
// # SCORING PONDERADO (AOS-269, ADR-021) — OPT-IN por composição
//
// Por omissão o router mantém EXACTAMENTE a ordenação lexicográfica de AOS-059
// descrita acima (carga → tier mais barato capaz → orçamento). Ligando
// [WithScoring], a ORDENAÇÃO dos sobreviventes passa a ser a soma ponderada
// determinística de routing/scoring, com pesos vindos de um artefacto ASSINADO
// (policy/weights). A POSTURA DE COMPATIBILIDADE é deliberada e declarada:
//
//   - sem [WithScoring] ⇒ comportamento lexicográfico INALTERADO. Um nó que hoje
//     arranca sem tabela de pesos continua a rotear exactamente como antes;
//   - com [WithScoring] ⇒ a tabela de pesos é OBRIGATÓRIA. O scorer só se constrói
//     sobre uma tabela verificada (assinatura + trust anchor pinado) e, defesa em
//     profundidade, um scorer não-armado faz o router REJEITAR toda a rota
//     (fail-closed) em vez de cair em pesos implícitos.
//
// COMPOSIÇÃO EM PRODUÇÃO (AOS-280, fecha DEF-271). Este router JÁ ESTÁ composto no
// pipeline de produção do gateway: `modelgateway.NewProduction` encadeia
// `failover.NewStage` → `routingstage.NewStage(router.New(…, WithScoring(…)))` no
// slot de roteamento — o failover impõe a soberania e sela o deny cross-border no
// WORM, este router refina DENTRO dessa fronteira (carga, tier capaz mais barato,
// degradação por orçamento, admissão global e ranking ponderado). A prova é pela
// cadeia real, ao nível do gateway composto (routing_chain_aos280_test.go).
//
// ALCANCE HONESTO DO QUE ISSO ARMA. A cadeia compõe-se quando o deployment DECLARA
// a sua escada de tiers (`RoutingConfig.Tiers`) — o custo/capacidade dos modelos que
// aquele nó pode servir, que nenhuma heurística pode adivinhar sem inventar política
// de qualidade no caminho quente. Sem escada declarada, o slot mantém-se só com o
// failover e este router não corre. É por isso a ESCADA, e não uma opção de scoring,
// que decide se o ranking ponderado tem efeito num deployment concreto.
//
// REGRA 3 DO ADR-021 — LEITURA RATIFICADA E COM EFEITO REAL. A EMENDA 1.1 do ADR-021
// (§5-bis, 2026-08-13, autoridade de dono) fixou que na v1 o scoring é composto POR
// OPÇÃO e que a regra 3 («sem tabela válida o router recusa») se aplica QUANDO o
// scoring está composto — a leitura que a implementação abaixo faz deixou de ser uma
// divergência do implementador. E deixou de estar inerte: no caminho de produção o
// scoring É armado sobre a tabela EMBEBIDA e ASSINADA, cuja verificação falhada
// impede o gateway de ARRANCAR (modelgateway.ErrRoutingWeights), muito antes de
// qualquer rota. Sem `WithScoring` o router mantém, inalterado, o ordenamento
// lexicográfico de AOS-059.
//
// DIVERGÊNCIA DECLARADA (não silenciada). O §5-bis do ADR-021 — autoridade CONGELADA —
// ainda diz, no texto da emenda 1.1, que o scoring «não tem efeito em produção»
// enquanto DEF-271 não fechar. DEF-271 FECHOU (AOS-280) e este parágrafo descreve o
// código que existe; actualizar o ADR é EMENDA de dono, não edição do implementador
// (Carta §6). A divergência está registada com eixo em DEF-280-ADR021 (registo de
// deferimentos), com o texto proposto para a emenda 1.2 — quem ler o ADR e este doc em
// conflito encontra ali qual dos dois está à espera de assinatura.
//
// As GUARDAS continuam PRIMEIRO e não são factores (ADR-021 regra 1): a partição de
// soberania, a allowlist do board e o piso de capacidade correm ANTES do ranking, e
// o scorer só vê sobreviventes intra-fronteira, permitidos e capazes. Um score alto
// NUNCA ressuscita um candidato cross-border — não porque se verifique depois, mas
// porque esse candidato nunca entra na lista pontuada.
//
// # Determinismo
//
// Sem relógio nem aleatoriedade na decisão: carga, orçamento, admissão e os
// factores de scoring são injectados por porta. A selecção é determinística
// (desempate estável) e a aritmética do score é INTEIRA (ponto fixo, zero floats).
package router

import (
	"context"
	"sort"
	"strconv"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/routing/degradation"
	"github.com/aos-ref/platform/model-gateway/routing/scoring"
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

	// Atributos do SCORING PONDERADO (AOS-269, ADR-021 §5 «Observabilidade»): o span
	// model_routing regista o PERFIL de pesos aplicado, a VERSÃO tamper-evident da
	// tabela assinada, o SCORE final e os FACTORES que o compuseram — sem isto a
	// calibração offline (regra 4) não teria de onde partir e uma decisão ponderada
	// seria inauditável.
	AttrRoutingScored       = "aos.routing.scored"
	AttrRoutingScore        = "aos.routing.score"
	AttrRoutingScoreProfile = "aos.routing.score_profile"
	AttrRoutingScoreWeights = "aos.routing.score_weights_version"
	AttrRoutingScoreFactors = "aos.routing.score_factors"
	AttrRoutingScoreDivisor = "aos.routing.score_scale"
	// Candidato que o SCORER elegeu ANTES de a degradação por orçamento o trocar
	// (só presente quando houve troca). Separa o «o que correu» — que é o que
	// aos.routing.score/score_factors descrevem — do «o que o score tinha eleito»,
	// para que a auditoria da troca não tenha de contaminar os campos de calibração.
	AttrRoutingScoredModel = "aos.routing.scored_model"
	AttrRoutingScoredScore = "aos.routing.scored_score"
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

// DecisionSinkFunc adapta uma função à porta [DecisionSink] — o mesmo padrão do
// VarianceSinkFunc da fachada do GW. Existe para que um composition root (ou um
// teste) ligue o registo post-hoc sem declarar um tipo só para isso.
type DecisionSinkFunc func(ctx context.Context, d Decision)

// Record implementa [DecisionSink].
func (f DecisionSinkFunc) Record(ctx context.Context, d Decision) {
	if f == nil {
		return
	}
	f(ctx, d)
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
	// Profile é o PERFIL DE PESOS pedido por ESTA chamada (ADR-021 §1 gap 2: a
	// intenção declarada do consumidor). Vazio ⇒ o perfil composto no scorer.
	// Ignorado no modo lexicográfico (não há pesos). Um perfil DESCONHECIDO é
	// rejeição fail-closed com razão própria — nunca um fallback silencioso.
	Profile string
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
	// Scored marca que a ORDENAÇÃO dos sobreviventes foi feita por SCORING PONDERADO
	// (ADR-021) e não pela composição lexicográfica de AOS-059. Falso ⇒ os campos de
	// scoring abaixo estão vazios (o router corre no modo compatível).
	Scored bool
	// ScoreProfile é o perfil de pesos aplicado (ex.: "balanced") — a INTENÇÃO
	// declarada do consumidor, que substitui a prioridade fixa gravada em código.
	ScoreProfile string
	// WeightsVersion é a identidade versionada e tamper-evident da tabela de pesos
	// ("versão#digest12"): liga a decisão aos pesos EXACTOS em vigor (ADR-012).
	WeightsVersion string
	// Score é a soma ponderada NORMALIZADA do modelo EFECTIVAMENTE despachado
	// (0..scoring.Scale), em aritmética inteira. Coerência exigida pela regra 4 do
	// ADR-021: se uma degradação por orçamento trocar o tier, este score é
	// RE-CALCULADO sobre o tier trocado — a calibração offline nunca recebe
	// factores atribuídos a um modelo que não correu.
	Score int
	// ScoreFactors são os factores normalizados que compuseram [Decision.Score] —
	// e, portanto, do modelo despachado (ver a nota de coerência acima). É o
	// detalhe que a calibração OFFLINE consome pelo DecisionSink.
	ScoreFactors scoring.Factors
	// ScoredModel/ScoredTier/ScoredScore preservam o candidato que o SCORER elegeu
	// ANTES de a degradação por orçamento o trocar. Ficam VAZIOS quando não houve
	// troca. Existem para que a auditoria da troca continue possível sem
	// contaminar os campos de calibração: [Decision.Score]/[Decision.ScoreFactors]
	// descrevem o que correu; estes descrevem o que o score tinha eleito.
	ScoredModel string
	ScoredTier  string
	ScoredScore int
	// Reason é a razão legível da decisão (registada para análise post-hoc). Numa
	// decisão pontuada inclui SEMPRE o perfil, a versão dos pesos, o score e os
	// factores — é esta razão que a pipeline propaga para a variância model_swap
	// (ADR-021 regra 5: o scoring nunca troca em silêncio).
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
	// scoring é o scorer ponderado (ADR-021). NIL ⇒ ordenação lexicográfica de
	// AOS-059 (postura de compatibilidade declarada no doc do pacote).
	scoring *scoring.Scorer
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

// WithScoring ARMA o scoring ponderado determinístico (AOS-269, ADR-021): a partir
// daqui a ORDENAÇÃO dos sobreviventes (região × tier) é a soma ponderada dos
// factores injectados, com os pesos do artefacto ASSINADO (policy/weights), em vez
// da composição lexicográfica de AOS-059.
//
// POSTURA DE COMPATIBILIDADE (RATIFICADA pela emenda 1.1 do ADR-021, §5-bis — ver o
// doc do pacote): o scoring é OPT-IN por composição. Não chamar esta opção mantém o
// router byte-a-byte no comportamento actual — nenhum nó já implantado deixa de
// rotear por não ter tabela de pesos. O composition root de produção
// (modelgateway.NewProduction) CHAMA-A quando o deployment declara a escada de
// tiers. Chamá-la torna a tabela OBRIGATÓRIA: [scoring.NewScorer] já não
// se constrói sem tabela verificada, e um scorer não-armado (valor-zero, tabela
// perdida, perfil de soma zero) faz o router REJEITAR toda a rota — nunca cair em
// pesos implícitos (ADR-021 regra 3).
//
// Um s nil é no-op (mantém o modo lexicográfico), coerente com as restantes opções.
func WithScoring(s *scoring.Scorer) Option {
	return func(r *Router) {
		if s != nil {
			r.scoring = s
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
//
//  2. CARGA: escolhe a região/endpoint MENOS CARREGADO entre os sobreviventes
//     (headroom TPM/RPM via porta) — determinístico, desempate por KeyID.
//
//  3. TIER (custo/capacidade + latência): o tier mais barato que satisfaz a
//     capacidade, DENTRO da allowlist da região escolhida (o filtro descarta
//     modelos fora da fronteira). Sem tier elegível ⇒ REJECT fail-closed.
//
//     Com [WithScoring] composto, (2) e (3) são substituídos por UM ranking
//     PONDERADO sobre o produto (região sobrevivente × tier permitido e capaz) —
//     ver [Router.scoreSurvivors]. As guardas de (1) e os filtros de allowlist/
//     capacidade continuam a correr ANTES, e o scorer só vê o que sobreviveu.
//
//  4. ORÇAMENTO: a ~80% (porta budget) OFERECE degradar para tier mais barato que
//     AINDA satisfaz a capacidade (exaustão graciosa) — nunca hard-stop cego, nunca
//     abaixo da capacidade exigida; a descida respeita a allowlist.
//
//  5. CONTA: escolhe a chave pooled menos-carregada da região (keypool, AOS-057)
//     ANTES da reserva de admissão — se o pool saturar, ADIA sem ter reservado
//     débito global (a porta de admissão não tem Release; a ordem é a garantia de
//     não haver FUGA de reserva que sature o tecto partilhado — o colapso agregado).
//
//  6. ADMISSION GLOBAL: reserva débito a montante (ADR-008). Sem headroom ⇒ DEFER
//     (retry_after), nunca despacha. Rejeição permanente ⇒ REJECT.
//
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

	// (1) SOBERANIA — GUARDA ESTRUTURAL, sempre a primeira: parte os candidatos e
	// DESCARTA os cross-border. É esta ausência (e não uma verificação posterior) que
	// impede qualquer ranking — lexicográfico ou ponderado — de os eleger.
	intra, dropped, ok := r.partition(req)
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

	// (2+3) ORDENAÇÃO dos sobreviventes: ponderada (ADR-021) se o scoring estiver
	// COMPOSTO, lexicográfica (AOS-059) caso contrário. Em AMBOS os ramos as guardas
	// já correram: soberania acima, allowlist + piso de capacidade dentro de cada ramo.
	var (
		region   string
		tier     tiering.Tier
		scoreSuf string // sufixo da razão com perfil/score/factores (vazio no modo lexicográfico)
	)
	if r.scoring != nil {
		var res scoring.Result
		var why scoreOutcome
		region, tier, res, why = r.scoreSurvivors(ctx, req, intra)
		if why != scoreElected {
			d.Outcome = OutcomeRejected
			// A razão é ATRIBUÍVEL à causa real (ADR-010/AOS-011): cada modo de falha
			// do ranking ponderado tem a sua, e NENHUM reutiliza a razão de outro. Um
			// operador que leia "allowlist" tem de poder ir depurar a allowlist.
			switch why {
			case scoreUnarmed:
				// Fail-closed da regra 3: scoring composto SEM tabela de pesos válida/
				// assinada NÃO rota com pesos implícitos — recusa.
				d.Reason = "rejeitado: scoring armado sem tabela de pesos valida/assinada (fail-closed, ADR-021 regra 3)"
			case scoreProfileUnknown:
				d.Reason = "rejeitado: perfil de pesos desconhecido na tabela em vigor (fail-closed, sem queda silenciosa no default)"
			default:
				d.Reason = "rejeitado: nenhum tier dentro da allowlist regional satisfaz a capacidade da tarefa"
			}
			return r.finish(ctx, span, d), nil
		}
		d.Scored = true
		d.ScoreProfile, d.WeightsVersion = res.Profile, res.WeightsVersion
		d.Score, d.ScoreFactors = res.Score, res.Factors
		scoreSuf = " | " + scoring.Reason(res)
	} else {
		region = r.leastLoaded(ctx, req, intra)
		d.Region = region
		filter := r.allowlistFilter(req.Board, region)
		tier, ok = r.ladder.Select(tiering.Request{Capability: req.Capability, Class: req.Class}, filter)
		if !ok {
			d.Outcome = OutcomeRejected
			d.Reason = "rejeitado: nenhum tier dentro da allowlist regional satisfaz a capacidade da tarefa"
			return r.finish(ctx, span, d), nil
		}
	}
	d.Region = region
	d.Model, d.Tier = tier.Model, tier.Name
	d.Reason = "tier mais barato que satisfaz a capacidade (" + classReason(req.Class) + ")" + scoreSuf
	if d.Scored {
		d.Reason = "melhor sobrevivente por score ponderado (" + classReason(req.Class) + ")" + scoreSuf
	}

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
					scoredModel, scoredTier, scoredScore := d.Model, d.Tier, d.Score
					d.Degraded = true
					d.FromTier, d.ToTier = tier.Name, cheaper.Name
					tier = cheaper
					d.Model, d.Tier = tier.Model, tier.Name
					if d.Scored {
						// COERÊNCIA MODELO↔FACTORES (ADR-021 regra 4). O tier trocado NÃO é
						// o que o scorer pontuou: manter Score/ScoreFactors do candidato
						// anterior faria o DecisionSink ensinar à calibração offline o
						// custo/latência/task-fit de um modelo que não foi despachado — e a
						// próxima versão da tabela seria promovida sobre dados corrompidos.
						// RE-PONTUA-SE o tier escolhido com o MESMO scorer (as guardas já
						// correram: capableAllowlistFilter impôs allowlist + piso de
						// capacidade, logo é um Score sobre UM sobrevivente), e o candidato
						// original fica preservado em ScoredModel/ScoredTier/ScoredScore
						// para a auditoria da troca.
						d.ScoredModel, d.ScoredTier, d.ScoredScore = scoredModel, scoredTier, scoredScore
						reres := r.scoring.Score(ctx, taskOf(req), scoring.Candidate{Region: region, Tier: tier})
						d.Score, d.ScoreFactors = reres.Score, reres.Factors
						d.ScoreProfile, d.WeightsVersion = reres.Profile, reres.WeightsVersion
						scoreSuf = " | " + scoring.Reason(reres)
					}
					// A razão da degradação PRESERVA o sufixo de scoring: a variância
					// model_swap a jusante tem de continuar a carregar perfil+score
					// (ADR-021 regra 5), mesmo quando a troca final foi por orçamento.
					d.Reason = offer.Reason + scoreSuf
				} else if offer.Exhausted {
					// Esgotado (>=100%) e SEM degrau capaz mais barato: propaga o sinal
					// "exhausted-no-cheaper" (distinto de "routed") para o Escalonador
					// poder rejeitar de forma informada — nunca hard-stop cego aqui, mas
					// também nunca um gasto silencioso sem sinal (observabilidade fiel).
					d.Reason = "orcamento esgotado sem tier mais barato capaz (exhausted-no-cheaper): sinal para a cadeia do Escalonador rejeitar de forma informada" + scoreSuf
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

// partition é a GUARDA DE SOBERANIA isolada (ADR-021 regra 1: guardas primeiro).
// Parte os candidatos em sobreviventes INTRA-fronteira e DESCARTADOS cross-border,
// SEM os ordenar. Devolvê-los separados é o que permite que o ramo lexicográfico
// (AOS-059) e o ramo ponderado (AOS-269) partilhem EXACTAMENTE a mesma guarda — não
// há um segundo caminho de soberania que se possa esquecer de aplicar.
//
// Sem candidatos explícitos, a própria região pedida é o único sobrevivente (é a
// sua própria fronteira, por definição). ok=false quando não há sobrevivente algum.
func (r *Router) partition(req Request) (intra, cross []sovereignty.Endpoint, ok bool) {
	if req.Region == "" {
		return nil, nil, false // jurisdição indefinida: fail-closed
	}
	if len(req.Candidates) == 0 {
		return []sovereignty.Endpoint{{Region: req.Region}}, nil, true
	}
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
		return nil, cross, false
	}
	// Ordem de entrada normalizada por KeyID: torna o desempate ESTÁVEL e
	// independente da ordem em que o chamador listou os candidatos.
	sort.SliceStable(intra, func(i, j int) bool { return intra[i].KeyID < intra[j].KeyID })
	return intra, cross, true
}

// leastLoaded é o ranking LEXICOGRÁFICO por CARGA de AOS-059 (o modo compatível):
// entre os sobreviventes intra-fronteira escolhe a região do endpoint com mais
// folga real de TPM/RPM, com desempate estável por KeyID. Sem LoadProvider, o
// desempate por KeyID é a própria escolha. Inalterado por AOS-269.
func (r *Router) leastLoaded(ctx context.Context, req Request, intra []sovereignty.Endpoint) string {
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
	return best.Region
}

// scoreOutcome descreve porque é que [Router.scoreSurvivors] não elegeu candidato.
// Existe para que a razão registada seja ATRIBUÍVEL: cada modo de falha tem a sua
// causa e a sua string, e nenhum herda a de outro (ADR-010/AOS-011 — um deny que
// culpa a peça errada manda o operador depurar a coisa errada).
type scoreOutcome int

const (
	// scoreElected — candidato eleito (o único caso de sucesso).
	scoreElected scoreOutcome = iota
	// scoreUnarmed — scoring composto sem tabela de pesos válida/assinada (regra 3).
	scoreUnarmed
	// scoreProfileUnknown — o perfil pedido não existe na tabela em vigor.
	scoreProfileUnknown
	// scoreNoSurvivor — as GUARDAS (soberania/allowlist/capacidade) não deixaram
	// candidato algum. É a ÚNICA falha atribuível à allowlist/capacidade.
	scoreNoSurvivor
)

// scoreSurvivors é o ranking PONDERADO de AOS-269 (ADR-021). Constrói o conjunto de
// sobreviventes como o produto (região intra-fronteira × tier da escada) FILTRADO
// pelas TRÊS guardas que o ADR-021 regra 1 enumera — e só por essas:
//
//   - soberania — já aplicada por [Router.partition] (só entram regiões intra);
//   - allowlist do board — [Allowlist.Allows] por (board, modelo, região): um
//     modelo fora da allowlist NUNCA é construído como candidato;
//   - piso de capacidade — t.Capability >= req.Capability: um tier incapaz NUNCA é
//     construído como candidato.
//
// A SATURAÇÃO NÃO É GUARDA — é FACTOR, e é deliberado. As três guardas acima são
// invariantes de FRONTEIRA: violá-las é ilegal, e a violação é permanente. A
// saturação (ou um erro transitório de leitura de carga) é PRESSÃO: tem degrau
// próprio na cadeia do ADR-008 (shed→defer→degradar→rejeitar) e resolve-se com
// retry_after, não com um drop. Descartar aqui a região saturada faria uma região
// única saturada colapsar em `cands` vazio ⇒ REJEIÇÃO PERMANENTE — e com a razão
// da allowlist, que está intacta. O modo lexicográfico não faz isso ([leastLoaded]
// fica-se por intra[0] mesmo saturado, e o keypool/admission produzem o DEFER);
// armar o scoring não pode converter «saturação ⇒ adia» em «saturação ⇒ dropa».
// A normalização prevista pela regra 2 («sinal ausente resolve pelo lado seguro»)
// é o VALOR 0 que [scoring.HeadroomFactor] já devolve para saturado/erro — não a
// exclusão do candidato.
//
// Só DEPOIS é que o scorer ordena. Um peso, por maior que seja, não tem sobre que
// candidato actuar se esse candidato não existe — é a regra 1 imposta por ausência.
func (r *Router) scoreSurvivors(ctx context.Context, req Request, intra []sovereignty.Endpoint) (string, tiering.Tier, scoring.Result, scoreOutcome) {
	if !r.scoring.Armed() {
		return "", tiering.Tier{}, scoring.Result{}, scoreUnarmed
	}
	if !r.scoring.HasProfile(req.Profile) {
		return "", tiering.Tier{}, scoring.Result{}, scoreProfileUnknown
	}
	// Regiões sobreviventes únicas, por ordem determinística (a slice já vem
	// normalizada por KeyID; a deduplicação preserva a primeira ocorrência).
	regions := make([]string, 0, len(intra))
	seen := make(map[string]struct{}, len(intra))
	for _, e := range intra {
		if _, dup := seen[e.Region]; dup {
			continue
		}
		seen[e.Region] = struct{}{}
		regions = append(regions, e.Region)
	}
	sort.Strings(regions)

	cands := make([]scoring.Candidate, 0, len(regions)*4)
	for _, reg := range regions {
		for _, t := range r.ladder.Tiers() {
			if t.Capability < req.Capability {
				continue // piso de capacidade (guarda)
			}
			if !r.allowlist.Allows(req.Board, t.Model, reg) {
				continue // allowlist do board (guarda, default-deny)
			}
			cands = append(cands, scoring.Candidate{Region: reg, Tier: t})
		}
	}
	best, res, ok := r.scoring.Best(ctx, taskOf(req), cands)
	if !ok {
		return "", tiering.Tier{}, scoring.Result{}, scoreNoSurvivor
	}
	return best.Region, best.Tier, res, scoreElected
}

// taskOf projecta o [Request] no [scoring.Task] — o input do lado da TAREFA da
// função pura de scoring. Existe num só sítio para que a re-pontuação depois da
// degradação por orçamento use EXACTAMENTE a mesma tarefa (incluindo o perfil) que
// a pontuação inicial: dois construtores divergentes dariam dois scores que se
// diriam comparáveis e não seriam.
func taskOf(req Request) scoring.Task {
	return scoring.Task{
		Board:      req.Board,
		Tenant:     req.Tenant,
		Provider:   req.Provider,
		Capability: req.Capability,
		Class:      req.Class,
		Profile:    req.Profile,
	}
}

// HeadroomReaderFrom adapta a porta [LoadProvider] JÁ EXISTENTE do router à porta
// mínima scoring.LoadReader do factor de headroom. É o ponto onde se declara que o
// scoring NÃO tem uma fonte de carga própria: reutiliza o mesmo headroom TPM/RPM
// (keypool AOS-057 / LoadProvider AOS-059) que o modo lexicográfico já usa.
func HeadroomReaderFrom(lp LoadProvider) scoring.LoadReader {
	if lp == nil {
		return nil
	}
	return loadReaderAdapter{lp: lp}
}

type loadReaderAdapter struct{ lp LoadProvider }

func (a loadReaderAdapter) Load(ctx context.Context, provider, region string) (int64, int64, bool, error) {
	h, err := a.lp.Load(ctx, provider, region)
	if err != nil {
		return 0, 0, false, err
	}
	return h.WorstUsed, h.WorstLimit, h.Saturated, nil
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
	span.SetAttribute(AttrRoutingScored, d.Scored)
	if d.Scored {
		// ADR-021 §5: perfil, versão dos pesos, score e FACTORES no span — sem eles a
		// decisão ponderada seria um número sem proveniência e a calibração offline
		// (regra 4) não teria de onde partir.
		span.SetAttribute(AttrRoutingScoreProfile, d.ScoreProfile)
		span.SetAttribute(AttrRoutingScoreWeights, d.WeightsVersion)
		span.SetAttribute(AttrRoutingScore, strconv.Itoa(d.Score))
		span.SetAttribute(AttrRoutingScoreDivisor, strconv.Itoa(scoring.Scale))
		span.SetAttribute(AttrRoutingScoreFactors, d.ScoreFactors.String())
		if d.ScoredModel != "" {
			// Houve degradação DEPOIS de pontuar: o span diz as duas coisas — o que o
			// score elegeu e o que efectivamente correu (os atributos acima).
			span.SetAttribute(AttrRoutingScoredModel, d.ScoredModel)
			span.SetAttribute(AttrRoutingScoredScore, strconv.Itoa(d.ScoredScore))
		}
	}
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
