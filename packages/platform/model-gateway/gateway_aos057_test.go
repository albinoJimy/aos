package modelgateway_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/identity"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/metering/attribution"
	"github.com/aos-ref/platform/model-gateway/pipeline/authn"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/routing/keypool"
)

// aos057Harness reúne o que os testes de governação de AOS-057 precisam.
type aos057Harness struct {
	gw    *modelgateway.Gateway
	iss   *identity.Issuer
	store audit.Store
	recs  *[]attribution.Record
	trace *agentruntime.RecordingTracer
	adpt  *adapters.FakeAdapter
}

// newAOS057Gateway compõe um GW REAL com o estágio authn (token + autoridade),
// keypool (chave por throughput desacoplada) e atribuição (span + WORM). O pool é
// injectado pelo teste para controlar a rotação de chaves de forma determinista.
func newAOS057Gateway(t *testing.T, pool *keypool.Pool, extra ...modelgateway.Option) *aos057Harness {
	t.Helper()
	return newAOS057GatewayOn(t, pool, audit.NewMemStore(), extra...)
}

// newAOS057GatewayOn é como [newAOS057Gateway] mas com o audit [audit.Store]
// injectável — permite testar o comportamento FAIL-CLOSED quando a selagem WORM
// falha (um store cuja Append devolve erro). As opções extra permitem compor outros
// estágios de metering (p.ex. WithCost de AOS-062) no MESMO GW/span.
func newAOS057GatewayOn(t *testing.T, pool *keypool.Pool, store audit.Store, extra ...modelgateway.Option) *aos057Harness {
	t.Helper()
	clock := func() time.Time { return time.Unix(1_700_000_000, 0) }

	// Identidade real (AOS-005/006).
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	classes := map[string]identity.ClassPolicy{
		"reader": {TTL: time.Hour, Scope: []string{"model:invoke"}},
	}
	iss, err := identity.NewIssuer("iss-test", priv, classes, identity.WithIssuerClock(clock))
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	ver := identity.NewVerifier(identity.WithTrustedIssuer("iss-test", pub), identity.WithVerifierClock(clock))
	resolver := authn.NewStaticAuthority().
		SetUser("alice", "model:invoke").
		SetUser("bob", "model:invoke").
		SetClass("reader", "model:invoke")
	pol, _ := authn.LoadPolicy()
	authStage := authn.New(ver, resolver, pol)

	// Keypool para (openai, eu).
	reg := keypool.NewRegistry()
	reg.Register("openai", "eu", pool)

	// Atribuição: WORM (injectado) + sink em memória.
	var captured []attribution.Record
	rec := attribution.NewRecorder(store, attribution.WithSink(attribution.SinkFunc(
		func(_ context.Context, r attribution.Record) { captured = append(captured, r) },
	)))

	// Credencial de infra (segredo via porta; nunca hard-coded na chamada).
	cs := adapters.NewStaticCredentialSource()
	cs.Set("openai", "eu", "sk-infra-secreto")

	adpt := adapters.NewFakeAdapter("openai")
	trace := &agentruntime.RecordingTracer{}
	opts := []modelgateway.Option{
		modelgateway.WithCredentialSource(cs),
		modelgateway.WithDefaultRegion("eu"),
		modelgateway.WithClock(clock),
		modelgateway.WithTracer(trace),
		modelgateway.WithAuthnStage(authStage),
		modelgateway.WithKeyPool(reg),
		modelgateway.WithAttribution(rec),
	}
	gw := modelgateway.New(adpt, append(opts, extra...)...)
	return &aos057Harness{gw: gw, iss: iss, store: store, recs: &captured, trace: trace, adpt: adpt}
}

func (h *aos057Harness) token(t *testing.T, user, agent string) string {
	t.Helper()
	tok, err := h.iss.Issue(context.Background(), identity.IssueRequest{
		UserID: user, AgentID: agent, AgentClass: "reader",
		UserAuthority: []string{"model:invoke"},
	})
	if err != nil {
		t.Fatalf("Issue(%s/%s): %v", user, agent, err)
	}
	return tok.Compact
}

