package engine_test

import (
	"context"
	"fmt"
	"sync"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/activity"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/engine"
	"github.com/aos-ref/kernel/agent-runtime/replay"
)

// fakeEngine é um STUB in-memory da porta [engine.Engine] — um BACKEND ALTERNATIVO
// que NÃO usa o contrato próprio (sem Event Store replicado, sem Reference Monitor,
// sem step-ledger real). Existe unicamente para provar o ISOLAMENTO POR CONTRATO
// (Princípio 8, ADR-015): o MESMO código de RT (o driver do teste de contrato) corre
// sobre ele e sobre o [engine.OwnContractEngine] sem uma linha de diferença.
//
// Honra as invariantes OBSERVÁVEIS da porta com a implementação mais simples
// possível — é o que basta para o RT não notar a troca de backend:
//   - Dispatch idempotente por (run_id, step_id): a primeira vez corre o "efeito"
//     (incrementa o contador) e memoriza; as seguintes devolvem o registado
//     (Deduplicated=true) sem novo efeito;
//   - output SEMPRE untrusted (ADR-005);
//   - Checkpoint guarda o último cursor; Resume devolve-o coerente (não FromScratch
//     se houve progresso) e NOMEIA o próximo passo (NextStepID) com a MESMA semântica
//     do [durable.Resumer] — derivado do [durable.StepSequencer] em fronteira de turno
//     ou da primeira PendingActivity mid-dispatch;
//   - Replay reconstrói do seu próprio registo: só reporta fidelidade 1.0/terminado
//     quando HÁ algo aplicado; um run sem despachos devolve a forma vazia (fidelidade
//     0), pelo que a asserção de replay significa algo mesmo no ramo do stub.
//
// NÃO é durável, NÃO é distribuído e NÃO tem as garantias do contrato próprio — não
// é um substrato de produção. É a "outra ponta" da troca de backend do teste de
// contrato. É deliberadamente ingénuo.
type fakeEngine struct {
	mu      sync.Mutex
	applied map[string][]byte      // key run_id:step_id -> output memorizado
	lastCP  *durable.Cursor        // último checkpoint (fonte do Resume)
	effects *int                   // efeitos externos REAIS (partilhado com a asserção do driver)
	seq     *durable.StepSequencer // MESMO derivador de step_id do Resumer (nomeia o NextStepID)
}

// newFakeEngine constrói o stub. effects é o contador de efeitos externos que o
// driver do teste inspecciona (o análogo, no backend próprio, do contador da tool
// registada no Reference Monitor). Usa o MESMO [durable.StepSequencer] canónico que
// o [durable.Resumer] para nomear o NextStepID de forma idêntica ao backend próprio.
func newFakeEngine(effects *int) *fakeEngine {
	return &fakeEngine{applied: make(map[string][]byte), effects: effects, seq: durable.NewStepSequencer()}
}

