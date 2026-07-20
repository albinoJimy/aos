package eval

import (
	"errors"
	"strings"
	"testing"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// TestGoldenSetValidateFailClosed prova que Validate REJEITA todo o golden-set
// mal-formado (versão inválida, kind/dataset inválidos, vazio, IDs duplicados, caso
// sem ID/input, caso vácuo) e ACEITA um set bem-formado. Fail-closed (AC1).
func TestGoldenSetValidateFailClosed(t *testing.T) {
	valid := SkillGoldenSet()
	if err := valid.Validate(); err != nil {
		t.Fatalf("golden-set válido rejeitado: %v", err)
	}

	tests := []struct {
		name string
		mut  func(gs *GoldenSet)
		want error
	}{
		{"versao-vazia", func(gs *GoldenSet) { gs.Version = "" }, ErrInvalidVersion},
		{"versao-nao-semver", func(gs *GoldenSet) { gs.Version = "1.0" }, ErrInvalidVersion},
		{"versao-nao-numerica", func(gs *GoldenSet) { gs.Version = "1.x.0" }, ErrInvalidVersion},
		{"kind-invalido", func(gs *GoldenSet) { gs.ArtifactKind = "widget" }, ErrInvalidKind},
		{"dataset-invalido", func(gs *GoldenSet) { gs.Dataset = "other" }, ErrInvalidDataset},
		{"vazio", func(gs *GoldenSet) { gs.Cases = nil }, ErrEmptyGoldenSet},
		{"id-duplicado", func(gs *GoldenSet) { gs.Cases = append(gs.Cases, gs.Cases[0]) }, ErrDuplicateCaseID},
		{"id-vazio", func(gs *GoldenSet) { gs.Cases[0].ID = "" }, ErrEmptyCaseID},
		{"input-vazio", func(gs *GoldenSet) { gs.Cases[0].Input = "" }, ErrEmptyCaseInput},
		{"caso-vacuo", func(gs *GoldenSet) {
			gs.Cases[0].ExpectSubstring = ""
			gs.Cases[0].RequiredActions = nil
			gs.Cases[0].ForbiddenActions = nil
		}, ErrVacuousCase},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gs := deepCopySet(valid)
			tc.mut(&gs)
			err := gs.Validate()
			if err == nil {
				t.Fatalf("esperado erro %v, obtido nil", tc.want)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("erro = %v; esperado que envolvesse %v", err, tc.want)
			}
		})
	}
}

// TestGoldenSetJSONRoundTrip prova que a serialização é byte-estável: Load→Canonical
// reproduz a entrada, e Canonical→Load→Canonical é idempotente (AC1 versionado/revisável).
func TestGoldenSetJSONRoundTrip(t *testing.T) {
	for _, gs := range BuiltSuites() {
		data, err := gs.CanonicalJSON()
		if err != nil {
			t.Fatalf("CanonicalJSON: %v", err)
		}
		loaded, err := LoadGoldenSet(data)
		if err != nil {
			t.Fatalf("LoadGoldenSet: %v", err)
		}
		again, err := loaded.CanonicalJSON()
		if err != nil {
			t.Fatalf("CanonicalJSON(loaded): %v", err)
		}
		if string(data) != string(again) {
			t.Fatalf("round-trip não byte-estável para %s", gs.EvalID())
		}
	}
}

// TestLoadGoldenSetRejectsUnknownField prova que um golden-set com um campo
// desconhecido (formato divergente) é rejeitado (fail-closed), não aceite em silêncio.
func TestLoadGoldenSetRejectsUnknownField(t *testing.T) {
	bad := `{"version":"1.0.0","artifact_kind":"skill","dataset":"golden","cases":[{"id":"a","input":"b","expect_substring":"c"}],"extra":true}`
	if _, err := LoadGoldenSet([]byte(bad)); err == nil {
		t.Fatal("esperado erro por campo desconhecido, obtido nil")
	}
}

// TestLoadGoldenSetRejectsMalformedJSON cobre o ramo de JSON inválido.
func TestLoadGoldenSetRejectsMalformedJSON(t *testing.T) {
	if _, err := LoadGoldenSet([]byte("{not json")); err == nil {
		t.Fatal("esperado erro por JSON malformado, obtido nil")
	}
}

// TestEvalIDStable prova que EvalID/Suite são deterministas e legíveis.
func TestEvalIDStable(t *testing.T) {
	gs := SkillFailureDerivedSet()
	if gs.Suite() != string(ArtifactSkill) {
		t.Fatalf("Suite = %q", gs.Suite())
	}
	want := "skill@1.0.0/failure_derived"
	if gs.EvalID() != want {
		t.Fatalf("EvalID = %q; want %q", gs.EvalID(), want)
	}
	if gs.Dataset != otelgenai.EvalDatasetFailureDerived {
		t.Fatalf("dataset = %q", gs.Dataset)
	}
	if !strings.Contains(gs.EvalID(), "failure_derived") {
		t.Fatal("EvalID deveria distinguir o dataset")
	}
}

// deepCopySet copia um GoldenSet e os seus casos (para mutações de teste isoladas).
func deepCopySet(gs GoldenSet) GoldenSet {
	out := gs
	out.Cases = make([]GoldenCase, len(gs.Cases))
	for i, c := range gs.Cases {
		cc := c
		cc.RequiredActions = append([]string(nil), c.RequiredActions...)
		cc.ForbiddenActions = append([]string(nil), c.ForbiddenActions...)
		out.Cases[i] = cc
	}
	return out
}
