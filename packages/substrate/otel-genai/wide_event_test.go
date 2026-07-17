package otelgenai

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// richToolSpan constrói um SpanData execute_tool com TODAS as dimensões que o wide
// event deve capturar: principal, decisão de política (PDP), taint, tokens/custo
// (herdados do turno para o exemplo), latência (via timestamps) e hashes.
func richToolSpan(traceB, spanB, parentB byte, start, end int64) SpanData {
	return SpanData{
		Name:          OpExecuteTool,
		SpanContext:   SpanContext{TraceID: traceID(traceB), SpanID: spanID(spanB)},
		ParentSpanID:  spanID(parentB),
		StartUnixNano: start,
		EndUnixNano:   end,
		Status:        Status{Code: StatusOK},
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpExecuteTool},
			{Key: AttrToolName, Value: "web.search"},
			{Key: AttrToolCallHash, Value: "abc123"},
			{Key: AttrPrincipalNHI, Value: "nhi:agent-42"},
			{Key: AttrDecision, Value: "permit"},
			{Key: AttrDeniedBy, Value: ""},
			{Key: AttrTaint, Value: "trusted"},
			{Key: AttrResultTaint, Value: "untrusted"},
			{Key: AttrRunID, Value: "run-7"},
			{Key: AttrStepID, Value: "step-3"},
		},
	}
}

// richChatSpan constrói um SpanData chat com modelo, tokens, custo, principal e
// timestamps (latência).
func richChatSpan(traceB, spanB, parentB byte, model string, in, out, microUSD, start, end int64) SpanData {
	return SpanData{
		Name:          OpChat,
		SpanContext:   SpanContext{TraceID: traceID(traceB), SpanID: spanID(spanB)},
		ParentSpanID:  spanID(parentB),
		StartUnixNano: start,
		EndUnixNano:   end,
		Status:        Status{Code: StatusOK},
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpChat},
			{Key: AttrRequestModel, Value: model},
			{Key: AttrPrincipalNHI, Value: "nhi:agent-42"},
			{Key: AttrInputTokens, Value: in},
			{Key: AttrOutputTokens, Value: out},
			{Key: AttrCostMicroUSD, Value: microUSD},
			{Key: AttrCostUSD, Value: MicroUSDToUSD(microUSD)},
			{Key: AttrPromptHash, Value: "ph"},
			{Key: AttrPrefixHash, Value: "xh"},
		},
	}
}

// pinnedVersions é o enriquecimento típico de versões pinadas do manifesto.
func pinnedVersions(tenant string) map[string]any {
	return map[string]any{
		AttrTenantID:                   tenant,
		PinnedVersionPrefix + "skill":  "search@1.4.2",
		PinnedVersionPrefix + "tool":   "web@2.0.0",
		PinnedVersionPrefix + "memory": "mem@3.1.0",
		PinnedVersionPrefix + "policy": "cedar@0.9.0", // dimensão pinada NOVA, capturada sem alterar código
	}
}

