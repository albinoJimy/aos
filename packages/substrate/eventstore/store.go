package eventstore

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// EventStore é a API pública do Event Store. Deliberadamente NÃO expõe qualquer
// operação de update ou delete: o log é append-only estrito.
type EventStore interface {
	// Append escreve um evento novo no fim do stream e devolve o seq atribuído.
	Append(ctx context.Context, streamID string, in EventInput, opts ...AppendOption) (AppendResult, error)
	// Read devolve os eventos committed do stream com seq >= fromSeq, ordenados
	// por seq ascendente. fromSeq é inclusivo; seq começa em 1.
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]Event, error)
	// Subscribe regista um subscritor push com o filtro dado.
	Subscribe(ctx context.Context, filter Filter, h Handler) (Subscription, error)
	// Close termina o store e liberta todas as subscrições.
	Close() error
}

// Observer é um gancho de observabilidade leve (opcional). Não puxa o SDK OTel
// (isso é EPIC-08); serve apenas para contagem/latência em testes e operação.
//
// Auditoria de rejeições (critério de aceitação C2): toda a tentativa rejeitada
// — incluindo a violação append-only (ErrAppendOnlyViolation) — é sinalizada via
// AppendRejected. A auditoria DURÁVEL dessa rejeição é responsabilidade do
// consumidor: deve injectar um Observer (WithObserver) que persista o registo. O
// Observer por omissão é no-op, pelo que sem configuração a rejeição devolve erro
// mas não é auditada de forma durável por este pacote.
type Observer interface {
	AppendCommitted(streamID string, seq uint64, latency time.Duration)
	AppendDuplicate(streamID string, seq uint64)
	AppendRejected(streamID string, err error)
	Published(streamID string, seq uint64, subscribers int)
}

// nopObserver é o Observer por omissão (não faz nada).
type nopObserver struct{}

func (nopObserver) AppendCommitted(string, uint64, time.Duration) {}
func (nopObserver) AppendDuplicate(string, uint64)                {}
func (nopObserver) AppendRejected(string, error)                  {}
func (nopObserver) Published(string, uint64, int)                 {}

// Store é a implementação de referência do EventStore: um cluster de réplicas
// in-process com replicação síncrona por quórum e transporte push.
type Store struct {
	mu       sync.RWMutex // serializa appends (o líder serializa) e muta estrutura
	replicas []*replica
	leaderID int
	quorum   int

	// committed é o commit index (total de eventos) confirmado por quórum mais
	// alto alguma vez atingido. É persistido para além da morte das réplicas: a
	// eleição recusa promover a líder qualquer réplica com count < committed,
	// evitando servir um log truncado como autoritativo após perda de quórum e
	// revivência de uma réplica desactualizada (durabilidade — ver electLeader).
	committed uint64

	subMu sync.Mutex
	subs  map[*subscription]struct{}

	obs    Observer
	closed atomic.Bool

	now func() time.Time // injectável para testes; por omissão time.Now
}

// Option configura o Store na construção.
type Option func(*config)

type config struct {
	replicas int
	quorum   int
	obs      Observer
	now      func() time.Time
}

// WithReplicas define o número de réplicas do cluster (>= 1).
func WithReplicas(n int) Option { return func(c *config) { c.replicas = n } }

// WithQuorum define o quórum de escrita. Se 0, usa a maioria (n/2 + 1).
func WithQuorum(q int) Option { return func(c *config) { c.quorum = q } }

// WithObserver injecta um gancho de observabilidade.
func WithObserver(o Observer) Option { return func(c *config) { c.obs = o } }

// withClock injecta um relógio (uso interno/testes).
func withClock(f func() time.Time) Option { return func(c *config) { c.now = f } }

// New constrói um Store. Por omissão: 3 réplicas, quórum de maioria (2).
func New(opts ...Option) (*Store, error) {
	c := &config{replicas: 3}
	for _, o := range opts {
		o(c)
	}
	if c.replicas < 1 {
		return nil, ErrConfig
	}
	if c.quorum == 0 {
		c.quorum = c.replicas/2 + 1
	}
	if c.quorum < 1 || c.quorum > c.replicas {
		return nil, ErrConfig
	}
	if c.obs == nil {
		c.obs = nopObserver{}
	}
	if c.now == nil {
		c.now = time.Now
	}
	s := &Store{
		replicas: make([]*replica, c.replicas),
		leaderID: 0,
		quorum:   c.quorum,
		subs:     make(map[*subscription]struct{}),
		obs:      c.obs,
		now:      c.now,
	}
	for i := range s.replicas {
		s.replicas[i] = newReplica(i)
	}
	return s, nil
}

