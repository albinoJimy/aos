package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// TestGatewayModelClient_EndToEnd prova que newGatewayModelClient compõe o Model Gateway REAL
// (NewProduction: allowlist assinada embebida + keypool + routing + authn + credential) e que o
// resultado, adaptado a agentruntime.ModelClient, chama de facto um endpoint OpenAI-compatível e
// devolve a conclusão. É o caminho nó→GW→provider inteiro, contra um httptest OpenAI-wire — sem
// duplicar o cliente OpenAI (que vive em internal/adapters do gateway).
func TestGatewayModelClient_EndToEnd(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Responde o wire OpenAI a qualquer /.../chat/completions.
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role, Content string
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cmpl-1","object":"chat.completion","model":"omni-1",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"resposta do gateway"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`))
	}))
	defer srv.Close()

	// Ambos os modelos da regra board-eu da allowlist ASSINADA embebida têm de atravessar e chegar
	// ao provider COM o nome correto. Um nome fora da allowlist seria negado fail-closed.
	for _, model := range []string{"gpt-4o", "gpt-4o-mini"} {
		gotModel = ""
		mc, err := newGatewayModelClient(srv.URL, model, "")
		if err != nil {
			t.Fatalf("newGatewayModelClient(%q): %v", model, err)
		}
		resp, err := mc.Call(context.Background(), agentruntime.PromptView{
			Turn:         1,
			Materialized: []byte("=== SYSTEM ===\nx\n=== CONTEXT ===\nolá"),
		})
		if err != nil {
			t.Fatalf("Call %q (nó->GW->provider) falhou: %v", model, err)
		}
		if resp.Text != "resposta do gateway" {
			t.Fatalf("[%s] texto = %q, esperado a conclusão do provider", model, resp.Text)
		}
		if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 7 {
			t.Fatalf("[%s] usage = %+v, esperado {11,7}", model, resp.Usage)
		}
		if gotModel != model {
			t.Fatalf("o gateway devia pedir o modelo %q, pediu %q", model, gotModel)
		}
	}
}

// TestParseModelFromEnv_Unset garante que sem AOS_MODEL_ENDPOINT o modelo fica nil (referenceModel,
// inalterado) e que um endpoint sem AOS_MODEL_NAME aborta fail-closed.
func TestParseModelFromEnv_Unset(t *testing.T) {
	t.Setenv("AOS_MODEL_ENDPOINT", "")
	t.Setenv("AOS_MODEL_NAME", "")
	mc, err := parseModelFromEnv()
	if err != nil || mc != nil {
		t.Fatalf("unset devia dar (nil,nil), veio (%v,%v)", mc, err)
	}
	t.Setenv("AOS_MODEL_ENDPOINT", "http://x/v1")
	t.Setenv("AOS_MODEL_NAME", "")
	if _, err := parseModelFromEnv(); err == nil {
		t.Fatalf("endpoint sem AOS_MODEL_NAME devia abortar (ErrBadModelConfig)")
	}
}
