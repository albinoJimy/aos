package agentruntime

// AOS-406 — um turno sem fonte de preço sai marcado como custo NÃO DERIVADO no span `chat` e no
// `turn.recorded`, em vez de um zero que se lê como gratuito.

import (
	"context"
	"encoding/json"
	"testing"

	eventstore "github.com/aos-ref/substrate/eventstore"
)

func TestAOS406_SpanChatSemCustoDerivadoNaoEmiteCusto(t *testing.T) {
	h := newHarness(t, nil)
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		return ModelResponse{Text: "feito", Final: true, Usage: Usage{InputTokens: 40, OutputTokens: 9}, CustoNaoDerivado: true}, nil
	})
	rt := New(model, h.rm, h.recorder, WithTracer(h.tracer))
	if _, err := rt.Run(context.Background(), sampleGoal()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	chats := 0
	for _, s := range h.tracer.Spans() {
		if s.Operation != "chat" {
			continue
		}
		chats++
		if v, ok := s.Attributes[AttrCostUndefined]; !ok || v != true {
			t.Errorf("o span chat tem de levar %s=true; veio %v (ok=%v)", AttrCostUndefined, v, ok)
		}
		for _, proibido := range []string{AttrCostMicroUSD, AttrCostUSD} {
			if _, ok := s.Attributes[proibido]; ok {
				t.Errorf("sem fonte de preço o span não pode levar %s (leria-se como custo zero)", proibido)
			}
		}
		if s.Attributes[AttrInputTokens] != int64(40) {
			t.Errorf("os tokens continuam medidos: %v", s.Attributes[AttrInputTokens])
		}
	}
	if chats != 1 {
		t.Fatalf("esperava 1 span chat, vieram %d", chats)
	}
	// O agregado do run no invoke_agent também não pode dizer «custo total 0».
	agentes := 0
	for _, s := range h.tracer.Spans() {
		if s.Operation != "invoke_agent" {
			continue
		}
		agentes++
		if v, ok := s.Attributes[AttrCostUndefined]; !ok || v != true {
			t.Errorf("o invoke_agent tem de levar %s=true; veio %v (ok=%v)", AttrCostUndefined, v, ok)
		}
		if _, ok := s.Attributes[AttrCostMicroUSD]; ok {
			t.Error("o invoke_agent não pode levar um total de custo sem fonte")
		}
	}
	if agentes != 1 {
		t.Fatalf("esperava 1 span invoke_agent, vieram %d", agentes)
	}
}

func TestAOS406_TurnRecordedMarcaOCustoNaoDerivado(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	gravar := func(run string, naoDerivado bool) map[string]any {
		t.Helper()
		if _, err := NewTurnRecorder(es).Record(context.Background(), TurnRecord{
			RunID: run, StepID: "step-1", Turn: 1,
			Usage:            Usage{InputTokens: 400, OutputTokens: 100},
			CustoNaoDerivado: naoDerivado,
			Producer:         eventstore.Producer{NHIID: "nhi:teste-aos406"},
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
		evs, err := es.Read(context.Background(), run, 1)
		if err != nil || len(evs) != 1 {
			t.Fatalf("Read: %v (%d eventos)", err, len(evs))
		}
		var p map[string]any
		if err := json.Unmarshal(evs[0].Payload, &p); err != nil {
			t.Fatalf("payload ilegível: %v", err)
		}
		return p
	}

	marcado := gravar("run-406-sem-preco", true)
	if marcado["custo_nao_derivado"] != true {
		t.Fatalf("o turno sem fonte de preço tem de gravar custo_nao_derivado=true: %v", marcado)
	}
	// Ortogonal ao AOS-336: os tokens foram medidos.
	if _, ok := marcado["usage_ausente"]; ok {
		t.Fatalf("custo não derivado não é usage ausente: %v", marcado)
	}
	comPreco := gravar("run-406-com-preco", false)
	if _, ok := comPreco["custo_nao_derivado"]; ok {
		t.Fatalf("um turno com preço grava os bytes de sempre (sem o campo): %v", comPreco)
	}
}
