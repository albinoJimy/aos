package harness

import (
	"context"

	"github.com/aos-ref/kernel/agent-runtime/durable"
)

// Helpers REUTILIZÁVEIS de idempotência por passo (AOS-112, AC5).
//
// Estes construtores e o driver [DriveEffectSchedule] (ver replay_idempotency.go) são a
// SUPERFÍCIE PÚBLICA com que QUALQUER ticket com efeito externo cumpre a DoD de
// idempotência sem reimplementar a mecânica: constrói um efeito de domínio idempotente
// (chave canónica f(run_id, step_id), contador observável) e submete-o ao calendário
// at-least-once com crash intercalado, provando ZERO efeitos observáveis duplicados
// (Observed()==1). COMPÕEM o step-ledger de AOS-014 ([durable.StepLedger]) — não o
// reimplementam.

// NewDomainEffect constrói um [Effect] de DOMÍNIO idempotente: uma activity com um
// efeito externo OBSERVÁVEL (aqui, uma escrita idempotente que incrementa um contador
// partilhado) cuja idempotency key é a forma canónica f(run_id, step_id) e NÃO varia
// com a tentativa. Sob o calendário at-least-once com crash intercalado
// ([DriveEffectSchedule]), o ledger de AOS-014 deduplica as re-tentativas e o efeito
// corre UMA só vez — [Effect.Observed] devolve 1. É o helper REUTILIZÁVEL com que
// qualquer ticket com efeito externo cumpre a DoD de idempotência (AOS-112, AC5).
//
// Um efeito com uma key NÃO-determinística (que varie com a tentativa) falha a
// deduplicar e Observed cresce — é a falha que a suite injecta como controlo negativo.
func NewDomainEffect(runID, stepID string) Effect {
	count := 0
	return Effect{
		StepID: stepID,
		KeyAt: func(int) (string, error) {
			return durable.IdempotencyKey(runID, stepID)
		},
		Run: func(context.Context) (durable.Result, error) {
			count++
			return durable.Result{Status: "ok", Payload: []byte("efeito:" + stepID)}, nil
		},
		Observed: func() int { return count },
	}
}

// StableEffect constrói um [Effect] idempotente para o SUB-PASSO (turno, índice) de um
// run, derivando o step_id canónico via [durable.StepSequencer.SubStepID]. É um
// [NewDomainEffect] com o step_id já sequenciado — a forma que as golden fixtures usam
// para exercitar cada tool call como um passo com efeito externo idempotente.
func StableEffect(runID string, seq *durable.StepSequencer, turn, index int) Effect {
	return NewDomainEffect(runID, seq.SubStepID(runID, turn, index))
}

// VerifyEffectIdempotency é o helper de UMA CHAMADA que qualquer ticket com efeito
// externo usa para cumprir a DoD de idempotência por passo: submete eff ao calendário
// at-least-once com crash intercalado ([DriveEffectSchedule]) sobre store e devolve o
// nº de vezes que o efeito OBSERVÁVEL correu de facto. Um efeito idempotente (chave
// estável f(run_id, step_id)) devolve 1; qualquer valor > 1 denuncia dedup falhada.
// store é o Event Store real de referência (eventstore.New() ou o ambiente efémero de
// AOS-110, que satisfaz [durable.EventStore]).
func VerifyEffectIdempotency(ctx context.Context, runID string, store durable.EventStore, eff Effect) (observed int, err error) {
	if err := DriveEffectSchedule(ctx, runID, store, eff); err != nil {
		return 0, err
	}
	return eff.Observed(), nil
}
