package wirefake

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// O FALSO QUE EXIGE O ESTADO DE VOLTA, INALTERADO (AOS-515). Imita as regras documentadas de um
// fornecedor com raciocínio assinado: em cada turno com tools emite blocos de raciocínio (um
// assinado, um redigido, um de texto vazio só com assinatura), e em cada pedido seguinte exige
//
//   - os blocos de CADA turno anterior de volta na mensagem `assistant` desse turno, com os
//     MESMOS BYTES em que os emitiu — os bytes são escritos de propósito com espaços, com `<`,
//     `&` e um escape, para que qualquer re-serialização os mude;
//   - o `reasoning_content` e a assinatura da tool call, também byte a byte;
//   - que o pedido anterior seja um PREFIXO exacto deste, mensagem a mensagem, e que `tools` não
//     tenha mudado;
//   - com [Exigente.ExigeID], o id que emitiu, no `assistant` e na mensagem `tool`.
//
// Falhando qualquer uma responde 400, como o fornecedor. Não valida assinaturas criptográficas:
// isso só o modelo real prova. Com [Exigente.Proibe] é o contrário: um provider que NÃO aceita
// estado — responde 400 a qualquer mensagem que traga um campo de raciocínio ou uma assinatura.
type Exigente struct {
	// Turnos é o número de turnos com tool call antes do texto final. Zero ⇒ 2.
	Turnos int
	// ExigeID exige o id de tool call que o falso emitiu.
	ExigeID bool
	// Proibe faz do falso um provider que recusa qualquer estado no pedido.
	Proibe bool
	// Tool e Argumentos são a tool call que o falso emite em cada turno: o nome da tool e os
	// argumentos, em JSON. Vazios ⇒ `doc_read` com `{"doc_id":"notas"}`, como sempre. Servem a
	// quem corre o falso atrás de um runtime com outras tools (o banco de ensaio, AOS-516).
	Tool       string
	Argumentos string

	mu       sync.Mutex
	pedidos  []Pedido
	anterior []json.RawMessage
	tools    json.RawMessage
}

// Pedidos devolve uma cópia dos pedidos recebidos, pela ordem.
func (e *Exigente) Pedidos() []Pedido {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Pedido(nil), e.pedidos...)
}

// BlocosEmitidos devolve os bytes do `thinking_blocks` que o falso emite no turno n (de 0).
func BlocosEmitidos(n int) []byte {
	return []byte(fmt.Sprintf(`[ {"type":"thinking","thinking":"S-PENSA-%d <b> & é","signature":"U0lH-%d+/=="} , {"type":"redacted_thinking","data":"UkVE-%d"},{"type":"thinking","thinking":"","signature":"VkFaSU8-%d"} ]`, n, n, n, n))
}

// RaciocinioEmitidoNoTurno devolve os bytes do `reasoning_content` do turno n.
func RaciocinioEmitidoNoTurno(n int) []byte {
	return []byte(fmt.Sprintf(`"S-RACIOCINIO-%d"`, n))
}

// AssinaturaEmitidaNoTurno devolve os bytes da `thought_signature` da tool call do turno n.
func AssinaturaEmitidaNoTurno(n int) []byte {
	return []byte(fmt.Sprintf(`"S-ASSINATURA-%d/+="`, n))
}

// IDEmitidoNoTurno devolve o id da tool call do turno n.
func IDEmitidoNoTurno(n int) string { return fmt.Sprintf("toolu_Exigente%02d", n) }

