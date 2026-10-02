package otelgenai

// AOS-402 — a escrita do selo de mediação por decisão, derivada dos mesmos wide events do SLI de
// overhead, sem SLO.

import (
	"testing"
	"time"
)

func porDecisao(t *testing.T, obs []AuditWriteLatency, dec string) AuditWriteLatency {
	t.Helper()
	for _, o := range obs {
		if o.Decision == dec {
			return o
		}
	}
	t.Fatalf("decisao %q ausente de %+v", dec, obs)
	return AuditWriteLatency{}
}

func TestAOS402_EscritaDoSeloPorDecisaoComPercentis(t *testing.T) {
	eventos := []WideEvent{
		spanDoMonitor("t1", 5*time.Millisecond, 10*time.Millisecond, time.Second),
		spanDoMonitor("t2", 5*time.Millisecond, 20*time.Millisecond, time.Second),
		spanDoMonitor("t3", 5*time.Millisecond, 30*time.Millisecond, time.Second),
		spanDoMonitor("t4", 5*time.Millisecond, 40*time.Millisecond, time.Second),
		spanDoMonitor("t5", 5*time.Millisecond, 50*time.Millisecond, time.Second),
	}
	recusa := spanDoMonitor("t6", 3*time.Millisecond, 7*time.Millisecond, 10*time.Millisecond)
	recusa.Decision = DecisionDeny
	eventos = append(eventos, recusa, spanDoWorker("t1", 2*time.Second))

	obs := MediationAuditWriteLatency(eventos)

	if len(obs) != 3 || obs[0].Decision != DecisionPermit || obs[1].Decision != DecisionDeny || obs[2].Decision != DecisionEscalate {
		t.Fatalf("as tres decisoes, pela ordem fixa; veio %+v", obs)
	}
	permit := porDecisao(t, obs, DecisionPermit)
	if permit.Samples != 5 {
		t.Fatalf("permit: 5 amostras (o span do worker nao conta); veio %d", permit.Samples)
	}
	if time.Duration(permit.P50) != 30*time.Millisecond || time.Duration(permit.Max) != 50*time.Millisecond {
		t.Fatalf("permit: p50=30ms max=50ms; veio p50=%v max=%v", time.Duration(permit.P50), time.Duration(permit.Max))
	}
	// type-7 sobre [10..50]: rank 0,95*4 = 3,8 ⇒ 40 + 0,8*10 = 48ms.
	if time.Duration(permit.P95) != 48*time.Millisecond {
		t.Fatalf("permit: p95=48ms (interpolacao); veio %v", time.Duration(permit.P95))
	}
	if d := porDecisao(t, obs, DecisionDeny); d.Samples != 1 || time.Duration(d.Max) != 7*time.Millisecond {
		t.Fatalf("deny: 1 amostra de 7ms; veio %+v", d)
	}
	if e := porDecisao(t, obs, DecisionEscalate); e.Samples != 0 || e.P95 != 0 {
		t.Fatalf("escalate sem medida fica a zero; veio %+v", e)
	}
}

// TestAOS402_SpanSemAMedidaNaoEntra — um Reference Monitor anterior ao AOS-401 decide mas não
// publica a escrita. Não entra, nem com zero: seria uma escrita que nunca foi medida.
func TestAOS402_SpanSemAMedidaNaoEntra(t *testing.T) {
	v0115 := WideEvent{
		Operation: OpExecuteTool, TraceIDHex: "t1", Decision: DecisionPermit,
		Attributes: map[string]any{AttrMediationDecisionLatencyNanos: int64(32 * time.Millisecond)},
	}
	if p := porDecisao(t, MediationAuditWriteLatency([]WideEvent{v0115}), DecisionPermit); p.Samples != 0 {
		t.Fatalf("sem o atributo da escrita nao ha amostra; veio %+v", p)
	}
}

// TestAOS402_RecusaPorContextoCanceladoNaoConta — o Reference Monitor sai antes de escrever e
// publica zero; contar esse zero puxava os percentis para baixo sem ser uma escrita.
func TestAOS402_RecusaPorContextoCanceladoNaoConta(t *testing.T) {
	cancelado := spanDoMonitor("t1", time.Millisecond, 0, time.Millisecond)
	cancelado.Decision = DecisionDeny
	cancelado.DeniedBy = "context"
	medido := spanDoMonitor("t2", time.Millisecond, 0, time.Millisecond)
	medido.Decision = DecisionDeny
	medido.DeniedBy = "policy"

	d := porDecisao(t, MediationAuditWriteLatency([]WideEvent{cancelado, medido}), DecisionDeny)
	if d.Samples != 1 {
		t.Fatalf("so a recusa que escreveu conta, mesmo com escrita de zero; veio %d amostras", d.Samples)
	}
}

// TestAOS402_DerivaDoBagDoSpan — o atributo que o Reference Monitor anota chega ao cálculo pela
// projecção em WideEvent, como no avaliador do nó.
func TestAOS402_DerivaDoBagDoSpan(t *testing.T) {
	sd := SpanData{
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpExecuteTool},
			{Key: AttrDecision, Value: DecisionPermit},
			{Key: AttrMediationPolicyLatencyNanos, Value: int64(6 * time.Millisecond)},
			{Key: AttrMediationAuditWriteLatencyNanos, Value: int64(25 * time.Millisecond)},
		},
		StartUnixNano: 1,
		EndUnixNano:   1 + int64(time.Second),
	}
	p := porDecisao(t, MediationAuditWriteLatency([]WideEvent{WideEventFromSpanData(sd)}), DecisionPermit)
	if p.Samples != 1 || time.Duration(p.P95) != 25*time.Millisecond {
		t.Fatalf("a escrita do bag tinha de chegar ao calculo (25ms); veio %+v", p)
	}
}
