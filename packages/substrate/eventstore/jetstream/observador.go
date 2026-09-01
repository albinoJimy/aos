package jetstream

import (
	"errors"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// observador.go — o gancho de observabilidade do adaptador (AOS-100, contrato C2).
//
// # Porque isto não é um extra
//
// O contrato C2 diz, no `Observer` do store de referência, que «toda a tentativa
// REJEITADA — incluindo a violação append-only — é sinalizada via AppendRejected», e que
// a auditoria DURÁVEL dessa rejeição é responsabilidade do consumidor, que injecta um
// Observer para a persistir.
//
// Sem este gancho, trocar o substrato para o replicado apagava essa via: as rejeições
// continuariam a ser devolvidas ao chamador, mas deixariam de ser AUDITÁVEIS. Um nó que
// migrasse para JetStream perdia observabilidade sem que nada o dissesse — e a auditoria
// de rejeições é precisamente o que distingue «recusámos» de «não aconteceu».
//
// # O que é sinalizado, e o que deliberadamente não é
//
// As quatro chamadas do contrato, com a mesma semântica do store de referência. NÃO se
// inventa nenhuma: um Observer que recebesse eventos que a referência não emite tornaria
// os dois substratos não-comparáveis, e é a comparabilidade que permite trocar um pelo
// outro sem reescrever quem observa.
//
// # Porque `Published` NÃO é chamado
//
// O contrato define `Published(streamID, seq, subscribers)` — quantos subscritores
// receberam o evento. No store de referência o escritor faz o fanout e conta-os.
//
// Aqui o fanout é do SERVIDOR: o escritor publica e vai-se embora, e não tem como saber
// quantos consumidores existem nem quantos receberam. Chamar `Published` com 1, ou com o
// número de subscrições LOCAIS, seria reportar um número que não é o que o contrato pede —
// e um número errado num sinal de observabilidade é pior do que a sua ausência, porque
// ninguém desconfia dele. Fica DECLARADO aqui em vez de descoberto por quem estranhar o
// dashboard.
//
// # A latência
//
// A latência reportada é a da operação COMPLETA vista pelo chamador — hidratação, CAS e
// resposta do servidor incluídas. É a que interessa a quem opera: o custo real de uma
// escrita neste substrato, e não o tempo de uma parte dele.

// ComObservador injecta o gancho de observabilidade. Sem ele o adaptador não observa nada
// (nop), tal como o store de referência.
func ComObservador(o eventstore.Observer) Option {
	return func(c *config) {
		if o != nil {
			c.obs = o
		}
	}
}

// observadorNulo é o default. Existe porque o campo nunca pode ser nil no caminho quente:
// um `if s.obs != nil` por chamada seria ruído em todas as vias de escrita, e esquecê-lo
// uma vez seria um panic no caminho mais crítico que existe.
type observadorNulo struct{}

func (observadorNulo) AppendCommitted(string, uint64, time.Duration) {}
func (observadorNulo) AppendDuplicate(string, uint64)                {}
func (observadorNulo) AppendRejected(string, error)                  {}
func (observadorNulo) Published(string, uint64, int)                 {}

// ComRastreador injecta a porta de rastreio (EPIC-08). Sem ela não se abre span nenhum.
//
// É DISTINTA de [ComObservador] por uma razão que o doc do `Observer` já dava: aquele é
// o gancho de contagem/auditoria durável, chamado depois da operação e sem contexto; este
// recebe o `ctx` e por isso o span nasce POR BAIXO do span que causou a escrita, em vez
// de órfão. Ter os dois é o que permite que a auditoria de rejeições e a árvore de spans
// não se atrapalhem uma à outra.
func ComRastreador(r eventstore.Rastreador) Option {
	return func(c *config) {
		if r != nil {
			c.rastro = r
		}
	}
}

// registarRecusa marca o span como RECUSADO e nomeia a causa.
//
// A causa vai no span porque é ela que diz ao operador o que fazer: um
// E_APPEND_ONLY_VIOLATION é um escritor a afirmar o passado, um E_SEQ_CONFLICT é um a
// afirmar o futuro, e um erro de ligação não é nem uma coisa nem outra. Um span que só
// dissesse «rejected» mandaria toda a gente ler os logs.
func registarRecusa(span eventstore.Rastro, err error) {
	span.Atributo(eventstore.AtributoDesfecho, "rejected")
	var se *eventstore.StoreError
	if errors.As(err, &se) {
		span.Atributo(eventstore.AtributoErro, se.Code)
		return
	}
	span.Atributo(eventstore.AtributoErro, err.Error())
}

// LigarRastreador liga o rastreio DEPOIS da construção do store.
//
// # Porque existe, em vez de ser só a opção de construtor
//
// No ápice de composição do nó, o Event Store é construído ANTES do tracer — e a ordem
// não é gratuita: mover a construção do tracer para cima arrastaria consigo a guarda de
// limpeza do exportador, que é exactamente onde esse ficheiro já teve um defeito
// registado (uma posse retida por um arranque abortado). Trocar um risco conhecido por um
// mexer numa ordem delicada seria mau negócio.
//
// # Porque RECUSA depois do primeiro uso, em vez de o desaconselhar
//
// Um rastreador ligado a meio produziria spans para umas operações e não para outras, sem
// nada a dizê-lo — uma observabilidade com buracos silenciosos é pior do que nenhuma,
// porque quem a lê não sabe que está incompleta. A invariante «liga-se antes de usar» é
// IMPOSTA e não recomendada: depois do primeiro Append, Read ou Subscribe, isto devolve
// erro.
func (s *Store) LigarRastreador(r eventstore.Rastreador) error {
	if r == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usado {
		return errors.New("jetstream: rastreador ligado depois de o store já ter sido usado — " +
			"produziria spans só para parte das operações, e uma observabilidade com buracos " +
			"silenciosos é pior do que nenhuma")
	}
	s.rastro = r
	return nil
}

// marcarUsado fecha a janela em que [Store.LigarRastreador] é aceite.
func (s *Store) marcarUsado() {
	s.mu.Lock()
	s.usado = true
	s.mu.Unlock()
}
