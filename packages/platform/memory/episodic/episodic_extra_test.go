package episodic

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/substrate/eventstore"
)

// TestOptionsAndTracingObservability cobre as Options e prova que a via episódica
// emite spans (observabilidade): o span da árvore de spans (record.Persist) e o
// span episódico com trace_id/episode_id/audit_seq.
func TestOptionsAndTracingObservability(t *testing.T) {
	es := newES(t)
	keys := NewInMemoryKeyStore((&seqRand{}).fill)
	chain := audit.NewMemStore()
	tr := &agentruntime.RecordingTracer{}

	s, err := NewTrajectoryStore(es, keys, chain,
		WithTracer(tr),
		WithClock(fixedClock()),
		WithRandSource((&seqRand{n: 7}).fill),
		WithChainPartition("custom.partition"),
		WithTTLPolicy(TTLPolicy{domain.TTLStandard: time.Hour}),
	)
	if err != nil {
		t.Fatalf("NewTrajectoryStore: %v", err)
	}
	if s.Partition() != "custom.partition" {
		t.Fatalf("Partition=%q, esperado custom.partition", s.Partition())
	}

	ctx := context.Background()
	mustEnqueue(t, s, baseInput("ep-1", "subj-1", "run-1", "g", []string{"a"}, domain.TTLStandard, 2))
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Span episódico emitido com os atributos canónicos.
	var found bool
	for _, sp := range tr.SpansByOperation(spanRecord) {
		found = true
		if sp.Attributes[attrEpisodeID] != "ep-1" {
			t.Fatalf("span sem episode_id: %+v", sp.Attributes)
		}
		if sp.Attributes[attrTraceID] != "trace-run-1" {
			t.Fatalf("span sem trace_id correcto: %+v", sp.Attributes)
		}
		if _, ok := sp.Attributes[attrAuditSeq]; !ok {
			t.Fatalf("span sem audit_seq")
		}
	}
	if !found {
		t.Fatalf("nenhum span %q emitido", spanRecord)
	}
	// A chave de partição custom recebeu a selagem.
	if head, _ := chain.Head(ctx, "custom.partition"); head != 1 {
		t.Fatalf("head da partição custom=%d, esperado 1", head)
	}
}

// failKeys é um KeyStore cujo EnsureKey falha — força um erro em persist ANTES de
// qualquer escrita (nem ES nem cadeia são tocados), exercendo o re-enqueue de Flush.
type failKeys struct {
	inner KeyStore
	err   error
}

func (f failKeys) EnsureKey(subjectID string) ([]byte, error) { return nil, f.err }
func (f failKeys) Key(subjectID string) ([]byte, bool)        { return f.inner.Key(subjectID) }
func (f failKeys) DeleteKey(subjectID string)                 { f.inner.DeleteKey(subjectID) }

func TestFlushErrorRequeues(t *testing.T) {
	es := newES(t)
	chain := audit.NewMemStore()
	sentinel := errors.New("kms indisponível")
	keys := failKeys{inner: NewInMemoryKeyStore(nil), err: sentinel}

	s, err := NewTrajectoryStore(es, keys, chain, WithClock(fixedClock()), WithRandSource((&seqRand{}).fill))
	if err != nil {
		t.Fatalf("NewTrajectoryStore: %v", err)
	}
	ctx := context.Background()
	mustEnqueue(t, s, baseInput("ep-1", "subj-1", "run-1", "g", nil, domain.TTLStandard, 1))

	n, ferr := s.Flush(ctx)
	if !errors.Is(ferr, sentinel) {
		t.Fatalf("Flush err=%v, esperado %v", ferr, sentinel)
	}
	if n != 0 {
		t.Fatalf("persisted=%d, esperado 0", n)
	}
	// O episódio é recolocado na fila (nada perdido) e nada foi selado na cadeia.
	if s.PendingCount() != 1 {
		t.Fatalf("PendingCount=%d, esperado 1 (re-enqueue)", s.PendingCount())
	}
	if head, _ := chain.Head(ctx, s.Partition()); head != 0 {
		t.Fatalf("cadeia foi tocada num erro pré-selagem: head=%d", head)
	}
}

// tamperingStore embrulha um audit.Store e, na Read, MUTA o EntryHash do primeiro
// registo — simulando adulteração para provar que VerifyChain a detecta.
type tamperingStore struct {
	inner audit.Store
	on    bool
}

