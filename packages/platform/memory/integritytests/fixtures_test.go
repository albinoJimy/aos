// Package integritytests é a SUITE DE INTEGRIDADE/MIGRAÇÃO/PROVENIÊNCIA de memória
// do AOS (AOS-044) — o safety net de QA do EPIC-04. É deliberadamente um pacote SÓ
// de testes (ficheiros _test.go): NÃO reimplementa nenhuma classe de memória — COMPÕE
// e afere os subpacotes reais já entregues (record/projection/working/episodic/
// semantic/provenance/schema/migrations/compression), provando em CI as três
// propriedades não-negociáveis da camada:
//
//   - INTEGRIDADE (Princípio 4, AOS-036): projecção/eviction/compressão nunca apagam
//     do REGISTO o que o audit trail exige;
//   - MIGRAÇÃO (AOS-041): round-trip expand→migrate→contract sem perda, rollback de
//     migração falhada, idempotência;
//   - PROVENIÊNCIA/SEGURANÇA (AOS-042): quarentena NÃO autoriza acções, taint
//     transitivo.
//
// Mais: crypto-shredding/TTL (AOS-038, irrecuperável SEM partir a hash-chain) e
// estabilidade de cache (AOS-043, prefixo imutável sob compressão).
//
// META-TESTES (metatests_test.go): cada invariante VIOLADA (injectada) é DETECTADA —
// a prova de que a suite NÃO é green-vazio. As fixtures são deterministas (relógio,
// entropia e ids injectáveis; sem time.Now/rand na decisão) e reprodutíveis.
//
// O gate CI fail-closed que corre esta suite é scripts/ci/memory.sh (à imagem do
// gate replay de AOS-024), ligado a run.sh/Makefile/ci.yml, com o self-test
// (scripts/ci/selftest.sh, secção E) a provar que uma violação injectada o torna
// VERMELHO (selftest_poison_test.go).
package integritytests

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/adapters"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/migrations"
	"github.com/aos-ref/platform/memory/ports"
	"github.com/aos-ref/platform/memory/projection"
	"github.com/aos-ref/platform/memory/provenance"
	"github.com/aos-ref/platform/memory/record"
	"github.com/aos-ref/platform/memory/schema"
	"github.com/aos-ref/platform/memory/working"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Erros sentinela da suite (o que os verificadores devolvem quando uma
// invariante é VIOLADA). São a linguagem partilhada pelos testes da suite (que
// exigem nil) e pelos meta-testes (que exigem um destes — prova de detecção).
// ---------------------------------------------------------------------------

var (
	// errRecordErased — a via de higiene apagou do registo algo que o audit exige.
	errRecordErased = errors.New("integridade: registo apagado (o audit trail exige-o)")
	// errRawLeaked — conteúdo cru vazou para a projecção/sumário (higiene indevida).
	errRawLeaked = errors.New("integridade: RawContent vazou para a projecção")
	// errRegisterIncomplete — a via de registo descartou turnos (não é completa).
	errRegisterIncomplete = errors.New("integridade: registo incompleto (turnos descartados)")
	// errMigrationLoss — o round-trip de migração perdeu/corrompeu dados.
	errMigrationLoss = errors.New("migração: round-trip com perda de dados")
	// errQuarantineBreached — memória untrusted chegou ao control-plane (planeador).
	errQuarantineBreached = errors.New("proveniência: quarentena furada (untrusted no control-plane)")
	// errQuarantineAuthorizes — memória em quarentena conseguiu autorizar uma acção.
	errQuarantineAuthorizes = errors.New("proveniência: quarentena autorizou uma acção privilegiada")
	// errChainBroken — a hash-chain de audit não verifica (partida/adulterada).
	errChainBroken = errors.New("shredding: hash-chain partida")
	// errPrefixMutated — o prefixo imutável mudou sob compressão (cache thrash).
	errPrefixMutated = errors.New("cache: prefixo mutado sob compressão")
)

// ---------------------------------------------------------------------------
// Helpers deterministas (relógio/entropia/ids injectáveis; sem time.Now/rand).
// ---------------------------------------------------------------------------

// fixedTime é um instante determinístico para created_at (sem time.Now nos testes).
var fixedTime = time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)

// fixedClock devolve um relógio fixo (determinismo do created_at / timestamp de audit).
func fixedClock() func() time.Time { return func() time.Time { return fixedTime } }

// seqRand é uma RandSource DETERMINÍSTICA para a cripto de envelope (episódica): um
// contador monotónico substitui crypto/rand — a selagem torna-se reproduzível.
type seqRand struct {
	mu sync.Mutex
	n  uint64
}

func (r *seqRand) fill(p []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range p {
		r.n++
		p[i] = byte(r.n)
	}
	return nil
}

// newES constrói um Event Store de referência (single-replica para determinismo).
func newES(t *testing.T) *eventstore.Store {
	t.Helper()
	es, err := eventstore.New(eventstore.WithReplicas(1), eventstore.WithQuorum(1))
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	return es
}

