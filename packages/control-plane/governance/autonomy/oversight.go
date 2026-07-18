package autonomy

import "github.com/aos-ref/kernel/reference-monitor/risk"

// OversightMode é o GRAU DE GATE aplicado a uma tool call — o resultado da
// composição NÍVEL × CLASSE DE RISCO ([Oversight]). É o vocabulário alargado que
// SUPERSET-a os modos do tiering SA-ROC (run/batch/confirm de AOS-095) com os dois
// extremos da escada L0–L5: a sugestão (L0) e a amostragem post-hoc (L5).
//
// É ORDENADO do mais restritivo ao menos restritivo. O valor-zero é
// [OversightSuggest] — FAIL-CLOSED: uma composição indeterminada exige que o
// humano execute tudo, nunca que a acção corra livre.
type OversightMode uint8

const (
	// OversightSuggest — o agente só PROPÕE; o humano executa. Nada corre
	// autonomamente. Valor-zero (fail-closed). É o modo de L0.
	OversightSuggest OversightMode = iota
	// OversightConfirm — CONFIRMAÇÃO INDIVIDUAL com preview antes de correr (o modo
	// de danger e de L1). Bloqueia à espera de um humano.
	OversightConfirm
	// OversightBatch — CONFIRMAÇÃO DE LOTE agregada com resumo (anti-fatigue; o
	// modo de gray). Bloqueia à espera de uma aprovação de grupo.
	OversightBatch
	// OversightPostHocSample — a acção CORRE já e é AMOSTRADA para revisão
	// post-hoc (o modo de danger a L5). Oversight sem bloquear.
	OversightPostHocSample
	// OversightRun — corre SEM gate (o modo de safe). Menos restritivo.
	OversightRun
)

// String devolve a forma textual canónica do modo (selada no audit / spans /
// obligations). Um valor fora do domínio devolve "suggest" (fail-closed).
func (m OversightMode) String() string {
	switch m {
	case OversightConfirm:
		return "confirm"
	case OversightBatch:
		return "batch"
	case OversightPostHocSample:
		return "post_hoc_sample"
	case OversightRun:
		return "run"
	default:
		return "suggest"
	}
}

// RequiresHumanGate indica se o modo BLOQUEIA a acção à espera de um humano ANTES
// de correr (suggest/confirm/batch). É o predicado que o PDP usa para traduzir o
// modo num efeito escalate. run e post_hoc_sample correm sem bloquear.
func (m OversightMode) RequiresHumanGate() bool {
	switch m {
	case OversightSuggest, OversightConfirm, OversightBatch:
		return true
	default:
		return false
	}
}

// Runs indica se a acção CORRE (com ou sem oversight post-hoc) — o complemento de
// [OversightMode.RequiresHumanGate].
func (m OversightMode) Runs() bool { return !m.RequiresHumanGate() }

// Oversight é a função PURA que compõe o NÍVEL de autonomia com a CLASSE DE RISCO
// da tool call e devolve o grau de gate a aplicar (AC3). É determinista, O(1) e
// sem efeitos — o coração da taxonomia.
//
// INTEGRA o tiering SA-ROC (não o reimplementa): a linha L3 delega em
// [oversightFromTiering], que reproduz o mapa classe→modo de AOS-074/095 (safe
// corre, gray em lote, danger confirma). Os restantes níveis deslocam esse baseline
// para mais oversight (L0–L2) ou menos (L4–L5), preservando a MONOTONIA: para uma
// classe fixa, um nível mais alto nunca aplica MAIS gate.
//
// INVARIANTE ADR-013 (preservada estruturalmente): danger NUNCA é agrupado em lote
// nem corre silenciosamente sem qualquer oversight — a L2/L3/L4 confirma
// individualmente e a L5 corre mas sob amostragem post-hoc (nunca [OversightRun]
// puro). FAIL-CLOSED: um nível inválido resolve para [OversightSuggest].
func Oversight(level Level, class risk.Class) OversightMode {
	if !level.Valid() {
		return OversightSuggest
	}
	switch level {
	case L0:
		// Sugestão: o humano executa tudo, qualquer que seja a classe.
		return OversightSuggest
	case L1:
		// Aprovação por acção: cada tool call confirma, qualquer que seja a classe.
		return OversightConfirm
	case L2:
		// Aprovação por lote. danger nunca é agrupado (ADR-013) → confirma individual.
		if class == risk.ClassDanger {
			return OversightConfirm
		}
		return OversightBatch
	case L3:
		// Autonomia supervisionada = o tiering SA-ROC base (composição AOS-074/095).
		return oversightFromTiering(class)
	case L4:
		// Autonomia por excepção: só escala em risco alto (danger é a excepção).
		if class == risk.ClassDanger {
			return OversightConfirm
		}
		return OversightRun
	case L5:
		// Autonomia plena por domínio: corre; danger (maior impacto) sob oversight
		// AMOSTRAL post-hoc — nunca run puro (a invariante de ADR-013 mantém-se).
		if class == risk.ClassDanger {
			return OversightPostHocSample
		}
		return OversightRun
	default:
		return OversightSuggest
	}
}

// oversightFromTiering reproduz o mapa classe→modo do tiering SA-ROC base
// (ADR-013, AOS-074/095): safe→run, gray→batch, danger→confirm. É a LINHA L3 e o
// ponto de composição com o tiering — o mesmo mapa, expresso no vocabulário
// alargado de [OversightMode]. FAIL-CLOSED: a classe-zero ([risk.ClassDanger]) e
// qualquer classe não mapeada confirmam individualmente.
func oversightFromTiering(class risk.Class) OversightMode {
	switch class {
	case risk.ClassSafe:
		return OversightRun
	case risk.ClassGray:
		return OversightBatch
	default: // risk.ClassDanger (valor-zero) + qualquer classe desconhecida
		return OversightConfirm
	}
}
