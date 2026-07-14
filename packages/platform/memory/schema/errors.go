package schema

// SchemaError é o erro sentinela do versionamento de schema de memória. Carrega um
// código estável, legível-por-máquina, comparável com errors.Is por identidade.
// Toda a validação resolve-se pelo lado seguro (fail-closed): uma versão inválida
// ou uma regressão de versão (SemVer não-monótona) é sempre REJEITADA, nunca
// aceite silenciosamente.
type SchemaError struct {
	Code string
	msg  string
}

func (e *SchemaError) Error() string { return e.Code + ": " + e.msg }

// Sentinelas de erro. Comparáveis com errors.Is, inclusive quando embrulhados com
// fmt.Errorf("%w: …", …).
var (
	// ErrInvalidVersion — a string não é um SemVer MAJOR.MINOR.PATCH válido.
	ErrInvalidVersion = &SchemaError{Code: "E_SCHEMA_INVALID_VERSION", msg: "versao de schema nao e SemVer MAJOR.MINOR.PATCH valido"}

	// ErrInvalidClass — a classe de memória não é uma das quatro canónicas.
	ErrInvalidClass = &SchemaError{Code: "E_SCHEMA_INVALID_CLASS", msg: "classe de memoria invalida"}

	// ErrNonMonotonic — uma tentativa de registar uma versão de schema que NÃO é
	// estritamente mais recente que a corrente (SemVer não-monótona). Fail-closed:
	// a versão corrente mantém-se; nunca se regride nem se re-adopta a mesma versão.
	ErrNonMonotonic = &SchemaError{Code: "E_SCHEMA_NON_MONOTONIC", msg: "versao de schema nao e estritamente mais recente que a corrente"}

	// ErrUnknownClassVersion — pediu-se a versão corrente de uma classe ainda sem
	// versão registada.
	ErrUnknownClassVersion = &SchemaError{Code: "E_SCHEMA_UNKNOWN_CLASS_VERSION", msg: "classe sem versao de schema registada"}

	// ErrRevertMismatch — uma reversão controlada da versão de schema de uma classe
	// (Revert) não pôde ser aplicada porque a versão corrente não coincide com a
	// esperada (compare-and-swap falhado) ou porque o alvo não é uma regressão.
	// Fail-closed: nunca se reverte sobre um estado que divergiu do esperado, nem se
	// usa Revert para AVANÇAR (para isso existe Register, monótono).
	ErrRevertMismatch = &SchemaError{Code: "E_SCHEMA_REVERT_MISMATCH", msg: "reversao de versao de schema rejeitada (corrente inesperada ou alvo nao e regressao)"}
)
