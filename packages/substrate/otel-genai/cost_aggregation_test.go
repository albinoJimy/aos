package otelgenai

import (
	"context"
	"math"
	"testing"
	"time"
)

// chatSpan constrói um SpanData `chat` sintético com trace/span/parent e custo/tokens.
func chatSpan(traceB, spanB, parentB byte, in, out, microUSD int64) SpanData {
	return SpanData{
		Name:         OpChat,
		SpanContext:  SpanContext{TraceID: traceID(traceB), SpanID: spanID(spanB)},
		ParentSpanID: spanID(parentB),
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpChat},
			{Key: AttrInputTokens, Value: in},
			{Key: AttrOutputTokens, Value: out},
			{Key: AttrCostMicroUSD, Value: microUSD},
			{Key: AttrCostUSD, Value: MicroUSDToUSD(microUSD)},
		},
	}
}

// agentSpan constrói um SpanData invoke_agent que carrega o AGREGADO do seu run — o
// span que a agregação NUNCA deve somar (dupla-contagem).
func agentSpan(traceB, spanB, parentB byte, in, out, microUSD int64) SpanData {
	return SpanData{
		Name:         OpInvokeAgent,
		SpanContext:  SpanContext{TraceID: traceID(traceB), SpanID: spanID(spanB)},
		ParentSpanID: spanID(parentB),
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpInvokeAgent},
			{Key: AttrInputTokens, Value: in},
			{Key: AttrOutputTokens, Value: out},
			{Key: AttrCostMicroUSD, Value: microUSD},
			{Key: AttrCostUSD, Value: MicroUSDToUSD(microUSD)},
		},
	}
}

// toolSpan constrói um SpanData execute_tool (sem custo de modelo) — também nunca somado.
func toolSpan(traceB, spanB, parentB byte) SpanData {
	return SpanData{
		Name:         OpExecuteTool,
		SpanContext:  SpanContext{TraceID: traceID(traceB), SpanID: spanID(spanB)},
		ParentSpanID: spanID(parentB),
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpExecuteTool},
			{Key: AttrToolName, Value: "search"},
		},
	}
}

func traceID(b byte) [16]byte {
	var t [16]byte
	t[0] = b
	return t
}

func spanID(b byte) [8]byte {
	if b == 0 {
		return [8]byte{} // raiz: parent nulo
	}
	var s [8]byte
	s[0] = b
	return s
}

// TestAggregateByTraceCountsOnlyChats prova que a soma por trajectória conta SÓ os
// spans chat — o invoke_agent (agregado) e o execute_tool são ignorados.
func TestAggregateByTraceCountsOnlyChats(t *testing.T) {
	// O agregado do invoke_agent carrega valores DELIBERADAMENTE DIFERENTES da soma
	// dos chats (777/333/9_999_999 vs 18/8/2100), para o teste DISCRIMINAR "conta os
	// chats" de "conta o invoke_agent": se o código somasse o agente, o resultado seria
	// visivelmente o valor errado, não a soma dos turnos.
	spans := []SpanData{
		agentSpan(1, 1, 0, 777, 333, 9_999_999), // agregado do run (NÃO deve entrar)
		chatSpan(1, 2, 1, 10, 5, 1200),          // turno 1
		toolSpan(1, 3, 1),                       // sem custo
		chatSpan(1, 4, 1, 8, 3, 900),            // turno 2
	}
	agg := AggregateByTrace(spans)
	if len(agg) != 1 {
		t.Fatalf("esperava 1 trajectória, obtive %d", len(agg))
	}
	got := agg[spans[1].SpanContext.TraceIDHex()]
	want := UsageTotals{InputTokens: 18, OutputTokens: 5 + 3, CostMicroUSD: 2100}
	if got != want {
		t.Fatalf("agregação por trajectória = %+v, esperava %+v (a soma dos chats, NÃO o agregado do invoke_agent)", got, want)
	}
}

