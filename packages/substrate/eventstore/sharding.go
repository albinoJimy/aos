package eventstore

import (
	"strings"
	"sync"
)

// AOS-100 — eliminação do single-writer.
//
// A ordem total do log é POR STREAM ((stream_id, seq), gapless desde 1), NUNCA
// global. Isto abre espaço para paralelizar a escrita ENTRE streams sem violar o
// contrato append-only. O modelo antigo serializava TODAS as escritas num único
// mutex global (o líder serializava tudo) — um ponto único de escrita (SPOF).
//
// stripeSet substitui esse serializador global por SERIALIZAÇÃO POR-STREAM: um
// conjunto fixo de locks listrados (striped locks). Duas escritas ao MESMO stream
// mapeiam para o mesmo lock e continuam serializadas (preservando seq gapless, o
// CAS de WithExpectedSeq, a dedup e a ordem de fanout desse stream); escritas a
// streams DIFERENTES mapeiam (quase sempre) para locks diferentes e correm EM
// PARALELO, sem contenção global. Uma colisão de stripe entre dois streams
// distintos apenas os serializa desnecessariamente — nunca é um problema de
// correcção, pois cada stream tem seq/CAS independentes.
//
// O número de stripes é uma potência de dois para permitir máscara em vez de
// módulo. É fixo (não cresce com o número de streams): limita o custo de memória
// e mantém o modelo determinista, ao contrário de um lock por stream criado
// dinamicamente.
type stripeSet struct {
	mask  uint64
	locks []sync.RWMutex
}

// newStripeSet constrói um conjunto de n stripes (arredondado para cima à potência
// de dois seguinte, mínimo 1).
func newStripeSet(n int) *stripeSet {
	size := 1
	for size < n {
		size <<= 1
	}
	return &stripeSet{
		mask:  uint64(size - 1),
		locks: make([]sync.RWMutex, size),
	}
}

// forStream devolve o lock listrado que serializa as operações do stream dado. O
// mesmo stream mapeia sempre para o mesmo lock (determinista).
func (s *stripeSet) forStream(stream string) *sync.RWMutex {
	return &s.locks[fnv1a(stream)&s.mask]
}

// fnv1a é o hash FNV-1a de 64 bits (inline, só stdlib) usado para distribuir os
// streams pelos stripes. É determinista e sem dependências externas.
func fnv1a(s string) uint64 {
	const (
		offset64 = 1469598103934665603
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return h
}

// --- Soberania regional (ADR-011) -----------------------------------------

// normalizeRegion devolve a forma canónica de uma região/board para comparação
// estável e case-insensitive (espaços aparados, caixa reduzida), coerente com o
// enforcement de soberania do plano de controlo (referencemonitor.enforceRegion,
// governance/sovereignty). Uma região vazia após aparar permanece vazia — e uma
// região vazia NUNCA autoriza (fail-closed).
func normalizeRegion(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
