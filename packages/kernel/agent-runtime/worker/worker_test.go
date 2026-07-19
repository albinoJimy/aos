package worker_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/worker"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ---------------------------------------------------------------------------
// Harness: relógio manual (expiração determinística), contador GLOBAL de efeitos
// externos (a tool corre uma vez por passo aplicado), e uma "processo" que constrói
// PRIMITIVOS FRESCOS sobre um Event Store PARTILHADO — o modelo de um worker novo
// que herda apenas o log durável (statelessness, AC2).
// ---------------------------------------------------------------------------

const leaseTTL = 30 * time.Second

type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func newManualClock() *manualClock { return &manualClock{t: time.Unix(1_700_000_000, 0)} }

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

func newStore(t *testing.T) *eventstore.Store {
	t.Helper()
	s, err := eventstore.New() // 3 réplicas, quórum 2 (default)
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// effectCounter conta as INVOCAÇÕES REAIS da tool (o efeito externo). É GLOBAL ao
// teste (partilhado entre "processos"), para provar que N passos produzem
// EXACTAMENTE N efeitos, mesmo com crash/retoma/reatribuição.
type effectCounter struct {
	perStep sync.Map // stepInput -> *int64
	total   int64
}

func (c *effectCounter) hit(input string) {
	atomic.AddInt64(&c.total, 1)
	v, _ := c.perStep.LoadOrStore(input, new(int64))
	atomic.AddInt64(v.(*int64), 1)
}

func (c *effectCounter) count(input string) int64 {
	v, ok := c.perStep.Load(input)
	if !ok {
		return 0
	}
	return atomic.LoadInt64(v.(*int64))
}

// proc é um "processo worker": um conjunto de primitivos FRESCOS (RM, ledger,
// resumer, checkpointer, lease manager, fenced appender) sobre o store e o relógio
// PARTILHADOS. Um proc novo não herda estado in-memory nenhum — só o log durável.
type proc struct {
	store  *eventstore.Store
	clock  *manualClock
	lm     *durable.LeaseManager
	fenced *durable.FencedAppender
	ledger *durable.StepLedger
	resume *durable.Resumer
	cpr    *durable.EventStoreCheckpointer
	rm     *referencemonitor.Monitor
	seq    *durable.StepSequencer
}

func newProc(t *testing.T, store *eventstore.Store, clock *manualClock, ctr *effectCounter, workerID string) *proc {
	t.Helper()
	lm, err := durable.NewLeaseManager(store, leaseTTL,
		durable.WithLeaseClock(clock), durable.WithWorkerID(workerID))
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
	rm := referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
	// A tool "echo" é o efeito externo: incrementa o contador GLOBAL por invocação.
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		ctr.hit(string(in))
		return append([]byte("echoed:"), in...), nil
	}); err != nil {
		t.Fatalf("Register(echo): %v", err)
	}
	return &proc{store: store, clock: clock, lm: lm, fenced: fenced, ledger: ledger, resume: resume, cpr: cpr, rm: rm, seq: seq}
}

// worker constrói um Worker deste processo, com um checkpointer opcionalmente
// substituído (para simular crash antes de persistir um checkpoint) e um tracer.
func (p *proc) worker(t *testing.T, cpr agentruntime.Checkpointer, tracer otelgenai.Tracer, opts ...worker.WorkerOption) *worker.Worker {
	t.Helper()
	if cpr == nil {
		cpr = p.cpr
	}
	base := []worker.WorkerOption{
		worker.WithStepSequencer(p.seq),
		worker.WithTracer(tracer),
		// Intervalo de heartbeat longo: nos testes de expiração/fencing controlamos o
		// relógio manualmente; o heartbeat real não dispara durante o teste.
		worker.WithHeartbeatInterval(time.Hour),
	}
	w, err := worker.NewWorker(p.lm, p.fenced, p.ledger, p.resume, cpr, p.rm, append(base, opts...)...)
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	return w
}

// plan constrói um RunPlan de n passos "echo" com inputs sintéticos (sem PII) e
// custo por passo.
func plan(runID string, n int) *worker.RunPlan {
	steps := make([]worker.Step, n)
	for i := range steps {
		input := fmt.Sprintf("payload-%d", i+1)
		steps[i] = worker.Step{
			Call: referencemonitor.Call{
				ToolID:     "echo",
				Capability: "cap:echo",
				Input:      []byte(input),
				Principal: referencemonitor.Principal{
					NHIID:           "nhi:agent-1",
					AgentID:         "agent-1",
					AgentClass:      "researcher",
					DelegationChain: []referencemonitor.DelegationHop{{Sub: "human:alice", ActAs: "nhi:agent-1"}},
					Authority:       []string{"cap:echo"},
				},
			},
			CostMicroUSD: int64((i + 1) * 1000),
		}
	}
	return &worker.RunPlan{RunID: runID, Steps: steps}
}

