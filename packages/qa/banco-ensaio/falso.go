package bancoensaio

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/aos-ref/platform/model-gateway/wiretest"
)

// O PROVIDER FALSO DO BANCO. Faz de fornecedor nos modos `falso` (directo, em CI) e `proxy`
// (atrás da imagem real do proxy). É determinista e não usa relógio nem rede.
//
// # COMO DECIDE
//
// Cada CONVERSA (um run: começa no pedido que ainda não tem nenhuma mensagem `assistant`) recebe
// um COMPORTAMENTO, tirado de um ROTEIRO pela ordem de chegada — a conversa n fica com o
// comportamento `roteiro[n mod len]`. Dentro da conversa a resposta só depende do comportamento
// e do pedido: se há tools oferecidas, e se o pedido já traz mensagens `tool`.
//
// É SEQUENCIAL por desenho: o banco corre um run de cada vez, e é a ordem de chegada que torna
// as taxas esperadas exactas.
//
// # O QUE REUTILIZA DO AOS-508
//
// As formas anormais da resposta — vazia com o raciocínio noutro campo, cortada, texto sem
// factos — são os corpos dos casos de wire do AOS-508 ([wiretest.Corpo]), servidos byte a
// byte. As tool calls e o texto final com os factos são construídos aqui, porque dependem do
// pedido (o nome do documento, o que a tool devolveu).

// Comportamento é o que o falso faz numa conversa. Vocabulário fechado.
type Comportamento string

const (
	// ComportamentoCumpre — com tools: pede-as pelo mecanismo nativo e, com os resultados,
	// responde com o que elas devolveram. Sem tools: responde com o que recebeu.
	ComportamentoCumpre Comportamento = "cumpre"
	// ComportamentoTexto — com tools: NÃO faz tool call nenhuma e escreve a chamada como texto,
	// com motivo `stop`. Sem tools comporta-se como [ComportamentoCumpre].
	ComportamentoTexto Comportamento = "texto"
	// ComportamentoVazia — no turno final devolve `content` vazio com o raciocínio em
	// `reasoning_content` (o caso de wire `rac_reasoning_content_content_vazio`, a forma H1).
	ComportamentoVazia Comportamento = "vazia"
	// ComportamentoCortada — no turno final devolve uma resposta cortada pelo limite de tokens
	// (o caso de wire `finish_length`).
	ComportamentoCortada Comportamento = "cortada"
	// ComportamentoSemFactos — no turno final devolve um texto que não tem os factos do
	// documento (o caso de wire `content_texto`).
	ComportamentoSemFactos Comportamento = "sem_factos"
	// ComportamentoErro500 — responde 500 a todos os pedidos da conversa.
	ComportamentoErro500 Comportamento = "erro_500"
	// ComportamentoErro401 — responde 401 a todos os pedidos da conversa: a chave recusada.
	ComportamentoErro401 Comportamento = "erro_401"
	// ComportamentoRejeitaSegundo — pede as tools e responde 400 ao pedido que as devolve: o
	// provider que não aceita o segundo turno.
	ComportamentoRejeitaSegundo Comportamento = "rejeita_segundo"
)

// Comportamentos devolve o vocabulário, numa ordem fixa.
func Comportamentos() []Comportamento {
	return []Comportamento{
		ComportamentoCumpre, ComportamentoTexto, ComportamentoVazia, ComportamentoCortada,
		ComportamentoSemFactos, ComportamentoErro500, ComportamentoErro401, ComportamentoRejeitaSegundo,
	}
}

// RoteiroPorOmissao é o roteiro do falso nos modos `falso` e `proxy`: dez conversas, com uma de
// cada comportamento anormal. As taxas que ele dá sobre a bateria estão presas por teste.
func RoteiroPorOmissao() []Comportamento {
	return []Comportamento{
		ComportamentoCumpre, ComportamentoCumpre, ComportamentoTexto, ComportamentoCumpre,
		ComportamentoVazia, ComportamentoCumpre, ComportamentoCortada, ComportamentoSemFactos,
		ComportamentoErro500, ComportamentoRejeitaSegundo,
	}
}

// LerRoteiro lê um roteiro escrito como `cumpre,texto,…`. Um nome fora do vocabulário é erro.
func LerRoteiro(s string) ([]Comportamento, error) {
	var out []Comportamento
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		achado := false
		for _, c := range Comportamentos() {
			if string(c) == p {
				achado = true
			}
		}
		if !achado {
			return nil, fmt.Errorf("banco-ensaio: comportamento desconhecido no roteiro: %q", p)
		}
		out = append(out, Comportamento(p))
	}
	return out, nil
}

// EscreverRoteiro é o inverso de [LerRoteiro].
func EscreverRoteiro(r []Comportamento) string {
	partes := make([]string, len(r))
	for i, c := range r {
		partes[i] = string(c)
	}
	return strings.Join(partes, ",")
}

