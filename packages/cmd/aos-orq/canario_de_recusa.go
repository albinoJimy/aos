package main

// canario_de_recusa.go — O CANÁRIO DA RECUSA DO PRÓPRIO OBJECTIVO (AOS-504). SÓ MEDIÇÃO.
//
// # O QUE SE MEDE
//
// Em produção, o nó de resumo de um plano — sem tools, a consumir a saída de outro nó — recusou
// o próprio objectivo em cerca de 1 plano em 20: respondeu que o `objective` vinha dentro de um
// `plan_input` com `taint=untrusted`, e concluiu. Nenhum sinal estrutural distingue esse run de
// um que fez o trabalho: sem tools, texto não vazio, motivo de paragem `stop`.
//
// Um resumo de um documento não tem razão para falar do vocabulário do protocolo da projecção.
// O canário conta os nós dessa classe cujo texto final o usa, ao lado do total da classe, para a
// taxa se ler nas métricas sem ninguém abrir textos à mão.
//
// # O QUE NÃO É
//
// NÃO É UM DETECTOR, E NADA DECIDE POR ELE. O ADR-037 recusa um veredicto que julgue o conteúdo
// de um texto, e este ficheiro não o contraria: o canário é chamado depois de o desfecho do nó
// estar decidido, não devolve nada, e nenhum ramo de decisão lê o que ele contou. Não muda o
// estado do nó do plano, a saída publicada, os eventos do plano, o `detail` do desfecho nem o
// código de saída; não repete nem falha nada.
//
// É UM LIMITE INFERIOR. Uma recusa que não use as palavras do protocolo escapa-lhe, e um texto
// legítimo que as use conta. Serve para comparar uma série com outra, não para afirmar quantas
// recusas houve.
//
// # O TEXTO NÃO SAI DAQUI
//
// O texto final é dado do titular. Lê-se em memória e fica reduzido a um booleano: não entra em
// métrica, log nem evento — nem um excerto, nem o tamanho, nem qual das palavras apareceu.

import (
	"strings"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// vocabularioDoProtocolo são as palavras do protocolo da projecção nativa que o canário procura
// (o `kind` do segmento de entrada do plano e o rótulo de taint). Lista fechada e exacta, sem
// normalização de caixa: são identificadores, e é como identificadores que as recusas medidas os
// citam.
var vocabularioDoProtocolo = []string{"plan_input", "taint=untrusted"}

// classeDoCanario diz se o nó é da classe que o canário mede: sem tools e com `consumes`. É uma
// propriedade do DOCUMENTO do plano, não do que o run fez.
func classeDoCanario(n plan.Node) bool {
	return len(n.Tools) == 0 && len(n.Consumes) > 0
}

// usaVocabularioDoProtocolo diz se o texto contém alguma palavra de [vocabularioDoProtocolo].
func usaVocabularioDoProtocolo(texto string) bool {
	for _, palavra := range vocabularioDoProtocolo {
		if strings.Contains(texto, palavra) {
			return true
		}
	}
	return false
}

// noDoCanarioConcluiu conta um nó da classe do canário que concluiu, e se o seu texto final usa
// o vocabulário do protocolo.
func (m *medicaoDoContrato) noDoCanarioConcluiu(canario bool) {
	if m == nil {
		return
	}
	m.canarioNos++
	if canario {
		m.canarioRecusas++
	}
}

// registarCanarioDeRecusa mede um nó que CONCLUIU. Não devolve nada e não decide nada: quem a
// chama já decidiu o desfecho do nó. Um nó fora da classe, ou que não concluiu, não conta.
func (e *executorDeNos) registarCanarioDeRecusa(n plan.Node, concluiu bool, textoFinal string) {
	if !concluiu || !classeDoCanario(n) {
		return
	}
	e.medicao.noDoCanarioConcluiu(usaVocabularioDoProtocolo(textoFinal))
}

// registarCanario soma às séries o que um `serve` mediu. As duas séries saem JUNTAS: havendo nós
// da classe, o canário escreve-se mesmo a zero, para a taxa ter numerador e denominador no mesmo
// ficheiro. Sem nós da classe não se escreve nenhuma.
func (m *metricasDoConsumo) registarCanario(c *medicaoDoContrato) {
	if c == nil || c.canarioNos == 0 {
		return
	}
	m.somar(metricaCanarioDeRecusaNos, float64(c.canarioNos))
	m.somar(metricaCanarioDeRecusa, float64(c.canarioRecusas))
}
