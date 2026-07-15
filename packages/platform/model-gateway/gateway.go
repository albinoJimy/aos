// Package modelgateway é a FACHADA canónica do Model Gateway (GW) do AOS
// (AOS-055, tecnica/06 §3). Implementa a porta única compatível OpenAI
// ([port.Gateway]) como o gate OBRIGATÓRIO de toda a model call: cada chamada
// atravessa a pipeline determinística (auth → allowlist → roteamento →
// validação de layout de cache → metering), obtém a credencial de infra via
// [adapters.CredentialSource] server-side, invoca o adaptador de provider (o
// ÚNICO ponto que fala com um provedor) e emite um span OTel GenAI ('chat') com
// gen_ai.request.model + gen_ai.usage.*.
//
// # Invariantes fundadoras
//
//   - Nenhum caminho fora do GW invoca um provider (imposto pelo arch-lint do
//     pacote model-gateway/archlint).
//   - Um swap de modelo/provider é EVENTO DE VARIÂNCIA explícito (nunca silencioso),
//     registado por [VarianceSink] para o replay fiel (ADR-010).
//   - Sem segredos em código/logs/spans: a chave entra por porta e nunca é
//     colocada num atributo de span (ADR-006).
//   - Determinismo: relógio e gerador de IDs injectáveis; sem time.Now/rand na
//     decisão nem na serialização.
//
// A implementação real de cada estágio (identidade, allowlist, roteamento, cache,
// custo) é dos tickets AOS-057..062; aqui os estágios são pass-through.
package modelgateway

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/metering/attribution"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/port"
)

// attrResponseModel é a chave semconv OTel GenAI do modelo EFECTIVO da resposta
// (gen_ai.response.model). O agent-runtime só define gen_ai.request.model
// (AttrRequestModel); o GW acrescenta a chave de resposta para tornar um swap de
// modelo observável no span (request.model != response.model).
const attrResponseModel = "gen_ai.response.model"

// opEmbeddings é o nome de operação OTel GenAI de uma chamada de embeddings. O
// agent-runtime só define OpChat; o GW acrescenta esta constante para que o span
// de embeddings seja ABERTO com a sua própria operação (e não sob "chat"), de
// modo que um tracer que indexe pela operação de StartSpan classifique embeddings
// correctamente (consistente com AttrOperationName).
const opEmbeddings = "embeddings"

// VarianceEvent regista uma variância explícita — tipicamente um swap de
// modelo/provider face ao pedido. Nunca silencioso: alimenta o replay fiel e a
// RCA (ADR-010). O tipo espelha o "model_downgraded"/swap descrito em tecnica/06 §3.
type VarianceEvent struct {
	// Kind identifica a variância ("model_swap", "provider_swap", "region_swap").
	Kind string
	// RequestedModel/ResolvedModel são o modelo pedido e o efectivo.
	RequestedModel string
	ResolvedModel  string
	// RequestedProvider/Provider são o provedor pedido (default do adaptador) e o
	// efectivo. Um provider_swap (mesmo modelo, provedor diferente) é observável.
	RequestedProvider string
	Provider          string
	// RequestedRegion/ResolvedRegion são a região pedida e a efectiva. Um
	// region_swap denota um failover cross-border (preocupação de soberania).
	RequestedRegion string
	ResolvedRegion  string
	// Reason descreve porquê (razão do estágio de roteamento).
	Reason string
	// Principal e Board tornam a variância atribuível.
	Principal string
	Board     string
}

// VarianceSink recebe eventos de variância. A implementação real (append-only
// no Event Store) é de outro ticket; aqui os testes fornecem um sink em memória.
type VarianceSink interface {
	Emit(ctx context.Context, ev VarianceEvent)
}

// VarianceSinkFunc adapta uma função a [VarianceSink] (útil em testes e wiring).
type VarianceSinkFunc func(ctx context.Context, ev VarianceEvent)

// Emit implementa [VarianceSink].
func (f VarianceSinkFunc) Emit(ctx context.Context, ev VarianceEvent) { f(ctx, ev) }

// nopVariance descarta eventos (default).
type nopVariance struct{}

func (nopVariance) Emit(context.Context, VarianceEvent) {}

