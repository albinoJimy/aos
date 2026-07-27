package otelgenai

// Evals ligados ao trace (AOS-084 — lado OBS, tecnica/08 §8.2, ADR-010/ADR-012).
//
// O eval-driven development torna-se viável porque a trajectória completa está
// SEMPRE no backend como árvore de spans (AOS-077). Cada avaliação — de um
// golden-set curado E estável, ou de um dataset derivado de falhas — é registada
// como um span gen_ai.evaluation.result ([OpEvaluation]) LIGADO por trace_id à
// trajectória que avaliou. A avaliação não é um relatório à parte: é um span de
// PRIMEIRA CLASSE, correlacionável com os tokens, o custo e as decisões de política
// da trajectória original PORQUE partilha o MESMO trace_id (a correlação faz-se via
// a agregação por trace de cost_aggregation.go / wide_event.go — não se duplica).
//
// # O que é ESTE módulo (folha) e o que é AOS-114/AOS-115 (diferido)
//
// Este ficheiro entrega o LADO OBS: o vocabulário do span de eval, um
// [EvaluationResult] tipado, um recorder que o emite ligado por trace_id, as PORTAS
// [EvalRunner] (corre a avaliação sobre uma trajectória) e [EvalGate] (admite/rejeita
// uma auto-modificação a partir do resultado), e um comparador de trace-diffing vs
// baseline que apanha regressões NOVAS. O runner CONCRETO, os golden-sets curados e
// o eval-gate CONCRETO pertencem a EPIC-11 (AOS-114/AOS-115) e ficam DIFERIDOS —
// aqui só as portas + impls de referência/no-op/fake para teste. É o padrão de
// wiring diferido do repo (ver doc.go, adapter OTLP diferido).
//
// # Folha, sem ciclos
//
// otel-genai é um módulo FOLHA (zero deps internas). O trace-diffing aqui é um
// comparador PRÓPRIO sobre [SpanData]/[EvaluationResult] — NÃO importa o replay
// engine (AOS-079, packages/kernel/agent-runtime/replay), que detém o seu próprio
// detectDivergence por passo e fica no seu lugar. Os dois são complementares: o
// replay compara o prompt_hash por passo contra a captura; este compara a árvore de
// spans (acções/custo/tokens/veredicto) de um candidato contra uma baseline aprovada.

import (
	"context"
	"encoding/hex"
	"fmt"
)

// EvalVerdict é o veredicto binário de uma avaliação (pass|fail). É a decisão que o
// eval-gate consome: fail-closed, só [EvalPass] admite (ver [FailClosedGate]).
type EvalVerdict string

const (
	// EvalPass — a avaliação passou (candidato elegível a promoção).
	EvalPass EvalVerdict = "pass"
	// EvalFail — a avaliação falhou (candidato rejeitado, sem ir a produção).
	EvalFail EvalVerdict = "fail"
)

// EvalDataset é a origem do dataset avaliado. Distingue o golden-set curado (apanha
// regressões NOVAS) dos datasets derivados de falhas passadas (regressões
// CONHECIDAS). O registo do span funciona para AMBOS os tipos.
type EvalDataset string

const (
	// EvalDatasetGolden — golden-set curado e estável (regressões novas).
	EvalDatasetGolden EvalDataset = "golden"
	// EvalDatasetFailureDerived — dataset derivado de falhas passadas (regressões
	// conhecidas).
	EvalDatasetFailureDerived EvalDataset = "failure_derived"
)

// EvaluationResult é o resultado tipado de uma avaliação de uma trajectória. É o que
// o recorder emite como span [OpEvaluation] e o que o [EvalGate] consome. Não
// carrega payload — só rótulos/scores/identificadores e a referência (trace_id) à
// trajectória avaliada.
type EvaluationResult struct {
	// Suite é o identificador da suite/classe de artefacto comportamental avaliada.
	Suite string
	// EvalID é o identificador único desta execução de avaliação.
	EvalID string
	// Dataset é a origem do dataset (golden|failure_derived).
	Dataset EvalDataset
	// Verdict é o veredicto pass|fail (a decisão que o eval-gate consome).
	Verdict EvalVerdict
	// Score é a métrica numérica (tipicamente 0..1) associada ao veredicto.
	Score float64
	// TargetTraceID é o trace_id (16 bytes) da trajectória AVALIADA — a referência
	// EXPLÍCITA que liga a eval à trajectória mesmo quando o span de eval é emitido
	// num trace próprio. Quando o span de eval é emitido no mesmo trace da
	// trajectória (ctx a propagar o SpanContext da trajectória), o TraceID partilhado
	// já a estabelece; ainda assim o recorder grava este atributo, redundando-a.
	TargetTraceID [16]byte
}

