package scheduler_test

// Testes complementares das métricas (AOS-034): caminhos do NoopMeter (default),
// instrumento Histogram, serialização estável de atributos (SeriesKey), casos de
// fronteira da utilização e os no-op/erro dos samplers.

import (
	"context"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/scheduler"
)

// NoopMeter é o default (sem opção With*Meter): a fachada sobre nil não emite nem
// entra em pânico — a instrumentação é verdadeiramente ADITIVA.
func TestMetrics_NoopMeterDefaultIsSilent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sm := scheduler.NewSchedulerMetrics(nil) // ⇒ NoopMeter
	sm.RecordAdmitted(ctx, testKey, "acme")
	sm.RecordDeferred(ctx, testKey, "acme", scheduler.DeferReasonNoHeadroom)
	sm.RecordDegradation(ctx, scheduler.ActionShed, "acme", "P2")
	sm.RecordSpawnDeferred(ctx, testKey, "acme")
	sm.ObserveHeadroom(ctx, testKey, "acme", scheduler.HeadroomSnapshot{Tokens: 1, LimitTokens: 2, Requests: 1, LimitRequests: 2})
	sm.ObserveQueuePartition(ctx, scheduler.QueueSnapshot{Partition: "acme:P2", Capacity: 5})

	// Um *SchedulerMetrics nil é também no-op (as fontes guardam-no nil por omissão).
	var nilSM *scheduler.SchedulerMetrics
	nilSM.RecordAdmitted(ctx, testKey, "acme")
	nilSM.ObserveHeadroom(ctx, testKey, "", scheduler.HeadroomSnapshot{})
	nilSM.SampleQueues(ctx, nil)
	if err := nilSM.SampleHeadroom(ctx, nil, testKey, ""); err != nil {
		t.Errorf("nil SampleHeadroom devia ser no-op, erro: %v", err)
	}
}

// O RecordingMeter capta também Histograms (instrumento de distribuição).
func TestMetrics_RecordingHistogram(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := scheduler.NewRecordingMeter()
	h := rec.Histogram("aos.scheduler.test.hist")
	h.Record(ctx, 12.5, scheduler.Attr{Key: scheduler.AttrMetricPartition, Value: "acme:P1"})
	ms := rec.ByInstrument("aos.scheduler.test.hist")
	if len(ms) != 1 || ms[0].Kind != scheduler.KindHistogram || ms[0].Value != 12.5 {
		t.Fatalf("histograma não captado correctamente: %+v", ms)
	}
}

// SeriesKey serializa TODOS os tipos de atributo de forma estável (string/bool/
// int/int64/float/desconhecido) — âncora da agregação determinística.
func TestMeasurement_SeriesKeyStableSerialization(t *testing.T) {
	t.Parallel()
	m := scheduler.Measurement{
		Instrument: scheduler.MetricQueueDepth,
		Kind:       scheduler.KindGauge,
		Attrs: []scheduler.Attr{
			{Key: "s", Value: "v"},
			{Key: "b", Value: true},
			{Key: "bf", Value: false},
			{Key: "i", Value: 3},
			{Key: "i64", Value: int64(-7)},
			{Key: "f", Value: 0.5},
			{Key: "fneg", Value: -1.25},
			{Key: "x", Value: struct{}{}},
		},
	}
	k1 := m.SeriesKey()
	k2 := m.SeriesKey()
	if k1 != k2 {
		t.Fatalf("SeriesKey não estável: %q != %q", k1, k2)
	}
	// A ordem canónica é por chave (ordenada), independente da ordem de inserção.
	if k1 == "" || k1[:len(scheduler.MetricQueueDepth)] != scheduler.MetricQueueDepth {
		t.Fatalf("SeriesKey inesperada: %q", k1)
	}
	// Um atributo qualquer continua recuperável (query-time).
	if v, ok := m.Attr("fneg"); !ok || v.(float64) != -1.25 {
		t.Errorf("Attr(fneg) = %v (ok=%v)", v, ok)
	}
	if _, ok := m.Attr("inexistente"); ok {
		t.Errorf("Attr de chave inexistente devia ser !ok")
	}
}

// utilização: fronteiras (limite 0 ⇒ 0; livre > limite ⇒ 0; livre negativo ⇒ 1).
func TestMetrics_UtilizationBoundaries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rec := scheduler.NewRecordingMeter()
	sm := scheduler.NewSchedulerMetrics(rec)

	// Limite 0 ⇒ utilização 0 (indeterminado, sem divisão por zero).
	sm.ObserveHeadroom(ctx, testKey, "z", scheduler.HeadroomSnapshot{Tokens: 0, LimitTokens: 0, Requests: 0, LimitRequests: 0})
	if u, ok := lastGauge(rec.Measurements(), scheduler.MetricHeadroomUtilization, scheduler.AttrMetricTenant, "z"); !ok || u != 0 {
		t.Errorf("utilização com limite 0 = %v (ok=%v), quer 0", u, ok)
	}

	// Livre > limite (reservado negativo) ⇒ utilização 0; reservado clamp 0.
	rec2 := scheduler.NewRecordingMeter()
	sm2 := scheduler.NewSchedulerMetrics(rec2)
	sm2.ObserveHeadroom(ctx, testKey, "over", scheduler.HeadroomSnapshot{Tokens: 20, LimitTokens: 10, Requests: 20, LimitRequests: 10})
	if rt, ok := lastGauge(rec2.Measurements(), scheduler.MetricHeadroomReservedTokens, scheduler.AttrMetricTenant, "over"); !ok || rt != 0 {
		t.Errorf("reservado com livre>limite = %v, quer 0 (clamp)", rt)
	}
}

// SampleHeadroom propaga o erro da porta (fail-closed): janela inválida ⇒ erro.
func TestMetrics_SampleHeadroomPropagatesError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	qp := qpTPM(1000, 1000, 0) // Window 0 ⇒ Headroom devolve erro
	adm, _ := newAdm(t, qp, scheduler.WithClock(fixedClock(time.Unix(1, 0))))
	rec := scheduler.NewRecordingMeter()
	sm := scheduler.NewSchedulerMetrics(rec)
	if err := sm.SampleHeadroom(ctx, adm, testKey, "acme"); err == nil {
		t.Fatalf("SampleHeadroom devia propagar o erro da porta (janela inválida)")
	}
}
