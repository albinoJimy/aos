package main

// VARRIMENTO DE APROVAÇÕES EXPIRADAS (AOS-021) — vive no LOOP DE SERVIÇO (decisão do dono).
//
// Uma tool call escalada suspende o run à espera de decisão humana. Se essa decisão nunca
// chegar, o pendente ficaria eternamente na lista do operador e o run eternamente marcado
// como à espera. O varrimento fecha esse ciclo: passado o TTL, o pendente EXPIRA.
//
// O QUE O VARRIMENTO NÃO FAZ (e é deliberado): não retoma o run nem re-executa nada. O run
// permanece RETOMÁVEL (decisão do dono) — quando alguém o retomar, a call escalada já não
// encontra grant e é NEGADA, e o agente segue outro caminho com o marcador de negação. A
// retoma é sempre um acto explícito e re-autenticado (POST /runs/{id}/resume).

import (
	"context"
	"time"
)

// DefaultApprovalSweepInterval é o período de varrimento por omissão. Bastante mais curto
// que o TTL das aprovações (15 min) para a expiração ser observada com pouca latência, e
// suficientemente longo para o varrimento ser irrelevante no custo do nó.
const DefaultApprovalSweepInterval = 1 * time.Minute

// WithApprovalSweepInterval sobrepõe o período de varrimento. <= 0 DESLIGA o varrimento
// (os pendentes nunca expiram sozinhos — usado em testes que conduzem o tempo à mão).
func WithApprovalSweepInterval(d time.Duration) NodeServiceOption {
	return func(c *nodeServiceConfig) { c.sweepInterval = d }
}

// sweepApprovals é o laço periódico. Termina quando stop fecha (shutdown do serviço).
func (s *NodeService) sweepApprovals(stop <-chan struct{}) {
	if s.sweepInterval <= 0 || s.node == nil || s.node.PendingApprovals == nil {
		return // varrimento desligado, ou sem four-eyes composto: nada a varrer
	}
	t := time.NewTicker(s.sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.sweepApprovalsOnce(context.Background())
		}
	}
}

// sweepApprovalsOnce expira os pendentes cuja espera excedeu o TTL. É idempotente (a
// expiração é um facto append-only com chave derivada de run+step) e best-effort: um erro
// é registado e re-tentado no tick seguinte — falhar a expirar nunca destrava nada nem
// interrompe o serviço.
//
// Exportada-por-teste através de [NodeService.SweepApprovalsNow].
func (s *NodeService) sweepApprovalsOnce(ctx context.Context) {
	pend := s.node.PendingApprovals
	expiraveis, err := pend.ListExpirable(ctx, time.Now(), s.approvalTTL)
	if err != nil {
		s.log("varrimento de aprovacoes: falha a listar expiraveis (re-tenta no proximo tick): %v", err)
		return
	}
	for _, rec := range expiraveis {
		if err := pend.Expire(ctx, rec.RunID, rec.StepID); err != nil {
			s.log("varrimento de aprovacoes: falha a expirar run=%q step=%q: %v", rec.RunID, rec.StepID, err)
			continue
		}
		s.log("aprovacao EXPIRADA (sem decisao em %s): run=%q step=%q tool=%q cap=%q — o run continua RETOMAVEL e a accao ficara negada",
			s.approvalTTL, rec.RunID, rec.StepID, rec.ToolID, rec.Capability)
	}
}

// SweepApprovalsNow corre UM varrimento imediatamente. Existe para os testes conduzirem o
// varrimento de forma determinista, sem esperar pelo ticker.
func (s *NodeService) SweepApprovalsNow(ctx context.Context) {
	if s.node == nil || s.node.PendingApprovals == nil {
		return
	}
	s.sweepApprovalsOnce(ctx)
}
