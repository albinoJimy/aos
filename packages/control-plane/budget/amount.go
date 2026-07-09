package budget

import "fmt"

// Amount é a denominação do orçamento nas DUAS dimensões que o AOS controla:
// Tokens e custo em micro-dólares (CostMicroUSD). Ambos são INTEIROS de
// propósito — nunca float — para que a comparação e o débito sejam exactos e
// livres de corrida/arredondamento (ADR-008). 1 USD = 1_000_000 micro-USD.
//
// Uma reserva só cabe num nó se couber em AMBAS as dimensões; nenhuma dimensão
// domina a outra (um call barato em $ mas pesado em tokens é negado na dimensão
// tokens, e vice-versa).
type Amount struct {
	Tokens       int64 `json:"tokens"`
	CostMicroUSD int64 `json:"cost_micro_usd"`
}

// Add devolve a soma componente-a-componente.
func (a Amount) Add(b Amount) Amount {
	return Amount{Tokens: a.Tokens + b.Tokens, CostMicroUSD: a.CostMicroUSD + b.CostMicroUSD}
}

// Sub devolve a diferença componente-a-componente.
func (a Amount) Sub(b Amount) Amount {
	return Amount{Tokens: a.Tokens - b.Tokens, CostMicroUSD: a.CostMicroUSD - b.CostMicroUSD}
}

// addChecked soma componente-a-componente detectando overflow/underflow de int64
// em qualquer dimensão. Devolve ok=false se alguma dimensão transbordar. Usado na
// reconstrução ([Rebuild]) sobre entrada potencialmente adversarial/corrompida do
// Event Store; o caminho normal de Reserve não transborda (limitado pelo limit).
func (a Amount) addChecked(b Amount) (Amount, bool) {
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

// IsZero indica que ambas as dimensões são nulas.
func (a Amount) IsZero() bool { return a.Tokens == 0 && a.CostMicroUSD == 0 }

// nonNegative indica que nenhuma dimensão é negativa (limite válido).
func (a Amount) nonNegative() bool { return a.Tokens >= 0 && a.CostMicroUSD >= 0 }

// validReserve indica que a quantia é uma reserva legítima: nenhuma dimensão
// negativa e pelo menos uma positiva (reservar zero não faz sentido).
func (a Amount) validReserve() bool { return a.nonNegative() && !a.IsZero() }

// fitsWithin indica se a quantia a cabe na capacidade cap em AMBAS as dimensões.
func (a Amount) fitsWithin(capacity Amount) bool {
	return a.Tokens <= capacity.Tokens && a.CostMicroUSD <= capacity.CostMicroUSD
}

// String formata a quantia de forma legível (tokens e $).
func (a Amount) String() string {
	return fmt.Sprintf("{tokens=%d cost=$%.6f}", a.Tokens, float64(a.CostMicroUSD)/1e6)
}
