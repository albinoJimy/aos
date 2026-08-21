package modelgateway

import (
	"errors"
	"testing"

	"github.com/aos-ref/platform/model-gateway/port"
)

// ---------------------------------------------------------------------------------------------
// UM 200 SEM ESCOLHAS NÃO É UMA CONCLUSÃO.
//
// Antes desta correcção, `len(Choices) == 0` produzia `Final=true` sem erro, e a cadeia a jusante
// selava o run como CONCLUÍDO COM SUCESSO — incluindo quando o corpo era um payload de ERRO do
// provider devolvido com 200, que é o que proxies compatíveis com OpenAI fazem.
//
// Sem compensação (a saga só dispara a partir de `failed`) e sem sinal para o operador: no log
// durável a avaria era indistinguível de um run que respondeu e concluiu.
// ---------------------------------------------------------------------------------------------

func TestRespostaSemEscolhasEErro(t *testing.T) {
	casos := map[string]port.ChatResponse{
		"choices ausente": {Model: "m"},
		"choices vazio":   {Model: "m", Choices: []port.Choice{}},
	}
	for nome, resp := range casos {
		t.Run(nome, func(t *testing.T) {
			out, err := translateResponse(resp)
			if !errors.Is(err, ErrRespostaSemChoices) {
				t.Fatalf("erro = %v, queria ErrRespostaSemChoices — sem isto o run sela COMPLETE "+
					"sobre uma avaria do gateway", err)
			}
			if out.Final {
				t.Error("devolveu Final=true a par do erro — quem ignorar o erro conclui o run")
			}
		})
	}
}

// TestUmaRespostaNORMALContinuaAConcluir é o CONTROLO, e sem ele a correcção seria indistinguível
// de «recusa tudo» — que partiria todos os runs em vez de os selar mal.
func TestUmaRespostaNormalContinuaAConcluir(t *testing.T) {
	out, err := translateResponse(port.ChatResponse{
		Model: "m",
		Choices: []port.Choice{{
			Message:      port.Message{Role: port.RoleAssistant, Content: "resposta"},
			FinishReason: "stop",
		}},
	})
	if err != nil {
		t.Fatalf("uma resposta bem-formada devia passar: %v", err)
	}
	if !out.Final || out.Text != "resposta" {
		t.Errorf("Final=%v Text=%q — a conclusao normal partiu-se", out.Final, out.Text)
	}
}

// TestUmaCHAMADADETOOLContinuaANaoSerFinal — o outro ramo do caminho normal.
func TestUmaChamadaDeToolContinuaANaoSerFinal(t *testing.T) {
	out, err := translateResponse(port.ChatResponse{
		Model: "m",
		Choices: []port.Choice{{
			Message: port.Message{Role: port.RoleAssistant, ToolCalls: []port.ToolCall{{
				Function: port.FunctionCall{Name: "doc_read", Arguments: `{"doc_id":"x"}`},
			}}},
			FinishReason: "tool_calls",
		}},
	})
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if out.Final {
		t.Error("uma chamada de tool nao e final")
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].ToolID != "doc_read" {
		t.Errorf("tool calls = %+v", out.ToolCalls)
	}
}

// TestFinishReasonVazioCONTINUAAConcluir fixa o LIMITE DECLARADO desta correcção.
//
// Um `finish_reason` vazio COM uma escolha presente continua a contar como final — há conteúdo, e
// mudá-lo partiria providers que o omitem legitimamente. Este teste existe para que a decisão
// seja deliberada e não arrastada: quem a quiser mudar, muda-a aqui e vê o que cai.
func TestFinishReasonVazioContinuaAConcluir(t *testing.T) {
	out, err := translateResponse(port.ChatResponse{
		Model:   "m",
		Choices: []port.Choice{{Message: port.Message{Content: "texto"}, FinishReason: ""}},
	})
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !out.Final {
		t.Error("finish_reason vazio com conteudo deixou de ser final — o limite declarado mudou " +
			"sem ninguem o decidir")
	}
}
