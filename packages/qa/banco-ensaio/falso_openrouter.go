package bancoensaio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
)

// A FORMA OPENROUTER DO PROVIDER FALSO DO ESTADO (AOS-516, decisão do dono de 2026-10-10).
//
// A OpenRouter fala o wire de chat da OpenAI e expõe o raciocínio em dois campos de `message`:
// `reasoning` (texto) e `reasoning_details` (uma lista de blocos; nos modelos da Anthropic o
// bloco de texto traz `signature`, e o redigido traz `data`). Para continuar uma conversa com
// tools, os `reasoning_details` têm de voltar INALTERADOS na mensagem `assistant` do turno.
//
// Com [FalsoDeEstado.FormaDoEstado] = [FormaOpenRouter] o falso emite essa forma e exige-a de volta:
// os `reasoning_details` de cada turno com tool calls têm de vir no TOPO da mensagem e com os
// mesmos VALORES. A exigência é sobre valores e não sobre bytes porque há um proxy no meio que
// re-serializa; os bytes iguais contam-se à parte (`reasoning_details.bytes_iguais`). O texto
// de `reasoning` não é exigido — o que a OpenRouter pede de volta são os detalhes —, só se
// conta se veio.
//
// O QUE NÃO PROVA: é a forma DOCUMENTADA da OpenRouter, escrita à mão, e não uma resposta
// gravada dela. Não valida assinaturas. O que a OpenRouter faz a um pedido que este falso
// aceita só a corrida com o modelo real mede.

// FormaOpenRouter é o valor de [FalsoDeEstado.FormaDoEstado] (e de `--forma-do-falso`) para esta forma.
const FormaOpenRouter = "openrouter"

// subchavesDeRaciocinio é a lista FECHADA das chaves que o falso nomeia dentro dos parâmetros
// `reasoning` e `thinking` de um pedido ([FormaNoFornecedor.Parametros]).
var subchavesDeRaciocinio = []string{"effort", "max_tokens", "exclude", "enabled", "type", "budget_tokens"}

// cabecalhosDoAdaptador são os cabeçalhos que o adaptador `openrouter` do proxy acrescenta por
// conta própria; o falso conta os pedidos que os trazem (o valor não vai).
var cabecalhosDoAdaptador = []string{"HTTP-Referer", "X-Title"}

// contarParametros regista, por NOME, os parâmetros de raciocínio de um pedido: os do topo, as
// subchaves de `reasoning` e de `thinking`, e os cabeçalhos do adaptador. Nenhum valor.
func (f *FalsoDeEstado) contarParametros(r *http.Request, topo map[string]json.RawMessage) {
	for _, p := range parametrosDeRaciocinio {
		cru, tem := topo[p]
		if !tem {
			continue
		}
		f.contar(&f.forma.Parametros, p)
		if p != "reasoning" && p != "thinking" {
			continue
		}
		var dentro map[string]json.RawMessage
		_ = json.Unmarshal(cru, &dentro)
		for _, s := range subchavesDeRaciocinio {
			if _, tem := dentro[s]; tem {
				f.contar(&f.forma.Parametros, p+"."+s)
			}
		}
	}
	for _, c := range cabecalhosDoAdaptador {
		if r.Header.Get(c) != "" {
			f.contar(&f.forma.Parametros, "cabecalho:"+strings.ToLower(c))
		}
	}
}

// detalhesDoTurno devolve os bytes dos `reasoning_details` que o falso emite no turno n: um
// bloco de texto assinado e um bloco redigido, na forma dos modelos da Anthropic na OpenRouter.
func detalhesDoTurno(n int) []byte {
	type bloco struct {
		Type      string `json:"type"`
		Text      string `json:"text,omitempty"`
		Signature string `json:"signature,omitempty"`
		Data      string `json:"data,omitempty"`
		ID        string `json:"id"`
		Format    string `json:"format"`
		Index     int    `json:"index"`
	}
	cru, _ := json.Marshal([]bloco{
		{Type: "reasoning.text", Text: pensamentoDoTurno(n), Signature: assinaturaDoTurno(n), ID: fmt.Sprintf("reasoning-text-%d", n), Format: "anthropic-claude-v1", Index: 0},
		{Type: "reasoning.encrypted", Data: redigidoDoTurno(n), ID: fmt.Sprintf("reasoning-encrypted-%d", n), Format: "anthropic-claude-v1", Index: 1},
	})
	return cru
}

