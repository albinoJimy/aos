package bancoensaio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/wiretest"
)

// O PROVIDER FALSO DO ESTADO OPACO (AOS-516). Faz de fornecedor com raciocínio assinado, para
// qualificar a devolução do estado sem modelo real. Fala dois wires:
//
//   - o de chat da OpenAI (`…/chat/completions`), no modo `falso`: cada conversa é entregue ao
//     falso EXIGENTE do AOS-515 ([wiretest.Exigente]) — o mesmo dos testes do gateway —, que
//     exige de volta, BYTE A BYTE, o estado que emitiu em cada turno com tools, e o pedido
//     anterior como prefixo exacto;
//   - o de mensagens da Anthropic (`…/v1/messages`), no modo `proxy` com uma rota `anthropic/…`:
//     o proxy traduz nos dois sentidos e re-serializa, por isso a exigência aqui é sobre os
//     VALORES — o texto e a assinatura do bloco de raciocínio e os dados do bloco redigido de
//     cada turno voltam iguais —, e não sobre os bytes.
//
// Com [FalsoDeEstado.Proibe] é o contrário: um provider que responde 400 a qualquer estado.
//
// REGISTA A FORMA, NUNCA O CONTEÚDO ([FormaNoFornecedor]): que chaves e que tipos de bloco traz
// cada mensagem `assistant` com tool calls que lhe volta, e o que do estado voltou intacto.
//
// O QUE NÃO PROVA: não valida assinaturas criptográficas, e não sabe o que o fornecedor real
// faz a um pedido que este falso aceita. Isso só a corrida com o modelo real mede.
type FalsoDeEstado struct {
	// Proibe faz do falso um provider que recusa qualquer estado no pedido.
	Proibe bool
	// Turnos é o número de turnos com tool call antes do texto final. Zero ⇒ 2.
	Turnos int
	// ExigeID exige de volta o id de tool call que o falso emitiu (só no wire de chat).
	ExigeID bool
	// ServidoComo, quando não vazio, faz o falso declarar-se como o proxy se declara: o nome vai
	// no cabeçalho do modelo servido ([port.HeaderServedModel]). É para o modo `falso`, que não
	// tem proxy; atrás do proxy real fica vazio, e quem declara é o proxy.
	ServidoComo string

	chaveEsperada [sha256.Size]byte
	exigeChave    bool

	mu       sync.Mutex
	conversa *wiretest.Exigente
	forma    FormaNoFornecedor
}

// Os modos do provider falso do estado, como se escrevem em `--estado`.
const (
	// EstadoExige — o falso exige de volta o estado que emitiu.
	EstadoExige = "exige"
	// EstadoProibe — o falso recusa qualquer estado no pedido.
	EstadoProibe = "proibe"
)

// hostAceite diz se h tem a forma de um nome de host: letras minúsculas, dígitos, `.`, `-` e,
// para a porta, `:` — sem esquema, sem caminho e sem credenciais.
func hostAceite(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == ':') {
			return false
		}
	}
	return true
}

// LerFormaDoFalso lê, por GET, a forma que um provider falso do estado registou (modo `proxy`:
// o falso corre num contentor, e a sua porta está publicada só em 127.0.0.1).
func LerFormaDoFalso(ctx context.Context, endereco string) (*FormaNoFornecedor, error) {
	ctx, cancelar := context.WithTimeout(ctx, 15*time.Second)
	defer cancelar()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endereco, "/")+CaminhoDaForma, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("banco-ensaio: o provider falso do estado respondeu %d ao pedido da forma", resp.StatusCode)
	}
	var f FormaNoFornecedor
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&f); err != nil {
		return nil, err
	}
	return &f, nil
}

// Os wires do falso do estado, como vão em [FormaNoFornecedor.Wire].
const (
	WireDeChat      = modelgateway.WireOpenAIChat
	WireDeMensagens = "anthropic-messages"
)

