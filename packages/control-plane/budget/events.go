package budget

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento autoritativo do orçamento gravados no Event Store (AOS-002).
// Cada mutação de contadores emite exactamente um evento; a repetição idempotente
// de commit/release NÃO emite. O log é a fonte durável — [Rebuild] reconstrói os
// contadores a partir dele.
const (
	// EventReserved — headroom debitado em Reserved na cadeia (budget.reserved).
	EventReserved = "budget.reserved"
	// EventCommitted — Reserved→Committed na cadeia (budget.committed).
	EventCommitted = "budget.committed"
	// EventReleased — Reserved revertido na cadeia (budget.released).
	EventReleased = "budget.released"
)

// Emitter é a porta mínima de log durável de que o Budget depende. Produção usa
// [NewEventStoreEmitter] (Event Store, AOS-002); testes puramente in-memory usam
// o nopEmitter por omissão.
type Emitter interface {
	// Emit grava um facto de orçamento de forma durável e idempotente.
	Emit(ctx context.Context, treeID, evType, stepID string, payload eventPayload) error
}

// eventPayload é o corpo JSON de um evento de orçamento. Carrega o suficiente
// para reconstruir os contadores de TODOS os nós afectados (a cadeia de
// ancestrais debitada) sem depender de estado externo. Não contém segredos.
type eventPayload struct {
	TreeID        string   `json:"tree_id"`
	ReservationID string   `json:"reservation_id"`
	NodeID        string   `json:"node_id"`
	Nodes         []string `json:"nodes"` // cadeia de nós afectados (folha→raiz)
	Amount        Amount   `json:"amount"`
}

// emit constrói o payload a partir da reserva e delega no Emitter. O stepID
// deriva do reservation.ID + tipo, dando idempotency_key estável no Event Store
// (run_id=tree_id, step_id=<res>:<tipo>): um re-emit dedup no store.
func (b *Budget) emit(ctx context.Context, evType string, rs *reservationState) error {
	nodes := make([]string, len(rs.chain))
	for i, n := range rs.chain {
		nodes[i] = n.id
	}
	pl := eventPayload{
		TreeID:        b.treeID,
		ReservationID: rs.res.ID,
		NodeID:        rs.res.NodeID,
		Nodes:         nodes,
		Amount:        rs.res.Amount,
	}
	stepID := rs.res.ID + ":" + evType
	return b.emitter.Emit(ctx, b.treeID, evType, stepID, pl)
}

// nopEmitter é o Emitter por omissão: descarta (Budget puramente in-memory).
type nopEmitter struct{}

func (nopEmitter) Emit(context.Context, string, string, string, eventPayload) error { return nil }

// appender é o subconjunto do Event Store de que o emitter depende (facilita
// testes e desacopla da superfície completa do store).
type appender interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
}

// eventStoreEmitter grava os eventos de orçamento no Event Store (AOS-002).
type eventStoreEmitter struct {
	store    appender
	producer eventstore.Producer
}

// NewEventStoreEmitter constrói um [Emitter] durável sobre o Event Store. O
// producer é a NHI que opera o admission control (cadeia de delegação para
// audit). O stream_id de cada árvore é o seu tree_id.
func NewEventStoreEmitter(store eventstore.EventStore, producer eventstore.Producer) Emitter {
	return &eventStoreEmitter{store: store, producer: producer}
}

func (e *eventStoreEmitter) Emit(ctx context.Context, treeID, evType, stepID string, payload eventPayload) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = e.store.Append(ctx, treeID, eventstore.EventInput{
		Type:     evType,
		Payload:  raw,
		RunID:    treeID,
		StepID:   stepID,
		Producer: e.producer,
	})
	return err
}

// Rebuild reconstrói os contadores de MOVIMENTO por nó a partir dos eventos de
// orçamento do Event Store (budget.reserved/committed/released), sem precisar da
// topologia: os nós afectados vêm no payload. É a prova de que a CONTABILIDADE
// in-memory (Reserved/Committed) é reconstruível do log durável (consistência com
// AOS-002). Eventos de outros tipos são ignorados.
//
// Âmbito da reconstrução (AOS-008). Rebuild recupera SÓ os movimentos
// (reserved/committed/released): o Limit fica a zero e a estrutura da árvore não
// é reproduzida. Os LIMITES e a TOPOLOGIA (New/AddNode) são configuração
// declarativa FORA do log de eventos — não são event-sourced e, por design, não
// são reconstruíveis a partir dele; têm de vir da mesma config declarativa que
// construiu a árvore. A comparação com [Budget.Snapshot] incide, portanto, apenas
// em Reserved e Committed, não no estado completo (Limit/topologia).
//
// Robustez fail-closed: entradas inválidas (amount com dimensão negativa) ou
// somas que transbordem int64 (evento corrompido/adversarial) são rejeitadas com
// [ErrCorruptEvent] em vez de produzirem contadores errados ou negativos.
func Rebuild(events []eventstore.Event) (map[string]NodeState, error) {
	out := make(map[string]NodeState)
	apply := func(nodeID string, dReserved, dCommitted Amount) error {
		st := out[nodeID]
		r, ok := st.Reserved.addChecked(dReserved)
		if !ok {
			return fmt.Errorf("%w: overflow ao reconstruir Reserved do no %q", ErrCorruptEvent, nodeID)
		}
		c, ok := st.Committed.addChecked(dCommitted)
		if !ok {
			return fmt.Errorf("%w: overflow ao reconstruir Committed do no %q", ErrCorruptEvent, nodeID)
		}
		st.Reserved, st.Committed = r, c
		out[nodeID] = st
		return nil
	}
	for _, ev := range events {
		switch ev.Type {
		case EventReserved, EventCommitted, EventReleased:
		default:
			continue
		}
		var pl eventPayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return nil, err
		}
		amt := pl.Amount
		// Um evento autoritativo nunca carrega uma quantia negativa; rejeita entrada
		// corrompida antes de a aplicar (evita contadores negativos/errados).
		if !amt.nonNegative() {
			return nil, fmt.Errorf("%w: amount negativo no evento %s (%v)", ErrCorruptEvent, ev.Type, amt)
		}
		neg := Amount{}.Sub(amt) // -amt
		for _, nodeID := range pl.Nodes {
			var err error
			switch ev.Type {
			case EventReserved:
				err = apply(nodeID, amt, Amount{})
			case EventCommitted:
				err = apply(nodeID, neg, amt) // reserved -= amt ; committed += amt
			case EventReleased:
				err = apply(nodeID, neg, Amount{}) // reserved -= amt
			}
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
