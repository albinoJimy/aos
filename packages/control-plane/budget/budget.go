package budget

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
)

// node é um nó da árvore de orçamento. O check-and-débito dos contadores é
// protegido por mu: a verificação de headroom e o débito de Reserved formam uma
// secção crítica INDIVISÍVEL — é isto que torna a reserva um compare-and-swap
// real e garante 0 overshoot sob concorrência (dois Reserve concorrentes nunca
// passam ambos um teste que só um cabia).
type node struct {
	id     string
	parent *node // nil na raiz
	limit  Amount

	mu        sync.Mutex
	reserved  Amount
	committed Amount
}

// available devolve o headroom do nó (Limit − Reserved − Committed) nas duas
// dimensões. Chamado sob n.mu.
func (n *node) availableLocked() Amount {
	return n.limit.Sub(n.reserved).Sub(n.committed)
}

// tryReserve tenta debitar amt em Reserved atomicamente: só procede se amt
// couber no headroom corrente nas DUAS dimensões. Devolve true se debitou.
func (n *node) tryReserve(amt Amount) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !amt.fitsWithin(n.availableLocked()) {
		return false
	}
	n.reserved = n.reserved.Add(amt)
	return true
}

// undoReserve reverte um débito de Reserved (rollback).
func (n *node) undoReserve(amt Amount) {
	n.mu.Lock()
	n.reserved = n.reserved.Sub(amt)
	n.mu.Unlock()
}

// commitReserved converte Reserved→Committed (débito final).
func (n *node) commitReserved(amt Amount) {
	n.mu.Lock()
	n.reserved = n.reserved.Sub(amt)
	n.committed = n.committed.Add(amt)
	n.mu.Unlock()
}

// NodeState é uma leitura consistente dos contadores de um nó (para snapshot,
// reconstrução e inspecção).
type NodeState struct {
	Limit     Amount
	Reserved  Amount
	Committed Amount
}

// Budget é a implementação de referência in-memory do orçamento hierárquico com
// reserva atómica (CAS). Satisfaz [Reserver], o seam plugável que produção troca
// por um token-bucket distribuído (ADR-008). Seguro para concorrência.
type Budget struct {
	treeID string

	mu    sync.RWMutex // guarda a estrutura das maps (nodes/reservations)
	nodes map[string]*node
	res   map[string]*reservationState

	emitter Emitter // log durável (Event Store); nunca nil (nopEmitter por omissão)
	idseq   atomic.Uint64
}

// Option configura um Budget na construção.
type Option func(*Budget)

// WithEmitter injecta o sink de eventos durável (ver [NewEventStoreEmitter]).
// Sem ele, o Budget é puramente in-memory (caminho rápido, sem log durável).
func WithEmitter(e Emitter) Option {
	return func(b *Budget) {
		if e != nil {
			b.emitter = e
		}
	}
}

// New constrói um Budget com o nó RAIZ (id = treeID) e o seu limite. As
// sub-árvores adicionam-se com [Budget.AddNode].
func New(treeID string, rootLimit Amount, opts ...Option) (*Budget, error) {
	if !rootLimit.nonNegative() {
		return nil, ErrInvalidLimit
	}
	b := &Budget{
		treeID:  treeID,
		nodes:   make(map[string]*node),
		res:     make(map[string]*reservationState),
		emitter: nopEmitter{},
	}
	b.nodes[treeID] = &node{id: treeID, parent: nil, limit: rootLimit}
	for _, o := range opts {
		o(b)
	}
	return b, nil
}