// referencemonitorNew constrói um Monitor com sink durável no store (helper de teste).
func referencemonitorNew(store *eventstore.Store) *referencemonitor.Monitor {
	return referencemonitor.New(referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(store)))
}

// registerBlocking regista uma tool "echo" que BLOQUEIA no primeiro efeito até o
// teste fechar release, sinalizando entered — para intercalar eventos (heartbeat,
// supersessão) enquanto o worker detém a partição. Conta cada efeito no contador.
func registerBlocking(t *testing.T, rm *referencemonitor.Monitor, ctr *effectCounter, entered chan<- struct{}, release <-chan struct{}) {
	t.Helper()
	if err := rm.Register("echo", func(_ context.Context, in []byte) ([]byte, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		ctr.hit(string(in))
		return in, nil
	}); err != nil {
		t.Fatalf("Register(echo blocking): %v", err)
	}
}

func countType(t *testing.T, store *eventstore.Store, runID, typ string) int {
	t.Helper()
	evs, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return 0
		}
		t.Fatalf("Read(%s): %v", runID, err)
	}
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// dropCheckpointer envolve o checkpointer real e ENGOLE o checkpoint de um turno
// específico — simula um crash APÓS o efeito (ledger aplicado) mas ANTES de o
// checkpoint desse turno persistir. É o cenário exacto que força a retoma a
// RE-EXECUTAR esse turno e a provar a dedup do ledger (0 efeitos duplicados).
type dropCheckpointer struct {
	inner    agentruntime.Checkpointer
	dropTurn int
}

func (d *dropCheckpointer) Checkpoint(ctx context.Context, cp agentruntime.Checkpoint) error {
	if cp.Turn == d.dropTurn {
		return nil // engolido: como se o processo morresse antes de persistir
	}
	return d.inner.Checkpoint(ctx, cp)
}

// ---------------------------------------------------------------------------
// AC1 + DoD: kill de worker a MEIO de um run → worker novo retoma RESUME-FROM-STEP
// → os efeitos aplicados UMA SÓ vez (0 duplicados). O crash cai entre o efeito do
// turno 2 e o seu checkpoint (dropCheckpointer), forçando a re-execução do turno 2
// no worker B e provando a dedup do ledger.
// ---------------------------------------------------------------------------

func TestWorker_KillMidRun_ResumesFromStep_NoDuplicateEffects(t *testing.T) {
	t.Parallel()
	const runID = "run_kill"
	const nSteps = 4
	store := newStore(t)
	clock := newManualClock()
	ctr := &effectCounter{}

	// Worker A processa 2 dos 4 passos e "morre" APÓS o efeito do turno 2 mas ANTES de
	// o seu checkpoint persistir (dropTurn=2). O ledger do turno 2 FICA no log; o
	// último checkpoint confirmado é o turno 1 → a retoma cai no turno 2.
	procA := newProc(t, store, clock, ctr, "worker-A")
	wA := procA.worker(t, &dropCheckpointer{inner: procA.cpr, dropTurn: 2}, otelgenai.NoopTracer{})
	outA, err := wA.Run(context.Background(), plan(runID, 2))
	if err != nil {
		t.Fatalf("worker A Run: %v", err)
	}
	if !outA.Completed {
		t.Fatalf("worker A não completou os seus 2 passos: %+v", outA)
	}

	// Cada efeito correu uma vez em A (2 passos, 2 efeitos).
	if got := atomic.LoadInt64(&ctr.total); got != 2 {
		t.Fatalf("após A: esperava 2 efeitos, obtive %d", got)
	}

	// O lease de A expira (ninguém faz heartbeat): avança o relógio para além do TTL.
	clock.Advance(leaseTTL + time.Second)

	// Worker B: processo NOVO (primitivos frescos, só o log partilhado). Reclama a
	// partição (token superior), reconstrói o cursor e retoma resume-from-step. Como o
	// checkpoint do turno 2 não persistiu, o último confirmado é o turno 1 → B retoma
	// no turno 2 e RE-EXECUTA-O; o ledger deduplica o efeito (não corre a tool).
	procB := newProc(t, store, clock, ctr, "worker-B")
	wB := procB.worker(t, nil, otelgenai.NoopTracer{})
	outB, err := wB.Run(context.Background(), plan(runID, nSteps))
	if err != nil {
		t.Fatalf("worker B Run: %v", err)
	}
	if !outB.Completed {
		t.Fatalf("worker B não completou: %+v", outB)
	}
	// B retomou no turno 2 (turno 1 confirmado saltado).
	if outB.ResumeTurn != 2 || outB.Skipped != 1 {
		t.Fatalf("B devia retomar no turno 2 (1 saltado): %+v", outB)
	}

	// PROVA CENTRAL (AC1/DoD): cada efeito correu EXACTAMENTE UMA vez no total, apesar
	// da re-execução do turno 2. Zero efeitos duplicados.
	if got := atomic.LoadInt64(&ctr.total); got != nSteps {
		t.Fatalf("EFEITOS DUPLICADOS: esperava %d efeitos no total, obtive %d", nSteps, got)
	}
	for i := 1; i <= nSteps; i++ {
		input := fmt.Sprintf("payload-%d", i)
		if got := ctr.count(input); got != 1 {
			t.Fatalf("passo %q correu %d vezes, esperava 1 (idempotência por passo)", input, got)
		}
	}

	// O run está confirmado até ao fim: um checkpoint verified para cada turno.
	if n := countType(t, store, runID, durable.EventTypeCheckpoint); n < nSteps {
		t.Fatalf("esperava >= %d checkpoints verified, obtive %d", nSteps, n)
	}
}

