package main

// AOS-257 — O TECTO DE ORÇAMENTO POR-RUN DO NÓ (token-only).
//
// A superfície é DELIBERADAMENTE de UMA variável, e a estreiteza é a decisão:
//
//   - AOS_BUDGET_MAX_TOKENS — o tecto, em TOKENS, que CADA run recebe. Vazia ⇒ o nó corre
//     SEM orçamento nenhum (o ponto de injecção "budget" fica com o stub neutro) e o banner
//     declara-o. Esse é o default DECLARADO: não se inventa aqui um número de tokens que o
//     nó não sabe justificar — o valor certo depende do modelo, do preço e do perfil da
//     carga, exactamente como nas velocidades de queima do disjuntor (AOS-246).
//
//   - AOS_BUDGET_MAX_COST_MICRO_USD (AOS-260) — o tecto OPCIONAL em DÓLARES, em micro-USD
//     INTEIRO (1 USD = 1 000 000), que cada run recebe. Vazia ⇒ a dimensão $ é MEDIDA e
//     legível mas NUNCA NEGA ([integration.UnlimitedCostMicroUSD]); definida ⇒ decide a par
//     dos tokens (uma reserva só cabe se couber nas DUAS dimensões). Exige orçamento: sem
//     AOS_BUDGET_MAX_TOKENS não há árvore onde pendurar o tecto, e o arranque aborta.
//
// Esta segunda variável não existia — e a ausência era uma decisão declarada, não um
// esquecimento: sem canal de custo ponta a ponta um tecto em $ seria comparado com um consumo
// contado a ZERO e não negaria nada. AOS-259 ligou o canal (o `translateResponse` do model
// gateway preenche `CostMicroUSD` a partir da tabela de preços) e AOS-260 pô-lo a debitar a
// árvore no turno de modelo; o tecto em dólares passou a ter com o que ser comparado.
//
// FAIL-CLOSED NA CONFIGURAÇÃO (molde de [ErrBadBreakerThresholds]/[ErrBadRetention]): um
// valor ilegível, negativo ou ZERO ABORTA o arranque. O zero merece a nota explícita: um
// tecto de 0 tokens não desliga o orçamento — nenhuma estimativa cabe em zero, logo NEGARIA
// 100% das tool calls, que é precisamente o modo de falha que AOS-256 existe para evitar.
// Quem quer o nó sem orçamento deixa a variável por definir.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// ErrBadBudget — o tecto de orçamento por-run está definido mas é inválido. O nó recusa
// arrancar em vez de degradar em silêncio para "sem orçamento" (o operador ficaria
// convencido de que há tecto quando não há) ou para "tudo negado".
var ErrBadBudget = errors.New("aos: orcamento por-run mal configurado — AOS_BUDGET_MAX_TOKENS tem de ser um inteiro > 0 (tokens por run). Deixe a variavel POR DEFINIR para correr sem orcamento; 0 nao desliga o orcamento, negaria todas as tool calls")

// ErrBadBudgetCost — o tecto em dólares está definido mas é inválido, OU foi pedido sem
// tecto em tokens.
//
// O segundo caso merece o erro próprio (e não um default silencioso) pela mesma razão de
// [ErrProgressBudgetUnwired]: sem AOS_BUDGET_MAX_TOKENS não há orçamento nenhum composto — nem
// árvore, nem nó por-run, nem hook na cadeia. Um tecto em $ sozinho seria escrito na config,
// lido pelo operador como protecção, e não existiria em lado nenhum.
var ErrBadBudgetCost = errors.New("aos: tecto de custo por-run mal configurado — AOS_BUDGET_MAX_COST_MICRO_USD tem de ser um inteiro > 0 (micro-USD por run) e exige AOS_BUDGET_MAX_TOKENS definida. Deixe a variavel POR DEFINIR para MEDIR dolares sem tecto em dolares; 0 nao desliga o tecto, negaria todo o turno de modelo com custo")

