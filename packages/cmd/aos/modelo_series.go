package main

// modelo_series.go — AS SÉRIES POR MODELO (AOS-520, PR 2; ADR-041 §2.8).
//
// Com um candidato A SERVIR, o `/metrics` ganha três famílias NOVAS, com o modelo no rótulo, ao lado
// das que já existiam (que não mudam de nome, de rótulos nem de valor):
//
//   - `aos_model_turns_by_model_total{model,stop_reason}` — turnos AO VIVO, por motivo de paragem;
//   - `aos_model_response_rejected_by_model_total{model,causa}` — respostas que o gateway recusou;
//   - `aos_runs_finished_by_model_total{model,outcome,reason}` — runs selados, por estado e razão.
//
// O RÓTULO `model` É DE CONJUNTO FECHADO: o alias do titular e o do candidato a servir — os nomes
// PEDIDOS, que vêm da configuração do nó e do registo assinado. Nunca o modelo que o fornecedor
// declarou ter servido: esse é texto de terceiros, e uma série por valor dele não tinha tecto. Um
// alias fora do conjunto não tem contador e não soma.
//
// SÓ EXISTEM COM UM CANDIDATO A SERVIR (interruptor a `on`, um registo não-titular, fatia > 0). Sem
// ele — sem pasta, com `off`, com a fatia a 0 — não se cria contador nenhum e o `/metrics` é o de
// antes, byte a byte. Em particular: um run já fixado no candidato que acabe depois de o
// interruptor passar a `off` conta nas famílias de sempre, e não aqui.
//
// O TECTO: dois modelos ([maxModelosNasSeries]) — o nó não arranca com `on` e mais de um candidato.
// [maxSeriesPorModelo] é o total das três famílias, e está preso por teste.

