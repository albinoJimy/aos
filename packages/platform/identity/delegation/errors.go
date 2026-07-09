package delegation

// DelegationError é o tipo de erro sentinela do pacote de delegação. Carrega um
// código estável, legível-por-máquina e comparável com errors.Is. Toda a
// rejeição é fail-closed: uma cadeia que não resolva até um humano responsável,
// ou cuja autoridade escale ao descer, é NEGADA — nunca aceite silenciosamente.
type DelegationError struct {
	Code string
	msg  string
}

func (e *DelegationError) Error() string { return e.Code + ": " + e.msg }

// Sentinelas de erro. Comparáveis com errors.Is, inclusive embrulhados com
// fmt.Errorf("%w: …", …).
var (
	// ErrEmptyChain — a cadeia não tem elos. Sem elos não há autoria: fail-closed.
	ErrEmptyChain = &DelegationError{Code: "E_CHAIN_EMPTY", msg: "cadeia de delegacao vazia"}

	// ErrOrphanChain — a raiz da cadeia não é um humano (Sub sem prefixo "human:").
	// Uma cadeia órfã não resolve até um humano responsável: é negada e auditada.
	ErrOrphanChain = &DelegationError{Code: "E_CHAIN_ORPHAN", msg: "cadeia orfa: raiz nao e um humano responsavel"}

	// ErrDepthNonMonotonic — a profundidade não começa em 0 ou não incrementa +1
	// por elo (cadeia adulterada ou mal construída).
	ErrDepthNonMonotonic = &DelegationError{Code: "E_CHAIN_DEPTH", msg: "profundidade nao monotona (esperava +1 por elo desde 0)"}

	// ErrHashMismatch — o PrevHash de um elo não corresponde ao hash do elo
	// anterior: a ordem da cadeia foi adulterada (reordenação/inserção/remoção).
	ErrHashMismatch = &DelegationError{Code: "E_CHAIN_HASH", msg: "encadeamento de hash quebrado (ordem adulterada)"}

	// ErrScopeEscalation — a autoridade de um elo NÃO é subconjunto da do elo
	// anterior: houve escalada de autoridade ao descer a cadeia (proibido).
	ErrScopeEscalation = &DelegationError{Code: "E_CHAIN_ESCALATION", msg: "escalada de autoridade: autoridade do filho nao e subconjunto da do pai"}

	// ErrInvalidLink — um elo tem campos obrigatórios em falta (Sub/ActAs vazios).
	ErrInvalidLink = &DelegationError{Code: "E_CHAIN_LINK", msg: "elo invalido (sub/act_as em falta)"}
)