// ---------------------------------------------------------------------------
// MEDIUM (custo/observabilidade): o custo por passo é contado UMA SÓ vez através da
// fronteira de kill/resume. O turno 2 é re-executado no worker B mas o seu efeito é
// DEDUPLICADO pelo ledger (applied==false) — o span desse turno NÃO anota custo, pelo
// que a agregação de custo (AttrCostMicroUSD) não sobre-contabiliza o passo de
// fronteira. O mesmo setup do teste de kill (crash entre ledger.Apply e Checkpoint do
// turno 2).
// ---------------------------------------------------------------------------

func TestWorker_ResumedStep_CostCountedOnce(t *testing.T) {
	t.Parallel()
	const runID = "run_cost_once"
	const nSteps = 4
	// attrStepApplied é o rótulo booleano que SÓ o span do worker traz (o RM não o
	// emite) — usamo-lo para isolar os spans do worker dos de mediação.
	const attrStepApplied = "aos.worker.step.applied"
	store := newStore(t)
	clock := newManualClock()
	ctr := &effectCounter{}

	// Worker A processa 2 passos e "morre" APÓS o efeito do turno 2 mas ANTES do seu
	// checkpoint (dropTurn=2) → a retoma cai no turno 2 e re-executa-o.
	procA := newProc(t, store, clock, ctr, "worker-A")
	wA := procA.worker(t, &dropCheckpointer{inner: procA.cpr, dropTurn: 2}, otelgenai.NoopTracer{})
	if _, err := wA.Run(context.Background(), plan(runID, 2)); err != nil {
		t.Fatalf("worker A Run: %v", err)
	}

	clock.Advance(leaseTTL + time.Second)

	// Worker B retoma no turno 2 (deduplicado), executa 3 e 4. Tracer a gravar para
	// inspeccionar o custo emitido por passo.
	procB := newProc(t, store, clock, ctr, "worker-B")
	tracerB := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	outB, err := procB.worker(t, nil, tracerB).Run(context.Background(), plan(runID, nSteps))
	if err != nil {
		t.Fatalf("worker B Run: %v", err)
	}
	if outB.ResumeTurn != 2 || outB.Skipped != 1 {
		t.Fatalf("B devia retomar no turno 2 (1 saltado): %+v", outB)
	}

	// Inspecção dos spans do worker B: exactamente UM span com applied==false (o turno 2
	// deduplicado) e SEM custo; os restantes applied==true COM custo. A soma do custo
	// exclui o turno de fronteira re-executado.
	var total int64
	appliedTrue, appliedFalse := 0, 0
	for _, s := range tracerB.SpansByOperation(otelgenai.OpExecuteTool) {
		ap, isWorkerSpan := s.Attributes[attrStepApplied]
		if !isWorkerSpan {
			continue // span de mediação do RM (não é do worker)
		}
		applied, ok := ap.(bool)
		if !ok {
			t.Fatalf("attrStepApplied não é bool: %v", ap)
		}
		cost, hasCost := s.Attributes[otelgenai.AttrCostMicroUSD]
		if applied {
			appliedTrue++
			c, ok := cost.(int64)
			if !hasCost || !ok {
				t.Fatalf("passo aplicado sem custo no span: %+v", s.Attributes)
			}
			total += c
		} else {
			appliedFalse++
			if hasCost {
				t.Fatalf("passo DEDUPLICADO (applied=false) não devia emitir custo: %v", cost)
			}
		}
	}
	if appliedFalse != 1 {
		t.Fatalf("esperava 1 passo deduplicado (turno 2), obtive %d", appliedFalse)
	}
	if appliedTrue != 2 {
		t.Fatalf("esperava 2 passos aplicados (turnos 3 e 4), obtive %d", appliedTrue)
	}
	// Custo do turno 3 (3000) + turno 4 (4000); o turno 2 (2000) NÃO re-contabiliza.
	const wantCost = int64(3*1000 + 4*1000)
	if total != wantCost {
		t.Fatalf("custo sobre-contabilizado na fronteira de retoma: esperava %d, obtive %d", wantCost, total)
	}
}

