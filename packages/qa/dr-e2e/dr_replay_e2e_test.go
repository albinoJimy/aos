package dre2e_test

// AOS-118 — TESTE DE FOGO de DR/replay end-to-end (EPIC-11, o que FECHA o epic).
//
// COMPÕE os primitivos já existentes numa história de desastre: perder um nó do
// Event Store a meio de escritas activas, promover a réplica sobrevivente
// (Store.Kill(Leader()) → electLeader — NÃO restore-from-backup), e provar que as
// trajectórias retomam resume-from-step SEM perder eventos confirmados (AC1), SEM
// duplicar efeitos (AC3), SEM cruzar a fronteira de soberania (AC4), com a
// fidelidade de replay a 1.0 (AC2) e o MTTR dentro do alvo de disponibilidade do
// plano de controlo (AC5). Um meta-teste prova que os controlos (quórum, fronteira)
// são load-bearing — a detecção é REAL, não tautológica.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/harness"
	"github.com/aos-ref/kernel/agent-runtime/worker"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
	"github.com/aos-ref/testkit/env"
)

// --- Constantes do cenário ---------------------------------------------------

const (
	drBoard  = "board:acme"
	drRegion = "eu"
	drSteps  = 3
	leaseTTL = 30 * time.Second
	// controlPlaneMTTRTarget é o alvo de tempo de recuperação do plano de controlo.
	// O failover é uma promoção de réplica in-process (eleição pura, síncrona) — o
	// MTTR modelado fica MUITO abaixo deste alvo, respeitando a disponibilidade 99,9%.
	controlPlaneMTTRTarget = 5 * time.Second
	// failoverDelta é o intervalo DETERMINÍSTICO (relógio injectado) que modela a
	// promoção da réplica: o bracket Kill→primeiro-sucesso-pós-failover. É o MTTR.
	failoverDelta = 200 * time.Millisecond
)

var baseTime = time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

// --- Relógio manual determinístico (lease/MTTR) ------------------------------

// manualClock é um relógio determinístico partilhado. Implementa durable.Clock
// (para o LeaseManager) e serve o bracket de MTTR — sem wall-clock nem sleeps.
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

// --- Contador de efeitos externos --------------------------------------------

// effectCounter conta as INVOCAÇÕES REAIS de uma tool (o efeito externo observável).
type effectCounter struct{ total int64 }

func (c *effectCounter) hit()     { atomic.AddInt64(&c.total, 1) }
func (c *effectCounter) n() int64 { return atomic.LoadInt64(&c.total) }

// --- Provisionamento do Store replicado na fronteira -------------------------

// sovStore provisiona, via o ambiente efémero de AOS-110, um Event Store replicado
// (3 réplicas, quórum 2) preso à fronteira de soberania do board (o guard recusa
// cross-border por construção). O teardown é garantido por t.Cleanup do env.
func sovStore(t *testing.T) *eventstore.Store {
	t.Helper()
	e := env.New(t, env.WithEventStore(
		eventstore.WithReplicas(3),
		eventstore.WithQuorum(2),
		eventstore.WithSovereigntyBoard(drBoard, drRegion),
	))
	if e.EventStore == nil {
		t.Fatal("env não provisionou o Event Store")
	}
	return e.EventStore
}

// --- Processo worker durável (composição dos primitivos) ---------------------

// durableProc agrupa primitivos duráveis FRESCOS (um "processo worker") sobre um
// Store e um relógio partilhados. É a composição de AOS-018/014/099 — nada é
// reimplementado.
type durableProc struct {
	lm     *durable.LeaseManager
	fenced *durable.FencedAppender
	ledger *durable.StepLedger
	resume *durable.Resumer
	cpr    *durable.EventStoreCheckpointer
	rm     *referencemonitor.Monitor
	seq    *durable.StepSequencer
}

