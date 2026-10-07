package port

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// A FORMA DA RESPOSTA (AOS-507, contrato 1.5.0).
//
// [ResponseShape] é a ficha do corpo cru de uma resposta de chat: que campos vieram, em que
// forma JSON e com quantos bytes. Não leva NENHUM byte de valor nem nenhum nome de chave do
// provider — só valores de vocabulário fechado (as constantes Shape* abaixo) e inteiros.
//
// É calculada por [ProbeResponseShape], uma sonda PRÓPRIA no molde de [wireUsageProbe] e
// [wireCachedProbe]: conhece só os campos que lhe interessam, corre ao lado da descodificação da
// resposta e NÃO a pode fazer falhar. Um corpo que a sonda não consiga ler dá uma ficha
// [ResponseShape.Unreadable].
//
// O caminho de streaming não é coberto: os runs não o usam.
type ResponseShape struct {
	// Unreadable — a sonda não conseguiu ler o corpo; os outros campos não valem.
	Unreadable bool
	// Content é a forma de `choices[0].message.content` (ShapeContent*); ContentBytes os bytes
	// do texto descodificado (string) ou do valor JSON (outras formas).
	Content      string
	ContentBytes int64
	// Reasoning é o campo em que veio o raciocínio (ShapeReasoning*); ReasoningForm a forma JSON
	// do primeiro presente, pela ordem de [nomesDoRaciocinio] (vazio sem raciocínio);
	// ReasoningBytes a soma dos bytes dos VALORES JSON presentes; ReasoningSigned se a mensagem
	// traz alguma chave `signature` ou `thought_signature`.
	Reasoning       string
	ReasoningForm   string
	ReasoningBytes  int64
	ReasoningSigned bool
	// Refusal é a presença de `message.refusal` (ShapeRefusal*).
	Refusal string
	// ToolCallsN, ToolCallID (ShapeID*), ToolCallIDMaxBytes e ArgumentsForm (ShapeForm*; vazio
	// sem tool calls) descrevem `message.tool_calls`.
	ToolCallsN         int64
	ToolCallID         string
	ToolCallIDMaxBytes int64
	ArgumentsForm      string
	// LegacyFunctionCall — a mensagem traz `function_call` (a forma antiga).
	LegacyFunctionCall bool
	// ChoicesN é o número de `choices`.
	ChoicesN int64
	// FinishReasonMapped — o `finish_reason` da primeira escolha é uma das constantes Finish*.
	FinishReasonMapped bool
	// ReasoningTokens é `usage.completion_tokens_details.reasoning_tokens`, e
	// HasReasoningTokens se o provider o reportou.
	ReasoningTokens    int64
	HasReasoningTokens bool
	// SystemFingerprint — a resposta traz `system_fingerprint`.
	SystemFingerprint bool
	// UnknownKeysN é o número de chaves de `message` que a sonda não conhece.
	UnknownKeysN int64
	// Digest é `sha256:` + o hash da lista ordenada de pares (caminho da chave, tipo JSON) da
	// resposta. Vazio quando a resposta excede os limites da sonda.
	Digest string
}

// Vocabulário fechado da ficha. Os textos são os que o runtime grava no `turn.recorded`.
const (
	ShapeContentAbsent     = "ausente"
	ShapeContentNull       = "nulo"
	ShapeContentEmpty      = "vazio"
	ShapeContentWhitespace = "so_brancos"
	ShapeContentText       = "texto"
	ShapeContentParts      = "partes"
	ShapeContentOther      = "outro"

	ShapeReasoningNone      = "nenhum"
	ShapeReasoningContent   = "reasoning_content"
	ShapeReasoningReasoning = "reasoning"
	ShapeReasoningThinking  = "thinking"
	ShapeReasoningBlocks    = "thinking_blocks"
	ShapeReasoningDetails   = "reasoning_details"
	ShapeReasoningSeveral   = "varios"

	ShapeFormString = "string"
	ShapeFormObject = "objecto"
	ShapeFormList   = "lista"
	ShapeFormOther  = "outro"

	ShapeRefusalAbsent = "ausente"
	ShapeRefusalNull   = "nulo"
	ShapeRefusalText   = "texto"

	ShapeIDNone      = "nenhum"
	ShapeIDCall      = "call_"
	ShapeIDFunctions = "functions_ponto"
	ShapeIDUUID      = "uuid"
	ShapeIDNumeric   = "numerico"
	ShapeIDEmpty     = "vazio"
	ShapeIDOther     = "outro"
)

// nomesDoRaciocinio são os campos de `message` em que um provider manda o raciocínio, pela ordem
// fixa em que o gateway os procura, e o valor do vocabulário de cada um.
var nomesDoRaciocinio = []struct{ chave, valor string }{
	{"reasoning_content", ShapeReasoningContent},
	{"reasoning", ShapeReasoningReasoning},
	{"reasoning_details", ShapeReasoningDetails},
	{"thinking_blocks", ShapeReasoningBlocks},
	{"thinking", ShapeReasoningThinking},
}

