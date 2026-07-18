package autonomy

// Level é o nível de autonomia de um par (agente, domínio) na taxonomia L0–L5
// (ADR-014). É ORDENADO: [L0] é o mais SUPERVISIONADO (o humano executa tudo) e
// [L5] o mais AUTÓNOMO (oversight amostral post-hoc). O valor-zero é [L0] —
// FAIL-CLOSED: um par sem nível registado é tratado como o MAIS RESTRITIVO.
type Level uint8

const (
	// L0 — SUGESTÃO: o agente propõe, o humano executa tudo. Nenhuma tool call
	// corre autonomamente. É o valor-zero (fail-closed).
	L0 Level = iota
	// L1 — APROVAÇÃO POR ACÇÃO: cada tool call espera aprovação individual,
	// independentemente da classe de risco.
	L1
	// L2 — APROVAÇÃO POR LOTE: as acções agrupam-se numa confirmação de lote com
	// resumo (anti-fatigue); danger nunca é agrupado (confirma individualmente).
	L2
	// L3 — AUTONOMIA SUPERVISIONADA: safe corre, gray agrupa em lote, danger
	// confirma. É EXACTAMENTE o tiering SA-ROC base (AOS-074/095).
	L3
	// L4 — AUTONOMIA POR EXCEPÇÃO: corre por omissão e só escala em risco alto
	// (danger) — a excepção que exige confirmação humana.
	L4
	// L5 — AUTONOMIA PLENA POR DOMÍNIO: corre; a classe de maior impacto (danger)
	// fica sob oversight AMOSTRAL e POST-HOC (corre já, revê-se por amostragem).
	L5
)

// maxLevel é o nível válido mais alto (fronteira de validação fail-closed).
const maxLevel = L5

// Valid indica se o nível está no domínio L0–L5. Um valor fora do domínio é
// rejeitado por [LevelRegistry.SetLevel] e tratado como o mais restritivo por
// [Oversight] (fail-closed).
func (l Level) Valid() bool { return l <= maxLevel }

// String devolve a forma textual canónica do nível ("L0".."L5"), selada no audit
// e exposta nos spans. Um nível fora do domínio devolve "L?" (nunca confundível
// com um nível válido).
func (l Level) String() string {
	switch l {
	case L0:
		return "L0"
	case L1:
		return "L1"
	case L2:
		return "L2"
	case L3:
		return "L3"
	case L4:
		return "L4"
	case L5:
		return "L5"
	default:
		return "L?"
	}
}

// Description devolve a descrição legível da semântica de oversight do nível,
// para changelogs/relatórios. Um nível inválido devolve "".
func (l Level) Description() string {
	switch l {
	case L0:
		return "sugestao: humano executa tudo"
	case L1:
		return "aprovacao por accao"
	case L2:
		return "aprovacao por lote"
	case L3:
		return "autonomia supervisionada (safe corre, danger confirma)"
	case L4:
		return "autonomia por excepcao"
	case L5:
		return "autonomia plena por dominio (oversight amostral post-hoc)"
	default:
		return ""
	}
}
