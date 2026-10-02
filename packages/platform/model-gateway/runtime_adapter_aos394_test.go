package modelgateway_test

import (
	"context"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// capturaGateway regista o ChatRequest que o adaptador entrega ao gateway. Só Chat é exercitado;
// os restantes métodos da porta ficam no valor embebido (nil) e não são chamados.
type capturaGateway struct {
	port.Gateway
	req port.ChatRequest
}

func (g *capturaGateway) Chat(_ context.Context, req port.ChatRequest) (port.ChatResponse, error) {
	g.req = req
	return port.ChatResponse{Choices: []port.Choice{{
		Message:      port.Message{Role: port.RoleAssistant, Content: "ok"},
		FinishReason: "stop",
	}}}, nil
}

// TestAOS394_Adaptador_CorrelacaoPorChamada fixa a precedência da correlação que chega ao gateway.
func TestAOS394_Adaptador_CorrelacaoPorChamada(t *testing.T) {
	t.Parallel()
	view := agentruntime.PromptView{Turn: 1, Materialized: []byte("x")}
	ctxTurno := agentruntime.ContextWithModelCall(context.Background(), "run-do-ctx", "step-000004")

	casos := []struct {
		nome               string
		opts               []modelgateway.RuntimeAdapterOption
		ctx                context.Context
		querRun, querPasso string
	}{
		// Adaptador por nó: o run e o passo vêm do turno.
		{"ctx por chamada", nil, ctxTurno, "run-do-ctx", "step-000004"},
		// O ctx ganha ao run fixado na construção.
		{"ctx ganha a WithRun", []modelgateway.RuntimeAdapterOption{modelgateway.WithRun("run-de-construcao")}, ctxTurno, "run-do-ctx", "step-000004"},
		// Adaptador por run sem ctx: fica o run de construção, sem passo inventado.
		{"WithRun como fallback", []modelgateway.RuntimeAdapterOption{modelgateway.WithRun("run-de-construcao")}, context.Background(), "run-de-construcao", ""},
		// Sem nenhum dos dois: a ausência segue visível.
		{"sem correlacao", nil, context.Background(), "", ""},
		// O anexo vale INTEIRO: um par com o run vazio NÃO é completado com o run de
		// construção — isso selaria o passo de um run no nome de outro.
		{
			"anexo com run vazio nao mistura com WithRun",
			[]modelgateway.RuntimeAdapterOption{modelgateway.WithRun("run-de-construcao")},
			agentruntime.ContextWithModelCall(context.Background(), "", "step-000011"),
			"", "step-000011",
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			gw := &capturaGateway{}
			mc := modelgateway.NewModelClient(gw, "gpt-4o", c.opts...)
			if _, err := mc.Call(c.ctx, view); err != nil {
				t.Fatalf("Call: %v", err)
			}
			if gw.req.RunID != c.querRun || gw.req.StepID != c.querPasso {
				t.Fatalf("ChatRequest com RunID=%q StepID=%q; quero %q/%q", gw.req.RunID, gw.req.StepID, c.querRun, c.querPasso)
			}
		})
	}
}
