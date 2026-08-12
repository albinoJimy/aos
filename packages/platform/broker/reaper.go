package broker

import "time"

// HIGIENE PRÉ-WIRING (AOS-264) — REAPER DE LEASES.
//
// O corte de acesso no TTL/revogação é imposto na INJECÇÃO ([Lease.usableLocked]):
// uma lease expirada/revogada nunca entrega o valor. Isso é a garantia de SEGURANÇA
// e já existia. O que faltava é HIGIENE de MEMÓRIA: sem varrimento, a entrada
// expirada/revogada — com o [internal/vault.Secret] encapsulado — permanece no store
// para sempre num serviço de longa duração. O reaper evicta-as.
//
// Molde de packages/cmd/aos/approval_sweeper.go (sweepApprovalsOnce): o método
// [Broker.ReapExpired] é a operação IDEMPOTENTE de UM varrimento, conduzível de fora
// (por um ticker no loop de serviço, à imagem de sweepApprovals). Este pacote NÃO
// corre um loop próprio — o broker é uma biblioteca componível; o ticker vive em
// quem o compõe (ligado com a troca em AOS-265). Aqui fica a SUPERFÍCIE.
//
// Evictar NÃO é revogar: uma lease evictada por expiração já era não-injectável (o
// TTL passou); uma revogada já fora cortada por [Broker.Revoke]. O reaper só liberta
// a memória do que já não autoriza nada — nunca corta uma lease ainda usável.

// ReapExpired evicta do store as leases que já NÃO são injectáveis a `now`
// (expiradas OU revogadas) e devolve quantas removeu. Idempotente e seguro para
// correr periodicamente. Uma lease ainda usável (dentro do TTL e não revogada) NUNCA
// é tocada. É a superfície de higiene a ligar a um ticker no wiring (AOS-265),
// gémea de [Broker.Revoke] (o corte imediato por id).
func (b *Broker) ReapExpired(now time.Time) int {
	return b.store.reap(now)
}

// reap remove as leases não-injectáveis (revogadas ou expiradas a `now`) de AMBOS
// os índices (handle e id). Corre sob o lock de escrita do store; a decisão de
// reapabilidade lê o estado da lease sob o l.mu dela ([Lease.reapable]) — a mesma
// ordem de aquisição (store → lease) que [leaseStore] já usa noutros pontos, pelo
// que não há inversão de lock. Uma injecção concorrente que tenha resolvido o *Lease
// ANTES do reap conclui sobre um ponteiro válido, e — se a lease for reapável —
// [Lease.usableLocked] recusá-la-ia na mesma: evictar não abre janela de entrega.
func (s *leaseStore) reap(now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for h, l := range s.leases {
		if l.reapable(now) {
			delete(s.leases, h)
			delete(s.byID, l.ID)
			removed++
		}
	}
	return removed
}

// reapable indica se a lease já não autoriza nada (revogada ou fora do TTL a `now`)
// e pode ser evictada com segurança. Lê o estado sob o l.mu — mesma disciplina de
// [Lease.usableLocked]/[Lease.revoke].
func (l *Lease) reapable(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.revoked || !now.Before(l.ExpiresAt)
}
