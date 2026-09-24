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

	// ErrRevocationsNotDurable — [Revocations.Rebuild] foi chamado num registo construído
	// SEM Event Store. Devolve-se erro em vez de um conjunto vazio porque os dois são
	// indistinguíveis para o chamador e significam coisas opostas: «nada foi revogado» e
	// «não sei o que foi revogado». Quem compõe a revogação tem de saber qual dos dois tem
	// antes de o verifier começar a servir (AOS-288).
	ErrRevocationsNotDurable = &IdentityError{Code: "E_REVOCATIONS_NOT_DURABLE", msg: "registo de revogacao sem Event Store: nao ha de onde reconstruir, e a projeccao nao sobrevive a um restart"}

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

	// ErrRevocationUnavailable — não foi possível CONSULTAR o registo de revogação (AOS-433).
	//
	// É fail-closed na mesma: o token é recusado, e o hook do RM nega em qualquer erro. O que
	// esta sentinela dá é a DISTINÇÃO — «não consegui perguntar» não é «foi revogado».
	//
	// Antes, as duas resolviam em [ErrTokenRevoked] e a causa era achatada para texto com `%v`.
	// Numa avaria do registo, todas as verificações de todos os titulares eram recusadas com o
	// log a dizer «revogada»: o operador revogaria e reemitiria identidades num incidente que se
	// resolvia reiniciando um serviço.
	//
	// A causa subjacente viaja com `%w` e é recuperável por errors.As.
	ErrRevocationUnavailable = &IdentityError{Code: "E_REVOCATION_UNAVAILABLE", msg: "registo de revogacao indisponivel (fail-closed)"}

	// ErrTTLForaDeGama — a política de uma classe pede um TTL inutilizável ou acima do tecto
	// (AOS-427, decisão 4). Recusa-se na CONSTRUÇÃO do emissor, não na emissão: um emissor
	// configurado com uma política impossível não chega a existir.
	//
	// Cobre as duas pontas com o mesmo sentinela porque é a mesma pergunta — «esta validade é
	// utilizável?». Um TTL <= 0 nasce expirado; um acima de TTLMaximo dá a uma credencial
	// automática o raio de acção que o atrito da cunhagem manual limitava por acidente.
	ErrTTLForaDeGama = &IdentityError{Code: "E_TTL_FORA_DE_GAMA", msg: "TTL da classe fora da gama permitida"}

	// ErrInvalidRequest — pedido de emissão/revogação com campos obrigatórios em
	// falta (ex.: user_id, agent_id ou jti vazios).
	ErrInvalidRequest = &IdentityError{Code: "E_INVALID_REQUEST", msg: "pedido invalido (campos obrigatorios em falta)"}

	// ErrInvalidSigner — o [crypto.Signer] fornecido ao Issuer (via
	// [NewIssuerWithSigner]) é inválido: nil, com uma chave pública não-ed25519 (ou
	// de tamanho errado), ou que produz uma assinatura de tamanho diferente de
	// ed25519.SignatureSize. Fail-closed: sem um signer ed25519 válido não se emite
	// NHI — a não-forjabilidade exige que a fronteira de assinatura seja íntegra.
	ErrInvalidSigner = &IdentityError{Code: "E_INVALID_SIGNER", msg: "signer invalido (nil, pubkey nao-ed25519 ou assinatura de tamanho errado)"}

	// ErrDelegationInvalid — a cadeia de delegação embebida no token é inválida:
	// vazia, órfã (raiz não-humana), com escalada de autoridade ou com o
	// encadeamento de hash quebrado. Envolve o erro sentinela do subpacote
	// delegation (comparável com errors.Is em ambos os níveis). Fail-closed:
	// AOS-006 exige que toda a NHI resolva até um humano responsável.
	ErrDelegationInvalid = &IdentityError{Code: "E_DELEGATION_INVALID", msg: "cadeia de delegacao invalida (nao resolve ate humano ou escala autoridade)"}
)