// ---------------------------------------------------------------------------
// AC1 (variante pura de dedup do ledger): reprocessar o MESMO plano no MESMO run
// não produz efeitos novos — o resume salta tudo e, mesmo forçando re-Apply, o
// ledger deduplica.
// ---------------------------------------------------------------------------

func TestWorker_ReprocessCompletedRun_NoNewEffects(t *testing.T) {
	t.Parallel()
	const runID = "run_reprocess"
	const nSteps = 3
	store := newStore(t)
	clock := newManualClock()
	ctr := &effectCounter{}

	p1 := newProc(t, store, clock, ctr, "w1")
	if _, err := p1.worker(t, nil, otelgenai.NoopTracer{}).Run(context.Background(), plan(runID, nSteps)); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if got := atomic.LoadInt64(&ctr.total); got != nSteps {
		t.Fatalf("run 1: esperava %d efeitos, obtive %d", nSteps, got)
	}

	clock.Advance(leaseTTL + time.Second)

	// Segundo processo reprocessa: resume acha tudo confirmado → salta todos.
	p2 := newProc(t, store, clock, ctr, "w2")
	out, err := p2.worker(t, nil, otelgenai.NoopTracer{}).Run(context.Background(), plan(runID, nSteps))
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if out.Skipped != nSteps || out.Executed != 0 {
		t.Fatalf("run 2 devia saltar todos: %+v", out)
	}
	if got := atomic.LoadInt64(&ctr.total); got != nSteps {
		t.Fatalf("run 2: efeitos duplicados — esperava %d no total, obtive %d", nSteps, got)
	}
}

// ---------------------------------------------------------------------------
// AC4: reatribuição de partição — um worker SUPERADO (token OBSOLETO) vê
// ErrStaleFencingToken e a sua escrita NÃO entra no log (sem dupla execução).
// ---------------------------------------------------------------------------

func TestWorker_SupersededWriterFencedOut(t *testing.T) {
	t.Parallel()
	const runID = "run_fenced"
	store := newStore(t)
	clock := newManualClock()
	ctr := &effectCounter{}

	// Worker A reclama a partição (token 1).
	procA := newProc(t, store, clock, ctr, "worker-A")
	leaseA, err := procA.lm.Claim(context.Background(), runID)
	if err != nil {
		t.Fatalf("Claim A: %v", err)
	}

	// O lease de A expira; worker B reclama a MESMA partição (token 2 > 1).
	clock.Advance(leaseTTL + time.Second)
	procB := newProc(t, store, clock, ctr, "worker-B")
	leaseB, err := procB.lm.Claim(context.Background(), runID)
	if err != nil {
		t.Fatalf("Claim B: %v", err)
	}
	if !(leaseB.Token.Value() > leaseA.Token.Value()) {
		t.Fatalf("token de B (%d) devia superar o de A (%d)", leaseB.Token.Value(), leaseA.Token.Value())
	}

	// A, superado, tenta servir a partição com o lease OBSOLETO (Adopt com o lease de
	// A). O gate fenced rejeita a primeira escrita: ErrLeaseLost, sem efeito.
	wA := procA.worker(t, nil, otelgenai.NoopTracer{})
	out, err := wA.Adopt(context.Background(), plan(runID, 3), leaseA)
	if !errors.Is(err, worker.ErrLeaseLost) {
		t.Fatalf("A superado devia falhar com ErrLeaseLost, obtive %v (out=%+v)", err, out)
	}

	// A escrita obsoleta de A NÃO entrou no log e NENHUM efeito correu.
	if n := countType(t, store, runID, worker.EventTypeWorkerStep); n != 0 {
		t.Fatalf("a escrita fenced-out de A entrou no log (%d marcadores)", n)
	}
	if got := atomic.LoadInt64(&ctr.total); got != 0 {
		t.Fatalf("worker superado produziu %d efeitos (esperava 0)", got)
	}
}

