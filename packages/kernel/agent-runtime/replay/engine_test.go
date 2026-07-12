package replay

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Harness: corre uma trajectória ORIGINAL sobre o loop real (AOS-013) + RM real
// (AOS-003) + Event Store real (AOS-002), com o EventStoreCapturer ligado, e
// devolve o material necessário para o replay.
// ---------------------------------------------------------------------------

type originalRun struct {
	store *eventstore.Store
	goal  agentruntime.Goal
	spec  TrajectorySpec
	// toolHits conta quantas vezes CADA tool foi EXECUTADA ao vivo (para provar
	// zero-efeitos no replay: o replay não pode incrementar isto).
	toolHits *int
}

func toolSet() []agentruntime.ToolSpec {
	return []agentruntime.ToolSpec{
		{Name: "web_search", Version: "1.7.0", Digest: "sha256:aa01"},
		{Name: "echo", Version: "0.9.0", Digest: "sha256:cc03"},
	}
}

func sampleGoal(runID string) agentruntime.Goal {
	return agentruntime.Goal{
		RunID: runID,
		Principal: referencemonitor.Principal{
			NHIID:      "nhi:agent-1",
			AgentID:    "agent-1",
			AgentClass: "researcher",
			Authority:  []string{"cap:echo"},
		},
		Scope:         []string{"cap:echo"},
		Model:         agentruntime.ModelConfig{ModelID: "claude-opus-4-8", Params: map[string]string{"temperature": "0"}, Seed: 42},
		System:        "És um agente de investigação do AOS.",
		Tools:         toolSet(),
		Skills:        []agentruntime.ToolSpec{{Name: "report_writer", Version: "2.3.1", Digest: "sha256:bb02"}},
		Objective:     "Faz echo do input.",
		MemoryContext: []byte("memoria-inicial"),
	}
}

// specFromGoal deriva a TrajectorySpec (inputs determinísticos) do goal — é o que
// o chamador do replay re-fornece (código/config). Inclui a config de modelo e a
// versão do assembler ESPERADAS para que o replay verifique o drift de modelo/seed/
// assembly (invisível ao prompt_hash) contra o manifesto gravado.
func specFromGoal(g agentruntime.Goal) TrajectorySpec {
	return TrajectorySpec{
		System:          g.System,
		Tools:           g.Tools,
		Objective:       g.Objective,
		MemoryContext:   g.MemoryContext,
		Model:           g.Model,
		AssemblyVersion: agentruntime.AssemblyVersion,
	}
}

// runOriginalB é o adaptador de benchmark de [runOriginal].
func runOriginalB(b *testing.B, runID string) originalRun { return runOriginal(b, runID) }

// runOriginal executa uma trajectória de 3 turnos: 2 com tool calls, 1 final.
func runOriginal(t testing.TB, runID string) originalRun {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	hits := 0
	sink := referencemonitor.NewEventStoreSink(store)
	rm := referencemonitor.New(referencemonitor.WithEventSink(sink))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		hits++
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	recorder := agentruntime.NewTurnRecorder(store)
	capturer, err := NewCapturer(store, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}

	callN := 0
	model := agentruntime.ModelClientFunc(func(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		callN++
		switch callN {
		case 1:
			return agentruntime.ModelResponse{
				Text:         "primeiro: chamo echo",
				ToolCalls:    []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("um")}},
				Usage:        agentruntime.Usage{InputTokens: 10, OutputTokens: 5},
				CostMicroUSD: 1200,
			}, nil
		case 2:
			return agentruntime.ModelResponse{
				Text:         "segundo: chamo echo outra vez",
				ToolCalls:    []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("dois")}},
				Usage:        agentruntime.Usage{InputTokens: 8, OutputTokens: 4},
				CostMicroUSD: 1000,
			}, nil
		default:
			return agentruntime.ModelResponse{
				Text:         "concluído",
				Final:        true,
				Usage:        agentruntime.Usage{InputTokens: 6, OutputTokens: 2},
				CostMicroUSD: 800,
			}, nil
		}
	})

	rt := agentruntime.New(model, rm, recorder, agentruntime.WithCapturer(capturer))
	goal := sampleGoal(runID)
	res, err := rt.Run(context.Background(), goal)
	if err != nil {
		t.Fatalf("Run original: %v", err)
	}
	if !res.Terminated || res.Turns != 3 {
		t.Fatalf("original inesperado: %+v", res)
	}
	if hits != 2 {
		t.Fatalf("esperava 2 execuções de tool ao vivo no original, obtive %d", hits)
	}
	return originalRun{store: store, goal: goal, spec: specFromGoal(goal), toolHits: &hits}
}

