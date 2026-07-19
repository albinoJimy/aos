package worker

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/aos-ref/kernel/agent-runtime/durable"
)

// Assigner é o modelo de POSSE/ATRIBUIÇÃO de partição por run (AC3/AC5) — uma
// OwnershipTable FINA sobre os leases existentes, NÃO um escalonador novo. Como as
// partições são por run_id (sharding natural), não há mapa de hash fixo a
// rebalancear: uma réplica assume uma partição RECLAMANDO o lease do run
// ([durable.LeaseManager.Claim]); um run com lease AINDA VÁLIDO é detido por outra
// réplica ([durable.ErrLeaseHeld]) e deixado em paz (sem roubo, sem coordenação
// intra-processo — AC3). O Assigner apenas MEMORIZA que runs esta réplica detém,
// para observabilidade e handoff; a verdade da posse vive no lease durável.
//
// Scale-in N→N-1 (AC5): a réplica terminada deixa de fazer heartbeat; o seu lease
// EXPIRA por TTL e outra réplica reclama-o via [Assigner.TryAcquire], retomando
// resume-from-step. O fencing garante que qualquer escrita tardia da réplica
// terminada é rejeitada (token obsoleto) — sem perda nem duplicação.
//
// Seguro para uso concorrente.
type Assigner struct {
	leases *durable.LeaseManager

	mu    sync.Mutex
	owned map[string]durable.Lease
}

// NewAssigner constrói a tabela de posse sobre o [durable.LeaseManager] dado
// (obrigatório).
func NewAssigner(leases *durable.LeaseManager) (*Assigner, error) {
	if leases == nil {
		return nil, ErrNilLeaseManager
	}
	return &Assigner{leases: leases, owned: make(map[string]durable.Lease)}, nil
}

// TryAcquire tenta assumir a partição do run reclamando o seu lease. Devolve
// (lease, true, nil) se a posse foi adquirida (run livre ou lease expirado);
// (_, false, nil) se outra réplica detém um lease vivo ([durable.ErrLeaseHeld] — a
// partição não é roubada); e propaga qualquer outro erro. O lease devolvido é
// entregue a [Worker.Adopt] para servir o run sem duplo claim.
func (a *Assigner) TryAcquire(ctx context.Context, runID string) (durable.Lease, bool, error) {
	if runID == "" {
		return durable.Lease{}, false, ErrEmptyRunID
	}
	lease, err := a.leases.Claim(ctx, runID)
	if err != nil {
		if errors.Is(err, durable.ErrLeaseHeld) {
			// Detido e vivo por outra réplica: não é reatribuível agora (sem rebalancing).
			return durable.Lease{}, false, nil
		}
		return durable.Lease{}, false, err
	}
	a.mu.Lock()
	a.owned[runID] = lease
	a.mu.Unlock()
	return lease, true, nil
}

// Release larga a posse EM-PROCESSO da partição (scale-in / handoff gracioso). NÃO
// revoga o lease durável — não há revogação: o lease EXPIRA por TTL (ausência de
// heartbeat) e outra réplica reclama-o. Uma escrita tardia desta réplica é
// fenced-out assim que o token for superado. É idempotente.
func (a *Assigner) Release(runID string) {
	a.mu.Lock()
	delete(a.owned, runID)
	a.mu.Unlock()
}

// Requeue larga a posse de uma partição cujo worker PERDEU o lease a meio
// ([ErrLeaseLost]) — sinónimo de [Release] com a intenção semântica de "disponível
// para outra réplica retomar". A retoma da outra réplica é resume-from-step (sem
// perda) e os efeitos já aplicados deduplicam (sem duplicação).
func (a *Assigner) Requeue(runID string) { a.Release(runID) }

// Owns reporta se esta réplica detém (em-processo) a partição do run.
func (a *Assigner) Owns(runID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.owned[runID]
	return ok
}

// Owned devolve os run_ids que esta réplica detém, por ordem estável
// (observabilidade). É uma cópia — mutá-la não afecta a tabela.
func (a *Assigner) Owned() []string {
	a.mu.Lock()
	out := make([]string, 0, len(a.owned))
	for id := range a.owned {
		out = append(out, id)
	}
	a.mu.Unlock()
	sort.Strings(out)
	return out
}
