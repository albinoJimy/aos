package main

import (
	"strings"
	"testing"
)

// TestAOS363_TaintGatePostureBanner cobre o critério 4: o banner ganha uma linha de postura
// do Reference Monitor, e ela DIFERE entre as duas configurações — a do conjunto vazio nomeia
// a inércia (não a disfarça). O argumento é o predicado real HasActiveTaintGate(); aqui
// exercita-se a função pura nos dois valores possíveis.
func TestAOS363_TaintGatePostureBanner(t *testing.T) {
	ativo := taintGatePostureBanner(true)
	inerte := taintGatePostureBanner(false)

	if len(ativo) == 0 || len(inerte) == 0 {
		t.Fatal("banner vazio num dos estados")
	}
	joinAtivo := strings.Join(ativo, "\n")
	joinInerte := strings.Join(inerte, "\n")

	if joinAtivo == joinInerte {
		t.Fatal("as duas posturas produzem a MESMA linha — o banner não distingue o gate activo do inerte")
	}

	// A linha inerte TEM de nomear a inércia — não pode ler-se como se a barreira estivesse
	// ligada. Um operador que leia o arranque precisa de saber que a defesa estrutural está desligada.
	low := strings.ToUpper(joinInerte)
	if !strings.Contains(low, "INERTE") {
		t.Errorf("a postura do conjunto vazio não nomeia a inércia: %q", joinInerte)
	}
	// E a linha activa TEM de declarar que está activa/endurecida.
	if !strings.Contains(strings.ToUpper(joinAtivo), "ATIVA") {
		t.Errorf("a postura do conjunto não-vazio não se declara activa: %q", joinAtivo)
	}
	// A inerte deve apontar o caminho de opt-in (a variável a definir), senão o operador não
	// sabe como ligar a barreira.
	if !strings.Contains(joinInerte, "AOS_PRIVILEGED_CAPS") {
		t.Errorf("a postura inerte não nomeia AOS_PRIVILEGED_CAPS (o caminho de opt-in): %q", joinInerte)
	}
}
