package scheduler_test

// Testes do scheduling priority-aware + aging (AOS-032). Todos deterministas:
// relógio injectável, sem time.Now nem rand na decisão. Cobrem os Testes
// Requeridos do ticket:
//   - unit: ordenação por prioridade;
//   - unit: aging PROMOVE trabalho antigo de baixa prioridade acima de trabalho
//     NOVO de maior prioridade;
//   - unit: AUSÊNCIA DE STARVATION num cenário adversarial (fluxo contínuo de alta
//     prioridade — o antigo de baixa prioridade É eventualmente despachado);
//   - unit: latency-aware (o SLO altera a decisão, não só a prioridade nominal);
//   - integração: despacho APENAS de trabalho admitido (integra AOS-027);
//   - integração: ordem REPRODUZÍVEL em replay (byte-a-byte);
//   - parâmetros de aging por classe/tenant respeitados.
//
// Reutiliza helpers de admission_test.go (mesmo pacote _test): fixedClock,
// mutClock, seqIDGen, testKey, qpTPM, newAdm.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/scheduler"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// classOpts devolve as opções de classes P0>P1>P2 com aging step/interval dados.
func classOpts(step int64, interval time.Duration) []scheduler.DispatcherOption {
	return []scheduler.DispatcherOption{
		scheduler.WithClassAging("P0", scheduler.AgingParams{Base: 300, AgingStep: step, AgingInterval: interval}),
		scheduler.WithClassAging("P1", scheduler.AgingParams{Base: 200, AgingStep: step, AgingInterval: interval}),
		scheduler.WithClassAging("P2", scheduler.AgingParams{Base: 100, AgingStep: step, AgingInterval: interval}),
	}
}

// mustDispatcher constrói um dispatcher de teste, falhando o teste em erro. Por
// omissão declara ordenação pura (WithoutAdmission); se as opções incluírem
// WithAdmission, essa decisão sobrepõe-se (ambas registam a decisão explícita).
func mustDispatcher(t *testing.T, opts ...scheduler.DispatcherOption) *scheduler.Dispatcher {
	t.Helper()
	full := append([]scheduler.DispatcherOption{scheduler.WithoutAdmission()}, opts...)
	d, err := scheduler.NewDispatcher(scheduler.AgingParams{Base: 0, AgingStep: 1, AgingInterval: time.Second}, full...)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return d
}

// ---------------------------------------------------------------------------
// Unit: ordenação por prioridade (mesma idade ⇒ P0 antes de P1 antes de P2).
// ---------------------------------------------------------------------------

func TestDispatch_OrdersByPriority(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	d := mustDispatcher(t, append(classOpts(0, 0), scheduler.WithDispatchClock(fixedClock(base)))...)
	ctx := context.Background()

	// Submete FORA de ordem de prioridade (prova que a ordenação não é FIFO de
	// submissão): P2, depois P0, depois P1.
	for _, tk := range []scheduler.Task{
		{ID: "t-p2", Tenant: "acme", Class: "P2"},
		{ID: "t-p0", Tenant: "acme", Class: "P0"},
		{ID: "t-p1", Tenant: "acme", Class: "P1"},
	} {
		if _, err := d.Submit(ctx, tk); err != nil {
			t.Fatalf("Submit %s: %v", tk.ID, err)
		}
	}

	want := []string{"t-p0", "t-p1", "t-p2"}
	for i, w := range want {
		res, err := d.Dispatch(ctx)
		if err != nil {
			t.Fatalf("Dispatch[%d]: %v", i, err)
		}
		if !res.Dispatched {
			t.Fatalf("Dispatch[%d] não despachou", i)
		}
		if res.Task.ID != w {
			t.Fatalf("Dispatch[%d]=%q, esperado %q (ordem por prioridade)", i, res.Task.ID, w)
		}
		if res.Aged {
			t.Fatalf("Dispatch[%d] marcado Aged sem aging (idade 0)", i)
		}
	}
	if d.Pending() != 0 {
		t.Fatalf("Pending=%d, esperado 0", d.Pending())
	}
}

// ---------------------------------------------------------------------------
// Unit: aging PROMOVE trabalho antigo de baixa prioridade acima de trabalho NOVO
// de maior prioridade.
// ---------------------------------------------------------------------------

