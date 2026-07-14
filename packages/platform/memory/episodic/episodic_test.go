package episodic

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/record"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Helpers deterministas
// ---------------------------------------------------------------------------

// seqRand é uma RandSource DETERMINÍSTICA para teste: preenche por um contador
// monotónico (sem crypto/rand real). Os nonces/DEKs diferem entre selagens, mas a
// sequência é reproduzível — a selagem torna-se byte-a-byte determinística.
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

// fixedClock devolve um relógio fixo (determinismo do Timestamp de audit).
func fixedClock() func() time.Time {
	t := time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
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

// newStore constrói um TrajectoryStore sobre um ES real, KeyStore in-memory e a
// hash-chain de audit, com cripto/relógio deterministas.
func newStore(t *testing.T, es EventLog) (*TrajectoryStore, *InMemoryKeyStore, *audit.MemStore) {
	t.Helper()
	keys := NewInMemoryKeyStore((&seqRand{}).fill)
	chain := audit.NewMemStore()
	s, err := NewTrajectoryStore(es, keys, chain,
		WithClock(fixedClock()),
		WithRandSource((&seqRand{n: 1000}).fill),
	)
	if err != nil {
		t.Fatalf("NewTrajectoryStore: %v", err)
	}
	return s, keys, chain
}

// buildTrajectory constrói uma trajectória (árvore de spans) com nTurns turnos —
// cada turno com RawContent (conteúdo cru, que NUNCA deve vazar na projecção) e
// Summary (o resumo higienizado, que a projecção devolve) — e um par de spans.
func buildTrajectory(traceID string, nTurns int) *record.TrajectoryRecord {
	rec := record.NewTrajectoryRecord(traceID)
	for i := 1; i <= nTurns; i++ {
		_ = rec.AppendTurn(record.Turn{
			Index:                 i,
			PromptHash:            "sha256:deadbeef",
			ModelID:               "test-model",
			AssemblyVersion:       "1.0.0",
			ManifestSchemaVersion: "1.0.0",
			RawContent:            "RAW-SECRET-" + traceID + "-turn-" + itoa(i),
			Summary:               "sum-" + traceID + "-t" + itoa(i),
		})
	}
	rec.AppendSpan(record.Span{ID: "root", Name: "invoke_agent"})
	rec.AppendSpan(record.Span{ID: "child", ParentID: "root", Name: "chat"})
	return rec
}

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

func baseInput(episodeID, subject, run, goal string, tags []string, ttl domain.TTLClass, nTurns int) EpisodeInput {
	return EpisodeInput{
		EpisodeID: episodeID,
		SubjectID: subject,
		AgentID:   "agent-1",
		RunID:     run,
		Goal:      goal,
		Tags:      tags,
		Outcome:   "success",
		TTLClass:  ttl,
		Record:    buildTrajectory("trace-"+run, nTurns),
	}
}

// countingLog embrulha um EventLog e conta Append/Read (prova da hot path).
type countingLog struct {
	inner   EventLog
	appends atomic.Int64
	reads   atomic.Int64
}

func (c *countingLog) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	c.appends.Add(1)
	return c.inner.Append(ctx, streamID, in, opts...)
}

func (c *countingLog) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	c.reads.Add(1)
	return c.inner.Read(ctx, streamID, fromSeq)
}

// ---------------------------------------------------------------------------
// Escrita fora da hot path
// ---------------------------------------------------------------------------

