package compression

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/memory/projection"
	"github.com/aos-ref/platform/memory/record"
	"github.com/aos-ref/platform/memory/working"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Helpers deterministas
// ---------------------------------------------------------------------------

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

// buildTurns constrói n turnos com conteúdo cru (que NUNCA deve vazar na projecção) e
// um resumo higienizado (o que a projecção devolve), cada um com manifesto completo.
func buildTurns(n int) []record.Turn {
	turns := make([]record.Turn, 0, n)
	for i := 1; i <= n; i++ {
		turns = append(turns, record.Turn{
			Index:                 i,
			PromptHash:            "sha256:beef" + strconv.Itoa(i),
			ModelID:               "test-model",
			AssemblyVersion:       "1.0.0",
			ManifestSchemaVersion: "1.0.0",
			RawContent:            "RAW-SECRET-turn-" + strconv.Itoa(i) + "-com-muito-conteudo-cru-detalhado",
			Summary:               "sum-t" + strconv.Itoa(i),
		})
	}
	return turns
}

func baseSource(run, checkpoint, prefixHash string, nTurns int) CompactionSource {
	return CompactionSource{
		RunID:        run,
		CheckpointID: checkpoint,
		TraceID:      "trace-" + run,
		AgentID:      "agent-1",
		PrefixHash:   prefixHash,
		Turns:        buildTurns(nTurns),
	}
}

// countingLog embrulha um EventLog e conta Append/Read (prova da hot path e da
// escrita durável).
type countingLog struct {
	inner   EventLog
	appends int
	reads   int
	mu      sync.Mutex
}

func (c *countingLog) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	c.mu.Lock()
	c.appends++
	c.mu.Unlock()
	return c.inner.Append(ctx, streamID, in, opts...)
}

func (c *countingLog) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	return c.inner.Read(ctx, streamID, fromSeq)
}

func (c *countingLog) appendCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.appends
}

// recordingAlerts capta os alertas de cache thrash.
type recordingAlerts struct {
	mu     sync.Mutex
	alerts []CacheThrashAlert
}

func (r *recordingAlerts) CacheThrash(_ context.Context, a CacheThrashAlert) {
	r.mu.Lock()
	r.alerts = append(r.alerts, a)
	r.mu.Unlock()
}

func (r *recordingAlerts) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.alerts)
}

func newCompactor(t *testing.T, es EventLog) *AsyncCompactor {
	t.Helper()
	c, err := NewAsyncCompactor(es)
	if err != nil {
		t.Fatalf("NewAsyncCompactor: %v", err)
	}
	return c
}

// ---------------------------------------------------------------------------
// Teste 1 — a compressão NÃO corre na hot path (asserção de caminho)
// ---------------------------------------------------------------------------

