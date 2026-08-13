// Package cost é a CONTABILIDADE DE CUSTO POR CHAMADA em USD do Model Gateway
// (AOS-062, tecnica/06 §4, ADR-008/ADR-010). Deriva o custo de cada model call a
// partir dos QUATRO tipos de token ([port.Usage]) e da tabela de preços versionada
// (pricing), em MICRO-USD INTEIRO — nunca float — compatível com [budget.Amount]
// (que alimenta o burn-down e o admission global, EPIC-03).
//
// # O que faz
//
//  1. CÁLCULO por chamada ([Calculator.Cost]) — custo em micro-USD int64 dos quatro
//     tipos de token, cada um ao seu rate DISTINTO (cache read < input; cache write
//     > input). Determinístico, overflow-checked, SEM float na acumulação.
//  2. AGREGAÇÃO por RUN e por ÁRVORE ([Recorder]) — acumula custo+tokens por run e
//     por árvore (treeID), disponível por PORTA para o burn-down/admission global
//     (ADR-008). Runs/árvores distintos NUNCA se contaminam (isolamento por chave).
//  3. EMISSÃO OTel ([MetricSink] + anotação de span) — custo em USD no span GenAI
//     (gen_ai.usage.cost_usd) + o micro-USD EXACTO (aos.cost.micro_usd, sem drift) +
//     tokens cache read/write, ligado a principal/modelo/região (AOS-057) e à
//     trajectória (run). SEM segredo (custo/tokens/versão-de-preço não são segredos).
//  4. RECONCILIAÇÃO — o custo agregado reconcilia com uma factura simulada do
//     provider dentro de uma tolerância acordada (ver reconcile_test.go e [Amount]).
//
// # Dinheiro em micro-USD INTEIRO (requisito duro, ADR-008)
//
// Todo o custo é int64 micro-USD (1 USD = 1_000_000 micro-USD). A acumulação é
// overflow-checked ([Amount.AddChecked], à imagem de budget.addChecked). O único
// ponto em que aparece um float é o ATRIBUTO de span em USD (gen_ai.usage.cost_usd,
// derivado por conveniência do OTel) — o micro-USD inteiro é a fonte de verdade e é
// emitido em paralelo (aos.cost.micro_usd) para a asserção/burn-down serem exactos.
//
// # Layering (decisão documentada)
//
// Este pacote é ZERO-dep de control-plane: define o seu próprio [Amount]
// (Tokens+CostMicroUSD, espelho de budget.Amount) e a aritmética checked, para ser
// testável isoladamente e não puxar o Escalonador para o caminho de metering. A
// ponte para budget.Amount (o tipo que o burn-down consome) vive num adaptador FINO
// em metering/cost/budgetbridge — o único ponto que importa control-plane/budget,
// à imagem do routing/tieradapter de AOS-059.
//
// # Zero dependências externas / determinismo
//
// Só stdlib. Sem relógio nem rand na decisão de custo (aritmética inteira
// determinista); o relógio injectável só carimba timestamps de métrica/evento.
package cost

import (
	"errors"
	"fmt"

	"github.com/aos-ref/platform/model-gateway/pricing"
)

// microPerUSD é o factor micro-USD por USD (1 USD = 1_000_000 micro-USD) E o
// denominador da conversão rate-por-1M-tokens → custo (o rate já é micro-USD por
// 1M tokens, pelo que tokens*rate/1_000_000 dá micro-USD). Coincidem por desenho.
const microPerUSD = 1_000_000

// Erros fail-closed do cálculo de custo.
var (
	// ErrNegativeTokens — o usage reporta um contador de token negativo. Fail-closed
	// (documentado): um token negativo é impossível e envenenaria o custo/burn-down.
	ErrNegativeTokens = errors.New("cost: contador de tokens negativo (fail-closed)")
	// ErrOverflow — a multiplicação/soma do custo transbordaria int64. Fail-closed:
	// um custo não-representável é um erro atribuível, nunca um valor truncado.
	ErrOverflow = errors.New("cost: overflow no calculo de custo em micro-USD (fail-closed)")
	// ErrNoPrice reexporta o fail-closed de preço ausente (pricing.ErrNoPrice) para
	// os chamadores do cost fazerem errors.Is sem importar pricing directamente.
	ErrNoPrice = pricing.ErrNoPrice
)

