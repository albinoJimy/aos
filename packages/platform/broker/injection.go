package broker

import (
	"context"
	"sync"
	"time"

	"github.com/aos-ref/platform/broker/internal/vault"
	"github.com/aos-ref/substrate/sandbox"
)

// Injector resolve um [Handle] opaco e INJECTA a credencial downstream
// SERVER-SIDE na sandbox (implementa [sandbox.CredentialInjector], AOS-064). O
// segredo NUNCA regressa ao chamador: é entregue ao [GuestSink] server-side (o
// mount de credencial da microVM). Uma lease expirada/revogada NÃO é injectada
// (fail-closed), cortando o acesso imediatamente.
type Injector struct {
	store *leaseStore
	clock func() time.Time
	guest vault.Sink
}

// verifica em compile-time que o Injector satisfaz a porta do SBX (AOS-064): a
// resolução do credentials_handle é server-side, exactamente onde AOS-070 promete.
var _ sandbox.CredentialInjector = (*Injector)(nil)

// NewInjector cria um injector que entrega ao guest sink SERVER-SIDE dado. O sink
// é infra server-side (o interior da microVM), NÃO alcançável pelo agente.
func (b *Broker) NewInjector(guest GuestSink) (*Injector, error) {
	if guest == nil {
		return nil, ErrNilGuestSink
	}
	return &Injector{store: b.store, clock: b.clock, guest: guest}, nil
}

// Inject implementa [sandbox.CredentialInjector]. Resolve o handle server-side e
// injecta a credencial no guest. Um handle vazio é no-op (a sandbox propaga
// handles opacos sem credencial). Fail-closed: handle desconhecido, lease
// expirada ou revogada devolvem erro SEM entregar o valor e SEM o expor no erro.
func (inj *Injector) Inject(_ context.Context, handle string, _ sandbox.Instance) error {
	if handle == "" {
		return nil
	}
	lease, ok := inj.store.get(Handle(handle))
	if !ok {
		return ErrUnknownHandle
	}
	return lease.injectInto(inj.guest, inj.clock())
}

// Revoke revoga uma lease pelo id NÃO-SECRETO: corta o acesso IMEDIATAMENTE (a
// próxima injecção falha com [ErrLeaseRevoked]). Devolve false se o id for
// desconhecido. É o corte central de revogação do broker (ADR-006).
func (b *Broker) Revoke(leaseID string) bool {
	return b.store.revokeByID(leaseID)
}

// ---------------------------------------------------------------------------
// MemoryGuest — sink de referência que modela o INTERIOR da microVM (server-side).
// ---------------------------------------------------------------------------

// MemoryGuest é um [GuestSink] de REFERÊNCIA que modela o mount de credencial no
// interior da microVM (server-side). Regista as colocações para inspecção de
// TESTE/operação server-side — NÃO é o contexto do agente. Concorrente-seguro.
//
// AVISO: representa o destino LEGÍTIMO da credencial (onde ela é usada). Não é
// código do agente, log de aplicação, span nem Event Store — os quatro sítios onde
// o segredo NUNCA pode aparecer (ADR-006). Existe para provar que a injecção
// server-side chega ao guest, e para os testes de TTL/revogação observarem o corte.
type MemoryGuest struct {
	mu     sync.Mutex
	placed map[string]int // ref → nº de colocações (metadados; NÃO guarda o valor)
	count  int
}

// NewMemoryGuest constrói o guest de referência.
func NewMemoryGuest() *MemoryGuest {
	return &MemoryGuest{placed: map[string]int{}}
}

// Place implementa [GuestSink]: recebe o valor server-side. A impl de referência
// NÃO retém o valor em claro (guarda apenas a contagem por ref) — assim nem o
// holder de teste expõe o segredo. O parâmetro secret é consumido e descartado.
func (g *MemoryGuest) Place(ref, secret string) error {
	_ = secret // consumido server-side; deliberadamente NÃO retido
	g.mu.Lock()
	defer g.mu.Unlock()
	g.placed[ref]++
	g.count++
	return nil
}

// Injections devolve o total de injecções server-side realizadas (asserção de
// fluxo — nunca expõe o valor).
func (g *MemoryGuest) Injections() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.count
}

// InjectedRef indica se houve injecção para o ref (rótulo não-secreto) dado.
func (g *MemoryGuest) InjectedRef(ref string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.placed[ref] > 0
}
