package main

// AOS-396 — pela cadeia real do nó (POST /runs → handleSubmit → NodeService.hostRun → runtime),
// o `turn.recorded` de cada turno regista o modelo pedido e o servido; o span `chat` e o
// replay falam do mesmo modelo.
//
// FALHA-ANTES: sem [Node.fixarModelo], o `model_id` sai vazio (o `submitRequest` não tem
// modelo) e um replay com o modelo CORRECTO diverge por `model` ("" contra o esperado).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// modeloQueReportaOServido faz o papel do adaptador do gateway: devolve o `model` do provider
// (uma versão datada do pedido) e conclui no primeiro turno.
type modeloQueReportaOServido struct{}

func (modeloQueReportaOServido) Call(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{
		Text:         "feito",
		Final:        true,
		Usage:        agentruntime.Usage{InputTokens: 10, OutputTokens: 4},
		CostMicroUSD: 100,
		Model:        "gpt-4o-mini-2024-07-18",
	}, nil
}

const aos396Objectivo = "resumir o relatorio"

// correrPelaAPI hospeda um run pelo POST /runs e devolve os eventos turn.recorded.
func correrPelaAPI(t *testing.T, node *Node, runID string) []agentruntime.ModelManifest {
	t.Helper()
	svc, h := newAPI(t, node)
	rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id": runID, "objective": aos396Objectivo, "principal_nhi": "nhi:" + runID,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if oc, ok, err := svc.Wait(ctx, runID); err != nil || !ok || !oc.Result.Terminated {
		t.Fatalf("o run devia terminar: ok=%v err=%v oc=%+v", ok, err, oc)
	}
	events, err := node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var out []agentruntime.ModelManifest
	for _, ev := range events {
		if ev.Type != agentruntime.EventTypeTurnRecorded {
			continue
		}
		var p struct {
			Manifest agentruntime.Manifest `json:"manifest"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("payload do turn.recorded: %v", err)
		}
		out = append(out, p.Manifest.Model)
	}
	if len(out) == 0 {
		t.Fatal("o run nao gravou nenhum turn.recorded")
	}
	return out
}

func TestAOS396_PelaAPI_ManifestoEChatComOModeloDoNo(t *testing.T) {
	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	cfg := obsConfig(srv.URL)
	cfg.Model = modeloQueReportaOServido{}
	cfg.ModelID = "gpt-4o-mini"
	// A captura de não-determinismo (que o replay exige para ser admissível) só é composta
	// com a execução durável.
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(t.TempDir(), "events.wal")
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	fechado := false
	defer func() {
		if !fechado {
			_ = node.Close()
		}
	}()

	const runID = "run-aos396-gw"
	manifestos := correrPelaAPI(t, node, runID)

	// REPLAY: a verificação de modelo está ligada — o mesmo modelo não diverge, outro diverge.
	// O conteúdo capturado vai selado por titular: o motor lê-o com o opener do nó, como o
	// GET /runs/{id}/reconstruct.
	eng, err := replay.NewEngine(node.EventStore, replay.WithContentOpener(node.contentOpener, replay.Accessor{
		Principal: "nhi:leitor-aos396", Scopes: []string{replay.DefaultSovereignContentScope},
	}))
	if err != nil {
		t.Fatalf("replay.NewEngine: %v", err)
	}
	igual, err := eng.Replay(context.Background(), runID, replay.Options{Spec: replay.TrajectorySpec{
		Objective: aos396Objectivo, Model: agentruntime.ModelConfig{ModelID: "gpt-4o-mini"},
	}})
	if err != nil {
		t.Fatalf("Replay (mesmo modelo): %v", err)
	}
	if igual.Divergence != nil {
		t.Fatalf("replay com o modelo gravado nao pode divergir; veio %+v", igual.Divergence)
	}
	outro, err := eng.Replay(context.Background(), runID, replay.Options{Spec: replay.TrajectorySpec{
		Objective: aos396Objectivo, Model: agentruntime.ModelConfig{ModelID: "claude-sonnet"},
	}})
	if err != nil {
		t.Fatalf("Replay (outro modelo): %v", err)
	}
	if outro.Divergence == nil || outro.Divergence.Reason != "model" {
		t.Fatalf("replay com outro modelo tinha de divergir por model; veio %+v", outro.Divergence)
	}

	// MANIFESTO: verificado depois do replay para que a falha-antes do replay (o mesmo modelo
	// a divergir contra um manifesto vazio) também se veja.
	for i, m := range manifestos {
		if m.ModelID != "gpt-4o-mini" || m.ServedModelID != "gpt-4o-mini-2024-07-18" {
			t.Fatalf("turno %d: manifest.model=%+v; quero pedido gpt-4o-mini e servido gpt-4o-mini-2024-07-18", i+1, m)
		}
		if m.Seed != 0 || len(m.Params) != 0 {
			t.Fatalf("turno %d: seed/params nao viajaram e nao podem aparecer: %+v", i+1, m)
		}
	}

	// SPANS chat e invoke_agent: o gen_ai.request.model é o mesmo modelo do manifesto.
	if err := node.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fechado = true
	vistos := map[string]bool{}
	for _, s := range col.spans(t) {
		// Só os spans da operação GenAI (com gen_ai.operation.name): o nó também exporta as
		// transições de estado do run com o nome invoke_agent, e essas não levam modelo.
		if op, ok := s.attr(otelgenai.AttrOperationName); !ok || (op != otelgenai.OpChat && op != otelgenai.OpInvokeAgent) {
			continue
		}
		vistos[s.Name] = true
		if v, ok := s.attr(otelgenai.AttrRequestModel); !ok || v != "gpt-4o-mini" {
			t.Fatalf("%s.%s = %q (ok=%v); quero gpt-4o-mini", s.Name, otelgenai.AttrRequestModel, v, ok)
		}
	}
	if !vistos[otelgenai.OpChat] || !vistos[otelgenai.OpInvokeAgent] {
		t.Fatalf("faltam spans exportados: %v", vistos)
	}
}

func TestAOS396_PelaAPI_ModeloDeReferenciaDeclaraOSeuNome(t *testing.T) {
	node, _ := newAPINode(t, nil, false)
	defer func() { _ = node.Close() }()
	for i, m := range correrPelaAPI(t, node, "run-aos396-ref") {
		if m.ModelID != ReferenceModelID || m.ServedModelID != ReferenceModelID {
			t.Fatalf("turno %d: manifest.model=%+v; quero %s nos dois campos", i+1, m, ReferenceModelID)
		}
	}
}

func TestAOS396_FixarModelo_RegraPorOrigem(t *testing.T) {
	base := agentruntime.Goal{RunID: "r", Model: agentruntime.ModelConfig{
		ModelID: "modelo-antigo", Seed: 7, Params: map[string]string{"temperature": "0"},
	}}

	gw := &Node{modelID: "gpt-4o-mini", modeloAutoritativo: true}
	if got := gw.fixarModelo(base); got.Model.ModelID != "gpt-4o-mini" {
		t.Fatalf("gateway e autoritativo: model_id=%q, quero gpt-4o-mini", got.Model.ModelID)
	} else if got.Model.Seed != 7 || got.Model.Params["temperature"] != "0" {
		t.Fatalf("seed/params nao se tocam: %+v", got.Model)
	}

	ref := &Node{modelID: ReferenceModelID}
	if got := ref.fixarModelo(base); got.Model.ModelID != "modelo-antigo" {
		t.Fatalf("referencia nao sobrepoe um goal com modelo: veio %q", got.Model.ModelID)
	}
	vazio := base
	vazio.Model.ModelID = ""
	if got := ref.fixarModelo(vazio); got.Model.ModelID != ReferenceModelID {
		t.Fatalf("referencia preenche um goal vazio: veio %q", got.Model.ModelID)
	}

	semNome := &Node{}
	if got := semNome.fixarModelo(vazio); got.Model.ModelID != "" {
		t.Fatalf("sem modelo declarado o goal fica como veio: veio %q", got.Model.ModelID)
	}
}

// TestAOS396_ArranquePorAmbienteDeclaraOModeloDoGateway: o nome que o adaptador do gateway
// envia (AOS_MODEL_NAME) chega a Config.ModelID; sem gateway fica vazio e o Bootstrap usa o
// modelo de referência.
func TestAOS396_ArranquePorAmbienteDeclaraOModeloDoGateway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	t.Setenv("AOS_MODEL_ENDPOINT", srv.URL)
	t.Setenv("AOS_MODEL_NAME", " gpt-4o-mini ")
	t.Setenv("AOS_MODEL_API_KEY_PATH", "")
	t.Setenv("AOS_MODEL_TOOLS", "")
	t.Setenv("AOS_MODEL_ALLOWLIST_BUNDLE_DIR", "")
	cfg, err := nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("nodeConfigFromEnv com gateway: %v", err)
	}
	if cfg.Model == nil || cfg.ModelID != "gpt-4o-mini" {
		t.Fatalf("com gateway: Model=%v ModelID=%q; quero o adaptador e gpt-4o-mini", cfg.Model, cfg.ModelID)
	}

	t.Setenv("AOS_MODEL_ENDPOINT", "")
	cfg, err = nodeConfigFromEnv()
	if err != nil {
		t.Fatalf("nodeConfigFromEnv sem gateway: %v", err)
	}
	if cfg.Model != nil || cfg.ModelID != "" {
		t.Fatalf("sem gateway: Model=%v ModelID=%q; quero ambos vazios", cfg.Model, cfg.ModelID)
	}
}
