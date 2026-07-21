package modelgateway_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/routing/failover"
)

// prodAuthn resolve o principal como o faria o estágio de authn de AOS-057 — o seam de
// identidade que torna as decisões de soberania atribuíveis. Em produção o composition
// root liga aqui o estágio real (identity.Verifier + policy); aqui é determinista.
type prodAuthn struct{}

func (prodAuthn) Name() string { return "auth-principal" }
func (prodAuthn) Process(_ context.Context, ex *pipeline.Exchange) error {
	ex.PrincipalUser = "alice"
	ex.PrincipalAgent = "agent-42"
	ex.AgentClass = "reader"
	ex.HumanRoot = "alice"
	ex.Principal = "alice/agent-42"
	ex.Record("auth-principal", "allow", "identidade de teste")
	return nil
}

// testCreds é um CredentialProvider determinista para os testes de produção: um mapa
// "provider|regiao" → segredo. Modela o seam que, em produção, o composition root liga
// ao vault/broker de EPIC-07.
type testCreds map[string]string

func (c testCreds) Fetch(_ context.Context, provider, region string) (string, error) {
	s, ok := c[provider+"|"+region]
	if !ok || s == "" {
		return "", errors.New("sem credencial de teste")
	}
	return s, nil
}

// okOpenAIServer devolve um httptest.Server que fala o wire OpenAI-compatible: responde
// 200 a /chat/completions com uma conclusão válida. Regista se foi invocado (para
// provar que um deny fail-closed NÃO o alcança).
func okOpenAIServer(t *testing.T, hit *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hit++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cmpl-1","object":"chat.completion","model":"gpt-4o",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func prodConfig(store audit.Store, baseURL string, client *http.Client, accounts []modelgateway.InfraAccount) modelgateway.ProductionConfig {
	return modelgateway.ProductionConfig{
		Provider:      "openai",
		BaseURL:       baseURL,
		HTTPClient:    client,
		DefaultRegion: "eu",
		Audit:         store,
		Credentials:   testCreds{"openai|eu": "sk-infra-eu", "openai|eu-west": "sk-infra-euw"},
		Accounts:      accounts,
		Authn:         prodAuthn{},
		Clock:         func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
}

// TestNewProduction_FailClosedConfig — a montagem recusa fail-closed sem audit,
// sem credenciais, com provider desconhecido ou sem baseURL.
func TestNewProduction_FailClosedConfig(t *testing.T) {
	store := audit.NewMemStore()
	base := modelgateway.ProductionConfig{Provider: "openai", BaseURL: "http://x", Audit: store, Credentials: testCreds{}, Authn: prodAuthn{}}

	noAudit := base
	noAudit.Audit = nil
	if _, err := modelgateway.NewProduction(context.Background(), noAudit); !errors.Is(err, modelgateway.ErrNoAuditStore) {
		t.Fatalf("sem audit devia ser fail-closed; got %v", err)
	}
	noCreds := base
	noCreds.Credentials = nil
	if _, err := modelgateway.NewProduction(context.Background(), noCreds); !errors.Is(err, modelgateway.ErrNoCredentialProvider) {
		t.Fatalf("sem credenciais devia ser fail-closed; got %v", err)
	}
	noAuthn := base
	noAuthn.Authn = nil
	if _, err := modelgateway.NewProduction(context.Background(), noAuthn); !errors.Is(err, modelgateway.ErrNoAuthnStage) {
		t.Fatalf("sem authn devia ser fail-closed; got %v", err)
	}
	unknown := base
	unknown.Provider = "acme"
	if _, err := modelgateway.NewProduction(context.Background(), unknown); !errors.Is(err, modelgateway.ErrUnknownProvider) {
		t.Fatalf("provider desconhecido devia falhar; got %v", err)
	}
	noURL := base
	noURL.BaseURL = ""
	if _, err := modelgateway.NewProduction(context.Background(), noURL); !errors.Is(err, modelgateway.ErrNoBaseURL) {
		t.Fatalf("sem baseURL devia falhar; got %v", err)
	}
}

// TestNewProduction_ActivationSealsChangelog — a activação sela a versão da allowlist
// no changelog WORM ANTES de servir tráfego (audit-before-effect).
func TestNewProduction_ActivationSealsChangelog(t *testing.T) {
	store := audit.NewMemStore()
	srv := okOpenAIServer(t, new(int))
	_, err := modelgateway.NewProduction(context.Background(), prodConfig(store, srv.URL, srv.Client(),
		[]modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu", LimitRPM: 100, LimitTPM: 100000}}))
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	head, _ := store.Head(context.Background(), "modelgw-gov:allowlist-changelog")
	if head != 1 {
		t.Fatalf("activação devia estar selada no changelog WORM; head=%d", head)
	}
}

// TestNewProduction_AllowPath_EndToEnd — um (board, modelo, região) permitido atravessa
// a pipeline REAL (allowlist + router de soberania) e alcança o provider; o allow é
// selado no WORM.
func TestNewProduction_AllowPath_EndToEnd(t *testing.T) {
	store := audit.NewMemStore()
	var hits int
	srv := okOpenAIServer(t, &hits)
	gw, err := modelgateway.NewProduction(context.Background(), prodConfig(store, srv.URL, srv.Client(),
		[]modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu", LimitRPM: 100, LimitTPM: 100000}}))
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}

	resp, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "gpt-4o", Principal: "tok", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "olá"}},
	})
	if err != nil {
		t.Fatalf("chat permitido devia passar; got %v", err)
	}
	if hits != 1 {
		t.Fatalf("provider devia ter sido invocado 1x; got %d", hits)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Fatalf("resposta inesperada: %+v", resp.Choices)
	}
	at, ok, _ := store.At(context.Background(), "modelgw-gov:board-eu", 1)
	if !ok || at.Decision != audit.DecisionAllow {
		t.Fatalf("allow devia estar selado; ok=%v dec=%v", ok, at.Decision)
	}
}

