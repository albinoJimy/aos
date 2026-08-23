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
	"errors"
	"fmt"
	"os"
	"strings"
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
		// Expira COM O TIPO do registo (AOS-263): a chave de expiração é por tipo, e usar a do
		// tipo default num pendente de outro tipo não o retiraria da lista — o varrimento
		// re-tentaria o mesmo registo em cada tick, para sempre.
		kind := rec.Kind.Resolved()
		if err := pend.ExpireKind(ctx, kind, rec.RunID, rec.StepID); err != nil {
			s.log("varrimento de pendentes: falha a expirar tipo=%q run=%q step=%q: %v", kind, rec.RunID, rec.StepID, err)
			continue
		}
		s.log("pendente EXPIRADO (sem decisao em %s): tipo=%q run=%q step=%q tool=%q cap=%q — o run continua RETOMAVEL e a accao ficara negada",
			s.approvalTTL, kind, rec.RunID, rec.StepID, rec.ToolID, rec.Capability)
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

// ErrBadApprovalSweepInterval — `AOS_APPROVAL_SWEEP_INTERVAL` ilegível ou <= 0.
//
// FAIL-CLOSED, e passou a sê-lo em 2026-08-23 porque a JUSTIFICAÇÃO DA EXCEPÇÃO CADUCOU.
//
// A linha que tratava esta variável descartava em silêncio um valor ilegível, e o comentário
// vizinho explicava a diferença: «o varrimento de aprovações é higiene operacional; [o de
// retenção] conduz a EXPIRAÇÃO POR TTL». Era verdade quando foi escrito.
//
// DEIXOU DE SER com AOS-263. A retoma de um run recusa enquanto houver um prompt de exaustão POR
// RESPONDER ([NodeService.exhaustionPromptPorResponder]); o `ListForRun` só o deixa de devolver
// depois de RETIRADO; e quem o retira é o `ExpireKind`, chamado APENAS por este varredor. O
// próprio log dele di-lo: «o run continua RETOMAVEL» — é a expiração que o torna retomável outra
// vez.
//
// Logo, com `AOS_APPROVAL_SWEEP_INTERVAL=0` — que era aceite — um run com pergunta de exaustão
// fica PERMANENTEMENTE irretomável, e o operador não tem forma de o notar: o run aparece em
// `waiting_on_human`, a rota de retoma recusa com 409, e a causa está numa variável de ambiente
// que ninguém volta a ler.
//
// NENHUM VALOR DESLIGA, tal como no de retenção. A opção [WithApprovalSweepInterval] continua a
// aceitar 0 — os testes precisam de desligar o laço — mas o AMBIENTE já não.
var ErrBadApprovalSweepInterval = errors.New("aos: AOS_APPROVAL_SWEEP_INTERVAL invalido (duracao > 0; nenhum valor desliga o varrimento — dele depende a retoma de runs com prompt de exaustao)")

// approvalSweepIntervalFromEnv resolve a cadência a partir do ambiente. Vazia ⇒
// [DefaultApprovalSweepInterval]; malformada ou <= 0 ⇒ [ErrBadApprovalSweepInterval] (aborta o
// arranque). Devolve SEMPRE a duração EM VIGOR, para que o valor anunciado e o valor ligado sejam
// o mesmo por construção — a disciplina de [retentionSweepIntervalFromEnv].
func approvalSweepIntervalFromEnv() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_APPROVAL_SWEEP_INTERVAL"))
	if raw == "" {
		return DefaultApprovalSweepInterval, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%w: AOS_APPROVAL_SWEEP_INTERVAL=%q", ErrBadApprovalSweepInterval, raw)
	}
	return d, nil
}
