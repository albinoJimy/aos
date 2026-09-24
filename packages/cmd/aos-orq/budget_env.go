package main

// budget_env.go — O TECTO DA ÁRVORE DE ORÇAMENTO DO PLANO (AOS-434).
//
// # O QUE ESTE TECTO GOVERNA — E É MENOS DO QUE O NOME SUGERE
//
// Era `1 << 30` em tokens e em micro-USD, fixo no binário, com o comentário «aqui são generosos
// e declarados, para que a admissão exercite o caminho de RESERVA sem ser o que decide o
// desfecho da demonstração». Era honesto para uma demonstração, e deixa de o ser quando algo
// passar a drenar a fila de pedidos.
//
// Mas o que ele governa tem de ser dito com precisão, porque o ticket que abriu isto — escrito
// por mim — dizia-o **errado**.
//
// A árvore de orçamento do `aos-orq` debita:
//
//   - a ESTIMATIVA do planeador (`perAttempt × maxAttempts`), reservada e confirmada tal-qual;
//   - o `BudgetEstimate` DECLARADO POR CADA NÓ do documento de plano;
//   - `{1,1}` por avaliação de aresta condicional;
//   - a reserva de spawn de um papel que expande.
//
// **Nada disto é consumo real.** O planeador reserva a estimativa e nunca a reconcilia com o
// `usage` da resposta; o trabalho dos nós corre como runs do nó `aos` e debita o orçamento DELE.
// Um tecto aqui limita a soma de números que o próprio documento declara — e nada mais.
//
// # ENTÃO PARA QUE SERVE
//
// Para o que a admissão de materialização existe para fazer: recusar um plano cujas estimativas
// declaradas são implausíveis. Com `1 << 30` essa admissão é **vácua** — nenhum documento é
// recusado, por mais absurdo que declare. É uma guarda que corre e nunca nega, que é o modo de
// falha que este repositório passou a série a fechar noutros sítios.
//
// # O TRAVÃO DE CUSTO REAL É OUTRO, E ESTÁ POR LIGAR
//
// É o `AOS_BUDGET_MAX_TOKENS` do **nó**, que reserva antes do turno e salda pelo consumo MEDIDO.
// Em produção está **por definir** — o que é uma escolha legítima e explícita («quem quer o nó
// sem orçamento deixa a variável por definir»), e o nó declara-a no arranque com todas as
// letras.
//
// Isto está escrito aqui, e sai no banner, porque a confusão entre os dois é fácil de fazer e
// cara: um operador que configure este tecto e conclua que está protegido do custo do modelo
// está enganado, e nada no sistema lho diria.
//
// # PORQUE É QUE O DEFAULT NÃO DESCE
//
// Não há medição de consumo de um plano no repositório — nem relatório, nem teste, nem número.
// O único consumo medido é de UM run do nó (1 749 tokens contra tecto de 200 000), de uma fonte
// que esta árvore nunca lê.
//
// Escolher um número sem medição seria inventar um tecto e chamar-lhe protecção. O default
// mantém-se, a variável existe para quem tenha o número, e o banner diz qual está em vigor.

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/aos-ref/control-plane/budget"
)

// Tectos por omissão da árvore de orçamento do plano.
//
// Mantêm o valor que estava fixo no binário: a mudança deste ticket é torná-los CONFIGURÁVEIS e
// DECLARADOS, não escolher um número novo sem medição para o justificar.
const (
	planBudgetTokensPorOmissao = int64(1) << 30
	planBudgetCustoPorOmissao  = int64(1) << 30
)

// ErrTectoDePlanoInvalido — o tecto está definido e é inválido.
//
// Fail-closed no arranque, no molde do `budget_env.go` do nó: um valor ilegível ou <= 0 aborta,
// em vez de degradar em silêncio. O zero merece a nota — não desligaria o tecto, negaria TODOS
// os planos, porque nenhuma estimativa cabe em zero.
var ErrTectoDePlanoInvalido = errors.New(
	"aos-orq: tecto de orcamento do plano mal configurado — AOS_ORQ_PLAN_BUDGET_MAX_TOKENS e " +
		"AOS_ORQ_PLAN_BUDGET_MAX_COST_MICRO_USD tem de ser inteiros > 0. Deixe POR DEFINIR para " +
		"usar o default; 0 nao desliga o tecto, negaria todos os planos")

