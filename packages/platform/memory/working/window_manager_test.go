package working

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/memory/adapters"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/ports"
)

// refTools é um tool set de referência congelado (ordem significativa).
func refTools() []ToolSpec {
	return []ToolSpec{
		{Name: "search", Version: "1.2.0", Digest: "sha256:aaa", MCPServer: "mcp-a"},
		{Name: "calc", Version: "0.9.1", Digest: "sha256:bbb", MCPServer: "mcp-b"},
	}
}

func newRefWM(t *testing.T, opts ...func(*Config)) *WindowManager {
	t.Helper()
	cfg := Config{
		RunID:           "run-ref",
		System:          "Sistema de referência: segue as regras. " + strings.Repeat("contexto base ", 40),
		Tools:           refTools(),
		ModelTokenLimit: 1000,
	}
	for _, o := range opts {
		o(&cfg)
	}
	wm, err := NewWindowManager(cfg)
	if err != nil {
		t.Fatalf("NewWindowManager: %v", err)
	}
	return wm
}

// ---------------------------------------------------------------------------
// Teste 1 — IMUTABILIDADE DO PREFIXO: hash constante ao longo dos turnos de um run.
// ---------------------------------------------------------------------------

func TestPrefixImmutableAcrossTurns(t *testing.T) {
	wm := newRefWM(t)
	ctx := context.Background()

	prefixHash0 := wm.PrefixHash()
	prefixBytes0 := string(wm.Prefix())

	var promptHashes []string
	for turn := 0; turn < 5; turn++ {
		// O tail CRESCE a cada turno (append-only), o prefixo NÃO.
		wm.Append(TailInput{Kind: TailHistory, Content: "turno " + itoa(turn) + " resultado do modelo"})
		res := wm.Turn(ctx)

		if got := wm.PrefixHash(); got != prefixHash0 {
			t.Fatalf("turno %d: prefix hash mutou: %q != %q", turn, got, prefixHash0)
		}
		if res.View.PrefixHash != prefixHash0 {
			t.Fatalf("turno %d: PromptView.PrefixHash divergente: %q", turn, res.View.PrefixHash)
		}
		if got := string(wm.Prefix()); got != prefixBytes0 {
			t.Fatalf("turno %d: bytes do prefixo mudaram", turn)
		}
		// O prefixo materializado tem de ser byte-idêntico ao prefixo congelado.
		if !strings.HasPrefix(string(res.View.Materialized), prefixBytes0) {
			t.Fatalf("turno %d: materializado não começa pelo prefixo congelado", turn)
		}
		promptHashes = append(promptHashes, res.View.PromptHash)
	}

	// O PROMPT hash (prefixo+tail) muda a cada turno porque o tail cresce — prova
	// que a janela cresce SÓ pelo tail, com o prefixo constante.
	for i := 1; i < len(promptHashes); i++ {
		if promptHashes[i] == promptHashes[i-1] {
			t.Fatalf("prompt hash não mudou entre turnos %d e %d (tail não cresceu?)", i-1, i)
		}
	}
}

// ---------------------------------------------------------------------------
// Teste 2 — CONTABILIDADE DE TOKENS vs limite + sinal a ~80% (nunca hard-stop).
// ---------------------------------------------------------------------------

