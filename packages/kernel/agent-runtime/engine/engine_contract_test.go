package engine_test

import (
	"context"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/activity"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/engine"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// ===========================================================================
// AOS-022 (fase feature) — teste de CONTRATO da porta engine.Engine.
//
// Prova três coisas, todas assentes numa única PORTA agnóstica ao backend:
//  1. PoC / cenário de referência: run multi-passo com CRASH e RETOMA corre sobre
//     a interface Engine; recolhe fidelidade de replay e efeitos duplicados.
//  2. Contrato: o adaptador de referência passa a suíte de IDEMPOTÊNCIA (AOS-014,
//     0 efeitos duplicados) e de REPLAY (AOS-016, fidelidade 100%).
//  3. Isolamento (Princípio 8): trocar o backend (adaptador de referência ↔ um
//     FAKE/stub Engine) NÃO altera a API/uso do RT — o MESMO driver corre sobre ambos.
// ===========================================================================

// ---------------------------------------------------------------------------
// O "código de RT": um driver AGNÓSTICO AO BACKEND, escrito contra engine.Engine.
//
// É deliberadamente O ÚNICO código que exercita a porta, e corre IDÊNTICO sobre o
// adaptador de referência e sobre o stub. Se compilasse/funcionasse só sobre um dos
// backends, o isolamento por contrato estaria quebrado. Recebe a porta e o contador
// de efeitos externos observável (no backend próprio é a tool registada no RM; no
// stub é o seu contador interno) — nada específico do backend.
// ---------------------------------------------------------------------------

// rtObservation recolhe os desfechos OBSERVÁVEIS do cenário para asserção uniforme.
type rtObservation struct {
	pass1Out     []string
	pass2Out     []string
	effectsPass1 int // efeitos externos no 1.º despacho de cada passo
	effectsPass2 int // efeitos externos na RE-execução pós-retoma (tem de ser 0)
	resume       durable.ResumePoint
	replay       replay.ReplayResult
}

// nSteps é o nº de activities (efeitos externos) do turno de referência.
const nSteps = 3

// driveRTScenario é o CENÁRIO DE REFERÊNCIA como o RT o veria: um turno com N
// efeitos externos, cada um confirmado por checkpoint; um CRASH; a RETOMA a partir
// do cursor durável; e a RE-execução idempotente dos passos (rede de segurança do
// ledger). No fim, um Replay (RCA/eval) sobre a mesma porta. NÃO conhece o backend.
func driveRTScenario(t *testing.T, name string, eng engine.Engine, effects *int, runID string, ropts replay.Options) rtObservation {
	t.Helper()
	ctx := context.Background()
	seq := durable.NewStepSequencer()
	turnStep := seq.StepID(runID, 1)
	steps := referenceSteps(runID, seq)

	base := *effects
	var obs rtObservation

	// --- Passo 1: despacha cada activity e confirma o progresso por checkpoint. ---
	for i, act := range steps {
		res, err := eng.Dispatch(ctx, act)
		if err != nil {
			t.Fatalf("[%s] Dispatch pass1 #%d: %v", name, i+1, err)
		}
		if res.Deduplicated {
			t.Fatalf("[%s] pass1 #%d não devia ser dedup (efeito novo esperado)", name, i+1)
		}
		if !res.Output.IsUntrusted() {
			t.Fatalf("[%s] pass1 #%d: output devia vir untrusted, taint=%q", name, i+1, res.Output.Taint)
		}
		obs.pass1Out = append(obs.pass1Out, string(res.Output.Value))

		cp := agentruntime.Checkpoint{
			RunID: runID, Turn: 1, Phase: agentruntime.PhaseDispatched,
			StepID: turnStep, ConfirmedStepID: act.StepID,
			PendingActivities: stepIDs(steps[i+1:]),
		}
		if err := eng.Checkpoint(ctx, cp); err != nil {
			t.Fatalf("[%s] Checkpoint dispatched #%d: %v", name, i+1, err)
		}
	}
	// Fase verified: o turno ficou completo.
	if err := eng.Checkpoint(ctx, agentruntime.Checkpoint{
		RunID: runID, Turn: 1, Phase: agentruntime.PhaseVerified,
		StepID: turnStep, ConfirmedStepID: turnStep,
	}); err != nil {
		t.Fatalf("[%s] Checkpoint verified: %v", name, err)
	}
	obs.effectsPass1 = *effects - base

	// --- RETOMA DE CURSOR (mesmo worker): relê o cursor do log durável e re-deriva o
	// ponto de retoma. NÃO é o crash durável — o ledger in-memory continua vivo, pelo
	// que a dedup do pass2 é servida pelo fast-path quente. O CRASH DURÁVEL real
	// (worker NOVO, ledger vazio + Rebuild a servir a dedup) é o TESTE 2. ---
	rp, err := eng.Resume(ctx, runID)
	if err != nil {
		t.Fatalf("[%s] Resume: %v", name, err)
	}
	obs.resume = rp

	// --- Passo 2: RE-despacha TODAS as activities. Idempotência ⇒ 0 efeitos novos. ---
	afterPass1 := *effects
	for i, act := range steps {
		res, err := eng.Dispatch(ctx, act)
		if err != nil {
			t.Fatalf("[%s] Dispatch pass2 #%d: %v", name, i+1, err)
		}
		if !res.Deduplicated {
			t.Fatalf("[%s] pass2 #%d devia ser dedup (already-applied)", name, i+1)
		}
		obs.pass2Out = append(obs.pass2Out, string(res.Output.Value))
	}
	obs.effectsPass2 = *effects - afterPass1

	// --- Replay (RCA/eval) sobre a porta. ---
	rr, err := eng.Replay(ctx, runID, ropts)
	if err != nil {
		t.Fatalf("[%s] Replay: %v", name, err)
	}
	obs.replay = rr
	return obs
}

// referenceSteps constrói as N activities do turno de referência (sub-passos 1..N).
// O ToolID "echo" está registado no RM do backend próprio; o stub ignora-o.
func referenceSteps(runID string, seq *durable.StepSequencer) []activity.Activity {
	steps := make([]activity.Activity, 0, nSteps)
	for i := 1; i <= nSteps; i++ {
		steps = append(steps, activity.Activity{
			RunID:      runID,
			StepID:     seq.SubStepID(runID, 1, i),
			ToolID:     "echo",
			Capability: "cap:echo",
			Input:      []byte("payload"),
		})
	}
	return steps
}

func stepIDs(acts []activity.Activity) []string {
	if len(acts) == 0 {
		return nil
	}
	out := make([]string, 0, len(acts))
	for _, a := range acts {
		out = append(out, a.StepID)
	}
	return out
}

// assertContract verifica as invariantes que QUALQUER backend conforme tem de honrar
// (as mesmas para o adaptador de referência e para o stub).
func assertContract(t *testing.T, name string, obs rtObservation, runID string) {
	t.Helper()
	// Idempotência (AOS-014): N efeitos no pass1, ZERO na re-execução pós-retoma.
	if obs.effectsPass1 != nSteps {
		t.Fatalf("[%s] esperava %d efeitos no pass1, obtive %d", name, nSteps, obs.effectsPass1)
	}
	if obs.effectsPass2 != 0 {
		t.Fatalf("[%s] esperava 0 efeitos duplicados na retoma, obtive %d", name, obs.effectsPass2)
	}
	// Resultado registado devolvido idêntico entre a execução e a re-execução.
	if len(obs.pass2Out) != len(obs.pass1Out) {
		t.Fatalf("[%s] contagem de outputs difere entre passes", name)
	}
	for i := range obs.pass1Out {
		if obs.pass1Out[i] != obs.pass2Out[i] {
			t.Fatalf("[%s] output do passo %d diverge entre execução (%q) e retoma (%q)",
				name, i+1, obs.pass1Out[i], obs.pass2Out[i])
		}
	}
	// Resume coerente: houve progresso confirmado ⇒ não FromScratch, do run certo.
	if obs.resume.FromScratch {
		t.Fatalf("[%s] resume não devia ser FromScratch após progresso confirmado", name)
	}
	if obs.resume.RunID != runID {
		t.Fatalf("[%s] resume.RunID = %q, esperava %q", name, obs.resume.RunID, runID)
	}
	if obs.resume.NextTurn < 1 {
		t.Fatalf("[%s] resume.NextTurn inválido: %d", name, obs.resume.NextTurn)
	}
	// NextStepID NOMEIA o ponto de retoma — o campo central do Resume (doc.go). O
	// cenário termina numa fronteira de turno (verified), pelo que qualquer backend
	// conforme tem de nomear o step_id do próximo turno com a derivação canónica. Um
	// stub que devolvesse NextStepID vazio ou errado tem de FALHAR aqui.
	wantNextStep := durable.NewStepSequencer().StepID(runID, obs.resume.NextTurn)
	if obs.resume.NextStepID == "" {
		t.Fatalf("[%s] resume.NextStepID vazio: não nomeia o próximo passo de retoma", name)
	}
	if obs.resume.NextStepID != wantNextStep {
		t.Fatalf("[%s] resume.NextStepID = %q, esperava %q (step_id do turno %d)",
			name, obs.resume.NextStepID, wantNextStep, obs.resume.NextTurn)
	}
	// Replay fiel: fidelidade 100%, sem divergência.
	if obs.replay.Fidelity != 1.0 {
		t.Fatalf("[%s] fidelidade de replay = %v, esperava 1.0", name, obs.replay.Fidelity)
	}
	if obs.replay.Divergence != nil {
		t.Fatalf("[%s] replay não devia divergir: %+v", name, obs.replay.Divergence)
	}
}

// ---------------------------------------------------------------------------
// Harness do backend PRÓPRIO (adaptador de referência) — Event Store + RM reais.
// ---------------------------------------------------------------------------

// echoTool é o efeito externo instrumentado do backend próprio: só sobe sob permit
// + despacho do Reference Monitor (a ÚNICA via). É o análogo do contador do stub.
func newRealRM(t testing.TB, effects *int, store *eventstore.Store) *referencemonitor.Monitor {
	t.Helper()
	sink := referencemonitor.NewEventStoreSink(store)
	rm := referencemonitor.New(referencemonitor.WithEventSink(sink))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		*effects++
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		t.Fatalf("Register(echo): %v", err)
	}
	return rm
}

