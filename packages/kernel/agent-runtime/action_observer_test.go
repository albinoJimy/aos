package agentruntime

import (
	"context"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// AOS-251 — o sinal de no-progress do disjuntor só existe se o loop REPORTAR cada acção
// mediada. Estes testes fixam o contrato do [ActionObserver]: uma observação por tool call
// MEDIADA (permit OU deny — o veredicto não interessa ao detector de repetição), com o
// MESMO hash canónico que o RM anota no span execute_tool.

// TestActionObserverReportsEveryMediatedCall prova que o observador é chamado UMA vez por
// mediação fechada, na ordem de despacho, com o hash canónico da call — e que esse hash é
// byte-idêntico ao atributo aos.tool_call.hash do span execute_tool (uma só noção de
// "acção" para telemetria e detector).
func TestActionObserverReportsEveryMediatedCall(t *testing.T) {
	h := newHarness(t, map[string]referencemonitor.ToolFunc{
		"echo": func(_ context.Context, input []byte) ([]byte, error) { return input, nil },
	})

	type obs struct{ runID, hash string }
	var seen []obs
	observer := ActionObserver(func(runID, hash string) { seen = append(seen, obs{runID, hash}) })

	echoInput := []byte(`{"doc":"a"}`)
	ghostInput := []byte(`{"doc":"b"}`)
	callN := 0
	model := ModelClientFunc(func(_ context.Context, _ PromptView) (ModelResponse, error) {
		callN++
		switch callN {
		case 1:
			// Permit: tool registada, capability no principal.
			return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: echoInput}}}, nil
		case 2:
			// Deny: tool NÃO registada — o RM nega por default-deny, mas a mediação FECHOU
			// e a acção conta para o detector (um deny-loop é exactamente o caso de AOS-251).
			return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "ghost", Capability: "cap:echo", Input: ghostInput}}}, nil
		default:
			return ModelResponse{Text: "fim", Final: true}, nil
		}
	})

	rt := New(model, h.rm, h.recorder, WithTracer(h.tracer), WithActionObserver(observer))
	goal := sampleGoal()
	res, err := rt.Run(context.Background(), goal)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Terminated || res.Turns != 3 {
		t.Fatalf("desfecho inesperado: %+v", res)
	}

	want := []obs{
		{goal.RunID, otelgenai.CanonicalToolCallHash("echo", echoInput)},
		{goal.RunID, otelgenai.CanonicalToolCallHash("ghost", ghostInput)},
	}
	if len(seen) != len(want) {
		t.Fatalf("observador chamado %d vezes, esperava %d (uma por mediação fechada): %+v", len(seen), len(want), seen)
	}
	for i, w := range want {
		if seen[i] != w {
			t.Errorf("observação %d = %+v, esperava %+v", i, seen[i], w)
		}
	}

	// A âncora reportada é a MESMA que o RM anotou no span execute_tool.
	spans := h.tracer.SpansByOperation(OpExecuteTool)
	if len(spans) != 2 {
		t.Fatalf("esperava 2 spans execute_tool, obtive %d", len(spans))
	}
	for i, sp := range spans {
		spanHash, _ := sp.Attributes[AttrToolCallHash].(string)
		if spanHash == "" || spanHash != seen[i].hash {
			t.Errorf("span execute_tool %d: hash=%q, observador reportou %q — as âncoras divergiram", i, spanHash, seen[i].hash)
		}
	}
}

// TestActionObserverNilKeepsBehaviour é a metade aditiva: sem observador ligado o run
// comporta-se exactamente como antes (e o nil não explode no fecho da mediação).
func TestActionObserverNilKeepsBehaviour(t *testing.T) {
	h := newHarness(t, map[string]referencemonitor.ToolFunc{
		"echo": func(_ context.Context, input []byte) ([]byte, error) { return input, nil },
	})
	callN := 0
	model := ModelClientFunc(func(_ context.Context, _ PromptView) (ModelResponse, error) {
		callN++
		if callN == 1 {
			return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("ola")}}}, nil
		}
		return ModelResponse{Text: "fim", Final: true}, nil
	})
	rt := New(model, h.rm, h.recorder, WithActionObserver(nil))
	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Terminated || res.Turns != 2 {
		t.Fatalf("desfecho inesperado sem observador: %+v", res)
	}
}
