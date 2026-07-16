package budgetbridge

import (
	"testing"

	"github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/platform/model-gateway/metering/cost"
)

// TestToBudgetAmountLossless prova que a medida de custo do GW (cost.Amount) entra no
// tipo de orçamento do control-plane (budget.Amount) SEM perda — ambos micro-USD int64
// + tokens int64. É a ponte que alimenta o burn-down/admission de EPIC-03 (ADR-008).
func TestToBudgetAmountLossless(t *testing.T) {
	a := cost.Amount{Tokens: 1500, CostMicroUSD: 10335}
	b := ToBudgetAmount(a)
	if b.Tokens != 1500 || b.CostMicroUSD != 10335 {
		t.Fatalf("conversao com perda: %+v", b)
	}
	// Round-trip.
	back := FromBudgetAmount(b)
	if back != a {
		t.Fatalf("round-trip com perda: %+v != %+v", back, a)
	}
}

// TestBridgeFeedsBudgetReserve prova que a medida de custo é compatível com a
// aritmética de orçamento do control-plane (o Amount.Add do budget aceita a medida).
func TestBridgeFeedsBudgetReserve(t *testing.T) {
	measured := ToBudgetAmount(cost.Amount{Tokens: 100, CostMicroUSD: 5000})
	other := budget.Amount{Tokens: 50, CostMicroUSD: 2500}
	sum := measured.Add(other)
	if sum.Tokens != 150 || sum.CostMicroUSD != 7500 {
		t.Fatalf("soma no dominio budget errada: %+v", sum)
	}
}
