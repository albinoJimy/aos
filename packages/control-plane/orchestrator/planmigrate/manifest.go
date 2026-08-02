package planmigrate

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// Manifest pina os TRÊS EIXOS de um run aprovado para replay determinístico (§3.6).
// São coordenadas INDEPENDENTES, com proveniência distinta (ver o doc do pacote):
//
//   - PlanVersion      — SCHEMA do PlanDocument (do reader congelado).
//   - PromptVersion    — COMPORTAMENTO do prompt (da captura `plan.proposed`).
//   - CapabilitiesHash — AMBIENTE / snapshot do REG (da captura `plan.proposed`).
//
// PlanHash liga o manifesto à captura: é o hash do documento aprovado, o mesmo que
// consta em `plan.approved`. O manifesto é serializável (JSON canónico) para entrar
// no registo do run; as tags mantêm os três pinos EXPLÍCITOS e separados no wire.
type Manifest struct {
	PlanID           string           `json:"plan_id"`
	PlanHash         string           `json:"plan_hash"`
	PlanVersion      plan.PlanVersion `json:"plan_version"`      // SCHEMA
	PromptVersion    string           `json:"prompt_version"`    // COMPORTAMENTO
	CapabilitiesHash string           `json:"capabilities_hash"` // AMBIENTE
}

// HashPlan devolve o hash canónico e determinístico do PlanDocument aprovado, no
// formato "sha256:<hex>". É a âncora que liga o READER (o documento) à CAPTURA (o
// hash em `plan.approved`): [Migrator.Replay] só admite um reader cujo HashPlan
// COINCIDA com o hash aprovado no stream. [plan.Encode] produz bytes estáveis
// (json.Marshal de structs sem mapas — ordem de campos fixa), pelo que o mesmo
// documento produz sempre o mesmo hash. Fail-closed: um erro de serialização (que
// não ocorre para o PlanDocument, mas é propagado por honestidade) impede o binding.
func HashPlan(doc plan.PlanDocument) (string, error) {
	raw, err := plan.Encode(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