// KeyPool selecciona a chave de infra pooled por THROUGHPUT (AOS-057,
// routing/keypool). A ASSINATURA recebe APENAS (provider, região) — NUNCA o
// principal: é a prova estrutural de que a escolha da chave está DESACOPLADA da
// identidade. *keypool.Registry satisfá-la.
type KeyPool interface {
	Select(provider, region string) (string, error)
}

// Gateway é a fachada do GW. Stateless: não detém estado autoritativo (buckets,
// métricas são externos, por porta). Construir com [New].
type Gateway struct {
	pipe     *pipeline.Pipeline
	adapter  adapters.Adapter
	creds    adapters.CredentialSource
	tracer   agentruntime.Tracer
	variance VarianceSink
	clock    func() time.Time
	newID    func() string
	region   string

	// --- AOS-057: identidade por principal vs chaves de infra pooled ---
	// authn, quando definido, substitui o estágio auth-principal pass-through pela
	// validação REAL do token do principal (fail-closed).
	authn pipeline.Stage
	// --- AOS-058: allowlist regional + guarda de soberania ---
	// allowlist, quando definido, substitui o estágio allowlist-regional pass-through
	// pela allowlist default-deny por board (fail-closed) — a soberania imposta por
	// desenho antes do roteamento.
	allowlist pipeline.Stage
	// keypool, quando definido, escolhe a chave de infra pooled por throughput,
	// DESACOPLADA da identidade — o KeyID não-secreto entra na atribuição.
	keypool KeyPool
	// --- AOS-059: router cost/load-aware + model tiering ---
	// routing, quando definido, substitui o estágio de roteamento pass-through
	// (IdentityRouting) pela escolha REAL de modelo/tier/região/conta dentro da
	// fronteira de soberania, coordenada com o admission global (ADR-008).
	routing pipeline.Stage
	// attribution, quando definido, regista principal/modelo/região por chamada,
	// liga ao span OTel GenAI e sela no audit WORM. Nunca "o pool".
	attribution *attribution.Recorder
}

// Compile-time: o Gateway satisfaz a porta compatível OpenAI.
var _ port.Gateway = (*Gateway)(nil)

// Option configura o [Gateway].
type Option func(*Gateway)

// WithPipeline injecta uma pipeline concreta (os tickets de extensão substituem
// estágios). Default: [pipeline.NewDefault] (todos pass-through).
func WithPipeline(p *pipeline.Pipeline) Option { return func(g *Gateway) { g.pipe = p } }

// WithCredentialSource injecta a porta de credenciais (ADR-006). Default: uma
// fonte estática vazia (falha fail-closed em qualquer chamada — segredo é
// obrigatório).
func WithCredentialSource(cs adapters.CredentialSource) Option {
	return func(g *Gateway) { g.creds = cs }
}

// WithTracer injecta o [agentruntime.Tracer] zero-dep. Default: NoopTracer.
func WithTracer(t agentruntime.Tracer) Option { return func(g *Gateway) { g.tracer = t } }

// WithVarianceSink injecta o sink de variância. Default: descarta.
func WithVarianceSink(s VarianceSink) Option { return func(g *Gateway) { g.variance = s } }

// WithClock injecta o relógio determinista (timestamps de resposta). Default:
// time.Now.
func WithClock(clock func() time.Time) Option { return func(g *Gateway) { g.clock = clock } }

// WithIDGenerator injecta o gerador determinista de IDs de resposta. Default: um
// contador estável, para reprodutibilidade.
func WithIDGenerator(f func() string) Option { return func(g *Gateway) { g.newID = f } }

// WithDefaultRegion define a região usada quando o pedido não a especifica.
func WithDefaultRegion(region string) Option { return func(g *Gateway) { g.region = region } }

// WithAuthnStage injecta o estágio auth-principal REAL de AOS-057 (validação
// fail-closed do token do principal + autoridade utilizador ∩ classe + policy-as-code).
// Substitui o pass-through de AOS-055. Default: pass-through.
func WithAuthnStage(s pipeline.Stage) Option { return func(g *Gateway) { g.authn = s } }

// WithAllowlistStage injecta o estágio allowlist-regional REAL de AOS-058: a
// allowlist default-deny por board (versionada + assinada) que recusa fail-closed um
// modelo fora da fronteira de soberania e regista a decisão por chamada (span +
// WORM). Substitui o pass-through de AOS-055. Default: pass-through.
func WithAllowlistStage(s pipeline.Stage) Option { return func(g *Gateway) { g.allowlist = s } }