func TestTokenAccountingAndGracefulSignal(t *testing.T) {
	// Estimador determinístico simples: 1 token por caractere, para controlar os
	// limiares com precisão.
	perChar := func(s string) int { return len([]rune(s)) }

	tests := []struct {
		name         string
		limit        int
		ratio        float64
		prefixSystem string
		appends      []string
		wantTrigger  bool
		wantAction   ExhaustionAction
		wantMarked   bool
	}{
		{
			name:         "abaixo do limiar não dispara",
			limit:        1000,
			ratio:        0.80,
			prefixSystem: "sys",
			appends:      []string{strings.Repeat("a", 100)},
			wantTrigger:  false,
			wantAction:   ActionNone,
			wantMarked:   false,
		},
		{
			name:         "a 80 por cento marca para compressao",
			limit:        1000,
			ratio:        0.80,
			prefixSystem: strings.Repeat("s", 100), // prefixo ~ >100 tokens (com layout)
			appends:      []string{strings.Repeat("a", 750)},
			wantTrigger:  true,
			wantAction:   ActionMarkForCompression,
			wantMarked:   true,
		},
		{
			name:         "acima do limite escala (nunca hard-stop)",
			limit:        500,
			ratio:        0.80,
			prefixSystem: "sys",
			appends:      []string{strings.Repeat("a", 600)},
			wantTrigger:  true,
			wantAction:   ActionEscalate,
			wantMarked:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wm, err := NewWindowManager(Config{
				RunID:           "run-tok",
				System:          tc.prefixSystem,
				ModelTokenLimit: tc.limit,
				ExhaustionRatio: tc.ratio,
				Estimator:       perChar,
			})
			if err != nil {
				t.Fatalf("NewWindowManager: %v", err)
			}

			var occ Occupancy
			for _, a := range tc.appends {
				occ = wm.Append(TailInput{Kind: TailMemory, Content: a})
			}

			// Contabilidade EM TOKENS (não nº de mensagens): total = prefixo + tail.
			if occ.Total() != occ.PrefixTokens+occ.TailTokens {
				t.Fatalf("total != prefixo+tail: %+v", occ)
			}
			// O append NUNCA falha por tamanho (exaustão graciosa, não cega): mesmo
			// acima do limite a ocupação é contabilizada e o tail foi aceite.
			if occ.TailTokens == 0 && len(tc.appends) > 0 {
				t.Fatalf("tail não contabilizado (append bloqueado?)")
			}

			sig := wm.Signal()
			if sig.Triggered != tc.wantTrigger {
				t.Fatalf("Triggered=%v, quero %v (ocupação=%d/%d limiar=%d)",
					sig.Triggered, tc.wantTrigger, occ.Total(), occ.Limit, occ.Threshold)
			}
			if sig.Action != tc.wantAction {
				t.Fatalf("Action=%q, quero %q", sig.Action, tc.wantAction)
			}
			if wm.MarkedForCompression() != tc.wantMarked {
				t.Fatalf("MarkedForCompression=%v, quero %v", wm.MarkedForCompression(), tc.wantMarked)
			}
		})
	}
}

func TestThresholdIsEightyPercentByDefault(t *testing.T) {
	wm := newRefWM(t)
	occ := wm.Occupancy()
	if occ.Threshold != int(float64(occ.Limit)*DefaultExhaustionRatio) {
		t.Fatalf("limiar default não é ~80%%: %d (limite %d)", occ.Threshold, occ.Limit)
	}
}

// ---------------------------------------------------------------------------
// Teste 3 — EVICTION do tail NÃO apaga o registo no backend (cruza AOS-036).
// ---------------------------------------------------------------------------

