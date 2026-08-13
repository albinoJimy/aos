// Package planbudget é o ADAPTADOR entre a hierarquia de ORÇAMENTO de AOS-008
// (`control-plane/budget`, reserva CAS por cadeia de ancestrais) e as portas de
// débito do despacho de plano — hoje, a avaliação de ARESTAS CONDICIONAIS de
// ADR-022 §2.1 (AOS-270).
//
// PORQUE É UM PACOTE PRÓPRIO (e não mais um ficheiro em `plandispatch`). O
// `plandispatch` tem um guard de imports EXECUTÁVEL (fronteira ADR-018): o pacote de
// produção só pode importar `plan` e `plannerevents`. É uma invariante deliberada —
// o SCH despacha, não é autoridade concorrente de nada — e não se contorna para
// acomodar um adaptador. O adaptador vive, portanto, deste lado da fronteira e
// liga-se por TIPO ESTRUTURAL: a porta `BranchBudget` é uma interface cujos tipos
// são todos de stdlib, pelo que [TreeBudgetMeter] a satisfaz sem que este pacote
// precise sequer de importar o `plandispatch`. Zero acoplamento nas duas direcções.
//
// Não reimplementa orçamento nenhum: reserva e confirma no [budget.Reserver] real.
package planbudget

import (
	"context"
	"errors"
	"sync"

	"github.com/aos-ref/control-plane/budget"
)

// ErrMeterDeps — Reserver em falta, ou custo de avaliação nulo/negativo. Um custo
// ZERO é recusado de propósito: «a avaliação de condições debita orçamento da
// árvore» (ADR-022 §2.4(4)) não admite um débito vazio, e [budget.Budget.Reserve]
// rejeitaria na mesma a reserva — falhar na CONSTRUÇÃO é honesto; falhar
// silenciosamente em cada avaliação (e o despacho ficar preso sem explicação) não.
var ErrMeterDeps = errors.New("planbudget: TreeBudgetMeter exige Reserver + custo de avaliação não-nulo")

// TreeBudgetMeter debita o custo de UMA decisão de ramo na árvore de orçamento:
// reserva no nó — o que, por [budget.Budget.Reserve], debita por CAS TODA a cadeia
// de ancestrais até à raiz — e confirma.
//
// Reserve→Commit, e não um débito directo, porque é assim que ADR-008 conta: o
// headroom é verificado ATOMICAMENTE em cada nível ANTES de se conceder, e uma
// confirmação falhada liberta o que foi reservado em vez de o deixar em fuga. Um nó
// sem headroom devolve erro — e o despachante trata-o como ESPERA, nunca como poda
// (ficar sem orçamento não é a condição ter dado falso).
//
// # AS DUAS FASES SÃO EXPOSTAS (não escondidas num Debit)
//
// O despachante precisa de intercalar a escrita do FACTO entre a reserva e a
// confirmação: sem facto durável não há decisão, e sem decisão não há nada a
// pagar. Confirmar antes de registar transformava uma indisponibilidade do Event
// Store num DRENO (N re-invocações do escalonador = N débitos pelo mesmo nó), que
// é o oposto do invariante «débito por DECISÃO, não por tentativa». A reserva em
// voo fica indexada por (plan_id, node_id) — a MESMA chave que torna o facto
// `plan.branch_decided` único —, pelo que não há handle opaco a atravessar a
// fronteira do pacote e o mapa nunca cresce para além das decisões em curso de UMA
// passagem (cada entrada sai em Commit ou Release).
type TreeBudgetMeter struct {
	reserver budget.Reserver
	cost     budget.Amount

	mu      sync.Mutex
	pending map[string]budget.Reservation
}

// NewTreeBudgetMeter liga o débito de avaliação de condições ao orçamento da
// árvore. `cost` é o preço FIXO e DECLARADO de uma decisão de ramo — fixo porque a
// avaliação é uma comparação sobre enums e inteiros, cujo custo não depende dos
// dados; declarado porque nenhum custo deve ser invisível no burn-down.
func NewTreeBudgetMeter(reserver budget.Reserver, cost budget.Amount) (*TreeBudgetMeter, error) {
	if reserver == nil {
		return nil, ErrMeterDeps
	}
	if cost.Tokens < 0 || cost.CostMicroUSD < 0 || (cost.Tokens == 0 && cost.CostMicroUSD == 0) {
		return nil, ErrMeterDeps
	}
	return &TreeBudgetMeter{reserver: reserver, cost: cost, pending: make(map[string]budget.Reservation)}, nil
}

// ErrNoPendingReservation — Commit/Release sem Reserve prévio para o mesmo
// (plan_id, node_id). Fail-closed: confirmar um débito que ninguém reservou seria
// inventar contabilidade.
var ErrNoPendingReservation = errors.New("planbudget: sem reserva em voo para esta decisão de ramo")

// pendingKey indexa a reserva em voo. O plano entra na chave (e não só o nó)
// porque o mesmo node_id pode existir em planos distintos no mesmo processo.
func pendingKey(planID, nodeID string) string { return planID + "\x00" + nodeID }

// ReserveConditionEval reserva o custo de UMA decisão de ramo no nó — o que, por
// [budget.Budget.Reserve], verifica e debita por CAS TODA a cadeia de ancestrais
// até à raiz. NÃO confirma: a confirmação espera pelo facto durável.
//
// A assinatura é a da porta `plandispatch.BranchBudget` (só tipos de stdlib).
func (m *TreeBudgetMeter) ReserveConditionEval(ctx context.Context, planID, nodeID string) error {
	res, err := m.reserver.Reserve(ctx, nodeID, m.cost)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if prev, dup := m.pending[pendingKey(planID, nodeID)]; dup {
		// Uma reserva anterior do MESMO nó ficou órfã (passagem abortada sem
		// Commit/Release). Devolve-se a antiga em vez de a perder — não fica em fuga.
		m.mu.Unlock()
		_ = m.reserver.Release(ctx, prev)
		m.mu.Lock()
	}
	m.pending[pendingKey(planID, nodeID)] = res
	m.mu.Unlock()
	return nil
}

// CommitConditionEval confirma a reserva do nó — o facto da decisão já está apenso.
func (m *TreeBudgetMeter) CommitConditionEval(ctx context.Context, planID, nodeID string) error {
	res, ok := m.take(planID, nodeID)
	if !ok {
		return ErrNoPendingReservation
	}
	if err := m.reserver.Commit(ctx, res); err != nil {
		// Não conseguimos confirmar: devolve o reservado em vez de o deixar preso.
		_ = m.reserver.Release(ctx, res)
		return err
	}
	return nil
}

// ReleaseConditionEval devolve a reserva do nó — o facto NÃO chegou a ser apenso,
// logo não há decisão e não há nada a pagar.
func (m *TreeBudgetMeter) ReleaseConditionEval(ctx context.Context, planID, nodeID string) error {
	res, ok := m.take(planID, nodeID)
	if !ok {
		return ErrNoPendingReservation
	}
	return m.reserver.Release(ctx, res)
}

// take remove e devolve a reserva em voo (uma reserva só se consome uma vez).
func (m *TreeBudgetMeter) take(planID, nodeID string) (budget.Reservation, bool) {
	k := pendingKey(planID, nodeID)
	m.mu.Lock()
	defer m.mu.Unlock()
	res, ok := m.pending[k]
	if ok {
		delete(m.pending, k)
	}
	return res, ok
}
