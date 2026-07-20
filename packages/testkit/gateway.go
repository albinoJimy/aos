package testkit

import (
	"context"
	"io"
	"sync"
)

// ===========================================================================
// Model Gateway (GW) — CONTRATO ALINHADO ao _BRIEF §2 + fake
//
// {Gateway.Chat / Embeddings / ChatStream, Adapter} (platform/model-gateway/port).
//
// LAYERING: o contrato real do GW vive em model-gateway (e os adaptadores em
// .../internal) e arrasta 12 replace — importá-lo pesaria o go.mod do testkit e a
// cadeia de build. Por isso o testkit define aqui uma superfície ALINHADA (forma
// OpenAI: mensagens com papéis, tool calls, usage) e um fake determinista. É um
// MOCK ALINHADO AO CONTRATO, não a porta real: a forma dos métodos (Chat/
// Embeddings/ChatStream) e dos tipos espelha o port para troca trivial pelo
// Gateway real quando o teste precisar da pipeline completa.
// ===========================================================================

// GWRole é o papel de uma mensagem (forma OpenAI; espelha port.Role).
type GWRole string

const (
	GWRoleSystem    GWRole = "system"
	GWRoleUser      GWRole = "user"
	GWRoleAssistant GWRole = "assistant"
	GWRoleTool      GWRole = "tool"
)

// GWMessage é uma mensagem da conversa (espelha port.Message, subconjunto).
type GWMessage struct {
	Role       GWRole
	Content    string
	ToolCallID string
}

// GWUsage é o consumo de tokens (espelha port.Usage, subconjunto).
type GWUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// GWChatRequest é o pedido de chat NORMALIZADO (espelha port.ChatRequest). Os
// metadados de plataforma (Principal/Region/Board/RunID) NÃO vão no "wire" — aqui
// existem só para o fake os poder registar/asserir.
type GWChatRequest struct {
	Model     string
	Messages  []GWMessage
	Stream    bool
	Principal string
	Region    string
	Board     string
	RunID     string
}

// GWChatResponse é a resposta de chat NORMALIZADA (espelha port.ChatResponse).
// Model é o EFECTIVAMENTE usado (pode diferir de GWChatRequest.Model num swap).
type GWChatResponse struct {
	ID      string
	Model   string
	Content string
	Usage   GWUsage
}

// GWEmbeddingsRequest é o pedido de embeddings NORMALIZADO (espelha port).
type GWEmbeddingsRequest struct {
	Model string
	Input []string
	RunID string
}

// GWEmbeddingsResponse é a resposta de embeddings NORMALIZADA (espelha port).
type GWEmbeddingsResponse struct {
	Model string
	// Vectors é um vector por input (deterministas no fake).
	Vectors [][]float64
	Usage   GWUsage
}

// GWChatStreamDelta é um incremento do streaming (espelha port.ChatStreamDelta).
type GWChatStreamDelta struct {
	Content      string
	FinishReason string
}

// GWChatStream é o iterador de deltas (espelha port.ChatStream). Recv devolve o
// próximo delta ou [io.EOF] no fim; Close é idempotente.
type GWChatStream interface {
	Recv() (GWChatStreamDelta, error)
	Close() error
}

// Gateway é a porta ÚNICA alinhada ao _BRIEF §2 (Chat/Embeddings/ChatStream).
type Gateway interface {
	PortVersion() string
	Chat(ctx context.Context, req GWChatRequest) (GWChatResponse, error)
	ChatStream(ctx context.Context, req GWChatRequest) (GWChatStream, error)
	Embeddings(ctx context.Context, req GWEmbeddingsRequest) (GWEmbeddingsResponse, error)
}

// FakeGateway é o Model Gateway de referência DETERMINISTA. Devolve respostas
// canónicas (ou programadas), fragmenta a resposta de chat em deltas estáveis no
// streaming, e REGISTA os pedidos observados. Nenhuma rede, nenhum provider — I/O
// isolado, sem flakiness. Consolida o padrão do adapters.FakeAdapter (que vive no
// pacote interno do GW) numa forma exportada e alinhada ao contrato.
type FakeGateway struct {
	mu sync.Mutex

	// Reply é o conteúdo de resposta de chat por omissão.
	Reply string
	// EffectiveModel é o modelo devolvido na resposta (default: o pedido). Preencher
	// para simular um swap de modelo/provider.
	EffectiveModel string
	// Err, se != nil, é devolvido por todos os métodos (fail-closed).
	Err error

	chats  []GWChatRequest
	embeds []GWEmbeddingsRequest
}

