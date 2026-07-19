package dr_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	"github.com/aos-ref/kernel/agent-runtime/worker"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/backup"
	"github.com/aos-ref/platform/dr"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ---------------------------------------------------------------------------
// Harness do game day end-to-end. Monta o pipeline COMPLETO com peças REAIS:
// execução original (RT loop AOS-013 que CAPTURA não-determinismo + worker AOS-099
// durável) -> WORM tamper-evident -> export do backup AOS-101 -> DESASTRE (Store novo)
// -> RestoreTo -> VerifyFromCheckpointAtHead -> Replay 100% -> retoma resume-from-step
// -> 0 efeitos duplicados -> RPO/RTO -> soberania.
// ---------------------------------------------------------------------------

const (
	drBoard  = "board-eu"
	drRegion = "eu-west"
	drRunID  = "run-dr-1"
	drSteps  = 3
	leaseTTL = 30 * time.Second
)

var baseTime = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

// manualClock é um relógio determinístico partilhado (expiração de lease controlada).
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func newManualClock() *manualClock { return &manualClock{t: baseTime} }

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// effectCounter conta as INVOCAÇÕES REAIS de uma tool (o efeito externo).
type effectCounter struct{ total int64 }

func (c *effectCounter) hit()     { atomic.AddInt64(&c.total, 1) }
func (c *effectCounter) n() int64 { return atomic.LoadInt64(&c.total) }

// sovStore constrói um Event Store com a fronteira de soberania do board (3 réplicas
// co-localizadas na região — o guard recusa cross-border por construção).
func sovStore(t *testing.T, region string) *eventstore.Store {
	t.Helper()
	s, err := eventstore.New(eventstore.WithReplicas(3), eventstore.WithSovereigntyBoard(drBoard, region))
	if err != nil {
		t.Fatalf("eventstore.New(%s): %v", region, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func fixed(at time.Time) func() time.Time { return func() time.Time { return at } }

// detRand devolve uma RandSource determinística para o cifrado do backup em testes.
func detRand() audit.RandSource {
	var n byte
	return func(p []byte) error {
		for i := range p {
			p[i] = n
			n++
		}
		return nil
	}
}

// sampleGoal é o goal da trajectória original (inputs determinísticos + captura).
func sampleGoal(runID string) agentruntime.Goal {
	return agentruntime.Goal{
		RunID: runID,
		Principal: referencemonitor.Principal{
			NHIID: "nhi:agent-1", AgentID: "agent-1", AgentClass: "researcher",
			Authority: []string{"cap:echo"},
		},
		Scope:         []string{"cap:echo"},
		Model:         agentruntime.ModelConfig{ModelID: "claude-opus-4-8", Params: map[string]string{"temperature": "0"}, Seed: 42},
		System:        "És um agente de investigação do AOS.",
		Tools:         []agentruntime.ToolSpec{{Name: "echo", Version: "0.9.0", Digest: "sha256:cc03"}},
		Objective:     "Faz echo do input.",
		MemoryContext: []byte("memoria-inicial"),
	}
}

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

// captureTrajectory corre o RT loop REAL (AOS-013) com o EventStoreCapturer ligado,
// produzindo turn.recorded (manifesto com prompt_hash) + replay.captured no stream do
// run — o material que o replay determinístico reproduz com fidelidade 100%. As
// mediações do RT loop vão para o Event Store (sink de referência); os efeitos ao vivo
// contam em trajHits (o replay NÃO os pode incrementar).
func captureTrajectory(t *testing.T, store *eventstore.Store, goal agentruntime.Goal, trajHits *effectCounter) {
	t.Helper()
	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		trajHits.hit()
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		t.Fatalf("Register(echo traj): %v", err)
	}
	recorder := agentruntime.NewTurnRecorder(store)
	capturer, err := replay.NewCapturer(store, replay.WithClock(fixed(baseTime)))
	if err != nil {
		t.Fatalf("NewCapturer: %v", err)
	}
	callN := 0
	model := agentruntime.ModelClientFunc(func(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		callN++
		switch callN {
		case 1:
			return agentruntime.ModelResponse{
				Text:      "primeiro: chamo echo",
				ToolCalls: []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("um")}},
				Usage:     agentruntime.Usage{InputTokens: 10, OutputTokens: 5},
			}, nil
		case 2:
			return agentruntime.ModelResponse{
				Text:      "segundo: chamo echo outra vez",
				ToolCalls: []agentruntime.ToolInvocation{{ToolID: "echo", Capability: "cap:echo", Input: []byte("dois")}},
				Usage:     agentruntime.Usage{InputTokens: 8, OutputTokens: 4},
			}, nil
		default:
			return agentruntime.ModelResponse{Text: "concluído", Final: true, Usage: agentruntime.Usage{InputTokens: 6, OutputTokens: 2}}, nil
		}
	})
	rt := agentruntime.New(model, rm, recorder, agentruntime.WithCapturer(capturer))
	res, err := rt.Run(context.Background(), goal)
	if err != nil {
		t.Fatalf("RT loop original: %v", err)
	}
	if !res.Terminated || res.Turns != 3 {
		t.Fatalf("trajectória original inesperada: %+v", res)
	}
}

