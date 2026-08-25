package main

// VARRIMENTO DE DEADLINES DURÁVEIS (AOS-252) — o caller periódico de
// [state.Machine.CheckDeadlines] que AOS-019 (liveness/doc.go) exige e que não existia:
// sem ele, o kill fail-closed de ADR-013 e o tecto de wall-clock em `running` eram letra
// morta — [CheckDeadlines] tinha ZERO chamadores de produção.
//
// O QUE ESTE VARRIMENTO COBRE (e o que NÃO cobre):
//
//   - running→timed_out (ReasonWallClockTimeout): o BACKSTOP de um run preso A MEIO de um
//     turno (uma chamada ao modelo/tool pendurada nunca chega à fronteira de fim-de-turno,
//     onde o breaker de AOS-080/081 avalia o MESMO tecto). O tecto é UM SÓ conceito —
//     AOS_BREAKER_MAX_WALL_CLOCK — aplicado em dois pontos de enforcement: o breaker na
//     fronteira de fim-de-turno (pára o run com veredicto) e este varrimento entre turnos
//     (materializa o estado; não duplica o breaker, cobre o que ele não alcança).
//   - waiting_on_human→killed fica DESLIGADO de propósito (humanTTL=0): a decisão do dono
//     em approval_sweeper.go é que um pendente expirado deixa o run RETOMÁVEL, não morto.
//
// O varrimento é BEST-EFFORT e idempotente: um erro de transição é registado e re-tentado
// no tick seguinte; uma máquina sem deadline configurado é no-op.

import (
	"context"
	"time"
)

// DefaultDeadlineSweepInterval é o período de varrimento por omissão — o mesmo do
// varrimento de aprovações: curto o suficiente para o kill fail-closed ser observado com
// pouca latência, longo o suficiente para ser irrelevante no custo do nó.
const DefaultDeadlineSweepInterval = 1 * time.Minute

// WithDeadlineSweepInterval sobrepõe o período do varrimento de deadlines. <= 0 DESLIGA
// (usado em testes que conduzem o varrimento à mão via [NodeService.SweepDeadlinesNow]).
// Arma também a flag Set — sem ela o "explicitamente 0" seria indistinguível de "não
// configurado" e o default ganharia (a desactivação em teste não funcionaria).
func WithDeadlineSweepInterval(d time.Duration) NodeServiceOption {
	return func(c *nodeServiceConfig) {
		c.deadlineSweepInterval = d
		c.deadlineSweepIntervalSet = true
	}
}

// sweepDeadlines é o laço periódico. Termina quando stop fecha (shutdown do serviço).
func (s *NodeService) sweepDeadlines(stop <-chan struct{}) {
	if s.deadlineSweepInterval <= 0 || s.node == nil || s.node.stateGates == nil {
		return // varrimento desligado, ou sem máquinas de estado: nada a varrer
	}
	t := time.NewTicker(s.deadlineSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.sweepDeadlinesOnce(context.Background())
			// Ver a nota em [NodeService]: marca-se na CONCLUSAO.
			s.passagensPrazo.Add(1)
			s.ultimoPrazoUnix.Store(time.Now().Unix())
		}
	}
}

// sweepDeadlinesOnce corre [state.Machine.CheckDeadlines] em cada run EM CURSO (o snapshot
// é do registo de em-curso: runs suspensos/pausados têm o gate fechado e os seus estados
// de espera não têm deadline neste desenho — ver o cabeçalho). Só máquinas com wall-clock
// configurado transicionam; as restantes são no-op barato.
//
// UM DEADLINE QUE DISPARA INTERROMPE O RUN (achado F-A5 da auditoria da W0). Marcar o
// estado sem parar o trabalho seria a pior das divergências estado↔efeito: o operador lê
// `timed_out`, deixa de olhar, e o agente continua a emitir tool calls — com o disjuntor
// cego (no-op fora de `running`) e [runGate.sealTerminal] já a no-op. Um timeout que não
// interrompe é PIOR do que timeout nenhum, precisamente porque é credível. O cancelamento
// é o MESMO mecanismo que o [NodeService.Shutdown] e o heartbeat de posse perdida usam
// (rs.cancel, o cancel do contexto do run) — não se inventa uma segunda via de paragem.
func (s *NodeService) sweepDeadlinesOnce(ctx context.Context) {
	// Snapshot do registo de em-curso COM o runState: é dele que sai o cancel do run.
	s.mu.Lock()
	pendentes := make([]*runState, 0, len(s.runs))
	for _, rs := range s.runs {
		pendentes = append(pendentes, rs)
	}
	s.mu.Unlock()
	for _, rs := range pendentes {
		gate := s.node.stateGates.resolveGate(rs.runID)
		if gate == nil {
			continue // saiu do registo entretanto
		}
		st, fired, err := gate.m.CheckDeadlines(ctx)
		if err != nil {
			s.log("varrimento de deadlines: CheckDeadlines do run %q falhou (re-tenta no proximo tick): %v", rs.runID, err)
			continue
		}
		if !fired {
			continue
		}
		// rs.cancel é escrito em Submit sob o mutex ANTES de `go hostRun` (happens-before);
		// é idempotente (context.CancelFunc) e a saída do run é conduzida pelos defers de
		// hostRun — este varrimento não toca em lease, registo nem desfecho.
		if rs.cancel != nil {
			rs.cancel()
		}
		s.log("deadline fail-closed materializado: run=%q → %s (o run estava preso a meio de um turno) e o run foi INTERROMPIDO (contexto cancelado)", rs.runID, st)
	}
}

// SweepDeadlinesNow corre UM varrimento imediatamente. Existe para os testes conduzirem o
// varrimento de forma determinista, sem esperar pelo ticker (molde de SweepApprovalsNow).
func (s *NodeService) SweepDeadlinesNow(ctx context.Context) {
	if s.node == nil || s.node.stateGates == nil {
		return
	}
	s.sweepDeadlinesOnce(ctx)
}
