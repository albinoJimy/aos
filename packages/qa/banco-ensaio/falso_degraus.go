package bancoensaio

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// O PROVIDER FALSO NA BATERIA POR DEGRAUS (AOS-525).
//
// O comportamento `cumpre` de sempre devolve tudo o que as tools entregaram. Na bateria por
// degraus isso despejava os distractores na saída: não serve de «modelo que cumpre». Os
// comportamentos abaixo respondem como um modelo que LEU o documento: só com os factos que o nó
// pede — e só os que encontraram no que receberam, das tools ou do `plan_input`. É isso que faz
// a cadeia do degrau 7 ser uma cadeia: um facto que um nó deixa cair não reaparece no seguinte.
//
// # COMO SABEM O QUE O NÓ PEDE
//
// O falso só vê o pedido. Reconhece o nó pelo OBJECTIVO, que vem inteiro numa mensagem do
// utilizador, procurando-o na bateria por degraus embebida no binário — a mesma que o banco
// corre (os objectivos são únicos: [CarregarBateriaPorDegraus] recusa dois iguais). Um pedido
// que não traga o objectivo de um nó dessa bateria leva 400: estes comportamentos não servem a
// bateria 1.0.0.
//
// # CADA DEGRAU TEM QUEM O CUMPRA E QUEM O FALHE
//
// [ComportamentoDegrauCumpre] e [ComportamentoDegrauUmPorTurno] cumprem os sete. Os outros três
// falham de uma maneira só, para o relatório os distinguir pela razão: um facto ausente, uma
// resposta errada, ou o contrato.
const (
	// ComportamentoDegrauCumpre — pede as leituras que o objectivo nomeia (todas no mesmo passo;
	// num nó com um documento negado, uma de cada vez, até uma dar um facto) e responde SÓ com os
	// factos exigidos do nó que encontrou no que recebeu.
	ComportamentoDegrauCumpre Comportamento = "degrau_cumpre"
	// ComportamentoDegrauOmite — como o anterior, mas deixa cair o último facto exigido: a
	// saída fica com um facto AUSENTE.
	ComportamentoDegrauOmite Comportamento = "degrau_omite"
	// ComportamentoDegrauDistractor — como o primeiro, e junta à resposta o primeiro facto
	// proibido do nó que encontrou: a RESPOSTA ERRADA. Num nó sem factos proibidos cumpre.
	ComportamentoDegrauDistractor Comportamento = "degrau_distractor"
	// ComportamentoDegrauUmaLeitura — pede só a PRIMEIRA leitura que o objectivo nomeia e
	// responde com o que encontrou nela: não faz a segunda chamada. Com um só documento cumpre.
	ComportamentoDegrauUmaLeitura Comportamento = "degrau_uma_leitura"
	// ComportamentoDegrauUmPorTurno — cumpre, mas pede UMA leitura por turno, pela ordem do
	// objectivo, e só responde depois de as ter todas. É o modelo que lê bem e não faz chamadas
	// em paralelo: tem de cumprir o degrau 2 (quatro leituras e a resposta são cinco turnos).
	ComportamentoDegrauUmPorTurno Comportamento = "degrau_um_por_turno"
)

// RoteiroDosDegraus é o roteiro do falso na experiência `degraus`, quando não vem outro: todas
// as conversas cumprem. Para ver um degrau falhar escolhe-se o roteiro (`--roteiro`).
func RoteiroDosDegraus() []Comportamento {
	return []Comportamento{ComportamentoDegrauCumpre}
}

// comportamentoDosDegraus diz se o comportamento é um dos da bateria por degraus.
func comportamentoDosDegraus(c Comportamento) bool {
	switch c {
	case ComportamentoDegrauCumpre, ComportamentoDegrauOmite, ComportamentoDegrauDistractor, ComportamentoDegrauUmaLeitura, ComportamentoDegrauUmPorTurno:
		return true
	}
	return false
}

// bateriaDoFalsoDosDegraus carrega, uma vez, a bateria por degraus embebida.
var bateriaDoFalsoDosDegraus = sync.OnceValues(CarregarBateriaPorDegraus)

// noDoObjectivo procura, na bateria, o nó cujo objectivo vem no texto do utilizador.
func noDoObjectivo(b *Bateria, doUtilizador string) (No, bool) {
	for _, c := range b.Casos {
		for _, n := range c.Nos {
			if strings.Contains(doUtilizador, n.Objectivo) {
				return n, true
			}
		}
	}
	return No{}, false
}

