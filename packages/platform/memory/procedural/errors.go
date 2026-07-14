// Package procedural implementa a MEMÓRIA PROCEDURAL do AOS (AOS-040): skills e
// heurísticas APRENDIDAS (auto-escritas) como artefactos comportamentais
// versionados (SemVer + manifesto) ligados a um pipeline de promoção estagiada
// fail-closed — staging → eval-gate → canary → ratificação humana assinada →
// produção — com rollback atómico em regressão.
//
// É, nas palavras da fonte (Dim. 7, Princípio 7; ADR-012), "a mudança de maior
// risco do sistema": a auto-modificação sofre de misevolution/drift mesmo sem
// atacante. Por isso NENHUMA skill chega a produção unilateralmente. Este pacote
// NÃO reimplementa o pipeline de auto-modificação nem o Registry (EPIC-05) nem o
// eval-gate (EPIC-08): LIGA a classe procedural (AOS-035, domain.ProceduralBody)
// aos hooks de promoção, consultando as PORTAS SkillRegistry/EvalGate/CanaryGate
// (impls de referência aqui) e selando cada transição na hash-chain de audit
// tamper-evident (AOS-011) com assinatura ed25519 (AOS-005/006, stdlib).
//
// Invariantes estruturais (provados por teste):
//   - allowlist fail-closed: só uma skill em PRODUÇÃO (pipeline completo) é
//     executável em prod; uma skill em staging/canary é estruturalmente excluída;
//   - activação bloqueada até eval-gate VERDE + canary VERDE + ratificação
//     assinada VÁLIDA — falta de qualquer um → RECUSADA;
//   - uma skill não se pode auto-promover (requer os gates externos + assinatura
//     humana);
//   - rollback atómico automático em regressão, sem downtime (a versão prod
//     anterior fica pinada; a activação da nova é atómica; em regressão reverte
//     atomicamente);
//   - toda a transição de estado é registada no audit trail assinado.
package procedural

// ProceduralError é o erro sentinela do pacote. Carrega um código estável
// comparável com errors.Is. Toda a decisão resolve-se pelo lado seguro
// (fail-closed): na dúvida, RECUSA.
type ProceduralError struct {
	Code string
	msg  string
}

func (e *ProceduralError) Error() string { return e.Code + ": " + e.msg }

