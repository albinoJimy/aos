package port_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/internal/wirefake"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-507 — A SONDA DA FORMA DA RESPOSTA.

// semDigest é a ficha sem o digest: a «classe» pela qual duas respostas se distinguem nos campos
// de vocabulário fechado e nos inteiros.
func semDigest(s port.ResponseShape) port.ResponseShape {
	s.Digest = ""
	return s
}

// CADA UMA DAS OITO FORMAS QUE ENCAIXAM NUM `empty_output` DÁ UMA CLASSE DISTINTA (desenho A2
// §2.2). Hoje as oito dão o mesmo registo: um turno, `stop`, zero tool calls, texto vazio.
func TestAOS507_H1aH8_ClassesDistintas(t *testing.T) {
	casos := []struct {
		nome  string
		prova func(port.ResponseShape) bool
	}{
		{"h1_raciocinio_em_reasoning_content", func(s port.ResponseShape) bool {
			return s.Content == port.ShapeContentNull && s.Reasoning == port.ShapeReasoningContent && s.ReasoningBytes > 2
		}},
		{"h2_raciocinio_noutro_campo", func(s port.ResponseShape) bool {
			return s.Content == port.ShapeContentEmpty && s.Reasoning == port.ShapeReasoningReasoning && s.ReasoningBytes > 2
		}},
		{"h3_raciocinio_escondido", func(s port.ResponseShape) bool {
			return s.Reasoning == port.ShapeReasoningNone && s.HasReasoningTokens && s.ReasoningTokens == 77
		}},
		{"h4_so_brancos", func(s port.ResponseShape) bool {
			return s.Content == port.ShapeContentWhitespace && s.ContentBytes == 5
		}},
		{"h5_recusa", func(s port.ResponseShape) bool { return s.Refusal == port.ShapeRefusalText }},
		{"h6_nada", func(s port.ResponseShape) bool {
			return s.Content == port.ShapeContentEmpty && s.Reasoning == port.ShapeReasoningNone && s.HasReasoningTokens && s.ReasoningTokens == 0 &&
				s.Refusal == port.ShapeRefusalAbsent && !s.LegacyFunctionCall && s.ChoicesN == 1
		}},
		{"h7_function_call_antigo_stop", func(s port.ResponseShape) bool { return s.LegacyFunctionCall && s.ToolCallsN == 0 }},
		{"h8_resposta_na_segunda_choice", func(s port.ResponseShape) bool { return s.ChoicesN == 2 }},
	}
	vistas := map[port.ResponseShape]string{}
	for _, c := range casos {
		s := port.ProbeResponseShape(wirefake.Corpo(c.nome))
		if s.Unreadable {
			t.Fatalf("%s: ficha ilegivel", c.nome)
		}
		if !c.prova(s) {
			t.Errorf("%s: a ficha nao tem o campo que a distingue: %+v", c.nome, s)
		}
		if !s.FinishReasonMapped || s.ToolCallsN != 0 {
			t.Errorf("%s: todas as oito tem finish_reason `stop` e zero tool calls: %+v", c.nome, s)
		}
		classe := semDigest(s)
		if outro, ha := vistas[classe]; ha {
			t.Errorf("%s e %s dao a MESMA classe: %+v", c.nome, outro, classe)
		}
		vistas[classe] = c.nome
	}
}

// A SONDA NÃO PODE FAZER FALHAR A RESPOSTA: o que não consegue ler dá `ilegivel`, e tipos
// inesperados em todos os campos dão uma ficha dentro do vocabulário.
func TestAOS507_Sonda_CorposIlegiveisETiposInesperados(t *testing.T) {
	inteiro := string(wirefake.Corpo("content_texto"))
	for nome, corpo := range map[string]string{
		"vazio":            "",
		"truncado":         inteiro[:len(inteiro)/2],
		"json invalido":    `{"choices":[{"message":{"content":}}]}`,
		"lista no topo":    `[1,2,3]`,
		"string no topo":   `"ola"`,
		"nulo":             `null`,
		"aninhado sem fim": strings.Repeat("[", 20000) + strings.Repeat("]", 20000),
	} {
		if s := port.ProbeResponseShape([]byte(corpo)); !s.Unreadable || s != (port.ResponseShape{Unreadable: true}) {
			t.Errorf("%s: queria so `ilegivel`, veio %+v", nome, s)
		}
	}
	for nome, corpo := range map[string]string{
		"tudo numeros":       `{"choices":7,"usage":7,"system_fingerprint":7}`,
		"choices de numeros": `{"choices":[7,8]}`,
		"message e string":   `{"choices":[{"message":"ola","finish_reason":7}]}`,
		"campos trocados":    `{"choices":[{"message":{"content":{"a":1},"tool_calls":"x","refusal":7,"function_call":[],"reasoning_content":7,"reasoning":true},"finish_reason":[]}],"usage":{"completion_tokens_details":{"reasoning_tokens":"muitos"}}}`,
		"tool calls tortas":  `{"choices":[{"message":{"tool_calls":[7,{"id":{},"function":7},{"function":{"arguments":[1]}}]}}],"usage":{"completion_tokens_details":7}}`,
		"tokens negativos":   `{"choices":[{"message":{}}],"usage":{"completion_tokens_details":{"reasoning_tokens":-5}}}`,
	} {
		s := port.ProbeResponseShape([]byte(corpo))
		if s.Unreadable {
			t.Errorf("%s: um objecto JSON valido le-se sempre; veio ilegivel", nome)
		}
		if s.Content == "" || s.Reasoning == "" || s.Refusal == "" || s.ToolCallID == "" {
			t.Errorf("%s: campo de vocabulario por preencher: %+v", nome, s)
		}
		if s.ReasoningTokens < 0 {
			t.Errorf("%s: tokens de raciocinio negativos: %+v", nome, s)
		}
	}
	// Uma resposta com mais pares distintos do que o limite fica sem digest, e o resto mede-se.
	var b strings.Builder
	b.WriteString(`{"choices":[{"message":{"content":"x"}}],"extra":{`)
	for i := 0; i < 5000; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `"k%d":1`, i)
	}
	b.WriteString(`}}`)
	if s := port.ProbeResponseShape([]byte(b.String())); s.Unreadable || s.Digest != "" || s.Content != port.ShapeContentText {
		t.Errorf("acima do limite de pares: queria a ficha sem digest, veio %+v", s)
	}
}

