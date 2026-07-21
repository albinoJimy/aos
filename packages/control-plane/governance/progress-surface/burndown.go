package progresssurface

import (
	"github.com/aos-ref/control-plane/budget"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Burndown é o BURN-DOWN de custo de uma trajectória (AC1): o consumido vs o orçamento e
// a fracção consumida. O Consumed é LIDO da agregação PÚBLICA dos spans (EPIC-08) — a
// superfície NUNCA recontabiliza custo (AC4). O Limit vem do orçamento por-árvore
// (EPIC-03) via a porta BudgetReader.
type Burndown struct {
	// Consumed é o custo/tokens já gastos, LIDO de otelgenai.AggregateByTrace (a soma dos
	// spans `chat` do trace) — NÃO uma soma-a-mão paralela.
	Consumed budget.Amount
	// Limit é o tecto do orçamento por-árvore (EPIC-03).
	Limit budget.Amount
	// Fraction é a fracção consumida em [0,1+): max sobre as duas dimensões (custo,
	// tokens) para limit>0; 0 se o limite é nulo (fail-safe — sem denominador não há
	// fracção que dispare o prompt).
	Fraction float64
}

// ComputeBurndown computa o burn-down LENDO a agregação pública: o consumido é
// otelgenai.AggregateByTrace(spans)[traceID] (a MESMA fonte-verdade do custo por span,
// sem dupla-contagem — só os spans `chat`), NÃO uma re-soma. O Limit é passado pelo
// chamador (lido da porta BudgetReader). Custo em micro-USD int64 (ADR-008), tokens em
// int64 — sem float na contabilidade; a fracção é o único float, e é derivada, não
// acumulada.
//
// AC4: Consumed.CostMicroUSD == otelgenai.AggregateByTrace(spans)[traceID].CostMicroUSD —
// por construção, é o MESMO valor, não uma cópia recontabilizada.
func ComputeBurndown(spans []otelgenai.SpanData, traceID string, limit budget.Amount) Burndown {
	totals := otelgenai.AggregateByTrace(spans)[traceID]
	consumed := budget.Amount{
		Tokens:       totals.TotalTokens(),
		CostMicroUSD: totals.CostMicroUSD,
	}
	return Burndown{
		Consumed: consumed,
		Limit:    limit,
		Fraction: consumedFraction(consumed, limit),
	}
}

// consumedFraction é a fracção consumida — a MESMA fórmula que o breaker usa no aviso
// ~80%: max( custo_consumido/custo_limite , tokens_consumidos/tokens_limite ), avaliada só
// nas dimensões com limite positivo. Uma dimensão sem limite (0) não contribui (evita a
// divisão por zero); ambas a zero ⇒ fracção 0 (fail-safe: sem orçamento não se dispara o
// prompt de exaustão — não se pode exaurir o que não tem tecto). Leitura PURA: não muta
// nada.
func consumedFraction(consumed, limit budget.Amount) float64 {
	var f float64
	if limit.CostMicroUSD > 0 {
		f = float64(consumed.CostMicroUSD) / float64(limit.CostMicroUSD)
	}
	if limit.Tokens > 0 {
		if ft := float64(consumed.Tokens) / float64(limit.Tokens); ft > f {
			f = ft
		}
	}
	return f
}
