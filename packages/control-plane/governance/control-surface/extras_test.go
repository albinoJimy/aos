package controlsurface_test

import (
	"context"
	"errors"
	"testing"
	"time"

	controlsurface "github.com/aos-ref/control-plane/governance/control-surface"
	"github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// TestSurface_ConstructorFailClosed — a superfície recusa-se a existir sem um canal.
func TestSurface_ConstructorFailClosed(t *testing.T) {
	if _, err := controlsurface.NewControlSurface(nil); !errors.Is(err, controlsurface.ErrNilChannel) {
		t.Fatalf("NewControlSurface(nil)=%v, quero ErrNilChannel", err)
	}
}

// TestSurface_VersionOption — a superfície aceita e reporta a versão configurada.
func TestSurface_VersionOption(t *testing.T) {
	st := newStore(t)
	ch := newChannel(t, st, authWith(t), nil)
	v := controlsurface.ControlSchemaVersion{Major: 1, Minor: 7, Patch: 2}
	s, err := controlsurface.NewControlSurface(ch, controlsurface.WithVersion(v))
	if err != nil {
		t.Fatalf("NewControlSurface: %v", err)
	}
	if !s.Version().Equal(v) {
		t.Fatalf("Version()=%s, quero %s", s.Version(), v)
	}
}

// TestVersion_StringAndChangeKindString — cobertura das formas textuais.
func TestVersion_StringAndChangeKindString(t *testing.T) {
	if got := (controlsurface.ControlSchemaVersion{Major: 3, Minor: 4, Patch: 5}).String(); got != "3.4.5" {
		t.Fatalf("String()=%q, quero 3.4.5", got)
	}
	cases := map[controlsurface.ChangeKind]string{
		controlsurface.ChangeNone:  "none",
		controlsurface.ChangePatch: "patch",
		controlsurface.ChangeMinor: "minor",
		controlsurface.ChangeMajor: "major",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("ChangeKind(%d).String()=%q, quero %q", k, got, want)
		}
	}
	// Compare simétrico nas três componentes.
	a := controlsurface.ControlSchemaVersion{Major: 1, Minor: 0, Patch: 0}
	if a.Compare(controlsurface.ControlSchemaVersion{Major: 1, Minor: 0, Patch: 1}) != -1 ||
		a.Compare(controlsurface.ControlSchemaVersion{Major: 1, Minor: 0, Patch: 0}) != 0 ||
		a.Compare(controlsurface.ControlSchemaVersion{Major: 0, Minor: 9, Patch: 9}) != 1 {
		t.Fatalf("Compare inesperado")
	}
}

// TestValidate_ResumeInlineCorrectionNeedsSignature — fail-closed: resume com
// correcção inline sem a assinatura da injecção é rejeitado.
func TestValidate_ResumeInlineCorrectionNeedsSignature(t *testing.T) {
	m := controlsurface.ControlMessage{
		SchemaVersion: controlsurface.CurrentVersion.String(),
		Kind:          controlsurface.KindResume,
		RunID:         testRunID,
		EmitterID:     testEmitter,
		Signature:     []byte("resume-sig"),
		Correction:    []byte("corrige"),
		// CorrectionSignature em falta.
	}
	if err := m.Validate(controlsurface.CurrentVersion); !errors.Is(err, controlsurface.ErrEmptyCorrectionSignature) {
		t.Fatalf("Validate=%v, quero ErrEmptyCorrectionSignature", err)
	}
}

// TestResume_WithCorrectionOnNonPausedRunLeavesNoDanglingCorrection — um
// resume-com-correcção sobre um run NÃO pausado é recusado com state.ErrInvalidTransition
// (como um resume limpo) e NÃO deixa a correcção injectada pendente no log (guarda de
// ordenação): não haveria retoma a consumi-la, e uma correcção órfã seria aplicada numa
// retoma futura. Fail-early sem escrever nada.
func TestResume_WithCorrectionOnNonPausedRunLeavesNoDanglingCorrection(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	auth := authWith(t)
	ch := newChannel(t, st, auth, nil)
	surface := newSurface(t, ch, nil)
	_, binding := runningMachine(t, st, testRunID) // running, NUNCA pausado

	correction := []byte("corrige o rumo")
	corrSig := sign(t, auth, testRunID, control.SignalSteer, correction, testEmitter)
	resumeSig := sign(t, auth, testRunID, control.SignalResume, nil, testEmitter)
	msg := controlsurface.NewResumeWithCorrection(testRunID, controlsurface.ChannelChatbot, testEmitter, resumeSig, correction, corrSig)

	_, err := surface.Dispatch(ctx, msg, binding)
	if !errors.Is(err, state.ErrInvalidTransition) {
		t.Fatalf("Dispatch(resume+correcção, run não pausado)=%v, quero state.ErrInvalidTransition", err)
	}
	// A correcção NÃO ficou pendente (a guarda impediu o steer inline).
	if _, ok := ch.PendingCorrection(testRunID); ok {
		t.Fatalf("resume recusado deixou uma correcção órfã pendente (não devia)")
	}
	if ch.PendingPause(testRunID) {
		t.Fatalf("resume recusado não devia deixar pausa pendente")
	}
}

