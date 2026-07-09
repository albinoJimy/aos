package identity

import (
	"context"

	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento de identidade gravados no Event Store (AOS-002).
const (
	// EventTypeIssued — uma NHI foi emitida.
	EventTypeIssued = "identity.nhi.issued"
	// EventTypeRevoked — uma NHI foi revogada.
	EventTypeRevoked = "identity.nhi.revoked"
)

// streamIdentity é o stream do Event Store onde vivem os factos de identidade.
// Um stream único dá um log ordenado e auditável de emissões/revogações.
const streamIdentity = "identity"

// appender é o subconjunto do Event Store de que este pacote depende. Mantê-lo
// mínimo desacopla o módulo da superfície completa do store e facilita testes.
type appender interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
}

// issuedPayload é o corpo JSON do evento identity.nhi.issued. Contém APENAS
// metadados de emissão — NUNCA o token bearer completo, a assinatura ou a chave
// (separação identidade vs segredos, ADR-003/006).
type issuedPayload struct {
	JTI        string   `json:"jti"`
	UserID     string   `json:"user_id"`
	AgentID    string   `json:"agent_id"`
	AgentClass string   `json:"agent_class"`
	PolicyRef  string   `json:"policy_ref,omitempty"`
	Scope      []string `json:"scope,omitempty"`
	Issuer     string   `json:"iss"`
	IssuedAt   int64    `json:"iat"`
	Expiry     int64    `json:"exp"`
}

// revokedPayload é o corpo JSON do evento identity.nhi.revoked.
type revokedPayload struct {
	JTI       string `json:"jti"`
	RevokedAt int64  `json:"revoked_at"`
}
