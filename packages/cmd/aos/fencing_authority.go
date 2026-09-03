package main

// AUTORIDADE DE FENCING DO NÓ (AOS-299).
//
// O `FencedStore` que fenceia as escritas do step-ledger e do checkpointer precisa de uma
// [durable.TokenSource] — quem sabe qual é o token corrente de um run. No nó, essa autoridade é
// o `*durable.LeaseManager`.
//
// # PORQUE É DE LIGAÇÃO TARDIA
//
// A ordem de composição não é escolha deste ticket: o ledger e o checkpointer nascem no
// [Bootstrap] (precisam do Event Store), e o `LeaseManager` nasce no [NewNodeService] (precisa do
// TTL e das opções de lease, que são do serviço). Ligar o token no bootstrap exigiria mover a
// criação do lease manager para lá, arrastando a superfície de opções do serviço — trabalho de
// reestruturação, não de fencing.
//
// O que este tipo faz é fechar a janela em vez de a esconder: enquanto a autoridade não estiver
// ligada, TODA a escrita fenceada é RECUSADA. Não há estado em que o nó escreva sem fencing por
// a composição ainda não ter chegado — que é o defeito que este ticket existe para fechar, e
// seria irónico reintroduzi-lo pela porta da ordem de arranque.

import (
	"context"
	"errors"
	"sync"

	durable "github.com/aos-ref/kernel/agent-runtime/durable"
)

// ErrFencingAuthorityMissing — o nó tem execução durável composta mas a autoridade de fencing
// não chegou ao [NewNodeService]. É um defeito de COMPOSIÇÃO, não de execução: recusa-se a
// construção do serviço em vez de deixar um nó a escrever sem fencing sem que ninguém dê por isso.
var ErrFencingAuthorityMissing = errors.New("aos: execucao duravel composta sem autoridade de fencing (AOS-299) — o Bootstrap tem de a criar junto do ledger; um no que hospeda runs sem ela escreve sem fencing em silencio")

// fencingAuthority é a [durable.TokenSource] do nó, com o `LeaseManager` ligado depois da
// construção. Implementa também [durable.LeaseExpiryAuthority] por delegação: sem isso, o
// `FencedAppender` deixaria de ver a autoridade de expiração (é uma type assertion) e a janela
// «lease expirado mas token ainda corrente» reabria em silêncio.
type fencingAuthority struct {
	mu     sync.RWMutex
	leases *durable.LeaseManager
}

// ligar associa o lease manager. Chamado uma vez, pelo [NewNodeService].
func (a *fencingAuthority) ligar(l *durable.LeaseManager) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.leases = l
}

func (a *fencingAuthority) actual() *durable.LeaseManager {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.leases
}

// CurrentToken implementa [durable.TokenSource].
//
// # SEM LEASE MANAGER LIGADO ⇒ SEM DETENTOR, E NÃO «ERRO»
//
// Devolver erro aqui pareceria a escolha fail-closed, e não é. Quem chega a este estado é um
// embedder que compôs o nó com [Bootstrap] e conduz o `Runtime` directamente, sem serviço — e
// esse nó não tem lease manager NENHUM, logo não há posse, logo não há detentor a superar. É
// exactamente a mesma situação de um run nunca reclamado, e o [durable.FencedStore] trata-a da
// mesma maneira: escreve, porque não há nada que o fencing pudesse proteger.
//
// O que NÃO pode acontecer é o NÓ chegar aqui. Isso está fechado do lado da composição:
// [NewNodeService] recusa-se a existir se houver ledger sem autoridade
// ([ErrFencingAuthorityMissing]), e liga-a antes de hospedar seja o que for. Um teste fixa-o.
func (a *fencingAuthority) CurrentToken(ctx context.Context, runID string) (durable.FencingToken, error) {
	l := a.actual()
	if l == nil {
		return 0, nil
	}
	return l.CurrentToken(ctx, runID)
}

// CurrentLeaseExpired implementa [durable.LeaseExpiryAuthority] por delegação.
func (a *fencingAuthority) CurrentLeaseExpired(ctx context.Context, runID string) (expired, exists bool, err error) {
	l := a.actual()
	if l == nil {
		// Sem lease manager não há lease — o mesmo (false, false, nil) que o LeaseManager
		// devolve para um run nunca reclamado. Coerente com CurrentToken acima.
		return false, false, nil
	}
	return l.CurrentLeaseExpired(ctx, runID)
}

var (
	_ durable.TokenSource          = (*fencingAuthority)(nil)
	_ durable.LeaseExpiryAuthority = (*fencingAuthority)(nil)
)
