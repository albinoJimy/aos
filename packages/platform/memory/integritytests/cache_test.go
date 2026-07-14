package integritytests

import (
	"context"
	"strings"
	"testing"

	"github.com/aos-ref/platform/memory/compression"
	"github.com/aos-ref/platform/memory/working"
)

// TestCachePrefixImmutableUnderCompression — a compressão actua sobre o tail/sumários
// e NUNCA muta nem reordena o prefixo imutável: o PrefixHash é byte-idêntico ao longo
// dos turnos E antes/depois da compactação; a compactação é reproduzível (mesmo Digest
// entre sessões).
func TestCachePrefixImmutableUnderCompression(t *testing.T) {
	ctx := context.Background()
	wm := newWindow(t, "run-cache", nil)
	prefixInitial := wm.PrefixHash()

	// Simula a hot path: N turnos, o tail cresce, o prefixo NUNCA muda.
	for i := 1; i <= 10; i++ {
		wm.Append(working.TailInput{Kind: working.TailMemory, Content: "tail-segment-" + itoa(i), Priority: 1})
		wm.Turn(ctx)
		if got := wm.PrefixHash(); got != prefixInitial {
			t.Fatalf("prefixo mutou no turno %d: %q -> %q", i, prefixInitial, got)
		}
	}

	es := newES(t)
	comp, err := compression.NewAsyncCompactor(es)
	if err != nil {
		t.Fatalf("NewAsyncCompactor: %v", err)
	}
	src := compression.CompactionSource{
		RunID:        "run-cache",
		CheckpointID: "cp-1",
		TraceID:      "trace-cache",
		AgentID:      "agent-1",
		PrefixHash:   wm.PrefixHash(),
		Turns:        turnsOf("trace-cache", 5),
	}
	res, err := comp.Compact(ctx, src, compression.DefaultCompressionPolicy(), wm.PrefixHash())
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := verifyCompressionPrefixStable(res); err != nil {
		t.Fatalf("compressão mutou o prefixo: %v", err)
	}
	if got := wm.PrefixHash(); got != prefixInitial {
		t.Fatalf("prefixo mutou pela compactação: %q -> %q", prefixInitial, got)
	}

	// Reprodutibilidade: um compactor NOVO sobre um ES novo dá o MESMO Digest.
	es2 := newES(t)
	comp2, err := compression.NewAsyncCompactor(es2)
	if err != nil {
		t.Fatalf("NewAsyncCompactor #2: %v", err)
	}
	res2, err := comp2.Compact(ctx, src, compression.DefaultCompressionPolicy(), src.PrefixHash)
	if err != nil {
		t.Fatalf("Compact #2: %v", err)
	}
	if res.Digest != res2.Digest {
		t.Fatalf("Digest não reproduzível: %q != %q", res.Digest, res2.Digest)
	}
}

// TestCacheHitRateAboveTarget — num cenário de referência (prefixo grande e estável,
// tails pequenos, muitos turnos) o SLI de cache-hit-rate fica ACIMA do alvo (>0.80),
// que é a poupança de prefix caching do ADR-009 tornada observável.
func TestCacheHitRateAboveTarget(t *testing.T) {
	ctx := context.Background()
	bigSystem := strings.Repeat("token ", 400) // prefixo grande e estável (~500 tokens)
	wm, err := working.NewWindowManager(working.Config{
		RunID:           "run-sli",
		System:          bigSystem,
		Tools:           []working.ToolSpec{{Name: "t1", Version: "1.0.0", Digest: "d1"}},
		ModelTokenLimit: 1000000,
	})
	if err != nil {
		t.Fatalf("NewWindowManager: %v", err)
	}
	for i := 1; i <= 12; i++ {
		wm.Append(working.TailInput{Kind: working.TailMemory, Content: "t" + itoa(i), Priority: 1})
		wm.Turn(ctx)
	}
	if rate := wm.CacheHitRate(); rate <= 0.80 {
		t.Fatalf("cache-hit-rate = %.4f, quero > 0.80 (prefixo estável)", rate)
	}
}
