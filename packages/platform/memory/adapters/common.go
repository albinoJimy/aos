package adapters

import (
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/ports"
)

// portVersion é a versão do contrato que ambos os adaptadores implementam. É a
// mesma fonte (ports.PortVersion) para os dois, garantindo que Version() é
// idêntico entre backends — parte do que torna o swap transparente.
const portVersion = ports.PortVersion

// idempotencyKey calcula a chave de idempotência de escrita = f(RunID, Class, ID).
// Incluir a Class garante que registos homónimos em classes diferentes NUNCA se
// deduplicam entre si (o dedup do Event Store é global ao store, não por stream),
// preservando a distinção das quatro classes. Ambos os adaptadores usam ESTA
// função — a paridade de idempotência é o que faz o contract test passar nos dois.
func idempotencyKey(runID string, class domain.MemoryClass, id string) string {
	return runID + ":" + string(class) + ":put:" + id
}

// matchesQuery aplica os filtros opcionais de uma Query (a Class já foi escopada
// pelo chamador). Vazio = não filtra nessa dimensão; combinação por AND.
func matchesQuery(rec domain.Record, q ports.Query) bool {
	if q.AgentID != "" && rec.Metadata.AgentID != q.AgentID {
		return false
	}
	if q.RunID != "" && rec.Metadata.RunID != q.RunID {
		return false
	}
	if q.Provenance != nil && rec.Metadata.Provenance != *q.Provenance {
		return false
	}
	return true
}
