package record_test

import (
	"testing"

	"github.com/aos-ref/platform/memory/record"
)

// TestView_ReadOnlyAccessors cobre os acessores de leitura da vista read-only e do
// registo: a projecção precisa deles, e a vista nunca expõe conteúdo cru.
func TestView_ReadOnlyAccessors(t *testing.T) {
	t.Parallel()
	rec := record.NewTrajectoryRecord("trace-view")
	for i := 1; i <= 2; i++ {
		if err := rec.AppendTurn(completeTurn(i, "resumo do turno", "CONTEUDO-CRU")); err != nil {
			t.Fatal(err)
		}
	}
	rec.AppendSpan(record.Span{ID: "s1", Name: "op"})
	rec.AppendSpan(record.Span{ID: "s2", Name: "op"})

	// Acessores directos do registo concreto.
	if rec.TraceID() != "trace-view" {
		t.Fatalf("TraceID inesperado: %q", rec.TraceID())
	}
	if rec.SpanCount() != 2 {
		t.Fatalf("SpanCount=%d, esperava 2", rec.SpanCount())
	}

	// Vista read-only.
	v := record.View(rec)
	if v.TraceID() != "trace-view" {
		t.Fatalf("view.TraceID inesperado: %q", v.TraceID())
	}
	if v.TurnCount() != 2 {
		t.Fatalf("view.TurnCount=%d, esperava 2", v.TurnCount())
	}
	if v.SpanCount() != 2 {
		t.Fatalf("view.SpanCount=%d, esperava 2", v.SpanCount())
	}

	summaries := v.TurnSummaries()
	if len(summaries) != 2 {
		t.Fatalf("esperava 2 resumos, obtive %d", len(summaries))
	}
	for _, s := range summaries {
		if s.Summary != "resumo do turno" {
			t.Fatalf("resumo inesperado: %q", s.Summary)
		}
		if s.PromptHash == "" || s.ModelID == "" {
			t.Fatalf("manifesto por turno ausente da vista: %+v", s)
		}
	}
}