func newStore(t testing.TB) *eventstore.Store {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// seedTrajectory corre o loop REAL de AOS-013 (com capturer de AOS-016) para semear
// turn.recorded + replay.captured no store — o material de que o Replay precisa.
// Usa o MESMO RM (contador de efeitos partilhado); as execuções do loop ocorrem
// ANTES de o driver medir o seu delta, pelo que não contaminam a contagem do cenário.
func seedTrajectory(t *testing.T, store *eventstore.Store, rm *referencemonitor.Monitor, runID string) replay.Options {
	t.Helper()
	recorder := agentruntime.NewTurnRecorder(store)
	capturer, err := replay.NewCapturer(store, replay.WithClock(func() time.Time {
		return time.Unix(1_700_000_000, 0).UTC()
	}))
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	callN := 0
	model := agentruntime.ModelClientFunc(func(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
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
	goal := seedGoal(runID)
	rt := agentruntime.New(model, rm, recorder, agentruntime.WithCapturer(capturer))
	if _, err := rt.Run(context.Background(), goal); err != nil {
		t.Fatalf("seed loop Run: %v", err)
	}
	return replay.Options{Spec: replay.TrajectorySpec{
		System:          goal.System,
		Tools:           goal.Tools,
		Objective:       goal.Objective,
		MemoryContext:   goal.MemoryContext,
		Model:           goal.Model,
		AssemblyVersion: agentruntime.AssemblyVersion,
	}}
}

func seedGoal(runID string) agentruntime.Goal {
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

// ===========================================================================
// TESTE 1 — Isolamento por contrato (Princípio 8): o MESMO driver corre sobre o
// adaptador de referência E sobre o stub, com asserções IDÊNTICAS.
// ===========================================================================

func TestContract_BackendSwap_RTUnchanged(t *testing.T) {
	t.Parallel()

	// Backend PRÓPRIO (adaptador de referência sobre Event Store + RM reais).
	realEffects := 0
	realStore := newStore(t)
	realRM := newRealRM(t, &realEffects, realStore)
	const realRun = "run_contract_own"
	realOpts := seedTrajectory(t, realStore, realRM, realRun)
	realEng, err := engine.NewOwnContractEngine(realStore, realRM,
		engine.WithProducer(eventstore.Producer{NHIID: "nhi:agent-1"}))
	if err != nil {
		t.Fatalf("NewOwnContractEngine: %v", err)
	}

	// Backend ALTERNATIVO (stub in-memory).
	fakeEffects := 0
	fakeEng := newFakeEngine(&fakeEffects)

	cases := []struct {
		name    string
		eng     engine.Engine
		effects *int
		runID   string
		ropts   replay.Options
	}{
		{"adaptador-referencia", realEng, &realEffects, realRun, realOpts},
		{"stub-fake-engine", fakeEng, &fakeEffects, "run_contract_fake", replay.Options{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// EXACTAMENTE o mesmo driver — nenhuma ramificação por backend.
			obs := driveRTScenario(t, tc.name, tc.eng, tc.effects, tc.runID, tc.ropts)
			assertContract(t, tc.name, obs, tc.runID)
		})
	}
}

// ===========================================================================
// TESTE 2 — Cenário de referência (PoC): CRASH + RETOMA com WORKER NOVO sobre o
// adaptador de referência, provando idempotência DURÁVEL (não só o fast-path
// in-memory) e replay 100% com ZERO efeitos externos.
// ===========================================================================

func TestReferenceScenario_CrashResume_DurableWorker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID = "run_ref_crash"
	effects := 0
	store := newStore(t)
	rm := newRealRM(t, &effects, store)
	ropts := seedTrajectory(t, store, rm, runID)

	seq := durable.NewStepSequencer()
	steps := referenceSteps(runID, seq)
	turnStep := seq.StepID(runID, 1)

	// --- Worker A: despacha e confirma (efeitos correm exactamente 1x cada). ---
	engA, err := engine.NewOwnContractEngine(store, rm)
	if err != nil {
		t.Fatalf("engine A: %v", err)
	}
	beforeDispatch := effects
	for i, act := range steps {
		if _, err := engA.Dispatch(ctx, act); err != nil {
			t.Fatalf("A Dispatch #%d: %v", i+1, err)
		}
		if err := engA.Checkpoint(ctx, agentruntime.Checkpoint{
			RunID: runID, Turn: 1, Phase: agentruntime.PhaseDispatched,
			StepID: turnStep, ConfirmedStepID: act.StepID, PendingActivities: stepIDs(steps[i+1:]),
		}); err != nil {
			t.Fatalf("A Checkpoint #%d: %v", i+1, err)
		}
	}
	if got := effects - beforeDispatch; got != nSteps {
		t.Fatalf("worker A: esperava %d efeitos, obtive %d", nSteps, got)
	}

	// --- CRASH. Worker B é um PROCESSO NOVO: ledger vazio reconstruído do log. ---
	ledgerB, err := durable.NewStepLedger(store)
	if err != nil {
		t.Fatalf("ledger B: %v", err)
	}
	if err := ledgerB.Rebuild(ctx, runID); err != nil { // recupera o estado durável
		t.Fatalf("ledger B Rebuild: %v", err)
	}
	engB, err := engine.NewOwnContractEngine(store, rm, engine.WithLedger(ledgerB))
	if err != nil {
		t.Fatalf("engine B: %v", err)
	}

	// Resume reconstrói o cursor a partir dos checkpoints (sobrevive à morte de A).
	rp, err := engB.Resume(ctx, runID)
	if err != nil {
		t.Fatalf("B Resume: %v", err)
	}
	if rp.FromScratch {
		t.Fatalf("B Resume não devia ser FromScratch após progresso de A")
	}

	// Re-despacha TODAS as activities no worker novo: dedup pelo ledger reconstruído,
	// ZERO efeitos duplicados (a garantia central de AOS-014 sob failover).
	beforeReexec := effects
	for i, act := range steps {
		res, err := engB.Dispatch(ctx, act)
		if err != nil {
			t.Fatalf("B Dispatch #%d: %v", i+1, err)
		}
		if !res.Deduplicated {
			t.Fatalf("B pass2 #%d devia dedup (ledger reconstruído do log)", i+1)
		}
	}
	if dup := effects - beforeReexec; dup != 0 {
		t.Fatalf("efeitos DUPLICADOS na retoma com worker novo: %d (esperava 0)", dup)
	}

	// --- Replay determinístico (AOS-016): fidelidade 100% e ZERO efeitos externos. ---
	beforeReplay := effects
	rr, err := engB.Replay(ctx, runID, ropts)
	if err != nil {
		t.Fatalf("B Replay: %v", err)
	}
	if rr.Fidelity != 1.0 {
		t.Fatalf("fidelidade de replay = %v, esperava 1.0", rr.Fidelity)
	}
	if rr.Divergence != nil {
		t.Fatalf("replay fiel não devia divergir: %+v", rr.Divergence)
	}
	if !rr.Terminated {
		t.Fatalf("replay devia reconstruir uma trajectória terminada")
	}
	if delta := effects - beforeReplay; delta != 0 {
		t.Fatalf("replay emitiu %d efeitos externos (esperava 0)", delta)
	}
}

// ===========================================================================
// TESTE 3 — Replay LOCALIZA a divergência (evolução de código): mutar o Spec
// (system prompt) faz o prompt_hash re-materializado divergir do gravado.
// ===========================================================================

func TestReferenceAdapter_ReplayDivergenceLocalized(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const runID = "run_ref_divergence"
	effects := 0
	store := newStore(t)
	rm := newRealRM(t, &effects, store)
	ropts := seedTrajectory(t, store, rm, runID)

	eng, err := engine.NewOwnContractEngine(store, rm)
	if err != nil {
		t.Fatalf("NewOwnContractEngine: %v", err)
	}

	// Simula "evolução de código": muda o system prompt re-fornecido.
	ropts.Spec.System = ropts.Spec.System + " (versão evoluída)"
	rr, err := eng.Replay(ctx, runID, ropts)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if rr.Divergence == nil {
		t.Fatalf("esperava divergência localizada por prompt evoluído")
	}
	if rr.Divergence.Reason != "prompt_hash" {
		t.Fatalf("razão de divergência = %q, esperava prompt_hash", rr.Divergence.Reason)
	}
	if rr.Fidelity == 1.0 {
		t.Fatalf("fidelidade não devia ser 1.0 sob divergência")
	}
}

// ===========================================================================
// TESTE 4 — Construção da porta e validações do adaptador de referência.
// ===========================================================================

func TestOwnContractEngine_Construction(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	effects := 0
	rm := newRealRM(t, &effects, store)

	// Nil store / nil mediator ⇒ fail-closed.
	if _, err := engine.NewOwnContractEngine(nil, rm); err != engine.ErrNilStore {
		t.Fatalf("nil store: err = %v, esperava ErrNilStore", err)
	}
	if _, err := engine.NewOwnContractEngine(store, nil); err != engine.ErrNilMediator {
		t.Fatalf("nil mediator: err = %v, esperava ErrNilMediator", err)
	}

	eng, err := engine.NewOwnContractEngine(store, rm)
	if err != nil {
		t.Fatalf("NewOwnContractEngine: %v", err)
	}
	if eng.Mode() != activity.ModeNormal {
		t.Fatalf("Mode() = %v, esperava ModeNormal", eng.Mode())
	}
	if eng.Ledger() == nil {
		t.Fatalf("Ledger() devia expor o step-ledger composto")
	}
}

// ===========================================================================
// TESTE 4b — Opções de composição do adaptador de referência: tracer, producer,
// step identity e modo sensível são cablados nas peças compostas sem alterar a porta.
// ===========================================================================

func TestOwnContractEngine_Options(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newStore(t)
	effects := 0
	rm := newRealRM(t, &effects, store)
	seq := durable.NewStepSequencer()

	// tracer=nil cai no guard do default; producer + stepIdentity são propagados às
	// peças (ledger/checkpointer + resumer). O engine continua a satisfazer a porta.
	eng, err := engine.NewOwnContractEngine(store, rm,
		engine.WithTracer(nil),
		engine.WithProducer(eventstore.Producer{NHIID: "nhi:agent-1"}),
		engine.WithStepIdentity(seq),
	)
	if err != nil {
		t.Fatalf("NewOwnContractEngine (opções): %v", err)
	}
	steps := referenceSteps("run_opts", seq)
	if _, err := eng.Dispatch(ctx, steps[0]); err != nil {
		t.Fatalf("Dispatch com opções: %v", err)
	}
	if _, err := eng.Resume(ctx, "run_opts"); err != nil {
		t.Fatalf("Resume com step identity custom: %v", err)
	}

	// Modo sensível: o ledger recusa memorizar Payload de resultado em claro. Basta
	// construir para exercitar a opção e o ramo; um Dispatch com output em claro
	// falharia fail-closed (a guarda de segredos do ledger, AOS-014).
	engS, err := engine.NewOwnContractEngine(newStore(t), rm, engine.WithSensitiveResults())
	if err != nil {
		t.Fatalf("NewOwnContractEngine (sensível): %v", err)
	}
	if engS.Mode() != activity.ModeNormal {
		t.Fatalf("engine sensível: Mode inesperado")
	}
}

// ===========================================================================
// TESTE 5 — Um run SEM progresso confirmado retoma FromScratch (fronteira zero).
// A mesma semântica observável no adaptador de referência e no stub.
// ===========================================================================

func TestResume_FromScratchWithoutProgress(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	realEffects := 0
	store := newStore(t)
	rm := newRealRM(t, &realEffects, store)
	realEng, err := engine.NewOwnContractEngine(store, rm)
	if err != nil {
		t.Fatalf("NewOwnContractEngine: %v", err)
	}
	fakeEng := newFakeEngine(new(int))

	backends := []struct {
		name string
		eng  engine.Engine
	}{
		{"adaptador-referencia", realEng},
		{"stub-fake-engine", fakeEng},
	}
	for _, b := range backends {
		rp, err := b.eng.Resume(ctx, "run_sem_progresso")
		if err != nil {
			t.Fatalf("[%s] Resume: %v", b.name, err)
		}
		if !rp.FromScratch || rp.NextTurn != 1 {
			t.Fatalf("[%s] esperava FromScratch turno 1, obtive %+v", b.name, rp)
		}
	}
}
