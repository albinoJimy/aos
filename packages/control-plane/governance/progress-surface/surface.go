package progresssurface

import (
	"context"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ProgressSurface é a SUPERFÍCIE que LÊ os sinais existentes (custo por span de EPIC-08,
// orçamento de EPIC-03, estado do run) e apresenta a semântica de progresso, o burn-down e
// — a ~limiar — o prompt de exaustão graciosa. COMPÕE as portas (BudgetReader/
// BudgetExtender/Degrader/ProgressReflector) e o Tracer; NÃO recontabiliza custo, NÃO muta
// o orçamento, NÃO morre em silêncio.
type ProgressSurface struct {
	reader    BudgetReader
	extender  BudgetExtender
	degrader  Degrader
	progress  ProgressReflector
	tracer    otelgenai.Tracer
	threshold float64
	runID     string
}

// Option configura a ProgressSurface na construção.
type Option func(*ProgressSurface)

// WithThreshold fixa o limiar do prompt de exaustão (fracção consumida, 0<t<1). Um limiar
// INVÁLIDO (<=0 ou >=1) é IGNORADO e mantém-se o DefaultThreshold (fail-closed/default,
// AC5) — a superfície nunca fica com um limiar que dispara sempre (0) ou nunca (>=1).
func WithThreshold(t float64) Option {
	return func(s *ProgressSurface) {
		if validThreshold(t) {
			s.threshold = t
		}
	}
}

// WithRunID fixa o run_id para correlacionar os spans do prompt/decisão ao run (AttrRunID).
func WithRunID(runID string) Option {
	return func(s *ProgressSurface) { s.runID = runID }
}

// New constrói a superfície. O tracer nil cai no NoopTracer (sem observabilidade, como o
// default do Runtime). As portas podem ser nil se a funcionalidade correspondente não for
// exercida (ex.: um reader nil só falha se Evaluate for chamado); os caminhos que precisam
// de uma porta em falta devolvem erro fail-closed em vez de fazer panic.
func New(reader BudgetReader, extender BudgetExtender, degrader Degrader, progress ProgressReflector, tracer otelgenai.Tracer, opts ...Option) *ProgressSurface {
	s := &ProgressSurface{
		reader:    reader,
		extender:  extender,
		degrader:  degrader,
		progress:  progress,
		tracer:    tracer,
		threshold: DefaultThreshold,
	}
	if s.tracer == nil {
		s.tracer = otelgenai.NoopTracer{}
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Threshold devolve o limiar efectivo em uso (para inspecção/testes).
func (s *ProgressSurface) Threshold() float64 { return s.threshold }

// Evaluation é o resultado de um Tick: o burn-down corrente, o progresso e — se a fracção
// atingiu o limiar — o prompt de exaustão a apresentar.
type Evaluation struct {
	// Burndown é o consumido vs orçamento e a fracção (AC1/AC4).
	Burndown Burndown
	// Progress é a semântica de progresso corrente (AC1).
	Progress ProgressSnapshot
	// Prompt é o prompt de exaustão a apresentar, ou nil se abaixo do limiar (AC2).
	Prompt *ExhaustionPrompt
	// State é o estado do prompt: PromptPrompting quando disparou, senão PromptIdle.
	State PromptState
}

// Evaluate (o Tick da superfície) LÊ o burn-down e o progresso e, se a fracção consumida
// atingir o limiar, produz o prompt de exaustão com as 3 opções (emitindo o span do
// prompt). É LEITURA PURA sobre os sinais — não muta orçamento nem contabiliza custo.
//
//   - spans   — os spans JÁ EMITIDOS (EPIC-08); o custo consumido lê-se via AggregateByTrace.
//   - traceID — o trace_id (hex) da trajectória a avaliar (chave da agregação).
//   - treeID  — o nó/árvore de orçamento cujo Limit se lê da porta BudgetReader.
func (s *ProgressSurface) Evaluate(ctx context.Context, spans []otelgenai.SpanData, traceID, treeID string) (Evaluation, error) {
	if s.reader == nil {
		return Evaluation{}, ErrNilBudgetReader
	}
	limit, err := s.reader.Limit(ctx, treeID)
	if err != nil {
		return Evaluation{}, err
	}
	ev := Evaluation{
		Burndown: ComputeBurndown(spans, traceID, limit),
		State:    PromptIdle,
	}
	if s.progress != nil {
		ev.Progress = s.progress.Snapshot()
	}
	if ev.Burndown.Fraction >= s.threshold {
		ev.Prompt = &ExhaustionPrompt{
			Fraction:  ev.Burndown.Fraction,
			Threshold: s.threshold,
			Options:   PromptOptions(),
		}
		ev.State = PromptPrompting
		s.emitPromptSpan(ctx, ev.Burndown, ev.Progress)
	}
	return ev, nil
}