// WithKeyPool injecta o selector de chave de infra pooled por throughput
// (AOS-057). A escolha é DESACOPLADA da identidade (a porta só recebe
// provider/região). Default: sem keypool (a credencial é obtida por provider/região
// como em AOS-055/056).
func WithKeyPool(kp KeyPool) Option { return func(g *Gateway) { g.keypool = kp } }

// WithRoutingStage injecta o estágio de roteamento REAL de AOS-059 (router
// cost/load-aware + model tiering): escolhe modelo/tier/região/conta dentro da
// fronteira de soberania (AOS-058), coordenado com o admission global (ADR-008), e
// regista modelo/tier/razão por decisão. Substitui o pass-through IdentityRouting
// de AOS-055. Um degrade fica observável como variância explícita (recordVariance).
// Default: pass-through.
func WithRoutingStage(s pipeline.Stage) Option { return func(g *Gateway) { g.routing = s } }

// WithAttribution injecta o recorder de atribuição por chamada (AOS-057): regista
// principal/modelo/região, anota o span e sela no audit WORM. Default: sem
// atribuição (o registo entra com AOS-057 wired).
func WithAttribution(r *attribution.Recorder) Option { return func(g *Gateway) { g.attribution = r } }

// New constrói o Gateway sobre um adaptador de provider. O adaptador é o ÚNICO
// componente que fala com um provedor; todo o resto passa pela pipeline.
func New(adapter adapters.Adapter, opts ...Option) *Gateway {
	g := &Gateway{
		adapter:  adapter,
		pipe:     pipeline.NewDefault(),
		creds:    adapters.NewStaticCredentialSource(),
		tracer:   agentruntime.NoopTracer{},
		variance: nopVariance{},
		clock:    time.Now,
	}
	// O gerador default de IDs é DETERMINISTA e concorrente-seguro: o Gateway é
	// stateless/partilhável, pelo que o contador é guardado por mutex para não
	// produzir IDs duplicados nem uma data race sob Chat/Embeddings concorrentes
	// (correlação de replay/audit depende de IDs únicos).
	var (
		seqMu sync.Mutex
		seq   int
	)
	g.newID = func() string {
		seqMu.Lock()
		seq++
		n := seq
		seqMu.Unlock()
		return "gw-" + strconv.Itoa(n)
	}
	for _, o := range opts {
		o(g)
	}
	if g.pipe == nil {
		g.pipe = pipeline.NewDefault()
	}
	// AOS-057: se um estágio auth-principal real foi injectado, substitui-o na
	// pipeline preservando a ORDEM fixa e os restantes estágios (pass-through fica
	// para os campos não sobrepostos por outros tickets).
	if g.authn != nil {
		st := g.pipe.Stages()
		st.Auth = g.authn
		g.pipe = pipeline.New(st)
	}
	// AOS-058: se um estágio allowlist-regional real foi injectado, substitui-o na
	// pipeline preservando a ORDEM fixa (allowlist corre a seguir ao auth, antes do
	// roteamento — o 1.º ramo do diagrama de decisão de tecnica/06 §5).
	if g.allowlist != nil {
		st := g.pipe.Stages()
		st.Allowlist = g.allowlist
		g.pipe = pipeline.New(st)
	}
	// AOS-059: se um estágio de roteamento real foi injectado, substitui o
	// IdentityRouting pass-through preservando a ORDEM fixa (roteamento corre a
	// seguir à allowlist, antes do cache-layout — o 3.º estágio da pipeline).
	if g.routing != nil {
		st := g.pipe.Stages()
		st.Routing = g.routing
		g.pipe = pipeline.New(st)
	}
	return g
}

// PortVersion implementa [port.Gateway]: a versão SemVer do contrato.
func (g *Gateway) PortVersion() string { return port.Version }

