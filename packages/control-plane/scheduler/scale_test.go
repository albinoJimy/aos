package scheduler_test

// Testes da ESCALA HORIZONTAL dirigida por SLIs + ESCADA de degradação GLOBAL
// (AOS-107). Compõem as peças já feitas (headroom/AOS-028, filas/AOS-030,
// política/AOS-030, degradação/AOS-031, p95 sobre WaitMs/AOS-032) — não as
// reimplementam. Todos deterministas: headroom/relógio injectáveis, iteração
// ordenada, sem time.Now/rand nas decisões. Reutilizam helpers do pacote
// scheduler_test: testKey, tierRouter, newQueues, baseFixedClock.
//
// Cobrem os Testes Requeridos do ticket:
//   - scale-out por SLI (fila + p95 sobem com headroom ⇒ desiredReplicas sobe,
//     LIMITADO pelo headroom);
//   - max_spawn acompanha o headroom (o alvo nunca ultrapassa deriveMaxSpawn);
//   - 0-crescimento sob headroom nulo (fail-closed);
//   - escada shed→defer→downgrade→reject accionada na ORDEM correcta sob
//     esgotamento de headroom, conforme a pressão agregada;
//   - ausência de acumulação ilimitada de fila;
//   - emissão de métricas de degradação + do degrau activo.

