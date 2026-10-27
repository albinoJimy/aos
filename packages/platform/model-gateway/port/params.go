package port

import (
	"errors"
	"fmt"
	"strconv"
)

// OS PARÂMETROS DO PEDIDO DECLARADOS PELA ROTA (AOS-513, contrato 1.8.0).
//
// Até aqui o wire não tinha onde levar um parâmetro de raciocínio. [RequestParams] é o conjunto
// FECHADO e TIPADO dos parâmetros que o perfil de uma rota pode mandar enviar no pedido a essa
// rota. Não é um mapa: um nome que não seja um campo desta struct não existe, e um valor fora do
// vocabulário de cada campo é recusado por [RequestParams.Validate].
//
// DE ONDE VÊM. Só do perfil da rota, que o gateway aplica no momento em que sabe a que rota o
// pedido vai. [ChatRequest.Thinking] e [ChatRequest.ReasoningEffort] não se lêem de JSON
// (`json:"-"`) e o gateway SOBREPÕE-NOS em cada pedido com os do perfil — o que um chamador lá
// tenha posto é deitado fora. Um plano, um manifesto de run, o corpo de um pedido HTTP, o
// conteúdo de um run e a resposta de um modelo não têm caminho até eles.
//
// NENHUM PARÂMETRO FAZ DO RACIOCÍNIO UMA RESPOSTA (decisão D3). Regulam o que o provider faz;
// o texto de um turno continua a vir só de `content`.

// ThinkingParam é o parâmetro `thinking` do pedido.
type ThinkingParam struct {
	// Type é o modo, no vocabulário fechado ThinkingType*.
	Type string `json:"type"`
	// BudgetTokens é o orçamento de tokens de raciocínio. Só com [ThinkingTypeEnabled], e aí é
	// opcional; zero ⇒ o campo não vai no pedido.
	BudgetTokens int `json:"budget_tokens,omitempty"`
}

// Modos de [ThinkingParam.Type]. Vocabulário FECHADO.
const (
	ThinkingTypeEnabled  = "enabled"
	ThinkingTypeDisabled = "disabled"
	ThinkingTypeAdaptive = "adaptive"
)

// Valores de [RequestParams.ReasoningEffort]. Vocabulário FECHADO.
const (
	ReasoningEffortNone    = "none"
	ReasoningEffortMinimal = "minimal"
	ReasoningEffortLow     = "low"
	ReasoningEffortMedium  = "medium"
	ReasoningEffortHigh    = "high"
)

// Limites dos parâmetros numéricos.
const (
	// MinThinkingBudgetTokens é o menor orçamento de raciocínio aceite (o mínimo documentado
	// pelos fornecedores que o têm).
	MinThinkingBudgetTokens = 1024
	// MaxRequestParamTokens é o tecto de `max_tokens` e de `budget_tokens` num perfil: um valor
	// acima dele é um erro de escrita, não uma configuração.
	MaxRequestParamTokens = 1 << 20
)

// ReasoningParam é o parâmetro `reasoning` do pedido (AOS-516): a forma com que um agregador de
// modelos pede o raciocínio, seja qual for o fornecedor por trás. Declara-se EXACTAMENTE um dos
// dois campos: o esforço, no vocabulário de [RequestParams.ReasoningEffort], ou o orçamento em
// tokens. As outras chaves que essa forma tem nos fornecedores não existem aqui: não foram
// medidas.
type ReasoningParam struct {
	// Effort é o esforço de raciocínio, no vocabulário fechado ReasoningEffort*.
	Effort string `json:"effort,omitempty"`
	// MaxTokens é o orçamento de tokens de raciocínio, de [MinThinkingBudgetTokens] a
	// [MaxRequestParamTokens].
	MaxTokens int `json:"max_tokens,omitempty"`
}