// budgetFromEnv resolve o orçamento por-run a partir do ambiente. Devolve (nil, nil) quando
// AOS_BUDGET_MAX_TOKENS não está definida — o estado que o banner declara como NÃO COMPOSTO.
func budgetFromEnv() (*integration.RunBudget, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_BUDGET_MAX_TOKENS"))
	rawCost := strings.TrimSpace(os.Getenv("AOS_BUDGET_MAX_COST_MICRO_USD"))
	if raw == "" {
		if rawCost != "" {
			return nil, fmt.Errorf("%w: AOS_BUDGET_MAX_COST_MICRO_USD=%q sem AOS_BUDGET_MAX_TOKENS", ErrBadBudgetCost, rawCost)
		}
		return nil, nil // não configurado ⇒ sem orçamento (stub neutro, declarado no banner)
	}
	maxTokens, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || maxTokens <= 0 {
		return nil, fmt.Errorf("%w: AOS_BUDGET_MAX_TOKENS=%q", ErrBadBudget, raw)
	}
	var opts []integration.RunBudgetOption
	if rawCost != "" {
		maxCost, cerr := strconv.ParseInt(rawCost, 10, 64)
		if cerr != nil || maxCost <= 0 {
			return nil, fmt.Errorf("%w: AOS_BUDGET_MAX_COST_MICRO_USD=%q", ErrBadBudgetCost, rawCost)
		}
		opts = append(opts, integration.WithMaxCostMicroUSDPerRun(maxCost))
	}
	rb, err := integration.NewRunBudget(maxTokens, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadBudget, err)
	}
	return rb, nil
}

// ErrBudgetCostNoPriceSource — `AOS_BUDGET_MAX_COST_MICRO_USD` está definida mas o nó não tem
// FONTE DE PREÇO que derive um custo real para os turnos deste modelo. Fail-closed NO ARRANQUE,
// no molde exacto de [ErrProgressBudgetUnwired] e [ErrBreakerVelocitySourceUnwired].
//
// PORQUE É FATAL E NÃO UM AVISO: um tecto em dólares só nega se houver dólares para comparar. Sem
// fonte de preço, `port.Usage.CostMicroUSD` é ZERO em todas as chamadas, o saldo debita zero,
// [integration.projectCost] projecta zero para sempre e a dimensão $ NUNCA nega. O nó arrancaria,
// o banner do orçamento diria ao operador que a dimensão de dólares «so NEGA se
// AOS_BUDGET_MAX_COST_MICRO_USD estiver definida» — e ela está —, e o operador leria uma
// protecção de N micro-USD por run que não existe em lado nenhum. É a CAPACIDADE-FANTASMA que
// toda esta camada de banners e gates existe para tornar impossível, com o agravante de o zero
// ser silencioso: nada falha, nada avisa, e a primeira vez que se descobre é na factura.
//
// O caso do MODELO DE REFERÊNCIA é pior do que o zero e é por isso que também aborta: aí o custo
// por turno não é zero, é a constante fabricada de [referenceModelCostMicroUSD] que o próprio
// código declara não ser uma medição. AOS-260 deu-lhe autoridade de ENFORCEMENT — passaria a
// debitar a árvore e a NEGAR turnos —, e a tarifa observada que dela sai (~75 micro-USD/token,
// ordens de grandeza acima de qualquer modelo real) mataria o run ao segundo turno com «orçamento
// esgotado» derivado de um número inventado.
var ErrBudgetCostNoPriceSource = errors.New("aos: AOS_BUDGET_MAX_COST_MICRO_USD esta definida mas o no NAO tem fonte de preco para derivar o custo dos turnos — o custo medido seria ZERO (ou uma constante fabricada) em todas as chamadas e a dimensao de dolares NUNCA negaria: o tecto seria uma protecao INEXISTENTE anunciada na config. Monte a sua tabela de precos em AOS_MODEL_PRICING_PATH (formato de pricing_table.json) cobrindo o par (AOS_MODEL_NAME, AOS_MODEL_REGION) deste no, ou REMOVA AOS_BUDGET_MAX_COST_MICRO_USD para medir dolares sem tecto em dolares")

