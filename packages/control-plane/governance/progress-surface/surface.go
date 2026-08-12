package progresssurface

import (
	"context"
	"sync"

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

	// source é a FONTE do burn-down (AOS-261). nil ⇒ [ProgressSurface.EvaluateRun]
	// recusa-se com [ErrNilBurndownSource] — nunca devolve 0%.
	source BurndownSource

	// mu protege warned. warned é o LATCH do aviso de exaustão (AOS-262): o span
	// `aos.control.budget_warning` é emitido UMA VEZ por run, não uma vez por turno.
	//
	// PORQUE UM MAPA POR runID e não um bool: a superfície pode ser partilhada por vários
	// runs; um bool único calaria o aviso do run B porque o run A já avisou. O mapa é
	// PODADO pelo composition root ([ProgressSurface.ForgetRun], chamado quando o run sai
	// do registo de em-curso, como [runBreakers.forget]), pelo que não cresce com o tempo
	// de vida do processo.
	//
	// O latch é POR-INCARNAÇÃO DO PROCESSO: uma retoma depois de um restart re-emite o
	// aviso uma vez. É a escolha certa entre re-avisar e calar — o operador da nova
	// incarnação não viu o aviso da anterior.
	mu     sync.Mutex
	warned map[string]struct{}
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

// WithBurndownSource liga a FONTE do burn-down (AOS-261) — o adaptador sobre o ledger de
// turnos. Sem ela, [ProgressSurface.EvaluateRun] devolve [ErrNilBurndownSource]: um valor
// nil NÃO é ignorado em silêncio como nas outras opções, porque aqui a degradação
// silenciosa seria precisamente o defeito (0% consumido para sempre).
func WithBurndownSource(src BurndownSource) Option {
	return func(s *ProgressSurface) { s.source = src }
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
		warned:    make(map[string]struct{}),
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
//
// AOS-261: uma fatia NIL passou a ser [ErrNoBurndownSpans] em vez de um burn-down de 0%.
// Nenhum nó produz/retém spans, pelo que `nil` era o caso NORMAL e a superfície devolvia
// sempre «0% consumido» — verde e falso. Quem tem um run e não tem spans usa
// [ProgressSurface.EvaluateRun] sobre o ledger de turnos.
func (s *ProgressSurface) Evaluate(ctx context.Context, spans []otelgenai.SpanData, traceID, treeID string) (Evaluation, error) {
	if s.reader == nil {
		return Evaluation{}, ErrNilBudgetReader
	}
	if spans == nil {
		return Evaluation{}, ErrNoBurndownSpans
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

// ---------------------------------------------------------------------------
// AOS-261/AOS-262 — a via POR RUN (fonte real + AVISO, sem opções de decisão)
// ---------------------------------------------------------------------------

// BudgetWarning é o AVISO de aproximação ao tecto (AOS-262, PRIMEIRA ENTREGA).
//
// NÃO É UM PROMPT e não traz opções — deliberadamente. As três opções de
// [ExhaustionPrompt] (extend/summarize_stop/abort) NÃO são apresentadas nesta entrega
// porque nenhuma tem executor nem autoridade no nó: `extend` exigiria um mutador do tecto
// e um principal autorizado a mexer-lhe (AOS-263, bloqueado nas decisões D4/D5/D6),
// `summarize_stop` exigiria um caminho de resumo-e-paragem que o loop não tem, e `abort`
// duplicaria o disjuntor sem o veredicto durável dele. Oferecer uma escolha que ninguém
// consegue executar é prometer o que não existe — pior do que não a oferecer.
type BudgetWarning struct {
	// RunID é o run avisado.
	RunID string
	// Turn é o turno em que o limiar foi atingido (a correlação (runID, turn) de AOS-261).
	Turn int
	// Fraction é a fracção consumida no instante do aviso.
	Fraction float64
	// Threshold é o limiar configurado que foi atingido.
	Threshold float64
	// SpanEmitted indica se ESTA avaliação emitiu o span do aviso. É false nas avaliações
	// seguintes do MESMO run acima do limiar — o latch (AOS-262: uma vez por run).
	SpanEmitted bool
}

// RunEvaluation é o resultado de [ProgressSurface.EvaluateRun]: o burn-down REAL do run
// (lido da [BurndownSource]), o progresso e — a partir do limiar — o aviso. Não tem campo
// de prompt nem de opções: ver [BudgetWarning].
type RunEvaluation struct {
	// Burndown é o consumido (fonte) vs o tecto (BudgetReader) e a fracção.
	Burndown Burndown
	// Progress é a semântica de progresso corrente (porta ProgressReflector).
	Progress ProgressSnapshot
	// Turns é o nº de turnos que a fonte somou (não-vacuosidade: 0 é impossível, a fonte
	// devolve erro nesse caso).
	Turns int
	// Warning é o aviso, ou nil abaixo do limiar.
	Warning *BudgetWarning
	// State é PromptWarned quando o limiar foi atingido, senão PromptIdle. NUNCA
	// PromptPrompting nesta entrega — não há prompt.
	State PromptState
}

// EvaluateRun é o TICK da fronteira de fim-de-turno (AOS-262): lê o consumo REAL do run na
// [BurndownSource] (AOS-261), o tecto na [BudgetReader], o progresso no
// [ProgressReflector], e — se a fracção atingir o limiar — devolve o AVISO e emite o span
// `aos.control.budget_warning` UMA VEZ por run (latch).
//
// FAIL-CLOSED em cada dependência que falta, nunca um zero:
//
//   - sem [BudgetReader]  ⇒ [ErrNilBudgetReader] (sem tecto não há denominador);
//   - sem [BurndownSource] ⇒ [ErrNilBurndownSource] (sem fonte não há numerador);
//   - fonte em erro (sem ledger, ledger vazio, ilegível) ⇒ o erro DELA, tal-qual.
//
// É LEITURA PURA: não muta o orçamento, não recontabiliza custo, não decide nada. O único
// efeito é o span do aviso.
//
//   - runID  — a chave do ledger de turnos E do latch (estável entre incarnações).
//   - turn   — o turno corrente, para correlacionar o aviso; não entra na aritmética.
//   - treeID — o nó de orçamento cujo Limit se lê (no nó, é o próprio runID).
func (s *ProgressSurface) EvaluateRun(ctx context.Context, runID string, turn int, treeID string) (RunEvaluation, error) {
	if s.reader == nil {
		return RunEvaluation{}, ErrNilBudgetReader
	}
	if s.source == nil {
		return RunEvaluation{}, ErrNilBurndownSource
	}
	limit, err := s.reader.Limit(ctx, treeID)
	if err != nil {
		return RunEvaluation{}, err
	}
	consumption, err := s.source.ConsumedByRun(ctx, runID)
	if err != nil {
		return RunEvaluation{}, err
	}
	ev := RunEvaluation{
		Burndown: BurndownFromConsumption(consumption, limit),
		Turns:    consumption.Turns,
		State:    PromptIdle,
	}
	if s.progress != nil {
		ev.Progress = s.progress.Snapshot()
	}
	if ev.Burndown.Fraction < s.threshold {
		return ev, nil
	}
	ev.State = PromptWarned
	ev.Warning = &BudgetWarning{
		RunID:       runID,
		Turn:        turn,
		Fraction:    ev.Burndown.Fraction,
		Threshold:   s.threshold,
		SpanEmitted: s.latchWarning(runID),
	}
	if ev.Warning.SpanEmitted {
		s.emitWarningSpan(ctx, runID, turn, ev.Burndown, ev.Progress)
	}
	return ev, nil
}

// latchWarning arma o latch do run e devolve true SÓ na primeira vez (AOS-262: o span do
// aviso é emitido uma vez por run, não uma vez por turno acima do limiar).
func (s *ProgressSurface) latchWarning(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, já := s.warned[runID]; já {
		return false
	}
	s.warned[runID] = struct{}{}
	return true
}

// ForgetRun liberta o latch do run. O composition root chama-o quando o run sai do registo
// de em-curso (mesmo ponto de [runBreakers.forget] no nó) — é o que impede o mapa de
// crescer com o tempo de vida do processo. Idempotente; um run desconhecido é ignorado.
func (s *ProgressSurface) ForgetRun(runID string) {
	s.mu.Lock()
	delete(s.warned, runID)
	s.mu.Unlock()
}

// ValidThreshold reporta se t é um limiar de fracção válido (0 < t < 1). EXPORTADA para
// que o gate de ARRANQUE do nó (AOS_PROGRESS_THRESHOLD, AOS-262) recuse exactamente os
// mesmos valores que [WithThreshold] rejeitaria — sem uma segunda noção de "válido" a
// divergir desta. A diferença de POSTURA é deliberada e fica do lado do nó: [WithThreshold]
// cai no [DefaultThreshold] (é uma opção de biblioteca), enquanto a env ABORTA o arranque
// (um operador que escreveu um limiar e recebeu outro fica convencido de que configurou o
// aviso quando não configurou).
func ValidThreshold(t float64) bool { return validThreshold(t) }