// As causas de um 400 do falso do estado ([FormaNoFornecedor.Recusas]). Vocabulário fechado.
const (
	// RecusaEstadoEmFalta — o falso exige o estado e o de um turno não veio.
	RecusaEstadoEmFalta = "estado_em_falta"
	// RecusaEstadoAlterado — o estado veio e não é o emitido (ou, no wire de chat, o pedido
	// anterior deixou de ser um prefixo exacto deste).
	RecusaEstadoAlterado = "estado_alterado_ou_prefixo_mudado"
	// RecusaEstadoPresente — o falso proíbe estado e o pedido trazia-o.
	RecusaEstadoPresente = "estado_presente"
	// RecusaPedidoIlegivel — o corpo não é JSON de um pedido.
	RecusaPedidoIlegivel = "pedido_ilegivel"
)

// FormaNoFornecedor é a FORMA com que os pedidos chegaram ao provider falso do estado: nomes de
// chaves, tipos de bloco e contagens. Nenhum valor de nenhum campo.
type FormaNoFornecedor struct {
	// Wire é o wire dos pedidos: [WireDeChat] ou [WireDeMensagens].
	Wire    string `json:"wire"`
	Pedidos int    `json:"pedidos"`
	// Turnos é o número de mensagens `assistant` com tool calls que o falso recebeu de volta,
	// somadas por todos os pedidos (um turno repete-se em cada pedido seguinte).
	Turnos int `json:"turnos_com_tool_calls_recebidos"`
	// Assistant conta essas mensagens pela sua forma. No wire de chat: `content=<nulo|vazio|
	// texto>` e as chaves da mensagem, por ordem alfabética. No de mensagens: a sequência dos
	// tipos dos blocos, pela ordem em que chegaram; um bloco de texto leva a sua classe —
	// `text(vazio)`, `text(aviso_do_proxy)` (começa por `[System:`) ou `text`.
	Assistant map[string]int `json:"forma_do_assistant"`
	// Estado conta, por campo, o que do estado voltou nessas mensagens.
	Estado map[string]int `json:"estado_que_voltou"`
	// Parametros conta os pedidos que traziam cada parâmetro de raciocínio no topo.
	Parametros map[string]int `json:"parametros_do_pedido"`
	// Recusas conta as respostas 400 do falso, por causa.
	Recusas map[string]int `json:"respostas_400_por_causa"`
}

// ExigirChave faz o falso recusar com 401 os pedidos cuja chave não tenha este sha256. A chave
// lê-se de `Authorization: Bearer …` (wire de chat) ou de `x-api-key` (wire de mensagens).
func (f *FalsoDeEstado) ExigirChave(sha [sha256.Size]byte) {
	f.chaveEsperada, f.exigeChave = sha, true
}

