package planner_test

// AOS-415 — a validação estrutural entra no LAÇO de tentativas, e a recusa volta ao modelo.
//
// Em produção (AOS-412 e AOS-414), a PRIMEIRA decomposição do modelo vivo foi recusada pela
// validação AOS-231 nas duas vezes, com a mesma razão, e o `serve` terminava: o laço só cobria o
// decode, e cada tentativa reenviava o mesmo prompt.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planner"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// decompositorQueRegista devolve sempre um documento válido de FORMA e guarda a recusa que
// recebeu em cada tentativa — é por aí que se prova que o feedback chegou ao decompositor.
type decompositorQueRegista struct {
	calls    atomic.Int64
	recusas  []*planner.Rejection
	objectos []string
}

func (d *decompositorQueRegista) Decompose(_ context.Context, in planner.DecomposeInput) (plan.PlanDocument, error) {
	d.calls.Add(1)
	d.recusas = append(d.recusas, in.Rejection)
	d.objectos = append(d.objectos, in.Context.Goal)
	return validDoc(), nil
}

// validadorQueRecusa recusa as primeiras `recusaAte` chamadas e admite a seguir.
type validadorQueRecusa struct {
	recusaAte int
	chamadas  int
}

func (v *validadorQueRecusa) Validate(plan.PlanDocument) *planner.Rejection {
	v.chamadas++
	if v.chamadas <= v.recusaAte {
		return &planner.Rejection{Rule: "schema", Reason: "consumes_taint_authority", NodeID: "n3"}
	}
	return nil
}

func pedidoDeTeste() planner.DecomposeRequest {
	return planner.DecomposeRequest{
		RunID: runID, ParentBudgetNode: parentNode, PlannerBudgetNode: plannerNode,
		Context: planner.PlanningContext{Goal: "g", ContextUnits: 4, CapabilitiesHash: "sha256:cap"},
	}
}

func plannerDeTeste(t *testing.T, dec planner.Decomposer, opts ...planner.Option) (*planner.Planner, planner.DecomposeRequest) {
	t.Helper()
	iss := newIssuer(t)
	b := newBudget(t, runID, amt(1_000_000, 1_000_000))
	p, err := planner.NewPlanner(b, permittingRM(t), iss, dec, opts...)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	req := pedidoDeTeste()
	req.ParentToken = runToken(t, iss).Compact
	req.Child = plannerChildReq()
	return p, req
}

// TestAOS415_RecusaDoValidadorGeraNovaTentativaComARazao é o caso que a produção mediu em falta.
// FALHA-ANTES: sem validador no laço, a 1.ª recusa acabava o planeamento.
func TestAOS415_RecusaDoValidadorGeraNovaTentativaComARazao(t *testing.T) {
	dec := &decompositorQueRegista{}
	p, req := plannerDeTeste(t, dec, planner.WithValidator(&validadorQueRecusa{recusaAte: 1}))

	res, err := p.Decompose(context.Background(), req)
	if err != nil {
		t.Fatalf("a 2.ª tentativa tinha de produzir plano: %v", err)
	}
	if res.Attempts != 2 || res.ValidatorRejections != 1 {
		t.Fatalf("tentativas=%d recusas=%d, queria 2 e 1", res.Attempts, res.ValidatorRejections)
	}
	if n := dec.calls.Load(); n != 2 {
		t.Fatalf("o decompositor tinha de ser chamado 2 vezes, foi %d", n)
	}
	// A 1.ª tentativa não traz recusa; a 2.ª traz a razão da 1.ª, em códigos.
	if dec.recusas[0] != nil {
		t.Fatalf("a 1.ª tentativa não podia trazer recusa: %+v", dec.recusas[0])
	}
	r := dec.recusas[1]
	if r == nil || r.Rule != "schema" || r.Reason != "consumes_taint_authority" || r.NodeID != "n3" {
		t.Fatalf("a 2.ª tentativa tinha de trazer a razão da recusa: %+v", r)
	}
}

