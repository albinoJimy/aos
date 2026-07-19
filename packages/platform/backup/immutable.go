package backup

import (
	"sync"
	"time"

	"github.com/aos-ref/platform/audit"
)

// ImmutableStore é a PORTA do armazenamento imutável de segmentos (object
// storage com object-lock/WORM). Ao contrário do audit.PayloadStore — cuja
// implementação SOBRESCREVE a mesma referência — aqui o Put é WRITE-ONCE: escrever
// numa referência já existente é recusado ([ErrImmutable]). É a fronteira estável:
// produção liga S3 Object Lock / GCS retention / Azure immutable blob por trás
// desta mesma interface, sem alterar o exportador nem o restaurador.
//
// A Region do destino participa no enforcement de soberania (ADR-011): o
// exportador recusa fail-closed um destino que cruze a fronteira do board.
type ImmutableStore interface {
	// Put escreve blob sob ref de forma WRITE-ONCE, com um object-lock até
	// retainUntil (o objecto não pode ser removido antes desse instante). Uma
	// segunda escrita à MESMA ref devolve [ErrImmutable] — o segmento é imutável.
	Put(ref string, blob []byte, retainUntil time.Time) error
	// Get devolve o blob imutável sob ref, ou [ErrNotFound].
	Get(ref string) ([]byte, error)
	// Delete remove o objecto SÓ se o object-lock já expirou (now >= retainUntil) e
	// não há legal hold activo; caso contrário devolve [ErrObjectLocked]. Modela a
	// expiração de retenção do WORM sem violar a imutabilidade dentro do período.
	Delete(ref string, now time.Time) error
	// Region devolve a região de soberania do destino (normalizada). "" = destino
	// sem região declarada (tratado como desconhecido ⇒ deny pelo exportador).
	Region() string
}

// objectRecord é um objecto imutável e o seu object-lock.
type objectRecord struct {
	blob        []byte
	retainUntil time.Time
}

// InMemoryImmutableStore é a implementação de referência do [ImmutableStore]:
// write-once em memória, com object-lock por objecto e legal hold opcional
// (reutiliza audit.LegalHold — o mesmo primitivo de preservação do audit).
// Segura para concorrência.
type InMemoryImmutableStore struct {
	region  string
	mu      sync.Mutex
	objects map[string]objectRecord
	hold    *audit.LegalHold // opcional; se a partição holdKey estiver retida, Delete é recusado
	holdKey string
}

// ImmutableOption configura o InMemoryImmutableStore.
type ImmutableOption func(*InMemoryImmutableStore)

// WithLegalHold liga um audit.LegalHold ao store: enquanto a partição holdKey
// estiver sob hold, nenhuma remoção é permitida (preservação para litígio,
// ADR-010/011), independentemente do object-lock ter expirado.
func WithLegalHold(hold *audit.LegalHold, holdKey string) ImmutableOption {
	return func(s *InMemoryImmutableStore) {
		s.hold = hold
		s.holdKey = holdKey
	}
}

// NewInMemoryImmutableStore constrói um store imutável para a região dada.
func NewInMemoryImmutableStore(region string, opts ...ImmutableOption) *InMemoryImmutableStore {
	s := &InMemoryImmutableStore{
		region:  normalizeRegion(region),
		objects: make(map[string]objectRecord),
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Region implementa [ImmutableStore].
func (s *InMemoryImmutableStore) Region() string { return s.region }

// Put implementa [ImmutableStore] (write-once).
func (s *InMemoryImmutableStore) Put(ref string, blob []byte, retainUntil time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.objects[ref]; exists {
		return ErrImmutable
	}
	cp := make([]byte, len(blob))
	copy(cp, blob)
	s.objects[ref] = objectRecord{blob: cp, retainUntil: retainUntil}
	return nil
}

// Get implementa [ImmutableStore].
func (s *InMemoryImmutableStore) Get(ref string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.objects[ref]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]byte, len(rec.blob))
	copy(out, rec.blob)
	return out, nil
}

// Delete implementa [ImmutableStore]: recusado dentro do object-lock ou sob hold.
func (s *InMemoryImmutableStore) Delete(ref string, now time.Time) error {
	if s.hold != nil && s.holdKey != "" && s.hold.HeldPartition(s.holdKey) {
		return ErrObjectLocked
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.objects[ref]
	if !ok {
		return ErrNotFound
	}
	if now.Before(rec.retainUntil) {
		return ErrObjectLocked
	}
	delete(s.objects, ref)
	return nil
}

// Len devolve o número de objectos (uso em testes/observação).
func (s *InMemoryImmutableStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}

// retainUntilFor computa o instante de expiração do object-lock a partir de uma
// política de retenção (reutiliza audit.RetentionPolicy). Sem período definido
// para a classe, o object-lock é "para sempre" dentro do modelo (retainUntil no
// futuro distante) — fail-closed: não se auto-purga o que não tem período.
func retainUntilFor(policy audit.RetentionPolicy, class audit.DataClass, now time.Time) time.Time {
	if d, ok := policy.Period(class); ok {
		return now.Add(d)
	}
	// Sem período: nunca expira automaticamente (piso muito no futuro).
	return now.Add(100 * 365 * 24 * time.Hour)
}
