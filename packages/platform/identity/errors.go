package identity

// IdentityError é o tipo de erro sentinela do módulo de identidade. Carrega um
// código estável e legível-por-máquina, comparável com errors.Is por identidade.
// TODAS as rejeições resolvem-se pelo lado seguro (fail-closed): a ausência de
// uma NHI válida é sempre negação, nunca permissão silenciosa.
type IdentityError struct {
	Code string
	msg  string
}

func (e *IdentityError) Error() string { return e.Code + ": " + e.msg }

// Sentinelas de erro. Comparáveis com errors.Is, inclusive quando embrulhados
// com fmt.Errorf("%w: …", …).
var (
	// ErrTokenMalformed — o token não é um JWS compacto de três segmentos
	// base64url, ou o header/claims não descodificam.
	ErrTokenMalformed = &IdentityError{Code: "E_TOKEN_MALFORMED", msg: "token nao e um JWS compacto valido"}

	// ErrUnsupportedAlg — algoritmo do header diferente de EdDSA. Defende contra a
	// confusão alg/none (um token com alg=none ou alg simétrico é rejeitado antes
	// de qualquer verificação de assinatura).
	ErrUnsupportedAlg = &IdentityError{Code: "E_UNSUPPORTED_ALG", msg: "algoritmo nao suportado (so EdDSA); rejeita alg/none"}

	// ErrSignatureInvalid — a assinatura ed25519 não valida contra a chave pública
	// do emissor (token forjado, adulterado ou assinado por outra chave).
	ErrSignatureInvalid = &IdentityError{Code: "E_SIGNATURE_INVALID", msg: "assinatura ed25519 invalida ou token adulterado"}

	// ErrUnknownIssuer — o campo iss está vazio ou não corresponde a nenhum trust
	// anchor conhecido do verificador (emissor desconhecido).
	ErrUnknownIssuer = &IdentityError{Code: "E_UNKNOWN_ISSUER", msg: "emissor desconhecido (sem trust anchor)"}

	// ErrTokenExpired — o token expirou (now >= exp) ou não tem exp (fail-closed:
	// um token sem prazo não é aceite).
	ErrTokenExpired = &IdentityError{Code: "E_TOKEN_EXPIRED", msg: "token expirado ou sem exp"}

	// ErrTokenNotYetValid — o token ainda não é válido (now < nbf).
	ErrTokenNotYetValid = &IdentityError{Code: "E_TOKEN_NOT_YET_VALID", msg: "token ainda nao valido (nbf no futuro)"}

	// ErrTokenRevoked — o jti do token consta da lista de revogação, ou a consulta
	// de revogação falhou (fail-closed: revogação indisponível ⇒ negação).
	ErrTokenRevoked = &IdentityError{Code: "E_TOKEN_REVOKED", msg: "token revogado ou revogacao indisponivel"}

	// ErrOutOfScope — a capability pedida no Call não está no escopo do token
	// (fora de escopo). Imposta pelo hook IdentityCheck na fronteira do RM.
	ErrOutOfScope = &IdentityError{Code: "E_OUT_OF_SCOPE", msg: "capability fora do escopo da NHI"}

	// ErrNoCredential — a chamada mediada não apresentou token NHI. Proibição de
	// identidade anónima/round-robin (ADR-003): sem NHI não há autoridade.
	ErrNoCredential = &IdentityError{Code: "E_NO_CREDENTIAL", msg: "chamada sem NHI (identidade anonima proibida)"}

	// ErrUnknownClass — pedido de emissão para uma classe de agente não
	// configurada (fail-closed: não se emite sob uma classe sem política de TTL/
	// escopo).
	ErrUnknownClass = &IdentityError{Code: "E_UNKNOWN_CLASS", msg: "classe de agente nao configurada"}

	// ErrInvalidRequest — pedido de emissão/revogação com campos obrigatórios em
	// falta (ex.: user_id, agent_id ou jti vazios).
	ErrInvalidRequest = &IdentityError{Code: "E_INVALID_REQUEST", msg: "pedido invalido (campos obrigatorios em falta)"}

	// ErrDelegationInvalid — a cadeia de delegação embebida no token é inválida:
	// vazia, órfã (raiz não-humana), com escalada de autoridade ou com o
	// encadeamento de hash quebrado. Envolve o erro sentinela do subpacote
	// delegation (comparável com errors.Is em ambos os níveis). Fail-closed:
	// AOS-006 exige que toda a NHI resolva até um humano responsável.
	ErrDelegationInvalid = &IdentityError{Code: "E_DELEGATION_INVALID", msg: "cadeia de delegacao invalida (nao resolve ate humano ou escala autoridade)"}
)
