package hitl

import (
	"context"
	"sync/atomic"
)

// OpApprovalConfirm é o nome do span que cobre uma decisão do gate HITL (AOS-095,
// DoD "override-rate em spans/métricas OTel"). O módulo NÃO puxa o SDK OTel (isso é
// EPIC-08) — expõe, à imagem de messaging/sandbox, portas mínimas [Tracer]/[Span] e
// [MetricSink] que o wiring liga a um exportador real.
const OpApprovalConfirm = "approval_confirm"

// MetricOverrideRate é o nome canónico da métrica de governação exigida por AC5
// ("approval.override_rate"). Alimenta AOS-090 (demoção por override-rate alto).
const MetricOverrideRate = "approval.override_rate"

// DefaultOverrideRateThreshold é o limiar anti-rubber-stamping por omissão: o Art. 14
// documenta que utilizadores experientes auto-aprovam >40% dos pedidos, anulando a
// governação. Um override-rate acima de 0.40 dispara o sinal [MetricSink.SignalHighOverrideRate].
const DefaultOverrideRateThreshold = 0.40

// Atributos de span canónicos da decisão HITL. NENHUM transporta segredo: a classe,
// o modo, a decisão, o motivo, o override-rate e o dual-control NÃO são segredos; o
// preview, o payload e QUALQUER chave nunca entram num span.
const (
	AttrClass        = "aos.hitl.class"
	AttrMode         = "aos.hitl.mode"
	AttrDecision     = "aos.hitl.decision"
	AttrReason       = "aos.hitl.reason"
	AttrApprover     = "aos.hitl.approver"
	AttrDualControl  = "aos.hitl.dual_control"
	AttrOverrideRate = "aos.hitl.override_rate"
	AttrIrreversible = "aos.hitl.irreversible"
)

// Span é uma unidade de trabalho observável mínima (porta; ver [NoopTracer]).
type Span interface {
	SetAttribute(key string, value any)
	End()
}

// Tracer abre spans. O default do [Channel] é [NoopTracer] (injectável via
// [WithTracer]).
type Tracer interface {
	StartSpan(ctx context.Context, name string) (context.Context, Span)
}

// NoopTracer descarta os spans (default). Mantém o módulo leve: sem tracer
// injectado, o gate não emite nada e não incorre custo.
type NoopTracer struct{}

// StartSpan implementa [Tracer].
func (NoopTracer) StartSpan(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, noopSpan{}
}

type noopSpan struct{}

func (noopSpan) SetAttribute(string, any) {}
func (noopSpan) End()                     {}

// MetricSink é a porta de MÉTRICAS que expõe o override-rate como sinal OTel (AC5).
// O default do [Channel] é [NoopSink]; o wiring liga um MeterProvider real.
type MetricSink interface {
	// RecordOverrideRate publica o valor corrente de [MetricOverrideRate] e os
	// contadores subjacentes após CADA decisão HITL.
	RecordOverrideRate(ctx context.Context, rate float64, prompted, overrides uint64)
	// SignalHighOverrideRate é chamado quando o override-rate ULTRAPASSA o limiar
	// configurado — o sinal anti-rubber-stamping que alimenta AOS-090. Idempotente do
	// lado do sink (pode ser chamado repetidamente enquanto o limiar se mantém excedido).
	SignalHighOverrideRate(ctx context.Context, rate, threshold float64)
}

// NoopSink descarta as métricas (default).
type NoopSink struct{}

// RecordOverrideRate implementa [MetricSink].
func (NoopSink) RecordOverrideRate(context.Context, float64, uint64, uint64) {}

// SignalHighOverrideRate implementa [MetricSink].
func (NoopSink) SignalHighOverrideRate(context.Context, float64, float64) {}

// Metrics são os contadores de observabilidade do gate HITL (molde de [risk.Metrics]
// de AOS-074, sem SDK OTel). O OVERRIDE-RATE (anti rubber-stamping) é a fracção de
// decisões PROMPTED que resultaram em aprovação. Todos os acessos são atómicos.
type Metrics struct {
	// Prompted é o número de acções gray/danger que chegaram a uma confirmação HITL
	// (o denominador do override-rate). Inclui timeouts (foram promptadas, o silêncio
	// negou), à imagem de [risk.Gate].
	Prompted atomic.Uint64
	// Overrides é o número dessas que o aprovador APROVOU (o numerador do override-rate).
	Overrides atomic.Uint64
	// Denials é o número de acções negadas (recusa assinada, timeout, aprovador sem
	// autoridade, assinatura forjada, 4-eyes).
	Denials atomic.Uint64
	// Timeouts é o número de acções (tipicamente irreversíveis) negadas por TIMEOUT
	// fail-closed (silêncio). Subconjunto de Denials.
	Timeouts atomic.Uint64
}

// OverrideRate devolve a fracção de acções prompted que foram aprovadas
// (Overrides/Prompted), em [0,1]. Devolve 0 se nada foi prompted (sem divisão por
// zero). Um valor alto sinaliza rubber-stamping.
func (m *Metrics) OverrideRate() float64 {
	prompted := m.Prompted.Load()
	if prompted == 0 {
		return 0
	}
	return float64(m.Overrides.Load()) / float64(prompted)
}

// Snapshot devolve uma leitura consistente-o-suficiente dos contadores mais o
// override-rate derivado.
func (m *Metrics) Snapshot() (prompted, overrides, denials, timeouts uint64, overrideRate float64) {
	prompted = m.Prompted.Load()
	overrides = m.Overrides.Load()
	denials = m.Denials.Load()
	timeouts = m.Timeouts.Load()
	if prompted > 0 {
		overrideRate = float64(overrides) / float64(prompted)
	}
	return
}

// Exceeds indica se o override-rate corrente ULTRAPASSA threshold (estritamente),
// desde que tenha havido pelo menos um prompt (evita disparar sobre amostra vazia).
func (m *Metrics) Exceeds(threshold float64) bool {
	return m.Prompted.Load() > 0 && m.OverrideRate() > threshold
}