// TestNoDoubleCounting é o teste-âncora de AOS-078: prova EXPLICITAMENTE que somar o
// invoke_agent-agregado + os chats NÃO acontece — a agregação bate na soma dos chats
// (a unidade-verdade), e o custo do invoke_agent-agregado por si só JÁ igualaria essa
// soma; contá-lo em cima duplicaria.
func TestNoDoubleCounting(t *testing.T) {
	// O invoke_agent-agregado carrega um custo DISTINTO (5000) da soma dos chats (2100),
	// para o teste discriminar de facto: se a agregação contasse o agente, dava 5000 ou
	// 7100 (agente+chats), nunca os 2100 exactos dos turnos.
	const agentAggregate = 5000
	spans := []SpanData{
		agentSpan(1, 1, 0, 3, 3, agentAggregate), // AGREGADO do run, valores distintos
		chatSpan(1, 2, 1, 10, 5, 1200),
		chatSpan(1, 4, 1, 8, 3, 900),
	}
	agg := AggregateByTrace(spans)[spans[1].SpanContext.TraceIDHex()]

	// A agregação (só-chats) é exactamente a soma dos turnos — NÃO o agregado do agente.
	if agg.CostMicroUSD != 2100 || agg.InputTokens != 18 || agg.OutputTokens != 8 {
		t.Fatalf("agregação só-chats errada: %+v (esperava a soma dos chats 18/8/2100)", agg)
	}
	// DISCRIMINANTE: se contasse o invoke_agent, bateria no valor do agente, não nos chats.
	if agg.CostMicroUSD == agentAggregate {
		t.Fatalf("a agregação (%d) igualou o AGREGADO do invoke_agent — está a contar o agente, não os chats", agg.CostMicroUSD)
	}

	// A soma NAIVE de TODOS os spans (invoke_agent + chats) duplicaria; confirmamos que
	// a agregação NÃO lá chega. 5000 + 1200 + 900 = 7100.
	naiveAll := int64(0)
	for _, sd := range spans {
		naiveAll += costMicroUSDOf(sd) // inclui o invoke_agent — ERRADO de propósito
	}
	if naiveAll != agentAggregate+2100 {
		t.Fatalf("sanity da soma naive: %d (esperava %d)", naiveAll, agentAggregate+2100)
	}
	if agg.CostMicroUSD == naiveAll {
		t.Fatalf("DUPLA-CONTAGEM: a agregação (%d) igualou a soma naive de todos os spans (%d)", agg.CostMicroUSD, naiveAll)
	}
}

// TestRollupOwnVsSubtree prova o rollup OWN vs SUBTREE numa árvore com sub-agente:
//
//	invoke_agent A (raiz)
//	  ├─ chat  (A turno 1)
//	  ├─ chat  (A turno 2)
//	  └─ invoke_agent B (sub-agente)
//	       └─ chat (B turno 1)
//
// OWN(A) = chats directos de A; SUBTREE(A) = A + B; OWN(B) = SUBTREE(B) = chat de B.
func TestRollupOwnVsSubtree(t *testing.T) {
	A := byte(1)
	B := byte(2)
	spans := []SpanData{
		agentSpan(1, A, 0, 0, 0, 0), // A raiz (agregado ignorado)
		chatSpan(1, 10, A, 10, 5, 1000),
		chatSpan(1, 11, A, 20, 6, 2000),
		agentSpan(1, B, A, 0, 0, 0), // B sub-agente sob A
		chatSpan(1, 12, B, 30, 7, 3000),
	}
	roll := RollupByTrace(spans)
	if len(roll) != 1 {
		t.Fatalf("esperava 1 trajectória, obtive %d", len(roll))
	}
	tr := roll[spans[0].SpanContext.TraceIDHex()]

	// Total do trace = os três chats.
	wantTotal := UsageTotals{InputTokens: 60, OutputTokens: 18, CostMicroUSD: 6000}
	if tr.Total != wantTotal {
		t.Fatalf("total do trace = %+v, esperava %+v", tr.Total, wantTotal)
	}
	if tr.Chats != 3 {
		t.Fatalf("chats = %d, esperava 3", tr.Chats)
	}

	aHex := spans[0].SpanContext.SpanIDHex()
	bHex := spans[3].SpanContext.SpanIDHex()

	// OWN(A) = só os chats filhos DIRECTOS de A (turnos 1 e 2), NÃO o de B.
	wantOwnA := UsageTotals{InputTokens: 30, OutputTokens: 11, CostMicroUSD: 3000}
	if tr.OwnByAgent[aHex] != wantOwnA {
		t.Fatalf("OWN(A) = %+v, esperava %+v", tr.OwnByAgent[aHex], wantOwnA)
	}
	// SUBTREE(A) = A + B = todos os três chats.
	if tr.SubtreeByAgent[aHex] != wantTotal {
		t.Fatalf("SUBTREE(A) = %+v, esperava %+v", tr.SubtreeByAgent[aHex], wantTotal)
	}
	// OWN(B) = SUBTREE(B) = o chat de B.
	wantB := UsageTotals{InputTokens: 30, OutputTokens: 7, CostMicroUSD: 3000}
	if tr.OwnByAgent[bHex] != wantB {
		t.Fatalf("OWN(B) = %+v, esperava %+v", tr.OwnByAgent[bHex], wantB)
	}
	if tr.SubtreeByAgent[bHex] != wantB {
		t.Fatalf("SUBTREE(B) = %+v, esperava %+v", tr.SubtreeByAgent[bHex], wantB)
	}

	// NÃO-DUPLA-CONTAGEM na árvore: OWN(A) + SUBTREE(B) = Total (partição dos chats).
	sum := tr.OwnByAgent[aHex].add(tr.SubtreeByAgent[bHex])
	if sum != wantTotal {
		t.Fatalf("partição OWN(A)+SUBTREE(B) = %+v, esperava Total %+v", sum, wantTotal)
	}
}

