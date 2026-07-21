package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aos-ref/integration"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/routing/failover"
)

// Estes testes e2e provam que o COMPOSITION ROOT monta um Model Gateway em que a
// soberania de AOS-058 é IMPOSTA no data-plane vivo — não um primitivo à parte. Cada
// teste atravessa o gateway REAL montado por AssembleModelGateway (allowlist + router
// de soberania reais), com o adaptador HTTP OpenAI-compatible apontado a um httptest.

// authnStub resolve o principal como o estágio de identidade de AOS-057 — o seam que
// torna as decisões de soberania atribuíveis. O composition root real liga aqui o
// estágio de authn concreto; aqui é determinista.
type authnStub struct{}

func (authnStub) Name() string { return "auth-principal" }
func (authnStub) Process(_ context.Context, ex *pipeline.Exchange) error {
	ex.PrincipalUser = "alice"
	ex.PrincipalAgent = "agent-42"
	ex.HumanRoot = "alice"
	ex.Principal = "alice/agent-42"
	ex.Record("auth-principal", "allow", "identidade e2e")
	return nil
}

// creds é uma fonte de credenciais determinista (o seam do vault/broker de EPIC-07).
type creds map[string]string

func (c creds) Fetch(_ context.Context, provider, region string) (string, error) {
	if s, ok := c[provider+"|"+region]; ok && s != "" {
		return s, nil
	}
	return "", errors.New("sem credencial e2e")
}

func openAIStub(t *testing.T, hit *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*hit++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cmpl-1","object":"chat.completion","model":"gpt-4o",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func baseConfig(store audit.Store, srv *httptest.Server, accounts []modelgateway.InfraAccount, cr creds) integration.ModelGatewayConfig {
	return integration.ModelGatewayConfig{
		Provider:      "openai",
		BaseURL:       srv.URL,
		HTTPClient:    srv.Client(),
		DefaultRegion: "eu",
		Audit:         store,
		Credentials:   cr,
		Authn:         authnStub{},
		Accounts:      accounts,
		Clock:         func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
}

// TestApex_FailClosedWithoutAudit — o ápice recusa montar sem audit.Store.
func TestApex_FailClosedWithoutAudit(t *testing.T) {
	cfg := integration.ModelGatewayConfig{Provider: "openai", BaseURL: "http://x", Authn: authnStub{}, Credentials: creds{}}
	if _, err := integration.AssembleModelGateway(context.Background(), cfg); !errors.Is(err, integration.ErrNoAuditStore) {
		t.Fatalf("sem audit o ápice devia recusar; got %v", err)
	}
}

// TestApex_AllowlistEnforced_AllowPath — um (board, modelo, região) permitido atravessa
// o gateway montado e alcança o provider; o allow é selado no WORM (governação por
// chamada) e a activação da allowlist foi selada no changelog.
func TestApex_AllowlistEnforced_AllowPath(t *testing.T) {
	store := audit.NewMemStore()
	var hits int
	srv := openAIStub(t, &hits)
	gw, err := integration.AssembleModelGateway(context.Background(), baseConfig(store, srv,
		[]modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu", LimitRPM: 100, LimitTPM: 100000}},
		creds{"openai|eu": "sk-eu"}))
	if err != nil {
		t.Fatalf("AssembleModelGateway: %v", err)
	}

	if _, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "gpt-4o", Principal: "tok", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "olá"}},
	}); err != nil {
		t.Fatalf("chat permitido devia passar; got %v", err)
	}
	if hits != 1 {
		t.Fatalf("provider devia ser invocado 1x; got %d", hits)
	}
	if head, _ := store.Head(context.Background(), "modelgw-gov:allowlist-changelog"); head != 1 {
		t.Fatalf("activação da allowlist devia estar selada; head=%d", head)
	}
	if at, ok, _ := store.At(context.Background(), "modelgw-gov:board-eu", 1); !ok || at.Decision != audit.DecisionAllow {
		t.Fatalf("allow por chamada devia estar selado; ok=%v dec=%v", ok, at.Decision)
	}
}

