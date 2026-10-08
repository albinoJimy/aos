package agentruntime

import "encoding/json"

// A FORMA DA RESPOSTA DO PROVIDER (AOS-507).
//
// Uma resposta que gasta tokens de saída e chega com o texto vazio dá hoje sempre o mesmo
// registo — um turno, `stop`, zero tool calls —, qualquer que tenha sido o corpo que o provider
// mandou. [ResponseShape] é a FICHA desse corpo: que campos vieram, em que forma JSON e com
// quantos bytes. É calculada por quem fez o pedido (no gateway, uma sonda sobre o corpo cru) e
// declarada na resposta, no molde de [ModelResponse.Projection] e [ModelResponse.RouteCheck].
//
// SEM CONTEÚDO. Todos os campos são de vocabulário FECHADO ou inteiros: nenhum byte de valor e
// nenhum nome de chave do provider. É o que a deixa ir em claro para o `turn.recorded`, como já
// vão o motivo de paragem e os tokens.
//
// NÃO DECIDE NADA. O runtime não lê a ficha: nem a regra de terminação ([TurnEndsRun]) nem o
// veredicto ([ConcludeRun]) a recebem. É só transporte até ao registo do turno.
//
// NÃO ENTRA NA CAPTURA, como [ModelResponse.ToolsOffered]: um turno reproduzido numa retoma
// volta sem ela, e no log fica o evento original.
type ResponseShape struct {
	// Unreadable — a sonda não conseguiu ler o corpo. Os outros campos não valem, e a ficha
	// grava-se só com `ilegivel`.
	Unreadable bool `json:"-"`
	// Content é a forma de `choices[0].message.content`; ContentBytes os bytes do texto (de uma
	// string, já descodificada) ou do valor JSON (das outras formas).
	Content      string `json:"content"`
	ContentBytes int64  `json:"content_bytes"`
	// Reasoning diz em que campo veio o raciocínio — só conta um campo COM conteúdo; um campo
	// presente e vazio dá `vazio`, distinto de `nenhum`. ReasoningForm é a forma JSON desse
	// campo (ausente sem campo nenhum); ReasoningBytes a soma dos bytes dos VALORES JSON de
	// todos os campos presentes (uma string vazia conta 2: as aspas); ReasoningSigned se a
	// mensagem traz alguma chave de assinatura.
	Reasoning       string `json:"reasoning"`
	ReasoningForm   string `json:"reasoning_form,omitempty"`
	ReasoningBytes  int64  `json:"reasoning_bytes"`
	ReasoningSigned string `json:"reasoning_signed"`
	// Refusal é a presença de `message.refusal`.
	Refusal string `json:"refusal"`
	// ProviderFieldsRefusal e ProviderFieldsReasoning são a recusa e o raciocínio DENTRO de
	// `message.provider_specific_fields`, nos vocabulários de Refusal e de Reasoning; ausentes
	// quando a mensagem não traz esse objecto. Um proxy move para lá os campos de `message` que
	// não conhece, e sem estes dois uma recusa chegava igual a «nada».
	ProviderFieldsRefusal   string `json:"psf_refusal,omitempty"`
	ProviderFieldsReasoning string `json:"psf_reasoning,omitempty"`
	// ToolCallsN é o número de tool calls; ToolCallID a classe da forma dos ids;
	// ToolCallIDMaxBytes o maior id; ArgumentsForm a forma JSON dos argumentos (ausente sem tool
	// calls).
	ToolCallsN         int64  `json:"tool_calls_n"`
	ToolCallID         string `json:"tool_call_id"`
	ToolCallIDMaxBytes int64  `json:"tool_call_id_max_bytes"`
	ArgumentsForm      string `json:"arguments_form,omitempty"`
	// LegacyFunctionCall — a mensagem traz `function_call` (a forma antiga).
	LegacyFunctionCall string `json:"legacy_function_call"`
	// ChoicesN é o número de `choices` da resposta.
	ChoicesN int64 `json:"choices_n"`
	// FinishReasonMapped — o `finish_reason` bruto é um dos que o AOS-491 mapeia.
	FinishReasonMapped string `json:"finish_reason_mapped"`
	// ReasoningTokens é `usage.completion_tokens_details.reasoning_tokens`; nil quando o provider
	// não o reporta.
	ReasoningTokens *int64 `json:"reasoning_tokens,omitempty"`
	// SystemFingerprint — a resposta traz `system_fingerprint`.
	SystemFingerprint string `json:"system_fingerprint"`
	// UnknownKeysN é o número de chaves de `message` que a sonda não conhece.
	UnknownKeysN int64 `json:"unknown_keys_n"`
	// ShapeDigest é o `sha256` da lista ordenada de pares (caminho da chave, tipo JSON) da
	// resposta: agrupa formas iguais sem guardar texto do provider. Ausente quando a resposta
	// excede os limites da sonda.
	ShapeDigest string `json:"shape_digest,omitempty"`
	// ProviderState diz se este turno trouxe ESTADO OPACO do provider e o que lhe aconteceu
	// (AOS-514): [ProviderStateCaptured] — foi para a captura — ou [ProviderStateNotReturnable]
	// — excedia o tecto e não foi guardado. Vazio (e omitido) quando o cliente não captura o
	// estado ou o turno não o trouxe: a ficha fica com os bytes de sempre. ProviderStateBytes é
	// o tamanho do envelope, nos dois casos. Nenhum byte do estado, e nem o seu digest.
	ProviderState      string `json:"provider_state,omitempty"`
	ProviderStateBytes int64  `json:"provider_state_bytes,omitempty"`
}

