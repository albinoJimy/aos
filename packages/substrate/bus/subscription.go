package bus

import (
	"context"
	"sync"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// streamCursor rastreia, em memória e por stream, o maior seq confirmado de
// forma CONTÍGUA. acked guarda confirmações fora de ordem à espera de fechar o
// buraco; upTo só avança quando todos os seq <= upTo estão confirmados. Isto
// garante at-least-once: o cursor durável nunca salta um evento não confirmado.
type streamCursor struct {
	upTo  uint64
	acked map[uint64]bool
}

// subscription é a implementação de Subscription. Uma única goroutine de entrega
// (run) é dona de watermark e cur; o buffer live (queue) é partilhado com a
// goroutine da subscrição live do Event Store via mu.
type subscription struct {
	bus     *Bus
	name    string
	filter  Filter
	handler Handler
	retry   int
	policy  OverflowPolicy
	bufMax  int

	ctx    context.Context
	cancel context.CancelFunc
	esSub  eventstore.Subscription

	mu      sync.Mutex
	cond    *sync.Cond // sinaliza "não vazio" à goroutine de entrega
	notFull *sync.Cond // sinaliza "há espaço" à intake (política Block)
	queue   []eventstore.Event
	closed  bool

	// Propriedade EXCLUSIVA da goroutine run (sem lock):
	watermark map[string]uint64 // maior seq já entregue por stream (dedup da costura)

	// cur é o tracker de cursor contíguo por stream. É escrito tanto pela goroutine
	// run (ACK/Nack→DLQ) como pela goroutine de intake live (buracos de
	// DropOldest/DeadLetter em overflow), pelo que é protegido por curMu.
	curMu sync.Mutex
	cur   map[string]*streamCursor

	done chan struct{}
}

// Name implementa Subscription.
func (s *subscription) Name() string { return s.name }

// onLive é o Handler da subscrição live do Event Store. Aplica o filtro completo
// (incluindo Producers, que o Event Store não expressa) e oferece ao buffer.
func (s *subscription) onLive(ev eventstore.Event) {
	if !s.filter.matches(ev) {
		return
	}
	s.offer(ev)
}

// offer coloca um evento no buffer live aplicando a OverflowPolicy declarada.
// Corre na goroutine da subscrição live do Event Store; bloquear aqui (Block)
// afecta apenas a intake deste subscritor — nunca o produtor nem outros.
func (s *subscription) offer(ev eventstore.Event) {
	s.mu.Lock()
	for {
		if s.closed {
			s.mu.Unlock()
			return
		}
		if len(s.queue) < s.bufMax {
			s.queue = append(s.queue, ev)
			s.cond.Signal()
			s.mu.Unlock()
			return
		}
		// Buffer cheio: aplica a política.
		switch s.policy {
		case Block:
			s.notFull.Wait()
			// reavalia no topo do laço
		case DropOldest:
			dropped := s.queue[0]
			s.queue = s.queue[1:]
			s.queue = append(s.queue, ev)
			s.cond.Signal()
			s.mu.Unlock()
			// O descartado é uma PERDA deliberada (sheds load): marca-o como buraco
			// conhecido para o cursor poder avançar para além dele. Sem isto o cursor
			// ficava preso no primeiro descarte para sempre e o mapa de acks crescia
			// sem limite (ver doc do pacote / AOS-009-Q2).
			s.resolve(dropped.StreamID, dropped.Seq)
			s.bus.metrics.dropped.Add(1)
			s.bus.obs.Dropped(s.name, dropped, s.policy)
			return
		case DeadLetter:
			s.mu.Unlock()
			s.bus.metrics.dropped.Add(1)
			s.bus.dlq.add(DeadLetterEntry{
				Subscriber: s.name,
				Event:      ev,
				Reason:     "overflow",
				Attempts:   0,
			})
			// O evento ficou capturado de forma inspecionável na dead-letter queue;
			// tal como no ramo Nack→DLQ da entrega, o cursor avança para além dele
			// para não ficar preso nem acumular acks fora de ordem (AOS-009-Q2).
			s.resolve(ev.StreamID, ev.Seq)
			s.bus.obs.Dropped(s.name, ev, s.policy)
			return
		default:
			s.mu.Unlock()
			return
		}
	}
}

// run é a goroutine de entrega: primeiro o catch-up histórico, depois o live.
func (s *subscription) run(starts map[string]uint64) {
	defer close(s.done)

	// Fase 1: catch-up. Lê o histórico do Event Store por stream a partir do seq
	// de arranque e entrega em ordem. Os eventos live que cheguem nesta fase ficam
	// no buffer (offer), a aguardar a fase 2.
	for _, stream := range s.filter.Streams {
		if s.isClosed() {
			return
		}
		start := starts[stream]
		evs, err := s.bus.es.Read(s.ctx, stream, start)
		if err != nil {
			// Stream ainda sem eventos (ou store fechado/ctx cancelado): nada a
			// recuperar deste stream. O live trata do resto.
			continue
		}
		for _, ev := range evs {
			if s.isClosed() {
				return
			}
			if !s.filter.matches(ev) {
				continue
			}
			s.watermark[ev.StreamID] = ev.Seq
			s.deliver(ev)
		}
	}

	// Fase 2: live. Drena o buffer, deduplicando por watermark na fronteira.
	s.drainLive()
}

// drainLive consome o buffer live em ordem, deduplicando eventos já entregues no
// catch-up (seq <= watermark do stream).
func (s *subscription) drainLive() {
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.closed {
			s.cond.Wait()
		}
		if s.closed {
			s.mu.Unlock()
			return
		}
		ev := s.queue[0]
		s.queue = s.queue[1:]
		s.notFull.Signal()
		s.mu.Unlock()

		if ev.Seq <= s.watermark[ev.StreamID] {
			continue // duplicado da costura catch-up→live
		}
		s.watermark[ev.StreamID] = ev.Seq
		s.deliver(ev)
	}
}

