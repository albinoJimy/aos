package agentruntime

// AOS-396 — o manifesto do turno grava o modelo PEDIDO (Goal.Model) e o modelo que SERVIU
// a resposta (ModelResponse.Model), este em `served_model_id`. Um cliente que não reporta o
// modelo não acrescenta a chave (os bytes do turno não mudam).

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// admissaoEspia admite tudo e regista o ModelID que cada turno pediu.
type admissaoEspia struct{ modelos []string }

func (a *admissaoEspia) AdmitTurn(_ context.Context, req TurnAdmissionRequest) (TurnAdmissionVerdict, error) {
	a.modelos = append(a.modelos, req.ModelID)
	return TurnAdmissionVerdict{Admitted: true}, nil
}

func (a *admissaoEspia) SettleTurn(context.Context, TurnSettlement) error { return nil }

func TestAOS396_ManifestoGravaOModeloPedidoEOServido(t *testing.T) {
	h := newHarness(t, map[string]referencemonitor.ToolFunc{})
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		return ModelResponse{Text: "ok", Final: true, Model: "gpt-4o-mini-2024-07-18"}, nil
	})
	goal := sampleGoal()
	goal.Model.ModelID = "gpt-4o-mini"
	admissao := &admissaoEspia{}
	rt := New(model, h.rm, h.recorder, WithTracer(h.tracer), WithModelAdmission(admissao))
	if _, err := rt.Run(context.Background(), goal); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Os consumidores do modelo pedido falam do mesmo valor que o manifesto.
	for _, op := range []string{OpInvokeAgent, OpChat} {
		for _, s := range h.tracer.SpansByOperation(op) {
			if s.Attributes[AttrRequestModel] != "gpt-4o-mini" {
				t.Fatalf("span %s: %s=%v; quero gpt-4o-mini", op, AttrRequestModel, s.Attributes[AttrRequestModel])
			}
		}
	}
	if len(admissao.modelos) != 1 || admissao.modelos[0] != "gpt-4o-mini" {
		t.Fatalf("admissao viu ModelID=%v; quero [gpt-4o-mini]", admissao.modelos)
	}
	events, err := h.store.Read(context.Background(), goal.RunID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	turns := filterType(events, EventTypeTurnRecorded)
	if len(turns) != 1 {
		t.Fatalf("esperava 1 turn.recorded, veio %d", len(turns))
	}
	var p turnPayload
	if err := json.Unmarshal(turns[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Manifest.Model.ModelID != "gpt-4o-mini" || p.Manifest.Model.ServedModelID != "gpt-4o-mini-2024-07-18" {
		t.Fatalf("manifest.model = %+v; quero pedido gpt-4o-mini e servido gpt-4o-mini-2024-07-18", p.Manifest.Model)
	}
}

func TestAOS396_ClienteSemModeloServidoNaoAcrescentaAChave(t *testing.T) {
	h := newHarness(t, map[string]referencemonitor.ToolFunc{})
	model := ModelClientFunc(func(context.Context, PromptView) (ModelResponse, error) {
		return ModelResponse{Text: "ok", Final: true}, nil
	})
	goal := sampleGoal()
	if _, err := New(model, h.rm, h.recorder).Run(context.Background(), goal); err != nil {
		t.Fatalf("Run: %v", err)
	}
	events, err := h.store.Read(context.Background(), goal.RunID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, ev := range filterType(events, EventTypeTurnRecorded) {
		if bytes.Contains(ev.Payload, []byte("served_model_id")) {
			t.Fatalf("sem modelo servido a chave nao pode aparecer: %s", ev.Payload)
		}
	}
}