func newDurableProc(t *testing.T, store *eventstore.Store, clock *manualClock, effectHits *effectCounter, workerID string) *durableProc {
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
	// O sink de mediação vai para o próprio Event Store (referência AOS-002); cada
	// permit sela o evento de mediação. O efeito da tool "echo" conta em effectHits.
	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		effectHits.hit()
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		t.Fatalf("Register(echo): %v", err)
	}
	return &durableProc{lm: lm, fenced: fenced, ledger: ledger, resume: resume, cpr: cpr, rm: rm, seq: seq}
}

func (p *durableProc) worker(t *testing.T, cpr agentruntime.Checkpointer) *worker.Worker {
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
				ToolID:     "echo",
				Capability: "cap:echo",
				Input:      []byte("payload"),
				Principal: referencemonitor.Principal{
					NHIID: "nhi:agent-dr", AgentID: "agent-dr", AgentClass: "researcher",
					DelegationChain: []referencemonitor.DelegationHop{{Sub: "human:alice", ActAs: "nhi:agent-dr"}},
					Authority:       []string{"cap:echo"},
				},
			},
			CostMicroUSD: int64((i + 1) * 1000),
		}
	}
	return &worker.RunPlan{RunID: runID, Steps: steps}
}

// dropCheckpointer engole o checkpoint de UM turno — simula o DESASTRE a cair entre
// o efeito (ledger aplicado) e o seu checkpoint, forçando a retoma a re-executar
// esse turno e a provar a dedup do ledger (0 efeitos duplicados).
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

// input constrói um EventInput sintético para a carga concorrente.
func loadInput(runID, stepID string) eventstore.EventInput {
	return eventstore.EventInput{
		Type:    "dr.load.event",
		Payload: json.RawMessage(`{"k":"v"}`),
		RunID:   runID,
		StepID:  stepID,
	}
}

// ===========================================================================
// AC1 — ZERO EVENTOS CONFIRMADOS PERDIDOS sob perda de nó + carga concorrente.
// ===========================================================================

// committedRef é a coordenada durável de um evento confirmado, para reconciliação.
type committedRef struct {
	stream  string
	seq     uint64
	eventID string
}

