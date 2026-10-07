package port_test

import (
	"testing"

	"github.com/aos-ref/platform/model-gateway/internal/wirefake"
	"github.com/aos-ref/platform/model-gateway/port"
)

// A FICHA SOBRE O QUE O PROXY DE PRODUÇÃO ENTREGA (revisão do AOS-507, 2026-10-08). Os corpos são
// os que a imagem de produção do proxy entregou ao gateway para cada forma do `empty_output`
// (`internal/wirefake/casos_pos_proxy`). Medido: o proxy renomeia `reasoning` para
// `reasoning_content`, e MOVE `refusal`, `thinking` e `reasoning_details` para
// `message.provider_specific_fields`. A ficha lê esses nomes lá dentro, e por isso separa, pela
// rota de produção, as formas que de outro modo chegavam iguais a «nada».
func TestAOS507_PosProxy_AsFormasDoVazioContinuamSeparadas(t *testing.T) {
	ficha := func(caso string) port.ResponseShape {
		s := port.ProbeResponseShape(wirefake.CorpoPosProxy(caso))
		if s.Unreadable {
			t.Fatalf("%s: ficha ilegivel", caso)
		}
		return s
	}
	type classe struct{ content, reasoning, psfRefusal, psfReasoning string }
	de := func(s port.ResponseShape) classe {
		return classe{s.Content, s.Reasoning, s.PSFRefusal, s.PSFReasoning}
	}
	quer := map[string]classe{
		// H1 e H2 com o nome `reasoning` chegam as duas com `reasoning_content` (o proxy copia-o
		// para lá), mas a segunda traz também o `reasoning` original em provider_specific_fields.
		"h1_raciocinio_em_reasoning_content": {port.ShapeContentNull, port.ShapeReasoningContent, port.ShapeRefusalNull, port.ShapeReasoningNone},
		"h2_raciocinio_noutro_campo":         {port.ShapeContentEmpty, port.ShapeReasoningContent, port.ShapeRefusalNull, port.ShapeReasoningReasoning},
		// O raciocínio em `thinking` e em `reasoning_details` chega dentro de provider_specific_fields.
		"rac_thinking_content_vazio":          {port.ShapeContentEmpty, port.ShapeReasoningNone, port.ShapeRefusalNull, port.ShapeReasoningThinking},
		"rac_reasoning_details_content_vazio": {port.ShapeContentEmpty, port.ShapeReasoningNone, port.ShapeRefusalNull, port.ShapeReasoningDetails},
		// A recusa também.
		"h5_recusa": {port.ShapeContentNull, port.ShapeReasoningNone, port.ShapeRefusalText, port.ShapeReasoningNone},
		// «Nada», e «nada» com o campo de raciocínio vazio.
		"h6_nada":                           {port.ShapeContentEmpty, port.ShapeReasoningNone, port.ShapeRefusalNull, port.ShapeReasoningNone},
		"vazio_com_reasoning_content_vazio": {port.ShapeContentEmpty, port.ShapeReasoningEmpty, port.ShapeRefusalNull, port.ShapeReasoningNone},
	}
	for caso, c := range quer {
		if got := de(ficha(caso)); got != c {
			t.Errorf("%s: classe = %+v, quero %+v", caso, got, c)
		}
	}
	// H3 e H6 separam-se pelos tokens de raciocínio, que SOBREVIVEM ao proxy.
	if h3, h6 := ficha("h3_raciocinio_escondido"), ficha("h6_nada"); !h3.HasReasoningTokens || h3.ReasoningTokens != 77 || !h6.HasReasoningTokens || h6.ReasoningTokens != 0 {
		t.Errorf("os tokens de raciocinio tinham de sobreviver ao proxy: h3=%+v h6=%+v", h3, h6)
	}
	// As assinaturas também sobrevivem, nos blocos e na tool call.
	for _, caso := range []string{"rac_blocos_assinados", "rac_assinatura_na_tool_call"} {
		if !ficha(caso).ReasoningSigned {
			t.Errorf("%s: a assinatura tinha de sobreviver ao proxy", caso)
		}
	}
	// H7 e H8 chegam como vieram.
	if s := ficha("h7_function_call_antigo_stop"); !s.LegacyFunctionCall {
		t.Errorf("h7: function_call tinha de chegar")
	}
	if s := ficha("h8_resposta_na_segunda_choice"); s.ChoicesN != 2 {
		t.Errorf("h8: as duas choices tinham de chegar; vieram %d", s.ChoicesN)
	}
	// Uma mensagem sem `provider_specific_fields` não tem os dois campos.
	if s := port.ProbeResponseShape(wirefake.Corpo("h5_recusa")); s.PSFRefusal != "" || s.PSFReasoning != "" || s.Refusal != port.ShapeRefusalText {
		t.Errorf("sem provider_specific_fields os dois campos ficam vazios: %+v", s)
	}
}