func fixedClock() func() time.Time {
	return func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
}

// mustEngine constrói o motor sobre o store do run original.
func mustEngine(t *testing.T, or originalRun, opts ...EngineOption) *ReplayEngine {
	t.Helper()
	e, err := NewEngine(or.store, opts...)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return e
}

// ---------------------------------------------------------------------------
// Teste 1 — Replay 100%: mesma sequência de step_ids, prompt_hash por turno COINCIDE.
// ---------------------------------------------------------------------------

func TestReplayFidelity100(t *testing.T) {
	or := runOriginal(t, "run_replay_100")
	e := mustEngine(t, or)

	res, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.Divergence != nil {
		t.Fatalf("replay fiel não devia divergir: %+v", res.Divergence)
	}
	if res.Fidelity != 1.0 {
		t.Fatalf("fidelidade = %v, esperava 1.0", res.Fidelity)
	}
	if len(res.Steps) != 3 {
		t.Fatalf("esperava 3 turnos replayados, obtive %d", len(res.Steps))
	}
	if !res.Terminated || res.FinalText != "concluído" {
		t.Fatalf("desfecho reconstruído errado: term=%v text=%q", res.Terminated, res.FinalText)
	}
	// Cada turno: hash re-materializado COINCIDE com o gravado, e step_ids na ordem.
	wantSteps := []string{"step-000001", "step-000002", "step-000003"}
	for i, st := range res.Steps {
		if st.StepID != wantSteps[i] {
			t.Fatalf("step_id[%d] = %q, esperava %q", i, st.StepID, wantSteps[i])
		}
		if !st.Matched {
			t.Fatalf("turno %d não coincidiu", st.Turn)
		}
		if st.PromptHash != st.RecordedPromptHash {
			t.Fatalf("turno %d: prompt_hash re-materializado %q != gravado %q", st.Turn, st.PromptHash, st.RecordedPromptHash)
		}
		if st.PromptHash[:7] != "sha256:" {
			t.Fatalf("prompt_hash mal-formado: %q", st.PromptHash)
		}
	}
}

// ---------------------------------------------------------------------------
// Teste 2 — Captura completa: modelo, tools, relógio e seed lidos do LOG.
// ---------------------------------------------------------------------------

func TestReplayReadsAllNonDeterminismFromLog(t *testing.T) {
	or := runOriginal(t, "run_capture_all")
	e := mustEngine(t, or)
	res, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	// Seed do manifesto (log), não ao vivo.
	for _, st := range res.Steps {
		if st.Seed != 42 {
			t.Fatalf("turno %d: seed = %d, esperava 42 (do manifesto)", st.Turn, st.Seed)
		}
		// Relógio da captura (log), determinístico.
		if st.ObservedAtUnixNano != time.Unix(1_700_000_000, 0).UTC().UnixNano() {
			t.Fatalf("turno %d: observed_at = %d, esperava o carimbo de captura", st.Turn, st.ObservedAtUnixNano)
		}
	}
	// Resposta do modelo reconstruída do log (texto + tool calls).
	if res.Steps[0].Response.Text != "primeiro: chamo echo" {
		t.Fatalf("resposta do turno 1 não veio do log: %q", res.Steps[0].Response.Text)
	}
	if len(res.Steps[0].Response.ToolCalls) != 1 || res.Steps[0].Response.ToolCalls[0].ToolID != "echo" {
		t.Fatalf("tool calls do turno 1 não vieram do log: %+v", res.Steps[0].Response.ToolCalls)
	}
	if res.Steps[2].Response.Final != true {
		t.Fatalf("turno final não marcado Final na reconstrução")
	}
}

// ---------------------------------------------------------------------------
// Teste 3 — Segurança: ZERO efeitos externos em modo replay.
// ---------------------------------------------------------------------------

