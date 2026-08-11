package agentruntime

// AOS-262 — o GANCHO do observador de burn-down na fronteira de fim-de-turno.
//
// O que se sela aqui é o SEAM, não a política: que o loop consulta o observador uma vez por
// turno NÃO-terminal, que um erro é fatal (o run não continua com o burn-down cego), e que
// sem observador o comportamento de AOS-013 é byte-idêntico.

import (
	"context"
	"errors"
	"testing"
)

// fakeProgress é um [ProgressObserver] determinista que regista os turnos observados.
type fakeProgress struct {
	observed []int
	err      error
	errOn    int // turno em que passa a devolver err (0 = sempre, se err != nil)
}

func (f *fakeProgress) ObserveProgress(_ context.Context, _ string, turn int) error {
	f.observed = append(f.observed, turn)
	if f.err != nil && (f.errOn == 0 || turn >= f.errOn) {
		return f.err
	}
	return nil
}

// TestProgressObserver_ConsultadoNaFronteiraDeFimDeTurno: o observador vê CADA turno
// não-terminal, e NÃO vê o turno que termina o run (nesse já não há orçamento a queimar).
func TestProgressObserver_ConsultadoNaFronteiraDeFimDeTurno(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Dois turnos com tool call e o terceiro final.
	n := 0
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		n++
		if n >= 3 {
			return ModelResponse{Text: "pronto", Final: true}, nil
		}
		return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("x")}}}, nil
	})
	fp := &fakeProgress{}
	rt := New(model, h.rm, h.recorder, WithProgressObserver(fp))

	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Terminated || res.Turns != 3 {
		t.Fatalf("res=%+v", res)
	}
	if len(fp.observed) != 2 || fp.observed[0] != 1 || fp.observed[1] != 2 {
		t.Fatalf("o observador devia ver os turnos 1 e 2 (o 3.º é terminal e retorna antes), got %v", fp.observed)
	}
}

// TestProgressObserver_ErroEFatal: sem dados o burn-down é cego, e um agente autónomo não
// continua a correr com o aviso prometido e desligado. O erro sobe.
func TestProgressObserver_ErroEFatal(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	semFonte := errors.New("sem ledger de turnos para o run")
	fp := &fakeProgress{err: semFonte}
	rt := New(loopingModel(), h.rm, h.recorder, WithProgressObserver(fp))

	res, err := rt.Run(context.Background(), sampleGoal())
	if !errors.Is(err, semFonte) {
		t.Fatalf("um erro do observador tem de ABORTAR o run, got err=%v res=%+v", err, res)
	}
	if len(fp.observed) != 1 {
		t.Fatalf("o run devia parar na PRIMEIRA falha, got %v", fp.observed)
	}
}

// TestProgressObserver_NaoDecide: o observador NÃO pode parar um run. Um observador que
// nunca falha deixa o loop seguir exactamente o seu curso — quem pára é o breaker/steer.
func TestProgressObserver_NaoDecide(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	fp := &fakeProgress{}
	rt := New(loopingModel(), h.rm, h.recorder, WithProgressObserver(fp))

	res, err := rt.Run(context.Background(), sampleGoal())
	if !errors.Is(err, ErrMaxTurnsExceeded) {
		t.Fatalf("o observador não decide nada — o run patológico continua a acabar por MaxTurns; err=%v", err)
	}
	if res.Tripped {
		t.Fatal("o observador NÃO pode produzir um veredicto de trip (isso é do disjuntor)")
	}
	// NÃO-VACUOSIDADE: o observador foi mesmo consultado em todos os turnos (senão o teste
	// provaria apenas que um observador inerte não faz nada).
	if len(fp.observed) != res.Turns {
		t.Fatalf("o observador devia ser consultado em cada um dos %d turnos, got %v", res.Turns, fp.observed)
	}
}

// TestProgressObserver_SemObservadorComportamentoInalterado: a retro-compatibilidade.
func TestProgressObserver_SemObservadorComportamentoInalterado(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	rt := New(loopingModel(), h.rm, h.recorder)
	if _, err := rt.Run(context.Background(), sampleGoal()); !errors.Is(err, ErrMaxTurnsExceeded) {
		t.Fatalf("sem observador o loop de AOS-013 é byte-idêntico; err=%v", err)
	}
	// Um nil explícito também é ignorado (a opção não pode instalar um observador vazio).
	rt2 := New(loopingModel(), h.rm, h.recorder, WithProgressObserver(nil))
	if rt2.progressObserver != nil {
		t.Fatal("WithProgressObserver(nil) não pode instalar nada")
	}
}
