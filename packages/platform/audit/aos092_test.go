package audit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// Este ficheiro cobre AOS-092 — TTL-por-classe como policy-as-code versionada
// (RetentionConfig), o JOB varredor de expiração idempotente (ExpirationJob), o
// legal hold a suspender a expiração, a configurabilidade do TTL, e a expiração/
// alteração-de-config como eventos auditáveis + spans OTel.

// ---------------------------------------------------------------------------
// Fakes de teste — fonte e sink in-memory idempotentes.
// ---------------------------------------------------------------------------

// fakeSource é uma [RecordSource] in-memory.
type fakeSource struct {
	recs []ExpirableRecord
}

func (s *fakeSource) List(context.Context) ([]ExpirableRecord, error) {
	out := make([]ExpirableRecord, len(s.recs))
	copy(out, s.recs)
	return out, nil
}

// fakeSink é um [ExpirationSink] idempotente que conta as expirações EFECTIVAS
// (primeira vez por ID) e o total de chamadas (para detectar duplicação).
type fakeSink struct {
	mu       sync.Mutex
	expired  map[string]bool
	calls    int
	failIDs  map[string]bool // IDs a fazer falhar (teste de erro)
	failWith error
}

func newFakeSink() *fakeSink {
	return &fakeSink{expired: make(map[string]bool), failIDs: make(map[string]bool)}
}

func (s *fakeSink) Expire(_ context.Context, rec ExpirableRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.failIDs[rec.ID] {
		return s.failWith
	}
	s.expired[rec.ID] = true // idempotente: re-marcar é no-op
	return nil
}

func (s *fakeSink) expiredCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.expired)
}

// classConfig devolve uma RetentionConfig válida com os períodos dados.
func classConfig(t *testing.T, version string, periods map[DataClass]time.Duration) RetentionConfig {
	t.Helper()
	cfg, err := NewRetentionConfig(version, periods)
	if err != nil {
		t.Fatalf("NewRetentionConfig: %v", err)
	}
	return cfg
}

// ---------------------------------------------------------------------------
// AC1/AC2 — expiração POR CLASSE: cada classe expira ao seu TTL; antes do TTL não.
// ---------------------------------------------------------------------------

func TestExpirationPerClass(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	cfg := classConfig(t, "1.0.0", map[DataClass]time.Duration{
		ClassDiagnostic: time.Hour,       // efémero
		ClassAudit:      100 * time.Hour, // retém-se muito mais
	})

	// diag-old: diagnóstico com 2h → expira (TTL 1h).
	// diag-new: diagnóstico com 30m → NÃO expira.
	// audit-old: audit com 2h → NÃO expira (TTL 100h), mesma idade que diag-old.
	src := &fakeSource{recs: []ExpirableRecord{
		{ID: "diag-old", Class: ClassDiagnostic, CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "diag-new", Class: ClassDiagnostic, CreatedAt: now.Add(-30 * time.Minute)},
		{ID: "audit-old", Class: ClassAudit, CreatedAt: now.Add(-2 * time.Hour)},
	}}
	sink := newFakeSink()
	store := NewMemStore()

	job := NewExpirationJob(cfg, NewLegalHold(), src, sink,
		WithExpirationClock(func() time.Time { return now }),
		WithExpirationAudit(store, DefaultRetentionPartition),
	)

	rep, err := job.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Expired != 1 || rep.NotExpired != 2 {
		t.Fatalf("esperava 1 expirado / 2 não-expirados, veio Expired=%d NotExpired=%d", rep.Expired, rep.NotExpired)
	}
	if len(rep.ExpiredIDs) != 1 || rep.ExpiredIDs[0] != "diag-old" {
		t.Fatalf("esperava expirar diag-old, veio %v", rep.ExpiredIDs)
	}
	if !sink.expired["diag-old"] || sink.expired["diag-new"] || sink.expired["audit-old"] {
		t.Fatalf("sink expirou o registo errado: %v", sink.expired)
	}

	// AC5 — a expiração foi selada como evento auditável na hash-chain.
	head, _ := store.Head(ctx, DefaultRetentionPartition)
	if head != 1 {
		t.Fatalf("esperava 1 evento retention.expired selado, head=%d", head)
	}
	got, _, _ := store.At(ctx, DefaultRetentionPartition, 1)
	if got.Resource.Type != RetentionExpiredEventType || got.Resource.Value != "diag-old" {
		t.Fatalf("evento selado errado: type=%q value=%q", got.Resource.Type, got.Resource.Value)
	}
	if len(got.Obligations) != 1 || got.Obligations[0].Params["class"] != string(ClassDiagnostic) {
		t.Fatalf("obligation do evento não regista a classe: %+v", got.Obligations)
	}
	if got.Obligations[0].Params["ttl"] != time.Hour.String() {
		t.Fatalf("obligation não regista o TTL: %q", got.Obligations[0].Params["ttl"])
	}
	// A cadeia do audit continua a verificar.
	if err := Verify(ctx, store, DefaultRetentionPartition, 1, head); err != nil {
		t.Fatalf("cadeia de retenção devia verificar: %v", err)
	}
}