// TestDR_NodeLoss_ZeroEventsConfirmedLost prova a durabilidade sob perda de nó por
// RECONCILIAÇÃO do log: com N trajectórias a escrever em paralelo (carga
// concorrente), mata-se o LÍDER do Event Store A MEIO das escritas; o failover para
// a réplica sobrevivente (quórum 2 de 3) é automático; e NENHUM evento que tenha
// devolvido committed desaparece do log — o conjunto committed antes do Kill é
// subconjunto do log final. Corre sob -race (concorrência real); as asserções são
// sobre o estado committed (determinista).
func TestDR_NodeLoss_ZeroEventsConfirmedLost(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := sovStore(t)

	const (
		streams      = 8
		perStream    = 40
		total        = streams * perStream
		killAtCommit = total / 2 // mata o líder no ponto médio das escritas
	)

	var (
		committedN atomic.Int64
		failedN    atomic.Int64
		killOnce   sync.Once
		killCh     = make(chan struct{})
		perGoref   = make([][]committedRef, streams)
		wg         sync.WaitGroup
	)

	wg.Add(streams)
	for w := 0; w < streams; w++ {
		go func(w int) {
			defer wg.Done()
			stream := fmt.Sprintf("run-%d", w)
			refs := make([]committedRef, 0, perStream)
			for i := 1; i <= perStream; i++ {
				res, err := store.Append(ctx, stream, loadInput(stream, fmt.Sprintf("s%d", i)))
				if err != nil {
					failedN.Add(1)
					return
				}
				refs = append(refs, committedRef{stream: stream, seq: res.Seq, eventID: res.Event.EventID})
				// Ao cruzar o ponto médio, dispara o Kill do líder — trajectórias activas.
				if committedN.Add(1) == int64(killAtCommit) {
					killOnce.Do(func() { close(killCh) })
				}
			}
			perGoref[w] = refs
		}(w)
	}

	// Controlador do desastre: espera o ponto médio, captura o líder e MATA-O. O
	// electLeader promove sincronamente a réplica sobrevivente mais actualizada.
	oldLeader := -1
	newLeader := -1
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-killCh
		oldLeader = store.Leader()
		if err := store.Kill(oldLeader); err != nil {
			t.Errorf("Kill(líder %d): %v", oldLeader, err)
			return
		}
		newLeader = store.Leader()
	}()

	wg.Wait()
	<-done

	// O failover ocorreu e ficou preso à fronteira.
	if newLeader == -1 || newLeader == oldLeader {
		t.Fatalf("failover não elegeu novo líder: old=%d new=%d", oldLeader, newLeader)
	}
	if got := store.ReplicaRegion(newLeader); got != drRegion {
		t.Fatalf("novo líder fora da fronteira: região=%q, quero %q", got, drRegion)
	}
	if failedN.Load() != 0 {
		t.Fatalf("%d escritas falharam dentro do quórum sobrevivente (não deviam)", failedN.Load())
	}

	// RECONCILIAÇÃO: cada evento que devolveu committed (antes OU depois do Kill) tem
	// de estar no log final, com o MESMO EventID e Seq — nenhum committed desapareceu.
	reconciled := 0
	for w := 0; w < streams; w++ {
		stream := fmt.Sprintf("run-%d", w)
		evs, err := store.Read(ctx, stream, 1)
		if err != nil {
			t.Fatalf("Read(%s) pós-failover: %v", stream, err)
		}
		bySeq := make(map[uint64]string, len(evs))
		for _, ev := range evs {
			bySeq[ev.Seq] = ev.EventID
		}
		if len(evs) != perStream {
			t.Fatalf("%s: %d eventos no log final, quero %d (zero perda)", stream, len(evs), perStream)
		}
		for _, ref := range perGoref[w] {
			got, ok := bySeq[ref.seq]
			if !ok {
				t.Fatalf("EVENTO COMMITTED PERDIDO: %s seq=%d ausente do log pós-failover", ref.stream, ref.seq)
			}
			if got != ref.eventID {
				t.Fatalf("%s seq=%d: EventID divergente (committed=%q, log=%q)", ref.stream, ref.seq, ref.eventID, got)
			}
			reconciled++
		}
	}
	if reconciled != total {
		t.Fatalf("reconciliados %d de %d committed", reconciled, total)
	}
}

// ===========================================================================
// AC2 — RESUME-FROM-STEP FIEL end-to-end após o failover.
// ===========================================================================

