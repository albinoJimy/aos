package controlsurface_test

import (
	"context"
	"testing"
	"time"

	controlsurface "github.com/aos-ref/control-plane/governance/control-surface"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// Identidades de teste: um emissor REGISTADO (control-plane legítimo) e um NÃO
// registado (o molde de conteúdo untrusted que tenta forjar um sinal).
const (
	testEmitter    = "operator-42"
	testSecret     = "s3cr3t-shared-key"
	unknownEmitter = "untrusted-tool-output"
	testRunID      = "run-119-abc"
)

var fixedInstant = time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)

func fixedClock() control.ClockFunc {
	return control.ClockFunc(func() time.Time { return fixedInstant })
}

func newStore(t testing.TB) *eventstore.Store {
	t.Helper()
	st, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// authWith devolve um autenticador HMAC com o emissor de teste registado (o emissor
// desconhecido fica DELIBERADAMENTE por registar — default-deny).
func authWith(t testing.TB) *control.HMACAuthenticator {
	t.Helper()
	a := control.NewHMACAuthenticator()
	a.Register(testEmitter, []byte(testSecret))
	return a
}

// newChannel constrói um SteerChannel de AOS-023 com relógio determinístico e um
// tracer opcional (nil ⇒ Noop). O canal é a peça que a superfície COMPÕE.
func newChannel(t testing.TB, st control.EventStore, a control.Authenticator, tr agentruntime.Tracer) *control.SteerChannel {
	t.Helper()
	opts := []control.ChannelOption{control.WithClock(fixedClock())}
	if tr != nil {
		opts = append(opts, control.WithTracer(tr))
	}
	ch, err := control.NewChannel(st, a, opts...)
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	return ch
}

// runningMachine constrói uma máquina durável de AOS-017 já em running (claim
// ready→running com fencing token válido) e devolve o binding da superfície.
func runningMachine(t testing.TB, st state.EventStore, runID string) (*state.Machine, controlsurface.RunBinding) {
	t.Helper()
	m, err := state.NewMachine(st, runID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if err := m.Transition(context.Background(), state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("claim ready→running: %v", err)
	}
	return m, controlsurface.NewRunBinding(m)
}

// sign devolve o Emitter autenticado (assinatura HMAC) para um sinal — o material que
// o canal verifica. Reusa o helper Sign do HMACAuthenticator de AOS-023.
func sign(t testing.TB, a *control.HMACAuthenticator, runID string, kind control.SignalKind, payload []byte, emitterID string) []byte {
	t.Helper()
	em, err := a.Sign(runID, kind, payload, emitterID)
	if err != nil {
		t.Fatalf("Sign(%s): %v", kind, err)
	}
	return em.Signature
}

// newSurface constrói a superfície sobre um canal, com um tracer opcional.
func newSurface(t testing.TB, ch *control.SteerChannel, tr agentruntime.Tracer) *controlsurface.ControlSurface {
	t.Helper()
	opts := []controlsurface.SurfaceOption{}
	if tr != nil {
		opts = append(opts, controlsurface.WithTracer(tr))
	}
	s, err := controlsurface.NewControlSurface(ch, opts...)
	if err != nil {
		t.Fatalf("NewControlSurface: %v", err)
	}
	return s
}
