package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
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
		// nil ⇒ allowlist EMBEBIDA; region/board casam com a regra board-eu embebida.
		mc, err := newGatewayModelClient(srv.URL, model, "", defaultModelGatewayRegion, defaultModelGatewayBoard, nil, nil)
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

// TestModelGateway_NoThroughputFuse prova que a ligação do nó ao Model Gateway NÃO tem o fusível
// de disponibilidade F1. O keypool é um SELECTOR de chave por throughput, não um rate-limiter: não
// tem janela temporal e o rpm só SOBE a cada Select. Declarar um LimitRPM finito na conta ÚNICA que
// o nó compõe transformava-o num FUSÍVEL permanente — à (limite+1).ª chamada da vida do processo,
// g.credential() falharia fail-closed (ErrNoCapacity) para SEMPRE até reiniciar. A ligação
// wired-in usa LimitRPM/LimitTPM = 0 (ilimitado): o tecto de throughput é do gateway EXTERNO
// (LiteLLM). Este teste faz MUITO mais do que o antigo fusível (120) chamadas pelo caminho
// nó→GW→provider e exige que TODAS passem; reintroduzir um limite sem janela avermelha-o na
// chamada (limite+1).
func TestModelGateway_NoThroughputFuse(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer srv.Close()

	// nil ⇒ allowlist EMBEBIDA; region/board casam com a regra board-eu. É o MESMO caminho de
	// composição que o nó usa em produção — a conta de infra (LimitRPM/TPM=0) vem de dentro de
	// newGatewayModelClient, não do teste, pelo que um regresso do fusível é apanhado aqui.
	mc, err := newGatewayModelClient(srv.URL, "gpt-4o-mini", "", defaultModelGatewayRegion, defaultModelGatewayBoard, nil, nil)
	if err != nil {
		t.Fatalf("newGatewayModelClient: %v", err)
	}

	// oldFuseRPM era o limite hardcoded que criava o brownout permanente (F1); passamos bem além
	// dele. Se o fusível voltar, a chamada oldFuseRPM+1 falha.
	const oldFuseRPM = 120
	const total = oldFuseRPM + 10
	for i := 1; i <= total; i++ {
		if _, err := mc.Call(context.Background(), agentruntime.PromptView{Turn: i, Materialized: []byte("olá")}); err != nil {
			t.Fatalf("chamada #%d falhou (%v) — o fusível de throughput F1 voltou: o keypool não tem janela, logo um LimitRPM finito na conta única brownout-a o nó para sempre. Mantenha LimitRPM/LimitTPM=0 (o tecto vive no gateway externo).", i, err)
		}
	}
	if calls != total {
		t.Fatalf("o upstream devia ter recebido %d chamadas (nenhuma cortada por saturação), recebeu %d", total, calls)
	}
}

// TestParseModelFromEnv_Unset garante que sem AOS_MODEL_ENDPOINT o modelo fica nil (referenceModel,
// inalterado) e que um endpoint sem AOS_MODEL_NAME aborta fail-closed.
func TestParseModelFromEnv_Unset(t *testing.T) {
	t.Setenv("AOS_MODEL_ENDPOINT", "")
	t.Setenv("AOS_MODEL_NAME", "")
	mc, err := parseModelFromEnv(false)
	if err != nil || mc != nil {
		t.Fatalf("unset devia dar (nil,nil), veio (%v,%v)", mc, err)
	}
	t.Setenv("AOS_MODEL_ENDPOINT", "http://x/v1")
	t.Setenv("AOS_MODEL_NAME", "")
	if _, err := parseModelFromEnv(false); err == nil {
		t.Fatalf("endpoint sem AOS_MODEL_NAME devia abortar (ErrBadModelConfig)")
	}
}

// TestExternalAllowlist_AllowsNonEmbeddedModel prova a externalização (padrão PDP): uma allowlist
// ASSINADA MONTADA (bundle) permite um modelo/board que a policy EMBEBIDA NÃO tem — removendo o
// acoplamento "modelos fixos no código". Assina uma policy com um modelo Kimi real e verifica que o
// nó (loadModelAllowlistFromEnv + newGatewayModelClient) o aceita e o envia ao upstream.
func TestExternalAllowlist_AllowsNonEmbeddedModel(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	pub := signAllowlistBundle(t, dir, "board-kimi", "kimi-for-coding", "eu")
	t.Setenv("AOS_MODEL_ALLOWLIST_BUNDLE_DIR", dir)
	t.Setenv("AOS_MODEL_ALLOWLIST_TRUST_ANCHOR", pub)

	pol, err := loadModelAllowlistFromEnv()
	if err != nil || pol == nil {
		t.Fatalf("loadModelAllowlistFromEnv: pol=%v err=%v", pol, err)
	}
	// kimi-for-coding/board-kimi NÃO estão na allowlist embebida — só passam pela externa.
	mc, err := newGatewayModelClient(srv.URL, "kimi-for-coding", "", "eu", "board-kimi", pol, nil)
	if err != nil {
		t.Fatalf("newGatewayModelClient: %v", err)
	}
	if _, err := mc.Call(context.Background(), agentruntime.PromptView{Turn: 1, Materialized: []byte("olá")}); err != nil {
		t.Fatalf("Call com allowlist externa (modelo não-embebido) falhou: %v", err)
	}
	if gotModel != "kimi-for-coding" {
		t.Fatalf("upstream devia receber kimi-for-coding, recebeu %q", gotModel)
	}
}

// TestExternalAllowlist_FailClosed — dir sem anchor aborta; anchor de outra chave (bundle não
// verifica) aborta. Fail-closed em ambos.
func TestExternalAllowlist_FailClosed(t *testing.T) {
	dir := t.TempDir()
	_ = signAllowlistBundle(t, dir, "board-kimi", "kimi-for-coding", "eu")

	t.Setenv("AOS_MODEL_ALLOWLIST_BUNDLE_DIR", dir)
	t.Setenv("AOS_MODEL_ALLOWLIST_TRUST_ANCHOR", "")
	if _, err := loadModelAllowlistFromEnv(); err == nil {
		t.Fatalf("dir sem anchor devia abortar (ErrBadModelAllowlist)")
	}

	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv("AOS_MODEL_ALLOWLIST_TRUST_ANCHOR", base64.StdEncoding.EncodeToString(otherPub))
	if _, err := loadModelAllowlistFromEnv(); err == nil {
		t.Fatalf("anchor de outra chave devia abortar (assinatura não verifica)")
	}
}

// signAllowlistBundle escreve allowlist_policy.json + .sig num dir, assinados por uma chave NOVA, e
// devolve a pubkey base64 (o trust anchor out-of-band). Espelha o gen_signature.go offline.
func signAllowlistBundle(t *testing.T, dir, board, model, region string) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policy := map[string]any{
		"version": "gw-allowlist/v1",
		"default": "deny",
		"rules": []map[string]any{{
			"id": "ext-" + board, "board": board, "models": []string{model}, "regions": []string{region},
		}},
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := allowlist.Digest(policyJSON)
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	sig := ed25519.Sign(priv, []byte(digest))
	if err := os.WriteFile(filepath.Join(dir, allowlist.BundlePolicyFile), policyJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, allowlist.BundleSigFile), []byte(base64.StdEncoding.EncodeToString(sig)), 0o644); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(pub)
}
