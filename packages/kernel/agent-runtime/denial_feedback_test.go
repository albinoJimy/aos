package agentruntime

import (
	"bytes"
	"context"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// denyingHook nega SEMPRE, com uma Reason controlada pelo teste.
type denyingHook struct{ reason string }

func (denyingHook) Name() string { return "politica-secreta" }

func (h denyingHook) Evaluate(context.Context, *referencemonitor.Call) (referencemonitor.HookResult, error) {
	return referencemonitor.HookResult{Decision: referencemonitor.HookDeny, Reason: h.reason}, nil
}

// capturingPrompts grava o prompt materializado de cada turno, para se observar o que o
// modelo REALMENTE vê no turno seguinte a uma negação.
type capturingPrompts struct {
	views     [][]byte
	responder func(turn int) ModelResponse
}

func (c *capturingPrompts) Call(_ context.Context, view PromptView) (ModelResponse, error) {
	c.views = append(c.views, append([]byte(nil), view.Materialized...))
	return c.responder(len(c.views)), nil
}

// denyThenFinish: turno 1 pede uma tool NÃO REGISTADA (⇒ default-deny do RM); turno 2 conclui.
func denyThenFinish(turn int) ModelResponse {
	if turn == 1 {
		return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "nao-registada", Capability: "cap:x"}}}
	}
	return ModelResponse{Text: "fim", Final: true}
}

// TestDenial_MarcadorSanitizadoNoTail é a prova do gap 2 (feedback de negação): quando o
// Reference Monitor NEGA uma tool call, o modelo passa a VER o facto no prompt do turno
// seguinte — em vez de um resultado vazio indistinguível de "a tool não devolveu nada".
func TestDenial_MarcadorSanitizadoNoTail(t *testing.T) {
	h := newHarness(t, nil) // nenhuma tool registada ⇒ default-deny
	model := &capturingPrompts{responder: denyThenFinish}
	rt := New(model, h.rm, h.recorder)
	if _, err := rt.Run(context.Background(), sampleGoal()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(model.views) < 2 {
		t.Fatalf("esperava >=2 turnos (o 2.º vê o resultado da negação), obtive %d", len(model.views))
	}
	turno2 := model.views[1]

	// (1) O FACTO da negação chega ao modelo.
	if !bytes.Contains(turno2, []byte("tool_denied=deny")) {
		t.Fatalf("o prompt do turno 2 devia conter o marcador de negação; tail=%q", turno2)
	}
	// (2) Com o código estável (enumeração fechada), para o modelo poder ramificar.
	if !bytes.Contains(turno2, []byte("denied_code="+referencemonitor.CodeToolNotRegistered)) {
		t.Fatalf("o prompt devia conter denied_code=%s; tail=%q", referencemonitor.CodeToolNotRegistered, turno2)
	}
	// (3) E continua marcado untrusted (o marcador é metadado, não eleva nada).
	if !bytes.Contains(turno2, []byte("taint="+TaintUntrusted)) {
		t.Fatalf("o resultado negado devia continuar marcado untrusted; tail=%q", turno2)
	}
}

// TestDenial_NuncaExpoeReason sela a fronteira de segurança: a Reason de uma decisão é
// texto livre de um hook/PDP (pode conter fragmentos de regra de política, hosts de
// allowlist ou erros internos) e NUNCA pode ser escrita no prompt — que é precisamente o
// plano que conteúdo untrusted consegue ler. Só rótulos de enumeração fechada saem.
func TestDenial_NuncaExpoeReason(t *testing.T) {
	const segredo = "SEGREDO-DE-POLITICA-NAO-EXPOR"
	h := newHarness(t, nil)
	// RM com um hook que NEGA com uma Reason contendo material sensível — o pior caso
	// realista (o PDP e o hook de identidade preenchem Reason com texto livre).
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	rm := referencemonitor.New(
		referencemonitor.WithHooks(denyingHook{reason: segredo}),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)),
	)
	model := &capturingPrompts{responder: denyThenFinish}
	rt := New(model, rm, h.recorder)
	if _, err := rt.Run(context.Background(), sampleGoal()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for i, v := range model.views {
		if bytes.Contains(v, []byte(segredo)) {
			t.Fatalf("a Reason da decisão VAZOU para o prompt do turno %d: %q", i+1, v)
		}
	}
	// …mas o facto da negação chegou (o rótulo, não a razão).
	if len(model.views) >= 2 && !bytes.Contains(model.views[1], []byte("tool_denied=")) {
		t.Fatalf("o marcador de negação devia estar presente; tail=%q", model.views[1])
	}
}

// TestDenial_PermitTailInalterado é a guarda de retro-compatibilidade: um PERMIT produz
// bytes de tail SEM qualquer marcador de negação — idênticos aos de antes desta
// funcionalidade. Sem isto, um bug que emitisse o bloco em permit passaria despercebido e
// mudaria o prompt_hash de TODOS os runs.
func TestDenial_PermitTailInalterado(t *testing.T) {
	h := newHarness(t, nil)
	if err := h.rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	model := &capturingPrompts{responder: func(turn int) ModelResponse {
		if turn == 1 {
			return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("ola")}}}
		}
		return ModelResponse{Text: "fim", Final: true}
	}}
	rt := New(model, h.rm, h.recorder)
	if _, err := rt.Run(context.Background(), sampleGoal()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(model.views) < 2 {
		t.Fatalf("esperava >=2 turnos, obtive %d", len(model.views))
	}
	for _, marcador := range []string{"tool_denied=", "denied_code=", "denied_by="} {
		if bytes.Contains(model.views[1], []byte(marcador)) {
			t.Fatalf("um PERMIT não devia emitir %q no tail; tail=%q", marcador, model.views[1])
		}
	}
	// E o output real da tool chegou (permit funciona como antes).
	if !bytes.Contains(model.views[1], []byte("echoed:ola")) {
		t.Fatalf("o output da tool permitida devia estar no tail; tail=%q", model.views[1])
	}
}

// TestDenial_MarcadorNaoEntraNoValue sela o invariante estrutural: o bloco de negação é
// metadado do SEGMENTO DE TAIL e NUNCA entra em [Tainted.Value] — o resultado de uma call
// negada continua untrusted-VAZIO, como antes.
func TestDenial_MarcadorNaoEntraNoValue(t *testing.T) {
	h := newHarness(t, nil)
	model := &capturingPrompts{responder: denyThenFinish}
	rt := New(model, h.rm, h.recorder)
	res, err := rt.Run(context.Background(), sampleGoal())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.ToolResults) != 1 {
		t.Fatalf("esperava 1 resultado, obtive %d", len(res.ToolResults))
	}
	if len(res.ToolResults[0].Value) != 0 {
		t.Fatalf("o Value de uma negação tem de continuar VAZIO (o marcador vive no tail): %q", res.ToolResults[0].Value)
	}
	if !res.ToolResults[0].IsUntrusted() {
		t.Fatalf("o resultado negado devia continuar untrusted")
	}
}
