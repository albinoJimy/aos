package scheduler_test

// Testes do backpressure (AOS-030): filas limitadas por partição tenant:priority,
// watermarks/histerese, deteção de saturação, motor de política declarativa
// versionado (JSON/SemVer, hot-reload com changelog no audit, validação
// fail-closed) e o acoplamento ADITIVO ao admission control (AOS-027). Todos
// deterministas: relógio injectável, iteração ordenada, sem time.Now/rand nas
// decisões. Reutilizam os helpers de admission_test.go (fixedClock, mutClock,
// qpTPM, newAdm, testKey — mesmo pacote scheduler_test).
//
// Cobrem os Testes Requeridos do ticket:
//   - unit: limites de fila e watermarks; saturação com HISTERESE (não flapping);
//   - unit: SEM acumulação ilimitada — ao atingir o limite aplica-se a política;
//   - integração: saturação PROPAGA backpressure ao admit (mais defers);
//   - integração: política selecciona a acção correcta; hot-reload sem perder
//     trabalho; validação fail-closed de config inválida;
//   - versionamento SemVer + changelog no audit trail; replay reconstrói.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/scheduler"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Fixtures de política.
// ---------------------------------------------------------------------------

// policyJSON serializa um PolicyDoc para o formato do artefacto versionado.
func policyJSON(t *testing.T, d scheduler.PolicyDoc) []byte {
	t.Helper()
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	return raw
}

// baseFixedClock é um instante fixo reutilizável.
func baseFixedClock() func() time.Time { return fixedClock(time.Unix(1_700_000_000, 0)) }

// newQueues constrói filas de teste com relógio fixo e (opcionalmente) log/policy.
func newQueues(t *testing.T, def scheduler.QueueLimits, opts ...scheduler.QueueOption) *scheduler.PartitionedQueues {
	t.Helper()
	base := []scheduler.QueueOption{scheduler.WithQueueClock(baseFixedClock())}
	q, err := scheduler.NewPartitionedQueues(def, append(base, opts...)...)
	if err != nil {
		t.Fatalf("NewPartitionedQueues: %v", err)
	}
	return q
}

// ---------------------------------------------------------------------------
// Unit: limites de fila e watermarks; saturação com HISTERESE (não flapping).
// ---------------------------------------------------------------------------

func TestQueue_WatermarksHysteresis_NoFlapping(t *testing.T) {
	ctx := context.Background()
	// MaxLen 5, High 4, Low 2: entre 2 e 4 o estado latched mantém-se.
	q := newQueues(t, scheduler.QueueLimits{MaxLen: 5, HighWatermark: 4, LowWatermark: 2})
	p := scheduler.Partition{Tenant: "acme", Priority: "P1"}

	enq := func() scheduler.EnqueueResult {
		r, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "x", Tenant: "acme", Priority: "P1"})
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		return r
	}
	deq := func() {
		if _, ok, err := q.Dequeue(ctx, p); err != nil || !ok {
			t.Fatalf("Dequeue: ok=%v err=%v", ok, err)
		}
	}

	// Depth 1,2,3 — abaixo do high (4): NÃO satura.
	for i := 0; i < 3; i++ {
		if r := enq(); r.Saturated {
			t.Fatalf("depth %d: saturou antes do high watermark", r.Depth)
		}
	}
	// Depth 4 — cruza o high: SATURA.
	if r := enq(); !r.Saturated {
		t.Fatalf("depth 4: devia saturar no high watermark")
	}
	if !q.IsSaturated(p) {
		t.Fatalf("partição devia estar saturada")
	}
	// Dequeue para depth 3 (>low 2): HISTERESE — mantém-se saturada (sem flapping).
	deq()
	if !q.IsSaturated(p) {
		t.Fatalf("depth 3 > low: histerese devia manter saturado (flapping detectado)")
	}
	// Dequeue para depth 2 (<=low 2): ALIVIA.
	deq()
	if q.IsSaturated(p) {
		t.Fatalf("depth 2 <= low: devia aliviar a saturação")
	}
	// Re-subir a 3 NÃO deve re-saturar (só o high re-satura).
	if r := enq(); r.Saturated {
		t.Fatalf("depth 3 a subir: não devia re-saturar antes do high (flapping)")
	}
}

func TestQueue_LimitsValidation_FailClosed(t *testing.T) {
	tests := []struct {
		name string
		lim  scheduler.QueueLimits
	}{
		{"maxlen_zero", scheduler.QueueLimits{MaxLen: 0, HighWatermark: 1}},
		{"high_zero", scheduler.QueueLimits{MaxLen: 5, HighWatermark: 0}},
		{"high_gt_maxlen", scheduler.QueueLimits{MaxLen: 5, HighWatermark: 6}},
		{"low_ge_high", scheduler.QueueLimits{MaxLen: 5, HighWatermark: 3, LowWatermark: 3}},
		{"low_negative", scheduler.QueueLimits{MaxLen: 5, HighWatermark: 3, LowWatermark: -1}},
		{"maxage_negative", scheduler.QueueLimits{MaxLen: 5, HighWatermark: 3, LowWatermark: 1, MaxAge: -time.Second}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := scheduler.NewPartitionedQueues(tc.lim); !errors.Is(err, scheduler.ErrInvalidQueueLimits) {
				t.Fatalf("err = %v, quero ErrInvalidQueueLimits", err)
			}
		})
	}
}

