package migrations_test

import (
	"strings"
	"time"

	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/migrations"
	"github.com/aos-ref/platform/memory/schema"
)

// fixedTime é um instante determinístico para created_at (sem time.Now nos testes).
var fixedTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func ver(s string) schema.Version { v, _ := schema.ParseVersion(s); return v }

// workingRec constrói um domain.Record de classe working com metadados completos e
// uma dada versão de schema. É a unidade sobre a qual as migrações operam nos testes.
func workingRec(id, schemaVersion, content string, tokens int) domain.Record {
	return domain.Record{
		ID:    id,
		Class: domain.ClassWorking,
		Metadata: domain.Metadata{
			AgentID:       "agent-1",
			RunID:         "run-1",
			Provenance:    domain.ProvenanceTrusted,
			CreatedAt:     fixedTime,
			TTLClass:      domain.TTLEphemeral,
			SchemaVersion: schemaVersion,
		},
		Body: domain.WorkingBody{TurnIndex: 0, Content: content, TokenCount: tokens},
	}
}

// suffix é a marca reversível que Up acrescenta ao Content (e Down remove). Torna
// o par Up/Down um INVERSO exacto — a base da prova de "sem perda de dados".
const suffix = "|migrated"

// makeMigration constrói uma migração reversível de working entre duas versões. Up
// acrescenta o sufixo e estampa To; Down remove-o e estampa From. Determinística.
func makeMigration(id, from, to string) migrations.Migration {
	fromV, toV := ver(from), ver(to)
	return migrations.Migration{
		ID:    id,
		Class: domain.ClassWorking,
		From:  fromV,
		To:    toV,
		Up: func(r domain.Record) (domain.Record, error) {
			b := r.Body.(domain.WorkingBody)
			b.Content = b.Content + suffix
			out := r.Clone()
			out.Body = b
			out.Metadata.SchemaVersion = to
			return out, nil
		},
		Down: func(r domain.Record) (domain.Record, error) {
			b := r.Body.(domain.WorkingBody)
			b.Content = strings.TrimSuffix(b.Content, suffix)
			out := r.Clone()
			out.Body = b
			out.Metadata.SchemaVersion = from
			return out, nil
		},
	}
}

// makeFailingMigration constrói uma migração cujo Up FALHA para o registo cujo
// Content é o gatilho dado — para provar o rollback de migração falhada.
func makeFailingMigration(id, from, to, trigger string) migrations.Migration {
	m := makeMigration(id, from, to)
	base := m.Up
	m.Up = func(r domain.Record) (domain.Record, error) {
		if r.Body.(domain.WorkingBody).Content == trigger {
			return domain.Record{}, errBoom
		}
		return base(r)
	}
	return m
}

type boomError struct{}

func (boomError) Error() string { return "boom: transform falhou de proposito" }

var errBoom = boomError{}

func contentOf(r domain.Record) string { return r.Body.(domain.WorkingBody).Content }
