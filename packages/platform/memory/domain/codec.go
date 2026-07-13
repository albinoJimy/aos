package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// A codificação persistida de um Record é um envelope estável e determinístico:
// os campos têm ordem e tags fixas, o instante serializa em RFC3339Nano UTC e o
// corpo tipado vai como json.RawMessage discriminado por Class. É esta forma que
// o adaptador de Event Store escreve como payload de evento e reconstrói na
// leitura, e que o adaptador in-memory usa para garantir cópia-por-valor. Manter
// a serialização no domínio garante que AMBOS os adaptadores partilham
// exactamente o mesmo formato (pré-requisito do backend-swap por contrato).

type metadataEnvelope struct {
	AgentID       string     `json:"agent_id"`
	RunID         string     `json:"run_id"`
	Provenance    Provenance `json:"provenance"`
	CreatedAt     string     `json:"created_at"`
	TTLClass      TTLClass   `json:"ttl_class"`
	SchemaVersion string     `json:"schema_version"`
}

type recordEnvelope struct {
	ID       string           `json:"id"`
	Class    MemoryClass      `json:"class"`
	Metadata metadataEnvelope `json:"metadata"`
	Body     json.RawMessage  `json:"body"`
}

// MarshalRecord serializa um Record na forma persistida estável. Não valida (o
// chamador valida antes de escrever); serializa fielmente o que recebe.
func MarshalRecord(r Record) ([]byte, error) {
	if r.Body == nil {
		return nil, ErrNilBody
	}
	body, err := json.Marshal(r.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptRecord, err)
	}
	env := recordEnvelope{
		ID:    r.ID,
		Class: r.Class,
		Metadata: metadataEnvelope{
			AgentID:       r.Metadata.AgentID,
			RunID:         r.Metadata.RunID,
			Provenance:    r.Metadata.Provenance,
			CreatedAt:     r.Metadata.CreatedAt.UTC().Format(time.RFC3339Nano),
			TTLClass:      r.Metadata.TTLClass,
			SchemaVersion: r.Metadata.SchemaVersion,
		},
		Body: body,
	}
	return json.Marshal(env)
}

// UnmarshalRecord reconstrói um Record a partir da forma persistida. O corpo
// tipado é resolvido pela Class (discriminador). Fail-closed: uma classe
// desconhecida ou um corpo indescodificável devolve ErrCorruptRecord.
func UnmarshalRecord(data []byte) (Record, error) {
	var env recordEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Record{}, fmt.Errorf("%w: %v", ErrCorruptRecord, err)
	}
	body, err := unmarshalBody(env.Class, env.Body)
	if err != nil {
		return Record{}, err
	}
	var createdAt time.Time
	if env.Metadata.CreatedAt != "" {
		t, perr := time.Parse(time.RFC3339Nano, env.Metadata.CreatedAt)
		if perr != nil {
			return Record{}, fmt.Errorf("%w: created_at: %v", ErrCorruptRecord, perr)
		}
		createdAt = t
	}
	return Record{
		ID:    env.ID,
		Class: env.Class,
		Metadata: Metadata{
			AgentID:       env.Metadata.AgentID,
			RunID:         env.Metadata.RunID,
			Provenance:    env.Metadata.Provenance,
			CreatedAt:     createdAt,
			TTLClass:      env.Metadata.TTLClass,
			SchemaVersion: env.Metadata.SchemaVersion,
		},
		Body: body,
	}, nil
}

// unmarshalBody resolve o corpo tipado pela classe. É o único ponto onde o
// discriminador Class → tipo concreto vive.
func unmarshalBody(class MemoryClass, raw json.RawMessage) (Body, error) {
	switch class {
	case ClassEpisodic:
		var b EpisodicBody
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("%w: episodic body: %v", ErrCorruptRecord, err)
		}
		return b, nil
	case ClassSemantic:
		var b SemanticBody
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("%w: semantic body: %v", ErrCorruptRecord, err)
		}
		return b, nil
	case ClassProcedural:
		var b ProceduralBody
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("%w: procedural body: %v", ErrCorruptRecord, err)
		}
		return b, nil
	case ClassWorking:
		var b WorkingBody
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("%w: working body: %v", ErrCorruptRecord, err)
		}
		return b, nil
	default:
		return nil, fmt.Errorf("%w: classe desconhecida %q", ErrCorruptRecord, class)
	}
}