func TestQueue_DefaultQueueLimits(t *testing.T) {
	l := scheduler.DefaultQueueLimits(10)
	if l.MaxLen != 10 || l.HighWatermark != 8 || l.LowWatermark != 5 {
		t.Fatalf("DefaultQueueLimits(10) = %+v, quero {10,_,8,5}", l)
	}
	// Casos degenerados continuam válidos (Low<High<=MaxLen).
	for _, n := range []int{1, 2, 3, 0, -5} {
		got := scheduler.DefaultQueueLimits(n)
		if _, err := scheduler.NewPartitionedQueues(got); err != nil {
			t.Fatalf("DefaultQueueLimits(%d)=%+v inválido: %v", n, got, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Unit: SEM acumulação ilimitada — ao atingir o limite aplica-se a política.
// ---------------------------------------------------------------------------

func TestQueue_BoundedNoUnboundedAccumulation(t *testing.T) {
	ctx := context.Background()
	engine, err := scheduler.NewPolicyEngine(scheduler.PolicyDoc{
		Version:       "1.0.0",
		DefaultAction: scheduler.ActionReject,
	})
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	q := newQueues(t, scheduler.QueueLimits{MaxLen: 3, HighWatermark: 3, LowWatermark: 1},
		scheduler.WithQueuePolicy(engine))

	// Enche até ao limite (3 admitidos).
	for i := 0; i < 3; i++ {
		r, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "a", Tenant: "t", Priority: "P0"})
		if err != nil || !r.Admitted {
			t.Fatalf("enqueue %d: admitted=%v err=%v", i, r.Admitted, err)
		}
	}
	// Mais 10 tentativas: NUNCA cresce além de MaxLen; aplica a política (reject).
	for i := 0; i < 10; i++ {
		r, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "b", Tenant: "t", Priority: "P0"})
		if err != nil {
			t.Fatalf("enqueue extra: %v", err)
		}
		if r.Admitted {
			t.Fatalf("tentativa %d: não devia admitir no limite", i)
		}
		if r.Depth != 3 {
			t.Fatalf("tentativa %d: depth=%d, fila cresceu além de MaxLen=3", i, r.Depth)
		}
		if r.Action != scheduler.ActionReject {
			t.Fatalf("tentativa %d: acção=%q, quero reject", i, r.Action)
		}
		if r.PolicyVersion != "1.0.0" {
			t.Fatalf("tentativa %d: versão=%q, quero 1.0.0", i, r.PolicyVersion)
		}
	}
	if got := q.Depth(scheduler.Partition{Tenant: "t", Priority: "P0"}); got != 3 {
		t.Fatalf("depth final = %d, quero 3 (bounded)", got)
	}
}

// TestQueue_BoundedUnderConcurrentLoad materializa a frase da DoD "sem acumulação
// ilimitada demonstrada SOB CARGA": N goroutines enfileiram em paralelo na MESMA
// partição e prova-se que (a) a profundidade nunca excede MaxLen, (b) só MaxLen
// itens são admitidos e (c) TODA a rejeição no limite traz a acção da política. O
// motor de política tem log partilhado, portanto as rejeições concorrentes exercem
// também o caminho lock-free Select->appendSelected sob -race (contador atómico de
// step_id) — sem esta serialização, os step_ids torn corromperiam a auditoria.
func TestQueue_BoundedUnderConcurrentLoad(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	engine, err := scheduler.NewPolicyEngine(
		scheduler.PolicyDoc{Version: "1.0.0", DefaultAction: scheduler.ActionReject},
		scheduler.WithPolicyLog(es), scheduler.WithPolicyClock(baseFixedClock()))
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	const maxLen = 4
	q := newQueues(t, scheduler.QueueLimits{MaxLen: maxLen, HighWatermark: maxLen, LowWatermark: 0},
		scheduler.WithQueuePolicy(engine), scheduler.WithQueueLog(es))
	p := scheduler.Partition{Tenant: "t", Priority: "P0"}

	const workers = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted, rejected, badAction := 0, 0, 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			r, err := q.Enqueue(ctx, scheduler.WorkItem{ID: fmt.Sprintf("w%d", id), Tenant: "t", Priority: "P0"})
			if err != nil {
				t.Errorf("enqueue %d: %v", id, err)
				return
			}
			// Invariante DURA: a fila NUNCA excede MaxLen, mesmo sob corrida.
			if r.Depth > maxLen {
				t.Errorf("depth=%d excede MaxLen=%d (acumulação ilimitada sob carga)", r.Depth, maxLen)
			}
			mu.Lock()
			defer mu.Unlock()
			if r.Admitted {
				admitted++
			} else {
				rejected++
				if r.Action == "" { // rejeição no limite tem de trazer acção da política
					badAction++
				}
			}
		}(i)
	}
	wg.Wait()

	if got := q.Depth(p); got != maxLen {
		t.Fatalf("depth final = %d, quero exactamente MaxLen=%d (bounded)", got, maxLen)
	}
	if admitted != maxLen {
		t.Fatalf("admitidos = %d, quero exactamente MaxLen=%d", admitted, maxLen)
	}
	if rejected != workers-maxLen {
		t.Fatalf("rejeitados = %d, quero %d", rejected, workers-maxLen)
	}
	if badAction != 0 {
		t.Fatalf("%d rejeições no limite sem acção de política", badAction)
	}
}

