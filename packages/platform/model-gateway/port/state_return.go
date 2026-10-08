package port

import (
	"bytes"
	"encoding/json"
	"errors"
)

// A DEVOLUÇÃO DO ESTADO OPACO AO PROVIDER (AOS-515, ADR-040 §2.9; contrato 1.9.0).
//
// [MessageState] é o estado opaco de UM turno do modelo, agarrado à mensagem `assistant` desse
// turno num pedido. Quem o agarra é a projecção nativa 1.3.0, a partir do envelope que a captura
// do turno guardou; quem decide se ele SAI é o gateway, no momento em que sabe a que rota o
// pedido vai ([MessageState.Return]); e quem o escreve no wire é [ChatRequest.MarshalWire].
//
// O QUE SAI, E COMO. Cada campo volta ao SÍTIO de onde veio, com o NOME com que veio e os BYTES
// que vieram ([ProviderStateField.Raw]), copiados para o corpo do pedido sem passar por nenhum
// codificador — nem para os compactar, nem para lhes escapar um carácter:
//
//   - os campos de raciocínio de `message` (`reasoning_content`, `reasoning`,
//     `reasoning_details`, `thinking_blocks`, `thinking`) voltam como chaves da mensagem
//     `assistant`;
//   - os que vieram em `message.provider_specific_fields` voltam dentro de um objecto
//     `provider_specific_fields` da mensagem;
//   - as assinaturas de cada tool call voltam na tool call (ou na sua `function`), na posição
//     dela;
//   - o id que o provider deu a cada tool call volta em `tool_calls[n].id` e no `tool_call_id`
//     da mensagem `tool` dessa chamada, quando a rota o pede ([MessageState.ProviderIDs]) — o
//     valor DESCODIFICADO e já limitado ([ProviderStateToolCall.IDValue]), nunca os bytes crus.
//
// O QUE NÃO SAI. Um estado sem [MessageState.Return] não muda um byte do pedido. E
// [Message.ReasoningContent] continua a ser retirado de todas as mensagens, com ou sem estado: o
// raciocínio só volta a um provider como carga opaca do estado, à rota que o produziu.
//
// NÃO É TEXTO NEM AUTORIDADE (decisão D3). Nada daqui entra em [Message.Content], e o gateway
// não lê os valores: só os copia.
type MessageState struct {
	// RouteProfileDigest e ServedModel são a ROTA a que o estado pertence, tal como o envelope
	// a gravou: o digest do perfil com que o turno foi comparado e o modelo que o serviu.
	RouteProfileDigest string
	ServedModel        string
	// Fields são os campos de raciocínio do turno, pela ordem em que vieram — os de `message`
	// ([StateWhereMessage]) e os de `message.provider_specific_fields` ([StateWherePSF]).
	Fields []ProviderStateField
	// ToolCalls tem uma entrada por tool call da mensagem, pela ordem, ou é vazio.
	ToolCalls []ProviderStateToolCall
	// Missing diz porque o turno NÃO TEM estado que se possa devolver, no vocabulário fechado
	// StateMissing*. Vazio ⇒ tem. Com Missing os outros campos estão vazios.
	Missing string
	// Return diz que o estado SAI neste pedido. Só o gateway o escreve, por mensagem e em cada
	// pedido, depois de conferir a rota; o que um chamador lá tenha posto é sobreposto.
	Return bool
	// ProviderIDs diz que os ids das tool calls deste turno vão no wire como o provider os deu.
	// Só vale com Return, e só o gateway o escreve.
	ProviderIDs bool
}

// Causas de um turno sem estado devolvível ([MessageState.Missing]). Vocabulário FECHADO.
const (
	// StateMissingAbsent — o tail não refere estado nenhum para o turno: o provider não o
	// mandou, ou ele não foi guardado (acima do tecto, sem nonce, desalinhado).
	StateMissingAbsent = "estado_ausente"
	// StateMissingReference — o tail refere o estado e os bytes não existem: a captura foi
	// feita em modo sensível, que guarda só a referência.
	StateMissingReference = "estado_so_referencia"
	// StateMissingDigest — os bytes existem e o seu `sha256` não é o do rótulo do tail.
	StateMissingDigest = "estado_digest_diferente"
	// StateMissingUnreadable — os bytes conferem com o rótulo e não são um envelope legível.
	StateMissingUnreadable = "estado_ilegivel"
	// StateMissingMisaligned — o envelope tem um número de tool calls que não é o do turno
	// projectado (o turno escalou a meio, e só parte das chamadas foi despachada).
	StateMissingMisaligned = "estado_desalinhado"
)

