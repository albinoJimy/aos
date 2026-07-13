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
