package referencemonitor

// AOS-405 — a janela da política partida por hook, na decisão e no span `execute_tool`.
//
// FALHA-ANTES: até aqui a política só existia inteira (`aos.mediation.policy_latency_ns`). Em
// produção (AOS-404) o excesso das políticas lentas estava todo num troço que junta o fsync do selo
// de revalidação com risk-classify, PDP, taint, scope, budget e egress, e nada o separava.

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

func hookQueDemora(clk *relogioManual, nome string, d time.Duration, dec HookDecision) *spyHook {
	return &spyHook{name: nome, result: HookResult{Decision: dec}, mutate: func(*Call) { clk.avancar(d) }}
}

func TestAOS405_PermitTrazALatenciaDeCadaHookPelaOrdemDaCadeia(t *testing.T) {
	clk := novoRelogioManual()
	spy := &spanSpy{}
	m := New(WithHooks(
		hookQueDemora(clk, "identity", 1*time.Millisecond, HookAllow),
		hookQueDemora(clk, "revalidation", 6*time.Millisecond, HookAllow),
		hookQueDemora(clk, "policy", 3*time.Millisecond, HookAllow),
	), WithEventSink(&sinkQueHonraOContexto{}), withClock(clk.agora))
	m.SetTracer(&tracerSpy{span: spy})
	if err := m.Register("tool.echo", func(_ context.Context, in []byte) ([]byte, error) {
		clk.avancar(time.Second)
		return in, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	dec, err := m.Mediate(context.Background(), baseCall())
	if err != nil || dec.Effect != EffectPermit {
		t.Fatalf("Mediate: effect=%v err=%v", dec.Effect, err)
	}
	quer := []HookLatency{
		{Hook: "identity", Latency: 1 * time.Millisecond},
		{Hook: "revalidation", Latency: 6 * time.Millisecond},
		{Hook: "policy", Latency: 3 * time.Millisecond},
	}
	if !reflect.DeepEqual(dec.HookLatencies, quer) {
		t.Fatalf("HookLatencies = %v, quero %v", dec.HookLatencies, quer)
	}
	// Os hooks somam-se dentro da política; a execução da tool (1 s) não entra em nenhum.
	var soma time.Duration
	for _, h := range dec.HookLatencies {
		soma += h.Latency
	}
	if soma > dec.PolicyLatency {
		t.Fatalf("soma dos hooks (%v) excede a política (%v)", soma, dec.PolicyLatency)
	}
	if dec.PolicyLatency != 10*time.Millisecond {
		t.Fatalf("PolicyLatency = %v, quero 10ms (a soma dos três hooks)", dec.PolicyLatency)
	}
	for _, h := range quer {
		v, ok := spy.atributo(otelgenai.MediationHookLatencyAttr(h.Hook))
		if !ok {
			t.Errorf("o span não anota %s", otelgenai.MediationHookLatencyAttr(h.Hook))
			continue
		}
		if got, ok := v.(int64); !ok || got != h.Latency.Nanoseconds() {
			t.Errorf("%s = %v (%T), quero %d", h.Hook, v, v, h.Latency.Nanoseconds())
		}
	}
}

func TestAOS405_RecusaSoTrazOsHooksQueCorreram(t *testing.T) {
	clk := novoRelogioManual()
	spy := &spanSpy{}
	nuncaCorre := hookQueDemora(clk, "egress", 50*time.Millisecond, HookAllow)
	m := New(WithHooks(
		hookQueDemora(clk, "identity", 2*time.Millisecond, HookAllow),
		hookQueDemora(clk, "policy", 4*time.Millisecond, HookDeny),
		nuncaCorre,
	), WithEventSink(&sinkQueHonraOContexto{}), withClock(clk.agora))
	m.SetTracer(&tracerSpy{span: spy})
	_ = m.Register("tool.echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil })

	dec, _ := m.Mediate(context.Background(), baseCall())
	if dec.Effect != EffectDeny {
		t.Fatalf("esperava deny, veio %v", dec.Effect)
	}
	quer := []HookLatency{{Hook: "identity", Latency: 2 * time.Millisecond}, {Hook: "policy", Latency: 4 * time.Millisecond}}
	if !reflect.DeepEqual(dec.HookLatencies, quer) {
		t.Fatalf("HookLatencies = %v, quero %v (o hook que recusou incluído, o seguinte não)", dec.HookLatencies, quer)
	}
	if _, ok := spy.atributo(otelgenai.MediationHookLatencyAttr("egress")); ok {
		t.Fatal("um hook que não correu não pode ter latência no span")
	}
}

func TestAOS405_HookComErroTambemTraz(t *testing.T) {
	clk := novoRelogioManual()
	falha := hookQueDemora(clk, "budget", 7*time.Millisecond, HookAllow)
	falha.err = errors.New("orçamento ilegível")
	m := New(WithHooks(falha), WithEventSink(&sinkQueHonraOContexto{}), withClock(clk.agora))
	_ = m.Register("tool.echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil })

	dec, _ := m.Mediate(context.Background(), baseCall())
	if dec.Effect != EffectDeny {
		t.Fatalf("esperava deny, veio %v", dec.Effect)
	}
	quer := []HookLatency{{Hook: "budget", Latency: 7 * time.Millisecond}}
	if !reflect.DeepEqual(dec.HookLatencies, quer) {
		t.Fatalf("HookLatencies = %v, quero %v", dec.HookLatencies, quer)
	}
}

func TestAOS405_NomesRepetidosSomamNoSpan(t *testing.T) {
	clk := novoRelogioManual()
	spy := &spanSpy{}
	m := New(WithHooks(
		hookQueDemora(clk, "risk", 2*time.Millisecond, HookAllow),
		hookQueDemora(clk, "risk", 3*time.Millisecond, HookAllow),
	), WithEventSink(&sinkQueHonraOContexto{}), withClock(clk.agora))
	m.SetTracer(&tracerSpy{span: spy})
	_ = m.Register("tool.echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil })

	dec, _ := m.Mediate(context.Background(), baseCall())
	if len(dec.HookLatencies) != 2 {
		t.Fatalf("a decisão guarda cada execução: %v", dec.HookLatencies)
	}
	v, ok := spy.atributo(otelgenai.MediationHookLatencyAttr("risk"))
	if !ok || v.(int64) != int64(5*time.Millisecond) {
		t.Fatalf("o span soma os dois `risk`: veio %v (ok=%v), quero 5ms", v, ok)
	}
}

func TestAOS405_ContextoCanceladoNaoTemHooks(t *testing.T) {
	clk := novoRelogioManual()
	m := New(WithHooks(hookQueDemora(clk, "identity", time.Millisecond, HookAllow)),
		WithEventSink(&sinkQueHonraOContexto{}), withClock(clk.agora))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dec, _ := m.Mediate(ctx, baseCall())
	if dec.HookLatencies != nil {
		t.Fatalf("com o contexto cancelado nenhum hook corre: %v", dec.HookLatencies)
	}
}