func TestQueue_AgeBoundAppliesPolicy(t *testing.T) {
	ctx := context.Background()
	clk := &mutClock{}
	clk.set(time.Unix(1_700_000_000, 0))
	engine, err := scheduler.NewPolicyEngine(scheduler.PolicyDoc{Version: "1.0.0", DefaultAction: scheduler.ActionDefer})
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	// MaxLen alto (não é o comprimento a limitar), mas MaxAge curto.
	q, err := scheduler.NewPartitionedQueues(
		scheduler.QueueLimits{MaxLen: 100, HighWatermark: 80, LowWatermark: 40, MaxAge: 5 * time.Second},
		scheduler.WithQueueClock(clk.now), scheduler.WithQueuePolicy(engine))
	if err != nil {
		t.Fatalf("NewPartitionedQueues: %v", err)
	}
	// Primeiro item entra.
	r, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "old", Tenant: "t", Priority: "P0"})
	if err != nil || !r.Admitted {
		t.Fatalf("enqueue inicial: admitted=%v err=%v", r.Admitted, err)
	}
	// Avança o relógio para além do MaxAge: o item mais antigo excede a idade.
	clk.advance(6 * time.Second)
	r, err = q.Enqueue(ctx, scheduler.WorkItem{ID: "new", Tenant: "t", Priority: "P0"})
	if err != nil {
		t.Fatalf("enqueue pós-idade: %v", err)
	}
	if r.Admitted {
		t.Fatalf("com item além do MaxAge devia aplicar política, não admitir (idade é limite)")
	}
	if r.Action != scheduler.ActionDefer {
		t.Fatalf("acção=%q, quero defer", r.Action)
	}
}

// TestQueue_AgeHysteresis_NoFlapping cobre a HISTERESE na dimensão de IDADE: um
// backlog envelhecido drenado através do low watermark NÃO pode fazer o estado
// latched oscilar a cada par enqueue(re-satura por idade)/dequeue(desce ao low).
// Também prova a PROPAGAÇÃO por idade ao admit (Backpressure) sem depender de um
// novo Enqueue. Só alivia quando já não há itens over-age.
func TestQueue_AgeHysteresis_NoFlapping(t *testing.T) {
	ctx := context.Background()
	clk := &mutClock{}
	clk.set(time.Unix(1_700_000_000, 0))
	q, err := scheduler.NewPartitionedQueues(
		scheduler.QueueLimits{MaxLen: 10, HighWatermark: 8, LowWatermark: 2, MaxAge: time.Second},
		scheduler.WithQueueClock(clk.now))
	if err != nil {
		t.Fatalf("NewPartitionedQueues: %v", err)
	}
	p := scheduler.Partition{Tenant: "acme", Priority: "P1"}

	// 3 itens jovens (abaixo do High): não satura.
	for i := 0; i < 3; i++ {
		r, err := q.Enqueue(ctx, scheduler.WorkItem{ID: fmt.Sprintf("a%d", i), Tenant: "acme", Priority: "P1"})
		if err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
		if r.Saturated {
			t.Fatalf("depth %d < high: não devia saturar", r.Depth)
		}
	}
	// Envelhece tudo para além do MaxAge.
	clk.advance(2 * time.Second)

	// O 1.º enqueue over-age satura por IDADE (não por profundidade) e não cresce.
	r, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "over", Tenant: "acme", Priority: "P1"})
	if err != nil {
		t.Fatalf("enqueue over-age: %v", err)
	}
	if r.Admitted {
		t.Fatalf("item além do MaxAge devia aplicar política, não admitir")
	}
	if !q.IsSaturated(p) {
		t.Fatalf("devia estar saturada por idade")
	}

	// Alterna dequeue(desce ao/abaixo do low)/enqueue(no limite por idade) enquanto
	// restam itens over-age: o latch NÃO pode oscilar (histerese cobre a idade).
	for i := 0; i < 2; i++ {
		if _, ok, err := q.Dequeue(ctx, p); err != nil || !ok {
			t.Fatalf("dequeue: ok=%v err=%v", ok, err)
		}
		if !q.IsSaturated(p) {
			t.Fatalf("dequeue com itens ainda over-age: histerese devia manter saturado (flapping)")
		}
		if _, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "x", Tenant: "acme", Priority: "P1"}); err != nil {
			t.Fatalf("re-enqueue: %v", err)
		}
		if !q.IsSaturated(p) {
			t.Fatalf("re-enqueue over-age: devia continuar saturado")
		}
	}

	// PROPAGAÇÃO por idade SEM novo Enqueue: o admit vê backpressure na leitura.
	if sig := q.Backpressure(ctx, testKey, "acme"); !sig.Saturated {
		t.Fatalf("saturação por idade devia propagar ao admit (Backpressure)")
	}

	// Drena o backlog envelhecido: já não há itens over-age nem latch → alivia.
	for {
		_, ok, err := q.Dequeue(ctx, p)
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
		if !ok {
			break
		}
	}
	if q.IsSaturated(p) {
		t.Fatalf("fila sem itens over-age devia aliviar a saturação")
	}
	if sig := q.Backpressure(ctx, testKey, "acme"); sig.Saturated {
		t.Fatalf("após drenar, não devia haver backpressure por idade")
	}
}