// workerProc agrupa primitivos duráveis FRESCOS (um "processo worker") sobre um Store
// e um relógio partilhados, com o RM a selar as mediações no WORM tamper-evident.
type workerProc struct {
	lm     *durable.LeaseManager
	fenced *durable.FencedAppender
	ledger *durable.StepLedger
	resume *durable.Resumer
	cpr    *durable.EventStoreCheckpointer
	rm     *referencemonitor.Monitor
	seq    *durable.StepSequencer
}

func newWorkerProc(t *testing.T, store *eventstore.Store, clock *manualClock, worm audit.Store, effectHits *effectCounter, workerID string) *workerProc {
	t.Helper()
	lm, err := durable.NewLeaseManager(store, leaseTTL, durable.WithLeaseClock(clock), durable.WithWorkerID(workerID))
	if err != nil {
		t.Fatalf("NewLeaseManager: %v", err)
	}
	fenced, err := durable.NewFencedAppender(store, lm)
	if err != nil {
		t.Fatalf("NewFencedAppender: %v", err)
	}
	ledger, err := durable.NewStepLedger(store)
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	seq := durable.NewStepSequencer()
	resume, err := durable.NewResumer(store, durable.WithStepIdentity(seq))
	if err != nil {
		t.Fatalf("NewResumer: %v", err)
	}
	cpr, err := durable.NewCheckpointer(store)
	if err != nil {
		t.Fatalf("NewCheckpointer: %v", err)
	}
	// O sink de mediação do worker é o audit tamper-evident (WORM): cada decisão
	// permit sela um registo na hash-chain, verificada no DR antes de restabelecer.
	rm := referencemonitor.New(referencemonitor.WithEventSink(audit.NewMediationSink(worm)))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		effectHits.hit()
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		t.Fatalf("Register(echo effect): %v", err)
	}
	return &workerProc{lm: lm, fenced: fenced, ledger: ledger, resume: resume, cpr: cpr, rm: rm, seq: seq}
}

func (p *workerProc) worker(t *testing.T, cpr agentruntime.Checkpointer) *worker.Worker {
	t.Helper()
	if cpr == nil {
		cpr = p.cpr
	}
	w, err := worker.NewWorker(p.lm, p.fenced, p.ledger, p.resume, cpr, p.rm,
		worker.WithStepSequencer(p.seq),
		worker.WithTracer(otelgenai.NoopTracer{}),
		worker.WithHeartbeatInterval(time.Hour), // relógio manual: o heartbeat real não dispara
	)
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	return w
}

func effectPlan(runID string, n int) *worker.RunPlan {
	steps := make([]worker.Step, n)
	for i := range steps {
		steps[i] = worker.Step{
			Call: referencemonitor.Call{
				ToolID: "echo", Capability: "cap:echo",
				Input: []byte("payload"),
				Principal: referencemonitor.Principal{
					NHIID: "nhi:agent-1", AgentID: "agent-1", AgentClass: "researcher",
					DelegationChain: []referencemonitor.DelegationHop{{Sub: "human:alice", ActAs: "nhi:agent-1"}},
					Authority:       []string{"cap:echo"},
				},
			},
			CostMicroUSD: int64((i + 1) * 1000),
		}
	}
	return &worker.RunPlan{RunID: runID, Steps: steps}
}

// dropCheckpointer engole o checkpoint de um turno — simula o DESASTRE a cair entre o
// efeito (ledger aplicado) e o seu checkpoint, forçando a retoma a re-executar esse
// turno e a provar a dedup do ledger (0 efeitos duplicados).
type dropCheckpointer struct {
	inner    agentruntime.Checkpointer
	dropTurn int
}