func TestDispatch_AgingPromotesOldLowOverNewHigh(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(1_000_000, 0))
	// AgingStep 10/s: após 100s, P2 (base 100) sobe +1000 ⇒ 1100 > P0 novo (300).
	d := mustDispatcher(t, append(classOpts(10, time.Second), scheduler.WithDispatchClock(clk.now))...)
	ctx := context.Background()

	if _, err := d.Submit(ctx, scheduler.Task{ID: "old-p2", Tenant: "acme", Class: "P2"}); err != nil {
		t.Fatalf("Submit old-p2: %v", err)
	}
	clk.advance(100 * time.Second) // o P2 envelhece
	if _, err := d.Submit(ctx, scheduler.Task{ID: "new-p0", Tenant: "acme", Class: "P0"}); err != nil {
		t.Fatalf("Submit new-p0: %v", err)
	}

	res, err := d.Dispatch(ctx)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !res.Dispatched || res.Task.ID != "old-p2" {
		t.Fatalf("despachou %+v, esperado old-p2 promovido por aging acima do P0 novo", res)
	}
	if !res.Aged {
		t.Fatalf("esperado Aged=true (promoção por aging)")
	}
	if res.EffectivePriority <= res.BasePriority {
		t.Fatalf("efectiva %d não excede base %d (sem promoção)", res.EffectivePriority, res.BasePriority)
	}
	// O P0 novo só sai depois.
	res2, err := d.Dispatch(ctx)
	if err != nil {
		t.Fatalf("Dispatch2: %v", err)
	}
	if !res2.Dispatched || res2.Task.ID != "new-p0" {
		t.Fatalf("segundo despacho %+v, esperado new-p0", res2)
	}
}

// ---------------------------------------------------------------------------
// Unit: AUSÊNCIA DE STARVATION num cenário ADVERSARIAL — fluxo CONTÍNUO de alta
// prioridade; o antigo de baixa prioridade É eventualmente despachado.
// ---------------------------------------------------------------------------

func TestDispatch_NoStarvationAdversarial(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(1_000_000, 0))
	// P0 base 1000; P2 base 0, aging 1/s. A vítima P2 tem de esperar ~1000s para
	// ultrapassar o fluxo P0, mas ultrapassa SEMPRE (aging sem tecto).
	d := mustDispatcher(t,
		scheduler.WithClassAging("P0", scheduler.AgingParams{Base: 1000, AgingStep: 1, AgingInterval: time.Second}),
		scheduler.WithClassAging("P2", scheduler.AgingParams{Base: 0, AgingStep: 1, AgingInterval: time.Second}),
		scheduler.WithDispatchClock(clk.now),
	)
	ctx := context.Background()

	// A vítima entra no instante 0 e nunca mais recebe prioridade nominal.
	if _, err := d.Submit(ctx, scheduler.Task{ID: "victim-p2", Tenant: "acme", Class: "P2"}); err != nil {
		t.Fatalf("Submit victim: %v", err)
	}

	const maxIters = 3000
	victimDispatched := false
	for i := 0; i < maxIters; i++ {
		// Fluxo adversarial contínuo: injecta um P0 fresco a cada ronda.
		if _, err := d.Submit(ctx, scheduler.Task{ID: fmt.Sprintf("flood-p0-%d", i), Tenant: "acme", Class: "P0"}); err != nil {
			t.Fatalf("Submit flood %d: %v", i, err)
		}
		clk.advance(time.Second)
		res, err := d.Dispatch(ctx)
		if err != nil {
			t.Fatalf("Dispatch %d: %v", i, err)
		}
		if res.Dispatched && res.Task.ID == "victim-p2" {
			victimDispatched = true
			if !res.Aged {
				t.Fatalf("vítima despachada sem Aged=true (deveria ter sido promovida por aging)")
			}
			t.Logf("vítima despachada na iteração %d (idade %d ms, efectiva %d)", i, res.WaitMs, res.EffectivePriority)
			break
		}
	}
	if !victimDispatched {
		t.Fatalf("STARVATION: vítima P2 nunca despachada em %d iterações de fluxo P0", maxIters)
	}
}

// ---------------------------------------------------------------------------
// Unit: latency-aware — o SLO altera a decisão, não só a prioridade nominal.
// Duas tarefas da MESMA classe e MESMA idade; a de SLO mais apertado vence.
// ---------------------------------------------------------------------------

func TestDispatch_LatencyAwareSLO(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(1_000_000, 0))
	// Aging desligado (step 0) para ISOLAR o efeito do SLO; SLOWeight domina.
	d := mustDispatcher(t,
		scheduler.WithClassAging("P1", scheduler.AgingParams{Base: 200, AgingStep: 0, SLOWeight: 1000}),
		scheduler.WithDispatchClock(clk.now),
	)
	ctx := context.Background()

	// Ambas P1, submetidas no mesmo instante. A "tight" tem SLO 10s; a "loose" 100s.
	if _, err := d.Submit(ctx, scheduler.Task{ID: "loose", Tenant: "acme", Class: "P1", SLO: 100 * time.Second}); err != nil {
		t.Fatalf("Submit loose: %v", err)
	}
	if _, err := d.Submit(ctx, scheduler.Task{ID: "tight", Tenant: "acme", Class: "P1", SLO: 10 * time.Second}); err != nil {
		t.Fatalf("Submit tight: %v", err)
	}
	clk.advance(5 * time.Second) // consome 50% do SLO da tight, 5% do da loose

	res, err := d.Dispatch(ctx)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !res.Dispatched || res.Task.ID != "tight" {
		t.Fatalf("despachou %+v, esperado 'tight' (latency-aware: SLO apertado prevalece a igual prioridade nominal)", res)
	}
}

