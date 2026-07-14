package signing

// SigningError é o erro sentinela do pilar assinatura (AOS-048). Código estável,
// comparável com errors.Is. FAIL-CLOSED em toda a superfície: qualquer condição
// que não seja uma assinatura inequivocamente válida de uma chave confiável
// resolve-se por RECUSA.
type SigningError struct {
	Code string
	msg  string
}

func (e *SigningError) Error() string { return e.Code + ": " + e.msg }

var (
	// ErrSignatureMissing — o artefacto NÃO traz assinatura. Sem prova de origem,
	// nunca passa de staging a active (fail-closed).
	ErrSignatureMissing = &SigningError{Code: "E_SIG_MISSING", msg: "assinatura ausente (origem nao autenticada)"}

	// ErrSignatureInvalid — a assinatura está presente mas NÃO valida sobre o tuplo
	// (id, version, digest) com a chave confiável (adulterada, digest trocado, ou
	// codificação corrompida). É o núcleo do bloqueio anti rug-pull.
	ErrSignatureInvalid = &SigningError{Code: "E_SIG_INVALID", msg: "assinatura invalida sobre (id, version, digest)"}

	// ErrUntrustedKey — o publicador do artefacto NÃO tem chave no trust store, ou a
	// sua chave foi REVOGADA. Uma assinatura de chave não-confiável é recusada.
	ErrUntrustedKey = &SigningError{Code: "E_SIG_UNTRUSTED_KEY", msg: "chave do publicador ausente ou revogada no trust store"}

	// ErrKeyRevoked — tentou-se re-confiar (Add) um keyID cuja chave já foi REVOGADA.
	// A revogação é TERMINAL por keyID (AOS-048 Q4): uma chave comprometida nunca é
	// re-activada por um Add subsequente (isso tornaria a revogação reversível por
	// quem tenha escrita no store, revalidando assinaturas antigas). Re-confiar a
	// origem exige rotação para um keyID NOVO. A tentativa é auditada como recusa.
	ErrKeyRevoked = &SigningError{Code: "E_SIG_KEY_REVOKED", msg: "keyID revogado: revogacao e terminal (use um keyID novo / rotacao)"}

	// ErrInvalidKey — a chave (pública ou privada) fornecida tem tamanho/forma
	// inválida para Ed25519. Fail-closed na configuração.
	ErrInvalidKey = &SigningError{Code: "E_SIG_INVALID_KEY", msg: "chave ed25519 invalida"}

	// ErrEmptyKeyID — tentou-se registar/assinar com um identificador de chave vazio.
	// Um key id é obrigatório (é a ligação entre o publicador e a sua chave confiável).
	ErrEmptyKeyID = &SigningError{Code: "E_SIG_EMPTY_KEYID", msg: "identificador de chave vazio"}

	// ErrNoAuditStore — construiu-se um TrustStore/AdmissionVerifier sem audit store.
	// A auditabilidade é uma pré-condição (ADR-010): um trust store que não regista
	// as suas mudanças, ou um verificador cujas decisões não são seladas, não é
	// admissível (fail-closed).
	ErrNoAuditStore = &SigningError{Code: "E_SIG_NO_AUDIT", msg: "audit store obrigatorio (auditabilidade e pre-condicao)"}

	// ErrAuditFailed — o registo no audit WORM falhou. Uma decisão de verificação ou
	// uma mudança de trust store que não pode ser selada na cadeia é uma acção sem
	// rasto: fail-closed (ADR-002/010) — a operação é recusada.
	ErrAuditFailed = &SigningError{Code: "E_SIG_AUDIT_FAILED", msg: "registo no audit WORM falhou (accao nao-auditavel recusada)"}
)
