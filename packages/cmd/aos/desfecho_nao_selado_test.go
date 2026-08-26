package main

// DESFECHO NÃO SELADO — a declaração dos três silêncios de [NodeService.sealTerminalState].
//
// O selo do estado terminal tinha três saídas MUDAS: sem máquina de estados composta, sem
// gate resolvido para o run, e o no-op de [runGate.sealTerminal] quando a máquina não está
// em `running` (que devolve `(estadoCorrente, nil)` — indistinguível, para o chamador, de
// uma transição bem sucedida). Em qualquer delas o run terminava em memória e o log durável
// ficava sem o fim registado, sem uma palavra.
//
// O efeito só aparece muito depois e noutro sítio: enquanto o desfecho viver no cache em
// memória o `GET /runs/{id}` responde-o; quando a poda FIFO ou um restart o levarem, a mesma
// leitura passa a 404 — que o handler documenta como «nunca existiu».
//
// Estes testes trancam as DUAS metades, porque só as duas juntas tornam a declaração útil:
// que ela FALA na divergência, e que se CALA nos no-ops que o código documenta como
// legítimos. Uma declaração que falasse sempre seria ruído sobre o caminho mais sensível do
// sistema, e o operador aprenderia a ignorá-la.

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// marcaDeclaracao é a âncora estável da declaração no log do operador. Aparece nos dois
// sentidos do teste (presença e ausência), pelo que uma mudança de texto que a quebre falha
// os dois — e não deixa passar um teste que deixou de observar o que dizia observar.
const marcaDeclaracao = "desfecho NAO SELADO"

// nodeServiceSoComLog constrói o mínimo para exercitar o caminho de selo: um serviço cujo
// `node` é nil (primeiro dos três silêncios) e cujo log é capturável.
func nodeServiceSoComLog(buf *bytes.Buffer) *NodeService {
	return &NodeService{logw: buf}
}

// TestDesfechoNaoSelado_DeclaraQuandoORunTerminou cobre a metade POSITIVA: o run terminou em
// memória e o log durável não ficou com o fim registado ⇒ o operador tem de o saber.
//
// Os três tuplos são os três desfechos que [runGate.sealTerminal] classificaria como
// terminais se a máquina estivesse em `running` — sucesso, erro de loop e panic recuperado.
// Cobri-los aos três impede que a guarda `terminouEmMemoria` seja escrita a olhar só para
// `res.Terminated`, que deixaria o erro e o panic a passar em silêncio.
func TestDesfechoNaoSelado_DeclaraQuandoORunTerminou(t *testing.T) {
	casos := []struct {
		nome     string
		res      agentruntime.Result
		runErr   error
		panicked bool
	}{
		{"resultado terminado", agentruntime.Result{Terminated: true}, nil, false},
		{"erro de loop", agentruntime.Result{}, errors.New("falha do loop"), false},
		{"panic recuperado", agentruntime.Result{}, nil, true},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			var buf bytes.Buffer
			s := nodeServiceSoComLog(&buf)

			s.sealTerminalState(&runState{runID: "run-divergente"}, "nhi:titular", c.res, c.runErr, c.panicked)

			saida := buf.String()
			if !strings.Contains(saida, marcaDeclaracao) {
				t.Fatalf("o desfecho NAO foi selado e o nó CALOU-SE.\nlog: %q", saida)
			}
			// A declaração sem o run a que pertence não é accionável: o operador não sabe o
			// que investigar.
			if !strings.Contains(saida, "run-divergente") {
				t.Fatalf("declaração sem identificar o run.\nlog: %q", saida)
			}
		})
	}
}

// TestDesfechoNaoSelado_CalaSeONoOpForLegitimo cobre a metade NEGATIVA — a que impede o
// ruído. Um run PARADO (steer/disjuntor) e um À ESPERA DE HUMANO (escalada) não terminaram:
// a ausência de estado terminal no log é exactamente o que se quer, e declará-la seria
// afirmar uma avaria que não existe.
//
// Sem este teste, a guarda podia ser removida sem nada ficar vermelho, e a declaração
// passaria a disparar em TODO o run suspenso — que neste nó é o caminho normal de qualquer
// tool call escalada.
func TestDesfechoNaoSelado_CalaSeONoOpForLegitimo(t *testing.T) {
	casos := []struct {
		nome string
		res  agentruntime.Result
	}{
		{"run parado pelo steer ou pelo disjuntor", agentruntime.Result{Paused: true}},
		{"run à espera de decisão humana", agentruntime.Result{Escalated: true}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			var buf bytes.Buffer
			s := nodeServiceSoComLog(&buf)

			s.sealTerminalState(&runState{runID: "run-suspenso"}, "nhi:titular", c.res, nil, false)

			if saida := buf.String(); strings.Contains(saida, marcaDeclaracao) {
				t.Fatalf("RUÍDO: um no-op legítimo foi declarado como divergência.\nlog: %q", saida)
			}
		})
	}
}

// TestDesfechoDuravelRegistado tranca o predicado, e em especial a inclusão de `failed`.
//
// `failed` está DE FORA de [state.IsTerminal] com razão — tem aresta de saída para a saga de
// compensação (AOS-254) e não é absorvente. Mas para a pergunta que o selo faz («o fim do run
// ficou registado?») a resposta é sim, e escrever o predicado como `state.IsTerminal(st)`
// sozinho declararia como não-selado TODO o run falhado. Este caso é o que impede essa
// simplificação de passar despercebida.
func TestDesfechoDuravelRegistado(t *testing.T) {
	registam := []state.State{state.Complete, state.Killed, state.TimedOut, state.Failed}
	for _, st := range registam {
		if !desfechoDuravelRegistado(st) {
			t.Errorf("%q REGISTA o fim do run mas o predicado diz que não", st)
		}
	}
	naoRegistam := []state.State{state.Ready, state.Running, state.Paused, state.WaitingOnHuman}
	for _, st := range naoRegistam {
		if desfechoDuravelRegistado(st) {
			t.Errorf("%q NÃO regista o fim do run mas o predicado diz que sim", st)
		}
	}
}
