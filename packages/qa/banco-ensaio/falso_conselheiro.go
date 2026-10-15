package bancoensaio

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// OS PROVIDERS FALSOS DA EXPERIÊNCIA `conselheiro` (AOS-528).
//
// # O EXECUTOR FALSO
//
// Quatro comportamentos novos do provider falso, que só respondem à bateria difícil (3.0.0). Como
// os dos degraus, reconhecem o nó pelo OBJECTIVO, pedem as leituras que ele nomeia todas no mesmo
// turno e respondem como um modelo que leu: com valores dos documentos. A diferença está na
// resposta e na consulta:
//
//   - [ComportamentoConselhoCumpre] responde certo sozinho e nunca consulta;
//   - [ComportamentoConselhoNaoConsulta] — (a) — nunca consulta, e erra: responde com um facto
//     proibido do nó;
//   - [ComportamentoConselhoSegue] — (b) — consulta quando tem a tool e responde com o que o
//     conselho trouxe (os factos do nó que estão no conselho); sem a tool, erra como (a);
//   - [ComportamentoConselhoIgnora] — (c) — consulta quando tem a tool, ignora o conselho e erra.
//
// A consulta leva o objectivo como pergunta e o texto dos documentos lidos como contexto: é
// assim que o conselheiro falso a reconhece, e é tudo o que ele vê.
//
// # O CONSELHEIRO FALSO
//
// [ConselheiroFalso] faz de modelo conselheiro: reconhece o nó pelo objectivo na pergunta e
// responde, no modo [ConselhoCerto], com os factos exigidos do nó que encontrar no que lhe
// mandaram; no modo [ConselhoErrado], com os factos proibidos. Prova que a medida distingue um
// conselho que ajuda de um que estraga.

// Os comportamentos do executor falso na experiência `conselheiro`.
const (
	ComportamentoConselhoCumpre      Comportamento = "conselho_cumpre"
	ComportamentoConselhoNaoConsulta Comportamento = "conselho_nao_consulta"
	ComportamentoConselhoSegue       Comportamento = "conselho_segue"
	ComportamentoConselhoIgnora      Comportamento = "conselho_ignora"
)

// RoteiroDoConselheiro é o roteiro do falso na experiência `conselheiro`, quando não vem outro:
// o executor que erra sozinho e segue o conselho quando o pode pedir (b).
func RoteiroDoConselheiro() []Comportamento {
	return []Comportamento{ComportamentoConselhoSegue}
}

// comportamentoDoConselheiro diz se o comportamento é um dos da experiência `conselheiro`.
func comportamentoDoConselheiro(c Comportamento) bool {
	switch c {
	case ComportamentoConselhoCumpre, ComportamentoConselhoNaoConsulta, ComportamentoConselhoSegue, ComportamentoConselhoIgnora:
		return true
	}
	return false
}

// bateriaDoFalsoDificil carrega, uma vez, a bateria difícil embebida.
var bateriaDoFalsoDificil = sync.OnceValues(CarregarBateriaDificil)

// SentinelaDoConselho abre todo o conselho que o conselheiro falso escreve. Existe para os testes
// varrerem o que o banco produz: se aparecer num relatório, num log ou num erro, o texto de um
// conselho saiu de onde não devia.
const SentinelaDoConselho = "SENTINELA-CONSELHO-AOS528"

// toolDoConselhoNoPedido devolve o nome da tool oferecida que pede conselho (a que tem o
// parâmetro `pergunta`), ou vazio.
func toolDoConselhoNoPedido(p pedidoAoFalso) string {
	for _, t := range p.Tools {
		if _, tem := t.Function.Parameters.Properties["pergunta"]; tem {
			return t.Function.Name
		}
	}
	return ""
}

