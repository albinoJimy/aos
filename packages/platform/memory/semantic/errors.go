package semantic

import "errors"

// Sentinelas de erro da memória semântica (base de conhecimento). Use errors.Is
// para ramificar. São fail-closed: uma escrita/consulta mal-formada nunca degrada
// silenciosamente — a proveniência é OBRIGATÓRIA e a sua ausência REJEITA a escrita.
var (
	// ErrNilStore — Event Store (log append-only, fonte de verdade) não configurado.
	ErrNilStore = errors.New("semantic: Event Store nil")
	// ErrNilKeyStore — KeyStore (chaves por titular; crypto-shredding de AOS-038) não
	// configurado.
	ErrNilKeyStore = errors.New("semantic: KeyStore nil")
	// ErrNilAuditStore — hash-chain de audit (AOS-011) não configurada. Sem ela não há
	// selagem do conteúdo nem promoção auditável (fail-closed).
	ErrNilAuditStore = errors.New("semantic: audit Store (hash-chain) nil")

	// ErrMissingFactID — facto sem id (a idempotência é f(run_id, fact_id)).
	ErrMissingFactID = errors.New("semantic: fact_id em falta")
	// ErrMissingKey — facto sem chave de consulta (dimensão de recuperação por chave).
	ErrMissingKey = errors.New("semantic: key em falta (recuperação por chave)")
	// ErrMissingSubjectID — facto sem titular (subject_id) — alvo do crypto-shredding.
	ErrMissingSubjectID = errors.New("semantic: subject_id em falta (alvo do crypto-shredding)")
	// ErrMissingAgentID — facto sem agent_id (proveniência/responsabilização).
	ErrMissingAgentID = errors.New("semantic: agent_id em falta (proveniência)")
	// ErrMissingRunID — facto sem run_id de origem — componente OBRIGATÓRIO da
	// proveniência (fonte, classificação, run_id). A sua ausência REJEITA a escrita.
	ErrMissingRunID = errors.New("semantic: run_id de origem em falta (proveniência obrigatória)")
	// ErrInvalidTTLClass — classe de retenção (ttl_class) fora do conjunto canónico.
	ErrInvalidTTLClass = errors.New("semantic: ttl_class inválida")
	// ErrMissingProvenanceSource — escrita SEM fonte de proveniência canónica. A
	// proveniência é OBRIGATÓRIA: um facto tem de declarar de onde veio (tool_result,
	// web, mcp_schema, system, …). Uma fonte vazia/desconhecida é REJEITADA aqui
	// (fail-closed) em vez de admitida silenciosamente — é a prova de "escrita sem
	// proveniência rejeitada" ao nível da base de conhecimento.
	ErrMissingProvenanceSource = errors.New("semantic: fonte de proveniência em falta ou não-canónica (escrita rejeitada, fail-closed)")
	// ErrProvenanceDowngrade — uma escrita de MENOR confiança (untrusted) tentou
	// sobrepor (shadow) um FactID que já é de MAIOR confiança (trusted por origem OU
	// promovido por curadoria). É REJEITADA fail-closed: a idempotência
	// f(run_id, fact_id) NUNCA permite um override cross-run que rebaixe a
	// classificação de um facto. Sem esta barreira, um segundo Write untrusted (web/
	// tool_result) com o MESMO FactID e RunID diferente substituiria o envelope
	// efectivo (last-write-wins) e envenenaria o control-plane / evictaria conhecimento
	// trusted do caminho do planeador (memory-poisoning ASI06).
	ErrProvenanceDowngrade = errors.New("semantic: escrita rejeitada — rebaixamento de proveniência de um FactID trusted/promovido (fail-closed)")

	// ErrFactNotFound — não existe facto com o id dado no log.
	ErrFactNotFound = errors.New("semantic: facto inexistente")
	// ErrFactShredded — o facto é IRRECUPERÁVEL: a chave do titular foi apagada
	// (crypto-shredding) ou expirou por TTL. O índice (chave/tags/proveniência)
	// PERMANECE e a hash-chain continua a verificar — só o conteúdo se perdeu (ADR-011).
	ErrFactShredded = errors.New("semantic: facto irrecuperável (chave apagada — crypto-shredding)")
)
