package harness

import (
	"context"
	"fmt"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// fixtureEpochUnix é o instante FIXO (UTC) do relógio de captura das fixtures. Um
// valor constante — nunca time.Now — para que as golden trajectories sejam
// byte-reprodutíveis entre execuções.
const fixtureEpochUnix = 1_700_000_000

// fixtureClock devolve o relógio DETERMINÍSTICO das fixtures (constante). Injectado
// no capturer de AOS-016 ([replay.WithClock]); o replay LÊ este carimbo do log e
// nunca um relógio ao vivo.
func fixtureClock() func() time.Time {
	return func() time.Time { return time.Unix(fixtureEpochUnix, 0).UTC() }
}

// Fixture é uma GOLDEN TRAJECTORY construída: a trajectória gravada (turn.recorded
// + replay.captured num Event Store em memória), o Event Store para o ledger de
// idempotência, e os efeitos / pontos de crash a exercitar. Reprodutível entre
// execuções (relógio fixo, modelo guionado, seed pinado). Chame [Fixture.Close]
// quando terminar.
type Fixture struct {
	Name        string
	RunID       string
	trajectory  *eventstore.Store
	ledgerStore *eventstore.Store
	spec        replay.TrajectorySpec
	effects     []Effect
	faults      []FaultPoint
}

// Case constrói a [Case] verificável desta fixture.
func (f *Fixture) Case() Case {
	return Case{
		Name:        f.Name,
		RunID:       f.RunID,
		Reader:      f.trajectory,
		Spec:        f.spec,
		LedgerStore: f.ledgerStore,
		Effects:     f.effects,
		Faults:      f.faults,
	}
}

// Close liberta os Event Stores da fixture.
func (f *Fixture) Close() {
	if f == nil {
		return
	}
	if f.trajectory != nil {
		_ = f.trajectory.Close()
	}
	if f.ledgerStore != nil {
		_ = f.ledgerStore.Close()
	}
}

// scriptedModel é um [agentruntime.ModelClient] DETERMINÍSTICO: devolve a resposta
// guionada do turno (indexada por [agentruntime.PromptView].Turn, 1-based). Sem
// relógio nem random — a fixture é reprodutível.
type scriptedModel struct {
	responses []agentruntime.ModelResponse
}

func (m *scriptedModel) Call(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	idx := view.Turn - 1
	if idx < 0 || idx >= len(m.responses) {
		return agentruntime.ModelResponse{}, fmt.Errorf("harness fixture: turno %d fora do guião (%d respostas)", view.Turn, len(m.responses))
	}
	return m.responses[idx], nil
}

// echoToolSet é o tool set CONGELADO das fixtures (ordem significativa, pinado).
func echoToolSet() []agentruntime.ToolSpec {
	return []agentruntime.ToolSpec{
		{Name: "web_search", Version: "1.7.0", Digest: "sha256:aa01"},
		{Name: "echo", Version: "0.9.0", Digest: "sha256:cc03"},
	}
}

// goldenGoal é o [agentruntime.Goal] base das fixtures: identidade, escopo, modelo
// pinado (model_id/params/seed), system prompt e tool set congelado.
func goldenGoal(runID string) agentruntime.Goal {
	return agentruntime.Goal{
		RunID: runID,
		Principal: referencemonitor.Principal{
			NHIID:      "nhi:agente-golden",
			AgentID:    "agente-golden",
			AgentClass: "researcher",
			Authority:  []string{"cap:echo"},
		},
		Scope:         []string{"cap:echo"},
		Model:         agentruntime.ModelConfig{ModelID: "claude-opus-4-8", Params: map[string]string{"temperature": "0"}, Seed: 42},
		System:        "És um agente determinístico de referência do AOS.",
		Tools:         echoToolSet(),
		Skills:        []agentruntime.ToolSpec{{Name: "report_writer", Version: "2.3.1", Digest: "sha256:bb02"}},
		Objective:     "Faz echo do input.",
		MemoryContext: []byte("memoria-golden"),
	}
}

// specFromGoal deriva a [replay.TrajectorySpec] (inputs determinísticos re-fornecidos
// ao replay) do goal — inclui a config de modelo e a versão do assembler ESPERADAS,
// para que o replay verifique também o drift de modelo/seed/assembly (invisível ao
// prompt_hash) contra o manifesto gravado.
func specFromGoal(g agentruntime.Goal) replay.TrajectorySpec {
	return replay.TrajectorySpec{
		System:          g.System,
		Tools:           g.Tools,
		Objective:       g.Objective,
		MemoryContext:   g.MemoryContext,
		Model:           g.Model,
		AssemblyVersion: agentruntime.AssemblyVersion,
	}
}

// stableEffect constrói um [Effect] IDEMPOTENTE: a idempotency key é a forma
// canónica f(run_id, sub-step_id) e NÃO varia com a tentativa — logo o ledger de
// AOS-014 deduplica as re-tentativas e o efeito corre uma só vez. O contador
// partilhado torna o nº de execuções observável.
func stableEffect(runID string, seq *durable.StepSequencer, turn, index int) Effect {
	stepID := seq.SubStepID(runID, turn, index)
	count := 0
	return Effect{
		StepID: stepID,
		KeyAt: func(int) (string, error) {
			return durable.IdempotencyKey(runID, stepID)
		},
		Run: func(context.Context) (durable.Result, error) {
			count++
			return durable.Result{Status: "ok", Payload: []byte("efeito:" + stepID)}, nil
		},
		Observed: func() int { return count },
	}
}

// BuildEchoGolden constrói a golden trajectory de referência "echo-3turns": três
// turnos, dois com uma tool call (echo) despachada via Reference Monitor real, o
// terceiro final. Corre o loop REAL de AOS-013 (RM + Event Store + capturer de
// AOS-016) com relógio fixo e modelo guionado — determinística e reprodutível.
// Os dois passos de tool são exercitados quanto a idempotência; os step_ids de
// fronteira de turno 2 e 3 são pontos de crash para a fault-injection.
func BuildEchoGolden(runID string) (*Fixture, error) {
	traj, err := eventstore.New()
	if err != nil {
		return nil, err
	}
	sink := referencemonitor.NewEventStoreSink(traj)
	rm := referencemonitor.New(referencemonitor.WithEventSink(sink))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		_ = traj.Close()
		return nil, err
	}

	recorder := agentruntime.NewTurnRecorder(traj)
	capturer, err := replay.NewCapturer(traj, replay.WithClock(fixtureClock()))
	if err != nil {
		_ = traj.Close()
		return nil, err
	}

	model := &scriptedModel{responses: []agentruntime.ModelResponse{
		{
			Text:         "turno 1: chamo echo",
			ToolCalls:    []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("um")}},
			Usage:        agentruntime.Usage{InputTokens: 10, OutputTokens: 5},
			CostMicroUSD: 1200,
		},
		{
			Text:         "turno 2: chamo echo outra vez",
			ToolCalls:    []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("dois")}},
			Usage:        agentruntime.Usage{InputTokens: 8, OutputTokens: 4},
			CostMicroUSD: 1000,
		},
		{
			Text:         "concluído",
			Final:        true,
			Usage:        agentruntime.Usage{InputTokens: 6, OutputTokens: 2},
			CostMicroUSD: 800,
		},
	}}

	goal := goldenGoal(runID)
	rt := agentruntime.New(model, rm, recorder, agentruntime.WithCapturer(capturer))
	res, err := rt.Run(context.Background(), goal)
	if err != nil {
		_ = traj.Close()
		return nil, err
	}
	if !res.Terminated || res.Turns != 3 {
		_ = traj.Close()
		return nil, fmt.Errorf("harness fixture echo-3turns: desfecho inesperado (%+v)", res)
	}

	ledgerStore, err := eventstore.New()
	if err != nil {
		_ = traj.Close()
		return nil, err
	}

	seq := durable.NewStepSequencer()
	effects := []Effect{
		stableEffect(runID, seq, 1, 1),
		stableEffect(runID, seq, 2, 1),
	}
	return &Fixture{
		Name:        "echo-3turns",
		RunID:       runID,
		trajectory:  traj,
		ledgerStore: ledgerStore,
		spec:        specFromGoal(goal),
		effects:     effects,
		faults:      []FaultPoint{{AtStepID: "step-000002"}, {AtStepID: "step-000003"}},
	}, nil
}

