package eval

import "testing"

// TestEmbeddedSuitesMatchBuilders prova que os ficheiros JSON embebidos e versionados
// NÃO divergem da fonte de verdade (os builders): cada artefacto embebido é byte-idêntico
// ao CanonicalJSON do builder correspondente. Se um builder mudar sem regenerar
// (go run gen_goldensets.go), este teste avermelha — sem drift silencioso (AC1).
func TestEmbeddedSuitesMatchBuilders(t *testing.T) {
	embedded, err := EmbeddedSuites()
	if err != nil {
		t.Fatalf("EmbeddedSuites: %v", err)
	}
	built := BuiltSuites()
	if len(embedded) != len(built) {
		t.Fatalf("nº de suites: embebidas=%d builders=%d", len(embedded), len(built))
	}
	// Indexa os builders por EvalID (a ordem de ficheiro pode diferir da dos builders).
	byID := make(map[string]GoldenSet, len(built))
	for _, gs := range built {
		byID[gs.EvalID()] = gs
	}
	for _, e := range embedded {
		b, ok := byID[e.EvalID()]
		if !ok {
			t.Fatalf("suite embebida %q sem builder correspondente", e.EvalID())
		}
		eb, err := e.CanonicalJSON()
		if err != nil {
			t.Fatal(err)
		}
		bb, err := b.CanonicalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if string(eb) != string(bb) {
			t.Fatalf("suite embebida %q diverge do builder — regenerar com go run gen_goldensets.go", e.EvalID())
		}
	}
}

// TestEmbeddedSuitesCoverBothClassesAndDatasets prova o AC1 "não-trivial por classe":
// existe >=1 golden E >=1 failure_derived por classe (skill, procedural_memory), com
// casos suficientes.
func TestEmbeddedSuitesCoverBothClassesAndDatasets(t *testing.T) {
	suites, err := EmbeddedSuites()
	if err != nil {
		t.Fatalf("EmbeddedSuites: %v", err)
	}
	type key struct {
		kind    ArtifactKind
		dataset string
	}
	counts := map[key]int{}
	for _, gs := range suites {
		counts[key{gs.ArtifactKind, string(gs.Dataset)}] += len(gs.Cases)
	}
	for _, kind := range []ArtifactKind{ArtifactSkill, ArtifactProceduralMemory} {
		if counts[key{kind, "golden"}] < 2 {
			t.Errorf("classe %q: golden-set curado trivial (%d casos)", kind, counts[key{kind, "golden"}])
		}
		if counts[key{kind, "failure_derived"}] < 1 {
			t.Errorf("classe %q: sem dataset failure_derived", kind)
		}
	}
}

// TestEmbeddedSuitesFor cobre o filtro por classe.
func TestEmbeddedSuitesFor(t *testing.T) {
	skill, err := EmbeddedSuitesFor(ArtifactSkill)
	if err != nil {
		t.Fatal(err)
	}
	if len(skill) < 2 { // golden + failure_derived
		t.Fatalf("esperados >=2 sets de skill, obtido %d", len(skill))
	}
	for _, gs := range skill {
		if gs.ArtifactKind != ArtifactSkill {
			t.Fatalf("filtro devolveu classe errada: %q", gs.ArtifactKind)
		}
	}
}