// SEM CONTEÚDO. Um corpo com sentinelas em todos os valores e em nomes de chave desconhecidos
// não deixa nenhuma na ficha; dois corpos que só diferem nos valores têm o mesmo digest; e um
// nome de chave diferente muda o digest sem aparecer em claro.
func TestAOS507_Sonda_SemConteudo(t *testing.T) {
	corpo := func(marca, chave string) []byte {
		return []byte(`{"id":"` + marca + `","model":"` + marca + `","system_fingerprint":"` + marca + `","` + chave + `":"` + marca + `",` +
			`"choices":[{"index":0,"finish_reason":"` + marca + `","message":{"role":"assistant","content":"` + marca + `",` +
			`"reasoning_content":"` + marca + `","reasoning":{"` + chave + `":"` + marca + `"},"thinking_blocks":[{"thinking":"` + marca + `","signature":"` + marca + `"}],` +
			`"refusal":"` + marca + `","function_call":{"name":"` + marca + `","arguments":"` + marca + `"},"` + chave + `":"` + marca + `",` +
			`"tool_calls":[{"id":"` + marca + `","type":"function","function":{"name":"` + marca + `","arguments":"{\"k\":\"` + marca + `\"}"}}]}}],` +
			`"usage":{"prompt_tokens":3,"completion_tokens":4,"completion_tokens_details":{"reasoning_tokens":2}}}`)
	}
	const marca, chave = "SENTINELA-VALOR-507", "sentinela_chave_507"
	a := port.ProbeResponseShape(corpo(marca, chave))
	if a.Unreadable {
		t.Fatal("ficha ilegivel")
	}
	if dump := fmt.Sprintf("%+v", a); strings.Contains(dump, marca) || strings.Contains(dump, chave) || strings.Contains(strings.ToLower(dump), "sentinela") {
		t.Fatalf("a ficha leva conteudo do provider: %s", dump)
	}
	if a.UnknownKeysN != 1 || !a.ReasoningSigned || a.Reasoning != port.ShapeReasoningSeveral || a.ToolCallID != port.ShapeIDOther || a.FinishReasonMapped {
		t.Errorf("a ficha nao mediu o corpo: %+v", a)
	}
	// Só os valores mudam (com o mesmo comprimento, para os inteiros não mudarem): a ficha é a mesma.
	b := port.ProbeResponseShape(corpo("OUTRO-VALOR-0000-507", chave))
	if len("OUTRO-VALOR-0000-507") != len(marca)+1 {
		t.Fatal("o teste precisa de marcas de comprimentos diferentes")
	}
	if a.Digest == "" || a.Digest != b.Digest {
		t.Errorf("dois corpos que so diferem nos valores tem de ter o mesmo digest: %q e %q", a.Digest, b.Digest)
	}
	if a.ContentBytes == b.ContentBytes {
		t.Errorf("os tamanhos medem-se: %d e %d", a.ContentBytes, b.ContentBytes)
	}
	c := port.ProbeResponseShape(corpo(marca, "outra_chave_507"))
	if c.Digest == a.Digest {
		t.Errorf("um nome de chave diferente e outra forma: o digest tinha de mudar")
	}
	if !strings.HasPrefix(a.Digest, "sha256:") || len(a.Digest) != len("sha256:")+64 {
		t.Errorf("digest com forma errada: %q", a.Digest)
	}
}

