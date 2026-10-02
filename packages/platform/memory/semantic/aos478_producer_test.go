package semantic_test

import (
	"context"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/episodic"
	"github.com/aos-ref/platform/memory/provenance"
	"github.com/aos-ref/platform/memory/semantic"
)

// TestAOS478_FactoEPromocaoLevamOAgente (AOS-478): `memory.semantic.fact.recorded` e
// `memory.semantic.fact.promoted` levam no envelope o agente autor.
func TestAOS478_FactoEPromocaoLevamOAgente(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	kb, err := semantic.NewKnowledgeBase(es, episodic.NewInMemoryKeyStore((&seqRand{}).fill), audit.NewMemStore(),
		semantic.WithClock(func() time.Time { return fixedTime }),
		semantic.WithRandSource((&seqRand{n: 1000}).fill),
	)
	if err != nil {
		t.Fatalf("NewKnowledgeBase: %v", err)
	}
	if _, err := kb.Write(ctx, baseFact("mem-478"), provenance.SourceToolResult); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := kb.Curate(ctx, semantic.CurationRequest{
		FactID: "mem-478", Method: provenance.ValidationHuman, Validator: "human:reviewer-7",
		Justification: "revisto", AgentID: "agent-curador", RunID: "run-1", AuditPartition: "run-1",
	}); err != nil {
		t.Fatalf("Curate: %v", err)
	}
	evs, err := es.Read(ctx, semantic.KnowledgeStreamID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	quer := map[string]string{
		semantic.EventTypeFactRecorded: "agent-1",
		semantic.EventTypeFactPromoted: "agent-curador",
	}
	vistos := 0
	for _, ev := range evs {
		w, ok := quer[ev.Type]
		if !ok {
			continue
		}
		vistos++
		if ev.Producer.NHIID != w {
			t.Errorf("%s producer=%+v, quer %q", ev.Type, ev.Producer, w)
		}
	}
	if vistos != 2 {
		t.Fatalf("eventos vistos=%d, quer 2", vistos)
	}
}
