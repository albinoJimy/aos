package dr

import "strings"

// BoundaryResolver resolve o id de um board de soberania para a sua REGIÃO
// autorizada. É a peça INJECTADA que mantém platform/dr abaixo do control-plane:
// platform/dr NUNCA importa control-plane/governance/* (seria um up-import ilegal).
// O chamador — que PODE consultar o plano de governação — fornece este mapeamento;
// o orquestrador apenas o LÊ e IMPÕE a fronteira resultante através do próprio guard
// do Event Store (WithSovereigntyBoard recusa cross-border por construção) mais uma
// asserção de que a região do Store de DR == região resolvida (AC6, ADR-011).
type BoundaryResolver interface {
	// RegionForBoard devolve a região autorizada do board, ou um erro se o board é
	// desconhecido. Uma região vazia é tratada como desconhecida (fail-closed).
	RegionForBoard(board string) (string, error)
}

// ResolverFunc adapta uma função à porta [BoundaryResolver].
type ResolverFunc func(board string) (string, error)

// RegionForBoard implementa [BoundaryResolver].
func (f ResolverFunc) RegionForBoard(board string) (string, error) { return f(board) }

// MapResolver é um [BoundaryResolver] de referência apoiado num mapa board→região.
// É deliberadamente trivial: a fonte de verdade da atribuição board→região vive no
// plano de governação (fora deste módulo); o chamador materializa-a neste mapa. Um
// board ausente — ou mapeado para região vazia — devolve [ErrUnknownBoard]
// (fail-closed: fronteira desconhecida nunca autoriza).
type MapResolver map[string]string

// RegionForBoard implementa [BoundaryResolver].
func (m MapResolver) RegionForBoard(board string) (string, error) {
	region, ok := m[board]
	if !ok || strings.TrimSpace(region) == "" {
		return "", ErrUnknownBoard
	}
	return region, nil
}

// normalizeRegion normaliza um rótulo de região (trim + lower), a MESMA disciplina
// do eventstore/backup (ADR-011): comparações de fronteira são case/space-insensitive
// e uma região vazia nunca autoriza. Reimplementado localmente (a função dos módulos
// compostos é não-exportada) — é uma normalização de string trivial, não uma garantia
// de soberania (essa é imposta pelo guard do eventstore + a asserção do orquestrador).
func normalizeRegion(r string) string { return strings.ToLower(strings.TrimSpace(r)) }
