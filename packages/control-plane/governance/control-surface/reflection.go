package controlsurface

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// EventSubscriber é o subconjunto do Event Store (AOS-002) de que a reflexão depende:
// Subscribe (subscrição push, FIFO por seq). *[eventstore.Store] satisfá-lo. É uma
// interface mínima para desacoplar o projector do store concreto e o tornar testável.
type EventSubscriber interface {
	Subscribe(ctx context.Context, filter eventstore.Filter, h eventstore.Handler) (eventstore.Subscription, error)
}

// StateProjector é o READ-MODEL por run da reflexão de estado (AC4). Subscreve as
// transições de estado de UM run
// (eventstore.Filter{Streams:[runID], Types:["run.state.transition"]}) e projecta o
// ÚLTIMO To como o estado corrente — a MESMA projecção que [state.Machine.Rebuild]
// faz do log (fonte única, ordem total por (stream, seq)). Faz FAN-OUT do estado a
// TODOS os canais subscritos: dois canais subscritos ao mesmo run vêem sempre o MESMO
// estado.
//
// É LEITURA PURA — nenhuma escrita nova no Event Store. Seguro para uso concorrente.
type StateProjector struct {
	runID string

	mu          sync.RWMutex
	current     state.State
	nextID      uint64
	subscribers map[uint64]func(state.State)

	sub eventstore.Subscription
}

// NewStateProjector constrói o projector e ABRE a subscrição ao stream do run,
// filtrada às transições de estado. O estado inicial é [state.Ready] (o estado
// implícito antes de qualquer transição, coerente com [state.Machine]). O ctx governa
// o ciclo de vida da subscrição (cancelá-lo desliga-a, como um [StateProjector.Close]).
//
// Fail-closed na construção: subscritor nil ⇒ [ErrNilSubscriber]; run vazio ⇒
// [ErrEmptyRunID].
func NewStateProjector(ctx context.Context, sub EventSubscriber, runID string) (*StateProjector, error) {
	if sub == nil {
		return nil, ErrNilSubscriber
	}
	if runID == "" {
		return nil, ErrEmptyRunID
	}
	p := &StateProjector{
		runID:       runID,
		current:     state.Ready,
		subscribers: make(map[uint64]func(state.State)),
	}
	filter := eventstore.Filter{
		Streams: []string{runID},
		Types:   []string{state.EventTypeTransition},
	}
	s, err := sub.Subscribe(ctx, filter, p.onEvent)
	if err != nil {
		return nil, err
	}
	p.sub = s
	return p, nil
}

// onEvent dobra uma transição no estado corrente e faz o fan-out. É o Handler da
// subscrição (invocado na goroutine do subscritor, em ordem de seq). Fail-closed: um
// evento cujo payload não descodifica ou cujo To não é um estado canónico é IGNORADO
// (não corrompe a projecção) — a reflexão nunca adopta um estado desconhecido.
func (p *StateProjector) onEvent(ev eventstore.Event) {
	var rec struct {
		To state.State `json:"to"`
	}
	if err := json.Unmarshal(ev.Payload, &rec); err != nil {
		return
	}
	if !state.IsKnown(rec.To) {
		return
	}
	p.mu.Lock()
	p.current = rec.To
	fns := make([]func(state.State), 0, len(p.subscribers))
	for _, fn := range p.subscribers {
		fns = append(fns, fn)
	}
	p.mu.Unlock()

	// Fan-out FORA do lock: um canal lento não bloqueia a projecção nem outros canais.
	for _, fn := range fns {
		fn(rec.To)
	}
}

// Current devolve o estado durável corrente projectado. É a base da query-estado
// (control.state) e reflecte o mesmo To que [state.Machine.Current] após a última
// transição ter sido entregue.
func (p *StateProjector) Current() state.State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.current
}

// RunID devolve o run que este projector reflecte.
func (p *StateProjector) RunID() string { return p.runID }

// Observe regista um CANAL (desktop/chatbot/API) para receber o estado a cada
// transição — o fan-out da reflexão. Devolve uma função de cancelamento
// (idempotente). O callback é invocado na goroutine da subscrição; deve retornar
// depressa (trabalho demorado é responsabilidade do canal).
//
// É esta função que concretiza a CONSISTÊNCIA (AC4): registar dois callbacks (dois
// canais) garante que ambos recebem o MESMO estado, da MESMA fonte, na MESMA ordem.
func (p *StateProjector) Observe(fn func(state.State)) (cancel func()) {
	p.mu.Lock()
	id := p.nextID
	p.nextID++
	p.subscribers[id] = fn
	p.mu.Unlock()
	return func() {
		p.mu.Lock()
		delete(p.subscribers, id)
		p.mu.Unlock()
	}
}

// Close desliga a subscrição e liberta a goroutine. Idempotente.
func (p *StateProjector) Close() {
	if p.sub != nil {
		p.sub.Unsubscribe()
	}
}
