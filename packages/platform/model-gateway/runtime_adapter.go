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
	// principCtx SOURCE o token do principal do CONTEXTO por-chamada (AOS-278). Quando
	// != nil e devolve um valor não-vazio, esse valor TEM PRECEDÊNCIA sobre [princip]: é
	// como a identidade REAL do RUN (o token NHI de Goal.Credential, o mesmo que cada tool
	// call mediada verifica) chega ao estágio authn do GW, que é construção-time e nível-nó
	// (não sabe qual run serve). Vazio/ausente ⇒ cai para [princip] (a omissão sob o cutover
	// duro é ""), e o estágio authn nega ATRIBUÍVELMENTE — nunca se forja um principal.
	principCtx func(context.Context) string
	runID      string
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

// WithPrincipalFromContext liga a fonte POR-CHAMADA do token do principal (AOS-278):
// o adaptador é construído UMA vez ao nível do nó, mas a identidade a apresentar ao
// estágio authn do GW é a do RUN, e essa só se conhece por-chamada — viaja no ctx que
// flui de Run(ctx, goal) até Call(ctx, view), a MESMA mecânica por-run que o plano de
// replay usa (ver resume_model.go). fn lê esse valor do ctx; um valor não-vazio tem
// precedência sobre [WithPrincipal]. É o que estende ao turno de modelo a identidade
// real que as tool calls já verificam. fn nil ⇒ opção inerte.
func WithPrincipalFromContext(fn func(context.Context) string) RuntimeAdapterOption {
	return func(a *ModelClientAdapter) {
		if fn != nil {
			a.principCtx = fn
		}
	}
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
	// IDENTIDADE POR-RUN (AOS-278): a fonte de ctx (o token NHI do run) tem precedência
	// sobre o token de construção. Ausente/vazia ⇒ fica o de construção (sob o cutover duro,
	// ""), e o estágio authn do GW nega atribuívelmente — nenhum principal é forjado.
	principal := a.princip
	if a.principCtx != nil {
		if p := a.principCtx(ctx); p != "" {
			principal = p
		}
	}
	req := port.ChatRequest{
		Model:     a.model,
		Messages:  []port.Message{{Role: port.RoleUser, Content: string(view.Materialized)}},
		Tools:     a.tools,
		Principal: principal,
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
//
// AOS-259 — É AQUI QUE O CANAL DE CUSTO ATRAVESSA A FRONTEIRA RT↔GW. O custo derivado
// pela contabilidade do gateway viaja em [port.Usage.CostMicroUSD] e projecta-se em
// [agentruntime.ModelResponse.CostMicroUSD], que o runtime JÁ consome em três sítios
// que até aqui recebiam zero: o acumulado do run (Result.TotalCostMicroUSD), o atributo
// do span `chat` (aos.cost.micro_usd) e — o que decide — o campo `cost_micro_usd` do
// evento durável `turn.recorded`, que é a fonte do burn-down do nó. Continua a ser UM
// canal: não se abre uma segunda contabilidade no runtime, projecta-se a que existe.
//
// Micro-USD INTEIRO em toda a travessia: os dois lados da fronteira são int64 e a
// projecção é uma cópia — sem conversão, sem float, sem arredondamento onde se pudesse
// perder um micro-USD.
func translateResponse(resp port.ChatResponse) agentruntime.ModelResponse {
	out := agentruntime.ModelResponse{
		Usage: agentruntime.Usage{
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		},
		CostMicroUSD: resp.Usage.CostMicroUSD,
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