// Dispatch: idempotente por (run_id, step_id). Primeira vez ⇒ "efeito" corre (o
// contador sobe) e memoriza; repetição ⇒ devolve o memorizado sem novo efeito.
func (f *fakeEngine) Dispatch(_ context.Context, act activity.Activity) (activity.Result, error) {
	key, err := durable.IdempotencyKey(act.RunID, act.StepID)
	if err != nil {
		return activity.Result{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if out, ok := f.applied[key]; ok {
		// already-applied: resultado registado, ZERO efeito novo.
		return activity.Result{
			Output:       agentruntime.Untrusted(out),
			Status:       activity.StatusOK,
			Deduplicated: true,
		}, nil
	}
	// primeira aplicação: o "efeito externo" corre exactamente uma vez.
	*f.effects++
	out := []byte(fmt.Sprintf("fake:%s", act.StepID))
	f.applied[key] = out
	return activity.Result{Output: agentruntime.Untrusted(out), Status: activity.StatusOK}, nil
}

// Checkpoint guarda o cursor do último checkpoint (fonte do Resume).
func (f *fakeEngine) Checkpoint(_ context.Context, cp agentruntime.Checkpoint) error {
	if cp.RunID == "" {
		return durable.ErrEmptyRunID
	}
	confirmed := cp.ConfirmedStepID
	if confirmed == "" {
		confirmed = cp.StepID
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastCP = &durable.Cursor{
		RunID:             cp.RunID,
		ConfirmedStepID:   confirmed,
		Turn:              cp.Turn,
		Phase:             string(cp.Phase),
		PendingActivities: cp.PendingActivities,
	}
	return nil
}

// Resume devolve o cursor de retoma a partir do último checkpoint. Sem checkpoints
// ⇒ FromScratch (turno 1). Reproduz a MESMA semântica observável do [durable.Resumer],
// incluindo o NEXTSTEPID (o campo que NOMEIA o ponto de retoma):
//   - FromScratch / fronteira de turno (verified ou despacho concluído): o step_id do
//     turno a (re)entrar, derivado do [durable.StepSequencer];
//   - mid-dispatch (há PendingActivities): o step_id da PRIMEIRA activity pendente.
func (f *fakeEngine) Resume(_ context.Context, runID string) (durable.ResumePoint, error) {
	if runID == "" {
		return durable.ResumePoint{}, durable.ErrEmptyRunID
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastCP == nil {
		return durable.ResumePoint{
			RunID:       runID,
			FromScratch: true,
			NextTurn:    1,
			NextStepID:  f.seq.StepID(runID, 1),
		}, nil
	}
	rp := durable.ResumePoint{
		RunID:             runID,
		LastConfirmed:     *f.lastCP,
		NextTurn:          f.lastCP.Turn,
		PendingActivities: f.lastCP.PendingActivities,
	}
	switch {
	case agentruntime.CheckpointPhase(f.lastCP.Phase) == agentruntime.PhaseVerified:
		// Turno completo: retoma no turno seguinte, nomeado pelo sequenciador.
		rp.NextTurn = f.lastCP.Turn + 1
		rp.NextStepID = f.seq.StepID(runID, f.lastCP.Turn+1)
	case len(f.lastCP.PendingActivities) > 0:
		// Mid-dispatch: retoma na primeira activity pendente (posição, não invocação).
		rp.NextStepID = f.lastCP.PendingActivities[0]
	default:
		// Fronteira de despacho sem pendentes: re-entra o mesmo turno.
		rp.NextStepID = f.seq.StepID(runID, f.lastCP.Turn)
	}
	return rp, nil
}

// Replay reconstrói do PRÓPRIO registo (o mapa applied), com ZERO efeitos — o stub
// não tem para onde os emitir. A fidelidade é DERIVADA do estado, não constante: só
// reporta 1.0/terminado quando houve pelo menos um despacho memorizado; um run sem
// registo devolve a forma vazia (não-terminado, fidelidade 0). Assim a asserção de
// replay do teste de contrato significa algo mesmo no ramo do stub. Não é um motor
// determinístico real (esse é AOS-016, provado contra o adaptador de referência).
func (f *fakeEngine) Replay(_ context.Context, runID string, _ replay.Options) (replay.ReplayResult, error) {
	if runID == "" {
		return replay.ReplayResult{}, durable.ErrEmptyRunID
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.applied) == 0 {
		// Nada aplicado: não há trajectória para reconstruir.
		return replay.ReplayResult{RunID: runID}, nil
	}
	return replay.ReplayResult{RunID: runID, Fidelity: 1.0, Terminated: true}, nil
}

// Mode: o stub opera em modo normal (executa e memoriza).
func (f *fakeEngine) Mode() activity.Mode { return activity.ModeNormal }

// Verificação em tempo de compilação: o STUB satisfaz a MESMA porta que o adaptador
// de referência. É a base do teste de contrato — se ambos satisfazem [engine.Engine],
// o driver de RT (escrito contra [engine.Engine]) compila e corre sobre os dois.
var _ engine.Engine = (*fakeEngine)(nil)
