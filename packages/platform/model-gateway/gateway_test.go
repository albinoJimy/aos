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

// newGateway constrói um GW de teste sobre um fake adapter, com credencial e
// relógio deterministas.
func newGateway(t *testing.T, adapter adapters.Adapter, opts ...modelgateway.Option) *modelgateway.Gateway {
	t.Helper()
	cs := adapters.NewStaticCredentialSource()
	cs.Set(adapter.Provider(), "eu", "sk-teste")
	base := []modelgateway.Option{
		modelgateway.WithCredentialSource(cs),
		modelgateway.WithDefaultRegion("eu"),
		modelgateway.WithClock(func() time.Time { return time.Unix(1_700_000_000, 0) }),
	}
	return modelgateway.New(adapter, append(base, opts...)...)
}

// TestGateway_PortVersion_SemVer confirma que a fachada expõe a versão SemVer.
func TestGateway_PortVersion_SemVer(t *testing.T) {
	t.Parallel()
	gw := newGateway(t, adapters.NewFakeAdapter("fake"))
	if gw.PortVersion() != port.Version {
		t.Errorf("PortVersion = %q, quer %q", gw.PortVersion(), port.Version)
	}
}

// TestGateway_Chat_RespostaNormalizada é o teste de integração base: uma chamada
// atravessa a pipeline e o adaptador fake devolve resposta normalizada.
func TestGateway_Chat_RespostaNormalizada(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "olá"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
	})
	gw := newGateway(t, f)
	resp, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Choices[0].Message.Content != "olá" || resp.Model != "m" {
		t.Errorf("resposta = %+v", resp)
	}
	if resp.ID == "" || resp.Created != time.Unix(1_700_000_000, 0).Unix() {
		t.Errorf("campos deterministas nao preenchidos: id=%q created=%d", resp.ID, resp.Created)
	}
	if f.Calls() != 1 {
		t.Errorf("adaptador chamado %d vezes", f.Calls())
	}
}

// TestGateway_Chat_FailClosed_SemCredencial: sem credencial para a região a
// chamada falha fail-closed e o adaptador NÃO é invocado.
func TestGateway_Chat_FailClosed_SemCredencial(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	gw := modelgateway.New(f,
		modelgateway.WithCredentialSource(adapters.NewStaticCredentialSource()), // vazia
		modelgateway.WithDefaultRegion("eu"),
	)
	_, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err != adapters.ErrNoCredential {
		t.Fatalf("erro = %v, quer ErrNoCredential", err)
	}
	if f.Calls() != 0 {
		t.Errorf("adaptador foi invocado sem credencial (nao fail-closed): %d", f.Calls())
	}
}

// TestGateway_Chat_AllowlistDeny_FailClosed: um estágio de allowlist que recusa
// aborta antes do provider.
func TestGateway_Chat_AllowlistDeny_FailClosed(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	p := pipeline.New(pipeline.Stages{
		Allowlist: pipeline.DenyStage{StageName: "allowlist-regional", Err: pipeline.ErrDenied},
	})
	gw := newGateway(t, f, modelgateway.WithPipeline(p))
	_, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err == nil {
		t.Fatal("chamada devia falhar fail-closed pela allowlist")
	}
	if f.Calls() != 0 {
		t.Errorf("provider invocado apesar do deny: %d", f.Calls())
	}
}

// TestGateway_Observabilidade_SpanChat prova que cada chamada emite um span
// 'chat' com gen_ai.request.model e gen_ai.usage.* (critério AOS-055).
func TestGateway_Observabilidade_SpanChat(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "x"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18},
	})
	tr := &agentruntime.RecordingTracer{}
	gw := newGateway(t, f, modelgateway.WithTracer(tr))
	if _, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	spans := tr.SpansByOperation(agentruntime.OpChat)
	if len(spans) != 1 {
		t.Fatalf("esperado 1 span 'chat', obtidos %d", len(spans))
	}
	attrs := spans[0].Attributes
	if attrs[agentruntime.AttrRequestModel] != "m" {
		t.Errorf("gen_ai.request.model = %v", attrs[agentruntime.AttrRequestModel])
	}
	if attrs[agentruntime.AttrInputTokens] != int64(11) || attrs[agentruntime.AttrOutputTokens] != int64(7) {
		t.Errorf("gen_ai.usage.* errados: in=%v out=%v", attrs[agentruntime.AttrInputTokens], attrs[agentruntime.AttrOutputTokens])
	}
	if !spans[0].Ended {
		t.Error("span nao foi fechado")
	}
}

// TestGateway_SwapModelo_EventoVariancia: quando o roteamento resolve outro
// modelo, o GW emite um evento de variância explícito (nunca silencioso).
func TestGateway_SwapModelo_EventoVariancia(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("modelo-barato", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
	})
	// Routing que faz downgrade explícito.
	p := pipeline.New(pipeline.Stages{
		Routing: pipeline.StageFunc{StageName: "roteamento", Fn: func(_ context.Context, ex *pipeline.Exchange) error {
			ex.ResolvedModel = "modelo-barato"
			return nil
		}},
	})
	var mu sync.Mutex
	var events []modelgateway.VarianceEvent
	sink := modelgateway.VarianceSinkFunc(func(_ context.Context, ev modelgateway.VarianceEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	gw := newGateway(t, f, modelgateway.WithPipeline(p), modelgateway.WithVarianceSink(sink))
	resp, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "modelo-frontier", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}}, Board: "b1",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Model != "modelo-barato" {
		t.Errorf("modelo efectivo = %q", resp.Model)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 {
		t.Fatalf("esperado 1 evento de variancia, obtidos %d", len(events))
	}
	ev := events[0]
	if ev.RequestedModel != "modelo-frontier" || ev.ResolvedModel != "modelo-barato" || ev.Board != "b1" {
		t.Errorf("evento de variancia errado: %+v", ev)
	}
}

