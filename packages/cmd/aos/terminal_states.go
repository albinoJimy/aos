package main

// ESTADOS TERMINAIS DURÁVEIS (AOS-252, achado F4).
//
// Antes deste ticket o desfecho de um run vivia APENAS no mapa em memória do serviço
// (`completed`, com poda FIFO): um run acabado por erro/panic/MaxTurns era, no log durável,
// INDISTINGUÍVEL de um crash a meio — a máquina de estados ficava em `running` para sempre.
// A tabela declarativa de AOS-017 tem as arestas running→complete e running→failed; este
// ficheiro é o seu ÚNICO condutor de produção.
//
// O SELO acontece no ponto único de saída do run ([NodeService.hostRun]), DEPOIS de o loop
// devolver (ou de um panic ser apanhado a caminho do recover de isolamento), e só quando a
// máquina está em `running`: os desfechos JÁ materializados por outros condutores — paused
// (steer/breaker), waiting_on_human (escalada), timed_out/killed (deadlines) — não são
// reescritos. Assim o log de um run conta sempre uma destas duas histórias, sem ambiguidade:
// claim → … → TERMINAL (fim conhecido) ou claim → … → NADA (crash/orfão, AOS-253).

import (
	"context"
	"errors"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// Razões canónicas gravadas nos eventos de transição terminal (rótulos de auditoria
// legíveis, nunca segredos) — atribuem a causa do desfecho.
const (
	// reasonRunComplete — running→complete: o loop atingiu uma resposta final.
	reasonRunComplete = "run_complete"
	// reasonRunFailed — running→failed: o loop devolveu erro (modelo, mediação fatal,
	// captura, MaxTurns esgotado, cancelamento, ...).
	reasonRunFailed = "run_failed"
	// reasonRunPanicked — running→failed: o run PANICOU e o panic foi recuperado pelo
	// isolamento por-run. Razão distinta para a auditoria distinguir falha de crash lógico.
	reasonRunPanicked = "run_panicked"
	// reasonMaxTurnsExhausted — running→timed_out por esgotamento do ORÇAMENTO DE TURNOS.
	// Rótulo DELIBERADAMENTE distinto do wall_clock_exceeded de [state.Machine.CheckDeadlines]
	// e do tecto do disjuntor: as três causas partilham o estado `timed_out` e só o rótulo as
	// torna atribuíveis no log.
	reasonMaxTurnsExhausted = "max_turns_exhausted"
	// reasonBudgetExhausted — running→timed_out por esgotamento do ORÇAMENTO DE GASTO
	// (AOS-260): a admissão do turno de modelo negou headroom e o loop parou ANTES da
	// inferência, sem retentar.
	//
	// `timed_out` e NÃO `failed` pela MESMA razão de [reasonMaxTurnsExhausted], e com mais
	// força ainda: um tecto de gasto atingido é um TECTO DEFENSIVO excedido, não uma falha
	// recuperável — propor-lhe a saga de rollback (failed→compensating, AOS-254) seria
	// desfazer efeitos legítimos por o run ter ficado sem orçamento. O que se quer é levantar
	// o tecto e re-correr. O rótulo é distinto dos outros três que partilham `timed_out`
	// (max_turns, wall-clock do disjuntor, deadline) porque só o rótulo torna a causa
	// atribuível no log.
	reasonBudgetExhausted = "budget_exhausted"
)

// sealTerminal materializa o estado TERMINAL do run no log durável. É NO-OP quando a
// máquina não está em `running` — os desfechos já materializados (paused, waiting_on_human,
// timed_out, killed) pertencem a outros condutores e não se reescrevem, e um run que nunca
// foi reclamado (ready) não tem falha a atribuir (não chegou a executar).
//
// O fencing token do lease acompanha o evento (correlação AOS-018); não é pré-condição —
// só o claim ready→running a exige. Uma falha da transição é devolvida ao chamador (o
// serviço regista-a em voz alta: sem ela o log volta à ambiguidade de F4).
// sealTerminal devolve o estado TERMINAL para que materializou (para o chamador saber se foi
// `failed` — a origem da saga de rollback, AOS-254) e o erro da transição. Um no-op (máquina fora
// de `running`) devolve o estado corrente sem erro. Numa FALHA da transição devolve o estado
// corrente (que não mudou) e o erro — o chamador não conduz a saga sobre um selo que não pegou.
func (g *runGate) sealTerminal(ctx context.Context, res agentruntime.Result, runErr error, panicked bool) (state.State, error) {
	if g.m.Current() != state.Running {
		return g.m.Current(), nil
	}
	var (
		to     state.State
		reason string
	)
	switch {
	case panicked:
		to, reason = state.Failed, reasonRunPanicked
	case runErr == nil && res.Terminated:
		to, reason = state.Complete, reasonRunComplete
	case runErr == nil && res.BudgetExhausted:
		// DEGRADAÇÃO DECLARADA (AOS-260): o loop parou por falta de orçamento, sem erro e sem
		// resposta final. Sem este ramo cairia no `default` e o log diria `failed` — a causa
		// errada, e ainda por cima a única que dispara a saga de compensação.
		to, reason = state.TimedOut, reasonBudgetExhausted
	case errors.Is(runErr, agentruntime.ErrMaxTurnsExceeded):
		// ORÇAMENTO DE TURNOS ESGOTADO ⇒ `timed_out`, NÃO `failed`. A distinção não é
		// cosmética: na tabela de AOS-017 `failed` é a falha RECUPERÁVEL cuja única aresta
		// de saída é failed→compensating — a saga de rollback (AOS-254). Propor uma
		// compensação a um run que apenas ficou sem turnos é a recuperação errada; o que se
		// quer é re-correr com mais orçamento. `timed_out` é o estado dos TECTOS DEFENSIVOS
		// excedidos, absorvente por construção, e é para onde o disjuntor já manda o seu
		// próprio tecto de wall-clock — mandar MaxTurns para `failed` deixaria dois tectos
		// irmãos em estados diferentes.
		to, reason = state.TimedOut, reasonMaxTurnsExhausted
	default:
		// Erro de loop, cancelamento — qualquer outra saída não-terminada é uma FALHA
		// durável, e aí a saga de compensação é a recuperação certa.
		to, reason = state.Failed, reasonRunFailed
	}
	if err := g.m.Transition(ctx, to, state.TransitionEvent{Token: g.token, Reason: reason}); err != nil {
		return g.m.Current(), err
	}
	return to, nil
}

// sealTerminalState sela o desfecho do run no log durável, na saída do hostRun. Corre com
// context.Background() de propósito: o selo é um facto de auditoria que NÃO pode ser
// cancelado com o ctx do run (shutdown/deadline) — senão o fim de um run cancelado voltava
// a ser invisível no log. Uma falha é registada em voz alta e NÃO sobrepõe o desfecho em
// memória (o trabalho já aconteceu; o que falhou foi o registo).
// sealTerminalState sela o desfecho durável e, quando esse desfecho é `failed`, CONDUZ a saga de
// rollback (AOS-254). titular é o Principal.NHIID do run (goal.Principal.NHIID), propagado do
// hostRun: a compensação corre no step-ledger cifrado por-titular, que o recusa se ele faltar.
func (s *NodeService) sealTerminalState(rs *runState, titular string, res agentruntime.Result, runErr error, panicked bool) {
	if s.node == nil || s.node.stateGates == nil {
		return
	}
	gate := s.node.stateGates.resolveGate(rs.runID)
	if gate == nil {
		return
	}
	sealed, err := gate.sealTerminal(context.Background(), res, runErr, panicked)
	if err != nil {
		s.log("selo do estado terminal do run %q FALHOU — o log duravel fica sem desfecho (indistinguivel de crash, F4): %v", rs.runID, err)
		return
	}
	// AOS-254: um desfecho `failed` — a falha RECUPERÁVEL — é a ÚNICA origem da saga de rollback
	// (failed→compensating). Os demais desfechos (complete/timed_out/killed) são absorventes e não
	// têm compensação. A condução decide, atribuivelmente, entre compensar e declarar a ausência.
	if sealed == state.Failed {
		s.driveSagaCompensation(rs, titular)
	}
}