// chavesConhecidasDaMensagem são as chaves de `message` que a sonda conhece; as outras contam em
// [ResponseShape.UnknownKeysN].
var chavesConhecidasDaMensagem = map[string]bool{
	"role": true, "content": true, "tool_calls": true, "function_call": true, "refusal": true,
	"reasoning_content": true, "reasoning": true, "reasoning_details": true, "thinking_blocks": true, "thinking": true,
}

// Limites da sonda: profundidade a que desce e número de pares distintos do digest.
const (
	shapeMaxDepth = 32
	shapeMaxPairs = 4096
)

// ProbeResponseShape calcula a ficha da forma de um corpo de resposta de chat. Nunca devolve
// erro e nunca entra em pânico: o que não consegue ler dá [ResponseShape.Unreadable].
func ProbeResponseShape(data []byte) (shape ResponseShape) {
	defer func() {
		if recover() != nil {
			shape = ResponseShape{Unreadable: true}
		}
	}()
	var tudo any
	if err := json.Unmarshal(data, &tudo); err != nil {
		return ResponseShape{Unreadable: true}
	}
	topo, ok := tudo.(map[string]any)
	if !ok {
		return ResponseShape{Unreadable: true}
	}
	var cru map[string]json.RawMessage
	if err := json.Unmarshal(data, &cru); err != nil {
		return ResponseShape{Unreadable: true}
	}
	shape.Digest = digestDaForma(tudo)
	shape.SystemFingerprint = presente(cru["system_fingerprint"])
	shape.ReasoningTokens, shape.HasReasoningTokens = tokensDeRaciocinio(cru["usage"])

	var choices []json.RawMessage
	_ = json.Unmarshal(cru["choices"], &choices)
	shape.ChoicesN = int64(len(choices))
	var choice, msg map[string]json.RawMessage
	if len(choices) > 0 {
		_ = json.Unmarshal(choices[0], &choice)
	}
	_ = json.Unmarshal(choice["message"], &msg)

	var finish string
	if json.Unmarshal(choice["finish_reason"], &finish) == nil {
		switch finish {
		case FinishStop, FinishToolCalls, FinishFunctionCall, FinishLength, FinishContentFilter:
			shape.FinishReasonMapped = true
		}
	}
	shape.Content, shape.ContentBytes = formaDoConteudo(msg)
	sondarRaciocinio(msg, &shape)
	shape.Refusal = formaDaRecusa(msg)
	sondarToolCalls(msg["tool_calls"], &shape)
	shape.LegacyFunctionCall = presente(msg["function_call"])
	for k := range msg {
		if !chavesConhecidasDaMensagem[k] {
			shape.UnknownKeysN++
		}
	}
	// A assinatura procura-se na mensagem inteira (blocos de raciocínio e tool calls).
	if cs, ok := topo["choices"].([]any); ok && len(cs) > 0 {
		if c, ok := cs[0].(map[string]any); ok {
			shape.ReasoningSigned = temAssinatura(c["message"], 0)
		}
	}
	return shape
}

// presente diz se um valor JSON existe e não é `null`.
func presente(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

func formaDoConteudo(msg map[string]json.RawMessage) (string, int64) {
	raw, ok := msg["content"]
	switch {
	case !ok || len(raw) == 0:
		return ShapeContentAbsent, 0
	case string(raw) == "null":
		return ShapeContentNull, 0
	case raw[0] == '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return ShapeContentOther, int64(len(raw))
		}
		switch {
		case s == "":
			return ShapeContentEmpty, 0
		case strings.TrimSpace(s) == "":
			return ShapeContentWhitespace, int64(len(s))
		}
		return ShapeContentText, int64(len(s))
	case raw[0] == '[':
		return ShapeContentParts, int64(len(raw))
	}
	return ShapeContentOther, int64(len(raw))
}

func formaJSON(raw json.RawMessage) string {
	switch raw[0] {
	case '"':
		return ShapeFormString
	case '{':
		return ShapeFormObject
	case '[':
		return ShapeFormList
	}
	return ShapeFormOther
}

func sondarRaciocinio(msg map[string]json.RawMessage, shape *ResponseShape) {
	shape.Reasoning = ShapeReasoningNone
	n := 0
	for _, nome := range nomesDoRaciocinio {
		raw := msg[nome.chave]
		if !presente(raw) {
			continue
		}
		n++
		shape.ReasoningBytes += int64(len(raw))
		if n == 1 {
			shape.Reasoning, shape.ReasoningForm = nome.valor, formaJSON(raw)
		}
	}
	if n > 1 {
		shape.Reasoning = ShapeReasoningSeveral
	}
}

func formaDaRecusa(msg map[string]json.RawMessage) string {
	raw, ok := msg["refusal"]
	switch {
	case !ok || len(raw) == 0:
		return ShapeRefusalAbsent
	case string(raw) == "null", string(raw) == `""`:
		return ShapeRefusalNull
	}
	return ShapeRefusalText
}