// Chat implementa [port.Gateway]. Atravessa a pipeline, obtém a credencial,
// invoca o adaptador e emite o span 'chat'. Fail-closed em qualquer recusa.
func (g *Gateway) Chat(ctx context.Context, req port.ChatRequest) (port.ChatResponse, error) {
	req, err := req.Normalize()
	if err != nil {
		return port.ChatResponse{}, err
	}
	ctx, span := g.tracer.StartSpan(ctx, agentruntime.OpChat)
	defer span.End()
	span.SetAttribute(agentruntime.AttrOperationName, agentruntime.OpChat)
	span.SetAttribute(agentruntime.AttrRequestModel, req.Model)

	ex := g.newExchange(pipeline.OpChat, req.Principal, req.Board, req.Model, g.regionOf(req.Region))

	var resp port.ChatResponse
	runErr := g.pipe.Execute(ctx, ex, func(ctx context.Context, ex *pipeline.Exchange) error {
		cred, err := g.credential(ctx, ex)
		if err != nil {
			return err
		}
		// AOS-057: sela a atribuição (principal/modelo/região/KeyID/policyVersion) no
		// WORM ANTES de o provider ser invocado — audit-before-effect (ADR-010). Todos
		// os campos já estão resolvidos (auth → routing → keypool) e NÃO dependem do
		// usage. Fail-closed: se a selagem falhar, o EFEITO (a model call) NÃO ocorre e
		// nenhuma lacuna silenciosa entra na cadeia tamper-evident.
		if err := g.attribute(ctx, span, ex); err != nil {
			return err
		}
		g.annotateAllowlist(span, ex)
		req.Model = ex.ResolvedModel
		r, err := g.adapter.Chat(ctx, req, cred)
		if err != nil {
			return err
		}
		resp = r
		ex.Usage = r.Usage
		return nil
	})
	if runErr != nil {
		span.SetAttribute(agentruntime.AttrErrorType, errType(runErr))
		return port.ChatResponse{}, runErr
	}

	g.finishResponse(ctx, span, ex, &resp)
	return resp, nil
}

// ChatStream implementa [port.Gateway] para streaming. Corre os estágios
// PRÉ-invocação (auth → allowlist → routing → cache-layout) e abre o stream do
// adaptador; o estágio de metering é ADIADO para o fim do stream, quando o usage
// final está disponível (ver [pipeline.Pipeline.Meter]) — caso contrário o
// metering (custo de AOS-062) correria sobre zero tokens. O span 'chat' abre aqui
// e fecha quando o stream é drenado, fechado (Close) OU o contexto é cancelado
// (backstop anti-fuga). O chamador DEVE fechar o stream (Close) quando o abandona.
func (g *Gateway) ChatStream(ctx context.Context, req port.ChatRequest) (port.ChatStream, error) {
	req, err := req.Normalize()
	if err != nil {
		return nil, err
	}
	ctx, span := g.tracer.StartSpan(ctx, agentruntime.OpChat)
	span.SetAttribute(agentruntime.AttrOperationName, agentruntime.OpChat)
	span.SetAttribute(agentruntime.AttrRequestModel, req.Model)

	ex := g.newExchange(pipeline.OpChat, req.Principal, req.Board, req.Model, g.regionOf(req.Region))

	var inner port.ChatStream
	runErr := g.pipe.ExecutePreInvoke(ctx, ex, func(ctx context.Context, ex *pipeline.Exchange) error {
		cred, err := g.credential(ctx, ex)
		if err != nil {
			return err
		}
		// AOS-057: sela a atribuição ANTES de abrir o stream (o EFEITO). Os campos de
		// atribuição (principal/modelo/região/KeyID) NÃO dependem do usage, pelo que o
		// audit-before-effect é alcançável também no streaming — e aqui é FAIL-CLOSED:
		// se a selagem falhar, o stream NÃO é aberto nem devolvido ao chamador, e não
		// fica uma lacuna silenciosa na cadeia WORM (fecha a fuga do best-effort
		// anterior). Só o metering (dependente do usage) é adiado para o fim do stream.
		if err := g.attribute(ctx, span, ex); err != nil {
			return err
		}
		g.annotateAllowlist(span, ex)
		req.Model = ex.ResolvedModel
		s, err := g.adapter.ChatStream(ctx, req, cred)
		if err != nil {
			return err
		}
		inner = s
		return nil
	})
	if runErr != nil {
		span.SetAttribute(agentruntime.AttrErrorType, errType(runErr))
		span.End()
		return nil, runErr
	}

	g.recordVariance(ctx, ex)
	// Envolve o stream: no fim (EOF/Close/ctx cancelado) corre o metering com o
	// usage final e fecha o span EXACTAMENTE uma vez.
	ms := &meteredStream{
		inner:    inner,
		finished: make(chan struct{}),
		onEnd: func(usage port.Usage) {
			ex.Usage = usage
			// A atribuição já foi selada (fail-closed) ANTES de o stream abrir; aqui só
			// corre o metering, dependente do usage final (custo USD de AOS-062).
			if err := g.pipe.Meter(ctx, ex); err != nil {
				span.SetAttribute(agentruntime.AttrErrorType, errType(err))
			}
			span.SetAttribute(agentruntime.AttrRequestModel, ex.RequestedModel)
			span.SetAttribute(attrResponseModel, ex.ResolvedModel)
			setUsageAttrs(span, usage)
			span.End()
		},
	}
	// Backstop anti-fuga: um consumidor que abandone o stream sem drenar nem
	// fechar deixaria o span aberto para sempre. O cancelamento do ctx fecha-o
	// (best-effort com o usage acumulado); se o stream terminar normalmente, a
	// goroutine sai por [meteredStream.finished] sem fugir.
	go func() {
		select {
		case <-ctx.Done():
			ms.end(ms.lastUsage())
		case <-ms.finished:
		}
	}()
	return ms, nil
}

