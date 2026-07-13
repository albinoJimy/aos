package scheduler

// breaker_parker.go — adaptador que liga o [TaskParker] do breaker (AOS-029) à
// MÁQUINA DE ESTADOS DURÁVEL das tarefas (AOS-017, package state). É a integração
// que faz um trip transitar as tarefas em curso para um estado DURÁVEL seguro, SEM
// reimplementar a máquina: usa Machine.Pause (running → paused, a suspensão legítima
// e retomável do AOS-017). A paragem é IDEMPOTENTE — uma tarefa que já não está em
// running (paused/suspensa/terminal) é no-op, pelo que um trip repetido (ou um retry
// após falha de infra) NÃO duplica efeitos (ADR-001).
//
// O core do breaker depende apenas do seam [TaskParker]; este adaptador isola o
// import de `state` para que os testes do breaker possam injectar parkers falsos e o
// breaker permaneça desacoplado da máquina das tarefas.

import (
	"context"
	"errors"

	"github.com/aos-ref/kernel/agent-runtime/state"
)

// DefaultBreakerParkReason é o motivo (rótulo de auditoria, não segredo) gravado na
// transição running → paused causada por um trip do breaker.
const DefaultBreakerParkReason = "budget_breaker_tripped"

// MachineParker implementa [TaskParker] sobre a máquina de estados durável do
// AOS-017. Resolve, por árvore, as [state.Machine] das tarefas em curso e transita
// as que estão em running para paused (suspensão legítima e retomável). Idempotente.
type MachineParker struct {
	reason  string
	resolve func(treeID string) []*state.Machine
}

// MachineParkerOption configura o [MachineParker].
type MachineParkerOption func(*MachineParker)

// WithParkReason define o motivo gravado na transição de paragem (default
// [DefaultBreakerParkReason]).
func WithParkReason(reason string) MachineParkerOption {
	return func(p *MachineParker) {
		if reason != "" {
			p.reason = reason
		}
	}
}

// NewMachineParker constrói o adaptador. `resolve` devolve as máquinas das tarefas em
// curso de uma árvore (o mapeamento árvore→tarefas é do dono do plano de execução —
// o breaker não o conhece). resolve nil é tratado como "sem tarefas".
func NewMachineParker(resolve func(treeID string) []*state.Machine, opts ...MachineParkerOption) *MachineParker {
	p := &MachineParker{
		reason:  DefaultBreakerParkReason,
		resolve: resolve,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// ParkTree implementa [TaskParker]: transita cada tarefa em curso (running) da
// árvore para paused via [state.Machine.Pause], IDEMPOTENTEMENTE. Tarefas que já não
// estão em running são ignoradas (no-op — sem duplicar efeitos). Uma transição
// recusada por corrida (a tarefa saiu de running entre a leitura e o Pause) é
// [state.ErrInvalidTransition] e tratada como já-segura; qualquer outro erro (ex.:
// Event Store sem quórum) PROPAGA para o breaker o poder tratar como retryável.
func (p *MachineParker) ParkTree(ctx context.Context, treeID string) error {
	if p.resolve == nil {
		return nil
	}
	for _, m := range p.resolve(treeID) {
		if m == nil {
			continue
		}
		// Só as tarefas em running são paradas; as suspensas/terminais são no-op. Esta
		// leitura+Pause não é atómica, mas o Pause valida a tabela declarativa: uma
		// corrida que já tenha saído de running devolve ErrInvalidTransition, tratada
		// como já-segura (idempotência).
		if m.Current() != state.Running {
			continue
		}
		if err := m.Pause(ctx, state.TransitionEvent{Reason: p.reason}); err != nil {
			if errors.Is(err, state.ErrInvalidTransition) {
				continue // corrida: saiu de running — já em estado seguro, no-op.
			}
			return err
		}
	}
	return nil
}

// Verificação estática de conformidade com o seam do breaker.
var _ TaskParker = (*MachineParker)(nil)