// ErrStateReturnWire — um estado marcado para sair não se consegue escrever no wire. Fail-closed:
// o pedido não é serializado.
var ErrStateReturnWire = errors.New("port: o estado opaco de um turno nao se escreve no pedido")

// temEstadoParaSair diz se alguma mensagem leva estado marcado para sair.
func temEstadoParaSair(msgs []Message) bool {
	for i := range msgs {
		if msgs[i].State != nil && msgs[i].State.Return {
			return true
		}
	}
	return false
}

// marshalComEstado serializa o pedido quando há estado para sair. A forma é a de
// [wireChatRequest], campo a campo e pela mesma ordem; a diferença é que o corpo é montado à
// mão, para que os bytes do estado sejam COPIADOS e não re-codificados. Para um pedido sem
// estado o resultado é, byte a byte, o de `json.Marshal(wireChatRequest)` — preso por teste.
func marshalComEstado(w wireChatRequest) ([]byte, error) {
	modelo, err := json.Marshal(w.Model)
	if err != nil {
		return nil, err
	}
	// O resto do pedido — tudo menos `model` e `messages` — pelo codificador de sempre.
	cauda, err := json.Marshal(struct {
		Tools           []Tool         `json:"tools,omitempty"`
		ToolChoice      string         `json:"tool_choice,omitempty"`
		Stream          bool           `json:"stream,omitempty"`
		Temperature     *float64       `json:"temperature,omitempty"`
		Seed            *int64         `json:"seed,omitempty"`
		MaxTokens       int            `json:"max_tokens,omitempty"`
		Thinking        *ThinkingParam `json:"thinking,omitempty"`
		ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	}{w.Tools, w.ToolChoice, w.Stream, w.Temperature, w.Seed, w.MaxTokens, w.Thinking, w.ReasoningEffort})
	if err != nil {
		return nil, err
	}

	// Os ids do provider, por id do runtime: a mensagem `tool` de uma chamada leva o mesmo id
	// que a tool call do `assistant` a que responde.
	trocas := map[string]string{}
	for i := range w.Messages {
		st := w.Messages[i].State
		if st == nil || !st.Return || !st.ProviderIDs {
			continue
		}
		if len(st.ToolCalls) != len(w.Messages[i].ToolCalls) {
			return nil, ErrStateReturnWire
		}
		for n, tc := range w.Messages[i].ToolCalls {
			if !st.ToolCalls[n].IDUsable || st.ToolCalls[n].IDValue == "" {
				return nil, ErrStateReturnWire
			}
			trocas[tc.ID] = st.ToolCalls[n].IDValue
		}
	}

	var b bytes.Buffer
	b.WriteString(`{"model":`)
	b.Write(modelo)
	b.WriteString(`,"messages":[`)
	for i, m := range w.Messages {
		if i > 0 {
			b.WriteByte(',')
		}
		if m.Role == RoleTool {
			if novo, troca := trocas[m.ToolCallID]; troca {
				m.ToolCallID = novo
			}
		}
		if m.State == nil || !m.State.Return {
			cru, err := json.Marshal(m)
			if err != nil {
				return nil, err
			}
			b.Write(cru)
			continue
		}
		if err := escreverMensagemComEstado(&b, m); err != nil {
			return nil, err
		}
	}
	b.WriteByte(']')
	if len(cauda) > 2 {
		b.WriteByte(',')
		b.Write(cauda[1 : len(cauda)-1])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// escreverMensagemComEstado escreve uma mensagem `assistant` com o estado do seu turno. Os campos
// da mensagem vêm pela ordem de [Message]; o estado vem depois deles.
func escreverMensagemComEstado(b *bytes.Buffer, m Message) error {
	st := m.State
	if len(st.ToolCalls) != 0 && len(st.ToolCalls) != len(m.ToolCalls) {
		return ErrStateReturnWire
	}
	b.WriteString(`{"role":`)
	if err := escreverJSON(b, m.Role); err != nil {
		return err
	}
	b.WriteString(`,"content":`)
	if err := escreverJSON(b, m.Content); err != nil {
		return err
	}
	if m.Name != "" {
		b.WriteString(`,"name":`)
		if err := escreverJSON(b, m.Name); err != nil {
			return err
		}
	}
	if m.ToolCallID != "" {
		b.WriteString(`,"tool_call_id":`)
		if err := escreverJSON(b, m.ToolCallID); err != nil {
			return err
		}
	}
	if len(m.ToolCalls) > 0 {
		b.WriteString(`,"tool_calls":[`)
		for n, tc := range m.ToolCalls {
			if n > 0 {
				b.WriteByte(',')
			}
			var doEstado *ProviderStateToolCall
			if len(st.ToolCalls) != 0 {
				doEstado = &st.ToolCalls[n]
			}
			if err := escreverChamadaComEstado(b, tc, doEstado, st.ProviderIDs); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	}
	// Os campos de raciocínio de `message`, pela ordem em que vieram.
	for _, f := range st.Fields {
		if f.Where == StateWhereMessage {
			if err := escreverCampoCru(b, f); err != nil {
				return err
			}
		}
	}
	// Os que vieram dentro de `provider_specific_fields`, no mesmo sítio.
	aberto := false
	for _, f := range st.Fields {
		switch f.Where {
		case StateWhereMessage:
		case StateWherePSF:
			if !aberto {
				b.WriteString(`,"provider_specific_fields":{`)
				aberto = true
			} else {
				b.WriteByte(',')
			}
			if err := escreverParCru(b, f); err != nil {
				return err
			}
		default:
			return ErrStateReturnWire
		}
	}
	if aberto {
		b.WriteByte('}')
	}
	b.WriteByte('}')
	return nil
}

// escreverChamadaComEstado escreve uma tool call com as assinaturas que o provider lhe deu, e com
// o id do provider quando a rota o pede.
func escreverChamadaComEstado(b *bytes.Buffer, tc ToolCall, st *ProviderStateToolCall, idDoProvider bool) error {
	id := tc.ID
	if idDoProvider {
		if st == nil || !st.IDUsable || st.IDValue == "" {
			return ErrStateReturnWire
		}
		id = st.IDValue
	}
	b.WriteString(`{"id":`)
	if err := escreverJSON(b, id); err != nil {
		return err
	}
	b.WriteString(`,"type":`)
	if err := escreverJSON(b, tc.Type); err != nil {
		return err
	}
	b.WriteString(`,"function":{"name":`)
	if err := escreverJSON(b, tc.Function.Name); err != nil {
		return err
	}
	b.WriteString(`,"arguments":`)
	if err := escreverJSON(b, tc.Function.Arguments); err != nil {
		return err
	}
	if st != nil {
		for _, f := range st.Fields {
			if f.Where == StateWhereFunction {
				if err := escreverCampoCru(b, f); err != nil {
					return err
				}
			}
		}
	}
	b.WriteByte('}')
	if st != nil {
		for _, f := range st.Fields {
			switch f.Where {
			case StateWhereFunction:
			case StateWhereToolCall:
				if err := escreverCampoCru(b, f); err != nil {
					return err
				}
			default:
				return ErrStateReturnWire
			}
		}
	}
	b.WriteByte('}')
	return nil
}

// escreverJSON escreve v pelo codificador de sempre (o mesmo de [ChatRequest.MarshalWire]).
func escreverJSON(b *bytes.Buffer, v any) error {
	cru, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b.Write(cru)
	return nil
}

// escreverCampoCru acrescenta `,"<nome>":<bytes>` a um objecto já aberto e com chaves.
func escreverCampoCru(b *bytes.Buffer, f ProviderStateField) error {
	b.WriteByte(',')
	return escreverParCru(b, f)
}

// escreverParCru escreve `"<nome>":<bytes>`. O nome tem de ser um dos nomes das listas fechadas
// do estado ([nomeDeEstadoNoWire]) e os bytes um valor JSON; os bytes são COPIADOS.
func escreverParCru(b *bytes.Buffer, f ProviderStateField) error {
	if !nomeDeEstadoNoWire(f.Where, f.Name) || len(f.Raw) == 0 || !json.Valid(f.Raw) {
		return ErrStateReturnWire
	}
	b.WriteByte('"')
	b.WriteString(f.Name)
	b.WriteString(`":`)
	b.Write(f.Raw)
	return nil
}

// nomeDeEstadoNoWire diz se nome é uma chave que o estado pode escrever no sítio dado: os nomes
// do raciocínio na mensagem e no seu `provider_specific_fields`, e os campos de assinatura por
// chamada na tool call e na sua `function`. São as MESMAS listas fechadas com que a sonda o
// tirou do corpo ([ProbeProviderState]): um envelope não escreve no pedido uma chave que a sonda
// não pudesse ter lido — nem `content`, nem `role`, nem `tool_calls`.
func nomeDeEstadoNoWire(onde, nome string) bool {
	switch onde {
	case StateWhereMessage, StateWherePSF:
		return eNomeDeRaciocinio(nome)
	case StateWhereToolCall, StateWhereFunction:
		return camposDeEstadoDaChamada[nome]
	default:
		return false
	}
}
