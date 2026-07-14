package tofu

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/domain"
)

// ---------------------------------------------------------------------------
// Fixtures deterministas
// ---------------------------------------------------------------------------

// fixedClock devolve um relógio determinista (nunca time.Now numa decisão; o
// timestamp de audit é apenas observacional).
func fixedClock() func() time.Time {
	t := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// mustVersion parseia uma versão SemVer ou falha o teste.
func mustVersion(t *testing.T, s string) domain.Version {
	t.Helper()
	v, err := domain.ParseVersion(s)
	if err != nil {
		t.Fatalf("ParseVersion(%q): %v", s, err)
	}
	return v
}

// newTestMonitor constrói um Monitor sobre um MemStore de audit real (AOS-011) com
// relógio determinista. Devolve o monitor e o store para inspecção da cadeia.
func newTestMonitor(t *testing.T, opts ...Option) (*Monitor, *audit.MemStore) {
	t.Helper()
	store := audit.NewMemStore()
	all := append([]Option{WithClock(fixedClock())}, opts...)
	m, err := NewMonitor(store, all...)
	if err != nil {
		t.Fatalf("NewMonitor: %v", err)
	}
	return m, store
}

// obs é um atalho para construir uma Observation.
func obs(t *testing.T, identity, version, dgst string) Observation {
	t.Helper()
	return Observation{Identity: identity, Version: mustVersion(t, version), Digest: dgst}
}

// auditRecords devolve todos os registos selados na partição TOFU, por ordem.
func auditRecords(t *testing.T, store *audit.MemStore, partition string) []audit.AuditRecord {
	t.Helper()
	head, err := store.Head(context.Background(), partition)
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head == 0 {
		return nil
	}
	recs, err := store.Read(context.Background(), partition, 1, head)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return recs
}

// ---------------------------------------------------------------------------
// Fakes de falha (fail-closed)
// ---------------------------------------------------------------------------

// failingAudit falha SEMPRE no Append — para provar que uma transição não-auditável
// é recusada (fail-closed) e não muta o estado.
type failingAudit struct{}

func (failingAudit) Append(context.Context, audit.AuditRecord) (audit.AuditRecord, error) {
	return audit.AuditRecord{}, errors.New("audit indisponivel")
}
func (failingAudit) Read(context.Context, string, uint64, uint64) ([]audit.AuditRecord, error) {
	return nil, nil
}
func (failingAudit) Head(context.Context, string) (uint64, error) { return 0, nil }
func (failingAudit) At(context.Context, string, uint64) (audit.AuditRecord, bool, error) {
	return audit.AuditRecord{}, false, nil
}
