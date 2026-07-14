package integritytests

import (
	"context"
	"os"
	"testing"

	"github.com/aos-ref/platform/memory/working"
)

// TestSelftestMemoryViolationReddensGate é um teste-VENENO: só corre com
// AOS_MEMORY_SELFTEST=1. Injecta uma violação de integridade (um sink que NÃO preserva
// o registo evictado) e assere — FALSAMENTE — que o registo foi preservado. Como o
// dropping sink não preservou nada, a asserção FALHA de propósito, PROVANDO que uma
// violação de invariante torna o gate de memória (scripts/ci/memory.sh) VERMELHO
// (fail-closed). O self-test scripts/ci/selftest.sh (secção E) corre-o com a env var e
// EXIGE que falhe. Fora do self-test é ignorado (não polui a suite verde nem o gate).
func TestSelftestMemoryViolationReddensGate(t *testing.T) {
	if os.Getenv("AOS_MEMORY_SELFTEST") != "1" {
		t.Skip("teste-veneno do self-test (correr com AOS_MEMORY_SELFTEST=1 via scripts/ci/selftest.sh)")
	}
	ctx := context.Background()
	port := newInMemoryPort() // backend vazio: o dropping sink nunca lá escreve
	wm := newWindow(t, "run-poison", droppingSink{})
	for i := 1; i <= 4; i++ {
		wm.Append(working.TailInput{
			Kind:     working.TailMemory,
			Content:  "poison-segment-value-" + itoa(i),
			Priority: i,
			RecordID: "rec-" + itoa(i),
		})
	}
	evicted, err := wm.EvictToTailBudget(ctx, 8)
	if err != nil {
		t.Fatalf("EvictToTailBudget: %v", err)
	}
	// Asserção do self-test: assevera (FALSAMENTE) que o registo foi preservado. O
	// verificador devolve erro (o registo desapareceu) — e esta asserção FALHA de
	// propósito, tornando o gate VERMELHO como o self-test exige.
	if verr := verifyEvictedRecordsPreserved(ctx, port, evicted); verr != nil {
		t.Fatalf("violação de integridade injectada foi detectada (esperado no self-test): %v", verr)
	}
}
