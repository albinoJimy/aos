package cost

import (
	"errors"
	"math"
	"testing"

	"github.com/aos-ref/platform/model-gateway/pricing"
)

// testTable é a tabela determinista dos testes de custo: quatro rates DISTINTOS.
func testTable(t *testing.T) *pricing.Table {
	t.Helper()
	tbl, err := pricing.NewTable("test", []pricing.Entry{
		{Model: "m", Region: "eu", Rate: pricing.Rate{
			InputPerMTokMicroUSD:      3_000_000,  // $3.00 / 1M
			OutputPerMTokMicroUSD:     15_000_000, // $15.00 / 1M
			CacheReadPerMTokMicroUSD:  300_000,    // $0.30 / 1M (mais barato)
			CacheWritePerMTokMicroUSD: 3_750_000,  // $3.75 / 1M (mais caro)
		}},
	})
	if err != nil {
		t.Fatalf("NewTable: %v", err)
	}
	return tbl
}

func TestCostBreakdownTableDriven(t *testing.T) {
	calc := NewCalculator(testTable(t))
	cases := []struct {
		name       string
		tc         TokenCounts
		wantInput  int64
		wantOutput int64
		wantCR     int64
		wantCW     int64
		wantTotal  int64
		wantTokens int64
	}{
		{
			name:       "so-input-output",
			tc:         TokenCounts{PromptTokens: 1000, CompletionTokens: 500},
			wantInput:  3000, // 1000*3_000_000/1e6
			wantOutput: 7500, // 500*15_000_000/1e6
			wantTotal:  10500,
			wantTokens: 1500,
		},
		{
			name:       "com-cache-read-write-rates-distintos",
			tc:         TokenCounts{PromptTokens: 1000, CompletionTokens: 500, CacheReadTokens: 200, CacheWriteTokens: 100},
			wantInput:  2400, // billable=800: 800*3_000_000/1e6
			wantOutput: 7500, // 500*15_000_000/1e6
			wantCR:     60,   // 200*300_000/1e6
			wantCW:     375,  // 100*3_750_000/1e6
			wantTotal:  10335,
			wantTokens: 1600, // prompt(1000)+completion(500)+cacheWrite(100); cacheRead é subconjunto do prompt
		},
		{
			name:       "tudo-cacheado-input-facturavel-zero",
			tc:         TokenCounts{PromptTokens: 500, CacheReadTokens: 500},
			wantInput:  0,   // billable = 500-500 = 0
			wantCR:     150, // 500*300_000/1e6
			wantTotal:  150,
			wantTokens: 500,
		},
		{
			name:       "cache-read-maior-que-prompt-saneado",
			tc:         TokenCounts{PromptTokens: 100, CacheReadTokens: 300},
			wantInput:  0,  // billable clamp >= 0
			wantCR:     90, // 300*300_000/1e6
			wantTotal:  90,
			wantTokens: 100,
		},
		{
			name:       "zero-tokens-custo-zero",
			tc:         TokenCounts{},
			wantTotal:  0,
			wantTokens: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			amt, bd, err := calc.CostBreakdown(c.tc, "m", "eu")
			if err != nil {
				t.Fatalf("CostBreakdown: %v", err)
			}
			if bd.InputMicroUSD != c.wantInput || bd.OutputMicroUSD != c.wantOutput ||
				bd.CacheReadMicroUSD != c.wantCR || bd.CacheWriteMicroUSD != c.wantCW {
				t.Fatalf("breakdown errado: %+v (esperava in=%d out=%d cr=%d cw=%d)", bd, c.wantInput, c.wantOutput, c.wantCR, c.wantCW)
			}
			if amt.CostMicroUSD != c.wantTotal {
				t.Fatalf("total errado: %d, esperava %d", amt.CostMicroUSD, c.wantTotal)
			}
			if amt.Tokens != c.wantTokens {
				t.Fatalf("tokens errado: %d, esperava %d", amt.Tokens, c.wantTokens)
			}
			if bd.Total() != amt.CostMicroUSD {
				t.Fatalf("breakdown.Total() %d != amount %d", bd.Total(), amt.CostMicroUSD)
			}
		})
	}
}

