package wirefake

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
)

// Os FALSOS QUE VALIDAM O PEDIDO (AOS-508): imitam um provider que, ao segundo turno, exige de
// volta o que emitiu no primeiro. Não validam assinaturas criptográficas — isso só o modelo real
// prova —, só a presença e a igualdade do que emitiram.

const (
	// IDEmitido é o id de tool call que o [Validador] emite no primeiro turno.
	IDEmitido = "call_Emitido01"
	// RaciocinioEmitido é o raciocínio que o [Validador] emite no primeiro turno.
	RaciocinioEmitido = "raciocinio do primeiro turno"
	// AssinaturaEmitida é a assinatura que o [Validador] emite na tool call do primeiro turno.
	AssinaturaEmitida = "YXNzaW5hdHVyYQ"
)

// Exigencia é o que um [Validador] exige do segundo pedido.
type Exigencia int

const (
	// ExigeIDEmitido — o `tool_call_id` da mensagem `tool` e o `id` da tool call do `assistant`
	// têm de ser o [IDEmitido]; um id que o falso não emitiu, ou com mais bytes do que
	// [Validador.MaxID], dá 400.
	ExigeIDEmitido Exigencia = iota
	// ExigeRaciocinio — o `assistant` com tool calls tem de trazer `reasoning_content` igual ao
	// [RaciocinioEmitido].
	ExigeRaciocinio
	// ExigeAssinatura — a tool call do `assistant` tem de trazer `thought_signature` igual à
	// [AssinaturaEmitida].
	ExigeAssinatura
)

// Validador é um provider falso de dois turnos. Ao primeiro pedido (sem nenhuma mensagem
// `assistant`) responde com uma tool call; aos seguintes valida o que o pedido devolve e
// responde o texto final, ou 400 com a causa.
type Validador struct {
	Exige Exigencia
	// MaxID é o comprimento máximo de um id aceite por [ExigeIDEmitido]. Zero ⇒ 40.
	MaxID int

	mu      sync.Mutex
	pedidos []Pedido
}

// Pedidos devolve uma cópia dos pedidos recebidos, pela ordem.
func (v *Validador) Pedidos() []Pedido {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]Pedido(nil), v.pedidos...)
}

type pedidoValidado struct {
	Messages []struct {
		Role             string          `json:"role"`
		ToolCallID       string          `json:"tool_call_id"`
		ReasoningContent json.RawMessage `json:"reasoning_content"`
		ToolCalls        []struct {
			ID               string `json:"id"`
			ThoughtSignature string `json:"thought_signature"`
		} `json:"tool_calls"`
	} `json:"messages"`
}

// ServeHTTP implementa [http.Handler].
func (v *Validador) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corpo, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	v.mu.Lock()
	v.pedidos = append(v.pedidos, Pedido{Caminho: r.URL.Path, Corpo: corpo})
	v.mu.Unlock()
	var p pedidoValidado
	if err := json.Unmarshal(corpo, &p); err != nil {
		recusar(w, "pedido ilegivel")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	segundo := false
	for _, m := range p.Messages {
		if m.Role == "assistant" {
			segundo = true
		}
	}
	if !segundo {
		_, _ = w.Write([]byte(`{"id":"chatcmpl-falso","object":"chat.completion","created":1700000000,"model":"modelo-falso",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":"` + RaciocinioEmitido + `",` +
			`"tool_calls":[{"id":"` + IDEmitido + `","type":"function","thought_signature":"` + AssinaturaEmitida + `",` +
			`"function":{"name":"arquivo","arguments":"{\"path\":\"notas.txt\"}"}}]},"finish_reason":"tool_calls"}],` +
			`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`))
		return
	}
	if causa := v.validar(p); causa != "" {
		recusar(w, causa)
		return
	}
	_, _ = w.Write(Corpo("content_texto"))
}

func (v *Validador) validar(p pedidoValidado) string {
	maxID := v.MaxID
	if maxID == 0 {
		maxID = 40
	}
	for _, m := range p.Messages {
		switch {
		case m.Role == "tool" && v.Exige == ExigeIDEmitido:
			if len(m.ToolCallID) > maxID {
				return "tool_call_id demasiado longo"
			}
			if m.ToolCallID != IDEmitido {
				return "tool_call_id que este provider nao emitiu"
			}
		case m.Role == "assistant" && len(m.ToolCalls) > 0:
			switch v.Exige {
			case ExigeIDEmitido:
				for _, tc := range m.ToolCalls {
					if len(tc.ID) > maxID {
						return "tool_call_id demasiado longo"
					}
					if tc.ID != IDEmitido {
						return "id de tool call que este provider nao emitiu"
					}
				}
			case ExigeRaciocinio:
				var s string
				if json.Unmarshal(m.ReasoningContent, &s) != nil || s != RaciocinioEmitido {
					return "assistant com tool calls sem o raciocinio do turno anterior"
				}
			case ExigeAssinatura:
				for _, tc := range m.ToolCalls {
					if tc.ThoughtSignature != AssinaturaEmitida {
						return "tool call sem a assinatura do turno anterior"
					}
				}
			}
		}
	}
	return ""
}

func recusar(w http.ResponseWriter, causa string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"error":{"message":"` + causa + `","type":"invalid_request_error"}}`))
}
