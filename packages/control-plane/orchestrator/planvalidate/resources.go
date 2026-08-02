package planvalidate

import (
	"github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// resources.go é o FIO que compõe as regras 5–6 de AOS-232 sobre o validador puro
// de AOS-231. Mantém-se no pacote [planvalidate] (coesão: o validador é uma só
// unidade), mas as regras 5–6 têm um ponto de entrada PRÓPRIO ([ValidateResources])
// porque exigem inputs que as regras 1–4 não têm (o [Pricer] e a política de
// orçamento/risco) e porque produzem, além do veredicto, o RISCO RESOLVIDO por nó
// que o gate AOS-236 consome. O [Validate] de AOS-231 (regras 1–4) fica intacto.

// ResourcePolicy agrega a política TRUSTED das regras 5–6, passada como ARGUMENTO
// (nunca lookup vivo) para preservar o determinismo.
type ResourcePolicy struct {
	// Budget é a política da regra 5 (Pricer + tectos + tolerância).
	Budget BudgetPolicy
	// Risk é a política de classificação SA-ROC da regra 6 (nil ⇒ [risk.DefaultPolicy]).
	Risk *risk.Policy
}

// Resolution é o resultado ESTRUTURADO das regras 5–6 — o artefacto que o gate
// AOS-236 consome. Carrega o risco RESOLVIDO por nó (piso derivado, elevado só se o
// rótulo do LLM for superior) e o TOTAL re-preçado autoritativo. Só é significativo
// quando [Validate] das regras 5 devolveu [accepted]; numa rejeição, [ValidateResources]
// devolve uma Resolution vazia (fail-closed: nada resolvido a jusante de uma rejeição).
type Resolution struct {
	// Nodes indexa o risco resolvido por node_id. Determinístico (uma entrada por nó).
	Nodes map[string]NodeRisk
	// RepricedTotal é a SOMA dos custos re-preçados (regra 5) — o total autoritativo,
	// NÃO o `budget_total` ecoado do documento.
	RepricedTotal budget.Amount
}

// ValidateResources aplica as regras 5–6 de AOS-232, ASSUMINDO que as regras 1–4
// ([Validate]) já aceitaram o documento contra este snapshot (em particular que
// cada tool RESOLVE — regra 3). Devolve:
//
//   - o RISCO RESOLVIDO por nó (regra 6): derivado das tools pinadas, elevado só se
//     o rótulo do LLM for superior ao piso (downgrade IGNORADO);
//   - o veredicto de ORÇAMENTO (regra 5): divergência de custo, teto por-nó
//     (breaker) e total vs raiz remanescente. A regra 6 NÃO rejeita — o risco é
//     RESOLVIDO (o piso vence sempre), e o tratamento de um nó `danger` (approval-card)
//     é do gate AOS-236.
//
// Defesa-em-profundidade: uma tool que não resolva no snapshot (não deveria chegar
// aqui, regra 3) é tratada como uma capability por-classificar (eixos-zero
// fail-closed ⇒ contribui `danger`), nunca ignorada. Determinística e sem I/O
// (assumindo um [Pricer] puro).
func ValidateResources(doc plan.PlanDocument, snap Snapshot, pol ResourcePolicy) (Resolution, Verdict) {
	// Regra 5 — orçamento re-preçado. Numa rejeição devolvemos [Resolution] vazia
	// (fail-closed: nada se resolve a jusante de uma rejeição de orçamento).
	v, total := checkBudget(doc, pol.Budget)
	if v.Rejected() {
		return Resolution{}, v
	}

	// Regra 6 — risco derivado por nó (não rejeita; RESOLVE). O piso vence sempre; o
	// rótulo do LLM só eleva.
	idx := snap.index()
	nodes := make(map[string]NodeRisk, len(doc.Nodes))
	for _, n := range doc.Nodes {
		caps := resolveCaps(n, idx)
		nodes[n.NodeID] = resolveNodeRisk(n, caps, pol.Risk)
	}
	return Resolution{Nodes: nodes, RepricedTotal: total}, accepted
}

// resolveCaps devolve as capabilities PINADAS que as tools de um nó resolvem no
// snapshot, pela ordem do slice. Uma tool que não resolva contribui uma capability
// de eixos-zero (fail-closed ⇒ danger): defesa-em-profundidade caso a regra 3 tenha
// sido saltada. Puro.
func resolveCaps(n plan.Node, idx map[toolKey]Capability) []Capability {
	caps := make([]Capability, 0, len(n.Tools))
	for _, t := range n.Tools {
		if c, ok := idx[toolKey{name: t.Name, version: t.Version}]; ok {
			caps = append(caps, c)
			continue
		}
		// Não-resolvida: capability fail-closed (eixos-zero ⇒ sensível/externo/irreversível).
		caps = append(caps, Capability{Name: t.Name, Version: t.Version})
	}
	return caps
}

// ValidatePlan é o ponto de entrada COMPLETO: regras 1–4 (AOS-231, [Validate])
// seguidas das regras 5–6 (AOS-232, [ValidateResources]). Fail-closed em ordem: se
// as regras 1–4 rejeitarem, devolve esse veredicto e uma [Resolution] vazia — o
// risco/orçamento só se resolvem sobre um plano estruturalmente aceite (em
// particular com as tools já resolvidas, pré-requisito da regra 6).
func ValidatePlan(doc plan.PlanDocument, snap Snapshot, ceil Ceilings, pol ResourcePolicy) (Resolution, Verdict) {
	if v := Validate(doc, snap, ceil); v.Rejected() {
		return Resolution{}, v
	}
	return ValidateResources(doc, snap, pol)
}
