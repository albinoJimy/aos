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
	"github.com/aos-ref/platform/model-gateway/pipeline"
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