// Amount é o custo nas duas dimensões que o AOS controla — Tokens e custo em
// micro-USD (CostMicroUSD) — ambos INTEIROS (nunca float), espelho estrutural de
// [budget.Amount] (ADR-008). O burn-down consome esta medida (via o adaptador
// budgetbridge). A acumulação é overflow-checked ([AddChecked]).
type Amount struct {
	Tokens       int64
	CostMicroUSD int64
}

// AddChecked soma componente-a-componente detectando overflow de int64 em qualquer
// dimensão (à imagem de budget.addChecked). ok=false se alguma dimensão transbordar
// — o agregador trata como fail-closed (nunca acumula um valor truncado que
// falsificaria o burn-down).
func (a Amount) AddChecked(b Amount) (Amount, bool) {
	t := a.Tokens + b.Tokens
	if (b.Tokens > 0 && t < a.Tokens) || (b.Tokens < 0 && t > a.Tokens) {
		return Amount{}, false
	}
	c := a.CostMicroUSD + b.CostMicroUSD
	if (b.CostMicroUSD > 0 && c < a.CostMicroUSD) || (b.CostMicroUSD < 0 && c > a.CostMicroUSD) {
		return Amount{}, false
	}
	return Amount{Tokens: t, CostMicroUSD: c}, true
}

// Breakdown é o custo DECOMPOSTO por tipo de token (micro-USD int64) — útil para a
// reconciliação e para o registo por chamada (transparência de onde vem o custo). A
// soma das quatro componentes é [Amount.CostMicroUSD].
type Breakdown struct {
	InputMicroUSD      int64
	OutputMicroUSD     int64
	CacheReadMicroUSD  int64
	CacheWriteMicroUSD int64
}

// Total soma as quatro componentes (sem overflow-check: os termos individuais já
// foram checked em [costForTokens] e a soma cabe porque cada termo <= custo total,
// que é checked em [Calculator.CostBreakdown]).
func (b Breakdown) Total() int64 {
	return b.InputMicroUSD + b.OutputMicroUSD + b.CacheReadMicroUSD + b.CacheWriteMicroUSD
}

