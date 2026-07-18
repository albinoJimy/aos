package hitl

import (
	"context"
	"crypto/ed25519"
	"sync"

	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// ApproverRegistry resolve um principal aprovador para a sua chave pública PINADA e
// a sua AUTORIDADE autoritativa. É a PORTA da identidade do aprovador (molde da
// [messaging.NHIRegistry]): a chave pública pinada é a ÚNICA âncora contra a qual a
// assinatura da decisão é verificada (um aprovador forjado não valida); a autoridade
// é o escopo REGISTADO — a fonte de verdade sobre QUE classes o principal pode
// aprovar, não um campo auto-declarado. Um principal ausente do registo não é
// autenticável (fail-closed).
type ApproverRegistry interface {
	// Lookup devolve a chave pública pinada e a autoridade autoritativa do aprovador.
	// ok=false quando o aprovador é desconhecido (não autenticável → fail-closed). Um
	// erro de backend é propagado (tratado fail-closed pelo Channel).
	Lookup(ctx context.Context, approver string) (pub ed25519.PublicKey, authority []string, ok bool, err error)
}

// RequiredAuthority devolve a capability de aprovação que uma acção da classe dada
// EXIGE do aprovador ("approve:<classe>", ex.: "approve:danger"). O aprovador só é
// autorizado se a sua autoridade autoritativa a contiver — é a expressão concreta do
// "utilizador ∩ classe" (AC2): não basta o principal ser autêntico, a sua autoridade
// tem de cobrir a classe que está a aprovar.
func RequiredAuthority(class risk.Class) string {
	return "approve:" + class.String()
}

// MemApproverRegistry é a implementação de referência in-memory do
// [ApproverRegistry]: aprovadores → (chave pública pinada, autoridade). Guarda SÓ a
// chave PÚBLICA — a privada vive no broker/Vault e nunca entra aqui. Segura para
// concorrência. Produção substitui por um directório de identidade real.
type MemApproverRegistry struct {
	mu      sync.RWMutex
	entries map[string]approverEntry
}

type approverEntry struct {
	pub       ed25519.PublicKey
	authority []string
}

// NewMemApproverRegistry constrói um registo vazio.
func NewMemApproverRegistry() *MemApproverRegistry {
	return &MemApproverRegistry{entries: make(map[string]approverEntry)}
}

// Register pina a chave pública e a autoridade de um aprovador. A autoridade é o
// conjunto de capabilities de aprovação (ex.: "approve:danger", "approve:gray") que
// o principal detém.
func (r *MemApproverRegistry) Register(approver string, pub ed25519.PublicKey, authority ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	auth := append([]string(nil), authority...)
	r.entries[approver] = approverEntry{pub: append(ed25519.PublicKey(nil), pub...), authority: auth}
}

// Lookup implementa [ApproverRegistry].
func (r *MemApproverRegistry) Lookup(_ context.Context, approver string) (ed25519.PublicKey, []string, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[approver]
	if !ok {
		return nil, nil, false, nil
	}
	return e.pub, append([]string(nil), e.authority...), true, nil
}