// TestClassWithoutPeriodNeverExpires — uma classe SEM período na política nunca
// expira (fail-closed), mesmo com idade enorme.
func TestClassWithoutPeriodNeverExpires(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	// Só diagnóstico tem período; trajectória não → nunca expira.
	cfg := classConfig(t, "1.0.0", map[DataClass]time.Duration{ClassDiagnostic: time.Hour})
	src := &fakeSource{recs: []ExpirableRecord{
		{ID: "traj", Class: ClassTrajectory, CreatedAt: now.Add(-10000 * time.Hour)},
	}}
	sink := newFakeSink()
	job := NewExpirationJob(cfg, nil, src, sink, WithExpirationClock(func() time.Time { return now }))
	rep, err := job.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Expired != 0 || rep.NotExpired != 1 {
		t.Fatalf("classe sem período não devia expirar: %+v", rep)
	}
}

// ---------------------------------------------------------------------------
// AC3 — LEGAL HOLD suspende a expiração mesmo após o TTL (por-titular e por-partição).
// ---------------------------------------------------------------------------

func TestLegalHoldSuspendsExpiration(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	cfg := classConfig(t, "1.0.0", map[DataClass]time.Duration{ClassPIIOperational: time.Hour})

	// Todos ultrapassam o TTL; held-subject e held-part estão sob hold.
	src := &fakeSource{recs: []ExpirableRecord{
		{ID: "free", Class: ClassPIIOperational, CreatedAt: now.Add(-2 * time.Hour), SubjectID: "s-free", Partition: "p-free"},
		{ID: "held-subject", Class: ClassPIIOperational, CreatedAt: now.Add(-2 * time.Hour), SubjectID: "s-held", Partition: "p-free"},
		{ID: "held-part", Class: ClassPIIOperational, CreatedAt: now.Add(-2 * time.Hour), SubjectID: "s-free2", Partition: "p-held"},
	}}
	holds := NewLegalHold()
	holds.HoldSubject("s-held")
	holds.HoldPartition("p-held")

	sink := newFakeSink()
	job := NewExpirationJob(cfg, holds, src, sink, WithExpirationClock(func() time.Time { return now }))

	rep, err := job.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Expired != 1 || rep.Held != 2 {
		t.Fatalf("esperava 1 expirado / 2 held, veio Expired=%d Held=%d", rep.Expired, rep.Held)
	}
	if !sink.expired["free"] || sink.expired["held-subject"] || sink.expired["held-part"] {
		t.Fatalf("legal hold não suspendeu a expiração corretamente: %v", sink.expired)
	}

	// Levantar os holds → os registos passam a expirar (a preservação era o único obstáculo).
	holds.ReleaseSubject("s-held")
	holds.ReleasePartition("p-held")
	rep2, err := job.Run(ctx)
	if err != nil {
		t.Fatalf("Run pós-release: %v", err)
	}
	if rep2.Expired != 2 || rep2.Held != 0 {
		t.Fatalf("após levantar holds esperava 2 expirados, veio %+v", rep2)
	}
}