// Passed reporta se o veredicto é explicitamente [EvalPass]. Qualquer outro valor
// (fail, vazio, desconhecido) é NÃO-passou — a base fail-closed do eval-gate.
func (r EvaluationResult) Passed() bool { return r.Verdict == EvalPass }

// TargetTraceIDHex devolve o trace_id-alvo em hex minúsculo de 32 dígitos, ou ""
// se não estiver definido (all-zero).
func (r EvaluationResult) TargetTraceIDHex() string {
	if r.TargetTraceID == ([16]byte{}) {
		return ""
	}
	return hex.EncodeToString(r.TargetTraceID[:])
}

// applyTo grava os atributos do resultado no span de eval. É a fonte única do
// mapeamento EvaluationResult→atributos (usada pelo recorder).
func (r EvaluationResult) applyTo(span Span) {
	span.SetAttribute(AttrOperationName, OpEvaluation)
	span.SetAttribute(AttrEvalVerdict, string(r.Verdict))
	span.SetAttribute(AttrEvalDataset, string(r.Dataset))
	span.SetAttribute(AttrEvalScore, r.Score)
	if r.Suite != "" {
		span.SetAttribute(AttrEvalSuite, r.Suite)
	}
	if r.EvalID != "" {
		span.SetAttribute(AttrEvalID, r.EvalID)
	}
	if hexID := r.TargetTraceIDHex(); hexID != "" {
		span.SetAttribute(AttrEvalTargetTraceID, hexID)
	}
}

// RecordEvaluation emite um span [OpEvaluation] para result via tr, LIGADO por
// trace_id à trajectória avaliada, e devolve o [SpanContext] do span emitido.
//
// A ligação faz-se por DUAS vias complementares, ambas activas:
//
//   - MESMO trace: se ctx propaga o [SpanContext] da trajectória avaliada (via
//     [ContextWithSpanContext]), o span de eval HERDA o seu TraceID — partilha o
//     trace, apontando o parent_span_id ao span da trajectória. É a ligação nativa
//     OTel, recuperável por qualquer exportador compatível.
//   - EXPLÍCITA: o atributo [AttrEvalTargetTraceID] grava o trace_id-alvo (de
//     result.TargetTraceID), ligando a eval mesmo se for emitida num trace próprio.
//
// Passe um result.TargetTraceID igual ao TraceID da trajectória (via
// [EvaluationResult.WithTarget] a partir do SpanContext dela) para que ambas as vias
// concordem. Com um [NoopTracer] devolve um SpanContext inválido (sem observabilidade).
//
// INVARIANTE DE LIGAÇÃO (fail-closed): a ligação é EXIGIDA, não uma convenção. Se
// NENHUMA das vias estiver presente — ctx sem SpanContext propagado válido E
// result.TargetTraceID all-zero — a eval ficaria num trace-raiz novo sem alvo, e um
// leitor não distinguiria "eval no mesmo trace" de "o chamador esqueceu o alvo",
// reportando um trace não-relacionado como alvo (correlação, garantia #2, quebrada em
// silêncio). Nesse caso RecordEvaluation RECUSA a emissão e devolve um SpanContext
// inválido (como o [NoopTracer]) — nunca produz um span de eval enganosamente
// auto-referente. É um erro do chamador tratável como o SpanContext inválido devolvido.
func RecordEvaluation(ctx context.Context, tr Tracer, result EvaluationResult) SpanContext {
	parent, hasParent := SpanContextFromContext(ctx)
	linked := (hasParent && parent.IsValid()) || result.TargetTraceIDHex() != ""
	if !linked {
		return SpanContext{}
	}
	_, span := tr.StartSpan(ctx, OpEvaluation)
	result.applyTo(span)
	sc := span.SpanContext()
	span.End()
	return sc
}

// WithTarget devolve uma cópia de r com o TargetTraceID fixado a partir do
// SpanContext da trajectória avaliada. Conveniência para ligar a eval à trajectória
// sem manipular bytes de trace_id no chamador.
func (r EvaluationResult) WithTarget(target SpanContext) EvaluationResult {
	r.TargetTraceID = target.TraceID
	return r
}