// SentinelaDeTexto abre todo o texto que o falso ESCREVE numa resposta. Existe para os testes
// varrerem o que o banco produz: se aparecer num relatório, num log ou num erro, o texto de uma
// resposta saiu de onde não devia.
const SentinelaDeTexto = "SENTINELA-TEXTO-DE-RESPOSTA-AOS512"

// SentinelasDosCasosDeWire são os textos que os casos de wire servidos pelo falso trazem (o
// `content` e o raciocínio). Os testes de fuga procuram-nos ao lado da [SentinelaDeTexto].
func SentinelasDosCasosDeWire() []string {
	return []string{"resposta final", "resposta cort", "primeiro penso; depois concluo"}
}

// ProviderFalso é o provider falso do banco. Implementa [http.Handler].
type ProviderFalso struct {
	roteiro []Comportamento
	// chaveEsperada, quando não vazia, é o sha256 do valor `Bearer …` que o falso exige no
	// cabeçalho de autorização: um pedido sem ele leva 401. É assim que o modo `proxy` prova
	// que a chave do fornecedor chegou ao fornecedor.
	chaveEsperada [sha256.Size]byte
	exigeChave    bool

	mu        sync.Mutex
	conversas int
	actual    Comportamento
	pedidos   int
	corpos    [][]byte
	guardar   bool
}

// NovoProviderFalso devolve um falso com o roteiro dado. Roteiro vazio ⇒ [RoteiroPorOmissao].
func NovoProviderFalso(roteiro []Comportamento) *ProviderFalso {
	if len(roteiro) == 0 {
		roteiro = RoteiroPorOmissao()
	}
	return &ProviderFalso{roteiro: append([]Comportamento(nil), roteiro...), actual: roteiro[0]}
}

// ExigirChave faz o falso recusar com 401 os pedidos cuja chave (o que vem depois de `Bearer `)
// não tenha este sha256.
func (f *ProviderFalso) ExigirChave(sha [sha256.Size]byte) {
	f.chaveEsperada, f.exigeChave = sha, true
}

// GuardarCorpos faz o falso guardar o corpo de cada pedido, para os testes os lerem.
func (f *ProviderFalso) GuardarCorpos() { f.guardar = true }

// Pedidos devolve quantos pedidos o falso recebeu.
func (f *ProviderFalso) Pedidos() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pedidos
}

// Corpos devolve uma cópia dos corpos recebidos (só com [ProviderFalso.GuardarCorpos]).
func (f *ProviderFalso) Corpos() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.corpos...)
}

type mensagemDoPedido struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type pedidoAoFalso struct {
	Messages []mensagemDoPedido `json:"messages"`
	Tools    []struct {
		Function struct {
			Name       string `json:"name"`
			Parameters struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"parameters"`
		} `json:"function"`
	} `json:"tools"`
}

// texto devolve o `content` de uma mensagem como texto (a forma string do wire).
func (m mensagemDoPedido) texto() string {
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	return string(m.Content)
}

var nomeDeDocumento = regexp.MustCompile(`[a-z0-9][a-z0-9-]*\.txt`)

// ServeHTTP implementa [http.Handler].
func (f *ProviderFalso) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corpo, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	w.Header().Set("Content-Type", "application/json")
	if f.exigeChave {
		chave := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		soma := sha256.Sum256([]byte(chave))
		if subtle.ConstantTimeCompare(soma[:], f.chaveEsperada[:]) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"chave recusada pelo provider falso","type":"authentication_error"}}`))
			return
		}
	}
	var p pedidoAoFalso
	if err := json.Unmarshal(corpo, &p); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"pedido ilegivel","type":"invalid_request_error"}}`))
		return
	}
	nova, comTool := true, 0
	var doUtilizador, dasTools []string
	for _, m := range p.Messages {
		switch m.Role {
		case "assistant":
			nova = false
		case "tool":
			comTool++
			dasTools = append(dasTools, m.texto())
		case "user":
			doUtilizador = append(doUtilizador, m.texto())
		}
	}
	if len(p.Messages) == 1 && len(p.Tools) == 0 && len(doUtilizador) == 1 && doUtilizador[0] == TextoDaSonda {
		// A SONDA do modo real: conta como pedido, responde 200 e NÃO consome o roteiro — as
		// conversas da corrida começam na primeira posição dele, com ou sem sonda.
		f.mu.Lock()
		f.pedidos++
		f.mu.Unlock()
		_, _ = w.Write(respostaDeTexto("ok", int64(len(corpo)/4+1)))
		return
	}
	f.mu.Lock()
	f.pedidos++
	if f.guardar {
		f.corpos = append(f.corpos, corpo)
	}
	if nova {
		f.actual = f.roteiro[f.conversas%len(f.roteiro)]
		f.conversas++
	}
	comportamento := f.actual
	f.mu.Unlock()

	temTools := len(p.Tools) > 0
	porPedir := temTools && comTool == 0
	tokensDeEntrada := int64(len(corpo)/4 + 1)

	switch {
	case comportamento == ComportamentoErro500:
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"erro interno do provider falso","type":"server_error"}}`))
	case comportamento == ComportamentoErro401:
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"chave recusada pelo provider falso (roteiro)","type":"authentication_error"}}`))
	case comportamento == ComportamentoTexto && temTools:
		// A tool call ESCRITA COMO TEXTO: o nome da tool no texto, nenhuma tool call nativa.
		texto := SentinelaDeTexto + " I will now call " + p.Tools[0].Function.Name + " with the document name as its argument."
		_, _ = w.Write(respostaDeTexto(texto, tokensDeEntrada))
	case porPedir:
		_, _ = w.Write(respostaComToolCalls(p, strings.Join(doUtilizador, "\n"), tokensDeEntrada))
	case comportamento == ComportamentoRejeitaSegundo && comTool > 0:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"este provider falso nao aceita o segundo turno","type":"invalid_request_error"}}`))
	case comportamento == ComportamentoVazia:
		_, _ = w.Write(wiretest.Corpo("rac_reasoning_content_content_vazio"))
	case comportamento == ComportamentoCortada:
		_, _ = w.Write(wiretest.Corpo("finish_length"))
	case comportamento == ComportamentoSemFactos:
		_, _ = w.Write(wiretest.Corpo("content_texto"))
	default:
		// O turno final de quem cumpre: devolve o que as tools entregaram e o que o pedido
		// trazia. É o que faz os factos do documento estarem na saída.
		fonte := append(append([]string(nil), dasTools...), doUtilizador...)
		_, _ = w.Write(respostaDeTexto(SentinelaDeTexto+"\n"+strings.Join(fonte, "\n"), tokensDeEntrada))
	}
}

