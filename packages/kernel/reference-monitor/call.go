package referencemonitor

import (
	"hash/fnv"
)

// DelegationHop é um elo da cadeia de delegação on-behalf-of do principal: o
// sujeito (Sub) age como (ActAs) a identidade seguinte. Espelha o modelo do
// Event Store (eventstore.DelegationHop) e termina sempre num humano
// responsável (ADR-003).
type DelegationHop struct {
	Sub   string
	ActAs string
}

// Principal é a identidade não-humana (NHI) que origina a tool call, a sua
// cadeia de delegação e a autoridade (capabilities) delegada. O RM resolve e
// valida o principal no hook de identidade (AOS-005); aqui o stub é neutro.
type Principal struct {
	// NHIID é o identificador estável da identidade não-humana.
	NHIID string
	// AgentID é o identificador do agente no run corrente.
	AgentID string
	// DelegationChain é a cadeia on-behalf-of (raiz humana → agente actual).
	DelegationChain []DelegationHop
	// Authority são as capabilities que o principal pode exercer (allowlist).
	Authority []string
}

// Resource é o alvo concreto da tool call (contrato C1, tecnica/12 §4).
type Resource struct {
	Type   string // ex.: "url", "file", "db"
	Value  string // ex.: "https://api.example.com/orders"
	Region string // ex.: "eu" (soberania de dados)
}

// CallContext transporta o contexto de decisão que a política avalia: taint,
// orçamento disponível, reversibilidade e sensibilidade (contrato C1).
type CallContext struct {
	// Taint marca conteúdo untrusted que não pode autorizar acções
	// privilegiadas (ADR-005). Ex.: "trusted", "untrusted".
	Taint string
	// BudgetTokensRemaining é o headroom de orçamento por árvore (ADR-008).
	BudgetTokensRemaining int64
	// Reversibility ex.: "reversible", "irreversible".
	Reversibility string
	// Sensitivity ex.: "public", "confidential".
	Sensitivity string
}

// PortVersion é a versão SemVer da porta C1 (contrato de mediação) que este RM
// implementa. É gravada em cada evento de mediação para que consumidores possam
// evoluir com o contrato (convenção transversal C1, tecnica/12 §72).
const PortVersion = "1.0.0"

// Call é o pedido de tool call submetido a [Monitor.Mediate]. É a única forma
// de descrever uma acção externa no AOS; nenhuma via alternativa a executa.
type Call struct {
	// RequestID correlaciona a mediação com o tracing distribuído (OTel) — é a
	// convenção transversal a todos os contratos (C1, tecnica/12 §72). Opcional
	// em AOS-003; propagado ao evento de auditoria quando presente.
	RequestID string
	// RunID e StepID correlacionam a mediação com a trajectória no Event Store
	// (stream_id = RunID; idempotency_key = RunID:StepID).
	RunID        string
	StepID       string
	ParentStepID string
	// ToolID identifica a tool registada a despachar (default-deny: uma tool
	// não registada é negada).
	ToolID string
	// Capability é o direito escopado que a política avalia (ex.: "cap:http.post").
	Capability string
	Resource   Resource
	Principal  Principal
	Context    CallContext
	// Input é o payload opaco entregue à tool após permit.
	Input []byte
}

// fingerprint calcula uma impressão determinística do call que liga o Permit à
// acção autorizada. Um Permit só é válido para o call de que foi mintado
// (defesa contra reutilização cruzada). Não é um mecanismo criptográfico — a
// inviolabilidade primária vem do campo não-exportado do Permit (ver decision.go).
func fingerprint(c Call) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(c.RunID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(c.StepID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(c.ToolID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(c.Capability))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(c.Resource.Type))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(c.Resource.Value))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(c.Principal.NHIID))
	return h.Sum64()
}
