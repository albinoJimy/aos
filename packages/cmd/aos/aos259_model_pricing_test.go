package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AOS-259 — A FONTE DO NÚMERO DO CUSTO, DO LADO DO NÓ.
//
// O canal de custo só transporta um número se houver PREÇO para o par (modelo, região)
// que o nó vai usar. Estes testes fixam as três posturas possíveis e, sobretudo, que
// nenhuma delas é silenciosa: armado, não-armado (zero declarado) e config inválida
// (fail-closed no arranque).

// tabelaDePrecosDeTeste escreve um documento de preços válido para (modelo, região) e
// devolve o caminho. É a tabela do OPERADOR — o formato que AOS_MODEL_PRICING_PATH aceita.
func tabelaDePrecosDeTeste(t *testing.T, model, region string) string {
	t.Helper()
	doc := `{"version":"teste-2026.08","entries":[{"model":"` + model + `","region":"` + region + `",` +
		`"rate":{"input_per_mtok_micro_usd":2000000,"output_per_mtok_micro_usd":8000000,` +
		`"cache_read_per_mtok_micro_usd":200000,"cache_write_per_mtok_micro_usd":2500000}}]}`
	path := filepath.Join(t.TempDir(), "pricing.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("escrever tabela de teste: %v", err)
	}
	return path
}

// TestAOS259_TabelaDoOperador_ArmaAContabilidade: com uma tabela que COBRE o par do nó, o
// recorder é composto e o banner declara ARMADO.
func TestAOS259_TabelaDoOperador_ArmaAContabilidade(t *testing.T) {
	t.Setenv("AOS_MODEL_PRICING_PATH", tabelaDePrecosDeTeste(t, "modelo-do-operador", "regiao-do-operador"))

	rec, posture, err := parseModelPricingFromEnv("modelo-do-operador", "regiao-do-operador")
	if err != nil {
		t.Fatalf("tabela valida e par coberto: %v", err)
	}
	if rec == nil {
		t.Fatal("par coberto TEM de compor a contabilidade — sem recorder o canal transporta zero")
	}
	if !posture.Armed || !posture.External {
		t.Fatalf("postura = %+v, quer Armed+External", posture)
	}
	if !strings.HasPrefix(posture.TableVersion, "teste-2026.08#") {
		t.Errorf("versao tamper-evident da tabela = %q, quer o prefixo da versao do documento com digest", posture.TableVersion)
	}
	linhas := modelPricingPostureBanner(true, posture)
	if len(linhas) != 1 || !strings.Contains(linhas[0], "ARMADO") {
		t.Fatalf("banner nao declara o estado armado: %v", linhas)
	}
}

// TestAOS259_ParSemPreco_NaoArmaENaoInventa é a decisão central do ticket: sem preço para
// o par do nó NÃO se compõe contabilidade (compô-la faria o fail-closed por chamada
// recusar TODAS as chamadas ao modelo) e NÃO se inventa um preço. O canal transporta zero
// e o banner di-lo.
func TestAOS259_ParSemPreco_NaoArmaENaoInventa(t *testing.T) {
	t.Setenv("AOS_MODEL_PRICING_PATH", "") // tabela EMBEBIDA (pares de referência)

	// Um modelo que a tabela embebida não conhece — o caso NORMAL de um nó real.
	rec, posture, err := parseModelPricingFromEnv("modelo-que-nao-existe-na-tabela", "eu")
	if err != nil {
		t.Fatalf("ausencia de preco NAO e erro de config: %v", err)
	}
	if rec != nil {
		t.Fatal("sem preco para o par o recorder NAO pode ser composto — cada chamada ao modelo seria recusada fail-closed")
	}
	if posture.Armed {
		t.Fatal("postura declarada como armada sem preco na tabela")
	}
	linhas := modelPricingPostureBanner(true, posture)
	if len(linhas) != 1 {
		t.Fatalf("esperava 1 linha de banner, veio %v", linhas)
	}
	if !strings.Contains(linhas[0], "FONTE AUSENTE") || !strings.Contains(linhas[0], "AOS_MODEL_PRICING_PATH") {
		t.Errorf("o banner tem de declarar a ausencia de fonte E como a resolver: %q", linhas[0])
	}
	if !strings.Contains(linhas[0], "AUSENCIA DE DADOS") {
		t.Errorf("o banner tem de dizer que o zero NAO e custo nulo: %q", linhas[0])
	}
}

// TestAOS259_TabelaInvalida_FailClosed: quem APONTA uma tabela obtém-na carregada ou o nó
// recusa arrancar. Degradar em silêncio para a embebida facturaria com preços que o
// operador julga ter substituído.
func TestAOS259_TabelaInvalida_FailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrompida.json")
	if err := os.WriteFile(path, []byte(`{"version":"x","entries":[{"model":"m","region":"r","rate":{"input_per_mtok_micro_usd":-1}}]}`), 0o600); err != nil {
		t.Fatalf("escrever: %v", err)
	}
	t.Setenv("AOS_MODEL_PRICING_PATH", path)

	if _, _, err := parseModelPricingFromEnv("m", "r"); !errors.Is(err, ErrBadModelPricing) {
		t.Fatalf("rate negativo devia dar ErrBadModelPricing, veio %v", err)
	}

	t.Setenv("AOS_MODEL_PRICING_PATH", filepath.Join(t.TempDir(), "nao-existe.json"))
	if _, _, err := parseModelPricingFromEnv("m", "r"); !errors.Is(err, ErrBadModelPricing) {
		t.Fatalf("caminho inexistente devia dar ErrBadModelPricing, veio %v", err)
	}
}

// TestAOS259_BannerSegueOEstadoComposto: sem gateway composto não há linha nenhuma (a
// disciplina de banner de AOS-203 — declara-se o que existe, não a intenção da config).
func TestAOS259_BannerSegueOEstadoComposto(t *testing.T) {
	t.Setenv("AOS_MODEL_PRICING_PATH", "")
	if linhas := modelPricingPostureBanner(false, modelPricingPostureFromEnv()); len(linhas) != 0 {
		t.Fatalf("sem gateway composto nao ha custo de modelo a declarar, veio %v", linhas)
	}
}

// TestAOS259_PosturaDoBannerCasaComACompostaGuarda a coerência entre o que fica ligado e o
// que é declarado: as duas leituras partem do MESMO ambiente e do MESMO par, e um banner
// que divergisse do estado composto seria pior do que não haver banner.
func TestAOS259_PosturaDoBannerCasaComAComposta(t *testing.T) {
	path := tabelaDePrecosDeTeste(t, "modelo-x", "regiao-x")
	t.Setenv("AOS_MODEL_PRICING_PATH", path)
	t.Setenv("AOS_MODEL_NAME", "modelo-x")
	t.Setenv("AOS_MODEL_REGION", "regiao-x")

	rec, composta, err := parseModelPricingFromEnv("modelo-x", "regiao-x")
	if err != nil {
		t.Fatalf("compor: %v", err)
	}
	declarada := modelPricingPostureFromEnv()
	if composta != declarada {
		t.Fatalf("o banner declara %+v mas o que ficou composto e %+v", declarada, composta)
	}
	if (rec != nil) != declarada.Armed {
		t.Fatalf("o banner declara Armed=%v mas o recorder composto e nil=%v", declarada.Armed, rec == nil)
	}
}
