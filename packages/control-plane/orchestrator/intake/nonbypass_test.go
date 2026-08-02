package intake

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// recordingGate é um [SpawnGate] de teste: conta as consultas e devolve uma decisão
// fixa (opcionalmente um erro). Permite afirmar que o guard consultou SEMPRE o gate.
type recordingGate struct {
	calls  atomic.Int64
	last   SpawnAttempt
	decide GateDecision
	err    error
}

func (g *recordingGate) GateSpawn(_ context.Context, a SpawnAttempt) (GateDecision, error) {
	g.calls.Add(1)
	g.last = a
	return g.decide, g.err
}

// levelGate é um gate de referência de teste que modela a política de arranque L0:
// até L3, o plano just-in-time exige aprovação humana (não aprovado); L4/L5
// auto-aprovam. Serve para exercer a reentrada "ao nível do chamador".
type levelGate struct{ calls atomic.Int64 }

func (g *levelGate) GateSpawn(_ context.Context, a SpawnAttempt) (GateDecision, error) {
	g.calls.Add(1)
	if a.CallerLevel <= L3 {
		return GateDecision{Approved: false, RequiresHuman: true, Reason: "jit_plan_needs_human"}, nil
	}
	return GateDecision{Approved: true, Reason: "within_envelope"}, nil
}

// TestNonBypass_SimpleRunDelegationIsGated é a invariante central: um run
// classificado SIMPLE que tenta delegar dispara o plano just-in-time GATED — o
// spawn NÃO ocorre a L0.
//
// Falha-antes: se [DelegationGuard.Delegate] tivesse um ramo que, para
// OriginClassification==simple, chamasse spawn directamente (o bypass), então:
// (a) o gate não seria consultado (calls==0) e (b) spawnRan viraria true. Ambos os
// asserts abaixo apanham esse bypass.
func TestNonBypass_SimpleRunDelegationIsGated(t *testing.T) {
	t.Parallel()

	gate := &levelGate{}
	guard, err := NewDelegationGuard(gate)
	if err != nil {
		t.Fatalf("NewDelegationGuard: %v", err)
	}

	var spawnRan atomic.Bool
	spawn := func(context.Context) (ChildHandle, error) {
		spawnRan.Store(true)
		return "child-handle", nil
	}

	attempt := SpawnAttempt{
		PlanID:               "p1",
		ParentNodeID:         "n0",
		ChildRole:            "worker",
		CallerLevel:          L0, // planeador nasce a L0: aprovação humana obrigatória
		OriginClassification: plannerevents.ClassificationSimple,
	}

	h, err := guard.Delegate(context.Background(), attempt, spawn)

	if !errors.Is(err, ErrSpawnGated) {
		t.Fatalf("delegação de run simples a L0 tem de ser gated (ErrSpawnGated), obtido err=%v h=%v", err, h)
	}
	if spawnRan.Load() {
		t.Fatal("BYPASS: o spawn correu apesar de o gate não ter aprovado")
	}
	if gate.calls.Load() != 1 {
		t.Fatalf("o gate por-spawn tem de ser consultado exactamente 1 vez, obtido %d", gate.calls.Load())
	}
}

// TestNonBypass_GateConsultedRegardlessOfClassification: nem `simple` nem `meta`
// saltam a consulta. Falha-antes: um ramo que curto-circuitasse o gate para
// qualquer classificação deixaria calls==0 nesse caso.
func TestNonBypass_GateConsultedRegardlessOfClassification(t *testing.T) {
	t.Parallel()

	for _, cls := range []plannerevents.Classification{
		plannerevents.ClassificationSimple,
		plannerevents.ClassificationMeta,
	} {
		gate := &recordingGate{decide: GateDecision{Approved: false}}
		guard, err := NewDelegationGuard(gate)
		if err != nil {
			t.Fatalf("NewDelegationGuard: %v", err)
		}
		_, _ = guard.Delegate(context.Background(),
			SpawnAttempt{PlanID: "p", CallerLevel: L2, OriginClassification: cls},
			func(context.Context) (ChildHandle, error) { return nil, nil })
		if gate.calls.Load() != 1 {
			t.Fatalf("classificação %q: gate consultado %d vezes, esperado 1", cls, gate.calls.Load())
		}
		if gate.last.OriginClassification != cls {
			t.Fatalf("o gate recebeu a classificação %q, esperado %q", gate.last.OriginClassification, cls)
		}
	}
}

