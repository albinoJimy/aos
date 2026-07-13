package scheduler_test

// Testes das MÉTRICAS de saturação e reserva de headroom (AOS-034). Todos
// deterministas: relógio/IDs injectáveis, sem time.Now/rand nas decisões.
// Reutilizam os helpers de admission_test.go / backpressure_test.go / degradation_test.go
// (mesmo pacote scheduler_test): fixedClock, seqIDGen, testKey, newAdm, qpTPM,
// newQueues, newDegrader, baseItem, baseTrigger.
//
// Cobrem os Testes Requeridos do ticket:
//   - unit: emissão de CADA métrica com os atributos correctos (nomes OTel estáveis);
//   - integração: sob carga sintética, as métricas reflectem saturação/headroom REAIS
//     e o alerta DISPARA em headroom crítico / saturação sustentada;
//   - integração: filtragem QUERY-TIME preserva o sinal (nada perdido no emit-time);
//   - dashboard mínimo agrega o estado correctamente;
//   - sem segredos nos atributos.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/scheduler"
)

// ---------------------------------------------------------------------------
// Helpers locais (spawn coordinator + alertas).
// ---------------------------------------------------------------------------

// newSpawnCoordForMetrics constrói um coordenador cujo admission NÃO concede
// headroom (TPM=1 << custo), forçando o caminho de spawn adiado.
func newSpawnCoordForMetrics(t *testing.T, base time.Time, rec *scheduler.RecordingMeter) *scheduler.SpawnCoordinator {
	t.Helper()
	adm, _ := newAdmForSpawn(t, 1, 1_000_000, base) // TPM=1: custo 1000 > tecto ⇒ não concede
	b, _ := budget.New("run-M", budget.Amount{Tokens: 1_000_000, CostMicroUSD: 1_000_000})
	sp := &budgetSpawner{b: b}
	coord, err := scheduler.NewSpawnCoordinator(adm, sp,
		scheduler.WithSpawnClock(fixed(base)),
		scheduler.WithSpawnMeter(rec),
	)
	if err != nil {
		t.Fatalf("NewSpawnCoordinator: %v", err)
	}
	return coord
}

// spawnReqNoHeadroom é um pedido cujo custo excede o tecto do bucket (defer/reject).
func spawnReqNoHeadroom() scheduler.SpawnAdmitRequest {
	return spawnReq("run-M", "child-1", 1000, budget.Amount{Tokens: 1000, CostMicroUSD: 1000})
}