// TestAggregateTwoTracesIsolated prova o isolamento entre trajectórias distintas.
func TestAggregateTwoTracesIsolated(t *testing.T) {
	spans := []SpanData{
		chatSpan(1, 2, 1, 10, 5, 1200),
		chatSpan(2, 2, 1, 7, 4, 800),
	}
	agg := AggregateByTrace(spans)
	if len(agg) != 2 {
		t.Fatalf("esperava 2 trajectórias, obtive %d", len(agg))
	}
	if agg[spans[0].SpanContext.TraceIDHex()].CostMicroUSD != 1200 {
		t.Fatalf("trace 1 contaminado: %+v", agg[spans[0].SpanContext.TraceIDHex()])
	}
	if agg[spans[1].SpanContext.TraceIDHex()].CostMicroUSD != 800 {
		t.Fatalf("trace 2 contaminado: %+v", agg[spans[1].SpanContext.TraceIDHex()])
	}
}

// TestCostFallbackFromUSD prova a tolerância do fallback: um span que só traz o USD
// float é convertido a micro-USD com round-half (1 micro-USD de tolerância).
func TestCostFallbackFromUSD(t *testing.T) {
	sd := SpanData{
		Name:        OpChat,
		SpanContext: SpanContext{TraceID: traceID(9), SpanID: spanID(2)},
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpChat},
			{Key: AttrInputTokens, Value: int64(1)},
			{Key: AttrOutputTokens, Value: int64(1)},
			{Key: AttrCostUSD, Value: 0.0012345}, // sem AttrCostMicroUSD
		},
	}
	got := costMicroUSDOf(sd)
	if got != 1234 && got != 1235 {
		t.Fatalf("fallback USD→micro-USD = %d, esperava ~1234/1235", got)
	}
}