func TestEvictionPreservesRecordInBackend(t *testing.T) {
	ctx := context.Background()
	port := adapters.NewInMemoryAdapter()
	sink := NewMemoryPortSink(MemoryPortSinkConfig{
		Port:    port,
		AgentID: "agent-1",
		RunID:   "run-evict",
	})

	perChar := func(s string) int { return len([]rune(s)) }
	wm, err := NewWindowManager(Config{
		RunID:           "run-evict",
		System:          "sys",
		ModelTokenLimit: 100000,
		Estimator:       perChar,
		Sink:            sink,
	})
	if err != nil {
		t.Fatalf("NewWindowManager: %v", err)
	}

	prefixHashBefore := wm.PrefixHash()

	// Três segmentos; o do meio tem prioridade alta (deve sobreviver na vista).
	wm.Append(TailInput{Kind: TailMemory, Content: strings.Repeat("x", 100), Priority: 0, RecordID: "seg-a"})
	wm.Append(TailInput{Kind: TailMemory, Content: strings.Repeat("y", 100), Priority: 9, RecordID: "seg-b"})
	wm.Append(TailInput{Kind: TailMemory, Content: strings.Repeat("z", 100), Priority: 0, RecordID: "seg-c"})

	// Evict até o tail caber em 150 tokens: saem os de prioridade 0 mais antigos.
	evicted, err := wm.EvictToTailBudget(ctx, 150)
	if err != nil {
		t.Fatalf("EvictToTailBudget: %v", err)
	}
	if len(evicted) == 0 {
		t.Fatalf("nada evictado")
	}

	// A eviction NUNCA toca o prefixo.
	if wm.PrefixHash() != prefixHashBefore {
		t.Fatalf("eviction mutou o prefixo")
	}

	// PRESERVAÇÃO: cada segmento evictado PERMANECE no backend (Princípio 4).
	for _, ev := range evicted {
		rec, err := port.Get(ctx, domain.ClassWorking, ev.RecordID)
		if err != nil {
			t.Fatalf("registo evictado %q perdido no backend: %v", ev.RecordID, err)
		}
		wb, ok := rec.Body.(domain.WorkingBody)
		if !ok {
			t.Fatalf("registo %q não é WorkingBody", ev.RecordID)
		}
		if wb.Content != ev.Content || wb.TokenCount != ev.Tokens {
			t.Fatalf("registo %q divergente: %+v vs %+v", ev.RecordID, wb, ev)
		}
	}

	// O segmento de prioridade alta NÃO saiu da vista.
	if _, err := port.Get(ctx, domain.ClassWorking, "seg-b"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("seg-b (prioridade alta) foi evictado indevidamente")
	}

	// A vista encolheu; o registo no backend permanece intacto e recuperável.
	if wm.Occupancy().TailTokens > 150 {
		t.Fatalf("tail não respeitou o orçamento pós-eviction: %d", wm.Occupancy().TailTokens)
	}
}

func TestEvictionWithoutSinkIsRefused(t *testing.T) {
	ctx := context.Background()
	perChar := func(s string) int { return len([]rune(s)) }
	wm, err := NewWindowManager(Config{
		RunID:           "run-nosink",
		System:          "sys",
		ModelTokenLimit: 100000,
		Estimator:       perChar,
	})
	if err != nil {
		t.Fatalf("NewWindowManager: %v", err)
	}
	wm.Append(TailInput{Kind: TailMemory, Content: strings.Repeat("x", 200)})
	if _, err := wm.EvictToTailBudget(ctx, 10); !errors.Is(err, ErrNoEvictionSink) {
		t.Fatalf("eviction sem sink devia ser recusada (fail-closed), got %v", err)
	}
	// Fail-closed: a vista NÃO foi alterada (o registo não seria preservado).
	if wm.Occupancy().TailTokens != 200 {
		t.Fatalf("tail alterado apesar da recusa: %d", wm.Occupancy().TailTokens)
	}
}

// Eviction com RecordID duplicado de conteúdo DIVERGENTE aborta fail-closed e NÃO
// remove o segmento distinto da vista (Princípio 4: o que sai da vista permanece
// byte-a-byte no registo; a chave idempotente não sobrescreve).
func TestEvictionRejectsDivergentDuplicateRecordID(t *testing.T) {
	ctx := context.Background()
	port := adapters.NewInMemoryAdapter()
	sink := NewMemoryPortSink(MemoryPortSinkConfig{Port: port, AgentID: "a", RunID: "run-dup"})
	perChar := func(s string) int { return len([]rune(s)) }
	wm, err := NewWindowManager(Config{
		RunID: "run-dup", System: "sys", ModelTokenLimit: 100000, Estimator: perChar, Sink: sink,
	})
	if err != nil {
		t.Fatalf("NewWindowManager: %v", err)
	}
	// Dois segmentos distintos partilham o MESMO RecordID explícito.
	wm.Append(TailInput{Kind: TailMemory, Content: "AAAA_original", Priority: 0, RecordID: "dupe"})
	wm.Append(TailInput{Kind: TailMemory, Content: "BBBB_different_content", Priority: 0, RecordID: "dupe"})
	tailBefore := wm.Occupancy().TailTokens

	_, err = wm.EvictToTailBudget(ctx, 0)
	if !errors.Is(err, ErrEvictionRecordConflict) {
		t.Fatalf("eviction com duplicado divergente devia abortar com ErrEvictionRecordConflict, got %v", err)
	}
	// Fail-closed: a vista NÃO encolheu (o segundo conteúdo não foi de facto persistido).
	if wm.Occupancy().TailTokens != tailBefore {
		t.Fatalf("vista alterada apesar do aborto: %d != %d", wm.Occupancy().TailTokens, tailBefore)
	}
	// O registo preservado é o PRIMEIRO conteúdo (a chave idempotente não sobrescreve).
	rec, gerr := port.Get(ctx, domain.ClassWorking, "dupe")
	if gerr != nil {
		t.Fatalf("registo dupe perdido: %v", gerr)
	}
	if wb := rec.Body.(domain.WorkingBody); wb.Content != "AAAA_original" {
		t.Fatalf("conteúdo persistido inesperado: %q", wb.Content)
	}
}