// Forma devolve uma cópia do que o falso registou.
func (f *FalsoDeEstado) Forma() *FormaNoFornecedor {
	f.mu.Lock()
	defer f.mu.Unlock()
	copia := func(m map[string]int) map[string]int {
		out := make(map[string]int, len(m))
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	return &FormaNoFornecedor{
		Wire: f.forma.Wire, Pedidos: f.forma.Pedidos, Turnos: f.forma.Turnos,
		Assistant: copia(f.forma.Assistant), Estado: copia(f.forma.Estado),
		Parametros: copia(f.forma.Parametros), Recusas: copia(f.forma.Recusas),
	}
}

func (f *FalsoDeEstado) contar(m *map[string]int, chave string) {
	if *m == nil {
		*m = map[string]int{}
	}
	(*m)[chave]++
}

func (f *FalsoDeEstado) turnos() int {
	if f.Turnos <= 0 {
		return 2
	}
	return f.Turnos
}

// parametrosDeRaciocinio são as chaves do topo de um pedido que o falso conta.
var parametrosDeRaciocinio = []string{"thinking", "reasoning_effort", "max_tokens", "max_completion_tokens", "output_config"}

// CaminhoDaForma é o caminho em que o falso do estado devolve, por GET, o que registou.
const CaminhoDaForma = "/forma"

// ServeHTTP implementa [http.Handler].
func (f *FalsoDeEstado) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet && r.URL.Path == CaminhoDaForma {
		// Só formas e contagens: não exige chave, e não tem nada que a merecesse.
		_ = json.NewEncoder(w).Encode(f.Forma())
		return
	}
	corpo, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if f.exigeChave {
		chave := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if chave == "" {
			chave = r.Header.Get("x-api-key")
		}
		soma := sha256.Sum256([]byte(chave))
		if subtle.ConstantTimeCompare(soma[:], f.chaveEsperada[:]) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"chave recusada pelo provider falso","type":"authentication_error"}}`))
			return
		}
	}
	if f.ServidoComo != "" {
		w.Header().Set(port.HeaderServedModel, f.ServidoComo)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forma.Pedidos++
	var topo map[string]json.RawMessage
	if json.Unmarshal(corpo, &topo) != nil {
		f.recusar(w, RecusaPedidoIlegivel, false)
		return
	}
	for _, p := range parametrosDeRaciocinio {
		if _, tem := topo[p]; tem {
			f.contar(&f.forma.Parametros, p)
		}
	}
	var msgs []map[string]json.RawMessage
	_ = json.Unmarshal(topo["messages"], &msgs)
	if strings.HasSuffix(r.URL.Path, "/messages") {
		f.forma.Wire = WireDeMensagens
		f.servirMensagens(w, corpo, topo, msgs)
		return
	}
	f.forma.Wire = WireDeChat
	f.servirChat(w, r, corpo, msgs)
}

// recusar responde 400 e conta a causa. Na forma do erro de cada wire.
func (f *FalsoDeEstado) recusar(w http.ResponseWriter, causa string, mensagens bool) {
	f.contar(&f.forma.Recusas, causa)
	w.WriteHeader(http.StatusBadRequest)
	if mensagens {
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"provider falso do estado: ` + causa + `"}}`))
		return
	}
	_, _ = w.Write([]byte(`{"error":{"message":"provider falso do estado: ` + causa + `","type":"invalid_request_error"}}`))
}

// chamadaDoFalso escolhe a tool call que o falso emite: a primeira tool oferecida. Uma tool com
// o parâmetro `path` é pedida com o primeiro nome de documento que o pedido menciona; outra,
// com um texto fixo. Sem tools oferecidas devolve o nome vazio.
func chamadaDoFalso(corpo []byte, nome string, propriedades map[string]json.RawMessage) (string, string) {
	if nome == "" {
		return "", ""
	}
	if _, tem := propriedades["path"]; tem {
		doc := nomeDeDocumento.FindString(string(corpo))
		if doc == "" {
			doc = "documento-nao-nomeado.txt"
		}
		args, _ := json.Marshal(map[string]string{"path": doc})
		return nome, string(args)
	}
	return nome, `{"texto":"pedido do provider falso do estado"}`
}

