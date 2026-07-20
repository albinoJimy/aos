package approvalcard

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Vocabulário de span da APRESENTAÇÃO do card (AC6). REUSA os rótulos aos.hitl.* já
// emitidos pelo [hitl.Channel] de AOS-095 (classe/irreversível) para que os spans de
// card e de decisão sejam consultáveis em conjunto, e ACRESCENTA a dimensão de
// apresentação que só o card conhece (aos.approval.card.*): o request-id de
// apresentação, a versão de schema, se o dual-control foi exigido e quantos
// aprovadores DISTINTOS o concederam.
//
// NENHUM atributo transporta segredo/PII: NUNCA o Preview, o Resource, o Call.Input
// nem qualquer chave. A classe, o irreversível, a decisão e a contagem de aprovadores
// não são segredos (molde metrics.go de AOS-095).
const (
	// OpApprovalCard é o nome do span de APRESENTAÇÃO do card (card mostrado + decisão
	// recolhida). É DISTINTO do OpApprovalConfirm que o [hitl.Channel] emite por decisão
	// assinada — o card NÃO duplica o span de decisão do Channel, acrescenta o de
	// apresentação, ligado ao mesmo trace pelo ctx.
	OpApprovalCard = "approval_card"

	// AttrClass — aos.hitl.class: a classe de risco LIDA (reutiliza o vocabulário HITL).
	AttrClass = "aos.hitl.class"
	// AttrIrreversible — aos.hitl.irreversible: a acção é não-desfazível (vocabulário HITL).
	AttrIrreversible = "aos.hitl.irreversible"
	// AttrDecision — aos.hitl.decision: allow/deny da apresentação (vocabulário HITL).
	AttrDecision = "aos.hitl.decision"

	// AttrCardSchemaVersion — aos.approval.card.schema_version: a linha de contrato do card.
	AttrCardSchemaVersion = "aos.approval.card.schema_version"
	// AttrCardRequestID — aos.approval.card.request_id: o id de apresentação (não-segredo).
	AttrCardRequestID = "aos.approval.card.request_id"
	// AttrCardDualControl — aos.approval.card.dual_control_required: exigiu 2 aprovadores.
	AttrCardDualControl = "aos.approval.card.dual_control_required"
	// AttrCardApproverCount — aos.approval.card.approver_count: nº de aprovadores DISTINTOS
	// que aprovaram (0/1/2). Contagem, não identidades — evita duplicar o AttrApprover que
	// o Channel já sela e mantém o span mínimo.
	AttrCardApproverCount = "aos.approval.card.approver_count"
)

// emitPresentationSpan abre e fecha o span de APRESENTAÇÃO do card (AC6) ligado ao
// trace do run (via ctx): a classe/irreversível LIDOS, a versão de schema, o
// request-id de apresentação, se o dual-control foi exigido, a decisão recolhida e a
// contagem de aprovadores distintos. Sem segredos/PII. Um tracer nil não emite nada.
func emitPresentationSpan(ctx context.Context, tracer agentruntime.Tracer, card ApprovalCard, dec Decision) {
	if tracer == nil {
		return
	}
	_, span := tracer.StartSpan(ctx, OpApprovalCard)
	span.SetAttribute(AttrClass, card.Class.String())
	span.SetAttribute(AttrIrreversible, card.Irreversible)
	span.SetAttribute(AttrCardSchemaVersion, card.SchemaVersion.String())
	span.SetAttribute(AttrCardRequestID, card.RequestID)
	span.SetAttribute(AttrCardDualControl, card.DualControlRequired)
	span.SetAttribute(AttrDecision, decisionLabel(dec.Authorized))
	span.SetAttribute(AttrCardApproverCount, len(dec.Approvers))
	span.End()
}

func decisionLabel(authorized bool) string {
	if authorized {
		return "allow"
	}
	return "deny"
}