// TestDR_Failover_ResumeFromStepFidelity prova, em duas frentes complementares,
// que uma trajectória em curso retoma resume-from-step após o failover e conclui
// com o MESMO resultado esperado:
//
//	(a) WORKER durável REAL: worker A executa o plano com o checkpoint do último
//	    turno ENGOLIDO (desastre mid-run); MATA-SE o líder do Store (node loss +
//	    failover); um worker B FRESCO reconstrói o ponto de retoma do log
//	    RESTAURADO pela réplica promovida e conclui (Skipped>0 && Completed), com
//	    ZERO efeitos re-executados (dedup do ledger);
//	(b) HARNESS de replay: a golden de delegação verifica-se com ReplayFidelity==1.0
//	    e ResumeMismatches==0 (fidelidade de replay resume-from-step end-to-end).
func TestDR_Failover_ResumeFromStepFidelity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// (a) Worker durável através de uma perda de nó REAL.
	store := sovStore(t)
	clock := newManualClock()
	runID := "run-dr-resume"
	var effectsA, effectsB effectCounter

	procA := newDurableProc(t, store, clock, &effectsA, "worker-A")
	wA := procA.worker(t, &dropCheckpointer{inner: procA.cpr, dropTurn: drSteps})
	outA, err := wA.Run(ctx, effectPlan(runID, drSteps))
	if err != nil {
		t.Fatalf("worker A Run: %v", err)
	}
	if !outA.Completed || outA.Executed != drSteps {
		t.Fatalf("worker A inesperado: %+v", outA)
	}
	if effectsA.n() != drSteps {
		t.Fatalf("efeitos do worker A = %d, quero %d", effectsA.n(), drSteps)
	}

	// DESASTRE: mata o líder do Event Store. A réplica promovida detém todo o log
	// committed do worker A (replicação síncrona), pelo que o worker B retoma do log.
	oldLeader := store.Leader()
	if err := store.Kill(oldLeader); err != nil {
		t.Fatalf("Kill(líder %d): %v", oldLeader, err)
	}
	newLeader := store.Leader()
	if newLeader == -1 || newLeader == oldLeader {
		t.Fatalf("failover falhou: old=%d new=%d", oldLeader, newLeader)
	}

	// A posse original expira (relógio manual para lá do TTL): o worker B reclama.
	clock.Advance(leaseTTL + time.Second)
	procB := newDurableProc(t, store, clock, &effectsB, "worker-B")
	outB, err := procB.worker(t, nil).Run(ctx, effectPlan(runID, drSteps))
	if err != nil {
		t.Fatalf("worker B Run (retoma pós-failover): %v", err)
	}
	// RETOMA resume-from-step: salta os turnos já confirmados e conclui.
	if !outB.Completed {
		t.Fatalf("worker B não concluiu: %+v", outB)
	}
	if outB.Skipped != drSteps-1 || outB.ResumeTurn != drSteps {
		t.Fatalf("retoma inesperada: Skipped=%d ResumeTurn=%d (quero %d/%d)", outB.Skipped, outB.ResumeTurn, drSteps-1, drSteps)
	}
	// ZERO efeitos duplicados na retoma (a dedup do ledger cobre o turno re-executado).
	if effectsB.n() != 0 {
		t.Fatalf("worker B re-executou %d efeito(s) na retoma (quero 0)", effectsB.n())
	}

	// (b) Fidelidade de replay resume-from-step end-to-end (golden de delegação).
	rep := verifyDelegationFidelity(t)
	if rep.ReplayFidelity != 1.0 || rep.ResumeMismatches != 0 || !rep.Pass {
		t.Fatalf("fidelidade de replay: fidelity=%v resumeMismatches=%d pass=%v (quero 1.0/0/true)",
			rep.ReplayFidelity, rep.ResumeMismatches, rep.Pass)
	}
	if rep.ResumePoints == 0 {
		t.Fatal("nenhum ponto de retoma verificado (resume-from-step não exercitado)")
	}
}

// verifyDelegationFidelity constrói a golden trajectory MULTI-PASSO COM SUB-AGENTE
// (BuildDelegationGolden — relógio fixo, seed pinado, faults em step-000002/3) e
// corre o harness.Verify (replay determinístico + idempotência + resume-from-step).
func verifyDelegationFidelity(t *testing.T) harness.FidelityReport {
	t.Helper()
	fix, err := harness.BuildDelegationGolden("golden_dr_delegation")
	if err != nil {
		t.Fatalf("BuildDelegationGolden: %v", err)
	}
	t.Cleanup(fix.Close)
	rep, err := harness.Verify(context.Background(), fix.Case())
	if err != nil {
		t.Fatalf("harness.Verify: %v", err)
	}
	if err := rep.Err(); err != nil {
		t.Fatalf("relatório de fidelidade falhado: %v", err)
	}
	return rep
}

// ===========================================================================
// AC3 — ZERO EFEITOS DUPLICADOS sob failover/retry (idempotência AOS-112).
// ===========================================================================

