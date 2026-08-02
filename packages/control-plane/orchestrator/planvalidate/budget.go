package planvalidate

import (
	"math"

	"github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// REGRA 5 — ORÇAMENTO RE-PREÇADO com TETO por nó (AOS-232, tecnica/18 §3.3).
//
// O custo de cada nó é RE-PREÇADO deterministicamente por um [Pricer] injectado
// (o eixo da tabela AOS-062) — NUNCA ecoado do `budget_estimate` que o LLM
// declara. As comparações usam a denominação [budget.Amount] de AOS-008 (inteiros
// nas duas dimensões — tokens e micro-USD — sem float, ver ADR-008); o
// `budget_total` do documento NÃO é consultado: o total autoritativo é a SOMA dos
// custos re-preçados.
//
// Três guardas fail-closed, por ordem determinística (nós pela ordem do slice,
// depois o total):
//
//  1. DIVERGÊNCIA por-nó: |re-preçado − declarado| > tolerância ⇒ REJEITA (política
//     escolhida: rejeitar, NUNCA clamp silencioso — um custo declarado a menos é um
//     sinal adversarial, não um arredondamento a corrigir por baixo da mesa).
//  2. TETO DURO por-nó (o «breaker» de AOS-029 à ADMISSÃO): um nó cujo custo
//     re-preçado excede o tecto por-nó dispara a rejeição fail-closed ANTES de
//     qualquer reserva. É o gate de admissão que impede um ramo insustentável de
//     sequer chegar ao [budget.Reserve] em run-time.
//  3. TOTAL vs RAIZ: a soma re-preçada tem de caber no orçamento raiz REMANESCENTE
//     (ambas as dimensões, semântica de headroom de AOS-008). Overflow na soma ⇒
//     REJEITA (entrada adversarial não produz um total errado).

// Pricer RE-PREÇA o custo de um nó deterministicamente a partir do seu trabalho
// declarado (tokens) e do modelo pinado — o eixo da tabela de custo AOS-062. É
// INJECTADO como função PURA para manter [planvalidate] sem uma dependência de
// metering viva e sem I/O (invariante do pacote): mesmo nó ⇒ mesmo custo.
//
// ESCOLHA DECLARADA: em vez de puxar packages/platform/model-gateway/metering/cost
// (um módulo novo), o custo é derivado por esta abstração local pura. O chamador de
// produção liga aqui um pricer suportado pela tabela real; os testes ligam um pricer
// determinístico. Nenhum require/replace foi acrescentado ao go.mod por causa disto.
type Pricer interface {
	// Price devolve o custo re-preçado do nó. Deve ser puro (sem relógio/rand/I/O) e
	// determinístico para preservar a determinismo de [ValidateResources].
	Price(node plan.Node, meta plan.PlannerMeta) budget.Amount
}

// BudgetPolicy é a política TRUSTED de orçamento da regra 5, passada como
// ARGUMENTO (nunca lookup vivo) para manter o validador puro.
type BudgetPolicy struct {
	// Pricer re-preça cada nó (obrigatório: um Pricer nil força fail-closed, ver
	// [ValidateResources]).
	Pricer Pricer
	// RootRemaining é o orçamento RAIZ remanescente. É um tecto DURO: uma dimensão a
	// zero significa «nada resta» (qualquer custo positivo nessa dimensão não cabe) —
	// NÃO «ilimitado». Fail-closed.
	RootRemaining budget.Amount
	// NodeCeiling é o TETO de custo duro POR NÓ (o breaker de AOS-029 à admissão).
	// Convenção de tecto: uma dimensão <= 0 está DESLIGADA (sem tecto nessa
	// dimensão), espelhando [Ceilings]. Um custo re-preçado que exceda um tecto
	// ligado dispara a rejeição.
	NodeCeiling budget.Amount
	// Tolerance é a divergência ABSOLUTA admitida por dimensão entre o custo
	// re-preçado e o declarado no nó. Zero ⇒ exige igualdade exacta. Negativa é
	// tratada como zero (fail-closed).
	Tolerance budget.Amount
}

// checkBudget — REGRA 5. Re-preça cada nó, aplica as três guardas e devolve o
// PRIMEIRO veredicto de rejeição (ou [accepted]) mais o TOTAL re-preçado
// autoritativo. Determinística: itera os nós pela ordem do slice. Um [BudgetPolicy.Pricer]
// nil é fail-closed (rejeita todo o plano — sem pricer não há como re-preçar, e
// ecoar o custo do LLM seria exactamente a falha que a regra 5 previne).
func checkBudget(doc plan.PlanDocument, pol BudgetPolicy) (Verdict, budget.Amount) {
	if pol.Pricer == nil {
		return reject(plannerevents.RuleBudget, ReasonNoPricer, Locator{}), budget.Amount{}
	}
	tol := nonNegAmount(pol.Tolerance)
	var total budget.Amount
	for _, n := range doc.Nodes {
		repriced := pol.Pricer.Price(n, doc.PlannerMeta)
		loc := Locator{NodeID: n.NodeID}

		// (1) Divergência re-preçado vs declarado, por dimensão. Rejeita (sem clamp).
		// O custo declarado do documento é uint64 (untrusted); satura em MaxInt64 na
		// conversão para a denominação int64 de [budget.Amount] — um valor >= 2^63
		// NÃO transborda para negativo (o que estreitaria falsamente a divergência),
		// fica no tecto e diverge fail-closed de qualquer custo re-preçado realista.
		declared := budget.Amount{
			Tokens:       clampU64ToInt64(n.BudgetEstimate.Tokens),
			CostMicroUSD: clampU64ToInt64(n.BudgetEstimate.CostMicroUSD),
		}
		if diverges(repriced, declared, tol) {
			return reject(plannerevents.RuleBudget, ReasonBranchCostDivergence, loc), budget.Amount{}
		}

		// (2) Teto duro por-nó (breaker à admissão).
		if exceedsCeiling(repriced, pol.NodeCeiling) {
			return reject(plannerevents.RuleBudget, ReasonNodeCeilingExceeded, loc), budget.Amount{}
		}

		// (3) Acumula o total autoritativo com soma verificada (fail-closed em overflow).
		sum, ok := checkedAdd(total, repriced)
		if !ok {
			return reject(plannerevents.RuleBudget, ReasonBudgetOverflow, loc), budget.Amount{}
		}
		total = sum
	}

	// TOTAL vs RAIZ remanescente (ambas as dimensões).
	if !fitsWithin(total, pol.RootRemaining) {
		return reject(plannerevents.RuleBudget, ReasonBudgetTotalExceeded, Locator{}), budget.Amount{}
	}
	return accepted, total
}

// diverges indica se a e b diferem, em alguma dimensão, por MAIS do que tol
// (absoluto). Puro.
func diverges(a, b, tol budget.Amount) bool {
	return absDiff(a.Tokens, b.Tokens) > tol.Tokens ||
		absDiff(a.CostMicroUSD, b.CostMicroUSD) > tol.CostMicroUSD
}

// absDiff devolve |x − y| como int64 sem transbordar para valores realistas de
// orçamento. Puro.
func absDiff(x, y int64) int64 {
	if x >= y {
		return x - y
	}
	return y - x
}

// exceedsCeiling indica se cost excede o tecto por-nó em alguma dimensão LIGADA
// (dimensão <= 0 está desligada, convenção de [Ceilings]). Puro.
func exceedsCeiling(cost, ceil budget.Amount) bool {
	if ceil.Tokens > 0 && cost.Tokens > ceil.Tokens {
		return true
	}
	if ceil.CostMicroUSD > 0 && cost.CostMicroUSD > ceil.CostMicroUSD {
		return true
	}
	return false
}

// fitsWithin indica se cost cabe em capacity em AMBAS as dimensões (semântica de
// headroom de AOS-008). Ao contrário de [exceedsCeiling], NÃO há dimensão
// «desligada»: capacity é um tecto duro (zero ⇒ nada cabe nessa dimensão). Puro.
func fitsWithin(cost, capacity budget.Amount) bool {
	return cost.Tokens <= capacity.Tokens && cost.CostMicroUSD <= capacity.CostMicroUSD
}

// checkedAdd soma dois [budget.Amount] detectando overflow de int64 em qualquer
// dimensão. Assume dimensões não-negativas (custos re-preçados). Fail-closed: em
// overflow devolve ok=false para a regra 5 rejeitar em vez de produzir um total
// errado a partir de uma entrada adversarial. Puro.
func checkedAdd(a, b budget.Amount) (budget.Amount, bool) {
	t := a.Tokens + b.Tokens
	if t < a.Tokens {
		return budget.Amount{}, false
	}
	c := a.CostMicroUSD + b.CostMicroUSD
	if c < a.CostMicroUSD {
		return budget.Amount{}, false
	}
	return budget.Amount{Tokens: t, CostMicroUSD: c}, true
}

// clampU64ToInt64 converte um custo declarado uint64 (untrusted) para int64
// SATURANDO em [math.MaxInt64] em vez de transbordar para negativo. Um custo
// declarado >= 2^63 (micro-USD ou tokens) é absurdo e adversarial; saturá-lo
// mantém a guarda de divergência fail-closed (fica enormemente acima de qualquer
// re-preçado realista ⇒ diverge ⇒ rejeita), em vez de embrulhar para um negativo
// que poderia estreitar a divergência aparente. Puro.
func clampU64ToInt64(u uint64) int64 {
	if u > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(u)
}

// nonNegAmount satura as dimensões negativas a zero (uma tolerância negativa é
// tratada como «igualdade exacta»). Puro.
func nonNegAmount(a budget.Amount) budget.Amount {
	if a.Tokens < 0 {
		a.Tokens = 0
	}
	if a.CostMicroUSD < 0 {
		a.CostMicroUSD = 0
	}
	return a
}
