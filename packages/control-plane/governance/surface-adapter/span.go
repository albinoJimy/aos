package surfaceadapter

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Vocabulário do span de INTERACÇÃO POR CANAL (AC / DoD). Emite-se UM span por
// superfície onde o card é apresentado (aos.surface.*), ligado ao trace do run pelo
// ctx via a porta [agentruntime.Tracer]. REUSA os rótulos aos.hitl.* de AOS-095/120
// (classe/irreversível) para que os spans de superfície, card e decisão sejam
// consultáveis em conjunto.
//
// NENHUM atributo transporta segredo/PII: NUNCA o Preview (que já vem redigido de
// AOS-120), o Resource, nem qualquer chave. A plataforma, o canal, a classe, o
// irreversível e o flag de degradação não são segredos.
const (
	// OpSurfaceInteraction é o nome do span de interacção por canal. É DISTINTO do
	// OpApprovalCard (apresentação do card) e do span de decisão do Channel — acrescenta
	// a dimensão de SUPERFÍCIE (onde o card foi renderizado), ligado ao mesmo trace.
	OpSurfaceInteraction = "surface_interaction"

	// AttrSurfacePlatform — aos.surface.platform: a superfície (slack/telegram/desktop).
	AttrSurfacePlatform = "aos.surface.platform"
	// AttrSurfaceChannel — aos.surface.channel: o [controlsurface.ChannelID] de AOS-119.
	AttrSurfaceChannel = "aos.surface.channel"
	// AttrSurfaceRendered — aos.surface.rendered: o card foi renderizado nesta superfície.
	AttrSurfaceRendered = "aos.surface.rendered"
	// AttrSurfaceDegraded — aos.surface.degraded: a render degradou fail-closed (a
	// superfície não representa a semântica de dual-control do card).
	AttrSurfaceDegraded = "aos.surface.degraded"

	// AttrClass — aos.hitl.class: a classe de risco LIDA (reutiliza o vocabulário HITL).
	AttrClass = "aos.hitl.class"
	// AttrIrreversible — aos.hitl.irreversible: a acção é não-desfazível (vocabulário HITL).
	AttrIrreversible = "aos.hitl.irreversible"
	// AttrDualControl — aos.approval.card.dual_control_required: exigiu 2 aprovadores.
	AttrDualControl = "aos.approval.card.dual_control_required"
	// AttrCardRequestID — aos.approval.card.request_id: o id de apresentação (não-segredo).
	AttrCardRequestID = "aos.approval.card.request_id"
)

// emitSurfaceSpan abre e fecha o span de interacção por canal, ligado ao trace do run
// (via ctx): a plataforma, o canal, se renderizou, se degradou, a classe/irreversível
// LIDOS e o request-id de apresentação. Sem segredos/PII (o Preview NUNCA entra). Um
// tracer nil não emite nada.
func emitSurfaceSpan(ctx context.Context, tracer agentruntime.Tracer, rc RenderedCard) {
	if tracer == nil {
		return
	}
	_, span := tracer.StartSpan(ctx, OpSurfaceInteraction)
	span.SetAttribute(AttrSurfacePlatform, string(rc.Platform))
	span.SetAttribute(AttrSurfaceChannel, string(rc.ChannelID))
	span.SetAttribute(AttrSurfaceRendered, true)
	span.SetAttribute(AttrSurfaceDegraded, rc.Degraded)
	span.SetAttribute(AttrClass, rc.Class.String())
	span.SetAttribute(AttrIrreversible, rc.Irreversible)
	span.SetAttribute(AttrDualControl, rc.DualControlRequired)
	span.SetAttribute(AttrCardRequestID, rc.RequestID)
	span.End()
}
