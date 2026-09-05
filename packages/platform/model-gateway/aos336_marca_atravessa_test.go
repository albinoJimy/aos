package modelgateway

// ---------------------------------------------------------------------------------------------
// AOS-336 — A MARCA DE «NÃO MEDIDO» ATRAVESSA A FRONTEIRA GW→RT.
//
// O AOS-321 fechou o zero silencioso DENTRO do gateway e nomeou o que ficava por fechar: o
// `translateResponse` copiava três campos numéricos para `agentruntime.ModelResponse`, que não
// tinha campo para «indefinido». Num deployment cuja tabela de preços não cubra o par configurado
// o recorder de custo é nil, `recordCost` retorna cedo, a resposta é servida — e o `turn.recorded`
// recebia `input_tokens: 0, cost_micro_usd: 0` para uma chamada NÃO MEDIDA. O mesmo defeito, um
// passo a jusante.
//
// Estes testes fixam a projecção. O consumo da marca — o guarda do burn-down do nó — é provado do
// outro lado, em `cmd/aos/aos336_burndown_run_misto_test.go`.
// ---------------------------------------------------------------------------------------------

import (
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

func respostaComUsage(u port.Usage) port.ChatResponse {
	return port.ChatResponse{
		Model:   "modelo-de-teste",
		Choices: []port.Choice{{Message: port.Message{Role: "assistant", Content: "ok"}, FinishReason: "stop"}},
		Usage:   u,
	}
}

func TestAOS336_UsageNaoMedidoAtravessaMarcado(t *testing.T) {
	t.Parallel()
	casos := []struct {
		nome  string
		usage port.Usage
	}{
		{
			// O `usage` em falta no wire: `UnmarshalChatResponse` marca-o desde AOS-321.
			nome:  "objecto usage ausente",
			usage: port.Usage{Ausente: true},
		},
		{
			// A AUSÊNCIA DISFARÇADA, que escapou à primeira versão de AOS-321: o objecto
			// veio, e não traz nenhum contador que o cálculo saiba ler. É o que os proxies
			// OpenAI-compatible produzem. Se a projecção olhasse para `.Ausente` em vez de
			// `!Definido()`, esta forma atravessava disfarçada de medição.
			nome:  "usage presente so com total_tokens",
			usage: port.Usage{TotalTokens: 1500},
		},
		{
			nome:  "usage vazio",
			usage: port.Usage{},
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			out, err := translateResponse(respostaComUsage(c.usage))
			if err != nil {
				t.Fatalf("translateResponse: %v", err)
			}
			if !out.Usage.Ausente {
				t.Errorf("a marca NAO atravessou: %+v — o turn.recorded vai gravar zeros para uma chamada nao medida", out.Usage)
			}
			if out.Usage.Definido() {
				t.Errorf("Definido() = true para um usage nao medido: %+v", out.Usage)
			}
		})
	}
}

// TestAOS336_UsageMedidoContinuaDefinido é o CONTROLO. Sem ele, uma projecção que marcasse
// SEMPRE `Ausente` passaria o teste acima — e marcar tudo como não medido abortaria todos os
// runs saudáveis do nó, que é um defeito pior do que o que se fecha.
func TestAOS336_UsageMedidoContinuaDefinido(t *testing.T) {
	t.Parallel()
	out, err := translateResponse(respostaComUsage(port.Usage{
		PromptTokens:     400,
		CompletionTokens: 100,
		TotalTokens:      500,
		CostMicroUSD:     1_250_000,
	}))
	if err != nil {
		t.Fatalf("translateResponse: %v", err)
	}
	if out.Usage.Ausente {
		t.Fatal("um usage MEDIDO foi marcado como ausente — o no abortaria todos os runs saudaveis")
	}
	if !out.Usage.Definido() {
		t.Fatal("Definido() = false para um usage medido")
	}
	// A projecção dos números não pode ter-se partido ao acrescentar a marca.
	if out.Usage.InputTokens != 400 || out.Usage.OutputTokens != 100 || out.CostMicroUSD != 1_250_000 {
		t.Fatalf("a projeccao numerica mudou: %+v cost=%d", out.Usage, out.CostMicroUSD)
	}
}

// TestAOS336_ZeroMedidoNaoEZeroIndefinido fixa a distinção que dá nome ao ticket, no ponto
// exacto em que ela tem de existir: os dois casos produzem `CostMicroUSD == 0` e são coisas
// diferentes. Um é um facto contabilizado; o outro é ausência de leitura.
func TestAOS336_ZeroMedidoNaoEZeroIndefinido(t *testing.T) {
	t.Parallel()
	medido, err := translateResponse(respostaComUsage(port.Usage{PromptTokens: 12, CompletionTokens: 0, CostMicroUSD: 0}))
	if err != nil {
		t.Fatalf("translateResponse (medido): %v", err)
	}
	indefinido, err := translateResponse(respostaComUsage(port.Usage{Ausente: true}))
	if err != nil {
		t.Fatalf("translateResponse (indefinido): %v", err)
	}
	if medido.CostMicroUSD != indefinido.CostMicroUSD {
		t.Fatalf("premissa do teste partida: os dois casos deviam ter o MESMO custo (0), got %d e %d", medido.CostMicroUSD, indefinido.CostMicroUSD)
	}
	if medido.Usage.Definido() == indefinido.Usage.Definido() {
		t.Fatal("«custo zero medido» e «custo indefinido» sao indistinguiveis do lado do runtime — e o defeito que este ticket fecha")
	}
	var _ agentruntime.ModelResponse = medido
}
