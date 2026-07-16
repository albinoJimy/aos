package sandbox

import (
	"context"
	"sync"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// newStore constrói um Event Store de referência (in-process) para os testes.
func newStore(t *testing.T) *eventstore.Store {
	t.Helper()
	store, err := eventstore.New(eventstore.WithReplicas(3))
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// newPermitMonitor constrói um RM que PERMITE (cadeia de stubs neutros) e sela as
// mediações no store dado.
func newPermitMonitor(store *eventstore.Store) *referencemonitor.Monitor {
	return referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.DefaultHooks()...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)),
	)
}

// denyHook é um Hook que nega sempre (fail-closed) — para provar que uma negação
// do RM impede qualquer efeito de sandbox.
type denyHook struct{}

func (denyHook) Name() string { return "test-deny" }
func (denyHook) Evaluate(context.Context, *referencemonitor.Call) (referencemonitor.HookResult, error) {
	return referencemonitor.HookResult{Decision: referencemonitor.HookDeny, Reason: "negado no teste"}, nil
}

// newDenyMonitor constrói um RM cuja cadeia nega sempre.
func newDenyMonitor(store *eventstore.Store) *referencemonitor.Monitor {
	return referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.IdentityStub{}, denyHook{}),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)),
	)
}

// defaultAuthz é uma autorização de teste com um principal mínimo válido.
func defaultAuthz() Authorization {
	return Authorization{
		Principal:  referencemonitor.Principal{NHIID: "nhi-test", AgentID: "agent-1", AgentClass: "class-a"},
		Capability: "cap:test.exec",
		Resource:   referencemonitor.Resource{Type: "vm", Value: "sandbox"},
		Credential: "tok-test",
	}
}

// readEvents devolve todos os eventos do stream (run) ordenados por seq.
func readEvents(t *testing.T, store *eventstore.Store, runID string) []eventstore.Event {
	t.Helper()
	evs, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("store.Read(%q): %v", runID, err)
	}
	return evs
}

// eventsOfType filtra os eventos por tipo.
func eventsOfType(evs []eventstore.Event, typ string) []eventstore.Event {
	var out []eventstore.Event
	for _, e := range evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// recordingTracer capta os atributos dos spans (asserção de "sem segredos" e custo).
type recordingTracer struct {
	mu    sync.Mutex
	spans []*recordingSpan
}

func (rt *recordingTracer) StartSpan(ctx context.Context, name string) (context.Context, Span) {
	s := &recordingSpan{name: name, attrs: map[string]any{}}
	rt.mu.Lock()
	rt.spans = append(rt.spans, s)
	rt.mu.Unlock()
	return ctx, s
}

// attr devolve o primeiro valor capturado para a chave dada (asserção de presença
// de um atributo específico, ex.: custo por span).
func (rt *recordingTracer) attr(key string) (any, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for _, s := range rt.spans {
		s.mu.Lock()
		v, ok := s.attrs[key]
		s.mu.Unlock()
		if ok {
			return v, true
		}
	}
	return nil, false
}

// attrValues devolve todos os valores de atributos capturados (para varrer segredos).
func (rt *recordingTracer) attrValues() []any {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var out []any
	for _, s := range rt.spans {
		s.mu.Lock()
		for _, v := range s.attrs {
			out = append(out, v)
		}
		s.mu.Unlock()
	}
	return out
}

type recordingSpan struct {
	name  string
	mu    sync.Mutex
	attrs map[string]any
	ended bool
}

func (s *recordingSpan) SetAttribute(k string, v any) {
	s.mu.Lock()
	s.attrs[k] = v
	s.mu.Unlock()
}

func (s *recordingSpan) End() {
	s.mu.Lock()
	s.ended = true
	s.mu.Unlock()
}
