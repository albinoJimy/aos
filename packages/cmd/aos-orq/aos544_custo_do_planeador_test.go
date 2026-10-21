package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-544: o custo do planeador. Com a tabela de preços do operador (AOS_MODEL_PRICING_PATH) o
// gateway deriva o custo de cada chamada e o medidor soma-o e declara-o MEDIDO ao nó; sem a tabela,
// os dólares ficam não medidos, como antes.

const aos544Tabela = `{"version":"t-544","entries":[{"model":"gpt-4o","region":"eu","rate":{"input_per_mtok_micro_usd":100000,"output_per_mtok_micro_usd":500000,"cache_read_per_mtok_micro_usd":0,"cache_write_per_mtok_micro_usd":0}}]}`

func aos544Ficheiro(t *testing.T, corpo string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pricing.json")
	if err := os.WriteFile(p, []byte(corpo), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAOS544_TabelaDoPlaneador(t *testing.T) {
	t.Setenv("AOS_MODEL_PRICING_PATH", "")
	if c, err := custoDoPlaneamentoFromEnv("gpt-4o", "eu"); err != nil || c != nil {
		t.Fatalf("sem tabela externa: nao arma (c=%v err=%v)", c, err)
	}
	t.Setenv("AOS_MODEL_PRICING_PATH", aos544Ficheiro(t, aos544Tabela))
	if c, err := custoDoPlaneamentoFromEnv("gpt-4o", "eu"); err != nil || c == nil {
		t.Fatalf("com a tabela e o par: arma (c=%v err=%v)", c, err)
	}
	if _, err := custoDoPlaneamentoFromEnv("outro", "eu"); err == nil || !strings.Contains(err.Error(), "nao tem o par") {
		t.Fatalf("sem o par do planeador tem de recusar: %v", err)
	}
	t.Setenv("AOS_MODEL_PRICING_PATH", aos544Ficheiro(t, "{nao e json"))
	if _, err := custoDoPlaneamentoFromEnv("gpt-4o", "eu"); err == nil {
		t.Fatal("tabela ilegivel tem de recusar")
	}
	t.Setenv("AOS_MODEL_PRICING_PATH", filepath.Join(t.TempDir(), "nao-existe.json"))
	if _, err := custoDoPlaneamentoFromEnv("gpt-4o", "eu"); err == nil {
		t.Fatal("ficheiro em falta tem de recusar")
	}
}

func TestAOS544_OMedidorSomaOCusto(t *testing.T) {
	ctx := context.Background()
	chamar := func(m *medidorDoPlaneamento, u port.Usage, erro error) {
		t.Helper()
		g := gatewayDecomposeModel{gw: &gatewayComUsage{fakeGateway: fakeGateway{resposta: "{}"}, usage: u, erro: erro}, medidor: m}
		_, _ = g.Complete(ctx, "s", "u")
	}
	// Armado: soma o custo derivado, medido.
	m := &medidorDoPlaneamento{}
	m.armarCusto()
	chamar(m, port.Usage{PromptTokens: 1000, CompletionTokens: 100, CostMicroUSD: 150}, nil)
	chamar(m, port.Usage{PromptTokens: 500, CompletionTokens: 10, CostMicroUSD: 55}, nil)
	if got := m.consumo(); got.CostMicroUSD != 205 || !got.CustoMedido {
		t.Fatalf("armado: %+v, quero 205 medidos", got)
	}
	// Uma chamada falhada torna o custo não medido.
	chamar(m, port.Usage{PromptTokens: 9, CompletionTokens: 1}, errors.New("upstream"))
	if got := m.consumo(); got.CustoMedido {
		t.Fatalf("com uma chamada nao medida o custo nao e medido: %+v", got)
	}
	// Desarmado: não medido, e não se soma o que o gateway não derivou.
	d := &medidorDoPlaneamento{}
	chamar(d, port.Usage{PromptTokens: 1000, CompletionTokens: 100}, nil)
	if got := d.consumo(); got.CustoMedido || got.CostMicroUSD != 0 {
		t.Fatalf("desarmado: %+v", got)
	}
	// Zero chamadas: zero medido, armado ou não.
	z := &medidorDoPlaneamento{}
	z.armarCusto()
	if got := z.consumo(); !got.CustoMedido || got.CostMicroUSD != 0 {
		t.Fatalf("zero chamadas: %+v", got)
	}
	// Um medidor nil não rebenta ao armar.
	var nulo *medidorDoPlaneamento
	nulo.armarCusto()
}