// requireCostSourceForBudgetCap cruza as DUAS posturas que, isoladas, mentem uma sobre a outra: o
// tecto em $ do orçamento (AOS-260) e a cobertura de preço do canal de custo (AOS-259).
//
// O cruzamento é feito AQUI, no arranque, e não no caminho quente, pela mesma razão dos outros
// gates fail-closed do nó: uma configuração impossível tem de impedir o arranque, não produzir um
// comportamento silenciosamente degradado que o operador só descobre pelo efeito.
//
// `model` é o [agentruntime.ModelClient] REALMENTE injectado ([Config.Model]) — nil ⇒ o nó vai
// correr com o [referenceModel]. É o estado composto, nunca a intenção da config: é a mesma
// disciplina de [modelPostureBanner].
//
// Um modelo INJECTADO que não seja o gateway cai no ramo da tabela de preços porque o nó não tem
// como saber se ele deriva custo (o banner já o declara: «a postura é a de quem o injectou»).
// Fail-closed é a leitura certa dessa ignorância — um tecto que talvez negue não é um tecto.
func requireCostSourceForBudgetCap(rb *integration.RunBudget, model agentruntime.ModelClient) error {
	if rb == nil {
		return nil
	}
	tecto, capped := rb.MaxCostMicroUSDPerRun()
	if !capped {
		return nil // dólares medidos, sem tecto ⇒ nada a garantir
	}
	if model == nil {
		return fmt.Errorf("%w. NESTE NO: nao ha Model Gateway composto (AOS_MODEL_ENDPOINT por definir), pelo que os turnos sao servidos pelo MODELO DE REFERENCIA, cujo custo e a constante fabricada de %d micro-USD por turno — nao uma medicao. Com o tecto de %d micro-USD a admissao passaria a negar runs com base nessa constante (tarifa observada ~%d micro-USD/token, ordens de grandeza acima de qualquer modelo real)",
			ErrBudgetCostNoPriceSource, referenceModelCostMicroUSD, tecto,
			referenceModelCostMicroUSD/(referenceModelInputTokens+referenceModelOutputTokens))
	}
	p := modelPricingPostureFromEnv()
	if !p.Armed {
		return fmt.Errorf("%w. NESTE NO: a tabela de precos em vigor (%s) NAO cobre o par (modelo=%q, regiao=%q), pelo que o [cost.Recorder] do gateway nao foi composto e cost_micro_usd viaja a ZERO em toda a travessia — o tecto de %d micro-USD nunca mordia",
			ErrBudgetCostNoPriceSource, p.TableVersion, p.Model, p.Region, tecto)
	}
	return nil
}

// consumoDuravelParaOrcamento adapta o ledger de turnos ao contrato que o tecto por-run espera.
//
// A ADAPTAÇÃO QUE IMPORTA é a do erro: [ErrBurndownNoLedger] significa «este run ainda não tem
// turnos», que é o caso NORMAL da primeira incarnação — e tem de virar consumo ZERO com erro nil,
// nunca uma degradação. Confundir os dois faria cada run novo arrancar com um aviso de ledger
// ilegível, e o aviso que interessa afogava-se nesses.
//
// Qualquer OUTRO erro passa como erro: aí o tecto degrada para inteiro, e essa degradação é
// declarada em voz alta pelo [integration.RunBudget].
func consumoDuravelParaOrcamento(s *turnLedgerBurndown) integration.ConsumoDuravel {
	if s == nil {
		return nil
	}
	return func(ctx context.Context, runID string) (budget.Amount, error) {
		c, err := s.ConsumedByRun(ctx, runID)
		if errors.Is(err, ErrBurndownNoLedger) {
			return budget.Amount{}, nil // run ainda sem turnos: consumo zero, e nao e falha
		}
		if err != nil {
			return budget.Amount{}, err
		}
		return c.Consumed, nil
	}
}