func (h *aos057Harness) chat(t *testing.T, token string) {
	t.Helper()
	_, err := h.gw.Chat(context.Background(), port.ChatRequest{
		Model:     "gpt-x",
		Messages:  []port.Message{{Role: port.RoleUser, Content: "oi"}},
		Principal: token,
		Region:    "eu",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
}

// TestGateway_CrossAttribution_SamePrincipalDifferentKeys é o teste-chave (a):
// o MESMO principal servido por chaves de infra DIFERENTES mantém o MESMO
// principal no registo — a chave rotou, a atribuição não.
func TestGateway_CrossAttribution_SamePrincipalDifferentKeys(t *testing.T) {
	t.Parallel()
	// Pool com DUAS contas de igual limite -> a selecção por throughput alterna.
	pool := keypool.NewPool(
		keypool.Account{KeyID: "acct-a", LimitRPM: 100},
		keypool.Account{KeyID: "acct-b", LimitRPM: 100},
	)
	h := newAOS057Gateway(t, pool)
	tok := h.token(t, "alice", "agent-1")

	h.chat(t, tok)
	h.chat(t, tok)

	recs := *h.recs
	if len(recs) != 2 {
		t.Fatalf("esperava 2 registos, obtive %d", len(recs))
	}
	// MESMO principal nas duas chamadas.
	if recs[0].PrincipalID() != recs[1].PrincipalID() {
		t.Fatalf("principal divergiu: %q vs %q", recs[0].PrincipalID(), recs[1].PrincipalID())
	}
	if recs[0].PrincipalID() != "user:alice;agent:agent-1" {
		t.Fatalf("principal = %q, quer user:alice;agent:agent-1", recs[0].PrincipalID())
	}
	// Chaves de infra DIFERENTES (a rotação por throughput aconteceu).
	if recs[0].KeyID == recs[1].KeyID {
		t.Fatalf("chaves iguais (%q); esperava rotacao entre acct-a/acct-b", recs[0].KeyID)
	}
	// Nenhum registo diz "o pool"; ambos têm um KeyID concreto e não-secreto.
	for i, r := range recs {
		if r.KeyID == "" || r.KeyID == "pool" {
			t.Fatalf("registo #%d sem chave concreta: %q", i, r.KeyID)
		}
	}
}

// TestGateway_CrossAttribution_DifferentPrincipalsSameKey é o teste-chave (b):
// principais DIFERENTES servidos pela MESMA chave permanecem DISTINGUÍVEIS.
func TestGateway_CrossAttribution_DifferentPrincipalsSameKey(t *testing.T) {
	t.Parallel()
	// Pool de UMA conta -> a mesma chave serve todas as chamadas.
	pool := keypool.NewPool(keypool.Account{KeyID: "acct-shared", LimitRPM: 1000})
	h := newAOS057Gateway(t, pool)

	h.chat(t, h.token(t, "alice", "agent-1"))
	h.chat(t, h.token(t, "bob", "agent-2"))

	recs := *h.recs
	if len(recs) != 2 {
		t.Fatalf("esperava 2 registos, obtive %d", len(recs))
	}
	// MESMA chave de infra.
	if recs[0].KeyID != "acct-shared" || recs[1].KeyID != "acct-shared" {
		t.Fatalf("chaves = %q,%q, quer ambas acct-shared", recs[0].KeyID, recs[1].KeyID)
	}
	// Principais DISTINGUÍVEIS apesar da chave partilhada.
	if recs[0].PrincipalID() == recs[1].PrincipalID() {
		t.Fatalf("principais indistinguiveis sob a mesma chave: %q", recs[0].PrincipalID())
	}
	if recs[0].PrincipalID() != "user:alice;agent:agent-1" || recs[1].PrincipalID() != "user:bob;agent:agent-2" {
		t.Fatalf("principais = %q,%q", recs[0].PrincipalID(), recs[1].PrincipalID())
	}
}

// TestGateway_Attribution_SpanAndWORM: uma chamada regista principal/modelo/região
// no span OTel GenAI E sela no audit WORM verificável.
func TestGateway_Attribution_SpanAndWORM(t *testing.T) {
	t.Parallel()
	pool := keypool.NewPool(keypool.Account{KeyID: "acct-eu-1", LimitRPM: 100})
	h := newAOS057Gateway(t, pool)
	h.chat(t, h.token(t, "alice", "agent-1"))

	// Span: principal + modelo + região + KeyID não-secreto; NUNCA o segredo.
	spans := h.trace.SpansByOperation(agentruntime.OpChat)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span chat, obtive %d", len(spans))
	}
	attrs := spans[0].Attributes
	if attrs[attribution.AttrPrincipalUser] != "alice" || attrs[attribution.AttrPrincipalAgent] != "agent-1" {
		t.Fatalf("span sem principal: %+v", attrs)
	}
	if attrs[attribution.AttrRegion] != "eu" || attrs[attribution.AttrKeyID] != "acct-eu-1" {
		t.Fatalf("span sem regiao/keyid: %+v", attrs)
	}
	if attrs[agentruntime.AttrRequestModel] != "gpt-x" {
		t.Fatalf("span sem modelo: %+v", attrs)
	}
	for k, v := range attrs {
		if s, ok := v.(string); ok && s == "sk-infra-secreto" {
			t.Fatalf("SEGREDO no span (atributo %q)", k)
		}
	}

	// WORM: registo selado, verificável na hash-chain.
	part := "modelgw:human:alice"
	head, _ := h.store.Head(context.Background(), part)
	if head != 1 {
		t.Fatalf("WORM head = %d, quer 1", head)
	}
	if err := audit.Verify(context.Background(), h.store, part, 1, head); err != nil {
		t.Fatalf("cadeia WORM invalida: %v", err)
	}
	sealed, _, _ := h.store.At(context.Background(), part, 1)
	if sealed.Principal.NHIID != "agent-1" || sealed.Resource.Value != "gpt-x" {
		t.Fatalf("registo WORM sem principal/modelo: %+v", sealed)
	}
}

// TestGateway_AuthnDeny_FailClosed: um token inválido é recusado ANTES do provider
// — o adaptador NÃO é invocado e NÃO há registo de atribuição.
func TestGateway_AuthnDeny_FailClosed(t *testing.T) {
	t.Parallel()
	pool := keypool.NewPool(keypool.Account{KeyID: "acct-eu-1", LimitRPM: 100})
	h := newAOS057Gateway(t, pool)

	_, err := h.gw.Chat(context.Background(), port.ChatRequest{
		Model:     "gpt-x",
		Messages:  []port.Message{{Role: port.RoleUser, Content: "oi"}},
		Principal: "token-invalido",
		Region:    "eu",
	})
	if err == nil {
		t.Fatalf("esperava recusa fail-closed com token invalido")
	}
	if h.adpt.Calls() != 0 {
		t.Fatalf("adaptador invocado %d vezes apesar da recusa authn", h.adpt.Calls())
	}
	if len(*h.recs) != 0 {
		t.Fatalf("houve atribuicao (%d) apesar da recusa", len(*h.recs))
	}
}

// stream drena um ChatStream do harness com um token válido (helper de streaming).
func (h *aos057Harness) stream(t *testing.T, token string) {
	t.Helper()
	s, err := h.gw.ChatStream(context.Background(), port.ChatRequest{
		Model:     "gpt-x",
		Messages:  []port.Message{{Role: port.RoleUser, Content: "oi"}},
		Principal: token,
		Region:    "eu",
	})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if _, err := port.CollectStream(s); err != nil {
		t.Fatalf("CollectStream: %v", err)
	}
}

// TestGateway_Attribution_Streaming_SpanAndWORM: também no caminho de STREAMING a
// atribuição é selada (ANTES de o stream abrir, fail-closed) — o span final leva
// principal/modelo/região/KeyID e o WORM tem 1 registo verificável. Fecha a lacuna
// de atribuição do streaming (o registo NÃO depende do usage).
func TestGateway_Attribution_Streaming_SpanAndWORM(t *testing.T) {
	t.Parallel()
	pool := keypool.NewPool(keypool.Account{KeyID: "acct-eu-1", LimitRPM: 100})
	h := newAOS057Gateway(t, pool)
	h.stream(t, h.token(t, "alice", "agent-1"))

	spans := h.trace.SpansByOperation(agentruntime.OpChat)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span chat (stream), obtive %d", len(spans))
	}
	attrs := spans[0].Attributes
	if attrs[attribution.AttrPrincipalUser] != "alice" || attrs[attribution.AttrPrincipalAgent] != "agent-1" {
		t.Fatalf("span de stream sem principal: %+v", attrs)
	}
	if attrs[attribution.AttrRegion] != "eu" || attrs[attribution.AttrKeyID] != "acct-eu-1" {
		t.Fatalf("span de stream sem regiao/keyid: %+v", attrs)
	}
	for k, v := range attrs {
		if s, ok := v.(string); ok && s == "sk-infra-secreto" {
			t.Fatalf("SEGREDO no span de stream (atributo %q)", k)
		}
	}
	// WORM: 1 registo selado e cadeia verificável.
	part := "modelgw:human:alice"
	head, _ := h.store.Head(context.Background(), part)
	if head != 1 {
		t.Fatalf("WORM head (stream) = %d, quer 1", head)
	}
	if err := audit.Verify(context.Background(), h.store, part, 1, head); err != nil {
		t.Fatalf("cadeia WORM (stream) invalida: %v", err)
	}
	if len(*h.recs) != 1 {
		t.Fatalf("esperava 1 registo de atribuicao (stream), obtive %d", len(*h.recs))
	}
}