// ---------------------------------------------------------------------------
// AC3/AC5: scale-in N→N-1 sob o modelo de posse. Uma réplica detém a partição; ao
// ser terminada, o lease expira e OUTRA réplica assume-a via Assigner e completa o
// run resume-from-step, sem perda nem duplicação. AC3 também: uma réplica não rouba
// uma partição de lease vivo.
// ---------------------------------------------------------------------------

func TestWorker_ScaleIn_PartitionReassignedViaAssigner(t *testing.T) {
	t.Parallel()
	const runID = "run_scalein"
	const nSteps = 4
	store := newStore(t)
	clock := newManualClock()
	ctr := &effectCounter{}

	// Réplica 1 assume a partição (Assigner) e processa 2 passos, depois "morre"
	// (checkpoint do turno 3 e seguintes engolidos → não persistem).
	p1 := newProc(t, store, clock, ctr, "replica-1")
	asg1, err := worker.NewAssigner(p1.lm)
	if err != nil {
		t.Fatalf("NewAssigner 1: %v", err)
	}
	lease1, ok, err := asg1.TryAcquire(context.Background(), runID)
	if err != nil || !ok {
		t.Fatalf("réplica 1 devia adquirir a partição: ok=%v err=%v", ok, err)
	}

	// AC3: enquanto o lease de 1 é vivo, a réplica 2 NÃO rouba a partição.
	p2 := newProc(t, store, clock, ctr, "replica-2")
	asg2, err := worker.NewAssigner(p2.lm)
	if err != nil {
		t.Fatalf("NewAssigner 2: %v", err)
	}
	if _, ok2, err := asg2.TryAcquire(context.Background(), runID); err != nil || ok2 {
		t.Fatalf("réplica 2 NÃO devia roubar um lease vivo (ok=%v err=%v)", ok2, err)
	}

	w1 := p1.worker(t, nil, otelgenai.NoopTracer{})
	// A réplica 1 processa até "morrer": para simular scale-in a meio, damos-lhe um
	// plano PARCIAL (2 passos) — completa o que consegue e larga a partição.
	if _, err := w1.Adopt(context.Background(), plan(runID, 2), lease1); err != nil {
		t.Fatalf("réplica 1 Adopt: %v", err)
	}
	asg1.Release(runID) // scale-in: a réplica 1 larga a posse em-processo (o lease expira)

	if got := atomic.LoadInt64(&ctr.total); got != 2 {
		t.Fatalf("após réplica 1: esperava 2 efeitos, obtive %d", got)
	}

	// O lease de 1 expira; a réplica 2 assume a partição e completa o run inteiro.
	clock.Advance(leaseTTL + time.Second)
	lease2, ok, err := asg2.TryAcquire(context.Background(), runID)
	if err != nil || !ok {
		t.Fatalf("réplica 2 devia assumir a partição expirada: ok=%v err=%v", ok, err)
	}
	if !(lease2.Token.Value() > lease1.Token.Value()) {
		t.Fatalf("token da réplica 2 (%d) devia superar o da 1 (%d)", lease2.Token.Value(), lease1.Token.Value())
	}
	w2 := p2.worker(t, nil, otelgenai.NoopTracer{})
	out, err := w2.Adopt(context.Background(), plan(runID, nSteps), lease2)
	if err != nil {
		t.Fatalf("réplica 2 Adopt: %v", err)
	}
	if !out.Completed {
		t.Fatalf("réplica 2 não completou o run: %+v", out)
	}

	// Sem perda nem duplicação: os 4 passos, exactamente uma vez cada.
	if got := atomic.LoadInt64(&ctr.total); got != nSteps {
		t.Fatalf("scale-in perdeu/duplicou trabalho: esperava %d efeitos, obtive %d", nSteps, got)
	}
	for i := 1; i <= nSteps; i++ {
		if got := ctr.count(fmt.Sprintf("payload-%d", i)); got != 1 {
			t.Fatalf("passo %d correu %d vezes (esperava 1)", i, got)
		}
	}
}

// ---------------------------------------------------------------------------
// AC3/AC5: scale-out N→N+1 — uma réplica NOVA entra e assume uma partição LIVRE sem
// perturbar a partição de lease vivo já detida por outra réplica (sharding natural,
// sem rebalancing disruptivo). Ambas completam os seus runs em paralelo, sem
// interferência nem duplicação cruzada.
// ---------------------------------------------------------------------------