// TestLegalHoldByPartitionViaSubjectIndex — um titular cujo índice mapeia a uma
// partição retida é suspenso mesmo que a Partition do próprio registo não o seja.
func TestLegalHoldByPartitionViaSubjectIndex(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	cfg := classConfig(t, "1.0.0", map[DataClass]time.Duration{ClassPIIOperational: time.Hour})

	idx := NewInMemorySubjectPartitionIndex()
	idx.Link("s1", "other-partition") // s1 também tem dados numa partição que estará retida
	holds := NewLegalHold()
	holds.HoldPartition("other-partition")

	src := &fakeSource{recs: []ExpirableRecord{
		{ID: "r1", Class: ClassPIIOperational, CreatedAt: now.Add(-2 * time.Hour), SubjectID: "s1", Partition: "own-partition"},
	}}
	sink := newFakeSink()
	job := NewExpirationJob(cfg, holds, src, sink,
		WithExpirationClock(func() time.Time { return now }),
		WithExpirationSubjectIndex(idx),
	)
	rep, err := job.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Held != 1 || rep.Expired != 0 {
		t.Fatalf("hold por-partição via índice devia suspender: %+v", rep)
	}
}

// ---------------------------------------------------------------------------
// AC4 — CONFIGURABILIDADE: alterar o TTL por política altera a expiração.
// ---------------------------------------------------------------------------

func TestTTLConfigurabilityChangesExpiration(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	rec := ExpirableRecord{ID: "r1", Class: ClassTrajectory, CreatedAt: now.Add(-50 * time.Hour)}

	// Política 1: TTL longo (100h) → NÃO expira.
	long := classConfig(t, "1.0.0", map[DataClass]time.Duration{ClassTrajectory: 100 * time.Hour})
	sink1 := newFakeSink()
	job1 := NewExpirationJob(long, nil, &fakeSource{recs: []ExpirableRecord{rec}}, sink1,
		WithExpirationClock(func() time.Time { return now }))
	rep1, err := job1.Run(ctx)
	if err != nil {
		t.Fatalf("Run long: %v", err)
	}
	if rep1.Expired != 0 {
		t.Fatalf("com TTL longo o registo não devia expirar: %+v", rep1)
	}

	// Política 2: TTL curto (1h) → passa a expirar (o mesmo registo, a mesma idade).
	short := classConfig(t, "2.0.0", map[DataClass]time.Duration{ClassTrajectory: time.Hour})
	sink2 := newFakeSink()
	job2 := NewExpirationJob(short, nil, &fakeSource{recs: []ExpirableRecord{rec}}, sink2,
		WithExpirationClock(func() time.Time { return now }))
	rep2, err := job2.Run(ctx)
	if err != nil {
		t.Fatalf("Run short: %v", err)
	}
	if rep2.Expired != 1 {
		t.Fatalf("com TTL curto o registo devia expirar: %+v", rep2)
	}
}

// TestRetentionConfigPolicyAsCode — a config carrega de JSON, valida fail-closed e
// uma alteração é selável como evento auditável (AC4).
func TestRetentionConfigPolicyAsCode(t *testing.T) {
	raw := []byte(`{"version":"1.2.0","periods":{"diagnostic":"24h","audit":"8760h"}}`)
	cfg, err := LoadRetentionConfig(raw)
	if err != nil {
		t.Fatalf("LoadRetentionConfig: %v", err)
	}
	if cfg.Version() != "1.2.0" {
		t.Fatalf("versão=%q", cfg.Version())
	}
	d, ok := cfg.Period(ClassDiagnostic)
	if !ok || d != 24*time.Hour {
		t.Fatalf("período diagnostic=%v ok=%v", d, ok)
	}
	if cfg.ContentHash() == "" {
		t.Fatalf("content hash vazio")
	}
	// Round-trip via Marshal → Load deve preservar o content hash.
	blob, err := cfg.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := LoadRetentionConfig(blob)
	if err != nil {
		t.Fatalf("re-Load: %v", err)
	}
	if back.ContentHash() != cfg.ContentHash() {
		t.Fatalf("content hash instável no round-trip: %s vs %s", back.ContentHash(), cfg.ContentHash())
	}

	// Fail-closed: versão inválida e período <= 0 são recusados.
	for _, bad := range []string{
		`{"version":"nope","periods":{"diagnostic":"1h"}}`,
		`{"version":"1.0.0","periods":{"diagnostic":"0s"}}`,
		`{"version":"1.0.0","periods":{"diagnostic":"-5h"}}`,
		`{"version":"1.0.0","periods":{"diagnostic":"banana"}}`,
	} {
		if _, err := LoadRetentionConfig([]byte(bad)); !errors.Is(err, ErrInvalidRetentionConfig) {
			t.Fatalf("config inválida %q devia falhar com ErrInvalidRetentionConfig, veio %v", bad, err)
		}
	}

	// AC4 — alteração de TTL selada como evento auditável no changelog.
	ctx := context.Background()
	store := NewMemStore()
	newCfg := classConfig(t, "1.3.0", map[DataClass]time.Duration{ClassDiagnostic: 12 * time.Hour, ClassAudit: 8760 * time.Hour})
	at := time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC)
	if err := SealRetentionConfigChange(store, cfg, newCfg, "compliance-officer", "reduzir TTL de diagnóstico", at, DefaultRetentionPartition); err != nil {
		t.Fatalf("SealRetentionConfigChange: %v", err)
	}
	got, _, _ := store.At(ctx, DefaultRetentionPartition, 1)
	if got.Resource.Type != RetentionConfigChangedEventType {
		t.Fatalf("evento de alteração errado: %q", got.Resource.Type)
	}
	if got.Obligations[0].Params["old_version"] != "1.2.0" || got.Obligations[0].Params["new_version"] != "1.3.0" {
		t.Fatalf("versões no changelog erradas: %+v", got.Obligations[0].Params)
	}
	if got.Principal.NHIID != "compliance-officer" {
		t.Fatalf("autor não selado: %q", got.Principal.NHIID)
	}
	// O diff determinista deve mencionar a classe alterada (24h→12h) e a adicionada.
	diff := got.Obligations[0].Fields
	if len(diff) == 0 {
		t.Fatalf("diff vazio no changelog de config")
	}
}

