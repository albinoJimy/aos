package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// harness monta o RT sobre o RM real (AOS-003) e o Event Store real (AOS-002).
type harness struct {
	store    *eventstore.Store
	rm       *referencemonitor.Monitor
	recorder *TurnRecorder
	tracer   *RecordingTracer
}

func newHarness(t *testing.T, tools map[string]referencemonitor.ToolFunc) *harness {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sink := referencemonitor.NewEventStoreSink(store)
	rm := referencemonitor.New(referencemonitor.WithEventSink(sink))
	for id, fn := range tools {
		if err := rm.Register(id, fn); err != nil {
			t.Fatalf("Register(%s): %v", id, err)
		}
	}
	return &harness{
		store:    store,
		rm:       rm,
		recorder: NewTurnRecorder(store),
		tracer:   &RecordingTracer{},
	}
}

func sampleGoal() Goal {
	return Goal{
		RunID: "run_7f3a9c2e",
		Principal: referencemonitor.Principal{
			NHIID:      "nhi:agent-1",
			AgentID:    "agent-1",
			AgentClass: "researcher",
			DelegationChain: []referencemonitor.DelegationHop{
				{Sub: "human:alice", ActAs: "nhi:agent-1"},
			},
			Authority: []string{"cap:echo"},
		},
		Scope:     []string{"cap:echo"},
		Model:     ModelConfig{ModelID: "claude-opus-4-8", Params: map[string]string{"temperature": "0"}, Seed: 42},
		System:    "És um agente de investigação do AOS.",
		Tools:     toolSet(),
		Skills:    []ToolSpec{{Name: "report_writer", Version: "2.3.1", Digest: "sha256:bb02"}},
		Objective: "Faz echo do input.",
	}
}

// TestRunEndToEnd é o percurso integral: montar → chamar (modelo mockado) →
// despachar (RM real) → verificar, gravando N eventos de turno no Event Store.
func TestRunEndToEnd(t *testing.T) {
	h := newHarness(t, map[string]referencemonitor.ToolFunc{
		"echo": func(_ context.Context, input []byte) ([]byte, error) {
			return append([]byte("echoed:"), input...), nil
		},
	})

	var prefixes [][]byte
	callN := 0
	model := ModelClientFunc(func(_ context.Context, view PromptView) (ModelResponse, error) {
		prefixes = append(prefixes, view.Prefix)
		callN++
		if callN == 1 {
			return ModelResponse{
				Text:         "vou chamar a tool echo",
				ToolCalls:    []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("ola")}},
				Usage:        Usage{InputTokens: 10, OutputTokens: 5},
				CostMicroUSD: 1200,
			}, nil
		}
		return ModelResponse{
			Text:         "concluído",
			Final:        true,
			Usage:        Usage{InputTokens: 8, OutputTokens: 3},
			CostMicroUSD: 900,
		}, nil
	})

	rt := New(model, h.rm, h.recorder, WithTracer(h.tracer))
	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Desfecho.
	if !res.Terminated || res.Turns != 2 || res.FinalText != "concluído" {
		t.Fatalf("desfecho inesperado: %+v", res)
	}
	if res.TotalUsage.InputTokens != 18 || res.TotalUsage.OutputTokens != 8 {
		t.Fatalf("uso agregado errado: %+v", res.TotalUsage)
	}
	if res.TotalCostMicroUSD != 2100 {
		t.Fatalf("custo agregado errado: %d", res.TotalCostMicroUSD)
	}

	// Taint: o resultado da tool voltou marcado untrusted (ADR-005).
	if len(res.ToolResults) != 1 {
		t.Fatalf("esperava 1 resultado de tool, obtive %d", len(res.ToolResults))
	}
	if !res.ToolResults[0].IsUntrusted() {
		t.Fatalf("resultado de tool devia estar untrusted, taint=%q", res.ToolResults[0].Taint)
	}
	if !bytes.Equal(res.ToolResults[0].Value, []byte("echoed:ola")) {
		t.Fatalf("valor do resultado errado: %q", res.ToolResults[0].Value)
	}

	// Prefixo byte-idêntico entre os dois turnos (regressão de cache no loop).
	if len(prefixes) != 2 || !bytes.Equal(prefixes[0], prefixes[1]) {
		t.Fatalf("prefixo do prompt não é byte-idêntico entre turnos")
	}

	// N eventos de turno gravados no Event Store real.
	events, err := h.store.Read(context.Background(), "run_7f3a9c2e", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	turns := filterType(events, EventTypeTurnRecorded)
	if len(turns) != 2 {
		t.Fatalf("esperava 2 eventos turn.recorded, obtive %d", len(turns))
	}
	if len(res.TurnSeqs) != 2 {
		t.Fatalf("esperava 2 TurnSeqs, obtive %d", len(res.TurnSeqs))
	}
	// A mediação da tool call também foi gravada pelo RM (no-bypass real).
	mediated := filterType(events, referencemonitor.EventTypeMediated)
	if len(mediated) != 1 {
		t.Fatalf("esperava 1 evento tool.call.mediated, obtive %d", len(mediated))
	}

	// Manifesto por trajectória completo no primeiro turno.
	assertManifest(t, turns[0], sampleGoal())

	// Cada turno tem step_id DISTINTO (evita dedup do Event Store).
	if turns[0].StepID == turns[1].StepID {
		t.Fatalf("step_id repetido entre turnos: %s", turns[0].StepID)
	}

	// Spans OTel GenAI: 1 invoke_agent, 2 chat, 1 execute_tool.
	assertSpans(t, h.tracer)
}