func TestWorker_ScaleOut_NewReplicaTakesFreePartition(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	clock := newManualClock()
	ctr := &effectCounter{}
	ctx := context.Background()

	// Réplica 1 detém a partição run_a (lease vivo).
	p1 := newProc(t, store, clock, ctr, "replica-1")
	asg1, err := worker.NewAssigner(p1.lm)
	if err != nil {
		t.Fatalf("NewAssigner 1: %v", err)
	}
	lease1, ok, err := asg1.TryAcquire(ctx, "run_a")
	if err != nil || !ok {
		t.Fatalf("réplica 1 devia adquirir run_a: ok=%v err=%v", ok, err)
	}

	// Scale-out: a réplica 2 entra. NÃO rouba a partição viva run_a (AC3) e assume uma
	// partição LIVRE distinta (run_b) — sem rebalancing da posse existente.
	p2 := newProc(t, store, clock, ctr, "replica-2")
	asg2, err := worker.NewAssigner(p2.lm)
	if err != nil {
		t.Fatalf("NewAssigner 2: %v", err)
	}
	if _, ok2, err := asg2.TryAcquire(ctx, "run_a"); err != nil || ok2 {
		t.Fatalf("réplica 2 NÃO devia roubar a partição viva run_a (ok=%v err=%v)", ok2, err)
	}
	lease2, ok, err := asg2.TryAcquire(ctx, "run_b")
	if err != nil || !ok {
		t.Fatalf("réplica 2 devia assumir a partição livre run_b: ok=%v err=%v", ok, err)
	}

	// Ambas as réplicas completam os seus runs, sem interferência.
	outA, err := p1.worker(t, nil, otelgenai.NoopTracer{}).Adopt(ctx, plan("run_a", 2), lease1)
	if err != nil || !outA.Completed {
		t.Fatalf("réplica 1 devia completar run_a: out=%+v err=%v", outA, err)
	}
	outB, err := p2.worker(t, nil, otelgenai.NoopTracer{}).Adopt(ctx, plan("run_b", 3), lease2)
	if err != nil || !outB.Completed {
		t.Fatalf("réplica 2 devia completar run_b: out=%+v err=%v", outB, err)
	}

	// 2 efeitos em run_a + 3 em run_b = 5, exactamente uma vez cada — sem perda nem
	// duplicação com a entrada da nova réplica.
	if got := atomic.LoadInt64(&ctr.total); got != 5 {
		t.Fatalf("scale-out perdeu/duplicou trabalho: esperava 5 efeitos, obtive %d", got)
	}
	if !asg1.Owns("run_a") || !asg2.Owns("run_b") {
		t.Fatalf("cada réplica devia manter a sua partição: owns(a)=%v owns(b)=%v", asg1.Owns("run_a"), asg2.Owns("run_b"))
	}
}

// ---------------------------------------------------------------------------
// AC2: ausência de estado durável no processo — um worker NOVO reconstrói o ponto
// de retoma inteiramente a partir do log; nada é herdado do processo anterior.
// ---------------------------------------------------------------------------

func TestWorker_StatelessReconstructionFromLog(t *testing.T) {
	t.Parallel()
	const runID = "run_stateless"
	const nSteps = 3
	store := newStore(t)
	clock := newManualClock()
	ctr := &effectCounter{}

	// Processo 1 completa o run.
	p1 := newProc(t, store, clock, ctr, "p1")
	if _, err := p1.worker(t, nil, otelgenai.NoopTracer{}).Run(context.Background(), plan(runID, nSteps)); err != nil {
		t.Fatalf("processo 1: %v", err)
	}

	clock.Advance(leaseTTL + time.Second)

	// Processo 2 — primitivos ENTEIRAMENTE novos, partilhando apenas o *eventstore.Store
	// (o log durável). Resume reconstrói a fronteira só do log: tudo confirmado.
	p2 := newProc(t, store, clock, ctr, "p2")
	rp, err := p2.resume.Resume(context.Background(), runID)
	if err != nil {
		t.Fatalf("Resume no processo novo: %v", err)
	}
	if rp.FromScratch || rp.NextTurn != nSteps+1 {
		t.Fatalf("o processo novo não reconstruiu a fronteira do log: %+v", rp)
	}
	// E servir de novo salta tudo (0 efeitos novos) — o estado vivia no log.
	out, err := p2.worker(t, nil, otelgenai.NoopTracer{}).Run(context.Background(), plan(runID, nSteps))
	if err != nil {
		t.Fatalf("processo 2 Run: %v", err)
	}
	if out.Skipped != nSteps || out.Executed != 0 {
		t.Fatalf("o processo novo devia saltar tudo a partir do log: %+v", out)
	}
	if got := atomic.LoadInt64(&ctr.total); got != nSteps {
		t.Fatalf("o processo novo re-executou efeitos: total=%d (esperava %d)", got, nSteps)
	}
}

