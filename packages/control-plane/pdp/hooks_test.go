package pdp

import (
	"context"
	"strings"
	"testing"

	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
)

// registerEcho regista tool.http a devolver o input e a sinalizar o dispatch.
func registerEcho(t testing.TB, m *rm.Monitor, dispatched *bool) {
	t.Helper()
	if err := m.Register("tool.http", func(_ context.Context, in []byte) ([]byte, error) {
		*dispatched = true
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
}

// TestDefaultHooksWithPDP_TotalMediation prova AC1/AC2: a cadeia SANCIONADA usa o
// PDP real (não o PolicyStub). Uma capability concedida na allowlist assinada ⇒
// permit + dispatch; uma capability AUSENTE da allowlist ⇒ deny determinístico
// (default-deny) pelo hook "policy", sem dispatch. Não há via directa: o único
// caminho de execução é [rm.Monitor.Mediate].
func TestDefaultHooksWithPDP_TotalMediation(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)

	t.Run("capability_concedida_permite_e_despacha", func(t *testing.T) {
		t.Parallel()
		m := rm.New(rm.WithHooks(DefaultHooksWithPDP(p)...))
		var dispatched bool
		registerEcho(t, m, &dispatched)

		d, err := m.Mediate(context.Background(), permitCall())
		if err != nil {
			t.Fatalf("Mediate: %v", err)
		}
		if !d.Permitted() {
			t.Fatalf("esperava permit, obtive %q (%s)", d.Effect, d.Reason)
		}
		if !dispatched {
			t.Error("a tool devia ter sido despachada sob permit")
		}
	})

	t.Run("capability_desconhecida_deny_default_deny", func(t *testing.T) {
		t.Parallel()
		m := rm.New(rm.WithHooks(DefaultHooksWithPDP(p)...))
		var dispatched bool
		registerEcho(t, m, &dispatched)

		call := permitCall()
		call.Capability = "cap:unknown.tool" // ausente da allowlist assinada
		d, err := m.Mediate(context.Background(), call)
		if err != nil {
			t.Fatalf("Mediate: %v", err)
		}
		if d.Effect != rm.EffectDeny {
			t.Fatalf("esperava deny (default-deny), obtive %q", d.Effect)
		}
		if d.DeniedBy != "policy" {
			t.Errorf("DeniedBy=%q, esperava policy", d.DeniedBy)
		}
		if !strings.Contains(d.Reason, "default-deny") {
			t.Errorf("reason devia nomear default-deny: %q", d.Reason)
		}
		if dispatched {
			t.Error("a tool NAO devia ser despachada num deny")
		}
	})
}

// TestDefaultHooksWithPDP_NilFailClosed: um PDP nil deixa o PolicyCheck fail-closed
// — a cadeia sancionada nega tudo, nunca abre default-allow.
func TestDefaultHooksWithPDP_NilFailClosed(t *testing.T) {
	t.Parallel()
	m := rm.New(rm.WithHooks(DefaultHooksWithPDP(nil)...))
	var dispatched bool
	registerEcho(t, m, &dispatched)
	d, err := m.Mediate(context.Background(), permitCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != rm.EffectDeny {
		t.Fatalf("PDP nil devia negar tudo, obtive %q", d.Effect)
	}
	if dispatched {
		t.Error("nenhuma tool despachada com PDP nil")
	}
}

// scopeChainCall constrói um call com cadeia de delegação bem-formada (humano-raiz →
// agente) para exercitar o ScopeGate. A capability pedida é cap:http.post e a
// autoridade RECLAMADA inclui-a (herdada de permitCall) — para que o PDP (que também
// avalia principal.authority) PERMITA e a decisão fique nas mãos do ScopeGate, que
// computa o escopo efectivo utilizador ∩ classe a partir da [authz.AuthoritySource].
func scopeChainCall() rm.Call {
	c := permitCall()
	c.Principal.DelegationChain = []rm.DelegationHop{
		{Sub: "human:alice", ActAs: "agent:worker-1"},
	}
	// Sensitivity limpa e Input vazio: no caminho deny o enforcement de obrigações nem
	// corre (nega-se no scope antes); no caminho permit não arrasta redact_pii.
	c.Context.Sensitivity = ""
	c.Input = nil
	return c
}

// TestDefaultHooksWithPDPAndScope_Authority prova AC3: a autoridade efectiva é
// utilizador ∩ classe. Uma capability concedida pela CLASSE (allowlist do PDP + tecto
// da classe) mas NÃO pelo UTILIZADOR fica FORA do escopo efectivo e é negada pelo
// ScopeGate, mesmo com o PDP a permitir (a autoridade reclamada acima do escopo é uma
// tentativa de alargamento). Concedê-la também ao utilizador liberta-a.
func TestDefaultHooksWithPDPAndScope_Authority(t *testing.T) {
	t.Parallel()
	p := mustOpen(t)

	t.Run("classe_concede_mas_utilizador_nao_deny", func(t *testing.T) {
		t.Parallel()
		// human:alice (utilizador) NÃO tem cap:http.post; a classe tem-na. O escopo
		// efectivo utilizador ∩ classe = {cap:fs.read} ⇒ cap:http.post fica de fora.
		src := authz.NewStaticAuthoritySource().
			Set("human:alice", "cap:fs.read").
			Set("agent:worker-1", "cap:fs.read", "cap:http.post").
			Set("agent:agent-worker", "cap:fs.read", "cap:http.post")

		m := rm.New(rm.WithHooks(DefaultHooksWithPDPAndScope(p, src)...))
		var dispatched bool
		registerEcho(t, m, &dispatched)

		d, err := m.Mediate(context.Background(), scopeChainCall())
		if err != nil {
			t.Fatalf("Mediate: %v", err)
		}
		if d.Effect != rm.EffectDeny {
			t.Fatalf("esperava deny (fora de utilizador ∩ classe), obtive %q (%s)", d.Effect, d.Reason)
		}
		if d.DeniedBy != "scope" {
			t.Errorf("DeniedBy=%q, esperava scope (intersecao utilizador ∩ classe)", d.DeniedBy)
		}
		if dispatched {
			t.Error("a tool NAO devia ser despachada")
		}
	})

	t.Run("utilizador_e_classe_concedem_permite", func(t *testing.T) {
		t.Parallel()
		// Agora o utilizador TAMBÉM tem cap:http.post ⇒ intersecção inclui-a.
		src := authz.NewStaticAuthoritySource().
			Set("human:alice", "cap:fs.read", "cap:http.post").
			Set("agent:worker-1", "cap:fs.read", "cap:http.post").
			Set("agent:agent-worker", "cap:fs.read", "cap:http.post")

		m := rm.New(rm.WithHooks(DefaultHooksWithPDPAndScope(p, src)...))
		var dispatched bool
		registerEcho(t, m, &dispatched)

		call := scopeChainCall()
		call.Principal.Authority = []string{"cap:http.post"} // ⊆ efectivo
		d, err := m.Mediate(context.Background(), call)
		if err != nil {
			t.Fatalf("Mediate: %v", err)
		}
		if !d.Permitted() {
			t.Fatalf("esperava permit (utilizador ∩ classe inclui a capability), obtive %q (%s)", d.Effect, d.Reason)
		}
		if !dispatched {
			t.Error("a tool devia ter sido despachada")
		}
	})
}
