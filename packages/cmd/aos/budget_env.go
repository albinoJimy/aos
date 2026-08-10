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
// NÃO HÁ EQUIVALENTE EM DÓLARES, e a ausência é uma decisão, não um esquecimento: o canal
// de custo em micro-USD não está ligado ponta a ponta (o `translateResponse` do model
// gateway não preenche `CostMicroUSD`), pelo que um `AOS_BUDGET_MAX_COST_MICRO_USD` seria
// comparado com um consumo contado a ZERO e não negaria nada — uma capacidade-fantasma
// com nome de tecto. A primeira env em `$` é a jusante de AOS-259.
//
// FAIL-CLOSED NA CONFIGURAÇÃO (molde de [ErrBadBreakerThresholds]/[ErrBadRetention]): um
// valor ilegível, negativo ou ZERO ABORTA o arranque. O zero merece a nota explícita: um
// tecto de 0 tokens não desliga o orçamento — nenhuma estimativa cabe em zero, logo NEGARIA
// 100% das tool calls, que é precisamente o modo de falha que AOS-256 existe para evitar.
// Quem quer o nó sem orçamento deixa a variável por definir.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/aos-ref/integration"
)

// ErrBadBudget — o tecto de orçamento por-run está definido mas é inválido. O nó recusa
// arrancar em vez de degradar em silêncio para "sem orçamento" (o operador ficaria
// convencido de que há tecto quando não há) ou para "tudo negado".
var ErrBadBudget = errors.New("aos: orcamento por-run mal configurado — AOS_BUDGET_MAX_TOKENS tem de ser um inteiro > 0 (tokens por run). Deixe a variavel POR DEFINIR para correr sem orcamento; 0 nao desliga o orcamento, negaria todas as tool calls")

// budgetFromEnv resolve o orçamento por-run a partir do ambiente. Devolve (nil, nil) quando
// a variável não está definida — o estado de hoje, que o banner declara como NÃO COMPOSTO.
func budgetFromEnv() (*integration.RunBudget, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_BUDGET_MAX_TOKENS"))
	if raw == "" {
		return nil, nil // não configurado ⇒ sem orçamento (stub neutro, declarado no banner)
	}
	maxTokens, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || maxTokens <= 0 {
		return nil, fmt.Errorf("%w: AOS_BUDGET_MAX_TOKENS=%q", ErrBadBudget, raw)
	}
	rb, err := integration.NewRunBudget(maxTokens)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadBudget, err)
	}
	return rb, nil
}