// TestNonBypass_ApprovedSpawnProceeds: com aprovação (L4, dentro do envelope), o
// spawn corre e o handle atravessa. Falha-antes: se o guard barrasse mesmo com
// aprovação, o handle não chegaria.
func TestNonBypass_ApprovedSpawnProceeds(t *testing.T) {
	t.Parallel()

	gate := &levelGate{}
	guard, _ := NewDelegationGuard(gate)

	var spawnRan atomic.Bool
	h, err := guard.Delegate(context.Background(),
		SpawnAttempt{PlanID: "p", CallerLevel: L4, OriginClassification: plannerevents.ClassificationSimple},
		func(context.Context) (ChildHandle, error) { spawnRan.Store(true); return "ok", nil })
	if err != nil {
		t.Fatalf("delegação aprovada não devia falhar: %v", err)
	}
	if !spawnRan.Load() {
		t.Fatal("o spawn devia ter corrido após aprovação")
	}
	if h != ChildHandle("ok") {
		t.Fatalf("handle esperado \"ok\", obtido %v", h)
	}
}

// TestNonBypass_GateErrorFailClosed: um erro do gate barra o spawn (fail-closed).
// Falha-antes: se o erro do gate fosse ignorado, spawn correria.
func TestNonBypass_GateErrorFailClosed(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("gate indisponível")
	gate := &recordingGate{err: sentinel}
	guard, _ := NewDelegationGuard(gate)

	var spawnRan atomic.Bool
	_, err := guard.Delegate(context.Background(),
		SpawnAttempt{PlanID: "p", CallerLevel: L5},
		func(context.Context) (ChildHandle, error) { spawnRan.Store(true); return nil, nil })
	if !errors.Is(err, sentinel) {
		t.Fatalf("erro do gate tem de propagar fail-closed, obtido %v", err)
	}
	if spawnRan.Load() {
		t.Fatal("BYPASS: spawn correu apesar do erro do gate")
	}
}

// TestNonBypass_InvalidCallerLevelFailClosed: um CallerLevel fora de L0–L5 é lixo e
// tem de ser recusado ANTES do gate — nem o gate é consultado, nem o spawn corre.
//
// Falha-antes: sem a validação de nível em [DelegationGuard.Delegate], um nível
// numérico grande (Level(200)) seria passado ao gate; um gate de envelope como
// [levelGate] avalia `CallerLevel <= L3` (200 <= 3 == false) e AUTO-APROVARIA — o
// spawn correria (spawnRan==true) e o handle atravessaria. Os asserts abaixo (erro
// ErrInvalidCallerLevel, gate.calls==0, spawn não corrido) apanham esse fail-open.
func TestNonBypass_InvalidCallerLevelFailClosed(t *testing.T) {
	t.Parallel()

	gate := &levelGate{} // envelope: L4/L5 auto-aprovam — um 200 fingiria estar dentro
	guard, err := NewDelegationGuard(gate)
	if err != nil {
		t.Fatalf("NewDelegationGuard: %v", err)
	}

	var spawnRan atomic.Bool
	h, err := guard.Delegate(context.Background(),
		SpawnAttempt{PlanID: "p", CallerLevel: Level(200), OriginClassification: plannerevents.ClassificationSimple},
		func(context.Context) (ChildHandle, error) { spawnRan.Store(true); return "leaked", nil })

	if !errors.Is(err, ErrInvalidCallerLevel) {
		t.Fatalf("nível inválido tem de dar ErrInvalidCallerLevel, obtido err=%v h=%v", err, h)
	}
	if spawnRan.Load() {
		t.Fatal("BYPASS: spawn correu com um CallerLevel-lixo")
	}
	if gate.calls.Load() != 0 {
		t.Fatalf("um nível inválido NÃO pode chegar ao gate, mas foi consultado %d vez(es)", gate.calls.Load())
	}
	if h != nil {
		t.Fatalf("nenhum handle deve atravessar com nível inválido, obtido %v", h)
	}
}

// TestNonBypass_NilGateRejected: construir um guard sem gate é fail-closed — um
// guard sem fronteira seria o próprio bypass. Falha-antes: se NewDelegationGuard
// aceitasse gate nil, devolveria um guard que ou faz panic ou salta o gate.
func TestNonBypass_NilGateRejected(t *testing.T) {
	t.Parallel()

	if _, err := NewDelegationGuard(nil); !errors.Is(err, ErrNilGate) {
		t.Fatalf("gate nil tem de ser recusado com ErrNilGate, obtido %v", err)
	}
}

// TestNonBypass_NilSpawnRejected: sem efeito de spawn a proteger, o guard recusa em
// vez de tratar nil como no-op silencioso.
func TestNonBypass_NilSpawnRejected(t *testing.T) {
	t.Parallel()

	gate := &recordingGate{decide: GateDecision{Approved: true}}
	guard, _ := NewDelegationGuard(gate)
	if _, err := guard.Delegate(context.Background(), SpawnAttempt{PlanID: "p"}, nil); !errors.Is(err, ErrNilSpawn) {
		t.Fatalf("spawn nil tem de ser recusado com ErrNilSpawn, obtido %v", err)
	}
}