import (
	"context"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/scheduler"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Fixtures locais.
// ---------------------------------------------------------------------------

// fakeHeadroom é uma [scheduler.HeadroomController] determinista: devolve um
// snapshot fixo (Admit/Release não são usados pelo scaler — no-ops).
type fakeHeadroom struct{ snap scheduler.HeadroomSnapshot }

func (f *fakeHeadroom) Headroom(context.Context, scheduler.ProviderKey, string) (scheduler.HeadroomSnapshot, error) {
	return f.snap, nil
}
func (f *fakeHeadroom) Admit(context.Context, scheduler.AdmitRequest) (scheduler.AdmitResult, error) {
	return scheduler.AdmitResult{}, nil
}
func (f *fakeHeadroom) Release(context.Context, scheduler.ProviderKey, string, int64, int64) error {
	return nil
}

// recSink capta os alvos de réplicas emitidos.
type recSink struct{ targets []scheduler.ReplicaTarget }

func (s *recSink) SetReplicaTarget(_ context.Context, t scheduler.ReplicaTarget) error {
	s.targets = append(s.targets, t)
	return nil
}

// fixedBacklog é uma [scheduler.BacklogSource] com um conjunto fixo de itens.
type fixedBacklog struct{ items []scheduler.DegradationItem }

func (b fixedBacklog) PendingItems(context.Context) []scheduler.DegradationItem { return b.items }

// scaleItem é um item Optional+Deferrable+reversível (premium): aplicável a
// QUALQUER degrau da escada (shed/defer/downgrade/reject), para que o degrau
// executado seja o determinado pelo PONTO DE ENTRADA (a pressão), não pela
// elegibilidade do item.
func scaleItem() scheduler.DegradationItem {
	return scheduler.DegradationItem{
		ID:           "w1",
		Tenant:       "acme",
		Priority:     "P2",
		Optional:     true,
		Deferrable:   true,
		CurrentTier:  "premium",
		CurrentModel: "claude-opus",
		Key:          testKey,
	}
}

func scaleCfg() scheduler.ReplicaScalerConfig {
	return scheduler.ReplicaScalerConfig{
		MinReplicas:                1,
		MaxReplicas:                64,
		TargetQueueDepthPerReplica: 16,
		P95WaitTarget:              250 * time.Millisecond,
		CostPerReplicaTokens:       1000,
	}
}

// enqueueN enfileira n itens numa partição (para levar a fila a uma profundidade
// conhecida). Ignora o veredicto (a fila é limitada por MaxLen).
func enqueueN(t *testing.T, q *scheduler.PartitionedQueues, tenant, prio string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if _, err := q.Enqueue(ctx, scheduler.WorkItem{ID: itoaTest(i), Tenant: tenant, Priority: prio}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var b [12]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

// ---------------------------------------------------------------------------
// deriveDesiredReplicas — a fórmula pura (AC1/AC3).
// ---------------------------------------------------------------------------

func TestDeriveDesiredReplicas_GrowsWithLoadUnderHeadroom(t *testing.T) {
	cfg := scaleCfg()
	hr := scheduler.HeadroomSnapshot{Tokens: 1_000_000, Requests: 10_000, LimitTokens: 1_000_000, LimitRequests: 10_000}
	p95 := (500 * time.Millisecond).Nanoseconds()

	// Fila vazia OU p95 abaixo do alvo ⇒ mantém-se o piso (Min).
	if got := scheduler.DeriveDesiredReplicasForTest(0, p95, hr, cfg); got != cfg.MinReplicas {
		t.Errorf("fila vazia: desired=%d, quero piso %d", got, cfg.MinReplicas)
	}
	lowP95 := (100 * time.Millisecond).Nanoseconds()
	if got := scheduler.DeriveDesiredReplicasForTest(64, lowP95, hr, cfg); got != cfg.MinReplicas {
		t.Errorf("p95 abaixo do alvo: desired=%d, quero piso %d", got, cfg.MinReplicas)
	}

	// Fila E p95 sobem ⇒ cresce (1 + ceil(64/16) = 5).
	if got := scheduler.DeriveDesiredReplicasForTest(64, p95, hr, cfg); got != 5 {
		t.Errorf("carga: desired=%d, quero 5", got)
	}

	// MONOTONIA na profundidade da fila.
	prev := 0
	for _, depth := range []int{0, 16, 32, 64, 128, 256} {
		got := scheduler.DeriveDesiredReplicasForTest(depth, p95, hr, cfg)
		if got < prev {
			t.Fatalf("não-monótono na fila: depth=%d desired=%d < anterior %d", depth, got, prev)
		}
		prev = got
	}
}

func TestDeriveDesiredReplicas_ZeroUnderNullHeadroom(t *testing.T) {
	cfg := scaleCfg()
	p95 := (500 * time.Millisecond).Nanoseconds()
	// Headroom nulo ⇒ 0-crescimento (fail-closed), mesmo com fila enorme e p95 alto.
	null := scheduler.HeadroomSnapshot{Tokens: 0, Requests: 0}
	if got := scheduler.DeriveDesiredReplicasForTest(10_000, p95, null, cfg); got != 0 {
		t.Fatalf("headroom nulo: desired=%d, quero 0 (fail-closed)", got)
	}
	// Sem tokens mas com requests (ou vice-versa) ⇒ ainda 0 (o mínimo domina).
	tokenOnly := scheduler.HeadroomSnapshot{Tokens: 1_000_000, Requests: 0}
	if got := scheduler.DeriveDesiredReplicasForTest(10_000, p95, tokenOnly, cfg); got != 0 {
		t.Fatalf("sem requests: desired=%d, quero 0", got)
	}
}

func TestDeriveDesiredReplicas_LimitedByHeadroom_TracksMaxSpawn(t *testing.T) {
	// Tecto absoluto ALTO para que o LIMITE efectivo seja o headroom (ADR-008).
	cfg := scheduler.ReplicaScalerConfig{
		MinReplicas:                1,
		MaxReplicas:                100_000,
		TargetQueueDepthPerReplica: 1, // procura enorme para forçar o limite pelo headroom
		P95WaitTarget:              10 * time.Millisecond,
		CostPerReplicaTokens:       1000,
	}
	p95 := (500 * time.Millisecond).Nanoseconds()
	depth := 100_000

	prev := 0
	for _, tokens := range []int64{0, 5000, 20000, 50000} {
		hr := scheduler.HeadroomSnapshot{Tokens: tokens, Requests: 100_000}
		want := scheduler.DeriveMaxSpawnForTest(tokens, 100_000, cfg.CostPerReplicaTokens)
		got := scheduler.DeriveDesiredReplicasForTest(depth, p95, hr, cfg)
		if got != want {
			t.Errorf("tokens=%d: desired=%d, quero limitado ao max_spawn=%d", tokens, got, want)
		}
		if got < prev {
			t.Fatalf("não-monótono no headroom: tokens=%d desired=%d < anterior %d", tokens, got, prev)
		}
		prev = got
	}
}

// ---------------------------------------------------------------------------
// WaitP95Recorder — o SLI de p95 de wait (AC1).
// ---------------------------------------------------------------------------

func TestWaitP95Recorder_NearestRankAndMetric(t *testing.T) {
	ctx := context.Background()
	rec := scheduler.NewRecordingMeter()
	r := scheduler.NewWaitP95Recorder(
		scheduler.WithWaitP95Window(100),
		scheduler.WithWaitP95Meter(rec),
	)
	// 1..100 ms ⇒ p95 nearest-rank = rank ceil(0.95*100)=95 ⇒ 95 ms.
	for i := 1; i <= 100; i++ {
		r.Observe(ctx, time.Duration(i)*time.Millisecond)
	}
	if got := r.P95(); got != 95*time.Millisecond {
		t.Errorf("p95 = %v, quero 95ms", got)
	}
	// A janela deslizante mantém no máximo `window` amostras.
	if got := r.Samples(); got != 100 {
		t.Errorf("amostras = %d, quero 100 (janela)", got)
	}
	// O gauge de p95 foi emitido (uma medição por Observe).
	if ms := rec.ByInstrument(scheduler.MetricDispatchWaitP95); len(ms) != 100 {
		t.Errorf("emissões de p95 = %d, quero 100", len(ms))
	}
	// ObserveDispatch de um resultado não-despachado é no-op.
	before := r.Samples()
	r.ObserveDispatch(ctx, scheduler.DispatchResult{Dispatched: false, WaitMs: 999})
	if r.Samples() != before {
		t.Error("ObserveDispatch de !Dispatched não devia registar amostra")
	}
}

// ---------------------------------------------------------------------------
// HorizontalScaler — scale-out com headroom (AC1/AC3).
// ---------------------------------------------------------------------------

func TestScaler_ScaleOutWithHeadroom(t *testing.T) {
	ctx := context.Background()
	q := newQueues(t, scheduler.QueueLimits{MaxLen: 1000, HighWatermark: 800, LowWatermark: 400})
	enqueueN(t, q, "acme", "P2", 64)

	wait := scheduler.NewWaitP95Recorder(scheduler.WithWaitP95Window(16))
	wait.Observe(ctx, 500*time.Millisecond) // p95 acima do alvo

	hr := &fakeHeadroom{snap: scheduler.HeadroomSnapshot{Tokens: 1_000_000, Requests: 10_000, LimitTokens: 1_000_000, LimitRequests: 10_000}}
	sink := &recSink{}
	rec := scheduler.NewRecordingMeter()

	s, err := scheduler.NewHorizontalScaler(testKey, hr, q, wait, scaleCfg(),
		scheduler.WithReplicaSink(sink),
		scheduler.WithScaleMeter(rec),
	)
	if err != nil {
		t.Fatalf("NewHorizontalScaler: %v", err)
	}

	dec, err := s.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if dec.Degraded {
		t.Error("com headroom não devia degradar")
	}
	if dec.DesiredReplicas != 5 { // 1 + ceil(64/16)
		t.Errorf("desired = %d, quero 5", dec.DesiredReplicas)
	}
	if dec.MaxSpawn != 1000 { // min(1_000_000/1000, 10_000)
		t.Errorf("max_spawn = %d, quero 1000", dec.MaxSpawn)
	}
	if !dec.TargetEmitted || len(sink.targets) != 1 || sink.targets[0].Desired != 5 {
		t.Errorf("sinal de escala: emitted=%v targets=%+v", dec.TargetEmitted, sink.targets)
	}
	if sink.targets[0].Reason != scheduler.ScaleReasonScaleOut {
		t.Errorf("reason = %q, quero %q", sink.targets[0].Reason, scheduler.ScaleReasonScaleOut)
	}
	// Métricas de escala emitidas (AC5).
	if len(rec.ByInstrument(scheduler.MetricDesiredReplicas)) == 0 {
		t.Error("gauge de desired_replicas não emitido")
	}
}

// TestScaler_MaxSpawnFollowsHeadroom: o alvo emitido acompanha o headroom — com mais
// headroom o max_spawn (e o alvo, se limitado por ele) sobe; a zero, o scaler NÃO
// escala (entra na degradação).
func TestScaler_MaxSpawnFollowsHeadroom(t *testing.T) {
	ctx := context.Background()
	q := newQueues(t, scheduler.QueueLimits{MaxLen: 100_000, HighWatermark: 80_000, LowWatermark: 40_000})
	enqueueN(t, q, "acme", "P2", 100_000)
	wait := scheduler.NewWaitP95Recorder()
	wait.Observe(ctx, time.Second)

	hr := &fakeHeadroom{}
	cfg := scheduler.ReplicaScalerConfig{
		MinReplicas: 1, MaxReplicas: 100_000, TargetQueueDepthPerReplica: 1,
		P95WaitTarget: 10 * time.Millisecond, CostPerReplicaTokens: 1000,
	}
	pol := mustPolicy(t, scheduler.PolicyDoc{Version: "1.0.0", DefaultAction: scheduler.ActionReject})
	deg, _ := newDegrader(t)
	s, err := scheduler.NewHorizontalScaler(testKey, hr, q, wait, cfg,
		scheduler.WithScalePolicy(pol), scheduler.WithScaleDegrader(deg))
	if err != nil {
		t.Fatalf("NewHorizontalScaler: %v", err)
	}

	prev := 0
	for _, tokens := range []int64{5000, 20000, 50000} {
		hr.snap = scheduler.HeadroomSnapshot{Tokens: tokens, Requests: 100_000}
		dec, err := s.Tick(ctx)
		if err != nil {
			t.Fatalf("Tick: %v", err)
		}
		want := scheduler.DeriveMaxSpawnForTest(tokens, 100_000, cfg.CostPerReplicaTokens)
		if dec.MaxSpawn != want || dec.DesiredReplicas != want {
			t.Errorf("tokens=%d: max_spawn=%d desired=%d, quero ambos %d", tokens, dec.MaxSpawn, dec.DesiredReplicas, want)
		}
		if dec.DesiredReplicas < prev {
			t.Fatalf("não-monótono: tokens=%d desired=%d < %d", tokens, dec.DesiredReplicas, prev)
		}
		prev = dec.DesiredReplicas
	}

	// Headroom a zero ⇒ não escala (degrada).
	hr.snap = scheduler.HeadroomSnapshot{Tokens: 0, Requests: 0}
	dec, err := s.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if !dec.Degraded || dec.MaxSpawn != 0 {
		t.Errorf("headroom nulo: degraded=%v max_spawn=%d, quero degradar com max_spawn 0", dec.Degraded, dec.MaxSpawn)
	}
}

// ---------------------------------------------------------------------------
// HorizontalScaler — escada de degradação na ORDEM correcta sob esgotamento (AC4/AC5).
// ---------------------------------------------------------------------------

func TestScaler_DegradationLadderInOrderUnderExhaustion(t *testing.T) {
	ctx := context.Background()
	// Política declarativa: fill crescente ⇒ shed→defer→downgrade→reject (primeira
	// regra que casa vence, thresholds do maior ao menor).
	pol := mustPolicy(t, scheduler.PolicyDoc{
		Version: "1.0.0",
		Rules: []scheduler.PolicyRule{
			{MinFillRatio: 0.95, Action: scheduler.ActionReject},
			{MinFillRatio: 0.85, Action: scheduler.ActionDowngrade},
			{MinFillRatio: 0.70, Action: scheduler.ActionDefer},
			{MinFillRatio: 0.50, Action: scheduler.ActionShed},
		},
		DefaultAction: scheduler.ActionShed,
	})

	rec := scheduler.NewRecordingMeter()
	deg, _ := newDegrader(t, scheduler.WithDegradationMeter(rec))
	q := newQueues(t, scheduler.QueueLimits{MaxLen: 100, HighWatermark: 80, LowWatermark: 40})
	wait := scheduler.NewWaitP95Recorder()

	// Headroom ESGOTADO: força a escada.
	hr := &fakeHeadroom{snap: scheduler.HeadroomSnapshot{Tokens: 0, Requests: 0}}
	s, err := scheduler.NewHorizontalScaler(testKey, hr, q, wait, scaleCfg(),
		scheduler.WithScalePolicy(pol),
		scheduler.WithScaleDegrader(deg),
		scheduler.WithScaleBacklog(fixedBacklog{items: []scheduler.DegradationItem{scaleItem()}}),
		scheduler.WithScaleMeter(rec),
	)
	if err != nil {
		t.Fatalf("NewHorizontalScaler: %v", err)
	}

	type step struct {
		toDepth int
		action  scheduler.DegradationAction
		level   int
	}
	steps := []step{
		{55, scheduler.ActionShed, 1},
		{75, scheduler.ActionDefer, 2},
		{90, scheduler.ActionDowngrade, 3},
		{100, scheduler.ActionReject, 4},
	}
	cur := 0
	for _, st := range steps {
		enqueueN(t, q, "acme", "P2", st.toDepth-cur)
		cur = st.toDepth
		dec, err := s.Tick(ctx)
		if err != nil {
			t.Fatalf("Tick (depth=%d): %v", st.toDepth, err)
		}
		if !dec.Degraded {
			t.Fatalf("depth=%d: devia degradar (headroom esgotado)", st.toDepth)
		}
		if dec.Action != st.action {
			t.Errorf("depth=%d: acção=%q, quero %q", st.toDepth, dec.Action, st.action)
		}
		if dec.DegradationLevel != st.level {
			t.Errorf("depth=%d: nível=%d, quero %d", st.toDepth, dec.DegradationLevel, st.level)
		}
		// A escada foi EXECUTADA por-item a partir do degrau seleccionado.
		if len(dec.Results) != 1 || dec.Results[0].Action != st.action {
			t.Errorf("depth=%d: results=%+v, quero acção executada %q", st.toDepth, dec.Results, st.action)
		}
	}

	// Métricas de degradação por acção emitidas (AC5): shed, defer, downgrade, reject.
	if got := rec.FilterByAttr(scheduler.AttrMetricAction, string(scheduler.ActionShed)); len(got) == 0 {
		t.Error("métrica de degradação 'shed' não emitida")
	}
	if got := rec.ByInstrument(scheduler.MetricDegradationLevel); len(got) != 4 {
		t.Errorf("emissões do gauge de nível = %d, quero 4", len(got))
	}
	// O último nível emitido é reject (4).
	levels := rec.ByInstrument(scheduler.MetricDegradationLevel)
	if last := levels[len(levels)-1]; last.Value != 4 {
		t.Errorf("último nível emitido = %v, quero 4 (reject)", last.Value)
	}
}

// TestScaler_NoUnboundedQueueUnderExhaustion: sob carga que excede o headroom, a fila
// NÃO acumula ilimitada (MaxLen limita-a) e o scaler responde com a escada — não com
// scale-out (AC6).
func TestScaler_NoUnboundedQueueUnderExhaustion(t *testing.T) {
	ctx := context.Background()
	q := newQueues(t, scheduler.QueueLimits{MaxLen: 50, HighWatermark: 40, LowWatermark: 20})
	// Tenta enfileirar MUITO mais do que o tecto: a fila NÃO cresce para lá de MaxLen.
	enqueueN(t, q, "acme", "P2", 500)
	if got := q.Depth(scheduler.Partition{Tenant: "acme", Priority: "P2"}); got > 50 {
		t.Fatalf("fila cresceu para %d > MaxLen 50 (acumulação ilimitada)", got)
	}

	rec := scheduler.NewRecordingMeter()
	deg, _ := newDegrader(t, scheduler.WithDegradationMeter(rec))
	pol := mustPolicy(t, scheduler.PolicyDoc{Version: "1.0.0", DefaultAction: scheduler.ActionReject})
	wait := scheduler.NewWaitP95Recorder()
	hr := &fakeHeadroom{snap: scheduler.HeadroomSnapshot{Tokens: 0, Requests: 0}}

	backlog := fixedBacklog{items: []scheduler.DegradationItem{scaleItem(), func() scheduler.DegradationItem {
		it := scaleItem()
		it.ID = "w2"
		return it
	}()}}
	s, err := scheduler.NewHorizontalScaler(testKey, hr, q, wait, scaleCfg(),
		scheduler.WithScalePolicy(pol), scheduler.WithScaleDegrader(deg), scheduler.WithScaleBacklog(backlog))
	if err != nil {
		t.Fatalf("NewHorizontalScaler: %v", err)
	}
	dec, err := s.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if !dec.Degraded {
		t.Fatal("headroom esgotado: devia degradar em vez de escalar")
	}
	if dec.DesiredReplicas != 0 { // hold: sem headroom não escala (actual=0)
		t.Errorf("desired = %d, quero 0 (sem scale-out sob headroom esgotado)", dec.DesiredReplicas)
	}
	// TODOS os itens do backlog foram tratados pela escada (nenhum deixado a acumular).
	if len(dec.Results) != len(backlog.items) {
		t.Errorf("itens tratados = %d, quero %d (a escada trata todo o backlog)", len(dec.Results), len(backlog.items))
	}
}

// TestScaler_NormalizeOnRecovery: ao recuperar headroom, o scaler reverte as
// degradações reversíveis (downgrade) via Degrader.Normalize.
func TestScaler_NormalizeOnRecovery(t *testing.T) {
	ctx := context.Background()
	q := newQueues(t, scheduler.QueueLimits{MaxLen: 100, HighWatermark: 80, LowWatermark: 40})
	enqueueN(t, q, "acme", "P2", 90)
	wait := scheduler.NewWaitP95Recorder()
	wait.Observe(ctx, 500*time.Millisecond)

	deg, es := newDegrader(t)
	pol := mustPolicy(t, scheduler.PolicyDoc{Version: "1.0.0", DefaultAction: scheduler.ActionDowngrade})
	hr := &fakeHeadroom{snap: scheduler.HeadroomSnapshot{Tokens: 0, Requests: 0}}
	s, err := scheduler.NewHorizontalScaler(testKey, hr, q, wait, scaleCfg(),
		scheduler.WithScalePolicy(pol),
		scheduler.WithScaleDegrader(deg),
		scheduler.WithScaleBacklog(fixedBacklog{items: []scheduler.DegradationItem{scaleItem()}}),
	)
	if err != nil {
		t.Fatalf("NewHorizontalScaler: %v", err)
	}

	// 1) Esgotado ⇒ downgrade executado ⇒ downgrade activo.
	dec, err := s.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick esgotado: %v", err)
	}
	if !dec.Degraded || dec.Action != scheduler.ActionDowngrade {
		t.Fatalf("dec = %+v, quero degradar com downgrade", dec)
	}
	if len(deg.ActiveDowngrades()) != 1 {
		t.Fatalf("downgrades activos = %d, quero 1", len(deg.ActiveDowngrades()))
	}

	// 2) Headroom recupera ⇒ scale-out + Normalize (reverte o downgrade).
	hr.snap = scheduler.HeadroomSnapshot{Tokens: 1_000_000, Requests: 10_000, LimitTokens: 1_000_000, LimitRequests: 10_000}
	dec2, err := s.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick recuperação: %v", err)
	}
	if dec2.Degraded {
		t.Error("com headroom não devia degradar")
	}
	if !dec2.Normalized {
		t.Error("recuperação devia normalizar (reverter downgrades)")
	}
	if len(deg.ActiveDowngrades()) != 0 {
		t.Errorf("downgrades activos após normalizar = %d, quero 0", len(deg.ActiveDowngrades()))
	}
	// O tier_restored foi persistido no log (reversão auditável).
	recs, err := deg.ReplayDegradation(ctx)
	if err != nil {
		t.Fatalf("ReplayDegradation: %v", err)
	}
	if countByType(recs, scheduler.EventTierRestored) != 1 {
		t.Errorf("tier_restored no log = %d, quero 1", countByType(recs, scheduler.EventTierRestored))
	}
	_ = es
}

// ---------------------------------------------------------------------------
// Fail-closed na construção.
// ---------------------------------------------------------------------------

func TestNewHorizontalScaler_FailClosedDeps(t *testing.T) {
	q := newQueues(t, scheduler.QueueLimits{MaxLen: 10, HighWatermark: 8, LowWatermark: 4})
	wait := scheduler.NewWaitP95Recorder()
	hr := &fakeHeadroom{}
	if _, err := scheduler.NewHorizontalScaler(testKey, nil, q, wait, scaleCfg()); err == nil {
		t.Error("headroom nil devia falhar (fail-closed)")
	}
	if _, err := scheduler.NewHorizontalScaler(testKey, hr, nil, wait, scaleCfg()); err == nil {
		t.Error("queues nil devia falhar (fail-closed)")
	}
	if _, err := scheduler.NewHorizontalScaler(testKey, hr, q, nil, scaleCfg()); err == nil {
		t.Error("wait recorder nil devia falhar (fail-closed)")
	}
}

// fakeCount reporta uma contagem fixa de réplicas correntes.
type fakeCount struct{ n int }

func (c fakeCount) ReplicaCount(context.Context) int { return c.n }

// TestScaler_ActualReplicasAndOptionsAndRun exercita as portas/opções auxiliares:
// contagem de réplicas correntes (gap desejado-vs-actual), tenant, tracer, o
// percentil configurável do recorder, a config por omissão e o laço Run.
func TestScaler_ActualReplicasAndOptionsAndRun(t *testing.T) {
	ctx := context.Background()
	q := newQueues(t, scheduler.QueueLimits{MaxLen: 1000, HighWatermark: 800, LowWatermark: 400})
	enqueueN(t, q, "acme", "P2", 64)
	wait := scheduler.NewWaitP95Recorder(scheduler.WithWaitP95Percentile(0.90))
	wait.Observe(ctx, 500*time.Millisecond)

	hr := &fakeHeadroom{snap: scheduler.HeadroomSnapshot{Tokens: 1_000_000, Requests: 10_000, LimitTokens: 1_000_000, LimitRequests: 10_000}}
	rec := scheduler.NewRecordingMeter()
	s, err := scheduler.NewHorizontalScaler(testKey, hr, q, wait, scheduler.DefaultReplicaScalerConfig(),
		scheduler.WithReplicaCountSource(fakeCount{n: 3}),
		scheduler.WithScaleTenant("acme"),
		scheduler.WithScaleTracer(agentruntime.NoopTracer{}),
		scheduler.WithScaleMeter(rec),
	)
	if err != nil {
		t.Fatalf("NewHorizontalScaler: %v", err)
	}
	dec, err := s.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if dec.ActualReplicas != 3 {
		t.Errorf("actual = %d, quero 3 (da fonte de contagem)", dec.ActualReplicas)
	}
	if len(rec.ByInstrument(scheduler.MetricActualReplicas)) == 0 {
		t.Error("gauge de actual_replicas não emitido")
	}

	// Laço Run: avalia e termina limpo ao cancelar o ctx.
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.Run(rctx, 10*time.Millisecond) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Run devia devolver o motivo de cancelamento do ctx")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run não terminou após cancelar o ctx")
	}
}

// mustPolicy constrói um PolicyEngine de teste (fail-closed na validação).
func mustPolicy(t *testing.T, doc scheduler.PolicyDoc) *scheduler.PolicyEngine {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	p, err := scheduler.NewPolicyEngine(doc, scheduler.WithPolicyLog(es), scheduler.WithPolicyClock(baseFixedClock()))
	if err != nil {
		t.Fatalf("NewPolicyEngine: %v", err)
	}
	return p
}