func assertManifest(t *testing.T, ev eventstore.Event, goal Goal) {
	t.Helper()
	var p turnPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("unmarshal turn payload: %v", err)
	}
	m := p.Manifest
	if len(m.PromptHash) < len("sha256:") || m.PromptHash[:7] != "sha256:" {
		t.Fatalf("prompt_hash em falta/mal-formado: %q", m.PromptHash)
	}
	if m.SystemHash[:7] != "sha256:" {
		t.Fatalf("system_hash mal-formado: %q", m.SystemHash)
	}
	if m.AssemblyVersion != AssemblyVersion {
		t.Fatalf("assembly_version = %q, esperava %q", m.AssemblyVersion, AssemblyVersion)
	}
	if m.Model.ModelID != goal.Model.ModelID {
		t.Fatalf("model_id = %q, esperava %q", m.Model.ModelID, goal.Model.ModelID)
	}
	if m.Model.Seed != goal.Model.Seed {
		t.Fatalf("seed = %d, esperava %d", m.Model.Seed, goal.Model.Seed)
	}
	if m.Model.Params["temperature"] != "0" {
		t.Fatalf("params.temperature em falta: %+v", m.Model.Params)
	}
	if len(m.Tools) != len(goal.Tools) {
		t.Fatalf("tools pinadas = %d, esperava %d", len(m.Tools), len(goal.Tools))
	}
	if m.Tools[0].Name != "web_search" || m.Tools[0].Version != "1.7.0" {
		t.Fatalf("primeira tool pinada errada: %+v", m.Tools[0])
	}
	if len(m.Skills) != 1 || m.Skills[0].Name != "report_writer" {
		t.Fatalf("skills pinadas erradas: %+v", m.Skills)
	}
}

func assertSpans(t *testing.T, tr *RecordingTracer) {
	t.Helper()
	if got := len(tr.SpansByOperation(OpInvokeAgent)); got != 1 {
		t.Fatalf("invoke_agent spans = %d, esperava 1", got)
	}
	chats := tr.SpansByOperation(OpChat)
	if len(chats) != 2 {
		t.Fatalf("chat spans = %d, esperava 2", len(chats))
	}
	if got := len(tr.SpansByOperation(OpExecuteTool)); got != 1 {
		t.Fatalf("execute_tool spans = %d, esperava 1", got)
	}
	// O span chat do turno 1 traz uso e custo (semconv GenAI).
	c0 := chats[0]
	if c0.Attributes[AttrOperationName] != OpChat {
		t.Fatalf("gen_ai.operation.name em falta no span chat")
	}
	if c0.Attributes[AttrInputTokens] != int64(10) {
		t.Fatalf("gen_ai.usage.input_tokens = %v, esperava 10", c0.Attributes[AttrInputTokens])
	}
	if c0.Attributes[AttrOutputTokens] != int64(5) {
		t.Fatalf("gen_ai.usage.output_tokens = %v, esperava 5", c0.Attributes[AttrOutputTokens])
	}
	cost, ok := c0.Attributes[AttrCostUSD].(float64)
	if !ok || cost <= 0 {
		t.Fatalf("gen_ai.usage.cost_usd em falta/errado: %v", c0.Attributes[AttrCostUSD])
	}
	if c0.Attributes[AttrPromptHash] == nil {
		t.Fatalf("aos.prompt_hash em falta no span chat")
	}
	// Todos os spans foram fechados.
	for _, s := range tr.Spans() {
		if !s.Ended {
			t.Fatalf("span %q não foi fechado", s.Operation)
		}
	}
	// O span invoke_agent traz o custo agregado.
	agent := tr.SpansByOperation(OpInvokeAgent)[0]
	if agent.Attributes[AttrCostUSD] == nil {
		t.Fatalf("custo agregado em falta no span invoke_agent")
	}
}

