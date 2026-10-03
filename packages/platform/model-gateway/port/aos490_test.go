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
		// A via de TOPO tem a mesma regra da do wire: sem sinal negativo e com tecto no prompt.
		{"topo negativo nao e leitura", `{"prompt_tokens":100,"cache_read_tokens":-7}`, 0},
		{"topo negativo cede ao cached_tokens do wire", `{"prompt_tokens":100,"cache_read_tokens":-7,"prompt_tokens_details":{"cached_tokens":64}}`, 64},
		{"topo acima do prompt fica no prompt", `{"prompt_tokens":100,"cache_read_tokens":1000000000000}`, 100},
		{"topo sem prompt nao inventa cache", `{"cache_read_tokens":50}`, 0},
		{"topo com prompt negativo", `{"prompt_tokens":-3,"cache_read_tokens":50}`, 0},
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

// A regra da cache vale também nas outras duas vias por onde um `usage` do provider entra: a
// resposta de embeddings e o chunk final de um stream.
func TestAOS490_TokensEmCache_EmbeddingsEStream(t *testing.T) {
	t.Parallel()
	emb, err := port.UnmarshalEmbeddingsResponse([]byte(`{"data":[],"usage":{"prompt_tokens":10,"cache_read_tokens":-7}}`))
	if err != nil || emb.Usage.CacheReadTokens != 0 {
		t.Fatalf("embeddings: %+v (%v)", emb.Usage, err)
	}
	for _, c := range []struct{ in, quer int64 }{{-7, 0}, {1 << 40, 100}, {64, 64}} {
		resp, err := port.CollectStream(port.NewSliceStream([]port.ChatStreamDelta{
			{Content: "ok", FinishReason: "stop", Usage: &port.Usage{PromptTokens: 100, CompletionTokens: 1, CacheReadTokens: c.in}},
		}))
		if err != nil || resp.Usage.CacheReadTokens != c.quer {
			t.Fatalf("stream com cache_read_tokens=%d: veio %d, quero %d (%v)", c.in, resp.Usage.CacheReadTokens, c.quer, err)
		}
	}
}

// `reasoning_content` É CARGA OPACA: qualquer valor JSON é aceite, e nenhum derruba a resposta.
// Uma string guarda-se descodificada; outra forma guarda-se como os bytes JSON que vieram; null
// ou ausente é vazio. O resto da mensagem e o usage lêem-se como sempre.
func TestAOS490_Raciocinio_QualquerFormaJSON(t *testing.T) {
	t.Parallel()
	casos := []struct {
		nome, valor, quer string
	}{
		{"string", `"penso\nlogo"`, "penso\nlogo"},
		{"string vazia", `""`, ""},
		{"null", `null`, ""},
		{"objecto", `{"x":1}`, `{"x":1}`},
		{"lista de strings", `["a","b"]`, `["a","b"]`},
		{"numero", `42`, `42`},
		{"booleano", `true`, `true`},
		{"lista de blocos", `[{"type":"thinking","thinking":"hmm","signature":"abc"}]`, `[{"type":"thinking","thinking":"hmm","signature":"abc"}]`},
		{"objecto com espaco fica como veio", `{ "x" : 1 }`, `{ "x" : 1 }`},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			corpo := `{"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"texto","reasoning_content":` + c.valor +
				`,"tool_calls":[{"id":"c1","type":"function","function":{"name":"t","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`
			resp, err := port.UnmarshalChatResponse([]byte(corpo))
			if err != nil {
				t.Fatalf("a resposta foi recusada por causa do reasoning_content: %v", err)
			}
			msg := resp.Choices[0].Message
			if msg.ReasoningContent != c.quer {
				t.Fatalf("ReasoningContent = %q, quero %q", msg.ReasoningContent, c.quer)
			}
			if msg.Role != port.RoleAssistant || msg.Content != "texto" || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "c1" || resp.Usage.PromptTokens != 7 {
				t.Fatalf("o resto da resposta nao foi lido como sempre: %+v / %+v", msg, resp.Usage)
			}
		})
	}
	// Ausente: vazio, e uma mensagem reutilizada não herda o raciocínio da leitura anterior.
	var m port.Message
	if err := m.UnmarshalJSON([]byte(`{"role":"assistant","content":"a","reasoning_content":"r"}`)); err != nil || m.ReasoningContent != "r" {
		t.Fatalf("leitura directa: %+v (%v)", m, err)
	}
	if err := m.UnmarshalJSON([]byte(`{"role":"assistant","content":"b"}`)); err != nil || m.ReasoningContent != "" || m.Content != "b" {
		t.Fatalf("mensagem sem o campo: %+v (%v)", m, err)
	}
	// JSON malformado continua a ser erro — a tolerância é para a FORMA do campo, não para o corpo.
	if _, err := port.UnmarshalChatResponse([]byte(`{"choices":[{"message":{"role":"assistant","reasoning_content":{]}}]}`)); err == nil {
		t.Fatal("um corpo que nao e JSON tinha de continuar a ser recusado")
	}
}
