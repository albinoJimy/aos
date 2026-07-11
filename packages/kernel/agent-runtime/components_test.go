package agentruntime

import (
	"context"
	"errors"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// fakeAppender captura os EventInput submetidos (isola o TurnRecorder do ES).
type fakeAppender struct {
	got []eventstore.EventInput
	err error
	seq uint64
}

func (f *fakeAppender) Append(_ context.Context, _ string, in eventstore.EventInput, _ ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if f.err != nil {
		return eventstore.AppendResult{}, f.err
	}
	f.got = append(f.got, in)
	f.seq++
	return eventstore.AppendResult{Seq: f.seq, Status: eventstore.StatusCommitted}, nil
}

func TestTurnRecorderDefaultsAndPayload(t *testing.T) {
	fa := &fakeAppender{}
	rec := NewTurnRecorder(fa)
	seq, err := rec.Record(context.Background(), TurnRecord{
		RunID:    "run1",
		StepID:   "step-000001",
		Turn:     1,
		Manifest: Manifest{PromptHash: "sha256:deadbeef"}, // sem SchemaVersion/AssemblyVersion
		Usage:    Usage{InputTokens: 3, OutputTokens: 2},
	})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if seq != 1 {
		t.Fatalf("seq = %d, esperava 1", seq)
	}
	if len(fa.got) != 1 || fa.got[0].Type != EventTypeTurnRecorded {
		t.Fatalf("evento gravado errado: %+v", fa.got)
	}
	if fa.got[0].RunID != "run1" || fa.got[0].StepID != "step-000001" {
		t.Fatalf("correlação errada: %+v", fa.got[0])
	}
}

func TestTurnRecorderPropagatesError(t *testing.T) {
	sentinel := errors.New("store indisponível")
	rec := NewTurnRecorder(&fakeAppender{err: sentinel})
	_, err := rec.Record(context.Background(), TurnRecord{RunID: "r", StepID: "s"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("esperava propagar %v, obtive %v", sentinel, err)
	}
}

func TestValidateErrors(t *testing.T) {
	store, _ := eventstore.New()
	t.Cleanup(func() { _ = store.Close() })
	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	rec := NewTurnRecorder(store)
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		return ModelResponse{Final: true}, nil
	})

	cases := []struct {
		name string
		goal Goal
		want error
	}{
		{"sem RunID", Goal{Principal: referencemonitor.Principal{NHIID: "n"}}, ErrEmptyRunID},
		{"sem Principal", Goal{RunID: "r"}, ErrNoPrincipal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := New(model, rm, rec)
			_, err := rt.Run(context.Background(), tc.goal)
			if !errors.Is(err, tc.want) {
				t.Fatalf("esperava %v, obtive %v", tc.want, err)
			}
		})
	}
}

func TestValidateMissingCollaborators(t *testing.T) {
	goal := sampleGoal()
	// model nil
	rt := New(nil, referencemonitor.New(), NewTurnRecorder(&fakeAppender{}))
	if _, err := rt.Run(context.Background(), goal); !errors.Is(err, ErrNoModelClient) {
		t.Fatalf("esperava ErrNoModelClient, obtive %v", err)
	}
	// rm nil
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) { return ModelResponse{Final: true}, nil })
	rt = New(model, nil, NewTurnRecorder(&fakeAppender{}))
	if _, err := rt.Run(context.Background(), goal); !errors.Is(err, ErrNoMonitor) {
		t.Fatalf("esperava ErrNoMonitor, obtive %v", err)
	}
	// recorder nil
	rt = New(model, referencemonitor.New(), nil)
	if _, err := rt.Run(context.Background(), goal); !errors.Is(err, ErrNoRecorder) {
		t.Fatalf("esperava ErrNoRecorder, obtive %v", err)
	}
}

func TestModelCallError(t *testing.T) {
	store, _ := eventstore.New()
	t.Cleanup(func() { _ = store.Close() })
	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	rec := NewTurnRecorder(store)
	boom := errors.New("gateway down")
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		return ModelResponse{}, boom
	})
	rt := New(model, rm, rec)
	_, err := rt.Run(context.Background(), sampleGoal())
	if !errors.Is(err, ErrModelCall) || !errors.Is(err, boom) {
		t.Fatalf("esperava ErrModelCall envolvendo boom, obtive %v", err)
	}
}

