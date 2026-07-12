package durable

import (
	"bytes"
	"context"
	"reflect"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Harness: o loop REAL de AOS-013 sobre o RM real (AOS-003) e o Event Store real
// replicado (AOS-002), com o Checkpointer REAL de AOS-015 ligado.
// ---------------------------------------------------------------------------

type loopHarness struct {
	store    *eventstore.Store
	rm       *referencemonitor.Monitor
	recorder *agentruntime.TurnRecorder
	prefixes [][]byte // Prefix capturado por chamada ao modelo
}

func newLoopHarness(t *testing.T, store *eventstore.Store) *loopHarness {
	t.Helper()
	sink := referencemonitor.NewEventStoreSink(store)
	rm := referencemonitor.New(referencemonitor.WithEventSink(sink))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		t.Fatalf("Register(echo): %v", err)
	}
	return &loopHarness{store: store, rm: rm, recorder: agentruntime.NewTurnRecorder(store)}
}

// model devolve um ModelClient que faz 1 tool call no turno 1 e termina no turno 2,
// capturando o Prefix de cada turno em h.prefixes.
func (h *loopHarness) model() agentruntime.ModelClient {
	callN := 0
	return agentruntime.ModelClientFunc(func(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		h.prefixes = append(h.prefixes, append([]byte(nil), view.Prefix...))
		callN++
		if callN == 1 {
			return agentruntime.ModelResponse{
				Text:      "chamo a echo",
				ToolCalls: []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("ola")}},
				Usage:     agentruntime.Usage{InputTokens: 10, OutputTokens: 5},
			}, nil
		}
		return agentruntime.ModelResponse{Text: "feito", Final: true, Usage: agentruntime.Usage{InputTokens: 4, OutputTokens: 2}}, nil
	})
}

func loopGoal(runID string) agentruntime.Goal {
	return agentruntime.Goal{
		RunID: runID,
		Principal: referencemonitor.Principal{
			NHIID:           "nhi:agent-1",
			AgentID:         "agent-1",
			AgentClass:      "researcher",
			DelegationChain: []referencemonitor.DelegationHop{{Sub: "human:alice", ActAs: "nhi:agent-1"}},
			Authority:       []string{"cap:echo"},
		},
		Scope:     []string{"cap:echo"},
		Model:     agentruntime.ModelConfig{ModelID: "claude-opus-4-8", Seed: 42},
		System:    "És um agente do AOS.",
		Tools:     []agentruntime.ToolSpec{{Name: "echo", Version: "1.0.0", Digest: "sha256:aa01"}},
		Objective: "Faz echo.",
	}
}

// runLoop corre o loop real com o checkpointer dado e devolve o resultado.
func (h *loopHarness) runLoop(t *testing.T, runID string, cpr agentruntime.Checkpointer, seq *StepSequencer) agentruntime.Result {
	t.Helper()
	rt := agentruntime.New(h.model(), h.rm, h.recorder,
		agentruntime.WithStepIdentity(seq),
		agentruntime.WithCheckpointer(cpr),
	)
	res, err := rt.Run(context.Background(), loopGoal(runID))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Terminated || res.Turns != 2 {
		t.Fatalf("desfecho inesperado: %+v", res)
	}
	return res
}

// ---------------------------------------------------------------------------
// Integração: os checkpoints persistem no Event Store e o resume reconstrói o
// cursor a partir do loop REAL.
// ---------------------------------------------------------------------------

