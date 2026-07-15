package modelgateway_test

import (
	"context"
	"errors"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
)

// fakeAuth popula a identidade resolvida (como o estágio authn de AOS-057) para que
// a atribuição da decisão de allowlist tenha principal — sem montar o Issuer/Verifier
// completo (esse caminho é testado em gateway_aos057_test.go). É o eixo da atribuição
// que a allowlist regional sela por chamada.
type fakeAuth struct{}

func (fakeAuth) Name() string { return "auth-principal" }
func (fakeAuth) Process(_ context.Context, ex *pipeline.Exchange) error {
	ex.PrincipalUser = "alice"
	ex.PrincipalAgent = "agent-42"
	ex.AgentClass = "reader"
	ex.HumanRoot = "alice"
	ex.EffectiveAuthority = []string{"model:invoke"}
	ex.Principal = "alice/agent-42"
	ex.Record("auth-principal", "allow", "fake identity (teste)")
	return nil
}

// newAOS058Gateway compõe um GW com o estágio allowlist-regional REAL (AOS-058) sobre
// a allowlist embebida assinada, mais o recorder de governação (span + WORM).
func newAOS058Gateway(t *testing.T, store audit.Store) (*modelgateway.Gateway, *adapters.FakeAdapter, *agentruntime.RecordingTracer) {
	t.Helper()
	pol, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	rec := allowlist.NewRecorder(store)
	stage := allowlist.NewStage(pol, allowlist.WithRecorder(rec))

	adpt := adapters.NewFakeAdapter("openai")
	adpt.SetChatResponse("gpt-4o", port.ChatResponse{Usage: port.Usage{PromptTokens: 10, CompletionTokens: 5}})
	adpt.SetEmbeddingsResponse("text-embedding-3-large", port.EmbeddingsResponse{Usage: port.Usage{PromptTokens: 7}})

	cs := adapters.NewStaticCredentialSource()
	cs.Set("openai", "eu", "sk-infra-secreto")

	tr := &agentruntime.RecordingTracer{}
	gw := modelgateway.New(adpt,
		modelgateway.WithAuthnStage(fakeAuth{}),
		modelgateway.WithAllowlistStage(stage),
		modelgateway.WithCredentialSource(cs),
		modelgateway.WithTracer(tr),
		modelgateway.WithDefaultRegion("eu"),
		modelgateway.WithClock(func() time.Time { return time.Unix(1_700_000_000, 0) }),
	)
	return gw, adpt, tr
}

// TestGateway_AllowlistAllow_EndToEnd — um (board, modelo, região) permitido atravessa
// a pipeline e invoca o provider; a decisão de allow é anotada no span e selada no WORM.
func TestGateway_AllowlistAllow_EndToEnd(t *testing.T) {
	store := audit.NewMemStore()
	gw, adpt, tr := newAOS058Gateway(t, store)

	_, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "gpt-4o", Principal: "tok", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "olá"}},
	})
	if err != nil {
		t.Fatalf("chat permitido devia passar; got %v", err)
	}
	if adpt.Calls() != 1 {
		t.Fatalf("provider devia ter sido invocado 1x; got %d", adpt.Calls())
	}
	// Decisão de allow selada no WORM (registo por chamada) atribuível a board+principal.
	at, ok, _ := store.At(context.Background(), "modelgw-gov:board-eu", 1)
	if !ok || at.Decision != audit.DecisionAllow {
		t.Fatalf("allow devia estar selado; ok=%v dec=%v", ok, at.Decision)
	}
	// Span anotado com o resultado da allowlist.
	spans := tr.SpansByOperation(agentruntime.OpChat)
	if len(spans) != 1 || spans[0].Attributes[allowlist.AttrAllowlistResult] != "allow" {
		t.Fatalf("span devia ter allowlist.result=allow; got %+v", spans)
	}
	if spans[0].Attributes[allowlist.AttrBoard] != "board-eu" {
		t.Fatalf("span devia ter o board")
	}
}

// TestGateway_AllowlistDeny_FailClosed — um modelo fora da allowlist do board é
// RECUSADO fail-closed: o provider NÃO é invocado e um deny atribuível a principal+board
// é selado no WORM.
func TestGateway_AllowlistDeny_FailClosed(t *testing.T) {
	store := audit.NewMemStore()
	gw, adpt, tr := newAOS058Gateway(t, store)

	_, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "claude-3", Principal: "tok", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "olá"}},
	})
	if !errors.Is(err, allowlist.ErrModelNotAllowed) {
		t.Fatalf("modelo fora da allowlist devia falhar fail-closed; got %v", err)
	}
	if adpt.Calls() != 0 {
		t.Fatalf("provider NAO devia ter sido invocado; got %d", adpt.Calls())
	}
	// Deny selado no WORM, atribuível a principal (agente) E board (partição+obligation).
	at, ok, _ := store.At(context.Background(), "modelgw-gov:board-eu", 1)
	if !ok || at.Decision != audit.DecisionDeny {
		t.Fatalf("deny devia estar selado; ok=%v dec=%v", ok, at.Decision)
	}
	if at.Principal.NHIID != "agent-42" || at.Obligations[0].Params["board"] != "board-eu" {
		t.Fatalf("deny devia ser atribuível a principal+board; got principal=%q board=%q", at.Principal.NHIID, at.Obligations[0].Params["board"])
	}
	// O span leva o error.type do estágio que recusou (fail-closed atribuível).
	spans := tr.SpansByOperation(agentruntime.OpChat)
	if len(spans) != 1 || spans[0].Attributes[agentruntime.AttrErrorType] != "stage:allowlist-regional" {
		t.Fatalf("span devia marcar o estágio que recusou; got %+v", spans[0].Attributes[agentruntime.AttrErrorType])
	}
}

