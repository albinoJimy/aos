package broker

import (
	"errors"

	"github.com/aos-ref/platform/broker/internal/vault"
)

// ErrNoMaterial é reexportado de [internal/vault]: o Vault não tem material para o
// pedido (fail-closed). Comparável por errors.Is.
var ErrNoMaterial = vault.ErrNoMaterial

var (
	// ErrNilMonitor — construção sem Reference Monitor (a troca TEM de ser mediada).
	ErrNilMonitor = errors.New("broker: reference monitor nil")
	// ErrNilVault — construção sem Vault (não há de onde obter material).
	ErrNilVault = errors.New("broker: vault nil")
	// ErrNilEventStore — construção sem Event Store (a troca TEM de ser registada).
	ErrNilEventStore = errors.New("broker: event store nil")
	// ErrEmptyToolID — id de tool de troca vazio.
	ErrEmptyToolID = errors.New("broker: tool id de troca vazio")
	// ErrNilGuestSink — injector sem sink server-side.
	ErrNilGuestSink = errors.New("broker: guest sink nil")

	// ErrOutOfScope — o pedido de troca está FORA do escopo do token (a capability
	// pedida não pertence à autoridade efectiva utilizador ∩ classe). Fail-closed.
	ErrOutOfScope = errors.New("broker: pedido fora do escopo do token (utilizador ∩ classe)")

	// ErrUnknownHandle — o handle apresentado à injecção é desconhecido (não
	// corresponde a nenhuma lease emitida). Fail-closed, sem expor o valor.
	ErrUnknownHandle = errors.New("broker: handle desconhecido")
	// ErrLeaseExpired — a lease expirou (passou o TTL). A credencial não é injectável.
	ErrLeaseExpired = errors.New("broker: lease expirada (TTL)")
	// ErrLeaseRevoked — a lease foi revogada. A credencial não é injectável.
	ErrLeaseRevoked = errors.New("broker: lease revogada")
)

// DeniedError descreve uma troca NEGADA/escalada pela mediação do Reference
// Monitor (nenhuma credencial foi emitida nem injectada). Espelha o padrão do SBX.
type DeniedError struct {
	Effect string
	Code   string
	Reason string
}

func (e *DeniedError) Error() string {
	return "broker: troca negada pela mediacao (" + e.Effect + "/" + e.Code + "): " + e.Reason
}
