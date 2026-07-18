package autonomy

import (
	"context"
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/risk"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// TestExposeLevelSpan prova que o span aos.autonomy.level expõe o nível corrente do
// par (agente, domínio) e a composição nível × classe (AC4/DoD).
func TestExposeLevelSpan(t *testing.T) {
	tr := otelgenai.NewRecordingTracer(nil)
	_, span := ExposeLevel(context.Background(), tr, "agent-9", "http", L4)
	AnnotateOversight(span, risk.ClassDanger, Oversight(L4, risk.ClassDanger))
	span.End()

	spans := tr.Spans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d; quer 1", len(spans))
	}
	sd := spans[0]
	if !sd.Ended {
		t.Error("span não foi fechado (End)")
	}
	assertAttr(t, sd, otelgenai.AttrOperationName, OpAutonomyLevel)
	assertAttr(t, sd, AttrAutonomyAgent, "agent-9")
	assertAttr(t, sd, AttrAutonomyDomain, "http")
	assertAttr(t, sd, AttrAutonomyLevel, "L4")
	assertAttr(t, sd, AttrAutonomyRiskClass, "danger")
	assertAttr(t, sd, AttrAutonomyOversight, "confirm")
}

// TestExposeLevelNilTracerNoop prova que um tracer nil não entra em panic (Noop).
func TestExposeLevelNilTracerNoop(t *testing.T) {
	_, span := ExposeLevel(context.Background(), nil, "a", "d", L0)
	AnnotateOversight(span, risk.ClassSafe, OversightSuggest)
	span.End()
	AnnotateOversight(nil, risk.ClassSafe, OversightSuggest) // não deve panicar
}

func assertAttr(t *testing.T, sd *otelgenai.RecordedSpan, key, want string) {
	t.Helper()
	v, ok := sd.Attributes[key]
	if !ok {
		t.Errorf("atributo %q ausente", key)
		return
	}
	if s, _ := v.(string); s != want {
		t.Errorf("atributo %q = %v; quer %q", key, v, want)
	}
}