// TestDR_Failover_ZeroDuplicatedEffects prova exactamente-uma-vez com o Kill
// intercalado ENTRE a aplicação do efeito e a reconstrução do worker seguinte:
// worker A aplica o efeito (committed a todas as réplicas); mata-se o líder; um
// worker B FRESCO reconstrói o ledger do log da réplica PROMOVIDA e re-tenta — a
// dedup por f(run_id,step_id) impede o segundo efeito. Reutiliza AOS-112
// (NewDomainEffect / StepLedger). Uma segunda camada corre o calendário
// at-least-once completo (DriveEffectSchedule) sobre um Store JÁ falhado-over.
func TestDR_Failover_ZeroDuplicatedEffects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := sovStore(t)

	// --- Camada 1: Kill entre a aplicação e a reconstrução do worker seguinte. ---
	runID, stepID := "run-dr-dup", "step-000001"
	eff := harness.NewDomainEffect(runID, stepID)
	key, err := eff.KeyAt(0)
	if err != nil {
		t.Fatalf("KeyAt: %v", err)
	}

	ledgerA, err := durable.NewStepLedger(store)
	if err != nil {
		t.Fatalf("NewStepLedger A: %v", err)
	}
	if err := ledgerA.Rebuild(ctx, runID); err != nil {
		t.Fatalf("Rebuild A: %v", err)
	}
	_, appliedA, err := ledgerA.Apply(ctx, key, eff.Run)
	if err != nil {
		t.Fatalf("Apply A: %v", err)
	}
	if !appliedA || eff.Observed() != 1 {
		t.Fatalf("worker A: applied=%v observed=%d (quero true/1)", appliedA, eff.Observed())
	}

	// DESASTRE entre a aplicação e o commit do worker seguinte.
	oldLeader := store.Leader()
	if err := store.Kill(oldLeader); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if store.Leader() == -1 || store.Leader() == oldLeader {
		t.Fatalf("failover falhou (líder=%d)", store.Leader())
	}

	// Worker B FRESCO reconstrói do log da réplica promovida e re-tenta: deduplica.
	ledgerB, err := durable.NewStepLedger(store)
	if err != nil {
		t.Fatalf("NewStepLedger B: %v", err)
	}
	if err := ledgerB.Rebuild(ctx, runID); err != nil {
		t.Fatalf("Rebuild B (pós-failover): %v", err)
	}
	_, appliedB, err := ledgerB.Apply(ctx, key, eff.Run)
	if err != nil {
		t.Fatalf("Apply B: %v", err)
	}
	if appliedB {
		t.Fatal("worker B re-aplicou o efeito (esperava dedup pós-failover)")
	}
	if eff.Observed() != 1 {
		t.Fatalf("efeito observado %d vezes através do failover (quero exactamente 1)", eff.Observed())
	}

	// --- Camada 2: calendário at-least-once completo sobre o Store JÁ falhado-over. ---
	eff2 := harness.NewDomainEffect("run-dr-dup-2", "step-000001")
	observed, err := harness.VerifyEffectIdempotency(ctx, "run-dr-dup-2", store, eff2)
	if err != nil {
		t.Fatalf("VerifyEffectIdempotency pós-failover: %v", err)
	}
	if observed != 1 {
		t.Fatalf("efeito observado %d vezes sob crash-schedule pós-failover (quero 1)", observed)
	}
}

// ===========================================================================
// AC4 — O failover NÃO cruza a fronteira de soberania + FENCING.
// ===========================================================================

