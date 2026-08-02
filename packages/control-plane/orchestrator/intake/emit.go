package intake

import (
	"context"

	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// ClassificationRecorder é a superfície MÍNIMA de emissão de que o intake depende:
// apensar `plan.intake_classified`. *[plannerevents.Recorder] satisfá-la. Declará-la
// aqui mantém o intake desacoplado do Event Store enquanto REUTILIZA a MESMA
// constante de evento e o MESMO payload de plannerevents — este pacote não cunha um
// tipo de evento novo (regra 4 / CA). O caminho de emissão continua a ser o de
// plannerevents, que resolve a constante [plannerevents.EventIntakeClassified].
type ClassificationRecorder interface {
	RecordIntakeClassified(ctx context.Context, p plannerevents.IntakeClassifiedPayload) (uint64, error)
}

// Payload projecta o veredicto num [plannerevents.IntakeClassifiedPayload] pronto a
// emitir. Só ids opacos e a heurística content-free entram — o `objective` (dados
// untrusted) NÃO viaja no evento. É a fronteira que impede eco de conteúdo no log.
func (r Result) Payload(g Goal) plannerevents.IntakeClassifiedPayload {
	return plannerevents.IntakeClassifiedPayload{
		PlanID:         g.PlanID,
		GoalID:         g.GoalID,
		Classification: r.Classification,
		Heuristic:      r.Heuristic,
	}
}

// ClassifyAndRecord classifica g contra policy (via [ClassifyGoal], que descarta o
// `objective`) e emite `plan.intake_classified` por rec. Devolve o veredicto e o
// número de sequência do evento. Fail-closed: se a emissão falhar, o erro propaga e
// o seq devolvido é 0 — o chamador não deve avançar como se a rota estivesse
// registada. A classificação PURA permanece separável em [ClassifyGoal]; esta
// função é apenas a aresta de I/O.
func ClassifyAndRecord(ctx context.Context, rec ClassificationRecorder, g Goal, policy TenantPolicy) (Result, uint64, error) {
	res := ClassifyGoal(g, policy)
	seq, err := rec.RecordIntakeClassified(ctx, res.Payload(g))
	if err != nil {
		return res, 0, err
	}
	return res, seq, nil
}
