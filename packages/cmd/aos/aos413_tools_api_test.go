package main

// AOS-413 (ADR-027) — o campo `tools` do `POST /runs` é a lista-branca do run: pelo nó real, uma
// tool call fora dela é cortada antes do Reference Monitor e a tool não executa.

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

func TestAOS413_ToolsDoPostRunsCortaAToolForaDaLista(t *testing.T) {
	// ["outra"]: a tool pedida não está na lista. []: o nó do plano sem tools pinadas — lista
	// PRESENTE e vazia, que nega tudo (ausente seria «sem restrição»).
	for nome, tools := range map[string][]string{"fora da lista": {"outra"}, "lista vazia": {}} {
		t.Run(nome, func(t *testing.T) { aos413CortaAntesDoRM(t, tools) })
	}
}

func aos413CortaAntesDoRM(t *testing.T, tools []string) {
	t.Helper()
	cfg := tnBaseConfig()
	model := &toolEmittingModel{inv: agentruntime.ToolInvocation{
		ToolID:     "echo",
		Capability: tnCap,
		Input:      []byte("ping"),
	}}
	cfg.Model = model
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()

	executou := 0
	if err := node.Runtime.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		executou++
		return in, nil
	}); err != nil {
		t.Fatalf("Register(echo): %v", err)
	}
	tok, err := node.Authority.MintForHuman(context.Background(), tnHuman, tnAgent, tnClass, []string{tnCap})
	if err != nil {
		t.Fatalf("MintForHuman: %v", err)
	}
	svc, h := newAPI(t, node)
	antesP, antesD, _ := node.Runtime.Monitor().Metrics().Snapshot()

	rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id":        "run-413.n1",
		"objective":     "o trabalho de um no do plano",
		"principal_nhi": medAgentID,
		"credential":    tok.Compact,
		"tools":         tools,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /runs devia dar 201, veio %d (%s)", rec.Code, rec.Body.String())
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, ok, werr := svc.Wait(waitCtx, "run-413.n1"); werr != nil || !ok {
		t.Fatalf("o run devia ter sido hospedado: ok=%v err=%v", ok, werr)
	}
	if model.turns < 2 {
		t.Fatalf("o modelo devia ter emitido a tool call; turnos=%d", model.turns)
	}
	if executou != 0 {
		t.Fatalf("a tool fora da lista-branca do run EXECUTOU %d vez(es)", executou)
	}
	// Cortada ANTES do RM: nenhuma mediação nova. (No teste AOS-169, sem lista-branca, a mesma
	// call atravessa o RM e o contador move-se.)
	depoisP, depoisD, _ := node.Runtime.Monitor().Metrics().Snapshot()
	if depoisP+depoisD != antesP+antesD {
		t.Fatalf("a call fora da lista-branca chegou ao RM (antes=%d, depois=%d)", antesP+antesD, depoisP+depoisD)
	}
}

func TestAOS413_ToolsComEntradaVaziaERecusado(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	_, h := newAPI(t, node)
	rec := postJSON(h, "POST", "/runs", map[string]any{
		"run_id":        "run-413-vazio",
		"objective":     "x",
		"principal_nhi": "nhi:run-413-vazio",
		"tools":         []string{"fs.read", ""},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("uma entrada vazia em tools devia dar 400, veio %d (%s)", rec.Code, rec.Body.String())
	}
}
