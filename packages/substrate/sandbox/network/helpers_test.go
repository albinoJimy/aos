package network

import (
	"context"
	"sync"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// fixedClock é um relógio determinista (sem time.Now na asserção).
func fixedClock() func() time.Time {
	t := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// principalClass constrói um principal escopado por classe de agente.
func principalClass(class string) referencemonitor.Principal {
	return referencemonitor.Principal{AgentClass: class}
}

// principalNHI constrói um principal escopado por NHI id.
func principalNHI(nhi string) referencemonitor.Principal {
	return referencemonitor.Principal{NHIID: nhi}
}

// recordingSink é um [SecurityAuditSink] fake: acumula os eventos selados e pode ser
// configurado para falhar a selagem (testar o fail-closed do audit).
type recordingSink struct {
	mu       sync.Mutex
	events   []SecurityEvent
	failWith error
}

func (s *recordingSink) Seal(_ context.Context, ev SecurityEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failWith != nil {
		return s.failWith
	}
	s.events = append(s.events, ev)
	return nil
}

func (s *recordingSink) all() []SecurityEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SecurityEvent, len(s.events))
	copy(out, s.events)
	return out
}

// recordingSpan captura os atributos anotados (para asserir a decisão no span e a
// ausência de segredo).
type recordingSpan struct {
	mu    sync.Mutex
	attrs map[string]any
	ended bool
}

func (s *recordingSpan) SetAttribute(k string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs == nil {
		s.attrs = map[string]any{}
	}
	s.attrs[k] = v
}

func (s *recordingSpan) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
}

func (s *recordingSpan) get(k string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.attrs[k]
	return v, ok
}

// recordingTracer devolve sempre o mesmo [recordingSpan] (um único span por teste).
type recordingTracer struct {
	span *recordingSpan
}

func newRecordingTracer() *recordingTracer {
	return &recordingTracer{span: &recordingSpan{}}
}

func (t *recordingTracer) StartSpan(ctx context.Context, _ string) (context.Context, Span) {
	return ctx, t.span
}
