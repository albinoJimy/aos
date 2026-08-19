package autonomy

import (
	"context"
	"testing"
)

// TestPisoDeclaradoAplicaSeAPariAusentes — a razão de ser do piso: um par que nunca foi registado
// deixa de cair em L0 por herança e passa a cair no que alguém DECLAROU.
//
// A diferença não é de comportamento (o valor por omissão continua a ser L0) — é de quem responde
// pela decisão. Hoje "ligar a autonomia" significa, sem mais nada, "todo o agente novo bloqueia",
// e ninguém escolheu isso: herdou-se do valor-zero.
func TestPisoDeclaradoAplicaSeAPariAusentes(t *testing.T) {
	r := NewLevelRegistry(WithDefaultLevel(L1))
	if got := r.LevelFor("agt-nunca-visto", "fs"); got != L1 {
		t.Fatalf("par ausente com piso L1 deu %v, quero L1", got)
	}

	// CONTROLO — SEM piso declarado continua a ser L0. É o que garante que acrescentar esta
	// opção não muda nada para quem não a use, e que o valor-zero permanece o mais restritivo.
	semPiso := NewLevelRegistry()
	if got := semPiso.LevelFor("agt-nunca-visto", "fs"); got != L0 {
		t.Fatalf("par ausente SEM piso deu %v, quero L0 (fail-closed inalterado)", got)
	}
}

// TestRegistoExplicitoGanhaAoPiso é o controlo que impede o piso de ser um interruptor geral.
//
// Se o piso passasse à frente de um nível registado, declarar `AOS_AUTONOMY_DEFAULT=L4` elevaria
// silenciosamente todos os pares que alguém tinha deliberadamente posto em L0 ou L1 — o oposto do
// que a resolução em cascata promete, e uma escalada de autonomia que ninguém pediu.
func TestRegistoExplicitoGanhaAoPiso(t *testing.T) {
	r := NewLevelRegistry(WithDefaultLevel(L4))
	if _, err := r.SetLevel(context.Background(), "agt-1", "fs", L0, "este fica preso", "human:op"); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}
	if got := r.LevelFor("agt-1", "fs"); got != L0 {
		t.Fatalf("o par REGISTADO a L0 resolveu para %v — o piso passou-lhe a frente", got)
	}
	// E o piso continua a valer para os outros, no mesmo registo.
	if got := r.LevelFor("agt-2", "fs"); got != L4 {
		t.Fatalf("par ausente deu %v, quero o piso L4", got)
	}
}

// TestPisoInvalidoNaoBaixaAGuarda — [WithDefaultLevel] com um valor fora do domínio é IGNORADO, e
// o piso fica em L0.
//
// É a segunda linha de defesa: a fronteira de ambiente já recusa um `AOS_AUTONOMY_DEFAULT` mal
// escrito e aborta o arranque. Mas se algum caminho futuro construir o registo a partir de um
// inteiro não validado, o pior que pode acontecer é ficar no mais supervisionado — nunca abrir.
func TestPisoInvalidoNaoBaixaAGuarda(t *testing.T) {
	r := NewLevelRegistry(WithDefaultLevel(Level(99)))
	if got := r.LevelFor("agt-x", "fs"); got != L0 {
		t.Fatalf("piso invalido resolveu para %v, quero L0", got)
	}
}