// TestNewProduction_AllowlistDeny_NeverHitsProvider — um modelo fora da allowlist do
// board é recusado fail-closed pelo estágio REAL; o provider nunca é alcançado.
func TestNewProduction_AllowlistDeny_NeverHitsProvider(t *testing.T) {
	store := audit.NewMemStore()
	var hits int
	srv := okOpenAIServer(t, &hits)
	gw, err := modelgateway.NewProduction(context.Background(), prodConfig(store, srv.URL, srv.Client(),
		[]modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu", LimitRPM: 100, LimitTPM: 100000}}))
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}

	_, err = gw.Chat(context.Background(), port.ChatRequest{
		Model: "claude-3", Principal: "tok", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "olá"}},
	})
	if !errors.Is(err, allowlist.ErrModelNotAllowed) {
		t.Fatalf("modelo fora da allowlist devia falhar fail-closed; got %v", err)
	}
	if hits != 0 {
		t.Fatalf("provider NAO devia ter sido invocado; got %d", hits)
	}
}

// TestNewProduction_CrossBorderFailover_BlockedInLiveDataPlane — o TESTE-CHAVE do
// wiring: com o primário indisponível e SÓ capacidade cross-border (us-east) para um
// board-eu, o router de soberania no data-plane VIVO bloqueia fail-closed — o provider
// nunca é alcançado e um deny atribuível é selado no WORM.
func TestNewProduction_CrossBorderFailover_BlockedInLiveDataPlane(t *testing.T) {
	store := audit.NewMemStore()
	var hits int
	srv := okOpenAIServer(t, &hits)
	cfg := prodConfig(store, srv.URL, srv.Client(),
		[]modelgateway.InfraAccount{{KeyID: "acct-us-1", Provider: "openai", Region: "us-east", LimitRPM: 100, LimitTPM: 100000}})
	// Só há credencial us-east disponível — mas o router bloqueia ANTES da credencial.
	cfg.Credentials = testCreds{"openai|us-east": "sk-infra-us"}
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}

	_, err = gw.Chat(context.Background(), port.ChatRequest{
		Model: "gpt-4o", Principal: "tok", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "olá"}},
	})
	if !errors.Is(err, failover.ErrCrossBorderBlocked) {
		t.Fatalf("failover cross-border devia bloquear no data-plane vivo; got %v", err)
	}
	if hits != 0 {
		t.Fatalf("provider cross-border NAO devia ter sido invocado; got %d", hits)
	}
	// Deny de failover cross-border selado, atribuível a board (partição).
	head, _ := store.Head(context.Background(), "modelgw-gov:board-eu")
	if head < 1 {
		t.Fatalf("deny cross-border devia estar selado no WORM; head=%d", head)
	}
}