// newInMemoryPort devolve um adaptador in-memory da MemoryPort (backend de teste).
func newInMemoryPort() ports.MemoryPort { return adapters.NewInMemoryAdapter() }

// newWindow constrói um WindowManager com prefixo estável (system + tool set
// congelado) e um sink de eviction (nil = eviction recusada). Limite de tokens
// grande (a eviction dos testes é dirigida por EvictToTailBudget, não por exaustão).
func newWindow(t *testing.T, runID string, sink working.EvictionSink) *working.WindowManager {
	t.Helper()
	wm, err := working.NewWindowManager(working.Config{
		RunID:           runID,
		System:          "SYSTEM PROMPT — parte imutável do prefixo do run",
		Tools:           []working.ToolSpec{{Name: "t1", Version: "1.0.0", Digest: "d1"}, {Name: "t2", Version: "2.0.0", Digest: "d2"}},
		ModelTokenLimit: 100000,
		Sink:            sink,
	})
	if err != nil {
		t.Fatalf("NewWindowManager: %v", err)
	}
	return wm
}

// projectForTest projecta a trajectória com a política de orçamento minúsculo (força
// descarte de contexto) — o atalho partilhado pelos testes de integridade.
func projectForTest(t *testing.T, rec *record.TrajectoryRecord) (projection.InjectedView, error) {
	t.Helper()
	return projection.ProjectContext(record.View(rec), tinyBudgetPolicy())
}

// itoa é uma conversão determinística sem dependências (evita fmt na hot path de teste).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// rawSecretPrefix marca o conteúdo CRU de um turno — o que NUNCA deve vazar para a
// projecção/sumário. Os testes de integridade procuram-no para provar a higiene.
const rawSecretPrefix = "RAW-SECRET-"

// buildTrajectory constrói uma trajectória (árvore de spans) com nTurns turnos —
// cada turno com RawContent (conteúdo cru, que NUNCA deve vazar na projecção) e
// Summary (o resumo higienizado, que a projecção devolve) — e um par de spans. É a
// FIXTURE GOLDEN partilhada por integridade/compressão/episódica.
func buildTrajectory(traceID string, nTurns int) *record.TrajectoryRecord {
	rec := record.NewTrajectoryRecord(traceID)
	for i := 1; i <= nTurns; i++ {
		_ = rec.AppendTurn(record.Turn{
			Index:                 i,
			PromptHash:            "sha256:deadbeef",
			ModelID:               "test-model",
			AssemblyVersion:       "1.0.0",
			ManifestSchemaVersion: "1.0.0",
			RawContent:            rawSecretPrefix + traceID + "-turn-" + itoa(i),
			Summary:               "sum-" + traceID + "-t" + itoa(i),
		})
	}
	rec.AppendSpan(record.Span{ID: "root", Name: "invoke_agent"})
	rec.AppendSpan(record.Span{ID: "child", ParentID: "root", Name: "chat"})
	return rec
}

// turnsOf devolve os turnos da trajectória como slice de record.Turn (para a origem
// de compactação). Reconstrói-os deterministicamente a partir da mesma golden.
func turnsOf(traceID string, nTurns int) []record.Turn {
	out := make([]record.Turn, 0, nTurns)
	for i := 1; i <= nTurns; i++ {
		out = append(out, record.Turn{
			Index:                 i,
			PromptHash:            "sha256:deadbeef",
			ModelID:               "test-model",
			AssemblyVersion:       "1.0.0",
			ManifestSchemaVersion: "1.0.0",
			RawContent:            rawSecretPrefix + traceID + "-turn-" + itoa(i),
			Summary:               "sum-" + traceID + "-t" + itoa(i),
		})
	}
	return out
}

// tinyBudgetPolicy é uma política de projecção com orçamento minúsculo, forçando o
// DESCARTE de contexto (IncludedTurns < TotalTurns) — legítimo na projecção, nunca no
// registo. É o cenário que expõe a fronteira contexto ≠ registo.
func tinyBudgetPolicy() projection.Policy {
	return projection.DefaultPolicy().WithTokenBudget(5)
}

// workingRec constrói um domain.Record de classe working com metadados completos e
// uma dada versão de schema — a unidade sobre a qual as migrações operam.
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

// semanticRec constrói um domain.Record semântico com metadados completos. A
// proveniência é imposta pela ingestão (AOS-042) — aqui é só um valor válido inicial.
func semanticRec(id string) domain.Record {
	return domain.Record{
		ID:    id,
		Class: domain.ClassSemantic,
		Metadata: domain.Metadata{
			AgentID:       "agent-1",
			RunID:         "run-1",
			Provenance:    domain.ProvenanceUntrusted,
			CreatedAt:     fixedTime,
			TTLClass:      domain.TTLStandard,
			SchemaVersion: "1.0.0",
		},
		Body: domain.SemanticBody{Subject: "s", Predicate: "p", Object: "o", Confidence: 0.9},
	}
}