// Embeddings implementa [port.Gateway]. Atravessa a MESMA pipeline determinística.
func (g *Gateway) Embeddings(ctx context.Context, req port.EmbeddingsRequest) (port.EmbeddingsResponse, error) {
	req, err := req.Normalize()
	if err != nil {
		return port.EmbeddingsResponse{}, err
	}
	ctx, span := g.tracer.StartSpan(ctx, opEmbeddings)
	defer span.End()
	span.SetAttribute(agentruntime.AttrOperationName, opEmbeddings)
	span.SetAttribute(agentruntime.AttrRequestModel, req.Model)

	ex := g.newExchange(pipeline.OpEmbeddings, req.Principal, req.Board, req.Model, g.regionOf(req.Region))

	var resp port.EmbeddingsResponse
	runErr := g.pipe.Execute(ctx, ex, func(ctx context.Context, ex *pipeline.Exchange) error {
		cred, err := g.credential(ctx, ex)
		if err != nil {
			return err
		}
		// AOS-057: audit-before-effect — sela a atribuição ANTES do invoke do provider
		// (fail-closed, ADR-010). Ver [Gateway.Chat].
		if err := g.attribute(ctx, span, ex); err != nil {
			return err
		}
		g.annotateAllowlist(span, ex)
		req.Model = ex.ResolvedModel
		r, err := g.adapter.Embeddings(ctx, req, cred)
		if err != nil {
			return err
		}
		resp = r
		ex.Usage = r.Usage
		return nil
	})
	if runErr != nil {
		span.SetAttribute(agentruntime.AttrErrorType, errType(runErr))
		return port.EmbeddingsResponse{}, runErr
	}
	g.recordVariance(ctx, ex)
	setUsageAttrs(span, resp.Usage)
	span.SetAttribute(attrResponseModel, ex.ResolvedModel)
	return resp, nil
}

// newExchange constrói o Exchange com o relógio injectado. RequestedProvider é
// semeado com o provedor do adaptador configurado (o provedor "pedido" por
// default); se o roteamento resolver outro provedor, o GW regista provider_swap.
func (g *Gateway) newExchange(op pipeline.Op, principal, board, model, region string) *pipeline.Exchange {
	ex := &pipeline.Exchange{
		Op:                op,
		Principal:         principal,
		Board:             board,
		RequestedModel:    model,
		RequestedProvider: g.adapter.Provider(),
		RequestedRegion:   region,
	}
	pipeline.WithClock(ex, g.clock)
	return ex
}

// credential resolve o provider efectivo e obtém a credencial via porta. O
// provider resolvido, se o roteamento o deixou vazio, é o do adaptador
// configurado (a fachada é dona da escolha de adaptador nesta fase).
func (g *Gateway) credential(ctx context.Context, ex *pipeline.Exchange) (adapters.Credential, error) {
	if ex.ResolvedProvider == "" {
		ex.ResolvedProvider = g.adapter.Provider()
	}
	if ex.ResolvedRegion == "" {
		ex.ResolvedRegion = ex.RequestedRegion
	}
	// AOS-057: se há um keypool, escolhe a chave de infra pooled por THROUGHPUT —
	// DESACOPLADA da identidade (a porta só recebe provider/região; o principal do
	// Exchange NÃO é passado). O KeyID não-secreto entra na atribuição; o segredo
	// concreto vem sempre da CredentialSource (broker/vault, ADR-006). Fail-closed
	// se o pool estiver saturado ou ausente.
	if g.keypool != nil {
		keyID, err := g.keypool.Select(ex.ResolvedProvider, ex.ResolvedRegion)
		if err != nil {
			return adapters.Credential{}, err
		}
		ex.KeyID = keyID
	}
	return g.creds.Fetch(ctx, ex.ResolvedProvider, ex.ResolvedRegion)
}