func TestEnqueueDoesNotTouchHotPath(t *testing.T) {
	es := newES(t)
	cl := &countingLog{inner: es}
	s, _, _ := newStore(t, cl)
	ctx := context.Background()

	in := baseInput("ep-1", "subj-1", "run-1", "resolver bug", []string{"go", "bug"}, domain.TTLStandard, 3)
	if err := s.Enqueue(in); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Enqueue NÃO pode ter tocado no Event Store (nem Append nem Read): é O(1) e
	// fora do turno crítico.
	if got := cl.appends.Load(); got != 0 {
		t.Fatalf("Enqueue tocou o ES: appends=%d, esperado 0 (hot path)", got)
	}
	if got := cl.reads.Load(); got != 0 {
		t.Fatalf("Enqueue tocou o ES: reads=%d, esperado 0 (hot path)", got)
	}
	if s.PendingCount() != 1 {
		t.Fatalf("PendingCount=%d, esperado 1", s.PendingCount())
	}

	// O trabalho pesado só acontece em Flush (fora da hot path).
	n, err := s.Flush(ctx)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if n != 1 {
		t.Fatalf("Flush persistiu %d, esperado 1", n)
	}
	if cl.appends.Load() == 0 {
		t.Fatalf("Flush não escreveu no ES (appends=0)")
	}
	if s.PendingCount() != 0 {
		t.Fatalf("fila não drenada: PendingCount=%d", s.PendingCount())
	}
}

