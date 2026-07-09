package eventstore

import "encoding/json"

// SchemaVersion é a versão corrente do envelope de evento publicada no registo
// de schemas (ver schemas/event-envelope-1.0.json).
const SchemaVersion = "1.0"

// DelegationHop é um elo da cadeia de delegação on-behalf-of do produtor: o
// sujeito (sub) age como (act_as) a identidade seguinte.
type DelegationHop struct {
	Sub   string `json:"sub"`
	ActAs string `json:"act_as"`
}

// Producer identifica a NHI (identidade não-humana) que emitiu o evento, a sua
// cadeia de delegação (que termina num humano responsável) e o scope activo.
type Producer struct {
	NHIID           string          `json:"nhi_id"`
	DelegationChain []DelegationHop `json:"delegation_chain"`
	Scope           []string        `json:"scope"`
}

// clone devolve uma cópia profunda do produtor para que o estado guardado nunca
// seja partilhado com o chamador.
func (p Producer) clone() Producer {
	cp := Producer{NHIID: p.NHIID}
	if p.DelegationChain != nil {
		cp.DelegationChain = make([]DelegationHop, len(p.DelegationChain))
		copy(cp.DelegationChain, p.DelegationChain)
	}
	if p.Scope != nil {
		cp.Scope = make([]string, len(p.Scope))
		copy(cp.Scope, p.Scope)
	}
	return cp
}

// EventInput é o que o produtor fornece a Append. O store atribui event_id, seq,
// ts e idempotency_key — nunca o chamador.
type EventInput struct {
	// Type é o nome canónico do facto (ex.: "tool.call.dispatched").
	Type string
	// Payload é o conteúdo do evento. Neste reference impl é inline; em produção
	// é uma referência de payload cifrada por titular (ver tecnica/13 §3).
	Payload json.RawMessage
	// SchemaVersion do payload/envelope; se vazio assume SchemaVersion corrente.
	SchemaVersion string
	// RunID e StepID formam a idempotency_key = run_id + ":" + step_id.
	RunID        string
	StepID       string
	ParentStepID string
	// Producer é a identidade emissora e a sua cadeia de delegação.
	Producer Producer
}

// Event é o envelope canónico append-only. As tags JSON honram o ticket AOS-002
// e tecnica/13 §3/§4.
type Event struct {
	EventID        string          `json:"event_id"`
	StreamID       string          `json:"stream_id"`
	Seq            uint64          `json:"seq"`
	Type           string          `json:"type"`
	Ts             string          `json:"ts"`
	Producer       Producer        `json:"producer"`
	Payload        json.RawMessage `json:"payload"`
	SchemaVersion  string          `json:"schema_version"`
	RunID          string          `json:"run_id"`
	StepID         string          `json:"step_id"`
	ParentStepID   string          `json:"parent_step_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key"`
}

// clone devolve uma cópia profunda do evento. Read e AppendResult devolvem
// sempre clones para que o log guardado seja imutável do ponto de vista do
// chamador (append-only estrito).
func (e Event) clone() Event {
	cp := e
	cp.Producer = e.Producer.clone()
	if e.Payload != nil {
		cp.Payload = make(json.RawMessage, len(e.Payload))
		copy(cp.Payload, e.Payload)
	}
	return cp
}

// idempotencyKey calcula a chave determinística a partir de run_id e step_id.
func idempotencyKey(runID, stepID string) string {
	return runID + ":" + stepID
}

// hasIdempotency indica se o input participa na deduplicação (pelo menos um de
// run_id/step_id presente).
func hasIdempotency(runID, stepID string) bool {
	return runID != "" || stepID != ""
}

// Status é o resultado de um Append do ponto de vista da idempotência.
type Status string

const (
	// StatusCommitted indica um evento novo escrito e replicado com quórum.
	StatusCommitted Status = "committed"
	// StatusDuplicate indica que a idempotency_key já existia; devolve o seq
	// committed original sem duplicar.
	StatusDuplicate Status = "duplicate"
)

// AppendResult é o retorno de um Append bem-sucedido.
type AppendResult struct {
	Seq    uint64
	Status Status
	Event  Event
}

// Filter selecciona que eventos um subscritor recebe. Um campo vazio não filtra
// (recebe tudo nessa dimensão). Streams e Types são combinados por AND.
type Filter struct {
	Streams []string
	Types   []string
}

// matches indica se um evento passa o filtro.
func (f Filter) matches(e Event) bool {
	if len(f.Streams) > 0 && !contains(f.Streams, e.StreamID) {
		return false
	}
	if len(f.Types) > 0 && !contains(f.Types, e.Type) {
		return false
	}
	return true
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// Handler é invocado (na goroutine do subscritor) por cada evento entregue.
type Handler func(Event)

// AppendOption configura um Append individual.
type AppendOption func(*appendOpts)

type appendOpts struct {
	hasExpected bool
	expectedSeq uint64
}

// WithExpectedSeq activa a concorrência optimista (CAS): o Append só procede se
// o último seq committed do stream for exactamente n. Ver Store.Append para a
// semântica exacta de conflito vs violação append-only.
func WithExpectedSeq(n uint64) AppendOption {
	return func(o *appendOpts) {
		o.hasExpected = true
		o.expectedSeq = n
	}
}
