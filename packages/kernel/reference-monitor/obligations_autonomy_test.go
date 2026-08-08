package referencemonitor

import "testing"

// TestEnforceAutonomy_ModoQueExigeHumanoSemProva: um permit que chega ao PEP com oversight
// exigente e SEM prova de aprovação verificada não liberta o efeito. É a segunda dobra do
// mesmo invariante (o PDP já rebaixaria para escalate) — aqui sobre a prova concreta.
func TestEnforceAutonomy_ModoQueExigeHumanoSemProva(t *testing.T) {
	for _, mode := range []string{"suggest", "confirm", "batch", "modo-que-nao-existe", ""} {
		call := Call{}
		reason, ok := enforceObligations(&call, []Obligation{
			{Type: ObligationAutonomy, Params: map[string]string{paramOversight: mode}},
		})
		if ok {
			t.Fatalf("oversight %q sem prova humana tinha de NEGAR", mode)
		}
		if reason == "" {
			t.Fatalf("oversight %q: a negacao tem de dizer porque", mode)
		}
	}
}

// TestEnforceAutonomy_ComProvaLiberta: com a prova VERIFICADA na call (só o ApprovalGate a
// escreve), o oversight está cumprido e o efeito segue.
func TestEnforceAutonomy_ComProvaLiberta(t *testing.T) {
	call := Call{humanApproved: &ApprovalProof{Approvers: []string{"human:alice", "human:bob"}, DualControl: true}}
	if reason, ok := enforceObligations(&call, []Obligation{
		{Type: ObligationAutonomy, Params: map[string]string{paramOversight: "confirm"}},
	}); !ok {
		t.Fatalf("com aprovacao verificada o oversight esta cumprido; negou: %s", reason)
	}
}

// TestEnforceAutonomy_ModosQueCorremNaoExigemNada: run/sample não param nada — a obligation
// existe para o audit, não como gate.
func TestEnforceAutonomy_ModosQueCorremNaoExigemNada(t *testing.T) {
	for _, mode := range []string{"run", "sample"} {
		call := Call{}
		if reason, ok := enforceObligations(&call, []Obligation{
			{Type: ObligationAutonomy, Params: map[string]string{paramOversight: mode}},
		}); !ok {
			t.Fatalf("oversight %q nao exige humano; negou: %s", mode, reason)
		}
	}
}

// TestEnforceAutonomy_ObligationDesconhecidaContinuaANegar sela que o default fail-closed
// NÃO foi afrouxado ao acrescentar o caso novo.
func TestEnforceAutonomy_ObligationDesconhecidaContinuaANegar(t *testing.T) {
	call := Call{humanApproved: &ApprovalProof{Approvers: []string{"human:alice"}}}
	if _, ok := enforceObligations(&call, []Obligation{{Type: "obrigacao_inventada"}}); ok {
		t.Fatal("uma obligation que o PEP nao sabe cumprir tem de continuar a NEGAR")
	}
}
