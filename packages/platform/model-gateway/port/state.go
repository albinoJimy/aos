package port

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// O ESTADO OPACO DO PROVIDER (AOS-514, ADR-040; contrato 1.7.0).
//
// [ProviderState] é o que uma resposta de chat traz e o provider pode exigir de volta no turno
// seguinte, tirado do CORPO CRU sem o interpretar:
//
//   - o valor de cada campo de raciocínio presente em `message` ([nomesDoRaciocinio]) — todos,
//     e não só o primeiro —, e os mesmos nomes dentro de `message.provider_specific_fields`,
//     para onde o proxy de produção move os que não conhece;
//   - por tool call, o `id` que o provider lhe deu e os campos de assinatura por chamada
//     ([camposDeEstadoDaChamada]), na tool call e na sua `function`.
//
// OS BYTES SÃO OS RECEBIDOS. Cada valor guarda-se como os bytes JSON que vieram no corpo: sem
// re-serializar, sem normalizar espaços nem a ordem das chaves, sem descodificar strings. Uma
// assinatura não sobrevive a nenhuma dessas operações, e quem a valida é o fornecedor — o
// gateway não a lê. A ordem é a do corpo, e uma chave repetida guarda-se as vezes que veio.
//
// É calculado por [ProbeProviderState], uma sonda PRÓPRIA no molde de [ProbeResponseShape]:
// corre ao lado da descodificação da resposta e não a pode fazer falhar.
//
// NÃO É RESPOSTA NEM INSTRUÇÃO. [Message.Content] continua a vir só de `content`; nada daqui
// entra num pedido ([ChatRequest.MarshalWire] não o conhece); e o `id` do provider não é
// identidade de nada — as tool calls do runtime são identificadas pelo runtime. O caminho de
// streaming não é coberto: os runs não o usam.
type ProviderState struct {
	// Fields são os campos de raciocínio de `message` e de `message.provider_specific_fields`,
	// pela ordem em que vieram.
	Fields []ProviderStateField `json:"fields,omitempty"`
	// ToolCalls tem uma entrada por tool call da resposta, pela ordem — a n-ésima é a tool call
	// a que o runtime dá o id `<passo>-tool-<n>`. Vazio quando nenhuma traz `id` nem assinatura.
	ToolCalls []ProviderStateToolCall `json:"tool_calls,omitempty"`
	// Misaligned diz que a sonda e o descodificador da resposta NÃO VIRAM A MESMA MENSAGEM
	// ([ProbeProviderStateFor]): o estado não é de confiança e não tem campos. Não vai no
	// envelope — um estado desalinhado nunca é guardado.
	Misaligned bool `json:"-"`
}

// ProviderStateField é um campo do estado: onde estava, com que nome, e os bytes do valor.
type ProviderStateField struct {
	// Where é o sítio do corpo, num vocabulário fechado (as constantes StateWhere*).
	Where string `json:"where"`
	// Name é a chave do campo — sempre um dos nomes das listas fechadas deste ficheiro, nunca
	// um nome escolhido pelo provider.
	Name string `json:"name"`
	// Raw são os bytes JSON do valor, tal como vieram.
	Raw []byte `json:"raw"`
}

// ProviderStateToolCall é o estado de uma tool call.
type ProviderStateToolCall struct {
	// N é a posição da tool call na resposta, a contar de 1.
	N int `json:"n"`
	// ID são os bytes JSON do `id` que o provider deu (uma string com aspas, um número, …);
	// nil quando não veio ou veio `null`. É carga opaca GUARDADA: nunca se usa como chave nem se
	// cola num pedido — para isso há [ProviderStateToolCall.IDValue].
	ID []byte `json:"id,omitempty"`
	// IDUsable diz que o `id` é uma string JSON cujo valor DESCODIFICADO tem de 1 a
	// [MaxProviderToolCallIDBytes] bytes no alfabeto de [idDoProviderUtilizavel]. É a ÚNICA
	// forma em que o id pode vir a ser posto num pedido ou usado como chave (AOS-515): é
	// escolhido pelo provider ou pelo modelo, e fora dela é só carga opaca guardada.
	IDUsable bool `json:"id_usable,omitempty"`
	// IDValue é o valor descodificado do `id` — o MESMO que [ProviderStateToolCall.IDUsable]
	// julgou —, preenchido só quando é utilizável. Os bytes crus de ID podem escrever o mesmo
	// valor com escapes (`"call\u005f1"` é `call_1`): quem usa o id usa este campo, e nunca os
	// bytes de ID só porque IDUsable é verdadeiro.
	IDValue string `json:"id_value,omitempty"`
	// Fields são os campos de assinatura da chamada, pela ordem em que vieram.
	Fields []ProviderStateField `json:"fields,omitempty"`
}

