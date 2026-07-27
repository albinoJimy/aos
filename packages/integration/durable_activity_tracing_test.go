package integration

// AOS-210 — o dispatcher durável que ESTE composition root constrói internamente
// (activity.Dispatcher, AOS-021) recebe agora o tracer por uma via EXPLÍCITA
// ([SecuredConfig.Tracer]). Estes testes são a prova de WIRING ao nível do pacote, nos
// DOIS sentidos:
//
//   - com Tracer: o span aos.activity é aberto e é PAI do execute_tool (mesmo trace_id,
//     parent_span_id == span_id do aos.activity), e o execute_tool aparece EXACTAMENTE
//     UMA VEZ — o risco que o comentário de [activity.OpActivity] nomeia (duplicar o span
//     quando o mesmo tracer é partilhado com o RM) NÃO se materializa;
//   - sem Tracer: ZERO spans aos.activity — o dispatcher fica com [agentruntime.NoopTracer]
//     e o comportamento é o de antes de AOS-210 (retro-compatibilidade estrita).
//
// A prova corre sobre a CADEIA REAL de [NewSecuredRuntime] no seu estado fail-closed
// (sem token NHI ⇒ a identidade nega). Isso é DELIBERADO e suficiente aqui: tanto o
// aos.activity (aberto em [activity.Dispatcher.Dispatch] antes de mediar) como o
// execute_tool (aberto pelo RM dentro de Mediate, antes de avaliar a cadeia) existem
// qualquer que seja o veredicto — a TOPOLOGIA é o que está sob prova, e a negação torna
// o teste independente de provisionamento de identidade/política. A prova da árvore no
// caminho PERMITIDO, a partir do NÓ e contra um colector OTLP real, vive em
// packages/cmd/aos/observability_durable_test.go.

import (
	"context"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/activity"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/provenance"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/toolset"
	"github.com/aos-ref/substrate/eventstore"
)

// durableTracingRun compõe um [SecuredRuntime] com execução durável ligada (Ledger
// sobre um Event Store) e corre um run de dois turnos cujo 1.º turno emite UMA tool
// call. tracer é entregue ao RT/RM via RuntimeOptions (a via já existente) e, quando
// wireChainTracer, TAMBÉM ao composition root via [SecuredConfig.Tracer] (a via nova
// de AOS-210). Devolve o número de execuções REAIS da tool.
func durableTracingRun(t *testing.T, tracer agentruntime.Tracer, wireChainTracer bool, runID string) int {
	t.Helper()
	ctx := context.Background()

	signer := testSigner(t)
	auditStore := audit.NewMemStore()
	trust := newTrust(t, ctx, auditStore, signer)
	entry := signedEntry(t, signer, "echo", "1.0.0", domain.Contract{Egress: domain.EgressNone})
	rv := newRevalidator(t, trust, auditStore,
		NewProvenanceQuarantiner(provenance.NewPartition(nil), WithQuarantineClock(fixedClock())),
		NewRecordingAlerter())

	trajStore, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New (trajectória): %v", err)
	}
	t.Cleanup(func() { _ = trajStore.Close() })

	ledgerStore, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New (ledger): %v", err)
	}
	t.Cleanup(func() { _ = ledgerStore.Close() })
	ledger, err := durable.NewStepLedger(ledgerStore)
	if err != nil {
		t.Fatalf("durable.NewStepLedger: %v", err)
	}

	cfg := SecuredConfig{
		Model:         &scriptedModel{responses: toolThenFinal("echo", []byte("ola"))},
		Recorder:      agentruntime.NewTurnRecorder(trajStore),
		Catalog:       &fakeCatalog{entries: []domain.Entry{entry}},
		Revalidator:   rv,
		Policy:        StaticPolicy{MaxEgress: domain.EgressExternal},
		WORM:          audit.NewMemStore(),
		Ledger:        ledger, // ⇒ dispatcher DURÁVEL (o alvo do ticket)
		FreezeOptions: []toolset.Option{toolset.WithClock(fixedClock())},
		// Via JÁ EXISTENTE: o RT partilha o tracer com o RM (execute_tool). Está sempre
		// ligada — é o que torna o teste "sem Tracer" NÃO-VACUOSO: o tracer chega ao
		// processo, apenas não chega ao dispatcher.
		RuntimeOptions: []agentruntime.Option{agentruntime.WithTracer(tracer)},
	}
	if wireChainTracer {
		cfg.Tracer = tracer // via NOVA (AOS-210)
	}

	sec, err := NewSecuredRuntime(cfg)
	if err != nil {
		t.Fatalf("NewSecuredRuntime: %v", err)
	}
	execs := 0
	if err := sec.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		execs++
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	res, _, err := sec.Run(ctx, testGoal(runID), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Terminated || res.Turns != 2 {
		t.Fatalf("desfecho inesperado: terminated=%v turns=%d", res.Terminated, res.Turns)
	}
	// ANTI-VACUIDADE: sem tool call emitida e mediada, "não vi spans" seria
	// indistinguível de "nunca houve nada a instrumentar".
	if len(res.ToolResults) != 1 {
		t.Fatalf("o modelo devia ter emitido EXACTAMENTE 1 tool call mediada, veio %d", len(res.ToolResults))
	}
	_, denials, _ := sec.Metrics().Snapshot()
	if denials < 1 {
		t.Fatalf("esperava a call NEGADA pela cadeia real fail-closed (denials=%d) — sem mediação não há span a asserir", denials)
	}
	return execs
}