// TestBackpressure_EmptyTenantAndColonIdentifiers cobre o isolamento de tenant na
// correspondência por CAMPO (não por prefixo textual): um ':' no identificador não
// causa match cruzado e o tenant vazio é uma partição legítima que não contorna o
// backpressure.
func TestBackpressure_EmptyTenantAndColonIdentifiers(t *testing.T) {
	ctx := context.Background()
	q := newQueues(t, scheduler.QueueLimits{MaxLen: 1, HighWatermark: 1, LowWatermark: 0})

	// Tenant "a:b" saturado; consultar "a" NÃO pode casar por prefixo "a:".
	if _, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "w", Tenant: "a:b", Priority: "P1"}); err != nil {
		t.Fatalf("enqueue a:b: %v", err)
	}
	if !q.Backpressure(ctx, testKey, "a:b").Saturated {
		t.Fatalf("tenant a:b devia estar saturado")
	}
	if q.Backpressure(ctx, testKey, "a").Saturated {
		t.Fatalf("tenant 'a' NÃO devia ver a saturação de 'a:b' (match cruzado por prefixo)")
	}

	// Tenant vazio é partição legítima: satura e propaga (não contorna o backpressure).
	if _, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "e", Tenant: "", Priority: "P0"}); err != nil {
		t.Fatalf("enqueue tenant vazio: %v", err)
	}
	if !q.Backpressure(ctx, testKey, "").Saturated {
		t.Fatalf("trabalho de tenant vazio saturado devia propagar backpressure")
	}
}

// ---------------------------------------------------------------------------
// Integração: saturação PROPAGA backpressure ao admit (mais defers).
// ---------------------------------------------------------------------------

