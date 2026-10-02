package compression

import (
	"context"
	"testing"
)

// TestAOS478_CompactacaoLevaOAgente (AOS-478): `memory.context.compacted` leva no envelope o
// agente do run compactado.
func TestAOS478_CompactacaoLevaOAgente(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	c := newCompactor(t, es)
	if _, err := c.Compact(ctx, baseSource("run-478", "ckpt-478", "sha256:p", 3), DefaultCompressionPolicy(), "sha256:p"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	evs, err := es.Read(ctx, CompressionStreamID, 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("Read: err=%v n=%d", err, len(evs))
	}
	if evs[0].Type != EventTypeContextCompacted || evs[0].Producer.NHIID != "agent-1" {
		t.Fatalf("%s producer=%+v, quer o agente agent-1", evs[0].Type, evs[0].Producer)
	}
}