// ---------------------------------------------------------------------------
// DoD: spans OTel GenAI com custo POR PASSO emitidos pelo worker, e toda a tool
// call mediada pelo Reference Monitor.
// ---------------------------------------------------------------------------

func TestWorker_EmitsPerStepCostSpans_AndMediates(t *testing.T) {
	t.Parallel()
	const runID = "run_spans"
	const nSteps = 3
	store := newStore(t)
	clock := newManualClock()
	ctr := &effectCounter{}

	p := newProc(t, store, clock, ctr, "w")
	tracer := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	out, err := p.worker(t, nil, tracer).Run(context.Background(), plan(runID, nSteps))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !out.Completed || out.Executed != nSteps {
		t.Fatalf("execução inesperada: %+v", out)
	}

	// O worker emite um span execute_tool por passo COM custo (AttrCostMicroUSD),
	// run_id e step_id. (O RM emite os SEUS próprios spans de mediação; filtramos os
	// que trazem o custo por passo — a marca do span do worker.)
	var withCost int
	seenSteps := map[string]bool{}
	for _, s := range tracer.SpansByOperation(otelgenai.OpExecuteTool) {
		cost, hasCost := s.Attributes[otelgenai.AttrCostMicroUSD]
		if !hasCost {
			continue // span de mediação do RM (sem custo por passo)
		}
		withCost++
		if !s.Ended {
			t.Fatalf("span do worker não foi fechado: %+v", s)
		}
		if _, ok := s.Attributes[otelgenai.AttrRunID]; !ok {
			t.Fatalf("span do worker sem AttrRunID: %+v", s.Attributes)
		}
		sid, ok := s.Attributes[otelgenai.AttrStepID].(string)
		if !ok || sid == "" {
			t.Fatalf("span do worker sem AttrStepID: %+v", s.Attributes)
		}
		seenSteps[sid] = true
		if c, ok := cost.(int64); !ok || c <= 0 {
			t.Fatalf("AttrCostMicroUSD inválido no span: %v", cost)
		}
	}
	if withCost != nSteps {
		t.Fatalf("esperava %d spans de custo por passo, obtive %d", nSteps, withCost)
	}
	if len(seenSteps) != nSteps {
		t.Fatalf("esperava %d step_ids distintos nos spans, obtive %d", nSteps, len(seenSteps))
	}

	// Toda a tool call foi mediada pelo RM: um evento de mediação por passo no log.
	if n := countType(t, store, runID, "tool.call.mediated"); n != nSteps {
		t.Fatalf("esperava %d eventos de mediação (RM), obtive %d", nSteps, n)
	}
}

// ---------------------------------------------------------------------------
// Heartbeat: renova o lease enquanto o worker detém a partição; uma renovação
// recusada (superado por um novo claim) PARA o worker fail-closed.
// ---------------------------------------------------------------------------

