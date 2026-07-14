package compression

import "errors"

// Sentinelas de erro da compressão de contexto assíncrona (AOS-043). Use errors.Is
// para ramificar. São fail-closed: uma configuração/entrada mal-formada nunca
// degrada silenciosamente numa compressão parcial ou divergente.
var (
	// ErrNilEventLog — Event Store (log append-only) não configurado. A compactação é
	// uma ACTIVITY durável (ADR-001): sem log, não há onde persistir idempotentemente.
	ErrNilEventLog = errors.New("compression: Event Store (EventLog) nil")

	// ErrNilCompactor — CheckpointTrigger construído sem compactor (nada a accionar).
	ErrNilCompactor = errors.New("compression: AsyncCompactor nil")

	// ErrMissingRunID — origem de compactação sem run_id (raiz de idempotência e
	// correlação f(run_id, checkpoint_id)).
	ErrMissingRunID = errors.New("compression: run_id obrigatório")

	// ErrMissingCheckpointID — origem sem checkpoint_id (a compactação SÓ ocorre em
	// checkpoints; o id é o discriminador de idempotência da activity).
	ErrMissingCheckpointID = errors.New("compression: checkpoint_id obrigatório")

	// ErrMissingTraceID — origem sem trace_id (liga o registo completo ao backend).
	ErrMissingTraceID = errors.New("compression: trace_id obrigatório")

	// ErrNoTurns — origem de compactação sem turnos (nada para sumarizar).
	ErrNoTurns = errors.New("compression: origem sem turnos a compactar")

	// ErrInvalidPolicyVersion — a versão da política de compressão não é SemVer válido.
	ErrInvalidPolicyVersion = errors.New("compression: versão da política inválida (SemVer MAJOR.MINOR.PATCH)")

	// ErrQueueFull — a fila de checkpoints pendentes atingiu o tecto (backpressure
	// fail-closed). O consumidor deve drenar (RunCheckpoint) antes de reenfileirar.
	ErrQueueFull = errors.New("compression: fila de checkpoints cheia (backpressure)")

	// ErrCorruptSummary — um sumário persistido no Event Store não desserializa como
	// envelope válido (corrupção/schema incompatível). Fail-closed: a verificação de
	// idempotência recusa continuar em vez de tratar um estado durável ilegível como
	// "não existe" (o que reexecutaria a compactação e re-emitiria o registo).
	ErrCorruptSummary = errors.New("compression: sumário persistido corrompido (envelope ilegível)")

	// ErrPrefixMutated — DETECÇÃO DE CACHE THRASH: o prefixo imutável mudou entre o
	// enfileiramento do checkpoint e a compactação. A compressão NUNCA muta o prefixo
	// (ADR-009); se o hash divergiu, algo externo invalidou o prefixo — a poupança de
	// prefix caching cai. É sinalizado (alerta) e a compactação ABORTA fail-closed.
	ErrPrefixMutated = errors.New("compression: prefixo mutado entre checkpoint e compactação (cache thrash)")
)
