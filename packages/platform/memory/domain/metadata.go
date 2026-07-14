package domain

import "time"

// Provenance é a classificação de confiança da origem de um registo de memória.
// É metadado OBRIGATÓRIO em todo o registo (prepara a quarentena de AOS-042 e a
// barreira control/data-plane do ADR-005). Valores fora do conjunto canónico são
// tratados como ausência (fail-closed).
type Provenance string

const (
	// ProvenanceTrusted — origem confiável (system / utilizador autenticado). Só
	// memória trusted alimenta o planeador (control-plane).
	ProvenanceTrusted Provenance = "trusted"

	// ProvenanceUntrusted — origem não-confiável (tool result, web, schema MCP,
	// memória derivada de untrusted). É dados, nunca instruções; a quarentena
	// concreta é AOS-042 (fora de âmbito aqui — só o metadado).
	ProvenanceUntrusted Provenance = "untrusted"
)

// Valid indica se p é uma proveniência canónica.
func (p Provenance) Valid() bool {
	return p == ProvenanceTrusted || p == ProvenanceUntrusted
}

// ProvenanceSource é o TIPO de fonte de origem de conteúdo ingerido — o "de onde
// veio" (tool_result, web, mcp_schema, system, …). É o componente FORENSE da
// proveniência, distinto da classificação trusted|untrusted: duas memórias podem
// ser ambas untrusted mas ter fontes diferentes (web vs. tool_result vs. schema
// MCP), informação crítica para o forense de memory poisoning (ASI06). Os valores
// canónicos espelham [github.com/aos-ref/platform/memory/provenance.Source]; a
// classificação trusted|untrusted deriva desta fonte na ingestão (Classify).
type ProvenanceSource string

const (
	// SourceSystem — conteúdo produzido pelo próprio sistema. Classifica trusted.
	SourceSystem ProvenanceSource = "system"
	// SourceAuthenticatedUser — input do utilizador autenticado. Classifica trusted.
	SourceAuthenticatedUser ProvenanceSource = "authenticated_user"
	// SourceToolResult — output de uma tool call (conteúdo externo). Untrusted.
	SourceToolResult ProvenanceSource = "tool_result"
	// SourceWeb — conteúdo obtido da web. Untrusted.
	SourceWeb ProvenanceSource = "web"
	// SourceMCPSchema — descrições/schemas de servidores MCP. Untrusted.
	SourceMCPSchema ProvenanceSource = "mcp_schema"
	// SourceDerivedMemory — memória derivada de outra memória (o taint deriva dos
	// pais, não da fonte directa). Untrusted por [Classify].
	SourceDerivedMemory ProvenanceSource = "derived_memory"
)

// Valid indica se s é uma fonte de proveniência canónica.
func (s ProvenanceSource) Valid() bool {
	switch s {
	case SourceSystem, SourceAuthenticatedUser, SourceToolResult, SourceWeb, SourceMCPSchema, SourceDerivedMemory:
		return true
	default:
		return false
	}
}

// TTLClass é a classe de retenção de um registo, base da política de TTL por
// classe que prepara a conformidade GDPR (ADR-011, AOS-041). É metadado
// OBRIGATÓRIO. O motor de expiração/crypto-shredding NÃO é implementado aqui —
// só o metadado tipado que o habilita.
type TTLClass string

const (
	// TTLEphemeral — retenção mínima (tipicamente memória de trabalho do turno).
	TTLEphemeral TTLClass = "ephemeral"
	// TTLShort — retenção curta.
	TTLShort TTLClass = "short"
	// TTLStandard — retenção padrão.
	TTLStandard TTLClass = "standard"
	// TTLLongLived — retenção longa (ex.: conhecimento semântico consolidado).
	TTLLongLived TTLClass = "long_lived"
	// TTLPermanent — sem expiração por política (ex.: registo episódico/audit).
	TTLPermanent TTLClass = "permanent"
)

// Valid indica se t é uma classe de retenção canónica.
func (t TTLClass) Valid() bool {
	switch t {
	case TTLEphemeral, TTLShort, TTLStandard, TTLLongLived, TTLPermanent:
		return true
	default:
		return false
	}
}

// Metadata são os metadados OBRIGATÓRIOS que TODO o registo de memória carrega.
// A ausência de qualquer um destes campos FALHA-FECHA na validação (nunca há
// default silencioso): em particular, escrever sem provenance OU sem
// schema_version é sempre rejeitado.
type Metadata struct {
	// AgentID — a NHI/agente que produziu o registo (cruza com AOS-005).
	AgentID string
	// RunID — o run de origem; raiz da idempotência f(run_id, mem_id).
	RunID string
	// Provenance — trusted|untrusted (prepara AOS-042/ADR-005).
	Provenance Provenance
	// Source — a FONTE de origem (tool_result|web|mcp_schema|system|…), componente
	// forense da proveniência que sobrevive à escrita. É estampada na ingestão
	// ([provenance.Ingestor]/fachada) a partir da fonte classificada; completa o
	// triplo (fonte, classificação, run_id) do AOS-042. Ausência é tolerada para
	// escritas que não atravessam a ingestão de proveniência; quando PRESENTE tem de
	// ser canónica (fail-closed).
	Source ProvenanceSource
	// CreatedAt — instante de criação; preenchido por relógio INJECTÁVEL na
	// fachada (determinismo — sem time.Now no caminho de decisão).
	CreatedAt time.Time
	// TTLClass — classe de retenção (prepara GDPR/TTL, ADR-011).
	TTLClass TTLClass
	// SchemaVersion — versão SemVer do schema deste registo (prepara AOS-041).
	SchemaVersion string
}

// Validate impõe a presença de TODOS os metadados obrigatórios (fail-closed).
// Devolve o primeiro erro sentinela encontrado, por ordem estável. Comparável
// com errors.Is.
func (m Metadata) Validate() error {
	if m.AgentID == "" {
		return ErrMissingAgentID
	}
	if m.RunID == "" {
		return ErrMissingRunID
	}
	if !m.Provenance.Valid() {
		return ErrMissingProvenance
	}
	// A fonte é opcional (nem toda a escrita atravessa a ingestão de proveniência),
	// mas quando presente tem de ser canónica: uma fonte forjada/desconhecida no
	// registo persistido é rejeitada (fail-closed), preservando o valor forense.
	if m.Source != "" && !m.Source.Valid() {
		return ErrInvalidProvenanceSource
	}
	if m.CreatedAt.IsZero() {
		return ErrMissingCreatedAt
	}
	if !m.TTLClass.Valid() {
		return ErrMissingTTLClass
	}
	if m.SchemaVersion == "" {
		return ErrMissingSchemaVersion
	}
	return nil
}
