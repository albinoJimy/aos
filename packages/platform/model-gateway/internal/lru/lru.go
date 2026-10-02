// Package lru é um mapa com TECTO e despejo do menos-recentemente-usado (AOS-397).
//
// Existe para os agregados do metering do gateway (custo por run/árvore, cache-hit-rate
// por run/tenant), que eram mapas sem remoção: com o run real a chegar em cada chamada
// (AOS-394), um processo de vida longa guardava uma entrada por run para sempre.
//
// O despejo é pelo uso e não pela chegada: um run activo é tocado a cada chamada e fica
// no fim da fila, pelo que só perde o agregado quando `capacidade` outros runs foram
// observados depois da sua última chamada.
//
// NÃO é concorrente-seguro: quem o usa já serializa o acesso com o seu próprio mutex (o
// agregado é lido e escrito na mesma secção crítica), e um segundo lock aqui só
// duplicaria o custo.
package lru

import "container/list"

// Map é o mapa com tecto. O valor zero não é utilizável: construir com [New].
type Map[K comparable, V any] struct {
	capacidade int
	ordem      *list.List // frente = mais recente
	itens      map[K]*list.Element
}

type entrada[K comparable, V any] struct {
	chave K
	valor V
}

// New constrói um mapa que retém no máximo `capacidade` chaves. Uma capacidade < 1 é
// tratada como 1: um tecto nulo não reteria nada e esconderia a configuração errada
// atrás de leituras sempre vazias.
func New[K comparable, V any](capacidade int) *Map[K, V] {
	if capacidade < 1 {
		capacidade = 1
	}
	return &Map[K, V]{capacidade: capacidade, ordem: list.New(), itens: make(map[K]*list.Element)}
}

// Get devolve o valor da chave SEM a tocar (leitura de introspecção não altera a ordem
// de despejo).
func (m *Map[K, V]) Get(k K) (V, bool) {
	if e, ok := m.itens[k]; ok {
		return e.Value.(*entrada[K, V]).valor, true
	}
	var zero V
	return zero, false
}

// Put grava o valor e marca a chave como a mais recente. Se a chave é nova e o tecto
// está cheio, despeja a menos recente e devolve-a com despejada=true.
func (m *Map[K, V]) Put(k K, v V) (despejada K, houveDespejo bool) {
	if e, ok := m.itens[k]; ok {
		e.Value.(*entrada[K, V]).valor = v
		m.ordem.MoveToFront(e)
		return despejada, false
	}
	if m.ordem.Len() >= m.capacidade {
		velha := m.ordem.Back()
		ent := velha.Value.(*entrada[K, V])
		m.ordem.Remove(velha)
		delete(m.itens, ent.chave)
		despejada, houveDespejo = ent.chave, true
	}
	m.itens[k] = m.ordem.PushFront(&entrada[K, V]{chave: k, valor: v})
	return despejada, houveDespejo
}

// Len devolve o número de chaves retidas.
func (m *Map[K, V]) Len() int { return m.ordem.Len() }

// Capacidade devolve o tecto.
func (m *Map[K, V]) Capacidade() int { return m.capacidade }
