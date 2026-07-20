package approvalcard

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// Decision é o veredicto da autorização de um card. É a projecção de apresentação da(s)
// [risk.ConfirmationResponse] que o [risk.ConfirmationChannel] devolveu — o card NÃO
// decide/assina, só agrega. Approvers lista as identidades DISTINTAS que aprovaram (0
// se negado, 1 para reversível, 2 para dual-control). Sem segredos.
type Decision struct {
	// Authorized é a decisão final: para reversíveis, uma aprovação basta; para
	// irreversíveis, exige dois aprovadores DISTINTOS Approved (AC3).
	Authorized bool
	// Approvers são as identidades distintas que aprovaram (do
	// [risk.ConfirmationResponse.Approver], resolvido a uma chave pinada pelo Channel).
	Approvers []string
	// Reason descreve o desfecho (sem segredos).
	Reason string
}

// DualControlCollector RESOLVE a interacção de aprovação de um card e DEVOLVE a(s)
// decisão(ões) ao [risk.ConfirmationChannel] (o [hitl.Channel] de AOS-095), que
// assina, verifica a autoridade, aplica anti-replay + 4-eyes e sela no audit. O
// coletor NÃO reimplementa nada disso — ACRESCENTA apenas, para acções irreversíveis,
// a regra de QUORUM que falta em AOS-095: dois aprovadores DISTINTOS (approver_1 !=
// approver_2). Seguro para concorrência na medida em que o canal o for. Construir com
// [NewDualControlCollector].
type DualControlCollector struct {
	channel risk.ConfirmationChannel
	tracer  agentruntime.Tracer
}

// CollectorOption configura o [DualControlCollector].
type CollectorOption func(*DualControlCollector)

// WithTracer injecta a porta de observabilidade do span de apresentação (AC6). Por
// omissão não emite (tracer nil).
func WithTracer(t agentruntime.Tracer) CollectorOption {
	return func(c *DualControlCollector) {
		if t != nil {
			c.tracer = t
		}
	}
}

// NewDualControlCollector constrói o coletor com o canal a que DEVOLVE a decisão. Um
// canal nil é fail-closed ([ErrNilChannel]): sem porta para assinar/impor, não há
// autorização possível.
func NewDualControlCollector(channel risk.ConfirmationChannel, opts ...CollectorOption) (*DualControlCollector, error) {
	if channel == nil {
		return nil, ErrNilChannel
	}
	c := &DualControlCollector{channel: channel}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// Authorize apresenta o card e RECOLHE a decisão, devolvendo-a ao canal HITL (AC4). O
// card é validado (fail-closed) antes de qualquer apresentação. A regra:
//
//   - REVERSÍVEL (DualControlRequired=false): UMA aprovação basta — delega tal-e-qual
//     no canal, que assina/sela/impõe. authorized == a resposta do canal.
//   - IRREVERSÍVEL (DualControlRequired=true): DUAS aprovações, cada uma submetida ao
//     MESMO canal (que faz assinatura + autoridade + anti-replay + 4-eyes + audit por
//     cada). O card só devolve "autorizado" quando AMBAS voltam Approved E os
//     aprovadores são identidades DISTINTAS e verificadas (approver_1 != approver_2, e
//     nenhum vazio). Um só aprovador, dois iguais, ou uma recusa/deny do canal NÃO
//     avança (fail-closed).
//
// Emite o span de apresentação (AC6) ligado ao trace pelo ctx, sem segredos.
func (c *DualControlCollector) Authorize(ctx context.Context, card ApprovalCard) (Decision, error) {
	if err := card.Validate(); err != nil {
		return Decision{Reason: "card invalido (fail-closed)"}, err
	}

	var dec Decision
	if card.DualControlRequired {
		dec = c.authorizeDual(ctx, card)
	} else {
		dec = c.authorizeSingle(ctx, card)
	}

	emitPresentationSpan(ctx, c.tracer, card, dec)
	return dec, nil
}

// authorizeSingle delega UMA aprovação no canal (reversível). O card não decide: a
// resposta do canal (assinada/selada) É a decisão.
func (c *DualControlCollector) authorizeSingle(ctx context.Context, card ApprovalCard) Decision {
	req := card.confirmationRequest()
	resp, err := c.channel.Confirm(ctx, req)
	if err != nil || !resp.Approved {
		return Decision{Authorized: false, Reason: "nao aprovado pelo canal HITL (fail-closed)"}
	}
	return Decision{Authorized: true, Approvers: approvers(resp.Approver), Reason: "aprovado (aprovacao unica)"}
}

// authorizeDual recolhe DUAS aprovações do MESMO canal e ACRESCENTA a regra de quorum
// approver_1 != approver_2. Cada Confirm passa pelo enforcement completo do Channel
// (assinatura/autoridade/anti-replay/4-eyes/audit); o card só compõe o resultado.
func (c *DualControlCollector) authorizeDual(ctx context.Context, card ApprovalCard) Decision {
	req := card.confirmationRequest()

	first, err := c.channel.Confirm(ctx, req)
	if err != nil || !first.Approved {
		return Decision{Authorized: false, Reason: "dual-control: primeira aprovacao nao concedida (fail-closed)"}
	}
	// Um aprovador não-verificado (Approver vazio) não conta como identidade distinta —
	// o Channel só preenche o Approver quando a assinatura é válida e autorizada.
	if first.Approver == "" {
		return Decision{Authorized: false, Reason: "dual-control: primeiro aprovador nao verificado (fail-closed)"}
	}

	second, err := c.channel.Confirm(ctx, req)
	if err != nil || !second.Approved {
		return Decision{Authorized: false, Approvers: approvers(first.Approver), Reason: "dual-control: segunda aprovacao nao concedida (fail-closed)"}
	}
	if second.Approver == "" {
		return Decision{Authorized: false, Approvers: approvers(first.Approver), Reason: "dual-control: segundo aprovador nao verificado (fail-closed)"}
	}

	// A REGRA NOVA (o gap que AOS-120 fecha): dois aprovadores DISTINTOS. Um só
	// aprovador a aprovar duas vezes (self-quorum) NÃO satisfaz o dual-control.
	if first.Approver == second.Approver {
		return Decision{Authorized: false, Approvers: approvers(first.Approver), Reason: "dual-control: exige dois aprovadores DISTINTOS (approver_1 == approver_2, fail-closed)"}
	}

	return Decision{Authorized: true, Approvers: []string{first.Approver, second.Approver}, Reason: "dual-control: dois aprovadores distintos aprovaram"}
}

// approvers devolve uma fatia com o aprovador dado se não-vazio (senão vazia), para o
// Decision.Approvers não carregar identidades vazias.
func approvers(a string) []string {
	if a == "" {
		return nil
	}
	return []string{a}
}
