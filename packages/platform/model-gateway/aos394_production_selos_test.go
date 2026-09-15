package modelgateway_test

// AOS-394 — os selos de governação da composição de PRODUÇÃO (NewProduction) levam o run e o passo
// do pedido nos dois veredictos que o nó de referência não consegue provocar: o deny de failover
// cross-border (o nó compõe uma só conta na região pedida) e a troca de modelo pelo refino (o nó
// não declara escada de tiers, DEF-280-NO).

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/routing/degradation"
	"github.com/aos-ref/platform/model-gateway/routing/failover"
)

const (
	aos394RunID  = "run-aos394-prod"
	aos394StepID = "step-000005"
)

// chatEUComPasso é o chatEU com a correlação de run e passo preenchida.
func chatEUComPasso(gw *modelgateway.Gateway) error {
	_, err := gw.Chat(context.Background(), port.ChatRequest{
		Model: "gpt-4o", Principal: "tok", Board: "board-eu", Region: "eu",
		RunID: aos394RunID, StepID: aos394StepID,
		Messages: []port.Message{{Role: port.RoleUser, Content: "olá"}},
	})
	return err
}

// selosComPasso verifica que TODOS os registos da partição levam o run e o passo, e devolve quantos são.
func selosComPasso(t *testing.T, store *audit.MemStore, partition string) int {
	t.Helper()
	ctx := context.Background()
	head, err := store.Head(ctx, partition)
	if err != nil {
		t.Fatalf("Head(%s): %v", partition, err)
	}
	for i := uint64(1); i <= head; i++ {
		rec, ok, err := store.At(ctx, partition, i)
		if err != nil || !ok {
			t.Fatalf("At(%s, %d): ok=%v err=%v", partition, i, ok, err)
		}
		if rec.RunID != aos394RunID || rec.StepID != aos394StepID {
			t.Fatalf("selo #%d (%s %s) com RunID=%q StepID=%q; quero %s/%s",
				i, rec.Decision, rec.Resource.Value, rec.RunID, rec.StepID, aos394RunID, aos394StepID)
		}
	}
	return int(head)
}

// TestAOS394_Production_DenyCrossBorderLigaORunEOPasso: só há capacidade fora da fronteira do
// board; o deny de failover fica selado com o run e o passo do pedido.
func TestAOS394_Production_DenyCrossBorderLigaORunEOPasso(t *testing.T) {
	store := audit.NewMemStore()
	var hits int
	srv := okOpenAIServer(t, &hits)
	cfg := prodConfig(store, srv.URL, srv.Client(),
		[]modelgateway.InfraAccount{{KeyID: "acct-us-1", Provider: "openai", Region: "us-east", LimitRPM: 100, LimitTPM: 100000}})
	cfg.Credentials = testCreds{"openai|us-east": "sk-infra-us"}
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	if err := chatEUComPasso(gw); !errors.Is(err, failover.ErrCrossBorderBlocked) {
		t.Fatalf("o failover cross-border tinha de bloquear; got %v", err)
	}
	if n := selosComPasso(t, store, "modelgw-gov:board-eu"); n < 1 {
		t.Fatal("o deny cross-border tinha de ficar selado")
	}
}

// TestAOS394_Production_TrocaDeModeloLigaORunEOPasso: a 90% do orçamento o refino troca gpt-4o por
// gpt-4o-mini; os dois registos da chamada (par pedido e par efectivo) levam o run e o passo.
func TestAOS394_Production_TrocaDeModeloLigaORunEOPasso(t *testing.T) {
	key := degradation.BudgetKey{Board: "board-eu", Tenant: "board-eu"}
	store := audit.NewMemStore()
	srv, log := recordingProviderServer(t)
	cfg := prodConfig(store, srv.URL, srv.Client(), twoRegionAccounts())
	cfg.Routing = modelgateway.RoutingConfig{
		Tiers:   chainTiers(),
		Profile: "quality",
		TaskFit: taskFitFavouring("gpt-4o"),
		Budget: degradation.NewStaticBudgetProvider(degradation.BudgetState{}).
			Set(key, degradation.BudgetState{Used: 90, Limit: 100}),
	}
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewProduction: %v", err)
	}
	if err := chatEUComPasso(gw); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := log.last(t); got.Model != "gpt-4o-mini" {
		t.Fatalf("pre-condicao do teste: esperava-se a troca para gpt-4o-mini, despachou %q", got.Model)
	}
	if !hasSealedModel(t, store, "modelgw-gov:board-eu", audit.DecisionAllow, "gpt-4o-mini") {
		t.Fatal("a troca de modelo tinha de ficar selada")
	}
	if n := selosComPasso(t, store, "modelgw-gov:board-eu"); n != 2 {
		t.Fatalf("esperava 2 registos (par pedido e par efectivo), veio %d", n)
	}
}
