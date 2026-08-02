package plannerprompt

import (
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// twoCaseSet monta um golden-set com um caso HARD ("hard-egress") e um caso normal
// ("easy"), versão base 1.0.0.
func twoCaseSet() GoldenSet {
	snap := testSnapshot()
	ceil := testCeilings()
	mk := func(id string, hard bool) Case {
		return Case{
			ID:         id,
			Objective:  "obj " + id,
			Hard:       hard,
			Assertions: []Assertion{Accepts(id+"-accepts", Security, snap, ceil)},
		}
	}
	return GoldenSet{
		Version: PromptVersion{Major: 1},
		Owner:   "team-planner",
		Cases:   []Case{mk("hard-egress", true), mk("easy", false)},
	}
}

// TestGoldenMutation_HardCaseRemovalRequiresApproval — CA REQUERIDO. Remover um caso
// DIFÍCIL do golden-set exige aprovação explícita (anti-envenenamento).
//
// FALHA-ANTES: sem o gate, a remoção de "hard-egress" passaria em silêncio, cegando o
// eval. Com o gate e SEM aprovação ⇒ ErrHardCaseRemoval; COM aprovação explícita ⇒ ok.
func TestGoldenMutation_HardCaseRemovalRequiresApproval(t *testing.T) {
	old := twoCaseSet()

	// new remove o caso HARD "hard-egress" (fica só "easy"). Versão sobe (governanca).
	newSet := twoCaseSet()
	newSet.Version = PromptVersion{Major: 1, Minor: 1}
	newSet.Cases = []Case{newSet.Cases[1]} // mantem so "easy"

	// Sem aprovacao ⇒ BLOQUEIA.
	if err := ValidateGoldenMutation(old, newSet, RemovalApproval{}); !errors.Is(err, ErrHardCaseRemoval) {
		t.Fatalf("remocao de caso dificil sem aprovacao devia bloquear com ErrHardCaseRemoval, obtive %v", err)
	}

	// Aprovacao de OUTRO caso ⇒ ainda bloqueia (nao cobre "hard-egress").
	wrongAp := RemovalApproval{Approver: "owner", CaseIDs: map[string]bool{"easy": true}}
	if err := ValidateGoldenMutation(old, newSet, wrongAp); !errors.Is(err, ErrHardCaseRemoval) {
		t.Fatalf("aprovacao do caso errado devia bloquear, obtive %v", err)
	}

	// Aprovacao sem aprovador (só id) ⇒ bloqueia (fail-closed no aprovador).
	noApprover := RemovalApproval{CaseIDs: map[string]bool{"hard-egress": true}}
	if err := ValidateGoldenMutation(old, newSet, noApprover); !errors.Is(err, ErrHardCaseRemoval) {
		t.Fatalf("aprovacao sem aprovador devia bloquear, obtive %v", err)
	}

	// Aprovacao explicita e completa ⇒ admite.
	ok := RemovalApproval{Approver: "owner", CaseIDs: map[string]bool{"hard-egress": true}}
	if err := ValidateGoldenMutation(old, newSet, ok); err != nil {
		t.Fatalf("remocao aprovada explicitamente devia passar, obtive %v", err)
	}
}

// TestGoldenMutation_HardCaseGuttingRequiresApproval — CA5/ACHADO-1. Manter o caso
// DIFÍCIL (mesmo id, mesmo nº de casos) mas ESVAZIAR a sua asserção — trocar a
// invariante de SEGURANÇA (validador aceita) por uma rubrica de QUALIDADE
// sempre-verdadeira — cega o caso sem o remover. Isto tem de ser gated como a remoção.
//
// FALHA-ANTES: o gate antigo só comparava ao nível do ID (goldenset.go:113); como o id
// e a contagem não mudam, changed ficava false e ValidateGoldenMutation devolvia nil —
// sem bump nem aprovação. A asserção neutralizada deixaria passar um candidato com
// regressão de segurança em Evaluate. Com o fix, a mudança de assinatura de asserções
// de um caso HARD retido exige aprovação explícita (ErrHardCaseGutted).
func TestGoldenMutation_HardCaseGuttingRequiresApproval(t *testing.T) {
	old := twoCaseSet() // "hard-egress" HARD com asserção de SEGURANÇA (validador aceita)

	// Prova da não-vacuidade do vector: a assinatura MUDA ao trocar Security→Quality.
	gutted := twoCaseSet()
	gutted.Cases[0].Assertions = []Assertion{
		Rubric("hard-egress-accepts", Quality, func(plan.PlanDocument) bool { return true }),
	}
	if old.Cases[0].assertionSignature() == gutted.Cases[0].assertionSignature() {
		t.Fatal("pre-condicao: o gutting Security->Quality devia MUDAR a assinatura de assercoes")
	}
	// O nº de casos e os ids são idênticos — a deteccao antiga (por id) nao veria mudança.
	if len(old.Cases) != len(gutted.Cases) {
		t.Fatal("pre-condicao: o gutting mantem o mesmo numero de casos")
	}

	// Sem aprovacao ⇒ BLOQUEIA (mesmo com bump de versao, para isolar do gate de versao).
	gutted.Version = PromptVersion{Major: 1, Minor: 1}
	if err := ValidateGoldenMutation(old, gutted, RemovalApproval{}); !errors.Is(err, ErrHardCaseGutted) {
		t.Fatalf("gutting de caso dificil sem aprovacao devia dar ErrHardCaseGutted, obtive %v", err)
	}

	// Aprovacao do caso ERRADO ⇒ ainda bloqueia.
	wrongAp := RemovalApproval{Approver: "owner", CaseIDs: map[string]bool{"easy": true}}
	if err := ValidateGoldenMutation(old, gutted, wrongAp); !errors.Is(err, ErrHardCaseGutted) {
		t.Fatalf("aprovacao do caso errado devia bloquear o gutting, obtive %v", err)
	}

	// Aprovacao sem aprovador ⇒ bloqueia (fail-closed no aprovador).
	noApprover := RemovalApproval{CaseIDs: map[string]bool{"hard-egress": true}}
	if err := ValidateGoldenMutation(old, gutted, noApprover); !errors.Is(err, ErrHardCaseGutted) {
		t.Fatalf("aprovacao sem aprovador devia bloquear o gutting, obtive %v", err)
	}

	// Aprovacao explicita e completa ⇒ admite.
	ok := RemovalApproval{Approver: "owner", CaseIDs: map[string]bool{"hard-egress": true}}
	if err := ValidateGoldenMutation(old, gutted, ok); err != nil {
		t.Fatalf("gutting aprovado explicitamente devia passar, obtive %v", err)
	}
}

// TestGoldenMutation_HardCaseGuttingCountsAsChange — o gutting de um caso HARD retido
// conta como mudança para efeitos de bump: aprovado mas SEM bump ⇒ ErrGoldenNotBumped.
//
// FALHA-ANTES: antes do fix, changed permanecia false (id/contagem iguais), logo o gate
// de versao nunca disparava sobre um gutting.
func TestGoldenMutation_HardCaseGuttingCountsAsChange(t *testing.T) {
	old := twoCaseSet()
	gutted := twoCaseSet() // versao NAO sobe (fica 1.0.0)
	gutted.Cases[0].Assertions = []Assertion{
		Rubric("hard-egress-accepts", Quality, func(plan.PlanDocument) bool { return true }),
	}
	ap := RemovalApproval{Approver: "owner", CaseIDs: map[string]bool{"hard-egress": true}}
	if err := ValidateGoldenMutation(old, gutted, ap); !errors.Is(err, ErrGoldenNotBumped) {
		t.Fatalf("gutting aprovado mas sem bump devia dar ErrGoldenNotBumped, obtive %v", err)
	}
}

// TestGoldenMutation_HardCaseAssertionsUnchangedFree — controlo negativo: reter um caso
// HARD com a MESMA assinatura de asserções (nem gutting nem bump) é livre — o gate não
// dispara falsos positivos sobre uma mutação que não toca a cobertura difícil.
func TestGoldenMutation_HardCaseAssertionsUnchangedFree(t *testing.T) {
	old := twoCaseSet()
	same := twoCaseSet() // asserções identicas, mesma versao
	if err := ValidateGoldenMutation(old, same, RemovalApproval{}); err != nil {
		t.Fatalf("mutacao no-op (asserções difíceis inalteradas) nao devia disparar, obtive %v", err)
	}
}

// TestGoldenMutation_NonHardRemovalFree — remover um caso NÃO difícil não exige
// aprovação (a proteção é dirigida à cobertura difícil).
func TestGoldenMutation_NonHardRemovalFree(t *testing.T) {
	old := twoCaseSet()
	newSet := twoCaseSet()
	newSet.Version = PromptVersion{Major: 1, Minor: 1}
	newSet.Cases = []Case{newSet.Cases[0]} // mantem so "hard-egress", remove "easy"

	if err := ValidateGoldenMutation(old, newSet, RemovalApproval{}); err != nil {
		t.Fatalf("remocao de caso nao-dificil nao devia exigir aprovacao, obtive %v", err)
	}
}

// TestGoldenMutation_ChangeRequiresVersionBump — mudar o conjunto de casos sem subir a
// versão é recusado (governanca do golden-set versionado).
func TestGoldenMutation_ChangeRequiresVersionBump(t *testing.T) {
	old := twoCaseSet()
	newSet := twoCaseSet()
	// remove o caso nao-dificil mas NAO sobe a versao.
	newSet.Cases = []Case{newSet.Cases[0]}
	if err := ValidateGoldenMutation(old, newSet, RemovalApproval{}); !errors.Is(err, ErrGoldenNotBumped) {
		t.Fatalf("mudanca sem bump devia dar ErrGoldenNotBumped, obtive %v", err)
	}
}

// TestGoldenMutation_RejectsNoOwner — new sem dono é recusado (DoD).
func TestGoldenMutation_RejectsNoOwner(t *testing.T) {
	old := twoCaseSet()
	newSet := twoCaseSet()
	newSet.Owner = ""
	if err := ValidateGoldenMutation(old, newSet, RemovalApproval{}); !errors.Is(err, ErrNoOwner) {
		t.Fatalf("golden-set sem dono devia dar ErrNoOwner, obtive %v", err)
	}
}

// TestGoldenSet_ValidateRejectsEmptyCaseAndDupID — forma fail-closed do golden-set.
func TestGoldenSet_ValidateRejectsEmptyCaseAndDupID(t *testing.T) {
	snap := testSnapshot()
	ceil := testCeilings()

	empty := GoldenSet{Version: PromptVersion{Major: 1}, Owner: "o", Cases: []Case{{ID: "c1"}}}
	if err := empty.validate(); !errors.Is(err, ErrEmptyCase) {
		t.Fatalf("caso sem assercoes devia dar ErrEmptyCase, obtive %v", err)
	}

	dup := GoldenSet{Version: PromptVersion{Major: 1}, Owner: "o", Cases: []Case{
		{ID: "c1", Assertions: []Assertion{Accepts("a", Security, snap, ceil)}},
		{ID: "c1", Assertions: []Assertion{Accepts("b", Security, snap, ceil)}},
	}}
	if err := dup.validate(); !errors.Is(err, ErrDuplicateCaseID) {
		t.Fatalf("case_id duplicado devia dar ErrDuplicateCaseID, obtive %v", err)
	}
}
