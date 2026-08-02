package planneraut

import "fmt"

// Level é o nível de autonomia L0–L5 do planeador (tecnica/18 §7.2, ADR-014).
// Definido LOCALMENTE neste pacote (à imagem de replan/ e intake/) — a taxonomia
// completa e o oversight vivem no seu pacote; aqui é o ordinal comparável que
// sustenta a promoção/demoção e o gatilho de auto-aprovação.
//
// O planeador NASCE a L0 (todo o plano exige aprovação humana). A promoção é por
// fiabilidade MEDIDA sobre um domínio recorrente; a demoção é imediata em anomalia.
type Level uint8

const (
	// L0 — aprovação humana de TODO o plano (nível de arranque; o piso).
	L0 Level = iota
	L1
	L2
	L3
	// L4 — auto-aprovação dentro do envelope; danger/capability_gap forçam revisão.
	L4
	L5
)

// maxLevel é o topo do intervalo fechado L0–L5.
const maxLevel = L5

// Valid indica se l está no intervalo fechado L0–L5. Fail-closed: um nível fora do
// intervalo é lixo e nunca é tratado como uma autonomia válida.
func (l Level) Valid() bool { return l <= maxLevel }

// String rende o nível como "L0".."L5" (ou "L?" se inválido).
func (l Level) String() string {
	if !l.Valid() {
		return "L?"
	}
	return fmt.Sprintf("L%d", uint8(l))
}