func filterType(events []eventstore.Event, typ string) []eventstore.Event {
	var out []eventstore.Event
	for _, e := range events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// TestRunTerminatesWithoutTools: uma resposta sem tool calls termina o loop já
// no primeiro turno.
func TestRunTerminatesWithoutTools(t *testing.T) {
	h := newHarness(t, nil)
	model := ModelClientFunc(func(_ context.Context, _ PromptView) (ModelResponse, error) {
		return ModelResponse{Text: "resposta directa", Usage: Usage{InputTokens: 4}}, nil
	})
	rt := New(model, h.rm, h.recorder)
	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Turns != 1 || !res.Terminated || res.FinalText != "resposta directa" {
		t.Fatalf("terminação inesperada: %+v", res)
	}
	if len(res.ToolResults) != 0 {
		t.Fatalf("não devia haver resultados de tool")
	}
}

// TestMaxTurnsExceeded: um modelo que pede tools indefinidamente esbarra no tecto.
func TestMaxTurnsExceeded(t *testing.T) {
	h := newHarness(t, map[string]referencemonitor.ToolFunc{
		"echo": func(_ context.Context, in []byte) ([]byte, error) { return in, nil },
	})
	model := ModelClientFunc(func(_ context.Context, _ PromptView) (ModelResponse, error) {
		return ModelResponse{
			Text:      "mais uma",
			ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("x")}},
		}, nil
	})
	rt := New(model, h.rm, h.recorder, WithMaxTurns(3))
	res, err := rt.Run(context.Background(), sampleGoal())
	if !errors.Is(err, ErrMaxTurnsExceeded) {
		t.Fatalf("esperava ErrMaxTurnsExceeded, obtive %v", err)
	}
	if res.Turns != 3 || res.Terminated {
		t.Fatalf("esperava 3 turnos não-terminados, obtive %+v", res)
	}
}

// TestPermittedToolErrorSurfaced: uma tool PERMITIDA que falha em runtime devolve
// dec.ToolErr — o RT anota o span execute_tool com error.type e materializa a
// condição de erro no tail do turno seguinte (o modelo vê "tool_error="), mantendo
// o resultado untrusted (AOS-013-Q2).
func TestPermittedToolErrorSurfaced(t *testing.T) {
	h := newHarness(t, map[string]referencemonitor.ToolFunc{
		"boom": func(_ context.Context, _ []byte) ([]byte, error) {
			return nil, errors.New("falha da tool downstream")
		},
	})
	var turn2Materialized []byte
	callN := 0
	model := ModelClientFunc(func(_ context.Context, view PromptView) (ModelResponse, error) {
		callN++
		if callN == 1 {
			return ModelResponse{
				Text:      "vou chamar boom",
				ToolCalls: []ToolInvocation{{ToolID: "boom", Capability: "cap:echo", Input: []byte("x")}},
			}, nil
		}
		turn2Materialized = append([]byte(nil), view.Materialized...)
		return ModelResponse{Text: "fim", Final: true}, nil
	})
	rt := New(model, h.rm, h.recorder, WithTracer(h.tracer))
	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Resultado da tool falhada continua untrusted.
	if len(res.ToolResults) != 1 || !res.ToolResults[0].IsUntrusted() {
		t.Fatalf("resultado de tool falhada devia estar untrusted: %+v", res.ToolResults)
	}
	// O span execute_tool traz error.type com a mensagem da tool.
	tools := h.tracer.SpansByOperation(OpExecuteTool)
	if len(tools) != 1 {
		t.Fatalf("esperava 1 span execute_tool, obtive %d", len(tools))
	}
	et, ok := tools[0].Attributes[AttrErrorType].(string)
	if !ok || !strings.Contains(et, "falha da tool downstream") {
		t.Fatalf("error.type em falta/errado no span execute_tool: %v", tools[0].Attributes[AttrErrorType])
	}
	// O tail do turno 2 materializa o marcador de erro para o modelo reagir.
	if !bytes.Contains(turn2Materialized, []byte("tool_error=falha da tool downstream")) {
		t.Fatalf("tail do turno 2 não contém o marcador tool_error:\n%s", turn2Materialized)
	}
}

