package env

import (
	"context"
	"sync"

	"github.com/aos-ref/substrate/eventstore"
)

// ===========================================================================
// Bus — transporte PUSH efémero sobre o Event Store
// ===========================================================================
//
// LAYERING: o AOS tem um bus dedicado (substrate/bus), mas ele vive noutro
// módulo com go.mod próprio; para manter o go.mod do testkit LEVE e zero-dep, o
// transporte push do ambiente efémero é servido pelas SUBSCRIÇÕES do próprio
// Event Store ([eventstore.Store.Subscribe]) — que já é push, event-driven, em
// ordem por seq e sem perda. O [Bus] é uma fina camada sobre essas subscrições
// que (a) as RASTREIA para teardown garantido (fecho das goroutines, sem leak)
// e (b) mantém um TAP de captura de todos os eventos, para asserção nos testes.

// Bus é o transporte push do ambiente. Todas as subscrições que cria ficam
// registadas para serem canceladas no teardown do Env — nenhuma goroutine de
// push fica pendurada. Concorrente-seguro (-race).
type Bus struct {
	store *eventstore.Store

	mu     sync.Mutex
	subs   []eventstore.Subscription
	tapLog []eventstore.Event
	closed bool
}

// newBus constrói o transporte sobre um Store e liga o TAP (uma subscrição sem
// filtro que regista cada evento committed, para asserções de entrega push).
func newBus(store *eventstore.Store) (*Bus, error) {
	b := &Bus{store: store}
	// TAP: captura TODOS os eventos. A subscrição fica rastreada e é cancelada no
	// close() — a sua goroutine termina de forma determinista.
	if _, err := b.Subscribe(eventstore.Filter{}, func(ev eventstore.Event) {
		b.mu.Lock()
		b.tapLog = append(b.tapLog, ev)
		b.mu.Unlock()
	}); err != nil {
		return nil, err
	}
	return b, nil
}

// Subscribe regista um subscritor push com o filtro dado. A subscrição é
// RASTREADA: o teardown do Env cancela-a automaticamente (fecha a goroutine e a
// fila, sem leak). Usa context.Background — o ciclo de vida é governado pelo
// teardown, não por um ctx do chamador.
func (b *Bus) Subscribe(filter eventstore.Filter, h eventstore.Handler) (eventstore.Subscription, error) {
	sub, err := b.store.Subscribe(context.Background(), filter, h)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.subs = append(b.subs, sub)
	b.mu.Unlock()
	return sub, nil
}

// Received devolve uma cópia dos eventos que o TAP capturou até agora (ordem de
// entrega). Serve para asserir que uma trajectória semeada foi PUBLICADA no
// transporte push.
func (b *Bus) Received() []eventstore.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]eventstore.Event, len(b.tapLog))
	copy(out, b.tapLog)
	return out
}

// Count devolve o número de eventos capturados pelo TAP.
func (b *Bus) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.tapLog)
}

// close cancela TODAS as subscrições rastreadas (fecha as goroutines de push) e
// marca o bus fechado. Idempotente: a segunda chamada é no-op. Chamado pelo
// [EphemeralEnv.Teardown]; as subscrições são canceladas ANTES do Store.Close.
func (b *Bus) close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := b.subs
	b.subs = nil
	b.mu.Unlock()

	// Unsubscribe fora do lock: bloqueia até a goroutine da subscrição terminar
	// (drena a fila) — garante ausência de goroutine órfã, provável com -race.
	for _, sub := range subs {
		sub.Unsubscribe()
	}
}
