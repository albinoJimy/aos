package domain

// Record é a unidade de memória do AOS: identidade + classe + metadados
// obrigatórios + corpo tipado da classe. É a única entidade que atravessa a
// porta (MemoryPort) e o único formato que os adaptadores persistem/reconstroem.
//
// Invariantes (impostas por Validate, fail-closed):
//   - ID não vazio;
//   - Class é uma das quatro classes canónicas;
//   - Body não nil e Body.Class() == Class (as classes não se cruzam);
//   - Metadata completos (todos os metadados obrigatórios preenchidos).
type Record struct {
	// ID é a identidade do registo DENTRO da sua classe. A idempotência de escrita
	// é f(RunID, Class, ID); (Class, ID) é a chave de leitura por Get.
	ID string
	// Class é a classe de memória a que o registo pertence.
	Class MemoryClass
	// Metadata são os metadados obrigatórios (ver Metadata.Validate).
	Metadata Metadata
	// Body é o corpo tipado específico da classe.
	Body Body
}

// Validate impõe todas as invariantes do registo (fail-closed). Devolve o
// primeiro erro sentinela encontrado, por ordem estável.
func (r Record) Validate() error {
	if r.ID == "" {
		return ErrMissingID
	}
	if !r.Class.Valid() {
		return ErrInvalidClass
	}
	if r.Body == nil {
		return ErrNilBody
	}
	if r.Body.Class() != r.Class {
		return ErrClassMismatch
	}
	return r.Metadata.Validate()
}

// Clone devolve uma cópia independente do registo. Os adaptadores devolvem
// sempre clones para que o estado guardado nunca seja partilhado com o chamador
// (fronteira análoga ao clone append-only do Event Store).
func (r Record) Clone() Record {
	cp := r
	if r.Body != nil {
		cp.Body = r.Body.clone()
	}
	return cp
}
