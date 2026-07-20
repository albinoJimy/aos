package approvalcard

import "errors"

// Sentinelas fail-closed do módulo. Toda a construção/validação/autorização recusa
// por omissão: uma versão de schema malformada, um card incoerente, um canal ausente
// ou uma falha de redacção NUNCA produzem um card/decisão silenciosamente permissivo.
var (
	// ErrInvalidSchemaVersion — a versão de schema não é "X.Y.Z" com inteiros
	// não-negativos. Fail-closed em [ParseCardSchemaVersion].
	ErrInvalidSchemaVersion = errors.New("approvalcard: versao de schema invalida (esperado X.Y.Z)")

	// ErrIncompatibleSchema — o card carimba um MAJOR incompatível com [CurrentVersion].
	// Quebra de contrato, rejeitada (fail-closed) em [ApprovalCard.Validate].
	ErrIncompatibleSchema = errors.New("approvalcard: versao de schema MAJOR incompativel (rejeitado)")

	// ErrInvalidCard — o card está incoerente: RequestID/Requester vazios, rótulo de
	// classe divergente do código, ou dual-control marcado numa acção reversível.
	// Fail-closed.
	ErrInvalidCard = errors.New("approvalcard: card invalido (request-id/requester/coerencia)")

	// ErrNilChannel — o coletor de dual-control foi construído sem um
	// [risk.ConfirmationChannel]. Sem porta para DEVOLVER a decisão, não há forma de
	// assinar/impor — fail-closed na construção.
	ErrNilChannel = errors.New("approvalcard: canal de confirmacao ausente (fail-closed)")

	// ErrRedaction — a redacção do preview/args falhou (ex.: tokenização sem KeySource).
	// Um preview por-redigir NUNCA entra no card: fail-closed em [BuildCard].
	ErrRedaction = errors.New("approvalcard: falha ao redigir o preview (fail-closed)")
)
