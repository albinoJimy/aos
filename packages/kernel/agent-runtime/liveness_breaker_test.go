package agentruntime

import (
	"context"
	"errors"
	"testing"
)

// fakeBreaker é um [LivenessBreaker] determinista: dispara no turno tripOn (0 = nunca).
type fakeBreaker struct {
	tripOn   int
	target   string
	err      error
	observed []int // turnos em que foi consultado
}

func (f *fakeBreaker) Observe(_ context.Context, _ string, turn int) (bool, string, error) {
	f.observed = append(f.observed, turn)
	if f.err != nil {
		return false, "", f.err
	}
	if f.tripOn > 0 && turn >= f.tripOn {
		return true, f.target, nil
	}
	return false, "", nil
}

// loopingModel pede SEMPRE a mesma tool (nunca conclui) — o run patológico que antes só
// podia acabar por esgotar MaxTurns.
func loopingModel() ModelClient {
	return ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("x")}}}, nil
	})
}

// TestBreaker_TrocaMaxTurnsPorVeredicto é o ponto do AOS-080/081: um run que não progride
// deixa de morrer com [ErrMaxTurnsExceeded] (uma paragem defensiva que não diz PORQUÊ) e
// passa a parar com um VEREDICTO útil no [Result].
func TestBreaker_TrocaMaxTurnsPorVeredicto(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	fb := &fakeBreaker{tripOn: 3, target: "paused"}
	rt := New(loopingModel(), h.rm, h.recorder, WithLivenessBreaker(fb))

	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("um trip do breaker NÃO é erro do loop (é um desfecho): %v", err)
	}
	if !res.Tripped {
		t.Fatalf("o run devia ter parado por trip do breaker; res=%+v", res)
	}
	if res.BreakerTarget != "paused" {
		t.Fatalf("BreakerTarget=%q, esperava \"paused\"", res.BreakerTarget)
	}
	if res.Turns != 3 {
		t.Fatalf("o run devia ter parado no turno 3 (não esgotar MaxTurns), parou em %d", res.Turns)
	}
	if res.Terminated {
		t.Fatalf("um trip não é uma terminação por resposta final")
	}
}

// TestBreaker_SemBreakerComportamentoInalterado é a guarda de retro-compatibilidade: sem
// [WithLivenessBreaker] o loop patológico continua a acabar exactamente como em AOS-013.
func TestBreaker_SemBreakerComportamentoInalterado(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	rt := New(loopingModel(), h.rm, h.recorder)
	res, err := rt.Run(context.Background(), sampleGoal())
	if !errors.Is(err, ErrMaxTurnsExceeded) {
		t.Fatalf("sem breaker, o loop devia esgotar MaxTurns como antes; err=%v", err)
	}
	if res.Tripped || res.BreakerTarget != "" {
		t.Fatalf("sem breaker não devia haver veredicto de trip; res=%+v", res)
	}
}

// TestBreaker_ErroEhFatal sela o fail-closed: se a transição durável do disparo falhar, o
// erro SOBE (não é engolido) — continuar deixaria o run a queimar recursos com o
// disjuntor cego.
func TestBreaker_ErroEhFatal(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	boom := errors.New("transição durável falhou")
	rt := New(loopingModel(), h.rm, h.recorder, WithLivenessBreaker(&fakeBreaker{err: boom}))
	if _, err := rt.Run(context.Background(), sampleGoal()); !errors.Is(err, boom) {
		t.Fatalf("um erro do breaker devia ser FATAL para o run; err=%v", err)
	}
}

// TestBreaker_NaoConsultadoEmRunTerminado assevera a ordem na fronteira de fim-de-turno: um
// run que CONCLUI (resposta final) retorna ANTES do disjuntor — nunca se dispara um
// disjuntor sobre um run já terminado.
func TestBreaker_NaoConsultadoEmRunTerminado(t *testing.T) {
	h := newHarness(t, nil)
	fb := &fakeBreaker{tripOn: 1, target: "paused"} // dispararia logo no 1.º turno
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		return ModelResponse{Text: "pronto", Final: true}, nil
	})
	rt := New(model, h.rm, h.recorder, WithLivenessBreaker(fb))
	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Terminated || res.Tripped {
		t.Fatalf("um run com resposta final termina normalmente, sem consultar o disjuntor; res=%+v", res)
	}
	if len(fb.observed) != 0 {
		t.Fatalf("o disjuntor NÃO devia ser consultado num run terminado; observed=%v", fb.observed)
	}
}