func TestBackpressure_PropagatesToAdmit_MoreDefers(t *testing.T) {
	ctx := context.Background()
	// Limites de admissão GENEROSOS: sem backpressure, tudo é admitido (há
	// headroom de sobra). Assim, um defer só pode vir do backpressure.
	qp := qpTPM(1_000_000, 1_000_000, time.Minute)

	// Filas: satura acme:P1 com MaxLen 2 / High 2.
	queues := newQueues(t, scheduler.QueueLimits{MaxLen: 2, HighWatermark: 2, LowWatermark: 0},
		scheduler.WithBackpressureRetry(2*time.Second))
	for i := 0; i < 2; i++ {
		if _, err := queues.Enqueue(ctx, scheduler.WorkItem{ID: "w", Tenant: "acme", Priority: "P1"}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	if !queues.IsSaturated(scheduler.Partition{Tenant: "acme", Priority: "P1"}) {
		t.Fatalf("acme:P1 devia estar saturada")
	}

	clk := baseFixedClock()

	// CONTROLO 1: admit SEM backpressure — acme é ADMITIDO (prova que a diferença
	// é o acoplamento, não os limites).
	admNoBP, _ := newAdm(t, qp, scheduler.WithClock(clk), scheduler.WithIDGen(seqIDGen()))
	res, err := admNoBP.Admit(ctx, scheduler.AdmitRequest{Key: testKey, Tenant: "acme", EstimatedTokens: 10})
	if err != nil {
		t.Fatalf("admit sem bp: %v", err)
	}
	if !res.Granted {
		t.Fatalf("sem backpressure, acme devia ser ADMITIDO (há headroom); res=%+v", res)
	}

	// COM backpressure: admit para acme (tenant saturado) passa a ADIAR.
	admBP, _ := newAdm(t, qp,
		scheduler.WithClock(clk), scheduler.WithIDGen(seqIDGen()),
		scheduler.WithBackpressure(queues))
	res, err = admBP.Admit(ctx, scheduler.AdmitRequest{Key: testKey, Tenant: "acme", EstimatedTokens: 10})
	if err != nil {
		t.Fatalf("admit com bp: %v", err)
	}
	if res.Granted {
		t.Fatalf("com backpressure, acme saturado devia ADIAR, não admitir; res=%+v", res)
	}
	if res.RetryAfter < 2*time.Second {
		t.Fatalf("retry_after=%v, quero >= bpRetry (2s)", res.RetryAfter)
	}

	// CONTROLO 2: tenant NÃO saturado ("other") continua a ser admitido mesmo com
	// o seam injectado — o backpressure é por tenant.
	res, err = admBP.Admit(ctx, scheduler.AdmitRequest{Key: testKey, Tenant: "other", EstimatedTokens: 10})
	if err != nil {
		t.Fatalf("admit other: %v", err)
	}
	if !res.Granted {
		t.Fatalf("tenant não saturado devia ser admitido; res=%+v", res)
	}
}

func TestBackpressure_ClearsAfterDrain(t *testing.T) {
	ctx := context.Background()
	queues := newQueues(t, scheduler.QueueLimits{MaxLen: 3, HighWatermark: 2, LowWatermark: 0})
	p := scheduler.Partition{Tenant: "acme", Priority: "P1"}
	for i := 0; i < 2; i++ {
		if _, err := queues.Enqueue(ctx, scheduler.WorkItem{ID: "w", Tenant: "acme", Priority: "P1"}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	sig := queues.Backpressure(ctx, testKey, "acme")
	if !sig.Saturated {
		t.Fatalf("devia sinalizar saturação")
	}
	// Drena tudo: desce a 0 <= low, alivia.
	for {
		_, ok, err := queues.Dequeue(ctx, p)
		if err != nil {
			t.Fatalf("dequeue: %v", err)
		}
		if !ok {
			break
		}
	}
	if sig := queues.Backpressure(ctx, testKey, "acme"); sig.Saturated {
		t.Fatalf("após drenar, não devia haver backpressure")
	}
}

// ---------------------------------------------------------------------------
// Integração: política declarativa selecciona a acção correcta por condição.
// ---------------------------------------------------------------------------

func TestPolicy_SelectsCorrectActionPerCondition(t *testing.T) {
	// Política ordenada: alta prioridade degrada para downgrade; baixa prioridade
	// com fila muito cheia é shed; caso contrário defer.
	doc := scheduler.PolicyDoc{
		Version: "1.0.0",
		Rules: []scheduler.PolicyRule{
			{Priority: "P0", MinFillRatio: 0.9, Action: scheduler.ActionDowngrade},
			{Priority: "P2", MinFillRatio: 0.8, Action: scheduler.ActionShed},
			{MinFillRatio: 0.95, Action: scheduler.ActionReject},
		},
		DefaultAction: scheduler.ActionDefer,
	}
	tests := []struct {
		name string
		cond scheduler.SaturationCondition
		want scheduler.DegradationAction
	}{
		{"p0_full_downgrade", scheduler.SaturationCondition{Priority: "P0", FillRatio: 0.95}, scheduler.ActionDowngrade},
		{"p0_not_full_default", scheduler.SaturationCondition{Priority: "P0", FillRatio: 0.5}, scheduler.ActionDefer},
		{"p2_high_shed", scheduler.SaturationCondition{Priority: "P2", FillRatio: 0.85}, scheduler.ActionShed},
		{"any_near_max_reject", scheduler.SaturationCondition{Priority: "P1", FillRatio: 0.97}, scheduler.ActionReject},
		{"low_fill_default", scheduler.SaturationCondition{Priority: "P1", FillRatio: 0.1}, scheduler.ActionDefer},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := doc.Select(tc.cond); got != tc.want {
				t.Fatalf("Select(%+v) = %q, quero %q", tc.cond, got, tc.want)
			}
			// Determinismo: repetir dá o MESMO resultado.
			if got := doc.Select(tc.cond); got != tc.want {
				t.Fatalf("Select não determinístico")
			}
		})
	}
}

func TestPolicy_AgeConditionSelects(t *testing.T) {
	doc := scheduler.PolicyDoc{
		Version: "1.0.0",
		Rules: []scheduler.PolicyRule{
			{MinAgeMs: 1000, Action: scheduler.ActionShed},
		},
		DefaultAction: scheduler.ActionDefer,
	}
	if got := doc.Select(scheduler.SaturationCondition{OldestAge: 2 * time.Second}); got != scheduler.ActionShed {
		t.Fatalf("idade >= 1s devia dar shed, deu %q", got)
	}
	if got := doc.Select(scheduler.SaturationCondition{OldestAge: 500 * time.Millisecond}); got != scheduler.ActionDefer {
		t.Fatalf("idade < 1s devia dar default defer, deu %q", got)
	}
}

// ---------------------------------------------------------------------------
// Integração: hot-reload SEM perder trabalho em curso.
// ---------------------------------------------------------------------------

func TestPolicy_HotReloadPreservesInFlightWork(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	engine, err := scheduler.NewPolicyEngine(
		scheduler.PolicyDoc{Version: "1.0.0", DefaultAction: scheduler.ActionShed},
		scheduler.WithPolicyLog(es), scheduler.WithPolicyClock(baseFixedClock()))
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	q := newQueues(t, scheduler.QueueLimits{MaxLen: 2, HighWatermark: 2, LowWatermark: 0},
		scheduler.WithQueuePolicy(engine))
	p := scheduler.Partition{Tenant: "t", Priority: "P0"}

	// Enche a fila (trabalho EM CURSO): 2 itens admitidos.
	for i := 0; i < 2; i++ {
		if _, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "w", Tenant: "t", Priority: "P0"}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	// No limite: política v1 selecciona shed.
	r, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "over", Tenant: "t", Priority: "P0"})
	if err != nil {
		t.Fatalf("enqueue over: %v", err)
	}
	if r.Action != scheduler.ActionShed || r.PolicyVersion != "1.0.0" {
		t.Fatalf("v1: acção=%q versão=%q, quero shed/1.0.0", r.Action, r.PolicyVersion)
	}

	depthBefore := q.Depth(p)

	// HOT-RELOAD para v2 (default reject).
	next, err := engine.Reload(ctx, policyJSON(t, scheduler.PolicyDoc{Version: "2.0.0", DefaultAction: scheduler.ActionReject}))
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if next.Version != "2.0.0" || engine.Version() != "2.0.0" {
		t.Fatalf("versão após reload = %q", engine.Version())
	}

	// Trabalho em curso PRESERVADO: a fila não perdeu itens.
	if got := q.Depth(p); got != depthBefore {
		t.Fatalf("hot-reload perdeu trabalho: depth %d -> %d", depthBefore, got)
	}
	// Nova política já em vigor: agora selecciona reject.
	r, err = q.Enqueue(ctx, scheduler.WorkItem{ID: "over2", Tenant: "t", Priority: "P0"})
	if err != nil {
		t.Fatalf("enqueue over2: %v", err)
	}
	if r.Action != scheduler.ActionReject || r.PolicyVersion != "2.0.0" {
		t.Fatalf("v2: acção=%q versão=%q, quero reject/2.0.0", r.Action, r.PolicyVersion)
	}
}

