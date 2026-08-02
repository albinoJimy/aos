package planmigrate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planmigrate"
	pe "github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// TestRetiredVersionInvalidatesFailClosed — CA: «se a versão foi RETIRADA antes da
// materialização ⇒ invalida → exige re-plano + re-aprovação (fail-closed)».
//
// FALSIFICÁVEL com CONTROLO: o MESMO run, com o MESMO MAJOR dentro da janela,
//   - admite quando a versão NÃO está retirada (controlo: prova que não é a janela a
//     bloquear);
//   - é invalidado com ErrRetired quando a versão exacta é retirada.
//
// Um replay que ignorasse o registo de retiradas (auto-migrasse um plano numa versão
// retirada) devolveria sucesso no segundo caso e este teste falharia.
func TestRetiredVersionInvalidatesFailClosed(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	planID := "plan-retired-1"
	ver := plan.PlanVersion{Major: 1, Minor: 3, Patch: 0}
	doc := buildDoc(ver)

	hash, _ := seedApproval(t, store, planID, doc, nil)
	seedMaterialized(t, store, planID, doc, hash)

	window := planmigrate.SupportWindow{MinMajor: 1, MaxMajor: 1} // MAJOR 1 dentro da janela

	// CONTROLO: sem retirada ⇒ admite.
	ok := planmigrate.NewMigrator(mustPolicy(t, window))
	if _, err := ok.Replay(context.Background(), store, planID, doc); err != nil {
		t.Fatalf("controlo (não-retirado) deveria admitir; erro=%v", err)
	}

	// RETIRADA da versão exacta ⇒ ErrRetired.
	policy := mustPolicy(t, window)
	policy.Retire(ver)
	retired := planmigrate.NewMigrator(policy)
	_, err := retired.Replay(context.Background(), store, planID, doc)
	if !errors.Is(err, planmigrate.ErrRetired) {
		t.Fatalf("erro=%v; esperado ErrRetired (versão retirada exige re-plano+re-aprovação)", err)
	}
}

// TestRunOutsideSupportWindowIsInadmissible — CA: «janela de suporte de MAJORs
// declarada; um run fora da janela é INADMISSÍVEL (como payload perdido, AOS-016)».
//
// FALSIFICÁVEL com CONTROLO: um plano no MAJOR 3
//   - é admitido quando a janela é [1,3] (reader retido);
//   - é INADMISSÍVEL com ErrOutsideSupportWindow quando a janela é [1,2] (reader não
//     retido — deprecação documentada avançou o topo).
//
// O bump de MAJOR (ADR-012) mantém-se replayável só com reader retido (janela que o
// cobre); fora dela é inadmissível — nunca auto-migrado.
func TestRunOutsideSupportWindowIsInadmissible(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	planID := "plan-window-1"
	ver := plan.PlanVersion{Major: 3, Minor: 0, Patch: 0}
	doc := buildDoc(ver)

	hash, _ := seedApproval(t, store, planID, doc, nil)
	seedMaterialized(t, store, planID, doc, hash)

	// CONTROLO: janela cobre MAJOR 3 ⇒ admite (reader retido).
	inWin := planmigrate.NewMigrator(mustPolicy(t, planmigrate.SupportWindow{MinMajor: 1, MaxMajor: 3}))
	if _, err := inWin.Replay(context.Background(), store, planID, doc); err != nil {
		t.Fatalf("controlo (janela [1,3]) deveria admitir; erro=%v", err)
	}

	// FORA da janela [1,2] ⇒ inadmissível.
	outWin := planmigrate.NewMigrator(mustPolicy(t, planmigrate.SupportWindow{MinMajor: 1, MaxMajor: 2}))
	_, err := outWin.Replay(context.Background(), store, planID, doc)
	if !errors.Is(err, planmigrate.ErrOutsideSupportWindow) {
		t.Fatalf("erro=%v; esperado ErrOutsideSupportWindow (MAJOR 3 fora de [1,2])", err)
	}
}

// TestMaterializeRefusesRetiredBeforeTouchingREGorRM — a regra «retirada ANTES da
// materialização ⇒ invalida» aplica-se no gate de ESCRITA, e ANTES de qualquer
// efeito: uma versão retirada é recusada sem sequer resolver o REG ou mediar no RM.
//
// FALSIFICÁVEL: o Materialize recebe REG/RM que contam; após a recusa os contadores
// TÊM de ficar a 0. Um gate colocado DEPOIS da travessia do REG/RM deixaria os
// contadores subir e este teste falharia.
func TestMaterializeRefusesRetiredBeforeTouchingREGorRM(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	planID := "plan-matretired-1"
	ver := plan.PlanVersion{Major: 1, Minor: 0, Patch: 0}
	doc := buildDoc(ver)

	policy := mustPolicy(t, planmigrate.SupportWindow{MinMajor: 1, MaxMajor: 1})
	policy.Retire(ver)

	reg := &countingResolver{}
	rm := &countingMonitor{}
	// Recorder real; o gate fail-closed falha ANTES de o Materialize o usar.
	rec, rErr := pe.NewRecorder(store)
	if rErr != nil {
		t.Fatalf("NewRecorder: %v", rErr)
	}
	mig := planmigrate.NewMigrator(policy,
		planmigrate.WithResolver(reg),
		planmigrate.WithReferenceMonitor(rm),
		planmigrate.WithRecorder(rec),
	)

	_, mErr := mig.Materialize(context.Background(), planID, doc)
	if !errors.Is(mErr, planmigrate.ErrRetired) {
		t.Fatalf("erro=%v; esperado ErrRetired na materialização", mErr)
	}
	if reg.calls.Load() != 0 || rm.calls.Load() != 0 {
		t.Fatalf("gate tardio: REG/RM tocados antes da recusa (reg=%d, rm=%d) — esperado 0/0", reg.calls.Load(), rm.calls.Load())
	}
}

// TestNewPolicyRejectsInvalidWindow — construção fail-closed: uma janela incoerente
// (Min > Max) não produz uma Policy.
func TestNewPolicyRejectsInvalidWindow(t *testing.T) {
	t.Parallel()
	_, err := planmigrate.NewPolicy(planmigrate.SupportWindow{MinMajor: 3, MaxMajor: 1})
	if !errors.Is(err, planmigrate.ErrInvalidWindow) {
		t.Fatalf("erro=%v; esperado ErrInvalidWindow", err)
	}
}

// TestAdmitRetirementPrecedesWindow — teste unitário directo do gate: uma versão
// retirada é ErrRetired MESMO com o MAJOR dentro da janela (a retirada precede a
// janela). E uma versão limpa dentro da janela admite.
func TestAdmitRetirementPrecedesWindow(t *testing.T) {
	t.Parallel()
	policy := mustPolicy(t, planmigrate.SupportWindow{MinMajor: 1, MaxMajor: 2})
	retired := plan.PlanVersion{Major: 1, Minor: 2, Patch: 0}
	policy.Retire(retired)

	if err := policy.Admit(retired); !errors.Is(err, planmigrate.ErrRetired) {
		t.Fatalf("Admit(retirada) = %v; esperado ErrRetired (precede a janela)", err)
	}
	clean := plan.PlanVersion{Major: 2, Minor: 0, Patch: 0}
	if err := policy.Admit(clean); err != nil {
		t.Fatalf("Admit(limpa dentro da janela) = %v; esperado nil", err)
	}
}
