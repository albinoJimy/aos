package adapters_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/port"
)

// TestStaticCredentialSource_FailClosed confirma que a fonte de credenciais é
// fail-closed (sem credencial ⇒ erro atribuível, nunca cai para outra).
func TestStaticCredentialSource_FailClosed(t *testing.T) {
	t.Parallel()
	cs := adapters.NewStaticCredentialSource()
	cs.Set("openai", "eu", "sk-secreto")
	if _, err := cs.Fetch(context.Background(), "openai", "us"); err != adapters.ErrNoCredential {
		t.Errorf("regiao sem credencial devia falhar ErrNoCredential, obtido %v", err)
	}
	cred, err := cs.Fetch(context.Background(), "openai", "eu")
	if err != nil {
		t.Fatalf("Fetch valido: %v", err)
	}
	// O segredo NUNCA aparece em String() (ADR-006).
	if strings.Contains(cred.String(), "sk-secreto") {
		t.Errorf("segredo vazou em String(): %s", cred.String())
	}
	if !strings.Contains(cred.String(), "REDACTED") {
		t.Errorf("String() nao redige o segredo: %s", cred.String())
	}
}

// TestFakeAdapter_Chat cobre o adaptador fake determinista no caminho síncrono.
func TestFakeAdapter_Chat(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "resposta"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5},
	})
	cred := adapters.NewCredential("fake", "eu", "s")
	resp, err := f.Chat(context.Background(), port.ChatRequest{Model: "m"}, cred)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Model != "m" || resp.Choices[0].Message.Content != "resposta" {
		t.Errorf("resposta fake errada: %+v", resp)
	}
	if f.Calls() != 1 || f.LastCredentialProvider() != "fake" {
		t.Errorf("credencial nao injectada server-side: calls=%d prov=%q", f.Calls(), f.LastCredentialProvider())
	}
}

// TestFakeAdapter_StreamAndToolCalling prova streaming e tool calling correctos:
// fragmenta em deltas e reconstrói via CollectStream idêntico ao síncrono.
func TestFakeAdapter_StreamAndToolCalling(t *testing.T) {
	t.Parallel()
	f := adapters.NewFakeAdapter("fake")
	f.SetChatResponse("m", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{
			Role:    port.RoleAssistant,
			Content: "vou chamar",
			ToolCalls: []port.ToolCall{{
				ID: "call_1", Type: "function",
				Function: port.FunctionCall{Name: "get_weather", Arguments: `{"city":"lx"}`},
			}},
		}, FinishReason: "tool_calls"}},
		Usage: port.Usage{PromptTokens: 4, CompletionTokens: 6, TotalTokens: 10},
	})
	stream, err := f.ChatStream(context.Background(), port.ChatRequest{Model: "m"}, adapters.NewCredential("fake", "eu", "s"))
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	resp, err := port.CollectStream(stream)
	if err != nil {
		t.Fatalf("CollectStream: %v", err)
	}
	msg := resp.Choices[0].Message
	if msg.Content != "vou chamar" {
		t.Errorf("texto streamed = %q", msg.Content)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("tool call streamed errada: %+v", msg.ToolCalls)
	}
	if msg.ToolCalls[0].Function.Arguments != `{"city":"lx"}` {
		t.Errorf("argumentos reconstruidos = %q", msg.ToolCalls[0].Function.Arguments)
	}
	if resp.Usage.TotalTokens != 10 {
		t.Errorf("usage final do stream = %+v", resp.Usage)
	}
}

// TestOpenAIHTTPAdapter_Chat testa o adaptador REAL contra um httptest.Server
// que fala o wire OpenAI. Verifica que o pedido tem o header Authorization com a
// credencial injectada server-side e que a resposta é normalizada.
func TestOpenAIHTTPAdapter_Chat(t *testing.T) {
	t.Parallel()
	var gotAuth, gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","object":"chat.completion","model":"gpt-x","created":1,
			"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"olá"}}],
			"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`)
	}))
	defer srv.Close()

	a := adapters.NewOpenAIHTTPAdapter("openai", srv.URL+"/v1", srv.Client())
	cred := adapters.NewCredential("openai", "eu", "sk-XYZ")
	resp, err := a.Chat(context.Background(), port.ChatRequest{
		Model:    "gpt-x",
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	}, cred)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-XYZ" {
		t.Errorf("Authorization = %q (credencial nao injectada server-side)", gotAuth)
	}
	if _, leaked := gotBody["principal"]; leaked {
		t.Errorf("metadado de plataforma vazou no wire: %v", gotBody)
	}
	if resp.Model != "gpt-x" || resp.Choices[0].Message.Content != "olá" || resp.Usage.TotalTokens != 9 {
		t.Errorf("resposta normalizada errada: %+v", resp)
	}
}

// TestOpenAIHTTPAdapter_Stream testa o adaptador REAL em streaming (SSE) contra
// httptest, incluindo tool call fragmentada e chunk final com usage.
func TestOpenAIHTTPAdapter_Stream(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"delta":{"role":"assistant","content":"Olá"}}]}`,
			`{"choices":[{"delta":{"content":" mundo"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"f","arguments":"{\"x\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
		}
		for _, c := range chunks {
			_, _ = io.WriteString(w, "data: "+c+"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	a := adapters.NewOpenAIHTTPAdapter("openai", srv.URL, srv.Client())
	stream, err := a.ChatStream(context.Background(), port.ChatRequest{
		Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	}, adapters.NewCredential("openai", "eu", "sk"))
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	resp, err := port.CollectStream(stream)
	if err != nil {
		t.Fatalf("CollectStream: %v", err)
	}
	msg := resp.Choices[0].Message
	if msg.Content != "Olá mundo" {
		t.Errorf("texto SSE agregado = %q", msg.Content)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Arguments != `{"x":1}` {
		t.Errorf("tool call SSE reconstruida errada: %+v", msg.ToolCalls)
	}
	if resp.Usage.TotalTokens != 3 {
		t.Errorf("usage SSE = %+v", resp.Usage)
	}
}

// TestOpenAIHTTPAdapter_Embeddings testa embeddings do adaptador real.
func TestOpenAIHTTPAdapter_Embeddings(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","model":"emb","data":[{"index":0,"object":"embedding","embedding":[0.1,0.2]}],
			"usage":{"prompt_tokens":3,"completion_tokens":0,"total_tokens":3}}`)
	}))
	defer srv.Close()
	a := adapters.NewOpenAIHTTPAdapter("openai", srv.URL, srv.Client())
	resp, err := a.Embeddings(context.Background(), port.EmbeddingsRequest{Model: "emb", Input: []string{"oi"}},
		adapters.NewCredential("openai", "eu", "sk"))
	if err != nil {
		t.Fatalf("Embeddings: %v", err)
	}
	if len(resp.Data) != 1 || len(resp.Data[0].Embedding) != 2 {
		t.Errorf("embeddings errados: %+v", resp)
	}
}

// TestOpenAIHTTPAdapter_ErrorStatus confirma erro fail-closed em status != 200.
func TestOpenAIHTTPAdapter_ErrorStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":"rate limited"}`)
	}))
	defer srv.Close()
	a := adapters.NewOpenAIHTTPAdapter("openai", srv.URL, srv.Client())
	_, err := a.Chat(context.Background(), port.ChatRequest{Model: "m", Messages: []port.Message{{Role: port.RoleUser, Content: "x"}}},
		adapters.NewCredential("openai", "eu", "sk"))
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("erro de status = %v, quer conter 429", err)
	}
}
