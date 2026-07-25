package runtime

import "context"

// ModelConfig identifica o modelo e os seus parâmetros não-determinísticos. É
// pinado no manifesto por trajectória (ADR-010, tecnica/13 §6): model_id, params
// e seed são os inputs não-determinísticos que o replay tem de reproduzir.
type ModelConfig struct {
	// ModelID é o identificador do modelo (ex.: "claude-opus-4-8").
	ModelID string
	// Params são os parâmetros de amostragem (ex.: {"temperature":"0","top_p":"1"}).
	// map[string]string (e não any) mantém a serialização determinística e simples.
	Params map[string]string
	// Seed é a semente de amostragem (0 quando não aplicável).
	Seed int64
}

// Usage é o consumo de tokens de uma chamada ao modelo. Os nomes espelham a
// semconv OTel GenAI (gen_ai.usage.input_tokens / output_tokens).
type Usage struct {
	InputTokens  int64
	OutputTokens int64
}

// ToolInvocation é uma tool call PRETENDIDA pelo modelo. É apenas uma INTENÇÃO:
// o RT nunca a executa directamente — traduz cada uma num [rm.Call] e submete-a
// a Mediate. Os campos são o suficiente para o RT construir o Call.
type ToolInvocation struct {
	// ToolID identifica a tool registada no Reference Monitor.
	ToolID string
	// Capability é o direito escopado que a política avalia (ex.: "cap:http.get").
	Capability string
	// ResourceType/Value/Region descrevem o alvo concreto (contrato C1 do RM).
	ResourceType   string
	ResourceValue  string
	ResourceRegion string
	// Input é o payload opaco entregue à tool após permit.
	Input []byte
	// AuthorizationTaint é o rótulo de taint da AUTORIZAÇÃO desta tool call — a
	// proveniência do PLANO que a originou (control-plane), NÃO a dos seus dados
	// (que são untrusted no data-plane). Só o planeador sobre dados trusted a marca
	// "trusted" (via AuthorizeTrusted, ADR-005/AOS-069); vazio/desconhecido ⇒
	// untrusted (fail-closed). O RT propaga-a ao [rm.CallContext].Taint, onde o
	// [rm.TaintGate] a impõe: untrusted não pode originar uma capability privilegiada.
	// Distingue-se do taint dos DADOS: uma tool call pode usar argumentos untrusted
	// (por handle) e ainda ser autorizada pelo control-plane trusted.
	AuthorizationTaint string
}

// ModelResponse é o resultado de uma chamada ao Model Gateway.
type ModelResponse struct {
	// Text é a resposta textual do modelo neste turno.
	Text string
	// ToolCalls são as tool calls pretendidas (a despachar via RM). Vazio ⇒ o
	// turno não pede tools.
	ToolCalls []ToolInvocation
	// Final indica que o modelo considera a tarefa concluída — o loop termina
	// com Text como resposta final. (A terminação rica é a máquina de estados
	// durável AOS-017; aqui é um stub simples.)
	Final bool
	// Usage é o consumo de tokens deste turno.
	Usage Usage
	// CostMicroUSD é o custo do turno em micro-USD INTEIRO (1 USD = 1_000_000).
	// Inteiro evita imprecisão de vírgula flutuante no burn-down de custo.
	CostMicroUSD int64
}

// ModelClient é a PORTA para o Model Gateway (GW). O GW real — routing,
// rate-limit, cache de prompt no provider — é EPIC-06; aqui é uma porta mínima,
// mockada nos testes. Recebe a [PromptView] materializada do turno e devolve a
// resposta do modelo.
type ModelClient interface {
	Call(ctx context.Context, view PromptView) (ModelResponse, error)
}

// ModelClientFunc adapta uma função à porta [ModelClient] (útil em testes).
type ModelClientFunc func(ctx context.Context, view PromptView) (ModelResponse, error)

// Call implementa [ModelClient].
func (f ModelClientFunc) Call(ctx context.Context, view PromptView) (ModelResponse, error) {
	return f(ctx, view)
}