func (ts *tamperingStore) Append(ctx context.Context, rec audit.AuditRecord) (audit.AuditRecord, error) {
	return ts.inner.Append(ctx, rec)
}
func (ts *tamperingStore) Head(ctx context.Context, p string) (uint64, error) {
	return ts.inner.Head(ctx, p)
}
func (ts *tamperingStore) At(ctx context.Context, p string, seq uint64) (audit.AuditRecord, bool, error) {
	return ts.inner.At(ctx, p, seq)
}
func (ts *tamperingStore) Read(ctx context.Context, p string, from, to uint64) ([]audit.AuditRecord, error) {
	recs, err := ts.inner.Read(ctx, p, from, to)
	if err != nil || !ts.on || len(recs) == 0 {
		return recs, err
	}
	if len(recs[0].EntryHash) > 0 {
		recs[0].EntryHash[0] ^= 0xFF // muta o hash selado
	}
	return recs, nil
}

func TestVerifyChainDetectsTamperAndEmpty(t *testing.T) {
	es := newES(t)
	ts := &tamperingStore{inner: audit.NewMemStore()}
	keys := NewInMemoryKeyStore((&seqRand{}).fill)
	s, err := NewTrajectoryStore(es, keys, ts, WithClock(fixedClock()), WithRandSource((&seqRand{}).fill))
	if err != nil {
		t.Fatalf("NewTrajectoryStore: %v", err)
	}
	ctx := context.Background()

	// Cadeia vazia => VerifyChain nil (head==0).
	if err := s.VerifyChain(ctx); err != nil {
		t.Fatalf("VerifyChain (vazia)=%v, esperado nil", err)
	}

	mustEnqueue(t, s, baseInput("ep-1", "subj-1", "run-1", "g", nil, domain.TTLPermanent, 1))
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := s.VerifyChain(ctx); err != nil {
		t.Fatalf("VerifyChain (íntegra)=%v", err)
	}
	// Liga a adulteração: VerifyChain tem de detectar.
	ts.on = true
	if err := s.VerifyChain(ctx); !errors.Is(err, audit.ErrTampered) {
		t.Fatalf("VerifyChain (adulterada)=%v, esperado audit.ErrTampered", err)
	}
}