// Sítios de um [ProviderStateField].
const (
	StateWhereMessage  = "message"
	StateWherePSF      = "message.provider_specific_fields"
	StateWhereToolCall = "tool_call"
	StateWhereFunction = "tool_call.function"
)

// camposDeEstadoDaChamada são as chaves de uma tool call (e da sua `function`) em que um
// provider ou o proxy manda estado por chamada. Lista fechada: `thought_signature` e
// `signature` são a assinatura; `provider_specific_fields` e `extra_content` são os sacos onde
// o proxy a guarda para os fornecedores que a exigem.
var camposDeEstadoDaChamada = map[string]bool{
	"thought_signature": true, "signature": true, "provider_specific_fields": true, "extra_content": true,
}

// MaxProviderToolCallIDBytes é o tecto de um id de tool call do provider para ele ser
// utilizável ([ProviderStateToolCall.IDUsable]). Os ids reais têm dezenas de bytes.
const MaxProviderToolCallIDBytes = 128

// idDoProviderUtilizavel diz se os bytes JSON de um `id` são uma string de 1 a
// [MaxProviderToolCallIDBytes] bytes só com letras e dígitos ASCII e `_ - . :`. Allowlist: os
// formatos conhecidos (`call_…`, `toolu_…`, `functions.<tool>:<n>`, UUID, numérico em string)
// cabem todos, e nada que feche uma linha, abra um cabeçalho ou precise de escape cabe.
//
// Devolve o valor DESCODIFICADO e se é utilizável; o valor só vale com true.
func idDoProviderUtilizavel(raw []byte) (string, bool) {
	if len(raw) < 3 || raw[0] != '"' {
		return "", false
	}
	var id string
	if json.Unmarshal(raw, &id) != nil || id == "" || len(id) > MaxProviderToolCallIDBytes {
		return "", false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_', c == '-', c == '.', c == ':':
		default:
			return "", false
		}
	}
	return id, true
}

// parJSON é uma chave de um objecto JSON e os bytes crus do seu valor.
type parJSON struct {
	chave string
	valor json.RawMessage
}

// paresEmOrdem lê um objecto JSON e devolve as suas chaves pela ordem em que vieram, com os
// bytes de cada valor TAL COMO VIERAM (o [json.RawMessage] copia-os do corpo, sem os tocar).
// Uma chave repetida aparece as vezes que veio. ok é false quando raw não é um objecto.
func paresEmOrdem(raw []byte) (pares []parJSON, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, false
		}
		chave, _ := t.(string)
		var valor json.RawMessage
		if err := dec.Decode(&valor); err != nil {
			return nil, false
		}
		pares = append(pares, parJSON{chave: chave, valor: valor})
	}
	return pares, true
}

// ultimo devolve o valor da ÚLTIMA ocorrência de chave — a que o `encoding/json` lê para um
// campo de struct —, ou nil.
func ultimo(pares []parJSON, chave string) json.RawMessage {
	for i := len(pares) - 1; i >= 0; i-- {
		if pares[i].chave == chave {
			return pares[i].valor
		}
	}
	return nil
}

// eNomeDeRaciocinio diz se chave é um dos campos de raciocínio que o gateway conhece.
func eNomeDeRaciocinio(chave string) bool {
	for _, n := range nomesDoRaciocinio {
		if n.chave == chave {
			return true
		}
	}
	return false
}