// Vocabulário FECHADO da ficha (AOS-507). O texto de cada valor é o que fica no `turn.recorded`.
const (
	// [ResponseShape.Content].
	ShapeContentAbsent     = "ausente"
	ShapeContentNull       = "nulo"
	ShapeContentEmpty      = "vazio"
	ShapeContentWhitespace = "so_brancos"
	ShapeContentText       = "texto"
	ShapeContentParts      = "partes"
	ShapeContentOther      = "outro"

	// [ResponseShape.Reasoning].
	ShapeReasoningNone      = "nenhum"
	ShapeReasoningContent   = "reasoning_content"
	ShapeReasoningReasoning = "reasoning"
	ShapeReasoningThinking  = "thinking"
	ShapeReasoningBlocks    = "thinking_blocks"
	ShapeReasoningDetails   = "reasoning_details"
	ShapeReasoningSeveral   = "varios"
	// ShapeReasoningEmpty — há um campo de raciocínio presente e VAZIO (`""`, `[]`, `{}`), e
	// nenhum com conteúdo. Não é raciocínio; é distinto de ausente.
	ShapeReasoningEmpty = "vazio"

	// [ResponseShape.ReasoningForm] e [ResponseShape.ArgumentsForm].
	ShapeFormString = "string"
	ShapeFormObject = "objecto"
	ShapeFormList   = "lista"
	ShapeFormOther  = "outro"

	// [ResponseShape.Refusal].
	ShapeRefusalAbsent = "ausente"
	ShapeRefusalNull   = "nulo"
	ShapeRefusalText   = "texto"

	// [ResponseShape.ToolCallID].
	ShapeIDNone      = "nenhum"
	ShapeIDCall      = "call_"
	ShapeIDFunctions = "functions_ponto"
	ShapeIDUUID      = "uuid"
	ShapeIDNumeric   = "numerico"
	ShapeIDEmpty     = "vazio"
	ShapeIDOther     = "outro"

	// Os campos de sim/não.
	ShapeYes = "sim"
	ShapeNo  = "nao"

	// ShapeUnreadable é o rótulo de uma ficha ilegível onde só cabe um valor (a métrica do nó).
	ShapeUnreadable = "ilegivel"
)

// ShapeContents devolve o vocabulário de [ResponseShape.Content], numa ordem fixa.
func ShapeContents() []string {
	return []string{ShapeContentAbsent, ShapeContentNull, ShapeContentEmpty, ShapeContentWhitespace, ShapeContentText, ShapeContentParts, ShapeContentOther}
}

// ShapeReasonings devolve o vocabulário de [ResponseShape.Reasoning], numa ordem fixa.
func ShapeReasonings() []string {
	return []string{ShapeReasoningNone, ShapeReasoningContent, ShapeReasoningReasoning, ShapeReasoningThinking, ShapeReasoningBlocks, ShapeReasoningDetails, ShapeReasoningSeveral, ShapeReasoningEmpty}
}

// maxShapeInt é o tecto de qualquer inteiro da ficha. O corpo de uma resposta está limitado a
// 1 MiB pelo adaptador; um contador acima disto não é uma medição.
const maxShapeInt = 1 << 40

func dentroDe(v string, vocab ...string) bool {
	for _, x := range vocab {
		if v == x {
			return true
		}
	}
	return false
}

