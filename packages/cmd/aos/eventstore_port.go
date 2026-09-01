package main

import (
	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/jetstream"
)

// eventstore_port.go — a PORTA do nó para o Event Store (AOS-100).
//
// # Porque esta porta existe, e porque é declarada AQUI
//
// Até AOS-100 o nó estava tipado no store CONCRETO (`*eventstore.Store`). Enquanto
// houve uma só implementação isso não custou nada; a partir do momento em que existe um
// Event Store replicado (`eventstore/jetstream`), custava tudo: o campo de config
// ACEITAVA apenas a implementação de referência, e o substrato partilhado — a coisa que
// o AOS-100 entrega — não podia ser ligado ao nó de forma nenhuma.
//
// A porta é declarada no CONSUMIDOR, não no substrato, e a diferença é deliberada: o
// contrato [eventstore.EventStore] fica com o que é do CONTRATO (append-only, ordem por
// stream, idempotência, push), e o que é apenas necessidade DESTE nó fica aqui. Assim
// uma implementação nova não é obrigada a crescer por causa de um health-check.
//
// MEDIDO a 2026-08-31, antes de escrever isto: em todo o `packages/cmd/aos` são
// chamados sobre o Event Store exactamente seis métodos — `Append`, `Read`,
// `Subscribe`, `Close` (o contrato) e `Healthy`, `Streams`. A porta é esses seis e mais
// nenhum; um método a mais aqui seria uma implementação nova impedida de existir sem
// razão.
type EventStorePort interface {
	eventstore.EventStore

	// Healthy indica se o store está utilizável. Serve `/readyz` e o avaliador de
	// SLO; um `false` degrada a prontidão do nó em vez de o matar.
	Healthy() bool

	// Streams devolve os stream_ids com pelo menos um evento. É a base do varredor
	// de órfãos no arranque (crash_resume), da retenção e do restauro do índice de
	// partições do WORM.
	//
	// NOTA para quem escrever uma implementação nova: num substrato PARTILHADO esta
	// pergunta deixa de ter resposta local — os streams que existem são os que
	// QUALQUER escritor criou, não os que este processo viu. Um índice em memória
	// responde a pergunta errada.
	Streams() []string
}

// asserções de conformidade: as duas implementações do repo satisfazem a porta. Se uma
// deixar de a satisfazer, quem o diz é o compilador — não um arranque em produção.
var (
	_ EventStorePort = (*eventstore.Store)(nil)
	_ EventStorePort = (*jetstream.Store)(nil)
)