// ---------------------------------------------------------------------------
// Integração: despacho APENAS de trabalho ADMITIDO (integra AOS-027).
// ---------------------------------------------------------------------------

func TestDispatch_OnlyAdmittedDispatched(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	// TPM 300, custo 100/tarefa ⇒ exactamente 3 admissões; janela grande e relógio
	// congelado ⇒ nada de refill durante o teste.
	qp := qpTPM(300, 1_000_000, time.Hour)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithIDGen(seqIDGen()),
	)
	d := mustDispatcher(t, append(classOpts(0, 0),
		scheduler.WithDispatchClock(fixedClock(base)),
		scheduler.WithAdmission(adm),
		scheduler.WithDefaultKey(testKey),
	)...)
	ctx := context.Background()

	const n = 5
	for i := 0; i < n; i++ {
		if _, err := d.Submit(ctx, scheduler.Task{ID: fmt.Sprintf("job-%d", i), Tenant: "acme", Class: "P1", Cost: 100}); err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
	}

	granted, deferred := 0, 0
	for i := 0; i < n; i++ {
		res, err := d.Dispatch(ctx)
		if err != nil {
			t.Fatalf("Dispatch %d: %v", i, err)
		}
		if res.Dispatched {
			granted++
			if res.ReservationID == "" {
				t.Fatalf("Dispatch %d admitido sem ReservationID (headroom não reservado)", i)
			}
		} else {
			deferred++
			if res.RetryAfter <= 0 {
				t.Fatalf("Dispatch %d adiado sem retry_after>0", i)
			}
		}
	}
	if granted != 3 {
		t.Fatalf("granted=%d, esperado 3 (TPM 300 / custo 100)", granted)
	}
	if deferred != 2 {
		t.Fatalf("deferred=%d, esperado 2", deferred)
	}
	// Os 2 não-admitidos PERMANECEM (não descartados): backpressure, não perda.
	if d.Pending() != 2 {
		t.Fatalf("Pending=%d, esperado 2 (trabalho adiado preservado)", d.Pending())
	}
}

// ---------------------------------------------------------------------------
// Integração: ordem REPRODUZÍVEL em replay (byte-a-byte). Duas execuções
// independentes com os mesmos inputs produzem os MESMOS bytes de evento na MESMA
// ordem.
// ---------------------------------------------------------------------------

func TestDispatch_ReplayByteForByte(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	run := func() [][]byte {
		es, err := eventstore.New()
		if err != nil {
			t.Fatalf("eventstore.New: %v", err)
		}
		clk := &mutClock{}
		clk.set(time.Unix(2_000_000, 0))
		d := mustDispatcher(t, append(classOpts(10, time.Second),
			scheduler.WithDispatchClock(clk.now),
			scheduler.WithDispatchLog(es),
		)...)
		// Cenário idêntico entre execuções: submete, envelhece, submete, despacha tudo.
		if _, err := d.Submit(ctx, scheduler.Task{ID: "a", Tenant: "acme", Class: "P2", SLO: 60 * time.Second}); err != nil {
			t.Fatalf("Submit a: %v", err)
		}
		clk.advance(50 * time.Second)
		if _, err := d.Submit(ctx, scheduler.Task{ID: "b", Tenant: "acme", Class: "P0"}); err != nil {
			t.Fatalf("Submit b: %v", err)
		}
		if _, err := d.Submit(ctx, scheduler.Task{ID: "c", Tenant: "acme", Class: "P1"}); err != nil {
			t.Fatalf("Submit c: %v", err)
		}
		for i := 0; i < 3; i++ {
			if _, err := d.Dispatch(ctx); err != nil {
				t.Fatalf("Dispatch %d: %v", i, err)
			}
		}
		evs, err := es.Read(ctx, d.DispatchStreamID(), 1)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		out := make([][]byte, 0, len(evs))
		for _, ev := range evs {
			out = append(out, append([]byte(ev.Type+"|"), ev.Payload...))
		}
		return out
	}

	a := run()
	b := run()
	if len(a) == 0 {
		t.Fatalf("sem eventos de despacho gerados")
	}
	if len(a) != len(b) {
		t.Fatalf("nº de eventos difere entre execuções: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatalf("evento %d difere byte-a-byte:\n  A=%s\n  B=%s", i, a[i], b[i])
		}
	}
}

// ---------------------------------------------------------------------------
// Parâmetros de aging por CLASSE/TENANT respeitados (override por especificidade).
// ---------------------------------------------------------------------------