// NewFakeGateway constrói um GW que responde `reply` a qualquer chat.
func NewFakeGateway(reply string) *FakeGateway {
	return &FakeGateway{Reply: reply}
}

// PortVersion devolve a versão do contrato alinhado.
func (g *FakeGateway) PortVersion() string { return "1.0.0" }

// Chat implementa [Gateway]. Determinista: usage derivada do tamanho do texto.
func (g *FakeGateway) Chat(_ context.Context, req GWChatRequest) (GWChatResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Err != nil {
		return GWChatResponse{}, g.Err
	}
	g.chats = append(g.chats, req)
	model := g.EffectiveModel
	if model == "" {
		model = req.Model
	}
	prompt := int64(0)
	for _, m := range req.Messages {
		prompt += int64(len(m.Content))
	}
	completion := int64(len(g.Reply))
	return GWChatResponse{
		ID:      "chat-testkit",
		Model:   model,
		Content: g.Reply,
		Usage:   GWUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion},
	}, nil
}

// ChatStream implementa [Gateway]: devolve a resposta canónica fragmentada em
// deltas por palavra (ordem estável), terminada por um delta com FinishReason.
func (g *FakeGateway) ChatStream(_ context.Context, req GWChatRequest) (GWChatStream, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Err != nil {
		return nil, g.Err
	}
	g.chats = append(g.chats, req)
	return newFakeStream(g.Reply), nil
}

// Embeddings implementa [Gateway]: um vector determinista por input (o comprimento
// de cada input, normalizado), sem qualquer modelo real.
func (g *FakeGateway) Embeddings(_ context.Context, req GWEmbeddingsRequest) (GWEmbeddingsResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.Err != nil {
		return GWEmbeddingsResponse{}, g.Err
	}
	g.embeds = append(g.embeds, req)
	vecs := make([][]float64, len(req.Input))
	var total int64
	for i, in := range req.Input {
		vecs[i] = []float64{float64(len(in)), float64(i)}
		total += int64(len(in))
	}
	return GWEmbeddingsResponse{
		Model:   req.Model,
		Vectors: vecs,
		Usage:   GWUsage{PromptTokens: total, TotalTokens: total},
	}, nil
}

// Chats devolve uma cópia dos pedidos de chat observados.
func (g *FakeGateway) Chats() []GWChatRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]GWChatRequest, len(g.chats))
	copy(out, g.chats)
	return out
}

// Embeds devolve uma cópia dos pedidos de embeddings observados.
func (g *FakeGateway) Embeds() []GWEmbeddingsRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]GWEmbeddingsRequest, len(g.embeds))
	copy(out, g.embeds)
	return out
}

// fakeStream é um [GWChatStream] em memória sobre uma lista de deltas.
type fakeStream struct {
	mu     sync.Mutex
	deltas []GWChatStreamDelta
	i      int
	closed bool
}

// newFakeStream fragmenta `reply` em deltas por palavra (delimitadas por espaço),
// preservando a ordem, e acrescenta um delta final com FinishReason="stop".
func newFakeStream(reply string) *fakeStream {
	var deltas []GWChatStreamDelta
	word := ""
	flush := func(trailing string) {
		if word != "" {
			deltas = append(deltas, GWChatStreamDelta{Content: word + trailing})
			word = ""
		}
	}
	for _, r := range reply {
		if r == ' ' {
			flush(" ")
			continue
		}
		word += string(r)
	}
	flush("")
	deltas = append(deltas, GWChatStreamDelta{FinishReason: "stop"})
	return &fakeStream{deltas: deltas}
}

// Recv devolve o próximo delta ou [io.EOF] quando esgota.
func (s *fakeStream) Recv() (GWChatStreamDelta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.i >= len(s.deltas) {
		return GWChatStreamDelta{}, io.EOF
	}
	d := s.deltas[s.i]
	s.i++
	return d, nil
}

// Close liberta o stream (idempotente).
func (s *fakeStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// compile-time: o FakeGateway satisfaz o contrato alinhado.
var _ Gateway = (*FakeGateway)(nil)