// TestAOS210_DurableDispatcherTracerProducesActivityParentOfExecuteTool prova o FECHO do
// defeito: com [SecuredConfig.Tracer] preenchido, o dispatcher durável abre o span
// aos.activity e ele é PAI do execute_tool aberto pelo RM — a camada intermédia da árvore
// (dedup/replay/custo do efeito real) deixa de faltar.
func TestAOS210_DurableDispatcherTracerProducesActivityParentOfExecuteTool(t *testing.T) {
	rec := &agentruntime.RecordingTracer{}
	if execs := durableTracingRun(t, rec, true, "run-aos210-wired"); execs != 0 {
		t.Fatalf("a tool não devia executar sob a cadeia fail-closed sem NHI, correu %d vezes", execs)
	}

	acts := rec.SpansByOperation(activity.OpActivity)
	if len(acts) != 1 {
		t.Fatalf("esperava EXACTAMENTE 1 span %q (uma tool call mediada), vieram %d — sem ele a árvore não tem a camada de dedup/replay/custo",
			activity.OpActivity, len(acts))
	}
	act := acts[0]
	if !act.SpanContext.IsValid() {
		t.Fatalf("span %q com SpanContext inválido — não pode ser pai de ninguém", activity.OpActivity)
	}
	if !act.Ended {
		t.Errorf("span %q não foi fechado", activity.OpActivity)
	}

	// NÃO-DUPLICAÇÃO (o risco que dispatch.go nomeia): partilhar o tracer com o RM não
	// pode fazer o execute_tool sair duas vezes.
	tools := rec.SpansByOperation(agentruntime.OpExecuteTool)
	if len(tools) != 1 {
		t.Fatalf("esperava EXACTAMENTE 1 span %q, vieram %d — DUPLICAR o span de tool seria pior que o defeito original (duplo-contar em agregadores por-operação)",
			agentruntime.OpExecuteTool, len(tools))
	}

	// TOPOLOGIA: mesmo trace, e o execute_tool aponta o aos.activity como pai.
	tool := tools[0]
	if tool.SpanContext.TraceID != act.SpanContext.TraceID {
		t.Fatalf("%q e %q em traces DIFERENTES (%s != %s) — a árvore está partida",
			agentruntime.OpExecuteTool, activity.OpActivity, tool.SpanContext.TraceIDHex(), act.SpanContext.TraceIDHex())
	}
	if tool.ParentSpanID != act.SpanContext.SpanID {
		t.Fatalf("%q com parent_span_id=%x, esperava %x (span_id de %q) — o aos.activity TEM de nascer PAI do execute_tool",
			agentruntime.OpExecuteTool, tool.ParentSpanID, act.SpanContext.SpanID, activity.OpActivity)
	}
	// E o aos.activity é ele próprio filho do invoke_agent do run (não um span órfão).
	agents := rec.SpansByOperation(agentruntime.OpInvokeAgent)
	if len(agents) != 1 {
		t.Fatalf("esperava 1 span %q, vieram %d", agentruntime.OpInvokeAgent, len(agents))
	}
	if act.ParentSpanID != agents[0].SpanContext.SpanID {
		t.Fatalf("%q com parent_span_id=%x, esperava %x (span_id de %q)",
			activity.OpActivity, act.ParentSpanID, agents[0].SpanContext.SpanID, agentruntime.OpInvokeAgent)
	}

	// O desfecho DURÁVEL é anotado no span certo — é a informação que o RM não conhece.
	if v, ok := act.Attributes[activity.AttrDecision]; !ok || v != "denied" {
		t.Errorf("%s.%s = %v (ok=%v), esperava \"denied\" (a call foi negada pela cadeia real)", activity.OpActivity, activity.AttrDecision, v, ok)
	}
}

// TestAOS210_WithoutChainTracerNoActivitySpanIsEmitted é a PROVA NEGATIVA de
// retro-compatibilidade: com [SecuredConfig.Tracer] em branco — exactamente o que o nó
// faz sem AOS_OTLP_ENDPOINT — o dispatcher durável fica com [agentruntime.NoopTracer] e
// NENHUM span aos.activity é emitido, ainda que o MESMO tracer esteja ligado ao RT/RM.
//
// NÃO-VACUOSO: o execute_tool CONTINUA a sair (o tracer chegou mesmo ao processo), pelo
// que a ausência do aos.activity é atribuível ao dispatcher e não a "não houve run".
func TestAOS210_WithoutChainTracerNoActivitySpanIsEmitted(t *testing.T) {
	rec := &agentruntime.RecordingTracer{}
	if execs := durableTracingRun(t, rec, false, "run-aos210-legacy"); execs != 0 {
		t.Fatalf("a tool não devia executar sob a cadeia fail-closed sem NHI, correu %d vezes", execs)
	}

	if acts := rec.SpansByOperation(activity.OpActivity); len(acts) != 0 {
		t.Fatalf("sem SecuredConfig.Tracer o dispatcher TEM de ficar com NoopTracer: vieram %d spans %q (retro-compatibilidade quebrada)",
			len(acts), activity.OpActivity)
	}
	if tools := rec.SpansByOperation(agentruntime.OpExecuteTool); len(tools) != 1 {
		t.Fatalf("esperava 1 span %q (o tracer ESTÁ ligado ao RT/RM) — vieram %d; sem ele a ausência de %q seria vacuosa",
			agentruntime.OpExecuteTool, len(tools), activity.OpActivity)
	}
}
