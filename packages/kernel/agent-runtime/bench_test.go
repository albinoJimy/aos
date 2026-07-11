package agentruntime

import (
	"context"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// BenchmarkAssemble mede a montagem cache-estável (hot path do loop).
func BenchmarkAssemble(b *testing.B) {
	asm := NewPromptAssembler("És um agente do AOS.", toolSet())
	tail := []TailSegment{
		{Kind: TailObjective, Content: []byte("objectivo do run")},
		{Kind: TailHistory, Content: []byte("turno anterior")},
		{Kind: TailToolResult, Content: []byte("resultado untrusted")},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = asm.Assemble(i, tail)
	}
}

// BenchmarkRunTurn mede um run de dois turnos (montar→chamar→despachar→verificar)
// com o RM e o Event Store reais.
func BenchmarkRunTurn(b *testing.B) {
	store, err := eventstore.New()
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	_ = rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil })
	rec := NewTurnRecorder(store)

	callN := 0
	model := ModelClientFunc(func(_ context.Context, _ PromptView) (ModelResponse, error) {
		callN++
		if callN%2 == 1 {
			return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("x")}}}, nil
		}
		return ModelResponse{Text: "fim", Final: true}, nil
	})
	rt := New(model, rm, rec)

	goal := sampleGoal()
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		callN = 0
		// RunID único por iteração para não colidir com a idempotência do ES.
		goal.RunID = "bench-" + itoa(i)
		if _, err := rt.Run(ctx, goal); err != nil {
			b.Fatal(err)
		}
	}
}