func TestCompaction_NaoCorreNaHotPath(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	log := &countingLog{inner: es}
	compactor := newCompactor(t, log)

	// Uma janela de trabalho (AOS-037) com prefixo pequeno e limite baixo, para que o
	// tail cruze o limiar de exaustão (~80%) ao fim de alguns turnos.
	wm, err := working.NewWindowManager(working.Config{
		RunID:           "run-1",
		System:          "sys",
		ModelTokenLimit: 40,
	})
	if err != nil {
		t.Fatalf("NewWindowManager: %v", err)
	}

	trigger, err := NewCheckpointTrigger(compactor, DefaultCompressionPolicy(),
		WithPrefixHash(wm.PrefixHash))
	if err != nil {
		t.Fatalf("NewCheckpointTrigger: %v", err)
	}

	// SIMULA A HOT PATH: vários turnos. A cada turno, Append + Turn + Observe(sinal).
	// A compactação NUNCA deve correr aqui.
	triggeredAtLeastOnce := false
	for i := 1; i <= 8; i++ {
		wm.Append(working.TailInput{
			Kind:     working.TailToolResult,
			Content:  "resultado-de-tool-turno-" + strconv.Itoa(i),
			Priority: 1,
		})
		res := wm.Turn(ctx)
		src := baseSource("run-1", "ckpt-"+strconv.Itoa(i), wm.PrefixHash(), i)
		enq, oerr := trigger.Observe(ctx, res.Exhaustion, src)
		if oerr != nil {
			t.Fatalf("Observe turno %d: %v", i, oerr)
		}
		if enq {
			triggeredAtLeastOnce = true
		}
		// ASSERÇÃO DE CAMINHO: o compactor NÃO foi invocado durante o turno.
		if got := compactor.Invocations(); got != 0 {
			t.Fatalf("turno %d: compactor invocado na hot path (Invocations=%d, esperava 0)", i, got)
		}
		// E nada foi escrito no Event Store na hot path.
		if got := log.appendCount(); got != 0 {
			t.Fatalf("turno %d: escrita no Event Store na hot path (appends=%d, esperava 0)", i, got)
		}
	}

	if !triggeredAtLeastOnce {
		t.Fatal("o sinal de exaustão (~80%) nunca disparou — cenário inválido")
	}
	if trigger.PendingCount() == 0 {
		t.Fatal("nenhum checkpoint enfileirado apesar do sinal de exaustão")
	}

	// FORA DA HOT PATH: agora sim, no checkpoint, a compactação corre.
	results, err := trigger.RunCheckpoint(ctx)
	if err != nil {
		t.Fatalf("RunCheckpoint: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("RunCheckpoint não produziu resultados")
	}
	if compactor.Invocations() == 0 {
		t.Fatal("compactor não foi invocado no checkpoint (Invocations=0)")
	}
	if log.appendCount() == 0 {
		t.Fatal("nenhum sumário persistido no Event Store no checkpoint")
	}
	if trigger.PendingCount() != 0 {
		t.Fatalf("fila não drenada após RunCheckpoint (pending=%d)", trigger.PendingCount())
	}
}

// ---------------------------------------------------------------------------
// Teste 2 — accionada pelo sinal a ~80% (não hard-stop)
// ---------------------------------------------------------------------------

func TestObserve_AccionadaPeloSinalDeExaustao(t *testing.T) {
	ctx := context.Background()
	compactor := newCompactor(t, newES(t))
	trigger, err := NewCheckpointTrigger(compactor, DefaultCompressionPolicy())
	if err != nil {
		t.Fatalf("NewCheckpointTrigger: %v", err)
	}

	occ := working.Occupancy{PrefixTokens: 10, TailTokens: 5, Limit: 100, Threshold: 80}
	tests := []struct {
		name    string
		sig     working.Exhaustion
		wantEnq bool
	}{
		{
			name:    "abaixo do limiar: não acciona",
			sig:     working.Exhaustion{Triggered: false, Action: working.ActionNone, Occupancy: occ},
			wantEnq: false,
		},
		{
			name:    "no limiar ~80%: marca para compressão",
			sig:     working.Exhaustion{Triggered: true, Action: working.ActionMarkForCompression, Occupancy: occ},
			wantEnq: true,
		},
		{
			name:    "acima do limite: escala (ainda acciona, não hard-stop)",
			sig:     working.Exhaustion{Triggered: true, Action: working.ActionEscalate, Occupancy: occ},
			wantEnq: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := trigger.PendingCount()
			src := baseSource("run-x", "ckpt-x", "sha256:prefix", 2)
			enq, err := trigger.Observe(ctx, tc.sig, src)
			if err != nil {
				t.Fatalf("Observe: %v", err)
			}
			if enq != tc.wantEnq {
				t.Fatalf("enq=%v, esperava %v", enq, tc.wantEnq)
			}
			delta := trigger.PendingCount() - before
			wantDelta := 0
			if tc.wantEnq {
				wantDelta = 1
			}
			if delta != wantDelta {
				t.Fatalf("delta de fila=%d, esperava %d", delta, wantDelta)
			}
			// NUNCA hard-stop: Observe nunca comprime (compactor a 0 até ao checkpoint).
			if compactor.Invocations() != 0 {
				t.Fatalf("Observe comprimiu na hot path (Invocations=%d)", compactor.Invocations())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Teste 3 — invariância do prefixo após compressão (hash constante)
// ---------------------------------------------------------------------------

func TestCompact_InvarianciaDoPrefixo(t *testing.T) {
	ctx := context.Background()
	compactor := newCompactor(t, newES(t))

	const prefixHash = "sha256:prefixo-imutavel-abc123"
	src := baseSource("run-1", "ckpt-1", prefixHash, 5)

	// nowPrefixHash == src.PrefixHash: o prefixo NÃO mudou (invariante).
	res, err := compactor.Compact(ctx, src, DefaultCompressionPolicy(), prefixHash)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !res.PrefixInvariant {
		t.Fatal("PrefixInvariant=false, esperava true (a compressão não muta o prefixo)")
	}
	if res.CacheThrash {
		t.Fatal("CacheThrash=true sem mutação de prefixo")
	}
	if res.PrefixHashBefore != prefixHash || res.PrefixHashAfter != prefixHash {
		t.Fatalf("hash do prefixo mudou: before=%q after=%q (esperava %q constante)",
			res.PrefixHashBefore, res.PrefixHashAfter, prefixHash)
	}
}

// ---------------------------------------------------------------------------
// Teste 4 — registo completo permanece após compressão (Princípio 4)
// ---------------------------------------------------------------------------

func TestCompact_RegistoCompletoPermanece(t *testing.T) {
	ctx := context.Background()
	compactor := newCompactor(t, newES(t))

	const nTurns = 6
	src := baseSource("run-1", "ckpt-1", "sha256:p", nTurns)
	res, err := compactor.Compact(ctx, src, DefaultCompressionPolicy(), "sha256:p")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	// O registo completo foi emitido: raiz + 1 span/turno + a árvore de spans registada.
	// FullRecordSpans tem de ser ESTRITAMENTE maior do que os turnos incluídos no
	// sumário — o backend recebe tudo, o contexto recebe só o resumo (Princípio 4).
	if res.FullRecordSpans <= res.Summary.IncludedTurns {
		t.Fatalf("FullRecordSpans=%d não excede IncludedTurns=%d (registo não é superset do sumário)",
			res.FullRecordSpans, res.Summary.IncludedTurns)
	}
	if res.TotalTurns != nTurns {
		t.Fatalf("TotalTurns=%d, esperava %d (o registo mantém todos os turnos)", res.TotalTurns, nTurns)
	}

	// O sumário é a PROJECÇÃO higienizada — NUNCA o conteúdo cru. Nenhum RawContent
	// deve vazar para o sumário.
	if res.Summary.Summary == "" {
		t.Fatal("sumário vazio")
	}
	for i := 1; i <= nTurns; i++ {
		raw := "RAW-SECRET-turn-" + strconv.Itoa(i)
		if containsSubstr(res.Summary.Summary, raw) {
			t.Fatalf("o conteúdo cru %q vazou para o sumário (projecção corrompida)", raw)
		}
	}

	// O sumário durável é RECUPERÁVEL do backend (o registo permanece intacto).
	summaries, err := compactor.Summaries(ctx)
	if err != nil {
		t.Fatalf("Summaries: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("esperava 1 sumário persistido, obtive %d", len(summaries))
	}
	if summaries[0].TotalTurns != nTurns {
		t.Fatalf("sumário persistido: TotalTurns=%d, esperava %d", summaries[0].TotalTurns, nTurns)
	}
	if summaries[0].FullRecordSpans != res.FullRecordSpans {
		t.Fatalf("sumário persistido: FullRecordSpans=%d, esperava %d", summaries[0].FullRecordSpans, res.FullRecordSpans)
	}
}

// ---------------------------------------------------------------------------
// Teste 5 — idempotência e reprodutibilidade (reaplicar = mesmo sumário)
// ---------------------------------------------------------------------------

func TestCompact_IdempotenteEReproduzivel(t *testing.T) {
	ctx := context.Background()
	log := &countingLog{inner: newES(t)}
	compactor := newCompactor(t, log)

	src := baseSource("run-1", "ckpt-1", "sha256:p", 5)
	policy := DefaultCompressionPolicy()

	res1, err := compactor.Compact(ctx, src, policy, "sha256:p")
	if err != nil {
		t.Fatalf("Compact #1: %v", err)
	}
	if res1.Duplicate {
		t.Fatal("primeira compactação marcada como duplicado")
	}

	// REAPLICAR a MESMA compactação (mesma origem + política): mesmo sumário byte-a-byte,
	// mesmo Digest, e no-op durável (Duplicate) — SEM dupla-compressão divergente.
	res2, err := compactor.Compact(ctx, src, policy, "sha256:p")
	if err != nil {
		t.Fatalf("Compact #2: %v", err)
	}
	if res1.Digest != res2.Digest {
		t.Fatalf("digest divergente ao reaplicar: %q vs %q", res1.Digest, res2.Digest)
	}
	if string(res1.Summary.Bytes()) != string(res2.Summary.Bytes()) {
		t.Fatal("sumário divergente ao reaplicar (não-reproduzível)")
	}
	if !res2.Duplicate {
		t.Fatal("reaplicar não foi detectado como duplicado (dupla-compressão)")
	}

	// Reprodutibilidade num compactor NOVO/limpo (outra "sessão"): o Digest e o sumário
	// dependem só de (origem, política), não de estado acumulado.
	fresh := newCompactor(t, &countingLog{inner: newES(t)})
	res3, err := fresh.Compact(ctx, src, policy, "sha256:p")
	if err != nil {
		t.Fatalf("Compact fresh: %v", err)
	}
	if res3.Digest != res1.Digest {
		t.Fatalf("digest não-reproduzível entre sessões: %q vs %q", res3.Digest, res1.Digest)
	}
}

// ---------------------------------------------------------------------------
// Teste 6 — SLI de cache-hit-rate não regride com compressão activa
// ---------------------------------------------------------------------------

func TestSLI_CacheHitRateNaoRegrideComCompressao(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	compactor := newCompactor(t, es)

	// Cenário de referência (AOS-037): prefixo GRANDE e estável, tails pequenos, muitos
	// turnos -> o SLI de cache-hit-rate fica acima do alvo (>80%).
	bigSystem := ""
	for i := 0; i < 200; i++ {
		bigSystem += "instrucao-de-sistema-estavel-" + strconv.Itoa(i) + " "
	}
	wm, err := working.NewWindowManager(working.Config{
		RunID:           "run-sli",
		System:          bigSystem,
		ModelTokenLimit: 100000,
	})
	if err != nil {
		t.Fatalf("NewWindowManager: %v", err)
	}
	prefixBefore := wm.PrefixHash()

	trigger, err := NewCheckpointTrigger(compactor, DefaultCompressionPolicy(),
		WithPrefixHash(wm.PrefixHash))
	if err != nil {
		t.Fatalf("NewCheckpointTrigger: %v", err)
	}

	const turns = 30
	for i := 1; i <= turns; i++ {
		wm.Append(working.TailInput{Kind: working.TailToolResult, Content: "t" + strconv.Itoa(i), Priority: 1})
		res := wm.Turn(ctx)
		src := baseSource("run-sli", "ckpt-"+strconv.Itoa(i), wm.PrefixHash(), i)
		if _, oerr := trigger.Observe(ctx, res.Exhaustion, src); oerr != nil {
			t.Fatalf("Observe: %v", oerr)
		}
	}

	// BASELINE de não-regressão: captura o SLI ANTES de correr a compactação. A
	// compressão nunca deve baixá-lo (não-regressão em sentido estrito, não só um piso).
	rateBefore := wm.CacheHitRate()

	// Corre a compactação nos checkpoints (fora do turno). Com o prefixo estável, a
	// compactação NUNCA o muta -> sem cache thrash -> o SLI não regride.
	results, err := trigger.RunCheckpoint(ctx)
	if err != nil {
		t.Fatalf("RunCheckpoint: %v", err)
	}
	for _, r := range results {
		if r.CacheThrash {
			t.Fatalf("cache thrash inesperado no checkpoint %s (prefixo estável)", r.CheckpointID)
		}
		if !r.PrefixInvariant {
			t.Fatalf("prefixo não-invariante no checkpoint %s", r.CheckpointID)
		}
	}

	// O prefixo permanece byte-idêntico DEPOIS da compressão (a compressão actua no tail).
	if wm.PrefixHash() != prefixBefore {
		t.Fatalf("prefixo mudou após compressão: %q != %q", wm.PrefixHash(), prefixBefore)
	}

	// NÃO-REGRESSÃO EXPLÍCITA: o SLI DEPOIS da compactação não é inferior ao de antes.
	// A compactação corre fora do turno e não toca na janela viva (opera sobre os
	// snapshots enfileirados), pelo que a taxa é preservada — a prova directa (além do
	// piso absoluto) de que activar a compressão não degrada o cache-hit-rate.
	rateAfter := wm.CacheHitRate()
	if rateAfter < rateBefore {
		t.Fatalf("cache-hit-rate regrediu com compressão: antes=%.4f depois=%.4f", rateBefore, rateAfter)
	}

	// SLI acima do alvo (>80%) COM compressão activa — não regrediu.
	if rateAfter <= 0.80 {
		t.Fatalf("cache-hit-rate=%.4f regrediu abaixo do alvo (>0.80) com compressão activa", rateAfter)
	}
}

// ---------------------------------------------------------------------------
// Teste 7 — detecção e alerta de cache thrash
// ---------------------------------------------------------------------------

func TestRunCheckpoint_AlertaDeCacheThrash(t *testing.T) {
	ctx := context.Background()
	compactor := newCompactor(t, newES(t))
	alerts := &recordingAlerts{}

	// O leitor de prefixo devolve um hash DIFERENTE do capturado na origem — simula uma
	// invalidação externa do prefixo entre o enfileiramento e o checkpoint.
	mutated := "sha256:prefixo-DIFERENTE-mutado"
	trigger, err := NewCheckpointTrigger(compactor, DefaultCompressionPolicy(),
		WithPrefixHash(func() string { return mutated }),
		WithAlertSink(alerts))
	if err != nil {
		t.Fatalf("NewCheckpointTrigger: %v", err)
	}

	src := baseSource("run-1", "ckpt-1", "sha256:prefixo-original", 3)
	sig := working.Exhaustion{Triggered: true, Action: working.ActionMarkForCompression}
	if _, err := trigger.Observe(ctx, sig, src); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	results, err := trigger.RunCheckpoint(ctx)
	if err != nil {
		t.Fatalf("RunCheckpoint: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("esperava 1 resultado, obtive %d", len(results))
	}
	if !results[0].CacheThrash {
		t.Fatal("cache thrash não detectado apesar do prefixo mutado")
	}
	if results[0].PrefixInvariant {
		t.Fatal("PrefixInvariant=true com prefixo mutado")
	}
	if alerts.count() != 1 {
		t.Fatalf("esperava 1 alerta de cache thrash, obtive %d", alerts.count())
	}
}

// ---------------------------------------------------------------------------
// Teste — Compact aborta directamente em prefixo mutado (fail-closed)
// ---------------------------------------------------------------------------

func TestCompact_PrefixoMutadoAbortaFailClosed(t *testing.T) {
	ctx := context.Background()
	compactor := newCompactor(t, newES(t))
	src := baseSource("run-1", "ckpt-1", "sha256:original", 3)

	_, err := compactor.Compact(ctx, src, DefaultCompressionPolicy(), "sha256:mutado")
	if !errors.Is(err, ErrPrefixMutated) {
		t.Fatalf("erro=%v, esperava ErrPrefixMutated", err)
	}
}

// ---------------------------------------------------------------------------
// Testes de validação fail-closed
// ---------------------------------------------------------------------------

func TestValidacao_FailClosed(t *testing.T) {
	ctx := context.Background()
	compactor := newCompactor(t, newES(t))

	tests := []struct {
		name    string
		src     CompactionSource
		policy  CompressionPolicy
		wantErr error
	}{
		{
			name:    "sem run_id",
			src:     CompactionSource{CheckpointID: "c", TraceID: "t", Turns: buildTurns(1)},
			policy:  DefaultCompressionPolicy(),
			wantErr: ErrMissingRunID,
		},
		{
			name:    "sem checkpoint_id",
			src:     CompactionSource{RunID: "r", TraceID: "t", Turns: buildTurns(1)},
			policy:  DefaultCompressionPolicy(),
			wantErr: ErrMissingCheckpointID,
		},
		{
			name:    "sem trace_id",
			src:     CompactionSource{RunID: "r", CheckpointID: "c", Turns: buildTurns(1)},
			policy:  DefaultCompressionPolicy(),
			wantErr: ErrMissingTraceID,
		},
		{
			name:    "sem turnos",
			src:     CompactionSource{RunID: "r", CheckpointID: "c", TraceID: "t"},
			policy:  DefaultCompressionPolicy(),
			wantErr: ErrNoTurns,
		},
		{
			name:    "política com versão inválida",
			src:     baseSource("r", "c", "p", 2),
			policy:  CompressionPolicy{Version: "nao-semver", Projection: projection.DefaultPolicy()},
			wantErr: ErrInvalidPolicyVersion,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := compactor.Compact(ctx, tc.src, tc.policy, tc.src.PrefixHash)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("erro=%v, esperava %v", err, tc.wantErr)
			}
		})
	}
}

func TestConstrutores_FailClosed(t *testing.T) {
	if _, err := NewAsyncCompactor(nil); !errors.Is(err, ErrNilEventLog) {
		t.Fatalf("NewAsyncCompactor(nil): erro=%v, esperava ErrNilEventLog", err)
	}
	if _, err := NewCheckpointTrigger(nil, DefaultCompressionPolicy()); !errors.Is(err, ErrNilCompactor) {
		t.Fatalf("NewCheckpointTrigger(nil): erro=%v, esperava ErrNilCompactor", err)
	}
	compactor := newCompactor(t, newES(t))
	badPolicy := CompressionPolicy{Version: "x", Projection: projection.DefaultPolicy()}
	if _, err := NewCheckpointTrigger(compactor, badPolicy); !errors.Is(err, ErrInvalidPolicyVersion) {
		t.Fatalf("NewCheckpointTrigger(política inválida): erro=%v, esperava ErrInvalidPolicyVersion", err)
	}
}

// ---------------------------------------------------------------------------
// Teste — backpressure da fila de checkpoints
// ---------------------------------------------------------------------------

func TestObserve_BackpressureFilaCheia(t *testing.T) {
	ctx := context.Background()
	compactor := newCompactor(t, newES(t))
	trigger, err := NewCheckpointTrigger(compactor, DefaultCompressionPolicy(),
		WithMaxCheckpoints(2))
	if err != nil {
		t.Fatalf("NewCheckpointTrigger: %v", err)
	}
	sig := working.Exhaustion{Triggered: true, Action: working.ActionMarkForCompression}
	for i := 0; i < 2; i++ {
		src := baseSource("r", "c"+strconv.Itoa(i), "p", 1)
		if _, err := trigger.Observe(ctx, sig, src); err != nil {
			t.Fatalf("Observe %d: %v", i, err)
		}
	}
	// A terceira excede o tecto: backpressure fail-closed.
	_, err = trigger.Observe(ctx, sig, baseSource("r", "c2", "p", 1))
	if !errors.Is(err, ErrQueueFull) {
		t.Fatalf("erro=%v, esperava ErrQueueFull", err)
	}
}

// ---------------------------------------------------------------------------
// Teste — spans do checkpoint (observabilidade, DoD)
// ---------------------------------------------------------------------------

func TestCompact_EmiteSpanDeCheckpoint(t *testing.T) {
	ctx := context.Background()
	tracer := &agentruntime.RecordingTracer{}
	compactor, err := NewAsyncCompactor(newES(t), WithCompactorTracer(tracer))
	if err != nil {
		t.Fatalf("NewAsyncCompactor: %v", err)
	}
	src := baseSource("run-1", "ckpt-1", "sha256:p", 3)
	if _, err := compactor.Compact(ctx, src, DefaultCompressionPolicy(), "sha256:p"); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	spans := tracer.SpansByOperation(spanCompact)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span %q, obtive %d", spanCompact, len(spans))
	}
	sp := spans[0]
	if sp.Attributes[attrOffHotPathCheckpt] != true {
		t.Fatal("span de compactação sem marca de checkpoint (aos...checkpoint=true)")
	}
	if sp.Attributes[attrPrefixInvariant] != true {
		t.Fatal("span de compactação sem prefix_invariant=true")
	}
	if !sp.Ended {
		t.Fatal("span de compactação não foi fechado")
	}
}

// ---------------------------------------------------------------------------
// Teste — RunCheckpoint recoloca os não-processados ao falhar (fail-closed)
// ---------------------------------------------------------------------------

// failingLog falha o Append após okAppends escritas bem-sucedidas.
type failingLog struct {
	inner      EventLog
	okAppends  int
	seen       int
	errToThrow error
}

func (f *failingLog) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	f.seen++
	if f.seen > f.okAppends {
		return eventstore.AppendResult{}, f.errToThrow
	}
	return f.inner.Append(ctx, streamID, in, opts...)
}

func (f *failingLog) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	return f.inner.Read(ctx, streamID, fromSeq)
}

func TestRunCheckpoint_RecolocaAoFalhar(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom-io")
	log := &failingLog{inner: newES(t), okAppends: 1, errToThrow: boom}
	compactor := newCompactor(t, log)
	trigger, err := NewCheckpointTrigger(compactor, DefaultCompressionPolicy())
	if err != nil {
		t.Fatalf("NewCheckpointTrigger: %v", err)
	}
	sig := working.Exhaustion{Triggered: true, Action: working.ActionMarkForCompression}
	for i := 0; i < 3; i++ {
		if _, err := trigger.Observe(ctx, sig, baseSource("r", "c"+strconv.Itoa(i), "p", 2)); err != nil {
			t.Fatalf("Observe %d: %v", i, err)
		}
	}
	// A 1ª compacta (append ok); a 2ª falha no Append -> pára e recoloca 2ª+3ª.
	results, err := trigger.RunCheckpoint(ctx)
	if !errors.Is(err, boom) {
		t.Fatalf("erro=%v, esperava boom", err)
	}
	if len(results) != 1 {
		t.Fatalf("esperava 1 resultado antes da falha, obtive %d", len(results))
	}
	if trigger.PendingCount() != 2 {
		t.Fatalf("esperava 2 checkpoints recolocados, pending=%d", trigger.PendingCount())
	}
}

// ---------------------------------------------------------------------------
// Teste — Summaries reconstrói vários checkpoints por replay
// ---------------------------------------------------------------------------

func TestSummaries_ReplayMultiplosCheckpoints(t *testing.T) {
	ctx := context.Background()
	compactor := newCompactor(t, newES(t))
	for i := 1; i <= 3; i++ {
		src := baseSource("run-1", "ckpt-"+strconv.Itoa(i), "sha256:p", i+1)
		if _, err := compactor.Compact(ctx, src, DefaultCompressionPolicy(), "sha256:p"); err != nil {
			t.Fatalf("Compact %d: %v", i, err)
		}
	}
	summaries, err := compactor.Summaries(ctx)
	if err != nil {
		t.Fatalf("Summaries: %v", err)
	}
	if len(summaries) != 3 {
		t.Fatalf("esperava 3 sumários, obtive %d", len(summaries))
	}
	// Ordem de escrita preservada (replay determinístico).
	for i, s := range summaries {
		want := "ckpt-" + strconv.Itoa(i+1)
		if s.CheckpointID != want {
			t.Fatalf("sumário %d: checkpoint=%q, esperava %q", i, s.CheckpointID, want)
		}
	}
	// Log vazio (stream inexistente) devolve nil sem erro.
	fresh := newCompactor(t, newES(t))
	empty, err := fresh.Summaries(ctx)
	if err != nil {
		t.Fatalf("Summaries (vazio): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("esperava 0 sumários num log vazio, obtive %d", len(empty))
	}
}

// ---------------------------------------------------------------------------
// Teste — acessores do trigger
// ---------------------------------------------------------------------------

func TestTrigger_Acessores(t *testing.T) {
	tracer := &agentruntime.RecordingTracer{}
	compactor, err := NewAsyncCompactor(newES(t), WithCompactorTracer(tracer))
	if err != nil {
		t.Fatalf("NewAsyncCompactor: %v", err)
	}
	policy := DefaultCompressionPolicy()
	trigger, err := NewCheckpointTrigger(compactor, policy, WithTriggerTracer(tracer))
	if err != nil {
		t.Fatalf("NewCheckpointTrigger: %v", err)
	}
	if trigger.Compactor() != compactor {
		t.Fatal("Compactor() não devolve o compactor injectado")
	}
	if trigger.Policy().Version != policy.Version {
		t.Fatalf("Policy().Version=%q, esperava %q", trigger.Policy().Version, policy.Version)
	}
}

// ---------------------------------------------------------------------------
// Teste — retry/failover é no-op ponta-a-ponta: NÃO re-emite o registo completo
// ---------------------------------------------------------------------------

// trajectoryPersistSpan é o nome do span raiz emitido por record.Persist (pacote
// record, const não exportada spanTrajectory). Reaplicar uma compactação já
// persistida NÃO deve re-emitir este span (senão cada retry duplicaria a árvore de
// spans do registo completo no backend real, EPIC-08).
const trajectoryPersistSpan = "trajectory.persist"

func TestCompact_RetryNaoReEmiteRegistoNemDiverge(t *testing.T) {
	ctx := context.Background()
	tracer := &agentruntime.RecordingTracer{}
	compactor, err := NewAsyncCompactor(newES(t), WithCompactorTracer(tracer))
	if err != nil {
		t.Fatalf("NewAsyncCompactor: %v", err)
	}
	src := baseSource("run-1", "ckpt-1", "sha256:p", 3)
	policy := DefaultCompressionPolicy()

	// 1ª compactação: emite o registo completo UMA vez (span raiz trajectory.persist).
	res1, err := compactor.Compact(ctx, src, policy, "sha256:p")
	if err != nil {
		t.Fatalf("Compact #1: %v", err)
	}
	if res1.Duplicate {
		t.Fatal("primeira compactação marcada como duplicado")
	}
	if n := len(tracer.SpansByOperation(trajectoryPersistSpan)); n != 1 {
		t.Fatalf("registo emitido %d vezes na 1ª compactação, esperava 1", n)
	}

	// RETRY (mesma f(run_id, checkpoint_id)): tem de ser no-op durável E NÃO re-emitir
	// o registo — a dedup é verificada ANTES de record.Persist.
	res2, err := compactor.Compact(ctx, src, policy, "sha256:p")
	if err != nil {
		t.Fatalf("Compact #2 (retry): %v", err)
	}
	if !res2.Duplicate {
		t.Fatal("retry não foi detectado como duplicado")
	}
	if n := len(tracer.SpansByOperation(trajectoryPersistSpan)); n != 1 {
		t.Fatalf("retry re-emitiu o registo completo (spans trajectory.persist=%d, esperava 1)", n)
	}

	// O resultado do retry coincide com o estado DURÁVEL (reidratado do sumário
	// persistido), não com um valor recém-computado potencialmente divergente.
	if res2.Digest != res1.Digest {
		t.Fatalf("retry divergiu no digest: %q vs %q", res2.Digest, res1.Digest)
	}
	if string(res2.Summary.Bytes()) != string(res1.Summary.Bytes()) {
		t.Fatal("retry divergiu no sumário face ao estado durável")
	}
	if res2.FullRecordSpans != res1.FullRecordSpans || res2.TotalTurns != res1.TotalTurns {
		t.Fatalf("retry divergiu nos metadados duráveis (spans=%d/%d turns=%d/%d)",
			res2.FullRecordSpans, res1.FullRecordSpans, res2.TotalTurns, res1.TotalTurns)
	}

	// E persistiu-se um ÚNICO sumário (sem dupla-escrita no Event Store).
	summaries, err := compactor.Summaries(ctx)
	if err != nil {
		t.Fatalf("Summaries: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("esperava 1 sumário durável após retry, obtive %d", len(summaries))
	}
}

// dupOnAppendLog força o ramo de CORRIDA: existingSummary não encontra nada (Read =>
// stream inexistente), mas o Append devolve StatusDuplicate com um evento DURÁVEL
// pré-fabricado — simula outro executor a persistir a mesma activity entre a
// verificação de dedup e o Append. Prova que o resultado reidrata do durável.
type dupOnAppendLog struct{ dupEvent eventstore.Event }

func (l *dupOnAppendLog) Append(_ context.Context, _ string, _ eventstore.EventInput, _ ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	return eventstore.AppendResult{Status: eventstore.StatusDuplicate, Event: l.dupEvent}, nil
}

func (l *dupOnAppendLog) Read(_ context.Context, _ string, _ uint64) ([]eventstore.Event, error) {
	return nil, eventstore.ErrStreamNotFound
}

func TestCompact_CorridaDuplicadaReidrataDoDuravel(t *testing.T) {
	ctx := context.Background()

	// Estado DURÁVEL "vencedor" com valores DISTINTIVOS (diferentes do que seria
	// recém-computado) — para provar que o resultado devolvido é o durável, não o novo.
	durable := summaryEnvelope{
		SchemaVersion:   summaryEnvelopeSchemaVersion,
		RunID:           "run-1",
		CheckpointID:    "ckpt-1",
		TraceID:         "trace-run-1",
		PolicyVersion:   DefaultCompressionPolicyVersion,
		PrefixHash:      "sha256:p",
		FullRecordSpans: 999,
		TotalTurns:      42,
		Digest:          "sha256:DURAVEL-VENCEDOR",
		Summary:         projection.InjectedView{},
	}
	payload, err := json.Marshal(durable)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	compactor := newCompactor(t, &dupOnAppendLog{dupEvent: eventstore.Event{Payload: payload}})

	src := baseSource("run-1", "ckpt-1", "sha256:p", 3)
	res, err := compactor.Compact(ctx, src, DefaultCompressionPolicy(), "sha256:p")
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if !res.Duplicate {
		t.Fatal("corrida não marcada como duplicado")
	}
	// Reidratou do estado DURÁVEL: digest/metadados são os do vencedor, não os novos.
	if res.Digest != durable.Digest {
		t.Fatalf("resultado não reidratou do durável: digest=%q, esperava %q", res.Digest, durable.Digest)
	}
	if res.FullRecordSpans != durable.FullRecordSpans || res.TotalTurns != durable.TotalTurns {
		t.Fatalf("metadados não coincidem com o durável: spans=%d turns=%d", res.FullRecordSpans, res.TotalTurns)
	}
}

// ---------------------------------------------------------------------------
// Teste — trigger multi-run: prefixo por run, sem thrash falso cruzado
// ---------------------------------------------------------------------------

func TestRunCheckpoint_MultiRunPrefixoPorRunSemThrashFalso(t *testing.T) {
	ctx := context.Background()
	compactor := newCompactor(t, newES(t))
	alerts := &recordingAlerts{}

	prefixA := "sha256:prefixo-run-A"
	prefixB := "sha256:prefixo-run-B"
	// Leitores POR RUN: cada run compara sempre contra o SEU prefixo (ambos estáveis).
	trigger, err := NewCheckpointTrigger(compactor, DefaultCompressionPolicy(),
		WithPrefixHashForRun("run-A", func() string { return prefixA }),
		WithPrefixHashForRun("run-B", func() string { return prefixB }),
		WithAlertSink(alerts))
	if err != nil {
		t.Fatalf("NewCheckpointTrigger: %v", err)
	}

	sig := working.Exhaustion{Triggered: true, Action: working.ActionMarkForCompression}
	if _, err := trigger.Observe(ctx, sig, baseSource("run-A", "ckpt-A", prefixA, 2)); err != nil {
		t.Fatalf("Observe run-A: %v", err)
	}
	if _, err := trigger.Observe(ctx, sig, baseSource("run-B", "ckpt-B", prefixB, 2)); err != nil {
		t.Fatalf("Observe run-B: %v", err)
	}

	results, err := trigger.RunCheckpoint(ctx)
	if err != nil {
		t.Fatalf("RunCheckpoint: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("esperava 2 resultados, obtive %d", len(results))
	}
	// NENHUM run é falsamente marcado com thrash: run-B nunca é comparado com o
	// prefixo vivo de run-A (a regressão que esta correcção elimina).
	for _, r := range results {
		if r.CacheThrash {
			t.Fatalf("thrash FALSO no run %s (prefixo do próprio run é estável)", r.RunID)
		}
		if !r.PrefixInvariant {
			t.Fatalf("prefixo não-invariante no run %s", r.RunID)
		}
	}
	if alerts.count() != 0 {
		t.Fatalf("alertas de thrash espúrios: %d (esperava 0)", alerts.count())
	}
}

// Um leitor por run que muda SÓ o prefixo de run-A deve detectar thrash em run-A e
// deixar run-B invariante — a detecção continua correcta e isolada por run.
func TestRunCheckpoint_MultiRunThrashIsoladoPorRun(t *testing.T) {
	ctx := context.Background()
	compactor := newCompactor(t, newES(t))
	alerts := &recordingAlerts{}

	prefixBStable := "sha256:prefixo-run-B"
	trigger, err := NewCheckpointTrigger(compactor, DefaultCompressionPolicy(),
		// run-A: leitor devolve um hash DIFERENTE do snapshot (mutação real).
		WithPrefixHashForRun("run-A", func() string { return "sha256:run-A-MUTADO" }),
		// run-B: leitor coincide com o snapshot (estável).
		WithPrefixHashForRun("run-B", func() string { return prefixBStable }),
		WithAlertSink(alerts))
	if err != nil {
		t.Fatalf("NewCheckpointTrigger: %v", err)
	}

	sig := working.Exhaustion{Triggered: true, Action: working.ActionMarkForCompression}
	if _, err := trigger.Observe(ctx, sig, baseSource("run-A", "ckpt-A", "sha256:run-A-original", 2)); err != nil {
		t.Fatalf("Observe run-A: %v", err)
	}
	if _, err := trigger.Observe(ctx, sig, baseSource("run-B", "ckpt-B", prefixBStable, 2)); err != nil {
		t.Fatalf("Observe run-B: %v", err)
	}

	results, err := trigger.RunCheckpoint(ctx)
	if err != nil {
		t.Fatalf("RunCheckpoint: %v", err)
	}
	byRun := map[string]CompactionResult{}
	for _, r := range results {
		byRun[r.RunID] = r
	}
	if !byRun["run-A"].CacheThrash {
		t.Fatal("run-A: thrash real não detectado (prefixo do run-A mudou)")
	}
	if byRun["run-B"].CacheThrash {
		t.Fatal("run-B: thrash falso (o prefixo de run-B é estável e independente de run-A)")
	}
	if alerts.count() != 1 {
		t.Fatalf("esperava 1 alerta (só run-A), obtive %d", alerts.count())
	}
}

// Um leitor GLOBAL (single-run) NÃO deve contaminar outros runs quando, por
// configuração incorrecta, o trigger acaba a servir vários runs: em lote multi-run o
// leitor global é ambíguo e cai no snapshot de cada pedido (sem thrash falso).
func TestRunCheckpoint_LeitorGlobalNaoContaminaMultiRun(t *testing.T) {
	ctx := context.Background()
	compactor := newCompactor(t, newES(t))
	alerts := &recordingAlerts{}

	liveA := "sha256:prefixo-run-A-vivo"
	trigger, err := NewCheckpointTrigger(compactor, DefaultCompressionPolicy(),
		WithPrefixHash(func() string { return liveA }), // pertence de facto a run-A
		WithAlertSink(alerts))
	if err != nil {
		t.Fatalf("NewCheckpointTrigger: %v", err)
	}

	sig := working.Exhaustion{Triggered: true, Action: working.ActionMarkForCompression}
	// run-A: snapshot == leitor global (invariante para o dono do leitor).
	if _, err := trigger.Observe(ctx, sig, baseSource("run-A", "ckpt-A", liveA, 2)); err != nil {
		t.Fatalf("Observe run-A: %v", err)
	}
	// run-B: snapshot próprio, DIFERENTE do leitor global de run-A.
	if _, err := trigger.Observe(ctx, sig, baseSource("run-B", "ckpt-B", "sha256:prefixo-run-B", 2)); err != nil {
		t.Fatalf("Observe run-B: %v", err)
	}

	results, err := trigger.RunCheckpoint(ctx)
	if err != nil {
		t.Fatalf("RunCheckpoint: %v", err)
	}
	for _, r := range results {
		if r.CacheThrash {
			t.Fatalf("thrash FALSO no run %s: o leitor global de outro run foi aplicado", r.RunID)
		}
	}
	if alerts.count() != 0 {
		t.Fatalf("alertas espúrios com leitor global em multi-run: %d", alerts.count())
	}
}

// containsSubstr reporta se s contém sub (sem depender de strings.Contains para
// clareza do intent do teste anti-vazamento).
func containsSubstr(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
