package modelgateway_test

import (
	"context"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/port"
)

// constAdapter é um adaptador SEM estado mutável — logo concorrente-seguro — para
// isolar a corrida no gerador de IDs do próprio Gateway (o FakeAdapter tem um
// contador não-sincronizado que não é o alvo deste teste).
type constAdapter struct{ provider string }

func (a constAdapter) Provider() string { return a.provider }

func (a constAdapter) Chat(_ context.Context, req port.ChatRequest, _ adapters.Credential) (port.ChatResponse, error) {
	return port.ChatResponse{
		Model:   req.Model,
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "x"}, FinishReason: "stop"}},
	}, nil
}

func (a constAdapter) ChatStream(_ context.Context, req port.ChatRequest, _ adapters.Credential) (port.ChatStream, error) {
	return port.NewSliceStream([]port.ChatStreamDelta{{Role: port.RoleAssistant, Content: "x", FinishReason: "stop"}}), nil
}

func (a constAdapter) Embeddings(_ context.Context, req port.EmbeddingsRequest, _ adapters.Credential) (port.EmbeddingsResponse, error) {
	return port.EmbeddingsResponse{Model: req.Model, Object: "list"}, nil
}

// TestGateway_Chat_ConcorrenteIDsUnicos prova que o gerador de IDs default é
// concorrente-seguro: N chamadas Chat concorrentes numa fachada partilhada
// produzem N IDs DISTINTOS (sem duplicados nem data race — o -race guarda o
// invariante). O Gateway é documentado stateless/partilhável.
func TestGateway_Chat_ConcorrenteIDsUnicos(t *testing.T) {
	t.Parallel()
	gw := newGateway(t, constAdapter{provider: "fake"})
	const n = 200
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		ids = make(map[string]struct{}, n)
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := gw.Chat(context.Background(), port.ChatRequest{
				Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
			})
			if err != nil {
				t.Errorf("Chat: %v", err)
				return
			}
			mu.Lock()
			ids[resp.ID] = struct{}{}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(ids) != n {
		t.Fatalf("esperado %d IDs distintos, obtidos %d (IDs duplicados/racy sob concorrencia)", n, len(ids))
	}
}

// TestGateway_Stream_MeteringVeUsageFinal prova que, no streaming, o estágio de
// metering corre com o usage FINAL do stream (não zero): o metering é adiado para
// o fecho do stream, para o ponto de extensão de custo (AOS-062) herdar tokens
// reais.
func TestGateway_Stream_MeteringVeUsageFinal(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "oi"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 100, CompletionTokens: 200, TotalTokens: 300},
	})
	var meteredTotal int64 = -1
	p := pipeline.New(pipeline.Stages{
		Metering: pipeline.StageFunc{StageName: "metering", Fn: func(_ context.Context, ex *pipeline.Exchange) error {
			meteredTotal = ex.Usage.TotalTokens
			return nil
		}},
	})
	gw := newGateway(t, f, modelgateway.WithPipeline(p))
	stream, err := gw.ChatStream(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if _, err := port.CollectStream(stream); err != nil {
		t.Fatalf("CollectStream: %v", err)
	}
	if meteredTotal != 300 {
		t.Fatalf("metering do streaming viu TotalTokens=%d, quer 300 (correu sobre usage vazio?)", meteredTotal)
	}
}

// TestGateway_ProviderSwap_Variancia: um roteamento que mantém o modelo mas troca
// de PROVEDOR emite uma variância explícita (provider_swap), nunca silenciosa.
func TestGateway_ProviderSwap_Variancia(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{Choices: []port.Choice{{Message: port.Message{Content: "x"}, FinishReason: "stop"}}})
	cs := adapters.NewStaticCredentialSource()
	cs.Set("fake", "eu", "sk-a")
	cs.Set("outro-provider", "eu", "sk-b")
	p := pipeline.New(pipeline.Stages{
		Routing: pipeline.StageFunc{StageName: "roteamento", Fn: func(_ context.Context, ex *pipeline.Exchange) error {
			ex.ResolvedModel = ex.RequestedModel   // mesmo modelo
			ex.ResolvedRegion = ex.RequestedRegion // mesma regiao
			ex.ResolvedProvider = "outro-provider" // troca de provedor
			return nil
		}},
	})
	var events []modelgateway.VarianceEvent
	sink := modelgateway.VarianceSinkFunc(func(_ context.Context, ev modelgateway.VarianceEvent) { events = append(events, ev) })
	gw := modelgateway.New(f,
		modelgateway.WithCredentialSource(cs),
		modelgateway.WithDefaultRegion("eu"),
		modelgateway.WithClock(func() time.Time { return time.Unix(1, 0) }),
		modelgateway.WithPipeline(p),
		modelgateway.WithVarianceSink(sink),
	)
	if _, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "provider_swap" {
		t.Fatalf("esperado 1 provider_swap, obtido %+v", events)
	}
	if events[0].RequestedProvider != "fake" || events[0].Provider != "outro-provider" {
		t.Errorf("provider_swap mal preenchido: %+v", events[0])
	}
}

