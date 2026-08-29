package control_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// ---------------------------------------------------------------------------
// GRACEFUL PAUSE — pausa no FIM do turno, NUNCA a meio de uma activity.
// ---------------------------------------------------------------------------

// TestGracefulPause_NeverMidActivity é o teste-chave de AOS-023: um sinal de pause
// emitido a MEIO de um turno (durante o despacho de activities) só se materializa na
// fronteira de FIM DE TURNO — todas as activities do turno corrente confirmam primeiro,
// sem efeitos parciais.
func TestGracefulPause_NeverMidActivity(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	ch := newChannel(t, st, a)
	runID := "run-barrier"
	m, gate := runningMachine(t, st, runID)

	const activitiesPerTurn = 3
	// O sinal chega a meio do turno 1 (antes da activity 1, com a activity 0 já feita).
	const pauseAtTurn, pauseAtActivity = 1, 1

	var effects []string // rasto ordenado de TODAS as activities confirmadas
	paused := false

	for turn := 1; turn <= 4 && !paused; turn++ {
		for act := 0; act < activitiesPerTurn; act++ {
			// Sinal out-of-band injectado a meio do turno.
			if turn == pauseAtTurn && act == pauseAtActivity {
				if err := ch.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
					t.Fatal(err)
				}
				// INVARIANTE: a máquina AINDA está running — a pausa não interrompeu
				// o turno a meio de uma activity.
				if m.Current() != state.Running {
					t.Fatalf("pausa materializou-se a meio do turno: estado %s", m.Current())
				}
			}
			// A activity confirma o seu efeito (nunca deixada a meio).
			effects = append(effects, fmt.Sprintf("t%d-a%d", turn, act))
		}

		// FRONTEIRA DE FIM DE TURNO — o único ponto onde a pausa graciosa se materializa.
		did, err := ch.GracefulPause(ctx, runID, gate)
		if err != nil {
			t.Fatalf("GracefulPause: %v", err)
		}
		if did {
			paused = true
		}
	}

	// O turno 1 completou as TRÊS activities (nenhuma deixada a meio); o turno 2 NUNCA
	// começou (a pausa tomou efeito no fim do turno 1).
	want := []string{"t1-a0", "t1-a1", "t1-a2"}
	if !reflect.DeepEqual(effects, want) {
		t.Fatalf("effects = %v, quer %v (pausa a meio de uma activity ou turno parcial)", effects, want)
	}
	if m.Current() != state.Paused {
		t.Fatalf("estado final = %s, quer paused", m.Current())
	}
	if !paused {
		t.Fatal("o loop não pausou")
	}
}

// TestGracefulPause_NoPendingIsNoop garante que, sem pausa pendente, a barreira não
// transita nada (o turno seguinte prossegue).
func TestGracefulPause_NoPendingIsNoop(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	ch := newChannel(t, st, a)
	runID := "run-nopause"
	m, gate := runningMachine(t, st, runID)

	did, err := ch.GracefulPause(ctx, runID, gate)
	if err != nil {
		t.Fatal(err)
	}
	if did {
		t.Fatal("GracefulPause pausou sem sinal pendente")
	}
	if m.Current() != state.Running {
		t.Fatalf("estado = %s, quer running", m.Current())
	}
}

func TestGracefulPause_NilGate(t *testing.T) {
	ch := newChannel(t, newStore(t), authWith(t))
	if _, err := ch.GracefulPause(context.Background(), "run", nil); !errors.Is(err, control.ErrNilGate) {
		t.Fatalf("err = %v, quer ErrNilGate", err)
	}
}

// ---------------------------------------------------------------------------
// RESUME — aplica a correcção como instrução confiável; transita paused→running.
// ---------------------------------------------------------------------------

// TestResume_AppliesCorrection prova o critério central: a correcção injectada é
// aplicada na retoma, com a identidade do emissor registada.
func TestResume_AppliesCorrection(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	ch := newChannel(t, st, a)
	runID := "run-resume"
	m, gate := runningMachine(t, st, runID)

	// pause → graceful pause → steer → resume.
	if err := ch.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.GracefulPause(ctx, runID, gate); err != nil {
		t.Fatal(err)
	}
	correction := []byte("responde em PT-PT e cita as fontes")
	if err := ch.Steer(ctx, runID, correction, signed(t, a, runID, control.SignalSteer, correction)); err != nil {
		t.Fatal(err)
	}

	corr, err := ch.Resume(ctx, runID, signed(t, a, runID, control.SignalResume, nil), gate)
	if err != nil {
		t.Fatal(err)
	}
	if !corr.Present {
		t.Fatal("retoma sem correcção — devia aplicar a steer")
	}
	if string(corr.Value) != string(correction) {
		t.Fatalf("correcção = %q, quer %q", corr.Value, correction)
	}
	if corr.EmitterID != testEmitter {
		t.Fatalf("emissor da correcção = %q, quer %q (não-repúdio)", corr.EmitterID, testEmitter)
	}
	if m.Current() != state.Running {
		t.Fatalf("estado = %s, quer running", m.Current())
	}

	// A correcção materializa-se como segmento de tail TRUSTED e distinto.
	seg := corr.TailSegment()
	if seg.Kind != control.TailSteer {
		t.Fatalf("tail kind = %q, quer steer", seg.Kind)
	}
	// AssemblyVersion 1.3.0: o rotulo trusted migrou do CORPO para a linha de delimitacao.
	trusted := false
	for _, m := range seg.Meta {
		if m.Key == "taint" && m.Value == agentruntime.TaintTrusted {
			trusted = true
		}
	}
	if !trusted {
		t.Fatalf("segmento de tail não marca trusted no delimitador: %+v", seg.Meta)
	}
	if strings.Contains(string(seg.Content), "taint=") {
		t.Fatalf("o rotulo TRUSTED voltou ao CORPO: %q", seg.Content)
	}
	if !strings.Contains(string(seg.Content), string(correction)) {
		t.Fatal("segmento de tail não contém a correcção")
	}
}

