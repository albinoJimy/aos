package modelgateway

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

// ModelClientAdapter adapta o [Gateway] à porta [agentruntime.ModelClient] do
// Agent Runtime (AOS-013). É a ligação canónica RT → GW: o loop do runtime tem
// uma porta ModelClient mínima (PromptView → ModelResponse); o GW é a
// implementação/fachada real por detrás dela. Traduz a [agentruntime.PromptView]
// materializada para o contrato compatível OpenAI e a [port.ChatResponse] de
// volta para [agentruntime.ModelResponse].
//
// Assim o RT continua a depender só da SUA porta (ModelClient) e o GW da SUA
// (port.Gateway) — o adaptador reconcilia os dois contratos sem que nenhum
// dependa do outro.
type ModelClientAdapter struct {
	gw      port.Gateway
	model   string
	tools   []port.Tool
	region  string
	board   string
	princip string
	runID   string
}

// Compile-time: o adaptador satisfaz a porta do runtime.
var _ agentruntime.ModelClient = (*ModelClientAdapter)(nil)

// RuntimeAdapterOption configura o [ModelClientAdapter].
type RuntimeAdapterOption func(*ModelClientAdapter)

// WithTools congela o tool set (do registry, EPIC-05) exposto ao modelo.
func WithTools(tools []port.Tool) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) { a.tools = tools }
}

// WithPrincipal define o token scoped do principal (validação forte é AOS-057).
func WithPrincipal(token string) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) { a.princip = token }
}

// WithRegionBoard define a fronteira de soberania alvo (consumida por AOS-058).
func WithRegionBoard(region, board string) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) { a.region, a.board = region, board }
}

// WithRun correlaciona as chamadas deste adaptador com a TRAJECTÓRIA (run) do
// agente: o runID entra em cada [port.ChatRequest] e torna-se o eixo de agregação
// do SLI de cache-hit-rate (AOS-061, por run/tenant) e a ligação da atribuição à
// trajectória (ADR-010). Um adaptador é tipicamente construído por run.
func WithRun(runID string) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) { a.runID = runID }
}

// NewModelClient constrói o adaptador RT→GW para um modelo dado.
func NewModelClient(gw port.Gateway, model string, opts ...RuntimeAdapterOption) *ModelClientAdapter {
	a := &ModelClientAdapter{gw: gw, model: model}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Call implementa [agentruntime.ModelClient]: materializa o pedido a partir da
// PromptView, invoca o GW (que atravessa a pipeline determinística) e traduz a
// resposta normalizada de volta para o runtime.
func (a *ModelClientAdapter) Call(ctx context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	req := port.ChatRequest{
		Model:     a.model,
		Messages:  []port.Message{{Role: port.RoleUser, Content: string(view.Materialized)}},
		Tools:     a.tools,
		Principal: a.princip,
		Region:    a.region,
		Board:     a.board,
		RunID:     a.runID,
	}
	resp, err := a.gw.Chat(ctx, req)
	if err != nil {
		return agentruntime.ModelResponse{}, err
	}
	return translateResponse(resp), nil
}

// translateResponse converte [port.ChatResponse] em [agentruntime.ModelResponse].
func translateResponse(resp port.ChatResponse) agentruntime.ModelResponse {
	out := agentruntime.ModelResponse{
		Usage: agentruntime.Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
	}
	if len(resp.Choices) == 0 {
		out.Final = true
		return out
	}
	choice := resp.Choices[0]
	out.Text = choice.Message.Content
	for _, tc := range choice.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, agentruntime.ToolInvocation{
			ToolID: tc.Function.Name,
			Input:  []byte(tc.Function.Arguments),
		})
	}
	// Sem tool calls e finish_reason terminal ⇒ o turno é final.
	out.Final = len(out.ToolCalls) == 0 && (choice.FinishReason == "stop" || choice.FinishReason == "")
	return out
}