func TestReplayZeroExternalEffects(t *testing.T) {
	or := runOriginal(t, "run_zero_effects")
	hitsBefore := *or.toolHits // 2 execuções ao vivo no original

	e := mustEngine(t, or)
	if _, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec}); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	// A tool NUNCA foi re-executada durante o replay (o contador não mexeu).
	if *or.toolHits != hitsBefore {
		t.Fatalf("replay executou tools ao vivo: hits %d → %d", hitsBefore, *or.toolHits)
	}

	// Prova ESTRUTURAL: o motor não escreveu no Event Store — o nº de eventos do
	// stream é o mesmo antes e depois do replay (só Read, nunca Append).
	before, _ := or.store.Read(context.Background(), or.goal.RunID, 1)
	if _, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec}); err != nil {
		t.Fatalf("Replay 2: %v", err)
	}
	after, _ := or.store.Read(context.Background(), or.goal.RunID, 1)
	if len(after) != len(before) {
		t.Fatalf("replay escreveu no Event Store: %d → %d eventos", len(before), len(after))
	}
}

// TestEngineHasNoLiveEffectPaths é a prova estrutural por reflexão: o ReplayEngine
// não detém nenhum campo capaz de efeito ao vivo (ModelClient, Monitor, tool,
// Append). Só um EventReader (Read) e um Tracer.
func TestEngineHasNoLiveEffectPaths(t *testing.T) {
	e := &ReplayEngine{}
	rt := reflectType(e)
	for _, forbidden := range []string{"ModelClient", "Monitor", "Appender", "EventStore"} {
		if rt.hasFieldType(forbidden) {
			t.Fatalf("ReplayEngine detém um campo do tipo %q — caminho de efeito ao vivo", forbidden)
		}
	}
	// O único acesso ao store é o EventReader (interface só-Read).
	if !rt.hasFieldType("EventReader") {
		t.Fatalf("ReplayEngine devia deter um EventReader (só-Read)")
	}
}

// ---------------------------------------------------------------------------
// Teste 4 — Negativo: divergência injectada → detecção do PASSO exacto.
// ---------------------------------------------------------------------------

func TestReplayDetectsInjectedDivergence(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(s *TrajectorySpec)
		wantTurn int
		wantStep string
	}{
		{
			name:     "system prompt alterado (evolução de código)",
			mutate:   func(s *TrajectorySpec) { s.System = "SYSTEM PROMPT EVOLUÍDO" },
			wantTurn: 1, // afecta o prefixo ⇒ diverge já no turno 1
			wantStep: "step-000001",
		},
		{
			name:     "objectivo alterado",
			mutate:   func(s *TrajectorySpec) { s.Objective = "objectivo diferente" },
			wantTurn: 1,
			wantStep: "step-000001",
		},
		{
			name: "tool set congelado alterado (digest)",
			mutate: func(s *TrajectorySpec) {
				tools := append([]agentruntime.ToolSpec(nil), s.Tools...)
				tools[0].Digest = "sha256:MUTADO"
				s.Tools = tools
			},
			wantTurn: 1,
			wantStep: "step-000001",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			or := runOriginal(t, "run_div_"+sanitize(tc.name))
			e := mustEngine(t, or)
			spec := or.spec
			tc.mutate(&spec)

			res, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: spec})
			if err != nil {
				t.Fatalf("Replay: %v", err)
			}
			if res.Divergence == nil {
				t.Fatalf("esperava divergência detectada")
			}
			if res.Divergence.Turn != tc.wantTurn || res.Divergence.StepID != tc.wantStep {
				t.Fatalf("divergência localizada em turno=%d step=%q, esperava turno=%d step=%q",
					res.Divergence.Turn, res.Divergence.StepID, tc.wantTurn, tc.wantStep)
			}
			if res.Divergence.Reason != "prompt_hash" {
				t.Fatalf("razão = %q, esperava prompt_hash", res.Divergence.Reason)
			}
			if res.Divergence.ExpectedHash == res.Divergence.ActualHash {
				t.Fatalf("hashes deviam diferir na divergência")
			}
			if res.Fidelity == 1.0 {
				t.Fatalf("fidelidade não devia ser 1.0 numa divergência")
			}
		})
	}
}