// TestTokensAxis_IncludesCacheWrite_ExcludesCacheRead FIXA a semântica do eixo de
// VOLUME de tokens do [Amount] (o que o burn-down por tokens consome, ADR-008/EPIC-03):
// Tokens = PromptTokens + CompletionTokens + CacheWriteTokens. O cache READ é
// subconjunto do prompt (não somado à parte); o cache WRITE é ADITIVO (volume real
// escrito ao cache) — coerente com a doc de [Calculator.CostBreakdown] e com o eixo de
// dinheiro (que também cobra o cache-write).
func TestTokensAxis_IncludesCacheWrite_ExcludesCacheRead(t *testing.T) {
	calc := NewCalculator(testTable(t))
	// cache READ (200) é subconjunto do prompt (1000): NÃO acresce ao volume.
	// cache WRITE (100) é ADITIVO: acresce ao volume.
	tc := TokenCounts{PromptTokens: 1000, CompletionTokens: 500, CacheReadTokens: 200, CacheWriteTokens: 100}
	amt, _, err := calc.CostBreakdown(tc, "m", "eu")
	if err != nil {
		t.Fatalf("CostBreakdown: %v", err)
	}
	if amt.Tokens != 1600 {
		t.Fatalf("eixo de tokens = %d, quer 1600 (prompt+completion+cacheWrite; cacheRead é subconjunto)", amt.Tokens)
	}

	// Propriedade: aumentar SÓ o cache WRITE aumenta o volume no mesmo delta;
	// aumentar SÓ o cache READ NÃO altera o volume (subconjunto do prompt).
	withMoreCW := TokenCounts{PromptTokens: 1000, CompletionTokens: 500, CacheReadTokens: 200, CacheWriteTokens: 100 + 50}
	amtCW, _, err := calc.CostBreakdown(withMoreCW, "m", "eu")
	if err != nil {
		t.Fatalf("CostBreakdown(+cw): %v", err)
	}
	if amtCW.Tokens != amt.Tokens+50 {
		t.Fatalf("cache-write não aditivo no eixo de tokens: %d, quer %d", amtCW.Tokens, amt.Tokens+50)
	}
	withMoreCR := TokenCounts{PromptTokens: 1000, CompletionTokens: 500, CacheReadTokens: 200 + 50, CacheWriteTokens: 100}
	amtCR, _, err := calc.CostBreakdown(withMoreCR, "m", "eu")
	if err != nil {
		t.Fatalf("CostBreakdown(+cr): %v", err)
	}
	if amtCR.Tokens != amt.Tokens {
		t.Fatalf("cache-read alterou o eixo de tokens (devia ser subconjunto do prompt): %d, quer %d", amtCR.Tokens, amt.Tokens)
	}
}

// TestCacheWrite_AdditiveBoundaryInvariant documenta e FIXA o invariante da fronteira
// do adaptador (AOS-062): PromptTokens NÃO inclui os CacheWriteTokens — o cache-write é
// ADITIVO. billableInput só subtrai o cache READ (subconjunto do prompt), pelo que o
// cache-write é cobrado UMA vez ao rate_cw e NUNCA também via rate_in. Um adaptador
// Anthropic-style (cache_creation_input_tokens separado de input_tokens) mapeia para
// este invariante; se um futuro adaptador enviasse o cache-write DENTRO do prompt,
// haveria sobrefacturação — este teste prende essa semântica.
func TestCacheWrite_AdditiveBoundaryInvariant(t *testing.T) {
	calc := NewCalculator(testTable(t))
	// Base sem cache-write.
	base := TokenCounts{PromptTokens: 1000, CompletionTokens: 500, CacheReadTokens: 200}
	_, bdBase, err := calc.CostBreakdown(base, "m", "eu")
	if err != nil {
		t.Fatalf("CostBreakdown(base): %v", err)
	}
	// Só acrescenta cache-write.
	withCW := base
	withCW.CacheWriteTokens = 100
	_, bdCW, err := calc.CostBreakdown(withCW, "m", "eu")
	if err != nil {
		t.Fatalf("CostBreakdown(+cw): %v", err)
	}
	// O input facturável (rate_in) NÃO muda com o cache-write — prova que o cache-write
	// não é cobrado pela via do input (sem dupla cobrança).
	if bdCW.InputMicroUSD != bdBase.InputMicroUSD {
		t.Fatalf("cache-write contaminou o custo de input: %d != %d (dupla cobrança)", bdCW.InputMicroUSD, bdBase.InputMicroUSD)
	}
	// O input facturável é (prompt-cacheRead)=800 ao rate_in: 800*3_000_000/1e6 = 2400.
	if bdCW.InputMicroUSD != 2400 {
		t.Fatalf("input facturável errado: %d, quer 2400 (800 tok ao rate_in)", bdCW.InputMicroUSD)
	}
	// O cache-write é cobrado UMA vez ao rate_cw: 100*3_750_000/1e6 = 375.
	if bdCW.CacheWriteMicroUSD != 375 {
		t.Fatalf("cache-write errado: %d, quer 375 (100 tok ao rate_cw, uma vez)", bdCW.CacheWriteMicroUSD)
	}
}

