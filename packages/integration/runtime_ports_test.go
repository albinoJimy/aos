package integration

import (
	"bytes"
	"context"
	"sync"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/activity"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/memory/compression"
	"github.com/aos-ref/platform/memory/record"
	"github.com/aos-ref/substrate/eventstore"
)

// portTestGoal é um goal mínimo para exercitar as portas RT/RM (AOS-157).
func portTestGoal() agentruntime.Goal {
	return agentruntime.Goal{
		RunID: "run-ports",
		Principal: referencemonitor.Principal{
			NHIID:           "nhi:test",
			AgentID:         "agt",
			AgentClass:      "researcher",
			Authority:       []string{"cap:echo"},
			DelegationChain: []referencemonitor.DelegationHop{{Sub: "human:alice", ActAs: "agt"}},
		},
		Scope:     []string{"cap:echo"},
		Model:     agentruntime.ModelConfig{ModelID: "claude-opus-4-8", Seed: 1},
		System:    "sistema de teste de portas RT/RM",
		Tools:     []agentruntime.ToolSpec{{Name: "echo", Version: "1.0.0", Digest: "sha256:aa"}},
		Objective: "faz echo do input",
	}
}

// portModel devolve um ModelClient de 2 turnos: turno 1 chama echo; turno 2 conclui.
// Se views != nil, captura o PromptView de cada turno (para asserção de byte-identidade).
func portModel(views *[]agentruntime.PromptView) agentruntime.ModelClient {
	turn := 0
	return agentruntime.ModelClientFunc(func(_ context.Context, pv agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		if views != nil {
			*views = append(*views, pv)
		}
		turn++
		if turn == 1 {
			return agentruntime.ModelResponse{
				Text:      "penso",
				ToolCalls: []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("x")}},
			}, nil
		}
		return agentruntime.ModelResponse{Final: true, Text: "fim"}, nil
	})
}

// ---------------------------------------------------------------------------
// AOS-037 — WindowManagerFactory (dono único, byte-identidade, D-TAIL)
// ---------------------------------------------------------------------------

// TestWindowManagerFactory_ByteIdenticalToInline prova que ligar o WindowManager como
// dono único da janela (D-TAIL) NÃO altera o prompt: os bytes materializados e os hashes
// de cada turno são IDÊNTICOS aos do default inline. E que o prefix-hash é único por run.
func TestWindowManagerFactory_ByteIdenticalToInline(t *testing.T) {
	run := func(opts ...agentruntime.Option) []agentruntime.PromptView {
		store, err := eventstore.New()
		if err != nil {
			t.Fatalf("eventstore.New: %v", err)
		}
		defer store.Close()
		rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
		if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
			t.Fatalf("Register: %v", err)
		}
		var views []agentruntime.PromptView
		rt := agentruntime.New(portModel(&views), rm, agentruntime.NewTurnRecorder(store), opts...)
		if _, err := rt.Run(context.Background(), portTestGoal()); err != nil {
			t.Fatalf("Run: %v", err)
		}
		return views
	}

	inline := run() // default inlineWindow
	// Opções exercitadas (rácio/estimador/tracer) — não alteram os bytes materializados,
	// só a contabilidade de tokens e a observabilidade da janela.
	wf, err := NewWindowManagerFactory(100_000,
		WithExhaustionRatio(0.75),
		WithTokenEstimator(func(s string) int { return len(s) }),
		WithWindowTracer(agentruntime.NoopTracer{}),
	)
	if err != nil {
		t.Fatalf("NewWindowManagerFactory: %v", err)
	}
	managed := run(agentruntime.WithWindowFactory(wf)) // WindowManager como dono

	if len(inline) != len(managed) || len(managed) < 2 {
		t.Fatalf("nº de turnos: inline=%d managed=%d (quero >=2 e iguais)", len(inline), len(managed))
	}
	for i := range inline {
		if !bytes.Equal(inline[i].Materialized, managed[i].Materialized) {
			t.Fatalf("turno %d: Materialized diverge do inline\n inline=%q\nmanaged=%q", i+1, inline[i].Materialized, managed[i].Materialized)
		}
		if inline[i].PrefixHash != managed[i].PrefixHash || inline[i].PromptHash != managed[i].PromptHash {
			t.Fatalf("turno %d: hashes divergem (prefix %s/%s, prompt %s/%s)", i+1, inline[i].PrefixHash, managed[i].PrefixHash, inline[i].PromptHash, managed[i].PromptHash)
		}
	}
	// D-TAIL: um só prefix-hash por run com o WindowManager.
	for i := range managed {
		if managed[i].PrefixHash != managed[0].PrefixHash {
			t.Fatalf("prefix-hash do turno %d diverge do turno 1 — o WindowManager não é o dono único estável", i+1)
		}
	}
}

