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
	// CacheReadTokens são os tokens de entrada que o provider serviu da sua cache de prefixo
	// (AOS-490). São um SUBCONJUNTO de InputTokens — a semântica do wire OpenAI
	// (`prompt_tokens_details.cached_tokens`) e a da contabilidade de custo do gateway —, não
	// um contador à parte. Zero quando o provider não os reporta: é medição de desempenho e
	// não entra em nenhuma decisão do runtime (o orçamento continua sobre InputTokens).
	CacheReadTokens int64

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
	// Reasoning é o raciocínio que o provider devolveu com este turno (o `reasoning_content`
	// do wire), como CARGA OPACA: o runtime não o lê, não o interpreta e não o altera
	// (AOS-490, ADR-036 §2.7). O ÚNICO destino é a captura do turno ([TurnCapture.Response]),
	// onde fica selado com o resto do conteúdo; a retoma e o replay devolvem-no igual. NÃO
	// entra no tail, no prompt, em spans nem em eventos em claro, e não é devolvido ao
	// provider. É saída do modelo, como [ModelResponse.Text]: não autoriza nada. Vazio quando
	// o provider não o envia.
	Reasoning string
	// Projection é a forma em que o cliente ENVIOU o prompt deste turno ao provider, quando
	// não foi a de sempre (AOS-490, ADR-036 §2.4): vazio ⇒ texto único — o prompt
	// materializado numa mensagem —, que é o que qualquer cliente anterior fazia;
	// [ProjectionNative] ⇒ mensagens nativas derivadas do tail. ProjectionVersion é a versão
	// da função de projecção usada (vazia com o texto único). O loop grava os dois no
	// manifesto do turno, para que o que foi enviado se reconstrua do registo: o
	// `prompt_hash` continua a ser o do tail canónico, e não o dos bytes enviados. É um facto
	// sobre o PEDIDO, declarado por quem o fez; não decide nada no runtime.
	Projection        string
	ProjectionVersion string
	// StopReason é o MOTIVO DE PARAGEM do turno, tal como o provider o declarou, já no
	// vocabulário fechado de [StopReason] (AOS-491). Quem traduz a resposta do provider — o
	// adaptador do gateway — faz a normalização; o valor bruto não entra no runtime, e o loop
	// fecha o vocabulário outra vez à entrada ([StopReason.Normalizado]) para que um cliente
	// que o preencha com outra coisa não chegue ao registo. Vazio ([StopUnreported]) quando o
	// provider não o envia ou o cliente não o sabe.
	//
	// Vai para a captura do turno, para o `turn.recorded` e para a contagem por motivo. NÃO
	// entra em [TurnEndsRun]. Desde o AOS-493 entra no VEREDICTO do run ([ConcludeRun]): um
	// turno que acaba o run com [StopLength] não é conclusão.
	StopReason StopReason
	// ToolsOffered é o número de tools que o cliente OFERECEU ao modelo no pedido deste turno
	// (AOS-491) — no gateway, quantos schemas o pedido levou no campo `tools`. Como
	// [ModelResponse.Projection], é um facto sobre o PEDIDO, declarado por quem o fez: o
	// runtime não o consegue saber de outra maneira, porque o que o pedido leva é decidido do
	// lado do cliente (o tool set do nó, cortado pela lista-branca do run) e pode não ser o
	// [Goal.Tools] que o prefixo lista. Zero quando o pedido não levou tools OU quando o
	// cliente não o declara. Vai para `tools_offered` do `turn.recorded`; não decide nada.
	//
	// A captura do turno NÃO o guarda (como não guarda o [ModelResponse.Model]): um turno
	// reproduzido numa retoma volta com zero em memória, e no log fica o evento original,
	// porque o Event Store descarta a regravação do mesmo passo.
	ToolsOffered int
	// RouteCheck é o resultado da comparação da ROTA deste turno com o perfil esperado, feita
	// por quem fez o pedido (AOS-505) — no gateway, o modelo que o proxy declarou ter servido
	// contra o do perfil do nome pedido. Vocabulário fechado ([RouteCheck]). Vazio ⇒ a rota não
	// está sob governação (ou o cliente não compara), e o turno regista-se como sempre.
	//
	// Quando vem preenchido, [ModelResponse.Model] é o modelo que o proxy DECLAROU — e fica
	// vazio se não o declarou; não é o nome pedido. RouteProfileDigest é o digest do perfil com
	// que se comparou (vazio se a rota não tem perfil). Os dois vão para o `turn.recorded`
	// (`route_check` e `manifest.model.route_profile_digest`) e para a captura do turno, que
	// neste caso guarda também o modelo servido. É medição declarada pelo cliente: não decide
	// nada no runtime.
	RouteCheck         RouteCheck
	RouteProfileDigest string
}

