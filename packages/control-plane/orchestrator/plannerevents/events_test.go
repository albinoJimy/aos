package plannerevents_test

import (
	"testing"

	pe "github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// TestNewValidationFailedFailsClosedOnUnknownRule — a projecção do evento é
// fail-closed: uma regra fora do enum classificado NÃO produz um payload (não se
// inventa um diagnóstico nem se cai para texto livre). É a metade «fail-closed» da
// fronteira anti-PII: sem código de diagnóstico conhecido, não há evento.
//
// FALSIFICÁVEL: uma implementação que devolvesse um diagnóstico por omissão (ex.:
// "unknown", ou o valor cru da regra) para uma regra desconhecida passaria a
// devolver err==nil e este teste falharia.
func TestNewValidationFailedFailsClosedOnUnknownRule(t *testing.T) {
	t.Parallel()
	_, err := pe.NewValidationFailed(pe.ValidationOutcome{
		PlanID: "p", PlanHash: "h", Rule: pe.Rule("totally_made_up"), Attempt: 1, MaxAttempts: 3,
	})
	if err == nil {
		t.Fatal("NewValidationFailed aceitou uma regra desconhecida — deveria falhar fail-closed")
	}
	if !isErr(err, pe.ErrUnknownRule) {
		t.Fatalf("erro inesperado: %v (esperado ErrUnknownRule)", err)
	}
}

// TestKnownRulesAllClassify — cada regra catalogada mapeia para um diagnóstico
// limitado e content-free (nenhuma regra válida cai no ramo fail-closed).
func TestKnownRulesAllClassify(t *testing.T) {
	t.Parallel()
	rules := []pe.Rule{
		pe.RuleSchema, pe.RuleAcyclicity, pe.RuleToolResolution,
		pe.RuleStructuralCeiling, pe.RuleBudget, pe.RuleRisk,
	}
	for _, r := range rules {
		p, err := pe.NewValidationFailed(pe.ValidationOutcome{
			PlanID: "p", PlanHash: "h", Rule: r, Attempt: 2, MaxAttempts: 3,
			RawDetail: "conteúdo sensível que NÃO pode sair",
		})
		if err != nil {
			t.Fatalf("regra %q: %v", r, err)
		}
		if p.Diagnostic == "" {
			t.Fatalf("regra %q: diagnóstico vazio", r)
		}
		if p.Rule != r {
			t.Fatalf("regra %q: payload.Rule=%q", r, p.Rule)
		}
	}
}
