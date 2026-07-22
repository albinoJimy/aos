package controlsurface

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// EventSubscriber é o subconjunto do Event Store (AOS-002) de que a reflexão depende:
// Read (backfill do backlog histórico, seq ascendente) + Subscribe (subscrição push,
// FIFO por seq). *[eventstore.Store] satisfá-lo. É uma interface mínima para
// desacoplar o projector do store concreto e o tornar testável.
type EventSubscriber interface {
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
	Subscribe(ctx context.Context, filter eventstore.Filter, h eventstore.Handler) (eventstore.Subscription, error)
}

// StateProjector é o READ-MODEL por run da reflexão de estado (AC4). Deriva o estado
// corrente A FRIO relendo o backlog de transições do run e depois SUBSCREVE as novas
// transições (eventstore.Filter{Streams:[runID], Types:["run.state.transition"]}),
// projectando o ÚLTIMO To como o estado corrente — a MESMA projecção que
// [state.Machine.Rebuild] faz do log (fonte única, ordem total por (stream, seq)). Faz
// FAN-OUT do estado a TODOS os canais subscritos: dois canais subscritos ao mesmo run
// vêem sempre o MESMO estado.
//
// Backfill+resume (AOS-150): a construção costura Read(backlog) + Subscribe(vivo) sob
// uma WATERMARK de seq, deduplicando a sobreposição. Um cliente que liga a um run já
// em `paused` (ou qualquer estado) vê-o IMEDIATAMENTE — não fica preso em [state.Ready]
// à espera da próxima transição. A [StateProjector.Watermark] expõe o cursor de seq
// para um cliente retomar (reconnect) sem perder nem duplicar.
//
// É LEITURA PURA — nenhuma escrita nova no Event Store. Seguro para uso concorrente.
type StateProjector struct {
	runID string

	mu      sync.RWMutex
	current state.State
	// watermark é o maior seq de transição já dobrado — o cursor de reflexão. Deduplica
	// a sobreposição backlog/vivo e é o fromSeq de retoma.
	watermark uint64
	// backfilling indica que a construção ainda está a dobrar o backlog: os eventos
	// vivos que cheguem entretanto são bufferizados (não perdidos) e drenados no fim.
	backfilling bool
	buffer      []eventstore.Event

	nextID      uint64
	subscribers map[uint64]func(state.State)

	sub eventstore.Subscription
}

// NewStateProjector constrói o projector com backfill a frio + subscrição viva. O
// estado corrente é derivado do backlog de transições do run (não fica em [state.Ready]
// se o run já transitou). O ctx governa o ciclo de vida da subscrição (cancelá-lo
// desliga-a, como um [StateProjector.Close]).
//
// Fail-closed na construção: subscritor nil ⇒ [ErrNilSubscriber]; run vazio ⇒
// [ErrEmptyRunID]; um erro de Read/Subscribe (que não "stream inexistente") aborta.
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
		backfilling: true,
	}
	filter := eventstore.Filter{
		Streams: []string{runID},
		Types:   []string{state.EventTypeTransition},
	}

	// (1) SUBSCREVER PRIMEIRO: captura (em buffer) qualquer transição viva que chegue
	// na janela entre o Read e o registo da subscrição — sem esta ordem, uma transição
	// concorrente ao backfill perder-se-ia.
	s, err := sub.Subscribe(ctx, filter, p.onEvent)
	if err != nil {
		return nil, err
	}
	p.sub = s

	// (2) LER O BACKLOG e derivar o estado corrente a frio + a watermark. Um stream
	// ainda inexistente (nenhuma transição) NÃO é erro: backlog vazio ⇒ estado Ready.
	backlog, err := sub.Read(ctx, runID, 1)
	if err != nil && !errors.Is(err, eventstore.ErrStreamNotFound) {
		p.sub.Unsubscribe()
		return nil, err
	}

	// (3) Dobrar backlog + drenar o buffer vivo (dedup por seq), tudo sob UM lock: a
	// goroutine da subscrição fica bloqueada em onEvent até terminarmos, pelo que
	// nenhum evento se perde nem se reordena. Sem subscritores ainda (Observe é
	// pós-construção) ⇒ o fan-out aqui é vazio.
	p.mu.Lock()
	for _, ev := range backlog {
		p.foldLocked(ev)
	}
	for _, ev := range p.buffer {
		p.foldLocked(ev)
	}
	p.buffer = nil
	p.backfilling = false
	p.mu.Unlock()

	return p, nil
}

// onEvent é o Handler da subscrição (invocado na goroutine do subscritor, em ordem de
// seq). Durante o backfill BUFFERIZA; depois dobra directamente (dedup por seq) e faz
// o fan-out.
func (p *StateProjector) onEvent(ev eventstore.Event) {
	p.mu.Lock()
	if p.backfilling {
		p.buffer = append(p.buffer, ev)
		p.mu.Unlock()
		return
	}
	to, fns, ok := p.foldLocked(ev)
	p.mu.Unlock()
	if !ok {
		return
	}
	// Fan-out FORA do lock: um canal lento não bloqueia a projecção nem outros canais.
	for _, fn := range fns {
		fn(to)
	}
}

// foldLocked dobra UMA transição no estado corrente sob o lock (o chamador detém p.mu).
// Deduplica por seq (um evento já coberto pela watermark — sobreposição backlog/vivo —
// é ignorado) e avança a watermark. Fail-closed: um payload que não descodifica ou cujo
// To não é canónico avança a watermark mas NÃO adopta o estado (a reflexão nunca
// adopta um estado desconhecido). Devolve (to, snapshot dos subscritores, true) quando
// há uma nova transição a difundir; caso contrário false.
func (p *StateProjector) foldLocked(ev eventstore.Event) (state.State, []func(state.State), bool) {
	if ev.Seq != 0 && ev.Seq <= p.watermark {
		return state.Ready, nil, false // dedup: seq já dobrado
	}
	if ev.Seq > p.watermark {
		p.watermark = ev.Seq
	}
	var rec struct {
		To state.State `json:"to"`
	}
	if err := json.Unmarshal(ev.Payload, &rec); err != nil {
		return state.Ready, nil, false
	}
	if !state.IsKnown(rec.To) {
		return state.Ready, nil, false
	}
	p.current = rec.To
	fns := make([]func(state.State), 0, len(p.subscribers))
	for _, fn := range p.subscribers {
		fns = append(fns, fn)
	}
	return rec.To, fns, true
}

// Current devolve o estado durável corrente projectado. É a base da query-estado
// (control.state) e reflecte o mesmo To que [state.Machine.Current] após a última
// transição ter sido dobrada — incluindo A FRIO, pelo backfill do backlog.
func (p *StateProjector) Current() state.State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.current
}

// Watermark devolve o maior seq de transição já dobrado — o cursor de reflexão. Um
// cliente que reconecta pode passá-lo como fromSeq para retomar; o backfill/subscrição
// deduplicam por seq, pelo que a retoma não perde nem duplica transições.
func (p *StateProjector) Watermark() uint64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.watermark
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