// Append implementa a semântica de escrita do contrato C2.
//
// Ordem de verificação: idempotência primeiro (o duplicado ganha), depois
// expected_seq, depois quórum e só então a escrita replicada.
//
// Semântica de WithExpectedSeq(n) — n é o último seq committed que o chamador
// afirma ser o corrente (o "prev"); o evento novo ficaria em n+1:
//   - n == último committed  → procede (fresh slot n+1);
//   - n <  último committed  → a posição pretendida (n+1) já está ocupada por
//     história committed → ErrAppendOnlyViolation (escrita no passado);
//   - n >  último committed  → o chamador está adiantado face à realidade →
//     ErrSeqConflict (concorrência optimista).
//
// Nota para retry de CAS concorrente: quando duas escritas com o mesmo
// WithExpectedSeq(n) correm em paralelo, o vencedor materializa n+1 e o perdedor
// passa a ver last=n+1 > n, caindo no ramo expected<last → ErrAppendOnlyViolation
// (e não ErrSeqConflict). Um chamador que implemente concorrência optimista deve,
// por isso, tratar TANTO ErrSeqConflict COMO ErrAppendOnlyViolation como sinal
// para reler o stream e reavaliar — nenhum dos dois é um erro fatal de
// integridade neste contexto; só uma reescrita de uma posição já materializada é
// que representa violação append-only genuína.
func (s *Store) Append(ctx context.Context, streamID string, in EventInput, opts ...AppendOption) (AppendResult, error) {
	if err := ctx.Err(); err != nil {
		return AppendResult{}, err
	}
	var o appendOpts
	for _, opt := range opts {
		opt(&o)
	}
	start := s.now()

	if s.closed.Load() {
		return AppendResult{}, ErrClosed
	}
	s.mu.Lock()
	l := s.leader()
	if l == nil {
		s.mu.Unlock()
		s.obs.AppendRejected(streamID, ErrNoQuorum)
		return AppendResult{}, ErrNoQuorum
	}

	// 1) Idempotência primeiro — o duplicado ganha, mesmo em modo degradado.
	key := idempotencyKey(in.RunID, in.StepID)
	if hasIdempotency(in.RunID, in.StepID) {
		if ref, ok := l.dedup[key]; ok {
			ev := l.lookup(ref)
			s.mu.Unlock()
			s.obs.AppendDuplicate(ref.stream, ref.seq)
			return AppendResult{Seq: ref.seq, Status: StatusDuplicate, Event: ev.clone()}, nil
		}
	}

	// 2) Concorrência optimista / append-only.
	last := l.lastSeq(streamID)
	if o.hasExpected {
		switch {
		case o.expectedSeq == last:
			// ok
		case o.expectedSeq < last:
			s.mu.Unlock()
			s.obs.AppendRejected(streamID, ErrAppendOnlyViolation)
			return AppendResult{}, ErrAppendOnlyViolation
		default: // expectedSeq > last
			s.mu.Unlock()
			s.obs.AppendRejected(streamID, ErrSeqConflict)
			return AppendResult{}, ErrSeqConflict
		}
	}

	// 3) Quórum — fail-closed, sem deixar rasto se sub-quórum.
	alive := s.aliveReplicas()
	if len(alive) < s.quorum {
		s.mu.Unlock()
		s.obs.AppendRejected(streamID, ErrNoQuorum)
		return AppendResult{}, ErrNoQuorum
	}

	// 4) Construção do envelope e replicação síncrona a todas as réplicas vivas.
	seq := last + 1
	schema := in.SchemaVersion
	if schema == "" {
		schema = SchemaVersion
	}
	ev := Event{
		EventID:        newULID(),
		StreamID:       streamID,
		Seq:            seq,
		Type:           in.Type,
		Ts:             s.now().UTC().Format(time.RFC3339Nano),
		Producer:       in.Producer.clone(),
		SchemaVersion:  schema,
		RunID:          in.RunID,
		StepID:         in.StepID,
		ParentStepID:   in.ParentStepID,
		IdempotencyKey: key,
	}
	if in.Payload != nil {
		ev.Payload = make([]byte, len(in.Payload))
		copy(ev.Payload, in.Payload)
	}
	for _, r := range alive {
		r.store(ev.clone())
	}
	// Regista o commit index confirmado por quórum (monotónico). Serve de
	// piso durável à eleição: nenhuma réplica abaixo deste valor pode tornar-se
	// líder autoritativo (ver electLeader / Revive).
	if l.count > s.committed {
		s.committed = l.count
	}

	// Commit atingido. Enfileira para os subscritores (não bloqueante) ainda sob
	// o lock, para preservar a ordem de seq entre appends concorrentes.
	nsent := s.fanout(ev)
	s.mu.Unlock()

	latency := s.now().Sub(start)
	s.obs.AppendCommitted(streamID, seq, latency)
	s.obs.Published(streamID, seq, nsent)
	return AppendResult{Seq: seq, Status: StatusCommitted, Event: ev.clone()}, nil
}

// Read devolve cópias dos eventos committed do stream (seq >= fromSeq).
func (s *Store) Read(ctx context.Context, streamID string, fromSeq uint64) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.closed.Load() {
		return nil, ErrClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	l := s.leader()
	if l == nil {
		return nil, ErrNoQuorum
	}
	log, ok := l.streams[streamID]
	if !ok || len(log) == 0 {
		return nil, ErrStreamNotFound
	}
	out := make([]Event, 0, len(log))
	for _, ev := range log {
		if ev.Seq >= fromSeq {
			out = append(out, ev.clone())
		}
	}
	return out, nil
}

// Close termina o store e liberta todas as subscrições (sem fugas).
func (s *Store) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return ErrClosed
	}

	s.subMu.Lock()
	subs := make([]*subscription, 0, len(s.subs))
	for sub := range s.subs {
		subs = append(subs, sub)
	}
	s.subs = make(map[*subscription]struct{})
	s.subMu.Unlock()

	for _, sub := range subs {
		sub.stop()
	}
	return nil
}
