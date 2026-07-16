package authz

import "errors"

// ErrScopeEscalation é devolvido quando um elo da cadeia de delegação tenta
// ALARGAR a autoridade — reclamar capabilities que o delegante (o principal
// acima) não possui. É a assinatura estrutural de uma escalada de privilégios /
// confused deputy na cadeia on-behalf-of: a cadeia só pode restringir. A negação
// é atribuível (registada no audit com DeniedBy="scope").
var ErrScopeEscalation = errors.New("authz: escalada de escopo na cadeia de delegacao (a autoridade so restringe, nunca amplia)")

// ErrOrphanChain é devolvido quando a cadeia de delegação não tem uma raiz
// humana atribuível. Sem um humano responsável na raiz (ADR-003) não há
// principal a quem escopar a autoridade — fail-closed.
var ErrOrphanChain = errors.New("authz: cadeia de delegacao sem raiz humana atribuivel")

// FoldScope computa o escopo efectivo como a DOBRA de intersecções dos conjuntos
// de autoridade, da RAIZ (utilizador) à FOLHA (agente actual): sets[0] ∩ sets[1]
// ∩ … ∩ sets[n]. É a formalização de "utilizador ∩ classe ∩ cadeia". Assinatura
// variádica de conveniência para call sites e testes; delega em [FoldSets].
//
// PROVA ESTRUTURAL da restrição monotónica: cada passo é [Intersect], cujo
// resultado é ⊆ de ambos os operandos; logo o acumulador só pode encolher ou
// manter-se ao longo da dobra — NUNCA cresce. Não existe operação de união nesta
// API. O resultado é canónico (ordenado, sem duplicados). Uma dobra sobre zero
// conjuntos, ou com qualquer conjunto vazio, colapsa para ∅ (fail-closed).
func FoldScope(sets ...[]string) []string {
	return FoldSets(sets)
}

// FoldSets computa a dobra de intersecções sobre uma lista de conjuntos de
// autoridade (raiz → folha). Ver [FoldScope] para a semântica e a prova.
func FoldSets(sets [][]string) []string {
	if len(sets) == 0 {
		return nil
	}
	acc := Normalize(sets[0])
	for i := 1; i < len(sets); i++ {
		if len(acc) == 0 {
			return nil
		}
		acc = Intersect(acc, sets[i])
	}
	return acc
}

// CheckNoEscalation valida que uma autoridade RECLAMADA por um principal não
// excede o escopo PERMITIDO (ground truth). Devolve [ErrScopeEscalation] se
// claimed ⊄ allowed — isto é, se o principal reclama alguma capability fora do
// que o utilizador ∩ classe lhe concede. Uma reivindicação vazia (o principal não
// reclama nada explicitamente) nunca escala.
func CheckNoEscalation(claimed, allowed []string) error {
	if Subset(claimed, allowed) {
		return nil
	}
	return ErrScopeEscalation
}

// RestrictAlong computa o escopo efectivo ao descer uma cadeia de autoridades
// DECLARADAS por elo (root → folha), IMPONDO a restrição monotónica: cada elo só
// pode declarar um escopo ⊆ do escopo corrente. Um elo que declare uma capability
// fora do escopo do delegante é uma tentativa de alargamento → [ErrScopeEscalation].
//
// Difere de [FoldSets]: FoldSets intersecta silenciosamente (a folha nunca vê
// mais do que a raiz permite, por construção); RestrictAlong DETECTA e REJEITA a
// tentativa explícita de alargar, para que a negação seja atribuível a um elo
// concreto. É o predicado do teste negativo de delegação (sub-agente não escala).
func RestrictAlong(root []string, declared ...[]string) ([]string, error) {
	acc := Normalize(root)
	for _, d := range declared {
		nd := Normalize(d)
		if !Subset(nd, acc) {
			return nil, ErrScopeEscalation
		}
		acc = Intersect(acc, nd)
	}
	return acc, nil
}