// TokenCounts são os quatro contadores de token de uma chamada, projectados de
// [port.Usage]. São a entrada do cálculo de custo, DESACOPLADA do tipo port.Usage
// para o cálculo ser testável sem o pacote port.
type TokenCounts struct {
	PromptTokens     int64
	CompletionTokens int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

// billableInput é o input NÃO-cacheado (o "in_tokens" da fórmula do ticket): os
// tokens de prompt menos os servidos por cache de leitura.
//
// MODELAÇÃO (documentada, coerente com AOS-061/cache_sli): os CacheReadTokens são um
// SUBCONJUNTO dos PromptTokens (semântica OpenAI/Anthropic — os cached tokens são
// parte do input total). Cobrar rate_in sobre o prompt INTEIRO E rate_cr sobre o
// cache read DUPLICARIA a cobrança dos tokens em cache. Por isso o input facturável
// é (prompt − cache_read), ao rate_in, e o cache read é cobrado à parte ao rate_cr
// (mais barato). A fórmula do ticket
//
//	cost = in*rate_in + out*rate_out + cache_read*rate_cr + cache_write*rate_cw
//
// mantém-se EXACTA com in = input não-cacheado. Um cache_read > prompt (provider
// inconsistente) é saneado (billableInput nunca < 0). O CacheWrite é ADITIVO
// (criação de cache, cobrada por cima — não faz parte do prompt).
func (tc TokenCounts) billableInput() int64 {
	in := tc.PromptTokens - tc.CacheReadTokens
	if in < 0 {
		in = 0
	}
	return in
}

// Calculator deriva o custo de uma chamada a partir de uma tabela de preços
// versionada. Stateless e determinista; partilhável concorrentemente (a tabela é
// imutável). Construir com [NewCalculator].
type Calculator struct {
	table *pricing.Table
}

// NewCalculator constrói o calculador sobre uma tabela de preços. Uma tabela nil
// torna todo o cálculo fail-closed (ErrNoPrice) — nunca um custo 0 silencioso.
func NewCalculator(table *pricing.Table) *Calculator {
	return &Calculator{table: table}
}

// PricingVersion devolve a versão tamper-evident da tabela em vigor ("versão#digest12"),
// para selar o custo à versão EXACTA de preços (base da reconciliação). Vazio se não
// há tabela.
func (c *Calculator) PricingVersion() string {
	if c.table == nil {
		return ""
	}
	return c.table.Version()
}

// HasPrice reporta se o par (modelo, região) TEM preço na tabela em vigor. É uma
// leitura PURA de COBERTURA — não calcula custo nenhum e não toca em estado.
//
// PORQUE EXISTE. O custo é fail-closed por (modelo, região): um par sem preço RECUSA
// a chamada em [CostBreakdown]. Só que essa recusa acontece DEPOIS de o provider ter
// sido invocado (e facturado) — o gateway calcula o custo com o usage em mão. Quem
// COMPÕE um deployment que pode despachar mais do que o modelo pedido (a escada de
// tiers do roteamento) precisa de cruzar essa escada com a tabela ANTES de servir
// tráfego, e é para isso que esta porta existe: transformar uma falha por chamada,
// depois de gastar dinheiro, numa recusa de ARRANQUE única e diagnosticável.
//
// Sem tabela, nada tem preço (fail-closed — nunca «cobre tudo» por ausência de dados).
func (c *Calculator) HasPrice(model, region string) bool {
	if c == nil || c.table == nil {
		return false
	}
	_, ok := c.table.RateFor(model, region)
	return ok
}

// CostBreakdown calcula o custo DECOMPOSTO e o [Amount] agregado de uma chamada,
// para (modelo, região). Fail-closed:
//   - tokens negativos ⇒ [ErrNegativeTokens];
//   - (modelo, região) sem preço ⇒ [ErrNoPrice] (custo NÃO-calculável, nunca 0);
//   - overflow int64 ⇒ [ErrOverflow].
//
// A dimensão Tokens do [Amount] é PromptTokens+CompletionTokens+CacheWriteTokens (o
// volume facturável de tokens que o burn-down controla): o cache READ é subconjunto do
// prompt e NÃO é somado duas vezes; o cache WRITE é ADITIVO (não faz parte do prompt —
// ver [TokenCounts.billableInput]) e É somado por ser volume real escrito ao cache,
// coerente com o eixo de DINHEIRO (que também cobra o cache-write ao rate_cw).
func (c *Calculator) CostBreakdown(tc TokenCounts, model, region string) (Amount, Breakdown, error) {
	if tc.PromptTokens < 0 || tc.CompletionTokens < 0 || tc.CacheReadTokens < 0 || tc.CacheWriteTokens < 0 {
		return Amount{}, Breakdown{}, fmt.Errorf("%w: (%s,%s)", ErrNegativeTokens, model, region)
	}
	if c.table == nil {
		return Amount{}, Breakdown{}, fmt.Errorf("%w: (%s,%s) [sem tabela]", ErrNoPrice, model, region)
	}
	rate, ok := c.table.RateFor(model, region)
	if !ok {
		return Amount{}, Breakdown{}, fmt.Errorf("%w: (%s,%s)", ErrNoPrice, model, region)
	}

	inputCost, err := costForTokens(tc.billableInput(), rate.InputPerMTokMicroUSD)
	if err != nil {
		return Amount{}, Breakdown{}, err
	}
	outputCost, err := costForTokens(tc.CompletionTokens, rate.OutputPerMTokMicroUSD)
	if err != nil {
		return Amount{}, Breakdown{}, err
	}
	cacheReadCost, err := costForTokens(tc.CacheReadTokens, rate.CacheReadPerMTokMicroUSD)
	if err != nil {
		return Amount{}, Breakdown{}, err
	}
	cacheWriteCost, err := costForTokens(tc.CacheWriteTokens, rate.CacheWritePerMTokMicroUSD)
	if err != nil {
		return Amount{}, Breakdown{}, err
	}

	bd := Breakdown{
		InputMicroUSD:      inputCost,
		OutputMicroUSD:     outputCost,
		CacheReadMicroUSD:  cacheReadCost,
		CacheWriteMicroUSD: cacheWriteCost,
	}
	// Soma overflow-checked das quatro componentes (defesa-em-profundidade; cada
	// termo já é não-negativo e checked, mas a soma poderia teoricamente transbordar).
	total, ok := addChecked4(inputCost, outputCost, cacheReadCost, cacheWriteCost)
	if !ok {
		return Amount{}, Breakdown{}, fmt.Errorf("%w: soma de componentes (%s,%s)", ErrOverflow, model, region)
	}
	// Eixo de VOLUME de tokens do burn-down: prompt (que já inclui o cache READ como
	// subconjunto — não somado à parte) + completion + cache WRITE (ADITIVO, volume
	// real escrito ao cache; ver billableInput). Overflow-checked em cada passo.
	tokens, ok := addChecked2(tc.PromptTokens, tc.CompletionTokens)
	if !ok {
		return Amount{}, Breakdown{}, fmt.Errorf("%w: soma de tokens (%s,%s)", ErrOverflow, model, region)
	}
	tokens, ok = addChecked2(tokens, tc.CacheWriteTokens)
	if !ok {
		return Amount{}, Breakdown{}, fmt.Errorf("%w: soma de tokens (%s,%s)", ErrOverflow, model, region)
	}
	return Amount{Tokens: tokens, CostMicroUSD: total}, bd, nil
}

// Cost é a variante que devolve apenas o [Amount] agregado (conveniência).
func (c *Calculator) Cost(tc TokenCounts, model, region string) (Amount, error) {
	amt, _, err := c.CostBreakdown(tc, model, region)
	return amt, err
}

// costForTokens converte (tokens, rate-por-1M-tokens) em custo micro-USD int64, com
// a REGRA DE ARREDONDAMENTO fixa e documentada: ROUND-HALF-UP (arredonda ao inteiro
// mais próximo; empates para cima). Determinístico e sem float:
//
//	custo = floor((tokens * ratePerMTok + 500_000) / 1_000_000)
//
// tokens e rate são não-negativos aqui (validados a montante). A multiplicação é
// overflow-checked ([ErrOverflow]); o +500_000 antes da divisão não transborda
// porque o produto checked deixa margem folgada face a int64.
func costForTokens(tokens, ratePerMTok int64) (int64, error) {
	if tokens == 0 || ratePerMTok == 0 {
		return 0, nil
	}
	prod, ok := mulChecked(tokens, ratePerMTok)
	if !ok {
		return 0, fmt.Errorf("%w: %d tokens * %d micro-USD/Mtok", ErrOverflow, tokens, ratePerMTok)
	}
	// Round-half-up. prod >= 0; o +microPerUSD/2 não transborda (mulChecked garantiu
	// prod <= MaxInt64, e um produto perto do tecto exigiria tokens/rate irrealistas;
	// verificamos mesmo assim).
	half := int64(microPerUSD / 2)
	if prod > (1<<63-1)-half {
		return 0, fmt.Errorf("%w: arredondamento", ErrOverflow)
	}
	return (prod + half) / microPerUSD, nil
}

// mulChecked multiplica dois int64 NÃO-NEGATIVOS detectando overflow.
func mulChecked(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	p := a * b
	if p/b != a {
		return 0, false
	}
	return p, true
}

// addChecked2 soma dois int64 não-negativos detectando overflow.
func addChecked2(a, b int64) (int64, bool) {
	s := a + b
	if s < a {
		return 0, false
	}
	return s, true
}

// addChecked4 soma quatro int64 não-negativos detectando overflow.
func addChecked4(a, b, c, d int64) (int64, bool) {
	s1, ok := addChecked2(a, b)
	if !ok {
		return 0, false
	}
	s2, ok := addChecked2(c, d)
	if !ok {
		return 0, false
	}
	return addChecked2(s1, s2)
}