// O latch marked-for-compression é DERIVADO da ocupação corrente: uma eviction que
// alivia a pressão abaixo do limiar tem de o limpar (não pode ficar preso a true).
func TestMarkedForCompressionClearedAfterEviction(t *testing.T) {
	ctx := context.Background()
	port := adapters.NewInMemoryAdapter()
	sink := NewMemoryPortSink(MemoryPortSinkConfig{Port: port, AgentID: "a", RunID: "run-latch"})
	perChar := func(s string) int { return len([]rune(s)) }
	wm, err := NewWindowManager(Config{
		RunID: "run-latch", System: "sys", ModelTokenLimit: 1000, ExhaustionRatio: 0.80,
		Estimator: perChar, Sink: sink,
	})
	if err != nil {
		t.Fatalf("NewWindowManager: %v", err)
	}
	// Cruza o limiar (~800): marca para compressão.
	wm.Append(TailInput{Kind: TailMemory, Content: strings.Repeat("a", 900), Priority: 0, RecordID: "big"})
	if !wm.MarkedForCompression() {
		t.Fatalf("devia estar marcado para compressão após cruzar o limiar")
	}
	// Eviction alivia bem abaixo do limiar → o latch tem de ser limpo.
	if _, err := wm.EvictToTailBudget(ctx, 10); err != nil {
		t.Fatalf("EvictToTailBudget: %v", err)
	}
	if wm.Occupancy().Exhausted() {
		t.Fatalf("pré-condição: ocupação devia estar abaixo do limiar pós-eviction")
	}
	if wm.MarkedForCompression() {
		t.Fatalf("latch marked-for-compression não foi limpo após eviction aliviar a pressão")
	}
}

// O run derivado herda EXACTAMENTE o mesmo rácio de exaustão (não reconstruído a
// partir do threshold inteiro truncado): o threshold do filho coincide com o do pai.
func TestDerivedRunPreservesExhaustionRatio(t *testing.T) {
	// limit=1234, ratio=0.80 → threshold=987. A reconstrução via float(987)/1234
	// daria 0.79983… → threshold=986 (desvio de 1 token). Preservado, mantém-se 987.
	wm, err := NewWindowManager(Config{
		RunID: "run-parent", System: "sys", Tools: refTools(),
		ModelTokenLimit: 1234, ExhaustionRatio: 0.80,
	})
	if err != nil {
		t.Fatalf("NewWindowManager: %v", err)
	}
	child, err := wm.NewRunWith("run-child")
	if err != nil {
		t.Fatalf("NewRunWith: %v", err)
	}
	if got, want := child.Occupancy().Threshold, wm.Occupancy().Threshold; got != want {
		t.Fatalf("threshold do run derivado desviou: %d != %d (rácio não preservado)", got, want)
	}
	if wm.Occupancy().Threshold != 987 {
		t.Fatalf("threshold pai inesperado: %d (quero 987)", wm.Occupancy().Threshold)
	}
}

// ---------------------------------------------------------------------------
// Teste 4 — SLI de CACHE-HIT-RATE acima do alvo (>80%) num cenário de referência.
// ---------------------------------------------------------------------------