// ---------------------------------------------------------------------------
// Idempotência — reexecutar o job sobre os mesmos registos não duplica efeitos.
// ---------------------------------------------------------------------------

func TestExpirationJobIdempotentReexecution(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	cfg := classConfig(t, "1.0.0", map[DataClass]time.Duration{ClassDiagnostic: time.Hour})

	src := &fakeSource{recs: []ExpirableRecord{
		{ID: "a", Class: ClassDiagnostic, CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "b", Class: ClassDiagnostic, CreatedAt: now.Add(-2 * time.Hour)},
	}}
	sink := newFakeSink()
	store := NewMemStore()
	job := NewExpirationJob(cfg, nil, src, sink,
		WithExpirationClock(func() time.Time { return now }),
		WithExpirationAudit(store, DefaultRetentionPartition),
	)

	rep1, err := job.Run(ctx)
	if err != nil {
		t.Fatalf("Run #1: %v", err)
	}
	if rep1.Expired != 2 {
		t.Fatalf("primeira passagem devia expirar 2, veio %d", rep1.Expired)
	}

	// Segunda passagem sobre os MESMOS registos: nada expira de novo (idempotência).
	rep2, err := job.Run(ctx)
	if err != nil {
		t.Fatalf("Run #2: %v", err)
	}
	if rep2.Expired != 0 || rep2.Skipped != 2 {
		t.Fatalf("reexecução devia saltar 2 e expirar 0, veio Expired=%d Skipped=%d", rep2.Expired, rep2.Skipped)
	}

	// Efeitos NÃO duplicados: 2 eventos selados no total, 2 registos expirados no sink.
	head, _ := store.Head(ctx, DefaultRetentionPartition)
	if head != 2 {
		t.Fatalf("esperava 2 eventos selados no total (sem duplicação), head=%d", head)
	}
	if sink.expiredCount() != 2 {
		t.Fatalf("esperava 2 registos expirados no sink, veio %d", sink.expiredCount())
	}
}

