package otelgenai

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestTraceParentRoundTrip prova que FormatTraceParent∘ParseTraceParent é a
// identidade para SpanContexts válidos: o carrier cross-fronteira preserva
// trace_id e span_id exactos (a ligação nativa OTel do filho ao pai).
func TestTraceParentRoundTrip(t *testing.T) {
	cases := []SpanContext{
		{TraceID: [16]byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80}, SpanID: [8]byte{0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54, 0x32, 0x10}},
		{TraceID: [16]byte{15: 1}, SpanID: [8]byte{7: 1}},
		{TraceID: [16]byte{0: 0xff, 15: 0xff}, SpanID: [8]byte{0: 0xff, 7: 0xff}},
	}
	for _, sc := range cases {
		s := FormatTraceParent(sc)
		if len(s) != traceParentLen {
			t.Fatalf("comprimento do traceparent = %d, esperava %d (%q)", len(s), traceParentLen, s)
		}
		if !strings.HasPrefix(s, "00-") || !strings.HasSuffix(s, "-01") {
			t.Fatalf("forma do traceparent inesperada: %q", s)
		}
		got, err := ParseTraceParent(s)
		if err != nil {
			t.Fatalf("ParseTraceParent(%q): %v", s, err)
		}
		if got != sc {
			t.Fatalf("round-trip = %+v, esperava %+v (via %q)", got, sc, s)
		}
	}
}

// TestParseTraceParentMalformed cobre os vectores fail-closed: qualquer entrada
// malformada devolve ErrInvalidTraceParent e NUNCA um SpanContext parcial.
func TestParseTraceParentMalformed(t *testing.T) {
	valid := FormatTraceParent(SpanContext{TraceID: [16]byte{15: 1}, SpanID: [8]byte{7: 1}})
	vectors := map[string]string{
		"vazio":              "",
		"curto":              "00-abcd",
		"comprido":           valid + "0",
		"versao_ff":          "ff-" + valid[3:],
		"versao_nao_hex":     "zz-" + valid[3:],
		"trace_all_zero":     "00-" + strings.Repeat("0", 32) + "-" + strings.Repeat("1", 16) + "-01",
		"span_all_zero":      "00-" + strings.Repeat("1", 32) + "-" + strings.Repeat("0", 16) + "-01",
		"trace_nao_hex":      "00-" + strings.Repeat("g", 32) + "-" + strings.Repeat("1", 16) + "-01",
		"span_nao_hex":       "00-" + strings.Repeat("1", 32) + "-" + strings.Repeat("g", 16) + "-01",
		"flags_nao_hex":      "00-" + strings.Repeat("1", 32) + "-" + strings.Repeat("1", 16) + "-gg",
		"separador_1_errado": "00x" + valid[3:],
		"separador_2_errado": valid[:35] + "x" + valid[36:],
		"separador_3_errado": valid[:52] + "x" + valid[53:],
		"sem_separadores":    strings.Repeat("0", traceParentLen),
		// W3C exige hex minúsculo; maiúsculo é rejeitado fail-closed (não aceite
		// silenciosamente por hex.DecodeString, que toleraria [A-F]).
		"trace_hex_maiusculo": "00-" + strings.Repeat("A", 32) + "-" + strings.Repeat("1", 16) + "-01",
		"span_hex_maiusculo":  "00-" + strings.Repeat("1", 32) + "-" + strings.Repeat("B", 16) + "-01",
	}
	for name, in := range vectors {
		got, err := ParseTraceParent(in)
		if !errors.Is(err, ErrInvalidTraceParent) {
			t.Errorf("%s: esperava ErrInvalidTraceParent, obtive err=%v", name, err)
		}
		if got != (SpanContext{}) {
			t.Errorf("%s: devolveu SpanContext não-zero em falha: %+v", name, got)
		}
	}
}

// assertConnectedAcyclicTree verifica a propriedade estrutural exigida por
// AOS-077: um só trace_id, exactamente uma raiz (sem parent_span_id), cada span
// não-raiz tem um pai PRESENTE, e não há ciclos (seguir a cadeia de pais termina
// sempre na raiz em passos finitos). span_ids são únicos.
func assertConnectedAcyclicTree(t *testing.T, spans []*RecordedSpan) {
	t.Helper()
	if len(spans) == 0 {
		t.Fatal("árvore vazia")
	}
	byID := make(map[[8]byte]*RecordedSpan, len(spans))
	var trace [16]byte
	roots := 0
	for i, s := range spans {
		if !s.SpanContext.IsValid() {
			t.Fatalf("span %d com SpanContext inválido", i)
		}
		if i == 0 {
			trace = s.SpanContext.TraceID
		} else if s.SpanContext.TraceID != trace {
			t.Fatalf("trace_id divergente: um só trace esperado (span %d)", i)
		}
		if _, dup := byID[s.SpanContext.SpanID]; dup {
			t.Fatalf("span_id duplicado: %x", s.SpanContext.SpanID)
		}
		byID[s.SpanContext.SpanID] = s
		if s.ParentSpanID == ([8]byte{}) {
			roots++
		}
	}
	if roots != 1 {
		t.Fatalf("esperava exactamente 1 raiz, obtive %d", roots)
	}
	// Cada não-raiz tem pai presente; seguir os pais termina na raiz sem ciclos.
	for _, s := range spans {
		steps := 0
		cur := s
		for cur.ParentSpanID != ([8]byte{}) {
			parent, ok := byID[cur.ParentSpanID]
			if !ok {
				t.Fatalf("span %x aponta a um pai ausente %x (árvore não ligada)", cur.SpanContext.SpanID, cur.ParentSpanID)
			}
			cur = parent
			steps++
			if steps > len(spans) {
				t.Fatalf("ciclo detectado a partir de %x", s.SpanContext.SpanID)
			}
		}
	}
}

