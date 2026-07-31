package episodic

import "errors"

// Sentinelas de erro da memória episódica. Use errors.Is para ramificar. São
// fail-closed: uma escrita/leitura mal-formada nunca degrada silenciosamente.
var (
	// ErrNilRecord — [EpisodeInput.Record] em falta (não há trajectória a persistir).
	ErrNilRecord = errors.New("episodic: trajectória (TrajectoryRecord) nil")
	// ErrMissingEpisodeID — episódio sem ID (a idempotência é f(run_id, episode_id)).
	ErrMissingEpisodeID = errors.New("episodic: episode_id em falta")
	// ErrMissingSubjectID — episódio sem titular (subject_id) — alvo do crypto-shredding.
	ErrMissingSubjectID = errors.New("episodic: subject_id em falta (alvo do crypto-shredding)")
	// ErrMissingRunID — episódio sem run_id (ligação ao Event Store / replay).
	ErrMissingRunID = errors.New("episodic: run_id em falta")
	// ErrMissingGoal — episódio sem objectivo (dimensão de indexação/recuperação).
	ErrMissingGoal = errors.New("episodic: goal em falta (indexação por objectivo)")
	// ErrMissingPrincipal — recuperação (Recall) sem a identidade VERIFICADA do
	// principal. O recall é ESCOPADO por principal (fail-closed): sem principal, a
	// recuperação é RECUSADA — nunca devolve memória de outro principal. Ver
	// [Query.PrincipalID].
	ErrMissingPrincipal = errors.New("episodic: principal_id em falta (recall escopado por principal — fail-closed)")
	// ErrInvalidTTLClass — classe de retenção (ttl_class) fora do conjunto canónico.
	ErrInvalidTTLClass = errors.New("episodic: ttl_class inválida")
	// ErrNilStore — Event Store (log append-only) não configurado.
	ErrNilStore = errors.New("episodic: Event Store nil")
	// ErrNilKeyStore — KeyStore (chaves por titular) não configurado.
	ErrNilKeyStore = errors.New("episodic: KeyStore nil")
	// ErrNilAuditStore — hash-chain de audit (integridade do log) não configurada.
	ErrNilAuditStore = errors.New("episodic: audit Store (hash-chain) nil")

	// ErrDecrypt — falha de decifragem do envelope (KEK errada ou blob adulterado).
	// É o erro de base do crypto-shredding na leitura.
	ErrDecrypt = errors.New("episodic: falha de decifragem do envelope")
	// ErrEpisodeShredded — o episódio é IRRECUPERÁVEL: a chave do titular foi apagada
	// (crypto-shredding) ou expirou por TTL. Distingue-se de ErrNotFound: o registo
	// selado CONTINUA no log e a hash-chain continua a verificar — só o plaintext se
	// perdeu (ADR-011). Desembrulha para [ErrDecrypt].
	ErrEpisodeShredded = errors.New("episodic: episódio irrecuperável (chave apagada — crypto-shredding)")
	// ErrEpisodeNotFound — não existe episódio com o id dado no log.
	ErrEpisodeNotFound = errors.New("episodic: episódio inexistente")

	// ErrResumeUnsupported — o EventLog subjacente não é um durable.EventStore, pelo
	// que o resume-from-step (que relê os checkpoints do run) não é suportado.
	ErrResumeUnsupported = errors.New("episodic: EventLog não suporta resume-from-step")

	// ErrQueueFull — a fila de escrita (em memória, fora da hot path) atingiu o tecto
	// configurado. É backpressure fail-closed: o produtor deve drenar (Flush) antes de
	// enfileirar mais, em vez de crescer memória sem limite. Ver [WithMaxQueue].
	ErrQueueFull = errors.New("episodic: fila de escrita cheia (drenar com Flush)")
)