// ---------------------------------------------------------------------------
// Integração: validação fail-closed de config inválida.
// ---------------------------------------------------------------------------

func TestPolicy_ReloadFailClosed_KeepsPrevious(t *testing.T) {
	ctx := context.Background()
	engine, err := scheduler.NewPolicyEngine(scheduler.PolicyDoc{Version: "1.0.0", DefaultAction: scheduler.ActionDefer})
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}

	tests := []struct {
		name    string
		raw     []byte
		wantErr error
	}{
		{"malformed_json", []byte(`{ not json `), scheduler.ErrInvalidPolicy},
		{"unknown_action", []byte(`{"version":"2.0.0","default_action":"explode"}`), scheduler.ErrInvalidPolicy},
		{"bad_semver", []byte(`{"version":"v2","default_action":"defer"}`), scheduler.ErrInvalidPolicy},
		{"unknown_field", []byte(`{"version":"2.0.0","default_action":"defer","bogus":1}`), scheduler.ErrInvalidPolicy},
		{"rule_bad_fill", []byte(`{"version":"2.0.0","default_action":"defer","rules":[{"min_fill_ratio":2,"action":"shed"}]}`), scheduler.ErrInvalidPolicy},
		{"stale_equal", policyJSON(t, scheduler.PolicyDoc{Version: "1.0.0", DefaultAction: scheduler.ActionShed}), scheduler.ErrStalePolicy},
		{"stale_older", policyJSON(t, scheduler.PolicyDoc{Version: "0.9.0", DefaultAction: scheduler.ActionShed}), scheduler.ErrStalePolicy},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := engine.Reload(ctx, tc.raw)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, quero %v", err, tc.wantErr)
			}
			// A política anterior mantém-se INTACTA (fail-closed).
			if engine.Version() != "1.0.0" {
				t.Fatalf("config inválida alterou a política corrente para %q", engine.Version())
			}
			if engine.Current().DefaultAction != scheduler.ActionDefer {
				t.Fatalf("config inválida alterou a acção por omissão para %q", engine.Current().DefaultAction)
			}
		})
	}
}