func (d *dropCheckpointer) Checkpoint(ctx context.Context, cp agentruntime.Checkpoint) error {
	if cp.Turn == d.dropTurn {
		return nil
	}
	return d.inner.Checkpoint(ctx, cp)
}

// pipeline é o material da execução original pronto para o DR.
type pipeline struct {
	exporter *backup.Exporter
	restorer *backup.Restorer
	auditCP  audit.Checkpoint
	auditPub []byte
	worm     audit.Store
	wormHead uint64
	goal     agentruntime.Goal
	clock    *manualClock
	drStore  *eventstore.Store // preenchido pela factory durante a recuperação
}

// buildOriginal monta a execução original COMPLETA e exporta o backup, devolvendo o
// material para a recuperação. trajHits/effectHits contam os efeitos ao vivo do
// original (o replay/retoma não os podem incrementar).
func buildOriginal(t *testing.T, trajHits, effectHits *effectCounter) *pipeline {
	t.Helper()
	ctx := context.Background()
	src := sovStore(t, drRegion)
	clock := newManualClock()
	worm := audit.NewMemStore()

	// (1) Trajectória replayável: RT loop com captura de não-determinismo.
	goal := sampleGoal(drRunID)
	captureTrajectory(t, src, goal, trajHits)

	// (2) Execução durável: worker sela ledger+checkpoints+mediações(WORM). O
	// dropCheckpointer engole o checkpoint do último turno (desastre mid-run).
	procA := newWorkerProc(t, src, clock, worm, effectHits, "worker-A")
	wA := procA.worker(t, &dropCheckpointer{inner: procA.cpr, dropTurn: drSteps})
	outA, err := wA.Run(ctx, effectPlan(drRunID, drSteps))
	if err != nil {
		t.Fatalf("worker original Run: %v", err)
	}
	if !outA.Completed || outA.Executed != drSteps {
		t.Fatalf("worker original inesperado: %+v", outA)
	}
	if got := effectHits.n(); got != drSteps {
		t.Fatalf("efeitos originais = %d, quero %d", got, drSteps)
	}

	// (3) Sela um checkpoint assinado do WORM no head (raiz de frescura do DR).
	sig, err := ephemeralAuditSigner()
	if err != nil {
		t.Fatalf("audit signer: %v", err)
	}
	head, err := worm.Head(ctx, drRunID)
	if err != nil {
		t.Fatalf("WORM Head: %v", err)
	}
	if head != drSteps {
		t.Fatalf("WORM head = %d, quero %d registos de mediação", head, drSteps)
	}
	cp, err := sig.Seal(ctx, worm, drRunID, head)
	if err != nil {
		t.Fatalf("Seal WORM: %v", err)
	}

	// (4) Export do backup imutável (cifrado em repouso) na fronteira eu.
	dst := backup.NewInMemoryImmutableStore(drRegion)
	bsig := ephemeralBackupSigner(t)
	exp, err := backup.NewExporter(src, dst, bsig, backup.WithRandSource(detRand()), backup.WithClock(fixed(baseTime)))
	if err != nil {
		t.Fatalf("NewExporter: %v", err)
	}
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("Export: %v", err)
	}
	rst, err := backup.NewRestorer(dst, exp.Vault(), exp.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	return &pipeline{
		exporter: exp, restorer: rst,
		auditCP: cp, auditPub: sig.Public(), worm: worm, wormHead: head,
		goal: goal, clock: clock,
	}
}

// recovery monta a Recovery a partir do pipeline, com a função de retoma a operar
// sobre o Store restaurado (worker fresco; a dedup do ledger garante 0 duplicados).
func (p *pipeline) recovery(t *testing.T, resumeHits *effectCounter) dr.Recovery {
	return dr.Recovery{
		Board:        drBoard,
		RunID:        drRunID,
		Manifest:     p.exporter.Manifest(),
		Backup:       p.exporter.Checkpoint(),
		ExpectedHead: 0,
		Spec:         specFromGoal(p.goal),
		Audit: dr.AuditCheck{
			Store: p.worm, Public: p.auditPub, Checkpoint: p.auditCP,
			ExpectedHead: p.wormHead, To: p.wormHead,
		},
		Resume: func(ctx context.Context, drStore *eventstore.Store) (dr.ResumeEvidence, error) {
			// Processo worker NOVO sobre o log RESTAURADO. Advance do relógio para lá do
			// TTL: a posse original expira e o worker de DR reclama a partição.
			p.clock.Advance(leaseTTL + time.Second)
			before := resumeHits.n()
			proc := newWorkerProc(t, drStore, p.clock, p.worm, resumeHits, "worker-DR")
			out, err := proc.worker(t, nil).Run(ctx, effectPlan(drRunID, drSteps))
			if err != nil {
				return dr.ResumeEvidence{}, err
			}
			return dr.ResumeEvidence{
				ResumeTurn:        out.ResumeTurn,
				Executed:          out.Executed,
				Skipped:           out.Skipped,
				DuplicatedEffects: int(resumeHits.n() - before),
			}, nil
		},
	}
}

