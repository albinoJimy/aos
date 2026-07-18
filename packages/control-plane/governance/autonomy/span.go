package autonomy

import (
	"context"

	"github.com/aos-ref/kernel/reference-monitor/risk"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Nomes de operação e atributos dos spans OTel de autonomia (AC4/DoD). São aos.*
// (nunca gen_ai.* de outra operação) e NUNCA transportam segredos — só
// identificadores do par e metadados de nível/classe/oversight.
const (
	// OpAutonomyLevel é o nome do span que expõe o nível corrente de um par
	// (agente, domínio) na observabilidade.
	OpAutonomyLevel = "aos.autonomy.level"

	// AttrAutonomyAgent — o agente (NHI) do par.
	AttrAutonomyAgent = "aos.autonomy.agent"
	// AttrAutonomyDomain — o domínio do par.
	AttrAutonomyDomain = "aos.autonomy.domain"
	// AttrAutonomyLevel — o nível corrente ("L0".."L5").
	AttrAutonomyLevel = "aos.autonomy.level"
	// AttrAutonomyRiskClass — a classe de risco da tool call ("safe"/"gray"/"danger").
	AttrAutonomyRiskClass = "aos.autonomy.risk_class"
	// AttrAutonomyOversight — o modo de oversight resultante (nível × classe).
	AttrAutonomyOversight = "aos.autonomy.oversight"
)

// ExposeLevel abre um span [OpAutonomyLevel] que EXPÕE o nível corrente do par
// (agente, domínio) na observabilidade (AC4/DoD). O chamador fecha o span com
// [otelgenai.Span.End]. Um tracer nil usa o [otelgenai.NoopTracer] (no-op, sem
// custo). NUNCA transporta segredos.
func ExposeLevel(ctx context.Context, tracer otelgenai.Tracer, agent, domain string, level Level) (context.Context, otelgenai.Span) {
	if tracer == nil {
		tracer = otelgenai.NoopTracer{}
	}
	ctx, span := tracer.StartSpan(ctx, OpAutonomyLevel)
	span.SetAttribute(otelgenai.AttrOperationName, OpAutonomyLevel)
	span.SetAttribute(AttrAutonomyAgent, agent)
	span.SetAttribute(AttrAutonomyDomain, domain)
	span.SetAttribute(AttrAutonomyLevel, level.String())
	return ctx, span
}

// AnnotateOversight anota um span (tipicamente o de [ExposeLevel]) com a CLASSE DE
// RISCO da tool call e o MODO DE OVERSIGHT resultante da composição nível × classe
// (AC3) — tornando a decisão de gate observável ponta-a-ponta.
func AnnotateOversight(span otelgenai.Span, class risk.Class, mode OversightMode) {
	if span == nil {
		return
	}
	span.SetAttribute(AttrAutonomyRiskClass, class.String())
	span.SetAttribute(AttrAutonomyOversight, mode.String())
}