func TestCacheHitRateSLIAboveTarget(t *testing.T) {
	// Cenário de referência: prefixo grande e estável (system + toolset), tails
	// pequenos, muitos turnos. O prefix caching (ADR-009) domina → hit-rate alto.
	wm := newRefWM(t)
	ctx := context.Background()

	const turns = 10
	for i := 0; i < turns; i++ {
		wm.Append(TailInput{Kind: TailHistory, Content: "ok"}) // tail minúsculo
		wm.Turn(ctx)
	}

	rate := wm.CacheHitRate()
	if rate <= 0.80 {
		t.Fatalf("cache-hit-rate SLI %.4f não está acima do alvo 0.80", rate)
	}

	// Não-regressão: mais turnos com o mesmo prefixo estável só sobem o SLI.
	prev := rate
	for i := 0; i < turns; i++ {
		wm.Append(TailInput{Kind: TailHistory, Content: "ok"})
		wm.Turn(ctx)
	}
	if wm.CacheHitRate() < prev {
		t.Fatalf("SLI regrediu com prefixo estável: %.4f < %.4f", wm.CacheHitRate(), prev)
	}
}

// ---------------------------------------------------------------------------
// Teste 5 — Novas tools que QUEBRARIAM o prefixo só entram em RUNS NOVOS.
// ---------------------------------------------------------------------------

func TestNewToolsOnlyEnterNewRuns(t *testing.T) {
	wm := newRefWM(t)
	newTool := ToolSpec{Name: "browser", Version: "2.0.0", Digest: "sha256:ccc", MCPServer: "mcp-c"}

	// No run corrente: introduzir uma tool nova é REJEITADO (prefixo pinado).
	if err := wm.RequireTool(newTool); !errors.Is(err, ErrPrefixPinned) {
		t.Fatalf("tool nova no run corrente devia ser rejeitada, got %v", err)
	}
	// Uma tool já congelada é aceite (no-op).
	if err := wm.RequireTool(refTools()[0]); err != nil {
		t.Fatalf("tool congelada devia ser aceite: %v", err)
	}
	// Uma tool com o mesmo NOME mas versão/digest diferente quebraria o prefixo →
	// também é rejeitada.
	drifted := refTools()[0]
	drifted.Version = "9.9.9"
	if err := wm.RequireTool(drifted); !errors.Is(err, ErrPrefixPinned) {
		t.Fatalf("tool com identidade divergente devia ser rejeitada, got %v", err)
	}

	// O run corrente conserva o seu prefixo intocado.
	prefixCurrent := wm.PrefixHash()

	// A tool nova SÓ entra num RUN NOVO: prefixo diferente, hash diferente.
	wm2, err := wm.NewRunWith("run-ref-2", newTool)
	if err != nil {
		t.Fatalf("NewRunWith: %v", err)
	}
	if !wm2.HasTool("browser") {
		t.Fatalf("run novo devia conter a tool nova")
	}
	if wm2.PrefixHash() == prefixCurrent {
		t.Fatalf("run novo devia ter um prefixo diferente (nova tool)")
	}
	// O run corrente permanece inalterado após derivar o run novo.
	if wm.PrefixHash() != prefixCurrent {
		t.Fatalf("run corrente mudou ao derivar um run novo")
	}
	// O run novo mantém o CONCEITO: prefixo imutável ao longo dos seus turnos.
	ctx := context.Background()
	h0 := wm2.PrefixHash()
	wm2.Append(TailInput{Kind: TailHistory, Content: "t"})
	wm2.Turn(ctx)
	if wm2.PrefixHash() != h0 {
		t.Fatalf("run novo não manteve o prefixo imutável")
	}
}

// ---------------------------------------------------------------------------
// Observabilidade — span com gen_ai.usage.* e prefix_hash (SLI observável).
// ---------------------------------------------------------------------------