// failingStore é um audit.Store cuja Append devolve SEMPRE erro (as leituras
// delegam num MemStore vazio). Simula um WORM indisponível para provar o
// comportamento fail-closed da atribuição.
type failingStore struct {
	inner *audit.MemStore
	err   error
}

func (s *failingStore) Append(context.Context, audit.AuditRecord) (audit.AuditRecord, error) {
	return audit.AuditRecord{}, s.err
}
func (s *failingStore) Read(ctx context.Context, p string, from, to uint64) ([]audit.AuditRecord, error) {
	return s.inner.Read(ctx, p, from, to)
}
func (s *failingStore) Head(ctx context.Context, p string) (uint64, error) {
	return s.inner.Head(ctx, p)
}
func (s *failingStore) At(ctx context.Context, p string, seq uint64) (audit.AuditRecord, bool, error) {
	return s.inner.At(ctx, p, seq)
}

// TestGateway_Attribution_FailClosed_OnWORMError prova a invariante mais forte de
// governação (Q1/Q3): se a selagem no WORM falhar, a model call NÃO se efectiva —
// audit-before-effect (ADR-010). Com um token VÁLIDO mas um audit store a falhar,
// Chat e Embeddings devolvem erro e o adaptador do provider NÃO é invocado (a
// resposta nunca é exposta), pelo que nenhuma chamada entra numa lacuna silenciosa
// da cadeia tamper-evident.
func TestGateway_Attribution_FailClosed_OnWORMError(t *testing.T) {
	t.Parallel()
	pool := keypool.NewPool(keypool.Account{KeyID: "acct-eu-1", LimitRPM: 100})
	store := &failingStore{inner: audit.NewMemStore(), err: errors.New("WORM indisponivel")}
	h := newAOS057GatewayOn(t, pool, store)
	tok := h.token(t, "alice", "agent-1")

	if _, err := h.gw.Chat(context.Background(), port.ChatRequest{
		Model: "gpt-x", Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
		Principal: tok, Region: "eu",
	}); err == nil {
		t.Fatalf("Chat: esperava erro fail-closed com WORM a falhar")
	}
	if _, err := h.gw.Embeddings(context.Background(), port.EmbeddingsRequest{
		Model: "gpt-x", Input: []string{"oi"}, Principal: tok, Region: "eu",
	}); err == nil {
		t.Fatalf("Embeddings: esperava erro fail-closed com WORM a falhar")
	}
	if h.adpt.Calls() != 0 {
		t.Fatalf("adaptador invocado %d vezes apesar da falha de selagem (audit-before-effect violado)", h.adpt.Calls())
	}
}
