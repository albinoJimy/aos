package network

import (
	"context"
	"sync/atomic"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
)

// buildMediatedMonitor monta um Reference Monitor REAL com a cadeia canónica em que
// o slot "egress" é ocupado pelo [EgressHook] (enforcement real). Devolve o monitor,
// um contador de execuções da tool e o audit store WORM.
func buildMediatedMonitor(t *testing.T) (*referencemonitor.Monitor, *atomic.Int64, audit.Store) {
	t.Helper()
	resolver, err := NewEmbeddedResolver()
	if err != nil {
		t.Fatalf("NewEmbeddedResolver: %v", err)
	}
	store := audit.NewMemStore()
	filter, err := NewEgressFilter(resolver, WithSecurityAuditSink(NewWORMSecuritySink(store)), withClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewEgressFilter: %v", err)
	}
	hook, err := NewEgressHook(filter)
	if err != nil {
		t.Fatalf("NewEgressHook: %v", err)
	}
	// Cadeia canónica com o egress REAL no lugar do stub.
	m := referencemonitor.New(referencemonitor.WithHooks(
		referencemonitor.IdentityStub{},
		referencemonitor.PolicyStub{},
		referencemonitor.BudgetStub{},
		hook,
		referencemonitor.AuditStub{},
	))
	var calls atomic.Int64
	if err := m.Register("tool.http", func(_ context.Context, in []byte) ([]byte, error) {
		calls.Add(1)
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return m, &calls, store
}

// TestHook_RM_AplicaDecisaoDeEgress prova que É O REFERENCE MONITOR que aplica a
// decisão de egress na mediação (o filtro apenas decide). Um destino na allowlist é
// PERMITIDO e a tool despacha; um destino fora da allowlist é NEGADO na cadeia, a
// tool NÃO despacha, e o bloqueio é selado no WORM — tudo pela via mediada
// (rm.Mediate), sem caminho que salte o RM.
func TestHook_RM_AplicaDecisaoDeEgress(t *testing.T) {
	m, calls, store := buildMediatedMonitor(t)
	principal := principalClass("web-fetcher")

	// (1) Egress PERMITIDO → permit + tool despachada.
	allowCall := referencemonitor.Call{
		RunID: "run-1", StepID: "step-1", ToolID: "tool.http",
		Principal: principal,
		Resource:  referencemonitor.Resource{Type: "url", Value: "https://api.github.com/repos"},
		Input:     []byte(`{}`),
	}
	dec, err := m.Mediate(context.Background(), allowCall)
	if err != nil {
		t.Fatalf("Mediate(allow): %v", err)
	}
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("esperava permit para destino na allowlist, obtive %q (%s)", dec.Effect, dec.Reason)
	}
	if calls.Load() != 1 {
		t.Fatalf("a tool deveria ter despachado uma vez, calls=%d", calls.Load())
	}

	// (2) Egress FORA da allowlist → deny na cadeia, tool NÃO despacha.
	denyCall := referencemonitor.Call{
		RunID: "run-2", StepID: "step-2", ToolID: "tool.http",
		Principal: principal,
		Resource:  referencemonitor.Resource{Type: "url", Value: "https://evil.example.com/collect"},
		Input:     []byte(`{}`),
	}
	dec2, err := m.Mediate(context.Background(), denyCall)
	if err != nil {
		t.Fatalf("Mediate(deny): %v", err)
	}
	if dec2.Effect != referencemonitor.EffectDeny {
		t.Fatalf("esperava deny para destino fora da allowlist, obtive %q", dec2.Effect)
	}
	if dec2.DeniedBy != "egress" {
		t.Fatalf("o deny deveria vir do hook de egress, DeniedBy=%q", dec2.DeniedBy)
	}
	if calls.Load() != 1 {
		t.Fatalf("a tool NÃO deveria despachar num deny, calls=%d", calls.Load())
	}

	// O bloqueio foi selado no WORM tamper-evident, correlacionado à trajectória.
	part := EgressAuditPartition(principal)
	head, _ := store.Head(context.Background(), part)
	if head != 1 {
		t.Fatalf("esperava 1 evento de bloqueio selado, head=%d", head)
	}
	if err := audit.Verify(context.Background(), store, part, 1, head); err != nil {
		t.Fatalf("audit.Verify: %v", err)
	}
	recs, _ := store.Read(context.Background(), part, 1, 1)
	if len(recs) != 1 || recs[0].RunID != "run-2" || recs[0].StepID != "step-2" {
		t.Fatalf("correlação run/step não selada: %+v", recs)
	}
}

// TestHook_RecursoNaoRede_Abstem prova que o hook de egress se ABSTÉM em recursos que
// não são egress de rede (ex.: file): a mediação prossegue e a tool despacha.
func TestHook_RecursoNaoRede_Abstem(t *testing.T) {
	m, calls, _ := buildMediatedMonitor(t)
	call := referencemonitor.Call{
		RunID: "run-3", StepID: "step-3", ToolID: "tool.http",
		Principal: principalClass("web-fetcher"),
		Resource:  referencemonitor.Resource{Type: "file", Value: "/reports/out.txt"},
		Input:     []byte(`{}`),
	}
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("recurso não-rede deveria passar o egress, obtive %q (%s)", dec.Effect, dec.Reason)
	}
	if calls.Load() != 1 {
		t.Fatalf("a tool deveria despachar, calls=%d", calls.Load())
	}
}

