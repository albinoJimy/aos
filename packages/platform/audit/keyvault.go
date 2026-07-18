package audit

import "sync"

// keyRefPrefix separa o domínio das chaves de PII por titular do audit de
// qualquer outro material de chave, e torna a KeyRef derivável do SubjectID de
// forma determinística — o [Shredder] localiza a chave a destruir a partir do
// titular sem indexação lateral.
const keyRefPrefix = "aos.audit.pii:"

// KeyRefFor devolve a KeyRef canónica da chave de PII de um titular. É a única
// fonte da correspondência SubjectID↔KeyRef usada pelo vault, pela ingestão e pelo
// shredder — mantendo-os consistentes sem estado partilhado adicional.
func KeyRefFor(subjectID string) string { return keyRefPrefix + subjectID }

// KeyVault é a PORTA das chaves de PII por titular que suportam o crypto-shredding
// (ADR-011). A PII de cada registo é cifrada por envelope sob a KEK do seu titular;
// APAGAR essa KEK ([Delete]) torna o plaintext IRRECUPERÁVEL, sem mutar nada na
// hash-chain (que sela o HASH do ciphertext, não o plaintext). É a fronteira
// estável: produção liga um KMS/HSM real por trás desta mesma interface, sem
// alterar a ingestão nem os verificadores. A chave privada real NUNCA vive no repo.
type KeyVault interface {
	// EnsureKey devolve a KEK do titular e a sua KeyRef, provisionando-a (via
	// RandSource) na primeira escrita e devolvendo a existente nas seguintes
	// (idempotente). É o caminho de ESCRITA/ingestão de um registo com PII.
	EnsureKey(subjectID string) (key []byte, keyRef string, err error)
	// Key devolve a KEK identificada por keyRef e se ela existe. É o caminho de
	// LEITURA (decifragem): se a chave foi shredded, ok=false e a PII é irrecuperável.
	Key(keyRef string) (key []byte, ok bool)
	// Delete apaga a KEK do titular (crypto-shredding, GDPR Art. 17). Idempotente:
	// apagar o que não existe é no-op.
	Delete(subjectID string)
}

// InMemoryKeyVault é a implementação de referência do [KeyVault]: KEKs por titular
// em memória, segura para concorrência. É a implementação MVP; produção liga um KMS
// real (envelope encryption com chaves geridas) por trás da mesma porta.
type InMemoryKeyVault struct {
	mu   sync.Mutex
	rand RandSource
	keys map[string][]byte // indexado por KeyRef
}

// NewInMemoryKeyVault constrói um vault vazio. randSrc nil cai em crypto/rand
// (produção); os testes injectam uma fonte determinística.
func NewInMemoryKeyVault(randSrc RandSource) *InMemoryKeyVault {
	if randSrc == nil {
		randSrc = cryptoRand
	}
	return &InMemoryKeyVault{rand: randSrc, keys: make(map[string][]byte)}
}

// EnsureKey implementa [KeyVault].
func (v *InMemoryKeyVault) EnsureKey(subjectID string) ([]byte, string, error) {
	ref := KeyRefFor(subjectID)
	v.mu.Lock()
	defer v.mu.Unlock()
	if key, ok := v.keys[ref]; ok {
		return cloneBytes(key), ref, nil
	}
	key := make([]byte, kekSize)
	if err := v.rand(key); err != nil {
		return nil, "", err
	}
	v.keys[ref] = key
	return cloneBytes(key), ref, nil
}

// Key implementa [KeyVault].
func (v *InMemoryKeyVault) Key(keyRef string) ([]byte, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	key, ok := v.keys[keyRef]
	if !ok {
		return nil, false
	}
	return cloneBytes(key), true
}

// Delete implementa [KeyVault] (crypto-shredding, idempotente).
func (v *InMemoryKeyVault) Delete(subjectID string) {
	ref := KeyRefFor(subjectID)
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.keys, ref)
}

