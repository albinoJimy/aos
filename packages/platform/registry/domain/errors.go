package domain

// DomainError é o erro sentinela do domínio do REG. Carrega um código estável,
// legível-por-máquina, comparável com errors.Is por identidade. Todas as
// invariantes resolvem-se pelo lado seguro (fail-closed): forma inválida ou
// transição ilegal são sempre REJEITADAS, nunca aceites silenciosamente.
type DomainError struct {
	Code string
	msg  string
}

func (e *DomainError) Error() string { return e.Code + ": " + e.msg }

// Sentinelas de erro do domínio. Comparáveis com errors.Is, inclusive embrulhados
// com fmt.Errorf("%w: …", …).
var (
	// ErrInvalidVersion — a string não é um SemVer MAJOR.MINOR.PATCH válido (inclui
	// referências flutuantes como "latest"/"main").
	ErrInvalidVersion = &DomainError{Code: "E_REG_INVALID_VERSION", msg: "versao nao e SemVer MAJOR.MINOR.PATCH valido"}

	// ErrEmptyID — a entrada não tem id.
	ErrEmptyID = &DomainError{Code: "E_REG_EMPTY_ID", msg: "id do artefacto vazio"}

	// ErrInvalidKind — o tipo de artefacto não é skill/tool/servidor MCP.
	ErrInvalidKind = &DomainError{Code: "E_REG_INVALID_KIND", msg: "tipo de artefacto invalido"}

	// ErrInvalidStatus — o estado do ciclo de vida não é canónico.
	ErrInvalidStatus = &DomainError{Code: "E_REG_INVALID_STATUS", msg: "estado de ciclo de vida invalido"}

	// ErrInvalidEgress — a classe de egress não é canónica.
	ErrInvalidEgress = &DomainError{Code: "E_REG_INVALID_EGRESS", msg: "classe de egress invalida"}

	// ErrInvalidTrust — o estado de confiança TOFU não é canónico.
	ErrInvalidTrust = &DomainError{Code: "E_REG_INVALID_TRUST", msg: "estado de confianca TOFU invalido"}

	// ErrMissingDigest — a entrada não tem digest (o campo é obrigatório mesmo sendo
	// placeholder em AOS-045).
	ErrMissingDigest = &DomainError{Code: "E_REG_MISSING_DIGEST", msg: "digest do artefacto ausente"}

	// ErrInvalidTransition — a transição de estado pedida não é permitida pela
	// máquina de estados fail-closed (inclui qualquer salto directo para active que
	// não parta de staging via o gate verificado).
	ErrInvalidTransition = &DomainError{Code: "E_REG_INVALID_TRANSITION", msg: "transicao de ciclo de vida nao permitida"}
)
