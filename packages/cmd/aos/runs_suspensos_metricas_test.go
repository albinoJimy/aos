package main

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------------------
// UMA PARAGEM DELIBERADA QUE NINGUÉM VIA.
//
// A serialização das retomas (achado 1.12) entrou em produção sem observabilidade nenhuma. Listadas
// TODAS as séries do nó em produção, não havia uma única sobre runs suspensos: um run preso em
// `waiting_on_human` que não conseguisse retomar não aparecia em lado nenhum — só se dava por ele
// quando alguém tentasse retomá-lo e falhasse.
//
// Uma guarda sem sinal é uma guarda que ninguém vê falhar.
// ---------------------------------------------------------------------------------------------

// svcComSuspensos monta um NodeService com N runs no balde de suspensos, carimbados há `ha`.
func svcComSuspensos(t *testing.T, ha time.Duration, ids ...string) *NodeService {
	t.Helper()
	svc := &NodeService{
		runs:      make(map[string]*runState),
		completed: make(map[string]*runState),
		suspended: make(map[string]*runState),
	}
	for _, id := range ids {
		svc.suspended[id] = &runState{
			runID: id, suspended: true, done: make(chan struct{}),
			suspendedAtUnix: time.Now().Add(-ha).Unix(),
		}
	}
	return svc
}

func TestRunsSuspensosSaemEmMetrics(t *testing.T) {
	h := &apiHandler{node: &Node{}, svc: svcComSuspensos(t, 90*time.Second, "run-a", "run-b")}
	corpo := metricasDe(t, h)

	n, ok := valorDe(t, corpo, "aos_runs_suspended")
	if !ok {
		t.Fatal("aos_runs_suspended NAO sai — a unica paragem deliberada do no continua invisivel")
	}
	if n != 2 {
		t.Errorf("aos_runs_suspended = %v, queria 2", n)
	}

	idade, ok := valorDe(t, corpo, "aos_runs_suspended_oldest_age_seconds")
	if !ok {
		t.Fatal("aos_runs_suspended_oldest_age_seconds NAO sai com runs a espera — a contagem " +
			"sozinha nao distingue «houve uma escalada agora» de «ninguem decide ha horas»")
	}
	if idade < 80 || idade > 200 {
		t.Errorf("idade = %v, esperava ~90s", idade)
	}
}

// TestAIdadeNAOSaiComOBaldeVazio é a disciplina que as nove métricas anteriores já seguem: uma
// série que MENTIRIA fica AUSENTE, nunca a zero.
//
// Emitir idade 0 com o balde vazio diria «alguém acabou de suspender» sobre um nó onde ninguém
// espera — e um nó saudável ficaria indistinguível de um com uma escalada acabada de acontecer.
func TestAIdadeNAOSaiComOBaldeVazio(t *testing.T) {
	h := &apiHandler{node: &Node{}, svc: svcComSuspensos(t, 0)}
	corpo := metricasDe(t, h)

	// A CONTAGEM sai, e sai a zero: «ninguém espera» é uma afirmação verdadeira.
	n, ok := valorDe(t, corpo, "aos_runs_suspended")
	if !ok || n != 0 {
		t.Errorf("aos_runs_suspended devia sair a 0 com o balde vazio; ok=%t v=%v", ok, n)
	}
	if _, ok := valorDe(t, corpo, "aos_runs_suspended_oldest_age_seconds"); ok {
		t.Error("a idade SAIU com o balde vazio — uma serie que mentiria tem de ficar AUSENTE")
	}
}

// TestOMaisAntigoEQueManda — a idade é a do PRIMEIRO a esperar, não a do último.
//
// Sem este teste, uma implementação que devolvesse qualquer um deles (ou o mais recente) passaria
// nos anteriores — e a série ficaria a dizer que a espera é curta enquanto alguém espera há horas.
func TestOMaisAntigoEQueManda(t *testing.T) {
	svc := svcComSuspensos(t, 30*time.Second, "recente")
	svc.suspended["antigo"] = &runState{
		runID: "antigo", suspended: true, done: make(chan struct{}),
		suspendedAtUnix: time.Now().Add(-3 * time.Hour).Unix(),
	}
	corpo := metricasDe(t, &apiHandler{node: &Node{}, svc: svc})

	idade, ok := valorDe(t, corpo, "aos_runs_suspended_oldest_age_seconds")
	if !ok {
		t.Fatal("a idade nao saiu")
	}
	if idade < 3*3600*0.9 {
		t.Errorf("idade = %v — devia ser a do MAIS ANTIGO (~3h), nao a do mais recente", idade)
	}
}

