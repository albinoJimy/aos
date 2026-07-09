package budget

import "sync/atomic"

// Reservation é o comprovativo (opaco) de uma reserva de headroom debitada em
// toda a cadeia de ancestrais de um nó. É devolvido por [Budget.Reserve] e
// consumido por [Budget.Commit] ou [Budget.Release] — exactamente um dos dois,
// exactamente uma vez. Os campos são de leitura/auditoria; o estado autoritativo
// (pending/committed/released) vive server-side no [Budget], como num backend
// distribuído real.
type Reservation struct {
	// ID identifica univocamente a reserva dentro do Budget.
	ID string
	// TreeID é a raiz da árvore de execução a que a reserva pertence.
	TreeID string
	// NodeID é o nó (sub-árvore) onde a reserva foi pedida.
	NodeID string
	// Amount é a quantia reservada em cada nível da cadeia.
	Amount Amount
}

// resState é o estado do ciclo de vida de uma reserva (transições por CAS).
type resState int32

const (
	statePending   resState = iota // reservada, ainda não confirmada nem libertada
	stateCommitted                 // débito final aplicado
	stateReleased                  // revertida (rollback)
)

// reservationState é o registo autoritativo server-side de uma reserva: o
// comprovativo público, a cadeia de nós debitados (raiz→folha resolvida) e o
// estado do ciclo de vida gerido por compare-and-swap para idempotência e
// exclusão mútua entre commit/release concorrentes.
type reservationState struct {
	res   Reservation
	chain []*node      // nós debitados (do nó pedido até à raiz)
	state atomic.Int32 // resState
}