func TestPolicy_NewEngineRejectsInvalidInitial(t *testing.T) {
	if _, err := scheduler.NewPolicyEngine(scheduler.PolicyDoc{Version: "bad", DefaultAction: scheduler.ActionDefer}); !errors.Is(err, scheduler.ErrInvalidPolicy) {
		t.Fatalf("versão inválida devia falhar fail-closed, err=%v", err)
	}
	if _, err := scheduler.NewPolicyEngine(scheduler.PolicyDoc{Version: "1.0.0", DefaultAction: "nope"}); !errors.Is(err, scheduler.ErrInvalidPolicy) {
		t.Fatalf("acção inválida devia falhar fail-closed, err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// Versionamento SemVer + changelog no audit trail; replay reconstrói.
// ---------------------------------------------------------------------------

func TestPolicy_VersioningChangelogAndReplay(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	engine, err := scheduler.NewPolicyEngine(
		scheduler.PolicyDoc{Version: "1.0.0", DefaultAction: scheduler.ActionDefer},
		scheduler.WithPolicyLog(es), scheduler.WithPolicyClock(baseFixedClock()))
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	// Sequência de reloads monótonos.
	for _, v := range []string{"1.1.0", "1.2.3", "2.0.0"} {
		if _, err := engine.Reload(ctx, policyJSON(t, scheduler.PolicyDoc{Version: v, DefaultAction: scheduler.ActionShed})); err != nil {
			t.Fatalf("Reload %s: %v", v, err)
		}
	}
	// Replay reconstrói a LINHAGEM: ""->1.0.0->1.1.0->1.2.3->2.0.0.
	changes, err := engine.ReplayVersions(ctx)
	if err != nil {
		t.Fatalf("ReplayVersions: %v", err)
	}
	wantFrom := []string{"", "1.0.0", "1.1.0", "1.2.3"}
	wantTo := []string{"1.0.0", "1.1.0", "1.2.3", "2.0.0"}
	if len(changes) != len(wantTo) {
		t.Fatalf("changelog tem %d entradas, quero %d: %+v", len(changes), len(wantTo), changes)
	}
	for i, c := range changes {
		if c.From != wantFrom[i] || c.To != wantTo[i] {
			t.Fatalf("changelog[%d] = %s->%s, quero %s->%s", i, c.From, c.To, wantFrom[i], wantTo[i])
		}
	}
}

func TestPolicy_SelectEmitsEventAndReplaysQueue(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	tr := &agentruntime.RecordingTracer{}
	engine, err := scheduler.NewPolicyEngine(
		scheduler.PolicyDoc{Version: "1.0.0", DefaultAction: scheduler.ActionReject},
		scheduler.WithPolicyLog(es), scheduler.WithPolicyClock(baseFixedClock()),
		scheduler.WithPolicyTracer(tr))
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	q := newQueues(t, scheduler.QueueLimits{MaxLen: 1, HighWatermark: 1, LowWatermark: 0},
		scheduler.WithQueuePolicy(engine), scheduler.WithQueueLog(es))

	// 1 admitido, depois 2 no limite (selecção de política + eventos de fila).
	for i := 0; i < 3; i++ {
		if _, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "w", Tenant: "t", Priority: "P0"}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	// Eventos de fila reconstroem-se por replay.
	recs, err := q.ReplayQueue(ctx)
	if err != nil {
		t.Fatalf("ReplayQueue: %v", err)
	}
	var sawSaturated, sawSignalled bool
	for _, r := range recs {
		if r.Type == scheduler.EventQueueSaturated {
			sawSaturated = true
		}
		if r.Type == scheduler.EventBackpressureSignalled {
			sawSignalled = true
		}
	}
	if !sawSaturated || !sawSignalled {
		t.Fatalf("replay de fila devia conter queue_saturated + backpressure_signalled; recs=%+v", recs)
	}
	// Span de selecção com atributos.
	if len(tr.SpansByOperation("backpressure_policy_select")) == 0 {
		t.Fatalf("esperava spans de selecção de política")
	}
}

// TestPolicy_SelectionPersistedAndReplayable fecha a prova de observabilidade do
// critério de aceitação 6 para as DECISÕES de política (não só para os estados de
// fila): uma selecção sob saturação persiste um evento append-only
// degradation_policy_selected, reconstruível do Event Store via ReplaySelections,
// com versão/acção/tenant/priority. ReplayVersions continua a expor só a linhagem.
func TestPolicy_SelectionPersistedAndReplayable(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	engine, err := scheduler.NewPolicyEngine(
		scheduler.PolicyDoc{Version: "1.4.2", DefaultAction: scheduler.ActionShed},
		scheduler.WithPolicyLog(es), scheduler.WithPolicyClock(baseFixedClock()))
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	q := newQueues(t, scheduler.QueueLimits{MaxLen: 1, HighWatermark: 1, LowWatermark: 0},
		scheduler.WithQueuePolicy(engine), scheduler.WithQueueLog(es))

	// 1 admitido; o 2.º satura e força uma SELECÇÃO de política.
	for i := 0; i < 2; i++ {
		if _, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "w", Tenant: "acme", Priority: "P2"}); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	sels, err := engine.ReplaySelections(ctx)
	if err != nil {
		t.Fatalf("ReplaySelections: %v", err)
	}
	if len(sels) != 1 {
		t.Fatalf("selecções persistidas = %d, quero 1: %+v", len(sels), sels)
	}
	if s := sels[0]; s.Version != "1.4.2" || s.Action != scheduler.ActionShed || s.Tenant != "acme" || s.Priority != "P2" {
		t.Fatalf("selecção replay = %+v, quero {1.4.2 shed acme P2}", s)
	}

	// ReplayVersions expõe SÓ a linhagem de versões (carga inicial), não selecções.
	vers, err := engine.ReplayVersions(ctx)
	if err != nil {
		t.Fatalf("ReplayVersions: %v", err)
	}
	if len(vers) != 1 || vers[0].To != "1.4.2" {
		t.Fatalf("ReplayVersions = %+v, quero só a carga inicial ->1.4.2", vers)
	}
}

func TestQueue_OptionsAndPerPartitionLimits(t *testing.T) {
	ctx := context.Background()
	es := newES(t)
	tr := &agentruntime.RecordingTracer{}
	engine, err := scheduler.NewPolicyEngine(
		scheduler.PolicyDoc{Version: "1.0.0", DefaultAction: scheduler.ActionShed},
		scheduler.WithPolicyLog(es), scheduler.WithPolicyClock(baseFixedClock()),
		scheduler.WithPolicyName("orders"),
		scheduler.WithPolicyProducer(eventstore.Producer{NHIID: "nhi:test/policy"}))
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	// Partição vip:P0 com limite Maior (override); a omissão é pequena.
	vip := scheduler.Partition{Tenant: "vip", Priority: "P0"}
	q, err := scheduler.NewPartitionedQueues(
		scheduler.QueueLimits{MaxLen: 1, HighWatermark: 1, LowWatermark: 0},
		scheduler.WithQueueClock(baseFixedClock()),
		scheduler.WithQueuePolicy(engine),
		scheduler.WithQueueLog(es),
		scheduler.WithQueueTracer(tr),
		scheduler.WithQueueName("orders"),
		scheduler.WithQueueProducer(eventstore.Producer{NHIID: "nhi:test/queue"}),
		scheduler.WithQueueLimitsFor(vip, scheduler.QueueLimits{MaxLen: 3, HighWatermark: 3, LowWatermark: 1}),
	)
	if err != nil {
		t.Fatalf("NewPartitionedQueues: %v", err)
	}
	// vip:P0 aceita 3 (override), o 4.º aplica política.
	for i := 0; i < 3; i++ {
		r, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "v", Tenant: "vip", Priority: "P0"})
		if err != nil || !r.Admitted {
			t.Fatalf("vip enqueue %d: admitted=%v err=%v", i, r.Admitted, err)
		}
	}
	r, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "v4", Tenant: "vip", Priority: "P0"})
	if err != nil {
		t.Fatalf("vip enqueue 4: %v", err)
	}
	if r.Admitted || r.Action != scheduler.ActionShed {
		t.Fatalf("vip 4.º: admitted=%v acção=%q, quero !admitted/shed", r.Admitted, r.Action)
	}
	// Partição por omissão (free:P2) satura logo ao 1.º item extra.
	if _, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "f", Tenant: "free", Priority: "P2"}); err != nil {
		t.Fatalf("free enqueue: %v", err)
	}
	rf, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "f2", Tenant: "free", Priority: "P2"})
	if err != nil {
		t.Fatalf("free enqueue 2: %v", err)
	}
	if rf.Admitted {
		t.Fatalf("free:P2 (MaxLen 1) devia aplicar política ao 2.º")
	}
	if len(tr.SpansByOperation("backpressure_enqueue")) == 0 {
		t.Fatalf("esperava spans de enqueue")
	}
	// Replay das versões usa o nome "orders".
	changes, err := engine.ReplayVersions(ctx)
	if err != nil || len(changes) != 1 || changes[0].To != "1.0.0" {
		t.Fatalf("ReplayVersions(orders) = %+v err=%v", changes, err)
	}
}

// ---------------------------------------------------------------------------
// SemVer helper (exposto para teste).
// ---------------------------------------------------------------------------

func TestSemVer_ParseAndCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.2.0", "1.1.9", 1},
		{"2.0.0", "1.9.9", 1},
		{"0.9.0", "1.0.0", -1},
	}
	for _, tc := range tests {
		if got := scheduler.CompareSemVerForTest(tc.a, tc.b); got != tc.want {
			t.Fatalf("compare(%s,%s) = %d, quero %d", tc.a, tc.b, got, tc.want)
		}
	}
	for _, bad := range []string{"", "1", "1.2", "1.2.3.4", "a.b.c", "1.-1.0"} {
		if scheduler.ValidSemVerForTest(bad) {
			t.Fatalf("%q não devia ser SemVer válido", bad)
		}
	}
}