func sondarToolCalls(raw json.RawMessage, shape *ResponseShape) {
	shape.ToolCallID = ShapeIDNone
	var calls []json.RawMessage
	if json.Unmarshal(raw, &calls) != nil || len(calls) == 0 {
		return
	}
	shape.ToolCallsN = int64(len(calls))
	for i, c := range calls {
		var call, fn map[string]json.RawMessage
		_ = json.Unmarshal(c, &call)
		_ = json.Unmarshal(call["function"], &fn)
		classe, bytes := classeDoID(call["id"])
		if bytes > shape.ToolCallIDMaxBytes {
			shape.ToolCallIDMaxBytes = bytes
		}
		forma := ShapeFormOther
		if args := fn["arguments"]; presente(args) && (args[0] == '"' || args[0] == '{') {
			forma = formaJSON(args)
		}
		if i == 0 {
			shape.ToolCallID, shape.ArgumentsForm = classe, forma
			continue
		}
		if classe != shape.ToolCallID {
			shape.ToolCallID = ShapeIDOther
		}
		if forma != shape.ArgumentsForm {
			shape.ArgumentsForm = ShapeFormOther
		}
	}
}

// classeDoID classifica a FORMA de um id de tool call, sem guardar nenhum byte dele.
func classeDoID(raw json.RawMessage) (string, int64) {
	if !presente(raw) {
		return ShapeIDEmpty, 0
	}
	if raw[0] != '"' {
		if raw[0] == '-' || (raw[0] >= '0' && raw[0] <= '9') {
			return ShapeIDNumeric, int64(len(raw))
		}
		return ShapeIDOther, int64(len(raw))
	}
	var id string
	if json.Unmarshal(raw, &id) != nil {
		return ShapeIDOther, int64(len(raw))
	}
	n := int64(len(id))
	switch {
	case id == "":
		return ShapeIDEmpty, 0
	case strings.HasPrefix(id, "call_"):
		return ShapeIDCall, n
	case strings.HasPrefix(id, "functions."):
		return ShapeIDFunctions, n
	case eUUID(id):
		return ShapeIDUUID, n
	case soDigitos(id):
		return ShapeIDNumeric, n
	}
	return ShapeIDOther, n
}

func soDigitos(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func eUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func tokensDeRaciocinio(usage json.RawMessage) (int64, bool) {
	var u, d map[string]json.RawMessage
	_ = json.Unmarshal(usage, &u)
	_ = json.Unmarshal(u["completion_tokens_details"], &d)
	var f float64
	raw := d["reasoning_tokens"]
	if !presente(raw) || json.Unmarshal(raw, &f) != nil {
		return 0, false
	}
	switch {
	case !(f >= 0):
		return 0, true
	case f > 1<<40:
		return 1 << 40, true
	}
	return int64(f), true
}

func temAssinatura(v any, depth int) bool {
	if depth > shapeMaxDepth {
		return false
	}
	switch x := v.(type) {
	case map[string]any:
		for k, f := range x {
			if k == "signature" || k == "thought_signature" || temAssinatura(f, depth+1) {
				return true
			}
		}
	case []any:
		for _, f := range x {
			if temAssinatura(f, depth+1) {
				return true
			}
		}
	}
	return false
}

// digestDaForma devolve `sha256:` + o hash da lista ordenada de pares (caminho, tipo JSON). Os
// elementos de uma lista partilham o mesmo caminho, pelo que o comprimento das listas não muda o
// digest; os VALORES nunca entram. Os nomes das chaves entram só no que é hashed. Devolve vazio
// se a resposta exceder [shapeMaxPairs].
func digestDaForma(v any) string {
	pares := map[string]struct{}{}
	if !paresDaForma(v, "", 0, pares) {
		return ""
	}
	linhas := make([]string, 0, len(pares))
	for p := range pares {
		linhas = append(linhas, p)
	}
	sort.Strings(linhas)
	h := sha256.New()
	for _, l := range linhas {
		_, _ = h.Write([]byte(l))
		_, _ = h.Write([]byte{'\n'})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

func paresDaForma(v any, caminho string, depth int, pares map[string]struct{}) bool {
	tipo := "null"
	switch v.(type) {
	case map[string]any:
		tipo = "object"
	case []any:
		tipo = "array"
	case string:
		tipo = "string"
	case float64:
		tipo = "number"
	case bool:
		tipo = "bool"
	}
	pares[caminho+"\x1e"+tipo] = struct{}{}
	if len(pares) > shapeMaxPairs {
		return false
	}
	if depth >= shapeMaxDepth {
		return true
	}
	switch x := v.(type) {
	case map[string]any:
		for k, f := range x {
			if !paresDaForma(f, caminho+"\x1f"+k, depth+1, pares) {
				return false
			}
		}
	case []any:
		for _, f := range x {
			if !paresDaForma(f, caminho+"\x1f[]", depth+1, pares) {
				return false
			}
		}
	}
	return true
}