// TestReflection_ConstructorFailClosed — o projector recusa subscritor nil e run vazio.
func TestReflection_ConstructorFailClosed(t *testing.T) {
	ctx := context.Background()
	if _, err := controlsurface.NewStateProjector(ctx, nil, testRunID); !errors.Is(err, controlsurface.ErrNilSubscriber) {
		t.Fatalf("NewStateProjector(nil)=%v, quero ErrNilSubscriber", err)
	}
	st := newStore(t)
	if _, err := controlsurface.NewStateProjector(ctx, st, ""); !errors.Is(err, controlsurface.ErrEmptyRunID) {
		t.Fatalf("NewStateProjector(runID vazio)=%v, quero ErrEmptyRunID", err)
	}
}

// TestReflection_AccessorsAndCancel — RunID, Close idempotente e o cancel de Observe.
func TestReflection_AccessorsAndCancel(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	proj, err := controlsurface.NewStateProjector(ctx, st, testRunID)
	if err != nil {
		t.Fatalf("NewStateProjector: %v", err)
	}
	if proj.RunID() != testRunID {
		t.Fatalf("RunID()=%q, quero %q", proj.RunID(), testRunID)
	}
	if proj.Current() != state.Ready {
		t.Fatalf("estado inicial=%s, quero ready", proj.Current())
	}

	// Um observador cancelado NÃO recebe transições posteriores.
	got := make(chan state.State, 4)
	cancel := proj.Observe(func(s state.State) { got <- s })
	cancel()

	m, err := state.NewMachine(st, testRunID)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	// A projecção converge na mesma (o Current reflecte), mas o observador cancelado
	// não recebe — espera curta e determinística por convergência de Current.
	deadline := time.After(2 * time.Second)
	for proj.Current() != state.Running {
		select {
		case <-deadline:
			t.Fatalf("Current não convergiu para running")
		default:
		}
	}
	select {
	case s := <-got:
		t.Fatalf("observador cancelado recebeu %s (não devia receber nada)", s)
	default:
	}

	proj.Close()
	proj.Close() // idempotente
}

// TestReflection_FailClosedIgnoresCorruptEvent — um evento de transição com payload
// corrompido ou estado desconhecido é IGNORADO (não corrompe a projecção).
func TestReflection_FailClosedIgnoresCorruptEvent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	proj, err := controlsurface.NewStateProjector(ctx, st, testRunID)
	if err != nil {
		t.Fatalf("NewStateProjector: %v", err)
	}
	t.Cleanup(proj.Close)

	seen := make(chan state.State, 4)
	proj.Observe(func(s state.State) { seen <- s })

	// Injecta directamente no stream um evento run.state.transition com um To
	// desconhecido (o molde de log corrompido/legado) — deve ser ignorado.
	if _, err := st.Append(ctx, testRunID, eventstore.EventInput{
		Type:    state.EventTypeTransition,
		Payload: []byte(`{"from":"running","to":"quantum_superposition","at":"2026-07-20T10:00:00Z"}`),
		RunID:   testRunID,
		StepID:  "state-1",
	}); err != nil {
		t.Fatalf("Append(corrompido): %v", err)
	}
	// E um payload não-JSON (também ignorado fail-closed).
	if _, err := st.Append(ctx, testRunID, eventstore.EventInput{
		Type:    state.EventTypeTransition,
		Payload: []byte(`nao-json`),
		RunID:   testRunID,
		StepID:  "state-2",
	}); err != nil {
		t.Fatalf("Append(nao-json): %v", err)
	}

	// Nenhum dos dois eventos corrompidos deve fazer a projecção sair de ready.
	select {
	case s := <-seen:
		t.Fatalf("evento corrompido projectou estado %s (devia ser ignorado)", s)
	case <-time.After(200 * time.Millisecond):
		// Nada entregue — correcto.
	}
	if proj.Current() != state.Ready {
		t.Fatalf("Current após eventos corrompidos=%s, quero ready (fail-closed)", proj.Current())
	}
}