// Esgotado o tecto, o planeamento falha COM a razão da última recusa — e não como uma falha de
// decomposição, que é outra coisa.
func TestAOS415_TectoEsgotadoDevolveARecusa(t *testing.T) {
	dec := &decompositorQueRegista{}
	p, req := plannerDeTeste(t, dec,
		planner.WithValidator(&validadorQueRecusa{recusaAte: 99}),
		planner.WithMaxAttempts(3))

	_, err := p.Decompose(context.Background(), req)
	if !errors.Is(err, planner.ErrPlanRejected) {
		t.Fatalf("esgotado o tecto, o erro tinha de ser ErrPlanRejected: %v", err)
	}
	if errors.Is(err, planner.ErrDecomposition) {
		t.Fatal("uma recusa do validador NÃO é uma falha de decomposição — o modelo produziu documento")
	}
	if n := dec.calls.Load(); n != 3 {
		t.Fatalf("o tecto é de 3 tentativas, houve %d", n)
	}
}

// Controlo negativo: SEM validador injectado, nada muda — o laço só cobre o decode, como antes.
func TestAOS415_SemValidadorOComportamentoEOAnterior(t *testing.T) {
	dec := &decompositorQueRegista{}
	p, req := plannerDeTeste(t, dec)
	res, err := p.Decompose(context.Background(), req)
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if res.Attempts != 1 || res.ValidatorRejections != 0 {
		t.Fatalf("sem validador: tentativas=%d recusas=%d, queria 1 e 0", res.Attempts, res.ValidatorRejections)
	}
	if dec.recusas[0] != nil {
		t.Fatalf("sem validador não há recusa para reapresentar: %+v", dec.recusas[0])
	}
}

// As tentativas recusadas continuam a ser tentativas: cada uma abre o seu span chat, anotado com
// o custo por tentativa — o planeamento não fica um ponto cego por o validador ter recusado. (O
// que este teste mede são os SPANS; a reserva continua dimensionada para `maxAttempts`, e uma
// recusa não gasta chamadas ao modelo a mais do que as que o tecto já admitia.)
func TestAOS415_TentativaRecusadaContinuaAAbrirSpanPorTentativa(t *testing.T) {
	dec := &decompositorQueRegista{}
	tracer := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	p, req := plannerDeTeste(t, dec,
		planner.WithValidator(&validadorQueRecusa{recusaAte: 1}),
		planner.WithTracer(tracer))

	if _, err := p.Decompose(context.Background(), req); err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if spans := tracer.SpansByOperation(otelgenai.OpChat); len(spans) != 2 {
		t.Fatalf("cada tentativa abre um span chat; queria 2, obtive %d", len(spans))
	}
}

// decompositorMisto: a 1.ª tentativa produz documento (que o validador recusa) e as seguintes
// falham no decode. O desfecho do planeamento NÃO pode depender de qual foi a ÚLTIMA falha.
type decompositorMisto struct {
	calls   atomic.Int64
	recusas []*planner.Rejection
}

func (d *decompositorMisto) Decompose(_ context.Context, in planner.DecomposeInput) (plan.PlanDocument, error) {
	n := d.calls.Add(1)
	d.recusas = append(d.recusas, in.Rejection)
	if n == 1 {
		return validDoc(), nil
	}
	return plan.PlanDocument{}, errors.New("decompose falhou (teste)")
}

// FALHA-ANTES (achado da revisão): recusa seguida de decode falhado devolvia ErrDecomposition, o
// chamador não largava a posse, e a invocação seguinte saía com 3 — o sintoma que este ticket
// fecha, a aparecer de forma intermitente.
func TestAOS415_RecusaSeguidaDeFalhaDeDecodeContinuaARecusa(t *testing.T) {
	dec := &decompositorMisto{}
	p, req := plannerDeTeste(t, dec,
		planner.WithValidator(&validadorQueRecusa{recusaAte: 99}),
		planner.WithMaxAttempts(3))

	_, err := p.Decompose(context.Background(), req)
	if !errors.Is(err, planner.ErrPlanRejected) {
		t.Fatalf("o desfecho tinha de ser ErrPlanRejected (houve recusa), veio %v", err)
	}
	// E a recusa VELHA não é reapresentada depois de uma tentativa que nem produziu documento.
	if len(dec.recusas) != 3 {
		t.Fatalf("esperava 3 tentativas, houve %d", len(dec.recusas))
	}
	if dec.recusas[1] == nil {
		t.Fatal("a 2.ª tentativa tinha de trazer a recusa da 1.ª")
	}
	if dec.recusas[2] != nil {
		t.Fatalf("a 3.ª não pode trazer a recusa da 1.ª: o documento 'anterior' nem existiu (%+v)", dec.recusas[2])
	}
}
