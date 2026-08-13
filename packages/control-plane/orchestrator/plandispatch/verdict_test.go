package plandispatch

import (
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// verdict_test.go — A PONTA ATADA: o veredicto EMITIDO por um verificador (AOS-271)
// é exactamente o que um ramo de qualidade CONSOME (AOS-270).

// TestVerdictAlphabetIsShared — as três pontas (schema da condição, evento emitido,
// observável consumido) falam o MESMO alfabeto por construção.
//
// FALHA-ANTES: com literais independentes, um `pass` emitido podia não ser o `pass`
// que o predicado compara — e o modo de falha era o silencioso («ausente/diferente ⇒
// não ramifica»), não um erro.
func TestVerdictAlphabetIsShared(t *testing.T) {
	if string(VerdictPass) != string(plan.EnumPass) || string(VerdictFail) != string(plan.EnumFail) {
		t.Fatalf("observável (%q,%q) divergente da gramática (%q,%q)", VerdictPass, VerdictFail, plan.EnumPass, plan.EnumFail)
	}
	if string(VerdictPass) != string(plannerevents.VerdictPass) {
		t.Fatalf("observável %q divergente do símbolo emitido %q", VerdictPass, plannerevents.VerdictPass)
	}
}

// TestResultFromVerdictFeedsQualityBranch — um `plan.verdict_recorded` de `pass` com
// métricas satisfaz o predicado de qualidade que o consome; o mesmo facto com `fail`
// não o satisfaz. É a prova de que a projecção fecha o circuito EMITIR→CONSUMIR.
func TestResultFromVerdictFeedsQualityBranch(t *testing.T) {
	floor := int64(800)
	edges := []plan.ConditionalEdge{{From: "review", When: []plan.Predicate{
		{Subject: plan.SubjectVerdict, Op: plan.OpEq, Enum: plan.EnumPass},
		{Subject: plan.SubjectMetric, Metric: "coverage_permille", Op: plan.OpGte, Number: &floor},
	}}}

	emitted := plannerevents.VerdictRecordedPayload{
		PlanID: "p", NodeID: "review", Subjects: []string{"draft"},
		Outcome: plannerevents.VerdictPass,
		Metrics: []plannerevents.VerdictMetric{{Name: "coverage_permille", Value: 874}},
	}
	consumer := Node{NodeID: "publish", DependsOn: []string{"draft"}, ConditionalOn: edges}
	rec := ResultFromVerdict(emitted)
	lookup := func(string) (NodeResultRecord, bool) { return rec, true }
	if got := evalConditional(consumer, lookup); got != branchTaken {
		t.Fatalf("ramo = %v; queria branchTaken (o veredicto emitido devia satisfazer o predicado)", got)
	}

	emitted.Outcome = plannerevents.VerdictFail
	failRec := ResultFromVerdict(emitted)
	if got := evalConditional(consumer, func(string) (NodeResultRecord, bool) { return failRec, true }); got != branchNotTaken {
		t.Fatalf("ramo = %v; queria branchNotTaken", got)
	}
}

// TestVerdictOnAnotherSubjectDoesNotReleaseTheBranch — A ATRIBUIÇÃO É VERIFICADA NO
// CAMINHO QUENTE. O verificador emite `pass` sobre `outro-trabalho`, e o ramo que
// consome esse veredicto guarda `draft`: o ramo NÃO abre.
//
// FALHA-ANTES (verificada): `ResultFromVerdict` descartava `subjects[]` por completo,
// pelo que um `pass` legítimo sobre o nó X libertava o trabalho do nó Y e nada no
// caminho quente confrontava a correspondência. A disposição é INDECIDA — esperar — e
// não «não tomado»: uma atribuição que não cobre o trabalho é um defeito de emissão, e
// podar por causa dele registaria um facto terminal sobre um erro alheio.
func TestVerdictOnAnotherSubjectDoesNotReleaseTheBranch(t *testing.T) {
	edges := []plan.ConditionalEdge{{From: "review", When: []plan.Predicate{
		{Subject: plan.SubjectVerdict, Op: plan.OpEq, Enum: plan.EnumPass},
	}}}
	consumer := Node{NodeID: "publish", DependsOn: []string{"draft"}, ConditionalOn: edges}

	misattributed := ResultFromVerdict(plannerevents.VerdictRecordedPayload{
		PlanID: "p", NodeID: "review", Subjects: []string{"outro-trabalho"},
		Outcome: plannerevents.VerdictPass,
	})
	if got := evalConditional(consumer, func(string) (NodeResultRecord, bool) { return misattributed, true }); got != branchUndecided {
		t.Fatalf("ramo = %v; queria branchUndecided (o veredicto não tem por sujeito o trabalho que a aresta guarda)", got)
	}

	// Não-vacuidade: com o sujeito CERTO, o mesmo veredicto abre o ramo.
	attributed := ResultFromVerdict(plannerevents.VerdictRecordedPayload{
		PlanID: "p", NodeID: "review", Subjects: []string{"draft"},
		Outcome: plannerevents.VerdictPass,
	})
	if got := evalConditional(consumer, func(string) (NodeResultRecord, bool) { return attributed, true }); got != branchTaken {
		t.Fatalf("ramo = %v; queria branchTaken", got)
	}
}

// TestResultFromVerdictNaoExpoeRazoes — as RAZÕES não atravessam para o observável.
// São diagnóstico para o audit e para o approval-card; se um ramo pudesse condicionar
// sobre elas, a gramática fechada de §2.1 ganhava um quarto observável sem passar
// pelo schema.
func TestResultFromVerdictNaoExpoeRazoes(t *testing.T) {
	rec := ResultFromVerdict(plannerevents.VerdictRecordedPayload{
		PlanID: "p", NodeID: "review", Subjects: []string{"draft"},
		Outcome: plannerevents.VerdictFail, Reasons: []string{"coverage_below_floor"},
		Metrics: []plannerevents.VerdictMetric{{Name: "m", Value: 1}},
	})
	if len(rec.Metrics) != 1 {
		t.Fatalf("métricas = %v; queria exactamente a métrica declarada", rec.Metrics)
	}
	if _, leaked := rec.Metrics["coverage_below_floor"]; leaked {
		t.Fatal("uma RAZÃO apareceu como métrica observável — o veredicto ganhou um canal de ramificação não declarado")
	}
	// O estado terminal NÃO vem do veredicto: é do ciclo de vida (AOS-017). Um
	// verificador não declara o seu próprio estado terminal.
	if rec.Terminal != TerminalUnset {
		t.Fatalf("terminal = %q; o veredicto não é fonte de estado terminal", rec.Terminal)
	}
}