func TestRecallEmptyAndProjectNotFound(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	ctx := context.Background()

	// Store vazio (stream ainda inexistente): Recall devolve vazio sem erro.
	got, err := s.Recall(ctx, Query{Goal: "nada"})
	if err != nil {
		t.Fatalf("Recall (vazio): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("recall=%d, esperado 0", len(got))
	}
	// Project de id inexistente => ErrEpisodeNotFound.
	if _, err := s.Project(ctx, "fantasma"); !errors.Is(err, ErrEpisodeNotFound) {
		t.Fatalf("Project(inexistente)=%v, esperado ErrEpisodeNotFound", err)
	}
}

func TestResumeFromMissingRun(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	if _, err := s.ResumeFrom(context.Background(), RecalledEpisode{}); !errors.Is(err, ErrMissingRunID) {
		t.Fatalf("ResumeFrom(sem run)=%v, esperado ErrMissingRunID", err)
	}
}

// TestDefaultCryptoRandRoundTrip exercita o caminho de produção (crypto/rand real,
// sem RandSource injectada): sela e recupera um episódio de ponta a ponta.
func TestDefaultCryptoRandRoundTrip(t *testing.T) {
	es := newES(t)
	keys := NewInMemoryKeyStore(nil) // crypto/rand real
	chain := audit.NewMemStore()
	s, err := NewTrajectoryStore(es, keys, chain, WithClock(fixedClock())) // rand default (crypto/rand)
	if err != nil {
		t.Fatalf("NewTrajectoryStore: %v", err)
	}
	ctx := context.Background()
	mustEnqueue(t, s, baseInput("ep-1", "subj-1", "run-1", "g", []string{"a"}, domain.TTLStandard, 2))
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	iv, err := s.Project(ctx, "ep-1")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if iv.TraceID != "trace-run-1" {
		t.Fatalf("projecção inesperada: %q", iv.TraceID)
	}
	if err := s.VerifyChain(ctx); err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Q1 (AOS-038): granularidade do crypto-shredding por TITULAR — o Sweep nunca
// pode destruir um episódio NÃO-expirado (incluindo permanente) que partilha o
// subject com um expirado. A KEK é por titular; só é apagada quando 100% dos
// episódios do titular expiraram. (O TestTTLPerClass base usa subjects DISTINTOS
// e não cobre este caso mesmo-subject/classes-mistas.)
// ---------------------------------------------------------------------------

func TestSweepSameSubjectMixedClassesRetainsKEK(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	ctx := context.Background()

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// MESMO titular subj-X: um episódio curto (24h) e um permanente (sem expiração).
	inShort := baseInput("ep-short", "subj-X", "run-s", "g", []string{"a"}, domain.TTLShort, 1)
	inShort.CreatedAt = t0
	inPerm := baseInput("ep-perm", "subj-X", "run-p", "g", []string{"a"}, domain.TTLPermanent, 1)
	inPerm.CreatedAt = t0
	mustEnqueue(t, s, inShort, inPerm)
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Sweep 48h depois: ep-short expirou, MAS ep-perm (mesmo titular) nunca expira.
	// A KEK do titular tem de ser RETIDA — nada é varrido.
	swept, err := s.Sweep(ctx, t0.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(swept) != 0 {
		t.Fatalf("swept=%+v, esperado 0 (um não-expirado retém a KEK do titular)", swept)
	}

	// AMBOS continuam recuperáveis — o permanente NÃO foi destruído silenciosamente,
	// e o curto também sobrevive porque a KEK partilhada não pôde ser apagada.
	if _, err := s.Project(ctx, "ep-perm"); err != nil {
		t.Fatalf("ep-perm (permanente, mesmo titular) foi destruído: %v", err)
	}
	if _, err := s.Project(ctx, "ep-short"); err != nil {
		t.Fatalf("ep-short devia sobreviver enquanto a KEK do titular for retida: %v", err)
	}
	if err := s.VerifyChain(ctx); err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
}

func TestSweepSameSubjectAllExpiredShredsAndReportsAll(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	ctx := context.Background()

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// MESMO titular subj-Y: dois episódios curtos (ambos expiram a 24h).
	inA := baseInput("ep-a", "subj-Y", "run-a", "g", []string{"a"}, domain.TTLShort, 1)
	inA.CreatedAt = t0
	inB := baseInput("ep-b", "subj-Y", "run-b", "g", []string{"a"}, domain.TTLShort, 1)
	inB.CreatedAt = t0
	mustEnqueue(t, s, inA, inB)
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// A 48h TODOS expiraram: a KEK é apagada e AMBOS os episódios do titular são
	// reportados em swept (não apenas o que disparou a expiração).
	swept, err := s.Sweep(ctx, t0.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	got := map[string]bool{}
	for _, sw := range swept {
		got[sw.EpisodeID] = true
	}
	if len(swept) != 2 || !got["ep-a"] || !got["ep-b"] {
		t.Fatalf("swept=%+v, esperado ambos ep-a e ep-b", swept)
	}
	for _, id := range []string{"ep-a", "ep-b"} {
		if _, err := s.Project(ctx, id); !errors.Is(err, ErrEpisodeShredded) {
			t.Fatalf("Project(%s)=%v, esperado ErrEpisodeShredded", id, err)
		}
	}
	if err := s.VerifyChain(ctx); err != nil {
		t.Fatalf("VerifyChain após shredding total: %v", err)
	}
}

// flakyES embrulha um EventLog e falha o PRÓXIMO Append (uma vez) — simula a morte
// da escrita no ES DEPOIS de a cadeia já ter selado, para exercer a janela
// selar↔escrever do invariante log↔cadeia 1:1 (Q2).
type flakyES struct {
	inner    EventLog
	failNext atomic.Bool
	err      error
}

func (f *flakyES) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if f.failNext.CompareAndSwap(true, false) {
		return eventstore.AppendResult{}, f.err
	}
	return f.inner.Append(ctx, streamID, in, opts...)
}

func (f *flakyES) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	return f.inner.Read(ctx, streamID, fromSeq)
}

// ---------------------------------------------------------------------------
// Q2 (AOS-038): crash-safety da idempotência. Se es.Append falhar DEPOIS de a
// cadeia selar, um retry NÃO pode re-selar o mesmo episódio (senão a cadeia
// cresceria com selos órfãos, quebrando log↔cadeia 1:1). O envelope selado fica
// stashed (progress) e a retoma salta a selagem. (O TestFlushErrorRequeues base
// falha ANTES da selagem e não cobre esta janela.)
// ---------------------------------------------------------------------------

func TestFlushRequeueDoesNotReSealAfterESFailure(t *testing.T) {
	inner := newES(t)
	flaky := &flakyES{inner: inner, err: errors.New("ES indisponível")}
	chain := audit.NewMemStore()
	keys := NewInMemoryKeyStore((&seqRand{}).fill)
	s, err := NewTrajectoryStore(flaky, keys, chain,
		WithClock(fixedClock()),
		WithRandSource((&seqRand{n: 1000}).fill),
	)
	if err != nil {
		t.Fatalf("NewTrajectoryStore: %v", err)
	}
	ctx := context.Background()

	mustEnqueue(t, s, baseInput("ep-1", "subj-1", "run-1", "g", []string{"a"}, domain.TTLPermanent, 2))

	// 1ª Flush: a cadeia sela (head 0->1) mas o es.Append falha => episódio recolocado
	// com o trabalho selado stashed. Nada escrito no ES.
	flaky.failNext.Store(true)
	if _, ferr := s.Flush(ctx); ferr == nil {
		t.Fatalf("Flush devia falhar (es.Append falhou)")
	}
	if head, _ := chain.Head(ctx, s.Partition()); head != 1 {
		t.Fatalf("head=%d após 1ª Flush, esperado 1 (selado uma vez)", head)
	}
	if s.PendingCount() != 1 {
		t.Fatalf("PendingCount=%d, esperado 1 (re-enqueue)", s.PendingCount())
	}
	if evs, _ := inner.Read(ctx, EpisodicStreamID, 1); len(evs) != 0 {
		t.Fatalf("ES tem %d eventos após falha, esperado 0", len(evs))
	}

	// 2ª Flush: es.Append já não falha. A RETOMA consome o stash e escreve — SEM
	// re-selar a cadeia. Head continua 1 (selo único), 1 evento no ES.
	if n, ferr := s.Flush(ctx); ferr != nil || n != 1 {
		t.Fatalf("Flush 2: n=%d err=%v, esperado 1/nil", n, ferr)
	}
	if head, _ := chain.Head(ctx, s.Partition()); head != 1 {
		t.Fatalf("head=%d após 2ª Flush, esperado 1 (SEM re-selagem — invariante log↔cadeia 1:1)", head)
	}
	evs, _ := inner.Read(ctx, EpisodicStreamID, 1)
	if len(evs) != 1 {
		t.Fatalf("ES tem %d eventos, esperado exactamente 1 (sem duplicação)", len(evs))
	}

	// O episódio ficou coerente: audit_seq==1 (o único selo), recuperável, cadeia íntegra.
	iv, perr := s.Project(ctx, "ep-1")
	if perr != nil {
		t.Fatalf("Project ep-1: %v", perr)
	}
	if iv.TraceID != "trace-run-1" {
		t.Fatalf("projecção inesperada: %q", iv.TraceID)
	}
	rec, _ := s.Recall(ctx, Query{Goal: "g"})
	if len(rec) != 1 || rec[0].AuditSeq != 1 {
		t.Fatalf("recall=%+v, esperado 1 episódio com audit_seq=1", rec)
	}
	if err := s.VerifyChain(ctx); err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Q3 (AOS-038): backpressure fail-closed da fila em memória — ao atingir o tecto,
// Enqueue devolve ErrQueueFull em vez de crescer memória sem limite. Após Flush a
// fila drena e volta a aceitar.
// ---------------------------------------------------------------------------

func TestEnqueueBackpressureQueueFull(t *testing.T) {
	es := newES(t)
	keys := NewInMemoryKeyStore((&seqRand{}).fill)
	chain := audit.NewMemStore()
	s, err := NewTrajectoryStore(es, keys, chain,
		WithClock(fixedClock()),
		WithRandSource((&seqRand{n: 1000}).fill),
		WithMaxQueue(2),
	)
	if err != nil {
		t.Fatalf("NewTrajectoryStore: %v", err)
	}
	ctx := context.Background()

	mustEnqueue(t, s,
		baseInput("ep-1", "subj-1", "run-1", "g", nil, domain.TTLStandard, 1),
		baseInput("ep-2", "subj-2", "run-2", "g", nil, domain.TTLStandard, 1),
	)
	// A fila está cheia (tecto=2): o 3º Enqueue é rejeitado (fail-closed).
	if err := s.Enqueue(baseInput("ep-3", "subj-3", "run-3", "g", nil, domain.TTLStandard, 1)); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Enqueue (fila cheia)=%v, esperado ErrQueueFull", err)
	}
	if s.PendingCount() != 2 {
		t.Fatalf("PendingCount=%d, esperado 2 (o rejeitado não entrou)", s.PendingCount())
	}

	// Drenar liberta a fila: volta a aceitar.
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := s.Enqueue(baseInput("ep-3", "subj-3", "run-3", "g", nil, domain.TTLStandard, 1)); err != nil {
		t.Fatalf("Enqueue após drenar: %v", err)
	}
}