// alertFired indica se um alerta com o nome dado disparou na lista.
func alertFired(alerts []scheduler.Alert, name string) bool {
	for _, a := range alerts {
		if a.Name == name && a.Fired {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Helpers de asserção sobre o RecordingMeter.
// ---------------------------------------------------------------------------

// countInstrument soma os valores de um counter (ou conta gauges) de um instrumento.
func sumInstrument(ms []scheduler.Measurement, name string) float64 {
	var s float64
	for _, m := range ms {
		if m.Instrument == name {
			s += m.Value
		}
	}
	return s
}

// lastGauge devolve o último valor de um gauge com um atributo == valor.
func lastGauge(ms []scheduler.Measurement, name, attrKey, attrVal string) (float64, bool) {
	var val float64
	var found bool
	for _, m := range ms {
		if m.Instrument != name {
			continue
		}
		if v, ok := m.Attr(attrKey); ok && attrEq(v, attrVal) {
			val = m.Value
			found = true
		}
	}
	return val, found
}

func attrEq(v any, want string) bool {
	switch t := v.(type) {
	case string:
		return t == want
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Unit: a fachada emite CADA métrica com os atributos correctos (nomes estáveis).
// ---------------------------------------------------------------------------

func TestMetrics_FacadeEmitsEachMetricWithStableAttrs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := scheduler.NewRecordingMeter()
	sm := scheduler.NewSchedulerMetrics(rec)

	sm.RecordAdmitted(ctx, testKey, "acme")
	sm.RecordDeferred(ctx, testKey, "acme", scheduler.DeferReasonNoHeadroom)
	sm.RecordDeferred(ctx, testKey, "acme", scheduler.DeferReasonBackpressure)
	sm.RecordDegradation(ctx, scheduler.ActionShed, "acme", "P2")
	sm.RecordSpawnDeferred(ctx, testKey, "acme")
	sm.ObserveHeadroom(ctx, testKey, "acme", scheduler.HeadroomSnapshot{
		Tokens: 40, Requests: 900, LimitTokens: 1000, LimitRequests: 1000,
	})
	sm.ObserveQueuePartition(ctx, scheduler.QueueSnapshot{
		Partition: "acme:P2", Tenant: "acme", Priority: "P2", Depth: 5, Capacity: 5, OldestAgeMs: 1200, Saturated: true,
	})

	ms := rec.Measurements()

	// Cada métrica emitida?
	wants := []string{
		scheduler.MetricAdmitted, scheduler.MetricDeferred, scheduler.MetricDegradation,
		scheduler.MetricSpawnDeferred, scheduler.MetricHeadroomFreeTokens,
		scheduler.MetricHeadroomFreeRequests, scheduler.MetricHeadroomReservedTokens,
		scheduler.MetricHeadroomUtilization, scheduler.MetricQueueDepth,
		scheduler.MetricQueueOldestAge, scheduler.MetricQueueSaturated,
	}
	for _, w := range wants {
		if len(rec.ByInstrument(w)) == 0 {
			t.Errorf("métrica %q não foi emitida", w)
		}
	}

	// Atributos estáveis: admitted carrega a chave decomposta.
	adm := rec.ByInstrument(scheduler.MetricAdmitted)[0]
	if v, _ := adm.Attr(scheduler.AttrMetricKey); v != testKey.String() {
		t.Errorf("admitted provider_key = %v, quer %q", v, testKey.String())
	}
	if v, _ := adm.Attr(scheduler.AttrMetricProvider); v != testKey.Provider {
		t.Errorf("admitted provider = %v", v)
	}
	// Defer carrega o motivo.
	defs := rec.ByInstrument(scheduler.MetricDeferred)
	reasons := map[string]bool{}
	for _, m := range defs {
		if v, ok := m.Attr(scheduler.AttrMetricDeferReason); ok {
			reasons[v.(string)] = true
		}
	}
	if !reasons[scheduler.DeferReasonNoHeadroom] || !reasons[scheduler.DeferReasonBackpressure] {
		t.Errorf("defer reasons = %v, quer no_headroom+backpressure", reasons)
	}
	// Degradação carrega a acção.
	deg := rec.ByInstrument(scheduler.MetricDegradation)[0]
	if v, _ := deg.Attr(scheduler.AttrMetricAction); v != string(scheduler.ActionShed) {
		t.Errorf("degradation action = %v, quer shed", v)
	}
	// Headroom livre reflecte o snapshot; reservado = limite - livre.
	if ft, ok := lastGauge(ms, scheduler.MetricHeadroomFreeTokens, scheduler.AttrMetricKey, testKey.String()); !ok || ft != 40 {
		t.Errorf("free_tokens = %v (ok=%v), quer 40", ft, ok)
	}
	if rt, ok := lastGauge(ms, scheduler.MetricHeadroomReservedTokens, scheduler.AttrMetricKey, testKey.String()); !ok || rt != 960 {
		t.Errorf("reserved_tokens = %v (ok=%v), quer 960", rt, ok)
	}
	// Utilização por dimensão tokens = 960/1000 = 0.96.
	if u, ok := lastGauge(ms, scheduler.MetricHeadroomUtilization, scheduler.AttrMetricDimension, "tokens"); !ok || u < 0.959 || u > 0.961 {
		t.Errorf("utilization tokens = %v (ok=%v), quer ~0.96", u, ok)
	}
	// Queue saturated = 1.
	if s, ok := lastGauge(ms, scheduler.MetricQueueSaturated, scheduler.AttrMetricPartition, "acme:P2"); !ok || s != 1 {
		t.Errorf("queue saturated = %v (ok=%v), quer 1", s, ok)
	}
}

// ---------------------------------------------------------------------------
// Unit: instrumentação ADITIVA do admission — admitted/deferred (defer-rate).
// ---------------------------------------------------------------------------

func TestMetrics_AdmissionEmitsAdmittedAndDeferred(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := time.Unix(2_000_000, 0)
	// TPM 1000, custo 400: dois grants (800), o terceiro não cabe (defer).
	qp := qpTPM(1000, 1_000_000, time.Minute)
	rec := scheduler.NewRecordingMeter()
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithIDGen(seqIDGen()),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 400}),
		scheduler.WithAdmissionMeter(rec),
	)

	for i := 0; i < 3; i++ {
		if _, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, Tenant: "acme"}); err != nil {
			t.Fatalf("Admit %d: %v", i, err)
		}
	}
	ms := rec.Measurements()
	if got := sumInstrument(ms, scheduler.MetricAdmitted); got != 2 {
		t.Errorf("admitted = %v, quer 2", got)
	}
	if got := sumInstrument(ms, scheduler.MetricDeferred); got != 1 {
		t.Errorf("deferred = %v, quer 1", got)
	}
	// O defer foi por falta de headroom (não backpressure).
	if len(rec.FilterByAttr(scheduler.AttrMetricDeferReason, scheduler.DeferReasonNoHeadroom)) != 1 {
		t.Errorf("defer no_headroom não registado")
	}
}

