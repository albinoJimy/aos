package budget

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
