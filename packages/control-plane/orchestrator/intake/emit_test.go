package intake

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// O Recorder REAL de plannerevents satisfaz a superfície de emissão do intake —
// prova, em tempo de compilação, que reutilizamos o MESMO caminho de emissão (e a
// constante EventIntakeClassified lá dentro), sem cunhar um tipo novo.
var _ ClassificationRecorder = (*plannerevents.Recorder)(nil)

// captureRecorder guarda o último payload emitido, para asserções.
type captureRecorder struct {
	got  plannerevents.IntakeClassifiedPayload
	n    int
	fail error
}

func (r *captureRecorder) RecordIntakeClassified(_ context.Context, p plannerevents.IntakeClassifiedPayload) (uint64, error) {
	r.n++
	if r.fail != nil {
		return 0, r.fail
	}
	r.got = p
	return uint64(r.n), nil
}

// TestClassifyAndRecord_EmitsHeuristicNoObjective: a emissão leva a classificação e
// a heurística aplicada, e os ids — mas NÃO o texto do objectivo. Falha-antes: se
// [Result.Payload] copiasse o objectivo para um campo do evento, o payload deixaria
// de ser content-free (aqui verificamos que os campos são só os classificados).
func TestClassifyAndRecord_EmitsHeuristicNoObjective(t *testing.T) {
	t.Parallel()

	rec := &captureRecorder{}
	g := Goal{
		GoalID: "g1", PlanID: "p1",
		Objective:  "conteúdo untrusted que NÃO deve aparecer no evento",
		IntakeMode: IntakeModeMeta, RoleCardinality: 2,
	}

	res, seq, err := ClassifyAndRecord(context.Background(), rec, g, policy)
	if err != nil {
		t.Fatalf("ClassifyAndRecord: %v", err)
	}
	if seq != 1 || rec.n != 1 {
		t.Fatalf("esperava exactamente 1 emissão (seq=1), obtido seq=%d n=%d", seq, rec.n)
	}
	if res.Classification != plannerevents.ClassificationMeta || res.Heuristic != HeuristicExplicitMeta {
		t.Fatalf("veredicto inesperado: %+v", res)
	}
	want := plannerevents.IntakeClassifiedPayload{
		PlanID:         "p1",
		GoalID:         "g1",
		Classification: plannerevents.ClassificationMeta,
		Heuristic:      HeuristicExplicitMeta,
	}
	if rec.got != want {
		t.Fatalf("payload emitido = %+v, esperado %+v", rec.got, want)
	}
	// O tipo do payload não tem sequer um campo para o objectivo — garantia
	// estrutural de que nenhum texto untrusted viaja no evento.
	_ = want
}

// TestClassifyAndRecord_FailClosedOnEmitError: se a emissão falha, o erro propaga e
// o seq é 0 — o chamador não avança como se a rota estivesse registada. Falha-antes:
// se a função engolisse o erro do recorder, devolveria seq>0 sem evento gravado.
func TestClassifyAndRecord_FailClosedOnEmitError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("append falhou")
	rec := &captureRecorder{fail: sentinel}
	_, seq, err := ClassifyAndRecord(context.Background(), rec,
		Goal{GoalID: "g", PlanID: "p", IntakeMode: IntakeModeSimple, RoleCardinality: 1}, policy)
	if !errors.Is(err, sentinel) {
		t.Fatalf("erro de emissão tem de propagar, obtido %v", err)
	}
	if seq != 0 {
		t.Fatalf("em falha, seq tem de ser 0, obtido %d", seq)
	}
}
