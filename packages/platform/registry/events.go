package registry

import (
	"encoding/json"

	"github.com/aos-ref/platform/registry/domain"
)

// streamRegistry é o stream do Event Store onde vivem os factos do catálogo. Um
// stream único dá um log total-ordenado e auditável de publicações e transições de
// estado — o material de replay a partir do qual o catálogo é reconstruído (a ES é
// a fonte de verdade, ADR-007; não há estado autoritativo em RAM).
const streamRegistry = "registry"

// Tipos de evento do catálogo, gravados no Event Store (AOS-002).
const (
	// EventTypePublished — um artefacto foi publicado (entra sempre em staging).
	EventTypePublished = "registry.artifact.published"
	// EventTypeStatusChanged — o estado de ciclo de vida de uma versão transitou.
	EventTypeStatusChanged = "registry.artifact.status_changed"
)

// publishedPayload é o corpo JSON de registry.artifact.published. Grava os campos
// essenciais da entrada (tecnica/05 §3). Não contém segredos — digest e signature
// são públicos; os credential_scopes são declarações, não segredos.
type publishedPayload struct {
	ID         string              `json:"id"`
	Version    string              `json:"version"`
	Kind       domain.ArtifactKind `json:"kind"`
	Digest     string              `json:"digest"`
	Signature  string              `json:"signature,omitempty"`
	Contract   domain.Contract     `json:"contract"`
	Provenance domain.Provenance   `json:"provenance"`
	Status     domain.Status       `json:"status"`
}

// statusChangedPayload é o corpo JSON de registry.artifact.status_changed. Regista
// a transição (from→to) e o instante, suficiente para reconstruir o estado corrente
// por fold do log.
type statusChangedPayload struct {
	ID      string        `json:"id"`
	Version string        `json:"version"`
	From    domain.Status `json:"from"`
	To      domain.Status `json:"to"`
	Ts      string        `json:"ts"`
}

// encodePublished serializa uma entrada recém-publicada para o payload de evento.
func encodePublished(e domain.Entry) (json.RawMessage, error) {
	p := publishedPayload{
		ID:         e.ID,
		Version:    e.Version.String(),
		Kind:       e.Kind,
		Digest:     e.Digest,
		Signature:  e.Signature,
		Contract:   e.Contract,
		Provenance: e.Provenance,
		Status:     e.Status,
	}
	return json.Marshal(p)
}

// decodePublished reconstrói a entrada a partir do payload published. Fail-closed:
// uma versão mal-formada no log é propagada como erro (o replay não inventa estado).
func decodePublished(raw json.RawMessage) (domain.Entry, error) {
	var p publishedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return domain.Entry{}, err
	}
	v, err := domain.ParseVersion(p.Version)
	if err != nil {
		return domain.Entry{}, err
	}
	return domain.Entry{
		ID:         p.ID,
		Version:    v,
		Kind:       p.Kind,
		Digest:     p.Digest,
		Signature:  p.Signature,
		Contract:   p.Contract,
		Provenance: p.Provenance,
		Status:     p.Status,
	}, nil
}

// encodeStatusChanged serializa uma transição de estado.
func encodeStatusChanged(id string, v domain.Version, from, to domain.Status, ts string) (json.RawMessage, error) {
	return json.Marshal(statusChangedPayload{
		ID:      id,
		Version: v.String(),
		From:    from,
		To:      to,
		Ts:      ts,
	})
}

// decodeStatusChanged reconstrói uma transição de estado a partir do payload.
func decodeStatusChanged(raw json.RawMessage) (statusChangedPayload, error) {
	var p statusChangedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return statusChangedPayload{}, err
	}
	return p, nil
}