func TestDispatch_AgingParamsPerClassTenant(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	// Override de tenant: para o tenant "vip", a classe P2 tem base 5000 (acima de
	// qualquer P0 regular). Prova que o override por (tenant,classe) é respeitado.
	d := mustDispatcher(t,
		scheduler.WithClassAging("P0", scheduler.AgingParams{Base: 300}),
		scheduler.WithClassAging("P2", scheduler.AgingParams{Base: 100}),
		scheduler.WithTenantClassAging("vip", "P2", scheduler.AgingParams{Base: 5000}),
		scheduler.WithDispatchClock(fixedClock(base)),
	)
	ctx := context.Background()

	if _, err := d.Submit(ctx, scheduler.Task{ID: "reg-p0", Tenant: "reg", Class: "P0"}); err != nil {
		t.Fatalf("Submit reg-p0: %v", err)
	}
	if _, err := d.Submit(ctx, scheduler.Task{ID: "vip-p2", Tenant: "vip", Class: "P2"}); err != nil {
		t.Fatalf("Submit vip-p2: %v", err)
	}

	res, err := d.Dispatch(ctx)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !res.Dispatched || res.Task.ID != "vip-p2" {
		t.Fatalf("despachou %+v, esperado vip-p2 (override de tenant Base=5000 domina)", res)
	}
	if res.BasePriority != 5000 {
		t.Fatalf("BasePriority=%d, esperado 5000 (parâmetro por tenant/classe)", res.BasePriority)
	}
}

// ---------------------------------------------------------------------------
// Integração com as filas particionadas (AOS-030): bounding + backpressure.
// ---------------------------------------------------------------------------

func TestDispatch_QueuesBoundingAndBackpressure(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	ctx := context.Background()

	pol, err := scheduler.NewPolicyEngine(scheduler.PolicyDoc{Version: "1.0.0", DefaultAction: scheduler.ActionShed})
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	q, err := scheduler.NewPartitionedQueues(
		scheduler.QueueLimits{MaxLen: 2, HighWatermark: 2, LowWatermark: 1},
		scheduler.WithQueuePolicy(pol),
		scheduler.WithQueueClock(fixedClock(base)),
	)
	if err != nil {
		t.Fatalf("NewPartitionedQueues: %v", err)
	}
	d := mustDispatcher(t, append(classOpts(0, 0),
		scheduler.WithDispatchClock(fixedClock(base)),
		scheduler.WithQueues(q),
	)...)

	// Duas cabem; a terceira satura ⇒ Queued=false com acção da política (shed).
	for i := 0; i < 2; i++ {
		res, err := d.Submit(ctx, scheduler.Task{ID: fmt.Sprintf("q-%d", i), Tenant: "acme", Class: "P1"})
		if err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
		if !res.Queued {
			t.Fatalf("Submit %d não enfileirado (deveria caber)", i)
		}
	}
	third, err := d.Submit(ctx, scheduler.Task{ID: "q-2", Tenant: "acme", Class: "P1"})
	if err != nil {
		t.Fatalf("Submit terceiro: %v", err)
	}
	if third.Queued {
		t.Fatalf("terceiro Submit enfileirado, esperado saturação (bounding)")
	}
	if third.Action != scheduler.ActionShed {
		t.Fatalf("acção=%q, esperado shed (política declarativa de AOS-030)", third.Action)
	}
	if d.Pending() != 2 {
		t.Fatalf("Pending=%d, esperado 2 (terceiro não indexado)", d.Pending())
	}

	part := scheduler.Partition{Tenant: "acme", Priority: "P1"}
	if got := q.Depth(part); got != 2 {
		t.Fatalf("Depth da fila=%d, esperado 2", got)
	}

	// Despachar liberta lugares na fila (a contagem desce).
	if _, err := d.Dispatch(ctx); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got := q.Depth(part); got != 1 {
		t.Fatalf("Depth após 1 dispatch=%d, esperado 1 (lugar libertado)", got)
	}
	// Agora há espaço: a re-submissão do q-2 é aceite.
	again, err := d.Submit(ctx, scheduler.Task{ID: "q-2", Tenant: "acme", Class: "P1"})
	if err != nil {
		t.Fatalf("Re-submit q-2: %v", err)
	}
	if !again.Queued {
		t.Fatalf("re-submit não enfileirado após libertar lugar")
	}
}

// ---------------------------------------------------------------------------
// ReplaySchedule reconstrói a sequência de despacho do Event Store, com o evento
// priority_aged marcado nas promoções.
// ---------------------------------------------------------------------------