// ProbeProviderState tira o estado opaco do corpo de uma resposta de chat. Devolve nil quando
// a resposta não traz estado nenhum — nenhum campo de raciocínio COM conteúdo
// ([RaciocinioComConteudo]) e nenhuma tool call com `id` ou assinatura — ou quando o corpo não
// se lê. Nunca devolve erro e nunca entra em pânico.
//
// Lê a primeira escolha, como o resto do gateway. Quando há estado, guardam-se TODOS os campos
// de raciocínio presentes e não nulos, incluindo os vazios: o que se devolve um dia ao provider
// é o que ele mandou.
func ProbeProviderState(data []byte) (state *ProviderState) {
	defer func() {
		if recover() != nil {
			state = nil
		}
	}()
	topo, ok := paresEmOrdem(data)
	if !ok {
		return nil
	}
	var choices []json.RawMessage
	if json.Unmarshal(ultimo(topo, "choices"), &choices) != nil || len(choices) == 0 {
		return nil
	}
	choice, ok := paresEmOrdem(choices[0])
	if !ok {
		return nil
	}
	msg, ok := paresEmOrdem(ultimo(choice, "message"))
	if !ok {
		return nil
	}
	out := &ProviderState{}
	comConteudo := false
	for _, p := range msg {
		switch {
		case eNomeDeRaciocinio(p.chave):
			if valorPresente(p.valor) {
				out.Fields = append(out.Fields, ProviderStateField{Where: StateWhereMessage, Name: p.chave, Raw: p.valor})
				comConteudo = comConteudo || RaciocinioComConteudo(p.valor)
			}
		case p.chave == "provider_specific_fields":
			dentro, _ := paresEmOrdem(p.valor)
			for _, q := range dentro {
				if eNomeDeRaciocinio(q.chave) && valorPresente(q.valor) {
					out.Fields = append(out.Fields, ProviderStateField{Where: StateWherePSF, Name: q.chave, Raw: q.valor})
					comConteudo = comConteudo || RaciocinioComConteudo(q.valor)
				}
			}
		}
	}
	if calls, temEstado := estadoDasChamadas(ultimo(msg, "tool_calls")); temEstado {
		out.ToolCalls = calls
		comConteudo = true
	}
	if !comConteudo {
		return nil
	}
	return out
}

// ProbeProviderStateFor é a [ProbeProviderState] do corpo de uma resposta JÁ DESCODIFICADA, e
// confere que as duas leituras viram a mesma mensagem.
//
// PORQUE É PRECISO (revisão do AOS-514, achado C1). A sonda lê as chaves com a caixa exacta e
// a ÚLTIMA ocorrência de `message`; o `encoding/json`, que descodifica a resposta, aceita chaves
// noutra caixa (`"TOOL_CALLS"`) e FUNDE objectos `message` repetidos. Num corpo anómalo ou
// hostil as duas leituras discordam — o runtime vê uma tool call que a sonda não viu —, e
// quebrava-se o invariante de que tudo depende: a n-ésima entrada do estado é a n-ésima tool
// call do turno. Em vez de pôr a sonda a imitar as regras do descodificador, falha-se fechado:
// quando não batem, o estado do turno é [ProviderState.Misaligned] — quem o recebe não o guarda.
//
// Confere-se, contra a primeira escolha de resp:
//
//   - o número de tool calls, quando a sonda tem entradas;
//   - que uma tool call com `id` no descodificador tem entradas na sonda;
//   - que o raciocínio que o descodificador leu ([Message.ReasoningContent], só de `message`)
//     corresponde a um campo de raciocínio com conteúdo que a sonda encontrou em `message`.
func ProbeProviderStateFor(data []byte, resp ChatResponse) *ProviderState {
	state := ProbeProviderState(data)
	if len(resp.Choices) == 0 {
		return state
	}
	msg := resp.Choices[0].Message
	var entradas []ProviderStateToolCall
	raciocinioNaSonda := false
	if state != nil {
		entradas = state.ToolCalls
		for _, f := range state.Fields {
			raciocinioNaSonda = raciocinioNaSonda || (f.Where == StateWhereMessage && RaciocinioComConteudo(f.Raw))
		}
	}
	desalinhado := len(entradas) != 0 && len(entradas) != len(msg.ToolCalls)
	for _, tc := range msg.ToolCalls {
		desalinhado = desalinhado || (tc.ID != "" && len(entradas) == 0)
	}
	desalinhado = desalinhado || (msg.ReasoningContent != "" && !raciocinioNaSonda)
	if desalinhado {
		return &ProviderState{Misaligned: true}
	}
	return state
}