// OS CAMPOS, UM A UM, sobre os casos do wirefake.
func TestAOS507_Sonda_CamposPorCaso(t *testing.T) {
	ficha := func(caso string) port.ResponseShape { return port.ProbeResponseShape(wirefake.Corpo(caso)) }
	for caso, quer := range map[string]string{
		"content_ausente": port.ShapeContentAbsent, "content_nulo": port.ShapeContentNull, "content_vazio": port.ShapeContentEmpty,
		"h4_so_brancos": port.ShapeContentWhitespace, "content_texto": port.ShapeContentText,
		"content_partes_texto": port.ShapeContentParts, "content_partes_com_imagem": port.ShapeContentParts, "content_numero": port.ShapeContentOther,
	} {
		if got := ficha(caso).Content; got != quer {
			t.Errorf("%s: content = %q, quero %q", caso, got, quer)
		}
	}
	for caso, quer := range map[string][2]string{
		"rac_reasoning_content_string":           {port.ShapeReasoningContent, port.ShapeFormString},
		"rac_reasoning_objecto":                  {port.ShapeReasoningReasoning, port.ShapeFormObject},
		"rac_reasoning_details_lista":            {port.ShapeReasoningDetails, port.ShapeFormList},
		"rac_thinking_blocks_lista":              {port.ShapeReasoningBlocks, port.ShapeFormList},
		"rac_thinking_string":                    {port.ShapeReasoningThinking, port.ShapeFormString},
		"rac_varios_nomes":                       {port.ShapeReasoningSeveral, port.ShapeFormString},
		"rac_reasoning_content_nulo_e_reasoning": {port.ShapeReasoningReasoning, port.ShapeFormString},
		"content_texto":                          {port.ShapeReasoningNone, ""},
	} {
		if s := ficha(caso); s.Reasoning != quer[0] || s.ReasoningForm != quer[1] {
			t.Errorf("%s: reasoning = %q/%q, quero %q/%q", caso, s.Reasoning, s.ReasoningForm, quer[0], quer[1])
		}
	}
	for caso, quer := range map[string]string{
		"content_texto": port.ShapeIDNone, "content_nulo_com_tool": port.ShapeIDCall, "id_functions_ponto": port.ShapeIDFunctions,
		"id_numerico": port.ShapeIDNumeric, "id_numero_json": port.ShapeIDNumeric, "id_uuid": port.ShapeIDUUID,
		"id_vazio": port.ShapeIDEmpty, "id_ausente": port.ShapeIDEmpty, "id_comprimento_fixo": port.ShapeIDOther, "id_repetido": port.ShapeIDCall,
	} {
		if got := ficha(caso).ToolCallID; got != quer {
			t.Errorf("%s: tool_call_id = %q, quero %q", caso, got, quer)
		}
	}
	for caso, quer := range map[string]string{
		"content_texto": "", "content_nulo_com_tool": port.ShapeFormString, "args_objecto": port.ShapeFormObject,
		"args_lista": port.ShapeFormOther, "args_nulo": port.ShapeFormOther, "args_vazio": port.ShapeFormString,
	} {
		if got := ficha(caso).ArgumentsForm; got != quer {
			t.Errorf("%s: arguments_form = %q, quero %q", caso, got, quer)
		}
	}
	if s := ficha("tools_paralelas"); s.ToolCallsN != 2 || s.ToolCallIDMaxBytes != int64(len("call_dois")) {
		t.Errorf("tools_paralelas: %+v", s)
	}
	if s := ficha("rac_blocos_assinados"); !s.ReasoningSigned {
		t.Errorf("blocos assinados: reasoning_signed tinha de ser sim")
	}
	if s := ficha("rac_assinatura_na_tool_call"); !s.ReasoningSigned {
		t.Errorf("thought_signature numa tool call: reasoning_signed tinha de ser sim")
	}
	if s := ficha("content_texto"); s.ReasoningSigned || s.SystemFingerprint || s.UnknownKeysN != 0 || s.HasReasoningTokens {
		t.Errorf("content_texto: %+v", s)
	}
	if s := ficha("extra_system_fingerprint_e_chaves"); !s.SystemFingerprint || s.UnknownKeysN != 2 {
		t.Errorf("extra: %+v", s)
	}
	if s := ficha("usage_com_reasoning_tokens"); !s.HasReasoningTokens || s.ReasoningTokens != 33 {
		t.Errorf("reasoning_tokens: %+v", s)
	}
	for caso, quer := range map[string]bool{"content_texto": true, "finish_length": true, "fc_antigo_finish_function_call": true,
		"finish_end_turn": false, "finish_stop_maiusculas": false, "finish_ausente": false, "finish_nulo": false} {
		if got := ficha(caso).FinishReasonMapped; got != quer {
			t.Errorf("%s: finish_reason_mapped = %v, quero %v", caso, got, quer)
		}
	}
	if s := ficha("choices_vazio"); s.Unreadable || s.ChoicesN != 0 || s.Content != port.ShapeContentAbsent {
		t.Errorf("choices_vazio: %+v", s)
	}
}
