package pdp

// Error é um erro de porta do contrato C1 (tecnica/12 §4) com um Code estável e
// legível-por-máquina, para que o chamador ramifique sem fazer parse de strings
// livres. Todos os erros de porta resolvem-se pelo lado seguro (fail-closed): a
// [Decision] associada é sempre Deny — a ausência de permit explícito é negação.
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

// Sentinelas de erro de porta C1. Comparáveis com errors.Is quando embrulhados
// com fmt.Errorf("%w: …", …).
var (
	// ErrPolicyUnavailable — bundle não carregado/indisponível. Tratado como
	// deny (E_POLICY_UNAVAILABLE).
	ErrPolicyUnavailable = &Error{Code: "E_POLICY_UNAVAILABLE", Msg: "bundle de politica nao carregado"}
	// ErrSignatureInvalid — bundle não-assinado, assinatura inválida ou conteúdo
	// adulterado (content_hash não corresponde). Rejeição fail-closed
	// (E_SIGNATURE_INVALID).
	ErrSignatureInvalid = &Error{Code: "E_SIGNATURE_INVALID", Msg: "assinatura do bundle invalida ou bundle adulterado"}
	// ErrMalformedRequest — request de decisão sem os campos mínimos
	// (E_MALFORMED_REQUEST).
	ErrMalformedRequest = &Error{Code: "E_MALFORMED_REQUEST", Msg: "pedido de decisao malformado"}
	// ErrStalePolicyVersion — hot-reload rejeitado porque a policy_version do
	// bundle não é estritamente mais recente que a em vigor (não regride política).
	// É distinto de [ErrSignatureInvalid]: NÃO indica falha criptográfica nem
	// adulteração — o bundle está validamente assinado, apenas não é mais recente.
	// Um chamador que use errors.Is(err, ErrSignatureInvalid) para detectar
	// adulteração não deve ser induzido em erro por um reload benigno no-op.
	ErrStalePolicyVersion = &Error{Code: "E_STALE_VERSION", Msg: "policy_version do bundle nao e mais recente que a em vigor"}
)