// estadoDasChamadas devolve uma entrada por tool call, e se alguma traz `id` ou assinatura.
func estadoDasChamadas(raw json.RawMessage) (out []ProviderStateToolCall, temEstado bool) {
	var calls []json.RawMessage
	if json.Unmarshal(raw, &calls) != nil {
		return nil, false
	}
	for i, c := range calls {
		entrada := ProviderStateToolCall{N: i + 1}
		pares, _ := paresEmOrdem(c)
		if id := ultimo(pares, "id"); valorPresente(id) {
			entrada.ID = id
			entrada.IDValue, entrada.IDUsable = idDoProviderUtilizavel(id)
		}
		for _, p := range pares {
			if camposDeEstadoDaChamada[p.chave] && RaciocinioComConteudo(p.valor) {
				entrada.Fields = append(entrada.Fields, ProviderStateField{Where: StateWhereToolCall, Name: p.chave, Raw: p.valor})
			}
		}
		fn, _ := paresEmOrdem(ultimo(pares, "function"))
		for _, p := range fn {
			if camposDeEstadoDaChamada[p.chave] && RaciocinioComConteudo(p.valor) {
				entrada.Fields = append(entrada.Fields, ProviderStateField{Where: StateWhereFunction, Name: p.chave, Raw: p.valor})
			}
		}
		temEstado = temEstado || entrada.ID != nil || len(entrada.Fields) > 0
		out = append(out, entrada)
	}
	return out, temEstado
}

// ProviderStateEnvelopeVersion é a versão do formato de [ProviderStateEnvelope].
const ProviderStateEnvelopeVersion = 1

// ProviderStateNonceBytes é o tamanho do nonce de um envelope: 256 bits.
const ProviderStateNonceBytes = 32

// ProviderStateEnvelope é a forma em que o estado de um turno atravessa para o runtime e fica
// na captura: o estado, a ROTA a que pertence e um nonce. O runtime trata os bytes do envelope
// ([ProviderStateEnvelope.Marshal]) como carga opaca e refere-os por `sha256`.
//
// A ROTA. RouteProfileDigest é o digest do perfil da rota com que o turno foi comparado (vazio
// com a governação da rota desligada), RequestedModel o nome pedido e ServedModel o modelo que
// serviu. É por eles que quem um dia devolver o estado (AOS-515) sabe a que rota ele pertence:
// o estado de uma rota nunca vai para outra.
//
// O NONCE. 32 bytes aleatórios, escolhidos por quem constrói o envelope. Existe porque o digest
// do envelope vai para o tail, que é enviado ao provider do turno seguinte — e depois de um
// failover esse provider não é o que produziu o estado. Sem nonce, o `sha256` de um raciocínio
// curto confirmava-se por tentativas; com ele, o digest não revela nada do que compromete.
type ProviderStateEnvelope struct {
	Version            int    `json:"v"`
	Nonce              []byte `json:"nonce"`
	RouteProfileDigest string `json:"route_profile_digest,omitempty"`
	RequestedModel     string `json:"requested_model,omitempty"`
	ServedModel        string `json:"served_model,omitempty"`
	ProviderState
}

// Erros de um envelope de estado. Nenhum leva bytes do envelope.
var (
	// ErrProviderStateEnvelope — os bytes não são um envelope desta versão.
	ErrProviderStateEnvelope = errors.New("port: envelope de estado do provider ilegivel ou de versao desconhecida")
	// ErrProviderStateNonce — o envelope não tem um nonce de [ProviderStateNonceBytes] bytes.
	ErrProviderStateNonce = errors.New("port: envelope de estado do provider sem nonce de 32 bytes")
)

// Marshal serializa o envelope. É determinista para o mesmo envelope (ordem de campos fixa
// pelas structs); os valores do estado vão em base64, que é o que guarda os bytes recebidos sem
// os tocar. Recusa um envelope sem a versão corrente ou sem nonce do tamanho certo.
func (e ProviderStateEnvelope) Marshal() ([]byte, error) {
	if e.Version != ProviderStateEnvelopeVersion {
		return nil, ErrProviderStateEnvelope
	}
	if len(e.Nonce) != ProviderStateNonceBytes {
		return nil, ErrProviderStateNonce
	}
	return json.Marshal(e)
}

// UnmarshalProviderStateEnvelope lê um envelope gravado. Os valores voltam byte a byte os que
// [ProbeProviderState] tirou do corpo.
func UnmarshalProviderStateEnvelope(data []byte) (ProviderStateEnvelope, error) {
	var e ProviderStateEnvelope
	if err := json.Unmarshal(data, &e); err != nil {
		return ProviderStateEnvelope{}, ErrProviderStateEnvelope
	}
	if e.Version != ProviderStateEnvelopeVersion {
		return ProviderStateEnvelope{}, fmt.Errorf("%w (versao %d)", ErrProviderStateEnvelope, e.Version)
	}
	if len(e.Nonce) != ProviderStateNonceBytes {
		return ProviderStateEnvelope{}, ErrProviderStateNonce
	}
	return e, nil
}