// TestWindowManagerFactory_FailClosed prova a validação fail-closed do limite de tokens.
func TestWindowManagerFactory_FailClosed(t *testing.T) {
	if _, err := NewWindowManagerFactory(0); err == nil {
		t.Fatal("ModelTokenLimit=0 devia ser recusado (fail-closed)")
	}
}

// ---------------------------------------------------------------------------
// AOS-021 — DurableDispatcher (preserva o Credential, deny não-fatal)
// ---------------------------------------------------------------------------

// credRecorder é um hook do RM que REGISTA o Credential de cada Call e permite.
type credRecorder struct {
	mu    sync.Mutex
	creds []string
}

func (h *credRecorder) Name() string { return "cred-rec" }
func (h *credRecorder) Evaluate(_ context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	h.mu.Lock()
	h.creds = append(h.creds, call.Credential)
	h.mu.Unlock()
	return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
}

// denyHook nega toda a call (para provar o deny não-fatal da via durável).
type denyHook struct{}

func (denyHook) Name() string { return "always-deny" }
func (denyHook) Evaluate(_ context.Context, _ *referencemonitor.Call) (referencemonitor.HookResult, error) {
	return referencemonitor.HookResult{Decision: referencemonitor.HookDeny, Reason: "teste"}, nil
}

func newDurableDispatcher(t *testing.T, store *eventstore.Store, rm *referencemonitor.Monitor) *DurableDispatcher {
	t.Helper()
	ledger, err := durable.NewStepLedger(store)
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	disp, err := activity.NewDispatcher(rm, ledger)
	if err != nil {
		t.Fatalf("activity.NewDispatcher: %v", err)
	}
	dd, err := NewDurableDispatcher(disp)
	if err != nil {
		t.Fatalf("NewDurableDispatcher: %v", err)
	}
	return dd
}

