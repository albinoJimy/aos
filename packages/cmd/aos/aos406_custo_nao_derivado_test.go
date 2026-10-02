package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// AOS-406 — sem fonte de preço, o nó marca cada turno como custo NÃO DERIVADO.

func aos406ClienteDoAmbiente(t *testing.T, pricingPath string) agentruntime.ModelClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	t.Setenv("AOS_MODEL_ENDPOINT", srv.URL)
	t.Setenv("AOS_MODEL_NAME", "gpt-4o-mini")
	t.Setenv("AOS_MODEL_REGION", "eu")
	t.Setenv("AOS_MODEL_API_KEY_PATH", "")
	t.Setenv("AOS_MODEL_TOOLS", "")
	t.Setenv("AOS_MODEL_ALLOWLIST_BUNDLE_DIR", "")
	t.Setenv("AOS_MODEL_PRICING_PATH", pricingPath)
	mc, _, err := parseModelFromEnv(false)
	if err != nil {
		t.Fatalf("parseModelFromEnv: %v", err)
	}
	return mc
}

// TestAOS406_SemPrecoOClienteMarcaOCusto é o par de produção (gpt-4o-mini em `eu`, que a tabela
// embebida não cobre) sem tools; a cadeia com tools está no teste seguinte. FALHA-ANTES: o cliente
// devolvia o zero do gateway sem marca.
func TestAOS406_SemPrecoOClienteMarcaOCusto(t *testing.T) {
	mc := aos406ClienteDoAmbiente(t, "")
	if _, ok := mc.(custoNaoDerivadoClient); !ok {
		t.Fatalf("sem fonte de preço o cliente tem de ser custoNaoDerivadoClient; veio %T", mc)
	}
}

// TestAOS406_ComToolsODecoradorFicaPorDentroDoEnriquecedor é a cadeia de produção: há tools
// montadas, pelo que o cliente exterior é o enriquecedor e o decorador tem de estar por dentro.
// Uma composição que só marcasse o custo sem tools passaria o teste anterior e partiria produção.
func TestAOS406_ComToolsODecoradorFicaPorDentroDoEnriquecedor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	t.Setenv("AOS_MODEL_ENDPOINT", srv.URL)
	t.Setenv("AOS_MODEL_NAME", "gpt-4o-mini")
	t.Setenv("AOS_MODEL_REGION", "eu")
	t.Setenv("AOS_MODEL_API_KEY_PATH", "")
	t.Setenv("AOS_MODEL_ALLOWLIST_BUNDLE_DIR", "")
	t.Setenv("AOS_MODEL_PRICING_PATH", "")
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, `[{"name":"doc_read","capability":"cap:fs.read","resource_type":"file","resource_value":"doc://notes","resource_region":"eu"}]`))
	mc, _, err := parseModelFromEnv(false)
	if err != nil {
		t.Fatalf("parseModelFromEnv: %v", err)
	}
	enriquecedor, ok := mc.(*toolEnrichingClient)
	if !ok {
		t.Fatalf("com tools o cliente exterior é o enriquecedor; veio %T", mc)
	}
	if _, ok := enriquecedor.inner.(custoNaoDerivadoClient); !ok {
		t.Fatalf("o decorador de custo tem de estar por dentro do enriquecedor; veio %T", enriquecedor.inner)
	}
	// E a marca atravessa o enriquecedor.
	enriquecedor.inner = custoNaoDerivadoClient{inner: aos406Modelo{resp: agentruntime.ModelResponse{Text: "ok"}}}
	resp, err := enriquecedor.Call(context.Background(), agentruntime.PromptView{})
	if err != nil || !resp.CustoNaoDerivado {
		t.Fatalf("a marca tem de sair do enriquecedor: resp=%+v err=%v", resp, err)
	}
}

// TestAOS406_AlertaDeCustoSemFonteDePrecoNaoTemProdutor — sem preço o SLI nunca tem amostras, e
// `produtor="1"` diria ao operador que a regra pode disparar.
func TestAOS406_AlertaDeCustoSemFonteDePrecoNaoTemProdutor(t *testing.T) {
	if produtorDoAlerta(otelgenai.SLICostPerTrajectory, true, true) {
		t.Fatal("sem fonte de preço o alerta de custo não tem produtor neste nó")
	}
	if !produtorDoAlerta(otelgenai.SLICostPerTrajectory, true, false) {
		t.Fatal("com preço e torneira o produtor existe")
	}
	if !produtorDoAlerta(otelgenai.SLIMediationOverheadP95, true, true) {
		t.Fatal("a postura de preço só mexe no SLI de custo")
	}
}

func TestAOS406_ComPrecoOClienteNaoEDecorado(t *testing.T) {
	mc := aos406ClienteDoAmbiente(t, tabelaDePrecosDeTeste(t, "gpt-4o-mini", "eu"))
	if _, ok := mc.(custoNaoDerivadoClient); ok {
		t.Fatal("com a tabela a cobrir o par, o custo é derivado e o cliente não leva a marca")
	}
}

type aos406Modelo struct {
	resp agentruntime.ModelResponse
	err  error
}

func (m aos406Modelo) Call(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return m.resp, m.err
}

func TestAOS406_DecoradorMarcaEZeraOCusto(t *testing.T) {
	c := custoNaoDerivadoClient{inner: aos406Modelo{resp: agentruntime.ModelResponse{
		Text: "ok", Usage: agentruntime.Usage{InputTokens: 7, OutputTokens: 3}, CostMicroUSD: 99,
	}}}
	resp, err := c.Call(context.Background(), agentruntime.PromptView{})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.CustoNaoDerivado || resp.CostMicroUSD != 0 {
		t.Fatalf("resposta = %+v, quero CustoNaoDerivado e custo 0", resp)
	}
	if resp.Usage.InputTokens != 7 || resp.Text != "ok" {
		t.Fatalf("o resto da resposta não pode mudar: %+v", resp)
	}

	falha := errors.New("provider em baixo")
	c = custoNaoDerivadoClient{inner: aos406Modelo{err: falha}}
	if _, err := c.Call(context.Background(), agentruntime.PromptView{}); !errors.Is(err, falha) {
		t.Fatalf("o erro do modelo passa intacto: %v", err)
	}
}

func TestAOS406_BannerDeclaraAPosturaDeSubscricao(t *testing.T) {
	linhas := modelPricingPostureBanner(true, modelPricingPosture{Armed: false, Model: "gpt-4o-mini", Region: "eu", TableVersion: "2026.07#abc"})
	if len(linhas) != 1 {
		t.Fatalf("banner: %v", linhas)
	}
	for _, quer := range []string{"custo_nao_derivado=true", "aos.cost.undefined=true", "SUBSCRICAO", "sem amostras"} {
		if !strings.Contains(linhas[0], quer) {
			t.Errorf("o banner sem fonte de preço tem de dizer %q", quer)
		}
	}
}
