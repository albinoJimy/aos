package planmigrate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planmigrate"
	pe "github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

var v100 = plan.PlanVersion{Major: 1, Minor: 0, Patch: 0}

// TestReplayIsDeterministic_NoREG_NoRM_NoLLM — CA central: «replay reproduz os
// eventos capturados; NUNCA re-resolve o REG nem re-atravessa o RM».
//
// FALSIFICÁVEL por TRÊS sondas independentes injectadas no Migrator:
//   - failResolver (REG): se o replay resolver qualquer tool, dispara t.Errorf e o
//     contador sobe. Asserção: contador == 0.
//   - failMonitor (RM): idem para a mediação por nó.
//   - countingProposer (LLM): consultado 1× na captura; se o replay re-derivasse
//     `plan.proposed` via modelo, subiria. Asserção: fica em 1.
//
// Uma via de replay que re-materializasse "do zero" (re-resolvendo tools ou
// re-mediando) em vez de LER a materialização capturada falharia todas as três.
func TestReplayIsDeterministic_NoREG_NoRM_NoLLM(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	planID := "plan-det-1"
	doc := buildDoc(v100)

	hash, proposer := seedApproval(t, store, planID, doc, nil)
	seedMaterialized(t, store, planID, doc, hash)

	if got := proposer.calls.Load(); got != 1 {
		t.Fatalf("proposer consultado %d vezes na captura; esperado exactamente 1", got)
	}

	reg := &failResolver{t: t}
	rm := &failMonitor{t: t}
	mig := planmigrate.NewMigrator(
		mustPolicy(t, planmigrate.SupportWindow{MinMajor: 1, MaxMajor: 1}),
		planmigrate.WithResolver(reg),
		planmigrate.WithReferenceMonitor(rm),
	)

	rp, err := mig.Replay(context.Background(), store, planID, doc)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}

	// PUREZA — as três sondas.
	if got := reg.calls.Load(); got != 0 {
		t.Fatalf("REG chamado %d vez(es) no replay — deve ser 0 (replay não re-resolve o REG)", got)
	}
	if got := rm.calls.Load(); got != 0 {
		t.Fatalf("RM chamado %d vez(es) no replay — deve ser 0 (replay não re-atravessa o RM)", got)
	}
	if got := proposer.calls.Load(); got != 1 {
		t.Fatalf("LLM re-chamado no replay (calls=%d) — deve ficar em 1 (captura), nunca re-derivar", got)
	}

	// NÃO-VACUIDADE do sucesso: os três eixos vêm pinados e a materialização
	// reproduzida é a CAPTURADA (não uma re-derivação vazia).
	if rp.Manifest.PlanVersion != doc.PlanVersion {
		t.Fatalf("manifest.PlanVersion=%s, esperado %s", rp.Manifest.PlanVersion, doc.PlanVersion)
	}
	if rp.Manifest.PromptVersion != "prompt-v3" {
		t.Fatalf("manifest.PromptVersion=%q, esperado %q", rp.Manifest.PromptVersion, "prompt-v3")
	}
	if rp.Manifest.CapabilitiesHash != "sha256:caps-7" {
		t.Fatalf("manifest.CapabilitiesHash=%q, esperado %q", rp.Manifest.CapabilitiesHash, "sha256:caps-7")
	}
	if rp.Manifest.PlanHash != hash {
		t.Fatalf("manifest.PlanHash=%q, esperado %q", rp.Manifest.PlanHash, hash)
	}
	if len(rp.Materialized.Nodes) != len(doc.Nodes) {
		t.Fatalf("materialização reproduzida com %d nós, esperado %d", len(rp.Materialized.Nodes), len(doc.Nodes))
	}
	if len(rp.Events) == 0 {
		t.Fatal("replay devolveu 0 eventos reconstruídos — captura vazia?")
	}
}

// TestMaterialize_TraversesREGandRM_ReplayDoesNot — prova a ASSIMETRIA que sustenta
// a não-vacuidade do teste anterior: a via de ESCRITA (Materialize) DE FACTO
// atravessa o REG (uma vez por tool = 3) e o RM (uma vez por nó = 2); a via de
// REPLAY sobre a mesma captura NÃO lhes toca. Falsificável em ambos os sentidos: se
// Materialize não os chamasse, os contadores ficariam a 0 aqui; se o replay os
// chamasse, o failResolver/failMonitor disparavam.
func TestMaterialize_TraversesREGandRM_ReplayDoesNot(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	planID := "plan-mat-1"
	doc := buildDoc(v100)

	hash, _ := seedApproval(t, store, planID, doc, nil)

	reg := &countingResolver{}
	rm := &countingMonitor{}
	rec, err := pe.NewRecorder(store)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	policy := mustPolicy(t, planmigrate.SupportWindow{MinMajor: 1, MaxMajor: 1})
	writer := planmigrate.NewMigrator(policy,
		planmigrate.WithResolver(reg),
		planmigrate.WithReferenceMonitor(rm),
		planmigrate.WithRecorder(rec),
	)

	payload, err := writer.Materialize(context.Background(), planID, doc)
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if got := reg.calls.Load(); got != 3 {
		t.Fatalf("REG.Resolve chamado %d vezes na materialização; esperado 3 (uma por tool)", got)
	}
	if got := rm.calls.Load(); got != 2 {
		t.Fatalf("RM.Mediate chamado %d vezes na materialização; esperado 2 (uma por nó)", got)
	}
	if payload.PlanHash != hash {
		t.Fatalf("materialização com hash %q, esperado %q", payload.PlanHash, hash)
	}

	// Replay da MESMA captura com sondas envenenadas: 0 chamadas.
	reg2 := &failResolver{t: t}
	rm2 := &failMonitor{t: t}
	replayer := planmigrate.NewMigrator(policy,
		planmigrate.WithResolver(reg2),
		planmigrate.WithReferenceMonitor(rm2),
	)
	if _, err := replayer.Replay(context.Background(), store, planID, doc); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if reg2.calls.Load() != 0 || rm2.calls.Load() != 0 {
		t.Fatalf("replay tocou no REG/RM (reg=%d, rm=%d) — deve ser 0/0", reg2.calls.Load(), rm2.calls.Load())
	}
}

