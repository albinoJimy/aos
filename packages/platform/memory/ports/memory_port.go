// Package ports define o contrato PÚBLICO e VERSIONADO do Memory Service: a porta
// MemoryPort. É a fronteira que NÃO vaza o backend de armazenamento ao chamador —
// nada dos tipos do Event Store, SQLite ou de qualquer adaptador aparece nas
// assinaturas; só entidades de domínio. Um swap de backend (Event Store ↔
// in-memory) é, por isso, possível por configuração sem alterar chamadores, e é
// provado pelo contract test partilhado que ambos os adaptadores passam.
package ports

import (
	"context"

	"github.com/aos-ref/platform/memory/domain"
)

// PortVersion é a versão SemVer do CONTRATO da MemoryPort. Alterações ao contrato
// seguem o versionamento de schema de memória (AOS-041) — MAJOR para quebras,
// MINOR para adições retrocompatíveis, PATCH para correcções sem impacto de
// contrato. Os adaptadores expõem-na via Version() para negociação/observação.
const PortVersion = "1.0.0"

// DeleteContext carrega a ATRIBUIÇÃO de um Delete para que o tombstone escrito no
// log append-only seja AUDITÁVEL (quem/que run removeu o registo). Um Delete é uma
// escrita no log de audit tamper-evident tanto como um Put; por isso os seus
// campos são OBRIGATÓRIOS e validados fail-closed (exactamente como os metadados
// obrigatórios de um Put). Uma remoção não-atribuída não é permitida.
type DeleteContext struct {
	// AgentID — a NHI/agente que ordenou a remoção.
	AgentID string
	// RunID — o run de origem da remoção (raiz da idempotência do tombstone).
	RunID string
	// Provenance — proveniência da operação de remoção (trusted|untrusted).
	Provenance domain.Provenance
}

// Validate impõe a presença da atribuição obrigatória de um Delete (fail-closed).
// Reutiliza os erros sentinela dos metadados: comparável com errors.Is.
func (d DeleteContext) Validate() error {
	if d.AgentID == "" {
		return domain.ErrMissingAgentID
	}
	if d.RunID == "" {
		return domain.ErrMissingRunID
	}
	if !d.Provenance.Valid() {
		return domain.ErrMissingProvenance
	}
	return nil
}

// Query é o critério de leitura por classe. A Class é OBRIGATÓRIA e escopa a
// consulta a UMA classe — as quatro classes não se cruzam (uma Query episódica
// nunca devolve registos semânticos). Os restantes campos são filtros opcionais
// (vazio = não filtra nessa dimensão), combinados por AND.
type Query struct {
	// Class é a classe a consultar (obrigatória).
	Class domain.MemoryClass
	// AgentID filtra por agente produtor (opcional).
	AgentID string
	// RunID filtra por run de origem (opcional).
	RunID string
	// Provenance filtra por proveniência (opcional; nil = qualquer).
	Provenance *domain.Provenance
}

// MemoryPort é a porta versionada do Memory Service, com operações CRUD/query
// POR CLASSE. Todas as operações recebem/devolvem apenas entidades de domínio.
//
// Semântica de idempotência: Put é idempotente por f(RunID, Class, ID) — uma
// segunda escrita com a mesma chave é um no-op observável (devolve o registo já
// persistido, coerente com o append-only do Event Store: o duplicado ganha).
// Isto é o que permite que a MESMA suite de contrato passe em ambos os
// adaptadores.
//
// Todas as operações emitem um span OTel via a porta Tracer do Agent Runtime, com
// atributos no namespace próprio aos.memory.* (as operações CRUD de memória NÃO
// são inferência GenAI, pelo que gen_ai.* fica reservado para atributos genuínos);
// um Tracer não configurado é no-op (sem novas dependências).
type MemoryPort interface {
	// Version devolve a versão SemVer do contrato implementado (== PortVersion).
	Version() string

	// Put escreve um registo (create idempotente). Valida os metadados
	// obrigatórios (fail-closed): sem provenance OU schema_version — ou qualquer
	// outro obrigatório — devolve erro e NÃO persiste. Devolve o registo efectivo
	// (o já persistido, em caso de duplicado).
	//
	// NÃO há update-in-place: coerente com o event-sourcing, mutar = novo evento
	// com nova chave. Um Put com a MESMA f(RunID, Class, ID) de um registo já
	// persistido é um NO-OP OBSERVÁVEL — devolve o registo ORIGINAL e DESCARTA o
	// Body do chamador SEM erro (o duplicado ganha). Para actualizar de facto o
	// valor, o chamador tem de usar um RunID distinto (last-write-wins na
	// reconstrução); não deve assumir que um re-Put da mesma chave actualizou.
	Put(ctx context.Context, rec domain.Record) (domain.Record, error)

	// Get devolve o registo (class, id) ou ErrNotFound se inexistente/apagado.
	Get(ctx context.Context, class domain.MemoryClass, id string) (domain.Record, error)

	// Query devolve os registos da classe que satisfazem os filtros, por ordem
	// estável de escrita. Um resultado vazio não é erro.
	Query(ctx context.Context, q Query) ([]domain.Record, error)

	// Delete marca (class, id) como apagado. No backend append-only isto é um
	// tombstone (novo evento), nunca uma mutação do log. Idempotente: apagar o que
	// não existe é no-op sem erro. O DeleteContext é OBRIGATÓRIO e validado
	// fail-closed (agent_id/run_id/provenance) para que a remoção fique atribuída e
	// auditável no próprio log append-only.
	Delete(ctx context.Context, class domain.MemoryClass, id string, dc DeleteContext) error
}