// TestRoundHalfUp prova a regra de arredondamento fixa (round-half-up) e a ausência
// de float drift: tudo aritmética int64.
func TestRoundHalfUp(t *testing.T) {
	cases := []struct {
		tokens int64
		rate   int64
		want   int64
	}{
		{1, 1_500_000, 2}, // 1.5 -> 2 (empate para cima)
		{1, 1_400_000, 1}, // 1.4 -> 1
		{1, 1_600_000, 2}, // 1.6 -> 2
		{1, 500_000, 1},   // 0.5 -> 1 (empate para cima)
		{1, 499_999, 0},   // 0.499999 -> 0
		{3, 333_333, 1},   // 0.999999 -> 1
	}
	for _, c := range cases {
		got, err := costForTokens(c.tokens, c.rate)
		if err != nil {
			t.Fatalf("costForTokens(%d,%d): %v", c.tokens, c.rate, err)
		}
		if got != c.want {
			t.Fatalf("costForTokens(%d,%d) = %d, esperava %d", c.tokens, c.rate, got, c.want)
		}
	}
}

// TestNoFloatDrift prova que a acumulação é EXACTA em int64: somar N custos por
// chamada dá exactamente o mesmo que multiplicar, sem drift de vírgula flutuante.
func TestNoFloatDrift(t *testing.T) {
	calc := NewCalculator(testTable(t))
	const n = 100_000
	var acc Amount
	tc := TokenCounts{PromptTokens: 7, CompletionTokens: 3} // custos com arredondamento
	per, _, err := calc.CostBreakdown(tc, "m", "eu")
	if err != nil {
		t.Fatalf("CostBreakdown: %v", err)
	}
	for i := 0; i < n; i++ {
		sum, ok := acc.AddChecked(per)
		if !ok {
			t.Fatalf("overflow inesperado na iteracao %d", i)
		}
		acc = sum
	}
	want := per.CostMicroUSD * int64(n)
	if acc.CostMicroUSD != want {
		t.Fatalf("acumulacao com drift: %d, esperava %d", acc.CostMicroUSD, want)
	}
}

func TestNegativeTokensFailClosed(t *testing.T) {
	calc := NewCalculator(testTable(t))
	_, _, err := calc.CostBreakdown(TokenCounts{PromptTokens: -1}, "m", "eu")
	if !errors.Is(err, ErrNegativeTokens) {
		t.Fatalf("esperava ErrNegativeTokens, obtive %v", err)
	}
}

func TestNoPriceFailClosed(t *testing.T) {
	calc := NewCalculator(testTable(t))
	_, _, err := calc.CostBreakdown(TokenCounts{PromptTokens: 10}, "m", "ap-south")
	if !errors.Is(err, ErrNoPrice) {
		t.Fatalf("esperava ErrNoPrice (regiao sem preco), obtive %v", err)
	}
	// Nil table também é fail-closed (nunca 0 silencioso).
	nilCalc := NewCalculator(nil)
	_, _, err = nilCalc.CostBreakdown(TokenCounts{PromptTokens: 10}, "m", "eu")
	if !errors.Is(err, ErrNoPrice) {
		t.Fatalf("esperava ErrNoPrice (sem tabela), obtive %v", err)
	}
}

func TestOverflowFailClosed(t *testing.T) {
	tbl, _ := pricing.NewTable("of", []pricing.Entry{
		{Model: "m", Region: "eu", Rate: pricing.Rate{InputPerMTokMicroUSD: math.MaxInt64}},
	})
	calc := NewCalculator(tbl)
	_, _, err := calc.CostBreakdown(TokenCounts{PromptTokens: math.MaxInt64}, "m", "eu")
	if !errors.Is(err, ErrOverflow) {
		t.Fatalf("esperava ErrOverflow, obtive %v", err)
	}
}

func TestAmountAddCheckedOverflow(t *testing.T) {
	a := Amount{CostMicroUSD: math.MaxInt64}
	if _, ok := a.AddChecked(Amount{CostMicroUSD: 1}); ok {
		t.Fatalf("esperava deteccao de overflow na soma de custo")
	}
	b := Amount{Tokens: math.MaxInt64}
	if _, ok := b.AddChecked(Amount{Tokens: 1}); ok {
		t.Fatalf("esperava deteccao de overflow na soma de tokens")
	}
}

func TestPricingVersionThreaded(t *testing.T) {
	calc := NewCalculator(testTable(t))
	if calc.PricingVersion() == "" {
		t.Fatalf("versao de preco vazia")
	}
	if NewCalculator(nil).PricingVersion() != "" {
		t.Fatalf("calculador sem tabela devia ter versao vazia")
	}
}
