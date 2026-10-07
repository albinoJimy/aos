package port_test

import (
	"errors"
	"testing"

	"github.com/aos-ref/platform/model-gateway/internal/wirefake"
	"github.com/aos-ref/platform/model-gateway/port"
)

// Correcções da revisão adversarial de AOS-507 e AOS-509 (2026-10-08).

func aos507Corpo(msg string) []byte {
	return []byte(`{"choices":[{"index":0,"message":` + msg + `,"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
}

// O-1 — O DIGEST NÃO COBRE NOMES DE CHAVE ESCRITOS PELO MODELO. Dois corpos que só diferem nas
// chaves DENTRO de `arguments` em objecto, de um campo de raciocínio, de `content` em partes ou
// de `provider_specific_fields` têm o mesmo digest: desses campos entra só o tipo do valor. Sem
// isto, quem lê o `turn.recorded` confirmava um palpite de chave por força bruta.
func TestAOS507_Digest_NaoDesceAoQueOModeloEscreve(t *testing.T) {
	pares := map[string][2]string{
		"arguments em objecto": {
			`{"content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"t","arguments":{"iban":"x"}}}]}`,
			`{"content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"t","arguments":{"nif":"y","morada":{"rua":1}}}}]}`,
		},
		"function_call antigo": {
			`{"content":null,"function_call":{"name":"t","arguments":{"iban":"x"}}}`,
			`{"content":null,"function_call":{"name":"t","arguments":{"nif":"y"}}}`,
		},
		"raciocinio em objecto": {
			`{"content":"r","reasoning":{"iban":"x"}}`,
			`{"content":"r","reasoning":{"nif":"y","passos":[{"a":1}]}}`,
		},
		"blocos de raciocinio": {
			`{"content":"r","thinking_blocks":[{"type":"thinking","iban":"x"}]}`,
			`{"content":"r","thinking_blocks":[{"type":"thinking","nif":"y"},{"outra":1}]}`,
		},
		"provider_specific_fields": {
			`{"content":"r","provider_specific_fields":{"iban":"x"}}`,
			`{"content":"r","provider_specific_fields":{"nif":{"y":1}}}`,
		},
		"content em partes": {
			`{"content":[{"type":"text","text":"a","iban":1}]}`,
			`{"content":[{"type":"text","text":"a","nif":1}]}`,
		},
	}
	for nome, par := range pares {
		a, b := port.ProbeResponseShape(aos507Corpo(par[0])), port.ProbeResponseShape(aos507Corpo(par[1]))
		if a.Digest == "" || a.Digest != b.Digest {
			t.Errorf("%s: as chaves que o modelo escreve mudaram o digest: %q e %q", nome, a.Digest, b.Digest)
		}
	}
	// O TIPO do valor continua a entrar: `arguments` em string e em objecto são formas diferentes.
	emString := port.ProbeResponseShape(aos507Corpo(`{"content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"t","arguments":"{}"}}]}`))
	emObjecto := port.ProbeResponseShape(aos507Corpo(pares["arguments em objecto"][0]))
	if emString.Digest == emObjecto.Digest {
		t.Errorf("o tipo do valor de `arguments` tinha de entrar no digest")
	}
	// E a estrutura do wire, fora desses campos, continua a distinguir formas.
	comChave := port.ProbeResponseShape(aos507Corpo(`{"content":"r","chave_nova_do_provider":1}`))
	semChave := port.ProbeResponseShape(aos507Corpo(`{"content":"r"}`))
	if comChave.Digest == semChave.Digest {
		t.Errorf("uma chave nova em `message` e outra forma: o digest tinha de mudar")
	}
}

// O-2 — UM CAMPO DE RACIOCÍNIO PRESENTE E VAZIO NÃO É RACIOCÍNIO. `reasoning_content: ""` com
// `content` vazio é a forma «nada» (H6) com o campo lá: tem de dar `vazio`, e não a classe de H1.
func TestAOS507_Raciocinio_PresenteEVazio(t *testing.T) {
	for msg, quer := range map[string]string{
		`{"content":"","reasoning_content":""}`:                              port.ShapeReasoningEmpty,
		`{"content":"","reasoning":""}`:                                      port.ShapeReasoningEmpty,
		`{"content":"","thinking_blocks":[]}`:                                port.ShapeReasoningEmpty,
		`{"content":"","reasoning_details":{}}`:                              port.ShapeReasoningEmpty,
		`{"content":"","thinking":false}`:                                    port.ShapeReasoningEmpty,
		`{"content":"","reasoning":0}`:                                       port.ShapeReasoningEmpty,
		`{"content":"","reasoning_content":"","thinking":""}`:                port.ShapeReasoningEmpty,
		`{"content":"","reasoning_content":null}`:                            port.ShapeReasoningNone,
		`{"content":""}`:                                                     port.ShapeReasoningNone,
		`{"content":"","reasoning_content":"penso"}`:                         port.ShapeReasoningContent,
		`{"content":"","reasoning_content":"","reasoning":"penso"}`:          port.ShapeReasoningReasoning,
		`{"content":"","reasoning_content":"a","thinking_blocks":[]}`:        port.ShapeReasoningContent,
		`{"content":"","reasoning_content":"a","thinking_blocks":[{"x":1}]}`: port.ShapeReasoningSeveral,
		`{"content":"","reasoning_content":" "}`:                             port.ShapeReasoningContent,
	} {
		if got := port.ProbeResponseShape(aos507Corpo(msg)).Reasoning; got != quer {
			t.Errorf("%s: reasoning = %q, quero %q", msg, got, quer)
		}
	}
	h1 := port.ProbeResponseShape(wirefake.Corpo("h1_raciocinio_em_reasoning_content"))
	vazio := port.ProbeResponseShape(wirefake.Corpo("vazio_com_reasoning_content_vazio"))
	if h1.Reasoning == vazio.Reasoning || vazio.Reasoning != port.ShapeReasoningEmpty || vazio.ReasoningBytes != 2 || vazio.ReasoningForm != port.ShapeFormString {
		t.Errorf("o campo vazio tinha de dar `vazio` (2 bytes, string), distinto de H1: %+v", vazio)
	}
}

