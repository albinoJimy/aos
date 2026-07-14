package migrations

import (
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/schema"
)

// Transform converte um registo de uma versão de schema para outra. É uma função
// PURA e DETERMINÍSTICA: a mesma entrada produz sempre a mesma saída, sem relógio,
// sem aleatoriedade e sem estado partilhado. O transform é responsável por estampar
// o schema_version alvo nos metadados do registo que devolve (o motor verifica-o,
// fail-closed).
type Transform func(domain.Record) (domain.Record, error)

// Migration é a definição declarativa de uma migração de schema de UMA classe,
// entre duas versões SemVer. É bidireccional por construção — Up migra From→To e
// Down migra To→From — porque a REVERSIBILIDADE de cada fase é um requisito
// inegociável (AOS-041): o dual-write escreve nas duas formas, o rollback recompõe
// a forma antiga a partir da nova, e o motor nunca depende de um caminho só de ida.
type Migration struct {
	// ID é a identidade estável da migração; raiz da idempotency key do registo
	// durável (coerente com ADR-001). Reaplicar a mesma migração é um no-op.
	ID string
	// Class é a classe de memória cujo schema a migração evolui.
	Class domain.MemoryClass
	// From e To são as versões de schema de origem e destino (SemVer).
	From schema.Version
	To   schema.Version
	// Up migra um registo From→To. Down migra To→From (inverso; reversibilidade).
	Up   Transform
	Down Transform
}

// Kind classifica a mudança de contrato (MAJOR/MINOR/PATCH) entre From e To. É o
// que o motor consulta para decidir se a migração precisa de passar pelo eval-gate:
// só ChangeMajor (quebra de contrato) exige aprovação.
func (m Migration) Kind() schema.ChangeKind {
	return schema.Classify(m.From, m.To)
}

// Validate impõe as invariantes fail-closed da definição. Uma migração inválida
// nunca é executada.
func (m Migration) Validate() error {
	if m.ID == "" {
		return ErrInvalidMigration
	}
	if !m.Class.Valid() {
		return ErrInvalidMigration
	}
	if m.Up == nil || m.Down == nil {
		return ErrInvalidMigration
	}
	if m.From.Equal(m.To) {
		return ErrInvalidMigration
	}
	return nil
}