func TestWorker_HeartbeatLoss_StopsFailClosed(t *testing.T) {
	t.Parallel()
	const runID = "run_hb"
	store := newStore(t)
	clock := newManualClock()
	ctr := &effectCounter{}

	// Ticker manual: o teste controla quando o heartbeat dispara.
	tickCh := make(chan time.Time, 1)
	factory := func(time.Duration) (<-chan time.Time, func()) { return tickCh, func() {} }

	// Tool que bloqueia no primeiro passo até o teste libertar — para intercalar a
	// perda de lease ANTES do gate fenced do passo seguinte.
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	p := newProc(t, store, clock, ctr, "worker-A")
	// Re-regista uma tool bloqueante substituindo a echo do proc (novo RM dedicado).
	rm := referencemonitorNew(store)
	registerBlocking(t, rm, ctr, entered, release)
	w, err := worker.NewWorker(p.lm, p.fenced, p.ledger, p.resume, p.cpr, rm,
		worker.WithStepSequencer(p.seq),
		worker.WithHeartbeatInterval(time.Hour),
		worker.WithTickerFactory(factory),
	)
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}

	done := make(chan error, 1)
	go func() { _, e := w.Run(context.Background(), plan(runID, 3)); done <- e }()

	<-entered // o worker está no efeito do turno 1 (lease detido, token 1)

	// Enquanto o worker está preso no turno 1, uma nova réplica supera-o: o lease
	// expira e alguém reclama token 2.
	clock.Advance(leaseTTL + time.Second)
	other, err := durable.NewLeaseManager(store, leaseTTL, durable.WithLeaseClock(clock))
	if err != nil {
		t.Fatalf("other LM: %v", err)
	}
	if _, err := other.Claim(context.Background(), runID); err != nil {
		t.Fatalf("claim concorrente: %v", err)
	}

	// Dispara o heartbeat: a renovação é recusada (superado) → o worker para fail-closed.
	tickCh <- clock.Now()
	// Liberta o efeito do turno 1 para o loop poder avançar e detectar a perda.
	close(release)

	select {
	case e := <-done:
		if !errors.Is(e, worker.ErrLeaseLost) {
			t.Fatalf("esperava ErrLeaseLost após perda de heartbeat, obtive %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("o worker não parou após a perda de lease (deadlock?)")
	}

	// O turno 1 pode ter aplicado o seu efeito (uma vez); os turnos seguintes NÃO
	// escrevem após a perda — o gate fenced do turno 2 é rejeitado (token 1 < 2).
	if n := countType(t, store, runID, worker.EventTypeWorkerStep); n > 1 {
		t.Fatalf("worker escreveu %d marcadores após perder o lease (esperava <= 1)", n)
	}
}

// ---------------------------------------------------------------------------
// AC4 (posse por lease, nunca PID) + higiene: um lease vivo detido não é reclamável.
// ---------------------------------------------------------------------------

func TestWorker_RunHeldPartitionReturnsLeaseHeld(t *testing.T) {
	t.Parallel()
	const runID = "run_held"
	store := newStore(t)
	clock := newManualClock()
	ctr := &effectCounter{}

	p := newProc(t, store, clock, ctr, "owner")
	if _, err := p.lm.Claim(context.Background(), runID); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// Outra réplica tenta Run sobre a mesma partição com lease vivo → ErrLeaseHeld.
	other := newProc(t, store, clock, ctr, "intruder")
	_, err := other.worker(t, nil, otelgenai.NoopTracer{}).Run(context.Background(), plan(runID, 2))
	if !errors.Is(err, durable.ErrLeaseHeld) {
		t.Fatalf("esperava ErrLeaseHeld sobre partição viva, obtive %v", err)
	}
	if got := atomic.LoadInt64(&ctr.total); got != 0 {
		t.Fatalf("intruso produziu %d efeitos (esperava 0)", got)
	}
}

// ---------------------------------------------------------------------------
// Construção: dependências obrigatórias.
// ---------------------------------------------------------------------------

func TestNewWorker_NilDeps(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	clock := newManualClock()
	ctr := &effectCounter{}
	p := newProc(t, store, clock, ctr, "w")

	if _, err := worker.NewWorker(nil, p.fenced, p.ledger, p.resume, p.cpr, p.rm); !errors.Is(err, worker.ErrNilLeaseManager) {
		t.Fatalf("esperava ErrNilLeaseManager, obtive %v", err)
	}
	if _, err := worker.NewWorker(p.lm, nil, p.ledger, p.resume, p.cpr, p.rm); !errors.Is(err, worker.ErrNilFencedAppender) {
		t.Fatalf("esperava ErrNilFencedAppender, obtive %v", err)
	}
	if _, err := worker.NewWorker(p.lm, p.fenced, nil, p.resume, p.cpr, p.rm); !errors.Is(err, worker.ErrNilLedger) {
		t.Fatalf("esperava ErrNilLedger, obtive %v", err)
	}
	if _, err := worker.NewWorker(p.lm, p.fenced, p.ledger, nil, p.cpr, p.rm); !errors.Is(err, worker.ErrNilResumer) {
		t.Fatalf("esperava ErrNilResumer, obtive %v", err)
	}
	if _, err := worker.NewWorker(p.lm, p.fenced, p.ledger, p.resume, nil, p.rm); !errors.Is(err, worker.ErrNilCheckpointer) {
		t.Fatalf("esperava ErrNilCheckpointer, obtive %v", err)
	}
	if _, err := worker.NewWorker(p.lm, p.fenced, p.ledger, p.resume, p.cpr, nil); !errors.Is(err, worker.ErrNilMonitor) {
		t.Fatalf("esperava ErrNilMonitor, obtive %v", err)
	}
}
