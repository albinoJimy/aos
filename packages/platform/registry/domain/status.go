package domain

// Status é o estado do ciclo de vida de admissão de um artefacto (tecnica/05 §3).
// A publicação entra SEMPRE em staging; nenhum artefacto salta directamente para
// active sem passar pela transição verificada staging→active. A máquina de estados
// é FAIL-CLOSED: qualquer transição não enumerada em transitions é recusada.
type Status string

const (
	// StatusStaging — admitido, ainda não verificado/promovido. Estado inicial
	// OBRIGATÓRIO de toda a publicação.
	StatusStaging Status = "staging"
	// StatusActive — promovido e resolvível para uso. Só se alcança via uma
	// transição verificada a partir de staging (ou reactivação de deprecated).
	StatusActive Status = "active"
	// StatusDeprecated — desencorajado mas ainda referenciável (deprecação formal
	// antes de qualquer retirada — AOS-052).
	StatusDeprecated Status = "deprecated"
	// StatusRevoked — revogado; bloqueio imediato. Estado TERMINAL.
	StatusRevoked Status = "revoked"
)

// Valid indica se s é um estado canónico (fail-closed).
func (s Status) Valid() bool {
	switch s {
	case StatusStaging, StatusActive, StatusDeprecated, StatusRevoked:
		return true
	default:
		return false
	}
}

// transitions é o grafo de transições PERMITIDAS do ciclo de vida. Tudo o que não
// esteja aqui é negado (default-deny da máquina de estados). Note-se em particular
// que NÃO existe qualquer aresta que produza active sem partir de staging (via o
// gate verificado) ou de deprecated (reactivação de uma versão já verificada):
// nunca há um "salto" directo para active.
//
//	staging    → active (via gate de verificação), revoked
//	active     → deprecated, revoked
//	deprecated → active (reactivação), revoked
//	revoked    → ∅ (terminal)
var transitions = map[Status]map[Status]bool{
	StatusStaging: {
		StatusActive:  true,
		StatusRevoked: true,
	},
	StatusActive: {
		StatusDeprecated: true,
		StatusRevoked:    true,
	},
	StatusDeprecated: {
		StatusActive:  true,
		StatusRevoked: true,
	},
	StatusRevoked: {},
}

// CanTransition indica se a transição de from para to é permitida pela máquina de
// estados. Fail-closed: estados inválidos ou uma transição não enumerada devolvem
// false. Uma transição para o mesmo estado (from == to) é um no-op e NÃO é
// permitida (o chamador deve tratar a idempotência explicitamente).
func CanTransition(from, to Status) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	return transitions[from][to]
}

// RequiresAdmissionGate indica se a transição atravessa o gate de verificação de
// admissão (staging → active). É o PONTO DE EXTENSÃO onde AOS-047 (hash), AOS-048
// (assinatura) e AOS-053 (eval-gate) impõem a verificação antes de promover. As
// restantes transições não passam por esse gate.
//
// DECISÃO EXPLÍCITA (AOS-045): a reactivação deprecated → active NÃO passa pelo gate.
// É defensável na fundação porque o conteúdo por (id, version) é IMUTÁVEL (append-only)
// e essa versão já atravessou o gate uma vez ao alcançar active — reactivar não muda
// o artefacto verificado. AVISO DE RASTREABILIDADE: quando AOS-053 (eval-gate) e
// AOS-049 (detecção de TOFU changed) aterrarem, uma versão deprecada por política ou
// incidente poderia voltar a active sem re-avaliação; nesse momento este predicado
// deve ser estendido (ou um gate de reactivação dedicado adicionado) para cobrir
// também deprecated → active, garantindo a re-verificação na re-promoção.
func RequiresAdmissionGate(from, to Status) bool {
	return from == StatusStaging && to == StatusActive
}