// mesmosValores diz se dois documentos JSON têm os mesmos valores (ordem das chaves e espaços
// não contam).
func mesmosValores(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

// idDoTurnoOpenRouter é o id que o falso dá à tool call do turno n nesta forma.
func idDoTurnoOpenRouter(n int) string { return fmt.Sprintf("toolu_Banco%02d", n) }

// PrefixoDoPedidoDeErro abre o texto do único pedido que faz o falso responder com um ERRO na
// forma da OpenRouter: `erro-da-openrouter-<código>`. Serve para medir como esse erro chega ao
// banco depois de atravessar o proxy. Nenhum caso da bateria tem este texto.
const PrefixoDoPedidoDeErro = "erro-da-openrouter-"

// errosDaOpenRouter são os corpos de erro que o falso sabe emitir, por código HTTP. A OpenRouter
// não manda `error.type`: manda o código como número e uma mensagem. São a forma DOCUMENTADA,
// escrita à mão; as mensagens não trazem nada de conta nenhuma.
var errosDaOpenRouter = map[string]struct {
	codigo int
	corpo  string
}{
	"400": {http.StatusBadRequest, `{"error":{"message":"autor/modelo-inexistente is not a valid model ID","code":400},"user_id":"utilizador-do-falso"}`},
	"401": {http.StatusUnauthorized, `{"error":{"message":"No auth credentials found","code":401}}`},
	"402": {http.StatusPaymentRequired, `{"error":{"message":"Insufficient credits. Add more using the credits page","code":402},"user_id":"utilizador-do-falso"}`},
	"404": {http.StatusNotFound, `{"error":{"message":"No endpoints found for autor/modelo-inexistente.","code":404},"user_id":"utilizador-do-falso"}`},
	"429": {http.StatusTooManyRequests, `{"error":{"message":"Rate limit exceeded: limit_rpm/autor/modelo","code":429,"metadata":{"headers":{"X-RateLimit-Limit":"20"}}},"user_id":"utilizador-do-falso"}`},
}

// erroPedido devolve o erro da OpenRouter que o pedido pede, se pedir algum.
func erroPedido(msgs []map[string]json.RawMessage) (int, string, bool) {
	if len(msgs) != 1 {
		return 0, "", false
	}
	var texto string
	_ = json.Unmarshal(msgs[0]["content"], &texto)
	codigo, pede := strings.CutPrefix(texto, PrefixoDoPedidoDeErro)
	e, existe := errosDaOpenRouter[codigo]
	return e.codigo, e.corpo, pede && existe
}

// servirOpenRouter serve um pedido no wire de chat, na forma da OpenRouter.
func (f *FalsoDeEstado) servirOpenRouter(w http.ResponseWriter, corpo []byte, topo map[string]json.RawMessage, msgs []map[string]json.RawMessage) {
	if codigo, erro, pede := erroPedido(msgs); pede {
		w.WriteHeader(codigo)
		_, _ = w.Write([]byte(erro))
		return
	}
	turnos, emFalta, alterado, presente := 0, false, false, false
	for _, m := range msgs {
		var papel string
		_ = json.Unmarshal(m["role"], &papel)
		var chamadas []map[string]json.RawMessage
		_ = json.Unmarshal(m["tool_calls"], &chamadas)
		if papel != "assistant" || len(chamadas) == 0 {
			continue
		}
		f.forma.Turnos++
		chaves, outra := make([]string, 0, len(m)), false
		for k := range m {
			if chavesDoAssistant[k] {
				chaves = append(chaves, k)
			} else {
				outra = true
			}
		}
		if outra {
			chaves = append(chaves, "outra")
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

		// O que conta é o que vem no TOPO da mensagem: é aí que a OpenRouter o lê.
		esperado := detalhesDoTurno(turnos)
		switch detalhes, tem := m["reasoning_details"]; {
		case !tem || string(detalhes) == "null":
			emFalta = true
			f.contar(&f.forma.Estado, "reasoning_details.em_falta")
		case mesmosValores(detalhes, esperado):
			presente = true
			f.contar(&f.forma.Estado, "reasoning_details.intacto")
			if bytes.Equal(detalhes, esperado) {
				f.contar(&f.forma.Estado, "reasoning_details.bytes_iguais")
			}
		default:
			presente, alterado = true, true
			f.contar(&f.forma.Estado, "reasoning_details.alterado")
		}
		for _, campo := range []string{"reasoning", "reasoning_content", "thinking_blocks"} {
			if cru, tem := m[campo]; tem && string(cru) != "null" {
				presente = true
				f.contar(&f.forma.Estado, campo+".presente")
			}
		}
		// O saco do proxy: se o estado só vier aqui, a OpenRouter não o lê. Conta-se à parte.
		var saco map[string]json.RawMessage
		_ = json.Unmarshal(m["provider_specific_fields"], &saco)
		for _, campo := range []string{"reasoning_details", "reasoning", "reasoning_content", "thinking_blocks"} {
			if _, tem := saco[campo]; tem {
				presente = true
				f.contar(&f.forma.Estado, "provider_specific_fields."+campo)
			}
		}
		for _, c := range chamadas {
			var id string
			_ = json.Unmarshal(c["id"], &id)
			if id == idDoTurnoOpenRouter(turnos) {
				f.contar(&f.forma.Estado, "tool_call.id_do_provider")
			} else {
				f.contar(&f.forma.Estado, "tool_call.id_do_runtime")
				alterado = alterado || f.ExigeID
			}
		}
		turnos++
	}
	switch {
	case f.Proibe && presente:
		f.recusar(w, RecusaEstadoPresente, false)
		return
	case !f.Proibe && alterado:
		f.recusar(w, RecusaEstadoAlterado, false)
		return
	case !f.Proibe && emFalta:
		f.recusar(w, RecusaEstadoEmFalta, false)
		return
	}
	var pedido pedidoAoFalso
	_ = json.Unmarshal(corpo, &pedido)
	var modelo string
	_ = json.Unmarshal(topo["model"], &modelo)
	if !nomeDeModeloAceite(modelo) {
		modelo = "modelo-falso-do-banco"
	}
	uso := map[string]any{"prompt_tokens": int64(len(corpo)/4 + 1), "completion_tokens": 16, "total_tokens": int64(len(corpo)/4 + 17)}
	resposta := func(motivo string, mensagem any) {
		cru, _ := json.Marshal(map[string]any{
			"id": "gen-banco", "object": "chat.completion", "model": modelo, "usage": uso,
			"choices": []any{map[string]any{"index": 0, "finish_reason": motivo, "message": mensagem}},
		})
		_, _ = w.Write(cru)
	}
	if len(pedido.Tools) == 0 || turnos >= f.turnos() {
		resposta("stop", map[string]any{"role": "assistant", "content": SentinelaDeTexto + " resposta final do provider falso do estado"})
		return
	}
	tool, args := chamadaDoFalso(corpo, pedido.Tools[0].Function.Name, pedido.Tools[0].Function.Parameters.Properties)
	mensagem := map[string]any{
		"role": "assistant", "content": nil,
		"reasoning": pensamentoDoTurno(turnos), "reasoning_details": json.RawMessage(detalhesDoTurno(turnos)),
		"tool_calls": []any{map[string]any{
			"id": idDoTurnoOpenRouter(turnos), "type": "function", "index": 0,
			"function": map[string]any{"name": tool, "arguments": args},
		}},
	}
	resposta("tool_calls", mensagem)
}
