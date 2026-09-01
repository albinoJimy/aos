package jetstream

import (
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