// TestWideEventCapturesAllDimensions é o teste de INTEGRAÇÃO: uma unidade de
// trabalho projecta-se num wide event com TODAS as dimensões exigidas — principal
// (NHI), modelo, tokens, custo, latência, decisão PDP, taint e versões pinadas —
// derivadas em campos tipados E presentes no bag completo (nada descartado).
func TestWideEventCapturesAllDimensions(t *testing.T) {
	const start, end = int64(1_000), int64(1_500) // latência = 500ns
	// Um span chat carrega modelo/tokens/custo; enriquecemos com tenant + versões
	// pinadas (que vivem no manifesto, não no span de modelo).
	sd := richChatSpan(1, 2, 1, "opus-4", 120, 40, 3_500_000, start, end)
	// Anota também a decisão PDP e o taint no bag (dimensões que um chat pode não
	// trazer, mas o wide event captura se presentes).
	sd.Attributes = append(sd.Attributes,
		KeyValue{Key: AttrDecision, Value: "permit"},
		KeyValue{Key: AttrTaint, Value: "trusted"},
		KeyValue{Key: AttrResultTaint, Value: "untrusted"},
	)

	w := WideEventFromSpanData(sd, pinnedVersions("acme"))

	// --- dimensões tipadas ---
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"principal (NHI)", w.PrincipalNHI, "nhi:agent-42"},
		{"tenant", w.TenantID, "acme"},
		{"modelo", w.Model, "opus-4"},
		{"input tokens", w.InputTokens, int64(120)},
		{"output tokens", w.OutputTokens, int64(40)},
		{"custo micro-USD", w.CostMicroUSD, int64(3_500_000)},
		{"latência ns", w.LatencyNanos, int64(500)},
		{"decisão PDP", w.Decision, "permit"},
		{"taint", w.Taint, "trusted"},
		{"result taint", w.ResultTaint, "untrusted"},
		{"operação", w.Operation, OpChat},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("dimensão %s = %v, esperava %v", c.name, c.got, c.want)
		}
	}
	if w.TotalTokens() != 160 {
		t.Errorf("total tokens = %d, esperava 160", w.TotalTokens())
	}
	if w.CostUSD() != 3.5 {
		t.Errorf("custo USD = %v, esperava 3.5", w.CostUSD())
	}
	if w.Latency() != 500*time.Nanosecond {
		t.Errorf("latência = %v, esperava 500ns", w.Latency())
	}

	// --- versões pinadas: todas as chaves aos.pinned.* recolhidas, incl. a NOVA ---
	wantPinned := map[string]string{
		PinnedVersionPrefix + "skill":  "search@1.4.2",
		PinnedVersionPrefix + "tool":   "web@2.0.0",
		PinnedVersionPrefix + "memory": "mem@3.1.0",
		PinnedVersionPrefix + "policy": "cedar@0.9.0",
	}
	if len(w.PinnedVersions) != len(wantPinned) {
		t.Fatalf("versões pinadas = %v, esperava %v", w.PinnedVersions, wantPinned)
	}
	for k, want := range wantPinned {
		if w.PinnedVersions[k] != want {
			t.Errorf("versão pinada %s = %q, esperava %q", k, w.PinnedVersions[k], want)
		}
	}

	// --- bag COMPLETO: nada descartado (span + enriquecimento) ---
	if _, ok := w.Attributes[AttrPromptHash]; !ok {
		t.Error("bag perdeu prompt_hash — houve descarte no emit-time (proibido)")
	}
	if _, ok := w.Attributes[AttrTenantID]; !ok {
		t.Error("bag não tem tenant_id enriquecido")
	}
	if w.Attributes[AttrCostUSD] == nil {
		t.Error("bag perdeu cost_usd — dimensão descartada")
	}

	// Marca de diagnóstico efémero.
	if !w.Ephemeral {
		t.Error("wide event devia ser marcado Ephemeral (diagnóstico, não audit)")
	}
}