// TestExpirationIdempotentAcrossSinkFailure — se a selagem no audit tem sucesso
// mas o sink falha a seguir, a expiração fica AUDITADA (1 evento) e a key MARCADA;
// a reexecução NÃO sela um SEGUNDO evento na WORM append-only (AC2/AC5). Regressão
// do achado HIGH: antes, a key só era marcada após o sink, pelo que uma falha do
// sink deixava a key por-marcar e a passagem seguinte re-selava um evento idêntico.
func TestExpirationIdempotentAcrossSinkFailure(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	cfg := classConfig(t, "1.0.0", map[DataClass]time.Duration{ClassDiagnostic: time.Hour})
	src := &fakeSource{recs: []ExpirableRecord{
		{ID: "a", Class: ClassDiagnostic, CreatedAt: now.Add(-2 * time.Hour)},
	}}
	sink := newFakeSink()
	sink.failIDs["a"] = true
	sink.failWith = errors.New("crypto-shred indisponível")
	store := NewMemStore()
	job := NewExpirationJob(cfg, nil, src, sink,
		WithExpirationClock(func() time.Time { return now }),
		WithExpirationAudit(store, DefaultRetentionPartition),
	)

	// Passagem #1: sela (head=1) mas o sink falha → erro agregado; a expiração já
	// está auditada e é contada no momento da selagem.
	rep1, err := job.Run(ctx)
	if err == nil {
		t.Fatalf("esperava erro agregado do sink a falhar")
	}
	if rep1.Expired != 1 {
		t.Fatalf("a expiração selada deve contar como expirada mesmo com o sink a falhar: %+v", rep1)
	}
	if head, _ := store.Head(ctx, DefaultRetentionPartition); head != 1 {
		t.Fatalf("esperava 1 evento selado após a passagem #1, head=%d", head)
	}

	// Passagem #2 sobre o MESMO registo: a key já está marcada → NÃO re-sela.
	rep2, err := job.Run(ctx)
	if err != nil {
		t.Fatalf("Run #2: %v", err)
	}
	if rep2.Expired != 0 || rep2.Skipped != 1 {
		t.Fatalf("reexecução devia saltar o registo já selado: %+v", rep2)
	}
	head, _ := store.Head(ctx, DefaultRetentionPartition)
	if head != 1 {
		t.Fatalf("a WORM NÃO devia ganhar um segundo evento retention.expired, head=%d", head)
	}
	if err := Verify(ctx, store, DefaultRetentionPartition, 1, head); err != nil {
		t.Fatalf("cadeia devia continuar a verificar: %v", err)
	}
}

// TestExpirationStableAcrossPolicyVersionBump — um registo já expirado e ainda
// visível numa fonte de soft-mark NÃO é re-selado quando a VERSÃO da política muda
// (AC4). Regressão do achado MEDIUM: a idempotência da expiração é por ID+classe,
// estável a bumps de versão de TTL (a versão entrava na key e re-selava).
func TestExpirationStableAcrossPolicyVersionBump(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	// Fonte de soft-mark: o registo continua visível em List() após ser expirado.
	src := &fakeSource{recs: []ExpirableRecord{
		{ID: "r1", Class: ClassDiagnostic, CreatedAt: now.Add(-2 * time.Hour)},
	}}
	sink := newFakeSink()
	store := NewMemStore()
	idem := NewInMemoryIdempotencyStore()
	clock := func() time.Time { return now }

	v1 := classConfig(t, "1.0.0", map[DataClass]time.Duration{ClassDiagnostic: time.Hour})
	job1 := NewExpirationJob(v1, nil, src, sink,
		WithExpirationClock(clock),
		WithExpirationAudit(store, DefaultRetentionPartition),
		WithExpirationIdempotency(idem),
	)
	if _, err := job1.Run(ctx); err != nil {
		t.Fatalf("Run v1: %v", err)
	}
	if head, _ := store.Head(ctx, DefaultRetentionPartition); head != 1 {
		t.Fatalf("esperava 1 evento após v1, head=%d", head)
	}

	// Alteração de TTL (policy-as-code): nova versão, MESMA fonte de soft-mark e
	// MESMO seen-set durável.
	v2 := classConfig(t, "1.1.0", map[DataClass]time.Duration{ClassDiagnostic: 30 * time.Minute})
	job2 := NewExpirationJob(v2, nil, src, sink,
		WithExpirationClock(clock),
		WithExpirationAudit(store, DefaultRetentionPartition),
		WithExpirationIdempotency(idem),
	)
	rep2, err := job2.Run(ctx)
	if err != nil {
		t.Fatalf("Run v2: %v", err)
	}
	if rep2.Expired != 0 || rep2.Skipped != 1 {
		t.Fatalf("bump de versão não devia re-expirar um registo já expirado: %+v", rep2)
	}
	if head, _ := store.Head(ctx, DefaultRetentionPartition); head != 1 {
		t.Fatalf("bump de versão NÃO devia selar um segundo evento na WORM, head=%d", head)
	}
}

