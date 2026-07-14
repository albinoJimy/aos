package integritytests

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/memory/compression"
	"github.com/aos-ref/platform/memory/working"
)

// TestIntegrityProjectionPreservesRecord — a projecção descarta do CONTEXTO (orçamento
// minúsculo) mas o REGISTO mantém todos os turnos com o conteúdo cru, e o resumo nunca
// vaza o cru (Princípio 4, AOS-036).
func TestIntegrityProjectionPreservesRecord(t *testing.T) {
	ctx := context.Background()
	rec := buildTrajectory("trace-proj", 4)

	// Cenário: o orçamento minúsculo FORÇA descarte de contexto — a prova de que
	// descartar da injecção é legítimo enquanto o registo permanece.
	iv, err := projectForTest(t, rec)
	if err != nil {
		t.Fatalf("ProjectContext: %v", err)
	}
	if iv.TotalTurns != 4 {
		t.Fatalf("TotalTurns = %d, quero 4 (registo mantém todos)", iv.TotalTurns)
	}
	if iv.IncludedTurns >= iv.TotalTurns {
		t.Fatalf("IncludedTurns = %d; esperava descarte de contexto (< %d)", iv.IncludedTurns, iv.TotalTurns)
	}

	if err := verifyProjectionPreservesRecord(ctx, rec, tinyBudgetPolicy()); err != nil {
		t.Fatalf("projecção apagou/vazou o registo: %v", err)
	}
}

// TestIntegrityEvictionPreservesRecord — a eviction do tail retira segmentos da VISTA
// mas preserva-os no backend (via MemoryPortSink) ANTES de os remover; o prefixo nunca
// muta; sem sink, a eviction é recusada (o registo nunca é perdido).
func TestIntegrityEvictionPreservesRecord(t *testing.T) {
	ctx := context.Background()
	port := newInMemoryPort()
	sink := working.NewMemoryPortSink(working.MemoryPortSinkConfig{
		Port: port, AgentID: "agent-1", RunID: "run-evict", Clock: fixedClock(),
	})
	wm := newWindow(t, "run-evict", sink)

	prefixBefore := wm.PrefixHash()
	for i := 1; i <= 5; i++ {
		wm.Append(working.TailInput{
			Kind:     working.TailMemory,
			Content:  "segment-content-value-" + itoa(i),
			Priority: i, // prioridade crescente: os de menor prioridade saem primeiro
			RecordID: "rec-" + itoa(i),
		})
	}

	evicted, err := wm.EvictToTailBudget(ctx, 12)
	if err != nil {
		t.Fatalf("EvictToTailBudget: %v", err)
	}
	if len(evicted) == 0 {
		t.Fatal("nada evictado — cenário inválido (esperava pressão de tail)")
	}
	if got := wm.PrefixHash(); got != prefixBefore {
		t.Fatalf("prefixo mutou pela eviction: %q -> %q", prefixBefore, got)
	}
	if err := verifyEvictedRecordsPreserved(ctx, port, evicted); err != nil {
		t.Fatalf("eviction apagou o registo: %v", err)
	}

	// Sem sink: a eviction é RECUSADA (o registo seria perdido) — fail-closed.
	wmNoSink := newWindow(t, "run-nosink", nil)
	wmNoSink.Append(working.TailInput{Kind: working.TailMemory, Content: "big-segment-content-xxxxxxxx", RecordID: "r1"})
	wmNoSink.Append(working.TailInput{Kind: working.TailMemory, Content: "big-segment-content-yyyyyyyy", RecordID: "r2"})
	if _, err := wmNoSink.EvictToTailBudget(ctx, 1); !errors.Is(err, working.ErrNoEvictionSink) {
		t.Fatalf("eviction sem sink devolveu %v, quero ErrNoEvictionSink", err)
	}
}

// TestIntegrityCompressionPreservesRecord — a compressão produz um SUMÁRIO (projecção)
// que cabe na janela, mas o backend recebe a trajectória COMPLETA (FullRecordSpans >
// turnos do sumário) e nada do conteúdo cru vaza; o sumário é recuperável do log.
func TestIntegrityCompressionPreservesRecord(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	comp, err := compression.NewAsyncCompactor(es)
	if err != nil {
		t.Fatalf("NewAsyncCompactor: %v", err)
	}
	src := compression.CompactionSource{
		RunID:        "run-comp",
		CheckpointID: "cp-1",
		TraceID:      "trace-comp",
		AgentID:      "agent-1",
		PrefixHash:   "sha256:prefix-comp",
		Turns:        turnsOf("trace-comp", 5),
	}
	// Política com projecção de orçamento minúsculo: força IncludedTurns < TotalTurns
	// (descarte de contexto) enquanto o registo permanece completo.
	policy := compression.CompressionPolicy{Version: "1.0.0", Projection: tinyBudgetPolicy()}

	res, err := comp.Compact(ctx, src, policy, src.PrefixHash)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if res.Summary.IncludedTurns >= res.TotalTurns {
		t.Fatalf("IncludedTurns = %d; esperava descarte de contexto (< %d)", res.Summary.IncludedTurns, res.TotalTurns)
	}
	if err := verifyCompressionPreservesRecord(res); err != nil {
		t.Fatalf("compressão apagou/vazou o registo: %v", err)
	}

	// O sumário durável é recuperável do log (o registo permanece), sem conteúdo cru.
	sums, err := comp.Summaries(ctx)
	if err != nil {
		t.Fatalf("Summaries: %v", err)
	}
	if len(sums) != 1 {
		t.Fatalf("sumários recuperados = %d, quero 1", len(sums))
	}
}