func TestEnqueueValidatesFailClosed(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	cases := []struct {
		name string
		mut  func(*EpisodeInput)
		want error
	}{
		{"nil record", func(in *EpisodeInput) { in.Record = nil }, ErrNilRecord},
		{"no episode id", func(in *EpisodeInput) { in.EpisodeID = "" }, ErrMissingEpisodeID},
		{"no subject", func(in *EpisodeInput) { in.SubjectID = "" }, ErrMissingSubjectID},
		{"no run", func(in *EpisodeInput) { in.RunID = "" }, ErrMissingRunID},
		{"no goal", func(in *EpisodeInput) { in.Goal = "" }, ErrMissingGoal},
		{"bad ttl", func(in *EpisodeInput) { in.TTLClass = "nope" }, ErrInvalidTTLClass},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput("ep", "subj", "run", "goal", nil, domain.TTLStandard, 1)
			tc.mut(&in)
			if err := s.Enqueue(in); !errors.Is(err, tc.want) {
				t.Fatalf("Enqueue err=%v, esperado %v", err, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Persistência append-only + ligação por trace_id
// ---------------------------------------------------------------------------

func TestAppendOnlyAndTraceLink(t *testing.T) {
	es := newES(t)
	s, _, chain := newStore(t, es)
	ctx := context.Background()

	in1 := baseInput("ep-a", "subj-a", "run-1", "objetivo X", []string{"t1"}, domain.TTLPermanent, 4)
	in2 := baseInput("ep-b", "subj-b", "run-2", "objetivo Y", []string{"t2"}, domain.TTLPermanent, 2)
	mustEnqueue(t, s, in1, in2)
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Append-only: dois eventos no stream episódico, por ordem de escrita.
	events, err := es.Read(ctx, EpisodicStreamID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("eventos=%d, esperado 2", len(events))
	}
	for i, ev := range events {
		if ev.Type != EventTypeEpisodeRecorded {
			t.Fatalf("evento %d tipo=%q", i, ev.Type)
		}
		if ev.Seq != uint64(i+1) {
			t.Fatalf("evento %d seq=%d, esperado %d (append-only ordenado)", i, ev.Seq, i+1)
		}
	}

	// Ligação por trace_id: o episódio recuperado liga-se à árvore de spans do run.
	recalled, err := s.Recall(ctx, Query{Goal: "objetivo X"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(recalled) != 1 {
		t.Fatalf("recall=%d, esperado 1", len(recalled))
	}
	if recalled[0].TraceID != "trace-run-1" {
		t.Fatalf("trace_id=%q, esperado trace-run-1", recalled[0].TraceID)
	}
	// O backend recebe a árvore COMPLETA (mais spans do que os turnos projectados).
	if recalled[0].EmittedSpans <= recalled[0].Projection.IncludedTurns {
		t.Fatalf("EmittedSpans=%d <= IncludedTurns=%d: backend devia receber a árvore completa",
			recalled[0].EmittedSpans, recalled[0].Projection.IncludedTurns)
	}

	headBefore, _ := chain.Head(ctx, s.Partition())

	// Idempotência append-only: re-enfileirar o MESMO episódio (mesma f(run,id)) não
	// duplica o evento nem re-sela a cadeia.
	mustEnqueue(t, s, in1)
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush 2: %v", err)
	}
	events2, _ := es.Read(ctx, EpisodicStreamID, 1)
	if len(events2) != 2 {
		t.Fatalf("após re-enqueue: eventos=%d, esperado 2 (idempotente)", len(events2))
	}
	headAfter, _ := chain.Head(ctx, s.Partition())
	if headBefore != headAfter {
		t.Fatalf("cadeia cresceu num duplicado: head %d -> %d", headBefore, headAfter)
	}
}

// ---------------------------------------------------------------------------
// Recuperação devolve PROJECÇÃO resumida, nunca a trajectória crua
// ---------------------------------------------------------------------------

func TestRecallReturnsProjectionNotRaw(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	ctx := context.Background()

	in := baseInput("ep-1", "subj-1", "run-1", "resumir", []string{"a", "b"}, domain.TTLStandard, 3)
	mustEnqueue(t, s, in)
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	recalled, err := s.Recall(ctx, Query{Goal: "resumir", Tags: []string{"a"}})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(recalled) != 1 || !recalled[0].Recoverable {
		t.Fatalf("recall=%+v", recalled)
	}
	proj := recalled[0].Projection

	// A projecção devolve o RESUMO higienizado...
	if !strings.Contains(proj.Summary, "sum-trace-run-1") {
		t.Fatalf("projecção não contém o resumo: %q", proj.Summary)
	}
	// ...e NUNCA o conteúdo cru da trajectória (Princípio 4).
	if strings.Contains(proj.Summary, "RAW-SECRET") {
		t.Fatalf("projecção VAZOU o conteúdo cru: %q", proj.Summary)
	}
	if strings.Contains(string(proj.Bytes()), "RAW-SECRET") {
		t.Fatalf("bytes da projecção vazaram o conteúdo cru")
	}
	// A projecção é resumida (limitada) e ligada ao trace.
	if proj.TraceID != "trace-run-1" {
		t.Fatalf("projecção não ligada ao trace: %q", proj.TraceID)
	}
	if proj.TotalTurns != 3 {
		t.Fatalf("TotalTurns=%d, esperado 3", proj.TotalTurns)
	}
}

func TestRecallRankingDeterministic(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	ctx := context.Background()

	// Três episódios com nº de tags casadas diferente => ordem determinística.
	mustEnqueue(t,
		s,
		baseInput("ep-1", "s1", "run-1", "g", []string{"x"}, domain.TTLStandard, 1),           // score 1
		baseInput("ep-2", "s2", "run-2", "g", []string{"x", "y", "z"}, domain.TTLStandard, 1), // score 3
		baseInput("ep-3", "s3", "run-3", "g", []string{"x", "y"}, domain.TTLStandard, 1),      // score 2
	)
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	want := []string{"ep-2", "ep-3", "ep-1"} // score desc
	for i := 0; i < 3; i++ {                 // reproduzível entre execuções
		got, err := s.Recall(ctx, Query{Tags: []string{"x", "y", "z"}})
		if err != nil {
			t.Fatalf("Recall: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("recall=%d, esperado 3", len(got))
		}
		for j, ep := range got {
			if ep.EpisodeID != want[j] {
				t.Fatalf("iter %d pos %d: got %q, esperado %q", i, j, ep.EpisodeID, want[j])
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Replay resume-from-step: episódio + Event Store
// ---------------------------------------------------------------------------

func TestResumeFromEpisodePlusEventStore(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	ctx := context.Background()

	const runID = "run-replay"

	// O Event Store tem os CHECKPOINTS do run (AOS-015): o turno 1 ficou verificado.
	cp, err := durable.NewCheckpointer(es)
	if err != nil {
		t.Fatalf("NewCheckpointer: %v", err)
	}
	seq := durable.NewStepSequencer()
	if err := cp.Checkpoint(ctx, agentruntime.Checkpoint{
		RunID:  runID,
		StepID: seq.StepID(runID, 1),
		Turn:   1,
		Phase:  agentruntime.PhaseVerified,
	}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	// A memória episódica indexa o episódio desse run (recuperável por objectivo).
	mustEnqueue(t, s, baseInput("ep-replay", "subj-r", runID, "tarefa longa", []string{"replay"}, domain.TTLPermanent, 1))
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	recalled, err := s.Recall(ctx, Query{Goal: "tarefa longa"})
	if err != nil || len(recalled) != 1 {
		t.Fatalf("Recall: %v (%d)", err, len(recalled))
	}

	// Episódio recuperado + Event Store => cursor de retoma resume-from-step.
	rp, err := s.ResumeFrom(ctx, recalled[0])
	if err != nil {
		t.Fatalf("ResumeFrom: %v", err)
	}
	if rp.FromScratch {
		t.Fatalf("ResumePoint FromScratch: o episódio+ES deviam dar o cursor do checkpoint")
	}
	if rp.RunID != runID {
		t.Fatalf("RunID=%q, esperado %q", rp.RunID, runID)
	}
	if rp.NextTurn != 2 {
		t.Fatalf("NextTurn=%d, esperado 2 (turno 1 verificado => retoma no 2)", rp.NextTurn)
	}
	if rp.NextStepID != seq.StepID(runID, 2) {
		t.Fatalf("NextStepID=%q, esperado %q", rp.NextStepID, seq.StepID(runID, 2))
	}
}

// ---------------------------------------------------------------------------
// Crypto-shredding: irrecuperável após apagar a chave, cadeia INTACTA
// ---------------------------------------------------------------------------

func TestCryptoShreddingChainIntact(t *testing.T) {
	es := newES(t)
	s, keys, _ := newStore(t, es)
	ctx := context.Background()

	mustEnqueue(t,
		s,
		baseInput("ep-1", "subj-1", "run-1", "g", []string{"a"}, domain.TTLPermanent, 2),
		baseInput("ep-2", "subj-2", "run-2", "g", []string{"a"}, domain.TTLPermanent, 2),
		baseInput("ep-3", "subj-3", "run-3", "g", []string{"a"}, domain.TTLPermanent, 2),
	)
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// A cadeia verifica ANTES do shredding.
	if err := s.VerifyChain(ctx); err != nil {
		t.Fatalf("VerifyChain (antes): %v", err)
	}
	// O episódio 2 é recuperável (projecção decifrada).
	if iv, err := s.Project(ctx, "ep-2"); err != nil {
		t.Fatalf("Project ep-2 (antes): %v", err)
	} else if iv.TraceID != "trace-run-2" {
		t.Fatalf("projecção ep-2 inesperada: %q", iv.TraceID)
	}

	// CRYPTO-SHREDDING: apagar a chave do titular do episódio 2.
	keys.DeleteKey("subj-2")

	// O episódio 2 é agora IRRECUPERÁVEL.
	if _, err := s.Project(ctx, "ep-2"); !errors.Is(err, ErrEpisodeShredded) {
		t.Fatalf("Project ep-2 (depois)=%v, esperado ErrEpisodeShredded", err)
	}
	// ErrEpisodeShredded distingue-se de ErrEpisodeNotFound (o registo ainda existe).
	if _, err := s.Project(ctx, "ep-2"); errors.Is(err, ErrEpisodeNotFound) {
		t.Fatalf("ep-2 não devia ser NotFound (o registo selado permanece)")
	}
	// Via Recall, o episódio aparece no índice mas Recoverable=false.
	recalled, err := s.Recall(ctx, Query{Goal: "g"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	var found bool
	for _, ep := range recalled {
		switch ep.EpisodeID {
		case "ep-2":
			found = true
			if ep.Recoverable {
				t.Fatalf("ep-2 devia ser irrecuperável após shredding")
			}
		default:
			if !ep.Recoverable {
				t.Fatalf("%s devia continuar recuperável (chave intacta)", ep.EpisodeID)
			}
		}
	}
	if !found {
		t.Fatalf("ep-2 sumiu do índice (só a chave devia ter sido apagada)")
	}

	// A HASH-CHAIN continua INTACTA e a verificar APÓS o shredding.
	if err := s.VerifyChain(ctx); err != nil {
		t.Fatalf("VerifyChain (depois do shredding): %v — a cadeia devia continuar íntegra", err)
	}
}

// ---------------------------------------------------------------------------
// TTL por classe (via crypto-shredding, cadeia intacta)
// ---------------------------------------------------------------------------

func TestTTLPerClass(t *testing.T) {
	es := newES(t)
	s, _, _ := newStore(t, es)
	ctx := context.Background()

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// epShort: TTL curto (24h). epPerm: permanente (sem expiração). Mesmo t0.
	inShort := baseInput("ep-short", "subj-short", "run-s", "g", []string{"a"}, domain.TTLShort, 1)
	inShort.CreatedAt = t0
	inPerm := baseInput("ep-perm", "subj-perm", "run-p", "g", []string{"a"}, domain.TTLPermanent, 1)
	inPerm.CreatedAt = t0
	mustEnqueue(t, s, inShort, inPerm)
	if _, err := s.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Sweep 48h depois: o curto expira, o permanente sobrevive.
	swept, err := s.Sweep(ctx, t0.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(swept) != 1 || swept[0].EpisodeID != "ep-short" {
		t.Fatalf("swept=%+v, esperado só ep-short", swept)
	}

	// O expirado é irrecuperável; o permanente continua recuperável.
	if _, err := s.Project(ctx, "ep-short"); !errors.Is(err, ErrEpisodeShredded) {
		t.Fatalf("ep-short (expirado)=%v, esperado ErrEpisodeShredded", err)
	}
	if _, err := s.Project(ctx, "ep-perm"); err != nil {
		t.Fatalf("ep-perm (permanente) devia sobreviver: %v", err)
	}
	// A cadeia continua intacta (TTL não parte a hash-chain).
	if err := s.VerifyChain(ctx); err != nil {
		t.Fatalf("VerifyChain após TTL sweep: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Construção fail-closed
// ---------------------------------------------------------------------------

func TestNewTrajectoryStoreFailClosed(t *testing.T) {
	es := newES(t)
	keys := NewInMemoryKeyStore(nil)
	chain := audit.NewMemStore()
	cases := []struct {
		name  string
		es    EventLog
		keys  KeyStore
		chain audit.Store
		want  error
	}{
		{"nil es", nil, keys, chain, ErrNilStore},
		{"nil keys", es, nil, chain, ErrNilKeyStore},
		{"nil chain", es, keys, nil, ErrNilAuditStore},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewTrajectoryStore(tc.es, tc.keys, tc.chain); !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, esperado %v", err, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Cripto de envelope: round-trip e shredding a nível de unidade
// ---------------------------------------------------------------------------

func TestEnvelopeSealOpenRoundTrip(t *testing.T) {
	r := (&seqRand{}).fill
	kek := make([]byte, kekSize)
	_ = r(kek)
	plaintext := []byte("projecção resumida sensível")

	blob, err := seal(kek, plaintext, r)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := open(kek, blob)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("round-trip falhou: %q != %q", got, plaintext)
	}
	// Chave errada => ErrDecrypt (o crypto-shredding na leitura).
	wrong := make([]byte, kekSize)
	_ = r(wrong)
	if _, err := open(wrong, blob); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("open(chave errada)=%v, esperado ErrDecrypt", err)
	}
}

func mustEnqueue(t *testing.T, s *TrajectoryStore, ins ...EpisodeInput) {
	t.Helper()
	for _, in := range ins {
		if err := s.Enqueue(in); err != nil {
			t.Fatalf("Enqueue(%s): %v", in.EpisodeID, err)
		}
	}
}