// TestVelocitySignal prova o sinal cost/token velocity: taxas por-segundo (wall-clock)
// e por-turno, a partir de spans com timestamps reais.
func TestVelocitySignal(t *testing.T) {
	base := time.Unix(1000, 0).UnixNano()
	sec := int64(time.Second)
	// Dois turnos ao longo de 2 segundos de wall-clock: [1000,1000.5] e [1001,1002].
	spans := []SpanData{
		{
			Name:          OpChat,
			SpanContext:   SpanContext{TraceID: traceID(1), SpanID: spanID(2)},
			StartUnixNano: base,
			EndUnixNano:   base + sec/2,
			Attributes: []KeyValue{
				{Key: AttrOperationName, Value: OpChat},
				{Key: AttrInputTokens, Value: int64(100)},
				{Key: AttrOutputTokens, Value: int64(100)},
				{Key: AttrCostMicroUSD, Value: int64(1_000_000)},
			},
		},
		{
			Name:          OpChat,
			SpanContext:   SpanContext{TraceID: traceID(1), SpanID: spanID(3)},
			StartUnixNano: base + sec,
			EndUnixNano:   base + 2*sec,
			Attributes: []KeyValue{
				{Key: AttrOperationName, Value: OpChat},
				{Key: AttrInputTokens, Value: int64(100)},
				{Key: AttrOutputTokens, Value: int64(100)},
				{Key: AttrCostMicroUSD, Value: int64(1_000_000)},
			},
		},
	}
	v := VelocityByTrace(spans)[spans[0].SpanContext.TraceIDHex()]
	if v.Turns != 2 {
		t.Fatalf("turns = %d, esperava 2", v.Turns)
	}
	if v.Totals.TotalTokens() != 400 || v.Totals.CostMicroUSD != 2_000_000 {
		t.Fatalf("totais errados: %+v", v.Totals)
	}
	if v.WallClock != 2*time.Second {
		t.Fatalf("wall-clock = %v, esperava 2s", v.WallClock)
	}
	// 400 tokens / 2s = 200 tok/s; 2_000_000 microUSD / 2s = 1_000_000 microUSD/s.
	if v.TokensPerSecond() != 200 {
		t.Fatalf("tokens/s = %v, esperava 200", v.TokensPerSecond())
	}
	if v.CostMicroUSDPerSecond() != 1_000_000 {
		t.Fatalf("microUSD/s = %v, esperava 1_000_000", v.CostMicroUSDPerSecond())
	}
	if v.CostUSDPerSecond() != 1.0 {
		t.Fatalf("USD/s = %v, esperava 1.0", v.CostUSDPerSecond())
	}
	if v.CostMicroUSDPerTurn() != 1_000_000 {
		t.Fatalf("microUSD/turno = %v, esperava 1_000_000", v.CostMicroUSDPerTurn())
	}
	if v.TokensPerTurn() != 200 {
		t.Fatalf("tokens/turno = %v, esperava 200", v.TokensPerTurn())
	}
}

// TestVelocityNoClockPerTurnStillValid prova que sem relógio (RecordingTracer) as
// taxas por-segundo são 0 mas as por-turno mantêm-se válidas.
func TestVelocityNoClockPerTurn(t *testing.T) {
	spans := []SpanData{
		chatSpan(1, 2, 1, 100, 100, 1_000_000), // sem timestamps
		chatSpan(1, 3, 1, 100, 100, 1_000_000),
	}
	v := VelocityByTrace(spans)[spans[0].SpanContext.TraceIDHex()]
	if v.WallClock != 0 {
		t.Fatalf("wall-clock = %v, esperava 0 (sem relógio)", v.WallClock)
	}
	if v.TokensPerSecond() != 0 || v.CostMicroUSDPerSecond() != 0 {
		t.Fatalf("taxas por-segundo deviam ser 0 sem relógio: %v %v", v.TokensPerSecond(), v.CostMicroUSDPerSecond())
	}
	if v.CostMicroUSDPerTurn() != 1_000_000 || v.TokensPerTurn() != 200 {
		t.Fatalf("por-turno inválido: %v %v", v.CostMicroUSDPerTurn(), v.TokensPerTurn())
	}

	// Guarda de zero turnos: as taxas por-turno devolvem 0 sem divisão por zero.
	var zero CostVelocity
	if zero.CostMicroUSDPerTurn() != 0 || zero.TokensPerTurn() != 0 {
		t.Fatalf("velocity vazio devia dar 0 por-turno: %v %v", zero.CostMicroUSDPerTurn(), zero.TokensPerTurn())
	}
}

// TestRecordedRollupAndVelocity cobre os wrappers RecordedSpan (Rollup/Velocity) e o
// helper CostUSD sobre um RecordingTracer (sem relógio: wall-clock 0, por-turno válido).
func TestRecordedRollupAndVelocity(t *testing.T) {
	tr := NewRecordingTracer(&SequentialIDGenerator{})
	ctx, agent := tr.StartSpan(context.Background(), OpInvokeAgent)
	agent.SetAttribute(AttrOperationName, OpInvokeAgent)
	_, c1 := tr.StartSpan(ctx, OpChat)
	c1.SetAttribute(AttrOperationName, OpChat)
	c1.SetAttribute(AttrInputTokens, int64(10))
	c1.SetAttribute(AttrOutputTokens, int64(5))
	c1.SetAttribute(AttrCostMicroUSD, int64(2_500_000))
	c1.End()
	agent.End()

	spans := tr.Spans()
	roll := RollupRecordedByTrace(spans)
	if len(roll) != 1 {
		t.Fatalf("esperava 1 trajectória no rollup, obtive %d", len(roll))
	}
	var tRoll TraceRollup
	for _, r := range roll {
		tRoll = r
	}
	if tRoll.Total.CostUSD() != 2.5 {
		t.Fatalf("CostUSD = %v, esperava 2.5", tRoll.Total.CostUSD())
	}
	vel := VelocityRecordedByTrace(spans)
	if len(vel) != 1 {
		t.Fatalf("esperava 1 trajectória em velocity, obtive %d", len(vel))
	}
	for _, v := range vel {
		if v.WallClock != 0 || v.TokensPerSecond() != 0 {
			t.Fatalf("sem relógio esperava wall-clock 0 e tokens/s 0: %+v", v)
		}
		if v.TokensPerTurn() != 15 {
			t.Fatalf("tokens/turno = %v, esperava 15", v.TokensPerTurn())
		}
	}
}