// ---------------------------------------------------------------------------
// Unit: instrumentação ADITIVA da degradação — counter por acção.
// ---------------------------------------------------------------------------

func TestMetrics_DegradationEmitsPerAction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := scheduler.NewRecordingMeter()
	d, _ := newDegrader(t, scheduler.WithDegradationMeter(rec))

	if _, err := d.Shed(ctx, baseItem(), baseTrigger()); err != nil {
		t.Fatalf("Shed: %v", err)
	}
	if _, err := d.Defer(ctx, baseItem(), baseTrigger()); err != nil {
		t.Fatalf("Defer: %v", err)
	}
	if _, err := d.Downgrade(ctx, baseItem(), baseTrigger()); err != nil {
		t.Fatalf("Downgrade: %v", err)
	}

	shed := rec.FilterByAttr(scheduler.AttrMetricAction, string(scheduler.ActionShed))
	deferm := rec.FilterByAttr(scheduler.AttrMetricAction, string(scheduler.ActionDefer))
	down := rec.FilterByAttr(scheduler.AttrMetricAction, string(scheduler.ActionDowngrade))
	if len(shed) != 1 || len(deferm) != 1 || len(down) != 1 {
		t.Errorf("acções shed=%d defer=%d downgrade=%d, quer 1 cada", len(shed), len(deferm), len(down))
	}
	// A partição/tenant viajam no wide event.
	if v, _ := shed[0].Attr(scheduler.AttrMetricTenant); v != "acme" {
		t.Errorf("degradation tenant = %v, quer acme", v)
	}
}

// ---------------------------------------------------------------------------
// Unit: instrumentação ADITIVA do spawn — counter de spawns adiados.
// ---------------------------------------------------------------------------

func TestMetrics_SpawnDeferredCounter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := time.Unix(3_000_000, 0)
	rec := scheduler.NewRecordingMeter()
	// Constrói coordenador com headroom nulo forçando defer, via helper local.
	sc := newSpawnCoordForMetrics(t, base, rec)

	_, err := sc.RequestSpawn(ctx, spawnReqNoHeadroom())
	if err == nil {
		t.Fatalf("esperava defer/erro por falta de headroom")
	}
	if got := sumInstrument(rec.Measurements(), scheduler.MetricSpawnDeferred); got != 1 {
		t.Errorf("spawn.deferred = %v, quer 1", got)
	}
	// O custo em micro-USD do sub-orçamento reservado viaja no wide event (dimensão $
	// visível na observabilidade — ADR-010). spawnReqNoHeadroom reserva 1000 µUSD.
	sd := rec.ByInstrument(scheduler.MetricSpawnDeferred)
	if len(sd) != 1 {
		t.Fatalf("spawn.deferred medições = %d, quer 1", len(sd))
	}
	cost, ok := sd[0].Attr(scheduler.AttrMetricCostMicroUSD)
	if !ok {
		t.Fatalf("spawn.deferred sem atributo de custo %q", scheduler.AttrMetricCostMicroUSD)
	}
	if c, _ := cost.(int64); c != 1000 {
		t.Errorf("cost_micro_usd = %v, quer 1000", cost)
	}
}

// ---------------------------------------------------------------------------
// Integração: sob carga, SampleHeadroom reflecte o headroom REAL do Admit.
// ---------------------------------------------------------------------------

