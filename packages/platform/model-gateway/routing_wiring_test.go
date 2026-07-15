package modelgateway_test

import (
	"context"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/routing/degradation"
	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/routingstage"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// routeAllow permite qualquer triplo (o wiring de soberania real é AOS-058; aqui
// prova-se a integração do estágio na pipeline do GW).
type routeAllow struct{}

func (routeAllow) Allows(_, _, _ string) bool { return true }

func wiringLadder() *tiering.Ladder {
	return tiering.NewLadder(
		tiering.Tier{Name: "frontier", Model: "big", CostRank: 3, Capability: tiering.CapabilityFrontier},
		tiering.Tier{Name: "standard", Model: "mid", CostRank: 2, Capability: tiering.CapabilityStandard},
		tiering.Tier{Name: "economy", Model: "small", CostRank: 1, Capability: tiering.CapabilityBasic},
	)
}

// TestGateway_RoutingStage_ResolvesModel prova o WIRING de AOS-059: com o estágio
// de roteamento real ligado (WithRoutingStage), o GW resolve o modelo pedido para
// o tier escolhido pelo router e invoca o adaptador com esse modelo.
func TestGateway_RoutingStage_ResolvesModel(t *testing.T) {
	f := adapters.NewFakeAdapter("openai")
	// O router escolhe "small" (economy) para uma tarefa basic/batch.
	r := router.New(wiringLadder(),
		router.WithAllowlist(routeAllow{}),
	)
	stage := routingstage.NewStage(r, routingstage.WithClassifier(func(*pipeline.Exchange) routingstage.Task {
		return routingstage.Task{Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch}
	}))
	// resposta para o modelo RESOLVIDO (small).
	f.SetChatResponse("small", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3},
	})

	var variances []modelgateway.VarianceEvent
	gw := newGateway(t, f,
		modelgateway.WithRoutingStage(stage),
		modelgateway.WithVarianceSink(modelgateway.VarianceSinkFunc(func(_ context.Context, ev modelgateway.VarianceEvent) {
			variances = append(variances, ev)
		})),
	)

	resp, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "big", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Model != "small" {
		t.Errorf("o GW deve resolver para o tier escolhido (small), obtive %q", resp.Model)
	}
	// O swap de modelo (big pedido -> small resolvido) é VARIÂNCIA explícita.
	found := false
	for _, v := range variances {
		if v.Kind == "model_swap" && v.RequestedModel == "big" && v.ResolvedModel == "small" {
			found = true
		}
	}
	if !found {
		t.Errorf("o swap de modelo deve emitir variancia explicita (model_swap), obtive %+v", variances)
	}
}

// TestGateway_RoutingStage_DeferFailsClosed prova que um defer do router (sem
// headroom de admissão) FALHA-FECHA a chamada: o adaptador NÃO é invocado.
func TestGateway_RoutingStage_DeferFailsClosed(t *testing.T) {
	f := adapters.NewFakeAdapter("openai")
	adm := router.NewStaticAdmissionCoordinator(1000, 1, 500)
	// Pré-consome o único request.
	_, _ = adm.Reserve(context.Background(), router.AdmissionRequest{Provider: "openai", Model: "small", Region: "eu", EstimatedTokens: 1})
	r := router.New(wiringLadder(), router.WithAllowlist(routeAllow{}), router.WithAdmission(adm))
	stage := routingstage.NewStage(r, routingstage.WithClassifier(func(*pipeline.Exchange) routingstage.Task {
		return routingstage.Task{Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch, EstimatedTokens: 1}
	}))
	gw := newGateway(t, f, modelgateway.WithRoutingStage(stage))

	_, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "big", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err == nil {
		t.Fatal("defer sem headroom deve falhar-fechar a chamada")
	}
	if f.Calls() != 0 {
		t.Errorf("o adaptador NAO deve ser invocado num defer fail-closed, chamado %d vezes", f.Calls())
	}
}

// TestGateway_RoutingStage_GracefulExhaustion prova a exaustão graciosa end-to-end:
// a ~80% do orçamento o GW degrada para um tier mais barato (continuação) em vez de
// parar; a resposta vem do modelo degradado.
func TestGateway_RoutingStage_GracefulExhaustion(t *testing.T) {
	f := adapters.NewFakeAdapter("openai")
	// Escada com dois tiers Frontier (rápido/caro e lento/barato): a exaustão
	// graciosa degrada PRESERVANDO a capacidade Frontier — nunca para um tier incapaz.
	dl := tiering.NewLadder(
		tiering.Tier{Name: "frontier-fast", Model: "big-fast", CostRank: 3, Capability: tiering.CapabilityFrontier, Fast: true},
		tiering.Tier{Name: "frontier-cheap", Model: "big-cheap", CostRank: 2, Capability: tiering.CapabilityFrontier},
		tiering.Tier{Name: "economy", Model: "small", CostRank: 1, Capability: tiering.CapabilityBasic},
	)
	budget := degradation.NewStaticBudgetProvider(degradation.BudgetState{}).
		Set(degradation.BudgetKey{Board: "board-eu", Tenant: "board-eu"}, degradation.BudgetState{Used: 90, Limit: 100})
	r := router.New(dl, router.WithAllowlist(routeAllow{}), router.WithBudget(budget))
	stage := routingstage.NewStage(r, routingstage.WithClassifier(func(*pipeline.Exchange) routingstage.Task {
		return routingstage.Task{Capability: tiering.CapabilityFrontier, Class: tiering.ClassInteractive}
	}))
	f.SetChatResponse("big-cheap", port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "degradado"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
	})
	gw := newGateway(t, f, modelgateway.WithRoutingStage(stage))

	resp, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "big-fast", Board: "board-eu", Region: "eu",
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	// frontier-fast degradou para o Frontier barato (big-cheap) por exaustão graciosa
	// — preservando a capacidade exigida, nunca parou nem baixou de capacidade.
	if resp.Model != "big-cheap" {
		t.Errorf("exaustao graciosa deve degradar para o Frontier barato (big-cheap), obtive %q", resp.Model)
	}
}
