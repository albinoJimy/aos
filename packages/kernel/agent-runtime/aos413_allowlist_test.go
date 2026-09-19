package agentruntime

// AOS-413 (ADR-027) — a lista-branca do run: um run que é o trabalho de UM nó de um plano só
// pode chamar as tools que o plano pinou a esse nó, mesmo que o token autorize mais.

import (
	"bytes"
	"context"
	"testing"
)

// aos413Echo regista duas tools permitidas pelo RM ("echo" e "outra") e conta as execuções.
func aos413Echo(t *testing.T) (*harness, map[string]int) {
	t.Helper()
	h := newHarness(t, nil)
	execucoes := map[string]int{}
	for _, nome := range []string{"echo", "outra"} {
		nome := nome
		if err := h.rm.Register(nome, func(_ context.Context, in []byte) ([]byte, error) {
			execucoes[nome]++
			return append([]byte(nome+":"), in...), nil
		}); err != nil {
			t.Fatalf("Register %s: %v", nome, err)
		}
	}
	return h, execucoes
}

func aos413ChamaEFinaliza(tool string) func(int) ModelResponse {
	return func(turn int) ModelResponse {
		if turn == 1 {
			return ModelResponse{ToolCalls: []ToolInvocation{{ToolID: tool, Capability: "cap:echo", Input: []byte("x")}}}
		}
		return ModelResponse{Text: "fim", Final: true}
	}
}

func TestAOS413_ToolForaDaListaBrancaENegadaSemDespacho(t *testing.T) {
	h, execucoes := aos413Echo(t)
	model := &capturingPrompts{responder: aos413ChamaEFinaliza("outra")}
	goal := sampleGoal()
	goal.AllowedTools = []string{"echo"}
	if _, err := New(model, h.rm, h.recorder).Run(context.Background(), goal); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if execucoes["outra"] != 0 {
		t.Fatalf("a tool fora da lista-branca EXECUTOU %d vez(es)", execucoes["outra"])
	}
	if len(model.views) < 2 || !bytes.Contains(model.views[1], []byte("denied_code="+CodeToolOutsideRunAllowlist)) {
		t.Fatalf("o modelo tinha de ver a negação com o código da lista-branca; views=%q", model.views)
	}
}

func TestAOS413_ToolNaListaBrancaCorreComoAntes(t *testing.T) {
	h, execucoes := aos413Echo(t)
	model := &capturingPrompts{responder: aos413ChamaEFinaliza("echo")}
	goal := sampleGoal()
	goal.AllowedTools = []string{"echo"}
	if _, err := New(model, h.rm, h.recorder).Run(context.Background(), goal); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if execucoes["echo"] != 1 {
		t.Fatalf("a tool na lista-branca tinha de executar uma vez, executou %d", execucoes["echo"])
	}
}

func TestAOS413_SemListaBrancaNadaMuda(t *testing.T) {
	h, execucoes := aos413Echo(t)
	model := &capturingPrompts{responder: aos413ChamaEFinaliza("outra")}
	if _, err := New(model, h.rm, h.recorder).Run(context.Background(), sampleGoal()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if execucoes["outra"] != 1 {
		t.Fatalf("sem lista-branca a tool autorizada tinha de executar, executou %d", execucoes["outra"])
	}
}

// Uma lista-branca PRESENTE e vazia nega tudo: é o nó do plano sem tools pinadas. Se valesse
// como «sem restrição», esse nó herdava as tools de todo o token do run.
func TestAOS413_ListaBrancaVaziaNegaTudo(t *testing.T) {
	h, execucoes := aos413Echo(t)
	model := &capturingPrompts{responder: aos413ChamaEFinaliza("echo")}
	goal := sampleGoal()
	goal.AllowedTools = []string{}
	if _, err := New(model, h.rm, h.recorder).Run(context.Background(), goal); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if execucoes["echo"] != 0 {
		t.Fatalf("com a lista-branca vazia a tool EXECUTOU %d vez(es)", execucoes["echo"])
	}
}
