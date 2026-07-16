package cost

import (
	"testing"

	"github.com/aos-ref/platform/model-gateway/pricing"
)

// TestReconciliationWithinTolerance prova que o custo AGREGADO (soma dos custos por
// chamada, cada um arredondado) reconcilia com uma FACTURA SIMULADA do provider
// (calculada uma vez sobre os tokens AGREGADOS) dentro de uma TOLERÂNCIA ACORDADA.
//
// # A tolerância e porquê existe
//
// A diferença provém EXCLUSIVAMENTE do arredondamento POR CHAMADA vs POR FACTURA: a
// nossa contabilidade arredonda o custo de cada chamada (round-half-up, por tipo de
// token), enquanto o provider tipicamente factura sobre os totais do período (um
// arredondamento por tipo de token). Para N chamadas e 4 tipos de token, cada tipo
// contribui um erro de arredondamento em [-0.5, +0.5] micro-USD por chamada; o limite
// superior do desvio acumulado é
//
//	tolerância = 4 tipos × (N + 1) × 0.5 micro-USD  ≈ 2·(N+1) micro-USD
//
// que é uma fracção ínfima do total (micro-USD sobre um custo em USD). O teste
// assere que |agregado − factura| <= tolerância. É a reconciliação exigida pelo DoD.
func TestReconciliationWithinTolerance(t *testing.T) {
	tbl, err := pricing.NewTable("recon", []pricing.Entry{
		{Model: "m", Region: "eu", Rate: pricing.Rate{
			InputPerMTokMicroUSD:      3_000_000,
			OutputPerMTokMicroUSD:     15_000_000,
			CacheReadPerMTokMicroUSD:  300_000,
			CacheWritePerMTokMicroUSD: 3_750_000,
		}},
	})
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	rate, _ := tbl.RateFor("m", "eu")
	calc := NewCalculator(tbl)

	// N chamadas DETERMINISTAS com contagens de token variadas (forçam arredondamento).
	const n = 5000
	var (
		aggregate    Amount // soma dos custos por chamada (a nossa medida)
		sumBillable  int64
		sumOutput    int64
		sumCacheRead int64
		sumCacheWr   int64
	)
	for i := 0; i < n; i++ {
		tc := TokenCounts{
			PromptTokens:     int64(700 + (i*37)%900),
			CompletionTokens: int64(123 + (i*17)%400),
			CacheReadTokens:  int64((i * 7) % 300),
			CacheWriteTokens: int64((i * 3) % 150),
		}
		amt, _, err := calc.CostBreakdown(tc, "m", "eu")
		if err != nil {
			t.Fatalf("CostBreakdown[%d]: %v", i, err)
		}
		sum, ok := aggregate.AddChecked(amt)
		if !ok {
			t.Fatalf("overflow[%d]", i)
		}
		aggregate = sum

		bi := tc.PromptTokens - tc.CacheReadTokens
		if bi < 0 {
			bi = 0
		}
		sumBillable += bi
		sumOutput += tc.CompletionTokens
		sumCacheRead += tc.CacheReadTokens
		sumCacheWr += tc.CacheWriteTokens
	}

	// FACTURA SIMULADA do provider: um arredondamento por tipo de token sobre os totais.
	invoice := mustCost(t, sumBillable, rate.InputPerMTokMicroUSD) +
		mustCost(t, sumOutput, rate.OutputPerMTokMicroUSD) +
		mustCost(t, sumCacheRead, rate.CacheReadPerMTokMicroUSD) +
		mustCost(t, sumCacheWr, rate.CacheWritePerMTokMicroUSD)

	diff := aggregate.CostMicroUSD - invoice
	if diff < 0 {
		diff = -diff
	}
	// Limite de PIOR CASO derivado exactamente: 4 tipos × (N+1) × 0.5 = 2·(N+1) micro-USD
	// (N roundings por-chamada + 1 por-factura, por tipo). Um erro sistemático na banda
	// [2(N+1),4(N+1)] deixaria de passar com esta tolerância apertada.
	tolerance := int64(2 * (n + 1))
	if diff > tolerance {
		t.Fatalf("reconciliacao fora da tolerancia: |%d - %d| = %d > %d micro-USD",
			aggregate.CostMicroUSD, invoice, diff, tolerance)
	}
	// Sanidade: a diferença é ínfima face ao total (< 0.01%).
	if aggregate.CostMicroUSD == 0 || diff*10000 > aggregate.CostMicroUSD {
		t.Fatalf("diferenca de reconciliacao inesperadamente grande: diff=%d total=%d", diff, aggregate.CostMicroUSD)
	}
}

func mustCost(t *testing.T, tokens, rate int64) int64 {
	t.Helper()
	c, err := costForTokens(tokens, rate)
	if err != nil {
		t.Fatalf("costForTokens: %v", err)
	}
	return c
}