// TestTraceParentSeededTreePropertyAnyDepth é o teste de PROPRIEDADE: para
// qualquer profundidade de delegação, semear cada nível a partir do traceparent do
// nível anterior (o exacto veículo cross-fronteira) produz uma árvore ACÍCLICA e
// COMPLETAMENTE LIGADA — um só trace_id, cada não-raiz com um pai presente. Modela
// a recursão pai→filho→neto→… sem depender do RT/ORQ concretos.
func TestTraceParentSeededTreePropertyAnyDepth(t *testing.T) {
	for _, depth := range []int{1, 2, 3, 5, 8, 25} {
		tracer := NewRecordingTracer(&SequentialIDGenerator{})
		var seed string // traceparent do nível anterior (vazio na raiz)
		for level := 0; level < depth; level++ {
			// Cada nível é uma FRONTEIRA nova: ctx-raiz limpo, semeado só pelo carrier.
			ctx := context.Background()
			if seed != "" {
				sc, err := ParseTraceParent(seed)
				if err != nil {
					t.Fatalf("nível %d: ParseTraceParent(%q): %v", level, seed, err)
				}
				ctx = ContextWithSpanContext(ctx, sc)
			}
			_, span := tracer.StartSpan(ctx, OpInvokeAgent)
			seed = FormatTraceParent(span.SpanContext())
			span.End()
		}
		spans := tracer.Spans()
		if len(spans) != depth {
			t.Fatalf("profundidade %d: obtive %d spans", depth, len(spans))
		}
		assertConnectedAcyclicTree(t, spans)
	}
}

// TestTraceParentSeededTreeFanOut exercita a topologia RAMIFICADA real da
// delegação — vários irmãos sob o MESMO pai (fan-out), e netos sob alguns irmãos —
// não só uma cadeia linear. Cada filho é semeado a partir do MESMO traceparent do
// pai (o veículo cross-fronteira), provando que N filhos partilham o trace_id do
// pai e parenteiam todos sob o span_id do pai, mantendo a árvore acíclica e
// completamente ligada (assertConnectedAcyclicTree é geral para árvores, não só
// caminhos). Complementa o teste de propriedade de cadeia linear.
func TestTraceParentSeededTreeFanOut(t *testing.T) {
	tracer := NewRecordingTracer(&SequentialIDGenerator{})

	// Raiz (o pai).
	_, root := tracer.StartSpan(context.Background(), OpInvokeAgent)
	rootSeed := FormatTraceParent(root.SpanContext())
	rootSpanID := root.SpanContext().SpanID
	root.End()

	// Fan-out: 4 filhos irmãos, cada um semeado do MESMO traceparent do pai (a
	// fronteira que cada spawn atravessa). Alguns filhos geram netos.
	const siblings = 4
	childSeeds := make([]string, 0, siblings)
	for i := 0; i < siblings; i++ {
		sc, err := ParseTraceParent(rootSeed)
		if err != nil {
			t.Fatalf("filho %d: ParseTraceParent: %v", i, err)
		}
		ctx := ContextWithSpanContext(context.Background(), sc)
		_, child := tracer.StartSpan(ctx, OpInvokeAgent)
		cc := child.SpanContext()
		if cc.TraceID != sc.TraceID {
			t.Fatalf("filho %d: trace_id divergente do pai", i)
		}
		childSeeds = append(childSeeds, FormatTraceParent(cc))
		child.End()
	}

	// Netos sob os dois primeiros irmãos (ramificação a 3 níveis).
	for i := 0; i < 2; i++ {
		sc, err := ParseTraceParent(childSeeds[i])
		if err != nil {
			t.Fatalf("neto de %d: ParseTraceParent: %v", i, err)
		}
		ctx := ContextWithSpanContext(context.Background(), sc)
		_, gc := tracer.StartSpan(ctx, OpInvokeAgent)
		gc.End()
	}

	spans := tracer.Spans()
	// 1 raiz + 4 filhos + 2 netos = 7.
	if len(spans) != 1+siblings+2 {
		t.Fatalf("esperava %d spans, obtive %d", 1+siblings+2, len(spans))
	}
	// Fan-out explícito: exactamente `siblings` spans parenteiam sob o span_id do pai.
	directChildren := 0
	for _, s := range spans {
		if s.ParentSpanID == rootSpanID {
			directChildren++
		}
	}
	if directChildren != siblings {
		t.Fatalf("esperava %d filhos directos sob o pai (fan-out), obtive %d", siblings, directChildren)
	}
	assertConnectedAcyclicTree(t, spans)
}