import (
	"strings"
	"sync/atomic"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// maxModelosNasSeries é quantos valores o rótulo `model` pode ter: o titular e um candidato (D6).
const maxModelosNasSeries = 2

// maxSeriesPorModelo é o tecto das três famílias juntas: por modelo, os motivos de paragem, as
// causas de rejeição, e os estados terminais vezes as razões do veredicto (com `none`).
func maxSeriesPorModelo() int {
	porModelo := len(agentruntime.StopReasons()) + len(modelgateway.ResponseRejectionCauses()) +
		3*(1+len(agentruntime.OutcomeReasons()))
	return maxModelosNasSeries * porModelo
}

// seriesPorModelo conta, por processo e desde o arranque, os turnos, as respostas recusadas e os
// desfechos de run por modelo PEDIDO. Um ponteiro nil é «sem candidato a servir»: todos os métodos
// são nil-safe e não fazem nada.
type seriesPorModelo struct {
	// modelos é o conjunto fechado do rótulo, pela ordem em que sai: o titular e o candidato.
	modelos []string
	motivos []agentruntime.StopReason
	causas  []string
	estados []state.State
	razoes  []agentruntime.OutcomeReason

	turnos     map[string]map[agentruntime.StopReason]*atomic.Int64
	recusadas  map[string]map[string]*atomic.Int64
	terminados map[string]map[state.State]map[agentruntime.OutcomeReason]*atomic.Int64
}

// novasSeriesPorModelo cria as séries do titular e do candidato. Um candidato com o nome do titular
// (que a composição não produz) não duplica o valor do rótulo.
func novasSeriesPorModelo(titular, candidato string) *seriesPorModelo {
	s := &seriesPorModelo{
		modelos:    []string{titular},
		motivos:    agentruntime.StopReasons(),
		causas:     modelgateway.ResponseRejectionCauses(),
		estados:    []state.State{state.Complete, state.Failed, state.TimedOut},
		razoes:     append([]agentruntime.OutcomeReason{agentruntime.OutcomeFulfilled}, agentruntime.OutcomeReasons()...),
		turnos:     map[string]map[agentruntime.StopReason]*atomic.Int64{},
		recusadas:  map[string]map[string]*atomic.Int64{},
		terminados: map[string]map[state.State]map[agentruntime.OutcomeReason]*atomic.Int64{},
	}
	if candidato != "" && candidato != titular {
		s.modelos = append(s.modelos, candidato)
	}
	for _, m := range s.modelos {
		s.turnos[m] = map[agentruntime.StopReason]*atomic.Int64{}
		for _, motivo := range s.motivos {
			s.turnos[m][motivo] = new(atomic.Int64)
		}
		s.recusadas[m] = map[string]*atomic.Int64{}
		for _, causa := range s.causas {
			s.recusadas[m][causa] = new(atomic.Int64)
		}
		s.terminados[m] = map[state.State]map[agentruntime.OutcomeReason]*atomic.Int64{}
		for _, e := range s.estados {
			s.terminados[m][e] = map[agentruntime.OutcomeReason]*atomic.Int64{}
			for _, r := range s.razoes {
				s.terminados[m][e][r] = new(atomic.Int64)
			}
		}
	}
	return s
}

// observarTurno conta o resultado de UM pedido ao vivo ao modelo de alias `alias` — o alias que o
// despachante escolheu, nunca o que a resposta declara. Com resposta, conta o turno pelo motivo de
// paragem; com erro, conta-o se for o de uma resposta recusada ([modelgateway.ResponseRejectionCause]).
// Qualquer outro erro (rede, um status que não é 200, uma negação da pipeline) não conta aqui.
func (s *seriesPorModelo) observarTurno(alias string, resp agentruntime.ModelResponse, err error) {
	if s == nil {
		return
	}
	if err != nil {
		if c := s.recusadas[alias][modelgateway.ResponseRejectionCause(err)]; c != nil {
			c.Add(1)
		}
		return
	}
	if c := s.turnos[alias][resp.StopReason.Normalizado()]; c != nil {
		c.Add(1)
	}
}

// contarDesfecho conta um run que este processo selou em `selado`, servido pelo modelo de alias
// `alias` — o que ficou no Goal do run. Um run sem modelo no conjunto (o recusado pela fixação, que
// não chegou a ter modelo) e um estado fora dos três (o selo foi no-op) não contam.
func (s *seriesPorModelo) contarDesfecho(alias string, selado state.State, res agentruntime.Result) {
	if s == nil {
		return
	}
	razao := agentruntime.OutcomeFulfilled
	if res.Verdict != nil {
		razao = res.Verdict.Reason
	}
	if c := s.terminados[alias][selado][razao]; c != nil {
		c.Add(1)
	}
}

func (s *seriesPorModelo) lidoTurnos(modelo string, motivo agentruntime.StopReason) int64 {
	if s == nil || s.turnos[modelo][motivo] == nil {
		return 0
	}
	return s.turnos[modelo][motivo].Load()
}

func (s *seriesPorModelo) lidoRecusadas(modelo, causa string) int64 {
	if s == nil || s.recusadas[modelo][causa] == nil {
		return 0
	}
	return s.recusadas[modelo][causa].Load()
}

func (s *seriesPorModelo) lidoTerminados(modelo string, estado state.State, razao agentruntime.OutcomeReason) int64 {
	if s == nil || s.terminados[modelo][estado][razao] == nil {
		return 0
	}
	return s.terminados[modelo][estado][razao].Load()
}

// rotuloDoModelo é o valor do rótulo `model`: o alias, com os três caracteres que o formato de
// exposição obriga a escapar. O alias é configuração do nó, não texto do fornecedor.
func rotuloDoModelo(alias string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(alias)
}

// seriesDoModelo devolve as séries por modelo do nó, ou nil sem candidato a servir.
func (n *Node) seriesDoModelo() *seriesPorModelo {
	if n == nil || n.perfisDaRota == nil {
		return nil
	}
	return n.perfisDaRota.porModelo
}