// servirConselho responde a um pedido do executor com um comportamento da experiência
// `conselheiro`.
func servirConselho(w http.ResponseWriter, c Comportamento, p pedidoAoFalso, doUtilizador, dasTools []string, tokensDeEntrada int64) {
	b, err := bateriaDoFalsoDificil()
	var no No
	conhecido := false
	if err == nil {
		no, conhecido = noDoObjectivo(b, strings.Join(doUtilizador, "\n"))
	}
	if !conhecido {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"comportamento do conselheiro pedido fora da bateria dificil","type":"invalid_request_error"}}`))
		return
	}
	documentos := documentosNomeados(no.Objectivo)
	if leitura := toolDeLeitura(p); leitura != "" && len(dasTools) == 0 && len(documentos) > 0 {
		var chamadas []any
		for _, doc := range documentos {
			chamadas = append(chamadas, map[string]any{
				"id": fmt.Sprintf("call_banco%02d", len(chamadas)+1), "type": "function",
				"function": map[string]any{"name": leitura, "arguments": `{"path":"` + doc + `"}`},
			})
		}
		_, _ = w.Write(respostaDasChamadas(chamadas, tokensDeEntrada))
		return
	}
	// As leituras vêm todas no primeiro turno: o que houver a mais nos resultados das tools é
	// o resultado das consultas.
	lidos, conselhos := dasTools, []string(nil)
	if len(dasTools) > len(documentos) {
		lidos, conselhos = dasTools[:len(documentos)], dasTools[len(documentos):]
	}
	consulta := toolDoConselhoNoPedido(p)
	querConsultar := c == ComportamentoConselhoSegue || c == ComportamentoConselhoIgnora
	if consulta != "" && querConsultar && len(conselhos) == 0 {
		args, _ := json.Marshal(map[string]string{"pergunta": no.Objectivo, "contexto": strings.Join(lidos, "\n")})
		chamada := map[string]any{
			"id": fmt.Sprintf("call_banco%02d", len(dasTools)+1), "type": "function",
			"function": map[string]any{"name": consulta, "arguments": string(args)},
		}
		_, _ = w.Write(respostaDasChamadas([]any{chamada}, tokensDeEntrada))
		return
	}
	fonte := strings.Join(lidos, "\n")
	var resposta []string
	switch {
	case c == ComportamentoConselhoCumpre:
		resposta = factosNaFonte(fonte, no.Factos)
	case c == ComportamentoConselhoSegue && len(conselhos) > 0:
		// Segue o conselho: o que ele trouxe, certo ou errado.
		doConselho := strings.Join(conselhos, "\n")
		resposta = append(factosNaFonte(doConselho, no.Factos), factosNaFonte(doConselho, no.FactosProibidos)...)
	default:
		// Sozinho, ou ignorando o conselho: a resposta errada plausível.
		if errados := factosNaFonte(fonte, no.FactosProibidos); len(errados) > 0 {
			resposta = errados[:1]
		}
	}
	if len(resposta) == 0 {
		resposta = []string{TextoSemFactosDoFalso}
	}
	_, _ = w.Write(respostaDeTexto(SentinelaDeTexto+"\n"+strings.Join(resposta, "\n"), tokensDeEntrada))
}

// Os modos do conselheiro falso.
const (
	// ConselhoCerto — responde com os factos exigidos do nó que encontrar no que lhe mandaram.
	ConselhoCerto = "certo"
	// ConselhoErrado — responde com os factos proibidos do nó: a resposta errada plausível.
	ConselhoErrado = "errado"
)

// ConselheiroFalso faz de modelo conselheiro nos testes e no modo `falso`. Implementa
// [http.Handler]: a API compatível OpenAI de chat, sem tools.
type ConselheiroFalso struct {
	// Modo é [ConselhoCerto] ou [ConselhoErrado].
	Modo string

	mu      sync.Mutex
	pedidos int
}

// Pedidos devolve quantos pedidos o conselheiro falso recebeu (com a sonda).
func (f *ConselheiroFalso) Pedidos() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pedidos
}

// ServeHTTP implementa [http.Handler].
func (f *ConselheiroFalso) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corpo, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	f.pedidos++
	f.mu.Unlock()
	var p pedidoAoFalso
	if err := json.Unmarshal(corpo, &p); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"pedido ilegivel","type":"invalid_request_error"}}`))
		return
	}
	tokensDeEntrada := int64(len(corpo)/4 + 1)
	var doUtilizador []string
	for _, m := range p.Messages {
		if m.Role == "user" {
			doUtilizador = append(doUtilizador, m.texto())
		}
	}
	texto := strings.Join(doUtilizador, "\n")
	if texto == TextoDaSonda {
		_, _ = w.Write(respostaDeTexto("ok", tokensDeEntrada))
		return
	}
	var achados []string
	if b, err := bateriaDoFalsoDificil(); err == nil {
		if no, ok := noDoObjectivo(b, texto); ok {
			factos := no.Factos
			if f.Modo == ConselhoErrado {
				factos = no.FactosProibidos
			}
			achados = factosNaFonte(texto, factos)
		}
	}
	if len(achados) == 0 {
		achados = []string{"I cannot tell from what I was given."}
	}
	_, _ = w.Write(respostaDeTexto(SentinelaDoConselho+"\n"+strings.Join(achados, "\n"), tokensDeEntrada))
}
