package modelgateway_test

// AOS-530 (peça 3b; ADR-042 §2.6) — UMA RECUSA PAGA LEVA O CUSTO A QUEM A PEDIU, SE FOR UMA TOOL.
//
// A comparação do modelo servido em `enforce` recusa uma resposta que o provider já deu e que o
// gateway já contou pela tabela de preços. A tool cujo efeito é essa chamada tem de gravar o custo
// («custou e grava-se»); um turno do executor tem de ver o erro de sempre, o mesmo valor.

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/metering/cost"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/pricing"
)

func TestAOS530_RecusaPagaLevaOCustoSoAUmaTool(t *testing.T) {
	proxy := &aos505Proxy{headers: aos505Cabecalhos("openai/outro", aos505APIBaseA)}
	srv := httptest.NewServer(proxy)
	t.Cleanup(srv.Close)
	tabela, err := pricing.NewTable("t-530", []pricing.Entry{{Model: "gpt-4o", Region: "eu", Rate: pricing.Rate{
		InputPerMTokMicroUSD: 2_000_000, OutputPerMTokMicroUSD: 8_000_000}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := prodConfig(audit.NewMemStore(), srv.URL, srv.Client(),
		[]modelgateway.InfraAccount{{KeyID: "acct-eu-1", Provider: "openai", Region: "eu"}})
	cfg.Cost = cost.NewRecorder(cost.NewCalculator(tabela))
	cfg.Route = modelgateway.RouteGovernance{Mode: modelgateway.RouteGovernanceEnforce}
	gw, err := modelgateway.NewProduction(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}

	// O TURNO: o erro de sempre, sem invólucro.
	_, errTurno := gw.Chat(context.Background(), aos505Pedido("gpt-4o"))
	var rerr *modelgateway.RouteVarianceError
	if !errors.As(errTurno, &rerr) || !errors.Is(errTurno, modelgateway.ErrRouteVariance) {
		t.Fatalf("o turno tinha de falhar com a variancia; veio %v", errTurno)
	}
	if _, _, pago := modelgateway.PaidRejectionOf(errTurno); pago {
		t.Fatal("um turno nao recebe o invólucro do custo")
	}
	var envolvido *modelgateway.PaidRejectionError
	if errors.As(errTurno, &envolvido) {
		t.Fatal("o erro de um turno nao e um PaidRejectionError")
	}

	// A TOOL: o mesmo erro, com o custo medido e o modelo servido.
	pedido := aos505Pedido("gpt-4o")
	pedido.OriginTool = "ocr_read"
	_, errTool := gw.Chat(context.Background(), pedido)
	if !errors.As(errTool, &rerr) || !errors.Is(errTool, modelgateway.ErrRouteVariance) {
		t.Fatalf("o erro da tool continua a ser a variancia; veio %v", errTool)
	}
	if errTool.Error() != errTurno.Error() {
		t.Fatalf("o texto do erro e o mesmo: %q vs %q", errTool, errTurno)
	}
	usage, servido, pago := modelgateway.PaidRejectionOf(errTool)
	// 10 tokens de entrada a 2 USD/Mtok + 5 de saída a 8 USD/Mtok = 20 + 40 micro-USD.
	if !pago || usage.PromptTokens != 10 || usage.CompletionTokens != 5 || usage.CostMicroUSD != 60 || servido != "openai/outro" {
		t.Fatalf("a recusa paga tinha de levar o usage medido, o custo (60) e o modelo servido: pago=%v %+v %q", pago, usage, servido)
	}

	// UMA RECUSA ANTES DO PROVIDER não é paga: sem invólucro, mesmo numa tool.
	semImagem := aos505Pedido("gpt-4o")
	semImagem.OriginTool = "ocr_read"
	semImagem.Messages[0].Images = []port.ImagePart{{MediaType: port.ImageMediaPNG, Data: []byte("\x89PNG\r\n\x1a\nresto")}}
	antes := len(proxy.pedidos())
	if _, err := gw.Chat(context.Background(), semImagem); err == nil {
		t.Fatal("uma imagem para uma rota sem a capacidade e recusada")
	} else if _, _, pago := modelgateway.PaidRejectionOf(err); pago {
		t.Fatalf("uma recusa antes do provider nao e paga: %v", err)
	}
	if len(proxy.pedidos()) != antes {
		t.Fatal("a recusa por capacidade nao chega ao provider")
	}
}
