package main

// AOS-395 — a chamada de decomposição leva o run e o passo da tentativa até ao gateway, e a
// ausência de correlação fica visível em vez de inventada. É o vector unitário do critério;
// o processo real (com WORM durável) está em aos395_audit_duravel_test.go.

import (
	"context"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

func TestAOS395_Complete_LevaRunEPassoDoCtx(t *testing.T) {
	fake := &fakeGateway{resposta: `{"plan_version":"1.0.0"}`}
	m := gatewayDecomposeModel{gw: fake, model: "gpt-4o", region: "eu", board: "board-eu", principal: "tok"}

	ctx := agentruntime.ContextWithModelCall(context.Background(), "run-395", "planstep:decompose:1")
	if _, err := m.Complete(ctx, "sistema", "objectivo"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if fake.ultimoReq.RunID != "run-395" || fake.ultimoReq.StepID != "planstep:decompose:1" {
		t.Fatalf("ChatRequest com RunID=%q StepID=%q; quero run-395/planstep:decompose:1",
			fake.ultimoReq.RunID, fake.ultimoReq.StepID)
	}
}

func TestAOS395_Complete_SemCtxMostraAAusencia(t *testing.T) {
	fake := &fakeGateway{resposta: `{"plan_version":"1.0.0"}`}
	m := gatewayDecomposeModel{gw: fake, model: "gpt-4o", region: "eu", board: "board-eu", principal: "tok"}

	if _, err := m.Complete(context.Background(), "sistema", "objectivo"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if fake.ultimoReq.RunID != "" || fake.ultimoReq.StepID != "" {
		t.Fatalf("sem anexo no ctx os campos tinham de ficar vazios; veio RunID=%q StepID=%q",
			fake.ultimoReq.RunID, fake.ultimoReq.StepID)
	}
}