// TestApex_AllowlistEnforced_DenyNeverReachesProvider — um modelo fora da allowlist do
// board é recusado fail-closed no data-plane vivo; o provider nunca é alcançado.
func TestApex_AllowlistEnforced_DenyNeverReachesProvider(t *testing.T) {
	store := audit.NewMemStore()
	var hits int
	srv := openAIStub(t, &hits)
	gw, err := integration.AssembleModelGateway(context.Background(), baseConfig(store, srv,
		[]modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu", LimitRPM: 100, LimitTPM: 100000}},
		creds{"openai|eu": "sk-eu"}))
	if err != nil {
		t.Fatalf("AssembleModelGateway: %v", err)
	}

	_, err = gw.Chat(context.Background(), port.ChatRequest{
		Model: "claude-3", Principal: "tok", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "olá"}},
	})
	if !errors.Is(err, allowlist.ErrModelNotAllowed) {
		t.Fatalf("modelo fora da allowlist devia falhar fail-closed; got %v", err)
	}
	if hits != 0 {
		t.Fatalf("provider NAO devia ser alcançado; got %d", hits)
	}
}

// TestApex_CrossBorderFailover_BlockedInLiveDataPlane — o TESTE-CHAVE do ticket: com o
// primário indisponível e SÓ capacidade cross-border (us-east) para um board-eu, o
// router de soberania montado no ápice bloqueia fail-closed — o provider cross-border
// nunca é alcançado e um deny atribuível a board é selado no WORM.
func TestApex_CrossBorderFailover_BlockedInLiveDataPlane(t *testing.T) {
	store := audit.NewMemStore()
	var hits int
	srv := openAIStub(t, &hits)
	gw, err := integration.AssembleModelGateway(context.Background(), baseConfig(store, srv,
		[]modelgateway.InfraAccount{{KeyID: "acct-us-1", Provider: "openai", Region: "us-east", LimitRPM: 100, LimitTPM: 100000}},
		creds{"openai|us-east": "sk-us"}))
	if err != nil {
		t.Fatalf("AssembleModelGateway: %v", err)
	}

	_, err = gw.Chat(context.Background(), port.ChatRequest{
		Model: "gpt-4o", Principal: "tok", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "olá"}},
	})
	if !errors.Is(err, failover.ErrCrossBorderBlocked) {
		t.Fatalf("failover cross-border devia bloquear no data-plane vivo; got %v", err)
	}
	if hits != 0 {
		t.Fatalf("provider cross-border NAO devia ser alcançado; got %d", hits)
	}
	if head, _ := store.Head(context.Background(), "modelgw-gov:board-eu"); head < 1 {
		t.Fatalf("deny cross-border devia estar selado no WORM; head=%d", head)
	}
}

// TestApex_FailoverIntraBorder_StaysInJurisdiction — com o primário (eu) em baixo mas
// capacidade intra-fronteira (eu-west, allowlisted), o failover permanece na
// jurisdição: resolve para eu-west e alcança o provider (a disponibilidade não é
// comprada à custa da soberania, mas também não é negada quando há capacidade legal).
func TestApex_FailoverIntraBorder_StaysInJurisdiction(t *testing.T) {
	store := audit.NewMemStore()
	var hits int
	srv := openAIStub(t, &hits)
	// Só capacidade em eu-west (intra-fronteira do board-eu); primário eu ausente.
	cfg := baseConfig(store, srv,
		[]modelgateway.InfraAccount{{KeyID: "acct-euw-1", Provider: "openai", Region: "eu-west", LimitRPM: 100, LimitTPM: 100000}},
		creds{"openai|eu-west": "sk-euw"})
	gw, err := integration.AssembleModelGateway(context.Background(), cfg)
	if err != nil {
		t.Fatalf("AssembleModelGateway: %v", err)
	}

	if _, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "gpt-4o", Principal: "tok", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "olá"}},
	}); err != nil {
		t.Fatalf("failover intra-fronteira devia passar; got %v", err)
	}
	if hits != 1 {
		t.Fatalf("provider intra-fronteira devia ser alcançado 1x; got %d", hits)
	}
}