// servirChat serve um pedido no wire de chat: regista a forma e entrega a conversa ao falso
// exigente do AOS-515.
func (f *FalsoDeEstado) servirChat(w http.ResponseWriter, r *http.Request, corpo []byte, msgs []map[string]json.RawMessage) {
	nova, emFalta, presente := true, false, false
	for _, m := range msgs {
		var papel string
		_ = json.Unmarshal(m["role"], &papel)
		if papel != "assistant" {
			continue
		}
		nova = false
		var chamadas []map[string]json.RawMessage
		_ = json.Unmarshal(m["tool_calls"], &chamadas)
		if len(chamadas) == 0 {
			continue
		}
		f.forma.Turnos++
		chaves := make([]string, 0, len(m))
		for k := range m {
			chaves = append(chaves, k)
		}
		sort.Strings(chaves)
		conteudo := "texto"
		switch string(m["content"]) {
		case "null", "":
			conteudo = "nulo"
		case `""`:
			conteudo = "vazio"
		}
		f.contar(&f.forma.Assistant, "content="+conteudo+" chaves="+strings.Join(chaves, ","))
		for _, campo := range []string{"thinking_blocks", "reasoning_content"} {
			if _, tem := m[campo]; tem {
				f.contar(&f.forma.Estado, campo)
				presente = true
			} else {
				emFalta = true
			}
		}
		for _, c := range chamadas {
			if _, tem := c["thought_signature"]; tem {
				f.contar(&f.forma.Estado, "tool_call.thought_signature")
				presente = true
			} else {
				emFalta = true
			}
			var id string
			_ = json.Unmarshal(c["id"], &id)
			if strings.HasPrefix(id, strings.TrimSuffix(wiretest.IDEmitidoNoTurno(0), "00")) {
				f.contar(&f.forma.Estado, "tool_call.id_do_provider")
			} else {
				f.contar(&f.forma.Estado, "tool_call.id_do_runtime")
			}
		}
	}
	var pedido pedidoAoFalso
	_ = json.Unmarshal(corpo, &pedido)
	if len(pedido.Tools) == 0 {
		// Um nó sem tools: não há estado a exigir. Responde em texto, como o falso de sempre.
		_, _ = w.Write(wiretest.Corpo("content_texto"))
		return
	}
	if nova || f.conversa == nil {
		tool, args := chamadaDoFalso(corpo, pedido.Tools[0].Function.Name, pedido.Tools[0].Function.Parameters.Properties)
		f.conversa = &wiretest.Exigente{Turnos: f.turnos(), ExigeID: f.ExigeID, Proibe: f.Proibe, Tool: tool, Argumentos: args}
	}
	// A resposta do exigente passa por um gravador: é pelo código que se conta a recusa, e a
	// causa é a que o banco viu no pedido — o texto do erro do exigente não é lido.
	gravador := &respostaGravada{cabecalho: http.Header{}}
	copia := r.Clone(r.Context())
	copia.Body = io.NopCloser(bytes.NewReader(corpo))
	f.conversa.ServeHTTP(gravador, copia)
	if gravador.codigo == http.StatusBadRequest {
		causa := RecusaEstadoAlterado
		switch {
		case f.Proibe && presente:
			causa = RecusaEstadoPresente
		case !f.Proibe && emFalta:
			causa = RecusaEstadoEmFalta
		}
		f.contar(&f.forma.Recusas, causa)
	}
	if gravador.codigo != 0 {
		w.WriteHeader(gravador.codigo)
	}
	_, _ = w.Write(gravador.corpo)
}

// respostaGravada é um [http.ResponseWriter] que guarda o código e o corpo.
type respostaGravada struct {
	cabecalho http.Header
	codigo    int
	corpo     []byte
}

func (g *respostaGravada) Header() http.Header { return g.cabecalho }
func (g *respostaGravada) WriteHeader(c int)   { g.codigo = c }
func (g *respostaGravada) Write(b []byte) (int, error) {
	g.corpo = append(g.corpo, b...)
	return len(b), nil
}

// O que o falso emite em cada turno no wire de mensagens. São sentinelas: fora do estado opaco
// de um pedido à rota que as produziu, não podem aparecer em lado nenhum.
func pensamentoDoTurno(n int) string { return fmt.Sprintf("S-PENSA-%d <b> & e", n) }
func assinaturaDoTurno(n int) string { return fmt.Sprintf("U0lH-%d+/==", n) }
func redigidoDoTurno(n int) string   { return fmt.Sprintf("UkVE-%d", n) }

// SentinelasDoEstado são os textos que os falsos do estado emitem como raciocínio, assinaturas e
// blocos redigidos. Os testes de fuga procuram-nos em tudo o que o banco escreve.
func SentinelasDoEstado() []string {
	return []string{"S-PENSA", "S-RACIOCINIO", "S-ASSINATURA", "U0lH", "UkVE", "VkFaSU8"}
}

