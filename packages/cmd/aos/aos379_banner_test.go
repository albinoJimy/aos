package main

import (
	"strings"
	"testing"
)

// TestAOS379_MediationChannelPostureBanner cobre o AC5 na parte do banner: a linha de postura do
// canal de eventos de mediação DIFERE entre os três estados e nunca diz "ligado" sobre algo não
// composto. Exercita a função pura nos três valores possíveis (composto+durável, composto+volátil,
// não-composto) sem levantar um nó — a mesma disciplina de [taintGatePostureBanner]/[budgetPostureBanner].
func TestAOS379_MediationChannelPostureBanner(t *testing.T) {
	duravel := strings.Join(mediationChannelPostureBanner(true, true), "\n")
	volatil := strings.Join(mediationChannelPostureBanner(true, false), "\n")
	naoComposto := strings.Join(mediationChannelPostureBanner(false, false), "\n")

	if duravel == "" || volatil == "" || naoComposto == "" {
		t.Fatal("banner vazio num dos estados")
	}
	// Os três estados TÊM de produzir linhas distintas — senão o banner não distingue a postura.
	if duravel == volatil || duravel == naoComposto || volatil == naoComposto {
		t.Fatalf("duas posturas produzem a MESMA linha:\n  duravel=%q\n  volatil=%q\n  nao=%q", duravel, volatil, naoComposto)
	}

	// Só o estado composto+durável pode afirmar o canal DURÁVEL e a implicação fail-closed.
	up := strings.ToUpper(duravel)
	if !strings.Contains(up, "DURAVEL") || !strings.Contains(up, "FAIL-CLOSED") {
		t.Errorf("a postura composta+duravel nao declara durabilidade + fail-closed: %q", duravel)
	}
	// O estado volátil NÃO pode ler-se como durável: tem de nomear o "NAO DURAVEL".
	if !strings.Contains(strings.ToUpper(volatil), "NAO DURAVEL") {
		t.Errorf("a postura in-memory nao nomeia a nao-durabilidade: %q", volatil)
	}
	// O estado não-composto TEM de dizer NAO COMPOSTO — a regra do ficheiro: nunca "ligado" sobre
	// algo não composto.
	if !strings.Contains(strings.ToUpper(naoComposto), "NAO COMPOSTO") {
		t.Errorf("a postura nao-composta nao se declara NAO COMPOSTO: %q", naoComposto)
	}
	if strings.Contains(strings.ToUpper(naoComposto), "FAIL-CLOSED") {
		t.Errorf("a postura nao-composta nao pode prometer fail-closed (nada esta composto): %q", naoComposto)
	}
}