// TestResume_WithoutCorrection cobre a retoma limpa (só pause→resume, sem steer).
func TestResume_WithoutCorrection(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	ch := newChannel(t, st, a)
	runID := "run-clean-resume"
	m, gate := runningMachine(t, st, runID)

	if err := ch.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.GracefulPause(ctx, runID, gate); err != nil {
		t.Fatal(err)
	}
	corr, err := ch.Resume(ctx, runID, signed(t, a, runID, control.SignalResume, nil), gate)
	if err != nil {
		t.Fatal(err)
	}
	if corr.Present {
		t.Fatalf("retoma limpa devolveu correcção: %+v", corr)
	}
	if m.Current() != state.Running {
		t.Fatalf("estado = %s, quer running", m.Current())
	}
}

// TestResume_Validation cobre os caminhos de rejeição da retoma.
func TestResume_Validation(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	ch := newChannel(t, st, a)
	runID := "run-resume-val"
	_, gate := runningMachine(t, st, runID)
	valid := signed(t, a, runID, control.SignalResume, nil)

	tests := []struct {
		name    string
		runID   string
		emitter control.Emitter
		gate    control.StateGate
		want    error
	}{
		{"run_id vazio", "", valid, gate, control.ErrEmptyRunID},
		{"emitter sem id", runID, control.Emitter{Signature: []byte("x")}, gate, control.ErrEmptyEmitterID},
		{"gate nil", runID, valid, nil, control.ErrNilGate},
		{"não autenticado", runID, control.Emitter{ID: testEmitter, Signature: []byte("forjada")}, gate, control.ErrUnauthenticated},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ch.Resume(ctx, tc.runID, tc.emitter, tc.gate); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, quer %v", err, tc.want)
			}
		})
	}
}

// TestResume_RejectedWhenNotPaused prova que a retoma respeita a tabela de AOS-017:
// retomar um run que NÃO está paused é recusado pela máquina, e a correcção pendente
// fica intacta.
func TestResume_RejectedWhenNotPaused(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	ch := newChannel(t, st, a)
	runID := "run-notpaused"
	m, gate := runningMachine(t, st, runID) // fica em running, NUNCA pausa

	correction := []byte("correcção que não deve perder-se")
	if err := ch.Steer(ctx, runID, correction, signed(t, a, runID, control.SignalSteer, correction)); err != nil {
		t.Fatal(err)
	}
	// running→running não é uma aresta válida ⇒ a retoma é recusada.
	if _, err := ch.Resume(ctx, runID, signed(t, a, runID, control.SignalResume, nil), gate); !errors.Is(err, state.ErrInvalidTransition) {
		t.Fatalf("err = %v, quer ErrInvalidTransition", err)
	}
	if m.Current() != state.Running {
		t.Fatalf("estado = %s, quer running (inalterado)", m.Current())
	}
	// A correcção NÃO foi consumida por uma retoma falhada.
	got, ok := ch.PendingCorrection(runID)
	if !ok || string(got) != string(correction) {
		t.Fatalf("correcção pendente = %q (ok=%v), quer intacta %q", got, ok, correction)
	}
}

// ---------------------------------------------------------------------------
// MachineGate — o adaptador para a máquina de AOS-017.
// ---------------------------------------------------------------------------

func TestMachineGate_PauseResume(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	runID := "run-gate"
	m, gate := runningMachine(t, st, runID)

	if err := gate.Pause(ctx, control.ReasonGracefulPause); err != nil {
		t.Fatalf("gate.Pause: %v", err)
	}
	if m.Current() != state.Paused {
		t.Fatalf("estado = %s, quer paused", m.Current())
	}
	if err := gate.Resume(ctx, control.ReasonSteerResume); err != nil {
		t.Fatalf("gate.Resume: %v", err)
	}
	if m.Current() != state.Running {
		t.Fatalf("estado = %s, quer running", m.Current())
	}
	// NewMachineGate expõe a máquina subjacente.
	if control.NewMachineGate(m).Machine != m {
		t.Fatal("NewMachineGate não referencia a máquina dada")
	}
}

// ---------------------------------------------------------------------------
// Observabilidade — cada sinal aceite emite um span com kind + emissor.
// ---------------------------------------------------------------------------

func TestObservability_SpansPerSignal(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	tracer := &agentruntime.RecordingTracer{}
	ch, err := control.NewChannel(st, a, control.WithClock(fixedClock()), control.WithTracer(tracer))
	if err != nil {
		t.Fatal(err)
	}
	runID := "run-obs"
	_, gate := runningMachine(t, st, runID)

	if err := ch.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.GracefulPause(ctx, runID, gate); err != nil {
		t.Fatal(err)
	}
	corr := []byte("c")
	if err := ch.Steer(ctx, runID, corr, signed(t, a, runID, control.SignalSteer, corr)); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.Resume(ctx, runID, signed(t, a, runID, control.SignalResume, nil), gate); err != nil {
		t.Fatal(err)
	}

	spans := tracer.SpansByOperation(control.OpControlSignal)
	if len(spans) != 3 {
		t.Fatalf("spans de controlo = %d, quer 3 (pause/steer/resume)", len(spans))
	}
	for _, s := range spans {
		if s.Attributes[control.AttrControlEmitter] != testEmitter {
			t.Errorf("span sem emissor: %+v", s.Attributes)
		}
		if !s.Ended {
			t.Error("span não fechado")
		}
	}
}