// TestDurableDispatcher_PreservesCredential é o CERNE do escopo AOS-021 escolhido: a via
// durável (activity.Dispatcher, idempotência por step-ledger) NÃO perde o Credential do
// loop — o token NHI (AOS-152) atravessa o loop → porta → Activity → Call e chega ao hook
// de identidade. Sem a preservação, a tool call seria anónima e negada.
func TestDurableDispatcher_PreservesCredential(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()
	rec := &credRecorder{}
	rm := referencemonitor.New(referencemonitor.WithHooks(rec), referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return append([]byte("echoed:"), in...), nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	dd := newDurableDispatcher(t, store, rm)

	rt := agentruntime.New(portModel(nil), rm, agentruntime.NewTurnRecorder(store), agentruntime.WithActivityDispatcher(dd))
	g := portTestGoal()
	g.Credential = "nhi-token-xyz"
	res, err := rt.Run(context.Background(), g)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Credential preservado pela via durável.
	rec.mu.Lock()
	creds := append([]string(nil), rec.creds...)
	rec.mu.Unlock()
	found := false
	for _, c := range creds {
		if c == "nhi-token-xyz" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Credential não preservado pela via durável (AOS-152): registados=%v", creds)
	}
	// O efeito correu: o Output voltou ao loop.
	if len(res.ToolResults) != 1 || !bytes.Contains(res.ToolResults[0].Value, []byte("echoed:x")) {
		t.Fatalf("output da tool não voltou pela via durável: %+v", res.ToolResults)
	}
}

// TestDurableDispatcher_DenyIsNonFatal prova que um deny do RM (activity.ErrMediationDenied)
// é traduzido numa Decision de Deny NÃO-FATAL: o run conclui e o resultado da tool volta
// vazio (untrusted), como na via directa.
func TestDurableDispatcher_DenyIsNonFatal(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()
	rm := referencemonitor.New(referencemonitor.WithHooks(denyHook{}), referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	dd := newDurableDispatcher(t, store, rm)

	rt := agentruntime.New(portModel(nil), rm, agentruntime.NewTurnRecorder(store), agentruntime.WithActivityDispatcher(dd))
	res, err := rt.Run(context.Background(), portTestGoal())
	if err != nil {
		t.Fatalf("Run devia concluir (deny é não-fatal): %v", err)
	}
	if len(res.ToolResults) != 1 || len(res.ToolResults[0].Value) != 0 {
		t.Fatalf("um deny devia dar resultado vazio (untrusted), tenho: %+v", res.ToolResults)
	}
}

// ---------------------------------------------------------------------------
// AOS-043 — CompactionTriggerAdapter (gating do sinal → enfileiramento)
// ---------------------------------------------------------------------------

// TestCompactionTriggerAdapter_ObserveGating prova o gating do adaptador: só enfileira
// quando o sinal disparou E o construtor de source devolve conteúdo a compactar; um sinal
// não-disparado ou uma source ainda-não-pronta não enfileiram.
func TestCompactionTriggerAdapter_ObserveGating(t *testing.T) {
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()
	compactor, err := compression.NewAsyncCompactor(store)
	if err != nil {
		t.Fatalf("NewAsyncCompactor: %v", err)
	}
	trigger, err := compression.NewCheckpointTrigger(compactor, compression.DefaultCompressionPolicy())
	if err != nil {
		t.Fatalf("NewCheckpointTrigger: %v", err)
	}

	sourceReady := true
	builder := func(runID string, turn int) (compression.CompactionSource, bool) {
		if !sourceReady {
			return compression.CompactionSource{}, false
		}
		return compression.CompactionSource{
			RunID:        runID,
			CheckpointID: "ckpt-1",
			TraceID:      "trace-1",
			Turns:        []record.Turn{{Index: turn}},
		}, true
	}
	adapter, err := NewCompactionTriggerAdapter(trigger, builder)
	if err != nil {
		t.Fatalf("NewCompactionTriggerAdapter: %v", err)
	}
	ctx := context.Background()

	// Sinal não-disparado ⇒ não enfileira.
	if enq, err := adapter.Observe(ctx, "run-1", 1, agentruntime.WindowSignal{Triggered: false}); err != nil || enq {
		t.Fatalf("não-disparado: enq=%v err=%v, quero (false,nil)", enq, err)
	}
	if trigger.PendingCount() != 0 {
		t.Fatalf("PendingCount=%d após não-disparado, quero 0", trigger.PendingCount())
	}

	// Disparado + source pronta ⇒ enfileira.
	sig := agentruntime.WindowSignal{Triggered: true, Action: "mark_for_compression", OccupancyTokens: 900, LimitTokens: 1000}
	if enq, err := adapter.Observe(ctx, "run-1", 1, sig); err != nil || !enq {
		t.Fatalf("disparado+source: enq=%v err=%v, quero (true,nil)", enq, err)
	}
	if trigger.PendingCount() != 1 {
		t.Fatalf("PendingCount=%d após enfileirar, quero 1", trigger.PendingCount())
	}

	// Disparado + source ainda-não-pronta ⇒ não enfileira (inalterado).
	sourceReady = false
	if enq, err := adapter.Observe(ctx, "run-1", 2, sig); err != nil || enq {
		t.Fatalf("source não-pronta: enq=%v err=%v, quero (false,nil)", enq, err)
	}
	if trigger.PendingCount() != 1 {
		t.Fatalf("PendingCount=%d, quero 1 (inalterado)", trigger.PendingCount())
	}
}
