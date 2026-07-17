package otelgenai

import (
	"crypto/rand"
	"encoding/binary"
	"io"
	"sync"
)

// IDGenerator produz os identificadores de trace/span. É injectável para que os
// testes usem uma fonte determinística ([SequentialIDGenerator]) e a produção
// use uma CSPRNG ([CryptoIDGenerator] sobre crypto/rand). Segue o padrão do repo
// de fonte de aleatoriedade injectável (nunca math/rand nem rand global).
type IDGenerator interface {
	// NewTraceID devolve um trace_id de 16 bytes, nunca todo-zero (senão o
	// SpanContext seria inválido).
	NewTraceID() [16]byte
	// NewSpanID devolve um span_id de 8 bytes, nunca todo-zero.
	NewSpanID() [8]byte
}

// CryptoIDGenerator gera ids a partir de uma io.Reader (crypto/rand em produção).
// É seguro para concorrência.
type CryptoIDGenerator struct {
	mu sync.Mutex
	r  io.Reader
}

// NewCryptoIDGenerator constrói um gerador sobre r. Se r for nil usa
// crypto/rand.Reader (a CSPRNG do SO). r é injectável para testes deterministas.
func NewCryptoIDGenerator(r io.Reader) *CryptoIDGenerator {
	if r == nil {
		r = rand.Reader
	}
	return &CryptoIDGenerator{r: r}
}

// NewTraceID implementa [IDGenerator].
func (g *CryptoIDGenerator) NewTraceID() [16]byte {
	var id [16]byte
	g.mu.Lock()
	// Uma leitura curta/erro da fonte NÃO deve emitir um id degenerado (todo-zero):
	// o fallback garante o invariante de validade sem propagar erro pela porta.
	if _, err := io.ReadFull(g.r, id[:]); err != nil {
		id[15] = 1
	}
	g.mu.Unlock()
	if id == ([16]byte{}) {
		id[15] = 1
	}
	return id
}

// NewSpanID implementa [IDGenerator].
func (g *CryptoIDGenerator) NewSpanID() [8]byte {
	var id [8]byte
	g.mu.Lock()
	if _, err := io.ReadFull(g.r, id[:]); err != nil {
		id[7] = 1
	}
	g.mu.Unlock()
	if id == ([8]byte{}) {
		id[7] = 1
	}
	return id
}

// SequentialIDGenerator produz ids deterministas por contador, para testes que
// asserem a topologia da árvore (trace comum, parent_span_id) sem depender de
// valores aleatórios. É seguro para concorrência. NUNCA usar em produção.
type SequentialIDGenerator struct {
	mu    sync.Mutex
	trace uint64
	span  uint64
}

// NewTraceID implementa [IDGenerator]: contador big-endian nos 8 bytes baixos.
func (g *SequentialIDGenerator) NewTraceID() [16]byte {
	g.mu.Lock()
	g.trace++
	v := g.trace
	g.mu.Unlock()
	var id [16]byte
	binary.BigEndian.PutUint64(id[8:], v)
	return id
}

// NewSpanID implementa [IDGenerator]: contador big-endian nos 8 bytes.
func (g *SequentialIDGenerator) NewSpanID() [8]byte {
	g.mu.Lock()
	g.span++
	v := g.span
	g.mu.Unlock()
	var id [8]byte
	binary.BigEndian.PutUint64(id[:], v)
	return id
}
