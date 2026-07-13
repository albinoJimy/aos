package projection

// ProjectionError é o tipo de erro sentinela da projecção, com um código estável
// comparável por errors.Is. Fail-closed: uma política malformada ou uma vista nil
// rejeitam a projecção, nunca produzem uma injecção silenciosa.
type ProjectionError struct {
	Code string
	msg  string
}

func (e *ProjectionError) Error() string { return e.Code + ": " + e.msg }

var (
	// ErrInvalidPolicyVersion — a versão da política não é um SemVer válido.
	ErrInvalidPolicyVersion = &ProjectionError{
		Code: "E_PROJ_INVALID_POLICY_VERSION",
		msg:  "versao da politica de projeccao invalida (SemVer MAJOR.MINOR.PATCH)",
	}

	// ErrInvalidTokenBudget — o orçamento de tokens não é positivo.
	ErrInvalidTokenBudget = &ProjectionError{
		Code: "E_PROJ_INVALID_TOKEN_BUDGET",
		msg:  "orcamento de tokens da projeccao deve ser positivo",
	}

	// ErrNilView — foi passada uma vista read-only nil à projecção.
	ErrNilView = &ProjectionError{
		Code: "E_PROJ_NIL_VIEW",
		msg:  "vista read-only do registo nil",
	}
)