// TestHook_CapabilidadeDeRede_TipoNaoRede_Deny cobre o fecho do vector de exfiltração
// (CamoLeak): um call que DECLARA uma capability de rede (cap:http.*) mas cujo
// Resource.Type NÃO é de rede (ex.: "file", vazio, "http") NÃO passa a mediação por
// abstenção — é NEGADO fail-closed na cadeia (o destino não é verificável contra a
// allowlist). A tool NÃO despacha.
func TestHook_CapabilidadeDeRede_TipoNaoRede_Deny(t *testing.T) {
	cases := []struct {
		name     string
		resType  string
		resValue string
		cap      string
	}{
		{"tipo file, cap http", "file", "/reports/out.txt", "cap:http.post"},
		{"tipo vazio, cap net", "", "api.evil.com:443", "cap:net.connect"},
		{"tipo http (nao-rede), cap http", "http", "api.evil.com", "cap:http.get"},
		{"tipo webhook, cap net", "webhook", "https://evil.com/x", "cap:net"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, calls, _ := buildMediatedMonitor(t)
			call := referencemonitor.Call{
				RunID: "run-x", StepID: "step-x", ToolID: "tool.http",
				Principal:  principalClass("web-fetcher"),
				Capability: tc.cap,
				Resource:   referencemonitor.Resource{Type: tc.resType, Value: tc.resValue},
				Input:      []byte(`{}`),
			}
			dec, err := m.Mediate(context.Background(), call)
			if err != nil {
				t.Fatalf("Mediate: %v", err)
			}
			if dec.Effect != referencemonitor.EffectDeny {
				t.Fatalf("egress com capability de rede e tipo não-rede deveria ser NEGADO, obtive %q", dec.Effect)
			}
			if dec.DeniedBy != "egress" {
				t.Fatalf("o deny deveria vir do hook de egress, DeniedBy=%q", dec.DeniedBy)
			}
			if calls.Load() != 0 {
				t.Fatalf("a tool NÃO deveria despachar, calls=%d", calls.Load())
			}
		})
	}
}

// TestHook_SemCapabilidadeDeRede_TipoNaoRede_Abstem confirma que, sem capability de
// rede, um recurso não-rede continua a fazer o hook ABSTER-SE (não é competência do
// egress): a mediação prossegue.
func TestHook_SemCapabilidadeDeRede_TipoNaoRede_Abstem(t *testing.T) {
	m, calls, _ := buildMediatedMonitor(t)
	call := referencemonitor.Call{
		RunID: "run-y", StepID: "step-y", ToolID: "tool.http",
		Principal:  principalClass("web-fetcher"),
		Capability: "cap:fs.write", // não é capability de rede
		Resource:   referencemonitor.Resource{Type: "file", Value: "/reports/out.txt"},
		Input:      []byte(`{}`),
	}
	dec, err := m.Mediate(context.Background(), call)
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if dec.Effect != referencemonitor.EffectPermit {
		t.Fatalf("recurso não-rede sem capability de rede deveria passar o egress, obtive %q (%s)", dec.Effect, dec.Reason)
	}
	if calls.Load() != 1 {
		t.Fatalf("a tool deveria despachar, calls=%d", calls.Load())
	}
}

// TestIsNetworkCapability cobre a heurística de capability de rede (case-insensitive,
// prefixo exacto ou seguido de ponto).
func TestIsNetworkCapability(t *testing.T) {
	yes := []string{"cap:http.post", "cap:http.get", "cap:net.connect", "cap:http", "cap:net", "CAP:HTTP.POST", "  cap:net.dial  "}
	for _, c := range yes {
		if !IsNetworkCapability(c) {
			t.Errorf("IsNetworkCapability(%q) = false, quero true", c)
		}
	}
	no := []string{"", "cap:fs.write", "cap:db.read", "cap:https.post", "cap:network", "http", "net", "cap:httpx.post"}
	for _, c := range no {
		if IsNetworkCapability(c) {
			t.Errorf("IsNetworkCapability(%q) = true, quero false", c)
		}
	}
}

// TestNewEgressHook_NilFilter cobre a recusa de construção sem filtro.
func TestNewEgressHook_NilFilter(t *testing.T) {
	if _, err := NewEgressHook(nil); err != ErrNilFilter {
		t.Fatalf("NewEgressHook(nil) err = %v, quero ErrNilFilter", err)
	}
}
