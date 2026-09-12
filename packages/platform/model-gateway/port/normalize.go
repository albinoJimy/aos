package port

import (
	"encoding/json"
	"errors"
	"io"
)

// Erros de validação/normalização do contrato. São fail-closed: um pedido
// malformado é rejeitado antes de qualquer estágio da pipeline.
var (
	// ErrNoModel — o pedido não nomeia um modelo.
	ErrNoModel = errors.New("port: modelo em falta no pedido")
	// ErrNoMessages — o chat não tem mensagens.
	ErrNoMessages = errors.New("port: chat sem mensagens")
	// ErrNoInput — os embeddings não têm input.
	ErrNoInput = errors.New("port: embeddings sem input")
	// ErrBadRole — uma mensagem tem um papel desconhecido.
	ErrBadRole = errors.New("port: papel de mensagem invalido")
)

// wireChatRequest é a forma JSON enviada ao provider (só os campos wire — os
// metadados de plataforma de [ChatRequest] com tag "-" são deliberadamente
// omitidos, garantindo que Principal/Region/Board nunca vazam para o provedor).
type wireChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Tools       []Tool    `json:"tools,omitempty"`
	ToolChoice  string    `json:"tool_choice,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	Seed        *int64    `json:"seed,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

// Normalize valida e canoniza um [ChatRequest] de forma DETERMINISTA: os mesmos
// inputs produzem sempre a mesma forma. Preenche defaults estáveis (Type das
// tools = "function"), valida papéis e devolve erro fail-closed se o pedido for
// inválido. Não introduz relógio nem aleatoriedade.
func (r ChatRequest) Normalize() (ChatRequest, error) {
	if r.Model == "" {
		return ChatRequest{}, ErrNoModel
	}
	if len(r.Messages) == 0 {
		return ChatRequest{}, ErrNoMessages
	}
	out := r
	out.Messages = make([]Message, len(r.Messages))
	for i, m := range r.Messages {
		if !validRole(m.Role) {
			return ChatRequest{}, ErrBadRole
		}
		out.Messages[i] = m
	}
	if len(r.Tools) > 0 {
		out.Tools = make([]Tool, len(r.Tools))
		for i, t := range r.Tools {
			if t.Type == "" {
				t.Type = "function"
			}
			out.Tools[i] = t
		}
	}
	return out, nil
}

func validRole(r Role) bool {
	switch r {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
		return true
	default:
		return false
	}
}

// MarshalWire serializa o pedido para o wire JSON compatível OpenAI. É estável
// (ordem de campos fixa pela struct wireChatRequest) e NUNCA inclui os metadados
// de plataforma (Principal/Region/Board). stream reflecte a intenção de
// streaming independentemente do valor em r.Stream.
func (r ChatRequest) MarshalWire(stream bool) ([]byte, error) {
	w := wireChatRequest{
		Model:       r.Model,
		Messages:    r.Messages,
		Tools:       r.Tools,
		ToolChoice:  r.ToolChoice,
		Stream:      stream,
		Temperature: r.Temperature,
		Seed:        r.Seed,
		MaxTokens:   r.MaxTokens,
	}
	return json.Marshal(w)
}

// Normalize valida um [EmbeddingsRequest].
func (r EmbeddingsRequest) Normalize() (EmbeddingsRequest, error) {
	if r.Model == "" {
		return EmbeddingsRequest{}, ErrNoModel
	}
	if len(r.Input) == 0 {
		return EmbeddingsRequest{}, ErrNoInput
	}
	return r, nil
}

// wireUsageProbe é a struct-wire INTERNA que decide a presença do objecto `usage`
// (AOS-321). O ponteiro é o mecanismo: `usage` ausente (ou `null`) deixa o campo a
// nil; `"usage": {}` produz um ponteiro para um [Usage] a zeros. É a única forma no
// wire de separar «o provedor não disse nada» de «o provedor disse zero».
//
// PORQUE UMA SEGUNDA PASSAGEM e não uma cópia da struct de resposta com o campo em
// ponteiro: uma cópia teria de repetir todos os campos de [ChatResponse] e de
// [EmbeddingsResponse] e ficaria a apodrecer em silêncio à primeira vez que um deles
// mudasse. A sonda só conhece o campo que lhe interessa e não pode divergir do resto.
type wireUsageProbe struct {
	Usage *Usage `json:"usage"`
}

// usageAusente reporta se o corpo JSON dado NÃO traz objecto `usage`. Um corpo que o
// primeiro Unmarshal já aceitou não volta a falhar aqui; se falhasse, a resposta
// prudente seria «ausente» (fail-closed), que é o que o erro devolve.
func usageAusente(data []byte) bool {
	var probe wireUsageProbe
	if err := json.Unmarshal(data, &probe); err != nil {
		return true
	}
	return probe.Usage == nil
}