// TestGateway_SemSwap_SemVariancia: numa chamada sem swap, nenhum evento é emitido.
func TestGateway_SemSwap_SemVariancia(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{Choices: []port.Choice{{Message: port.Message{Content: "x"}, FinishReason: "stop"}}})
	emitted := false
	sink := modelgateway.VarianceSinkFunc(func(_ context.Context, _ modelgateway.VarianceEvent) { emitted = true })
	gw := newGateway(t, f, modelgateway.WithVarianceSink(sink))
	if _, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if emitted {
		t.Error("evento de variancia emitido sem swap")
	}
}

// TestGateway_Stream_ToolCalling: streaming pela fachada, com tool calling, e
// span fechado com usage no fim do stream.
func TestGateway_Stream_ToolCalling(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{
			Role: port.RoleAssistant, Content: "a chamar",
			ToolCalls: []port.ToolCall{{ID: "c1", Type: "function", Function: port.FunctionCall{Name: "f", Arguments: `{"k":1}`}}},
		}, FinishReason: "tool_calls"}},
		Usage: port.Usage{PromptTokens: 2, CompletionTokens: 8, TotalTokens: 10},
	})
	tr := &agentruntime.RecordingTracer{}
	gw := newGateway(t, f, modelgateway.WithTracer(tr))
	stream, err := gw.ChatStream(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	resp, err := port.CollectStream(stream)
	if err != nil {
		t.Fatalf("CollectStream: %v", err)
	}
	if resp.Choices[0].Message.ToolCalls[0].Function.Name != "f" {
		t.Errorf("tool call streamed errada: %+v", resp.Choices[0].Message.ToolCalls)
	}
	spans := tr.SpansByOperation(agentruntime.OpChat)
	if len(spans) != 1 || !spans[0].Ended {
		t.Fatalf("span de streaming nao fechado: %+v", spans)
	}
	if spans[0].Attributes[agentruntime.AttrOutputTokens] != int64(8) {
		t.Errorf("usage do stream nao emitido no span: %v", spans[0].Attributes[agentruntime.AttrOutputTokens])
	}
}

// TestGateway_Embeddings cobre a superfície de embeddings pela fachada.
func TestGateway_Embeddings(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetEmbeddingsResponse("emb", port.EmbeddingsResponse{
		Data:  []port.Embedding{{Index: 0, Embedding: []float64{0.1, 0.2}}},
		Usage: port.Usage{PromptTokens: 4, TotalTokens: 4},
	})
	gw := newGateway(t, f)
	resp, err := gw.Embeddings(context.Background(), port.EmbeddingsRequest{Model: "emb", Input: []string{"oi"}})
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if resp.Model != "emb" || len(resp.Data) != 1 {
		t.Errorf("embeddings = %+v", resp)
	}
}

// TestGateway_RuntimeIntegration é o teste de integração Agent Runtime → GW →
// adaptador FAKE: o ModelClientAdapter satisfaz a porta agentruntime.ModelClient
// e o loop obtém uma ModelResponse normalizada (texto + tool calls traduzidos).
func TestGateway_RuntimeIntegration(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("claude-x", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{
			Role: port.RoleAssistant, Content: "resultado do turno",
			ToolCalls: []port.ToolCall{{ID: "c1", Type: "function", Function: port.FunctionCall{Name: "get_weather", Arguments: `{"city":"lx"}`}}},
		}, FinishReason: "tool_calls"}},
		Usage: port.Usage{PromptTokens: 12, CompletionTokens: 4, TotalTokens: 16},
	})
	gw := newGateway(t, f)

	// O adaptador RT→GW: satisfaz agentruntime.ModelClient.
	var client agentruntime.ModelClient = modelgateway.NewModelClient(gw, "claude-x",
		modelgateway.WithPrincipal("tok-principal"), modelgateway.WithRegionBoard("eu", "board-1"))

	// Materializa um prompt com o assembler real do runtime (integração fiel).
	asm := agentruntime.NewPromptAssembler("system prompt", nil)
	view := asm.Assemble(1, []agentruntime.TailSegment{{Kind: agentruntime.TailObjective, Content: []byte("faz X")}})

	resp, err := client.Call(context.Background(), view)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp.Text != "resultado do turno" {
		t.Errorf("texto = %q", resp.Text)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ToolID != "get_weather" {
		t.Fatalf("tool calls traduzidas erradas: %+v", resp.ToolCalls)
	}
	if string(resp.ToolCalls[0].Input) != `{"city":"lx"}` {
		t.Errorf("input da tool = %q", resp.ToolCalls[0].Input)
	}
	if resp.Usage.InputTokens != 12 || resp.Usage.OutputTokens != 4 {
		t.Errorf("usage traduzido errado: %+v", resp.Usage)
	}
	if resp.Final {
		t.Error("turno com tool calls nao devia ser Final")
	}
}

// TestGateway_RuntimeIntegration_TurnoFinal: sem tool calls e finish=stop ⇒ Final.
func TestGateway_RuntimeIntegration_TurnoFinal(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "feito"}, FinishReason: "stop"}},
	})
	gw := newGateway(t, f)
	client := modelgateway.NewModelClient(gw, "m")
	asm := agentruntime.NewPromptAssembler("s", nil)
	resp, err := client.Call(context.Background(), asm.Assemble(1, nil))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if !resp.Final || resp.Text != "feito" {
		t.Errorf("turno final errado: %+v", resp)
	}
}
