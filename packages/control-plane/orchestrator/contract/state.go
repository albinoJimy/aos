package contract

import "errors"

// State é um estado da máquina de estados MÍNIMA de uma tarefa no plano de
// controlo (AOS-012). É deliberadamente um subconjunto da máquina de estados
// durável completa de tecnica/02 §5 (que acrescenta waiting_on_tool,
// waiting_on_human, paused, timed_out, killed, compensating). EPIC-03 estende
// este conjunto SEM quebrar os valores já existentes — os literais aqui são
// contrato estável.
type State string

const (
	// StateReady — a tarefa é elegível para escalonamento (claim). É o estado
	// inicial de todo o nó do grafo emitido pelo Orquestrador.
	StateReady State = "ready"
	// StateRunning — a tarefa foi reclamada pelo Escalonador e está a despachar.
	// (Na máquina completa, um fencing token acompanha esta transição — EPIC-03.)
	StateRunning State = "running"
	// StateComplete — estado terminal de sucesso.
	StateComplete State = "complete"
	// StateFailed — estado terminal de falha (recuperável na máquina completa via
	// compensating→ready; aqui é terminal simples — EPIC-03 acrescenta a saga).
	StateFailed State = "failed"
)

// ErrInvalidTransition é devolvido por [Transition] quando a transição pedida
// não é permitida pela máquina de estados mínima.
var ErrInvalidTransition = errors.New("orchestrator/contract: transição de estado inválida")

// validTransitions é a tabela de transições permitidas. É a autoridade única do
// que a máquina mínima aceita: ready → running → complete|failed. Estados
// terminais não têm sucessores.
//
// PONTO DE EXTENSÃO (EPIC-03): acrescentar aqui as arestas para os estados de
// suspensão (waiting_on_tool, waiting_on_human, paused) e recuperação
// (compensating→ready, timed_out, killed) sem remover as existentes.
var validTransitions = map[State]map[State]bool{
	StateReady:    {StateRunning: true},
	StateRunning:  {StateComplete: true, StateFailed: true},
	StateComplete: {},
	StateFailed:   {},
}

// Known indica se s é um estado reconhecido pela máquina mínima.
func (s State) Known() bool {
	_, ok := validTransitions[s]
	return ok
}

// Terminal indica se s é um estado terminal (sem transições de saída).
func (s State) Terminal() bool {
	return s == StateComplete || s == StateFailed
}

// CanTransition indica se a transição from → to é válida na máquina mínima.
// Um estado de origem desconhecido nunca transita (fail-closed).
func CanTransition(from, to State) bool {
	succ, ok := validTransitions[from]
	if !ok {
		return false
	}
	return succ[to]
}

// Transition valida from → to e devolve o estado de destino em caso de sucesso.
// Uma transição inválida devolve [ErrInvalidTransition] e o estado de origem
// inalterado (o chamador nunca é levado a um estado ilegal).
func Transition(from, to State) (State, error) {
	if !CanTransition(from, to) {
		return from, ErrInvalidTransition
	}
	return to, nil
}
