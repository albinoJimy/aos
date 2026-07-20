package controlsurface

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/control"
)

// Vocabulário de span da superfície de controlo. REUSA os atributos já emitidos pelo
// [control.SteerChannel] de AOS-023 (control.OpControlSignal, control.AttrControlSignal,
// control.AttrControlEmitter) e os atributos GenAI partilhados (agentruntime.AttrRunID,
// AttrOperationName, AttrPrincipalNHI). ACRESCENTA a dimensão de APRESENTAÇÃO que só a
// superfície conhece — o CANAL de onde veio a acção e o actor HITL — sem editar a
// fonte partilhada otel-genai/semconv.go (mantém o git status confinado a este módulo).
const (
	// AttrControlChannel — aos.control.channel: a superfície de origem (desktop/
	// chatbot/api). É o que distingue, no trace, "o mesmo run controlado de dois
	// canais". Rótulo de observabilidade, nunca segredo.
	AttrControlChannel = "aos.control.channel"
	// AttrControlActor — aos.hitl.actor: o actor humano/serviço que emitiu a acção
	// (alinha com o vocabulário HITL). É o EmitterID (a NHI) — identidade, nunca
	// credencial.
	AttrControlActor = "aos.hitl.actor"
	// AttrControlSchemaVersion — aos.control.schema_version: a versão SemVer do
	// contrato desta mensagem, para correlacionar a acção com a linha de contrato.
	AttrControlSchemaVersion = "aos.control.schema_version"
)

// emitInteractionSpan abre e fecha um span de INTERACÇÃO de controlo (AC6) ligado ao
// trace do run: QUEM (emitter/actor + principal NHI), QUANDO (o próprio span), QUE
// SINAL (o kind/sinal) e de que CANAL. É a camada de APRESENTAÇÃO da observabilidade —
// distinta e complementar do span interno que o [control.SteerChannel] já emite por
// sinal aceite: este captura a dimensão de canal que o canal durável não conhece.
//
// Reusa a operação [control.OpControlSignal] para que os spans de controlo — de canal
// e de superfície — vivam sob o MESMO nome de operação e sejam consultáveis em
// conjunto (RecordingTracer.SpansByOperation(control.OpControlSignal)). Sem segredos:
// só rótulos (run_id, kind, emitter_id, canal, versão) entram.
func (s *ControlSurface) emitInteractionSpan(ctx context.Context, m ControlMessage) {
	_, span := s.tracer.StartSpan(ctx, control.OpControlSignal)
	span.SetAttribute(agentruntime.AttrOperationName, control.OpControlSignal)
	span.SetAttribute(agentruntime.AttrRunID, m.RunID)
	span.SetAttribute(control.AttrControlSignal, m.signalLabel())
	if m.EmitterID != "" {
		span.SetAttribute(control.AttrControlEmitter, m.EmitterID)
		span.SetAttribute(agentruntime.AttrPrincipalNHI, m.EmitterID)
		span.SetAttribute(AttrControlActor, m.EmitterID)
	}
	if m.Channel != "" {
		span.SetAttribute(AttrControlChannel, string(m.Channel))
	}
	if m.SchemaVersion != "" {
		span.SetAttribute(AttrControlSchemaVersion, m.SchemaVersion)
	}
	span.End()
}