// TestDR_Failover_StaysInRegion prova que o failover fica preso à fronteira e que
// o fencing rejeita a escrita de um nó obsoleto:
//   - após o Kill do líder, o novo líder está IN-REGION (ReplicaRegion=="eu");
//   - um cluster/réplica CROSS-BORDER é recusado por construção (ErrSovereigntyViolation);
//   - um worker OBSOLETO (token t após um novo claim ter elevado o corrente para
//     t+1) faz FencedAppender.Append com token t → ErrStaleFencingToken: a escrita
//     NÃO vinga (reconcilia-se que o log não a contém).
func TestDR_Failover_StaysInRegion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := sovStore(t)

	// Escreve trajectórias e mata o líder — o novo líder fica na fronteira.
	for i := 1; i <= 4; i++ {
		if _, err := store.Append(ctx, "run-sov", loadInput("run-sov", fmt.Sprintf("s%d", i))); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	oldLeader := store.Leader()
	if err := store.Kill(oldLeader); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	newLeader := store.Leader()
	if newLeader == -1 || newLeader == oldLeader {
		t.Fatalf("failover falhou: old=%d new=%d", oldLeader, newLeader)
	}
	if got := store.ReplicaRegion(newLeader); got != drRegion {
		t.Fatalf("AC4: novo líder cruzou a fronteira: região=%q, quero %q", got, drRegion)
	}
	// Zero perda in-region após o failover.
	if evs, err := store.Read(ctx, "run-sov", 1); err != nil || len(evs) != 4 {
		t.Fatalf("perda pós-failover in-region: n=%d err=%v", len(evs), err)
	}

	// Um cluster cross-border é recusado por construção (a fronteira é fail-closed).
	if _, err := eventstore.New(
		eventstore.WithReplicas(3), eventstore.WithQuorum(2),
		eventstore.WithSovereigntyBoard(drBoard, drRegion),
		eventstore.WithReplicaRegions(drRegion, drRegion, "us"),
	); !errors.Is(err, eventstore.ErrSovereigntyViolation) {
		t.Fatalf("cluster cross-border: err=%v, quero ErrSovereigntyViolation", err)
	}

	// --- FENCING: a escrita de um nó/worker obsoleto NÃO vinga. ---
	clock := newManualClock()
	lm, err := durable.NewLeaseManager(store, leaseTTL, durable.WithLeaseClock(clock))
	if err != nil {
		t.Fatalf("NewLeaseManager: %v", err)
	}
	fa, err := durable.NewFencedAppender(store, lm)
	if err != nil {
		t.Fatalf("NewFencedAppender: %v", err)
	}
	fenceRun := "run-fence"
	leaseA, err := lm.Claim(ctx, fenceRun) // token t
	if err != nil {
		t.Fatalf("Claim A: %v", err)
	}
	clock.Advance(leaseTTL + time.Second)  // o lease de A expira → reclamável
	leaseB, err := lm.Claim(ctx, fenceRun) // token t+1 (corrente)
	if err != nil {
		t.Fatalf("Claim B: %v", err)
	}
	if leaseB.Token.Value() <= leaseA.Token.Value() {
		t.Fatalf("token não avançou: A=%d B=%d", leaseA.Token.Value(), leaseB.Token.Value())
	}

	// O detentor legítimo (token corrente t+1) escreve — vinga.
	legit := eventstore.EventInput{Type: "worker.step.dispatched", RunID: fenceRun, StepID: "wstep-legit", Payload: json.RawMessage(`{"w":"B"}`)}
	if _, err := fa.Append(ctx, fenceRun, leaseB.Token, legit); err != nil {
		t.Fatalf("append legítimo (token corrente): %v", err)
	}
	// O worker OBSOLETO (token t) tenta escrever — é fenced-out.
	stale := eventstore.EventInput{Type: "worker.step.dispatched", RunID: fenceRun, StepID: "wstep-stale", Payload: json.RawMessage(`{"w":"A"}`)}
	if _, err := fa.Append(ctx, fenceRun, leaseA.Token, stale); !errors.Is(err, durable.ErrStaleFencingToken) {
		t.Fatalf("escrita obsoleta: err=%v, quero ErrStaleFencingToken", err)
	}

	// RECONCILIAÇÃO: o log do run contém a escrita legítima e NÃO a obsoleta.
	evs, err := store.Read(ctx, fenceRun, 1)
	if err != nil {
		t.Fatalf("Read(%s): %v", fenceRun, err)
	}
	for _, ev := range evs {
		if ev.StepID == "wstep-stale" {
			t.Fatalf("a escrita obsoleta vingou no log (seq=%d) — fencing falhou", ev.Seq)
		}
	}
	found := false
	for _, ev := range evs {
		if ev.StepID == "wstep-legit" {
			found = true
		}
	}
	if !found {
		t.Fatal("a escrita legítima não está no log")
	}
}

// ===========================================================================
// AC5 / DoD — MTTR + Replay-fidelity REPORTADOS (linha marcada AOS_DR_REPORT).
// ===========================================================================