// TestReplayDetectsStepIDSequenceDivergence injecta uma StepIdentity incompatível:
// o motor re-deriva os step_ids com um esquema diferente do gravado ⇒ divergência
// de SEQUÊNCIA DE PASSOS localizada já no turno 1 (o prompt_hash coincide; é a
// derivação de step_id que difere). Cobre o ramo Reason=="step_id sequence".
func TestReplayDetectsStepIDSequenceDivergence(t *testing.T) {
	or := runOriginal(t, "run_div_stepid")
	e := mustEngine(t, or)

	res, err := e.Replay(context.Background(), or.goal.RunID, Options{
		Spec:         or.spec,
		StepIdentity: badStepIdentity{},
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.Divergence == nil || res.Divergence.Reason != "step_id sequence" {
		t.Fatalf("esperava divergência de sequência de step_id, obtive %+v", res.Divergence)
	}
	if res.Divergence.Turn != 1 {
		t.Fatalf("divergência de step_id devia localizar-se no turno 1, obtive %d", res.Divergence.Turn)
	}
}

// TestReplayDetectsMidTrajectoryPromptDivergence prova a LOCALIZAÇÃO do passo exacto
// quando o turno 1 COINCIDE e só o turno 2 diverge no prompt_hash — o ramo que anexa
// os turnos já coincididos (res.Steps) e reporta um turno POSTERIOR, e a fidelidade
// PARCIAL matched/verified = 1/2 = 0.5. A divergência a meio é simulada adulterando
// APENAS o prompt_hash gravado do turno 2 (via um EventReader que reescreve esse
// manifesto), deixando o turno 1 intacto — o replay re-materializa o prompt genuíno
// e este passa a divergir do (agora adulterado) gravado só no turno 2.
func TestReplayDetectsMidTrajectoryPromptDivergence(t *testing.T) {
	or := runOriginal(t, "run_div_mid_prompt")
	const tamperedHash = "sha256:TURNO-2-ADULTERADO"
	reader := &manifestMutatingReader{
		inner: or.store,
		mutate: func(turn int, m *agentruntime.Manifest) {
			if turn == 2 {
				m.PromptHash = tamperedHash
			}
		},
	}
	e, err := NewEngine(reader)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	res, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.Divergence == nil {
		t.Fatalf("esperava divergência a meio da trajectória")
	}
	if res.Divergence.Reason != "prompt_hash" {
		t.Fatalf("razão = %q, esperava prompt_hash", res.Divergence.Reason)
	}
	if res.Divergence.Turn != 2 || res.Divergence.StepID != "step-000002" {
		t.Fatalf("divergência localizada em turno=%d step=%q, esperava turno=2 step=step-000002",
			res.Divergence.Turn, res.Divergence.StepID)
	}
	if res.Divergence.ExpectedHash != tamperedHash {
		t.Fatalf("ExpectedHash = %q, esperava o hash adulterado do turno 2", res.Divergence.ExpectedHash)
	}
	// O turno 1 coincidiu (Matched) e o turno 2 foi anexado divergente (não Matched).
	if len(res.Steps) != 2 {
		t.Fatalf("esperava 2 turnos anexados (1 coincidido + 2 divergente), obtive %d", len(res.Steps))
	}
	if !res.Steps[0].Matched || res.Steps[0].Turn != 1 {
		t.Fatalf("turno 1 devia estar coincidido: %+v", res.Steps[0])
	}
	if res.Steps[1].Matched || res.Steps[1].Turn != 2 {
		t.Fatalf("turno 2 devia estar divergente: %+v", res.Steps[1])
	}
	// Fidelidade PARCIAL: 1 coincidido de 2 verificados.
	if res.Fidelity != 0.5 {
		t.Fatalf("fidelidade = %v, esperava 0.5 (matched=1/verified=2)", res.Fidelity)
	}
}

// TestReplayDetectsModelDrift prova que o drift de modelo/seed — INVISÍVEL ao
// prompt_hash (o modelo não entra nos bytes materializados) — é detectado por
// comparação explícita do manifesto contra a config de modelo re-fornecida. Sem
// esta comparação, uma troca de seed/model_id passaria como fidelidade 1.0.
func TestReplayDetectsModelDrift(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *TrajectorySpec)
	}{
		{
			name:   "seed alterado",
			mutate: func(s *TrajectorySpec) { s.Model.Seed = 999 },
		},
		{
			name:   "model_id alterado",
			mutate: func(s *TrajectorySpec) { s.Model.ModelID = "claude-outro-modelo" },
		},
		{
			name: "params alterados",
			mutate: func(s *TrajectorySpec) {
				s.Model.Params = map[string]string{"temperature": "0.7"}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			or := runOriginal(t, "run_model_"+sanitize(tc.name))
			e := mustEngine(t, or)
			spec := or.spec
			tc.mutate(&spec)

			res, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: spec})
			if err != nil {
				t.Fatalf("Replay: %v", err)
			}
			if res.Divergence == nil {
				t.Fatalf("esperava divergência de modelo (invisível ao prompt_hash)")
			}
			if res.Divergence.Reason != "model" {
				t.Fatalf("razão = %q, esperava model", res.Divergence.Reason)
			}
			// A divergência de modelo surge já no turno 1 (o manifesto pina o modelo em
			// cada turno) — o prompt_hash coincidiu, logo NÃO é prompt_hash.
			if res.Divergence.Turn != 1 || res.Divergence.StepID != "step-000001" {
				t.Fatalf("divergência de modelo devia localizar-se no turno 1: %+v", res.Divergence)
			}
			if res.Fidelity == 1.0 {
				t.Fatalf("fidelidade não devia ser 1.0 num drift de modelo")
			}
		})
	}
}

