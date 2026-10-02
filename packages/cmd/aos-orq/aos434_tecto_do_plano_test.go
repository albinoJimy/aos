package main

// aos434_tecto_do_plano_test.go — o tecto da árvore de orçamento do plano.
//
// O ticket que abriu isto — escrito por mim — dizia que `1<<30` deixava «queimar orçamento de
// modelo sem travão node-local». **Está errado**, e a correcção está no Estado do AOS-434: esta
// árvore nunca vê consumo real. O que ela governa são ESTIMATIVAS DECLARADAS, e com `1<<30` a
// admissão de materialização era vácua — nenhum documento era recusado, por mais absurdo que
// declarasse.
//
// É isso que estes testes fixam.

import (
	"errors"
	"strings"
	"testing"
)

func TestAOS434TectoPorOmissaoNaoMuda(t *testing.T) {
	t.Setenv("AOS_ORQ_PLAN_BUDGET_MAX_TOKENS", "")
	t.Setenv("AOS_ORQ_PLAN_BUDGET_MAX_COST_MICRO_USD", "")

	tecto, err := tectoDoPlanoDoAmbiente()
	if err != nil {
		t.Fatalf("sem variaveis definidas nao pode falhar: %v", err)
	}
	if tecto.Tokens != planBudgetTokensPorOmissao || tecto.CostMicroUSD != planBudgetCustoPorOmissao {
		t.Errorf("o default mudou: %+v\n"+
			"este ticket tornou o tecto CONFIGURAVEL; nao escolheu um numero novo, porque nao ha "+
			"medicao de consumo de um plano no repositorio que o justificasse", tecto)
	}
}

func TestAOS434TectoConfiguradoEeUsado(t *testing.T) {
	t.Setenv("AOS_ORQ_PLAN_BUDGET_MAX_TOKENS", "50000")
	t.Setenv("AOS_ORQ_PLAN_BUDGET_MAX_COST_MICRO_USD", "7500")

	tecto, err := tectoDoPlanoDoAmbiente()
	if err != nil {
		t.Fatalf("tecto valido recusado: %v", err)
	}
	if tecto.Tokens != 50000 || tecto.CostMicroUSD != 7500 {
		t.Errorf("tecto = %+v, quer {50000, 7500}", tecto)
	}
}

// TestAOS434ValorInvalidoABORTA — fail-closed, no molde do `budget_env.go` do nó.
//
// O ZERO tem caso próprio porque é o que engana: não desligaria o tecto, negaria TODOS os
// planos, porque nenhuma estimativa cabe em zero.
func TestAOS434ValorInvalidoABORTA(t *testing.T) {
	for _, c := range []struct{ nome, tokens, custo string }{
		{"tokens zero", "0", ""},
		{"tokens negativo", "-1", ""},
		{"tokens ilegivel", "muitos", ""},
		{"custo zero", "", "0"},
		{"custo negativo", "", "-5"},
		{"custo ilegivel", "", "barato"},
	} {
		t.Run(c.nome, func(t *testing.T) {
			t.Setenv("AOS_ORQ_PLAN_BUDGET_MAX_TOKENS", c.tokens)
			t.Setenv("AOS_ORQ_PLAN_BUDGET_MAX_COST_MICRO_USD", c.custo)

			_, err := tectoDoPlanoDoAmbiente()
			if !errors.Is(err, ErrTectoDePlanoInvalido) {
				t.Fatalf("%s tinha de abortar com ErrTectoDePlanoInvalido, veio %v\n"+
					"degradar em silencio deixaria o operador convencido de que ha tecto", c.nome, err)
			}
		})
	}
}

// TestAOS434OBannerDizOQueOTectoNAOProtege é o teste que mais importa deste ficheiro.
//
// Um banner que dissesse só «tecto: N tokens» convidaria à conclusão errada — que o plano está
// protegido do custo do modelo. Não está, e foi essa confusão que fez o ticket original afirmar
// o que não era verdade. Se a linha deixar de o dizer, o próximo a ler fica enganado da mesma
// maneira.
func TestAOS434OBannerDizOQueOTectoNAOProtege(t *testing.T) {
	t.Setenv("AOS_ORQ_PLAN_BUDGET_MAX_TOKENS", "")
	t.Setenv("AOS_ORQ_PLAN_BUDGET_MAX_COST_MICRO_USD", "")
	tecto, err := tectoDoPlanoDoAmbiente()
	if err != nil {
		t.Fatalf("tecto: %v", err)
	}

	linha := bannerDoOrcamentoDoPlano(tecto)

	for _, exigido := range []struct{ frag, porque string }{
		{"NAO** COBRE", "sem isto, o banner parece prometer proteccao de custo real"},
		{"AOS_BUDGET_MAX_TOKENS do NO", "o operador tem de saber ONDE esta o travao real"},
		{"ESTIMATIVAS DECLARADAS", "e isso, e so isso, que esta arvore mede"},
		{"POR OMISSAO", "um tecto por omissao tem de se identificar como tal"},
		{"AOS_ORQ_PLAN_BUDGET_MAX_TOKENS", "quem le tem de saber como o mudar"},
	} {
		if !strings.Contains(linha, exigido.frag) {
			t.Errorf("o banner perdeu %q — %s\n\nbanner:\n%s", exigido.frag, exigido.porque, linha)
		}
	}
}

// TestAOS434OBannerDeclaraOValorEMVIGOR — e não o default, quando são diferentes.
func TestAOS434OBannerDeclaraOValorEMVIGOR(t *testing.T) {
	t.Setenv("AOS_ORQ_PLAN_BUDGET_MAX_TOKENS", "123456")
	t.Setenv("AOS_ORQ_PLAN_BUDGET_MAX_COST_MICRO_USD", "654321")
	tecto, err := tectoDoPlanoDoAmbiente()
	if err != nil {
		t.Fatalf("tecto: %v", err)
	}

	linha := bannerDoOrcamentoDoPlano(tecto)
	if !strings.Contains(linha, "123456") || !strings.Contains(linha, "654321") {
		t.Errorf("o banner nao declara o valor em vigor:\n%s", linha)
	}
	if strings.Contains(linha, "POR OMISSAO") {
		t.Error("o banner diz POR OMISSAO com as duas variaveis DEFINIDAS — a linha passou a " +
			"descrever a intencao em vez do estado, que e o defeito que a convencao deste " +
			"repositorio existe para nao ter")
	}
}
