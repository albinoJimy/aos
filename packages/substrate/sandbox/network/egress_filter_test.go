package network

import (
	"context"
	"errors"
	"strings"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
)

// newEmbeddedFilter constrói um filtro sobre a allowlist embutida com um sink e um
// tracer dados.
func newEmbeddedFilter(t *testing.T, opts ...EgressFilterOption) *EgressFilter {
	t.Helper()
	resolver, err := NewEmbeddedResolver()
	if err != nil {
		t.Fatalf("NewEmbeddedResolver: %v", err)
	}
	f, err := NewEgressFilter(resolver, append([]EgressFilterOption{withClock(fixedClock())}, opts...)...)
	if err != nil {
		t.Fatalf("NewEgressFilter: %v", err)
	}
	return f
}

// TestEgress_ForaDaAllowlist_BloqueadoEAuditado cobre o critério SEGURANÇA: um egress
// para um destino FORA da allowlist é bloqueado E gera um evento de segurança no
// audit WORM tamper-evident — verificado por [audit.Verify] sobre a cadeia real.
func TestEgress_ForaDaAllowlist_BloqueadoEAuditado(t *testing.T) {
	store := audit.NewMemStore()
	sink := NewWORMSecuritySink(store)
	f := newEmbeddedFilter(t, WithSecurityAuditSink(sink))

	principal := principalClass("web-fetcher")
	dest := NewDestination("evil.example.com", 443)

	dec, err := f.Decide(context.Background(), principal, dest)
	if err != nil {
		t.Fatalf("Decide erro inesperado: %v", err)
	}
	if dec.Allow {
		t.Fatal("egress fora da allowlist deveria ser BLOQUEADO")
	}
	if dec.Reason != ReasonNotInList {
		t.Fatalf("razão = %q, quero %q", dec.Reason, ReasonNotInList)
	}

	// O bloqueio SELOU um evento de segurança no WORM, atribuível ao principal e ao
	// destino tentado.
	part := EgressAuditPartition(principal)
	head, err := store.Head(context.Background(), part)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != 1 {
		t.Fatalf("esperava 1 registo de audit selado, obtive %d", head)
	}
	// A CADEIA está íntegra (verifica o WORM tamper-evident).
	if err := audit.Verify(context.Background(), store, part, 1, head); err != nil {
		t.Fatalf("audit.Verify da cadeia de egress: %v", err)
	}
	recs, err := store.Read(context.Background(), part, 1, 1)
	if err != nil || len(recs) != 1 {
		t.Fatalf("Read: %v, n=%d", err, len(recs))
	}
	rec := recs[0]
	if rec.Decision != audit.DecisionDeny {
		t.Fatalf("decisão de audit = %q, quero deny", rec.Decision)
	}
	if rec.Resource.Value != "evil.example.com:443" {
		t.Fatalf("destino auditado = %q, quero evil.example.com:443", rec.Resource.Value)
	}
	if rec.Principal.NHIID != "class:web-fetcher" {
		t.Fatalf("atribuição = %q, quero class:web-fetcher", rec.Principal.NHIID)
	}
	if rec.Capability != capabilityNetEgress {
		t.Fatalf("capability = %q, quero %q", rec.Capability, capabilityNetEgress)
	}
	// A versão da allowlist em vigor está selada (policy-as-code versionada).
	if !strings.HasPrefix(rec.PolicyVersion, "sbx-egress/v1#") {
		t.Fatalf("policy_version selada = %q", rec.PolicyVersion)
	}
	// SEM SEGREDO: nenhum campo/obligation transporta credencial/token.
	assertNoSecret(t, rec)
}