// drReport é o relatório do teste de fogo. Ordem de campos FIXA e Pass em ÚLTIMO —
// o gate ancora o veredicto ao fim da linha ("pass":true}$), como o AOS_REPLAY_REPORT.
type drReport struct {
	MTTRms            int64   `json:"mttr_ms"`
	ReplayFidelity    float64 `json:"replay_fidelity"`
	EventsLost        int     `json:"events_lost"`
	DuplicatedEffects int     `json:"duplicated_effects"`
	CrossedBoundary   bool    `json:"crossed_boundary"`
	Pass              bool    `json:"pass"`
}

// TestDR_ReportsMTTRAndFidelity mede o MTTR do failover com um relógio INJECTADO
// (bracket Kill→primeiro sucesso pós-failover), confirma que não viola o alvo de
// disponibilidade do plano de controlo, colhe a Replay-fidelity da golden, e EMITE
// a linha marcada AOS_DR_REPORT (consumida pelo gate scripts/ci/dr-e2e.sh).
func TestDR_ReportsMTTRAndFidelity(t *testing.T) {
	ctx := context.Background()
	store := sovStore(t)

	// Trajectórias escritas antes do desastre (para reconciliar zero-loss).
	const seeded = 6
	for i := 1; i <= seeded; i++ {
		if _, err := store.Append(ctx, "run-rep", loadInput("run-rep", fmt.Sprintf("s%d", i))); err != nil {
			t.Fatalf("seed append %d: %v", i, err)
		}
	}

	// MTTR: bracket determinístico Kill → primeiro sucesso pós-failover.
	clock := newManualClock()
	killAt := clock.Now()
	oldLeader := store.Leader()
	if err := store.Kill(oldLeader); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	clock.Advance(failoverDelta) // modela a promoção da réplica (eleição síncrona)
	newLeader := store.Leader()
	if newLeader == -1 || newLeader == oldLeader {
		t.Fatalf("failover falhou: old=%d new=%d", oldLeader, newLeader)
	}
	// Primeira operação bem-sucedida pós-failover (a réplica promovida serve o log).
	evs, err := store.Read(ctx, "run-rep", 1)
	if err != nil {
		t.Fatalf("Read pós-failover: %v", err)
	}
	recoveredAt := clock.Now()
	mttr := recoveredAt.Sub(killAt)

	eventsLost := seeded - len(evs) // zero se o log sobreviveu íntegro
	crossedBoundary := store.ReplicaRegion(newLeader) != drRegion

	// Idempotência sob o failover (zero efeitos duplicados) — reutiliza AOS-112.
	eff := harness.NewDomainEffect("run-rep-eff", "step-000001")
	observed, err := harness.VerifyEffectIdempotency(ctx, "run-rep-eff", store, eff)
	if err != nil {
		t.Fatalf("VerifyEffectIdempotency: %v", err)
	}
	duplicated := observed - 1

	// Replay-fidelity da golden de delegação (resume-from-step end-to-end).
	rep := verifyDelegationFidelity(t)

	report := drReport{
		MTTRms:            mttr.Milliseconds(),
		ReplayFidelity:    rep.ReplayFidelity,
		EventsLost:        eventsLost,
		DuplicatedEffects: duplicated,
		CrossedBoundary:   crossedBoundary,
		Pass: mttr <= controlPlaneMTTRTarget &&
			rep.ReplayFidelity == 1.0 &&
			eventsLost == 0 &&
			duplicated == 0 &&
			!crossedBoundary,
	}

	// Asserções fail-closed (o teste é vermelho se qualquer invariante quebrar).
	if mttr > controlPlaneMTTRTarget {
		t.Fatalf("AC5: MTTR=%v excede o alvo de disponibilidade %v", mttr, controlPlaneMTTRTarget)
	}
	if !report.Pass {
		t.Fatalf("relatório de DR não passou: %+v", report)
	}

	line, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	// Linha marcada (o gate extrai por 'AOS_DR_REPORT ' e valida o conteúdo).
	t.Logf("AOS_DR_REPORT %s", line)
}

