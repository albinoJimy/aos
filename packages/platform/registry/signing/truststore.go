package signing

import (
	"context"
	"crypto/ed25519"
	"sync"
	"time"

	"github.com/aos-ref/platform/audit"
)

// DefaultTrustStorePartition é a partição da hash-chain de audit onde as mudanças
// do trust store (add/revoke) se selam por omissão. Uma cadeia dedicada mantém as
// mudanças de confiança auditáveis e verificáveis de forma independente das
// decisões de admissão.
const DefaultTrustStorePartition = "registry.truststore"

// Capabilities de audit das mudanças de trust store (vocabulário estável, selado
// na cadeia para ser tamper-evident).
const (
	capTrustAdd    = "registry.truststore.add"
	capTrustRevoke = "registry.truststore.revoke"
)

// trustedKey é a entrada interna do trust store: a chave pública de um publicador
// e o seu estado de revogação. NUNCA contém a chave privada.
type trustedKey struct {
	pub     ed25519.PublicKey
	revoked bool
}

// TrustStore é o registo de chaves PÚBLICAS de publicadores confiáveis (AOS-048).
// É GERÍVEL (Add/Revoke) e AUDITÁVEL: cada mudança sela-se no audit hash-chain
// WORM (AOS-011). Uma chave revogada deixa IMEDIATAMENTE de validar (Lookup
// devolve não-confiável). NUNCA guarda chaves privadas — a metade privada vive
// fora do REG. Seguro para concorrência. Construir com [NewTrustStore].
type TrustStore struct {
	mu        sync.RWMutex
	keys      map[string]trustedKey
	audit     audit.Store
	partition string
	now       func() time.Time
}

// TrustStoreOption configura o TrustStore.
type TrustStoreOption func(*TrustStore)

// WithTrustPartition define a partição de audit das mudanças do trust store. Por
// omissão [DefaultTrustStorePartition].
func WithTrustPartition(p string) TrustStoreOption {
	return func(t *TrustStore) {
		if p != "" {
			t.partition = p
		}
	}
}

// WithTrustClock injecta o relógio (determinismo em testes; só para timestamps
// observacionais de audit — nunca numa decisão de confiança).
func WithTrustClock(f func() time.Time) TrustStoreOption {
	return func(t *TrustStore) {
		if f != nil {
			t.now = f
		}
	}
}

// NewTrustStore constrói um trust store auditável sobre o audit store dado. Um
// audit store nil devolve [ErrNoAuditStore]: a auditabilidade é uma pré-condição
// (ADR-010) — um trust store cujas mudanças não podem ser seladas não é admissível.
func NewTrustStore(auditStore audit.Store, opts ...TrustStoreOption) (*TrustStore, error) {
	if auditStore == nil {
		return nil, ErrNoAuditStore
	}
	t := &TrustStore{
		keys:      make(map[string]trustedKey),
		audit:     auditStore,
		partition: DefaultTrustStorePartition,
		now:       time.Now,
	}
	for _, o := range opts {
		o(t)
	}
	return t, nil
}

// Add regista uma chave pública confiável sob keyID e SELA a mudança no audit WORM
// ANTES de a tornar efectiva. Fail-closed: key id vazio → [ErrEmptyKeyID]; chave de
// tamanho inválido → [ErrInvalidKey]; falha de audit → [ErrAuditFailed] e a mudança
// NÃO toma efeito (uma mudança de confiança não-auditável é recusada).
//
// A revogação é TERMINAL por keyID (AOS-048 Q4): tentar Add sobre um keyID já
// REVOGADO é RECUSADO com [ErrKeyRevoked] — nunca o re-activa. Caso contrário, quem
// tivesse escrita no store poderia reverter silenciosamente uma revogação de uma
// chave comprometida (revalidando assinaturas antigas dessa chave). Re-confiar a
// origem exige um keyID NOVO (rotação). A TENTATIVA de re-confiar um keyID revogado é
// ela própria auditada como RECUSA (o rasto da tentativa importa), distinta do
// registo allow de um Add legítimo.
func (t *TrustStore) Add(ctx context.Context, keyID string, pub ed25519.PublicKey) error {
	if keyID == "" {
		return ErrEmptyKeyID
	}
	if len(pub) != ed25519.PublicKeySize {
		return ErrInvalidKey
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// Revogação terminal: um keyID revogado NUNCA é re-activado por Add. Audita a
	// tentativa como recusa (deny) — mantendo o rasto — e recusa fail-closed. O mapa
	// não é mutado.
	if existing, ok := t.keys[keyID]; ok && existing.revoked {
		if err := t.record(ctx, capTrustAdd, keyID, audit.DecisionDeny); err != nil {
			return err
		}
		return ErrKeyRevoked
	}
	// Sela a mudança PRIMEIRO: se o audit falhar, o mapa não é mutado (fail-closed).
	if err := t.record(ctx, capTrustAdd, keyID, audit.DecisionAllow); err != nil {
		return err
	}
	// Cópia defensiva: o trust store nunca partilha o backing array da chave com o
	// chamador (a chave selada é imutável do ponto de vista do store).
	cp := make(ed25519.PublicKey, len(pub))
	copy(cp, pub)
	t.keys[keyID] = trustedKey{pub: cp, revoked: false}
	return nil
}

// Revoke marca a chave de keyID como REVOGADA e sela a mudança no audit WORM.
// A partir daí, Lookup(keyID) devolve não-confiável e qualquer assinatura dessa
// chave deixa de validar. Fail-closed: revogar um keyID desconhecido é um no-op
// idempotente AUDITADO (regista-se a intenção de revogação, mesmo sem chave
// presente — o rasto da tentativa importa). Falha de audit → [ErrAuditFailed] e a
// revogação NÃO toma efeito.
func (t *TrustStore) Revoke(ctx context.Context, keyID string) error {
	if keyID == "" {
		return ErrEmptyKeyID
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.record(ctx, capTrustRevoke, keyID, audit.DecisionDeny); err != nil {
		return err
	}
	if k, ok := t.keys[keyID]; ok {
		k.revoked = true
		t.keys[keyID] = k
	}
	return nil
}

// Lookup devolve a chave pública confiável de keyID e true SE E SÓ SE a chave
// está presente E não-revogada. Uma chave ausente ou revogada devolve (nil,
// false) — o chamador trata isso como não-confiável (fail-closed). Devolve uma
// cópia para preservar a imutabilidade do estado selado.
func (t *TrustStore) Lookup(keyID string) (ed25519.PublicKey, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	k, ok := t.keys[keyID]
	if !ok || k.revoked {
		return nil, false
	}
	cp := make(ed25519.PublicKey, len(k.pub))
	copy(cp, k.pub)
	return cp, true
}

// record sela uma mudança de trust store na hash-chain de audit. A mudança é
// atribuível ao keyID afectado (ToolID/Resource), com a acção em Capability e o
// veredicto em Decision (allow=confiar / deny=revogar). Fail-closed: um erro do
// store é embrulhado em [ErrAuditFailed] para que o chamador recuse a mudança.
func (t *TrustStore) record(ctx context.Context, capability, keyID string, decision audit.Decision) error {
	rec := audit.AuditRecord{
		Partition:  t.partition,
		Timestamp:  t.now(),
		Decision:   decision,
		Capability: capability,
		ToolID:     keyID,
		Resource:   audit.Resource{Type: "publisher_key", Value: keyID},
		RunID:      t.partition,
		StepID:     capability + ":" + keyID,
	}
	if _, err := t.audit.Append(ctx, rec); err != nil {
		return ErrAuditFailed
	}
	return nil
}