// TestEgress_NaAllowlist_Permitido cobre o allow: um destino na allowlist do
// principal é permitido, com a versão da política propagada. Por omissão, um allow
// NÃO sela evento de segurança (o WORM foca-se nos bloqueios).
func TestEgress_NaAllowlist_Permitido(t *testing.T) {
	store := audit.NewMemStore()
	f := newEmbeddedFilter(t, WithSecurityAuditSink(NewWORMSecuritySink(store)))

	principal := principalClass("web-fetcher")
	dec, err := f.Decide(context.Background(), principal, NewDestination("api.github.com", 443))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !dec.Allow {
		t.Fatalf("destino na allowlist deveria ser permitido, reason=%q", dec.Reason)
	}
	if !strings.HasPrefix(dec.PolicyVersion, "sbx-egress/v1#") {
		t.Fatalf("policy_version = %q", dec.PolicyVersion)
	}
	// Sem selagem de allow por omissão.
	head, _ := store.Head(context.Background(), EgressAuditPartition(principal))
	if head != 0 {
		t.Fatalf("allow não deveria selar por omissão, head=%d", head)
	}
}

// TestEgress_EscopoPorPrincipal cobre POLÍTICA (escopo): o mesmo destino é permitido
// para o principal certo e negado para outro.
func TestEgress_EscopoPorPrincipal(t *testing.T) {
	f := newEmbeddedFilter(t, WithSecurityAuditSink(NewWORMSecuritySink(audit.NewMemStore())))
	dest := NewDestination("10.20.0.5", 443) // CIDR de billing

	if dec, _ := f.Decide(context.Background(), principalClass("billing"), dest); !dec.Allow {
		t.Fatal("billing deveria alcançar o seu próprio CIDR")
	}
	if dec, _ := f.Decide(context.Background(), principalClass("web-fetcher"), dest); dec.Allow {
		t.Fatal("web-fetcher NÃO deveria alcançar o CIDR de billing (escopo por principal)")
	}
}

// TestEgress_FailClosed_SemAllowlist cobre FAIL-CLOSED: sem allowlist configurada
// (resolver devolve nil), TODO o egress é negado; um resolver com erro é igualmente
// deny.
func TestEgress_FailClosed_SemAllowlist(t *testing.T) {
	sink := &recordingSink{}
	// Resolver que nunca resolve uma allowlist (nil, nil).
	nilResolver := ResolverFunc(func(context.Context, referencemonitor.Principal) (*Policy, error) {
		return nil, nil
	})
	f, err := NewEgressFilter(nilResolver, WithSecurityAuditSink(sink), withClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewEgressFilter: %v", err)
	}
	dec, err := f.Decide(context.Background(), principalClass("web-fetcher"), NewDestination("api.github.com", 443))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if dec.Allow {
		t.Fatal("sem allowlist, todo o egress deve ser negado (fail-closed)")
	}
	if dec.Reason != ReasonNoPolicy {
		t.Fatalf("razão = %q, quero %q", dec.Reason, ReasonNoPolicy)
	}
	if len(sink.all()) != 1 {
		t.Fatalf("o bloqueio deve ser auditado, eventos=%d", len(sink.all()))
	}

	// Resolver com erro → deny também.
	errResolver := ResolverFunc(func(context.Context, referencemonitor.Principal) (*Policy, error) {
		return nil, errors.New("pdp indisponivel")
	})
	f2, err := NewEgressFilter(errResolver, WithSecurityAuditSink(&recordingSink{}), withClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewEgressFilter: %v", err)
	}
	if dec, _ := f2.Decide(context.Background(), principalClass("web-fetcher"), NewDestination("api.github.com", 443)); dec.Allow {
		t.Fatal("resolver com erro deve negar (fail-closed)")
	}
}

// TestEgress_FailClosed_DestinoInvalido cobre o fail-closed de destino: um destino
// sem porta/localizador é negado sem sequer consultar a allowlist.
func TestEgress_FailClosed_DestinoInvalido(t *testing.T) {
	f := newEmbeddedFilter(t, WithSecurityAuditSink(&recordingSink{}))
	cases := []Destination{
		{Host: "api.github.com", Port: 0}, // sem porta
		{Port: 443},                       // sem localizador
		{},                                // vazio
	}
	for _, d := range cases {
		if dec, _ := f.Decide(context.Background(), principalClass("web-fetcher"), d); dec.Allow {
			t.Fatalf("destino inválido %+v deveria ser negado", d)
		}
	}
}