func TestDispatch_ReplayScheduleReconstructs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	clk := &mutClock{}
	clk.set(time.Unix(3_000_000, 0))
	// Exercita WithDefaultAging/WithDispatchProducer/WithDispatchName além do log.
	d, err := scheduler.NewDispatcher(
		scheduler.AgingParams{Base: 50, AgingStep: 10, AgingInterval: time.Second},
		scheduler.WithDefaultAging(scheduler.AgingParams{Base: 50, AgingStep: 10, AgingInterval: time.Second}),
		scheduler.WithClassAging("P0", scheduler.AgingParams{Base: 300}),
		scheduler.WithClassAging("P2", scheduler.AgingParams{Base: 100, AgingStep: 10, AgingInterval: time.Second}),
		scheduler.WithDispatchClock(clk.now),
		scheduler.WithDispatchLog(es),
		scheduler.WithDispatchProducer(eventstore.Producer{NHIID: "nhi:test/dispatch"}),
		scheduler.WithDispatchName("replay-inst"),
		scheduler.WithoutAdmission(),
	)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}

	if _, err := d.Submit(ctx, scheduler.Task{ID: "old", Tenant: "acme", Class: "P2"}); err != nil {
		t.Fatalf("Submit old: %v", err)
	}
	clk.advance(30 * time.Second) // P2: 100 + 10*30 = 400 > P0 (300)
	if _, err := d.Submit(ctx, scheduler.Task{ID: "fresh", Tenant: "acme", Class: "P0"}); err != nil {
		t.Fatalf("Submit fresh: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := d.Dispatch(ctx); err != nil {
			t.Fatalf("Dispatch %d: %v", i, err)
		}
	}

	recs, err := d.ReplaySchedule(ctx)
	if err != nil {
		t.Fatalf("ReplaySchedule: %v", err)
	}
	// Espera: task_scheduled(old) + priority_aged(old) + task_scheduled(fresh).
	if len(recs) != 3 {
		t.Fatalf("recs=%d, esperado 3 (%+v)", len(recs), recs)
	}
	if recs[0].Type != scheduler.EventTaskScheduled || recs[0].TaskID != "old" {
		t.Fatalf("rec[0]=%+v, esperado task_scheduled(old)", recs[0])
	}
	if recs[1].Type != scheduler.EventPriorityAged || recs[1].TaskID != "old" || !recs[1].Aged {
		t.Fatalf("rec[1]=%+v, esperado priority_aged(old)", recs[1])
	}
	if recs[0].EffectivePriority <= recs[0].BasePriority {
		t.Fatalf("efectiva %d de old não excede base %d", recs[0].EffectivePriority, recs[0].BasePriority)
	}
	if recs[2].Type != scheduler.EventTaskScheduled || recs[2].TaskID != "fresh" {
		t.Fatalf("rec[2]=%+v, esperado task_scheduled(fresh)", recs[2])
	}

	// Sem log, ReplaySchedule devolve nil sem erro.
	d2 := mustDispatcher(t, classOpts(0, 0)...)
	if r, err := d2.ReplaySchedule(ctx); err != nil || r != nil {
		t.Fatalf("ReplaySchedule sem log: r=%v err=%v, esperado nil/nil", r, err)
	}
}

// ---------------------------------------------------------------------------
// Construção fail-closed: parâmetros de aging inválidos são rejeitados.
// ---------------------------------------------------------------------------

func TestNewDispatcher_ValidatesAgingParams(t *testing.T) {
	t.Parallel()
	// AgingStep>0 sem AgingInterval é inválido (fail-closed).
	if _, err := scheduler.NewDispatcher(scheduler.AgingParams{Base: 1, AgingStep: 5, AgingInterval: 0}); !errors.Is(err, scheduler.ErrInvalidAgingParams) {
		t.Fatalf("esperado ErrInvalidAgingParams para default inválido, obtido %v", err)
	}
	// Override de classe inválido também reprova.
	if _, err := scheduler.NewDispatcher(
		scheduler.AgingParams{Base: 0, AgingStep: 1, AgingInterval: time.Second},
		scheduler.WithClassAging("P0", scheduler.AgingParams{AgingStep: -1}),
	); !errors.Is(err, scheduler.ErrInvalidAgingParams) {
		t.Fatalf("esperado ErrInvalidAgingParams para classe inválida, obtido %v", err)
	}
}

// ---------------------------------------------------------------------------
// Submit rejeita task_id duplicado (fail-closed).
// ---------------------------------------------------------------------------

func TestDispatch_DuplicateTaskRejected(t *testing.T) {
	t.Parallel()
	d := mustDispatcher(t, classOpts(0, 0)...)
	ctx := context.Background()
	if _, err := d.Submit(ctx, scheduler.Task{ID: "dup", Tenant: "acme", Class: "P1"}); err != nil {
		t.Fatalf("primeiro Submit: %v", err)
	}
	if _, err := d.Submit(ctx, scheduler.Task{ID: "dup", Tenant: "acme", Class: "P1"}); !errors.Is(err, scheduler.ErrDuplicateTask) {
		t.Fatalf("esperado ErrDuplicateTask, obtido %v", err)
	}
}

// ---------------------------------------------------------------------------
// Determinismo sob concorrência (-race): Submit/Dispatch concorrentes não
// corrompem o índice; todo o trabalho é despachado exactamente uma vez.
// ---------------------------------------------------------------------------