// tectoDoPlanoDoAmbiente resolve o tecto da raiz da árvore. Nunca devolve zero.
//
// # OS `os.Getenv` SÃO LITERAIS, E ISSO É EXIGIDO POR UM GATE
//
// A primeira versão tinha um helper `inteiroPositivoDoAmbiente(nome string, …)` que fazia
// `os.Getenv(nome)`. O CI recusou-a, e com razão: o `env_surface_test.go` do nó varre a árvore
// — incluindo este módulo — para construir a superfície de variáveis DOCUMENTADA, e um nome
// não-literal cega-o. Uma variável que existe e que nenhuma documentação consegue enumerar é
// exactamente a classe de defeito que este repositório persegue.
//
// O gate oferecia duas saídas: literal, ou declarar a função em `envNameWrappers`. A segunda é
// para quando a leitura REAL está no chamador do wrapper, o que aqui não acontece — usá-la seria
// contornar o gate em vez de o satisfazer.
//
// Lição de processo, registada: o gate vive em `packages/cmd/aos` e varre `../aos-orq/`. Correr
// a suite do módulo que se tocou não chega quando o gate que o julga vive noutro.
func tectoDoPlanoDoAmbiente() (budget.Amount, error) {
	tokens, err := inteiroPositivo(
		"AOS_ORQ_PLAN_BUDGET_MAX_TOKENS",
		strings.TrimSpace(os.Getenv("AOS_ORQ_PLAN_BUDGET_MAX_TOKENS")),
		planBudgetTokensPorOmissao,
	)
	if err != nil {
		return budget.Amount{}, err
	}
	custo, err := inteiroPositivo(
		"AOS_ORQ_PLAN_BUDGET_MAX_COST_MICRO_USD",
		strings.TrimSpace(os.Getenv("AOS_ORQ_PLAN_BUDGET_MAX_COST_MICRO_USD")),
		planBudgetCustoPorOmissao,
	)
	if err != nil {
		return budget.Amount{}, err
	}
	return budget.Amount{Tokens: tokens, CostMicroUSD: custo}, nil
}

// inteiroPositivo valida um valor JÁ LIDO. Recebe o nome só para a mensagem de erro — nunca
// para ler o ambiente, que é o que mantém os `os.Getenv` acima literais e enumeráveis.
func inteiroPositivo(nome, bruto string, omissao int64) (int64, error) {
	if bruto == "" {
		return omissao, nil
	}
	v, err := strconv.ParseInt(bruto, 10, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%w: %s=%q", ErrTectoDePlanoInvalido, nome, bruto)
	}
	return v, nil
}

// bannerDoOrcamentoDoPlano declara o tecto EM VIGOR e, sobretudo, o que ele NÃO cobre.
//
// A segunda metade é a que importa. Um banner que dissesse só «tecto: N tokens» convidaria a
// conclusão errada — que o plano está protegido do custo do modelo. Não está: o que este tecto
// mede são estimativas que o próprio documento declara.
func bannerDoOrcamentoDoPlano(tecto budget.Amount) string {
	omissao := ""
	if tecto.Tokens == planBudgetTokensPorOmissao && tecto.CostMicroUSD == planBudgetCustoPorOmissao {
		omissao = " (POR OMISSAO — nenhuma das duas variaveis esta definida)"
	}
	return fmt.Sprintf(
		"orcamento do plano (AOS-434): raiz da arvore com tecto de %d tokens / %d micro-USD%s. "+
			"O QUE ISTO COBRE: a soma das ESTIMATIVAS DECLARADAS — a do planeador e o "+
			"`budget_estimate` de cada no do documento. Um plano cuja soma nao caiba e RECUSADO na "+
			"materializacao (nao adiado: nao ha adiamento nesta arvore). "+
			"O QUE ISTO **NAO** COBRE: o consumo REAL de tokens. O planeador reserva a estimativa e "+
			"nunca a reconcilia com o usage da resposta, e o trabalho dos nos corre como runs do no "+
			"`aos` e debita o orcamento DELE. O travao de custo real e o AOS_BUDGET_MAX_TOKENS do NO, "+
			"que reserva antes do turno e salda pelo consumo MEDIDO — e que e outra configuracao, "+
			"noutro processo. Ajustar este tecto nao protege do custo do modelo. "+
			"Configuravel por AOS_ORQ_PLAN_BUDGET_MAX_TOKENS / AOS_ORQ_PLAN_BUDGET_MAX_COST_MICRO_USD.",
		tecto.Tokens, tecto.CostMicroUSD, omissao)
}