func inteiroDaFicha(v int64) int64 {
	if v < 0 {
		return 0
	}
	if v > maxShapeInt {
		return maxShapeInt
	}
	return v
}

// Normalizado devolve a ficha DENTRO do vocabulário fechado, ou nil para uma ficha nil. Um campo
// com um valor fora do vocabulário torna a ficha inteira [ResponseShape.Unreadable]: o cliente de
// modelo é a fronteira untrusted e a ficha vai em claro para um evento — texto que não seja do
// vocabulário não passa, e não se adivinha o que queria dizer. Os inteiros ficam entre zero e um
// tecto, e o digest só passa com a forma `sha256:` + 64 hexadecimais.
func (s *ResponseShape) Normalizado() *ResponseShape {
	if s == nil {
		return nil
	}
	if s.Unreadable {
		return &ResponseShape{Unreadable: true}
	}
	simNao := []string{ShapeYes, ShapeNo}
	formas := []string{ShapeFormString, ShapeFormObject, ShapeFormList, ShapeFormOther}
	ok := dentroDe(s.Content, ShapeContents()...) &&
		dentroDe(s.Reasoning, ShapeReasonings()...) &&
		(s.ReasoningForm == "" || dentroDe(s.ReasoningForm, formas...)) &&
		dentroDe(s.ReasoningSigned, simNao...) &&
		dentroDe(s.Refusal, ShapeRefusalAbsent, ShapeRefusalNull, ShapeRefusalText) &&
		(s.ProviderFieldsRefusal == "" || dentroDe(s.ProviderFieldsRefusal, ShapeRefusalAbsent, ShapeRefusalNull, ShapeRefusalText)) &&
		(s.ProviderFieldsReasoning == "" || dentroDe(s.ProviderFieldsReasoning, ShapeReasonings()...)) &&
		dentroDe(s.ToolCallID, ShapeIDNone, ShapeIDCall, ShapeIDFunctions, ShapeIDUUID, ShapeIDNumeric, ShapeIDEmpty, ShapeIDOther) &&
		(s.ArgumentsForm == "" || dentroDe(s.ArgumentsForm, ShapeFormString, ShapeFormObject, ShapeFormOther)) &&
		dentroDe(s.LegacyFunctionCall, simNao...) &&
		dentroDe(s.FinishReasonMapped, simNao...) &&
		dentroDe(s.SystemFingerprint, simNao...) &&
		(s.ProviderState == "" || dentroDe(s.ProviderState, string(ProviderStateCaptured), string(ProviderStateNotReturnable)))
	if !ok {
		return &ResponseShape{Unreadable: true}
	}
	out := *s
	out.ContentBytes = inteiroDaFicha(s.ContentBytes)
	out.ReasoningBytes = inteiroDaFicha(s.ReasoningBytes)
	out.ToolCallsN = inteiroDaFicha(s.ToolCallsN)
	out.ToolCallIDMaxBytes = inteiroDaFicha(s.ToolCallIDMaxBytes)
	out.ChoicesN = inteiroDaFicha(s.ChoicesN)
	out.UnknownKeysN = inteiroDaFicha(s.UnknownKeysN)
	if s.ReasoningTokens != nil {
		v := inteiroDaFicha(*s.ReasoningTokens)
		out.ReasoningTokens = &v
	}
	out.ShapeDigest = NormalizeRouteProfileDigest(s.ShapeDigest)
	out.ProviderStateBytes = inteiroDaFicha(s.ProviderStateBytes)
	if out.ProviderState == "" {
		// Sem estado declarado não há tamanho a gravar.
		out.ProviderStateBytes = 0
	}
	return &out
}

// MarshalJSON grava a ficha. Uma ficha ilegível grava-se só com `{"ilegivel":true}`: os outros
// campos não foram medidos, e um zero ali lia-se como uma medição.
func (s ResponseShape) MarshalJSON() ([]byte, error) {
	if s.Unreadable {
		return []byte(`{"ilegivel":true}`), nil
	}
	type semMetodos ResponseShape
	return json.Marshal(semMetodos(s))
}

// UnmarshalJSON lê uma ficha gravada por [ResponseShape.MarshalJSON].
func (s *ResponseShape) UnmarshalJSON(data []byte) error {
	type semMetodos ResponseShape
	aux := struct {
		*semMetodos
		Ilegivel bool `json:"ilegivel"`
	}{semMetodos: (*semMetodos)(s)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	s.Unreadable = aux.Ilegivel
	return nil
}
