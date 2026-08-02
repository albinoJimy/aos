package planmigrate_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planmigrate"
)

// TestManifestPinsThreeDistinctAxes — DETALHE do ticket: plan_version (schema) ≠
// prompt_version (comportamento) ≠ capabilities_hash (ambiente). Os três ficam
// EXPLÍCITOS e SEPARADOS no manifesto e no seu wire JSON.
//
// FALSIFICÁVEL: os três valores são deliberadamente distintos entre si; o teste
// exige que cada campo do manifesto carregue o SEU valor (não haja conflação) e que
// o JSON tenha três chaves separadas. Um manifesto que colapsasse dois eixos num só
// campo falharia.
func TestManifestPinsThreeDistinctAxes(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	planID := "plan-axes-1"
	doc := buildDoc(plan.PlanVersion{Major: 1, Minor: 2, Patch: 3})

	hash, _ := seedApproval(t, store, planID, doc, nil)
	seedMaterialized(t, store, planID, doc, hash)

	mig := planmigrate.NewMigrator(mustPolicy(t, planmigrate.SupportWindow{MinMajor: 1, MaxMajor: 1}))
	rp, err := mig.Replay(context.Background(), store, planID, doc)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	m := rp.Manifest

	// Eixo 1: SCHEMA — vem do reader congelado.
	if m.PlanVersion.String() != "1.2.3" {
		t.Fatalf("plan_version=%s, esperado 1.2.3 (eixo schema)", m.PlanVersion)
	}
	// Eixo 2: COMPORTAMENTO — vem da captura (plan.proposed).
	if m.PromptVersion != "prompt-v3" {
		t.Fatalf("prompt_version=%q, esperado prompt-v3 (eixo comportamento)", m.PromptVersion)
	}
	// Eixo 3: AMBIENTE — vem da captura (plan.proposed).
	if m.CapabilitiesHash != "sha256:caps-7" {
		t.Fatalf("capabilities_hash=%q, esperado sha256:caps-7 (eixo ambiente)", m.CapabilitiesHash)
	}
	// Os três são valores distintos — sem conflação acidental.
	if m.PlanVersion.String() == m.PromptVersion || m.PromptVersion == m.CapabilitiesHash || m.PlanVersion.String() == m.CapabilitiesHash {
		t.Fatalf("eixos conflacionados: version=%s prompt=%q caps=%q", m.PlanVersion, m.PromptVersion, m.CapabilitiesHash)
	}

	// Persistência/round-trip: o manifesto serializa com TRÊS chaves separadas e
	// desserializa igual (o manifesto do run inclui-o).
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifesto: %v", err)
	}
	for _, key := range []string{`"plan_version":"1.2.3"`, `"prompt_version":"prompt-v3"`, `"capabilities_hash":"sha256:caps-7"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("wire do manifesto sem %s:\n%s", key, raw)
		}
	}
	var back planmigrate.Manifest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal manifesto: %v", err)
	}
	if back != m {
		t.Fatalf("round-trip divergente:\n got=%+v\nwant=%+v", back, m)
	}
}

// TestFrozenVersionNeverAutoMigrated — CA: «planos aprovados CONGELADOS na versão;
// nunca auto-migrados». O manifesto reflecte a versão do READER, não a linha
// corrente do módulo.
//
// FALSIFICÁVEL: o plano é aprovado num MAJOR (2) diferente de
// [plan.CurrentPlanVersion] (1.0.0). O manifesto reconstruído TEM de manter 2.4.1 —
// se o replay "actualizasse" o plano para a versão corrente (auto-migração), o
// manifesto viria 1.0.0 e este teste falharia.
func TestFrozenVersionNeverAutoMigrated(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	planID := "plan-frozen-1"
	frozen := plan.PlanVersion{Major: 2, Minor: 4, Patch: 1}
	doc := buildDoc(frozen)

	// Sanidade: a versão congelada NÃO é a corrente (senão o teste seria vacuoso).
	if frozen == plan.CurrentPlanVersion {
		t.Fatalf("fixture inválida: versão congelada == CurrentPlanVersion (%s)", frozen)
	}

	hash, _ := seedApproval(t, store, planID, doc, nil)
	seedMaterialized(t, store, planID, doc, hash)

	// Janela cobre o MAJOR 2 (reader retido), pelo que o run é admissível e
	// replayável — mas sempre na SUA versão.
	mig := planmigrate.NewMigrator(mustPolicy(t, planmigrate.SupportWindow{MinMajor: 1, MaxMajor: 2}))
	rp, err := mig.Replay(context.Background(), store, planID, doc)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if rp.Manifest.PlanVersion != frozen {
		t.Fatalf("manifest.PlanVersion=%s, esperado %s (congelado, nunca auto-migrado)", rp.Manifest.PlanVersion, frozen)
	}
	if rp.Manifest.PlanVersion == plan.CurrentPlanVersion {
		t.Fatalf("auto-migração detectada: manifesto assumiu a versão corrente %s", plan.CurrentPlanVersion)
	}
}

// TestHashPlanIsDeterministic — o binding reader↔captura assenta num hash estável: o
// mesmo documento produz sempre o mesmo hash; um documento diferente, outro.
func TestHashPlanIsDeterministic(t *testing.T) {
	t.Parallel()
	doc := buildDoc(v100)
	h1, err := planmigrate.HashPlan(doc)
	if err != nil {
		t.Fatalf("HashPlan: %v", err)
	}
	h2, err := planmigrate.HashPlan(buildDoc(v100))
	if err != nil {
		t.Fatalf("HashPlan: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("HashPlan não-determinístico: %s != %s", h1, h2)
	}
	if !strings.HasPrefix(h1, "sha256:") {
		t.Fatalf("hash sem prefixo sha256: %s", h1)
	}
	other := buildDoc(v100)
	other.Objective = "outro objectivo"
	h3, err := planmigrate.HashPlan(other)
	if err != nil {
		t.Fatalf("HashPlan: %v", err)
	}
	if h3 == h1 {
		t.Fatalf("documentos diferentes produziram o mesmo hash: %s", h1)
	}
}
