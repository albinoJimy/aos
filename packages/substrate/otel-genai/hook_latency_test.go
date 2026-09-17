package otelgenai

import (
	"reflect"
	"testing"
	"time"
)

func eventoComHooks(decisao, negadoPor string, hooks map[string]time.Duration) WideEvent {
	bag := map[string]any{}
	for h, d := range hooks {
		bag[MediationHookLatencyAttr(h)] = int64(d)
	}
	return WideEvent{Operation: OpExecuteTool, Decision: decisao, DeniedBy: negadoPor, Attributes: bag}
}

func TestAOS405_LatenciaPorHookComPercentis(t *testing.T) {
	ms := time.Millisecond
	events := []WideEvent{
		eventoComHooks(DecisionPermit, "", map[string]time.Duration{"identity": 1 * ms, "revalidation": 4 * ms, "policy": 2 * ms}),
		eventoComHooks(DecisionPermit, "", map[string]time.Duration{"identity": 1 * ms, "revalidation": 40 * ms, "policy": 3 * ms}),
		// Uma recusa na política: o hook que recusou conta, os seguintes não correram.
		eventoComHooks(DecisionDeny, "policy", map[string]time.Duration{"identity": 2 * ms, "policy": 5 * ms}),
	}
	got := MediationHookLatency(events)
	quer := []HookLatencyObservation{
		{Hook: "identity", Samples: 3, P50: int64(1 * ms), P95: int64(1900 * time.Microsecond), Max: int64(2 * ms)},
		{Hook: "policy", Samples: 3, P50: int64(3 * ms), P95: int64(4800 * time.Microsecond), Max: int64(5 * ms)},
		{Hook: "revalidation", Samples: 2, P50: int64(22 * ms), P95: int64(38200 * time.Microsecond), Max: int64(40 * ms)},
	}
	if !reflect.DeepEqual(got, quer) {
		t.Fatalf("MediationHookLatency =\n  %+v\nquero\n  %+v", got, quer)
	}
}

func TestAOS405_ExclusoesDaAmostra(t *testing.T) {
	ms := time.Millisecond
	events := []WideEvent{
		// Recusa por contexto cancelado: nenhum hook correu de facto.
		eventoComHooks(DecisionDeny, DeniedByContext, map[string]time.Duration{"identity": 9 * ms}),
		// execute_tool sem decisão (worker/launcher): não é o Reference Monitor.
		eventoComHooks("", "", map[string]time.Duration{"identity": 9 * ms}),
		// Outra operação.
		{Operation: OpChat, Decision: DecisionPermit, Attributes: map[string]any{MediationHookLatencyAttr("identity"): int64(9 * ms)}},
		// Valor que não é inteiro.
		{Operation: OpExecuteTool, Decision: DecisionPermit, Attributes: map[string]any{MediationHookLatencyAttr("identity"): "lento"}},
	}
	if got := MediationHookLatency(events); got != nil {
		t.Fatalf("nenhum destes eventos é amostra; veio %+v", got)
	}
}

func TestAOS405_NomeDoHookFicaSeguro(t *testing.T) {
	casos := map[string]string{
		"revalidation":       AttrMediationHookLatencyPrefix + "revalidation",
		"broker-scope":       AttrMediationHookLatencyPrefix + "broker-scope",
		"risk_aos289":        AttrMediationHookLatencyPrefix + "risk_aos289",
		`x"y{z}.w`:           AttrMediationHookLatencyPrefix + "x_y_z__w",
		"":                   AttrMediationHookLatencyPrefix + "_",
		"allowlist regional": AttrMediationHookLatencyPrefix + "allowlist_regional",
	}
	for nome, quer := range casos {
		if got := MediationHookLatencyAttr(nome); got != quer {
			t.Errorf("MediationHookLatencyAttr(%q) = %q, quero %q", nome, got, quer)
		}
	}
}

// TestAOS405_DerivaDoSpanData — o caminho real: atributos de span → wide event → observação.
func TestAOS405_DerivaDoSpanData(t *testing.T) {
	sd := SpanData{
		Attributes: []KeyValue{
			{Key: AttrOperationName, Value: OpExecuteTool},
			{Key: AttrDecision, Value: DecisionPermit},
			{Key: MediationHookLatencyAttr("egress"), Value: int64(3 * time.Millisecond)},
		},
		StartUnixNano: 1,
		EndUnixNano:   1 + int64(time.Second),
	}
	got := MediationHookLatency([]WideEvent{WideEventFromSpanData(sd)})
	if len(got) != 1 || got[0].Hook != "egress" || got[0].Max != int64(3*time.Millisecond) {
		t.Fatalf("derivação do span: %+v", got)
	}
}
