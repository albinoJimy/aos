package modelgateway

// AOS-491 — O MOTIVO DE PARAGEM ATRAVESSA A FRONTEIRA GW→RT, NORMALIZADO.
//
// O `translateResponse` lia o `finish_reason` da primeira escolha para calcular o `Final` e
// deitava-o fora. Estes testes fixam a projecção no vocabulário fechado do runtime, nas duas
// formas do pedido (texto único e mensagens nativas), e que o `Final` ficou como estava.

import (
	"context"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

func aos491Resposta(finish string, comTool bool) port.ChatResponse {
	msg := port.Message{Role: port.RoleAssistant, Content: "texto"}
	if comTool {
		msg.ToolCalls = []port.ToolCall{{ID: "call-1", Type: "function", Function: port.FunctionCall{Name: "echo", Arguments: "{}"}}}
	}
	return port.ChatResponse{
		Model:   "modelo-de-teste",
		Choices: []port.Choice{{Message: msg, FinishReason: finish}},
		Usage:   port.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
	}
}

// O MAPA: cada `finish_reason` conhecido tem o seu valor; o vazio fica vazio; tudo o resto —
// outra caixa, espaços, o vocabulário de outro provider — vira `other`, e o texto bruto não sai.
func TestAOS491_Traducao_FinishReasonNoVocabularioFechado(t *testing.T) {
	t.Parallel()
	casos := []struct {
		finish string
		quer   agentruntime.StopReason
	}{
		{"stop", agentruntime.StopStop},
		{"tool_calls", agentruntime.StopToolCalls},
		{"function_call", agentruntime.StopToolCalls},
		{"length", agentruntime.StopLength},
		{"content_filter", agentruntime.StopContentFilter},
		{"", agentruntime.StopUnreported},
		{"end_turn", agentruntime.StopOther},
		{"max_tokens", agentruntime.StopOther},
		{"STOP", agentruntime.StopOther},
		{" stop", agentruntime.StopOther},
		{"other", agentruntime.StopOther},
		{strings.Repeat("x", 4096) + "\n# HELP", agentruntime.StopOther},
	}
	for _, c := range casos {
		for _, comTool := range []bool{false, true} {
			out, err := translateResponse(aos491Resposta(c.finish, comTool))
			if err != nil {
				t.Fatalf("finish_reason %q: translateResponse: %v", c.finish, err)
			}
			if out.StopReason != c.quer {
				t.Fatalf("finish_reason %q (tool call: %v): StopReason = %q, quero %q", c.finish, comTool, out.StopReason, c.quer)
			}
			if out.StopReason != out.StopReason.Normalizado() {
				t.Fatalf("finish_reason %q: o adaptador devolveu um valor fora do vocabulario: %q", c.finish, out.StopReason)
			}
		}
	}
}

// AS CONSTANTES DA PORTA são os textos do wire OpenAI, e é delas que o mapa sai.
func TestAOS491_ConstantesDaPorta(t *testing.T) {
	t.Parallel()
	for quer, got := range map[string]string{
		"stop": port.FinishStop, "tool_calls": port.FinishToolCalls, "function_call": port.FinishFunctionCall,
		"length": port.FinishLength, "content_filter": port.FinishContentFilter,
	} {
		if got != quer {
			t.Fatalf("constante da porta = %q, quero %q", got, quer)
		}
	}
}

// SÓ TRANSPORTE: o `Final` é calculado exactamente como antes — sem tool calls e com
// `finish_reason` `stop` ou vazio. Um `length` sem tool calls continua a NÃO ser final.
func TestAOS491_Traducao_OFinalNaoMudou(t *testing.T) {
	t.Parallel()
	for _, finish := range []string{"stop", "", "length", "content_filter", "tool_calls", "end_turn"} {
		for _, comTool := range []bool{false, true} {
			out, err := translateResponse(aos491Resposta(finish, comTool))
			if err != nil {
				t.Fatalf("translateResponse: %v", err)
			}
			quer := !comTool && (finish == "stop" || finish == "")
			if out.Final != quer {
				t.Fatalf("finish_reason %q (tool call: %v): Final = %v, quero %v — o AOS-491 nao muda o Final", finish, comTool, out.Final, quer)
			}
		}
	}
}

// aos491Gateway devolve sempre a mesma resposta.
type aos491Gateway struct{ resp port.ChatResponse }

func (g aos491Gateway) Chat(context.Context, port.ChatRequest) (port.ChatResponse, error) {
	return g.resp, nil
}
func (g aos491Gateway) ChatStream(context.Context, port.ChatRequest) (port.ChatStream, error) {
	return nil, nil
}
func (g aos491Gateway) Embeddings(context.Context, port.EmbeddingsRequest) (port.EmbeddingsResponse, error) {
	return port.EmbeddingsResponse{}, nil
}
func (g aos491Gateway) PortVersion() string { return port.Version }

// NAS DUAS PROJECÇÕES: o motivo chega pelo `Call` do adaptador, em texto único e em mensagens
// nativas — e na nativa a resposta declara mesmo a projecção, para o teste não medir o texto
// duas vezes.
func TestAOS491_Adaptador_MotivoNasDuasProjeccoes(t *testing.T) {
	t.Parallel()
	asm, err := agentruntime.NewPromptAssemblerFor(agentruntime.AssemblyVersion140, "sistema", nil)
	if err != nil {
		t.Fatalf("NewPromptAssemblerFor: %v", err)
	}
	view := asm.Assemble(1, []agentruntime.TailSegment{{Kind: agentruntime.TailObjective, Content: []byte("faz")}})
	gw := aos491Gateway{resp: aos491Resposta("length", false)}
	for _, modo := range []string{ProjectionText, ProjectionNative} {
		out, err := NewModelClient(gw, "modelo", WithProjection(modo)).Call(context.Background(), view)
		if err != nil {
			t.Fatalf("projeccao %s: Call: %v", modo, err)
		}
		if out.StopReason != agentruntime.StopLength {
			t.Fatalf("projeccao %s: StopReason = %q, quero length", modo, out.StopReason)
		}
		if nativa := out.Projection == agentruntime.ProjectionNative; nativa != (modo == ProjectionNative) {
			t.Fatalf("projeccao %s: a resposta declara Projection=%q", modo, out.Projection)
		}
	}
}

// AS TOOLS OFERECIDAS são as que o PEDIDO levou: o tool set do nó quando o run não tem
// lista-branca, só as admitidas quando tem, e zero quando a lista não admite nenhuma.
func TestAOS491_Adaptador_DeclaraAsToolsQueOPedidoLevou(t *testing.T) {
	t.Parallel()
	asm, err := agentruntime.NewPromptAssemblerFor(agentruntime.AssemblyVersion140, "sistema", nil)
	if err != nil {
		t.Fatalf("NewPromptAssemblerFor: %v", err)
	}
	view := asm.Assemble(1, []agentruntime.TailSegment{{Kind: agentruntime.TailObjective, Content: []byte("faz")}})
	tool := func(nome string) port.Tool {
		return port.Tool{Type: "function", Function: port.FunctionDef{Name: nome}}
	}
	doNo := []port.Tool{tool("a"), tool("b"), tool("c")}
	oferta := func(admitidas ...string) func(context.Context) (func(string) bool, bool) {
		return func(context.Context) (func(string) bool, bool) {
			return func(nome string) bool {
				for _, a := range admitidas {
					if a == nome {
						return true
					}
				}
				return false
			}, true
		}
	}
	gw := aos491Gateway{resp: aos491Resposta("stop", false)}
	casos := []struct {
		nome string
		opts []RuntimeAdapterOption
		quer int
	}{
		{"sem tools no no", nil, 0},
		{"tool set do no, run sem lista-branca", []RuntimeAdapterOption{WithTools(doNo)}, 3},
		{"lista-branca admite duas", []RuntimeAdapterOption{WithTools(doNo), WithToolOfferFromContext(oferta("a", "c"))}, 2},
		{"lista-branca vazia", []RuntimeAdapterOption{WithTools(doNo), WithToolOfferFromContext(oferta())}, 0},
	}
	for _, c := range casos {
		for _, modo := range []string{ProjectionText, ProjectionNative} {
			out, err := NewModelClient(gw, "modelo", append([]RuntimeAdapterOption{WithProjection(modo)}, c.opts...)...).Call(context.Background(), view)
			if err != nil {
				t.Fatalf("%s (%s): Call: %v", c.nome, modo, err)
			}
			if out.ToolsOffered != c.quer {
				t.Fatalf("%s (%s): ToolsOffered = %d, quero %d", c.nome, modo, out.ToolsOffered, c.quer)
			}
		}
	}
}

// A PRIMEIRA ESCOLHA (revisão do AOS-491, m1). Uma resposta com várias `choices` de motivos
// diferentes: o runtime recebe o texto, as tool calls e o `Final` da primeira, e o motivo tem de
// ser o DESSA — o de outra escolha descreveria um turno que o runtime não viu. Todas as outras
// fixtures têm uma só escolha, pelo que sem este teste «primeira» não estava fixado por nada.
func TestAOS491_Traducao_VariasEscolhas_OMotivoEODaPrimeira(t *testing.T) {
	t.Parallel()
	escolha := func(texto, finish string) port.Choice {
		return port.Choice{Message: port.Message{Role: port.RoleAssistant, Content: texto}, FinishReason: finish}
	}
	casos := []struct {
		finishes []string
		quer     agentruntime.StopReason
	}{
		{[]string{"length", "stop"}, agentruntime.StopLength},
		{[]string{"stop", "length"}, agentruntime.StopStop},
		{[]string{"content_filter", "stop", "length"}, agentruntime.StopContentFilter},
		{[]string{"", "length"}, agentruntime.StopUnreported},
		{[]string{"end_turn", "stop"}, agentruntime.StopOther},
	}
	for _, c := range casos {
		resp := port.ChatResponse{Model: "modelo-de-teste", Usage: port.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}}
		for i, f := range c.finishes {
			resp.Choices = append(resp.Choices, escolha("escolha-"+string(rune('0'+i)), f))
		}
		out, err := translateResponse(resp)
		if err != nil {
			t.Fatalf("finish_reasons %q: translateResponse: %v", c.finishes, err)
		}
		if out.StopReason != c.quer {
			t.Fatalf("finish_reasons %q: StopReason = %q, quero %q (o da primeira escolha)", c.finishes, out.StopReason, c.quer)
		}
		if out.Text != "escolha-0" {
			t.Fatalf("finish_reasons %q: o texto veio de outra escolha: %q", c.finishes, out.Text)
		}
		// O `Final` continua a sair da mesma escolha que o motivo.
		if quer := c.finishes[0] == "stop" || c.finishes[0] == ""; out.Final != quer {
			t.Fatalf("finish_reasons %q: Final = %v, quero %v", c.finishes, out.Final, quer)
		}
	}
}
