package plannerevents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aos-ref/substrate/eventstore"
)

// EventReader é o ÚNICO acesso ao Event Store de que [Reconstruct] depende:
// apenas Read. *eventstore.Store satisfá-la, tal como o [replay.EventReader] de
// AOS-016. É o que torna a reconstrução ZERO-EFEITOS de forma ESTRUTURAL: não há
// Append, não há Proposer, não há modelo — o replay não detém sequer um caminho
// para re-chamar o LLM (§3.4/§6.1). O documento aprovado no log é o input; a
// reconstrução só o LÊ.
type EventReader interface {
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// PlanEvent é um facto reconstruído do stream do plano. Preserva o tipo, o seq
// (posição na ordem total do stream), o step id e os BYTES do payload EXACTAMENTE
// como foram apensos — a reconstrução é byte-a-byte (ADR-010).
type PlanEvent struct {
	Type    string
	Seq     uint64
	StepID  string
	Payload json.RawMessage
}

// ErrUnknownEventType é devolvido quando o stream contém um tipo `plan.*` fora do
// catálogo deste domínio — fail-closed: a reconstrução recusa o desconhecido em
// vez de o aceitar em silêncio (nada de defaults permissivos).
var ErrUnknownEventType = errors.New("plannerevents: tipo de evento desconhecido no stream")

// ErrUnknownDomainVersion é devolvido quando um evento do domínio carrega uma
// versão de schema diferente de [DomainVersion] — fail-closed (tecnica/18 §3.6:
// um `plan_version` fora da janela é inadmissível, não auto-migrado).
var ErrUnknownDomainVersion = errors.New("plannerevents: versão de domínio desconhecida")

// Reconstruct relê o stream do plano do Event Store e devolve a sequência de
// eventos do domínio `aos.planner.v1` na MESMA ORDEM em que foram apensos (ADR-010)
// e com os BYTES do payload preservados (byte-a-byte). NÃO re-ordena, NÃO
// deduplica, NÃO projecta por mapa: a ordem é a ordem total por-stream que o Event
// Store devolve (seq ascendente).
//
// É READ-ONLY por construção: recebe um [EventReader] (só Read), pelo que não pode
// apensar nem consultar um modelo. Nenhum evento é re-derivado — em particular
// `plan.proposed` é LIDO do log, nunca re-proposto pelo LLM (§3.4/§6.1).
//
// Fail-closed: um tipo `plan.*` fora do catálogo ([ErrUnknownEventType]) ou uma
// versão de domínio inesperada ([ErrUnknownDomainVersion]) abortam a reconstrução.
// Eventos de OUTROS domínios (famílias que não `plan.`) que partilhem o stream são
// IGNORADOS — este reconstrutor projecta só o seu domínio.
func Reconstruct(ctx context.Context, reader EventReader, planID string) ([]PlanEvent, error) {
	if reader == nil {
		return nil, fmt.Errorf("plannerevents: EventReader nil")
	}
	if planID == "" {
		return nil, fmt.Errorf("plannerevents: plan_id vazio")
	}
	events, err := reader.Read(ctx, planID, 1)
	if err != nil {
		return nil, err
	}
	out := make([]PlanEvent, 0, len(events))
	for _, ev := range events {
		if !isPlanDomain(ev.Type) {
			// Facto de outro domínio a partilhar o stream: não é nosso, ignora-se.
			continue
		}
		if !knownType(ev.Type) {
			return nil, fmt.Errorf("%w: %q (seq %d)", ErrUnknownEventType, ev.Type, ev.Seq)
		}
		if ev.SchemaVersion != DomainVersion {
			return nil, fmt.Errorf("%w: %q no evento %q (seq %d)", ErrUnknownDomainVersion, ev.SchemaVersion, ev.Type, ev.Seq)
		}
		// Cópia defensiva dos bytes: o chamador nunca partilha o buffer do store.
		payload := make(json.RawMessage, len(ev.Payload))
		copy(payload, ev.Payload)
		out = append(out, PlanEvent{
			Type:    ev.Type,
			Seq:     ev.Seq,
			StepID:  ev.StepID,
			Payload: payload,
		})
	}
	return out, nil
}

// isPlanDomain indica se um tipo de evento pertence à família `plan.` deste
// domínio. Usa o prefixo canónico — o mesmo primeiro segmento das constantes.
func isPlanDomain(t string) bool {
	const prefix = "plan."
	return len(t) > len(prefix) && t[:len(prefix)] == prefix
}

// Types projecta a sequência reconstruída na lista dos seus tipos, pela ordem —
// conveniência para asserções de ordem e para o consumidor a jusante.
func Types(seq []PlanEvent) []string {
	out := make([]string, len(seq))
	for i, e := range seq {
		out[i] = e.Type
	}
	return out
}