// TestEgress_FailClosed_AuditIndisponivel cobre o fail-closed do AUDIT: se a selagem
// do bloqueio falhar, a decisão permanece deny e o erro é surfaçado; se a selagem de
// um ALLOW (com WithAuditAllows) falhar, o allow degrada para DENY
// (audit-before-effect).
func TestEgress_FailClosed_AuditIndisponivel(t *testing.T) {
	// Bloqueio com sink que falha: decisão deny + erro surfaçado.
	failing := &recordingSink{failWith: errors.New("worm offline")}
	fBlock := newEmbeddedFilter(t, WithSecurityAuditSink(failing))
	dec, err := fBlock.Decide(context.Background(), principalClass("web-fetcher"), NewDestination("evil.io", 443))
	if dec.Allow {
		t.Fatal("bloqueio nunca abre, mesmo com audit a falhar")
	}
	if err == nil {
		t.Fatal("falha de selagem do bloqueio deve ser surfaçada")
	}

	// Allow com WithAuditAllows e sink a falhar: degrada para deny.
	fAllow := newEmbeddedFilter(t, WithSecurityAuditSink(failing), WithAuditAllows())
	dec2, err2 := fAllow.Decide(context.Background(), principalClass("web-fetcher"), NewDestination("api.github.com", 443))
	if dec2.Allow {
		t.Fatal("allow não-auditável deve degradar para deny (audit-before-effect)")
	}
	if dec2.Reason != ReasonAuditFailed || err2 == nil {
		t.Fatalf("esperava ReasonAuditFailed + erro, obtive reason=%q err=%v", dec2.Reason, err2)
	}
}

// TestEgress_Span_DecisaoSemSegredo cobre o DoD de observabilidade: o span transporta
// a decisão de egress (principal, destino, allow/deny, versão) e NENHUM segredo.
func TestEgress_Span_DecisaoSemSegredo(t *testing.T) {
	tr := newRecordingTracer()
	f := newEmbeddedFilter(t, WithTracer(tr), WithSecurityAuditSink(NewWORMSecuritySink(audit.NewMemStore())))
	_, _ = f.Decide(context.Background(), principalClass("web-fetcher"), NewDestination("evil.io", 443))

	if v, ok := tr.span.get(AttrEgressAllowed); !ok || v != false {
		t.Fatalf("span deve marcar allowed=false, obtive %v (ok=%v)", v, ok)
	}
	if v, ok := tr.span.get(AttrEgressReason); !ok || v != ReasonNotInList {
		t.Fatalf("span deve ter a razão do bloqueio, obtive %v", v)
	}
	if v, ok := tr.span.get(AttrPolicyVersion); !ok || !strings.HasPrefix(v.(string), "sbx-egress/v1#") {
		t.Fatalf("span deve ter a versão da política, obtive %v", v)
	}
	if !tr.span.ended {
		t.Fatal("o span deve ser fechado")
	}
	// Nenhum atributo de span transporta segredo (só principal/destino/decisão).
	for k, v := range tr.span.attrs {
		if s, ok := v.(string); ok && looksSecret(s) {
			t.Fatalf("atributo de span %q parece transportar segredo: %q", k, s)
		}
	}
}

// assertNoSecret confirma que um registo de audit não transporta segredos.
func assertNoSecret(t *testing.T, rec audit.AuditRecord) {
	t.Helper()
	for _, ob := range rec.Obligations {
		for k, v := range ob.Params {
			if looksSecret(v) {
				t.Fatalf("obligation %q param %q parece segredo: %q", ob.Type, k, v)
			}
		}
	}
}

// looksSecret é uma heurística grosseira para o teste de não-vazamento.
func looksSecret(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, "secret") || strings.Contains(low, "token") ||
		strings.Contains(low, "bearer") || strings.Contains(low, "password") ||
		strings.Contains(low, "api-key") || strings.Contains(low, "apikey")
}