// UnmarshalChatResponse desserializa o wire JSON de uma resposta de chat para a
// forma normalizada. Aceita a forma OpenAI; campos ausentes ficam nos zeros.
//
// EXCEPÇÃO DELIBERADA A ESSA REGRA: o objecto `usage` (AOS-321). Um 200 de um
// provedor que o OMITA deixava aqui um [Usage] zerado indistinguível de uma chamada
// de custo nulo, e esse zero descia até ao agregado por run/árvore e ao evento
// durável `turn.recorded` — fail-open do burn-down que o ADR-008 exige. A ausência
// passa a ficar marcada em [Usage.Ausente]: é neste ponto do wire que a distinção
// existe de facto, e é aqui que ela tem de ser capturada.
func UnmarshalChatResponse(data []byte) (ChatResponse, error) {
	var resp ChatResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return ChatResponse{}, err
	}
	resp.Usage.Ausente = usageAusente(data)
	return resp, nil
}

// UnmarshalEmbeddingsResponse desserializa o wire JSON de uma resposta de
// embeddings para a forma normalizada. Marca [Usage.Ausente] pela mesma razão e
// pelo mesmo mecanismo de [UnmarshalChatResponse] (AOS-321).
func UnmarshalEmbeddingsResponse(data []byte) (EmbeddingsResponse, error) {
	var resp EmbeddingsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return EmbeddingsResponse{}, err
	}
	resp.Usage.Ausente = usageAusente(data)
	return resp, nil
}

// SliceStream é uma implementação de [ChatStream] sobre uma fatia de deltas em
// memória. Útil para adaptadores in-memory (fake) e para testes: entrega os
// deltas por ordem e devolve [io.EOF] no fim. É seguro para um único consumidor.
type SliceStream struct {
	deltas []ChatStreamDelta
	pos    int
	closed bool
}

// NewSliceStream constrói um [SliceStream] a partir de uma cópia dos deltas.
func NewSliceStream(deltas []ChatStreamDelta) *SliceStream {
	cp := make([]ChatStreamDelta, len(deltas))
	copy(cp, deltas)
	return &SliceStream{deltas: cp}
}

// Recv devolve o próximo delta ou [io.EOF].
func (s *SliceStream) Recv() (ChatStreamDelta, error) {
	if s.closed {
		return ChatStreamDelta{}, io.EOF
	}
	if s.pos >= len(s.deltas) {
		return ChatStreamDelta{}, io.EOF
	}
	d := s.deltas[s.pos]
	s.pos++
	return d, nil
}

// Close marca o stream como terminado (idempotente).
func (s *SliceStream) Close() error {
	s.closed = true
	return nil
}

// CollectStream drena um [ChatStream] até [io.EOF] e reconstrói a [ChatResponse]
// agregada: concatena o texto e agrega os fragmentos de tool call por índice.
// É a ponte determinista entre a superfície de streaming e a forma síncrona
// (usada pelo Agent Runtime e por testes de correcção de streaming/tool calling).
func CollectStream(s ChatStream) (ChatResponse, error) {
	defer func() { _ = s.Close() }()
	var (
		text   string
		finish string
		// AOS-321: um stream que NUNCA traga um chunk com `usage` é o mesmo defeito do
		// caminho síncrono por outra porta — a reconstrução devolveria um [Usage] a
		// zeros indistinguível de custo nulo. Parte-se de INDEFINIDO e só um delta com
		// usage o define. Ver [Usage.Ausente].
		usage = Usage{Ausente: true}
		byIdx = map[int]*ToolCall{}
		order []int
	)
	for {
		d, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ChatResponse{}, err
		}
		text += d.Content
		if d.FinishReason != "" {
			finish = d.FinishReason
		}
		if d.Usage != nil {
			usage = *d.Usage
		}
		for _, tc := range d.ToolCalls {
			cur, ok := byIdx[tc.Index]
			if !ok {
				cur = &ToolCall{Type: "function"}
				byIdx[tc.Index] = cur
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Name != "" {
				cur.Function.Name = tc.Name
			}
			cur.Function.Arguments += tc.ArgumentsFragment
		}
	}
	msg := Message{Role: RoleAssistant, Content: text}
	for _, idx := range order {
		msg.ToolCalls = append(msg.ToolCalls, *byIdx[idx])
	}
	if finish == "" {
		finish = "stop"
	}
	return ChatResponse{
		Object:  "chat.completion",
		Choices: []Choice{{Index: 0, Message: msg, FinishReason: finish}},
		Usage:   usage,
	}, nil
}
