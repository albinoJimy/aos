package agentruntime

import (
	"context"
	"strings"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// newStoreT abre um Event Store real com cleanup (o recorder de turnos precisa de um).
func newStoreT(t *testing.T) *eventstore.Store {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// finalTextModel devolve sempre uma resposta final com o texto dado (sem tools):
// um sub-agente que termina num turno. Serve para árvores de delegação limpas.
func finalTextModel(text string) ModelClient {
	return ModelClientFunc(func(_ context.Context, _ PromptView) (ModelResponse, error) {
		return ModelResponse{Text: text, Final: true, Usage: Usage{InputTokens: 3, OutputTokens: 2}, CostMicroUSD: 100}, nil
	})
}

// runLevel corre um nível da árvore de delegação partilhando o MESMO tracer
// (backend). Semeia o ctx a partir de parentSeed (o traceparent do invoke_agent do
// nível anterior — o veículo cross-fronteira) e devolve o resumo + o traceparent do
// SEU invoke_agent (para o nível seguinte parentear sob ele).
func runLevel(t *testing.T, tracer *RecordingTracer, runID, parentSeed, finalText string) (TrajectorySummary, string) {
	t.Helper()
	store := newStoreT(t)
	rm := referencemonitor.New()
	rt := New(finalTextModel(finalText), rm, NewTurnRecorder(store), WithTracer(tracer))
	goal := Goal{
		RunID:             runID,
		Principal:         referencemonitor.Principal{NHIID: "nhi:" + runID},
		Model:             ModelConfig{ModelID: "claude-opus-4-8"},
		System:            "sub-agente",
		Objective:         "trabalha",
		ParentTraceParent: parentSeed,
	}
	res, summary, err := rt.RunDelegated(context.Background(), goal, 2000)
	if err != nil {
		t.Fatalf("RunDelegated(%s): %v", runID, err)
	}
	// O invoke_agent DESTE run é a âncora do nível seguinte: encontra-o por run_id.
	var seed string
	for _, s := range tracer.SpansByOperation(OpInvokeAgent) {
		if s.Attributes[AttrRunID] == runID {
			seed = FormatTraceParent(s.SpanContext)
		}
	}
	if seed == "" {
		t.Fatalf("invoke_agent do run %s não encontrado no backend", runID)
	}
	_ = res
	return summary, seed
}

// TestDelegationTreeThreeLevels é o teste de INTEGRAÇÃO de AOS-077: um run
// pai→filho→neto produz uma árvore de 3 NÍVEIS no backend (tracer partilhado),
// ligada por trace_id comum + parent_span_id em cada aresta, enquanto o CONTEXTO do
// pai recebe SÓ o resumo higienizado de cada sub-agente — não a árvore.
func TestDelegationTreeThreeLevels(t *testing.T) {
	tracer := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})

	// Nível 1 (pai): run-raiz, sem seed.
	_, parentSeed := runLevel(t, tracer, "run-parent", "", "objectivo do utilizador")
	// Nível 2 (filho): semeado a partir do invoke_agent do pai.
	childSummary, childSeed := runLevel(t, tracer, "run-child", parentSeed, strings.Repeat("detalhe do filho. ", 500))
	// Nível 3 (neto): semeado a partir do invoke_agent do filho.
	grandSummary, _ := runLevel(t, tracer, "run-grandchild", childSeed, strings.Repeat("detalhe do neto. ", 500))

	// --- (1) Árvore de 3 níveis no backend, um só trace_id, arestas por span_id. ---
	invokes := tracer.SpansByOperation(OpInvokeAgent)
	byRun := map[string]*RecordedSpan{}
	for _, s := range invokes {
		byRun[s.Attributes[AttrRunID].(string)] = s
	}
	parent, child, grand := byRun["run-parent"], byRun["run-child"], byRun["run-grandchild"]
	if parent == nil || child == nil || grand == nil {
		t.Fatalf("faltam invoke_agent de algum nível: %+v", byRun)
	}
	// Trace_id comum a toda a árvore.
	if child.SpanContext.TraceID != parent.SpanContext.TraceID || grand.SpanContext.TraceID != parent.SpanContext.TraceID {
		t.Fatalf("trace_id não é comum aos 3 níveis")
	}
	// Aresta filho→pai e neto→filho por span_id (ligação NATIVA, não por atributo NHI).
	if child.ParentSpanID != parent.SpanContext.SpanID {
		t.Fatalf("invoke_agent do filho não parenteia sob o pai por span_id")
	}
	if grand.ParentSpanID != child.SpanContext.SpanID {
		t.Fatalf("invoke_agent do neto não parenteia sob o filho por span_id")
	}
	// A raiz (pai) não tem pai.
	if parent.ParentSpanID != ([8]byte{}) {
		t.Fatalf("invoke_agent do pai não devia ter parent_span_id (é raiz)")
	}

	// A sub-árvore de cada nível está COMPLETA no backend: o span chat de cada run
	// existe e partilha o trace, ligado ao seu invoke_agent (nada se perde).
	for _, runID := range []string{"run-parent", "run-child", "run-grandchild"} {
		var chat *RecordedSpan
		for _, s := range tracer.SpansByOperation(OpChat) {
			if s.Attributes[AttrRunID] == runID {
				chat = s
			}
		}
		if chat == nil {
			t.Fatalf("chat do run %s ausente no backend (trajectória perdida)", runID)
		}
		if chat.SpanContext.TraceID != parent.SpanContext.TraceID {
			t.Fatalf("chat do run %s não partilha o trace comum", runID)
		}
		if chat.ParentSpanID != byRun[runID].SpanContext.SpanID {
			t.Fatalf("chat do run %s não parenteia sob o seu invoke_agent", runID)
		}
	}

	// --- (2) Propriedade estrutural: acíclica + completamente ligada. ---
	assertConnectedAcyclicTree(t, tracer.Spans())

	// --- (3) Contexto do pai ≠ registo: só o resumo volta, e é PEQUENO. ---
	// O filho executou centenas de repetições (árvore rica no backend), mas o resumo
	// devolvido está truncado ao tecto de tokens — artefacto distinto da árvore.
	if !childSummary.Truncated {
		t.Fatalf("resumo do filho devia estar truncado ao tecto de higiene")
	}
	if got := len([]rune(childSummary.Text)); got > childSummary.MaxTokens*approxTokensPerBudgetUnit {
		t.Fatalf("resumo do filho excede o tecto: %d runes", got)
	}
	if childSummary.RunID != "run-child" || grandSummary.RunID != "run-grandchild" {
		t.Fatalf("resumo não correlaciona com o run: %q / %q", childSummary.RunID, grandSummary.RunID)
	}
	// O resumo NÃO carrega spans: é um artefacto distinto da árvore no backend.
	// (A árvore vive no tracer; o resumo é só texto+metadados de desfecho.)
	if childSummary.Text == "" || !childSummary.Terminated {
		t.Fatalf("resumo do filho mal-formado: %+v", childSummary)
	}
}

