package replay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
)

// ---------------------------------------------------------------------------
// CONTENT-CAPTURE MODE 3 (AOS-079) — PayloadStore com IAM PRÓPRIO.
//
// O evento "replay.captured" do Event Store deve ficar PEQUENO e fora do caminho
// quente: o payload não-determinístico COMPLETO (resposta do modelo + resultados
// de tools) reside num STORAGE EXTERNO com um controlo de acesso SEPARADO do
// escritor do Event Store (OTel content-capture "mode 3"). O evento do ES / o span
// carregam apenas uma REFERÊNCIA opaca e verificável por hash.
//
// Este ficheiro define:
//   - a PORTA [PayloadStore] (Put/Get) cujo Get impõe um IAM PRÓPRIO — um accessor
//     não autorizado é NEGADO fail-closed ([ErrPayloadAccessDenied]);
//   - [PayloadRef], referência opaca content-addressable (o digest É sha256 do
//     conteúdo — a referência PROVA a integridade);
//   - [InMemoryPayloadStore], a impl de REFERÊNCIA zero-dep/offline que MODELA o
//     IAM (escopos de leitura/escrita distintos). O adapter real (ex.: S3 +
//     política IAM) é diferido para deployment.
// ---------------------------------------------------------------------------

// PayloadRef é uma referência OPACA e verificável por hash a um payload no
// [PayloadStore]. É content-addressable: Digest == sha256(payload) no formato
// "sha256:<hex>". Resolver e re-hashar o conteúdo prova a sua integridade — a
// referência não é um mero ponteiro, é um compromisso criptográfico sobre os bytes.
type PayloadRef struct {
	Digest string `json:"digest"`
}

// IsZero indica se a referência é vazia (sem payload externo associado).
func (r PayloadRef) IsZero() bool { return r.Digest == "" }

// Accessor identifica o principal e os escopos com que se acede ao [PayloadStore].
// O content-capture mode 3 exige que o payload viva atrás de um IAM PRÓPRIO: o
// principal/escopo que LÊ o payload é SEPARADO do que ESCREVE o Event Store. Um
// accessor sem o escopo exigido é negado fail-closed no Get.
type Accessor struct {
	// Principal é a identidade (NHI/serviço) que acede ao store.
	Principal string
	// Scopes são as autoridades detidas por este accessor (ex.: "payload:read").
	Scopes []string
}