// AddNode regista uma sub-árvore com o limite dado sob parentID. O limite da
// sub-árvore pode exceder o do pai — a admissão hierárquica garante na mesma que
// o CONSUMO efectivo nunca ultrapassa nenhum ancestral (o pai é o tecto real).
func (b *Budget) AddNode(nodeID, parentID string, limit Amount) error {
	if !limit.nonNegative() {
		return ErrInvalidLimit
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.nodes[nodeID]; ok {
		return ErrNodeExists
	}
	parent, ok := b.nodes[parentID]
	if !ok {
		return ErrUnknownParent
	}
	b.nodes[nodeID] = &node{id: nodeID, parent: parent, limit: limit}
	return nil
}

// RemoveNode retira uma sub-árvore da árvore de orçamento. É o outro lado de
// [Budget.AddNode] e existe porque um nó por-run tem de ser LIBERTADO no fim do run
// (AOS-256): sem libertação, cada run deixaria um nó vivo para sempre (crescimento
// ilimitado do mapa) e a RETOMA do mesmo run — que reutiliza o RunID — colidiria com
// [ErrNodeExists].
//
// IDEMPOTENTE por desenho (nó inexistente ⇒ nil), pela mesma razão que
// [Budget.Release] o é: a libertação é chamada por `defer` e tem de ser segura em
// todos os caminhos (retorno normal, erro e panic), incluindo duas vezes.
//
// A RAIZ nunca se remove ([ErrRootRemoval]): removê-la deixaria os nós restantes com
// uma cadeia de ancestrais que já não é alcançável por id — o tecto do topo passaria
// a ser invisível a [Budget.AddNode] sem nunca deixar de ser debitado.
//
// SEMÂNTICA DAS RESERVAS PENDENTES: uma reserva já emitida guarda a sua PRÓPRIA
// cadeia de ponteiros ([reservationState.chain]), pelo que Commit/Release de uma
// reserva feita antes da remoção continuam a debitar/creditar correctamente os
// ancestrais — remover o nó impede reservas NOVAS nele, não corrompe as em curso.
func (b *Budget) RemoveNode(nodeID string) error {
	if nodeID == b.treeID {
		return ErrRootRemoval
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.nodes, nodeID)
	return nil
}

// TreeID devolve a raiz da árvore.
func (b *Budget) TreeID() string { return b.treeID }

// Available devolve o headroom corrente de um nó (Limit − Reserved − Committed).
func (b *Budget) Available(nodeID string) (Amount, error) {
	b.mu.RLock()
	n, ok := b.nodes[nodeID]
	b.mu.RUnlock()
	if !ok {
		return Amount{}, ErrUnknownNode
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.availableLocked(), nil
}

// ancestry devolve a cadeia de nós do nó pedido até à raiz (inclusive).
func (n *node) ancestry() []*node {
	chain := make([]*node, 0, 4)
	for cur := n; cur != nil; cur = cur.parent {
		chain = append(chain, cur)
	}
	return chain
}

// Reserve debita amt em Reserved em TODA a cadeia de ancestrais do nó, por
// compare-and-swap atómico em cada nível. Se algum nível não tiver headroom
// (nalguma dimensão), os níveis já debitados são revertidos (rollback parcial) e
// devolve-se [ErrNoHeadroom] — NUNCA se ultrapassa o limite de nenhum nível.
//
// Em sucesso emite budget.reserved no log durável; se a emissão falhar, a
// reserva é revertida por inteiro (fail-closed: não se concede headroom que não
// se consegue registar).
func (b *Budget) Reserve(ctx context.Context, nodeID string, amt Amount) (Reservation, error) {
	if err := ctx.Err(); err != nil {
		return Reservation{}, err
	}
	if !amt.validReserve() {
		return Reservation{}, ErrInvalidAmount
	}

	b.mu.RLock()
	n, ok := b.nodes[nodeID]
	b.mu.RUnlock()
	if !ok {
		return Reservation{}, ErrUnknownNode
	}

	chain := n.ancestry()

	// Débito atómico ao subir a cadeia; rollback do prefixo já reservado se algum
	// nível falhar. Cada tryReserve é indivisível → sem overshoot em nenhum nível.
	for i, cur := range chain {
		if !cur.tryReserve(amt) {
			for _, done := range chain[:i] {
				done.undoReserve(amt)
			}
			return Reservation{}, ErrNoHeadroom
		}
	}

	id := b.treeID + "-r" + strconv.FormatUint(b.idseq.Add(1), 10)
	rs := &reservationState{
		res:   Reservation{ID: id, TreeID: b.treeID, NodeID: nodeID, Amount: amt},
		chain: chain,
	}
	b.mu.Lock()
	b.res[id] = rs
	b.mu.Unlock()

	if err := b.emit(ctx, EventReserved, rs); err != nil {
		// Não conseguimos registar de forma durável → não concedemos a reserva.
		for _, cur := range chain {
			cur.undoReserve(amt)
		}
		b.mu.Lock()
		delete(b.res, id)
		b.mu.Unlock()
		return Reservation{}, err
	}
	return rs.res, nil
}

// Commit converte Reserved→Committed (débito final) em toda a cadeia. Idempotente
// por reservation.ID: um segundo commit é no-op; commit após release devolve
// [ErrCommitAfterRelease].
//
// Fail-closed simétrico ao [Budget.Reserve]: emite o facto durável ANTES de
// aplicar a mutação dos contadores. Se a emissão falhar, a transição de estado é
// revertida para pending e os contadores NÃO são tocados — o estado in-memory
// nunca fica "committed" quando o log durável só tem "reserved" (preserva a
// invariante Rebuild==in-memory), e um retry pode re-tentar a confirmação.
func (b *Budget) Commit(ctx context.Context, r Reservation) error {
	rs, err := b.lookup(r.ID)
	if err != nil {
		return err
	}
	if rs.state.CompareAndSwap(int32(statePending), int32(stateCommitted)) {
		if err := b.emit(ctx, EventCommitted, rs); err != nil {
			// Não conseguimos registar de forma durável → não confirmamos a mutação:
			// reverte a transição (volta a pending) sem mexer nos contadores.
			rs.state.Store(int32(statePending))
			return err
		}
		for _, cur := range rs.chain {
			cur.commitReserved(rs.res.Amount)
		}
		return nil
	}
	if resState(rs.state.Load()) == stateCommitted {
		return nil // idempotente
	}
	return ErrCommitAfterRelease
}

// Release devolve Reserved a Available (rollback) em toda a cadeia. Idempotente
// por reservation.ID: um segundo release é no-op; release após commit devolve
// [ErrReleaseAfterCommit]. Garante que reserva não consumida em falha/
// cancelamento não faz LEAK de orçamento.
//
// Fail-closed simétrico ao [Budget.Reserve]: emite o facto durável ANTES de
// libertar o headroom. Se a emissão falhar, a transição de estado é revertida
// para pending e os contadores NÃO são tocados — o headroom permanece reservado
// (a coincidir com o log durável, que ainda regista a reserva), preservando a
// invariante Rebuild==in-memory; um retry pode re-tentar a libertação.
func (b *Budget) Release(ctx context.Context, r Reservation) error {
	rs, err := b.lookup(r.ID)
	if err != nil {
		return err
	}
	if rs.state.CompareAndSwap(int32(statePending), int32(stateReleased)) {
		if err := b.emit(ctx, EventReleased, rs); err != nil {
			// Não conseguimos registar a libertação → não a aplicamos: reverte a
			// transição (volta a pending) e mantém o headroom reservado.
			rs.state.Store(int32(statePending))
			return err
		}
		for _, cur := range rs.chain {
			cur.undoReserve(rs.res.Amount)
		}
		return nil
	}
	if resState(rs.state.Load()) == stateReleased {
		return nil // idempotente
	}
	return ErrReleaseAfterCommit
}

// lookup resolve o estado autoritativo de uma reserva pelo ID.
func (b *Budget) lookup(id string) (*reservationState, error) {
	b.mu.RLock()
	rs, ok := b.res[id]
	b.mu.RUnlock()
	if !ok {
		return nil, ErrReservationNotFound
	}
	return rs, nil
}

// Snapshot devolve uma leitura consistente dos contadores de cada nó. É o estado
// in-memory autoritativo, comparável com [Rebuild] sobre os eventos.
func (b *Budget) Snapshot() map[string]NodeState {
	b.mu.RLock()
	ns := make([]*node, 0, len(b.nodes))
	for _, n := range b.nodes {
		ns = append(ns, n)
	}
	b.mu.RUnlock()

	out := make(map[string]NodeState, len(ns))
	for _, n := range ns {
		n.mu.Lock()
		out[n.id] = NodeState{Limit: n.limit, Reserved: n.reserved, Committed: n.committed}
		n.mu.Unlock()
	}
	return out
}

// Reserver é o seam plugável do orçamento — a superfície mínima de que o
// adaptador [BudgetCheck] depende. [Budget] (in-memory, CAS) é a implementação
// de referência; produção troca-a por um token-bucket distribuído (Redis/
// consenso) sem tocar no RM (ADR-008).
type Reserver interface {
	Reserve(ctx context.Context, nodeID string, amt Amount) (Reservation, error)
	Commit(ctx context.Context, r Reservation) error
	Release(ctx context.Context, r Reservation) error
}

// Assegura em compile-time que Budget satisfaz o seam Reserver.
var _ Reserver = (*Budget)(nil)