// TestReplayDetectsAssemblyVersionDrift prova que uma subida da versão do assembler
// sem mexer nos bytes do prompt (invisível ao prompt_hash) é detectada.
func TestReplayDetectsAssemblyVersionDrift(t *testing.T) {
	or := runOriginal(t, "run_assembly_drift")
	e := mustEngine(t, or)
	spec := or.spec
	spec.AssemblyVersion = "2.0.0" // gravado foi agentruntime.AssemblyVersion ("1.0.0")

	res, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: spec})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if res.Divergence == nil || res.Divergence.Reason != "assembly_version" {
		t.Fatalf("esperava divergência de assembly_version, obtive %+v", res.Divergence)
	}
	if res.Divergence.Turn != 1 {
		t.Fatalf("divergência de assembly devia localizar-se no turno 1, obtive %d", res.Divergence.Turn)
	}
	if res.Divergence.ExpectedHash != agentruntime.AssemblyVersion || res.Divergence.ActualHash != "2.0.0" {
		t.Fatalf("assembly esperado/actual errados: %+v", res.Divergence)
	}
}

// ---------------------------------------------------------------------------
// Teste 5 — Resume: replay a partir de step_id intermédio → mesmo estado.
// ---------------------------------------------------------------------------

func TestReplayResumeFromStepProducesSameState(t *testing.T) {
	or := runOriginal(t, "run_resume")
	e := mustEngine(t, or)

	full, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("Replay completo: %v", err)
	}
	// Estado do run no início do turno 2 (o IncomingStateHash do 2º turno do full).
	stateAtTurn2 := full.Steps[1].IncomingStateHash

	resumed, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec, FromStepID: "step-000002"})
	if err != nil {
		t.Fatalf("Replay resume: %v", err)
	}
	if resumed.ResumedFromStepID != "step-000002" {
		t.Fatalf("resume mal registado: %q", resumed.ResumedFromStepID)
	}
	// O segmento resumido começa no turno 2 (turno 1 foi dobrado, não verificado).
	if len(resumed.Steps) != 2 {
		t.Fatalf("esperava 2 turnos no segmento resumido (2 e 3), obtive %d", len(resumed.Steps))
	}
	if resumed.Steps[0].Turn != 2 {
		t.Fatalf("segmento resumido devia começar no turno 2, começou no %d", resumed.Steps[0].Turn)
	}
	// MESMO estado no ponto de retoma: o IncomingStateHash do primeiro passo resumido
	// coincide com o estado que o replay completo tinha nesse mesmo ponto.
	if resumed.Steps[0].IncomingStateHash != stateAtTurn2 {
		t.Fatalf("estado no resume diverge do completo: %q != %q", resumed.Steps[0].IncomingStateHash, stateAtTurn2)
	}
	// MESMO estado final e mesmo desfecho.
	if resumed.FinalStateHash != full.FinalStateHash {
		t.Fatalf("estado final diverge entre completo e resume: %q != %q", resumed.FinalStateHash, full.FinalStateHash)
	}
	if resumed.FinalText != full.FinalText || !resumed.Terminated {
		t.Fatalf("desfecho do resume diverge: %q term=%v", resumed.FinalText, resumed.Terminated)
	}
	if resumed.Fidelity != 1.0 {
		t.Fatalf("fidelidade do resume = %v, esperava 1.0", resumed.Fidelity)
	}
}

