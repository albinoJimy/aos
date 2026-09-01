package eventstore

import (
	"encoding/json"
	"time"
)

// envelope.go — a construção do envelope canónico, extraída de [Store.Append] para
// que QUALQUER implementação de [EventStore] a possa reutilizar.
//
// # Porque isto deixou de viver dentro do Store
//
// O contrato C2 diz que o store atribui `event_id`, `seq`, `ts` e `idempotency_key` —
// nunca o chamador. Enquanto houve uma só implementação, essa construção podia viver
// inline no Append. Com um segundo backend (AOS-100), inline significa DUPLICADA: dois
// sítios a decidir o formato do `ts`, a versão de schema por omissão, se o payload é
// copiado, como se compõe a chave de idempotência. Duas cópias divergem — e o envelope
// é a base do audit hash-chain (ADR-010), onde divergir é indistinguível de adulterar.
//
// Estas funções são a fronteira: o que é CONTRATO do envelope está aqui; o que é
// mecânica de um backend (quórum, WAL, CAS remoto) fica em cada implementação.

// NewEvent constrói o envelope canónico de um evento a partir do input do produtor.
//
// O chamador fornece o `seq` já decidido (é a implementação que o atribui, e a forma de
// o decidir é o que distingue os backends) e o instante `now`; tudo o resto é derivado
// do contrato. O payload é COPIADO: o store nunca retém uma referência à memória do
// produtor, e o Event devolvido pode ser guardado sem alias.
func NewEvent(streamID string, seq uint64, in EventInput, now time.Time) Event {
	schema := in.SchemaVersion
	if schema == "" {
		schema = SchemaVersion
	}
	ev := Event{
		EventID:        newULID(),
		StreamID:       streamID,
		Seq:            seq,
		Type:           in.Type,
		Ts:             now.UTC().Format(time.RFC3339Nano),
		Producer:       in.Producer.clone(),
		SchemaVersion:  schema,
		RunID:          in.RunID,
		StepID:         in.StepID,
		ParentStepID:   in.ParentStepID,
		IdempotencyKey: idempotencyKey(in.RunID, in.StepID),
	}
	if in.Payload != nil {
		ev.Payload = make(json.RawMessage, len(in.Payload))
		copy(ev.Payload, in.Payload)
	}
	return ev
}

// IdempotencyKey é a chave determinística de deduplicação: f(run_id, step_id).
// Exportada porque um backend remoto precisa dela para indexar o log que lê de volta.
func IdempotencyKey(runID, stepID string) string { return idempotencyKey(runID, stepID) }

// HasIdempotency indica se o input participa na deduplicação (pelo menos um de
// run_id/step_id presente). Um evento sem nenhum dos dois NÃO é deduplicado — escrever
// duas vezes produz dois eventos, e é o comportamento correcto: sem chave não há
// afirmação de identidade a honrar.
func HasIdempotency(runID, stepID string) bool { return hasIdempotency(runID, stepID) }

// Matches indica se um evento passa o filtro. Exportada para que uma implementação de
// [Subscribe] fora deste pacote aplique EXACTAMENTE a mesma semântica de filtragem —
// campos vazios não filtram, Streams e Types combinam por AND.
func (f Filter) Matches(e Event) bool { return f.matches(e) }

// Clone devolve uma cópia profunda do evento. O contrato exige que [EventStore.Read]
// devolva cópias: o chamador nunca mantém uma referência ao estado guardado.
func (e Event) Clone() Event { return e.clone() }

// ExpectedSeqOf extrai a afirmação de concorrência optimista de um conjunto de
// [AppendOption]. Devolve (n, true) se [WithExpectedSeq] foi passada.
//
// Existe porque `appendOpts` é privado, e uma implementação de [EventStore] FORA deste
// pacote não tinha como saber que o chamador pediu CAS — ficaria a escrever sempre no
// fim, silenciosamente, e a concorrência optimista de que a disciplina de posse depende
// (AOS-018, ADR-023) seria ignorada sem nenhum sinal.
func ExpectedSeqOf(opts []AppendOption) (uint64, bool) {
	var o appendOpts
	for _, opt := range opts {
		opt(&o)
	}
	return o.expectedSeq, o.hasExpected
}