// M-1 — CHAVES REPETIDAS lêem-se uma ocorrência de cada vez, como o campo string da base: uma
// string substitui, `null` não altera. M-2 — uma parte com `type` ou `text` repetido não é de
// texto. M-3 — `false`, `0`, `[]`, `{}` e `""` nos outros nomes do raciocínio não são raciocínio.
func TestAOS509_ChavesRepetidasEValoresVazios(t *testing.T) {
	for msg, quer := range map[string]string{
		`{"content":"a","content":null}`:                          "a",
		`{"content":null,"content":"a"}`:                          "a",
		`{"content":"a","content":"b"}`:                           "b",
		`{"content":"a","content":[{"type":"text","text":"b"}]}`:  "b",
		`{"content":[{"type":"text","text":"b"}],"content":null}`: "b",
		`{"content":"a","content":""}`:                            "",
	} {
		if m, err := aos509Mensagem(t, msg); err != nil || m.Content != quer {
			t.Errorf("%s: content = %q (%v), quero %q", msg, m.Content, err, quer)
		}
	}
	for msg, quer := range map[string]error{
		`{"content":"a","content":7}`:                                                                         port.ErrContentForm,
		`{"content":[{"type":"thinking","type":"text","text":"segredo"}]}`:                                    port.ErrContentPartNotText,
		`{"content":[{"type":"text","type":"thinking","text":"segredo"}]}`:                                    port.ErrContentPartNotText,
		`{"content":[{"type":"text","type":"text","text":"x"}]}`:                                              port.ErrContentPartNotText,
		`{"content":[{"type":"text","text":"a","text":"b"}]}`:                                                 port.ErrContentPartNotText,
		`{"content":[{"type":"text","text":"a"},{"type":"text","text":null}]}`:                                port.ErrContentPartNotText,
		`{"content":[{"type":["text"],"text":"a"}]}`:                                                          port.ErrContentPartNotText,
		`{"content":null,"tool_calls":[{"id":"c","function":{"name":"t","arguments":"{}","arguments":[1]}}]}`: port.ErrArgumentsForm,
	} {
		if _, err := aos509Mensagem(t, msg); !errors.Is(err, quer) {
			t.Errorf("%s: queria %v, veio %v", msg, quer, err)
		}
	}
	// `arguments` repetido: a string substitui, `null` não altera — como na base.
	m, err := aos509Mensagem(t, `{"content":null,"tool_calls":[{"id":"c","function":{"name":"t","arguments":"{\"a\":1}","arguments":null}}]}`)
	if err != nil || m.ToolCalls[0].Function.Arguments != `{"a":1}` || m.ToolCalls[0].Function.Name != "t" {
		t.Errorf("arguments repetido com null: %+v (%v)", m.ToolCalls, err)
	}
	for msg, quer := range map[string]string{
		`{"content":"r","thinking":false}`:                             "",
		`{"content":"r","reasoning":0}`:                                "",
		`{"content":"r","reasoning_details":[]}`:                       "",
		`{"content":"r","thinking_blocks":{}}`:                         "",
		`{"content":"r","reasoning":""}`:                               "",
		`{"content":"r","reasoning":"","thinking":"penso"}`:            "penso",
		`{"content":"r","reasoning":false,"reasoning_details":[1]}`:    "[1]",
		`{"content":"r","reasoning_content":false}`:                    "false", // o campo de sempre fica como o AOS-490 o lia
		`{"content":"r","reasoning_content":"","reasoning":"perdido"}`: "",
	} {
		if m, err := aos509Mensagem(t, msg); err != nil || m.ReasoningContent != quer {
			t.Errorf("%s: raciocinio = %q (%v), quero %q", msg, m.ReasoningContent, err, quer)
		}
	}
}
