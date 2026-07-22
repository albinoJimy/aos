package hitl

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/aos-ref/substrate/eventstore"
)

const (
	// eventTypeNonceConsumed é o tipo do evento que marca um nonce de ratificação como
	// consumido. Cada (scope, nonce) tem o seu próprio stream de uso-único.
	eventTypeNonceConsumed = "ratification.nonce.consumed"
	// nonceStreamPrefix prefixa o stream por-nonce (evita colisão com streams de run).
	nonceStreamPrefix = "ratify-nonce:"
	// nonceProducerNHI é a identidade emissora do evento de consumo (o próprio gate).
	nonceProducerNHI = "nhi:ratification-gate"
)

// NonceAppender é o subconjunto do Event Store (AOS-002) de que o nonce-store durável
// depende: Append com concorrência optimista (WithExpectedSeq). *[eventstore.Store]
// satisfá-lo. Interface mínima para desacoplar o store concreto e o tornar testável.
type NonceAppender interface {
	Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error)
}

// EventStoreNonceStore é um [RatificationNonceStore] DURÁVEL e ATÓMICO sobre o Event
// Store (AOS-159). Fecha o buraco do endurecimento anti-replay de ADR-012: sem um
// nonce-store durável, uma ratificação assinada (identidade estável de conteúdo) podia
// re-promover N vezes — inclusive DEPOIS de um rollback que marcou a versão como má, e
// através de um restart do processo.
//
// Cada (scope, nonce) é um stream próprio; [ConsumeNonce] faz um append-if-empty
// (WithExpectedSeq(0)): a concorrência optimista do store dá o CHECK-AND-SET ATÓMICO —
// o PRIMEIRO append vence (nonce FRESCO), um segundo colide
// ([eventstore.ErrSeqConflict]/[eventstore.ErrAppendOnlyViolation] ⇒ REPLAY). O estado
// é o próprio log append-only (durável): sobrevive a um restart SEM rebuild, e um
// replay é detectado mesmo depois de o processo reiniciar (ao contrário do
// nonce-store in-memory de referência, que perderia o seen-set no restart).
type EventStoreNonceStore struct {
	store NonceAppender
}

// NewEventStoreNonceStore constrói o nonce-store durável sobre o Event Store dado.
func NewEventStoreNonceStore(store NonceAppender) *EventStoreNonceStore {
	return &EventStoreNonceStore{store: store}
}

// ConsumeNonce implementa [RatificationNonceStore]: regista atomicamente (scope, nonce)
// como usado e reporta se era FRESCO. Devolve (true, nil) para um nonce nunca visto,
// (false, nil) para um replay, e (false, err) num erro de backend — os dois últimos
// tratados como BLOQUEIO pelo gate (fail-closed). scope é o
// [SelfModArtifact.RatificationID] (uso-único por identidade de artefacto+eval).
func (n *EventStoreNonceStore) ConsumeNonce(ctx context.Context, scope string, nonce []byte) (bool, error) {
	stream := nonceStreamPrefix + scope + ":" + hex.EncodeToString(nonce)
	// Sem RunID/StepID: a idempotência do Event Store (chave = run_id+step_id)
	// deduplicaria o 2º append como "sucesso" (StatusDuplicate) ANTES do CAS,
	// mascarando o replay. Deixando-a DESLIGADA, o CAS (WithExpectedSeq(0)) é o único
	// árbitro: o 1º append vence (stream vazio, last=0), o 2º colide (last=1 ⇒
	// ErrAppendOnlyViolation) — o check-and-set atómico do uso-único.
	_, err := n.store.Append(ctx, stream, eventstore.EventInput{
		Type:     eventTypeNonceConsumed,
		Producer: eventstore.Producer{NHIID: nonceProducerNHI},
	}, eventstore.WithExpectedSeq(0))
	switch {
	case err == nil:
		return true, nil // fresco: o append-if-empty venceu
	case errors.Is(err, eventstore.ErrSeqConflict), errors.Is(err, eventstore.ErrAppendOnlyViolation):
		return false, nil // replay: o stream do nonce já tinha um consumo
	default:
		return false, err // erro de backend ⇒ bloqueio no gate (fail-closed)
	}
}