// TestUmSuspensoSemCarimboNaoInventaIdade — fail-closed da série.
//
// Um `runState` sem carimbo (injectado por teste, ou vindo de um caminho que não o preencha) tem
// `suspendedAtUnix == 0`, e `time.Unix(0,0)` é 1970: a idade sairia em DÉCADAS. Uma série que
// grita não é um alarme, é ruído que se aprende a ignorar.
func TestUmSuspensoSemCarimboNaoInventaIdade(t *testing.T) {
	svc := svcComSuspensos(t, 0)
	svc.suspended["sem-carimbo"] = &runState{runID: "sem-carimbo", suspended: true, done: make(chan struct{})}
	corpo := metricasDe(t, &apiHandler{node: &Node{}, svc: svc})

	if n, _ := valorDe(t, corpo, "aos_runs_suspended"); n != 1 {
		t.Errorf("a CONTAGEM tem de o incluir (ele espera mesmo); veio %v", n)
	}
	if idade, ok := valorDe(t, corpo, "aos_runs_suspended_oldest_age_seconds"); ok {
		t.Errorf("saiu idade %v a partir de um carimbo em falta — sao decadas desde 1970", idade)
	}
}

// TestUmRunREALMENTESuspensoAparece é a CABLAGEM, e é a décima primeira vez que este padrão
// aparece no repositório.
//
// Os quatro testes acima INJECTAM `suspendedAtUnix` à mão. Uma mutação que removesse o carimbo do
// caminho real — onde o run entra no balde, em `service.go` — passava em todos: as séries
// continuam correctas e nenhum run real alguma vez lhes chega com idade.
func TestUmRunREALMENTESuspensoAparece(t *testing.T) {
	h := newACNHarness(t)
	h.submete(t)
	if _, susp := h.svc.Suspended(t.Context(), acnRunID); !susp {
		t.Fatal("o run devia ter suspendido — o cenario nao esta montado")
	}
	corpo := metricasDe(t, &apiHandler{node: h.node, svc: h.svc})

	n, ok := valorDe(t, corpo, "aos_runs_suspended")
	if !ok || n != 1 {
		t.Fatalf("um run REAL suspenso devia dar aos_runs_suspended=1; ok=%t v=%v", ok, n)
	}
	if _, ok := valorDe(t, corpo, "aos_runs_suspended_oldest_age_seconds"); !ok {
		t.Error("o run REAL entrou no balde SEM carimbo — a idade nunca sai em producao, e a " +
			"contagem sozinha nao distingue «escalada agora» de «ninguem decide ha horas»")
	}
}

// TestUmSemCarimboNaoAPAGAAIdadeDeOutro é a lacuna que a mutação N3 revelou.
//
// A mutação «um carimbo em falta passa a valer 1970» não caía, porque os testes acima só tinham
// entradas SEM carimbo — e nesse caso o `maisAntigo` fica a 0 de qualquer forma e a série é
// filtrada pelo `> 0`.
//
// O caso real é a MISTURA: um run sem carimbo ao lado de um que espera há horas. Sem o guarda, o
// zero do primeiro ganha a comparação e a idade DESAPARECE — a série cala-se exactamente quando
// tem mais a dizer.
func TestUmSemCarimboNaoAPAGAAIdadeDeOutro(t *testing.T) {
	svc := svcComSuspensos(t, 4*time.Hour, "espera-ha-horas")
	svc.suspended["sem-carimbo"] = &runState{runID: "sem-carimbo", suspended: true, done: make(chan struct{})}

	corpo := metricasDe(t, &apiHandler{node: &Node{}, svc: svc})
	if n, _ := valorDe(t, corpo, "aos_runs_suspended"); n != 2 {
		t.Errorf("a contagem devia incluir ambos; veio %v", n)
	}
	idade, ok := valorDe(t, corpo, "aos_runs_suspended_oldest_age_seconds")
	if !ok {
		t.Fatal("a idade DESAPARECEU por causa de uma entrada sem carimbo — a serie cala-se " +
			"exactamente quando tem mais a dizer")
	}
	if idade < 4*3600*0.9 {
		t.Errorf("idade = %v, esperava ~4h", idade)
	}
}