// TestPrefixHashObservableOnChatSpan: o span chat emite aos.prefix_hash e este é
// byte-idêntico entre turnos do mesmo run — o cache-hit-rate do prefixo torna-se
// observável por telemetria (AOS-013-F2).
func TestPrefixHashObservableOnChatSpan(t *testing.T) {
	h := newHarness(t, map[string]referencemonitor.ToolFunc{
		"echo": func(_ context.Context, in []byte) ([]byte, error) { return in, nil },
	})
	callN := 0
	model := ModelClientFunc(func(_ context.Context, _ PromptView) (ModelResponse, error) {
		callN++
		if callN == 1 {
			return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo"}}}, nil
		}
		return ModelResponse{Text: "fim", Final: true}, nil
	})
	rt := New(model, h.rm, h.recorder, WithTracer(h.tracer))
	if _, err := rt.Run(context.Background(), sampleGoal()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	chats := h.tracer.SpansByOperation(OpChat)
	if len(chats) != 2 {
		t.Fatalf("esperava 2 spans chat, obtive %d", len(chats))
	}
	ph0, _ := chats[0].Attributes[AttrPrefixHash].(string)
	ph1, _ := chats[1].Attributes[AttrPrefixHash].(string)
	if ph0 == "" || !strings.HasPrefix(ph0, "sha256:") {
		t.Fatalf("aos.prefix_hash em falta/mal-formado no span chat: %q", ph0)
	}
	if ph0 != ph1 {
		t.Fatalf("prefix_hash devia ser byte-idêntico entre turnos: %q != %q", ph0, ph1)
	}
}

// TestAgentSpanAnnotatedOnErrorPath: mesmo quando a run falha (modelo devolve
// erro), o span invoke_agent é anotado com o custo/uso agregado (burn-down parcial)
// antes de fechar (AOS-013-Q3).
func TestAgentSpanAnnotatedOnErrorPath(t *testing.T) {
	h := newHarness(t, nil)
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		return ModelResponse{}, errors.New("gateway down")
	})
	rt := New(model, h.rm, h.recorder, WithTracer(h.tracer))
	if _, err := rt.Run(context.Background(), sampleGoal()); !errors.Is(err, ErrModelCall) {
		t.Fatalf("esperava ErrModelCall, obtive %v", err)
	}
	agents := h.tracer.SpansByOperation(OpInvokeAgent)
	if len(agents) != 1 {
		t.Fatalf("esperava 1 span invoke_agent, obtive %d", len(agents))
	}
	if agents[0].Attributes[AttrCostUSD] == nil {
		t.Fatalf("custo agregado devia ser anotado mesmo no caminho de erro")
	}
	if !agents[0].Ended {
		t.Fatalf("span invoke_agent devia estar fechado")
	}
}

// TestDeniedToolStillUntrusted: mesmo quando o RM NEGA (tool não registada), o
// resultado devolvido ao loop está marcado untrusted (nunca há Output cru).
func TestDeniedToolStillUntrusted(t *testing.T) {
	h := newHarness(t, nil) // nenhuma tool registada ⇒ default-deny
	callN := 0
	model := ModelClientFunc(func(_ context.Context, _ PromptView) (ModelResponse, error) {
		callN++
		if callN == 1 {
			return ModelResponse{
				ToolCalls: []ToolInvocation{{ToolID: "nao-registada", Capability: "cap:x"}},
			}, nil
		}
		return ModelResponse{Text: "fim", Final: true}, nil
	})
	rt := New(model, h.rm, h.recorder)
	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.ToolResults) != 1 || !res.ToolResults[0].IsUntrusted() {
		t.Fatalf("resultado negado devia continuar untrusted: %+v", res.ToolResults)
	}
	if len(res.ToolResults[0].Value) != 0 {
		t.Fatalf("negação não devia ter Output: %q", res.ToolResults[0].Value)
	}
}
