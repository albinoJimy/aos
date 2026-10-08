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
}

// ErrBadRequestParams — um parâmetro do pedido está fora do conjunto fechado. A mensagem só
// leva o nome do campo: o valor recusado não é repetido.
var ErrBadRequestParams = errors.New("port: parametro do pedido fora do conjunto fechado")

// IsZero diz se o perfil não declara parâmetro nenhum.
func (p RequestParams) IsZero() bool {
	return p.Thinking == nil && p.ReasoningEffort == "" && p.MaxTokens == 0
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
	return nil
}

// Chaves de [RequestParams.Manifest]. São as únicas que um turno grava em `manifest.model.params`
// por causa do perfil da rota.
const (
	ParamKeyThinking        = "thinking"
	ParamKeyReasoningEffort = "reasoning_effort"
	ParamKeyMaxTokens       = "max_tokens"
)

// Manifest devolve os parâmetros na forma em que o turno os grava: chaves do conjunto fechado
// acima e valores de vocabulário fechado ou inteiros — `thinking` é o modo, seguido de `:` e do
// orçamento quando o tem (`enabled:4096`). nil quando o perfil não declara nenhum.
func (p RequestParams) Manifest() map[string]string {
	if p.IsZero() {
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
	return out
}

// Apply escreve os parâmetros do perfil no pedido que vai sair para a rota. `thinking` e
// `reasoning_effort` são SEMPRE os do perfil — os que o pedido trouxesse são apagados, mesmo
// quando o perfil não declara nenhum —; `max_tokens` só é sobreposto quando o perfil o declara.
func (p RequestParams) Apply(req *ChatRequest) {
	req.Thinking, req.ReasoningEffort = nil, p.ReasoningEffort
	if p.Thinking != nil {
		copia := *p.Thinking
		req.Thinking = &copia
	}
	if p.MaxTokens != 0 {
		req.MaxTokens = p.MaxTokens
	}
}
