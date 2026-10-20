package main

// ADAPTADOR DE ESCALADA (AOS-021) — liga a porta [agentruntime.EscalationSink] do loop à
// máquina de estados durável do run e ao registo de pendentes.
//
// É a peça que faz o bridge negação→aprovação→reexecução CORRER no nó: sem ela as portas
// do kernel ficam inertes e um veredicto `escalate` do Reference Monitor comporta-se como
// uma negação (o run prossegue sem que ninguém peça aval).
//
// NÃO decide nada: a decisão é do RM (escalate) e do humano (four-eyes). Aqui só se
// materializa a suspensão durável e se regista o que o operador precisa de ver.

import (
	"context"
	"errors"
	"fmt"
	"time"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// ErrNoStateGateForRun — pediu-se a suspensão de um run sem máquina de estados aberta.
// FAIL-CLOSED: sem transição durável não há suspensão, e deixar o run seguir daria um
// agente a avançar como se nada tivesse ficado por decidir.
var ErrNoStateGateForRun = errors.New("aos: run sem gate de estado aberto — nao ha como suspender para aval humano (fail-closed)")

// nodeEscalationSink implementa [agentruntime.EscalationSink] sobre o registo de gates
// por-run do nó (a MESMA state.Machine de AOS-218 que o steer usa — reutilizada, não
// duplicada) e o registo DURÁVEL de pendentes.
type nodeEscalationSink struct {
	gates   *runStateGates
	pending *integration.PendingApprovals
	clock   func() time.Time
	// taintPede é a classificação do gate de taint ESCALANTE ([referencemonitor.TaintGate.RequiresTrustedAuthorization]),
	// composta só com `AOS_TAINT_ESCALA=on` (AOS-541). nil ⇒ só conta como do taint a escalada que
	// o próprio gate levantou.
	taintPede func(referencemonitor.Call) bool
}

// doTaint diz se a escalada é do gate de taint (AOS-541): levantada por ele, ou levantada ANTES por
// outro hook (a autonomia, dentro da política) sobre uma call que o taint também escalaria — uma
// call privilegiada pedida com o contexto `untrusted`. No segundo caso o pendente leva na mesma
// `escalated_by=taint`: o aprovador vê que o pedido vem de algo de fora, e a regra da retoma
// (expirada ou recusada ⇒ o run falha) vale também para ele. Basta a aprovação de uma pessoa, como
// para qualquer escalada (decisão do dono de 2026-10-20).
func (s *nodeEscalationSink) doTaint(p agentruntime.PendingApproval) bool {
	if p.EscalatedBy == integration.EscalatedByTaint {
		return true
	}
	if s.taintPede == nil || !taint.ParseLabel(p.Taint).IsUntrusted() {
		return false
	}
	return s.taintPede(referencemonitor.Call{
		RunID: p.RunID, StepID: p.StepID, ToolID: p.ToolID, Capability: p.Capability,
		Resource: referencemonitor.Resource{Type: p.ResourceType, Value: p.ResourceValue, Region: p.ResourceRegion},
	})
}

// newNodeEscalationSink constrói o adaptador. Ambos os colaboradores são obrigatórios: um
// sink que não suspende, ou que suspende sem registar o pendente, seria pior do que não
// existir (o run pararia sem ninguém saber o que aprovar).
func newNodeEscalationSink(gates *runStateGates, pending *integration.PendingApprovals) (*nodeEscalationSink, error) {
	if gates == nil {
		return nil, fmt.Errorf("aos: registo de gates de estado nil")
	}
	if pending == nil {
		return nil, fmt.Errorf("aos: registo de pendentes de aprovacao nil")
	}
	return &nodeEscalationSink{gates: gates, pending: pending, clock: time.Now}, nil
}

// Escalate implementa [agentruntime.EscalationSink].
//
// ORDEM DELIBERADA — registar o pendente ANTES de suspender: se a suspensão falhar, o erro
// sobe e o run aborta (fail-closed), e o pendente já registado é inofensivo (fica sem
// grant e, sem retoma, nunca destrava nada). A ordem inversa deixaria uma janela em que o
// run está suspenso e o operador não tem o que aprovar — o pior dos dois estados.
func (s *nodeEscalationSink) Escalate(ctx context.Context, p agentruntime.PendingApproval) error {
	// AOS-541 — uma escalada do GATE DE TAINT tem regras próprias. Se é a MESMA call (mesmo passo,
	// mesma preview) cujo pendente já saiu da lista sem grant — expirou, ou um humano decidiu sem
	// aprovar —, a retoma não volta a suspender à espera de uma aprovação que já não vem: o run
	// falha com a causa nomeada. Nada é registado nem suspenso; o erro sobe e o loop termina o run.
	//
	// SÓ para o taint: uma escalada da autonomia retomada depois de expirar volta a suspender, como
	// sempre (o interruptor desligado não muda nada, e as outras escaladas não são deste ticket).
	escaladaDoTaint := s.doTaint(p)
	if escaladaDoTaint {
		retirada, err := s.pending.RetiradoSemGrant(ctx, p.RunID, p.StepID, p.Preview)
		if err != nil {
			// Fail-closed: sem saber se já foi recusada, não se volta a pedir — falha-se.
			return fmt.Errorf("aos: ler o registo de aprovacoes da escalada do taint (AOS-541): %w", err)
		}
		switch {
		case retirada.Decisao != "":
			return fmt.Errorf("%w: run=%q step=%q tool=%q decisao=%q por %q", ErrAprovacaoDoTaintRecusada, p.RunID, p.StepID, p.ToolID, retirada.Decisao, retirada.Principal)
		case retirada.Expirada:
			return fmt.Errorf("%w: run=%q step=%q tool=%q", ErrAprovacaoDoTaintExpirada, p.RunID, p.StepID, p.ToolID)
		}
	}
	escaladoPor := ""
	if escaladaDoTaint {
		// Só o taint fica escrito: o pendente da autonomia continua byte-idêntico ao de antes.
		escaladoPor = integration.EscalatedByTaint
	}
	if err := s.pending.Put(ctx, integration.PendingRecord{
		RunID:          p.RunID,
		StepID:         p.StepID,
		Turn:           p.Turn,
		ToolID:         p.ToolID,
		Capability:     p.Capability,
		ResourceType:   p.ResourceType,
		ResourceValue:  p.ResourceValue,
		ResourceRegion: p.ResourceRegion,
		Preview:        p.Preview,
		EscalatedBy:    escaladoPor,
		// Âncora do TTL: é daqui que o varrimento periódico sabe que a espera excedeu os
		// 15 minutos. Sem ela o pendente nunca expiraria sozinho (fail-safe).
		CreatedAt: s.clock().UTC().Format(time.RFC3339Nano),
		// AOS-478: a escalada é um acto do RUN — o envelope do pendente leva o principal da
		// call escalada, o mesmo que a preview amarra e que o `approval.consumed` vai levar.
		Producer: p.Principal.EventProducer(),
	}); err != nil {
		return fmt.Errorf("aos: registar aprovacao pendente: %w", err)
	}
	gate := s.gates.resolveGate(p.RunID)
	if gate == nil {
		return ErrNoStateGateForRun
	}
	if err := gate.EscalateToHuman(ctx, "tool call escalada: aguarda aval humano (AOS-021)"); err != nil {
		return fmt.Errorf("aos: suspender run para aval humano: %w", err)
	}
	return nil
}

var _ agentruntime.EscalationSink = (*nodeEscalationSink)(nil)