// drFactory constrói o Store de DR na fronteira (WithSovereigntyBoard), capturando-o
// no pipeline para asserções pós-recuperação.
func (p *pipeline) drFactory() dr.StoreFactory {
	return func(board, region string) (*eventstore.Store, error) {
		s, err := eventstore.New(eventstore.WithReplicas(3), eventstore.WithSovereigntyBoard(board, region))
		if err != nil {
			return nil, err
		}
		p.drStore = s
		return s, nil
	}
}

// ---------------------------------------------------------------------------
// GAME DAY END-TO-END (AC1–AC7): prova Fidelity==1.0, 0 efeitos duplicados,
// hash-chain verificada, RPO<=1min, RTO<=alvo, soberania respeitada.
// ---------------------------------------------------------------------------

func TestGameDay_EndToEnd_Recovery(t *testing.T) {
	ctx := context.Background()
	var trajHits, effectHits, resumeHits effectCounter
	p := buildOriginal(t, &trajHits, &effectHits)

	resolver := dr.MapResolver{drBoard: drRegion}
	// Relógio de RTO: dois instantes (início/fim) espaçados 5 min < 30 min.
	rto := stepClock(baseTime, 5*time.Minute)
	rec, err := dr.NewRecoverer(resolver, p.drFactory(), p.restorer, dr.WithClock(rto))
	if err != nil {
		t.Fatalf("NewRecoverer: %v", err)
	}

	var persisted []dr.GameDayEvidence
	gd, err := dr.NewGameDay(rec, p.exporter, time.Minute, 30*time.Minute, 24*time.Hour,
		dr.WithGameDayClock(fixed(baseTime)),
		dr.WithEvidencePersister(func(e dr.GameDayEvidence) error { persisted = append(persisted, e); return nil }),
	)
	if err != nil {
		t.Fatalf("NewGameDay: %v", err)
	}

	ev, err := gd.Run(ctx, p.recovery(t, &resumeHits))
	if err != nil {
		t.Fatalf("game day Run: %v", err)
	}

	// AC5: WORM verificado ANTES de restabelecer.
	if !ev.AuditVerified {
		t.Fatal("AC5: audit WORM não verificado")
	}
	// AC3: fidelidade 100% (todos os passos reproduzíveis, sem divergência).
	if ev.Replay.Fidelity != 1.0 || ev.Replay.Diverged {
		t.Fatalf("AC3: fidelidade=%v diverged=%v (quero 1.0/false)", ev.Replay.Fidelity, ev.Replay.Diverged)
	}
	if ev.Replay.Turns != 3 {
		t.Fatalf("AC3: turnos replayados = %d, quero 3", ev.Replay.Turns)
	}
	// AC3 (zero efeitos no replay): o replay não re-executou nenhuma tool da trajectória.
	if got := trajHits.n(); got != 2 {
		t.Fatalf("AC3: replay re-executou efeitos (trajHits=%d, quero 2 do original)", got)
	}
	// AC1/AC4: retoma resume-from-step com 0 efeitos duplicados.
	if ev.Resume.ResumeTurn != drSteps || ev.Resume.Skipped != drSteps-1 {
		t.Fatalf("AC1: retoma inesperada: %+v", ev.Resume)
	}
	if ev.Resume.DuplicatedEffects != 0 || resumeHits.n() != 0 {
		t.Fatalf("AC4: efeitos duplicados na retoma = %d (quero 0)", ev.Resume.DuplicatedEffects)
	}
	// AC6: soberania — o Store de DR e o log restaurado residem na fronteira-alvo.
	if got := normalize(p.drStore.Region()); got != normalize(drRegion) {
		t.Fatalf("AC6: região do Store de DR = %q, quero %q", got, drRegion)
	}
	if normalize(ev.Region) != normalize(drRegion) || normalize(ev.Restore.Region) != normalize(drRegion) {
		t.Fatalf("AC6: evidência fora da fronteira: region=%q restore=%q", ev.Region, ev.Restore.Region)
	}
	// AC2: RPO <= 1 min e RTO <= 30 min medidos e cumpridos.
	if !ev.RPOWithin {
		t.Fatalf("AC2: RPO fora do alvo (janela=%v alvo=%v)", ev.RPOWindow, ev.RPOTarget)
	}
	if !ev.RTOWithin || ev.RTO != 5*time.Minute {
		t.Fatalf("AC2: RTO=%v (within=%v) alvo=%v", ev.RTO, ev.RTOWithin, ev.RTOTarget)
	}
	// AC7: veredicto global + evidência persistida + próximo exercício agendado.
	if !ev.Passed {
		t.Fatalf("AC7: game day não passou: %+v", ev)
	}
	if len(persisted) != 1 || !persisted[0].Passed {
		t.Fatalf("AC7: evidência não persistida (%d)", len(persisted))
	}
	if !ev.NextExercise.After(ev.At) {
		t.Fatalf("AC7: próximo exercício não agendado (%v)", ev.NextExercise)
	}
	last, ok := gd.Last()
	if !ok || last.RunID != drRunID {
		t.Fatalf("AC7: Last() não devolveu a evidência do exercício")
	}
	// A restauração reinseriu o log preservando o envelope (eventos > 0).
	if ev.Restore.EventsRestored == 0 || !ev.Restore.Verified {
		t.Fatalf("restauro sem eventos/verificação: %+v", ev.Restore)
	}
}

