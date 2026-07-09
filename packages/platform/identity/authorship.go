package identity

import (
	"github.com/aos-ref/platform/identity/delegation"
	"github.com/aos-ref/substrate/eventstore"
)

// AuthorFromEventChain reconstrói "quem autorizou" a partir da cadeia de
// delegação registada num evento de tool call lido do Event Store (AOS-002). O
// evento guarda os hops (sub/act_as) por ordem, da raiz humana à folha; o autor
// responsável é o sujeito da raiz.
//
// Fail-closed: uma cadeia vazia dá [delegation.ErrEmptyChain] e uma cadeia cuja
// raiz não seja um humano ("human:*") dá [delegation.ErrOrphanChain] — nunca se
// atribui autoria a um principal não-humano ("The Audit Log Lied", ADR-003).
func AuthorFromEventChain(hops []eventstore.DelegationHop) (string, error) {
	if len(hops) == 0 {
		return "", delegation.ErrEmptyChain
	}
	root := hops[0].Sub
	if !delegation.IsHuman(root) {
		return "", delegation.ErrOrphanChain
	}
	return root, nil
}

// AuthorFromEvent é uma conveniência que reconstrói o autor a partir do Producer
// de um evento lido do Event Store.
func AuthorFromEvent(ev eventstore.Event) (string, error) {
	return AuthorFromEventChain(ev.Producer.DelegationChain)
}
