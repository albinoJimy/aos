package controlsurface_test

import (
	"context"
	"testing"

	controlsurface "github.com/aos-ref/control-plane/governance/control-surface"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/control"
)

// TestSpan_EachActionEmitsInteractionSpan — AC6: cada acção de controlo emite um span
// de interacção ligado ao trace do run, com QUEM (emitter), QUE SINAL e de que CANAL.
// Reusa a operação control.OpControlSignal.
//
// O tracer da SUPERFÍCIE é o RecordingTracer; o do CANAL é Noop — assim
// SpansByOperation(control.OpControlSignal) devolve exactamente os spans de interacção
// da superfície (um por Dispatch), sem os spans internos do canal.
func TestSpan_EachActionEmitsInteractionSpan(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	auth := authWith(t)
	ch := newChannel(t, st, auth, nil) // canal com tracer Noop
	rec := &agentruntime.RecordingTracer{}
	surface := newSurface(t, ch, rec) // superfície com RecordingTracer
	m, binding := runningMachine(t, st, testRunID)

	// (1) interrupt
	pauseSig := sign(t, auth, testRunID, control.SignalPause, nil, testEmitter)
	if _, err := surface.Dispatch(ctx, controlsurface.NewInterrupt(testRunID, controlsurface.ChannelDesktop, testEmitter, pauseSig), binding); err != nil {
		t.Fatalf("Dispatch(interrupt): %v", err)
	}
	// (2) steer  — mantém o run pausável; não precisa de estar pausado para steer.
	steerCorr := []byte("mantém o rumo")
	steerSig := sign(t, auth, testRunID, control.SignalSteer, steerCorr, testEmitter)
	if _, err := surface.Dispatch(ctx, controlsurface.NewSteer(testRunID, controlsurface.ChannelChatbot, testEmitter, steerSig, steerCorr), binding); err != nil {
		t.Fatalf("Dispatch(steer): %v", err)
	}
	// (3) state — leitura pura, também emite span de interacção.
	if _, err := surface.Dispatch(ctx, controlsurface.NewStateQuery(testRunID, controlsurface.ChannelAPI), binding); err != nil {
		t.Fatalf("Dispatch(state): %v", err)
	}
	// (4) resume — pausa primeiro (fim do turno), depois retoma.
	if _, err := ch.GracefulPause(ctx, testRunID, binding.Gate); err != nil {
		t.Fatalf("GracefulPause: %v", err)
	}
	_ = m
	resumeSig := sign(t, auth, testRunID, control.SignalResume, nil, testEmitter)
	if _, err := surface.Dispatch(ctx, controlsurface.NewResume(testRunID, controlsurface.ChannelDesktop, testEmitter, resumeSig), binding); err != nil {
		t.Fatalf("Dispatch(resume): %v", err)
	}

	spans := rec.SpansByOperation(control.OpControlSignal)
	if len(spans) != 4 {
		t.Fatalf("nº de spans de interacção=%d, quero 4 (interrupt, steer, state, resume)", len(spans))
	}

	// Cada span carrega run_id, o sinal correcto, o emissor e o canal.
	wantSignals := []struct {
		signal  string
		channel string
	}{
		{string(control.SignalPause), string(controlsurface.ChannelDesktop)},
		{string(control.SignalSteer), string(controlsurface.ChannelChatbot)},
		{"state", string(controlsurface.ChannelAPI)},
		{string(control.SignalResume), string(controlsurface.ChannelDesktop)},
	}
	for i, sp := range spans {
		if !sp.Ended {
			t.Errorf("span %d não foi fechado", i)
		}
		if got := sp.Attributes[agentruntime.AttrRunID]; got != testRunID {
			t.Errorf("span %d run_id=%v, quero %q", i, got, testRunID)
		}
		if got := sp.Attributes[control.AttrControlSignal]; got != wantSignals[i].signal {
			t.Errorf("span %d signal=%v, quero %q", i, got, wantSignals[i].signal)
		}
		if got := sp.Attributes[controlsurface.AttrControlChannel]; got != wantSignals[i].channel {
			t.Errorf("span %d channel=%v, quero %q", i, got, wantSignals[i].channel)
		}
	}

	// As acções mutantes carregam a identidade do emissor (não-repúdio no trace); a
	// query de estado não exige emissor.
	for i := 0; i < 3; i++ {
		if i == 2 {
			continue // state query — sem emissor obrigatório
		}
		if got := spans[i].Attributes[control.AttrControlEmitter]; got != testEmitter {
			t.Errorf("span %d emitter=%v, quero %q", i, got, testEmitter)
		}
		if got := spans[i].Attributes[controlsurface.AttrControlActor]; got != testEmitter {
			t.Errorf("span %d actor=%v, quero %q", i, got, testEmitter)
		}
	}
}

// TestSpan_LinkedToRunTrace — AC6: o span de interacção herda o trace do run quando o
// ctx já traz um SpanContext (liga-se ao trace do run, não abre um trace órfão).
func TestSpan_LinkedToRunTrace(t *testing.T) {
	st := newStore(t)
	auth := authWith(t)
	ch := newChannel(t, st, auth, nil)
	rec := &agentruntime.RecordingTracer{}
	surface := newSurface(t, ch, rec)
	_, binding := runningMachine(t, st, testRunID)

	// Semeia o ctx com o trace do run (a raiz de propagação — o span do invoke_agent).
	ctxRoot, rootSpan := rec.StartSpan(context.Background(), agentruntime.OpInvokeAgent)
	rootTrace := rootSpan.SpanContext().TraceID
	rootSpan.End()

	pauseSig := sign(t, auth, testRunID, control.SignalPause, nil, testEmitter)
	if _, err := surface.Dispatch(ctxRoot, controlsurface.NewInterrupt(testRunID, controlsurface.ChannelDesktop, testEmitter, pauseSig), binding); err != nil {
		t.Fatalf("Dispatch(interrupt): %v", err)
	}

	spans := rec.SpansByOperation(control.OpControlSignal)
	if len(spans) != 1 {
		t.Fatalf("nº de spans de controlo=%d, quero 1", len(spans))
	}
	if spans[0].SpanContext.TraceID != rootTrace {
		t.Fatalf("span de controlo trace=%x, quero ligado ao trace do run %x", spans[0].SpanContext.TraceID, rootTrace)
	}
}
