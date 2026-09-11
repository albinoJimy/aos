package referencemonitor

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// aos090_outcome_test.go — ADR-025/AOS-090: o RM regista o DESFECHO pós-efeito de uma tool
// call permitida (ok/erro de execução), a fonte honesta de fiabilidade que o selo de decisão
// (permit, antes do efeito) é cego. FAIL-OPEN na medição, sem tocar no fail-closed do permit.

type fakeOutcomeSink struct {
	mu   sync.Mutex
	recs []OutcomeRecord
	erro error
}

func (f *fakeOutcomeSink) RecordOutcome(_ context.Context, rec OutcomeRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recs = append(f.recs, rec)
	return f.erro
}

func (f *fakeOutcomeSink) todos() []OutcomeRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]OutcomeRecord, len(f.recs))
	copy(out, f.recs)
	return out
}

func chamadaComClasse(classe string) Call {
	c := baseCall()
	c.Principal.AgentClass = classe
	return c
}

// TestAOS090_Outcome_PermitOKEmiteDesfechoComClasse — uma tool permitida que corre bem emite um
// desfecho `ok` com a classe do agente e a capability (de onde o domínio deriva).
func TestAOS090_Outcome_PermitOKEmiteDesfechoComClasse(t *testing.T) {
	out := &fakeOutcomeSink{}
	hook := &spyHook{name: "policy", result: HookResult{Decision: HookAllow}}
	m := New(WithHooks(hook), WithEventSink(&sinkQueHonraOContexto{}), WithOutcomeSink(out))
	var correu bool
	if err := m.Register("tool.echo", toolSpy(&correu, []byte("ok"))); err != nil {
		t.Fatal(err)
	}
	d, err := m.Mediate(context.Background(), chamadaComClasse("worker"))
	if err != nil || d.Effect != EffectPermit {
		t.Fatalf("permit esperado: eff=%q err=%v", d.Effect, err)
	}
	recs := out.todos()
	if len(recs) != 1 {
		t.Fatalf("um desfecho esperado; veio %d", len(recs))
	}
	if recs[0].Outcome != OutcomeOK || recs[0].AgentClass != "worker" || recs[0].Capability != "cap:echo" {
		t.Fatalf("desfecho errado: %+v", recs[0])
	}
}

// TestAOS090_Outcome_ToolErroEmiteDesfechoError — uma tool PERMITIDA que rebenta emite um
// desfecho `error`. É exactamente o que o selo de decisão (permit) não distingue.
func TestAOS090_Outcome_ToolErroEmiteDesfechoError(t *testing.T) {
	out := &fakeOutcomeSink{}
	hook := &spyHook{name: "policy", result: HookResult{Decision: HookAllow}}
	m := New(WithHooks(hook), WithEventSink(&sinkQueHonraOContexto{}), WithOutcomeSink(out))
	if err := m.Register("tool.echo", func(context.Context, []byte) ([]byte, error) {
		return nil, errors.New("a tool rebentou")
	}); err != nil {
		t.Fatal(err)
	}
	d, err := m.Mediate(context.Background(), chamadaComClasse("worker"))
	if err != nil {
		t.Fatalf("Mediate: %v", err)
	}
	if d.Effect != EffectPermit || d.ToolErr == nil {
		t.Fatalf("a DECISÃO é permit e o ERRO é da execução: eff=%q toolErr=%v", d.Effect, d.ToolErr)
	}
	recs := out.todos()
	if len(recs) != 1 || recs[0].Outcome != OutcomeError || recs[0].ErrorKind == "" {
		t.Fatalf("desfecho `error` esperado: %+v", recs)
	}
}

// TestAOS090_Outcome_DenyNaoEmiteDesfecho — uma call NEGADA não corre, logo não há desfecho de
// execução (só o selo de decisão).
func TestAOS090_Outcome_DenyNaoEmiteDesfecho(t *testing.T) {
	out := &fakeOutcomeSink{}
	hook := &spyHook{name: "policy", result: HookResult{Decision: HookDeny, Reason: "nao"}}
	m := New(WithHooks(hook), WithEventSink(&sinkQueHonraOContexto{}), WithOutcomeSink(out))
	var correu bool
	if err := m.Register("tool.echo", toolSpy(&correu, []byte("x"))); err != nil {
		t.Fatal(err)
	}
	d, _ := m.Mediate(context.Background(), chamadaComClasse("worker"))
	if d.Effect != EffectDeny {
		t.Fatalf("deny esperado: %q", d.Effect)
	}
	if correu {
		t.Fatal("a tool não devia correr numa negação")
	}
	if n := len(out.todos()); n != 0 {
		t.Fatalf("uma call negada não corre — não há desfecho; veio %d", n)
	}
}

// TestAOS090_Outcome_ErroDoSinkNaoMudaADecisao — FAIL-OPEN na medição: um OutcomeSink que
// falha NÃO altera o permit (o efeito já aconteceu). Contrasta com o EventSink no permit.
func TestAOS090_Outcome_ErroDoSinkNaoMudaADecisao(t *testing.T) {
	out := &fakeOutcomeSink{erro: errors.New("event store em baixo")}
	hook := &spyHook{name: "policy", result: HookResult{Decision: HookAllow}}
	m := New(WithHooks(hook), WithEventSink(&sinkQueHonraOContexto{}), WithOutcomeSink(out))
	var correu bool
	if err := m.Register("tool.echo", toolSpy(&correu, []byte("ok"))); err != nil {
		t.Fatal(err)
	}
	d, err := m.Mediate(context.Background(), chamadaComClasse("worker"))
	if err != nil || d.Effect != EffectPermit || !correu {
		t.Fatalf("o erro do sink de desfecho não pode mudar o permit: eff=%q correu=%v err=%v", d.Effect, correu, err)
	}
}

// TestAOS090_Outcome_SemSinkNaoRegista — sem OutcomeSink composto, o RM não regista desfechos
// (comportamento anterior a ADR-025; zero overhead).
func TestAOS090_Outcome_SemSinkNaoRegista(t *testing.T) {
	hook := &spyHook{name: "policy", result: HookResult{Decision: HookAllow}}
	m := New(WithHooks(hook), WithEventSink(&sinkQueHonraOContexto{}))
	var correu bool
	if err := m.Register("tool.echo", toolSpy(&correu, []byte("ok"))); err != nil {
		t.Fatal(err)
	}
	if d, err := m.Mediate(context.Background(), chamadaComClasse("worker")); err != nil || d.Effect != EffectPermit || !correu {
		t.Fatalf("permit + tool corre sem sink de desfecho: eff=%q correu=%v err=%v", d.Effect, correu, err)
	}
}
