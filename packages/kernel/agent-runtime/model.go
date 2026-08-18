package agentruntime

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
// o RT nunca a executa directamente — traduz cada uma num [referencemonitor.Call]
// e submete-a a Mediate. Os campos são o suficiente para o RT construir o Call.
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
	// [TaintTrusted] (via [AuthorizeTrusted], ADR-005/AOS-069); vazio/desconhecido
	// ⇒ untrusted (fail-closed). O RT propaga-a ao [referencemonitor.CallContext].
	// Taint, onde o [referencemonitor.TaintGate] a impõe: untrusted não pode
	// originar uma capability privilegiada. Distingue-se do taint dos DADOS: uma
	// tool call pode usar argumentos untrusted (por handle) e ainda ser autorizada
	// pelo control-plane trusted.
	//
	// CONTRATO DE SEGURANÇA CRÍTICO (AOS-069): este campo é in-band no mesmo struct
	// que a fronteira UNTRUSTED (o [ModelClient]) produz. A garantia "só o
	// control-plane marca trusted" é, ENQUANTO este for um campo público settable,
	// CONVENÇÃO — não estrutura (ao contrário do [referencemonitor.Permit], mintado e
	// infalsificável, ou da barreira de DADOS via [Handle]/[Quarantine], de campos
	// não-exportados). Portanto, INVARIANTE INEGOCIÁVEL: NENHUM adaptador
	// [ModelClient] / gateway / normalizador pode preencher AuthorizationTaint a
	// partir de dados do modelo — a marca trusted SÓ pode nascer de [AuthorizeTrusted]
	// chamado por um [ControlPlanner] sobre uma [PlannerView] (dados trusted +
	// handles). O único mecanismo que impede a escalada quando esta convenção é
	// violada é o fail-closed de [authorizationTaintOf]/[taint.ParseLabel] (qualquer
	// valor que não seja a string canónica "trusted" resolve untrusted). Tornar a
	// autorização estruturalmente infalsificável (mintá-la no RT a partir da saída do
	// ControlPlanner, à semelhança do Permit) é a evolução planeada — DIFERIDA para o
	// ticket de integração de superfície que liga [SeparatePlanes] ao loop (ver
	// loop.go, "SEPARAÇÃO DE PLANOS ... DIFERIDA").
	AuthorizationTaint string

	// Reversibility é a REVERSIBILIDADE DECLARADA do efeito ("reversible"), vinda do
	// registry de tools. Chega ao [risk.Classify] pelo CallContext e é a PRIMEIRA regra do
	// classificador — sem ela, `IsIrreversible()` devolve true (o valor-zero é desconhecido,
	// e desconhecido conta como irreversível) e TODA a acção sai `danger`.
	//
	// FAIL-CLOSED: vazio continua a significar irreversível. Declarar custa uma linha no
	// registry; NÃO declarar nunca é interpretado como benigno.
	Reversibility string
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