// finishResponse preenche campos deterministas da resposta, regista variância e
// emite os atributos de usage no span.
func (g *Gateway) finishResponse(ctx context.Context, span agentruntime.Span, ex *pipeline.Exchange, resp *port.ChatResponse) {
	if resp.ID == "" {
		resp.ID = g.newID()
	}
	if resp.Created == 0 {
		resp.Created = g.clock().Unix()
	}
	if resp.Object == "" {
		resp.Object = "chat.completion"
	}
	if resp.Model == "" {
		resp.Model = ex.ResolvedModel
	}
	g.recordVariance(ctx, ex)
	span.SetAttribute(attrResponseModel, resp.Model)
	setUsageAttrs(span, resp.Usage)
}

// attribute produz o registo de atribuição da chamada (AOS-057): principal
// (utilizador, agente), modelo e região — SEMPRE, seja qual for a chave de infra
// (KeyID não-secreto). Anota o span OTel GenAI e sela no audit WORM. No-op se não
// há recorder configurado. NUNCA regista o segredo nem "o pool".
func (g *Gateway) attribute(ctx context.Context, span agentruntime.Span, ex *pipeline.Exchange) error {
	if g.attribution == nil {
		return nil
	}
	rec := attribution.Record{
		UserID:          ex.PrincipalUser,
		AgentID:         ex.PrincipalAgent,
		AgentClass:      ex.AgentClass,
		HumanRoot:       ex.HumanRoot,
		DelegationChain: toAttrHops(ex.DelegationChain),
		Model:           ex.ResolvedModel,
		Region:          ex.ResolvedRegion,
		KeyID:           ex.KeyID,
		Operation:       string(ex.Op),
		PolicyVersion:   ex.PolicyVersion,
		Timestamp:       g.clock(),
	}
	if err := g.attribution.Record(ctx, span, rec); err != nil {
		return &attributionError{err: err}
	}
	return nil
}

// annotateAllowlist anota o span com a decisão do estágio allowlist-regional
// (AOS-058) a partir do rasto de decisões do Exchange — os estágios não recebem o
// span, pelo que a anotação por chamada (resultado, modelo, região) é feita aqui,
// no caminho de allow (num deny o span já leva AttrErrorType="stage:allowlist-regional"
// e a selagem WORM regista o motivo). No-op se o estágio allowlist real não correu.
func (g *Gateway) annotateAllowlist(span agentruntime.Span, ex *pipeline.Exchange) {
	if span == nil {
		return
	}
	for _, d := range ex.Decisions {
		if d.Stage != "allowlist-regional" || d.Result == "" {
			continue
		}
		span.SetAttribute(allowlist.AttrAllowlistResult, d.Result)
		span.SetAttribute(allowlist.AttrBoard, ex.Board)
		span.SetAttribute(allowlist.AttrModel, ex.RequestedModel)
		span.SetAttribute(allowlist.AttrRegion, ex.RequestedRegion)
		return
	}
}

// attributionError marca uma falha de selagem/registo da atribuição. É distinta
// de um erro de estágio ou de provider para que [errType] a classifique como
// "attribution_error" no span, seja qual for o caminho (Chat/Embeddings/stream).
type attributionError struct{ err error }

func (e *attributionError) Error() string {
	return "atribuicao nao selada (fail-closed): " + e.err.Error()
}
func (e *attributionError) Unwrap() error { return e.err }

// toAttrHops projecta a cadeia de delegação do Exchange (forma primitiva do
// pipeline) para os hops do registo de atribuição.
func toAttrHops(hops []pipeline.DelegationHop) []attribution.Hop {
	if len(hops) == 0 {
		return nil
	}
	out := make([]attribution.Hop, len(hops))
	for i, h := range hops {
		out[i] = attribution.Hop{Sub: h.Sub, ActAs: h.ActAs}
	}
	return out
}

