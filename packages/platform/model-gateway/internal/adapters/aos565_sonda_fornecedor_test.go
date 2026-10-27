package adapters_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-565 — A SONDA DO FORNECEDOR A MONTANTE NO ADAPTADOR, opt-in como as outras duas.
//
// Prova as três posturas que o contrato distingue, e que colapsá-las faria um nó mentir:
//
//   - sonda DESLIGADA ⇒ campo VAZIO («não se mediu»), que é o adaptador de sempre;
//   - sonda LIGADA e o proxy declara ⇒ o nome;
//   - sonda LIGADA e o proxy cala ⇒ `nao_reportado` («mediu-se e não veio»), nunca vazio.

func aos565Servidor(t *testing.T, corpo string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, corpo)
	}))
	t.Cleanup(srv.Close)
	return srv
}

const aos565Resposta = `{"id":"x","object":"chat.completion","model":"gpt-x","created":1,"provider":"Google",
	"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"oi"}}],
	"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

const aos565RespostaSemFornecedor = `{"id":"x","object":"chat.completion","model":"gpt-x","created":1,
	"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"oi"}}],
	"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

func aos565Chat(t *testing.T, corpo string, ligar bool) port.ChatResponse {
	t.Helper()
	srv := aos565Servidor(t, corpo)
	a := adapters.NewOpenAIHTTPAdapter("openai", srv.URL, srv.Client())
	if ligar {
		a.ObserveUpstreamProvider()
	}
	resp, err := a.Chat(context.Background(), port.ChatRequest{
		Model: "gpt-x", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	}, adapters.NewCredential("openai", "eu", "sk-XYZ"))
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	return resp
}

func TestAOS565_SondaDoFornecedorAMontante(t *testing.T) {
	t.Parallel()
	if got := aos565Chat(t, aos565Resposta, false).UpstreamProvider; got != "" {
		t.Fatalf("sem a sonda ligada o campo e vazio — o adaptador de sempre; veio %q", got)
	}
	if got := aos565Chat(t, aos565Resposta, true).UpstreamProvider; got != "Google" {
		t.Fatalf("com a sonda ligada e o nome declarado: %q", got)
	}
	if got := aos565Chat(t, aos565RespostaSemFornecedor, true).UpstreamProvider; got != port.UpstreamProviderUnreported {
		t.Fatalf("o proxy nao declarou: quero %q, veio %q", port.UpstreamProviderUnreported, got)
	}
}
