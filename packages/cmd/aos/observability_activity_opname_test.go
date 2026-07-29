package main

// AOS-211 — os dois atributos em falta no `aos.activity` agora exportado pelo nó.
//
// # O que este ficheiro prova (contra um colector OTLP httptest, com -race)
//
// AOS-210 pôs o `aos.activity` na árvore EXPORTADA PELO NÓ com execução durável. Uma vez
// lá, duas lacunas do span deixaram de ser teóricas. AOS-211 fecha a primeira e nomeia a
// segunda:
//
//  1. EIXO 1 (gen_ai.operation.name) — ENTREGUE. O `aos.activity` exportado traz agora
//     `gen_ai.operation.name = aos.activity`. Antes, `startSpan` anotava tool/run_id/step_id
//     mas NÃO a operação, pelo que `otelgenai.ValidateSpanData` resolvia a operação por
//     fallback ao Name, não encontrava contrato e ACEITAVA o span sem validar — o único span
//     da árvore durável isento da semconv de AOS-076. A prova de conformidade ao nível do
//     CONTRATO (falha-antes/passa-depois) vive em otel-genai/contract_test.go
//     (TestAOS211_ActivityUnderContractIsNotVacuouslyAccepted); aqui prova-se ao nível do NÓ
//     que o atributo aparece MESMO no wire OTLP.
//
//  2. EIXO 2 (custo por efeito real) — ENTREGUE por AOS-212 (Decision.CostMicroUSD +
//     RegisterCosting + canal lateral no dispatcher). Aqui a tool de referência "counter" é
//     registada por Register (SEM custo, o caso HONESTO das tools de referência de
//     produção), pelo que o span continua a NÃO trazer `gen_ai.usage.cost_usd` — agora como
//     RETRO-COMPATIBILIDADE (custo == 0 não emite), não como selo de deferimento. A prova
//     POSITIVA do custo (a tool reporta C ⇒ o span traz C, uma vez; dedup/replay ⇒ 0) vive
//     em observability_activity_cost_test.go.

import (
	"net/http/httptest"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/activity"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// TestAOS211_ExportedActivitySpanCarriesOperationName é a prova ao NÍVEL DO NÓ do EIXO 1:
// com AOS_DURABLE_EXECUTION ligado e a observabilidade activa, o `aos.activity` que o nó
// exporta traz `gen_ai.operation.name = aos.activity` — e NÃO traz custo (EIXO 2 deferido).
func TestAOS211_ExportedActivitySpanCarriesOperationName(t *testing.T) {
	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	got := runDurableToolRun(t, srv.URL, "run-obs-durable-opname")
	assertDurableRunHealthy(t, got)

	spans := col.spans(t)
	if len(spans) == 0 {
		t.Fatal("nenhum span exportado — a observabilidade nao fluiu ponta-a-ponta")
	}

	acts := obsSpansNamed(spans, activity.OpActivity)
	if len(acts) != 1 {
		t.Fatalf("esperava EXACTAMENTE 1 span %q na arvore exportada, vieram %d; nomes vistos: %v",
			activity.OpActivity, len(acts), names(spans))
	}
	act := acts[0]

	// EIXO 1: o atributo que faltava. Sem ele o span era vacuosamente aceite pelo contrato
	// semconv (fallback ao Name), e consumidores que leem estritamente o atributo (ex.
	// operationOf de platform/eval) nunca o viam como operação.
	v, ok := act.attr(otelgenai.AttrOperationName)
	if !ok {
		t.Fatalf("%s exportado SEM %s — o defeito EIXO 1 de AOS-211 voltou (startSpan nao anota a operacao)",
			activity.OpActivity, otelgenai.AttrOperationName)
	}
	if v != activity.OpActivity {
		t.Errorf("%s.%s = %q, esperava %q — o nome da operacao tem de ser a propria string aos.activity",
			activity.OpActivity, otelgenai.AttrOperationName, v, activity.OpActivity)
	}

	// EIXO 2 (RETRO-COMPAT após AOS-212): a tool de referência "counter" é registada SEM
	// custo (Register), logo o span NÃO emite gen_ai.usage.cost_usd — custo == 0 não emite.
	// Selar a ausência apanha uma regressão que forjasse um custo onde a tool não reporta
	// nenhum. A prova POSITIVA (tool reporta C ⇒ span traz C) está em
	// observability_activity_cost_test.go.
	if cv, has := act.attr(otelgenai.AttrCostUSD); has {
		t.Errorf("%s NAO devia trazer %s com uma tool sem custo (Register reporta 0), veio %q",
			activity.OpActivity, otelgenai.AttrCostUSD, cv)
	}

	// A árvore durável continua sem segredos (retro-compat de AOS-210).
	assertNoSecrets(t, spans)
}