// ServeHTTP implementa [http.Handler].
func (e *Exigente) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corpo, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pedidos = append(e.pedidos, Pedido{Caminho: r.URL.Path, Corpo: corpo})
	var topo struct {
		Messages []json.RawMessage `json:"messages"`
		Tools    json.RawMessage   `json:"tools"`
	}
	if err := json.Unmarshal(corpo, &topo); err != nil {
		recusar(w, "pedido ilegivel")
		return
	}
	turnos, causa := e.validar(topo.Messages)
	if causa == "" && !e.Proibe {
		switch {
		case len(topo.Messages) < len(e.anterior):
			causa = "o pedido tem menos mensagens do que o anterior"
		case e.anterior != nil && !bytes.Equal(e.tools, topo.Tools):
			causa = "as tools mudaram entre turnos"
		default:
			for i := range e.anterior {
				if !bytes.Equal(e.anterior[i], topo.Messages[i]) {
					causa = fmt.Sprintf("a mensagem %d mudou em relacao ao pedido anterior", i)
					break
				}
			}
		}
	}
	if causa != "" {
		recusar(w, causa)
		return
	}
	e.anterior, e.tools = topo.Messages, topo.Tools
	w.Header().Set("Content-Type", "application/json")
	max := e.Turnos
	if max == 0 {
		max = 2
	}
	if turnos >= max {
		_, _ = w.Write(Corpo("content_texto"))
		return
	}
	tool, argumentos := e.Tool, e.Argumentos
	if tool == "" {
		tool = "doc_read"
	}
	if argumentos == "" {
		argumentos = `{"doc_id":"notas"}`
	}
	// O nome e os argumentos vão pelo codificador: os argumentos são uma STRING com JSON dentro.
	nome, _ := json.Marshal(tool)
	args, _ := json.Marshal(argumentos)
	_, _ = w.Write([]byte(`{"id":"chatcmpl-exigente","object":"chat.completion","created":1700000000,"model":"modelo-falso",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":` + string(RaciocinioEmitidoNoTurno(turnos)) + `,` +
		`"thinking_blocks":` + string(BlocosEmitidos(turnos)) + `,` +
		`"tool_calls":[{"id":"` + IDEmitidoNoTurno(turnos) + `","type":"function","thought_signature":` + string(AssinaturaEmitidaNoTurno(turnos)) + `,` +
		`"function":{"name":` + string(nome) + `,"arguments":` + string(args) + `}}]},"finish_reason":"tool_calls"}],` +
		`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`))
}

// validar confere o estado de cada turno anterior do pedido e devolve quantos turnos com tool
// calls ele traz, e a causa da recusa (vazia se nenhuma).
func (e *Exigente) validar(msgs []json.RawMessage) (turnos int, causa string) {
	idEsperado := ""
	for _, cru := range msgs {
		var m map[string]json.RawMessage
		if json.Unmarshal(cru, &m) != nil {
			return turnos, "mensagem ilegivel"
		}
		var papel string
		_ = json.Unmarshal(m["role"], &papel)
		var chamadas []map[string]json.RawMessage
		_ = json.Unmarshal(m["tool_calls"], &chamadas)
		if e.Proibe {
			for _, chave := range []string{"reasoning_content", "reasoning", "reasoning_details", "thinking_blocks", "thinking", "provider_specific_fields"} {
				if _, tem := m[chave]; tem {
					return turnos, "este provider nao aceita " + chave + " num pedido"
				}
			}
			for _, c := range chamadas {
				for _, chave := range []string{"thought_signature", "signature", "provider_specific_fields", "extra_content"} {
					if _, tem := c[chave]; tem {
						return turnos, "este provider nao aceita " + chave + " numa tool call"
					}
				}
			}
		}
		switch {
		case papel == "tool" && e.ExigeID && idEsperado != "":
			var id string
			_ = json.Unmarshal(m["tool_call_id"], &id)
			if id != idEsperado {
				return turnos, "tool_call_id que este provider nao emitiu"
			}
		case papel == "assistant" && len(chamadas) > 0:
			n := turnos
			turnos++
			if e.Proibe {
				continue
			}
			if !bytes.Equal(m["thinking_blocks"], BlocosEmitidos(n)) {
				return turnos, "os blocos de raciocinio do turno faltam ou foram alterados"
			}
			if !bytes.Equal(m["reasoning_content"], RaciocinioEmitidoNoTurno(n)) {
				return turnos, "o raciocinio do turno falta ou foi alterado"
			}
			if len(chamadas) != 1 || !bytes.Equal(chamadas[0]["thought_signature"], AssinaturaEmitidaNoTurno(n)) {
				return turnos, "a assinatura da tool call falta ou foi alterada"
			}
			if e.ExigeID {
				var id string
				_ = json.Unmarshal(chamadas[0]["id"], &id)
				if id != IDEmitidoNoTurno(n) {
					return turnos, "id de tool call que este provider nao emitiu"
				}
				idEsperado = id
			}
		}
	}
	return turnos, ""
}