// TestSummarizeBoundsAndDefaults cobre os limites do artefacto de resumo: o default
// aplica-se com maxTokens ≤ 0, texto curto não é truncado, e texto longo é.
func TestSummarizeBoundsAndDefaults(t *testing.T) {
	// maxTokens ≤ 0 ⇒ DefaultSummaryMaxTokens; texto curto ⇒ sem truncagem.
	short := Result{RunID: "r1", FinalText: "curto", Turns: 1, Terminated: true}.Summarize(0)
	if short.MaxTokens != DefaultSummaryMaxTokens {
		t.Fatalf("default não aplicado: MaxTokens=%d", short.MaxTokens)
	}
	if short.Truncated || short.Text != "curto" {
		t.Fatalf("texto curto não devia ser truncado: %+v", short)
	}
	// Texto exactamente no limite ⇒ sem truncagem; acima ⇒ truncado.
	limitRunes := 3 * approxTokensPerBudgetUnit
	exact := Result{FinalText: strings.Repeat("x", limitRunes)}.Summarize(3)
	if exact.Truncated {
		t.Fatalf("texto no limite exacto não devia truncar")
	}
	over := Result{FinalText: strings.Repeat("x", limitRunes+1)}.Summarize(3)
	if !over.Truncated || len([]rune(over.Text)) != limitRunes {
		t.Fatalf("texto acima do limite mal truncado: trunc=%v len=%d", over.Truncated, len([]rune(over.Text)))
	}
}

// TestRunSeedMalformedTraceParentFailOpen prova que um ParentTraceParent malformado
// é ignorado (fail-open): o run corre como raiz (invoke_agent sem parent_span_id),
// sem erro — a perda da ligação ao pai nunca aborta a trajectória do filho.
func TestRunSeedMalformedTraceParentFailOpen(t *testing.T) {
	tracer := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	rt := New(finalTextModel("ok"), referencemonitor.New(), NewTurnRecorder(newStoreT(t)), WithTracer(tracer))
	goal := Goal{
		RunID:             "run-bad-seed",
		Principal:         referencemonitor.Principal{NHIID: "nhi:x"},
		Model:             ModelConfig{ModelID: "m"},
		Objective:         "trabalha",
		ParentTraceParent: "lixo-nao-e-traceparent",
	}
	res, err := rt.Run(context.Background(), goal)
	if err != nil {
		t.Fatalf("Run com seed malformado devia correr sem erro: %v", err)
	}
	if !res.Terminated {
		t.Fatalf("run devia terminar: %+v", res)
	}
	inv := tracer.SpansByOperation(OpInvokeAgent)
	if len(inv) != 1 || inv[0].ParentSpanID != ([8]byte{}) {
		t.Fatalf("seed malformado devia deixar o invoke_agent como raiz: %+v", inv)
	}
}

// assertConnectedAcyclicTree espelha a verificação de propriedade do otel-genai
// sobre a lista de spans do RecordingTracer: um só trace_id, uma raiz, cada não-raiz
// com pai presente, sem ciclos, span_ids únicos.
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
			t.Fatalf("trace_id divergente (span %d)", i)
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
	for _, s := range spans {
		steps := 0
		for cur := s; cur.ParentSpanID != ([8]byte{}); {
			parent, ok := byID[cur.ParentSpanID]
			if !ok {
				t.Fatalf("span %x aponta a pai ausente %x", cur.SpanContext.SpanID, cur.ParentSpanID)
			}
			cur = parent
			if steps++; steps > len(spans) {
				t.Fatalf("ciclo detectado a partir de %x", s.SpanContext.SpanID)
			}
		}
	}
}