// recordVariance emite um evento de variância explícito por cada dimensão em que
// o efectivo diverge do pedido — modelo, provedor E região — nunca silencioso
// (ADR-010). Um roteamento que mantenha o nome do modelo mas troque de provedor,
// ou um failover cross-border de região (soberania), fica assim observável.
func (g *Gateway) recordVariance(ctx context.Context, ex *pipeline.Exchange) {
	reason := g.routingReason(ex)
	emit := func(kind string) {
		g.variance.Emit(ctx, VarianceEvent{
			Kind:              kind,
			RequestedModel:    ex.RequestedModel,
			ResolvedModel:     ex.ResolvedModel,
			RequestedProvider: ex.RequestedProvider,
			Provider:          ex.ResolvedProvider,
			RequestedRegion:   ex.RequestedRegion,
			ResolvedRegion:    ex.ResolvedRegion,
			Reason:            reason,
			Principal:         ex.Principal,
			Board:             ex.Board,
		})
	}
	if ex.ResolvedModel != "" && ex.ResolvedModel != ex.RequestedModel {
		emit("model_swap")
	}
	if ex.ResolvedProvider != "" && ex.ResolvedProvider != ex.RequestedProvider {
		emit("provider_swap")
	}
	if ex.ResolvedRegion != "" && ex.ResolvedRegion != ex.RequestedRegion {
		emit("region_swap")
	}
}

// routingReason devolve a razão registada pelo estágio de roteamento, ou um
// default estável se o estágio não a documentou.
func (g *Gateway) routingReason(ex *pipeline.Exchange) string {
	reason := "roteamento divergiu do pedido"
	for _, d := range ex.Decisions {
		if d.Stage == "roteamento" && d.Reason != "" {
			reason = d.Reason
		}
	}
	return reason
}

func (g *Gateway) regionOf(region string) string {
	if region != "" {
		return region
	}
	return g.region
}

// setUsageAttrs emite os atributos gen_ai.usage.* no span. NUNCA emite segredos.
func setUsageAttrs(span agentruntime.Span, u port.Usage) {
	span.SetAttribute(agentruntime.AttrInputTokens, u.PromptTokens)
	span.SetAttribute(agentruntime.AttrOutputTokens, u.CompletionTokens)
}

func errType(err error) string {
	var ae *attributionError
	if errors.As(err, &ae) {
		return "attribution_error"
	}
	var se *pipeline.StageError
	if errors.As(err, &se) {
		return "stage:" + se.Stage
	}
	return "provider_error"
}

// meteredStream envolve o stream do adaptador para capturar o usage do chunk
// final e correr o metering + fecho do span EXACTAMENTE uma vez, seja qual for o
// gatilho de fim (EOF em Recv, Close explícito, ou o backstop de ctx cancelado).
// Delega Recv/Close ao stream interno. O mutex torna o fim seguro sob a corrida
// entre o consumidor (Recv/Close) e a goroutine de backstop do ctx.
type meteredStream struct {
	inner    port.ChatStream
	onEnd    func(port.Usage)
	finished chan struct{}

	mu    sync.Mutex
	usage port.Usage
	ended bool
}

// end corre onEnd no MÁXIMO uma vez (idempotente e concorrente-seguro).
func (m *meteredStream) end(usage port.Usage) {
	m.mu.Lock()
	if m.ended {
		m.mu.Unlock()
		return
	}
	m.ended = true
	if m.finished != nil {
		close(m.finished)
	}
	m.mu.Unlock()
	if m.onEnd != nil {
		m.onEnd(usage)
	}
}

// lastUsage devolve o usage acumulado até agora (para o backstop de ctx).
func (m *meteredStream) lastUsage() port.Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usage
}

// Recv delega e memoriza o usage do chunk final; em EOF/erro dispara o fim.
func (m *meteredStream) Recv() (port.ChatStreamDelta, error) {
	d, err := m.inner.Recv()
	if d.Usage != nil {
		m.mu.Lock()
		m.usage = *d.Usage
		m.mu.Unlock()
	}
	if err != nil {
		m.end(m.lastUsage())
	}
	return d, err
}

// Close fecha o stream interno e corre o fim (se ainda não correu).
func (m *meteredStream) Close() error {
	m.end(m.lastUsage())
	return m.inner.Close()
}
