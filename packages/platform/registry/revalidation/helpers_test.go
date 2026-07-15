package revalidation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/digest"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/platform/registry/signing"
	"github.com/aos-ref/platform/registry/toolset"
)

// --- relógio / versões determinísticos -------------------------------------

func ver(mj, mn, p int) domain.Version { return domain.Version{Major: mj, Minor: mn, Patch: p} }

func fixedClock() func() time.Time {
	base := time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC)
	return func() time.Time { return base }
}

// keyFromSeed produz um par Ed25519 DETERMINÍSTICO a partir de um byte de seed.
func keyFromSeed(b byte) ed25519.PrivateKey {
	seed := bytes.Repeat([]byte{b}, ed25519.SeedSize)
	return ed25519.NewKeyFromSeed(seed)
}

// --- trust store de teste --------------------------------------------------

// newSigner constrói um assinante determinista com um keyID estável.
func newSigner(t *testing.T, keyID string, seed byte) *signing.Signer {
	t.Helper()
	s, err := signing.NewSigner(keyID, keyFromSeed(seed))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

// newTrust constrói um TrustStore real com as chaves públicas dos signers dados.
func newTrust(t *testing.T, signers ...*signing.Signer) *signing.TrustStore {
	t.Helper()
	ts, err := signing.NewTrustStore(audit.NewMemStore())
	if err != nil {
		t.Fatalf("NewTrustStore: %v", err)
	}
	for _, s := range signers {
		if err := ts.Add(context.Background(), s.KeyID(), s.PublicKey()); err != nil {
			t.Fatalf("TrustStore.Add(%s): %v", s.KeyID(), err)
		}
	}
	return ts
}

// --- entradas assinadas com digest SHA-256 real ----------------------------

// contractWith constrói um contrato de teste com um marcador no InputSchema (para
// forçar digests distintos entre variantes), scopes e classe de egress.
func contractWith(marker string, egress domain.EgressClass, scopes ...string) domain.Contract {
	in, _ := json.Marshal(map[string]string{"marker": marker})
	return domain.Contract{
		InputSchema:      json.RawMessage(in),
		CredentialScopes: scopes,
		Egress:           egress,
	}
}

// signedEntry constrói uma domain.Entry COERENTE: digest = SHA-256 real do
// (kind, contract) via AOS-047, e assinatura do signer sobre (id, version, digest)
// via AOS-048. É o artefacto "íntegro" contra o qual o congelado casa.
func signedEntry(id string, v domain.Version, kind domain.ArtifactKind, c domain.Contract, signer *signing.Signer) domain.Entry {
	dig := digest.SHA256Digester{}.Digest(kind, c)
	sig := signer.Sign(id, v, dig)
	return domain.Entry{
		ID: id, Version: v, Kind: kind, Digest: dig, Signature: sig,
		Contract:   c,
		Provenance: domain.Provenance{Origin: "mcp://" + id, Publisher: signer.KeyID(), Trust: domain.TrustPinned},
		Status:     domain.StatusActive,
	}
}

// --- catálogo fake + congelamento ------------------------------------------

type fakeCatalog struct{ entries []domain.Entry }

func (f fakeCatalog) ActiveEntries(context.Context) ([]domain.Entry, error) {
	out := make([]domain.Entry, len(f.entries))
	copy(out, f.entries)
	return out, nil
}

// freeze congela um conjunto de entradas num FrozenToolSet de run (a EXPECTATIVA).
func freeze(t *testing.T, runID string, entries ...domain.Entry) *toolset.FrozenToolSet {
	t.Helper()
	f, err := toolset.FreezeToolSet(context.Background(), fakeCatalog{entries: entries}, runID, nil, toolset.WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("FreezeToolSet(%s): %v", runID, err)
	}
	return f
}

// --- recorders (quarentena / alerta) ---------------------------------------

// recordingQuarantine regista os artefactos isolados. Opcionalmente falha (para
// provar que uma quarentena falhada NÃO desbloqueia).
type recordingQuarantine struct {
	mu   sync.Mutex
	arts []Artifact
	err  error
}

func (q *recordingQuarantine) Quarantine(_ context.Context, art Artifact) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.arts = append(q.arts, art)
	return q.err
}

func (q *recordingQuarantine) count() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.arts)
}

func (q *recordingQuarantine) last() (Artifact, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.arts) == 0 {
		return Artifact{}, false
	}
	return q.arts[len(q.arts)-1], true
}

// recordingAlerter regista os alertas emitidos.
type recordingAlerter struct {
	mu     sync.Mutex
	alerts []Alert
}

func (a *recordingAlerter) Alert(_ context.Context, al Alert) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.alerts = append(a.alerts, al)
}

func (a *recordingAlerter) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.alerts)
}

func (a *recordingAlerter) last() (Alert, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.alerts) == 0 {
		return Alert{}, false
	}
	return a.alerts[len(a.alerts)-1], true
}

// --- audit falhado ---------------------------------------------------------

// failingAudit é um audit.Store cujo Append falha SEMPRE — modela a auditoria
// indisponível (prova o fail-closed: uma autorização não-auditável é recusada).
type failingAudit struct{}

func (failingAudit) Append(context.Context, audit.AuditRecord) (audit.AuditRecord, error) {
	return audit.AuditRecord{}, errAuditDown
}
func (failingAudit) Read(context.Context, string, uint64, uint64) ([]audit.AuditRecord, error) {
	return nil, nil
}
func (failingAudit) Head(context.Context, string) (uint64, error) { return 0, nil }
func (failingAudit) At(context.Context, string, uint64) (audit.AuditRecord, bool, error) {
	return audit.AuditRecord{}, false, nil
}

var errAuditDown = &auditDownErr{}

type auditDownErr struct{}

func (*auditDownErr) Error() string { return "audit indisponivel" }

// --- fábrica de revalidador de teste ---------------------------------------

// harness reúne o revalidador e os recorders para asserção.
type harness struct {
	rv    *Revalidator
	audit *audit.MemStore
	quar  *recordingQuarantine
	alert *recordingAlerter
}

// newHarness constrói um revalidador com trust real, audit em memória e recorders.
func newHarness(t *testing.T, trust TrustStore, opts ...Option) *harness {
	t.Helper()
	au := audit.NewMemStore()
	q := &recordingQuarantine{}
	al := &recordingAlerter{}
	base := []Option{
		WithClock(fixedClock()),
		WithQuarantiner(q),
		WithAlerter(al),
	}
	base = append(base, opts...)
	rv, err := New(trust, au, base...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &harness{rv: rv, audit: au, quar: q, alert: al}
}

// auditRecords lê todos os registos da partição de revalidação.
func (h *harness) auditRecords(t *testing.T, partition string) []audit.AuditRecord {
	t.Helper()
	head, err := h.audit.Head(context.Background(), partition)
	if err != nil {
		t.Fatalf("audit.Head: %v", err)
	}
	if head == 0 {
		return nil
	}
	recs, err := h.audit.Read(context.Background(), partition, 1, head)
	if err != nil {
		t.Fatalf("audit.Read: %v", err)
	}
	return recs
}