// factosNaFonte devolve, pela ordem dada, os factos que ocorrem na fonte pela regra dos factos.
func factosNaFonte(fonte string, factos []string) []string {
	var out []string
	for _, f := range factos {
		if factoNaSaida(fonte, f) {
			out = append(out, f)
		}
	}
	return out
}

// TextoSemFactosDoFalso é o que o falso dos degraus escreve quando não tem facto nenhum para
// dar: uma resposta não vazia, para o run concluir e a falta se contar como facto ausente.
const TextoSemFactosDoFalso = "I could not find what the objective asks for."

// servirDegrau responde a um pedido com um comportamento da bateria por degraus.
func servirDegrau(w http.ResponseWriter, c Comportamento, p pedidoAoFalso, doUtilizador, dasTools []string, tokensDeEntrada int64) {
	b, err := bateriaDoFalsoDosDegraus()
	var no No
	conhecido := false
	if err == nil {
		no, conhecido = noDoObjectivo(b, strings.Join(doUtilizador, "\n"))
	}
	if !conhecido {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"comportamento dos degraus pedido fora da bateria por degraus","type":"invalid_request_error"}}`))
		return
	}
	// A fonte é o que o «modelo» recebeu: os resultados das tools e as mensagens do utilizador
	// (o `plan_input`). O objectivo também lá está, e não traz facto nenhum (a bateria recusa-o).
	fonte := strings.Join(append(append([]string(nil), dasTools...), doUtilizador...), "\n")
	achados := factosNaFonte(fonte, no.Factos)

	if tool := toolDeLeitura(p); tool != "" {
		documentos := documentosNomeados(no.Objectivo)
		var pedir []string
		switch {
		case len(documentos) == 0:
		case c == ComportamentoDegrauUmaLeitura:
			if len(dasTools) == 0 {
				pedir = documentos[:1]
			}
		case c == ComportamentoDegrauUmPorTurno:
			if len(dasTools) < len(documentos) {
				pedir = documentos[len(dasTools) : len(dasTools)+1]
			}
		case no.Nega != "":
			// O outro caminho: uma leitura de cada vez, pela ordem do objectivo, até uma dar um facto.
			if len(dasTools) < len(documentos) && len(achados) == 0 {
				pedir = documentos[len(dasTools) : len(dasTools)+1]
			}
		case len(dasTools) == 0:
			pedir = documentos
		}
		if len(pedir) > 0 {
			var chamadas []any
			for _, doc := range pedir {
				chamadas = append(chamadas, map[string]any{
					"id": fmt.Sprintf("call_banco%02d", len(dasTools)+len(chamadas)+1), "type": "function",
					"function": map[string]any{"name": tool, "arguments": `{"path":"` + doc + `"}`},
				})
			}
			_, _ = w.Write(respostaDasChamadas(chamadas, tokensDeEntrada))
			return
		}
	}

	switch c {
	case ComportamentoDegrauOmite:
		if len(achados) > 0 {
			achados = achados[:len(achados)-1]
		}
	case ComportamentoDegrauDistractor:
		if errados := factosNaFonte(fonte, no.FactosProibidos); len(errados) > 0 {
			achados = append(achados, errados[0])
		}
	}
	if len(achados) == 0 {
		achados = []string{TextoSemFactosDoFalso}
	}
	_, _ = w.Write(respostaDeTexto(SentinelaDeTexto+"\n"+strings.Join(achados, "\n"), tokensDeEntrada))
}

// toolDeLeitura devolve o nome da tool oferecida que lê um documento pelo nome (a que tem o
// parâmetro `path`), ou vazio se o pedido não a oferece.
func toolDeLeitura(p pedidoAoFalso) string {
	for _, t := range p.Tools {
		if _, tem := t.Function.Parameters.Properties["path"]; tem {
			return t.Function.Name
		}
	}
	return ""
}

// documentosNomeados devolve os nomes de documento que um texto menciona, sem repetir, pela
// ordem em que aparecem.
func documentosNomeados(texto string) []string {
	var out []string
	vistos := map[string]bool{}
	for _, doc := range nomeDeDocumento.FindAllString(texto, -1) {
		if !vistos[doc] {
			vistos[doc] = true
			out = append(out, doc)
		}
	}
	return out
}