func TestIntegration_LoopPersistsCheckpoints(t *testing.T) {
	const runID = "run_loop_int"
	seq := NewStepSequencer()
	store := newStore(t)
	h := newLoopHarness(t, store)

	cpr, err := NewCheckpointer(store, WithCheckpointProducer(eventstore.Producer{NHIID: "nhi:agent-1"}))
	if err != nil {
		t.Fatalf("NewCheckpointer: %v", err)
	}
	h.runLoop(t, runID, cpr, seq)

	// A identidade emissora vai gravada nos eventos de checkpoint (proveniência).
	evs, _ := store.Read(context.Background(), runID, 1)
	for _, e := range evs {
		if e.Type == EventTypeCheckpoint && e.Producer.NHIID != "nhi:agent-1" {
			t.Fatalf("checkpoint sem producer esperado: %+v", e.Producer)
		}
	}

	// O loop emite checkpoints em TODAS as fases: turno 1 (assembled, model_called,
	// turn_recorded, dispatched-tool-1, verified) + turno 2 (4 fases, sem tools) = 9.
	if n := countType(t, store, runID, EventTypeCheckpoint); n != 9 {
		t.Fatalf("esperava 9 eventos de checkpoint, obtive %d", n)
	}

	// O resume reconstrói a fronteira: turno 2 verified ⇒ próximo é o turno 3.
	resumer, _ := NewResumer(store, WithStepIdentity(seq))
	rp, err := resumer.Resume(context.Background(), runID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if rp.FromScratch || rp.NextTurn != 3 || rp.NextStepID != seq.StepID(runID, 3) {
		t.Fatalf("fronteira de resume inesperada: %+v", rp)
	}
	if agentruntime.CheckpointPhase(rp.LastConfirmed.Phase) != agentruntime.PhaseVerified {
		t.Fatalf("última fase confirmada = %q, esperava verified", rp.LastConfirmed.Phase)
	}
}

// ---------------------------------------------------------------------------
// Integração: os checkpoints sobrevivem a FAILOVER de worker (ES 3 réplicas +
// kill/elect). A fonte de verdade é o Event Store replicado (ADR-007).
// ---------------------------------------------------------------------------

func TestIntegration_CheckpointsSurviveFailover(t *testing.T) {
	const runID = "run_failover"
	seq := NewStepSequencer()
	store := newStore(t) // 3 réplicas, quórum 2
	if got := store.AliveCount(); got != 3 {
		t.Fatalf("esperava 3 réplicas vivas, obtive %d", got)
	}
	h := newLoopHarness(t, store)
	cpr, _ := NewCheckpointer(store)

	// Worker A escreve os checkpoints ao correr o loop.
	h.runLoop(t, runID, cpr, seq)

	resumer, _ := NewResumer(store, WithStepIdentity(seq))
	want, err := resumer.Resume(context.Background(), runID)
	if err != nil {
		t.Fatalf("Resume (pré-failover): %v", err)
	}

	// Failover #1: mata o líder → uma follower actualizada é eleita (kill/elect).
	leader0 := store.Leader()
	if err := store.Kill(leader0); err != nil {
		t.Fatalf("Kill(%d): %v", leader0, err)
	}
	leader1 := store.Leader()
	if leader1 == -1 || leader1 == leader0 {
		t.Fatalf("eleição falhou após kill do líder %d: novo líder %d", leader0, leader1)
	}
	if store.AliveCount() != 2 {
		t.Fatalf("esperava 2 réplicas vivas após 1 kill, obtive %d", store.AliveCount())
	}

	// Worker B (Resumer novo sobre o mesmo cluster) recupera o cursor — os
	// checkpoints sobreviveram à morte do worker/líder que os escreveu.
	assertResumeEquals(t, store, seq, runID, want, "após failover #1")

	// Failover #2: revive a réplica morta (resync a partir do líder) e mata o líder
	// actual — mantém quórum e prova durabilidade ao longo de eleições sucessivas.
	if err := store.Revive(leader0); err != nil {
		t.Fatalf("Revive(%d): %v", leader0, err)
	}
	if err := store.Kill(leader1); err != nil {
		t.Fatalf("Kill(%d): %v", leader1, err)
	}
	if store.Leader() == -1 {
		t.Fatalf("store indisponível após revive+kill (perda de quórum inesperada)")
	}
	assertResumeEquals(t, store, seq, runID, want, "após failover #2 (revive+kill)")
}

func assertResumeEquals(t *testing.T, store *eventstore.Store, seq *StepSequencer, runID string, want ResumePoint, when string) {
	t.Helper()
	resumer, _ := NewResumer(store, WithStepIdentity(seq))
	got, err := resumer.Resume(context.Background(), runID)
	if err != nil {
		t.Fatalf("Resume (%s): %v", when, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cursor de resume divergiu %s:\n got=%+v\nwant=%+v", when, got, want)
	}
}

// ---------------------------------------------------------------------------
// A escrita de checkpoint NÃO muta o prefixo cache-estável do prompt (ADR-009).
// ---------------------------------------------------------------------------

func TestIntegration_CheckpointDoesNotMutatePrefix(t *testing.T) {
	const runReal, runNoop = "run_prefix_real", "run_prefix_noop"
	seq := NewStepSequencer()

	// Run A: com o Checkpointer REAL (escreve checkpoints entre passos).
	storeA := newStore(t)
	hA := newLoopHarness(t, storeA)
	cpr, _ := NewCheckpointer(storeA)
	hA.runLoop(t, runReal, cpr, seq)

	// Dentro do run, o prefixo é byte-idêntico entre turnos (o tail cresce, o
	// prefixo imutável não).
	if len(hA.prefixes) != 2 || !bytes.Equal(hA.prefixes[0], hA.prefixes[1]) {
		t.Fatalf("prefixo não é byte-idêntico entre turnos com checkpointer real")
	}

	// Run B: com o Checkpointer no-op (default). O prefixo TEM de ser byte-idêntico
	// ao do run com checkpointer real — provando que a escrita de checkpoint não
	// influencia o prefixo cache-estável (o checkpointer nem toca no assembler).
	storeB := newStore(t)
	hB := newLoopHarness(t, storeB)
	hB.runLoop(t, runNoop, noopCP{}, seq)

	if len(hB.prefixes) != 2 {
		t.Fatalf("esperava 2 prefixos no run no-op, obtive %d", len(hB.prefixes))
	}
	if !bytes.Equal(hA.prefixes[0], hB.prefixes[0]) {
		t.Fatalf("o prefixo com checkpointer real difere do prefixo sem checkpointer — mutação proibida")
	}

	// E o registo só CRESCE (append-only): há checkpoints no run real e nenhum no
	// run no-op — o tail regista, sem reescrever história.
	if countType(t, storeA, runReal, EventTypeCheckpoint) == 0 {
		t.Fatalf("run real não escreveu checkpoints")
	}
	if n := countType(t, storeB, runNoop, EventTypeCheckpoint); n != 0 {
		t.Fatalf("run no-op não devia ter checkpoints, obtive %d", n)
	}
}

// noopCP é um Checkpointer que não persiste nada (equivalente ao default do loop),
// usado para isolar o efeito da escrita de checkpoint no prefixo.
type noopCP struct{}

func (noopCP) Checkpoint(context.Context, agentruntime.Checkpoint) error { return nil }