var (
	// ErrNilAuditStore — construção sem Store de audit. Toda a transição TEM de ser
	// auditável; sem hash-chain não há pipeline (fail-closed).
	ErrNilAuditStore = &ProceduralError{Code: "E_PROC_NIL_AUDIT_STORE", msg: "audit store obrigatorio para transicoes auditaveis"}
	// ErrNilSigner — construção sem assinador. Cada transição é assinada; sem chave
	// não há promoção.
	ErrNilSigner = &ProceduralError{Code: "E_PROC_NIL_SIGNER", msg: "signer ed25519 obrigatorio para transicoes assinadas"}
	// ErrNilRegistry — construção sem Skill/Tool Registry (EPIC-05, porta). O
	// pin+hash+assinatura do artefacto é obrigatório.
	ErrNilRegistry = &ProceduralError{Code: "E_PROC_NIL_REGISTRY", msg: "skill registry (EPIC-05) obrigatorio para pin+hash+assinatura"}
	// ErrNilEvalGate — construção sem eval-gate (EPIC-08, porta).
	ErrNilEvalGate = &ProceduralError{Code: "E_PROC_NIL_EVAL_GATE", msg: "eval-gate (EPIC-08) obrigatorio"}
	// ErrNilCanaryGate — construção sem canary-gate (EPIC-08, porta).
	ErrNilCanaryGate = &ProceduralError{Code: "E_PROC_NIL_CANARY_GATE", msg: "canary-gate (EPIC-08) obrigatorio"}

	// ErrInvalidManifest — manifesto incompleto (skill_name/autor/run_id em falta
	// ou versão SemVer inválida). O artefacto SEM manifesto completo é rejeitado.
	ErrInvalidManifest = &ProceduralError{Code: "E_PROC_INVALID_MANIFEST", msg: "manifesto invalido: skill_name/version(SemVer)/autor/run_id obrigatorios"}
	// ErrContentHashMismatch — o hash do conteúdo não corresponde ao declarado no
	// manifesto. O pin de integridade falhou (conteúdo forjado/corrompido).
	ErrContentHashMismatch = &ProceduralError{Code: "E_PROC_CONTENT_HASH_MISMATCH", msg: "hash do conteudo nao corresponde ao manifesto (pin de integridade falhou)"}
	// ErrDuplicateVersion — submissão de uma (skill,versão) já existente. As versões
	// são imutáveis (SemVer); reescrever uma versão é proibido.
	ErrDuplicateVersion = &ProceduralError{Code: "E_PROC_DUPLICATE_VERSION", msg: "versao ja submetida: artefactos SemVer sao imutaveis"}
	// ErrSkillNotFound — operação sobre uma (skill,versão) inexistente.
	ErrSkillNotFound = &ProceduralError{Code: "E_PROC_SKILL_NOT_FOUND", msg: "skill/versao nao encontrada"}

	// ErrEvalGateNotPassed — o eval-gate (golden-set + trace-diffing) NÃO ficou
	// verde. O canary e a activação ficam bloqueados (fail-closed).
	ErrEvalGateNotPassed = &ProceduralError{Code: "E_PROC_EVAL_GATE_NOT_PASSED", msg: "eval-gate nao verde (golden-set/trace-diffing): promocao bloqueada"}
	// ErrCanaryNotPassed — o canary (success-rate/unsafe-action rate) NÃO ficou
	// verde. A ratificação e a activação ficam bloqueadas.
	ErrCanaryNotPassed = &ProceduralError{Code: "E_PROC_CANARY_NOT_PASSED", msg: "canary nao verde (success-rate/unsafe-action rate): promocao bloqueada"}
	// ErrRatificationInvalid — a ratificação humana tem assinatura ed25519 ausente
	// ou inválida para o alvo (skill,versão). Sem ratificação válida não há prod.
	ErrRatificationInvalid = &ProceduralError{Code: "E_PROC_RATIFICATION_INVALID", msg: "ratificacao humana ausente ou assinatura ed25519 invalida"}
	// ErrRatifierNotAuthorized — a assinatura provém de um ratificador não incluído
	// na allowlist de ratificadores autorizados.
	ErrRatifierNotAuthorized = &ProceduralError{Code: "E_PROC_RATIFIER_NOT_AUTHORIZED", msg: "ratificador nao autorizado (fora da allowlist de chaves)"}
	// ErrActivationRefused — activação em PRODUÇÃO pedida sem os três gates verdes
	// (eval-gate + canary + ratificação assinada). É a barreira central fail-closed:
	// falta de QUALQUER um → RECUSADA. Também cobre a auto-promoção (uma skill não se
	// promove a si própria: exige os gates externos + assinatura humana).
	ErrActivationRefused = &ProceduralError{Code: "E_PROC_ACTIVATION_REFUSED", msg: "activacao em prod recusada: exige eval-gate verde + canary verde + ratificacao assinada"}
	// ErrNotExecutableInProd — tentativa de executar em produção uma skill que NÃO
	// está na allowlist de produção (ex.: em staging/canary). Barreira estrutural.
	ErrNotExecutableInProd = &ProceduralError{Code: "E_PROC_NOT_EXECUTABLE_IN_PROD", msg: "skill nao executavel em prod: fora da allowlist (so uma versao com pipeline completo entra)"}
	// ErrInvalidStageTransition — transição de estado pedida a partir de um estado
	// que não a permite (ex.: correr o eval-gate sobre uma skill que já não está em
	// staging). A máquina é fail-closed: só transições válidas são aceites.
	ErrInvalidStageTransition = &ProceduralError{Code: "E_PROC_INVALID_STAGE_TRANSITION", msg: "transicao de estado invalida a partir do estado corrente"}
	// ErrNoPreviousVersion — rollback pedido sem versão prod anterior pinada. O
	// rollback fail-closed DESACTIVA a skill (nenhuma versão em prod é melhor que
	// uma versão regredida) mas sinaliza a ausência de alvo de reversão.
	ErrNoPreviousVersion = &ProceduralError{Code: "E_PROC_NO_PREVIOUS_VERSION", msg: "sem versao prod anterior para reverter: skill desactivada (fail-closed)"}
)
