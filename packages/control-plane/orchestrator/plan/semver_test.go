package plan

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestParsePlanVersion_StrictSemVer prova que o parser aceita SÓ "X.Y.Z" estrito
// de inteiros não-negativos e recusa tudo o resto.
//
// FALSIFICAÇÃO: um parser laxo — ex.: strings.SplitN sem exigir 3 componentes, ou
// que ignore o resto após "X.Y" — aceitaria "1.2" ou "v1.2.3" e este teste FALHA
// (o caso inválido esperava erro). NÃO-VÁCUO: exercita ambos os ramos (válidos que
// TÊM de parsear e produzir a struct certa; inválidos que TÊM de devolver
// ErrInvalidPlanVersion).
func TestParsePlanVersion_StrictSemVer(t *testing.T) {
	t.Parallel()
	valid := map[string]PlanVersion{
		"0.0.1":    {0, 0, 1},
		"1.0.0":    {1, 0, 0},
		"2.13.7":   {2, 13, 7},
		"10.20.30": {10, 20, 30},
	}
	for s, want := range valid {
		got, err := ParsePlanVersion(s)
		if err != nil {
			t.Fatalf("ParsePlanVersion(%q) erro inesperado: %v", s, err)
		}
		if got != want {
			t.Fatalf("ParsePlanVersion(%q) = %+v, quer %+v", s, got, want)
		}
	}

	invalid := []string{
		"",          // vazio
		"1",         // um componente
		"1.2",       // dois componentes
		"1.2.3.4",   // quatro componentes
		"v1.2.3",    // prefixo v
		"1.2.x",     // não-numérico
		"1..3",      // componente vazio
		"-1.2.3",    // negativo
		"1.2.3-rc1", // pré-release não suportado
		"+1.2.3",    // sinal espúrio
		" ",         // só espaço
		"  1.2.3  ", // whitespace envolvente (não-canónico, fail-closed)
		"1.2.3 ",    // whitespace à direita
		" 1.2.3",    // whitespace à esquerda
		"1. 2.3",    // whitespace interno
	}
	for _, s := range invalid {
		if _, err := ParsePlanVersion(s); !errors.Is(err, ErrInvalidPlanVersion) {
			t.Fatalf("ParsePlanVersion(%q) esperava ErrInvalidPlanVersion, obteve %v", s, err)
		}
	}
}

// TestPlanVersion_JSONRoundTrip prova que a versão serializa como a string
// canónica "X.Y.Z" e desserializa de volta ao mesmo valor.
//
// FALSIFICAÇÃO: se MarshalJSON emitisse o objecto {Major,Minor,Patch} em vez da
// string, o UnmarshalJSON (que espera string) falharia e o round-trip quebra.
// NÃO-VÁCUO: compara o valor reconstruído com o original.
func TestPlanVersion_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	orig := PlanVersion{Major: 3, Minor: 4, Patch: 5}
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(raw) != `"3.4.5"` {
		t.Fatalf("wire = %s, quer \"3.4.5\"", raw)
	}
	var back PlanVersion
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back != orig {
		t.Fatalf("round-trip = %+v, quer %+v", back, orig)
	}
}

// TestPlanVersion_UnmarshalRejectsNonString prova fail-closed: um plan_version que
// não seja string JSON (número, objecto) é recusado.
//
// FALSIFICAÇÃO: um UnmarshalJSON que caísse em zero-value silencioso em vez de
// erro aceitaria `123` e este teste FALHA.
func TestPlanVersion_UnmarshalRejectsNonString(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`123`, `{"major":1}`, `1.0`, `null`, `true`} {
		var v PlanVersion
		if err := json.Unmarshal([]byte(raw), &v); err == nil {
			t.Fatalf("Unmarshal(%s) devia falhar, obteve %+v", raw, v)
		}
	}
}

// TestPlanVersion_CompareAndCompatible prova a ordenação total (MAJOR>MINOR>PATCH)
// e a regra de compatibilidade por MAJOR (§3.6).
//
// FALSIFICAÇÃO: se Compatible comparasse MINOR/PATCH, {1,0,0} e {1,9,9} seriam
// tidos por incompatíveis e o teste FALHA; se Compare invertesse a precedência,
// {2,0,0} vs {1,9,9} daria o sinal errado.
func TestPlanVersion_CompareAndCompatible(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b PlanVersion
		cmp  int
	}{
		{PlanVersion{1, 0, 0}, PlanVersion{1, 0, 0}, 0},
		{PlanVersion{2, 0, 0}, PlanVersion{1, 9, 9}, 1},
		{PlanVersion{1, 2, 0}, PlanVersion{1, 3, 0}, -1},
		{PlanVersion{1, 2, 3}, PlanVersion{1, 2, 4}, -1},
	}
	for _, c := range cases {
		if got := c.a.Compare(c.b); got != c.cmp {
			t.Fatalf("%v.Compare(%v) = %d, quer %d", c.a, c.b, got, c.cmp)
		}
	}
	// Mesmo MAJOR ⇒ compatível, mesmo com MINOR/PATCH divergentes.
	if !(PlanVersion{1, 0, 0}).Compatible(PlanVersion{1, 9, 9}) {
		t.Fatal("mesmo MAJOR devia ser compativel")
	}
	// MAJOR diferente (mesmo downgrade) ⇒ incompatível, fail-closed.
	if (PlanVersion{2, 0, 0}).Compatible(PlanVersion{1, 0, 0}) {
		t.Fatal("MAJOR diferente devia ser incompativel")
	}
}
