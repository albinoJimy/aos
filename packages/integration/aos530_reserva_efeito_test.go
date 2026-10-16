package integration

import (
	"context"
	"errors"
	"testing"

	budget "github.com/aos-ref/control-plane/budget"
)

// AOS-530 (peça 3b) — A RESERVA DE UM EFEITO PAGO DE UMA TOOL sobre o nó por-run (ADR-042 §2.6).

// novoOrcamentoComRun devolve um orçamento com o nó do run vivo.
func novoOrcamentoComRun(t *testing.T, runID string, tokens, custo int64, opts ...RunBudgetOption) *RunBudget {
	t.Helper()
	if custo > 0 {
		opts = append(opts, WithMaxCostMicroUSDPerRun(custo))
	}
	rb, err := NewRunBudget(tokens, opts...)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := rb.acquire(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(rel)
	return rb
}

// O MÁXIMO CABE ⇒ reserva-se; saldar troca-o pelo medido, e o que fica disponível é o tecto menos
// o medido (não menos o máximo).
func TestAOS530_ReservaSaldadaPeloMedido(t *testing.T) {
	ctx := context.Background()
	rb := novoOrcamentoComRun(t, "r", 100_000, 50_000)
	res, err := rb.ReservarEfeitoDeTool(ctx, "r", budget.Amount{Tokens: 12_288, CostMicroUSD: 22_938})
	if err != nil {
		t.Fatalf("a reserva cabia: %v", err)
	}
	if c, _ := rb.AvailableCostMicroUSD("r"); c != 50_000-22_938 {
		t.Fatalf("durante o efeito o maximo esta reservado: disponivel %d", c)
	}
	cobrado, err := res.Saldar(ctx, budget.Amount{Tokens: 930, CostMicroUSD: 840})
	if err != nil || cobrado != (budget.Amount{Tokens: 930, CostMicroUSD: 840}) {
		t.Fatalf("Saldar: %v %+v", err, cobrado)
	}
	if c, _ := rb.AvailableCostMicroUSD("r"); c != 50_000-840 {
		t.Fatalf("depois do saldo fica o tecto menos o medido: %d", c)
	}
	if tk, _ := rb.AvailableTokens("r"); tk != 100_000-930 {
		t.Fatalf("tokens disponiveis %d", tk)
	}
	// Resolve-se uma vez.
	if c2, err := res.Saldar(ctx, budget.Amount{Tokens: 1, CostMicroUSD: 1}); err != nil || c2 != (budget.Amount{}) {
		t.Fatalf("um segundo Saldar e no-op: %v %+v", err, c2)
	}
	if err := res.Libertar(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _ := rb.AvailableCostMicroUSD("r"); c != 50_000-840 {
		t.Fatalf("Libertar depois de Saldar nao devolve o cobrado: %d", c)
	}
}

// O MÁXIMO NÃO CABE no que resta ao tecto em dólares ⇒ ErrNoHeadroom, sem débito.
func TestAOS530_ReservaQueNaoCabeENegada(t *testing.T) {
	ctx := context.Background()
	rb := novoOrcamentoComRun(t, "r", 100_000, 10_000)
	if _, err := rb.ReservarEfeitoDeTool(ctx, "r", budget.Amount{Tokens: 12_288, CostMicroUSD: 22_938}); !errors.Is(err, budget.ErrNoHeadroom) {
		t.Fatalf("quer ErrNoHeadroom; veio %v", err)
	}
	if c, _ := rb.AvailableCostMicroUSD("r"); c != 10_000 {
		t.Fatalf("a negacao nao debita: %d", c)
	}
	if _, err := rb.ReservarEfeitoDeTool(ctx, "outro", budget.Amount{Tokens: 1}); !errors.Is(err, budget.ErrUnknownNode) {
		t.Fatalf("sem no do run: quer ErrUnknownNode; veio %v", err)
	}
	var nada *RunBudget
	if _, err := nada.ReservarEfeitoDeTool(ctx, "r", budget.Amount{Tokens: 1}); !errors.Is(err, ErrReservaDeEfeitoSemOrcamento) {
		t.Fatalf("orcamento nil: %v", err)
	}
}

// UM CUSTO MEDIDO ACIMA DO QUE RESTA cobra o headroom até ao topo e devolve o cobrado; o real não
// se perde (é do chamador gravá-lo). Libertar devolve tudo.
func TestAOS530_ReservaComRealAcimaDoHeadroom(t *testing.T) {
	ctx := context.Background()
	rb := novoOrcamentoComRun(t, "r", 100_000, 30_000)
	res, err := rb.ReservarEfeitoDeTool(ctx, "r", budget.Amount{Tokens: 100, CostMicroUSD: 20_000})
	if err != nil {
		t.Fatal(err)
	}
	cobrado, err := res.Saldar(ctx, budget.Amount{Tokens: 100, CostMicroUSD: 45_000})
	if err != nil || cobrado.CostMicroUSD != 30_000 {
		t.Fatalf("quer cobrar o headroom (30000); veio %+v %v", cobrado, err)
	}
	if c, _ := rb.AvailableCostMicroUSD("r"); c != 0 {
		t.Fatalf("a dimensao fica a zero: %d", c)
	}
	if _, err := rb.ReservarEfeitoDeTool(ctx, "r", budget.Amount{Tokens: 1, CostMicroUSD: 1}); !errors.Is(err, budget.ErrNoHeadroom) {
		t.Fatalf("a reserva seguinte com custo ja nao cabe: %v", err)
	}

	res2, err := rb.ReservarEfeitoDeTool(ctx, "r", budget.Amount{Tokens: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := res2.Libertar(ctx); err != nil {
		t.Fatal(err)
	}
	if tk, _ := rb.AvailableTokens("r"); tk != 100_000-100 {
		t.Fatalf("Libertar devolve a reserva inteira: %d", tk)
	}
}

// A RETOMA LÊ O CONSUMO DURÁVEL: a incarnação seguinte nasce com o tecto menos o que o run já
// gastou (os turnos e os efeitos pagos das tools, somados pela fonte durável), e a reserva da
// chamada seguinte vê-o.
func TestAOS530_ARetomaVeOConsumoDuravelDoEfeito(t *testing.T) {
	ctx := context.Background()
	consumido := budget.Amount{Tokens: 930, CostMicroUSD: 40_000}
	rb, err := NewRunBudget(100_000, WithMaxCostMicroUSDPerRun(50_000),
		WithConsumoDuravel(func(context.Context, string) (budget.Amount, error) { return consumido, nil }))
	if err != nil {
		t.Fatal(err)
	}
	rel, err := rb.acquire(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	if _, err := rb.ReservarEfeitoDeTool(ctx, "r", budget.Amount{Tokens: 12_288, CostMicroUSD: 22_938}); !errors.Is(err, budget.ErrNoHeadroom) {
		t.Fatalf("com 40000 de 50000 ja gastos o maximo de 22938 nao cabe; veio %v", err)
	}
}
