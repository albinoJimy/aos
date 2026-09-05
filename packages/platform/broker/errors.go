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

	// ErrProviderOutOfScope — o PROVEDOR pedido não pertence à autoridade efectiva
	// de provedor do principal (tecto da classe ∩ grants do token, AOS-324).
	// Fail-closed e ATRIBUÍVEL: é POLÍTICA, não ausência de material — nunca
	// confundir com [ErrNoMaterial] (errors.Is distingue-os).
	ErrProviderOutOfScope = errors.New("broker: provedor fora da autoridade do principal (eixo provider)")
	// ErrProviderUndetermined — o pedido não determina um provedor (campo vazio, ou
	// envelope de troca ilegível sob política declarada). Sem provedor não há chave
	// legítima no Vault: NEGA fail-closed nas duas posturas ([ProviderPosture]).
	ErrProviderUndetermined = errors.New("broker: provedor indeterminado no pedido de troca")

	// ErrResourceOutOfScope — o provedor está autorizado, mas o RECURSO de destino não lhe
	// pertence: o host pedido não consta da allowlist desse provedor (AOS-331).
	//
	// É DISTINTO de [ErrProviderOutOfScope] de propósito. «Este provedor não é teu» e «este
	// destino não é deste provedor» são diagnósticos diferentes e levam o operador a sítios
	// diferentes — a primeira é a política de classe, a segunda é a allowlist de hosts.
	ErrResourceOutOfScope = errors.New("broker: recurso de destino fora do escopo do provedor autorizado")

	// ErrResourceUndetermined — sob política declarada, o recurso não permite decidir: valor
	// que não se analisa, ou tipo que não é de rede. Informação insuficiente é recusa, a mesma
	// postura do envelope ilegível no eixo provider (AOS-331).
	ErrResourceUndetermined = errors.New("broker: recurso de destino indeterminado (nao analisavel ou tipo nao suportado)")

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
	// DeniedBy é o NOME do hook que negou/escalou (ex.: "broker-scope"), propagado
	// de `Decision.DeniedBy`. Torna a negação ATRIBUÍVEL a um ponto de decisão
	// concreto — quem lê sabe QUEM negou, não só que foi negado (AOS-324). Vazio
	// quando a decisão não nomeia um hook.
	DeniedBy string
}

func (e *DeniedError) Error() string {
	s := "broker: troca negada pela mediacao (" + e.Effect + "/" + e.Code
	if e.DeniedBy != "" {
		s += " por " + e.DeniedBy
	}
	return s + "): " + e.Reason
}
