package memory

import (
	"context"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/ports"
)

// Service é a FACHADA do Memory Service (MEM). Assenta sobre uma MemoryPort
// (backend-agnóstica) e acrescenta duas responsabilidades de sistema:
//
//   - preenche os campos SUPRIDOS PELO SISTEMA — CreatedAt (relógio injectável)
//     e, opcionalmente, o ID (gerador injectável) — de forma determinística, sem
//     time.Now/rand no caminho de decisão;
//   - valida os metadados obrigatórios ANTES de tocar no backend (fail-closed).
//
// A fachada NUNCA supre provenance nem schema_version: esses têm de vir do
// chamador, e a sua ausência falha-fecha (é isto que preserva a garantia de
// segurança/proveniência ao nível do serviço, não só do adaptador).
type Service struct {
	port  ports.MemoryPort
	now   func() time.Time
	newID func() string
}

// Option configura o Service.
type Option func(*Service)

// WithClock injecta o relógio usado para preencher CreatedAt (default time.Now).
// Injectar um relógio fixo torna a escrita determinística em testes.
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// WithIDGenerator injecta o gerador de IDs usado quando o chamador não fornece um
// (default: um contador monotónico interno, sem rand — determinístico por
// instância). O gerador só é usado por Remember; Put respeita o ID que receber.
func WithIDGenerator(gen func() string) Option {
	return func(s *Service) {
		if gen != nil {
			s.newID = gen
		}
	}
}

// NewService constrói a fachada sobre uma porta. Uma porta nil é erro de
// programação (as operações entrariam em panic); o chamador deve fornecer um dos
// adaptadores.
func NewService(port ports.MemoryPort, opts ...Option) *Service {
	var counter atomic.Uint64
	s := &Service{
		port: port,
		now:  time.Now,
		newID: func() string {
			return "mem-" + strconv.FormatUint(counter.Add(1), 10)
		},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// PortVersion devolve a versão do contrato da porta subjacente.
func (s *Service) PortVersion() string { return s.port.Version() }

// Remember assembla e escreve um registo de uma classe. Preenche CreatedAt pelo
// relógio injectado (se zero) e o ID pelo gerador (se vazio), valida (fail-closed)
// e delega em Put. É o caminho ergonómico; o Body tem de corresponder à classe.
//
// provenance/schema_version/agent_id/run_id/ttl_class vêm SEMPRE do chamador (via
// meta): a fachada não os inventa. Sem provenance OU schema_version, devolve erro.
func (s *Service) Remember(ctx context.Context, class domain.MemoryClass, meta domain.Metadata, body domain.Body) (domain.Record, error) {
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = s.now()
	}
	rec := domain.Record{
		ID:       s.newID(),
		Class:    class,
		Metadata: meta,
		Body:     body,
	}
	if err := rec.Validate(); err != nil {
		return domain.Record{}, err
	}
	return s.port.Put(ctx, rec)
}

// Put escreve um registo já montado pelo chamador (respeita o ID fornecido).
// Preenche CreatedAt pelo relógio se estiver a zero; valida e delega na porta.
func (s *Service) Put(ctx context.Context, rec domain.Record) (domain.Record, error) {
	if rec.Metadata.CreatedAt.IsZero() {
		rec.Metadata.CreatedAt = s.now()
	}
	if err := rec.Validate(); err != nil {
		return domain.Record{}, err
	}
	return s.port.Put(ctx, rec)
}

// Get devolve o registo (class,id) ou domain.ErrNotFound.
func (s *Service) Get(ctx context.Context, class domain.MemoryClass, id string) (domain.Record, error) {
	return s.port.Get(ctx, class, id)
}

// Query devolve os registos da classe que satisfazem os filtros.
func (s *Service) Query(ctx context.Context, q ports.Query) ([]domain.Record, error) {
	return s.port.Query(ctx, q)
}

// Delete marca (class,id) como apagado (tombstone no backend append-only). O
// DeleteContext (agent_id/run_id/provenance) é obrigatório e atribui a remoção no
// log de audit; a sua ausência falha-fecha na porta.
func (s *Service) Delete(ctx context.Context, class domain.MemoryClass, id string, dc ports.DeleteContext) error {
	return s.port.Delete(ctx, class, id, dc)
}
