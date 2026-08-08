package referencemonitor

import "testing"

// TestEnforceAutonomy_ModoQueExigeHumanoSemProva: um permit que chega ao PEP com oversight
// exigente e SEM prova de aprovação verificada não liberta o efeito. É a segunda dobra do
// mesmo invariante (o PDP já rebaixaria para escalate) — aqui sobre a prova concreta.
func TestEnforceAutonomy_ModoQueExigeHumanoSemProva(t *testing.T) {
	for _, mode := range []string{"suggest", "confirm", "batch"} {
		call := Call{}
		reason, ok := enforceObligations(&call, []Obligation{
			{Type: ObligationAutonomy, Params: map[string]string{
				paramOversight: mode, ParamAutonomyRequiresHuman: "true"}},
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
		{Type: ObligationAutonomy, Params: map[string]string{
			paramOversight: "confirm", ParamAutonomyRequiresHuman: "true"}},
	}); !ok {
		t.Fatalf("com aprovacao verificada o oversight esta cumprido; negou: %s", reason)
	}
}

// TestEnforceAutonomy_ModosQueCorremNaoExigemNada: run/sample não param nada — a obligation
// existe para o audit, não como gate.
func TestEnforceAutonomy_ModosQueCorremNaoExigemNada(t *testing.T) {
	// `post_hoc_sample` é o modo que EXPÔS o defeito: o PEP reinterpretava o NOME e uma
	// segunda tabela divergiu da taxonomia, negando acções que correm.
	for _, mode := range []string{"run", "post_hoc_sample"} {
		call := Call{}
		if reason, ok := enforceObligations(&call, []Obligation{
			{Type: ObligationAutonomy, Params: map[string]string{
				paramOversight: mode, ParamAutonomyRequiresHuman: "false"}},
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

// TestEnforceAutonomy_SemVeredictoEhFailClosed: o PEP não adivinha a partir do nome do
// modo — foi essa adivinhação que criou a divergência. Uma obligation de autonomia sem o
// veredicto utilizável nega, mesmo com aprovação humana presente.
func TestEnforceAutonomy_SemVeredictoEhFailClosed(t *testing.T) {
	for _, params := range []map[string]string{
		{paramOversight: "confirm"},                                       // veredicto AUSENTE
		{paramOversight: "confirm", ParamAutonomyRequiresHuman: "talvez"}, // veredicto ilegível
	} {
		call := Call{humanApproved: &ApprovalProof{Approvers: []string{"human:alice"}}}
		if _, ok := enforceObligations(&call, []Obligation{{Type: ObligationAutonomy, Params: params}}); ok {
			t.Fatalf("sem veredicto utilizavel o PEP tem de NEGAR; params=%v", params)
		}
	}
}
