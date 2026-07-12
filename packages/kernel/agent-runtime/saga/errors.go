package saga

import "errors"

var (
	// ErrNilLedger — o [SagaCoordinator] foi construído sem um step-ledger (nil). A
	// idempotência das compensações (0 reversões duplicadas) ASSENTA no ledger de
	// AOS-014; sem ele não há garantia durável de already-applied.
	ErrNilLedger = errors.New("saga: step-ledger em falta")

	// ErrNilMachine — o [SagaCoordinator] foi construído sem a máquina de estados
	// (nil). A saga coordena as transições failed → compensating → ready de AOS-017
	// através dela; sem máquina não há estado durável nem reconstrução por replay.
	ErrNilMachine = errors.New("saga: máquina de estados em falta")

	// ErrNilRegistry — o [SagaCoordinator] foi construído sem um [CompensationRegistry]
	// (nil). Sem registo não há acções inversas a executar.
	ErrNilRegistry = errors.New("saga: registo de compensações em falta")

	// ErrEmptyStepID — foi registada (ou pedida a chave de) uma compensação com
	// step_id vazio. O step_id é metade da identidade da compensação e da sua chave
	// de idempotência f(run_id, comp-step_id).
	ErrEmptyStepID = errors.New("saga: step_id vazio")

	// ErrNilAction — a [Compensation] registada não trouxe acção inversa (Action nil).
	// Uma compensação sem efeito inverso não desfaz nada — recusa-se no registo.
	ErrNilAction = errors.New("saga: acção de compensação nil")

	// ErrNotCompensating — [SagaCoordinator.Compensate] foi invocado com o run num
	// estado do qual a saga NÃO pode partir. A saga só entra a partir de [state.Failed]
	// (aciona failed → compensating) ou retoma a partir de [state.Compensating]
	// (crash-resume). Qualquer outro estado é recusado sem tocar no run.
	ErrNotCompensating = errors.New("saga: run não está em failed nem em compensating")

	// ErrCompensationExhausted — uma compensação FALHOU após esgotar a política de
	// retry idempotente. A saga NÃO finge sucesso: não transita compensating → ready;
	// o run PERMANECE em compensating (estado honesto de "preso, exige intervenção") e
	// é ESCALADO via alerta ([Observer.Escalated]). Nota: a tabela de
	// AOS-017 não tem aresta compensating → killed, pelo que a escalada é por alerta +
	// paragem, não por uma transição de estado forjada.
	ErrCompensationExhausted = errors.New("saga: compensação irrecuperável após retries — escalada (run preso em compensating)")
)
