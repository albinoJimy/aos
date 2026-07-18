package autonomy

import (
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// TestOversightPerLevel é o teste POR NÍVEL exigido pela spec: cada L0–L5 produz o
// grau de oversight ESPERADO para uma tool call de cada classe de risco. A tabela
// completa é a expressão testável da AC3 (nível × classe → modo de gate).
func TestOversightPerLevel(t *testing.T) {
	cases := []struct {
		level Level
		safe  OversightMode
		gray  OversightMode
		dang  OversightMode
	}{
		// L0 sugestão: humano executa tudo, qualquer classe.
		{L0, OversightSuggest, OversightSuggest, OversightSuggest},
		// L1 aprovação por acção: confirma sempre, qualquer classe.
		{L1, OversightConfirm, OversightConfirm, OversightConfirm},
		// L2 aprovação por lote: safe/gray em lote; danger nunca em lote → confirm.
		{L2, OversightBatch, OversightBatch, OversightConfirm},
		// L3 autonomia supervisionada = tiering SA-ROC: safe corre, gray lote, danger confirma.
		{L3, OversightRun, OversightBatch, OversightConfirm},
		// L4 autonomia por excepção: safe/gray correm; danger (risco alto) confirma.
		{L4, OversightRun, OversightRun, OversightConfirm},
		// L5 autonomia plena: safe/gray correm; danger corre sob amostragem post-hoc.
		{L5, OversightRun, OversightRun, OversightPostHocSample},
	}
	for _, c := range cases {
		if got := Oversight(c.level, risk.ClassSafe); got != c.safe {
			t.Errorf("Oversight(%s, safe) = %s; quer %s", c.level, got, c.safe)
		}
		if got := Oversight(c.level, risk.ClassGray); got != c.gray {
			t.Errorf("Oversight(%s, gray) = %s; quer %s", c.level, got, c.gray)
		}
		if got := Oversight(c.level, risk.ClassDanger); got != c.dang {
			t.Errorf("Oversight(%s, danger) = %s; quer %s", c.level, got, c.dang)
		}
	}
}

// TestOversightDangerNeverRunsBare prova a INVARIANTE ADR-013: nenhuma composição
// nível × danger produz [OversightRun] puro — danger corre no máximo sob oversight
// amostral post-hoc (L5) e caso contrário confirma. Nunca há danger silencioso.
func TestOversightDangerNeverRunsBare(t *testing.T) {
	for l := L0; l <= L5; l++ {
		if got := Oversight(l, risk.ClassDanger); got == OversightRun {
			t.Errorf("Oversight(%s, danger) = run; danger NUNCA corre sem oversight (ADR-013)", l)
		}
	}
}

// TestOversightMonotonicNonIncreasing prova a MONOTONIA: para uma classe fixa, um
// nível mais alto nunca aplica MAIS gate (o modo é não-crescente em restritividade
// à medida que o nível sobe). Codifica-se a restritividade pela ordem enum
// (Suggest > Confirm > Batch > PostHocSample > Run).
func TestOversightMonotonicNonIncreasing(t *testing.T) {
	classes := []risk.Class{risk.ClassSafe, risk.ClassGray, risk.ClassDanger}
	for _, c := range classes {
		for l := L0; l < L5; l++ {
			cur := Oversight(l, c)
			next := Oversight(l+1, c)
			// OversightMode é ordenado do mais restritivo (0) ao menos (4); subir de
			// nível não pode DECRESCER o valor (ficar mais restritivo).
			if next < cur {
				t.Errorf("monotonia violada para classe %s: %s→%s mas %s < %s",
					c.String(), l, l+1, next, cur)
			}
		}
	}
}

// TestOversightInvalidLevelFailClosed prova o fail-closed: um nível fora de L0–L5
// resolve para o mais restritivo ([OversightSuggest]).
func TestOversightInvalidLevelFailClosed(t *testing.T) {
	invalid := Level(99)
	if invalid.Valid() {
		t.Fatal("Level(99) devia ser inválido")
	}
	for _, c := range []risk.Class{risk.ClassSafe, risk.ClassGray, risk.ClassDanger} {
		if got := Oversight(invalid, c); got != OversightSuggest {
			t.Errorf("Oversight(invalido, %s) = %s; quer suggest (fail-closed)", c.String(), got)
		}
	}
}

// TestOversightL3EqualsTiering prova que a linha L3 É o tiering SA-ROC base
// (composição, não reimplementação): safe→run, gray→batch, danger→confirm.
func TestOversightL3EqualsTiering(t *testing.T) {
	if Oversight(L3, risk.ClassSafe) != OversightRun {
		t.Error("L3/safe deve correr (tiering base)")
	}
	if Oversight(L3, risk.ClassGray) != OversightBatch {
		t.Error("L3/gray deve agrupar em lote (tiering base)")
	}
	if Oversight(L3, risk.ClassDanger) != OversightConfirm {
		t.Error("L3/danger deve confirmar (tiering base)")
	}
}

// TestClassZeroValueFailClosed prova que a classe valor-zero ([risk.ClassDanger])
// é tratada como o pior caso em oversightFromTiering (fail-closed).
func TestClassZeroValueFailClosed(t *testing.T) {
	var zero risk.Class // ClassDanger é o valor-zero de risk.Class
	if zero != risk.ClassDanger {
		t.Fatalf("pressuposto quebrado: valor-zero de risk.Class = %v", zero)
	}
	if got := oversightFromTiering(zero); got != OversightConfirm {
		t.Errorf("classe valor-zero deve confirmar (fail-closed); obteve %s", got)
	}
}

// TestOversightModePredicates cobre RequiresHumanGate/Runs/String em todos os modos.
func TestOversightModePredicates(t *testing.T) {
	gate := map[OversightMode]bool{
		OversightSuggest:       true,
		OversightConfirm:       true,
		OversightBatch:         true,
		OversightPostHocSample: false,
		OversightRun:           false,
	}
	names := map[OversightMode]string{
		OversightSuggest:       "suggest",
		OversightConfirm:       "confirm",
		OversightBatch:         "batch",
		OversightPostHocSample: "post_hoc_sample",
		OversightRun:           "run",
	}
	for m, want := range gate {
		if m.RequiresHumanGate() != want {
			t.Errorf("%s.RequiresHumanGate()=%v; quer %v", m, m.RequiresHumanGate(), want)
		}
		if m.Runs() == want {
			t.Errorf("%s.Runs()=%v; devia ser o inverso de RequiresHumanGate", m, m.Runs())
		}
		if m.String() != names[m] {
			t.Errorf("%s.String()=%q; quer %q", m, m.String(), names[m])
		}
	}
	// Valor fora do domínio → "suggest" (fail-closed).
	if OversightMode(99).String() != "suggest" {
		t.Error("modo inválido deve serializar como suggest")
	}
}