func TestTurnEmitsUsageSpan(t *testing.T) {
	tr := &agentruntime.RecordingTracer{}
	wm := newRefWM(t, func(c *Config) { c.Tracer = tr })
	ctx := context.Background()

	wm.Append(TailInput{Kind: TailMemory, Content: "algum contexto"})
	res := wm.Turn(ctx)

	spans := tr.SpansByOperation(spanWindowTurn)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span de turno, got %d", len(spans))
	}
	s := spans[0]
	if _, ok := s.Attributes[agentruntime.AttrInputTokens]; !ok {
		t.Fatalf("span sem gen_ai.usage.input_tokens")
	}
	if got := s.Attributes[agentruntime.AttrPrefixHash]; got != res.View.PrefixHash {
		t.Fatalf("prefix_hash do span divergente: %v vs %v", got, res.View.PrefixHash)
	}
	if got := s.Attributes[agentruntime.AttrInputTokens]; got != res.Occupancy.Total() {
		t.Fatalf("input_tokens do span != ocupação: %v vs %d", got, res.Occupancy.Total())
	}
	if !s.Ended {
		t.Fatalf("span não foi fechado")
	}
}

// ---------------------------------------------------------------------------
// Validação fail-closed da construção.
// ---------------------------------------------------------------------------

func TestNewWindowManagerValidation(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want error
	}{
		{"sem run id", Config{ModelTokenLimit: 100}, ErrMissingRunID},
		{"limite não-positivo", Config{RunID: "r"}, ErrInvalidLimit},
		{"rácio inválido", Config{RunID: "r", ModelTokenLimit: 100, ExhaustionRatio: 1.5}, ErrInvalidRatio},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewWindowManager(tc.cfg); !errors.Is(err, tc.want) {
				t.Fatalf("erro=%v, quero %v", err, tc.want)
			}
		})
	}
}

// Garante que o sink escreve metadados válidos (não-zero CreatedAt determinístico).
func TestMemoryPortSinkWritesValidRecord(t *testing.T) {
	ctx := context.Background()
	port := adapters.NewInMemoryAdapter()
	sink := NewMemoryPortSink(MemoryPortSinkConfig{Port: port, AgentID: "a", RunID: "r"})
	if err := sink.Persist(ctx, EvictedSegment{RecordID: "s1", TurnIndex: 2, Kind: "memory", Content: "c", Tokens: 3}); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	rec, err := port.Get(ctx, domain.ClassWorking, "s1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := rec.Validate(); err != nil {
		t.Fatalf("registo persistido inválido: %v", err)
	}
	if rec.Metadata.TTLClass != domain.TTLEphemeral {
		t.Fatalf("TTL default devia ser ephemeral, got %v", rec.Metadata.TTLClass)
	}
}

// Eviction sem RecordID explícito deriva um ID estável de (run, turn, seq) e
// preserva na mesma o registo no backend.
func TestEvictionDerivesRecordIDWhenAbsent(t *testing.T) {
	ctx := context.Background()
	port := adapters.NewInMemoryAdapter()
	sink := NewMemoryPortSink(MemoryPortSinkConfig{Port: port, AgentID: "a", RunID: "run-auto"})
	perChar := func(s string) int { return len([]rune(s)) }
	wm, err := NewWindowManager(Config{
		RunID: "run-auto", System: "sys", ModelTokenLimit: 100000, Estimator: perChar, Sink: sink,
	})
	if err != nil {
		t.Fatalf("NewWindowManager: %v", err)
	}
	if wm.RunID() != "run-auto" {
		t.Fatalf("RunID accessor: %q", wm.RunID())
	}
	if wm.Occupancy().Ratio() < 0 {
		t.Fatalf("Ratio negativo")
	}
	wm.Append(TailInput{Kind: TailMemory, Content: strings.Repeat("x", 100)}) // sem RecordID
	evicted, err := wm.EvictToTailBudget(ctx, 10)
	if err != nil {
		t.Fatalf("EvictToTailBudget: %v", err)
	}
	if len(evicted) != 1 || evicted[0].RecordID == "" {
		t.Fatalf("RecordID derivado em falta: %+v", evicted)
	}
	if _, err := port.Get(ctx, domain.ClassWorking, evicted[0].RecordID); err != nil {
		t.Fatalf("registo com id derivado perdido: %v", err)
	}
}

// Confirma que a MemoryPort satisfaz o contrato usado pelo sink (compile-time).
var _ ports.MemoryPort = (*adapters.InMemoryAdapter)(nil)