// TestReplayFailsClosedWithoutApproval — sem `plan.approved` na captura não há plano
// congelado; o replay aborta fail-closed em vez de inventar um manifesto.
func TestReplayFailsClosedWithoutApproval(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	planID := "plan-noapprove-1"
	doc := buildDoc(v100)

	// Só `plan.proposed` — sem validated/approved/materialized.
	hash, err := planmigrate.HashPlan(doc)
	if err != nil {
		t.Fatalf("HashPlan: %v", err)
	}
	rec, _ := pe.NewRecorder(store)
	proposer := &countingProposer{hash: hash, meta: pe.PlannerMeta{
		Model: doc.PlannerMeta.Model, PromptVersion: doc.PlannerMeta.PromptVersion, CapabilitiesHash: doc.PlannerMeta.CapabilitiesHash,
	}}
	if _, err := rec.RecordProposedFrom(context.Background(), planID, 1, proposer); err != nil {
		t.Fatalf("seed proposed: %v", err)
	}

	mig := planmigrate.NewMigrator(mustPolicy(t, planmigrate.SupportWindow{MinMajor: 1, MaxMajor: 1}))
	_, err = mig.Replay(context.Background(), store, planID, doc)
	if !errors.Is(err, planmigrate.ErrNotApproved) {
		t.Fatalf("erro=%v; esperado ErrNotApproved", err)
	}
}

// TestReplayFailsClosedWithoutMaterialized — aprovado mas ainda não materializado:
// não há sequência a reproduzir. Fail-closed com ErrNotMaterialized.
func TestReplayFailsClosedWithoutMaterialized(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	planID := "plan-nomat-1"
	doc := buildDoc(v100)

	seedApproval(t, store, planID, doc, nil) // sem seedMaterialized

	mig := planmigrate.NewMigrator(mustPolicy(t, planmigrate.SupportWindow{MinMajor: 1, MaxMajor: 1}))
	_, err := mig.Replay(context.Background(), store, planID, doc)
	if !errors.Is(err, planmigrate.ErrNotMaterialized) {
		t.Fatalf("erro=%v; esperado ErrNotMaterialized", err)
	}
}

// TestReaderMismatchFailsClosed — o READER (documento) tem de corresponder à
// CAPTURA. Um documento cujo hash difere do hash aprovado (aqui: objectivo alterado)
// invalida o replay: um plano congelado só é replayável com o SEU reader.
//
// FALSIFICÁVEL: um replay que ignorasse o binding (reconstruísse o manifesto sem
// verificar HashPlan(doc) == hash aprovado) aceitaria o documento errado e este
// teste falharia.
func TestReaderMismatchFailsClosed(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	planID := "plan-mismatch-1"
	approved := buildDoc(v100)

	hash, _ := seedApproval(t, store, planID, approved, nil)
	seedMaterialized(t, store, planID, approved, hash)

	// Reader adulterado: mesmo plan_version/meta, mas objectivo diferente ⇒ hash
	// diferente ⇒ não é o documento aprovado.
	tampered := buildDoc(v100)
	tampered.Objective = "objectivo ADULTERADO"

	mig := planmigrate.NewMigrator(mustPolicy(t, planmigrate.SupportWindow{MinMajor: 1, MaxMajor: 1}))
	_, err := mig.Replay(context.Background(), store, planID, tampered)
	if !errors.Is(err, planmigrate.ErrReaderMismatch) {
		t.Fatalf("erro=%v; esperado ErrReaderMismatch (reader adulterado)", err)
	}
}

// TestCaptureMetaDisagreesWithReader — o binding não é só pelo hash: se a captura
// gravou um `plan.proposed` cujo planner_meta DIVERGE do planner_meta do documento
// aprovado (mesmo com o hash do doc a bater no `plan.approved`), o run é
// incoerente e o replay recusa-o com ErrReaderMismatch.
//
// FALSIFICÁVEL: constrói uma captura onde o hash aprovado == HashPlan(doc) MAS o
// meta proposto tem PromptVersion "prompt-DIVERGENTE". Um replay que confiasse só no
// hash (sem cruzar o meta captura↔reader) aceitaria e este teste falharia.
func TestCaptureMetaDisagreesWithReader(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	planID := "plan-metadiverge-1"
	doc := buildDoc(v100)

	badMeta := pe.PlannerMeta{
		Model:            doc.PlannerMeta.Model,
		PromptVersion:    "prompt-DIVERGENTE",
		CapabilitiesHash: doc.PlannerMeta.CapabilitiesHash,
	}
	hash, _ := seedApproval(t, store, planID, doc, &badMeta)
	seedMaterialized(t, store, planID, doc, hash)

	mig := planmigrate.NewMigrator(mustPolicy(t, planmigrate.SupportWindow{MinMajor: 1, MaxMajor: 1}))
	_, err := mig.Replay(context.Background(), store, planID, doc)
	if !errors.Is(err, planmigrate.ErrReaderMismatch) {
		t.Fatalf("erro=%v; esperado ErrReaderMismatch (meta captura≠reader)", err)
	}
}
