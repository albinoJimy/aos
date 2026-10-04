package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// AOS-491 — o motivo de paragem do modelo chega ao runtime e ao `turn.recorded`, num vocabulário
// fechado, e não muda onde o loop termina.

// O VOCABULÁRIO É FECHADO: um valor conhecido fica, qualquer outro texto vira `other`, e o vazio
// é do vocabulário.
func TestAOS491_StopReason_VocabularioFechado(t *testing.T) {
	t.Parallel()
	quer := []StopReason{"stop", "tool_calls", "length", "content_filter", "other", ""}
	if got := StopReasons(); !reflect.DeepEqual(got, quer) {
		t.Fatalf("StopReasons() = %q, quero %q", got, quer)
	}
	for _, m := range quer {
		if got := m.Normalizado(); got != m {
			t.Fatalf("%q e do vocabulario e foi normalizado para %q", m, got)
		}
	}
	for _, bruto := range []StopReason{"end_turn", "STOP", " stop", "max_tokens", "eos", "stop\n", `x"} 1`} {
		if got := bruto.Normalizado(); got != StopOther {
			t.Fatalf("%q esta fora do vocabulario e devia virar other; virou %q", bruto, got)
		}
	}
}

func aos491Echo() ToolInvocation {
	return ToolInvocation{ToolID: "echo", Capability: "cap:echo", ResourceType: "t", ResourceValue: "v", Input: []byte("x")}
}

// aos491Guiao devolve um cliente que responde pelo guião, indexado pelo turno.
func aos491Guiao(respostas ...ModelResponse) ModelClient {
	return ModelClientFunc(func(_ context.Context, v PromptView) (ModelResponse, error) {
		return respostas[v.Turn-1], nil
	})
}

// O `turn.recorded` GRAVA O MOTIVO E AS TOOLS OFERECIDAS, turno a turno. Um valor fora do
// vocabulário que o cliente deixe passar é gravado como `other` — o texto bruto não chega ao
// evento.
func TestAOS491_TurnRecorded_MotivoEToolsOferecidas(t *testing.T) {
	const bruto = "MOTIVO-BRUTO-DO-PROVIDER"
	turnos, todos, _ := aos490Correr(t, aos491Guiao(
		ModelResponse{Text: "vou chamar", StopReason: StopToolCalls, ToolsOffered: 3, Usage: Usage{InputTokens: 5}, ToolCalls: []ToolInvocation{aos491Echo()}},
		ModelResponse{Text: "outra vez", StopReason: bruto, ToolsOffered: -4, Usage: Usage{InputTokens: 5}, ToolCalls: []ToolInvocation{aos491Echo()}},
		ModelResponse{Text: "cortado a mei", StopReason: StopLength, ToolsOffered: 7, Usage: Usage{InputTokens: 5}},
	))
	if len(turnos) != 3 {
		t.Fatalf("queria 3 turnos, vieram %d", len(turnos))
	}
	// O que se grava é o que o CLIENTE declarou em cada turno — não o comprimento de Goal.Tools
	// (o goal de teste tem um tool set, e nenhum destes números é o dele). Uma contagem negativa
	// não chega ao registo.
	ofertas := []int{3, 0, 7}
	for i, quer := range []StopReason{StopToolCalls, StopOther, StopLength} {
		var p turnPayload
		if err := json.Unmarshal(turnos[i], &p); err != nil {
			t.Fatal(err)
		}
		if p.StopReason != quer {
			t.Fatalf("turno %d: stop_reason = %q, quero %q (%s)", i+1, p.StopReason, quer, turnos[i])
		}
		if p.ToolsOffered != ofertas[i] {
			t.Fatalf("turno %d: tools_offered = %d, quero %d — o que o cliente declarou", i+1, p.ToolsOffered, ofertas[i])
		}
	}
	if bytes.Contains(turnos[1], []byte("tools_offered")) {
		t.Fatalf("uma contagem negativa nao pode ser gravada: %s", turnos[1])
	}
	if !bytes.HasSuffix(turnos[2], []byte(`"final":false,"stop_reason":"length","tools_offered":7}`)) {
		t.Fatalf("os campos novos tinham de vir no fim do payload, depois de `final`: %s", turnos[2])
	}
	for _, ev := range todos {
		if bytes.Contains(ev, []byte(bruto)) {
			t.Fatalf("o motivo bruto do provider chegou a um evento: %s", ev)
		}
	}
}

