package main

import (
	"context"
	"errors"
	"testing"

	durable "github.com/aos-ref/kernel/agent-runtime/durable"
	state "github.com/aos-ref/kernel/agent-runtime/state"
)

// ---------------------------------------------------------------------------------------------
// `paused` DEIXA DE SER UM ESTADO ABSORVENTE.
//
// Achado da verificação de completude de 2026-08-23, e sobreviveu a refutação com agravamento.
//
// Nada no binário conduzia `paused → running`. O condutor EXISTE — [runGate.Resume] — e tinha
// ZERO chamadores de produção, mantido vivo pelo compilador por um
// `var _ control.StateGate = (*runGate)(nil)`. Não era uma peça que ficou noutro pacote: estava
// aqui, ligada à porta, e ninguém a abria.
//
// E o nó ANUNCIAVA o contrário, em dois sítios servidos ao operador: a recusa de
// `POST /exhaustion` e o banner de arranque dizem ambos que a pausa graciosa «deixa o run
// RETOMÁVEL». Postura anunciada ≠ postura ligada.
//
// ALCANÇÁVEL SEM OPERADOR: os defaults do disjuntor (3 iterações estéreis, 30 min de wall-clock)
// levam um deny-loop a `paused` sozinhos.
//
// A ÚNICA SAÍDA QUE EXISTIA era re-submeter o mesmo run id depois de um restart — e era a pior
// possível: `claimRunning` é no-op fora de `Ready`, pelo que a segunda execução corria com o
// disjuntor CEGO, sem selo terminal e sem deadline.
// ---------------------------------------------------------------------------------------------

// runPausado devolve um nó com um run genuinamente PAUSADO na máquina de estados durável.
func runPausado(t *testing.T, runID string) (*Node, *NodeService) {
	t.Helper()
	node, svc, _, _ := aos263Node(t)
	ctx := context.Background()
	aos263TornaRetomavel(t, node, svc, runID)

	g := node.stateGates.resolveGate(runID)
	if g == nil {
		if err := node.stateGates.Open(ctx, runID, durable.FencingToken(1)); err != nil {
			t.Fatalf("Open(%s): %v", runID, err)
		}
		g = node.stateGates.resolveGate(runID)
		if g == nil {
			t.Fatal("o gate nao apareceu depois do Open")
		}
	}
	if err := g.Pause(ctx, "pausa graciosa de teste"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	// FORA DO BALDE EM MEMORIA: sem isto o `Resume` le `susp=true` e NUNCA chega ao caminho
	// duravel — que e o que decide se uma pausa conta como suspensao. A mutacao que remove
	// `state.Paused` de `suspendedDurably` nao caia, e foi assim que se soube.
	//
	// E tambem o cenario REAL que interessa: depois de um restart o balde esta vazio.
	svc.mu.Lock()
	delete(svc.suspended, runID)
	svc.completed[runID] = &runState{runID: runID, done: make(chan struct{})}
	svc.mu.Unlock()

	// CONTROLO DO CENÁRIO: sem isto o teste mediria outra coisa.
	if st, _ := svc.DurableState(ctx, runID); st != state.Paused {
		t.Fatalf("o run devia estar PAUSADO na maquina duravel; esta em %q", st)
	}
	return node, svc
}

func TestUmRunPAUSADOERetomavel(t *testing.T) {
	const run = "run-pausa-saida"
	_, svc := runPausado(t, run)

	err := svc.Resume(context.Background(), run, "cred-fresca")
	if errors.Is(err, ErrRunNotSuspended) {
		t.Fatalf("um run PAUSADO foi recusado como nao-suspenso — `paused` continua absorvente, "+
			"e o no anuncia ao operador que a pausa graciosa o deixa RETOMAVEL: %v", err)
	}
}

// TestARetomaDeUmaPausaPOEOrunEmRunning é a metade que interessa à segurança.
//
// Aceitar a retoma e deixar a máquina em `paused` seria pior do que recusar: o disjuntor
// (`CountsAsActiveWork` exige `Running`), o selo terminal e o deadline ficariam TODOS desarmados
// durante toda a nova hospedagem — que é exactamente o que a re-submissão fazia.
func TestARetomaDeUmaPausaPOEOrunEmRunning(t *testing.T) {
	const run = "run-pausa-running"
	node, _ := runPausado(t, run)
	ctx := context.Background()

	g := node.stateGates.resolveGate(run)
	if g == nil {
		t.Fatal("gate do run desapareceu")
	}
	if err := g.resumeIfWaiting(ctx); err != nil {
		t.Fatalf("resumeIfWaiting sobre um run pausado: %v", err)
	}
	if got := g.m.Current(); got != state.Running {
		t.Errorf("depois da retoma a maquina esta em %q e devia estar em %q — com ela em `paused` "+
			"o disjuntor, o selo terminal e o deadline ficam todos desarmados", got, state.Running)
	}
}

// TestUmRunTERMINADOContinuaARecusar é o controlo, e guarda uma decisão que já existia.
//
// A contabilidade LOCAL de um desfecho sobrepõe-se ao log: um run que ESTA réplica viu terminar
// não é retomável, mesmo que o durável diga outra coisa. Sem este ramo, «aceitar tudo o que não
// esteja a correr» passaria no teste acima e reabriria runs terminados.
func TestUmRunTERMINADOContinuaARecusar(t *testing.T) {
	node, svc, _, _ := aos263Node(t)
	_ = node
	const run = "run-terminado"
	svc.mu.Lock()
	svc.completed[run] = &runState{runID: run, err: errors.New("falhou")}
	svc.mu.Unlock()

	if err := svc.Resume(context.Background(), run, "x"); !errors.Is(err, ErrRunNotSuspended) {
		t.Errorf("um run TERMINADO nesta replica devia continuar a recusar; veio %v", err)
	}
}
