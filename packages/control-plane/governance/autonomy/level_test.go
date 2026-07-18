package autonomy

import "testing"

func TestLevelStringAndValid(t *testing.T) {
	names := map[Level]string{L0: "L0", L1: "L1", L2: "L2", L3: "L3", L4: "L4", L5: "L5"}
	for l, want := range names {
		if !l.Valid() {
			t.Errorf("%s devia ser válido", want)
		}
		if l.String() != want {
			t.Errorf("String()=%q; quer %q", l.String(), want)
		}
		if l.Description() == "" {
			t.Errorf("%s sem descrição", want)
		}
	}
	if Level(6).Valid() {
		t.Error("L6 (=6) devia ser inválido")
	}
	if Level(200).String() != "L?" {
		t.Errorf("nível inválido devia serializar como L?; obteve %q", Level(200).String())
	}
	if Level(200).Description() != "" {
		t.Error("nível inválido devia ter descrição vazia")
	}
}

// TestL0IsZeroValue garante que o valor-zero de Level é L0 (fail-closed).
func TestL0IsZeroValue(t *testing.T) {
	var z Level
	if z != L0 {
		t.Errorf("valor-zero de Level = %s; quer L0 (fail-closed)", z)
	}
}