func TestTurnRecordErrorPropagation(t *testing.T) {
	// Recorder sobre um appender que falha ⇒ Run devolve ErrTurnRecord.
	rec := NewTurnRecorder(&fakeAppender{err: errors.New("no quorum")})
	rm := referencemonitor.New()
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		return ModelResponse{Final: true}, nil
	})
	rt := New(model, rm, rec)
	_, err := rt.Run(context.Background(), sampleGoal())
	if !errors.Is(err, ErrTurnRecord) {
		t.Fatalf("esperava ErrTurnRecord, obtive %v", err)
	}
}

// failCheckpointer erra na fase indicada (exercita o ponto de ligação AOS-015).
type failCheckpointer struct {
	failOn CheckpointPhase
	err    error
}

func (f failCheckpointer) Checkpoint(_ context.Context, cp Checkpoint) error {
	if cp.Phase == f.failOn {
		return f.err
	}
	return nil
}

func TestCheckpointerErrorHalts(t *testing.T) {
	store, _ := eventstore.New()
	t.Cleanup(func() { _ = store.Close() })
	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	rec := NewTurnRecorder(store)
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		return ModelResponse{Final: true}, nil
	})
	sentinel := errors.New("checkpoint falhou")
	rt := New(model, rm, rec, WithCheckpointer(failCheckpointer{failOn: PhaseAssembled, err: sentinel}))
	_, err := rt.Run(context.Background(), sampleGoal())
	if !errors.Is(err, sentinel) {
		t.Fatalf("esperava propagar erro do checkpointer, obtive %v", err)
	}
}

// TestContextCanceledDuringDispatch: um contexto cancelado faz o RM devolver erro
// que o loop propaga.
func TestContextCanceledDuringDispatch(t *testing.T) {
	store, _ := eventstore.New()
	t.Cleanup(func() { _ = store.Close() })
	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatal(err)
	}
	rec := NewTurnRecorder(store)

	ctx, cancel := context.WithCancel(context.Background())
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		cancel() // cancela antes do despacho da tool
		return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo"}}}, nil
	})
	rt := New(model, rm, rec)
	_, err := rt.Run(ctx, sampleGoal())
	if err == nil {
		t.Fatalf("esperava erro de contexto cancelado")
	}
}

func TestNoopTracerAndDefaults(t *testing.T) {
	// New com todas as opções a nil ⇒ defaults aplicados.
	rt := New(
		ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) { return ModelResponse{Final: true}, nil }),
		referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(mustStore(t)))),
		NewTurnRecorder(mustStore(t)),
		WithTracer(nil), WithStepIdentity(nil), WithCheckpointer(nil), WithAssemblyVersion(""), WithMaxTurns(0),
	)
	if _, ok := rt.tracer.(NoopTracer); !ok {
		t.Fatalf("tracer default devia ser NoopTracer")
	}
	if rt.assemblyVersion != AssemblyVersion || rt.defaultMaxTurns != DefaultMaxTurns {
		t.Fatalf("defaults não aplicados")
	}
	// NoopTracer não deve entrar em panic.
	_, span := NoopTracer{}.StartSpan(context.Background(), OpChat)
	span.SetAttribute("k", "v")
	span.End()
}

func mustStore(t *testing.T) *eventstore.Store {
	t.Helper()
	s, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestTaintHelpers(t *testing.T) {
	u := Untrusted([]byte("x"))
	if !u.IsUntrusted() || u.Taint != TaintUntrusted {
		t.Fatalf("Untrusted mal construído: %+v", u)
	}
	trusted := Tainted{Value: []byte("y"), Taint: TaintTrusted}
	if trusted.IsUntrusted() {
		t.Fatalf("trusted não devia ser untrusted")
	}
}

func TestStepIdentitySequential(t *testing.T) {
	s := sequentialStepIdentity{}
	if got := s.StepID("run", 1); got != "step-000001" {
		t.Fatalf("StepID(1) = %q", got)
	}
	if got := s.StepID("run", 42); got != "step-000042" {
		t.Fatalf("StepID(42) = %q", got)
	}
	// step_ids distintos por turno.
	if s.StepID("run", 1) == s.StepID("run", 2) {
		t.Fatalf("step_ids não distintos entre turnos")
	}
}