// AC2 fail-closed no SLO: uma recuperação ÍNTEGRA mas com RTO acima do alvo é um game
// day FALHADO — Run devolve ErrTargetsExceeded (não nil), mas a evidência completa
// (Passed==false) fica na mesma preenchida e persistida. Fecha o vector de um chamador
// que só verifique err==nil tratar um exercício fora do SLO como sucesso.
func TestGameDay_RTOExceeded_FailsButPersists(t *testing.T) {
	ctx := context.Background()
	var trajHits, effectHits, resumeHits effectCounter
	p := buildOriginal(t, &trajHits, &effectHits)

	resolver := dr.MapResolver{drBoard: drRegion}
	// Relógio de RTO: 40 min entre início/fim — ACIMA do alvo de 30 min.
	rto := stepClock(baseTime, 40*time.Minute)
	rec, err := dr.NewRecoverer(resolver, p.drFactory(), p.restorer, dr.WithClock(rto))
	if err != nil {
		t.Fatalf("NewRecoverer: %v", err)
	}

	var persisted []dr.GameDayEvidence
	gd, err := dr.NewGameDay(rec, p.exporter, time.Minute, 30*time.Minute, 24*time.Hour,
		dr.WithGameDayClock(fixed(baseTime)),
		dr.WithEvidencePersister(func(e dr.GameDayEvidence) error { persisted = append(persisted, e); return nil }),
	)
	if err != nil {
		t.Fatalf("NewGameDay: %v", err)
	}

	ev, err := gd.Run(ctx, p.recovery(t, &resumeHits))
	// Erro de SLO (não nil), mas a evidência vem preenchida.
	if !errors.Is(err, dr.ErrTargetsExceeded) {
		t.Fatalf("esperava ErrTargetsExceeded, obtive %v", err)
	}
	// A recuperação foi ÍNTEGRA (só o SLO falhou): integridade verdadeira, veredicto falso.
	if !ev.AuditVerified || ev.Replay.Fidelity != 1.0 || ev.Resume.DuplicatedEffects != 0 {
		t.Fatalf("recuperação devia ser íntegra: %+v", ev)
	}
	if ev.RTOWithin || ev.RTO != 40*time.Minute {
		t.Fatalf("RTO devia exceder o alvo: within=%v rto=%v", ev.RTOWithin, ev.RTO)
	}
	if ev.Passed {
		t.Fatal("Passed devia ser false com RTO acima do alvo")
	}
	// A evidência do exercício FALHADO é na mesma persistida e acessível (AC7).
	if len(persisted) != 1 || persisted[0].Passed {
		t.Fatalf("evidência do exercício falhado não persistida corretamente: %d", len(persisted))
	}
	if last, ok := gd.Last(); !ok || last.Passed {
		t.Fatalf("Last() devia devolver o exercício falhado (Passed=false)")
	}
}