// TestGovernance_CrossBorderFailover_DenyAttributable — o TESTE-CHAVE de governação:
// uma tentativa de failover cross-border (só há capacidade fora da fronteira) produz
// um DENY explícito, registado e atribuível a principal E board no WORM. É o padrão
// que o router (AOS-059) segue: a guarda de soberania decide, o recorder de governação
// sela o deny.
func TestGovernance_CrossBorderFailover_DenyAttributable(t *testing.T) {
	store := audit.NewMemStore()
	guard := sovereignty.NewGuard()

	// Primário (eu) caiu; só há capacidade cross-border (us-east) → a guarda rejeita.
	dec := guard.Route("eu",
		sovereignty.Endpoint{KeyID: "acct-eu-1", Region: "eu"},
		[]sovereignty.Endpoint{{KeyID: "acct-us-1", Region: "us-east"}},
		func(sovereignty.Endpoint) bool { return false }, // primário indisponível
	)
	if !dec.CrossBorderBlocked() {
		t.Fatalf("a guarda devia bloquear o cross-border; got %+v", dec)
	}

	// O router sela o DENY explícito, atribuível a principal + board (nunca anónimo).
	rec := allowlist.NewRecorder(store)
	sealed, err := rec.Seal(context.Background(), allowlist.GovRecord{
		Board:          "board-eu",
		PrincipalUser:  "alice",
		PrincipalAgent: "agent-42",
		HumanRoot:      "alice",
		Model:          "gpt-4o",
		Region:         "eu",
		Decision:       audit.DecisionDeny,
		Reason:         "failover cross-border bloqueado (soberania): " + string(dec.Home),
		PolicyVersion:  "gw-allowlist/v1",
		Operation:      "chat",
		Timestamp:      time.Unix(1_700_000_000, 0),
	})
	if err != nil {
		t.Fatalf("Seal do deny cross-border: %v", err)
	}
	if sealed.Decision != audit.DecisionDeny {
		t.Fatalf("Decision = %q; quero deny", sealed.Decision)
	}
	if sealed.Principal.NHIID != "agent-42" {
		t.Fatalf("deny devia ser atribuível ao principal; got %q", sealed.Principal.NHIID)
	}
	if sealed.Obligations[0].Params["board"] != "board-eu" {
		t.Fatalf("deny devia ser atribuível ao board")
	}
	// Está mesmo no WORM append-only.
	head, _ := store.Head(context.Background(), "modelgw-gov:board-eu")
	if head != 1 {
		t.Fatalf("deny cross-border devia estar selado; head=%d", head)
	}
}

// TestIntegration_SovereigntyCoherentWithAllowlist — a decisão de soberania da guarda
// é COERENTE com o que o router (AOS-059) consumirá: as regiões que a allowlist permite
// ao (board, modelo) são exactamente a fronteira que a guarda usa para filtrar o
// failover. Um candidato numa região allowlisted é elegível; fora dela, cross-border.
func TestIntegration_SovereigntyCoherentWithAllowlist(t *testing.T) {
	pol, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}
	// Fronteira de soberania de (board-eu, gpt-4o) segundo a allowlist assinada.
	regions := pol.AllowedRegions("board-eu", "gpt-4o") // ["eu", "eu-west"]

	// A guarda agrupa as regiões allowlisted numa fronteira comuum "board-eu".
	opts := make([]sovereignty.Option, 0, len(regions))
	for _, r := range regions {
		opts = append(opts, sovereignty.WithBoundary(r, "board-eu"))
	}
	guard := sovereignty.NewGuard(opts...)

	// Failover a partir de "eu": um candidato em "eu-west" (allowlisted) é intra-fronteira;
	// um em "us-east" (fora da allowlist do board) é cross-border e descartado.
	dec := guard.Failover("eu", []sovereignty.Endpoint{
		{KeyID: "acct-euw", Region: "eu-west"},
		{KeyID: "acct-us", Region: "us-east"},
	})
	if dec.Outcome != sovereignty.OutcomeFailover || dec.Chosen.Region != "eu-west" {
		t.Fatalf("candidato allowlisted devia ser elegível; got %+v", dec)
	}
	if len(dec.Dropped) != 1 || dec.Dropped[0].Region != "us-east" {
		t.Fatalf("candidato fora da allowlist devia ser cross-border descartado; got %+v", dec.Dropped)
	}

	// E se SÓ houver o candidato fora da fronteira → rejeição cross-border (governação).
	only := guard.Failover("eu", []sovereignty.Endpoint{{KeyID: "acct-us", Region: "us-east"}})
	if !only.CrossBorderBlocked() {
		t.Fatalf("só cross-border devia ser bloqueado; got %+v", only)
	}
}
