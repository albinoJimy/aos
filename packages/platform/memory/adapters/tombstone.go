package adapters

import (
	"encoding/json"
	"fmt"

	"github.com/aos-ref/platform/memory/domain"
)

// tombstonePayload é o corpo do evento memory.record.deleted: a identidade do
// registo apagado (suficiente para o replay remover a entrada) MAIS a atribuição
// da remoção (agent_id/run_id/provenance), para que um delete seja auditável no
// próprio log append-only tal como uma escrita.
type tombstonePayload struct {
	ID         string            `json:"id"`
	AgentID    string            `json:"agent_id"`
	RunID      string            `json:"run_id"`
	Provenance domain.Provenance `json:"provenance"`
}

// marshalTombstone serializa o payload do tombstone (id + atribuição) de forma
// segura para qualquer id/valor.
func marshalTombstone(id, agentID, runID string, prov domain.Provenance) ([]byte, error) {
	return json.Marshal(tombstonePayload{
		ID:         id,
		AgentID:    agentID,
		RunID:      runID,
		Provenance: prov,
	})
}

// decodeDeletedID extrai o id de um payload de tombstone. Fail-closed: um payload
// indescodificável é registo corrompido no log.
func decodeDeletedID(raw []byte) (string, error) {
	var p tombstonePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("%w: tombstone: %v", domain.ErrCorruptRecord, err)
	}
	return p.ID, nil
}
