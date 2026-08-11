package budget

// Error é um erro do orçamento com um Code estável e legível-por-máquina, para
// que o chamador ramifique por igualdade sem fazer parse de strings livres.
// Todos resolvem-se pelo lado seguro (fail-closed): a ausência de headroom
// reservado é negação.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

// Sentinelas de erro do orçamento. Comparáveis com errors.Is por identidade.
var (
	// ErrNoHeadroom — a reserva não cabe (nalguma dimensão, nalgum nível da
	// árvore). É a negação de admissão (deny fail-closed); os níveis já debitados
	// na tentativa foram revertidos (rollback parcial, sem débito residual).
	ErrNoHeadroom = &Error{Code: "E_NO_HEADROOM", Msg: "sem headroom para a reserva (tokens ou custo)"}

	// ErrUnknownNode — o nó indicado não existe na árvore de orçamento.
	ErrUnknownNode = &Error{Code: "E_UNKNOWN_NODE", Msg: "no de orcamento inexistente"}

	// ErrUnknownParent — o parent indicado ao registar uma sub-árvore não existe.
	ErrUnknownParent = &Error{Code: "E_UNKNOWN_PARENT", Msg: "parent de orcamento inexistente"}

	// ErrNodeExists — já existe um nó com o id indicado.
	ErrNodeExists = &Error{Code: "E_NODE_EXISTS", Msg: "no de orcamento ja registado"}

	// ErrRootRemoval — tentativa de remover a RAIZ da árvore com [Budget.RemoveNode].
	// A raiz é o tecto de todos os nós: removê-la tornaria o topo inalcançável por id
	// (nenhum AddNode voltaria a poder pendurar nada nele) sem deixar de o debitar.
	ErrRootRemoval = &Error{Code: "E_ROOT_REMOVAL", Msg: "a raiz da arvore de orcamento nao se remove"}

	// ErrInvalidAmount — quantia de reserva inválida (dimensão negativa ou zero).
	ErrInvalidAmount = &Error{Code: "E_INVALID_AMOUNT", Msg: "quantia invalida (negativa ou zero)"}

	// ErrInvalidLimit — limite de nó inválido (dimensão negativa).
	ErrInvalidLimit = &Error{Code: "E_INVALID_LIMIT", Msg: "limite invalido (dimensao negativa)"}

	// ErrReservationNotFound — a reserva referida em Commit/Release não existe
	// (nunca criada ou já esquecida). Fail-closed.
	ErrReservationNotFound = &Error{Code: "E_RESERVATION_NOT_FOUND", Msg: "reserva inexistente"}

	// ErrCommitAfterRelease — tentativa de commit de uma reserva já libertada. Uma
	// reserva é commit OU release exactamente uma vez.
	ErrCommitAfterRelease = &Error{Code: "E_COMMIT_AFTER_RELEASE", Msg: "commit apos release da mesma reserva"}

	// ErrReleaseAfterCommit — tentativa de release de uma reserva já committed.
	ErrReleaseAfterCommit = &Error{Code: "E_RELEASE_AFTER_COMMIT", Msg: "release apos commit da mesma reserva"}

	// ErrCorruptEvent — um evento de orçamento lido do Event Store é inválido para
	// reconstrução: amount com dimensão negativa, ou soma que transborda int64
	// (entrada corrompida/adversarial). [Rebuild] rejeita fail-closed em vez de
	// produzir contadores errados ou negativos.
	ErrCorruptEvent = &Error{Code: "E_CORRUPT_EVENT", Msg: "evento de orcamento invalido ou corrompido"}
)
