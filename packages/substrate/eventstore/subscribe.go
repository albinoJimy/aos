package eventstore

import (
	"context"
	"sync"
)

// Subscription é o handle de um subscritor push.
type Subscription interface {
	// Unsubscribe cancela a subscrição e liberta a goroutine e a fila. É
	// idempotente e bloqueia até a goroutine terminar (ver a nota sobre
	// bloqueio de shutdown em stop).
	Unsubscribe()
	// ID devolve o identificador único da subscrição.
	ID() string
}

// subscription entrega eventos a um Handler numa goroutine dedicada, através de
// uma fila FIFO ilimitada protegida por Cond. Isto garante: ordem por seq, sem
// perda, e que um subscritor lento nunca bloqueia o produtor nem outros
// subscritores (o enqueue é O(1) e nunca bloqueia).
//
// Tradeoff (reference impl): a fila não tem tecto nem política de descarte, pelo
// que um subscritor permanentemente lento ou preso acumula eventos sem limite
// (crescimento de memória ilimitado). Backpressure, limite de profundidade e
// política de drop são responsabilidade do backend de produção (NATS JetStream),
// não deste modelo determinístico de referência.
type subscription struct {
	id     string
	store  *Store
	filter Filter
	h      Handler

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []Event
	closed bool
	done   chan struct{}
}

// Subscribe regista um subscritor push com o filtro dado.
//
// O ctx governa o ciclo de vida da subscrição: se for cancelado, a subscrição é
// automaticamente removida do store e a goroutine e a fila libertadas (como se
// o chamador tivesse invocado Unsubscribe). Um ctx sem cancelamento (ex.:
// context.Background()) deixa a subscrição activa até Unsubscribe ou Close.
func (s *Store) Subscribe(ctx context.Context, filter Filter, h Handler) (Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h == nil {
		return nil, ErrConfig
	}
	if s.closed.Load() {
		return nil, ErrClosed
	}

	sub := &subscription{
		id:     newULID(),
		store:  s,
		filter: filter,
		h:      h,
		done:   make(chan struct{}),
	}
	sub.cond = sync.NewCond(&sub.mu)

	s.subMu.Lock()
	// Close marca s.closed (atómico) ANTES de adquirir subMu para drenar; se
	// chegamos aqui depois disso, vemos closed=true e recusamos — sem fuga.
	if s.closed.Load() {
		s.subMu.Unlock()
		return nil, ErrClosed
	}
	s.subs[sub] = struct{}{}
	s.subMu.Unlock()

	go sub.run()
	// Liga o ciclo de vida ao ctx: cancelar o ctx desregista a subscrição. O
	// watcher termina quando a subscrição para (sub.done fecha), seja por
	// cancelamento, Unsubscribe ou Close — logo não fica pendurado.
	if ctx.Done() != nil {
		go sub.watchContext(ctx)
	}
	return sub, nil
}

// watchContext desliga a subscrição quando o ctx do Subscribe é cancelado. Sai
// sem efeito se a subscrição já terminou por outra via (Unsubscribe/Close).
func (sub *subscription) watchContext(ctx context.Context) {
	select {
	case <-ctx.Done():
		sub.Unsubscribe()
	case <-sub.done:
	}
}

// fanout enfileira o evento em cada subscrição cujo filtro corresponda e devolve
// o número de subscrições notificadas. Chamado sob s.mu para preservar a ordem
// de seq entre appends.
func (s *Store) fanout(ev Event) int {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	n := 0
	for sub := range s.subs {
		if sub.filter.matches(ev) {
			sub.enqueue(ev)
			n++
		}
	}
	return n
}

// enqueue acrescenta um evento à fila e sinaliza a goroutine. Nunca bloqueia.
func (sub *subscription) enqueue(ev Event) {
	sub.mu.Lock()
	if !sub.closed {
		sub.queue = append(sub.queue, ev)
		sub.cond.Signal()
	}
	sub.mu.Unlock()
}

// run é o laço da goroutine do subscritor: consome a fila em ordem e invoca o
// handler. Termina quando a subscrição é fechada e a fila esvazia.
func (sub *subscription) run() {
	defer close(sub.done)
	for {
		sub.mu.Lock()
		for len(sub.queue) == 0 && !sub.closed {
			sub.cond.Wait()
		}
		if sub.closed && len(sub.queue) == 0 {
			sub.mu.Unlock()
			return
		}
		ev := sub.queue[0]
		sub.queue = sub.queue[1:]
		sub.mu.Unlock()
		sub.h(ev)
	}
}

// stop fecha a subscrição e espera a goroutine terminar. Não remove a subscrição
// do store (usado por Close, que já limpa o mapa).
//
// Bloqueio: stop (e portanto Unsubscribe/Close) espera que run() drene a fila,
// invocando o handler por cada evento pendente. Um handler que bloqueie
// indefinidamente bloqueia o encerramento — não há deadline no shutdown. O
// contrato é: os handlers devem retornar; trabalho demorado ou cancelável é
// responsabilidade do handler (que deve honrar o seu próprio ctx).
func (sub *subscription) stop() {
	sub.mu.Lock()
	if sub.closed {
		sub.mu.Unlock()
		<-sub.done
		return
	}
	sub.closed = true
	sub.cond.Signal()
	sub.mu.Unlock()
	<-sub.done
}

// Unsubscribe remove a subscrição do store e liberta a goroutine.
func (sub *subscription) Unsubscribe() {
	sub.store.subMu.Lock()
	delete(sub.store.subs, sub)
	sub.store.subMu.Unlock()
	sub.stop()
}

// ID devolve o identificador da subscrição.
func (sub *subscription) ID() string { return sub.id }