// RequestParams são os parâmetros que o perfil de uma rota manda enviar no pedido. O valor-zero
// não envia nada, e o pedido é o de sempre, byte a byte.
type RequestParams struct {
	// Thinking é o `thinking` do pedido. nil ⇒ o campo não vai.
	Thinking *ThinkingParam `json:"thinking,omitempty"`
	// ReasoningEffort é o `reasoning_effort` do pedido. Vazio ⇒ o campo não vai.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// MaxTokens é o `max_tokens` do pedido. Zero ⇒ o perfil não o declara, e vale o que o
	// pedido já trazia.
	MaxTokens int `json:"max_tokens,omitempty"`
	// Reasoning é o `reasoning` do pedido (AOS-516). nil ⇒ o campo não vai. Não se declara ao
	// lado de Thinking nem de ReasoningEffort: são três formas de pedir a mesma coisa, e uma
	// rota pede-a de uma só maneira.
	Reasoning *ReasoningParam `json:"reasoning,omitempty"`
	// ToolChoice é a REGRA com que o gateway pede ao provider a tool call (AOS-526), no
	// vocabulário fechado ToolChoice* abaixo. Não é o valor do wire: com
	// [ToolChoiceWhileContractPending] o gateway escreve `tool_choice` no pedido só enquanto o run
	// ainda não pediu tool nenhuma e o contrato de conclusão tem tools por cumprir que o pedido
	// oferece ([ChatRequest.ContractToForce]). Vazio ⇒ o campo nunca vai, e o pedido é o de sempre.
	ToolChoice string `json:"tool_choice,omitempty"`
}

// Regras de [RequestParams.ToolChoice]. Vocabulário FECHADO.
const (
	// ToolChoiceNever — o gateway nunca escreve `tool_choice`. É a omissão (o campo vazio).
	ToolChoiceNever = ""
	// ToolChoiceNeverName é a forma escrita de [ToolChoiceNever] num perfil lido de JSON.
	ToolChoiceNeverName = "nunca"
	// ToolChoiceWhileContractPending — o pedido leva `tool_choice` só enquanto o run ainda não
	// pediu tool nenhuma e o contrato de conclusão tem tools por cumprir que o pedido oferece: a
	// tool nomeada quando é uma só, `required` quando são mais. A partir da primeira tool call do
	// run — de qualquer tool, recusada, falhada ou efectiva —, num nó sem contrato ou sem tools
	// oferecidas, o campo não vai. Cada run — e cada nova tentativa de um nó é um run novo — só pode
	// ser forçado nos turnos antes da sua primeira tool call.
	ToolChoiceWhileContractPending = "enquanto_o_contrato_nao_estiver_satisfeito"
)

// ErrBadRequestParams — um parâmetro do pedido está fora do conjunto fechado. A mensagem só
// leva o nome do campo: o valor recusado não é repetido.
var ErrBadRequestParams = errors.New("port: parametro do pedido fora do conjunto fechado")

// IsZero diz se o perfil não declara parâmetro nenhum.
func (p RequestParams) IsZero() bool {
	return p.Thinking == nil && p.ReasoningEffort == "" && p.MaxTokens == 0 && p.Reasoning == nil && p.ToolChoice == ToolChoiceNever
}

// Validate recusa qualquer valor fora do vocabulário de cada campo. Fail-closed: quem carrega um
// perfil chama-a antes de o usar, e um perfil recusado não compõe gateway nenhum.
func (p RequestParams) Validate() error {
	if t := p.Thinking; t != nil {
		switch t.Type {
		case ThinkingTypeEnabled:
			if t.BudgetTokens != 0 && (t.BudgetTokens < MinThinkingBudgetTokens || t.BudgetTokens > MaxRequestParamTokens) {
				return fmt.Errorf("%w: thinking.budget_tokens (aceite: ausente, ou de %d a %d)", ErrBadRequestParams, MinThinkingBudgetTokens, MaxRequestParamTokens)
			}
		case ThinkingTypeDisabled, ThinkingTypeAdaptive:
			if t.BudgetTokens != 0 {
				return fmt.Errorf("%w: thinking.budget_tokens so se declara com type enabled", ErrBadRequestParams)
			}
		default:
			return fmt.Errorf("%w: thinking.type (aceites: enabled, disabled, adaptive)", ErrBadRequestParams)
		}
	}
	switch p.ReasoningEffort {
	case "", ReasoningEffortNone, ReasoningEffortMinimal, ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh:
	default:
		return fmt.Errorf("%w: reasoning_effort (aceites: none, minimal, low, medium, high)", ErrBadRequestParams)
	}
	if p.MaxTokens < 0 || p.MaxTokens > MaxRequestParamTokens {
		return fmt.Errorf("%w: max_tokens (aceite: de 1 a %d)", ErrBadRequestParams, MaxRequestParamTokens)
	}
	if r := p.Reasoning; r != nil {
		if p.Thinking != nil || p.ReasoningEffort != "" {
			return fmt.Errorf("%w: reasoning nao se declara ao lado de thinking nem de reasoning_effort", ErrBadRequestParams)
		}
		if (r.Effort == "") == (r.MaxTokens == 0) {
			return fmt.Errorf("%w: reasoning declara exactamente um de effort e max_tokens", ErrBadRequestParams)
		}
		switch r.Effort {
		case "", ReasoningEffortNone, ReasoningEffortMinimal, ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh:
		default:
			return fmt.Errorf("%w: reasoning.effort (aceites: none, minimal, low, medium, high)", ErrBadRequestParams)
		}
		if r.MaxTokens != 0 && (r.MaxTokens < MinThinkingBudgetTokens || r.MaxTokens > MaxRequestParamTokens) {
			return fmt.Errorf("%w: reasoning.max_tokens (aceite: de %d a %d)", ErrBadRequestParams, MinThinkingBudgetTokens, MaxRequestParamTokens)
		}
		if r.MaxTokens != 0 && p.MaxTokens != 0 && r.MaxTokens >= p.MaxTokens {
			return fmt.Errorf("%w: reasoning.max_tokens tem de ser menor do que max_tokens", ErrBadRequestParams)
		}
	}
	// O orçamento de raciocínio conta para o `max_tokens`: com os dois declarados, um orçamento
	// que não caiba deixa zero tokens para a resposta (e há fornecedores que recusam o pedido).
	if p.Thinking != nil && p.Thinking.BudgetTokens != 0 && p.MaxTokens != 0 && p.Thinking.BudgetTokens >= p.MaxTokens {
		return fmt.Errorf("%w: thinking.budget_tokens tem de ser menor do que max_tokens", ErrBadRequestParams)
	}
	switch p.ToolChoice {
	case ToolChoiceNever, ToolChoiceWhileContractPending:
	default:
		return fmt.Errorf("%w: tool_choice (aceites: %s, %s)", ErrBadRequestParams, ToolChoiceNeverName, ToolChoiceWhileContractPending)
	}
	return nil
}

