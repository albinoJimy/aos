package main

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"

	durable "github.com/aos-ref/kernel/agent-runtime/durable"
	state "github.com/aos-ref/kernel/agent-runtime/state"
)

// O READ-PATH TEM DE SER EXAUSTIVO SOBRE A TABELA CANONICA DE ESTADOS.
//
// O `compensating` caia no 404 uniforme por OMISSAO — ninguem o tinha enumerado no switch do
// handleGet. E a diferenca entre isso e o 404 do `ready`/`running` e a diferenca entre um
// esquecimento e uma decisao: os segundos caem la por escolha DECLARADA (um orfao sem desfecho
// nao se distingue de um inexistente, AOS-253); o primeiro simplesmente nao estava na lista.
//
// Um run interrompido a meio da compensacao lia-se como um run que NUNCA EXISTIU.
//
// Este gate nao verifica o `compensating`: verifica que NENHUM dos dez estados canonicos pode
// ficar de fora sem alguem escrever porque. `state.AllStates` existe exactamente para
// "qualquer enumeracao exaustiva" — e esta e uma.
//
// COMO SE ACTUALIZA quando um estado novo entrar na tabela: ou se lhe da representacao no
// read-path (e acrescenta-se a `representados`), ou se declara aqui a razao do 404. As duas
// exigem uma frase escrita; nenhuma e o silencio que este gate fecha.
func TestReadPath_ExaustivoSobreOsEstadosCanonicos(t *testing.T) {
	// (a) ESTADOS COM REPRESENTACAO no read-path.
	representados := map[state.State]bool{
		state.Complete:       true, // "completed"
		state.Failed:         true, // "failed"
		state.TimedOut:       true, // "timed_out"
		state.Killed:         true, // "killed"
		state.Paused:         true, // "paused" (achado C)
		state.WaitingOnHuman: true, // ramo de suspensao, antes do switch
		state.Compensating:   true, // "compensating" — o que este ticket acrescenta
	}

	// (b) ESTADOS QUE CAEM NO 404 POR DECISAO, cada um com a razao escrita. A razao nao e
	// decorativa: e o que distingue uma omissao de uma escolha, e e o que um leitor futuro
	// tem de conseguir contestar.
	quatroCentoEQuatro := map[state.State]string{
		state.Ready: "um run em `ready` nao tem desfecho a reportar — ou nunca arrancou, ou e um " +
			"orfao pos-crash (AOS-253). A leitura e um caminho de CONSULTA e o 404 uniforme e " +
			"NAO-ENUMERAVEL de proposito: nada distingue `nunca existiu` de `existe e nao e seu`.",
		state.Running: "um run em `running` que esta replica NAO hospeda e um orfao sem desfecho; " +
			"o que ela hospeda sai por `InProgress()` como `in_progress`, antes do switch.",
		state.WaitingOnTool: "estado da tabela canonica que NENHUM condutor de producao materializa " +
			"(grep: so aparece em liveness/waiting_states.go). Uma aresta morta, nao um risco — " +
			"e representa-lo seria anunciar um estado que o binario nunca produz.",
	}

	var semTratamento []string
	for _, st := range state.AllStates {
		_, temRep := representados[st]
		razao, tem404 := quatroCentoEQuatro[st]

		if !temRep && !tem404 {
			semTratamento = append(semTratamento, string(st))
			continue
		}
		if temRep && tem404 {
			t.Errorf("estado %q esta nas DUAS listas — decida-se: ou tem representacao, ou tem razao para o 404", st)
		}
		if tem404 && strings.TrimSpace(razao) == "" {
			t.Errorf("estado %q declara-se 404 com razao VAZIA — e o silencio que este gate fecha", st)
		}
	}
	sort.Strings(semTratamento)
	if len(semTratamento) > 0 {
		t.Fatalf("estado(s) canonico(s) %v sem representacao no read-path E sem razao declarada para o 404.\n"+
			"Foi assim que o `compensating` passou anos a ler-se como `nunca existiu`.\n"+
			"Ou lhe da representacao no switch do handleGet, ou escreva aqui porque nao tem.", semTratamento)
	}

	// CONTROLO ANTI-VACUIDADE (1): as duas listas juntas TEM de cobrir a tabela inteira, e a
	// tabela tem de ter o tamanho que julgamos. Se `AllStates` encolhesse, o teste passaria a
	// verificar menos sem ninguem reparar.
	if n := len(representados) + len(quatroCentoEQuatro); n != len(state.AllStates) {
		t.Fatalf("as listas cobrem %d estados mas a tabela canonica tem %d — uma das duas ficou para tras", n, len(state.AllStates))
	}
	// CONTROLO ANTI-VACUIDADE (2): as duas listas tem de ser NAO-VAZIAS. Um gate em que tudo
	// caisse numa das colunas nao distinguiria nada — se todos fossem "404 declarado", o
	// read-path podia nao representar estado nenhum e isto continuaria verde.
	if len(representados) == 0 || len(quatroCentoEQuatro) == 0 {
		t.Fatal("uma das listas esta vazia — o gate deixou de distinguir representacao de 404 declarado")
	}
	// CONTROLO ANTI-VACUIDADE (3): nenhum estado desconhecido entrou nas listas. Um typo num
	// nome faria o estado real ficar sem tratamento E o gate continuar verde pela contagem.
	for st := range representados {
		if !state.IsKnown(st) {
			t.Errorf("%q nao e um estado canonico — typo na lista de representados", st)
		}
	}
	for st := range quatroCentoEQuatro {
		if !state.IsKnown(st) {
			t.Errorf("%q nao e um estado canonico — typo na lista de 404 declarados", st)
		}
	}
}

