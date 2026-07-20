package testkit

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// ===========================================================================
// Credential Broker (BRK) — CONTRATO ALINHADO ao _BRIEF §2 + fake
//
// {CredentialBroker.Issue / Revoke} (platform/broker).
//
// LAYERING: o broker real vive em platform/broker e o seu Vault em .../internal/
// vault; a troca é mediada pelo Reference Monitor e arrasta essa cadeia. O testkit
// define aqui a superfície ALINHADA (Issue devolve um HANDLE OPACO — nunca o
// segredo; Revoke corta o acesso) e um fake determinista que HONRA A INVARIANTE
// central de AOS-070: o valor da credencial NUNCA é observável a jusante. É um
// MOCK ALINHADO AO CONTRATO, não o broker real.
// ===========================================================================

// BrokerHandle é o identificador OPACO e NÃO-SECRETO devolvido pela troca (espelha
// broker.Handle). Resolve-se server-side; o agente nunca vê o valor da credencial.
type BrokerHandle string

// BrokerDownstream descreve a credencial downstream pedida (campos NÃO-SECRETOS).
type BrokerDownstream struct {
	Provider   string
	Region     string
	Capability string
	Resource   string
}

// BrokerIssueRequest é o pedido de troca (espelha broker.ExchangeRequest, forma
// não-secreta). O token NHI (Credential) é um bearer efémero, não o segredo.
type BrokerIssueRequest struct {
	RunID        string
	StepID       string
	PrincipalNHI string
	Credential   string
	Downstream   BrokerDownstream
}

// CredentialBroker é a interface ALINHADA ao _BRIEF §2: Issue troca um token
// scoped por um handle opaco; Revoke corta o acesso de uma lease pelo seu handle.
// O contrato central: Issue NUNCA devolve o valor do segredo.
type CredentialBroker interface {
	Issue(ctx context.Context, req BrokerIssueRequest) (BrokerHandle, error)
	Revoke(ctx context.Context, handle BrokerHandle) error
}

// ErrBrokerDenied é devolvido quando o fake está programado para negar a troca.
var ErrBrokerDenied = errors.New("testkit.FakeBroker: troca negada (fail-closed)")

// ErrBrokerUnknownHandle é devolvido por Revoke para um handle desconhecido.
var ErrBrokerUnknownHandle = errors.New("testkit.FakeBroker: handle desconhecido")

// brokerLease é o registo server-side de uma troca (metadados NÃO-SECRETOS). O
// campo `secret` é DELIBERADAMENTE não-exportado e nunca escapa por nenhum método
// exportado — modela que o valor fica encapsulado server-side.
type brokerLease struct {
	handle       BrokerHandle
	runID        string
	principalNHI string
	capability   string
	secret       string // encapsulado: nunca devolvido por um método exportado
	revoked      bool
}

// FakeBroker é o Credential Broker de referência DETERMINISTA. Issue devolve um
// handle opaco SEQUENCIAL (via [SeqIDGen], nunca aleatório) e regista a lease
// server-side; Revoke marca-a revogada. Nenhum método exportado devolve o segredo
// — a invariante de AOS-070 é estrutural. Concorrente-seguro (-race).
type FakeBroker struct {
	mu     sync.Mutex
	ids    *SeqIDGen
	leases map[BrokerHandle]*brokerLease

	// Deny, se true, faz Issue negar fail-closed (ErrBrokerDenied).
	Deny bool
	// Secret é o valor server-side que a troca "resolveria" — encapsulado na lease,
	// nunca devolvido. Existe só para provar a invariante nos testes.
	Secret string
}

// NewFakeBroker constrói um broker vazio. Handles são deterministas ("h-1", "h-2").
func NewFakeBroker() *FakeBroker {
	return &FakeBroker{
		ids:    NewSeqIDGen("h"),
		leases: map[BrokerHandle]*brokerLease{},
		Secret: "SUPER-SECRET-VALUE",
	}
}

// Issue implementa [CredentialBroker]. Devolve APENAS um handle opaco; o segredo
// fica encapsulado na lease server-side. Fail-closed se Deny estiver ligado.
func (b *FakeBroker) Issue(_ context.Context, req BrokerIssueRequest) (BrokerHandle, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Deny {
		return "", ErrBrokerDenied
	}
	h := BrokerHandle(b.ids.Next())
	b.leases[h] = &brokerLease{
		handle:       h,
		runID:        req.RunID,
		principalNHI: req.PrincipalNHI,
		capability:   req.Downstream.Capability,
		secret:       b.Secret,
	}
	return h, nil
}

// Revoke implementa [CredentialBroker]: corta o acesso da lease. Devolve
// [ErrBrokerUnknownHandle] para um handle nunca emitido (fail-closed).
func (b *FakeBroker) Revoke(_ context.Context, handle BrokerHandle) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, ok := b.leases[handle]
	if !ok {
		return ErrBrokerUnknownHandle
	}
	l.revoked = true
	return nil
}

// Revoked indica se a lease do handle está revogada (falso se desconhecida).
func (b *FakeBroker) Revoked(handle BrokerHandle) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	l, ok := b.leases[handle]
	return ok && l.revoked
}

// Issued devolve o número de leases emitidas.
func (b *FakeBroker) Issued() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.leases)
}

// LeakedInto verifica a INVARIANTE de AOS-070: o valor do segredo NUNCA deve
// aparecer numa superfície observável pelo agente. Devolve true se o segredo
// encapsulado tiver vazado para `observed` — um teste de política de segredos
// chama-o sobre o handle, logs e outputs para provar a não-fuga. (Com o fake
// correcto devolve sempre false; existe para o teste asserir a propriedade.)
func (b *FakeBroker) LeakedInto(observed string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.Secret == "" {
		return false
	}
	return strings.Contains(observed, b.Secret)
}

// compile-time: o FakeBroker satisfaz o contrato alinhado.
var _ CredentialBroker = (*FakeBroker)(nil)
