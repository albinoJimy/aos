package main

// AOS-414 — a fronteira do `POST /runs` para os payloads do plano: contrato completo, digest que
// bate com o conteúdo, e tectos. Um payload que não é o que o plano publicou não entra no run.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

func aos414Digest(s string) string {
	soma := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(soma[:])
}

func aos414Submeter(t *testing.T, node *Node, h http.Handler, runID string, inputs []map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return postJSON(h, "POST", "/runs", map[string]any{
		"run_id":        runID,
		"credential":    credencialDeTeste(t, node),
		"objective":     "verifica",
		"principal_nhi": "nhi:" + runID,
		"inputs":        inputs,
	})
}

func TestAOS414_InputsNaFronteiraDoNo(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)

	const conteudo = "o documento lido"
	casos := []struct {
		nome   string
		inputs []map[string]any
		quer   int
	}{
		{"bem formado", []map[string]any{{"from": "read_notes", "output": "notes_document", "digest": aos414Digest(conteudo), "content": conteudo}}, http.StatusCreated},
		{"digest que nao bate", []map[string]any{{"from": "read_notes", "output": "notes_document", "digest": aos414Digest("outro"), "content": conteudo}}, http.StatusBadRequest},
		{"sem contrato", []map[string]any{{"from": "", "output": "notes_document", "digest": aos414Digest(conteudo), "content": conteudo}}, http.StatusBadRequest},
		{"sem digest", []map[string]any{{"from": "read_notes", "output": "notes_document", "content": conteudo}}, http.StatusBadRequest},
		{"agregado acima do tecto", []map[string]any{
			{"from": "a", "output": "o1", "digest": aos414Digest(strings.Repeat("x", 200<<10)), "content": strings.Repeat("x", 200<<10)},
			{"from": "b", "output": "o2", "digest": aos414Digest(strings.Repeat("x", 200<<10)), "content": strings.Repeat("x", 200<<10)},
			{"from": "c", "output": "o3", "digest": aos414Digest(strings.Repeat("x", 200<<10)), "content": strings.Repeat("x", 200<<10)},
		}, http.StatusBadRequest},
		{"contrato com nome enorme", []map[string]any{{"from": strings.Repeat("n", 200), "output": "o", "digest": aos414Digest("x"), "content": "x"}}, http.StatusBadRequest},
		{"conteudo gigante", []map[string]any{{"from": "read_notes", "output": "notes_document", "digest": aos414Digest(strings.Repeat("x", 200<<10)), "content": strings.Repeat("x", 200<<10)}}, http.StatusBadRequest},
	}
	for i, k := range casos {
		t.Run(k.nome, func(t *testing.T) {
			rec := aos414Submeter(t, node, h, "run-414-"+strconv.Itoa(i), k.inputs)
			if rec.Code != k.quer {
				t.Fatalf("veio %d, quero %d (%s)", rec.Code, k.quer, rec.Body.String())
			}
		})
	}
}

// modeloQueCaptura guarda o prompt materializado do primeiro turno e conclui.
type modeloQueCaptura struct{ visto string }

func (m *modeloQueCaptura) Call(_ context.Context, v agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	if m.visto == "" {
		m.visto = string(v.Materialized)
	}
	return agentruntime.ModelResponse{Text: "fim", Final: true, Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1}}, nil
}

// O payload submetido chega REALMENTE ao prompt do run, marcado untrusted e com proveniencia.
// FALHA-ANTES: o campo `inputs` era aceite na fronteira e nunca chegava ao Goal.
func TestAOS414_PayloadSubmetidoChegaAoPromptDoRun(t *testing.T) {
	model := &modeloQueCaptura{}
	node, _ := newAPINode(t, model, false)
	defer func() { _ = node.Close() }()
	svc, h := newAPI(t, node)

	const conteudo = "o documento lido pelo no anterior"
	rec := aos414Submeter(t, node, h, "run-414-prompt", []map[string]any{
		{"from": "read_notes", "output": "notes_document", "digest": aos414Digest(conteudo), "content": conteudo},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, ok, err := svc.Wait(ctx, "run-414-prompt"); err != nil || !ok {
		t.Fatalf("run devia ter sido hospedado: ok=%v err=%v", ok, err)
	}
	for _, quer := range []string{"<plan_input", "taint=untrusted", "plan_input_from=read_notes", conteudo} {
		if !strings.Contains(model.visto, quer) {
			t.Fatalf("o prompt do run tinha de trazer %q:\n%s", quer, model.visto)
		}
	}
}
