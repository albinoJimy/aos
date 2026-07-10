package bus

import (
	"sync"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// DeadLetter é uma entrada da dead-letter queue: um evento que não pôde ser
// entregue com sucesso (Handler falhou mais do que Retry vezes) ou que foi
// descartado pela política de overflow DeadLetter.
type DeadLetterEntry struct {
	// Subscriber é o Name da subscrição de onde o evento saiu.
	Subscriber string
	// Event é o envelope original (cópia).
	Event eventstore.Event
	// Cause é o último erro reportado pelo Handler (via Delivery.Nack), ou nil se
	// a origem foi overflow.
	Cause error
	// Attempts é o número de tentativas de entrega feitas antes de desistir.
	Attempts int
	// Reason distingue a origem: "handler" (falha repetida) ou "overflow".
	Reason string
	// Ts é o instante em que a entrada foi criada.
	Ts time.Time
}

// DeadLetterQueue é uma fila inspecionável e segura para concorrência. É
// deliberadamente in-memory no modelo de referência; em produção é um stream
// dedicado (ex.: dead-letter subject no NATS).
type DeadLetterQueue struct {
	mu      sync.Mutex
	entries []DeadLetterEntry
	now     func() time.Time
}

func newDeadLetterQueue(now func() time.Time) *DeadLetterQueue {
	return &DeadLetterQueue{now: now}
}

func (q *DeadLetterQueue) add(dl DeadLetterEntry) {
	// Event já é uma cópia própria: o Event Store devolve clones em Read/Subscribe.
	dl.Ts = q.now()
	q.mu.Lock()
	q.entries = append(q.entries, dl)
	q.mu.Unlock()
}

// Len devolve o número de entradas na fila.
func (q *DeadLetterQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.entries)
}

// Entries devolve uma cópia das entradas por ordem de chegada.
func (q *DeadLetterQueue) Entries() []DeadLetterEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]DeadLetterEntry, len(q.entries))
	copy(out, q.entries)
	return out
}
