package coverage_test

import (
	"strings"
	"testing"

	"github.com/aos-ref/testkit/coverage"
)

// TestConvertToLCOV_Basico: um coverprofile simples produz LCOV determinista com
// DA/LF/LH correctos e ficheiros ordenados.
func TestConvertToLCOV_Basico(t *testing.T) {
	t.Parallel()
	profile := strings.Join([]string{
		"mode: atomic",
		"pkg/b.go:10.2,12.3 2 1", // linhas 10,11,12 cobertas (count 1)
		"pkg/b.go:20.2,20.3 1 0", // linha 20 não coberta
		"pkg/a.go:1.1,1.2 1 5",   // linha 1 coberta (count 5)
		"",
	}, "\n")

	var out strings.Builder
	if err := coverage.ConvertToLCOV(strings.NewReader(profile), &out); err != nil {
		t.Fatalf("ConvertToLCOV: %v", err)
	}
	got := out.String()

	// Ficheiros ordenados alfabeticamente: a.go antes de b.go.
	ia := strings.Index(got, "SF:pkg/a.go")
	ib := strings.Index(got, "SF:pkg/b.go")
	if ia < 0 || ib < 0 || ia > ib {
		t.Fatalf("ordem de ficheiros incorrecta:\n%s", got)
	}

	// a.go: 1 linha, 1 hit.
	assertContains(t, got, "SF:pkg/a.go\nDA:1,5\nLF:1\nLH:1\nend_of_record")

	// b.go: linhas 10,11,12 (hit) + 20 (miss) = LF:4 LH:3.
	for _, want := range []string{"DA:10,1", "DA:11,1", "DA:12,1", "DA:20,0", "LF:4", "LH:3"} {
		assertContains(t, got, want)
	}
}

// TestConvertToLCOV_AgregaMaximo: a mesma linha coberta por vários blocos/perfis
// conta o count MÁXIMO (coberta se qualquer bloco executou).
func TestConvertToLCOV_AgregaMaximo(t *testing.T) {
	t.Parallel()
	profile := strings.Join([]string{
		"mode: count",
		"p/x.go:5.1,5.2 1 0", // linha 5 miss
		"p/x.go:5.1,6.2 2 3", // linha 5 e 6 com count 3 (bloco sobreposto)
		"",
	}, "\n")
	var out strings.Builder
	if err := coverage.ConvertToLCOV(strings.NewReader(profile), &out); err != nil {
		t.Fatalf("ConvertToLCOV: %v", err)
	}
	got := out.String()
	assertContains(t, got, "DA:5,3") // max(0,3)=3
	assertContains(t, got, "DA:6,3")
	assertContains(t, got, "LH:2")
}

// TestConvertToLCOV_Malformado: uma linha inválida aborta (fail-closed).
func TestConvertToLCOV_Malformado(t *testing.T) {
	t.Parallel()
	cases := []string{
		"mode: set\nlixo sem posicao aqui\n",
		"mode: set\npkg/a.go:1.1,1.2 1 naoNumero\n",
		"mode: set\npkg/a.go:semposicao 1 1\n",
		"mode: set\npkg/a.go:2.1,1.2 1 1\n", // endLine < startLine
	}
	for _, c := range cases {
		var out strings.Builder
		if err := coverage.ConvertToLCOV(strings.NewReader(c), &out); err == nil {
			t.Fatalf("esperava erro para perfil malformado: %q", c)
		}
	}
}

// TestConvertToLCOV_Determinista: a mesma entrada produz sempre a mesma saída.
func TestConvertToLCOV_Determinista(t *testing.T) {
	t.Parallel()
	profile := "mode: atomic\nz.go:3.1,3.2 1 1\na.go:1.1,1.2 1 0\n"
	var o1, o2 strings.Builder
	_ = coverage.ConvertToLCOV(strings.NewReader(profile), &o1)
	_ = coverage.ConvertToLCOV(strings.NewReader(profile), &o2)
	if o1.String() != o2.String() {
		t.Fatal("saida nao determinista")
	}
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("saida LCOV nao contem %q:\n%s", needle, haystack)
	}
}