// BuildImmediateFinalGolden constrói a golden trajectory "immediate-final": um
// único turno que responde directamente (Final, sem tools). Exercita o caminho de
// terminação imediata e a retoma trivial (resume no turno 1). Sem efeitos externos.
func BuildImmediateFinalGolden(runID string) (*Fixture, error) {
	traj, err := eventstore.New()
	if err != nil {
		return nil, err
	}
	sink := referencemonitor.NewEventStoreSink(traj)
	rm := referencemonitor.New(referencemonitor.WithEventSink(sink))

	recorder := agentruntime.NewTurnRecorder(traj)
	capturer, err := replay.NewCapturer(traj, replay.WithClock(fixtureClock()))
	if err != nil {
		_ = traj.Close()
		return nil, err
	}

	model := &scriptedModel{responses: []agentruntime.ModelResponse{
		{
			Text:         "resposta directa, sem tools",
			Final:        true,
			Usage:        agentruntime.Usage{InputTokens: 4, OutputTokens: 3},
			CostMicroUSD: 500,
		},
	}}

	goal := goldenGoal(runID)
	goal.Objective = "Responde directamente, sem tools."
	rt := agentruntime.New(model, rm, recorder, agentruntime.WithCapturer(capturer))
	res, err := rt.Run(context.Background(), goal)
	if err != nil {
		_ = traj.Close()
		return nil, err
	}
	if !res.Terminated || res.Turns != 1 {
		_ = traj.Close()
		return nil, fmt.Errorf("harness fixture immediate-final: desfecho inesperado (%+v)", res)
	}

	ledgerStore, err := eventstore.New()
	if err != nil {
		_ = traj.Close()
		return nil, err
	}
	return &Fixture{
		Name:        "immediate-final",
		RunID:       runID,
		trajectory:  traj,
		ledgerStore: ledgerStore,
		spec:        specFromGoal(goal),
		effects:     nil,
		faults:      []FaultPoint{{AtStepID: "step-000001"}},
	}, nil
}

