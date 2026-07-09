package identity

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// Revocations é o registo de revogação de NHIs: um conjunto de jti em memória,
// consultado por [Verifier.Verify] (via [RevocationChecker]) e alimentado por
// [Revocations.Revoke], que grava um evento identity.nhi.revoked no Event Store.
// O TTL curto dos tokens minimiza a janela em que uma revogação tem de valer.
type Revocations struct {
	mu      sync.RWMutex
	revoked map[string]struct{}
	store   appender
	now     func() time.Time
}

// RevocationsOption configura o registo de revogação.
type RevocationsOption func(*Revocations)

// WithRevocationClock injecta o relógio (uso interno/testes).
func WithRevocationClock(f func() time.Time) RevocationsOption {
	return func(r *Revocations) {
		if f != nil {
			r.now = f
		}
	}
}

// NewRevocations constrói o registo. store é o Event Store onde Revoke grava o
// evento; pode ser nil (a revogação regista-se em memória mas não é auditada —
// produção deve injectar um store real).
func NewRevocations(store eventstore.EventStore, opts ...RevocationsOption) *Revocations {
	r := &Revocations{
		revoked: make(map[string]struct{}),
		now:     time.Now,
	}
	if store != nil {
		r.store = store
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Revoke revoga o token identificado por jti: acrescenta-o ao conjunto e grava
// identity.nhi.revoked no Event Store. É idempotente — revogar o mesmo jti duas
// vezes não duplica o efeito nem o evento (idempotency key por jti).
func (r *Revocations) Revoke(ctx context.Context, jti string) error {
	if jti == "" {
		return ErrInvalidRequest
	}
	r.mu.Lock()
	r.revoked[jti] = struct{}{}
	r.mu.Unlock()

	if r.store == nil {
		return nil
	}
	payload := revokedPayload{JTI: jti, RevokedAt: r.now().Unix()}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = r.store.Append(ctx, streamIdentity, eventstore.EventInput{
		Type:     EventTypeRevoked,
		Payload:  raw,
		RunID:    streamIdentity,
		StepID:   "nhi.revoked:" + jti, // idempotência por jti
		Producer: eventstore.Producer{NHIID: jti},
	})
	return err
}

// IsRevoked implementa [RevocationChecker]. Nunca devolve erro nesta
// implementação em memória; a assinatura mantém-no para permitir backends
// remotos (introspecção) que possam falhar — nesse caso o verificador é
// fail-closed.
func (r *Revocations) IsRevoked(_ context.Context, jti string) (bool, error) {
	// Guarda contra um *Revocations nil embrulhado numa interface não-nil (ex.:
	// WithRevocations((*Revocations)(nil))): o guard v.revocations != nil no
	// Verifier passa, e sem esta protecção o r.mu.RLock() sobre receiver nil
	// entraria em panic. Um registo nil não tem jti revogado ⇒ (false, nil).
	if r == nil {
		return false, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.revoked[jti]
	return ok, nil
}
