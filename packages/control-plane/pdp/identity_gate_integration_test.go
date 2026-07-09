package pdp

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/identity"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-007 — fronteira de confiança identidade↔allowlist (remediação).
//
// O gate default-deny da allowlist keia em agent_class. Em produção essa classe
// TEM de chegar RESOLVIDA da NHI verificada pelo hook de identidade (AOS-005/006),
// nunca do Call bruto do caller — senão um caller forja agent_class e amplifica
// capabilities. buildRM (rmadapter_test.go) usa o IdentityStub neutro de propósito
// (identidade fora do âmbito dos testes focados no PDP), pelo que NÃO demonstra
// esta fronteira. Estes testes compõem o IdentityCheck REAL antes do PolicyCheck
// real e provam que a agent_class do TOKEN sobrepõe a forjada no Call, ANTES de o
// gate decidir.

// secureIssuerID é o emissor confiável usado nestes testes de composição segura.
const secureIssuerID = "aos-issuer-secure"

// buildSecureRM compõe a cadeia de mediação de REFERÊNCIA de produção para o gate
// de capabilities: IdentityCheck real (resolve/valida a NHI e RE-DERIVA o
// Principal, incl. agent_class, do token verificado) → PolicyCheck real (gate
// default-deny da allowlist + regras Cedar) → stubs neutros dos restantes hooks.
// Devolve o RM e o Issuer confiável para cunhar tokens de teste.
func buildSecureRM(t testing.TB, store eventstore.EventStore) (*rm.Monitor, *identity.Issuer) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// As class policies do EMISSOR (escopo-máximo por classe) são deliberadamente
	// PERMISSIVAS quanto a cap:http.post para AMBAS as classes: assim a fronteira de
	// escopo do token (imposta pelo IdentityCheck) passa em ambos os casos e ISOLA a
	// decisão do gate default-deny da allowlist do PDP — que keia na agent_class e é
	// a camada sob teste. A allowlist de referência (policies/capabilities) só lista
	// cap:http.post para agent-worker, NÃO para agent-reader.
	classes := map[string]identity.ClassPolicy{
		"agent-worker": {TTL: 5 * time.Minute, Scope: []string{"cap:http.post", "cap:fs.read"}},
		"agent-reader": {TTL: 5 * time.Minute, Scope: []string{"cap:http.post", "cap:fs.read"}},
	}
	iss, err := identity.NewIssuer(secureIssuerID, priv, classes)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	v := identity.NewVerifier(identity.WithTrustedIssuer(secureIssuerID, pub))

	p := mustOpen(t)
	m := rm.New(
		rm.WithHooks(
			identity.NewIdentityCheck(v), // identidade REAL: re-deriva o Principal do token
			NewPolicyCheck(p),            // gate da allowlist + Cedar
			rm.BudgetStub{},
			rm.EgressStub{},
			rm.AuditStub{},
		),
		rm.WithEventSink(rm.NewEventStoreSink(store)),
	)
	if err := m.Register("tool.http", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return m, iss
}

// mintToken cunha um token NHI válido para (agentID, agentClass) com o escopo dado.
func mintToken(t testing.TB, iss *identity.Issuer, agentID, agentClass string, scope []string) string {
	t.Helper()
	tok, err := iss.Issue(context.Background(), identity.IssueRequest{
		UserID:        "human:alice",
		AgentID:       agentID,
		AgentClass:    agentClass,
		UserAuthority: scope,
	})
	if err != nil {
		t.Fatalf("Issue(%s/%s): %v", agentID, agentClass, err)
	}
	return tok.Compact
}

