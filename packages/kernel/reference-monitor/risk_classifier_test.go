package referencemonitor

import (
	"context"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// TestClassificadorAnotaAsTresClasses — a razão de ser do hook: pôr a classe no contexto ANTES de
// a política correr, para que o oráculo de autonomia a veja.
func TestClassificadorAnotaAsTresClasses(t *testing.T) {
	c := NewRiskClassifier(nil)
	casos := []struct {
		nome  string
		call  *Call
		rev   string
		sens  string
		taint string
		quero risk.Class
	}{
		// O caso que interessa em produção: tool call originada pelo MODELO, logo untrusted.
		{"leitura local reversivel, untrusted", &Call{Capability: "cap:fs.read", Resource: Resource{Type: "file", Value: "doc://x"}}, "reversible", "", "untrusted", risk.ClassGray},
		// MESMA acção, sensibilidade declarada interna — CONTINUA gray. O taint untrusted ELEVA a
		// sensibilidade (effectiveSensitivity), pelo que `safe` é INALCANÇÁVEL para tudo o que o
		// modelo origine. Escrevi este caso à espera de `safe`; o mecanismo corrigiu-me, e é o
		// que fundamenta escolher a L4 e não a L3: em L3 o gray AGRUPA (continua a ser portão).
		{"a mesma, com sensibilidade interna", &Call{Capability: "cap:fs.read", Resource: Resource{Type: "file", Value: "doc://x"}}, "reversible", "internal", "untrusted", risk.ClassGray},
		// Só uma autorização de proveniência TRUSTED (control-plane) alcança safe.
		{"a mesma, mas trusted", &Call{Capability: "cap:fs.read", Resource: Resource{Type: "file", Value: "doc://x"}}, "reversible", "internal", "trusted", risk.ClassSafe},
		{"egress externo de sensiveis", &Call{Capability: "cap:http.post", Resource: Resource{Type: "url", Value: "https://x.test/y"}}, "reversible", "confidential", "untrusted", risk.ClassDanger},
		{"sem declaracao nenhuma", &Call{Capability: "cap:fs.read", Resource: Resource{Type: "file", Value: "doc://x"}}, "", "", "untrusted", risk.ClassDanger},
	}
	for _, k := range casos {
		k.call.Context.Reversibility = k.rev
		k.call.Context.Sensitivity = k.sens
		k.call.Context.Taint = k.taint
		res, err := c.Evaluate(context.Background(), k.call)
		if err != nil {
			t.Fatalf("%s: %v", k.nome, err)
		}
		if res.Decision != HookAllow {
			t.Errorf("%s: decidiu %v — este hook NUNCA decide", k.nome, res.Decision)
		}
		if got := k.call.Context.RiskClass; got != k.quero.String() {
			t.Errorf("%s: classe = %q, quero %q", k.nome, got, k.quero.String())
		}
	}
}

// TestClassificadorNuncaDecide é o teste que protege tudo o resto.
//
// Este hook foi inserido ANTES da política numa cadeia que faz CURTO-CIRCUITO no primeiro
// não-permit. Se alguma vez devolver deny ou escalate, passa a decidir sobre acções que a
// política ainda não viu — e a precedência que a cadeia garante (deny da política ganha) fica
// invertida, silenciosamente, para TODA a mediação do nó.
//
// Por isso o caso mais importante é a forma que o [RiskGate] NEGA: danger com egress externo e
// SEM destino resolvido (SAROC-04). O classificador tem de a deixar passar — a imposição é do
// gate, e o gate corre onde sempre correu.
func TestClassificadorNuncaDecide(t *testing.T) {
	c := NewRiskClassifier(nil)

	// A forma que o RiskGate nega fail-closed.
	saroc04 := &Call{Capability: "cap:http.post", Resource: Resource{Type: "url", Value: ""}}
	saroc04.Context.Sensitivity = "confidential"
	res, err := c.Evaluate(context.Background(), saroc04)
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if res.Decision != HookAllow {
		t.Fatalf("SAROC-04 (danger + egress + destino vazio) foi %v pelo CLASSIFICADOR — "+
			"este hook corre ANTES da política numa cadeia de curto-circuito; decidir aqui "+
			"inverte a precedência do deny para todo o nó", res.Decision)
	}
	// CONTROLO: o RiskGate, esse, TEM de continuar a negar a mesma forma. Se isto passasse a
	// permitir, a imposição teria sido perdida na mudança em vez de deslocada.
	g := RiskGate{}
	mesma := &Call{Capability: "cap:http.post", Resource: Resource{Type: "url", Value: ""}}
	mesma.Context.Sensitivity = "confidential"
	if r, _ := g.Evaluate(context.Background(), mesma); r.Decision != HookDeny {
		t.Fatalf("o RiskGate deixou de negar SAROC-04 (%v) — a imposição desapareceu", r.Decision)
	}

	// E nenhuma entrada, por estranha que seja, o faz decidir.
	for _, call := range []*Call{
		{},
		{Capability: ""},
		{Capability: "cap:fs.delete", Resource: Resource{Type: "file", Value: "x"}},
		{Capability: "cap:http.post", Resource: Resource{Type: "url", Value: "https://a.test"}},
	} {
		if r, err := c.Evaluate(context.Background(), call); err != nil || r.Decision != HookAllow {
			t.Errorf("capability %q: decisao=%v err=%v — quero sempre HookAllow", call.Capability, r.Decision, err)
		}
	}
}

// TestClassificadorEIdempotenteFaceAoGate — o RiskGate recalcula a classificação a jusante. Se os
// dois discordassem, o audit selaria uma classe e a política teria decidido por outra, e ninguém
// conseguiria reconciliar o registo com a decisão.
func TestClassificadorEIdempotenteFaceAoGate(t *testing.T) {
	c := NewRiskClassifier(nil)
	g := RiskGate{}
	for _, cap := range []string{"cap:fs.read", "cap:http.post", "cap:fs.delete"} {
		a := &Call{Capability: cap, Resource: Resource{Type: "file", Value: "x"}}
		a.Context.Reversibility = "reversible"
		b := &Call{Capability: cap, Resource: Resource{Type: "file", Value: "x"}}
		b.Context.Reversibility = "reversible"

		_, _ = c.Evaluate(context.Background(), a)
		_, _ = g.Evaluate(context.Background(), b)
		if a.Context.RiskClass != b.Context.RiskClass {
			t.Errorf("%s: classificador=%q gate=%q — divergem, e o audit ficaria a mentir sobre a decisão",
				cap, a.Context.RiskClass, b.Context.RiskClass)
		}
	}
}