// respostaDeTexto constrói uma resposta final com o texto dado e motivo `stop`.
func respostaDeTexto(texto string, tokensDeEntrada int64) []byte {
	saida := int64(len(texto)/4 + 1)
	b, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-banco", "object": "chat.completion", "created": 1700000000, "model": "modelo-falso-do-banco",
		"choices": []any{map[string]any{
			"index": 0, "finish_reason": "stop",
			"message": map[string]any{"role": "assistant", "content": texto},
		}},
		"usage": map[string]any{"prompt_tokens": tokensDeEntrada, "completion_tokens": saida, "total_tokens": tokensDeEntrada + saida},
	})
	return b
}

// respostaComToolCalls constrói o turno em que o falso pede as tools oferecidas. Os argumentos
// saem do schema de cada tool: uma tool com o parâmetro `path` é pedida uma vez por cada nome de
// documento que o pedido menciona (duas menções ⇒ duas tool calls no mesmo turno); uma com o
// parâmetro `texto` é pedida uma vez, com o conteúdo inteiro das mensagens do utilizador.
func respostaComToolCalls(p pedidoAoFalso, doUtilizador string, tokensDeEntrada int64) []byte {
	var chamadas []any
	nova := func(nome string, args any) {
		cru, _ := json.Marshal(args)
		chamadas = append(chamadas, map[string]any{
			"id": fmt.Sprintf("call_banco%02d", len(chamadas)+1), "type": "function",
			"function": map[string]any{"name": nome, "arguments": string(cru)},
		})
	}
	for _, t := range p.Tools {
		props := t.Function.Parameters.Properties
		if _, ok := props["path"]; ok {
			vistos := map[string]bool{}
			for _, doc := range nomeDeDocumento.FindAllString(doUtilizador, -1) {
				if !vistos[doc] {
					vistos[doc] = true
					nova(t.Function.Name, map[string]string{"path": doc})
				}
			}
			continue
		}
		if _, ok := props["texto"]; ok {
			nova(t.Function.Name, map[string]string{"texto": doUtilizador})
		}
	}
	if len(chamadas) == 0 {
		// Nada para pedir (o pedido não nomeia nenhum documento): responde em texto.
		return respostaDeTexto(SentinelaDeTexto+"\n"+doUtilizador, tokensDeEntrada)
	}
	saida := int64(8 * len(chamadas))
	b, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-banco", "object": "chat.completion", "created": 1700000000, "model": "modelo-falso-do-banco",
		"choices": []any{map[string]any{
			"index": 0, "finish_reason": "tool_calls",
			"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": chamadas},
		}},
		"usage": map[string]any{"prompt_tokens": tokensDeEntrada, "completion_tokens": saida, "total_tokens": tokensDeEntrada + saida},
	})
	return b
}