func TestMetrics_SampleHeadroomReflectsReal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := time.Unix(4_000_000, 0)
	qp := qpTPM(1000, 1_000_000, time.Minute)
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithIDGen(seqIDGen()),
		scheduler.WithCostEstimator(scheduler.FixedCostEstimator{Tokens: 1}),
	)
	// Reserva 960 tokens (deixa 40 livres → 4% ratio).
	res, err := adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, Tenant: "acme", EstimatedTokens: 960})
	if err != nil || !res.Granted {
		t.Fatalf("Admit: granted=%v err=%v", res.Granted, err)
	}

	rec := scheduler.NewRecordingMeter()
	sm := scheduler.NewSchedulerMetrics(rec)
	if err := sm.SampleHeadroom(ctx, adm, testKey, "acme"); err != nil {
		t.Fatalf("SampleHeadroom: %v", err)
	}
	ms := rec.Measurements()
	if ft, ok := lastGauge(ms, scheduler.MetricHeadroomFreeTokens, scheduler.AttrMetricKey, testKey.String()); !ok || ft != 40 {
		t.Errorf("free_tokens amostrado = %v (ok=%v), quer 40 (headroom REAL)", ft, ok)
	}
	if rt, ok := lastGauge(ms, scheduler.MetricHeadroomReservedTokens, scheduler.AttrMetricKey, testKey.String()); !ok || rt != 960 {
		t.Errorf("reserved_tokens amostrado = %v (ok=%v), quer 960", rt, ok)
	}
}

// ---------------------------------------------------------------------------
// Integração: SampleQueues reflecte a profundidade/saturação REAL das filas.
// ---------------------------------------------------------------------------

func TestMetrics_SampleQueuesReflectsDepth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	q := newQueues(t, scheduler.QueueLimits{MaxLen: 3, HighWatermark: 3, LowWatermark: 1})
	for i := 0; i < 3; i++ {
		if _, err := q.Enqueue(ctx, scheduler.WorkItem{ID: "x", Tenant: "acme", Priority: "P1"}); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
	rec := scheduler.NewRecordingMeter()
	sm := scheduler.NewSchedulerMetrics(rec)
	sm.SampleQueues(ctx, q)

	ms := rec.Measurements()
	if depth, ok := lastGauge(ms, scheduler.MetricQueueDepth, scheduler.AttrMetricPartition, "acme:P1"); !ok || depth != 3 {
		t.Errorf("queue depth amostrado = %v (ok=%v), quer 3 (REAL)", depth, ok)
	}
	if sat, ok := lastGauge(ms, scheduler.MetricQueueSaturated, scheduler.AttrMetricPartition, "acme:P1"); !ok || sat != 1 {
		t.Errorf("queue saturated amostrado = %v (ok=%v), quer 1", sat, ok)
	}
}

// ---------------------------------------------------------------------------
// Integração: WIDE EVENTS — filtragem QUERY-TIME preserva o sinal. Um atributo
// NÃO usado como eixo no emit continua disponível para query (nada perdido).
// ---------------------------------------------------------------------------

func TestMetrics_WideEvents_QueryTimePreservesUnfilteredAttr(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := scheduler.NewRecordingMeter()
	sm := scheduler.NewSchedulerMetrics(rec)

	// Emite admissões com um atributo EXTRA de alta cardinalidade (board) que NENHUMA
	// métrica "agrega" no emit-time. No emit não se filtra nada.
	sm.RecordAdmitted(ctx, testKey, "acme", scheduler.Attr{Key: "aos.board", Value: "board-42"})
	sm.RecordAdmitted(ctx, testKey, "acme", scheduler.Attr{Key: "aos.board", Value: "board-7"})
	sm.RecordDeferred(ctx, testKey, "acme", scheduler.DeferReasonNoHeadroom,
		scheduler.Attr{Key: "aos.board", Value: "board-42"})

	// QUERY-TIME: pergunta nova (só board-42) responde-se sobre dados já recolhidos,
	// sem reinstrumentar. O atributo board NÃO foi filtrado no emit — está disponível.
	got := rec.FilterByAttr("aos.board", "board-42")
	if len(got) != 2 {
		t.Fatalf("query board-42 = %d medições, quer 2 (1 admit + 1 defer) — sinal perdido no emit?", len(got))
	}
	// E o cruzamento com o instrumento continua possível (nada destruído).
	var admits int
	for _, m := range got {
		if m.Instrument == scheduler.MetricAdmitted {
			admits++
		}
	}
	if admits != 1 {
		t.Errorf("query board-42 ∩ admitted = %d, quer 1", admits)
	}
}