// TestWideEventAdHocQueryCostPerTenantPerModel é o teste de QUERY: uma pergunta
// analítica NÃO prevista à instrumentação — "custo por tenant E por modelo" — é
// respondida por agregação AD HOC sobre os eventos JÁ recolhidos, sem
// reinstrumentar. A chave composta combina duas dimensões (tenant, modelo) que
// nenhuma agregação pré-existente cruzava.
func TestWideEventAdHocQueryCostPerTenantPerModel(t *testing.T) {
	events := []WideEvent{
		WideEventFromSpanData(richChatSpan(1, 2, 1, "opus-4", 10, 5, 1_000_000, 0, 0), pinnedVersions("acme")),
		WideEventFromSpanData(richChatSpan(1, 3, 1, "opus-4", 20, 5, 2_000_000, 0, 0), pinnedVersions("acme")),
		WideEventFromSpanData(richChatSpan(1, 4, 1, "haiku-4", 30, 5, 300_000, 0, 0), pinnedVersions("acme")),
		WideEventFromSpanData(richChatSpan(2, 2, 1, "opus-4", 40, 5, 4_000_000, 0, 0), pinnedVersions("globex")),
	}

	// Chave composta AD HOC — jamais prevista na instrumentação.
	key := func(w WideEvent) string { return w.TenantID + "|" + w.Model }
	byTenantModel := AggregateUsage(events, key)

	cases := map[string]int64{
		"acme|opus-4":   3_000_000, // 1M + 2M
		"acme|haiku-4":  300_000,
		"globex|opus-4": 4_000_000,
	}
	if len(byTenantModel) != len(cases) {
		t.Fatalf("grupos = %d (%v), esperava %d", len(byTenantModel), byTenantModel, len(cases))
	}
	for k, wantCost := range cases {
		if got := byTenantModel[k].CostMicroUSD; got != wantCost {
			t.Errorf("custo[%s] = %d, esperava %d", k, got, wantCost)
		}
	}

	// Outra pergunta ad-hoc sobre os MESMOS dados, sem reinstrumentar: só a acme,
	// agrupada por modelo, via Filter + AggregateUsage.
	acme := Filter(events, func(w WideEvent) bool { return w.TenantID == "acme" })
	if len(acme) != 3 {
		t.Fatalf("filtro acme = %d eventos, esperava 3", len(acme))
	}
	acmeByModel := AggregateUsage(acme, func(w WideEvent) string { return w.Model })
	if acmeByModel["opus-4"].CostMicroUSD != 3_000_000 || acmeByModel["haiku-4"].CostMicroUSD != 300_000 {
		t.Errorf("agregação acme por modelo errada: %+v", acmeByModel)
	}
	if acmeByModel["opus-4"].TotalTokens() != 40 { // (10+5)+(20+5)
		t.Errorf("tokens acme/opus-4 = %d, esperava 40", acmeByModel["opus-4"].TotalTokens())
	}

	// E uma terceira: latência total por operação via SumBy (métrica genérica).
	lat := SumBy(events, func(w WideEvent) string { return w.Operation }, func(w WideEvent) int64 { return w.LatencyNanos })
	if lat[OpChat] != 0 { // estes spans não têm relógio ⇒ latência 0
		t.Errorf("latência total chat = %d, esperava 0 (sem relógio)", lat[OpChat])
	}

	// GroupByWide também expõe as fatias cruas para inspecção ad-hoc.
	grouped := GroupByWide(events, key)
	if len(grouped["acme|opus-4"]) != 2 {
		t.Errorf("grupo acme|opus-4 tem %d eventos, esperava 2", len(grouped["acme|opus-4"]))
	}
}

// TestWideEventStoreTTLEviction prova a eviction por TTL com relógio injectável, e
// a separação ESTRUTURAL face ao audit WORM permanente (o store é destrutivo — a
// antítese do write-once-read-many).
func TestWideEventStoreTTLEviction(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	store := NewWideEventStore(60*time.Second, clock)

	// t=1000: insere dois eventos (expiram a t=1060).
	w1 := store.Record(richChatSpan(1, 2, 1, "opus-4", 10, 5, 1_000_000, 0, 0), pinnedVersions("acme"))
	store.Record(richChatSpan(1, 3, 1, "opus-4", 20, 5, 2_000_000, 0, 0), pinnedVersions("acme"))

	// O carimbo de TTL é now+60s — a marca EFÉMERA (o audit WORM não a tem).
	wantExpiry := now.Add(60 * time.Second).UnixNano()
	if w1.ExpiresAtUnixNano != wantExpiry {
		t.Fatalf("ExpiresAt = %d, esperava %d", w1.ExpiresAtUnixNano, wantExpiry)
	}
	if !w1.Ephemeral {
		t.Fatal("evento devia ser Ephemeral")
	}
	if store.Len() != 2 {
		t.Fatalf("vivos = %d, esperava 2", store.Len())
	}

	// t=1030 (< TTL): nada expira; os dados ainda respondem a queries.
	now = time.Unix(1030, 0)
	if store.Len() != 2 {
		t.Fatalf("a t=1030 vivos = %d, esperava 2 (dentro do TTL)", store.Len())
	}

	// t=1061 (> TTL): AMBOS expiram — a eviction é DESTRUTIVA.
	now = time.Unix(1061, 0)
	if reaped := store.Reap(); reaped != 2 {
		t.Fatalf("Reap descartou %d, esperava 2", reaped)
	}
	if store.Len() != 0 {
		t.Fatalf("após TTL vivos = %d, esperava 0 (eviction destrutiva)", store.Len())
	}
	if got := store.Events(); len(got) != 0 {
		t.Fatalf("Events após TTL = %d, esperava 0", len(got))
	}

	// Separação estrutural do WORM: um novo insert a t=1061 volta a expirar a
	// t=1121 — a perda por TTL é a norma, ao contrário do audit permanente. E o
	// store não expõe qualquer superfície de audit (hash-chain/assinatura): é só
	// diagnóstico efémero.
	w3 := store.Record(richChatSpan(1, 9, 1, "haiku-4", 1, 1, 100, 0, 0))
	if w3.ExpiresAtUnixNano != now.Add(60*time.Second).UnixNano() {
		t.Fatalf("re-insert ExpiresAt = %d, esperava %d", w3.ExpiresAtUnixNano, now.Add(60*time.Second).UnixNano())
	}
}

