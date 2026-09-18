package otelgenai

import "testing"

// chatSemPreco é um span chat como o que o Agent Runtime emite sem fonte de preço (AOS-406):
// tokens medidos, sem `aos.cost.*`, com `aos.cost.undefined=true`.
func chatSemPreco(traceB, spanB, parentB byte, in, out int64) SpanData {
	return SpanData{
		Name:         OpChat,
		SpanContext:  SpanContext{TraceID: traceID(traceB), SpanID: spanID(spanB)},
		ParentSpanID: spanID(parentB),
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpChat},
			{Key: AttrInputTokens, Value: in},
			{Key: AttrOutputTokens, Value: out},
			{Key: AttrCostUndefined, Value: true},
		},
	}
}

// TestAOS406_SLIDeCustoSemPrecoNaoEAvaliado — FALHA-ANTES: sem a marca, estes traces contavam como
// amostras de custo zero e o SLI saía cumprido (o falso verde de produção).
func TestAOS406_SLIDeCustoSemPrecoNaoEAvaliado(t *testing.T) {
	spans := []SpanData{
		chatSemPreco(1, 10, 0, 426, 92),
		chatSemPreco(1, 11, 0, 907, 410),
		chatSemPreco(2, 20, 0, 300, 50),
	}
	sli := costPerTrajectorySLI(spans, 1_000_000)
	if sli.Samples != 0 || sli.Evaluated() {
		t.Fatalf("traces sem custo derivado não são amostras: Samples=%d Evaluated=%v", sli.Samples, sli.Evaluated())
	}
}

// TestAOS406_TraceMistoSaiInteiro — um trace com um chat com preço e outro sem é uma soma parcial:
// fica fora, para o SLI não subestimar o custo dessa trajectória.
func TestAOS406_TraceMistoSaiInteiro(t *testing.T) {
	spans := []SpanData{
		chatSpan(1, 10, 0, 100, 10, 5_000),
		chatSemPreco(1, 11, 0, 100, 10),
		chatSpan(2, 20, 0, 100, 10, 7_000),
	}
	sli := costPerTrajectorySLI(spans, 1_000_000)
	if sli.Samples != 1 {
		t.Fatalf("só o trace inteiramente com preço conta: Samples=%d", sli.Samples)
	}
	if sli.Value != 7_000 {
		t.Fatalf("o valor tem de ser o do trace com preço (7000), veio %v", sli.Value)
	}
	agg := AggregateByTrace(spans)
	if !agg[traceIDHexOf(1)].CostUndefined || agg[traceIDHexOf(2)].CostUndefined {
		t.Fatalf("a agregação tem de propagar a marca por trace: %+v", agg)
	}
}

func traceIDHexOf(b byte) string {
	sc := SpanContext{TraceID: traceID(b)}
	return sc.TraceIDHex()
}
