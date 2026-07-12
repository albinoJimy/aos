package state

import "testing"

// TestExactlyTenStates fixa o número canónico de estados em DEZ (critério AOS-017).
func TestExactlyTenStates(t *testing.T) {
	if len(AllStates) != 10 {
		t.Fatalf("esperava 10 estados canónicos, obtive %d", len(AllStates))
	}
	// Sem duplicados e todos conhecidos.
	seen := make(map[State]bool, len(AllStates))
	for _, s := range AllStates {
		if seen[s] {
			t.Fatalf("estado duplicado em AllStates: %q", s)
		}
		seen[s] = true
		if !IsKnown(s) {
			t.Fatalf("estado %q em AllStates mas não reconhecido por IsKnown", s)
		}
	}
}

// expectedValid é a matriz de VERDADE independente da implementação: lista os 13
// pares válidos à mão (não deriva de validTransitions), para que o teste seja um
// oráculo genuíno e detecte tanto pares em falta como pares a mais.
var expectedValid = map[transition]bool{
	{Ready, Running}:          true,
	{Running, WaitingOnTool}:  true,
	{WaitingOnTool, Running}:  true,
	{Running, WaitingOnHuman}: true,
	{WaitingOnHuman, Running}: true,
	{WaitingOnHuman, Killed}:  true,
	{Running, Paused}:         true,
	{Paused, Running}:         true,
	{Running, Complete}:       true,
	{Running, Failed}:         true,
	{Running, TimedOut}:       true,
	{Failed, Compensating}:    true,
	{Compensating, Ready}:     true,
}

// TestTransitionMatrix10x10 VARRE a matriz completa 10×10 (100 pares): cada par
// listado em expectedValid deve ser aceite por IsValidTransition, e TODOS os
// restantes 87 pares devem ser rejeitados. É o oráculo puro da tabela declarativa.
func TestTransitionMatrix10x10(t *testing.T) {
	if len(expectedValid) != 13 {
		t.Fatalf("oráculo deve ter 13 pares válidos, tem %d", len(expectedValid))
	}
	var validCount, invalidCount int
	for _, from := range AllStates {
		for _, to := range AllStates {
			p := transition{from, to}
			want := expectedValid[p]
			got := IsValidTransition(from, to)
			if got != want {
				t.Errorf("IsValidTransition(%q, %q) = %v; quero %v", from, to, got, want)
			}
			if want {
				validCount++
			} else {
				invalidCount++
			}
		}
	}
	if validCount != 13 {
		t.Fatalf("varredura contou %d pares válidos, esperava 13", validCount)
	}
	if invalidCount != 87 {
		t.Fatalf("varredura contou %d pares inválidos, esperava 87 (100-13)", invalidCount)
	}
}

// TestTableMatchesOracle garante que a tabela interna validTransitions coincide
// EXACTAMENTE com o oráculo — sem pares a mais nem a menos (protege contra edições
// acidentais da tabela).
func TestTableMatchesOracle(t *testing.T) {
	if len(validTransitions) != len(expectedValid) {
		t.Fatalf("tabela tem %d pares, oráculo tem %d", len(validTransitions), len(expectedValid))
	}
	for p := range validTransitions {
		if !expectedValid[p] {
			t.Errorf("tabela contém par %v que o oráculo não considera válido", p)
		}
	}
}

// TestTerminalStatesHaveNoOutgoing confirma que os terminais absorventes não têm
// qualquer aresta de saída na tabela.
func TestTerminalStatesHaveNoOutgoing(t *testing.T) {
	for _, s := range []State{Complete, Killed, TimedOut} {
		if !IsTerminal(s) {
			t.Errorf("%q devia ser terminal", s)
		}
		for _, to := range AllStates {
			if IsValidTransition(s, to) {
				t.Errorf("estado terminal %q não devia ter saída para %q", s, to)
			}
		}
	}
}

// TestFailedIsRecoverableNotAbsorbing confirma que failed NÃO é absorvente: a única
// saída é para compensating.
func TestFailedIsRecoverableNotAbsorbing(t *testing.T) {
	if IsTerminal(Failed) {
		t.Fatal("failed não deve ser terminal absorvente")
	}
	if !IsFailure(Failed) {
		t.Fatal("failed deve ser classificado como falha recuperável")
	}
	var outgoing []State
	for _, to := range AllStates {
		if IsValidTransition(Failed, to) {
			outgoing = append(outgoing, to)
		}
	}
	if len(outgoing) != 1 || outgoing[0] != Compensating {
		t.Fatalf("failed deve ter exactamente uma saída (compensating), tem %v", outgoing)
	}
}

// TestSuspendedClassification fixa a família de suspensão legítima.
func TestSuspendedClassification(t *testing.T) {
	suspended := map[State]bool{WaitingOnTool: true, WaitingOnHuman: true, Paused: true}
	for _, s := range AllStates {
		if IsSuspended(s) != suspended[s] {
			t.Errorf("IsSuspended(%q) = %v; quero %v", s, IsSuspended(s), suspended[s])
		}
	}
}

// TestRequiresFencingTokenOnlyOnClaim confirma que só o claim ready→running exige
// token — nenhuma outra aresta (incluindo as retomas de suspensão para running).
func TestRequiresFencingTokenOnlyOnClaim(t *testing.T) {
	for _, from := range AllStates {
		for _, to := range AllStates {
			want := from == Ready && to == Running
			if RequiresFencingToken(from, to) != want {
				t.Errorf("RequiresFencingToken(%q,%q)=%v; quero %v", from, to, RequiresFencingToken(from, to), want)
			}
		}
	}
	// As outras arestas para running NÃO exigem token.
	for _, from := range []State{WaitingOnTool, WaitingOnHuman, Paused} {
		if RequiresFencingToken(from, Running) {
			t.Errorf("retoma %q→running não devia exigir fencing token", from)
		}
	}
}

// TestUint64TokenValidity fixa a semântica do token de referência.
func TestUint64TokenValidity(t *testing.T) {
	if (Uint64Token(0)).Valid() {
		t.Error("token 0 devia ser inválido (ausência de token)")
	}
	if !(Uint64Token(1)).Valid() {
		t.Error("token 1 devia ser válido")
	}
	if got := (Uint64Token(42)).Value(); got != 42 {
		t.Errorf("Value()=%d; quero 42", got)
	}
}

// TestIsKnownRejectsForged rejeita estados forjados.
func TestIsKnownRejectsForged(t *testing.T) {
	for _, s := range []State{"", "RUNNING", "done", "zombie", "ready "} {
		if IsKnown(s) {
			t.Errorf("IsKnown(%q) devia ser false", s)
		}
	}
}
