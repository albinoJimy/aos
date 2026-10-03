package port_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-490 — o contrato transporta o raciocínio do modelo (só nas respostas) e lê os tokens em
// cache do sítio onde o wire OpenAI os reporta.

// aos490RespostaLiteLLM é a forma da resposta do LiteLLM de produção a um pedido com tools (a do
// pedido 1 da «Medição de 2026-10-03» do ticket AOS-490: `content` vazio, `tool_calls` com id do
// provider, `reasoning_content`, `provider_specific_fields` e `completion_tokens_details`), com
// o `prompt_tokens_details` que um provider com cache de prefixo acrescenta ao `usage`.
const aos490RespostaLiteLLM = `{"id":"chatcmpl-9f1","created":1791000000,"model":"kimi-for-coding","object":"chat.completion",` +
	`"choices":[{"finish_reason":"tool_calls","index":0,"message":{"content":"","role":"assistant",` +
	`"tool_calls":[{"function":{"arguments":"{\"doc_id\":\"notes\"}","name":"doc_read"},"id":"tool_Zx81","type":"function"}],` +
	`"function_call":null,"reasoning_content":"The user wants the notes document.\nI should call doc_read.",` +
	`"provider_specific_fields":{"reasoning_content":"The user wants the notes document.\nI should call doc_read."}}}],` +
	`"usage":{"completion_tokens":61,"prompt_tokens":1380,"total_tokens":1441,` +
	`"completion_tokens_details":{"reasoning_tokens":33},"prompt_tokens_details":{"cached_tokens":1024}}}`

func TestAOS490_Resposta_RaciocinioETokensEmCacheDoWire(t *testing.T) {
	t.Parallel()
	resp, err := port.UnmarshalChatResponse([]byte(aos490RespostaLiteLLM))
	if err != nil {
		t.Fatalf("UnmarshalChatResponse: %v", err)
	}
	msg := resp.Choices[0].Message
	if quer := "The user wants the notes document.\nI should call doc_read."; msg.ReasoningContent != quer {
		t.Fatalf("reasoning_content: veio %q, quero %q", msg.ReasoningContent, quer)
	}
	if msg.Content != "" || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "doc_read" {
		t.Fatalf("a mensagem nao foi lida como antes: %+v", msg)
	}
	quer := port.Usage{PromptTokens: 1380, CompletionTokens: 61, TotalTokens: 1441, CacheReadTokens: 1024}
	if !reflect.DeepEqual(resp.Usage, quer) {
		t.Fatalf("usage: veio %+v, quero %+v", resp.Usage, quer)
	}
}

func TestAOS490_TokensEmCache_Regra(t *testing.T) {
	t.Parallel()
	casos := []struct {
		nome  string
		usage string
		quer  int64
	}{
		{"sem prompt_tokens_details", `{"prompt_tokens":100,"completion_tokens":5}`, 0},
		{"details sem cached_tokens", `{"prompt_tokens":100,"prompt_tokens_details":{"audio_tokens":0}}`, 0},
		{"details null", `{"prompt_tokens":100,"prompt_tokens_details":null}`, 0},
		{"cached_tokens do wire", `{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":64}}`, 64},
		{"cache_read_tokens de topo prevalece", `{"prompt_tokens":100,"cache_read_tokens":10,"prompt_tokens_details":{"cached_tokens":64}}`, 10},
		{"negativo nao e leitura", `{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":-5}}`, 0},
		{"mais cache do que prompt fica no prompt", `{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":500}}`, 100},
		{"usage sem prompt nao inventa cache", `{"prompt_tokens_details":{"cached_tokens":64}}`, 0},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			resp, err := port.UnmarshalChatResponse([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":` + c.usage + `}`))
			if err != nil {
				t.Fatalf("UnmarshalChatResponse: %v", err)
			}
			if resp.Usage.CacheReadTokens != c.quer {
				t.Fatalf("CacheReadTokens = %d, quero %d", resp.Usage.CacheReadTokens, c.quer)
			}
		})
	}
	// Sem objecto usage a ausência continua marcada, e não há cache.
	resp, err := port.UnmarshalChatResponse([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	if err != nil || !resp.Usage.Ausente || resp.Usage.CacheReadTokens != 0 {
		t.Fatalf("usage ausente: %+v (%v)", resp.Usage, err)
	}
}

// O RACIOCÍNIO NÃO SAI NUM PEDIDO, qualquer que seja o chamador: mesmo uma mensagem assistant
// reencaminhada tal como veio numa resposta vai para o wire sem ele, e a do chamador fica intacta.
func TestAOS490_MarshalWire_NaoEnviaRaciocinio(t *testing.T) {
	t.Parallel()
	msgs := []port.Message{
		{Role: port.RoleUser, Content: "oi"},
		{Role: port.RoleAssistant, Content: "", ReasoningContent: "RACIOCINIO-QUE-NAO-PODE-SAIR", ToolCalls: []port.ToolCall{{ID: "step-000001-tool-1", Type: "function", Function: port.FunctionCall{Name: "t", Arguments: "{}"}}}},
		{Role: port.RoleTool, ToolCallID: "step-000001-tool-1", Content: "r"},
	}
	req := port.ChatRequest{Model: "m", Messages: msgs}
	for _, stream := range []bool{false, true} {
		wire, err := req.MarshalWire(stream)
		if err != nil {
			t.Fatalf("MarshalWire: %v", err)
		}
		if strings.Contains(string(wire), "RACIOCINIO") || strings.Contains(string(wire), "reasoning_content") {
			t.Fatalf("o pedido leva o raciocinio (stream=%v): %s", stream, wire)
		}
	}
	if msgs[1].ReasoningContent != "RACIOCINIO-QUE-NAO-PODE-SAIR" {
		t.Fatal("MarshalWire alterou as mensagens do chamador")
	}
	// Um pedido sem raciocínio é serializado como sempre.
	wire, _ := port.ChatRequest{Model: "m", Messages: msgs[:1]}.MarshalWire(false)
	if string(wire) != `{"model":"m","messages":[{"role":"user","content":"oi"}]}` {
		t.Fatalf("wire de um pedido simples mudou: %s", wire)
	}
}
