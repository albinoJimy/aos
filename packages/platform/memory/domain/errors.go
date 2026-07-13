package domain

// MemoryError é o tipo de erro sentinela do domínio de memória. Carrega um código
// estável, legível-por-máquina, comparável com errors.Is por identidade. TODA a
// validação de metadados obrigatórios resolve-se pelo lado seguro (fail-closed):
// a ausência de um metadado obrigatório é sempre rejeição, NUNCA um default
// silencioso.
type MemoryError struct {
	Code string
	msg  string
}

func (e *MemoryError) Error() string { return e.Code + ": " + e.msg }

// Sentinelas de erro. Comparáveis com errors.Is, inclusive quando embrulhados
// com fmt.Errorf("%w: …", …).
var (
	// ErrMissingAgentID — metadado obrigatório agent_id em falta.
	ErrMissingAgentID = &MemoryError{Code: "E_MEM_MISSING_AGENT_ID", msg: "agent_id obrigatorio em falta"}

	// ErrMissingRunID — metadado obrigatório run_id em falta (também raiz da
	// idempotência f(run_id, mem_id)).
	ErrMissingRunID = &MemoryError{Code: "E_MEM_MISSING_RUN_ID", msg: "run_id obrigatorio em falta"}

	// ErrMissingProvenance — proveniência em falta ou não canónica. Fail-closed:
	// uma escrita sem trusted/untrusted é sempre rejeitada (prepara AOS-042).
	ErrMissingProvenance = &MemoryError{Code: "E_MEM_MISSING_PROVENANCE", msg: "provenance obrigatoria em falta (trusted|untrusted)"}

	// ErrMissingCreatedAt — created_at em falta (zero). O relógio é injectável na
	// fachada; a ausência num registo que chega à porta é fail-closed.
	ErrMissingCreatedAt = &MemoryError{Code: "E_MEM_MISSING_CREATED_AT", msg: "created_at obrigatorio em falta"}

	// ErrMissingTTLClass — ttl_class em falta ou não canónica (prepara GDPR/TTL,
	// ADR-011).
	ErrMissingTTLClass = &MemoryError{Code: "E_MEM_MISSING_TTL_CLASS", msg: "ttl_class obrigatoria em falta"}

	// ErrMissingSchemaVersion — schema_version em falta. Fail-closed: uma escrita
	// sem versão de schema é sempre rejeitada (prepara o versionamento AOS-041).
	ErrMissingSchemaVersion = &MemoryError{Code: "E_MEM_MISSING_SCHEMA_VERSION", msg: "schema_version obrigatoria em falta"}

	// ErrInvalidClass — classe de memória desconhecida (nem episodic/semantic/
	// procedural/working).
	ErrInvalidClass = &MemoryError{Code: "E_MEM_INVALID_CLASS", msg: "classe de memoria invalida"}

	// ErrMissingID — o registo não tem identidade (id vazio). A fachada pode
	// atribuir um id; um registo que chega à porta sem id é fail-closed.
	ErrMissingID = &MemoryError{Code: "E_MEM_MISSING_ID", msg: "id de registo obrigatorio em falta"}

	// ErrNilBody — o registo não tem corpo tipado.
	ErrNilBody = &MemoryError{Code: "E_MEM_NIL_BODY", msg: "corpo tipado do registo em falta"}

	// ErrClassMismatch — o corpo tipado não pertence à classe declarada no registo
	// (ex.: Class=semantic mas Body é EpisodicBody). Garante que as quatro classes
	// não se cruzam ao nível do domínio.
	ErrClassMismatch = &MemoryError{Code: "E_MEM_CLASS_MISMATCH", msg: "corpo tipado nao corresponde a classe declarada"}

	// ErrNotFound — pedido Get de um registo inexistente (ou já apagado por
	// tombstone) na classe dada.
	ErrNotFound = &MemoryError{Code: "E_MEM_NOT_FOUND", msg: "registo de memoria inexistente"}

	// ErrCorruptRecord — um evento do log não descodifica para um registo válido
	// (envelope corrompido). Fail-closed na reconstrução.
	ErrCorruptRecord = &MemoryError{Code: "E_MEM_CORRUPT_RECORD", msg: "registo de memoria corrompido no log"}
)