// EvaluationResultFromSpanData reconstrói o [EvaluationResult] tipado a partir de um
// span [OpEvaluation] exportado (ok=false se o span não é uma eval). É a leitura
// simétrica de [EvaluationResult.applyTo]: permite ao eval-gate consumir o resultado
// directamente dos spans exportados/recolhidos, fechando o ciclo emit→consume.
func EvaluationResultFromSpanData(sd SpanData) (EvaluationResult, bool) {
	if operationOf(sd) != OpEvaluation {
		return EvaluationResult{}, false
	}
	var r EvaluationResult
	r.Verdict = EvalVerdict(attrString(sd, AttrEvalVerdict))
	r.Dataset = EvalDataset(attrString(sd, AttrEvalDataset))
	r.Suite = attrString(sd, AttrEvalSuite)
	r.EvalID = attrString(sd, AttrEvalID)
	if v, ok := sd.Attribute(AttrEvalScore); ok {
		if f, ok := attrFloat64(v); ok {
			r.Score = f
		}
	}
	// O trace-alvo prefere o atributo explícito. Na sua ausência, só cai no TraceID do
	// próprio span quando o span de eval é um FILHO na árvore da trajectória (caso
	// "mesmo trace": nasceu com o ctx a propagar o SpanContext da trajectória, logo
	// tem parent_span_id não-nulo — o seu trace_id É o da trajectória avaliada). Um
	// span de eval RAIZ (sem pai) e sem atributo explícito NÃO partilha nenhuma
	// trajectória: o seu trace_id é um trace-raiz novo, não um alvo. Deixa-se então o
	// alvo por preencher (TargetTraceID all-zero ⇒ TargetTraceIDHex "" ⇒ "unlinked")
	// em vez de reportar CONFIANTEMENTE um trace não-relacionado como o alvo avaliado.
	if h := attrString(sd, AttrEvalTargetTraceID); h != "" {
		if raw, err := hex.DecodeString(h); err == nil && len(raw) == 16 {
			copy(r.TargetTraceID[:], raw)
		}
	} else if parentHexOf(sd) != "" {
		r.TargetTraceID = sd.SpanContext.TraceID
	}
	return r, true
}