// RouteCheck é o resultado da comparação da rota de um turno com o perfil esperado, num
// vocabulário FECHADO (AOS-505). O texto de cada valor é o que fica gravado no `turn.recorded` e
// na captura, e o rótulo da métrica do nó.
type RouteCheck string

const (
	// RouteUngoverned — a rota não foi comparada. É o valor-zero, e o de qualquer turno gravado
	// antes do AOS-505.
	RouteUngoverned RouteCheck = ""
	// RouteEqual — a rota declarada é a do perfil.
	RouteEqual RouteCheck = "igual"
	// RouteDifferent — a rota declarada não é a do perfil, ou o nome pedido não tem perfil.
	RouteDifferent RouteCheck = "diferente"
	// RouteUnreported — quem serviu não declarou o que era preciso para comparar.
	RouteUnreported RouteCheck = "nao_reportado"
)

// Normalizado devolve o resultado DENTRO do vocabulário fechado: um valor conhecido fica como
// está; qualquer outro texto vira [RouteUnreported] — um resultado ilegível não prova igualdade.
func (c RouteCheck) Normalizado() RouteCheck {
	switch c {
	case RouteUngoverned, RouteEqual, RouteDifferent, RouteUnreported:
		return c
	default:
		return RouteUnreported
	}
}

// NormalizeRouteProfileDigest devolve o digest de um perfil de rota se ele tiver a forma
// `sha256:` + 64 dígitos hexadecimais minúsculos, e vazio caso contrário. É um campo declarado
// pelo cliente de modelo que fica em claro num evento: só a forma certa lá chega.
func NormalizeRouteProfileDigest(d string) string {
	const prefixo = "sha256:"
	if len(d) != len(prefixo)+64 || d[:len(prefixo)] != prefixo {
		return ""
	}
	for _, c := range d[len(prefixo):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return d
}

// StopReason é o motivo de paragem de um turno de modelo, num vocabulário FECHADO (AOS-491).
// O texto de cada valor é o que fica gravado na captura e no `turn.recorded`, e o rótulo da
// métrica por motivo — por ser fechado, nunca leva texto de terceiros.
type StopReason string

const (
	// StopUnreported — o provider não enviou motivo de paragem (ou o cliente não o sabe). É
	// o valor-zero, e é também o de qualquer captura gravada antes do AOS-491.
	StopUnreported StopReason = ""
	// StopStop — o modelo parou por si: deu o turno por acabado.
	StopStop StopReason = "stop"
	// StopToolCalls — o modelo parou para pedir tool calls.
	StopToolCalls StopReason = "tool_calls"
	// StopLength — a resposta foi CORTADA pelo limite de tokens.
	StopLength StopReason = "length"
	// StopContentFilter — a resposta foi retida ou cortada por um filtro de conteúdo.
	StopContentFilter StopReason = "content_filter"
	// StopOther — o provider declarou um motivo fora do mapa conhecido. O valor bruto não é
	// guardado em lado nenhum do runtime.
	StopOther StopReason = "other"
)

// StopReasons devolve o vocabulário fechado, numa ordem fixa. É o que quem rotula por motivo
// de paragem (a métrica do nó) percorre.
func StopReasons() []StopReason {
	return []StopReason{StopStop, StopToolCalls, StopLength, StopContentFilter, StopOther, StopUnreported}
}

// Normalizado devolve o motivo DENTRO do vocabulário fechado: um valor conhecido fica como
// está; qualquer outro texto vira [StopOther]. O vazio é do vocabulário ([StopUnreported]).
func (s StopReason) Normalizado() StopReason {
	switch s {
	case StopUnreported, StopStop, StopToolCalls, StopLength, StopContentFilter, StopOther:
		return s
	default:
		return StopOther
	}
}

// ProjectionNative é o valor de [ModelResponse.Projection] e de `manifest.projection` para um
// turno enviado ao provider em mensagens nativas (AOS-490). O texto único não tem valor: é a
// ausência do campo.
const ProjectionNative = "native"

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