// ADITIVO: um turno de um cliente que não declara motivo nem oferta grava os bytes de antes —
// mesmo num run cujo goal tem tools —, e um `turn.recorded` anterior aos campos lê-se com os dois
// a zero.
func TestAOS491_TurnRecorded_SemMotivoNemOferta_BytesDeSempre(t *testing.T) {
	h := newHarness(t, nil)
	goal := sampleGoal()
	if len(goal.Tools) == 0 {
		t.Fatal("o goal de teste tem de ter tools: e isso que prova que tools_offered nao sai de Goal.Tools")
	}
	model := aos491Guiao(ModelResponse{Text: "ok", Final: true, Usage: Usage{InputTokens: 380, OutputTokens: 12}})
	if _, err := New(model, h.rm, h.recorder).Run(context.Background(), goal); err != nil {
		t.Fatalf("Run: %v", err)
	}
	events, err := h.store.Read(context.Background(), goal.RunID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var turno []byte
	for _, ev := range events {
		if ev.Type == EventTypeTurnRecorded {
			turno = ev.Payload
		}
	}
	if turno == nil {
		t.Fatal("o run nao gravou turno nenhum")
	}
	for _, chave := range []string{"stop_reason", "tools_offered"} {
		if bytes.Contains(turno, []byte(chave)) {
			t.Fatalf("um turno de um cliente que nao declara motivo nem oferta nao pode ganhar a chave %q: %s", chave, turno)
		}
	}
	if !bytes.HasSuffix(turno, []byte(`"tool_calls_requested":0,"final":true}`)) {
		t.Fatalf("o payload devia acabar onde acabava antes do AOS-491: %s", turno)
	}

	// Um evento gravado antes do ticket (sem as duas chaves) continua a ler-se.
	var antigo turnPayload
	if err := json.Unmarshal([]byte(`{"turn":1,"manifest":{"schema_version":"1.0","prompt_hash":"sha256:aa","system_hash":"sha256:bb","assembly_version":"1.3.0","model":{"model_id":"m","seed":0}},"input_tokens":1,"output_tokens":1,"cost_micro_usd":0,"tool_calls_requested":0,"final":true}`), &antigo); err != nil {
		t.Fatalf("um turn.recorded antigo deixou de se ler: %v", err)
	}
	if antigo.StopReason != StopUnreported || antigo.ToolsOffered != 0 || !antigo.Final {
		t.Fatalf("turn.recorded antigo lido de outra forma: %+v", antigo)
	}
}

// O OBSERVADOR recebe um motivo por turno, já no vocabulário fechado.
func TestAOS491_StopReasonStats_UmPorTurno(t *testing.T) {
	h := newHarness(t, map[string]referencemonitor.ToolFunc{
		"echo": func(_ context.Context, in []byte) ([]byte, error) { return in, nil },
	})
	type visto struct {
		run    string
		motivo StopReason
	}
	var vistos []visto
	goal := sampleGoal()
	model := aos491Guiao(
		ModelResponse{StopReason: StopToolCalls, Usage: Usage{InputTokens: 5}, ToolCalls: []ToolInvocation{aos491Echo()}},
		ModelResponse{StopReason: "end_turn", Usage: Usage{InputTokens: 5}, ToolCalls: []ToolInvocation{aos491Echo()}},
		ModelResponse{Text: "fim", Final: true, Usage: Usage{InputTokens: 5}},
	)
	rt := New(model, h.rm, h.recorder, WithStopReasonStats(func(run string, m StopReason) {
		vistos = append(vistos, visto{run, m})
	}), WithStopReasonStats(nil))
	if _, err := rt.Run(context.Background(), goal); err != nil {
		t.Fatalf("Run: %v", err)
	}
	quer := []visto{{goal.RunID, StopToolCalls}, {goal.RunID, StopOther}, {goal.RunID, StopUnreported}}
	if !reflect.DeepEqual(vistos, quer) {
		t.Fatalf("observador viu %+v, quero %+v", vistos, quer)
	}
}

// O LOOP TERMINA NOS MESMOS TURNOS. O motivo de paragem é medição: para cada motivo do
// vocabulário, um run que o declara pára no mesmo turno, com o mesmo texto final e os mesmos
// `prompt_hash`, que o mesmo run sem motivo nenhum. Em particular, um `length` com tool calls
// continua, e um `tool_calls` sem tool calls termina — como antes do ticket.
func TestAOS491_OMotivoNaoMudaOndeOLoopTermina(t *testing.T) {
	correr := func(t *testing.T, guiao []ModelResponse) (Result, []string) {
		t.Helper()
		h := newHarness(t, map[string]referencemonitor.ToolFunc{
			"echo": func(_ context.Context, in []byte) ([]byte, error) { return in, nil },
		})
		goal := sampleGoal()
		res, err := New(aos491Guiao(guiao...), h.rm, h.recorder).Run(context.Background(), goal)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		events, err := h.store.Read(context.Background(), goal.RunID, 1)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		var hashes []string
		for _, ev := range events {
			if ev.Type != EventTypeTurnRecorded {
				continue
			}
			var p turnPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatal(err)
			}
			hashes = append(hashes, p.Manifest.PromptHash)
		}
		return res, hashes
	}
	base := []ModelResponse{
		{Text: "um", Usage: Usage{InputTokens: 5}, ToolCalls: []ToolInvocation{aos491Echo()}},
		{Text: "dois", Usage: Usage{InputTokens: 5}, ToolCalls: []ToolInvocation{aos491Echo()}},
		{Text: "tres", Usage: Usage{InputTokens: 5}}, // sem tool calls e sem Final: termina
		{Text: "nunca chega aqui", Final: true, Usage: Usage{InputTokens: 5}},
	}
	resBase, hashesBase := correr(t, base)
	if !resBase.Terminated || resBase.Turns != 3 || resBase.FinalText != "tres" {
		t.Fatalf("o run de base devia terminar no turno 3: %+v", resBase)
	}
	for _, motivo := range append(StopReasons(), "fora-do-vocabulario") {
		comMotivo := make([]ModelResponse, len(base))
		for i, r := range base {
			r.StopReason = motivo
			comMotivo[i] = r
		}
		res, hashes := correr(t, comMotivo)
		if res.Terminated != resBase.Terminated || res.Turns != resBase.Turns || res.FinalText != resBase.FinalText {
			t.Fatalf("motivo %q: o run terminou de outra forma: %+v (base %+v)", motivo, res, resBase)
		}
		if !reflect.DeepEqual(hashes, hashesBase) {
			t.Fatalf("motivo %q: os prompt_hash mudaram: %v (base %v)", motivo, hashes, hashesBase)
		}
	}
}
