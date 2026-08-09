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
)

// sealTerminal materializa o estado TERMINAL do run no log durável. É NO-OP quando a
// máquina não está em `running` — os desfechos já materializados (paused, waiting_on_human,
// timed_out, killed) pertencem a outros condutores e não se reescrevem, e um run que nunca
// foi reclamado (ready) não tem falha a atribuir (não chegou a executar).
//
// O fencing token do lease acompanha o evento (correlação AOS-018); não é pré-condição —
// só o claim ready→running a exige. Uma falha da transição é devolvida ao chamador (o
// serviço regista-a em voz alta: sem ela o log volta à ambiguidade de F4).
func (g *runGate) sealTerminal(ctx context.Context, res agentruntime.Result, runErr error, panicked bool) error {
	if g.m.Current() != state.Running {
		return nil
	}
	switch {
	case panicked:
		return g.m.Transition(ctx, state.Failed, state.TransitionEvent{Token: g.token, Reason: reasonRunPanicked})
	case runErr == nil && res.Terminated:
		return g.m.Transition(ctx, state.Complete, state.TransitionEvent{Token: g.token, Reason: reasonRunComplete})
	default:
		// Erro de loop, MaxTurns esgotado, cancelamento — qualquer saída não-terminada é
		// uma FALHA durável (recuperável: a aresta failed→compensating é da saga, AOS-254).
		return g.m.Transition(ctx, state.Failed, state.TransitionEvent{Token: g.token, Reason: reasonRunFailed})
	}
}

// sealTerminalState sela o desfecho do run no log durável, na saída do hostRun. Corre com
// context.Background() de propósito: o selo é um facto de auditoria que NÃO pode ser
// cancelado com o ctx do run (shutdown/deadline) — senão o fim de um run cancelado voltava
// a ser invisível no log. Uma falha é registada em voz alta e NÃO sobrepõe o desfecho em
// memória (o trabalho já aconteceu; o que falhou foi o registo).
func (s *NodeService) sealTerminalState(rs *runState, res agentruntime.Result, runErr error, panicked bool) {
	if s.node == nil || s.node.stateGates == nil {
		return
	}
	gate := s.node.stateGates.resolveGate(rs.runID)
	if gate == nil {
		return
	}
	if err := gate.sealTerminal(context.Background(), res, runErr, panicked); err != nil {
		s.log("selo do estado terminal do run %q FALHOU — o log duravel fica sem desfecho (indistinguivel de crash, F4): %v", rs.runID, err)
	}
}
