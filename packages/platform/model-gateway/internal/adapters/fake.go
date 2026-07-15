package adapters

import (
	"context"

	"github.com/aos-ref/platform/model-gateway/port"
)

// FakeAdapter é um adaptador in-memory DETERMINISTA (AOS-055): não faz I/O, não
// tem relógio nem aleatoriedade. É configurado com respostas roteadas por modelo
// e serve tanto o caminho síncrono como o streaming e o tool calling. É o
// adaptador usado nos testes de integração Agent Runtime → GW → fake.
type FakeAdapter struct {
	provider string
	// chat mapeia modelo → resposta de chat a devolver.
	chat map[string]port.ChatResponse
	// embed mapeia modelo → resposta de embeddings a devolver.
	embed map[string]port.EmbeddingsResponse
	// lastCred regista a última credencial recebida (asserção de injecção
	// server-side em testes, sem expor o segredo).
	lastCred Credential
	// calls conta as invocações (determinismo/asserção).
	calls int
}

// NewFakeAdapter constrói um adaptador fake para o provedor dado.
func NewFakeAdapter(provider string) *FakeAdapter {
	return &FakeAdapter{
		provider: provider,
		chat:     map[string]port.ChatResponse{},
		embed:    map[string]port.EmbeddingsResponse{},
	}
}

// Provider implementa [Adapter].
func (f *FakeAdapter) Provider() string { return f.provider }

// SetChatResponse programa a resposta de chat para um modelo.
func (f *FakeAdapter) SetChatResponse(model string, resp port.ChatResponse) {
	f.chat[model] = resp
}

// SetEmbeddingsResponse programa a resposta de embeddings para um modelo.
func (f *FakeAdapter) SetEmbeddingsResponse(model string, resp port.EmbeddingsResponse) {
	f.embed[model] = resp
}

// Calls devolve o número de invocações feitas ao adaptador.
func (f *FakeAdapter) Calls() int { return f.calls }

// LastCredentialProvider devolve o provider da última credencial injectada
// (asserção de que a credencial chegou server-side, sem revelar o segredo).
func (f *FakeAdapter) LastCredentialProvider() string { return f.lastCred.Provider }

// Chat implementa [Adapter]: devolve a resposta programada para o modelo
// EFECTIVO do pedido, com Model preenchido de forma determinista.
func (f *FakeAdapter) Chat(_ context.Context, req port.ChatRequest, cred Credential) (port.ChatResponse, error) {
	f.calls++
	f.lastCred = cred
	resp, ok := f.chat[req.Model]
	if !ok {
		// Default determinista: eco mínimo, sem aleatoriedade.
		resp = port.ChatResponse{
			Object:  "chat.completion",
			Choices: []port.Choice{{Index: 0, Message: port.Message{Role: port.RoleAssistant, Content: ""}, FinishReason: "stop"}},
		}
	}
	resp.Model = req.Model
	return resp, nil
}

// ChatStream implementa [Adapter]: fragmenta a resposta programada em deltas
// deterministas (um delta por tool call e um delta de texto), servindo o teste
// de correcção de streaming e tool calling.
func (f *FakeAdapter) ChatStream(_ context.Context, req port.ChatRequest, cred Credential) (port.ChatStream, error) {
	f.calls++
	f.lastCred = cred
	resp, ok := f.chat[req.Model]
	if !ok {
		resp = port.ChatResponse{Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant}, FinishReason: "stop"}}}
	}
	resp.Model = req.Model
	return port.NewSliceStream(chatResponseToDeltas(resp)), nil
}

// Embeddings implementa [Adapter].
func (f *FakeAdapter) Embeddings(_ context.Context, req port.EmbeddingsRequest, cred Credential) (port.EmbeddingsResponse, error) {
	f.calls++
	f.lastCred = cred
	resp, ok := f.embed[req.Model]
	if !ok {
		resp = port.EmbeddingsResponse{Object: "list"}
	}
	resp.Model = req.Model
	return resp, nil
}

// chatResponseToDeltas converte uma resposta síncrona numa sequência de deltas
// de streaming DETERMINISTA: primeiro o papel + texto, depois um delta por tool
// call, e um delta final com finish_reason e usage. É a inversa lógica de
// [port.CollectStream].
func chatResponseToDeltas(resp port.ChatResponse) []port.ChatStreamDelta {
	var deltas []port.ChatStreamDelta
	if len(resp.Choices) == 0 {
		return []port.ChatStreamDelta{{FinishReason: "stop"}}
	}
	msg := resp.Choices[0].Message
	deltas = append(deltas, port.ChatStreamDelta{Role: port.RoleAssistant, Content: msg.Content})
	for i, tc := range msg.ToolCalls {
		deltas = append(deltas, port.ChatStreamDelta{
			ToolCalls: []port.ToolCallDelta{{
				Index:             i,
				ID:                tc.ID,
				Name:              tc.Function.Name,
				ArgumentsFragment: tc.Function.Arguments,
			}},
		})
	}
	finish := resp.Choices[0].FinishReason
	if finish == "" {
		finish = "stop"
	}
	usage := resp.Usage
	deltas = append(deltas, port.ChatStreamDelta{FinishReason: finish, Usage: &usage})
	return deltas
}