// TestWideEventStoreNoTTLKeepsEvents prova que ttl<=0 desliga a expiração (os
// eventos ficam), útil para uma janela de retenção gerida externamente.
func TestWideEventStoreNoTTLKeepsEvents(t *testing.T) {
	now := time.Unix(1000, 0)
	store := NewWideEventStore(0, func() time.Time { return now })
	w := store.Record(richToolSpan(1, 2, 1, 0, 0))
	if w.ExpiresAtUnixNano != 0 {
		t.Fatalf("sem TTL ExpiresAt devia ser 0, deu %d", w.ExpiresAtUnixNano)
	}
	now = time.Unix(999999, 0) // muito depois
	if store.Len() != 1 {
		t.Fatalf("sem TTL não devia expirar: vivos = %d", store.Len())
	}
	if store.Reap() != 0 {
		t.Fatal("sem TTL Reap não devia descartar nada")
	}

	// Clock nil ⇒ default time.Now: o store constrói-se e aceita eventos (com um TTL
	// real, os eventos ficam vivos dentro da janela).
	def := NewWideEventStore(time.Hour, nil)
	def.Record(richToolSpan(1, 2, 1, 0, 0))
	if def.Len() != 1 {
		t.Fatalf("store com clock default: vivos = %d, esperava 1", def.Len())
	}
}

// TestWideEventEnrichAfterProjection cobre o enriquecimento tardio (Enrich): junta
// dimensões ao bag e re-deriva os campos tipados, sem descartar nada.
func TestWideEventEnrichAfterProjection(t *testing.T) {
	w := WideEventFromSpanData(richChatSpan(1, 2, 1, "opus-4", 10, 5, 1_000_000, 100, 700))
	if w.TenantID != "" {
		t.Fatal("sem enriquecimento o tenant devia estar vazio")
	}
	if w.LatencyNanos != 600 {
		t.Fatalf("latência = %d, esperava 600", w.LatencyNanos)
	}
	w.Enrich(map[string]any{
		AttrTenantID:                  "initech",
		PinnedVersionPrefix + "skill": "s@1",
	})
	if w.TenantID != "initech" {
		t.Fatalf("após Enrich tenant = %q, esperava initech", w.TenantID)
	}
	if w.PinnedVersions[PinnedVersionPrefix+"skill"] != "s@1" {
		t.Fatalf("após Enrich versão pinada em falta: %v", w.PinnedVersions)
	}
	// Não descartou o que já existia.
	if w.Model != "opus-4" || w.CostMicroUSD != 1_000_000 {
		t.Fatalf("Enrich descartou dimensões: model=%q cost=%d", w.Model, w.CostMicroUSD)
	}

	// Enrich sobre um WideEvent zero (bag nil) não deve entrar em pânico.
	var zero WideEvent
	zero.Enrich(map[string]any{AttrTenantID: "x"})
	if zero.TenantID != "x" {
		t.Fatal("Enrich sobre WideEvent zero falhou")
	}
}

