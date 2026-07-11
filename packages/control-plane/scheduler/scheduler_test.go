package scheduler_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/contract"
	"github.com/aos-ref/control-plane/scheduler"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/bus"
	"github.com/aos-ref/substrate/eventstore"
)

const toyTool = "tool:echo"

// harness monta a composição completa do plano de controlo de brinquedo:
// Event Store + barramento + Reference Monitor + Orquestrador + Escalonador.
type harness struct {
	es    *eventstore.Store
	bus   *bus.Bus
	rm    *referencemonitor.Monitor
	orch  *orchestrator.Orchestrator
	sch   *scheduler.Scheduler
	calls *atomic.Int64 // efeito observável da tool de brinquedo
	ctx   context.Context
}

// newHarness constrói a composição. hooks é a cadeia do RM: DefaultHooks permite
// (fluxo completa); uma cadeia com um hook de negação prova o gate.
func newHarness(t *testing.T, hooks ...referencemonitor.Hook) *harness {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	b, err := bus.New(es)
	if err != nil {
		t.Fatalf("bus.New: %v", err)
	}
	rm := referencemonitor.New(
		referencemonitor.WithHooks(hooks...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
	)
	var calls atomic.Int64
	// A tool de brinquedo é registada NO RM. O Escalonador nunca a vê: só a pode
	// executar via rm.Mediate (no-bypass estrutural do AOS-003).
	if err := rm.Register(toyTool, func(_ context.Context, input []byte) ([]byte, error) {
		calls.Add(1)
		return append([]byte("echo:"), input...), nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	orch, err := orchestrator.New(b)
	if err != nil {
		t.Fatalf("orchestrator.New: %v", err)
	}
	sch, err := scheduler.New(b, rm, es)
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}
	t.Cleanup(func() {
		sch.Stop()
		_ = b.Close()
		_ = es.Close()
	})
	return &harness{es: es, bus: b, rm: rm, orch: orch, sch: sch, calls: &calls, ctx: context.Background()}
}

// collect subscreve um coletor de eventos por tipo. Chamar ANTES de Submit.
func (h *harness) collect(t *testing.T, evType string) <-chan eventstore.Event {
	t.Helper()
	ch := make(chan eventstore.Event, 8)
	sub, err := h.bus.Subscribe(h.ctx, bus.SubConfig{
		Name:   "coletor:" + evType,
		Filter: bus.Filter{Types: []string{evType}},
		Handler: func(d *bus.Delivery) {
			d.Ack()
			ch <- d.Event
		},
	})
	if err != nil {
		t.Fatalf("subscribe %s: %v", evType, err)
	}
	t.Cleanup(sub.Unsubscribe)
	return ch
}

func waitEvent(t *testing.T, ch <-chan eventstore.Event) eventstore.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timeout à espera de evento")
		return eventstore.Event{}
	}
}

func toyGoal() contract.Goal {
	return contract.Goal{
		Objective: "eco de brinquedo",
		Task: contract.TaskSpec{
			ToolID:     toyTool,
			Capability: "cap:echo",
			Resource:   contract.ResourceSpec{Type: "toy", Value: "echo"},
			Input:      []byte("olá"),
		},
	}
}