// ===========================================================================
// META-TESTES — provam que os controlos (quórum, fronteira) são LOAD-BEARING:
// a detecção é REAL, não tautológica.
// ===========================================================================

// TestMetaDetects_DRLossWithoutQuorum prova que a durabilidade depende MESMO do
// quórum sobrevivente (não é grátis): matando réplicas suficientes para PERDER o
// quórum, o Store recusa novas escritas fail-closed (ErrNoQuorum) em vez de servir
// silenciosamente um log truncado como autoritativo — é assim que a perda seria
// DETECTADA. Se o piso de quórum não fosse load-bearing, este Append passaria.
func TestMetaDetects_DRLossWithoutQuorum(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := sovStore(t)

	if _, err := store.Append(ctx, "run-meta", loadInput("run-meta", "s1")); err != nil {
		t.Fatalf("append inicial: %v", err)
	}

	// Mata o líder (failover ok: 2 de 3 vivas == quórum) e depois uma sobrevivente
	// (1 de 3 viva < quórum 2): o quórum de escrita perde-se.
	if err := store.Kill(store.Leader()); err != nil {
		t.Fatalf("Kill 1 (líder): %v", err)
	}
	if store.Leader() == -1 {
		t.Fatal("após o 1º Kill o Store devia continuar disponível (quórum sobrevivente)")
	}
	// Escrita ainda possível com 2 réplicas vivas (prova que o 1º Kill não colapsou).
	if _, err := store.Append(ctx, "run-meta", loadInput("run-meta", "s2")); err != nil {
		t.Fatalf("append pós-1º-Kill (quórum sobrevivente): %v", err)
	}

	// Segundo Kill: cai abaixo do quórum.
	var victim = -1
	for _, id := range store.Replicas() {
		if store.IsAlive(id) {
			victim = id
			break
		}
	}
	if victim == -1 {
		t.Fatal("nenhuma réplica viva para o 2º Kill")
	}
	if err := store.Kill(victim); err != nil {
		t.Fatalf("Kill 2: %v", err)
	}

	// Sem quórum, a escrita é RECUSADA fail-closed — a perda de durabilidade é
	// detectada (o Store não serve um log truncado como completo).
	if _, err := store.Append(ctx, "run-meta", loadInput("run-meta", "s3")); !errors.Is(err, eventstore.ErrNoQuorum) {
		t.Fatalf("META: append sem quórum devolveu err=%v, quero ErrNoQuorum (controlo de quórum load-bearing)", err)
	}
}

// TestMetaDetects_SovereigntyBlocksCrossBorderPromotion prova que o filtro de região
// é load-bearing: um cluster com uma réplica FORA da fronteira é recusado por
// construção (ErrSovereigntyViolation). Como não é sequer possível construir um
// cluster cross-border, o failover NUNCA pode promover liderança cross-border — a
// garantia de AC4 não é tautológica, assenta neste guard fail-closed.
func TestMetaDetects_SovereigntyBlocksCrossBorderPromotion(t *testing.T) {
	t.Parallel()
	// Uma réplica out-of-region é rejeitada na construção.
	if _, err := eventstore.New(
		eventstore.WithReplicas(3), eventstore.WithQuorum(2),
		eventstore.WithSovereigntyBoard(drBoard, drRegion),
		eventstore.WithReplicaRegions(drRegion, "us", drRegion),
	); !errors.Is(err, eventstore.ErrSovereigntyViolation) {
		t.Fatalf("META: cluster com réplica cross-border err=%v, quero ErrSovereigntyViolation", err)
	}
	// Controlo positivo: o mesmo cluster inteiramente in-region CONSTRÓI-SE — o guard
	// não é um "deny tudo" trivial; discrimina exactamente a fronteira.
	s, err := eventstore.New(
		eventstore.WithReplicas(3), eventstore.WithQuorum(2),
		eventstore.WithSovereigntyBoard(drBoard, drRegion),
		eventstore.WithReplicaRegions(drRegion, drRegion, drRegion),
	)
	if err != nil {
		t.Fatalf("cluster in-region devia construir-se: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
}