// TestWideEventStoreRecordConcurrent prova que Record devolve SEMPRE o evento
// inserido pela PRÓPRIA chamada, mesmo sob inserções concorrentes: o carimbo é
// calculado dentro da secção crítica de Add e devolvido directamente, sem re-ler
// events[len-1] fora do lock (onde outra goroutine poderia ter inserido o seu). O
// -race torna a regressão determinável.
func TestWideEventStoreRecordConcurrent(t *testing.T) {
	now := time.Unix(1000, 0)
	store := NewWideEventStore(time.Hour, func() time.Time { return now })

	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make([]string, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			runID := fmt.Sprintf("run-%d", i)
			sd := richChatSpan(1, 2, 1, "opus-4", 1, 1, 100, 0, 0)
			sd.Attributes = append(sd.Attributes, KeyValue{Key: AttrRunID, Value: runID})
			got := store.Record(sd)
			if got.RunID != runID {
				errs[i] = fmt.Sprintf("Record devolveu RunID=%q, inseriu %q (evento de outra goroutine)", got.RunID, runID)
			}
			if !got.Ephemeral || got.ExpiresAtUnixNano != now.Add(time.Hour).UnixNano() {
				errs[i] = fmt.Sprintf("carimbo TTL/Ephemeral errado no retorno: %+v", got)
			}
		}(i)
	}
	wg.Wait()

	for _, e := range errs {
		if e != "" {
			t.Fatal(e)
		}
	}
	if store.Len() != n {
		t.Fatalf("estado do store = %d eventos, esperava %d", store.Len(), n)
	}
}

// TestWideEventCostFallbackFromUSD prova que o wide event deriva o custo do USD
// float quando o inteiro micro-USD está ausente (mesma regra da agregação).
func TestWideEventCostFallbackFromUSD(t *testing.T) {
	sd := SpanData{
		Name:        OpChat,
		SpanContext: SpanContext{TraceID: traceID(5), SpanID: spanID(2)},
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpChat},
			{Key: AttrRequestModel, Value: "opus-4"},
			{Key: AttrCostUSD, Value: 0.0012345}, // sem AttrCostMicroUSD
		},
	}
	w := WideEventFromSpanData(sd)
	if w.CostMicroUSD != 1234 && w.CostMicroUSD != 1235 {
		t.Fatalf("custo fallback = %d, esperava ~1234/1235", w.CostMicroUSD)
	}
}

// TestWideEventFromRecordingTracer prova o caminho RecordedSpan → SpanData →
// WideEvent (o que RT/RM usam), incluindo latência 0 sem relógio.
func TestWideEventFromRecordingTracer(t *testing.T) {
	tr := NewRecordingTracer(&SequentialIDGenerator{})
	_, chat := tr.StartSpan(t.Context(), OpChat)
	chat.SetAttribute(AttrOperationName, OpChat)
	chat.SetAttribute(AttrRequestModel, "opus-4")
	chat.SetAttribute(AttrPrincipalNHI, "nhi:x")
	chat.SetAttribute(AttrInputTokens, int64(7))
	chat.SetAttribute(AttrOutputTokens, int64(3))
	chat.SetAttribute(AttrCostMicroUSD, int64(500))
	chat.End()

	sd := tr.Spans()[0].ToSpanData()
	w := WideEventFromSpanData(sd, map[string]any{AttrTenantID: "t1"})
	if w.Model != "opus-4" || w.PrincipalNHI != "nhi:x" || w.TenantID != "t1" {
		t.Fatalf("dimensões erradas: %+v", w)
	}
	if w.TotalTokens() != 10 || w.CostMicroUSD != 500 {
		t.Fatalf("usage errado: tokens=%d cost=%d", w.TotalTokens(), w.CostMicroUSD)
	}
	if w.LatencyNanos != 0 {
		t.Fatalf("sem relógio a latência devia ser 0, deu %d", w.LatencyNanos)
	}
}
