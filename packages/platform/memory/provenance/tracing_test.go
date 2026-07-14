package provenance_test

import (
	"context"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/provenance"
)

// TestSpans_IngestAndPromote confirma que a ingestão e a promoção emitem spans OTel
// via a porta Tracer zero-dep do Agent Runtime, com os atributos de proveniência
// esperados (sem segredos). Cobre também as opções WithIngestTracer/WithPromoteTracer.
func TestSpans_IngestAndPromote(t *testing.T) {
	t.Parallel()
	tr := &agentruntime.RecordingTracer{}

	ing := provenance.NewIngestor(nil, provenance.WithIngestTracer(tr))
	entry, err := ing.Ingest(context.Background(), semanticRecord("s-span", ""), provenance.SourceToolResult)
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	store := audit.NewMemStore()
	promoter, err := provenance.NewPromoter(store,
		provenance.WithClock(func() time.Time { return fixedTime }),
		provenance.WithPromoteTracer(tr),
	)
	if err != nil {
		t.Fatalf("NewPromoter: %v", err)
	}
	if _, _, err := promoter.Promote(context.Background(), provenance.PromotionRequest{
		Entry:     entry,
		Method:    provenance.ValidationPolicy,
		Validator: "policy:eval",
		RunID:     "run-span",
	}); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	ingestSpans := tr.SpansByOperation("memory.provenance.ingest")
	if len(ingestSpans) != 1 {
		t.Fatalf("esperava 1 span de ingestão, obtive %d", len(ingestSpans))
	}
	if !ingestSpans[0].Ended {
		t.Fatal("o span de ingestão não foi fechado (End)")
	}
	if got := ingestSpans[0].Attributes["aos.memory.provenance.provenance"]; got != string(provenance.Untrusted) {
		t.Fatalf("atributo de proveniência do span=%v, esperava untrusted", got)
	}

	promoteSpans := tr.SpansByOperation("memory.provenance.promote")
	if len(promoteSpans) != 1 {
		t.Fatalf("esperava 1 span de promoção, obtive %d", len(promoteSpans))
	}
	if got := promoteSpans[0].Attributes["aos.memory.provenance.result"]; got != "promoted" {
		t.Fatalf("resultado do span de promoção=%v, esperava promoted", got)
	}
}

// TestProvenanceError_Error cobre o formato estável do erro sentinela do pacote.
func TestProvenanceError_Error(t *testing.T) {
	t.Parallel()
	if got := provenance.ErrNilAuditStore.Error(); got == "" {
		t.Fatal("ErrNilAuditStore.Error() vazio")
	}
	// O código estável está no prefixo.
	if provenance.ErrPromotionNotValidated.Code == "" {
		t.Fatal("ErrPromotionNotValidated.Code vazio")
	}
}