// deliver invoca o Handler para um evento, tratando Ack/Nack/retry/dead-letter.
// Corre sempre na goroutine run.
func (s *subscription) deliver(ev eventstore.Event) {
	attempts := 0
	for {
		if s.isClosed() {
			return
		}
		attempts++
		d := &Delivery{Event: ev}
		s.bus.metrics.delivered.Add(1)
		s.bus.metrics.observeLatency(deliveryLatency(ev, s.bus.now))
		s.bus.obs.Delivered(s.name, ev, deliveryLatency(ev, s.bus.now))

		s.handler(d)
		res, cause := d.result()
		switch res {
		case outcomeAck:
			s.resolve(ev.StreamID, ev.Seq)
			s.bus.metrics.acked.Add(1)
			s.bus.obs.Acked(s.name, ev.StreamID, ev.Seq)
			return
		case outcomeNack:
			s.bus.metrics.nacked.Add(1)
			if attempts > s.retry {
				s.bus.dlq.add(DeadLetterEntry{
					Subscriber: s.name,
					Event:      ev,
					Cause:      cause,
					Attempts:   attempts,
					Reason:     "handler",
				})
				s.bus.metrics.deadlettered.Add(1)
				s.bus.obs.DeadLettered(s.name, ev, cause)
				// Avança o cursor para não prender a subscrição no evento venenoso.
				s.resolve(ev.StreamID, ev.Seq)
				return
			}
			// re-entrega o MESMO evento
		default:
			// Não confirmado (nem Ack nem Nack): o cursor NÃO avança; o evento será
			// re-entregue após reinício (at-least-once). Segue para o próximo.
			return
		}
	}
}

// resolve regista um seq como RESOLVIDO — confirmado (ACK), enviado para
// dead-letter, ou descartado por overflow — e avança o cursor durável de forma
// contígua. Todos estes casos partilham a mesma semântica de cursor: o evento
// não voltará a ser entregue, pelo que o piso pode ultrapassá-lo. Um evento
// entregue mas NÃO confirmado (nem Ack nem Nack) NUNCA chama resolve: fica como
// buraco por fechar, prendendo o piso — é o contrato at-least-once (re-entrega no
// reinício). É chamada tanto pela goroutine run como pela goroutine de intake
// live, por isso é serializada por curMu.
//
// Durabilidade fail-closed (AOS-009-Q4): o cursor em memória só avança DEPOIS de
// o CursorStore.Save do novo piso ter sucesso. Se o Save falhar, o piso em
// memória não avança e os seqs resolvidos ficam retidos em acked para uma nova
// tentativa num resolve posterior — nunca se dá por durável uma posição que não
// foi persistida.
func (s *subscription) resolve(stream string, seq uint64) {
	s.curMu.Lock()
	defer s.curMu.Unlock()
	c := s.cur[stream]
	if c == nil {
		c = &streamCursor{acked: make(map[uint64]bool)}
		s.cur[stream] = c
	}
	if seq <= c.upTo {
		return // já resolvido (replay/duplicado)
	}
	c.acked[seq] = true
	// Calcula o novo piso contíguo SEM ainda o materializar.
	next := c.upTo
	for c.acked[next+1] {
		next++
	}
	if next == c.upTo {
		return // buraco por fechar: o piso não avança
	}
	// Persiste ANTES de avançar em memória (fail-closed). Um erro mantém o piso e
	// os acks para nova tentativa; não avança sem durabilidade.
	if err := s.bus.cursors.Save(s.name, stream, next); err != nil {
		return
	}
	for c.upTo < next {
		c.upTo++
		delete(c.acked, c.upTo)
	}
}

func (s *subscription) isClosed() bool {
	s.mu.Lock()
	c := s.closed
	s.mu.Unlock()
	return c
}

// Unsubscribe implementa Subscription: remove do barramento e desliga.
func (s *subscription) Unsubscribe() {
	s.bus.mu.Lock()
	delete(s.bus.subs, s)
	s.bus.mu.Unlock()
	s.shutdown()
}

// shutdown desliga a subscrição: acorda intake e entrega, cancela o ctx (que
// desregista a subscrição live) e espera a goroutine run terminar. Idempotente.
func (s *subscription) shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.done
		return
	}
	s.closed = true
	s.cond.Broadcast()
	s.notFull.Broadcast()
	s.mu.Unlock()

	s.cancel()            // cancela o ctx → a subscrição live auto-desregista
	s.esSub.Unsubscribe() // idempotente; bloqueia até a goroutine live sair
	<-s.done
}

// deliveryLatency calcula o tempo desde o commit (Event.Ts) até agora. Se o Ts
// não fizer parse ou for futuro, devolve 0.
func deliveryLatency(ev eventstore.Event, now func() time.Time) time.Duration {
	t, err := time.Parse(time.RFC3339Nano, ev.Ts)
	if err != nil {
		return 0
	}
	d := now().UTC().Sub(t)
	if d < 0 {
		return 0
	}
	return d
}