func TestDispatch_ConcurrentRaceFree(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	tracer := &agentruntime.RecordingTracer{}
	d := mustDispatcher(t, append(classOpts(0, 0),
		scheduler.WithDispatchClock(fixedClock(base)),
		scheduler.WithDispatchTracer(tracer),
	)...)
	ctx := context.Background()

	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = d.Submit(ctx, scheduler.Task{ID: fmt.Sprintf("c-%d", i), Tenant: "acme", Class: "P1"})
		}(i)
	}
	wg.Wait()
	if d.Pending() != n {
		t.Fatalf("Pending=%d após submits, esperado %d", d.Pending(), n)
	}

	var mu sync.Mutex
	seen := make(map[string]int)
	dispatched := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := d.Dispatch(ctx)
			if err != nil {
				t.Errorf("Dispatch: %v", err)
				return
			}
			if res.Dispatched {
				mu.Lock()
				seen[res.Task.ID]++
				dispatched++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if dispatched != n {
		t.Fatalf("despachadas=%d, esperado %d", dispatched, n)
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("tarefa %q despachada %d vezes (esperado exactamente 1)", id, c)
		}
	}
	if d.Pending() != 0 {
		t.Fatalf("Pending=%d no fim, esperado 0", d.Pending())
	}
}

// ---------------------------------------------------------------------------
// Latency-aware por PRAZO (EDF): uma tarefa NOVA de SLO apertado ultrapassa uma
// ANTIGA de SLO folgado com MAIS folga real — sem inversão de prazo (finding
// slo-correctness). O aging é neutralizado (intervalo enorme) para ISOLAR o SLO.
// ---------------------------------------------------------------------------

func TestDispatch_DeadlineAwareNoInversion(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(1_000_000, 0))
	// AgingInterval enorme ⇒ aging ~0 no horizonte do teste (isola o termo de SLO).
	d, err := scheduler.NewDispatcher(
		scheduler.AgingParams{Base: 0, AgingStep: 1, AgingInterval: 1000 * time.Hour},
		scheduler.WithClassAging("P1", scheduler.AgingParams{Base: 200, SLOWeight: 1000}),
		scheduler.WithDispatchClock(clk.now),
		scheduler.WithoutAdmission(),
	)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	ctx := context.Background()

	// loose: SLO 100s, submetida primeiro e ENVELHECIDA 60s ⇒ 40s de folga real.
	if _, err := d.Submit(ctx, scheduler.Task{ID: "loose", Tenant: "acme", Class: "P1", SLO: 100 * time.Second}); err != nil {
		t.Fatalf("Submit loose: %v", err)
	}
	clk.advance(59800 * time.Millisecond)
	// tight: SLO 2s, acabada de submeter ⇒ ficará com ~1.8s de folga (prestes a falhar).
	if _, err := d.Submit(ctx, scheduler.Task{ID: "tight", Tenant: "acme", Class: "P1", SLO: 2 * time.Second}); err != nil {
		t.Fatalf("Submit tight: %v", err)
	}
	clk.advance(200 * time.Millisecond) // loose age 60s (40s folga), tight age 0.2s (1.8s folga)

	res, err := d.Dispatch(ctx)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	// A fracção consumida daria a vitória à loose (60% vs 10%); por PRAZO (folga
	// absoluta) a tight, prestes a falhar, tem de vencer.
	if !res.Dispatched || res.Task.ID != "tight" {
		t.Fatalf("despachou %+v, esperado 'tight' (deadline-aware: menor folga absoluta vence, sem inversão de prazo)", res)
	}
}

// ---------------------------------------------------------------------------
// Overflow do termo de SLO: com SLOWeight enorme e espera longa, a multiplicação
// ingénua transbordaria o int64 e tornaria a tarefa MAIS overdue NEGATIVA
// (invertendo a urgência). A saturação mantém a ordem (finding integer-overflow).
// ---------------------------------------------------------------------------

func TestDispatch_SLOOverflowSafe(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(1_000_000, 0))
	d, err := scheduler.NewDispatcher(
		scheduler.AgingParams{Base: 0, AgingStep: 1, AgingInterval: 1000 * time.Hour}, // aging ~0 (isola SLO)
		scheduler.WithClassAging("P1", scheduler.AgingParams{Base: 200, SLOWeight: 1_000_000}),
		scheduler.WithDispatchClock(clk.now),
		scheduler.WithoutAdmission(),
	)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	ctx := context.Background()

	// overdue: SLO 30s, envelhecida 3h — muito ALÉM do prazo e na zona onde a
	// multiplicação ingénua (SLOWeight*ageNanos) transbordaria (~2h33m).
	if _, err := d.Submit(ctx, scheduler.Task{ID: "overdue", Tenant: "acme", Class: "P1", SLO: 30 * time.Second}); err != nil {
		t.Fatalf("Submit overdue: %v", err)
	}
	clk.advance(3 * time.Hour)
	if _, err := d.Submit(ctx, scheduler.Task{ID: "fresh", Tenant: "acme", Class: "P1", SLO: 30 * time.Second}); err != nil {
		t.Fatalf("Submit fresh: %v", err)
	}

	res, err := d.Dispatch(ctx)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !res.Dispatched || res.Task.ID != "overdue" {
		t.Fatalf("despachou %+v, esperado 'overdue' (overflow NÃO pode inverter a urgência da tarefa mais atrasada)", res)
	}
}