// ver faz o parse SemVer (fixtures de migração).
func ver(s string) schema.Version { v, _ := schema.ParseVersion(s); return v }

// migSuffix é a marca reversível que Up acrescenta ao Content (e Down remove). Torna
// o par Up/Down um INVERSO exacto — a base da prova de "sem perda de dados".
const migSuffix = "|migrated"

// makeReversibleMigration constrói uma migração reversível de working entre duas
// versões. Up acrescenta o sufixo e estampa To; Down remove-o e estampa From.
// Determinística — Down(Up(x)) == x byte-a-byte.
func makeReversibleMigration(id, from, to string) migrations.Migration {
	return migrations.Migration{
		ID:    id,
		Class: domain.ClassWorking,
		From:  ver(from),
		To:    ver(to),
		Up: func(r domain.Record) (domain.Record, error) {
			b := r.Body.(domain.WorkingBody)
			b.Content += migSuffix
			out := r.Clone()
			out.Body = b
			out.Metadata.SchemaVersion = to
			return out, nil
		},
		Down: func(r domain.Record) (domain.Record, error) {
			b := r.Body.(domain.WorkingBody)
			b.Content = strings.TrimSuffix(b.Content, migSuffix)
			out := r.Clone()
			out.Body = b
			out.Metadata.SchemaVersion = from
			return out, nil
		},
	}
}

// makeLossyMigration constrói uma migração COM PERDA: Up descarta o Content (não
// reversível) e Down não o consegue restaurar. É a fixture de violação da MIGRAÇÃO —
// o motor tem de a RECUSAR (ErrIrreversibleMigration) pelo backstop de reversibilidade.
func makeLossyMigration(id, from, to string) migrations.Migration {
	m := makeReversibleMigration(id, from, to)
	m.Up = func(r domain.Record) (domain.Record, error) {
		b := r.Body.(domain.WorkingBody)
		b.Content = "" // PERDA: descarta o conteúdo
		out := r.Clone()
		out.Body = b
		out.Metadata.SchemaVersion = to
		return out, nil
	}
	return m
}

// boomError é o erro de um transform que falha de propósito (rollback de migração).
type boomError struct{}

func (boomError) Error() string { return "boom: transform falhou de propósito" }

// makeFailingMigration constrói uma migração cujo Up FALHA para o registo cujo
// Content é o gatilho dado — para provar o rollback de migração falhada.
func makeFailingMigration(id, from, to, trigger string) migrations.Migration {
	m := makeReversibleMigration(id, from, to)
	base := m.Up
	m.Up = func(r domain.Record) (domain.Record, error) {
		if r.Body.(domain.WorkingBody).Content == trigger {
			return domain.Record{}, boomError{}
		}
		return base(r)
	}
	return m
}

// contentOf devolve o Content do corpo working de um registo (asserções de migração).
func contentOf(r domain.Record) string { return r.Body.(domain.WorkingBody).Content }

// ---------------------------------------------------------------------------
// Test doubles de VIOLAÇÃO (usados pelos meta-testes para injectar uma falha e
// provar que a suite a DETECTA). São test-only e deterministas.
// ---------------------------------------------------------------------------

// droppingSink é um EvictionSink que ACEITA (err=nil) sem preservar nada: injecta a
// violação "eviction apaga o registo". A eviction procede, mas o registo desaparece —
// o verificador de preservação tem de o apanhar (meta-teste de integridade).
type droppingSink struct{}

func (droppingSink) Persist(_ context.Context, _ working.EvictedSegment) error { return nil }

// alwaysTrustedTC é um TaintController rígido que classifica TUDO como trusted e lava
// o taint na derivação: injecta a violação "quarentena furada / taint lavado". O
// verificador de proveniência tem de o apanhar (meta-testes de proveniência).
type alwaysTrustedTC struct{}

func (alwaysTrustedTC) Classify(provenance.Source) provenance.Provenance { return provenance.Trusted }
func (alwaysTrustedTC) Derive(...provenance.Provenance) provenance.Provenance {
	return provenance.Trusted
}

// tamperingStore embrulha uma audit.Store e ADULTERA um registo na leitura (muta o
// Capability sem recalcular o EntryHash): injecta a violação "hash-chain partida". O
// verificador de cadeia (audit.Verify) tem de o apanhar (meta-teste de shredding).
type tamperingStore struct{ audit.Store }

func (t tamperingStore) Read(ctx context.Context, p string, from, to uint64) ([]audit.AuditRecord, error) {
	recs, err := t.Store.Read(ctx, p, from, to)
	if err != nil {
		return nil, err
	}
	// Clona antes de mutar para não corromper o store real (o EntryHash mantém-se o
	// armazenado — a recomputação de audit.Verify há-de divergir e detectar a mutação).
	out := append([]audit.AuditRecord(nil), recs...)
	if len(out) > 0 {
		out[0].Capability += "-tampered"
	}
	return out, nil
}