// TestExpirationJobFailClosedOnSealError — se a selagem no audit falhar, o registo
// NÃO é expirado no sink (nada é destruído sem evento auditável) e é retentável.
func TestExpirationJobFailClosedOnSealError(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	cfg := classConfig(t, "1.0.0", map[DataClass]time.Duration{ClassDiagnostic: time.Hour})
	src := &fakeSource{recs: []ExpirableRecord{
		{ID: "a", Class: ClassDiagnostic, CreatedAt: now.Add(-2 * time.Hour)},
	}}
	sink := newFakeSink()
	failing := &failingStore{}
	job := NewExpirationJob(cfg, nil, src, sink,
		WithExpirationClock(func() time.Time { return now }),
		WithExpirationAudit(failing, DefaultRetentionPartition),
	)
	rep, err := job.Run(ctx)
	if err == nil {
		t.Fatalf("esperava erro agregado da selagem falhada")
	}
	if rep.Expired != 0 {
		t.Fatalf("nada devia ser contado como expirado com selagem a falhar: %+v", rep)
	}
	if sink.calls != 0 {
		t.Fatalf("o sink NÃO devia ser chamado quando a selagem falha (fail-closed), calls=%d", sink.calls)
	}

	// Uma passagem posterior com store são deve conseguir expirar (era retentável).
	store := NewMemStore()
	job2 := NewExpirationJob(cfg, nil, src, sink,
		WithExpirationClock(func() time.Time { return now }),
		WithExpirationAudit(store, DefaultRetentionPartition),
		WithExpirationIdempotency(job.idem), // partilha o mesmo seen-set: a key nunca foi marcada
	)
	rep2, err := job2.Run(ctx)
	if err != nil {
		t.Fatalf("Run retentado: %v", err)
	}
	if rep2.Expired != 1 {
		t.Fatalf("registo devia ser retentado e expirar, veio %+v", rep2)
	}
}

// failingStore é um [Store] cujo Append falha sempre (para o teste fail-closed).
type failingStore struct{}

func (failingStore) Append(context.Context, AuditRecord) (AuditRecord, error) {
	return AuditRecord{}, errors.New("store indisponível")
}
func (failingStore) Read(context.Context, string, uint64, uint64) ([]AuditRecord, error) {
	return nil, nil
}
func (failingStore) Head(context.Context, string) (uint64, error) { return 0, nil }
func (failingStore) At(context.Context, string, uint64) (AuditRecord, bool, error) {
	return AuditRecord{}, false, nil
}

// ---------------------------------------------------------------------------
// DoD — spans OTel de expiração emitidos.
// ---------------------------------------------------------------------------

func TestExpirationEmitsSpans(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	cfg := classConfig(t, "1.0.0", map[DataClass]time.Duration{ClassDiagnostic: time.Hour})
	src := &fakeSource{recs: []ExpirableRecord{
		{ID: "a", Class: ClassDiagnostic, CreatedAt: now.Add(-2 * time.Hour)},
	}}
	tr := otelgenai.NewRecordingTracer(nil)
	job := NewExpirationJob(cfg, nil, src, newFakeSink(),
		WithExpirationClock(func() time.Time { return now }),
		WithExpirationTracer(tr),
	)
	if _, err := job.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sweep := tr.SpansByOperation(opRetentionSweep)
	expire := tr.SpansByOperation(opRetentionExpire)
	if len(sweep) != 1 || !sweep[0].Ended {
		t.Fatalf("esperava 1 span de sweep fechado, veio %d", len(sweep))
	}
	if len(expire) != 1 || !expire[0].Ended {
		t.Fatalf("esperava 1 span de expire fechado, veio %d", len(expire))
	}
	if expire[0].Attributes[attrRetentionClass] != string(ClassDiagnostic) {
		t.Fatalf("span de expire sem a classe: %+v", expire[0].Attributes)
	}
	if expire[0].Attributes[attrRetentionResult] != retentionResultExpired {
		t.Fatalf("span de expire com result errado: %v", expire[0].Attributes[attrRetentionResult])
	}
	if sweep[0].Attributes[attrRetentionExpiredCount] != 1 {
		t.Fatalf("span de sweep sem a contagem de expirados: %+v", sweep[0].Attributes)
	}
}

// TestNoSourceFailsClosed — job sem fonte/sink devolve erro (não expira nada).
func TestNoSourceFailsClosed(t *testing.T) {
	cfg := classConfig(t, "1.0.0", map[DataClass]time.Duration{ClassDiagnostic: time.Hour})
	job := NewExpirationJob(cfg, nil, nil, nil)
	if _, err := job.Run(context.Background()); !errors.Is(err, ErrNoExpirationSource) {
		t.Fatalf("esperava ErrNoExpirationSource, veio %v", err)
	}
}
