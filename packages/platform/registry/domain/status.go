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
// que NÃO existe qualquer aresta que produza active sem partir de staging ou de
// deprecated, e AMBAS atravessam o gate de verificação (ver RequiresAdmissionGate):
// nunca há um "salto" directo para active nem uma re-promoção não-verificada.
//
//	staging    → active (via gate de verificação), revoked
//	active     → deprecated, revoked
//	deprecated → active (reactivação, via gate de verificação), revoked
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
// admissão (QUALQUER promoção a active). É o PONTO DE EXTENSÃO onde AOS-047 (hash),
// AOS-048 (assinatura) e AOS-053 (eval-gate) impõem a verificação antes de promover.
// As restantes transições (active→deprecated, *→revoked) não passam por esse gate.
//
// TODA a aresta que PRODUZ active é gated: staging→active (primeira promoção) E
// deprecated→active (reactivação). Embora o conteúdo por (id, version) seja IMUTÁVEL
// (append-only), a CONFIANÇA na sua origem NÃO é imutável: a chave do publicador pode
// ter sido REVOGADA entre a primeira promoção e a reactivação. Se a reactivação não
// re-verificasse assinatura+trust, uma versão previamente activa por uma chave depois
// comprometida voltaria a active SEM re-verificar a revogação — a revogação seria
// inefectiva nessa aresta (AOS-048 Q1). Fechar deprecated→active no gate garante a
// re-verificação criptográfica em cada re-promoção, fechando essa janela de revogação.
func RequiresAdmissionGate(from, to Status) bool {
	return to == StatusActive && (from == StatusStaging || from == StatusDeprecated)
}
