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

	// Ausente marca que o turno NÃO FOI MEDIDO — o provedor respondeu e não reportou
	// usage em que se possa confiar. É a distinção entre «o provedor disse zero» e «o
	// provedor não disse nada», e sem ela um turno não medido desce indistinguível de
	// um turno gratuito (AOS-336).
	//
	// PORQUE PRECISA DE ATRAVESSAR ESTA FRONTEIRA. O gateway já a tinha em
	// `port.Usage.Ausente` (AOS-321), mas o tradutor RT↔GW copiava só os números: num
	// deployment sem contabilidade de custo composta o gateway serve a resposta na
	// mesma (é o limite declarado do AOS-321) e o `turn.recorded` recebia
	// `input_tokens: 0, cost_micro_usd: 0` para uma chamada não medida — o zero
	// silencioso fechado dentro do gateway, reaparecido um passo a jusante. É este
	// campo que o leva até ao evento durável que o burn-down do nó lê.
	//
	// POLARIDADE DELIBERADA, igual à de `port.Usage.Ausente`: o valor-zero é
	// «definido». Um [Usage] construído em código afirma o que escreve; só quem
	// TRADUZ material recebido — o adaptador do gateway — marca a ausência. A marca é
	// ADITIVA e não reinterpreta nenhum consumidor existente.
	Ausente bool
}

// Definido reporta se este usage é uma MEDIÇÃO em que se possa confiar.
//
// Espelha [github.com/aos-ref/platform/model-gateway/port.Usage.Definido] do outro lado
// da fronteira, e pelas mesmas duas razões: a marca explícita cobre o `usage` ausente, e
// `InputTokens > 0` cobre a ausência DISFARÇADA — um `usage` presente mas sem contadores
// legíveis. Não existe chamada de modelo sem entrada: há sempre system+user. Zero tokens
// de entrada é ausência de dados disfarçada de leitura, nunca uma medição.
func (u Usage) Definido() bool {
	if u.Ausente {
		return false
	}
	return u.InputTokens > 0
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
	// SEM CAMPO DE AUTORIZAÇÃO, DE PROPÓSITO (AOS-069, ADR-034). Esta struct é produzida
	// pela fronteira UNTRUSTED — o [ModelClient] e os adaptadores à volta dele. Até ao ADR-034
	// transportava um `AuthorizationTaint` string, preenchível por qualquer adaptador, cuja
	// garantia «só o control-plane marca trusted» era convenção e não estrutura; a
	// autorização estruturalmente cunhada no runtime ficou DIFERIDA no DEF-807. Hoje o taint
	// da autorização é cunhado pelo loop a partir do CONTEXTO que o modelo viu
	// ([ContextAuthority]) e escrito directamente no [referencemonitor.CallContext]: não há
	// campo aqui por onde o modelo, ou um adaptador, o possa afirmar.

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
	// CustoNaoDerivado diz que o cliente NÃO TEM FONTE DE PREÇO para este turno (AOS-406): o
	// CostMicroUSD a zero é ausência de dados, não custo nulo. Em produção é o caso de um
	// modelo pago por subscrição, sem preço por token. Os tokens continuam medidos e o
	// orçamento em tokens continua a decidir; só o custo em dólares não existe.
	CustoNaoDerivado bool
	// Model é o modelo que SERVIU a resposta, tal como o cliente o reporta (AOS-396): no
	// Model Gateway, o `model` devolvido pelo provider (que pode ser uma versão datada do
	// modelo pedido, ou outro modelo se o gateway o trocou). Vazio quando o cliente não o
	// sabe. Vai para `manifest.model.served_model_id` do `turn.recorded`; o modelo PEDIDO
	// continua a vir de [Goal.Model].
	Model string
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