// Chaves de [RequestParams.Manifest]. São as únicas que um turno grava em `manifest.model.params`
// por causa do perfil da rota.
const (
	ParamKeyThinking        = "thinking"
	ParamKeyReasoningEffort = "reasoning_effort"
	ParamKeyMaxTokens       = "max_tokens"
	ParamKeyReasoning       = "reasoning"
	// ParamKeyToolChoice só entra no manifesto de um turno cujo pedido LEVOU `tool_choice`
	// (AOS-526), com o valor [ToolChoiceRequired] ou [ToolChoiceManifestNamed]. A regra do perfil
	// não entra: o que se grava é o que o pedido levou.
	ParamKeyToolChoice = "tool_choice"
	// ParamKeyProvider só entra no manifesto de um turno cujo pedido LEVOU a declaração de
	// fornecedor do perfil (AOS-565), com o valor de [ProviderPolicy.Manifest]. Não é um
	// [RequestParams]: é a fixação do fornecedor, que o gateway escreve ao lado dos parâmetros
	// e pela mesma razão — é por ela que um 4xx deste turno conta como recusa do que o perfil
	// mandou enviar.
	ParamKeyProvider = "provider"
)

// ToolChoiceManifestNamed é o valor de [ParamKeyToolChoice] num turno cujo pedido nomeou a tool.
// O nome da tool não vai no manifesto: é o do contrato, que o manifesto já grava.
const ToolChoiceManifestNamed = "function"

// Manifest devolve os parâmetros na forma em que o turno os grava: chaves do conjunto fechado
// acima e valores de vocabulário fechado ou inteiros — `thinking` é o modo, seguido de `:` e do
// orçamento quando o tem (`enabled:4096`); `reasoning` é o nome do campo declarado, `:` e o seu
// valor (`effort:medium`, `max_tokens:2048`). nil quando o perfil não declara nenhum. A regra de
// `tool_choice` não entra aqui: o manifesto grava, turno a turno, o que o pedido levou
// ([ApplyToolChoice]).
func (p RequestParams) Manifest() map[string]string {
	if p.Thinking == nil && p.ReasoningEffort == "" && p.MaxTokens == 0 && p.Reasoning == nil {
		return nil
	}
	out := map[string]string{}
	if t := p.Thinking; t != nil {
		v := t.Type
		if t.BudgetTokens != 0 {
			v += ":" + strconv.Itoa(t.BudgetTokens)
		}
		out[ParamKeyThinking] = v
	}
	if p.ReasoningEffort != "" {
		out[ParamKeyReasoningEffort] = p.ReasoningEffort
	}
	if p.MaxTokens != 0 {
		out[ParamKeyMaxTokens] = strconv.Itoa(p.MaxTokens)
	}
	if r := p.Reasoning; r != nil {
		if r.MaxTokens != 0 {
			out[ParamKeyReasoning] = "max_tokens:" + strconv.Itoa(r.MaxTokens)
		} else {
			out[ParamKeyReasoning] = "effort:" + r.Effort
		}
	}
	return out
}

