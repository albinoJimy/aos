package worker

import "errors"

var (
	// ErrNilLeaseManager — o [Worker] foi construído sem um [durable.LeaseManager]
	// (nil): não há autoridade de posse de partição (lease/fencing).
	ErrNilLeaseManager = errors.New("worker: lease manager em falta")

	// ErrNilFencedAppender — o [Worker] foi construído sem um [durable.FencedAppender]
	// (nil): não haveria enforcement de fencing nas escritas de progresso.
	ErrNilFencedAppender = errors.New("worker: fenced appender em falta")

	// ErrNilLedger — o [Worker] foi construído sem um [durable.StepLedger] (nil): não
	// haveria idempotência por passo (a garantia de zero efeitos duplicados).
	ErrNilLedger = errors.New("worker: step ledger em falta")

	// ErrNilResumer — o [Worker] foi construído sem um [durable.Resumer] (nil): não
	// haveria resume-from-step.
	ErrNilResumer = errors.New("worker: resumer em falta")

	// ErrNilCheckpointer — o [Worker] foi construído sem um
	// [durable.EventStoreCheckpointer] (nil): o cursor de progresso nunca avançaria e
	// um worker de substituição re-executaria tudo desde o início.
	ErrNilCheckpointer = errors.New("worker: checkpointer em falta")

	// ErrNilMonitor — o [Worker] foi construído sem um [Mediator] (nil): sem o
	// Reference Monitor não há caminho legítimo para um efeito externo (ADR-002).
	ErrNilMonitor = errors.New("worker: reference monitor (mediator) em falta")

	// ErrNilPlan — [Worker.Run]/[Worker.Adopt] recebeu um [RunPlan] nil.
	ErrNilPlan = errors.New("worker: run plan nil")

	// ErrEmptyRunID — o [RunPlan] devolveu um run_id vazio.
	ErrEmptyRunID = errors.New("worker: run_id vazio")

	// ErrLeaseLost — o worker perdeu a posse da partição a meio do run (o lease foi
	// superado por um novo claim ou expirou por ausência de heartbeat) e PAROU de
	// escrever fail-closed. NÃO é corrupção: outra réplica assume a partição e retoma
	// resume-from-step. O caller pode requeue a partição (ver [Assigner.Requeue]).
	ErrLeaseLost = errors.New("worker: posse da partição perdida (lease superado/expirado); parado fail-closed")

	// ErrDenied — a mediação da tool call de um passo devolveu deny/escalate (o
	// Reference Monitor recusou). O passo NÃO produziu efeito; o worker propaga a
	// recusa em vez de a mascarar (fail-closed de política).
	ErrDenied = errors.New("worker: tool call negada pelo reference monitor")
)
