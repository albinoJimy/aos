package bus

import (
	"context"
	"sync"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// OverflowPolicy declara o que fazer quando o buffer live de uma subscrição
// enche. É sempre explícita (parte do contrato da subscrição).
type OverflowPolicy int

const (
	// Block faz a intake DESTE subscritor esperar por espaço. Nunca bloqueia o
	// produtor nem outros subscritores. Preserva ordem e at-least-once. É a
	// política por omissão.
	Block OverflowPolicy = iota
	// DropOldest descarta o evento mais antigo do buffer para admitir o novo
	// (sheds load). O descartado é uma PERDA deliberada: fica marcado como buraco
	// conhecido e o cursor avança para além dele (não é re-entregue no reinício).
	// Degrada DURABILIDADE (o evento descartado perde-se), não a liveness do cursor.
	DropOldest
	// DeadLetter encaminha o evento em excesso para a dead-letter queue.
	DeadLetter
)

func (p OverflowPolicy) String() string {
	switch p {
	case Block:
		return "Block"
	case DropOldest:
		return "DropOldest"
	case DeadLetter:
		return "DeadLetter"
	default:
		return "Unknown"
	}
}

func (p OverflowPolicy) valid() bool {
	return p == Block || p == DropOldest || p == DeadLetter
}

// defaultBuffer é o tamanho do buffer live por subscrição quando SubConfig.Buffer
// é 0.
const defaultBuffer = 1024

// SubConfig é a configuração de uma subscrição durável.
type SubConfig struct {
	// Name é o identificador durável do subscritor (obrigatório). O cursor é
	// guardado por (Name, stream).
	Name string
	// Filter selecciona os eventos por type, stream e/ou producer.
	Filter Filter
	// FromSeq, se não-nil, força o arranque nesse seq (replay), ignorando o
	// cursor guardado para efeitos de posição inicial.
	FromSeq *uint64
	// Handler processa cada evento (ver Handler).
	Handler Handler
	// Retry é o número máximo de re-entregas após uma falha (Nack) antes de
	// enviar para a dead-letter. 0 = sem retries (dead-letter à 1.ª falha).
	Retry int
	// Buffer é a profundidade máxima do buffer live. 0 usa defaultBuffer.
	Buffer int
	// Overflow é a política de degradação quando o buffer enche.
	Overflow OverflowPolicy
}

// Subscription é o handle de uma subscrição durável.
type Subscription interface {
	// Unsubscribe cancela a subscrição e liberta a goroutine de entrega e a
	// subscrição live subjacente. É idempotente e bloqueia até terminar. NÃO apaga
	// o cursor durável — uma nova subscrição com o mesmo Name retoma de onde ficou.
	Unsubscribe()
	// Name devolve o nome durável da subscrição.
	Name() string
}

// Option configura o Bus.
type Option func(*Bus)

// WithCursorStore injecta um CursorStore (por omissão MemoryCursorStore).
func WithCursorStore(cs CursorStore) Option {
	return func(b *Bus) { b.cursors = cs }
}

// WithObserver injecta um gancho de observabilidade.
func WithObserver(o Observer) Option {
	return func(b *Bus) { b.obs = o }
}

// withClock injecta um relógio (uso interno/testes).
func withClock(f func() time.Time) Option {
	return func(b *Bus) { b.now = f }
}

// Bus é o barramento de eventos push durável sobre um EventStore.
type Bus struct {
	es      eventstore.EventStore
	cursors CursorStore
	dlq     *DeadLetterQueue
	obs     Observer
	metrics Metrics
	now     func() time.Time

	mu     sync.Mutex
	subs   map[*subscription]struct{}
	closed bool
}

// New constrói um Bus que envolve es. Falha com ErrNilStore se es for nil.
func New(es eventstore.EventStore, opts ...Option) (*Bus, error) {
	if es == nil {
		return nil, ErrNilStore
	}
	b := &Bus{
		es:   es,
		obs:  nopObserver{},
		now:  time.Now,
		subs: make(map[*subscription]struct{}),
	}
	for _, o := range opts {
		o(b)
	}
	if b.cursors == nil {
		b.cursors = NewMemoryCursorStore()
	}
	b.dlq = newDeadLetterQueue(b.now)
	return b, nil
}

// Publish delega em EventStore.Append (pass-through fino). Fornecido por
// conveniência para produtores que já falam a linguagem do barramento; um
// produtor pode igualmente escrever directamente no Event Store.
func (b *Bus) Publish(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	return b.es.Append(ctx, streamID, in, opts...)
}

// DeadLetter devolve a dead-letter queue inspecionável do barramento.
func (b *Bus) DeadLetter() *DeadLetterQueue { return b.dlq }

// Metrics devolve os contadores agregados de entrega/latência.
func (b *Bus) Metrics() MetricsSnapshot { return b.metrics.Snapshot() }

// Subscribe regista uma subscrição durável e inicia a entrega push (catch-up →
// live). Ver a doc do pacote para a costura e o contrato at-least-once.
func (b *Bus) Subscribe(ctx context.Context, cfg SubConfig) (Subscription, error) {
	if cfg.Name == "" || cfg.Handler == nil || cfg.Buffer < 0 || cfg.Retry < 0 || !cfg.Overflow.valid() {
		return nil, ErrConfig
	}
	// Replay (FromSeq) exige um stream: a leitura histórica do Event Store é por
	// stream (Read(stream, fromSeq)) e o seq é POR stream. Pedir replay sem Streams
	// no filtro é irrealizável — falha-rápido em vez de silenciosamente entregar só
	// live a partir da subscrição (AOS-009-Q1). Ver a nota sobre subscrições sem
	// Streams na doc do pacote.
	if cfg.FromSeq != nil && len(cfg.Filter.Streams) == 0 {
		return nil, ErrConfig
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrClosed
	}
	b.mu.Unlock()

	bufMax := cfg.Buffer
	if bufMax == 0 {
		bufMax = defaultBuffer
	}

	subCtx, cancel := context.WithCancel(ctx)
	sub := &subscription{
		bus:       b,
		name:      cfg.Name,
		filter:    cfg.Filter,
		handler:   cfg.Handler,
		retry:     cfg.Retry,
		policy:    cfg.Overflow,
		bufMax:    bufMax,
		ctx:       subCtx,
		cancel:    cancel,
		watermark: make(map[string]uint64),
		cur:       make(map[string]*streamCursor),
		done:      make(chan struct{}),
	}
	sub.cond = sync.NewCond(&sub.mu)
	sub.notFull = sync.NewCond(&sub.mu)

	// Determina o seq de arranque por stream (FromSeq no replay, senão cursor+1)
	// e semeia o tracker de cursor em memória a partir do valor durável.
	starts := make(map[string]uint64, len(cfg.Filter.Streams))
	for _, stream := range cfg.Filter.Streams {
		var start uint64 = 1
		if cfg.FromSeq != nil {
			start = *cfg.FromSeq
		} else if last, ok := b.cursors.Load(cfg.Name, stream); ok {
			start = last + 1
			sub.cur[stream] = &streamCursor{upTo: last, acked: make(map[uint64]bool)}
		}
		if start < 1 {
			start = 1
		}
		starts[stream] = start
	}

	// Liga PRIMEIRO a subscrição live (passa a bufferizar), só depois lê o
	// histórico — é isto que garante a costura sem buracos (ver doc do pacote).
	esSub, err := b.es.Subscribe(subCtx, cfg.Filter.toEventStore(), sub.onLive)
	if err != nil {
		cancel()
		return nil, err
	}
	sub.esSub = esSub

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		esSub.Unsubscribe()
		cancel()
		return nil, ErrClosed
	}
	b.subs[sub] = struct{}{}
	b.mu.Unlock()

	go sub.run(starts)
	return sub, nil
}

// Close cancela todas as subscrições e liberta recursos. Não fecha o EventStore
// subjacente (o barramento não é dono dele). Idempotente.
func (b *Bus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return ErrClosed
	}
	b.closed = true
	subs := make([]*subscription, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.subs = make(map[*subscription]struct{})
	b.mu.Unlock()

	for _, s := range subs {
		s.shutdown()
	}
	return nil
}