// TestToyEndToEnd é o fluxo e2e de brinquedo: submit → task.ready → schedule →
// tool call mediada pelo RM → task.complete, correlacionado por run_id.
// Determinístico: sincronização por canais/ACK, sem sleeps.
func TestToyEndToEnd(t *testing.T) {
	t.Parallel()
	h := newHarness(t, referencemonitor.DefaultHooks()...)
	completeCh := h.collect(t, contract.EventTaskComplete)

	// O Escalonador tem de subscrever ANTES do Submit (entrega live).
	if err := h.sch.Start(h.ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	runID, err := h.orch.Submit(h.ctx, toyGoal())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	ev := waitEvent(t, completeCh)
	// Correlação por run_id (stream e payload).
	if ev.StreamID != string(runID) {
		t.Fatalf("task.complete no stream %q, quero %q", ev.StreamID, runID)
	}
	var tp contract.TaskPayload
	if err := json.Unmarshal(ev.Payload, &tp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tp.RunID != string(runID) {
		t.Fatalf("task.complete.run_id=%q, quero %q", tp.RunID, runID)
	}
	if tp.State != string(contract.StateComplete) {
		t.Fatalf("state=%q, quero complete", tp.State)
	}
	if string(tp.Output) != "echo:olá" {
		t.Fatalf("output=%q, quero 'echo:olá'", tp.Output)
	}
	// A tool foi de facto executada (uma vez), via RM.
	if got := h.calls.Load(); got != 1 {
		t.Fatalf("tool executada %d vezes, quero 1", got)
	}

	// Estado persistido como eventos, na ordem esperada e incluindo o evento de
	// mediação do RM (tool.call.mediated) ENTRE task.running e task.complete —
	// prova de que o despacho passou pelo gate.
	assertStreamTypes(t, h.es, string(runID), []string{
		contract.EventRunCreated,
		contract.EventTaskReady,
		contract.EventTaskRunning,
		referencemonitor.EventTypeMediated,
		contract.EventTaskComplete,
	})
}

// TestSchedulerOnlyExecutesViaRM prova que o Escalonador só executa tools via
// RM: com um RM que NEGA, o fluxo termina em task.failed SEM efeito (a tool não
// é executada). É a garantia de no-bypass reutilizada do AOS-003.
func TestSchedulerOnlyExecutesViaRM(t *testing.T) {
	t.Parallel()
	h := newHarness(t, denyHook{})
	failedCh := h.collect(t, contract.EventTaskFailed)

	if err := h.sch.Start(h.ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	runID, err := h.orch.Submit(h.ctx, toyGoal())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	ev := waitEvent(t, failedCh)
	var tp contract.TaskPayload
	if err := json.Unmarshal(ev.Payload, &tp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tp.RunID != string(runID) {
		t.Fatalf("task.failed.run_id=%q, quero %q", tp.RunID, runID)
	}
	if tp.State != string(contract.StateFailed) {
		t.Fatalf("state=%q, quero failed", tp.State)
	}
	if tp.Code != referencemonitor.CodeDeniedByHook {
		t.Fatalf("código=%q, quero %q", tp.Code, referencemonitor.CodeDeniedByHook)
	}
	// SEM efeito: a tool NUNCA foi executada porque a negação bloqueou o despacho.
	if got := h.calls.Load(); got != 0 {
		t.Fatalf("tool executada %d vezes numa negação, quero 0 (bypass!)", got)
	}
	// A negação foi auditada pelo RM como tool.call.denied no stream do run.
	assertStreamContains(t, h.es, string(runID), referencemonitor.EventTypeDenied)
	// E NÃO há evento de tool.call.mediated (permit) — nada foi despachado.
	assertStreamNotContains(t, h.es, string(runID), referencemonitor.EventTypeMediated)
}

// TestToolErrorLeadsToFailed: permit, mas a tool falha na execução → task.failed
// com código de erro de tool (a decisão foi permit; a TAREFA falha na mesma).
func TestToolErrorLeadsToFailed(t *testing.T) {
	t.Parallel()
	es, _ := eventstore.New()
	b, _ := bus.New(es)
	rm := referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.DefaultHooks()...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
	)
	boom := errors.New("boom")
	if err := rm.Register(toyTool, func(_ context.Context, _ []byte) ([]byte, error) {
		return nil, boom
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	orch, _ := orchestrator.New(b)
	sch, _ := scheduler.New(b, rm, es)
	t.Cleanup(func() { sch.Stop(); _ = b.Close(); _ = es.Close() })

	ctx := context.Background()
	failedCh := make(chan eventstore.Event, 4)
	sub, err := b.Subscribe(ctx, bus.SubConfig{
		Name:    "coletor:failed",
		Filter:  bus.Filter{Types: []string{contract.EventTaskFailed}},
		Handler: func(d *bus.Delivery) { d.Ack(); failedCh <- d.Event },
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(sub.Unsubscribe)

	if err := sch.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := orch.Submit(ctx, toyGoal()); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	ev := waitEvent(t, failedCh)
	var tp contract.TaskPayload
	if err := json.Unmarshal(ev.Payload, &tp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tp.Code != "E_TOOL_ERROR" {
		t.Fatalf("código=%q, quero E_TOOL_ERROR", tp.Code)
	}
	// Numa permit, a mediação FOI registada (o efeito foi tentado).
	assertStreamContains(t, es, tp.RunID, referencemonitor.EventTypeMediated)
}

// TestInvalidReadyStateLeadsToFailed: um task.ready com estado ilegal (não
// "ready") não pode transitar para running; o Escalonador termina em failed sem
// despachar. Injecta-se o evento cru no barramento (o ORQ nunca o emitiria).
func TestInvalidReadyStateLeadsToFailed(t *testing.T) {
	t.Parallel()
	h := newHarness(t, referencemonitor.DefaultHooks()...)
	failedCh := h.collect(t, contract.EventTaskFailed)

	if err := h.sch.Start(h.ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	const runID = "run-manual"
	bad := contract.TaskPayload{
		RunID:  runID,
		TaskID: contract.MinimalTaskID,
		StepID: contract.StepReady(contract.MinimalTaskID),
		State:  string(contract.StateComplete), // estado ilegal para arrancar
		ToolID: toyTool,
	}
	raw, _ := json.Marshal(bad)
	if _, err := h.bus.Publish(h.ctx, runID, eventstore.EventInput{
		Type:    contract.EventTaskReady,
		Payload: raw,
		RunID:   runID,
		StepID:  bad.StepID,
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	ev := waitEvent(t, failedCh)
	var tp contract.TaskPayload
	if err := json.Unmarshal(ev.Payload, &tp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tp.Code != "E_INVALID_TRANSITION" {
		t.Fatalf("código=%q, quero E_INVALID_TRANSITION", tp.Code)
	}
	if got := h.calls.Load(); got != 0 {
		t.Fatalf("tool executada %d vezes num estado inválido, quero 0", got)
	}
}

// TestContractStability documenta a estabilidade das assinaturas: os tipos
// concretos implementam as portas estáveis e a construção valida os invariantes.
func TestContractStability(t *testing.T) {
	t.Parallel()
	// Conformidade de porta (runtime, além da verificação estática nos pacotes).
	var _ contract.Orchestrator = (*orchestrator.Orchestrator)(nil)
	var _ contract.Scheduler = (*scheduler.Scheduler)(nil)

	es, _ := eventstore.New()
	b, _ := bus.New(es)
	rm := referencemonitor.New()
	t.Cleanup(func() { _ = b.Close(); _ = es.Close() })

	if _, err := scheduler.New(nil, rm, es); err == nil {
		t.Fatal("New(nil bus) devia falhar")
	}
	if _, err := scheduler.New(b, nil, es); err == nil {
		t.Fatal("New(nil rm) devia falhar (execução só via RM)")
	}
	if _, err := scheduler.New(b, rm, nil); err == nil {
		t.Fatal("New(nil reader) devia falhar (guard de idempotência exige leitura)")
	}
	sch, err := scheduler.New(b, rm, es)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := sch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := sch.Start(context.Background()); err == nil {
		t.Fatal("Start duplo devia falhar")
	}
	sch.Stop()
	sch.Stop() // idempotente
}

// TestSchedulerOptions exercita WithSubscriberName/WithProducer/WithPrincipal e
// verifica que a NHI produtora aparece nos eventos de estado emitidos pelo SCH e
// que o Principal injectado chega ao RM (via o Producer do evento de mediação).
func TestSchedulerOptions(t *testing.T) {
	t.Parallel()
	es, _ := eventstore.New()
	b, _ := bus.New(es)
	rm := referencemonitor.New(
		referencemonitor.WithHooks(referencemonitor.DefaultHooks()...),
		referencemonitor.WithEventSink(referencemonitor.NewEventStoreSink(es)),
	)
	if err := rm.Register(toyTool, func(_ context.Context, in []byte) ([]byte, error) { return in, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	orch, _ := orchestrator.New(b)
	const schNHI = "nhi:sch-teste"
	sch, err := scheduler.New(b, rm, es,
		scheduler.WithSubscriberName("sch-teste"),
		scheduler.WithProducer(eventstore.Producer{NHIID: schNHI}),
		scheduler.WithPrincipal(referencemonitor.Principal{NHIID: schNHI, AgentClass: "classe-teste"}),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { sch.Stop(); _ = b.Close(); _ = es.Close() })

	ctx := context.Background()
	completeCh := make(chan eventstore.Event, 4)
	sub, err := b.Subscribe(ctx, bus.SubConfig{
		Name:    "coletor:opt-complete",
		Filter:  bus.Filter{Types: []string{contract.EventTaskComplete}},
		Handler: func(d *bus.Delivery) { d.Ack(); completeCh <- d.Event },
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(sub.Unsubscribe)

	if err := sch.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	runID, err := orch.Submit(ctx, toyGoal())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	ev := waitEvent(t, completeCh)
	// A NHI produtora injectada tem de constar do envelope do evento de estado.
	if ev.Producer.NHIID != schNHI {
		t.Fatalf("produtor do task.complete=%q, quero %q", ev.Producer.NHIID, schNHI)
	}
	// O Principal injectado chega ao RM: o evento de mediação é produzido com a
	// NHI do principal.
	evs, err := es.Read(ctx, string(runID), 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var sawMediated bool
	for _, e := range evs {
		if e.Type == referencemonitor.EventTypeMediated {
			sawMediated = true
			if e.Producer.NHIID != schNHI {
				t.Fatalf("mediação com produtor %q, quero %q (principal não propagou)", e.Producer.NHIID, schNHI)
			}
		}
	}
	if !sawMediated {
		t.Fatal("sem evento de mediação no stream")
	}
}

// TestGarbagePayloadIsSkipped: um task.ready com payload não-JSON é descartado
// (Ack, sem crash, sem despacho) e não impede o processamento do evento válido
// seguinte — o Escalonador sobrevive a entradas corrompidas.
func TestGarbagePayloadIsSkipped(t *testing.T) {
	t.Parallel()
	h := newHarness(t, referencemonitor.DefaultHooks()...)
	completeCh := h.collect(t, contract.EventTaskComplete)

	if err := h.sch.Start(h.ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Evento task.ready com JSON inválido, num stream próprio.
	if _, err := h.bus.Publish(h.ctx, "run-lixo", eventstore.EventInput{
		Type:    contract.EventTaskReady,
		Payload: []byte("isto-não-é-json"),
		RunID:   "run-lixo",
		StepID:  "s1",
	}); err != nil {
		t.Fatalf("Publish lixo: %v", err)
	}
	// Um submit válido a seguir tem de completar (o SCH não ficou preso no lixo).
	runID, err := h.orch.Submit(h.ctx, toyGoal())
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	ev := waitEvent(t, completeCh)
	var tp contract.TaskPayload
	if err := json.Unmarshal(ev.Payload, &tp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tp.RunID != string(runID) {
		t.Fatalf("completou o run errado: %q != %q", tp.RunID, runID)
	}
}

// --- auxiliares ---

type denyHook struct{}

func (denyHook) Name() string { return "test-deny" }

func (denyHook) Evaluate(_ context.Context, _ *referencemonitor.Call) (referencemonitor.HookResult, error) {
	return referencemonitor.HookResult{Decision: referencemonitor.HookDeny, Reason: "negado no teste"}, nil
}

func readTypes(t *testing.T, es *eventstore.Store, stream string) []string {
	t.Helper()
	evs, err := es.Read(context.Background(), stream, 1)
	if err != nil {
		t.Fatalf("Read(%s): %v", stream, err)
	}
	types := make([]string, len(evs))
	for i, e := range evs {
		types[i] = e.Type
	}
	return types
}

func assertStreamTypes(t *testing.T, es *eventstore.Store, stream string, want []string) {
	t.Helper()
	got := readTypes(t, es, stream)
	if len(got) != len(want) {
		t.Fatalf("stream %s tem tipos %v, quero %v", stream, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stream %s evento %d=%s, quero %s (todos: %v)", stream, i, got[i], want[i], got)
		}
	}
}

func assertStreamContains(t *testing.T, es *eventstore.Store, stream, typ string) {
	t.Helper()
	for _, ty := range readTypes(t, es, stream) {
		if ty == typ {
			return
		}
	}
	t.Fatalf("stream %s não contém evento %s", stream, typ)
}

func assertStreamNotContains(t *testing.T, es *eventstore.Store, stream, typ string) {
	t.Helper()
	for _, ty := range readTypes(t, es, stream) {
		if ty == typ {
			t.Fatalf("stream %s contém %s inesperadamente", stream, typ)
		}
	}
}