// GoldenSet constrói o CONJUNTO de golden trajectories de referência e devolve os
// casos verificáveis mais um closer que liberta os Event Stores. É o material
// reutilizável por outros epics (EPIC-11) e pelo gate 8 do CI.
func GoldenSet() ([]Case, func(), error) {
	f1, err := BuildEchoGolden("golden_echo_3turns")
	if err != nil {
		return nil, nil, err
	}
	f2, err := BuildImmediateFinalGolden("golden_immediate_final")
	if err != nil {
		f1.Close()
		return nil, nil, err
	}
	fixtures := []*Fixture{f1, f2}
	cases := []Case{f1.Case(), f2.Case()}
	closer := func() {
		for _, f := range fixtures {
			f.Close()
		}
	}
	return cases, closer, nil
}

// GoldenReport constrói o golden set, verifica-o e devolve o [AggregateReport] mais
// o closer dos Event Stores. É a conveniência que o gate de CI e as métricas do
// backlog consomem.
func GoldenReport(ctx context.Context, opts ...VerifyOption) (AggregateReport, func(), error) {
	cases, closer, err := GoldenSet()
	if err != nil {
		return AggregateReport{}, nil, err
	}
	agg, err := VerifyAll(ctx, cases, opts...)
	if err != nil {
		closer()
		return AggregateReport{}, nil, err
	}
	return agg, closer, nil
}