// ---------------------------------------------------------------------------
// Anti-starvation FAIL-CLOSED: um override de classe com SÓ Base (sem AgingStep)
// HERDA o aging por omissão em vez de o desligar — a vítima não starva mesmo sob
// fluxo contínuo de alta prioridade (finding starvation-fail-open).
// ---------------------------------------------------------------------------

func TestDispatch_ClassOverrideInheritsAgingNoStarvation(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(1_000_000, 0))
	// Default com aging 10/s. Overrides definem SÓ Base (AgingStep omitido ⇒ herdado).
	d, err := scheduler.NewDispatcher(
		scheduler.AgingParams{Base: 0, AgingStep: 10, AgingInterval: time.Second},
		scheduler.WithClassAging("P0", scheduler.AgingParams{Base: 1000}), // só Base ⇒ herda aging
		scheduler.WithClassAging("P2", scheduler.AgingParams{Base: 0}),    // só Base ⇒ herda aging
		scheduler.WithDispatchClock(clk.now),
		scheduler.WithoutAdmission(),
	)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	ctx := context.Background()

	if _, err := d.Submit(ctx, scheduler.Task{ID: "victim-p2", Tenant: "acme", Class: "P2"}); err != nil {
		t.Fatalf("Submit victim: %v", err)
	}
	const maxIters = 500
	victimDispatched := false
	for i := 0; i < maxIters; i++ {
		if _, err := d.Submit(ctx, scheduler.Task{ID: fmt.Sprintf("flood-p0-%d", i), Tenant: "acme", Class: "P0"}); err != nil {
			t.Fatalf("Submit flood %d: %v", i, err)
		}
		clk.advance(time.Second)
		res, err := d.Dispatch(ctx)
		if err != nil {
			t.Fatalf("Dispatch %d: %v", i, err)
		}
		if res.Dispatched && res.Task.ID == "victim-p2" {
			victimDispatched = true
			if !res.Aged {
				t.Fatalf("vítima despachada sem Aged=true (o aging herdado deveria tê-la promovido)")
			}
			break
		}
	}
	if !victimDispatched {
		t.Fatalf("STARVATION: vítima P2 (override só-Base) nunca despachada — o aging não foi herdado")
	}
}

// ---------------------------------------------------------------------------
// Construção FAIL-CLOSED: um default de aging desligado (AgingStep=0) é rejeitado
// (a garantia anti-starvation não pode ser silenciosamente desactivada).
// ---------------------------------------------------------------------------

func TestNewDispatcher_RejectsDisabledDefaultAging(t *testing.T) {
	t.Parallel()
	if _, err := scheduler.NewDispatcher(scheduler.AgingParams{Base: 100}, scheduler.WithoutAdmission()); !errors.Is(err, scheduler.ErrInvalidAgingParams) {
		t.Fatalf("esperado ErrInvalidAgingParams para default sem aging (starvation), obtido %v", err)
	}
}

// ---------------------------------------------------------------------------
// Construção FAIL-CLOSED da admissão: sem decisão explícita a construção é
// recusada; WithAdmission ou WithoutAdmission satisfazem-na (finding
// admission-fail-open).
// ---------------------------------------------------------------------------

func TestNewDispatcher_RequiresAdmissionDecision(t *testing.T) {
	t.Parallel()
	valid := scheduler.AgingParams{Base: 0, AgingStep: 1, AgingInterval: time.Second}
	if _, err := scheduler.NewDispatcher(valid); !errors.Is(err, scheduler.ErrAdmissionNotConfigured) {
		t.Fatalf("esperado ErrAdmissionNotConfigured sem decisão de admissão, obtido %v", err)
	}
	if _, err := scheduler.NewDispatcher(valid, scheduler.WithoutAdmission()); err != nil {
		t.Fatalf("WithoutAdmission deveria construir (ordenação pura): %v", err)
	}
}

// grantingGate concede sempre e regista as libertações (rollback) recebidas.
type grantingGate struct {
	released []string
}

func (g *grantingGate) Admit(_ context.Context, req scheduler.AdmitRequest) (scheduler.AdmitResult, error) {
	return scheduler.AdmitResult{Granted: true, ReservationID: "resv-" + req.RequestID}, nil
}

func (g *grantingGate) Release(_ context.Context, _ scheduler.ProviderKey, reservationID string, _, _ int64) error {
	g.released = append(g.released, reservationID)
	return nil
}

