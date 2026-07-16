// Package budgetbridge é o ADAPTADOR FINO entre a medida de custo do Model Gateway
// (metering/cost, AOS-062) e o tipo de orçamento do control-plane
// ([budget.Amount], ADR-008) que o burn-down e o admission global consomem.
//
// # Porquê um pacote separado (decisão de layering)
//
// O núcleo metering/cost é ZERO-dep de control-plane: define o seu próprio
// [cost.Amount] (Tokens+CostMicroUSD, espelho estrutural de budget.Amount) e a
// aritmética checked, para ser testável isoladamente sem puxar o Escalonador/budget
// para o caminho de metering. Este adaptador é o ÚNICO ponto que importa
// control-plane/budget — à imagem do routing/tieradapter de AOS-059 (opção b): a
// fronteira platform → control-plane fica confinada aqui, e só aqui.
//
// A conversão é trivial e SEM PERDA: ambos os tipos são micro-USD int64 + tokens
// int64 (1 USD = 1_000_000 micro-USD), pelo que a medida de custo alimenta o
// burn-down orçamental (em tokens E $) sem qualquer drift de dinheiro.
package budgetbridge

import (
	"github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/platform/model-gateway/metering/cost"
)

// ToBudgetAmount converte a medida de custo do GW ([cost.Amount]) no tipo de
// orçamento do control-plane ([budget.Amount]) — SEM perda (ambos micro-USD int64 +
// tokens int64). É o ponto de entrada da medida de AOS-062 no burn-down/admission de
// EPIC-03.
func ToBudgetAmount(a cost.Amount) budget.Amount {
	return budget.Amount{Tokens: a.Tokens, CostMicroUSD: a.CostMicroUSD}
}

// FromBudgetAmount é a conversão inversa (introspecção/reconciliação): projecta um
// [budget.Amount] de volta para a medida do GW.
func FromBudgetAmount(a budget.Amount) cost.Amount {
	return cost.Amount{Tokens: a.Tokens, CostMicroUSD: a.CostMicroUSD}
}