// attrString lê um atributo string de uma SpanData (vazio se ausente/de outro tipo).
func attrString(sd SpanData, key string) string {
	if v, ok := sd.Attribute(key); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// PORTAS — EvalRunner + EvalGate (concreto = AOS-114/AOS-115, DIFERIDO)
// ---------------------------------------------------------------------------

// EvalTarget é a trajectória a avaliar entregue ao [EvalRunner]: o trace_id da
// trajectória e os seus spans (a árvore já emitida). O runner concreto (AOS-114,
// entregue em packages/platform/eval) corre o golden-set/dataset sobre esta
// trajectória e produz um [EvaluationResult].
type EvalTarget struct {
	// TraceID é o trace_id da trajectória avaliada (liga o resultado à trajectória).
	TraceID [16]byte
	// Spans é a árvore de spans da trajectória (leitura; o runner não a altera).
	Spans []SpanData
}

// EvalRunner é a PORTA para o harness de avaliação concreto (EPIC-11, AOS-114). Run
// avalia a trajectória target (golden-set curado e/ou dataset derivado de falhas) e
// devolve o [EvaluationResult]. O runner CONCRETO — os golden-sets curados, os
// datasets de falhas, o critério de pass/fail — pertence a AOS-114 e fica DIFERIDO.
// Aqui só a porta + impls de referência ([StaticEvalRunner], [EvalRunnerFunc]).
type EvalRunner interface {
	Run(ctx context.Context, target EvalTarget) EvaluationResult
}

// EvalRunnerFunc adapta uma função à porta [EvalRunner].
type EvalRunnerFunc func(ctx context.Context, target EvalTarget) EvaluationResult

// Run implementa [EvalRunner].
func (f EvalRunnerFunc) Run(ctx context.Context, target EvalTarget) EvaluationResult {
	return f(ctx, target)
}

// StaticEvalRunner é um runner de referência/fake que devolve sempre Result (com o
// TargetTraceID preenchido a partir do target, para o resultado ficar ligado à
// trajectória). É o duplo de teste desta folha — NÃO é um harness real (não corre
// golden-sets). O harness REAL já existe: packages/platform/eval (AOS-114). O que
// falta é o gate de promoção composto num caminho de produção (AOS-115) — ver
// DEF-009 do registo de deferimentos.
type StaticEvalRunner struct {
	Result EvaluationResult
}

// Run implementa [EvalRunner]: devolve Result ligado ao target.TraceID.
func (r StaticEvalRunner) Run(_ context.Context, target EvalTarget) EvaluationResult {
	out := r.Result
	out.TargetTraceID = target.TraceID
	return out
}

// EvalGate é a PORTA de admission-control da auto-modificação (ADR-012; eixo do
// concreto: AOS-115): a partir de um [EvaluationResult], Admit reporta se o artefacto
// comportamental
// candidato (skill/memória procedural auto-escrita) pode ser promovido. O gate
// CONCRETO — a política completa de promoção staging→canary→prod — pertence a
// AOS-114/AOS-115 e fica DIFERIDO; aqui a porta + a referência FAIL-CLOSED [FailClosedGate].
type EvalGate interface {
	Admit(result EvaluationResult) bool
}

// FailClosedGate é a referência do eval-gate: FAIL-CLOSED por construção. Admite
// SÓ um veredicto explícito [EvalPass] cujo Score atinja MinScore. Qualquer outro
// caso — veredicto fail, vazio, desconhecido, ou score abaixo do limiar — NÃO admite.
// Por omissão (MinScore 0) exige apenas o veredicto pass; um MinScore positivo
// acrescenta o limiar de score.
type FailClosedGate struct {
	// MinScore é o score mínimo exigido (além do veredicto pass). 0 = sem limiar.
	MinScore float64
}

// Admit implementa [EvalGate], fail-closed: só [EvalPass] com Score >= MinScore.
func (g FailClosedGate) Admit(result EvaluationResult) bool {
	if !result.Passed() {
		return false
	}
	return result.Score >= g.MinScore
}

// EvalGateFunc adapta uma função à porta [EvalGate].
type EvalGateFunc func(result EvaluationResult) bool

// Admit implementa [EvalGate].
func (f EvalGateFunc) Admit(result EvaluationResult) bool { return f(result) }

// ---------------------------------------------------------------------------
// TRACE-DIFFING vs baseline (regressões NOVAS)
// ---------------------------------------------------------------------------

// RegressionKind classifica a natureza de uma regressão comportamental detectada
// pelo trace-diffing.
type RegressionKind string

const (
	// RegressionToolSequence — a sequência ordenada de acções (tool calls) divergiu
	// (tool trocada, acrescentada, removida ou reordenada). É a regressão NOVA típica
	// de uma alteração de skill que nenhum dataset de falha conhecida apanharia.
	RegressionToolSequence RegressionKind = "tool_sequence"
	// RegressionCost — o custo agregado da trajectória divergiu além do limiar.
	RegressionCost RegressionKind = "cost"
	// RegressionTokens — o volume agregado de tokens divergiu além do limiar.
	RegressionTokens RegressionKind = "tokens"
)

// Regression é uma diferença comportamental SIGNIFICATIVA (acima do ruído tolerável)
// entre a baseline e o candidato, accionável para eval-driven development.
type Regression struct {
	// Kind é a natureza da regressão.
	Kind RegressionKind
	// Step é o índice (0-based) da acção divergente para [RegressionToolSequence], ou
	// -1 para regressões agregadas (custo/tokens).
	Step int
	// Baseline e Candidate descrevem o valor de cada lado (nomes de tool, ou o total).
	Baseline  string
	Candidate string
	// Detail é uma descrição legível da divergência.
	Detail string
}

// TraceDiffConfig são os limiares que separam RUÍDO TOLERÁVEL de REGRESSÃO
// significativa (AOS-115). O valor-zero é estrito: qualquer delta de custo/tokens > 0
// e qualquer troca de tool contam como regressão. Alargue os limiares para tolerar a
// variação esperada e evitar falsos-positivos por não-determinismo.
type TraceDiffConfig struct {
	// CostToleranceMicroUSD é o delta ABSOLUTO de custo (micro-USD) tolerado sem
	// sinalizar regressão. Um |custo_candidato - custo_baseline| <= este valor é ruído.
	CostToleranceMicroUSD int64
	// TokenTolerance é o delta ABSOLUTO de tokens totais tolerado sem sinalizar.
	TokenTolerance int64
}

// TraceDiff compara a árvore de spans de uma trajectória candidata contra uma
// baseline aprovada (ambas sobre o mesmo input) e devolve as regressões
// comportamentais SIGNIFICATIVAS. Compara a ESTRUTURA SEMÂNTICA — a sequência
// ordenada de acções (tool calls) e os agregados de custo/tokens — NORMALIZANDO os
// campos não-deterministas (trace_id/span_id/timestamps são IGNORADOS), pelo que só
// divergências de comportamento afloram.
//
// É esta comparação vs baseline que apanha uma regressão NOVA (uma tool trocada por
// uma alteração de skill, um salto de custo) que NENHUM dataset de falhas passadas
// apanharia — porque a regressão é definida por diferença face ao baseline aprovado,
// não por um caso de falha fixo. Baseline == candidato ⇒ zero regressões; uma
// variação dentro dos limiares de [TraceDiffConfig] não gera falso-positivo.
func TraceDiff(baseline, candidate []SpanData, cfg TraceDiffConfig) []Regression {
	var out []Regression

	// (1) Sequência de acções: compara os nomes de tool dos execute_tool por ordem.
	baseTools := orderedToolNames(baseline)
	candTools := orderedToolNames(candidate)
	out = append(out, diffToolSequence(baseTools, candTools)...)

	// (2) Custo/tokens agregados: soma sobre os spans chat (a unidade-verdade), sem
	// dupla-contagem (reutiliza a projecção de cost_aggregation.go).
	baseUsage := trajectoryUsage(baseline)
	candUsage := trajectoryUsage(candidate)

	if d := absInt64(candUsage.CostMicroUSD - baseUsage.CostMicroUSD); d > cfg.CostToleranceMicroUSD {
		out = append(out, Regression{
			Kind:      RegressionCost,
			Step:      -1,
			Baseline:  fmt.Sprintf("%d", baseUsage.CostMicroUSD),
			Candidate: fmt.Sprintf("%d", candUsage.CostMicroUSD),
			Detail:    fmt.Sprintf("custo agregado divergiu %d micro-USD (limiar %d)", d, cfg.CostToleranceMicroUSD),
		})
	}
	if d := absInt64(candUsage.TotalTokens() - baseUsage.TotalTokens()); d > cfg.TokenTolerance {
		out = append(out, Regression{
			Kind:      RegressionTokens,
			Step:      -1,
			Baseline:  fmt.Sprintf("%d", baseUsage.TotalTokens()),
			Candidate: fmt.Sprintf("%d", candUsage.TotalTokens()),
			Detail:    fmt.Sprintf("tokens agregados divergiram %d (limiar %d)", d, cfg.TokenTolerance),
		})
	}
	return out
}

// diffToolSequence compara duas sequências ordenadas de nomes de tool e devolve as
// regressões de sequência. Reporta a PRIMEIRA divergência posicional e, se os
// comprimentos diferirem, as acções em excesso/em falta.
func diffToolSequence(base, cand []string) []Regression {
	var out []Regression
	n := len(base)
	if len(cand) < n {
		n = len(cand)
	}
	for i := 0; i < n; i++ {
		if base[i] != cand[i] {
			out = append(out, Regression{
				Kind:      RegressionToolSequence,
				Step:      i,
				Baseline:  base[i],
				Candidate: cand[i],
				Detail:    fmt.Sprintf("acção %d: baseline %q, candidato %q", i, base[i], cand[i]),
			})
		}
	}
	// Acções em excesso no candidato (acrescentadas por uma alteração de skill).
	for i := n; i < len(cand); i++ {
		out = append(out, Regression{
			Kind:      RegressionToolSequence,
			Step:      i,
			Baseline:  "",
			Candidate: cand[i],
			Detail:    fmt.Sprintf("acção %d acrescentada pelo candidato: %q", i, cand[i]),
		})
	}
	// Acções em falta no candidato (removidas).
	for i := n; i < len(base); i++ {
		out = append(out, Regression{
			Kind:      RegressionToolSequence,
			Step:      i,
			Baseline:  base[i],
			Candidate: "",
			Detail:    fmt.Sprintf("acção %d da baseline em falta no candidato: %q", i, base[i]),
		})
	}
	return out
}

// orderedToolNames extrai, por ordem de aparição, os nomes de tool dos spans
// execute_tool de uma trajectória — a sequência de acções sobre a qual se faz o diff.
// Ignora os restantes spans (a ordem é a da lista, não a dos timestamps, que são
// normalizados). Um execute_tool sem nome de tool contribui com "".
func orderedToolNames(spans []SpanData) []string {
	var out []string
	for _, sd := range spans {
		if operationOf(sd) != OpExecuteTool {
			continue
		}
		out = append(out, attrString(sd, AttrToolName))
	}
	return out
}

// trajectoryUsage soma tokens/custo dos spans chat de uma trajectória (a unidade-
// verdade), reutilizando a projecção sem dupla-contagem de cost_aggregation.go.
func trajectoryUsage(spans []SpanData) UsageTotals {
	var t UsageTotals
	for _, sd := range spans {
		if cs, ok := chatSampleFromSpanData(sd); ok {
			t = t.add(cs.totals)
		}
	}
	return t
}

// absInt64 devolve o valor absoluto de v.
func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
