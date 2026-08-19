package autonomy

import (
	"context"
	"testing"
)

// TestCascataInstanciaGanhaAClasse — a ordem de resolução, do mais específico para o mais geral.
//
// Se a classe passasse à frente da instância, registar `class:agent-worker:fs=L4` elevaria em
// silêncio um agente que alguém tinha posto deliberadamente em L0 — a mesma escalada disfarçada
// de configuração que o piso já não pode fazer.
func TestCascataInstanciaGanhaAClasse(t *testing.T) {
	r := NewLevelRegistry(WithDefaultLevel(L1))
	ctx := context.Background()
	if _, err := r.SetLevel(ctx, ClassPrefix+"agent-worker", "fs", L4, "classe corre leitura", "human:op"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetLevel(ctx, "agt-preso", "fs", L0, "este fica preso", "human:op"); err != nil {
		t.Fatal(err)
	}

	// (1) instância registada GANHA à classe.
	if got := r.LevelForAgentOrClass("agt-preso", "agent-worker", "fs"); got != L0 {
		t.Errorf("instancia registada a L0 resolveu para %v — a classe passou-lhe a frente", got)
	}
	// (2) agente NUNCA VISTO herda da classe. É o ponto todo: cobre identidades que ainda não
	// existem, e os agent_id deste sistema são cunhados por run.
	if got := r.LevelForAgentOrClass("agt-acabado-de-nascer", "agent-worker", "fs"); got != L4 {
		t.Errorf("agente novo da classe resolveu para %v, quero L4 herdado da classe", got)
	}
	// (3) sem instância NEM classe registadas, cai no PISO.
	if got := r.LevelForAgentOrClass("agt-x", "researcher", "fs"); got != L1 {
		t.Errorf("sem instancia nem classe deu %v, quero o piso L1", got)
	}
	// (4) o domínio continua a separar: a mesma classe noutro domínio não herda.
	if got := r.LevelForAgentOrClass("agt-y", "agent-worker", "http"); got != L1 {
		t.Errorf("classe registada em fs vazou para http (%v) — o dominio deixou de separar", got)
	}
}

// TestCascataNaoAfectaQuemSoUsaLevelFor — o contrato antigo fica intacto.
//
// [LevelFor] não conhece classes, e tem de continuar a não conhecer: é a interface que os
// implementadores existentes satisfazem. Se passasse a consultar a classe, um oráculo que
// devolvesse L0 para um par começaria a devolver outra coisa sem que ninguém mudasse nada.
func TestCascataNaoAfectaQuemSoUsaLevelFor(t *testing.T) {
	r := NewLevelRegistry()
	if _, err := r.SetLevel(context.Background(), ClassPrefix+"agent-worker", "fs", L5, "x", "human:op"); err != nil {
		t.Fatal(err)
	}
	if got := r.LevelFor("agt-qualquer", "fs"); got != L0 {
		t.Fatalf("LevelFor consultou a classe (%v) — o contrato antigo mudou de comportamento", got)
	}
}

// TestClasseVaziaNaoHerdaNada — uma NHI sem `agent_class` não pode apanhar uma regra de classe por
// acidente. Sem esta guarda, `ClassPrefix + ""` seria uma chave válida e uma regra registada com
// classe vazia aplicar-se-ia a TODA a NHI que não declarasse classe — um curinga acidental.
func TestClasseVaziaNaoHerdaNada(t *testing.T) {
	r := NewLevelRegistry(WithDefaultLevel(L2))
	if _, err := r.SetLevel(context.Background(), ClassPrefix, "fs", L5, "curinga acidental", "human:op"); err != nil {
		t.Fatal(err)
	}
	if got := r.LevelForAgentOrClass("agt-sem-classe", "", "fs"); got != L2 {
		t.Fatalf("uma NHI sem classe apanhou a regra de classe vazia (%v) — curinga acidental", got)
	}
}
