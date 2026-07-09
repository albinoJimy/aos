package budget

import (
	"context"
	"sync"

	rm "github.com/aos-ref/kernel/reference-monitor"
)

// BudgetCheck adapta o orçamento hierárquico à interface Hook do Reference
// Monitor (AOS-003), ocupando o ponto de injecção do hook "budget" (o antigo
// BudgetStub). Materializa o admission control em tokens/$: por cada Call estima
// o custo e RESERVA headroom na árvore antes do spawn/tool call. Sem headroom →
// HookDeny (fail-closed) e o RM audita a negação no Event Store.
//
// Ciclo de vida da reserva. A reserva feita no Evaluate fica PENDENTE, indexada
// pela chave do call (run_id:step_id). O consumidor, consoante a [rm.Decision]
// final, confirma ([BudgetCheck.Commit], em permit) ou liberta
// ([BudgetCheck.Release], em deny/escalate/falha) — garantindo que reserva não
// consumida não faz leak. Ver [BudgetCheck.Settle] para o atalho por Decision.
type BudgetCheck struct {
	budget   Reserver
	estimate CostFunc
	nodeOf   NodeFunc
	trip     Amount // circuit breaker: trip por custo/token; zero = desligado

	mu      sync.Mutex
	pending map[string]Reservation
}

// CostFunc estima a quantia (tokens/$) que um Call vai consumir.
type CostFunc func(call *rm.Call) Amount

// NodeFunc mapeia um Call ao nó da árvore de orçamento a debitar (a sub-árvore
// do run/step). Por omissão usa o RunID como nó.
type NodeFunc func(call *rm.Call) string

// AdapterOption configura o BudgetCheck.
type AdapterOption func(*BudgetCheck)

// WithEstimator define o estimador de custo do Call.
func WithEstimator(f CostFunc) AdapterOption {
	return func(b *BudgetCheck) {
		if f != nil {
			b.estimate = f
		}
	}
}

// WithNodeSelector define o mapeamento Call→nó de orçamento.
func WithNodeSelector(f NodeFunc) AdapterOption {
	return func(b *BudgetCheck) {
		if f != nil {
			b.nodeOf = f
		}
	}
}

// WithCircuitBreaker activa um trip simples (integração LEVE, detalhe em
// EPIC-08): um Call cuja estimativa exceda trip nalguma dimensão é negado de
// imediato, sem sequer tentar reservar. Zero desliga o breaker.
func WithCircuitBreaker(trip Amount) AdapterOption {
	return func(b *BudgetCheck) { b.trip = trip }
}

// NewBudgetCheck constrói o adaptador sobre um [Reserver]. Um Reserver nil deixa
// o adaptador sem backend: fail-closed (todo o Evaluate devolve HookDeny).
func NewBudgetCheck(r Reserver, opts ...AdapterOption) *BudgetCheck {
	b := &BudgetCheck{
		budget:   r,
		estimate: DefaultEstimator,
		nodeOf:   func(c *rm.Call) string { return c.RunID },
		pending:  make(map[string]Reservation),
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Name é o identificador estável do hook (usado em DeniedBy e nos eventos).
func (*BudgetCheck) Name() string { return "budget" }

// Evaluate implementa rm.Hook. Estima o custo, aplica o circuit breaker e
// RESERVA headroom. Sem headroom (ou qualquer erro de backend) → HookDeny
// fail-closed; nunca entra em panic nem devolve erro (o RM audita a decisão).
func (b *BudgetCheck) Evaluate(ctx context.Context, call *rm.Call) (rm.HookResult, error) {
	if b == nil || b.budget == nil {
		return rm.HookResult{Decision: rm.HookDeny, Reason: "budget backend indisponivel"}, nil
	}

	amt := b.estimate(call)

	// Circuit breaker leve: trip por custo/token acima do limiar.
	if !b.trip.IsZero() && !amt.fitsWithin(b.trip) {
		return rm.HookResult{Decision: rm.HookDeny, Reason: "circuit breaker: custo/token acima do limiar de trip"}, nil
	}

	nodeID := b.nodeOf(call)
	r, err := b.budget.Reserve(ctx, nodeID, amt)
	if err != nil {
		return rm.HookResult{Decision: rm.HookDeny, Reason: err.Error()}, nil
	}

	b.mu.Lock()
	b.pending[callKey(call)] = r
	b.mu.Unlock()
	return rm.HookResult{Decision: rm.HookAllow}, nil
}

// take remove e devolve a reserva pendente associada ao call.
func (b *BudgetCheck) take(call *rm.Call) (Reservation, bool) {
	key := callKey(call)
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.pending[key]
	if ok {
		delete(b.pending, key)
	}
	return r, ok
}

// Commit confirma a reserva feita para este Call (débito final). Chamado pelo
// consumidor quando a [rm.Decision] foi permit. Se não houver reserva pendente
// (ex.: o budget negou), é no-op.
func (b *BudgetCheck) Commit(ctx context.Context, call *rm.Call) error {
	r, ok := b.take(call)
	if !ok {
		return nil
	}
	return b.budget.Commit(ctx, r)
}

// Release liberta a reserva feita para este Call (rollback, sem leak). Chamado
// pelo consumidor quando a decisão NÃO foi permit ou a execução falhou. No-op se
// não houver reserva pendente.
func (b *BudgetCheck) Release(ctx context.Context, call *rm.Call) error {
	r, ok := b.take(call)
	if !ok {
		return nil
	}
	return b.budget.Release(ctx, r)
}

// Settle é o atalho do ciclo commit-em-permit / release-em-tudo-o-resto: confirma
// a reserva se a decisão autorizou, liberta-a caso contrário. É o padrão que o
// consumidor do RM aplica após [rm.Monitor.Mediate].
func (b *BudgetCheck) Settle(ctx context.Context, call *rm.Call, d rm.Decision) error {
	if d.Effect == rm.EffectPermit && d.ToolErr == nil {
		return b.Commit(ctx, call)
	}
	return b.Release(ctx, call)
}

// callKey é a chave de indexação da reserva pendente: espelha a idempotency_key
// do Event Store (run_id:step_id).
func callKey(call *rm.Call) string { return call.RunID + ":" + call.StepID }

// DefaultEstimator é um estimador de custo PLACEHOLDER honesto: deriva os tokens
// do tamanho do Input (~1 token por 4 bytes, +1 base) e o custo a uma tarifa fixa
// de 10 micro-USD/token. Produção injecta um estimador real (contagem de tokens
// do prompt materializado e tarifa do provider) via [WithEstimator].
func DefaultEstimator(call *rm.Call) Amount {
	toks := int64(len(call.Input)/4 + 1)
	return Amount{Tokens: toks, CostMicroUSD: toks * 10}
}

// Assegura em compile-time que BudgetCheck satisfaz o contrato Hook do RM.
var _ rm.Hook = (*BudgetCheck)(nil)
