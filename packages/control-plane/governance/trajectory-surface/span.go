package trajectorysurface

import (
	"context"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Vocabulário de span de INTERACÇÃO da superfície (DoD). A superfície emite um span
// próprio por interacção (construção da árvore / drill-down) LIGADO à trajectória pelo
// ctx propagado e por aos.run_id — molde de aos.control.*/aos.autonomy.* (AOS-119..126).
// São RÓTULOS de observabilidade: só o TIPO de interacção e CONTAGENS (nº de raízes /
// nº total de spans navegados). SEM SEGREDOS — nenhum atributo de span da trajectória,
// nenhum valor de conteúdo, nenhuma PII.
const (
	// OpTrajectorySurface — o nome da operação do span de interacção da superfície.
	OpTrajectorySurface = "aos.trajectory.surface"

	// AttrTrajectorySurfaceKind — aos.trajectory.surface.kind: o tipo de interacção
	// ("tree_view" | "drill_down").
	AttrTrajectorySurfaceKind = "aos.trajectory.surface.kind"
	// AttrTrajectorySurfaceRoots — aos.trajectory.surface.roots: nº de raízes da
	// árvore construída (só em tree_view).
	AttrTrajectorySurfaceRoots = "aos.trajectory.surface.roots"
	// AttrTrajectorySurfaceSpans — aos.trajectory.surface.spans: nº total de spans
	// navegados (tree_view: todos; drill_down: a sub-árvore do nó inspeccionado).
	AttrTrajectorySurfaceSpans = "aos.trajectory.surface.spans"
	// AttrTrajectorySurfaceNodeKind — aos.trajectory.surface.node_kind: o KIND do nó
	// inspeccionado (só em drill_down) — um rótulo de operação, não conteúdo.
	AttrTrajectorySurfaceNodeKind = "aos.trajectory.surface.node_kind"

	// SurfaceKindTreeView — a construção/apresentação da árvore de spans.
	SurfaceKindTreeView = "tree_view"
	// SurfaceKindDrillDown — a inspecção (drill-down) de um span/sub-agente.
	SurfaceKindDrillDown = "drill_down"
)

// emitTreeView abre e fecha um span de interacção tree_view ligado à trajectória (via
// ctx + aos.run_id), anotando o nº de raízes e o total de spans. Sem segredos.
func (s *TrajectorySurface) emitTreeView(ctx context.Context, roots, totalSpans int) {
	_, span := s.tracer.StartSpan(ctx, OpTrajectorySurface)
	span.SetAttribute(otelgenai.AttrOperationName, OpTrajectorySurface)
	span.SetAttribute(AttrTrajectorySurfaceKind, SurfaceKindTreeView)
	span.SetAttribute(AttrTrajectorySurfaceRoots, int64(roots))
	span.SetAttribute(AttrTrajectorySurfaceSpans, int64(totalSpans))
	if s.runID != "" {
		span.SetAttribute(otelgenai.AttrRunID, s.runID)
	}
	span.End()
}

// emitDrillDown abre e fecha um span de interacção drill_down ligado à trajectória,
// anotando o KIND do nó inspeccionado e o tamanho da sua sub-árvore. Sem segredos:
// nenhum valor de atributo do span inspeccionado é copiado para aqui.
func (s *TrajectorySurface) emitDrillDown(ctx context.Context, nodeKind string, subtreeSpans int) {
	_, span := s.tracer.StartSpan(ctx, OpTrajectorySurface)
	span.SetAttribute(otelgenai.AttrOperationName, OpTrajectorySurface)
	span.SetAttribute(AttrTrajectorySurfaceKind, SurfaceKindDrillDown)
	span.SetAttribute(AttrTrajectorySurfaceNodeKind, nodeKind)
	span.SetAttribute(AttrTrajectorySurfaceSpans, int64(subtreeSpans))
	if s.runID != "" {
		span.SetAttribute(otelgenai.AttrRunID, s.runID)
	}
	span.End()
}
