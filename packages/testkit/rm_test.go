package testkit_test

import (
	"context"
	"errors"
	"testing"

	rm "github.com/aos-ref/kernel/reference-monitor"
	tk "github.com/aos-ref/testkit"
)

// TestRM_PermitDespachaERegista: com hooks neutros e um FakeEventSink, um call
// válido é permitido, a ToolSpy é despachada e o sink regista a mediação.
func TestRM_PermitDespachaERegista(t *testing.T) {
	t.Parallel()
	m, sink := tk.NewMonitor() // cadeia neutra (DefaultHooks) + FakeEventSink
	tool := tk.NewToolSpy([]byte("ok"), nil)
	if err := m.Register("tool.echo", tool.Func()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	d, err := m.Mediate(context.Background(), tk.BaseCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if !d.Permitted() {
		t.Fatalf("esperava permit, obtive %q (%s)", d.Effect, d.Reason)
	}
	if !tool.Called() {
		t.Fatal("a tool devia ter sido despachada num permit")
	}
	if sink.Count() != 1 {
		t.Fatalf("esperava 1 registo no sink, obtive %d", sink.Count())
	}
	if recs := sink.Records(); recs[0].Effect != rm.EffectPermit {
		t.Fatalf("registo com Effect=%q, esperava permit", recs[0].Effect)
	}
}

// TestRM_DenyHookNaoDespacha: um DenyHook (duplo do PDP a negar) bloqueia o
// despacho e o sink regista o deny.
func TestRM_DenyHookNaoDespacha(t *testing.T) {
	t.Parallel()
	m, sink := tk.NewMonitor(
		tk.AllowHook("identity"),
		tk.DenyHook("policy", "negado no teste"),
		tk.AllowHook("audit"),
	)
	tool := tk.NewToolSpy([]byte("ok"), nil)
	_ = m.Register("tool.echo", tool.Func())

	d, err := m.Mediate(context.Background(), tk.BaseCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != rm.EffectDeny {
		t.Fatalf("esperava deny, obtive %q", d.Effect)
	}
	if d.DeniedBy != "policy" {
		t.Fatalf("DeniedBy=%q, esperava policy", d.DeniedBy)
	}
	if tool.Called() {
		t.Fatal("a tool NAO devia ser despachada num deny")
	}
	if sink.Count() != 1 || sink.Records()[0].Effect != rm.EffectDeny {
		t.Fatalf("sink devia registar 1 deny, obtive %+v", sink.Records())
	}
}

// TestRM_OrdemDaCadeia: o HookRecorder prova que os hooks correm pela ordem dada.
func TestRM_OrdemDaCadeia(t *testing.T) {
	t.Parallel()
	rec := tk.NewHookRecorder()
	m, _ := tk.NewMonitor(
		rec.Hook("identity", rm.HookResult{Decision: rm.HookAllow}),
		rec.Hook("policy", rm.HookResult{Decision: rm.HookAllow}),
		rec.Hook("audit", rm.HookResult{Decision: rm.HookAllow}),
	)
	_ = m.Register("tool.echo", tk.NewToolSpy(nil, nil).Func())
	if _, err := m.Mediate(context.Background(), tk.BaseCall()); err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	order := rec.Order()
	want := []string{"identity", "policy", "audit"}
	if len(order) != len(want) {
		t.Fatalf("ordem=%v, esperava %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("ordem=%v, esperava %v", order, want)
		}
	}
}

// TestRM_SinkFailClosed: um erro do sink no caminho de permit degrada para deny
// (fail-closed de auditoria).
func TestRM_SinkFailClosed(t *testing.T) {
	t.Parallel()
	m, sink := tk.NewMonitor()
	sink.FailOnEffect(rm.EffectPermit, errors.New("sink em baixo"))
	tool := tk.NewToolSpy([]byte("ok"), nil)
	_ = m.Register("tool.echo", tool.Func())

	d, err := m.Mediate(context.Background(), tk.BaseCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != rm.EffectDeny {
		t.Fatalf("esperava deny (auditoria indisponivel), obtive %q", d.Effect)
	}
	if tool.Called() {
		t.Fatal("uma accao nao-auditavel nao deve ser despachada")
	}
}

// TestRM_EscalateEPanic: EscalateHook escala; um SpyHook com Panic é convertido
// em deny fail-closed pelo RM.
func TestRM_EscalateEPanic(t *testing.T) {
	t.Parallel()
	// Escalate.
	m, _ := tk.NewMonitor(tk.AllowHook("identity"), tk.EscalateHook("risk", "gate humano"))
	_ = m.Register("tool.echo", tk.NewToolSpy(nil, nil).Func())
	d, err := m.Mediate(context.Background(), tk.BaseCall())
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != rm.EffectEscalate {
		t.Fatalf("esperava escalate, obtive %q", d.Effect)
	}

	// Panic ⇒ deny fail-closed.
	panicHook := &tk.SpyHook{HookName: "boom", Panic: true}
	m2, _ := tk.NewMonitor(tk.AllowHook("identity"), panicHook)
	_ = m2.Register("tool.echo", tk.NewToolSpy(nil, nil).Func())
	d2, err := m2.Mediate(context.Background(), tk.BaseCall())
	if err != nil {
		t.Fatalf("Mediate(panic): %v", err)
	}
	if d2.Effect != rm.EffectDeny {
		t.Fatalf("panic devia dar deny fail-closed, obtive %q", d2.Effect)
	}
}

// TestRM_ToolSpyCapturaInput: a ToolSpy captura o input despachado (base de um
// teste de enforcement de obrigações antes do efeito).
func TestRM_ToolSpyCapturaInput(t *testing.T) {
	t.Parallel()
	m, _ := tk.NewMonitor()
	tool := tk.NewToolSpy([]byte("resposta"), nil)
	_ = m.Register("tool.echo", tool.Func())
	call := tk.BaseCall()
	call.Input = []byte("entrada-x")
	if _, err := m.Mediate(context.Background(), call); err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if got := string(tool.LastInput()); got != "entrada-x" {
		t.Fatalf("LastInput=%q, esperava entrada-x", got)
	}
	if tool.Calls() != 1 {
		t.Fatalf("Calls=%d, esperava 1", tool.Calls())
	}
}

// TestRM_SpyHookInspeccao: o SpyHook regista invocações e o call observado.
func TestRM_SpyHookInspeccao(t *testing.T) {
	t.Parallel()
	spy := tk.AllowHook("policy")
	m, _ := tk.NewMonitor(tk.AllowHook("identity"), spy)
	_ = m.Register("tool.echo", tk.NewToolSpy(nil, nil).Func())
	if _, err := m.Mediate(context.Background(), tk.BaseCall()); err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if spy.Invocations() != 1 {
		t.Fatalf("invocations=%d, esperava 1", spy.Invocations())
	}
	if spy.LastCall().ToolID != "tool.echo" {
		t.Fatalf("LastCall.ToolID=%q, esperava tool.echo", spy.LastCall().ToolID)
	}
}
