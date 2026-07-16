package sandbox

import (
	"context"
	"testing"
	"time"
)

// TestAccessors_PublicAPI exercita os acessores/opções triviais da API pública de
// AOS-065 (introspecção e configuração), garantindo que expõem o estado esperado.
func TestAccessors_PublicAPI(t *testing.T) {
	snap := newSnap(t)
	if snap.Version() != "img-v1" {
		t.Fatalf("Version = %q", snap.Version())
	}

	ov, _ := snap.Restore()
	if ov.ID() == "" {
		t.Fatal("Overlay.ID vazio")
	}
	if ov.ImageVersion() != "img-v1" {
		t.Fatalf("Overlay.ImageVersion = %q", ov.ImageVersion())
	}

	// WithHandoff (custo de handoff do warm hit) e WithDriverKind (eixo do SLI).
	p := newPool(t, 1,
		WithHandoff(2*time.Millisecond),
		WithDriverKind(DriverFirecracker),
		WithHandoff(-1),    // ignorado
		WithDriverKind(""), // ignorado
		WithSynchronousReplenish(),
	)
	if p.key().Driver != DriverFirecracker {
		t.Fatalf("driver do pool = %q", p.key().Driver)
	}
	lease, err := p.Reserve(context.Background())
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if lease.ColdStart() != 2*time.Millisecond {
		t.Fatalf("warm hit devia reflectir handoff 2ms, obtive %v", lease.ColdStart())
	}
	lease.Release()
}

// TestMemorySinks_Readers exercita os leitores das impls de referência in-memory.
func TestMemorySinks_Readers(t *testing.T) {
	metrics := &MemoryColdStartMetricSink{}
	alerts := &MemoryColdStartAlertSink{}
	rec := NewColdStartRecorder(
		WithColdStartTarget(1*time.Millisecond),
		WithColdStartMetricSink(metrics),
		WithColdStartAlertSink(alerts),
	)
	rec.Observe(context.Background(), nil, sample(50*time.Millisecond))
	if len(metrics.Metrics()) == 0 || metrics.Len() == 0 {
		t.Fatal("esperava métricas registadas")
	}
	if len(alerts.Alerts()) != 1 || alerts.Len() != 1 {
		t.Fatalf("esperava 1 alerta, obtive %d", alerts.Len())
	}
}
