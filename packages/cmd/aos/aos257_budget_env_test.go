package main

// AOS-257 — a SUPERFÍCIE DE AMBIENTE do orçamento por-run, e a amarra entre o que o nó
// compõe e o que o banner declara.
//
// Três eixos, todos gates:
//
//  1. o default é a AUSÊNCIA de orçamento (variável por definir) — e é essa a postura que o
//     banner tem de declarar;
//  2. um valor presente mas inválido ABORTA o arranque; em particular o `0`, que parece
//     "desligado" e é "tudo negado" (nenhuma estimativa cabe em zero);
//  3. POSTURA ANUNCIADA = POSTURA LIGADA: o argumento do banner deriva do orçamento
//     REALMENTE composto, não de um literal. É o complemento simétrico do guard de AOS-255.

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestAOS257BudgetEnvAusenteNaoCompoeNada prova o default DECLARADO: sem
// AOS_BUDGET_MAX_TOKENS não se inventa tecto nenhum — o nó corre sem orçamento e o banner
// di-lo. (Um default numérico inventado aqui seria o mesmo defeito das velocidades de queima
// do disjuntor: um limiar que o nó não sabe justificar.)
func TestAOS257BudgetEnvAusenteNaoCompoeNada(t *testing.T) {
	t.Setenv("AOS_BUDGET_MAX_TOKENS", "")

	rb, err := budgetFromEnv()
	if err != nil {
		t.Fatalf("budgetFromEnv sem a variavel definida devia ser um no-op; err=%v", err)
	}
	if rb != nil {
		t.Fatal("sem AOS_BUDGET_MAX_TOKENS nao se compoe orcamento nenhum")
	}

	linha := strings.Join(budgetPostureBanner(rb != nil), "\n")
	if !strings.Contains(linha, "NAO COMPOSTO") {
		t.Errorf("o banner tem de declarar NAO COMPOSTO neste estado:\n%s", linha)
	}
	if !strings.Contains(linha, "AOS_BUDGET_MAX_TOKENS") {
		t.Errorf("o banner devia nomear a variavel que LIGA o orcamento — sem ela o operador nao sabe o que fazer com a informacao:\n%s", linha)
	}
}

// TestAOS257BudgetEnvValidaCompoeOTecto prova o outro estado: com um tecto configurado, o
// orçamento é composto com EXACTAMENTE esse valor, e o banner passa a COMPOSTO.
func TestAOS257BudgetEnvValidaCompoeOTecto(t *testing.T) {
	t.Setenv("AOS_BUDGET_MAX_TOKENS", " 50000 ") // espaços em volta são tolerados (TrimSpace)

	rb, err := budgetFromEnv()
	if err != nil {
		t.Fatalf("budgetFromEnv: %v", err)
	}
	if rb == nil {
		t.Fatal("com AOS_BUDGET_MAX_TOKENS definida o orcamento TEM de ser composto")
	}
	if got := rb.MaxTokensPerRun(); got != 50000 {
		t.Errorf("tecto por-run = %d, esperado 50000 (o valor da config nao pode ser reinterpretado)", got)
	}

	linha := strings.Join(budgetPostureBanner(rb != nil), "\n")
	if strings.Contains(linha, "NAO COMPOSTO") {
		t.Errorf("com orcamento composto o banner NAO pode declarar-se nao-composto:\n%s", linha)
	}
	for _, marcador := range []string{"COMPOSTO", "TOOL-ONLY", "TOKEN-ONLY", "POR-RUN", "AOS_BUDGET_MAX_TOKENS"} {
		if !strings.Contains(linha, marcador) {
			t.Errorf("o banner do estado composto devia conter %q:\n%s", marcador, linha)
		}
	}
}

// TestAOS257BudgetEnvInvalidaAborta é o fail-closed de CONFIG, no molde de
// [ErrBadBreakerThresholds]/[ErrBadRetention]: nunca um fallback silencioso.
func TestAOS257BudgetEnvInvalidaAborta(t *testing.T) {
	casos := map[string]string{
		"nao-inteiro":     "muitos",
		"decimal":         "1.5",
		"negativo":        "-1",
		"zero":            "0", // NÃO é "desligado": negaria 100% das tool calls
		"sufixo":          "50000tokens",
		"vazio-com-sinal": "+",
	}
	for nome, valor := range casos {
		t.Run(nome, func(t *testing.T) {
			t.Setenv("AOS_BUDGET_MAX_TOKENS", valor)
			rb, err := budgetFromEnv()
			if !errors.Is(err, ErrBadBudget) {
				t.Fatalf("AOS_BUDGET_MAX_TOKENS=%q devia ABORTAR com ErrBadBudget; err=%v", valor, err)
			}
			if rb != nil {
				t.Error("um valor invalido nao pode devolver um orcamento parcialmente composto")
			}
		})
	}
}

// TestAOS257BannerDerivaDoOrcamentoComposto sela a amarra POSTURA ANUNCIADA = POSTURA
// LIGADA no composition-root: o argumento de [budgetPostureBanner] tem de ser o MESMO valor
// entregue a [integration.SecuredConfig.Budget], não um literal nem uma leitura repetida do
// ambiente (que poderia divergir do que foi realmente composto).
//
// É o par simétrico de [TestAOS255CallSiteMatchesComposition]: aquele impede que o banner
// NEGUE um orçamento composto; este impede que o argumento volte a ser uma constante.
func TestAOS257BannerDerivaDoOrcamentoComposto(t *testing.T) {
	t.Parallel()

	fonte, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatalf("ler bootstrap.go: %v", err)
	}
	src := string(fonte)

	if !strings.Contains(src, "budgetPostureBanner(runBudget != nil)") {
		t.Error("o composition-root tem de chamar budgetPostureBanner(runBudget != nil) — o argumento e o ESTADO composto, nunca um literal nem uma segunda leitura do ambiente")
	}
	// Sem espaços: o alinhamento do gofmt no literal da struct não pode partir o gate.
	if !strings.Contains(strings.ReplaceAll(src, " ", ""), "Budget:runBudget,") {
		t.Error("o MESMO runBudget tem de ser entregue a integration.SecuredConfig.Budget — se o banner e a cadeia lessem fontes diferentes, a linha podia anunciar o que a cadeia nao compos")
	}
	if strings.Contains(src, "budgetPostureBanner(false)") || strings.Contains(src, "budgetPostureBanner(true)") {
		t.Error("budgetPostureBanner voltou a ser chamado com um literal no composition-root")
	}
}