func TestReplayResumeUnknownStep(t *testing.T) {
	or := runOriginal(t, "run_resume_unknown")
	e := mustEngine(t, or)
	_, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec, FromStepID: "step-999999"})
	if !errors.Is(err, ErrStepNotFound) {
		t.Fatalf("esperava ErrStepNotFound, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// Observabilidade — marcador de replay/eval ligado ao trace original (ADR-010).
// ---------------------------------------------------------------------------

func TestReplayEmitsEvalMarker(t *testing.T) {
	or := runOriginal(t, "run_marker")
	tr := &agentruntime.RecordingTracer{}
	e := mustEngine(t, or, WithTracer(tr))
	res, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	spans := tr.SpansByOperation(OpReplay)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span replay, obtive %d", len(spans))
	}
	sp := spans[0]
	if sp.Attributes[agentruntime.AttrRunID] != or.goal.RunID {
		t.Fatalf("span replay não ligado ao run original: %v", sp.Attributes[agentruntime.AttrRunID])
	}
	if sp.Attributes[AttrEvalResult] != "pass" {
		t.Fatalf("eval result = %v, esperava pass", sp.Attributes[AttrEvalResult])
	}
	if sp.Attributes[AttrReplayFidelity] != res.Fidelity {
		t.Fatalf("fidelidade no span = %v, esperava %v", sp.Attributes[AttrReplayFidelity], res.Fidelity)
	}
	if diverged, _ := sp.Attributes[AttrReplayDiverged].(bool); diverged {
		t.Fatalf("aos.replay.diverged devia ser false num replay fiel")
	}
	if !sp.Ended {
		t.Fatalf("span replay não foi fechado")
	}
}

func TestReplayMarkerOnDivergence(t *testing.T) {
	or := runOriginal(t, "run_marker_div")
	tr := &agentruntime.RecordingTracer{}
	e := mustEngine(t, or, WithTracer(tr))
	spec := or.spec
	spec.System = "evoluído"
	if _, err := e.Replay(context.Background(), or.goal.RunID, Options{Spec: spec}); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	sp := tr.SpansByOperation(OpReplay)[0]
	if sp.Attributes[AttrEvalResult] != "fail" {
		t.Fatalf("eval result = %v, esperava fail", sp.Attributes[AttrEvalResult])
	}
	if diverged, _ := sp.Attributes[AttrReplayDiverged].(bool); !diverged {
		t.Fatalf("aos.replay.diverged devia ser true numa divergência")
	}
}

// ---------------------------------------------------------------------------
// Erros de construção / trajectória.
// ---------------------------------------------------------------------------

func TestReplayErrors(t *testing.T) {
	or := runOriginal(t, "run_errs")
	e := mustEngine(t, or)

	if _, err := e.Replay(context.Background(), "", Options{Spec: or.spec}); !errors.Is(err, ErrEmptyRunID) {
		t.Fatalf("esperava ErrEmptyRunID, obtive %v", err)
	}
	if _, err := e.Replay(context.Background(), "run_inexistente", Options{Spec: or.spec}); !errors.Is(err, ErrNoTrajectory) {
		t.Fatalf("esperava ErrNoTrajectory, obtive %v", err)
	}
	if _, err := NewEngine(nil); !errors.Is(err, ErrNilStore) {
		t.Fatalf("esperava ErrNilStore, obtive %v", err)
	}
}

// badStepIdentity deriva step_ids diferentes dos do loop (para forçar divergência
// de sequência de passos).
type badStepIdentity struct{}

func (badStepIdentity) StepID(_ string, turn int) string { return "X" + itoa(turn) }

// manifestMutatingReader embrulha um EventReader e reescreve o manifesto dos eventos
// "turn.recorded" ANTES de os entregar ao motor. Usa-se para simular uma divergência
// localizada num turno específico (adulterar o prompt_hash gravado de um turno) sem
// tocar nos restantes — o único acesso continua a ser Read (zero-efeitos preservado).
type manifestMutatingReader struct {
	inner  EventReader
	mutate func(turn int, m *agentruntime.Manifest)
}

func (r *manifestMutatingReader) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	events, err := r.inner.Read(ctx, streamID, fromSeq)
	if err != nil {
		return nil, err
	}
	out := make([]eventstore.Event, len(events))
	copy(out, events)
	for i := range out {
		if out[i].Type != agentruntime.EventTypeTurnRecorded {
			continue
		}
		var p turnRecordedPayload
		if err := json.Unmarshal(out[i].Payload, &p); err != nil {
			return nil, err
		}
		r.mutate(p.Turn, &p.Manifest)
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		out[i].Payload = raw
	}
	return out, nil
}

