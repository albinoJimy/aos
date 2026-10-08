package port_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-509 — a descodificação tolerante da mensagem e da invocação de função, na porta.

func aos509Mensagem(t *testing.T, msg string) (port.Message, error) {
	t.Helper()
	resp, err := port.UnmarshalChatResponse([]byte(`{"choices":[{"index":0,"message":` + msg + `,"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	if err != nil {
		return port.Message{}, err
	}
	return resp.Choices[0].Message, nil
}

func TestAOS509_Content_PartesDeTexto(t *testing.T) {
	for msg, quer := range map[string]string{
		`{"content":[{"type":"text","text":"um"}]}`:                                "um",
		`{"content":[{"type":"text","text":"um"},{"type":"text","text":" dois"}]}`: "um dois",
		`{"content":[{"type":"text","text":""},{"type":"text","text":"x"}]}`:       "x",
		`{"content":[]}`:                "",
		`{"content":null}`:              "",
		`{}`:                            "",
		`{"content":""}`:                "",
		`{"content":"texto de sempre"}`: "texto de sempre",
		`{"content":[{"type":"text","text":"a\nb","cache_control":{"type":"ephemeral"}}]}`: "a\nb",
	} {
		m, err := aos509Mensagem(t, msg)
		if err != nil || m.Content != quer {
			t.Errorf("%s: content = %q (%v), quero %q", msg, m.Content, err, quer)
		}
	}
	for msg, quer := range map[string]error{
		`{"content":[{"type":"text","text":"um"},{"type":"image_url","image_url":{"url":"u"}}]}`: port.ErrContentPartNotText,
		`{"content":[{"type":"input_audio"}]}`:                                                   port.ErrContentPartNotText,
		`{"content":[{"type":"refusal","refusal":"r"}]}`:                                         port.ErrContentPartNotText,
		`{"content":[{"text":"sem type"}]}`:                                                      port.ErrContentPartNotText,
		`{"content":[{"type":"text"}]}`:                                                          port.ErrContentPartNotText,
		`{"content":[{"type":"text","text":7}]}`:                                                 port.ErrContentPartNotText,
		`{"content":[{"type":"text","text":null}]}`:                                              port.ErrContentPartNotText,
		`{"content":["texto solto"]}`:                                                            port.ErrContentPartNotText,
		`{"content":[null]}`:                                                                     port.ErrContentPartNotText,
		`{"content":[[{"type":"text","text":"aninhada"}]]}`:                                      port.ErrContentPartNotText,
		`{"content":7}`:            port.ErrContentForm,
		`{"content":{"text":"x"}}`: port.ErrContentForm,
		`{"content":true}`:         port.ErrContentForm,
	} {
		if _, err := aos509Mensagem(t, msg); !errors.Is(err, quer) {
			t.Errorf("%s: queria %v, veio %v", msg, quer, err)
		}
	}
}

func TestAOS509_Arguments_ObjectoSaoOsBytesCrus(t *testing.T) {
	tc := func(args string) string {
		return `{"content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"arquivo","arguments":` + args + `}}]}`
	}
	for args, quer := range map[string]string{
		`"{\"b\":1,\"a\":2}"`:        `{"b":1,"a":2}`,
		`{"b":1,"a":2}`:              `{"b":1,"a":2}`, // a ordem das chaves é a que veio
		`{ "b" : 1,  "a":[1, 2] }`:   `{ "b" : 1,  "a":[1, 2] }`,
		`{"a":{"a":{"a":{"a":{}}}}}`: `{"a":{"a":{"a":{"a":{}}}}}`,
		`{"a":1,"a":2}`:              `{"a":1,"a":2}`, // chaves repetidas: quem decide é o schema da tool, como com a string
		`{}`:                         `{}`,
		`""`:                         ``,
		`null`:                       ``,
		`"nao e json"`:               `nao e json`,
	} {
		m, err := aos509Mensagem(t, tc(args))
		if err != nil || len(m.ToolCalls) != 1 || m.ToolCalls[0].Function.Arguments != quer || m.ToolCalls[0].Function.Name != "arquivo" {
			t.Errorf("%s: veio %+v (%v), quero os argumentos %q", args, m.ToolCalls, err, quer)
		}
		// Determinístico: duas leituras do mesmo corpo dão os mesmos bytes.
		if m2, _ := aos509Mensagem(t, tc(args)); len(m2.ToolCalls) != 1 || m2.ToolCalls[0].Function.Arguments != quer {
			t.Errorf("%s: a segunda leitura deu outros bytes", args)
		}
	}
	for _, args := range []string{`[1,2]`, `7`, `true`, `[{"a":1}]`} {
		if _, err := aos509Mensagem(t, tc(args)); !errors.Is(err, port.ErrArgumentsForm) {
			t.Errorf("%s: queria ErrArgumentsForm, veio %v", args, err)
		}
	}
	// Um pedido continua a levar os argumentos como STRING, mesmo os que vieram em objecto.
	m, _ := aos509Mensagem(t, tc(`{"a":1}`))
	m.Role = port.RoleAssistant
	cru, err := port.ChatRequest{Model: "m", Messages: []port.Message{m}}.MarshalWire(false)
	if err != nil || !strings.Contains(string(cru), `"arguments":"{\"a\":1}"`) {
		t.Errorf("o pedido tinha de levar os argumentos em string: %s (%v)", cru, err)
	}
}

// O RACIOCÍNIO DE QUALQUER NOME NÃO SAI NEM APARECE: é lido para [port.Message.ReasoningContent],
// nunca para `Content`, e `MarshalWire` retira-o de todos os pedidos.
func TestAOS509_Raciocinio_OutrosNomes_NaoSaiEmNenhumPedido(t *testing.T) {
	const marca = "SENTINELA-RACIOCINIO-509"
	for _, nome := range []string{"reasoning_content", "reasoning", "reasoning_details", "thinking_blocks", "thinking"} {
		for forma, valor := range map[string]string{
			"string": `"` + marca + `"`, "objecto": `{"text":"` + marca + `"}`, "lista": `[{"thinking":"` + marca + `","signature":"` + marca + `"}]`,
		} {
			for _, content := range []string{`null`, `""`, `"  \n"`, `"resposta"`} {
				m, err := aos509Mensagem(t, `{"role":"assistant","content":`+content+`,"`+nome+`":`+valor+`}`)
				if err != nil {
					t.Fatalf("%s/%s: nunca e erro: %v", nome, forma, err)
				}
				var quer string
				_ = json.Unmarshal([]byte(content), &quer)
				if m.Content != quer || strings.Contains(m.Content, marca) {
					t.Errorf("%s/%s, content %s: Content = %q — o raciocinio nunca e a resposta", nome, forma, content, m.Content)
				}
				if !strings.Contains(m.ReasoningContent, marca) {
					t.Errorf("%s/%s: o raciocinio tinha de ser lido como carga opaca; veio %q", nome, forma, m.ReasoningContent)
				}
				if forma != "string" && m.ReasoningContent != valor {
					t.Errorf("%s/%s: uma forma que nao e string guarda os bytes crus; veio %q", nome, forma, m.ReasoningContent)
				}
				cru, err := port.ChatRequest{Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "x"}, m}}.MarshalWire(false)
				if err != nil || strings.Contains(string(cru), marca) || strings.Contains(string(cru), "reasoning") || strings.Contains(string(cru), "thinking") {
					t.Errorf("%s/%s: o pedido leva o raciocinio: %s (%v)", nome, forma, cru, err)
				}
			}
		}
	}
	// `reasoning_content` presente — mesmo vazio — vale sozinho, como antes.
	for msg, quer := range map[string]string{
		`{"content":"r","reasoning_content":"A","reasoning":"B","thinking":"C"}`:                         "A",
		`{"content":"r","reasoning_content":"","reasoning":"B"}`:                                         "",
		`{"content":"r","reasoning_content":null,"reasoning":"B","thinking":"C"}`:                        "B",
		`{"content":"r","reasoning":null,"reasoning_details":null,"thinking_blocks":[1],"thinking":"C"}`: "[1]",
		`{"content":"r","thinking":"C"}`:                                                                 "C",
		`{"content":"r"}`:                                                                                "",
	} {
		if m, err := aos509Mensagem(t, msg); err != nil || m.ReasoningContent != quer {
			t.Errorf("%s: raciocinio = %q (%v), quero %q", msg, m.ReasoningContent, err, quer)
		}
	}
}

// AS FORMAS QUE ESTE TICKET NÃO PASSA A LER ficam como estavam: `refusal`, `function_call` e as
// escolhas além da primeira não chegam a nenhum campo da mensagem.
func TestAOS509_FormasNaoLidasFicamComoEstavam(t *testing.T) {
	m, err := aos509Mensagem(t, `{"role":"assistant","content":null,"refusal":"nao posso","function_call":{"name":"arquivo","arguments":"{}"}}`)
	if err != nil || m.Content != "" || len(m.ToolCalls) != 0 || m.ReasoningContent != "" {
		t.Errorf("refusal e function_call nao sao lidos: %+v (%v)", m, err)
	}
	if port.Version != "1.6.0" && port.Version != "1.7.0" {
		t.Errorf("a descodificacao tolerante entrou na porta na 1.6.0; a versao e %s", port.Version)
	}
}
