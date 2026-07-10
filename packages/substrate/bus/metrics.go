package bus

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// Observer é um gancho de observabilidade leve (opcional). Não puxa o SDK OTel
// (isso é EPIC-08); serve para métricas de entrega, latência e degradação em
// testes e operação. O Observer por omissão é no-op.
type Observer interface {
	// Delivered é chamado imediatamente antes de cada invocação do Handler.
	// latency é o tempo desde o Event.Ts (commit) até à entrega.
	Delivered(sub string, ev eventstore.Event, latency time.Duration)
	// Acked é chamado quando uma entrega é confirmada e o cursor avança.
	Acked(sub, stream string, seq uint64)
	// DeadLettered é chamado quando um evento vai para a dead-letter queue.
	DeadLettered(sub string, ev eventstore.Event, cause error)
	// Dropped é chamado quando a política de overflow descarta um evento.
	Dropped(sub string, ev eventstore.Event, policy OverflowPolicy)
}

type nopObserver struct{}

func (nopObserver) Delivered(string, eventstore.Event, time.Duration) {}
func (nopObserver) Acked(string, string, uint64)                      {}
func (nopObserver) DeadLettered(string, eventstore.Event, error)      {}
func (nopObserver) Dropped(string, eventstore.Event, OverflowPolicy)  {}

// latReservoirCap é a capacidade do reservatório circular de latências que
// alimenta os percentis. Limita a memória (bounded) e reflecte o comportamento
// recente — é observável em produção, não só dentro de um teste.
const latReservoirCap = 8192

// latReservoir é um buffer circular protegido por mutex que guarda as últimas
// latências observadas (em nanosegundos) para o cálculo EXACTO de percentis
// sobre a janela recente. É seguro para escrita concorrente (várias subscrições
// observam em paralelo) e para leitura no Snapshot.
type latReservoir struct {
	mu     sync.Mutex
	buf    []uint64
	next   int
	filled bool
}

func (r *latReservoir) add(n uint64) {
	r.mu.Lock()
	if r.buf == nil {
		r.buf = make([]uint64, latReservoirCap)
	}
	r.buf[r.next] = n
	r.next++
	if r.next == len(r.buf) {
		r.next = 0
		r.filled = true
	}
	r.mu.Unlock()
}

// percentile devolve o percentil p (0..1) EXACTO sobre o conteúdo actual do
// reservatório. Devolve 0 se ainda não houver amostras.
func (r *latReservoir) percentile(p float64) time.Duration {
	r.mu.Lock()
	var n int
	if r.filled {
		n = len(r.buf)
	} else {
		n = r.next
	}
	if n == 0 {
		r.mu.Unlock()
		return 0
	}
	sample := make([]uint64, n)
	copy(sample, r.buf[:n])
	r.mu.Unlock()

	sort.Slice(sample, func(i, j int) bool { return sample[i] < sample[j] })
	idx := int(float64(n-1) * p)
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return time.Duration(sample[idx])
}

// Metrics são contadores agregados do barramento, expostos via Bus.Metrics. São
// seguros para leitura concorrente.
type Metrics struct {
	delivered    atomic.Uint64
	acked        atomic.Uint64
	nacked       atomic.Uint64
	deadlettered atomic.Uint64
	dropped      atomic.Uint64
	// latência de entrega agregada (nanosegundos) para cálculo de média/máximo.
	latSumNanos atomic.Uint64
	latMaxNanos atomic.Uint64
	latCount    atomic.Uint64
	// reservatório de amostras recentes para percentis observáveis (p50/p95/p99).
	latSamples latReservoir
}

func (m *Metrics) observeLatency(d time.Duration) {
	if d < 0 {
		d = 0
	}
	n := uint64(d.Nanoseconds())
	m.latSumNanos.Add(n)
	m.latCount.Add(1)
	m.latSamples.add(n)
	for {
		cur := m.latMaxNanos.Load()
		if n <= cur || m.latMaxNanos.CompareAndSwap(cur, n) {
			break
		}
	}
}

// MetricsSnapshot é uma leitura instantânea dos contadores.
type MetricsSnapshot struct {
	Delivered    uint64
	Acked        uint64
	Nacked       uint64
	DeadLettered uint64
	Dropped      uint64
	AvgLatency   time.Duration
	MaxLatency   time.Duration
	// Percentis de latência de entrega, calculados sobre a janela recente de
	// amostras. Ao contrário de Avg/Max, o p95 (alvo de AC5) fica OBSERVÁVEL em
	// produção via Bus.Metrics(), não só dentro de um teste.
	P50Latency time.Duration
	P95Latency time.Duration
	P99Latency time.Duration
}

// Snapshot devolve uma leitura consistente-o-suficiente dos contadores.
func (m *Metrics) Snapshot() MetricsSnapshot {
	cnt := m.latCount.Load()
	var avg time.Duration
	if cnt > 0 {
		avg = time.Duration(m.latSumNanos.Load() / cnt)
	}
	return MetricsSnapshot{
		Delivered:    m.delivered.Load(),
		Acked:        m.acked.Load(),
		Nacked:       m.nacked.Load(),
		DeadLettered: m.deadlettered.Load(),
		Dropped:      m.dropped.Load(),
		AvgLatency:   avg,
		MaxLatency:   time.Duration(m.latMaxNanos.Load()),
		P50Latency:   m.latSamples.percentile(0.50),
		P95Latency:   m.latSamples.percentile(0.95),
		P99Latency:   m.latSamples.percentile(0.99),
	}
}
