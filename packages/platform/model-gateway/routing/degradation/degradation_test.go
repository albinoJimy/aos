package degradation

import (
	"context"
	"testing"
)

func TestDefaultOrderMatchesChain(t *testing.T) {
	// A ordem declarativa do GW tem de espelhar a cadeia canónica do Escalonador
	// (shed → defer → downgrade → reject) — o GW não inventa uma cadeia paralela.
	want := []Action{ActionShed, ActionDefer, ActionDegrade, ActionReject}
	if len(DefaultOrder) != len(want) {
		t.Fatalf("DefaultOrder tem %d degraus, esperava %d", len(DefaultOrder), len(want))
	}
	for i, a := range want {
		if DefaultOrder[i] != a {
			t.Errorf("degrau %d: esperava %q, obtive %q", i, a, DefaultOrder[i])
		}
	}
	// Os valores têm de bater com scheduler.DegradationAction ("downgrade", não "degrade").
	if ActionDegrade != "downgrade" {
		t.Errorf("ActionDegrade deve ser \"downgrade\" (coerente com o Escalonador), obtive %q", ActionDegrade)
	}
}

func TestBudgetStateThresholds(t *testing.T) {
	tests := []struct {
		name        string
		used, limit int64
		pct         int
		wantAtPct   bool
		wantExhaust bool
	}{
		{"abaixo de 80", 79, 100, 80, false, false},
		{"exactamente 80", 80, 100, 80, true, false},
		{"acima de 80", 90, 100, 80, true, false},
		{"esgotado 100", 100, 100, 80, true, true},
		{"excedido", 120, 100, 80, true, true},
		{"ilimitado nunca atinge", 1000, 0, 80, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := BudgetState{Used: tc.used, Limit: tc.limit}
			if got := st.AtOrAbovePct(tc.pct); got != tc.wantAtPct {
				t.Errorf("AtOrAbovePct(%d) = %v, esperava %v", tc.pct, got, tc.wantAtPct)
			}
			if got := st.Exhausted(); got != tc.wantExhaust {
				t.Errorf("Exhausted() = %v, esperava %v", got, tc.wantExhaust)
			}
		})
	}
}

func TestOfferGracefulExhaustion(t *testing.T) {
	p := DefaultPolicy()
	// Abaixo de 80%: sem oferta de degradação.
	if off := p.OfferFor(BudgetState{Used: 50, Limit: 100}); off.Degrade {
		t.Error("abaixo do limiar: nao deve oferecer degradacao")
	}
	// A 80%: OFERECE degradar (exaustão graciosa), não hard-stop.
	off := p.OfferFor(BudgetState{Used: 80, Limit: 100})
	if !off.Degrade {
		t.Error("a 80%%: deve OFERECER degradar (exaustao graciosa), nunca hard-stop cego")
	}
	if off.Exhausted {
		t.Error("a 80%% ainda nao esta esgotado")
	}
	// A 100%: continua a OFERECER degradar (nunca hard-stop cego), com Exhausted.
	offExh := p.OfferFor(BudgetState{Used: 100, Limit: 100})
	if !offExh.Degrade || !offExh.Exhausted {
		t.Errorf("esgotado: deve oferecer degradar E marcar Exhausted; obtive Degrade=%v Exhausted=%v", offExh.Degrade, offExh.Exhausted)
	}
}

func TestCustomThreshold(t *testing.T) {
	p := NewPolicy(nil, 50) // limiar a 50%
	if off := p.OfferFor(BudgetState{Used: 49, Limit: 100}); off.Degrade {
		t.Error("49%% < limiar 50%%: sem degradacao")
	}
	if off := p.OfferFor(BudgetState{Used: 50, Limit: 100}); !off.Degrade {
		t.Error("50%% >= limiar 50%%: deve oferecer degradar")
	}
}

func TestPolicyDefaultsApplied(t *testing.T) {
	// Ordem vazia e limiar inválido caem nos defaults seguros.
	p := NewPolicy(nil, 0)
	if len(p.Order) != len(DefaultOrder) {
		t.Errorf("ordem vazia deve cair na DefaultOrder")
	}
	if p.DegradeThresholdPct != DefaultDegradeThresholdPct {
		t.Errorf("limiar invalido deve cair em %d, obtive %d", DefaultDegradeThresholdPct, p.DegradeThresholdPct)
	}
	p2 := NewPolicy(nil, 200) // fora de [1,100]
	if p2.DegradeThresholdPct != DefaultDegradeThresholdPct {
		t.Errorf("limiar 200 invalido deve cair no default")
	}
}

func TestStaticBudgetProvider(t *testing.T) {
	bp := NewStaticBudgetProvider(BudgetState{Used: 0, Limit: 1000})
	bp.Set(BudgetKey{Board: "b1"}, BudgetState{Used: 900, Limit: 1000})

	got, err := bp.Budget(context.Background(), BudgetKey{Board: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.AtOrAbovePct(80) {
		t.Error("b1 a 90%% deve estar acima de 80%%")
	}
	// Chave não configurada cai no default (0/1000 = 0%).
	def, _ := bp.Budget(context.Background(), BudgetKey{Board: "outro"})
	if def.AtOrAbovePct(80) {
		t.Error("board nao configurado cai no default (0%%)")
	}
}
