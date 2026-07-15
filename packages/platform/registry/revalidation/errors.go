package revalidation

import "errors"

// Erros sentinela da construção do revalidador (AOS-051). Todos FAIL-CLOSED: um
// revalidador que não possa verificar assinaturas nem selar decisões é RECUSADO na
// construção, nunca degradado silenciosamente.
var (
	// ErrNoTrustStore — não foi fornecido trust store. Sem chaves de confiança
	// nenhuma assinatura é revalidável — pré-condição da verificação (AOS-048).
	ErrNoTrustStore = errors.New("revalidation: trust store obrigatório (revalidação de assinatura é pré-condição)")

	// ErrNoAuditStore — não foi fornecido audit store. Sem audit nenhuma decisão é
	// selável; uma decisão não-auditável não é admissível (ADR-002/010).
	ErrNoAuditStore = errors.New("revalidation: audit store obrigatório (decisão não-auditável é recusada)")
)

// Reason é o CÓDIGO ESTÁVEL da razão de uma decisão de revalidação — vocabulário
// fechado, seguro para spans/audit (público, nunca um segredo). Cada bloqueio tem
// um código específico que identifica o passo fail-closed que o produziu.
type Reason string

const (
	// ReasonPermitted — todos os passos passaram; o despacho é autorizado.
	ReasonPermitted Reason = "permitted"

	// ReasonNotFrozen — a tool NÃO está no conjunto congelado do run (não foi
	// resolvida no arranque). Default-deny; sem quarentena (não há artefacto
	// conhecido a isolar).
	ReasonNotFrozen Reason = "not_frozen"

	// ReasonIdentityDrift — a identidade pinada (id, version) da definição em
	// backing store NÃO coincide com a congelada. Um swap de versão a meio do run
	// é drift → bloqueio + quarentena + alerta.
	ReasonIdentityDrift Reason = "identity_drift"

	// ReasonDigestMismatch — o digest recalculado da definição actual DIVERGE do
	// digest congelado (schema drift / rug-pull) → bloqueio + quarentena + alerta.
	ReasonDigestMismatch Reason = "digest_mismatch"

	// ReasonSignatureMissing — a definição actual não traz assinatura → bloqueio.
	ReasonSignatureMissing Reason = "signature_missing"

	// ReasonUntrustedKey — o publicador da definição actual não tem chave confiável
	// no trust store (ausente ou revogada) → bloqueio.
	ReasonUntrustedKey Reason = "untrusted_key"

	// ReasonSignatureInvalid — a assinatura não valida sobre (id, version, digest)
	// congelados → bloqueio + quarentena + alerta.
	ReasonSignatureInvalid Reason = "signature_invalid"

	// ReasonScopeDenied — um scope de credencial declarado no contract está FORA do
	// permitido pela política do run (ADR-006) → bloqueio + quarentena + alerta.
	ReasonScopeDenied Reason = "scope_denied"

	// ReasonEgressDenied — a classe de egress declarada excede o tecto permitido
	// pela política do run → bloqueio + quarentena + alerta.
	ReasonEgressDenied Reason = "egress_denied"

	// ReasonEgressHostDenied — o host concreto alvo não está na allowlist de egress
	// (EPIC-07) → bloqueio + quarentena + alerta.
	ReasonEgressHostDenied Reason = "egress_host_denied"

	// ReasonAuditFailed — o registo da decisão no audit WORM falhou. Uma
	// autorização não-auditável degrada para bloqueio (fail-closed sobre fail-closed).
	ReasonAuditFailed Reason = "audit_failed"

	// ReasonContextCanceled — o contexto foi cancelado antes da avaliação →
	// bloqueio (não auditado: gravar exigiria o mesmo contexto já cancelado).
	ReasonContextCanceled Reason = "context_canceled"
)

// Stage é o PASSO da sequência fail-closed em que a decisão foi tomada (público,
// para spans/observabilidade). Espelha o diagrama de tecnica/05 §5.
type Stage string

const (
	// StageLookup — passo (1): pertença ao conjunto congelado.
	StageLookup Stage = "lookup"
	// StageDigest — passo (2): revalidação do digest contra o congelado.
	StageDigest Stage = "digest"
	// StageSignature — passo (3): revalidação da assinatura.
	StageSignature Stage = "signature"
	// StageScopeEgress — passo (4): scopes e classe de egress dentro do permitido.
	StageScopeEgress Stage = "scope_egress"
	// StageExec — passo (5): autorização de despacho (permit emitido).
	StageExec Stage = "exec"
)