// TestAttrCoercions cobre a leitura robusta de atributos numéricos (int/int32/uint64 e
// float32) que a emissão pode produzir por variação de tipo.
func TestAttrCoercions(t *testing.T) {
	for _, v := range []any{int(7), int32(7), uint64(7), int64(7)} {
		if n, ok := attrInt64(v); !ok || n != 7 {
			t.Fatalf("attrInt64(%T) = %d,%v", v, n, ok)
		}
	}
	if _, ok := attrInt64("x"); ok {
		t.Fatal("attrInt64(string) devia falhar")
	}
	// uint64 acima de MaxInt64 é leitura inválida (transbordaria para negativo): a
	// coerção rejeita (ok=false) em vez de devolver um total negativo silencioso.
	if n, ok := attrInt64(uint64(math.MaxInt64) + 1); ok {
		t.Fatalf("attrInt64(uint64>MaxInt64) devia falhar, deu %d", n)
	}
	if f, ok := attrFloat64(float32(1.5)); !ok || f != 1.5 {
		t.Fatalf("attrFloat64(float32) = %v,%v", f, ok)
	}
	if _, ok := attrFloat64("x"); ok {
		t.Fatal("attrFloat64(string) devia falhar")
	}
	// span chat cujos tokens vêm como int (não int64): a coerção mantém a contagem.
	sd := SpanData{
		Name:        OpChat,
		SpanContext: SpanContext{TraceID: traceID(3), SpanID: spanID(2)},
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpChat},
			{Key: AttrInputTokens, Value: int(4)},
			{Key: AttrOutputTokens, Value: int32(2)},
			{Key: AttrCostMicroUSD, Value: int64(500)},
		},
	}
	agg := AggregateByTrace([]SpanData{sd})[sd.SpanContext.TraceIDHex()]
	if agg.InputTokens != 4 || agg.OutputTokens != 2 || agg.CostMicroUSD != 500 {
		t.Fatalf("coerção de tokens no chat falhou: %+v", agg)
	}
}

// TestRecordedTracerAggregation prova a agregação sobre os spans de um RecordingTracer
// (o caminho que o RT/RM usam nos testes) — via RecordedSpan → SpanData.
func TestRecordedTracerAggregation(t *testing.T) {
	tr := NewRecordingTracer(&SequentialIDGenerator{})
	ctx, agent := tr.StartSpan(context.Background(), OpInvokeAgent)
	agent.SetAttribute(AttrOperationName, OpInvokeAgent)
	for _, tok := range []struct{ in, out, cost int64 }{{10, 5, 1200}, {8, 3, 900}} {
		_, cs := tr.StartSpan(ctx, OpChat)
		cs.SetAttribute(AttrOperationName, OpChat)
		cs.SetAttribute(AttrInputTokens, tok.in)
		cs.SetAttribute(AttrOutputTokens, tok.out)
		cs.SetAttribute(AttrCostMicroUSD, tok.cost)
		cs.End()
	}
	agent.SetAttribute(AttrInputTokens, int64(18)) // agregado no invoke_agent
	agent.SetAttribute(AttrOutputTokens, int64(8))
	agent.SetAttribute(AttrCostMicroUSD, int64(2100))
	agent.End()

	agg := AggregateRecordedByTrace(tr.Spans())
	if len(agg) != 1 {
		t.Fatalf("esperava 1 trajectória, obtive %d", len(agg))
	}
	for _, v := range agg {
		if v.CostMicroUSD != 2100 || v.InputTokens != 18 || v.OutputTokens != 8 {
			t.Fatalf("agregação sobre RecordedSpan errada: %+v", v)
		}
	}
}