// UM RUN EM `compensating` LE-SE COMO `compensating`, E NAO COMO INEXISTENTE.
//
// O gate de exaustividade acima e uma DECLARACAO, e a mutacao provou-o: remover o
// `case state.Compensating` do handleGet NAO o fazia cair. Duas listas mantidas a mao dizem o
// que DEVIA estar representado; so um pedido real prova que ESTA. E a decima sexta vez que o
// padrao de cablagem aparece neste repositorio — desta vez no meu proprio trabalho, e foi a
// mutacao que o mostrou.
func TestReadPath_CompensatingNaoSeLeComoInexistente(t *testing.T) {
	const run = "run-a-compensar"
	node, svc, h := runEmCompensacao(t, run)
	_ = node

	// PRECONDICAO: o estado duravel e mesmo `compensating`. Sem isto o teste poderia estar a
	// medir um 200 vindo de outro ramo do handleGet.
	if st, _ := svc.DurableState(context.Background(), run); st != state.Compensating {
		t.Fatalf("PRECONDICAO: o run devia estar em compensating, esta em %q", st)
	}
	// PRECONDICAO 2: a replica NAO pode conhecer o run em memoria — e o cenario que importa
	// (o processo morreu a meio da compensacao) e e o unico que chega ao switch.
	if _, susp := svc.Suspended(context.Background(), run); susp {
		t.Fatal("PRECONDICAO: o run nao devia sair pelo ramo de suspensao")
	}

	codigo, corpo := leRun(t, h, run)
	if codigo == http.StatusNotFound {
		t.Fatal("um run a COMPENSAR responde 404 — indistinguivel de um run que nunca existiu")
	}
	if codigo != http.StatusOK {
		t.Fatalf("HTTP %d, queria 200", codigo)
	}
	if got := corpo["status"]; got != string(state.Compensating) {
		t.Fatalf("status=%v, queria %q", got, state.Compensating)
	}
	// A compensacao NAO terminou o run: esta a desfaze-lo.
	if corpo["terminated"] == true {
		t.Fatal("um run a compensar veio marcado como terminado")
	}
}

// CONTROLO ANTI-VACUIDADE: UM RUN INEXISTENTE CONTINUA A DAR 404.
//
// Sem este caso, um handleGet que devolvesse 200 para tudo passaria o teste de cima — e a
// nao-enumerabilidade, que e uma propriedade de SEGURANCA deste endpoint, teria sido
// destruida sem ninguem reparar.
func TestReadPath_InexistenteContinuaNaoEnumeravel(t *testing.T) {
	node, _, h := runEmCompensacao(t, "run-a-compensar-controlo")
	_ = node

	if codigo, _ := leRun(t, h, "run-que-nunca-existiu"); codigo != http.StatusNotFound {
		t.Fatalf("CONTROLO: um run inexistente devia dar 404, veio %d — a nao-enumerabilidade caiu", codigo)
	}
	// E um run em `ready`, que e 404 POR DECISAO declarada, tambem.
	if codigo, _ := leRun(t, h, "run-em-ready-nunca-arrancado"); codigo != http.StatusNotFound {
		t.Fatalf("CONTROLO: um run sem transicoes devia dar 404, veio %d", codigo)
	}
}

// runEmCompensacao conduz um run ate `compensating` pela tabela canonica
// (ready->running->failed->compensating) e deixa-o FORA dos baldes em memoria, que e o
// cenario de um processo que morreu a meio da saga.
func runEmCompensacao(t *testing.T, runID string) (*Node, *NodeService, http.Handler) {
	t.Helper()
	node, svc, h, _ := aos263Node(t)
	ctx := context.Background()
	aos263TornaRetomavel(t, node, svc, runID)

	g := node.stateGates.resolveGate(runID)
	if g == nil {
		if err := node.stateGates.Open(ctx, runID, durable.FencingToken(1)); err != nil {
			t.Fatalf("Open: %v", err)
		}
		g = node.stateGates.resolveGate(runID)
	}
	if g == nil {
		t.Fatal("o gate nao apareceu depois do Open")
	}
	// `ready->running` exige fencing token — usa-se o condutor REAL, que o traz do lease.
	if err := g.claimRunning(ctx); err != nil {
		t.Fatalf("claimRunning: %v", err)
	}
	for _, alvo := range []state.State{state.Failed, state.Compensating} {
		if err := g.m.Transition(ctx, alvo, state.TransitionEvent{Reason: "teste: caminho canonico ate a saga"}); err != nil {
			t.Fatalf("Transition(%s): %v", alvo, err)
		}
	}
	// FORA DOS BALDES: e o que faz a leitura cair no switch sobre o estado duravel, que e o
	// caminho sob teste. Com o run em memoria a resposta viria de outro ramo.
	svc.mu.Lock()
	delete(svc.suspended, runID)
	delete(svc.completed, runID)
	delete(svc.runs, runID)
	svc.mu.Unlock()
	return node, svc, h
}