// TestGateway_RegionSwap_Variancia: um failover cross-border (troca de REGIÃO)
// emite variância explícita (region_swap) — preocupação de soberania observável.
func TestGateway_RegionSwap_Variancia(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{Choices: []port.Choice{{Message: port.Message{Content: "x"}, FinishReason: "stop"}}})
	cs := adapters.NewStaticCredentialSource()
	cs.Set("fake", "eu", "sk-a")
	cs.Set("fake", "us", "sk-b")
	p := pipeline.New(pipeline.Stages{
		Routing: pipeline.StageFunc{StageName: "roteamento", Fn: func(_ context.Context, ex *pipeline.Exchange) error {
			ex.ResolvedModel = ex.RequestedModel
			ex.ResolvedRegion = "us" // failover cross-border (pedido: eu)
			return nil
		}},
	})
	var events []modelgateway.VarianceEvent
	sink := modelgateway.VarianceSinkFunc(func(_ context.Context, ev modelgateway.VarianceEvent) { events = append(events, ev) })
	gw := modelgateway.New(f,
		modelgateway.WithCredentialSource(cs),
		modelgateway.WithDefaultRegion("eu"),
		modelgateway.WithClock(func() time.Time { return time.Unix(1, 0) }),
		modelgateway.WithPipeline(p),
		modelgateway.WithVarianceSink(sink),
	)
	if _, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "region_swap" {
		t.Fatalf("esperado 1 region_swap, obtido %+v", events)
	}
	if events[0].RequestedRegion != "eu" || events[0].ResolvedRegion != "us" {
		t.Errorf("region_swap mal preenchido: %+v", events[0])
	}
}

// endTracer é um Tracer de teste que sinaliza (uma vez) o End do span por canal —
// leitura concorrente-segura, ao contrário de ler RecordedSpan.Ended sob corrida.
type endTracer struct {
	ended chan struct{}
	once  sync.Once
}

func newEndTracer() *endTracer { return &endTracer{ended: make(chan struct{})} }

func (t *endTracer) StartSpan(ctx context.Context, _ string) (context.Context, agentruntime.Span) {
	return ctx, &endSpan{t: t}
}

type endSpan struct{ t *endTracer }

func (s *endSpan) SetAttribute(string, any) {}
func (s *endSpan) End()                     { s.t.once.Do(func() { close(s.t.ended) }) }

// TestGateway_Stream_SpanFechaEmCtxCancelado prova o backstop anti-fuga: um
// consumidor que ABANDONA o stream (sem drenar nem Close) não deixa o span
// aberto para sempre — o cancelamento do ctx fecha-o.
func TestGateway_Stream_SpanFechaEmCtxCancelado(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Content: "oi"}, FinishReason: "stop"}},
		Usage:   port.Usage{TotalTokens: 3},
	})
	tr := newEndTracer()
	gw := newGateway(t, f, modelgateway.WithTracer(tr))
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := gw.ChatStream(ctx, port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	_ = stream // abandonado de propósito: sem Recv nem Close
	cancel()
	select {
	case <-tr.ended:
	case <-time.After(2 * time.Second):
		t.Fatal("span de streaming NAO fechou apos cancelamento do ctx (fuga de span)")
	}
}

// TestGateway_Embeddings_SpanOperacao prova que o span de embeddings é ABERTO com
// a sua própria operação ('embeddings'), não sob 'chat' — um tracer que indexe
// pela operação de StartSpan classifica-o correctamente.
func TestGateway_Embeddings_SpanOperacao(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetEmbeddingsResponse("emb", port.EmbeddingsResponse{
		Data:  []port.Embedding{{Index: 0, Embedding: []float64{0.1}}},
		Usage: port.Usage{PromptTokens: 4, TotalTokens: 4},
	})
	tr := &agentruntime.RecordingTracer{}
	gw := newGateway(t, f, modelgateway.WithTracer(tr))
	if _, err := gw.Embeddings(context.Background(), port.EmbeddingsRequest{Model: "emb", Input: []string{"oi"}}); err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	emb := tr.SpansByOperation("embeddings")
	if len(emb) != 1 {
		t.Fatalf("esperado 1 span 'embeddings', obtidos %d", len(emb))
	}
	if got := len(tr.SpansByOperation(agentruntime.OpChat)); got != 0 {
		t.Errorf("embeddings NAO devia abrir span sob 'chat', obtidos %d", got)
	}
	if emb[0].Attributes[agentruntime.AttrOperationName] != "embeddings" {
		t.Errorf("gen_ai.operation.name = %v, quer 'embeddings'", emb[0].Attributes[agentruntime.AttrOperationName])
	}
}
