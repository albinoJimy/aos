package durable

// aos483_predicado_test.go — O PREDICADO EXPORTADO E A RECUSA REAL SÃO A MESMA COISA.
//
// O [CaractereProibidoNaChave] existe para que quem compõe um `run_id` a montante derive da
// regra em vez de copiar o carácter. Isso só vale enquanto o predicado disser EXACTAMENTE o que
// a [IdempotencyKey] recusa: um predicado mais estreito deixa passar o que a chave recusa — que
// é o defeito do AOS-483 outra vez, uma camada acima —, e um mais largo faz a ponta de fora
// escapar caracteres que não precisavam, mudando ids que já existiam sem razão.
//
// Por isso a prova não é uma lista: é o espaço inteiro de um byte, comparado contra o
// comportamento real da função.

import (
	"errors"
	"testing"
)

// TestAOS483PredicadoCasaComAIdempotencyKey varre os 256 bytes e exige que o predicado e a
// recusa da [IdempotencyKey] concordem em todos.
func TestAOS483PredicadoCasaComAIdempotencyKey(t *testing.T) {
	for b := 0; b < 256; b++ {
		r := rune(b)
		// O `run_id` é «x<c>y» para que o carácter esteja no MEIO: um carácter no início ou no
		// fim poderia ser recusado por outra razão (id vazio de um dos lados).
		runID := "x" + string(r) + "y"
		_, err := IdempotencyKey(runID, "passo")
		recusado := errors.Is(err, ErrDelimiterInInput)
		proibido := CaractereProibidoNaChave(r)
		if recusado != proibido {
			t.Errorf("byte 0x%02x: a IdempotencyKey recusa=%v e o predicado diz proibido=%v.\n"+
				"Os dois TÊM de concordar: o predicado é a forma de quem compõe um run_id a "+
				"montante derivar desta regra em vez de a copiar. Se a regra mudou, mude o "+
				"predicado no MESMO commit — ver AOS-483 e o comentário de CaractereProibidoNaChave.",
				b, recusado, proibido)
		}
	}
}

// TestAOS483PredicadoNaoEVacuo é o controlo de não-vacuidade. Sem ele, um predicado que
// devolvesse sempre `false` passaria no teste acima se a [IdempotencyKey] também deixasse de
// recusar — os dois errados da mesma maneira, e a asserção a medir nada.
func TestAOS483PredicadoNaoEVacuo(t *testing.T) {
	if !CaractereProibidoNaChave(':') {
		t.Fatal("o ':' deixou de ser proibido na chave: o delimitador de run_id:step_id passou a " +
			"ser admitido nos inputs, e a decomposição da chave deixou de ser única")
	}
	for _, r := range []rune{'a', '0', '_', '-', '.', '~', '+'} {
		if CaractereProibidoNaChave(r) {
			t.Errorf("o %q passou a ser proibido na chave. Se foi deliberado, o escape do "+
				"`childRunID` no aos-orq aperta com isto por construção — mas confirme que os "+
				"ids que já existem não mudam de forma inesperada (ver childRunID §COMPATIBILIDADE)", r)
		}
	}
}