// TestReplayResumeFromDurableCursor prova a INTEGRAÇÃO de formato entre o cursor de
// AOS-015 e o resume-from-step do replay: um durable.Resumer, sobre um checkpoint de
// fronteira de turno, produz um NextStepID que é passado TAL-E-QUAL como
// Options.FromStepID do ReplayEngine — e o replay retoma no turno correcto. Fecha o
// "alinhamento por convenção" com um teste que liga as duas peças por código (o
// StepSequencer canónico que o Resumer usa == o formato que o replay casa).
func TestReplayResumeFromDurableCursor(t *testing.T) {
	or := runOriginal(t, "run_durable_cursor")
	e := mustEngine(t, or)
	ctx := context.Background()

	// Escreve um checkpoint de fronteira de turno (turno 1 VERIFIED) com o checkpointer
	// REAL de AOS-015 e reconstrói o cursor de retoma com o Resumer REAL.
	cpStore, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = cpStore.Close() })
	seq := durable.NewStepSequencer()
	cpr, err := durable.NewCheckpointer(cpStore)
	if err != nil {
		t.Fatalf("NewCheckpointer: %v", err)
	}
	if err := cpr.Checkpoint(ctx, agentruntime.Checkpoint{
		RunID:  or.goal.RunID,
		StepID: seq.StepID(or.goal.RunID, 1),
		Turn:   1,
		Phase:  agentruntime.PhaseVerified,
	}); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	resumer, err := durable.NewResumer(cpStore)
	if err != nil {
		t.Fatalf("NewResumer: %v", err)
	}
	rp, err := resumer.Resume(ctx, or.goal.RunID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// O NextStepID do durable é o formato canónico que o replay casa: turno 2.
	if rp.NextStepID != "step-000002" || rp.NextStepID != seq.StepID(or.goal.RunID, 2) {
		t.Fatalf("NextStepID do durable = %q, esperava step-000002 (formato canónico)", rp.NextStepID)
	}

	// Passa o NextStepID do durable como FromStepID do replay — sem qualquer tradução.
	full, err := e.Replay(ctx, or.goal.RunID, Options{Spec: or.spec})
	if err != nil {
		t.Fatalf("Replay completo: %v", err)
	}
	resumed, err := e.Replay(ctx, or.goal.RunID, Options{Spec: or.spec, FromStepID: rp.NextStepID})
	if err != nil {
		t.Fatalf("Replay resume: %v", err)
	}
	if resumed.ResumedFromStepID != rp.NextStepID {
		t.Fatalf("resume mal registado: %q", resumed.ResumedFromStepID)
	}
	if len(resumed.Steps) != 2 || resumed.Steps[0].Turn != 2 {
		t.Fatalf("segmento resumido devia começar no turno 2 com 2 passos: %+v", resumed.Steps)
	}
	// Mesmo estado final e desfecho que o replay completo — a retoma via cursor do
	// durable produz o MESMO estado que o re-fold integral.
	if resumed.FinalStateHash != full.FinalStateHash {
		t.Fatalf("estado final diverge entre completo e resume-via-cursor: %q != %q", resumed.FinalStateHash, full.FinalStateHash)
	}
	if resumed.Fidelity != 1.0 {
		t.Fatalf("fidelidade do resume = %v, esperava 1.0", resumed.Fidelity)
	}
}