// servirMensagens serve um pedido no wire de mensagens da Anthropic.
func (f *FalsoDeEstado) servirMensagens(w http.ResponseWriter, corpo []byte, topo map[string]json.RawMessage, msgs []map[string]json.RawMessage) {
	turnos, emFalta, alterado, presente := 0, false, false, false
	for _, m := range msgs {
		var papel string
		_ = json.Unmarshal(m["role"], &papel)
		var blocos []map[string]json.RawMessage
		if papel != "assistant" || json.Unmarshal(m["content"], &blocos) != nil {
			continue
		}
		var tipos []string
		temChamada, pensou, redigiu := false, false, false
		for _, b := range blocos {
			var tipo, texto, assinatura, dados, id string
			_ = json.Unmarshal(b["type"], &tipo)
			_ = json.Unmarshal(b["text"], &texto)
			if tipo == "thinking" {
				_ = json.Unmarshal(b["thinking"], &texto)
			}
			_ = json.Unmarshal(b["signature"], &assinatura)
			_ = json.Unmarshal(b["data"], &dados)
			_ = json.Unmarshal(b["id"], &id)
			switch tipo {
			case "thinking":
				presente = true
				if texto == pensamentoDoTurno(turnos) && assinatura == assinaturaDoTurno(turnos) {
					pensou = true
					f.contar(&f.forma.Estado, "thinking.intacto")
				} else {
					alterado = true
					f.contar(&f.forma.Estado, "thinking.alterado")
				}
			case "redacted_thinking":
				presente = true
				if dados == redigidoDoTurno(turnos) {
					redigiu = true
					f.contar(&f.forma.Estado, "redacted_thinking.intacto")
				} else {
					alterado = true
					f.contar(&f.forma.Estado, "redacted_thinking.alterado")
				}
			case "text":
				switch {
				case texto == "":
					tipo = "text(vazio)"
				case strings.HasPrefix(texto, "[System:"):
					tipo = "text(aviso_do_proxy)"
				}
			case "tool_use":
				temChamada = true
				if strings.HasPrefix(id, "toolu_Banco") {
					f.contar(&f.forma.Estado, "tool_use.id_do_provider")
				} else {
					f.contar(&f.forma.Estado, "tool_use.id_do_runtime")
				}
			default:
				tipo = "outro"
			}
			tipos = append(tipos, tipo)
		}
		if !temChamada {
			continue
		}
		f.forma.Turnos++
		f.contar(&f.forma.Assistant, strings.Join(tipos, ","))
		if !pensou || !redigiu {
			emFalta = true
		}
		turnos++
	}
	switch {
	case f.Proibe && presente:
		f.recusar(w, RecusaEstadoPresente, true)
		return
	case !f.Proibe && alterado:
		f.recusar(w, RecusaEstadoAlterado, true)
		return
	case !f.Proibe && emFalta:
		f.recusar(w, RecusaEstadoEmFalta, true)
		return
	}
	var tools []struct {
		Name   string `json:"name"`
		Schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"input_schema"`
	}
	_ = json.Unmarshal(topo["tools"], &tools)
	entrada := int64(len(corpo)/4 + 1)
	if len(tools) == 0 || turnos >= f.turnos() {
		cru, _ := json.Marshal(map[string]any{
			"id": "msg_banco", "type": "message", "role": "assistant", "model": "modelo-falso-do-banco",
			"content":     []any{map[string]any{"type": "text", "text": SentinelaDeTexto + " resposta final do provider falso do estado"}},
			"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": entrada, "output_tokens": 12},
		})
		_, _ = w.Write(cru)
		return
	}
	tool, args := chamadaDoFalso(corpo, tools[0].Name, tools[0].Schema.Properties)
	cru, _ := json.Marshal(map[string]any{
		"id": "msg_banco", "type": "message", "role": "assistant", "model": "modelo-falso-do-banco",
		"content": []any{
			map[string]any{"type": "thinking", "thinking": pensamentoDoTurno(turnos), "signature": assinaturaDoTurno(turnos)},
			map[string]any{"type": "redacted_thinking", "data": redigidoDoTurno(turnos)},
			map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_Banco%02d", turnos), "name": tool, "input": json.RawMessage(args)},
		},
		"stop_reason": "tool_use", "usage": map[string]any{"input_tokens": entrada, "output_tokens": 16},
	})
	_, _ = w.Write(cru)
}
