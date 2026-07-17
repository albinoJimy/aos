package agentruntime

import "context"

// Desacoplamento resumo↔árvore (AOS-077, Princípio 4: contexto ≠ registo).
//
// Um sub-agente delegado tem DOIS artefactos com destinos DISTINTOS a partir da
// MESMA execução:
//
//   - a ÁRVORE DE SPANS COMPLETA — invoke_agent → chat → execute_tool de todos os
//     turnos — que rt.tracer exporta SEMPRE ao backend de observabilidade (para
//     debug/drill-down, eval-driven development e replay fiel). Descartar isto do
//     backend NUNCA é legítimo;
//   - o RESUMO HIGIENIZADO ([TrajectorySummary]) — pequeno, limitado a ~1–2k tokens —
//     que é o ÚNICO que volta ao CONTEXTO do pai (higiene de contexto, menos custo).
//     Descartar a trajectória do contexto injectado é legítimo.
//
// Os dois eixos não se contaminam: o resumo ser pequeno NÃO amputa a árvore no
// backend; a árvore ser completa NÃO incha o contexto do pai. É esta separação que
// resolve a contradição "avaliamos trajectórias, não saídas" vs "o filho só devolve
// o resumo ao pai".

// DefaultSummaryMaxTokens é o tecto por omissão do resumo devolvido ao contexto do
// pai (~1–2k tokens de higiene). Configurável por chamada — NÃO é uma constante
// mágica de política.
const DefaultSummaryMaxTokens = 2000

// TrajectorySummary é o artefacto de RESUMO devolvido ao CONTEXTO do pai — distinto
// da árvore de spans persistida no backend. É deliberadamente pequeno e limitado
// por tamanho: transporta o desfecho higienizado do sub-agente, não a sua
// trajectória. NÃO contém spans, tool-results crus, nem contabilidade de custo/token
// por span (isso é a árvore no backend, e a fina é AOS-078).
type TrajectorySummary struct {
	// RunID identifica a trajectória do sub-agente (correlaciona o resumo com a
	// sub-árvore completa no backend, pelo aos.run_id dos spans).
	RunID string
	// Text é o resumo higienizado (a resposta final do sub-agente), TRUNCADO ao
	// limite de tokens. É o que se injecta no tail do pai.
	Text string
	// Turns e Terminated são metadados de desfecho baratos (não a trajectória).
	Turns      int
	Terminated bool
	// Truncated indica que Text foi cortado para caber em MaxTokens (a trajectória
	// completa continua no backend, nunca se perde).
	Truncated bool
	// MaxTokens é o tecto de tokens aplicado a Text (proveniência da higienização).
	MaxTokens int
}

// Summarize projecta um [Result] no artefacto de resumo LIMITADO a devolver ao
// contexto do pai. maxTokens ≤ 0 usa [DefaultSummaryMaxTokens]. A árvore de spans
// da execução é independente deste resumo — já foi exportada pelo tracer à medida
// que cada span fechou, pelo que resumir NUNCA perde trajectória.
func (r Result) Summarize(maxTokens int) TrajectorySummary {
	if maxTokens <= 0 {
		maxTokens = DefaultSummaryMaxTokens
	}
	text, truncated := truncateToApproxTokens(r.FinalText, maxTokens)
	return TrajectorySummary{
		RunID:      r.RunID,
		Text:       text,
		Turns:      r.Turns,
		Terminated: r.Terminated,
		Truncated:  truncated,
		MaxTokens:  maxTokens,
	}
}

// RunDelegated corre um sub-agente DELEGADO e devolve OS DOIS artefactos distintos
// (AOS-077): o [Result] completo — cuja árvore de spans rt.tracer já exportou na
// íntegra ao backend — e o [TrajectorySummary] limitado que volta ao CONTEXTO do
// pai. O ctx-raiz é semeado a partir de goal.ParentTraceParent (via [Run]), pelo que
// o invoke_agent do filho parenteia sob a âncora do Spawn (mesmo trace_id do pai).
//
// O resumo é devolvido MESMO em erro: a trajectória parcial do sub-agente está
// exportada no backend independentemente do desfecho — nada se perde no handoff.
func (rt *Runtime) RunDelegated(ctx context.Context, goal Goal, summaryMaxTokens int) (Result, TrajectorySummary, error) {
	res, err := rt.Run(ctx, goal)
	return res, res.Summarize(summaryMaxTokens), err
}

// approxTokensPerBudgetUnit é o nº de runes que se conta por "token" na estimativa
// grosseira do tamanho do resumo. É um PROXY de tamanho para higiene de contexto —
// NÃO contabilidade fina de tokens por span (AOS-078). ~4 chars/token é a heurística
// habitual para texto latino.
const approxTokensPerBudgetUnit = 4

// truncateToApproxTokens corta s para caber em maxTokens (estimados a
// [approxTokensPerBudgetUnit] runes por token), preservando runes inteiros. Devolve
// o texto (possivelmente cortado) e se houve corte.
func truncateToApproxTokens(s string, maxTokens int) (string, bool) {
	if maxTokens <= 0 {
		return "", s != ""
	}
	maxRunes := maxTokens * approxTokensPerBudgetUnit
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s, false
	}
	return string(runes[:maxRunes]), true
}