// failingDispatchLog satisfaz EventLog mas falha SEMPRE o Append (força o erro no
// emit dos eventos de despacho, depois de a admissão já ter concedido a reserva).
type failingDispatchLog struct{}

func (failingDispatchLog) Append(_ context.Context, _ string, _ eventstore.EventInput, _ ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	return eventstore.AppendResult{}, errors.New("boom: append de despacho falhou")
}

func (failingDispatchLog) Read(_ context.Context, _ string, _ uint64) ([]eventstore.Event, error) {
	return nil, eventstore.ErrStreamNotFound
}

// ---------------------------------------------------------------------------
// Não-vazamento de headroom: se um erro ocorrer DEPOIS de a admissão conceder a
// reserva (emit falha), a reserva é LIBERTADA (rollback) antes de propagar o erro
// (finding resource-leak). Sem isto o headroom admitido nunca regressaria.
// ---------------------------------------------------------------------------

func TestDispatch_ReleasesReservationOnEmitError(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	gate := &grantingGate{}
	d := mustDispatcher(t,
		scheduler.WithDispatchClock(fixedClock(base)),
		scheduler.WithAdmission(gate),
		scheduler.WithDispatchLog(failingDispatchLog{}),
		scheduler.WithDefaultKey(testKey),
	)
	ctx := context.Background()

	if _, err := d.Submit(ctx, scheduler.Task{ID: "job", Tenant: "acme", Class: "P1", Cost: 100}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	// O despacho: admite (concede reserva) e depois falha no emit ⇒ erro propagado.
	if _, err := d.Dispatch(ctx); err == nil {
		t.Fatalf("esperado erro do emit falhado, obtido nil")
	}
	// A reserva concedida TEM de ter sido libertada (headroom devolvido).
	if len(gate.released) != 1 {
		t.Fatalf("reservas libertadas=%d, esperado 1 (headroom vazado no erro de despacho)", len(gate.released))
	}
	if gate.released[0] != "resv-sched:job" {
		t.Fatalf("reserva libertada=%q, esperado a reserva concedida 'resv-sched:job'", gate.released[0])
	}
}

// ---------------------------------------------------------------------------
// Observabilidade: cada despacho emite um span priority_dispatch com a classe e o
// tempo de espera (wait_ms) — spans/métricas de tempo de espera POR CLASSE (DoD,
// finding AOS-032-C1).
// ---------------------------------------------------------------------------

func TestDispatch_EmitsWaitTimeSpansPerClass(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(1_000_000, 0))
	tracer := &agentruntime.RecordingTracer{}
	d := mustDispatcher(t, append(classOpts(0, 0),
		scheduler.WithDispatchClock(clk.now),
		scheduler.WithDispatchTracer(tracer),
	)...)
	ctx := context.Background()

	// P0 submetida cedo, P2 mais tarde ⇒ tempos de espera POR CLASSE distintos.
	if _, err := d.Submit(ctx, scheduler.Task{ID: "a-p0", Tenant: "acme", Class: "P0"}); err != nil {
		t.Fatalf("Submit a-p0: %v", err)
	}
	clk.advance(3 * time.Second)
	if _, err := d.Submit(ctx, scheduler.Task{ID: "b-p2", Tenant: "acme", Class: "P2"}); err != nil {
		t.Fatalf("Submit b-p2: %v", err)
	}
	clk.advance(2 * time.Second) // agora: P0 esperou 5s, P2 esperou 2s

	for i := 0; i < 2; i++ {
		res, err := d.Dispatch(ctx)
		if err != nil || !res.Dispatched {
			t.Fatalf("Dispatch[%d]: res=%+v err=%v", i, res, err)
		}
	}

	spans := tracer.SpansByOperation("priority_dispatch")
	waitByClass := make(map[string]int64)
	dispatched := 0
	const attrClass, attrWaitMs = "aos.scheduling.class", "aos.scheduling.wait_ms"
	for _, s := range spans {
		cls, ok := s.Attributes[attrClass]
		if !ok {
			continue // span de um Dispatch sem despacho (nenhum candidato); ignora
		}
		wait, ok := s.Attributes[attrWaitMs]
		if !ok {
			t.Fatalf("span de despacho sem %s: %+v", attrWaitMs, s.Attributes)
		}
		dispatched++
		waitByClass[cls.(string)] = wait.(int64)
	}
	if dispatched != 2 {
		t.Fatalf("spans de despacho com classe=%d, esperado 2", dispatched)
	}
	if waitByClass["P0"] != 5000 {
		t.Fatalf("wait_ms da classe P0=%d, esperado 5000 (esperou 5s)", waitByClass["P0"])
	}
	if waitByClass["P2"] != 2000 {
		t.Fatalf("wait_ms da classe P2=%d, esperado 2000 (esperou 2s)", waitByClass["P2"])
	}
}