// Declared devolve TUDO o que o perfil declara, na forma de [RequestParams.Manifest] mais a regra
// de `tool_choice` em [ParamKeyToolChoice] (AOS-526), com o seu nome do vocabulário. É para quem
// MOSTRA o perfil a uma pessoa — o resumo do que se vai assinar, o do banco —: uma regra que só
// muda o pedido em certos turnos não pode ficar fora do que o dono vê. nil quando não declara nada.
// Não é o que um turno grava: esse é [RequestParams.Manifest] mais o que o pedido levou.
func (p RequestParams) Declared() map[string]string {
	m := p.Manifest()
	if p.ToolChoice != ToolChoiceNever {
		if m == nil {
			m = map[string]string{}
		}
		m[ParamKeyToolChoice] = p.ToolChoice
	}
	return m
}

// Apply escreve os parâmetros do perfil no pedido que vai sair para a rota. `thinking`,
// `reasoning_effort` e `reasoning` são SEMPRE os do perfil — os que o pedido trouxesse são apagados, mesmo
// quando o perfil não declara nenhum —; `max_tokens` só é sobreposto quando o perfil o declara.
func (p RequestParams) Apply(req *ChatRequest) {
	req.Thinking, req.ReasoningEffort, req.Reasoning = nil, p.ReasoningEffort, nil
	if p.Reasoning != nil {
		copia := *p.Reasoning
		req.Reasoning = &copia
	}
	if p.Thinking != nil {
		copia := *p.Thinking
		req.Thinking = &copia
	}
	if p.MaxTokens != 0 {
		req.MaxTokens = p.MaxTokens
	}
}

// ApplyToolChoice escreve no pedido o `tool_choice` que a regra do perfil manda (AOS-526), e
// devolve o valor que o manifesto do turno grava em [ParamKeyToolChoice] — vazio quando o pedido
// não o leva.
//
// Com a regra [ToolChoiceNever] não toca no pedido: é o caminho de sempre, byte a byte. Com
// [ToolChoiceWhileContractPending], o `tool_choice` do pedido passa a ser SÓ o da regra — o que
// um chamador lá tivesse posto é apagado — e decide-se pelas tools que o kernel deixou forçar
// ([ChatRequest.ContractToForce]: as do contrato por cumprir, só enquanto o run não pediu tool
// nenhuma) e que o pedido OFERECE (`tools`, por nome exacto):
//
//   - nenhuma ⇒ o campo não vai (contrato satisfeito, nó sem contrato, sem tools oferecidas, ou o
//     run já pediu alguma tool — o modelo responde livremente, e a RAZÃO do veredicto é a mesma
//     que sem a regra; o 1.º turno é que foi forçado);
//   - uma ⇒ a tool nomeada (no wire, `{"type":"function","function":{"name":…}}`);
//   - mais de uma ⇒ [ToolChoiceRequired]: o provider tem de chamar uma tool, e o contrato diz
//     quais faltam; nomear uma delas obrigava-o a deixar as outras para outro turno.
func (p RequestParams) ApplyToolChoice(req *ChatRequest) string {
	if p.ToolChoice != ToolChoiceWhileContractPending {
		return ""
	}
	oferecidas := make(map[string]bool, len(req.Tools))
	for _, t := range req.Tools {
		oferecidas[t.Function.Name] = true
	}
	var porChamar []string
	for _, nome := range req.ContractToForce {
		if nome != "" && oferecidas[nome] {
			porChamar = append(porChamar, nome)
			oferecidas[nome] = false // um nome repetido no contrato conta uma vez
		}
	}
	switch len(porChamar) {
	case 0:
		req.ToolChoice = ""
		return ""
	case 1:
		if !toolChoiceReservado(porChamar[0]) {
			req.ToolChoice = porChamar[0]
			return ToolChoiceManifestNamed
		}
		// Uma tool chamada `auto`, `none` ou `required` não se nomeia por este campo: o wire lia-a
		// como o modo — e `none` proibia a chamada que a regra quer pedir. Vai `required`.
		req.ToolChoice = ToolChoiceRequired
		return ToolChoiceRequired
	default:
		req.ToolChoice = ToolChoiceRequired
		return ToolChoiceRequired
	}
}