// TestIntegration_IdentityGate_ForgedAgentClassIgnored_Deny é o teste nuclear da
// remediação: um caller apresenta um Call com agent_class FORJADA "agent-worker"
// (que lista cap:http.post na allowlist) mas com uma Credential cujo token real é
// da classe "agent-reader" (que NÃO lista cap:http.post). Com o IdentityCheck real
// à frente do PolicyCheck, o gate decide sobre a classe do TOKEN (agent-reader) e
// NEGA — a forja é ignorada. Sem esta composição (só IdentityStub) o gate leria a
// classe forjada e PERMITIRIA: o bypass que a remediação fecha.
func TestIntegration_IdentityGate_ForgedAgentClassIgnored_Deny(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	m, iss := buildSecureRM(t, store)

	// Token REAL de agent-reader, com cap:http.post NO ESCOPO (passa a fronteira de
	// escopo do IdentityCheck) — mas a classe agent-reader não a lista na allowlist.
	readerTok := mintToken(t, iss, "agt-reader", "agent-reader", []string{"cap:http.post"})

	var dispatched bool
	_ = m.Register("tool.http", func(_ context.Context, in []byte) ([]byte, error) {
		dispatched = true
		return in, nil
	})

	call := rm.Call{
		RequestID: "req-forge", RunID: "run-forge-deny", StepID: "s1",
		ToolID: "tool.http", Capability: "cap:http.post",
		Credential: readerTok, // NHI real = agent-reader
		Resource:   rm.Resource{Type: "url", Value: "https://api.example.com/x", Region: "eu"},
		// Principal FORJADO pelo caller: classe de maior privilégio + autoridade.
		Principal: rm.Principal{NHIID: "forjado", AgentClass: "agent-worker", Authority: []string{"cap:http.post"}},
		Context:   rm.CallContext{Taint: "trusted"},
		Input:     []byte("body"),
	}

	d, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != rm.EffectDeny {
		t.Fatalf("esperava DENY (classe real agent-reader nao lista cap:http.post), obtive %q (%s)", d.Effect, d.Reason)
	}
	if d.DeniedBy != "policy" {
		t.Errorf("DeniedBy=%q, esperava policy (gate da allowlist, nao a identidade)", d.DeniedBy)
	}
	if dispatched {
		t.Error("a tool NAO devia ser despachada: o gate negou pela classe REAL do token")
	}
	// A razão nomeia a classe do TOKEN (agent-reader), não a forjada (agent-worker):
	// prova de que a decisão usou a identidade verificada.
	if !contains(d.Reason, "agent-reader") {
		t.Errorf("reason=%q devia nomear a classe REAL do token (agent-reader)", d.Reason)
	}
	if contains(d.Reason, "agent-worker") {
		t.Errorf("reason=%q NAO devia referir a classe forjada agent-worker", d.Reason)
	}

	// O evento de negação regista a agent_class RESOLVIDA (agent-reader), não a forja.
	ev := readOne(t, store, "run-forge-deny")
	if ev.Type != rm.EventTypeDenied {
		t.Errorf("Type=%q, esperava %q", ev.Type, rm.EventTypeDenied)
	}
	var pl mediationPayloadView
	if err := json.Unmarshal(ev.Payload, &pl); err != nil {
		t.Fatalf("payload invalido: %v", err)
	}
	if pl.Principal.AgentClass != "agent-reader" {
		t.Errorf("evento: agent_class=%q, esperava agent-reader (resolvida do token, forja ignorada)", pl.Principal.AgentClass)
	}
	if pl.Principal.NHIID != "agt-reader" {
		t.Errorf("evento: nhi_id=%q, esperava agt-reader (resolvido do token)", pl.Principal.NHIID)
	}
}

// TestIntegration_IdentityGate_RealClassUsed_Permit é o simétrico: um Call com
// agent_class FORJADA "agent-reader" (menor privilégio) mas Credential de um token
// real da classe "agent-worker" (que lista cap:http.post). A decisão usa a classe
// do TOKEN (agent-worker) e PERMITE — provando que a resolução da identidade
// sobrepõe a forja em AMBAS as direcções, não só a favor da negação.
func TestIntegration_IdentityGate_RealClassUsed_Permit(t *testing.T) {
	t.Parallel()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = store.Close() }()

	m, iss := buildSecureRM(t, store)
	workerTok := mintToken(t, iss, "agt-worker", "agent-worker", []string{"cap:http.post"})

	call := rm.Call{
		RequestID: "req-real", RunID: "run-real-permit", StepID: "s1",
		ToolID: "tool.http", Capability: "cap:http.post",
		Credential: workerTok, // NHI real = agent-worker
		Resource:   rm.Resource{Type: "url", Value: "https://api.example.com/orders", Region: "eu"},
		// Forja de menor privilégio: irrelevante, o token manda.
		Principal: rm.Principal{NHIID: "forjado", AgentClass: "agent-reader", Authority: []string{"cap:http.post"}},
		Context:   rm.CallContext{Taint: "trusted"},
		Input:     []byte("body"),
	}

	d, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if !d.Permitted() {
		t.Fatalf("esperava PERMIT (classe real agent-worker lista cap:http.post), obtive %q (%s)", d.Effect, d.Reason)
	}

	ev := readOne(t, store, "run-real-permit")
	if ev.Type != rm.EventTypeMediated {
		t.Errorf("Type=%q, esperava %q", ev.Type, rm.EventTypeMediated)
	}
	var pl mediationPayloadView
	if err := json.Unmarshal(ev.Payload, &pl); err != nil {
		t.Fatalf("payload invalido: %v", err)
	}
	if pl.Principal.AgentClass != "agent-worker" {
		t.Errorf("evento: agent_class=%q, esperava agent-worker (resolvida do token)", pl.Principal.AgentClass)
	}
}