// hasScope indica se o accessor detém o escopo exigido.
func (a Accessor) hasScope(scope string) bool {
	for _, s := range a.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// PayloadPutRequest é o pedido de escrita de um payload completo no store externo.
type PayloadPutRequest struct {
	// RunID/StepID/Turn localizam a origem do payload (metadados de IAM/auditoria).
	RunID  string
	StepID string
	Turn   int
	// Payload são os bytes COMPLETOS do conteúdo não-determinístico (já minimizado/
	// redigido na fronteira quando o modo sensível está activo — ver a captura).
	Payload []byte
	// Writer é o principal que ESCREVE (o produtor do Event Store). O seu escopo de
	// escrita é distinto do escopo de leitura exigido no Get — é o que materializa o
	// "IAM próprio, separado do escritor do Event Store".
	Writer Accessor
}

// PayloadStore é a PORTA do storage externo de payloads (content-capture mode 3). O
// Get impõe um CONTROLO DE ACESSO/IAM PRÓPRIO: um accessor sem autoridade de leitura
// é negado ([ErrPayloadAccessDenied]); o payload devolvido é verificado contra o
// hash da referência ([ErrPayloadIntegrity]).
//
// A impl real (S3 + política IAM, KMS por titular) é um adapter de deployment
// DIFERIDO — aqui a porta é satisfeita por [InMemoryPayloadStore] (zero-dep).
type PayloadStore interface {
	// Put escreve o payload completo e devolve a sua [PayloadRef] content-addressable.
	// Exige que o Writer detenha o escopo de escrita (fail-closed).
	Put(ctx context.Context, req PayloadPutRequest) (PayloadRef, error)
	// Get resolve a referência para os bytes do payload, IMPONDO o IAM: o accessor tem
	// de deter o escopo de leitura. Verifica a integridade (hash) do conteúdo devolvido.
	Get(ctx context.Context, ref PayloadRef, accessor Accessor) ([]byte, error)
}

// PayloadStoreOption configura o [InMemoryPayloadStore].
type PayloadStoreOption func(*InMemoryPayloadStore)

// WithReadScope define o escopo de leitura EXIGIDO no Get (default "payload:read").
// É o escopo que um accessor tem de deter para resolver um payload — a base do IAM
// próprio do store, separado do escritor do Event Store.
func WithReadScope(scope string) PayloadStoreOption {
	return func(s *InMemoryPayloadStore) { s.readScope = scope }
}

// WithWriteScope define o escopo de escrita EXIGIDO no Put (default "payload:write").
// Distinto do escopo de leitura: um principal que só escreve (o produtor do ES) NÃO
// pode ler os payloads — prova de que o payload está atrás do seu próprio IAM.
func WithWriteScope(scope string) PayloadStoreOption {
	return func(s *InMemoryPayloadStore) { s.writeScope = scope }
}

// Escopos default do IAM modelado.
const (
	// DefaultPayloadReadScope é o escopo de leitura exigido por omissão.
	DefaultPayloadReadScope = "payload:read"
	// DefaultPayloadWriteScope é o escopo de escrita exigido por omissão.
	DefaultPayloadWriteScope = "payload:write"
)

// InMemoryPayloadStore é a impl de REFERÊNCIA da porta [PayloadStore]: guarda os
// payloads em memória indexados pelo seu digest (content-addressable) e MODELA um
// IAM próprio via escopos de leitura/escrita. É determinística e segura para uso
// concorrente (mutex). NÃO puxa nenhum SDK de cloud — o adapter real é diferido.
type InMemoryPayloadStore struct {
	mu         sync.RWMutex
	blobs      map[string][]byte
	readScope  string
	writeScope string
}

// NewInMemoryPayloadStore constrói o store de referência com o IAM modelado.
func NewInMemoryPayloadStore(opts ...PayloadStoreOption) *InMemoryPayloadStore {
	s := &InMemoryPayloadStore{
		blobs:      make(map[string][]byte),
		readScope:  DefaultPayloadReadScope,
		writeScope: DefaultPayloadWriteScope,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// digestOf devolve o digest content-addressable ("sha256:<hex>") de b.
func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Put escreve o payload e devolve a referência. Fail-closed: um Writer sem o escopo
// de escrita é negado ANTES de qualquer escrita.
func (s *InMemoryPayloadStore) Put(_ context.Context, req PayloadPutRequest) (PayloadRef, error) {
	if !req.Writer.hasScope(s.writeScope) {
		// O produtor tem de provar autoridade de ESCRITA — separada da de leitura.
		return PayloadRef{}, ErrPayloadAccessDenied
	}
	digest := digestOf(req.Payload)
	// Cópia defensiva: o store detém os bytes, imune a mutação do chamador.
	stored := make([]byte, len(req.Payload))
	copy(stored, req.Payload)
	s.mu.Lock()
	s.blobs[digest] = stored
	s.mu.Unlock()
	return PayloadRef{Digest: digest}, nil
}

// Get resolve a referência IMPONDO o IAM próprio do store:
//  1. o accessor TEM de deter o escopo de leitura — senão [ErrPayloadAccessDenied]
//     (fail-closed, mesmo que o payload exista);
//  2. a referência tem de existir — senão [ErrPayloadNotFound];
//  3. o conteúdo é verificado contra o hash da referência — senão
//     [ErrPayloadIntegrity] (content-addressable: a ref prova a integridade).
//
// A verificação de IAM precede a de existência: um accessor não autorizado não
// consegue sequer distinguir "não existe" de "existe mas sem acesso".
func (s *InMemoryPayloadStore) Get(_ context.Context, ref PayloadRef, accessor Accessor) ([]byte, error) {
	if !accessor.hasScope(s.readScope) {
		return nil, ErrPayloadAccessDenied
	}
	s.mu.RLock()
	stored, ok := s.blobs[ref.Digest]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrPayloadNotFound
	}
	if digestOf(stored) != ref.Digest {
		return nil, ErrPayloadIntegrity
	}
	out := make([]byte, len(stored))
	copy(out, stored)
	return out, nil
}

// evict remove um payload do store (usado em teste para simular perda/retenção
// expirada — TTL/crypto-shredding de ADR-011 — e provar a admissão fail-closed).
func (s *InMemoryPayloadStore) evict(ref PayloadRef) {
	s.mu.Lock()
	delete(s.blobs, ref.Digest)
	s.mu.Unlock()
}

// count devolve o nº de payloads retidos (usado em teste).
func (s *InMemoryPayloadStore) count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.blobs)
}

// Verificação em tempo de compilação: o store de referência satisfaz a porta.
var _ PayloadStore = (*InMemoryPayloadStore)(nil)

// asIncompleteCapture traduz um erro de resolução de payload no erro de admissão
// apropriado. Um payload em falta ([ErrPayloadNotFound]) é captura INCOMPLETA (o
// replay é inadmissível); os restantes erros (acesso negado, integridade) propagam
// tal-e-qual — são condições distintas do gate de admissão.
func asIncompleteCapture(err error) error {
	if errors.Is(err, ErrPayloadNotFound) {
		return ErrIncompleteCapture
	}
	return err
}
