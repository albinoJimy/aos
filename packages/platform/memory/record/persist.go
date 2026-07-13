package record

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Atributos de span emitidos pela via persist. Namespace próprio aos.trajectory.*
// (não é inferência GenAI); reutiliza os atributos canónicos do Agent Runtime
// (prompt_hash, model, run_id) onde aplicável para o backend real (EPIC-08) mapear
// sem renomear.
const (
	attrTraceID    = "aos.trajectory.trace_id"
	attrKind       = "aos.trajectory.kind"
	attrTurnIndex  = "aos.trajectory.turn_index"
	attrRawLen     = "aos.trajectory.raw_len"
	attrSpanID     = "aos.trajectory.span_id"
	attrParentSpan = "aos.trajectory.parent_span_id"
	attrParamPref  = "aos.trajectory.param."
)

// Nomes de span da via persist.
const (
	spanTrajectory = "trajectory.persist" // span raiz do registo completo
	spanTurn       = "trajectory.turn"    // um span por turno (carrega o manifesto)
)

// EventTurn é a projecção COMPLETA de um turno no evento persistido: inclui o
// conteúdo cru E o manifesto por turno. Nada é descartado (contraste directo com
// a projecção de contexto).
type EventTurn struct {
	Index                 int
	PromptHash            string
	ModelID               string
	Params                map[string]string
	AssemblyVersion       string
	ManifestSchemaVersion string
	RawContent            string
	Summary               string
}

// TrajectoryEvent é o EVENTO produzido pela via persist: a trajectória COMPLETA
// (todos os turnos com conteúdo cru + manifesto por turno, e a árvore de spans
// completa). É o registo — a fonte de verdade que alimenta replay/RCA/eval. A via
// persist NUNCA descarta: não há caminho de higiene aqui.
type TrajectoryEvent struct {
	SchemaVersion string
	TraceID       string
	Turns         []EventTurn
	Spans         []Span
	// EmittedSpans é o nº de spans emitidos para o backend de observabilidade
	// (raiz + um por turno + a árvore de spans registada). É sempre estritamente
	// maior do que a vista injectada no modelo (que leva só o resumo) — a prova de
	// que o backend recebe a trajectória completa enquanto o pai recebe o resumo.
	EmittedSpans int
}

// Persist grava a trajectória COMPLETA no backend de observabilidade (EPIC-08),
// ligada ao trace por TraceID, e devolve o [TrajectoryEvent] materializado. É a
// via de REGISTO — fisicamente separada da projecção de contexto: opera sobre o
// registo concreto (*TrajectoryRecord), não sobre a vista read-only, e emite tudo
// (cru + manifesto + árvore de spans) sem qualquer passo de higiene.
//
// O backend real é EPIC-08; aqui a árvore de spans é emitida pela PORTA zero-dep
// agentruntime.Tracer (um tracer nil cai no NoopTracer, sem dependências novas).
// Cada turno emite um span com o manifesto por turno (prompt_hash/model-id/versões)
// como atributos, INDEPENDENTEMENTE do que a projecção higienizou.
func Persist(ctx context.Context, rec *TrajectoryRecord, tracer agentruntime.Tracer) (TrajectoryEvent, error) {
	if rec == nil {
		return TrajectoryEvent{}, ErrNilRecord
	}
	if tracer == nil {
		tracer = agentruntime.NoopTracer{}
	}

	emitted := 0

	// Span raiz do registo completo.
	ctx, root := tracer.StartSpan(ctx, spanTrajectory)
	root.SetAttribute(attrTraceID, rec.traceID)
	root.SetAttribute(agentruntime.AttrRunID, rec.traceID)
	emitted++

	// Um span por turno, carregando o manifesto por turno completo. Grava-se SEMPRE
	// o hash do prompt materializado, model-id/params e versões — o que foi
	// higienizado no contexto não afecta o registo.
	turns := rec.turnsClone()
	eventTurns := make([]EventTurn, len(turns))
	for i, t := range turns {
		_, ts := tracer.StartSpan(ctx, spanTurn)
		ts.SetAttribute(attrKind, "turn")
		ts.SetAttribute(attrTurnIndex, t.Index)
		ts.SetAttribute(agentruntime.AttrPromptHash, t.PromptHash)
		ts.SetAttribute(agentruntime.AttrRequestModel, t.ModelID)
		ts.SetAttribute(attrRawLen, len(t.RawContent))
		// Parâmetros do modelo por ordem estável de chave (determinismo).
		for _, k := range t.SortedParamKeys() {
			ts.SetAttribute(attrParamPref+k, t.Params[k])
		}
		ts.End()
		emitted++

		// Turn e EventTurn têm o MESMO layout — a conversão preserva todos os
		// campos (conteúdo cru + manifesto por turno). O EventTurn é o mesmo turno
		// visto na fronteira do evento persistido (nada é descartado).
		eventTurns[i] = EventTurn(t)
	}

	// Árvore de spans COMPLETA para o backend (EPIC-08). Ordem de registo preservada.
	spans := rec.spansClone()
	for _, s := range spans {
		_, sp := tracer.StartSpan(ctx, s.Name)
		sp.SetAttribute(attrKind, "span")
		sp.SetAttribute(attrSpanID, s.ID)
		if s.ParentID != "" {
			sp.SetAttribute(attrParentSpan, s.ParentID)
		}
		for _, k := range s.SortedAttrKeys() {
			sp.SetAttribute(k, s.Attributes[k])
		}
		sp.End()
		emitted++
	}

	root.End()

	return TrajectoryEvent{
		SchemaVersion: RecordSchemaVersion,
		TraceID:       rec.traceID,
		Turns:         eventTurns,
		Spans:         spans,
		EmittedSpans:  emitted,
	}, nil
}