// PayloadStore guarda o CIPHERTEXT de envelope da PII FORA da hash-chain, indexado
// por uma referência opaca (o hex do ContentHash selado). A cadeia sela só o hash;
// o ciphertext vive aqui. Após o crypto-shredding a chave desaparece do [KeyVault]
// e este blob, embora ainda presente, deixa de ser decifrável — a separação entre
// "o facto selado" (na cadeia) e "o conteúdo pessoal" (aqui, cifrado) é o que
// reconcilia imutabilidade com o direito ao apagamento.
type PayloadStore interface {
	// Put guarda o blob cifrado sob a referência dada (idempotente por referência:
	// o mesmo ciphertext produz a mesma referência).
	Put(ref string, blob []byte)
	// Get devolve o blob cifrado e se ele existe.
	Get(ref string) ([]byte, bool)
}

// SubjectPartitionIndex mapeia cada titular às partições onde tem PISI selada, para
// que o [Shredder] possa fazer valer o legal hold POR PARTIÇÃO. Sem este índice o
// shredder não sabe que partições cobrem um titular e só o hold por-titular é
// executável — por isso o índice tem de ser ligado ao pipeline (que o alimenta na
// ingestão via [IngestPipeline.Ingest]) E ao shredder (que o consulta no shred).
// A KEK do crypto-shredding é POR-TITULAR e a sua destruição é global (não por
// partição): logo, se QUALQUER partição que contém dados do titular estiver sob
// legal hold, o shred inteiro é recusado (fail-closed) — destruí-la tornaria
// irrecuperável também a prova retida nessa partição.
type SubjectPartitionIndex interface {
	// Link regista que subjectID tem dados selados em partition (idempotente).
	Link(subjectID, partition string)
	// Partitions devolve as partições onde subjectID tem dados selados.
	Partitions(subjectID string) []string
}

// InMemorySubjectPartitionIndex é a implementação de referência do
// [SubjectPartitionIndex], segura para concorrência. Produção liga um índice
// persistente (derivável dos PayloadRef selados) por trás da mesma porta.
type InMemorySubjectPartitionIndex struct {
	mu      sync.Mutex
	subject map[string]map[string]bool // subjectID → conjunto de partições
}

// NewInMemorySubjectPartitionIndex constrói um índice vazio.
func NewInMemorySubjectPartitionIndex() *InMemorySubjectPartitionIndex {
	return &InMemorySubjectPartitionIndex{subject: make(map[string]map[string]bool)}
}

// Link implementa [SubjectPartitionIndex].
func (i *InMemorySubjectPartitionIndex) Link(subjectID, partition string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	parts := i.subject[subjectID]
	if parts == nil {
		parts = make(map[string]bool)
		i.subject[subjectID] = parts
	}
	parts[partition] = true
}

// Partitions implementa [SubjectPartitionIndex].
func (i *InMemorySubjectPartitionIndex) Partitions(subjectID string) []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	parts := i.subject[subjectID]
	out := make([]string, 0, len(parts))
	for p := range parts {
		out = append(out, p)
	}
	return out
}

// InMemoryPayloadStore é a implementação de referência do [PayloadStore], segura
// para concorrência. Produção liga um object store real por trás da mesma porta.
type InMemoryPayloadStore struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

// NewInMemoryPayloadStore constrói um payload store vazio.
func NewInMemoryPayloadStore() *InMemoryPayloadStore {
	return &InMemoryPayloadStore{blobs: make(map[string][]byte)}
}

// Put implementa [PayloadStore].
func (s *InMemoryPayloadStore) Put(ref string, blob []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blobs[ref] = cloneBytes(blob)
}

// Get implementa [PayloadStore].
func (s *InMemoryPayloadStore) Get(ref string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	blob, ok := s.blobs[ref]
	if !ok {
		return nil, false
	}
	return cloneBytes(blob), true
}
