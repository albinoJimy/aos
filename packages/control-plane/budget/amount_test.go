package budget

import "testing"

func TestAmount_ArithmeticAndPredicates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		a, b      Amount
		wantAdd   Amount
		wantSub   Amount
		aValidRes bool
		aNonNeg   bool
		aFitsInB  bool
	}{
		{
			name:      "ambas positivas",
			a:         Amount{Tokens: 100, CostMicroUSD: 500},
			b:         Amount{Tokens: 40, CostMicroUSD: 200},
			wantAdd:   Amount{Tokens: 140, CostMicroUSD: 700},
			wantSub:   Amount{Tokens: 60, CostMicroUSD: 300},
			aValidRes: true,
			aNonNeg:   true,
			aFitsInB:  false, // 100>40
		},
		{
			name:      "cabe nas duas dimensoes",
			a:         Amount{Tokens: 10, CostMicroUSD: 10},
			b:         Amount{Tokens: 10, CostMicroUSD: 20},
			wantAdd:   Amount{Tokens: 20, CostMicroUSD: 30},
			wantSub:   Amount{Tokens: 0, CostMicroUSD: -10},
			aValidRes: true,
			aNonNeg:   true,
			aFitsInB:  true, // 10<=10 e 10<=20
		},
		{
			name:      "cabe em tokens mas nao em custo",
			a:         Amount{Tokens: 5, CostMicroUSD: 100},
			b:         Amount{Tokens: 10, CostMicroUSD: 50},
			wantAdd:   Amount{Tokens: 15, CostMicroUSD: 150},
			wantSub:   Amount{Tokens: -5, CostMicroUSD: 50},
			aValidRes: true,
			aNonNeg:   true,
			aFitsInB:  false, // custo 100>50
		},
		{
			name:      "zero nao e reserva valida",
			a:         Amount{},
			b:         Amount{Tokens: 1, CostMicroUSD: 1},
			wantAdd:   Amount{Tokens: 1, CostMicroUSD: 1},
			wantSub:   Amount{Tokens: -1, CostMicroUSD: -1},
			aValidRes: false,
			aNonNeg:   true,
			aFitsInB:  true,
		},
		{
			name:      "dimensao negativa nao e reserva valida",
			a:         Amount{Tokens: -1, CostMicroUSD: 10},
			b:         Amount{Tokens: 5, CostMicroUSD: 5},
			wantAdd:   Amount{Tokens: 4, CostMicroUSD: 15},
			wantSub:   Amount{Tokens: -6, CostMicroUSD: 5},
			aValidRes: false,
			aNonNeg:   false,
			aFitsInB:  false, // custo 10>5
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.a.Add(tc.b); got != tc.wantAdd {
				t.Errorf("Add = %v, quero %v", got, tc.wantAdd)
			}
			if got := tc.a.Sub(tc.b); got != tc.wantSub {
				t.Errorf("Sub = %v, quero %v", got, tc.wantSub)
			}
			if got := tc.a.validReserve(); got != tc.aValidRes {
				t.Errorf("validReserve = %v, quero %v", got, tc.aValidRes)
			}
			if got := tc.a.nonNegative(); got != tc.aNonNeg {
				t.Errorf("nonNegative = %v, quero %v", got, tc.aNonNeg)
			}
			if got := tc.a.fitsWithin(tc.b); got != tc.aFitsInB {
				t.Errorf("fitsWithin = %v, quero %v", got, tc.aFitsInB)
			}
		})
	}
}

func TestAmount_String(t *testing.T) {
	t.Parallel()
	got := Amount{Tokens: 1000, CostMicroUSD: 2_500_000}.String()
	want := "{tokens=1000 cost=$2.500000}"
	if got != want {
		t.Errorf("String = %q, quero %q", got, want)
	}
}
