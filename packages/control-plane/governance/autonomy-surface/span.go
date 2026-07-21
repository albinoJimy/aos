package autonomysurface

import (
	"context"

	"github.com/aos-ref/control-plane/governance/autonomy"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Vocabulário de span da superfície de autonomia (DoD). REUSA o span aos.autonomy.level
// de AOS-089 ([autonomy.ExposeLevel] — agent/domain/level) e ACRESCENTA a dimensão que só
// a superfície conhece: o TIPO de interacção (vista/pedido/aviso de demoção) e o motivo
// quando o há. São RÓTULOS de observabilidade ligados ao trace por AttrRunID/agent/
// domain/level — NUNCA segredos (sem prompt, sem args, sem PII).
const (
	// AttrAutonomySurfaceKind — aos.autonomy.surface.kind: o tipo de interacção da
	// superfície ("view" | "request_review" | "demotion_notice").
	AttrAutonomySurfaceKind = "aos.autonomy.surface.kind"

	// SurfaceKindView — a construção/apresentação de uma [LevelView].
	SurfaceKindView = "view"
	// SurfaceKindRequest — um pedido de revisão de nível delegado à política.
	SurfaceKindRequest = "request_review"
	// SurfaceKindDemotion — o aviso imediato de uma demoção automática.
	SurfaceKindDemotion = "demotion_notice"
)

// emitInteraction abre e fecha um span de INTERACÇÃO da superfície ligado ao trace: reusa
// [autonomy.ExposeLevel] (op aos.autonomy.level — agent/domain/level) e anota o tipo de
// interacção, o run_id (quando conhecido) e o motivo (só nas transições/demoções, onde é
// a métrica que a política selou). Sem segredos: só identificadores, o nível, o tipo e o
// motivo já público da decisão.
func (s *Surface) emitInteraction(ctx context.Context, agent, domain string, level autonomy.Level, kind, reason string) {
	_, span := autonomy.ExposeLevel(ctx, s.tracer, agent, domain, level)
	span.SetAttribute(AttrAutonomySurfaceKind, kind)
	if s.runID != "" {
		span.SetAttribute(agentruntime.AttrRunID, s.runID)
	}
	if reason != "" {
		span.SetAttribute(autonomy.AttrAutonomyReason, reason)
	}
	span.End()
}
