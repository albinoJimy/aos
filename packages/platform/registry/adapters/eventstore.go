// Package adapters contém os adaptadores de infra-estrutura do REG. O Journal é a
// FONTE DE VERDADE do catálogo sobre o Event Store replicado (AOS-002, ADR-007):
// escreve factos append-only e RECONSTRÓI a leitura do log por replay. Não há
// estado autoritativo em RAM nem single-writer SQLite — o catálogo é uma projecção
// determinística do stream de eventos.
package adapters

import (
	"context"
	"encoding/json"

	"github.com/aos-ref/substrate/eventstore"
)

// Journal é o adaptador append-only sobre um stream do Event Store. É deliberadamente
// genérico (não conhece os payloads do REG): grava um evento tipado e relê o log
// inteiro para que a camada de serviço o reconstrua por fold. Assim a lógica de
// domínio fica testável sem infra e a fonte de verdade permanece a ES.
type Journal struct {
	store  eventstore.EventStore
	stream string
}

// NewJournal constrói um Journal sobre o stream dado. store nil devolve um Journal
// cujas operações falham fail-closed (ver Append/ReadAll).
func NewJournal(store eventstore.EventStore, stream string) *Journal {
	return &Journal{store: store, stream: stream}
}

// Configured indica se o Journal tem um Event Store subjacente.
func (j *Journal) Configured() bool { return j != nil && j.store != nil }

// Append grava um evento tipado no fim do stream com concorrência optimista
// (WithExpectedSeq): expectedSeq é o último seq committed que o chamador observou.
// A idempotência do Event Store (idempotency_key = stream:stepID) torna a re-escrita
// exacta do mesmo facto num no-op seguro. Devolve o seq atribuído (ou o original em
// caso de duplicado).
//
// Erros de concorrência (eventstore.ErrSeqConflict / ErrAppendOnlyViolation) são
// PROPAGADOS para que a camada de serviço releia e reavalie (retry optimista).
func (j *Journal) Append(ctx context.Context, evType string, payload json.RawMessage, stepID, publisher string, expectedSeq uint64) (eventstore.AppendResult, error) {
	if !j.Configured() {
		return eventstore.AppendResult{}, eventstore.ErrClosed
	}
	return j.store.Append(ctx, j.stream, eventstore.EventInput{
		Type:    evType,
		Payload: payload,
		RunID:   j.stream,
		StepID:  stepID,
		Producer: eventstore.Producer{
			// A identidade do produtor é o publicador do artefacto (metadados de
			// proveniência; nunca um segredo). A verificação de origem por assinatura
			// é AOS-048.
			NHIID: publisher,
		},
	}, eventstore.WithExpectedSeq(expectedSeq))
}

// ReadAll relê o stream inteiro em ordem de seq ascendente e devolve os eventos e o
// último seq observado (0 se o stream ainda não existe). Um stream vazio/inexistente
// NÃO é erro — é o catálogo vazio (fonte de verdade sem factos ainda).
func (j *Journal) ReadAll(ctx context.Context) ([]eventstore.Event, uint64, error) {
	if !j.Configured() {
		return nil, 0, eventstore.ErrClosed
	}
	evs, err := j.store.Read(ctx, j.stream, 1)
	if err != nil {
		if err == eventstore.ErrStreamNotFound {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	var last uint64
	if len(evs) > 0 {
		last = evs[len(evs)-1].Seq
	}
	return evs, last, nil
}
