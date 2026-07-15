package promotion

// PromotionError é o erro sentinela do ciclo de publicação/promoção (AOS-053).
// Código estável, comparável com errors.Is. Fail-closed em toda a superfície:
// qualquer condição que não seja um sucesso inequívoco resolve-se por rejeição.
type PromotionError struct {
	Code string
	msg  string
}

func (e *PromotionError) Error() string { return e.Code + ": " + e.msg }

var (
	// ErrNilRegistry — o Pipeline foi construído sem Registry. Sem o catálogo
	// append-only não há onde publicar/promover (fail-closed).
	ErrNilRegistry = &PromotionError{Code: "E_PROMO_NIL_REGISTRY", msg: "registry nao configurado"}

	// ErrNilIntegrity — o Pipeline foi construído sem verificador de integridade
	// (assinatura, AOS-048). Sem ele nenhuma promoção a active é verificável; o
	// default é recusar, nunca admitir (ADR-002).
	ErrNilIntegrity = &PromotionError{Code: "E_PROMO_NIL_INTEGRITY", msg: "verificador de integridade (assinatura) nao configurado"}

	// ErrNoAudit — o Pipeline foi construído sem audit store. Sem selagem WORM
	// nenhuma transição é auditável; a governação exige-o (ADR-010/012, fail-closed).
	ErrNoAudit = &PromotionError{Code: "E_PROMO_NO_AUDIT", msg: "audit store nao configurado"}

	// ErrNotStaging — tentou-se PROMOVER (a active) um artefacto que não está em
	// staging. A primeira promoção parte SEMPRE de staging; a re-activação de uma
	// versão deprecated é o caminho de rollback, não de promoção directa.
	ErrNotStaging = &PromotionError{Code: "E_PROMO_NOT_STAGING", msg: "promocao exige estado staging"}

	// ErrIntegrityRejected — a PRÉ-CONDIÇÃO de integridade (hash + assinatura +
	// contrato + ValidateBump) falhou. O artefacto NÃO é promovido (permanece em
	// staging, fora de active). Embrulha a causa concreta (digest/contrato/bump/
	// assinatura) para diagnóstico.
	ErrIntegrityRejected = &PromotionError{Code: "E_PROMO_INTEGRITY_REJECTED", msg: "verificacao de integridade recusou a promocao"}

	// ErrEvalGateRejected — uma SKILL AUTO-ESCRITA falhou o eval-gate (golden-set +
	// trace-diffing vs baseline, ADR-012). É REJEITADA: não vai a produção.
	ErrEvalGateRejected = &PromotionError{Code: "E_PROMO_EVAL_REJECTED", msg: "skill auto-escrita falhou o eval-gate: rejeitada"}

	// ErrNoEvalGate — tentou-se promover uma skill auto-escrita num Pipeline SEM
	// eval-gate injectado. Fail-closed: sem o gate a skill comportamental não é
	// avaliável, logo não é promovível (nunca se admite por omissão de wiring).
	ErrNoEvalGate = &PromotionError{Code: "E_PROMO_NO_EVAL_GATE", msg: "skill auto-escrita sem eval-gate configurado (fail-closed)"}

	// ErrRatificationRequired — uma skill auto-escrita foi submetida a promoção SEM
	// ratificação humana assinada. Sem ratificação válida a activação é recusada.
	ErrRatificationRequired = &PromotionError{Code: "E_PROMO_RATIFICATION_REQUIRED", msg: "promocao de skill auto-escrita exige ratificacao humana assinada"}

	// ErrRatificationInvalid — a ratificação apresentada é inválida: ratificador
	// não-autorizado (fora da allowlist) ou assinatura que não valida sobre o tuplo
	// canónico (id, version, digest). Fail-closed: não-repúdio exige assinatura
	// verificável de uma chave humana autorizada.
	ErrRatificationInvalid = &PromotionError{Code: "E_PROMO_RATIFICATION_INVALID", msg: "ratificacao humana invalida ou nao-autorizada"}

	// ErrNoRatifiers — tentou-se promover uma skill auto-escrita num Pipeline SEM
	// allowlist de ratificadores. Fail-closed: sem chaves autorizadas nenhuma
	// ratificação é verificável, logo nenhuma skill auto-escrita é promovível.
	ErrNoRatifiers = &PromotionError{Code: "E_PROMO_NO_RATIFIERS", msg: "sem allowlist de ratificadores configurada (fail-closed)"}

	// ErrAuditFailed — a selagem WORM de uma transição falhou. Uma transição
	// não-auditável não é admissível: prevalece a recusa (fail-closed sobre
	// fail-closed).
	ErrAuditFailed = &PromotionError{Code: "E_PROMO_AUDIT_FAILED", msg: "selagem da transicao no audit WORM falhou"}

	// ErrRollbackTarget — o alvo de rollback é inválido (inexistente, não-deprecated,
	// revogado, ou já active). O rollback re-promove uma versão FORMALMENTE
	// deprecated e verificada, nunca um atalho para active.
	ErrRollbackTarget = &PromotionError{Code: "E_PROMO_ROLLBACK_TARGET", msg: "alvo de rollback invalido (inexistente/nao-deprecated/revogado/ja active)"}

	// ErrNotApproved — o verificador composto negou a promoção de uma skill
	// auto-escrita porque não existe aprovação (eval-gate + ratificação) registada
	// para (id, version, digest). É a barreira ESTRUTURAL que impede uma skill
	// auto-escrita de chegar a active por um caminho que ignore o Pipeline.
	ErrNotApproved = &PromotionError{Code: "E_PROMO_NOT_APPROVED", msg: "skill auto-escrita sem aprovacao (eval-gate+ratificacao) registada"}
)
