package modelgateway_test

import (
	"context"
	"strings"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/pipeline"
	"github.com/aos-ref/platform/model-gateway/policy/weights"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/routingstage"
	"github.com/aos-ref/platform/model-gateway/routing/scoring"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// TestAOS269_Gateway_ScoredSwapCarriesScoringReason prova a CADEIA REAL de AOS-269
// ponta-a-ponta — sem substituir peça nenhuma por um duplo:
//
//	tabela de pesos ASSINADA (policy/weights, trust anchor pinado)
//	  → scorer REAL (routing/scoring, aritmética inteira)
//	    → router REAL com WithScoring (guardas antes do score)
//	      → estágio de roteamento REAL (routingstage)
//	        → Gateway REAL (pipeline + sink de variância)
//
// O invariante provado é a regra 5 do ADR-021: quando o scoring escolhe um modelo
// DIFERENTE do pedido, a variância model_swap emitida pelo GW carrega a RAZÃO DE
// SCORING — perfil de pesos, versão da tabela, score e factores. Uma troca ponderada
// nunca é silenciosa nem inexplicável a posteriori.
func TestAOS269_Gateway_ScoredSwapCarriesScoringReason(t *testing.T) {
	f := adapters.NewFakeAdapter("openai")

	// Tabela de pesos EMBEBIDA e ASSINADA (o artefacto de produção), perfil "cheap":
	// o factor custo domina, pelo que o vencedor é o tier mais barato capaz.
	tab, err := weights.LoadTable()
	if err != nil {
		t.Fatalf("weights.LoadTable (artefacto assinado): %v", err)
	}
	ladder := wiringLadder()
	sc, err := scoring.NewScorer(scoring.TableFrom(tab), "cheap",
		scoring.WithCost(scoring.CostFromLadder(ladder)),
		scoring.WithLatency(scoring.NewStaticLatency(true)),
		scoring.WithHealth(scoring.NewStaticHealth(scoring.Scale)),
		scoring.WithStability(scoring.NewStaticStability(scoring.Scale)),
		scoring.WithTaskFit(scoring.NewStaticTaskFit(0).Set("small", tiering.CapabilityBasic, 900)),
	)
	if err != nil {
		t.Fatalf("NewScorer sobre a tabela assinada: %v", err)
	}

	r := router.New(ladder, router.WithAllowlist(routeAllow{}), router.WithScoring(sc))
	stage := routingstage.NewStage(r, routingstage.WithClassifier(func(*pipeline.Exchange) routingstage.Task {
		return routingstage.Task{Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch}
	}))
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
		t.Fatalf("o perfil 'cheap' devia eleger o tier mais barato capaz (small), obtive %q", resp.Model)
	}

	var swap *modelgateway.VarianceEvent
	for i := range variances {
		if variances[i].Kind == "model_swap" && variances[i].RequestedModel == "big" && variances[i].ResolvedModel == "small" {
			swap = &variances[i]
		}
	}
	if swap == nil {
		t.Fatalf("a troca ponderada tem de emitir variância model_swap, obtive %+v", variances)
	}
	// A RAZÃO da variância traz o scoring completo (ADR-021 regra 5 + §5 Observabilidade).
	for _, want := range []string{"scoring ponderado determinista", "perfil=cheap", "pesos=" + tab.Version(), "score=", "factores[health="} {
		if !strings.Contains(swap.Reason, want) {
			t.Fatalf("a razão do model_swap não contém %q: %q", want, swap.Reason)
		}
	}
}

// TestAOS269_Gateway_SemScoringMantemComportamento é o CONTROLO DE COMPATIBILIDADE
// da postura escolhida (scoring OPT-IN por composição): o MESMO gateway, com o mesmo
// router SEM WithScoring e sem tabela de pesos nenhuma, continua a rotear pela
// composição lexicográfica de AOS-059 — e a razão do model_swap NÃO menciona
// scoring. É a prova de que este ticket não parte um nó já implantado.
func TestAOS269_Gateway_SemScoringMantemComportamento(t *testing.T) {
	f := adapters.NewFakeAdapter("openai")
	r := router.New(wiringLadder(), router.WithAllowlist(routeAllow{}))
	stage := routingstage.NewStage(r, routingstage.WithClassifier(func(*pipeline.Exchange) routingstage.Task {
		return routingstage.Task{Capability: tiering.CapabilityBasic, Class: tiering.ClassBatch}
	}))
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
		t.Fatalf("sem scoring o router mantém AOS-059 (small), obtive %q", resp.Model)
	}
	for _, v := range variances {
		if v.Kind == "model_swap" && strings.Contains(v.Reason, "scoring") {
			t.Fatalf("sem scoring composto a razão não pode mencionar scoring: %q", v.Reason)
		}
	}
}