// ---------------------------------------------------------------------------
// Integração: sob CARGA SINTÉTICA concorrente, as métricas reflectem
// saturação/headroom REAIS e o ALERTA dispara em headroom crítico. (-race)
// ---------------------------------------------------------------------------

func TestMetrics_UnderLoad_ReflectsHeadroomAndAlertFires(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := time.Unix(5_000_000, 0)
	// TPM 1000; cada worker reserva 100; 10 workers concorrentes saturam ao tecto.
	qp := qpTPM(1000, 1_000_000, time.Minute)
	rec := scheduler.NewRecordingMeter()
	adm, _ := newAdm(t, qp,
		scheduler.WithClock(fixedClock(base)),
		scheduler.WithIDGen(seqIDGen()),
		scheduler.WithAdmissionMeter(rec),
	)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = adm.Admit(ctx, scheduler.AdmitRequest{Key: testKey, Tenant: "acme", EstimatedTokens: 100})
		}()
	}
	wg.Wait()

	// Amostra o headroom REAL após a carga.
	sm := scheduler.NewSchedulerMetrics(rec)
	if err := sm.SampleHeadroom(ctx, adm, testKey, "acme"); err != nil {
		t.Fatalf("SampleHeadroom: %v", err)
	}

	// Dashboard agregado + avaliação de alerta.
	cfg := scheduler.DefaultSLOConfig() // crit free ratio 0.05
	ev := scheduler.NewSLOEvaluator(cfg)
	dash := scheduler.BuildDashboard(rec.Measurements(), ev)

	// As reservas concedidas NUNCA excedem o tecto (invariante do AOS-027 reflectido
	// na métrica): free >= 0 e reservado <= 1000.
	if dash.TotalFreeTokens < 0 {
		t.Fatalf("headroom livre negativo (oversubscription refletido): %d", dash.TotalFreeTokens)
	}
	// Com o tecto saturado, a fracção livre mínima é criticamente baixa → alerta crítico.
	if dash.MinHeadroomFreeRatio >= cfg.HeadroomCriticalFreeRatio {
		t.Fatalf("fracção livre %.3f não é crítica — carga não saturou o tecto?", dash.MinHeadroomFreeRatio)
	}
	if !alertFired(dash.Alerts, scheduler.AlertHeadroomCritical) {
		t.Errorf("alerta %q não disparou sob headroom crítico", scheduler.AlertHeadroomCritical)
	}
}

// ---------------------------------------------------------------------------
// No secrets: nenhum atributo expõe tokens/chaves/PII.
// ---------------------------------------------------------------------------

func TestMetrics_NoSecretsInAttributes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := scheduler.NewRecordingMeter()
	sm := scheduler.NewSchedulerMetrics(rec)
	sm.RecordAdmitted(ctx, testKey, "acme")
	sm.RecordDeferred(ctx, testKey, "acme", scheduler.DeferReasonBackpressure)
	sm.RecordDegradation(ctx, scheduler.ActionReject, "acme", "P0")
	sm.ObserveHeadroom(ctx, testKey, "acme", scheduler.HeadroomSnapshot{Tokens: 1, LimitTokens: 2, Requests: 1, LimitRequests: 2})
	sm.ObserveQueuePartition(ctx, scheduler.QueueSnapshot{Partition: "acme:P0", Tenant: "acme", Priority: "P0", Capacity: 5})

	forbidden := []string{"secret", "token", "apikey", "api_key", "password", "passwd", "bearer", "authorization", "sk-"}
	for _, m := range rec.Measurements() {
		for _, a := range m.Attrs {
			lk := strings.ToLower(a.Key)
			for _, f := range forbidden {
				if strings.Contains(lk, f) {
					t.Errorf("atributo suspeito de segredo na chave: %q (métrica %q)", a.Key, m.Instrument)
				}
			}
			if s, ok := a.Value.(string); ok {
				ls := strings.ToLower(s)
				if strings.HasPrefix(ls, "sk-") || strings.Contains(ls, "bearer ") {
					t.Errorf("valor suspeito de segredo: %q (chave %q)", s, a.Key)
				}
			}
		}
	}
}
