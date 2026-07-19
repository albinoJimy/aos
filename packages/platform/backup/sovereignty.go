package backup

import "strings"

// normalizeRegion devolve a forma canónica de uma região para comparação estável
// e case-insensitive (espaços aparados, caixa reduzida). É a MESMA disciplina do
// eventstore.normalizeRegion e do enforcement de soberania do plano de controlo
// (reimplementada aqui porque a do eventstore é package-private). Uma região vazia
// após aparar permanece vazia — e uma região vazia NUNCA autoriza (fail-closed).
func normalizeRegion(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// checkSovereignty faz valer a fronteira regional de soberania do backup (ADR-011),
// fail-closed. srcRegion é a região do Event Store (board); dstRegion é a região do
// destino imutável. Regras:
//
//   - destino sem região (ausente/desconhecida) ⇒ [ErrSovereigntyViolation]
//     (não se prova que respeita a fronteira ⇒ deny);
//   - board com fronteira (srcRegion != "") e destino noutra região ⇒
//     [ErrSovereigntyViolation] (cross-border — backups NUNCA cruzam a fronteira).
//
// Se o Store não tiver fronteira configurada (srcRegion == ""), exige-se apenas
// que o destino declare uma região (não-vazia): não há board a cruzar, mas um
// destino sem região é sempre recusado.
func checkSovereignty(srcRegion, dstRegion string) error {
	src := normalizeRegion(srcRegion)
	dst := normalizeRegion(dstRegion)
	if dst == "" {
		return ErrSovereigntyViolation
	}
	if src != "" && dst != src {
		return ErrSovereigntyViolation
	}
	return nil
}
