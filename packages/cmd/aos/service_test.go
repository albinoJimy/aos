package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// Modelos de referência para o loop de serviço
// ---------------------------------------------------------------------------

// countingModel conclui cada run no 1º turno (sem tool calls) e conta as invocações.
// Prova que N runs foram REALMENTE executados (não-vacuoso).
type countingModel struct{ calls int64 }

func (m *countingModel) Call(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	atomic.AddInt64(&m.calls, 1)
	return agentruntime.ModelResponse{
		Text:  "run concluido",
		Final: true,
		Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

// panicOnMarkerModel entra em PANIC quando o prompt materializado contém o marcador dado
// (semeado no Objective do goal); caso contrário conclui saudável. Permite exercitar o
// ISOLAMENTO por-run com o MESMO runtime partilhado por vários runs.
type panicOnMarkerModel struct {
	marker  string
	healthy int64 // nº de runs saudáveis concluídos
}

func (m *panicOnMarkerModel) Call(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	if bytes.Contains(view.Materialized, []byte(m.marker)) {
		panic("panic deliberado do run (teste de isolamento)")
	}
	atomic.AddInt64(&m.healthy, 1)
	return agentruntime.ModelResponse{
		Text:  "run saudavel concluido",
		Final: true,
		Usage: agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

// blockingModel bloqueia até o ctx ser cancelado (simula um run longo), devolvendo o erro
// do ctx. Exercita o drain/cancelamento do shutdown gracioso.
type blockingModel struct {
	started chan struct{}
	once    sync.Once
}

func (m *blockingModel) Call(ctx context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.once.Do(func() {
		if m.started != nil {
			close(m.started)
		}
	})
	<-ctx.Done()
	return agentruntime.ModelResponse{}, ctx.Err()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// svcClock é um relógio determinístico para o lease manager (o lease nunca é visto como
// expirado durante o teste — TTL >> 0 e relógio fixo).
func svcClock() durable.Clock {
	return durable.ClockFunc(func() time.Time { return time.Unix(1_700_000_000, 0).UTC() })
}

// newTestNode compõe um Node de referência com o modelo dado (identidade real mínima).
func newTestNode(t *testing.T, model agentruntime.ModelClient) *Node {
	t.Helper()
	cfg := Config{
		IssuerID: tnIssuerID,
		Humans:   []string{tnHuman},
		IssuerClasses: map[string]identity.ClassPolicy{
			tnClass: {TTL: 15 * time.Minute, Scope: []string{tnCap}},
		},
		IssuerClock:   tnClock(),
		VerifierClock: tnClock(),
		Model:         model,
	}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	return node
}

// svcGoal constrói um goal mínimo válido (Principal com NHIID — exigido por Run.validate).
// O Objective transporta um marcador opcional (para o panicOnMarkerModel).
func svcGoal(runID, objective string) agentruntime.Goal {
	return agentruntime.Goal{
		RunID:     runID,
		Principal: referencemonitor.Principal{NHIID: "nhi:" + runID},
		Objective: objective,
	}
}

// ---------------------------------------------------------------------------
// (a) Hospeda N runs concorrentes; todos terminam/registam.
// ---------------------------------------------------------------------------

func TestServiceHostsConcurrentRuns(t *testing.T) {
	model := &countingModel{}
	node := newTestNode(t, model)
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	defer func() { _ = node.Close() }()

	const n = 24
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := svc.Submit(ctx, svcGoal(fmt.Sprintf("run-%02d", i), "")); err != nil {
				t.Errorf("Submit run-%02d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	// Espera todos terminarem e verifica o desfecho de cada um.
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for i := 0; i < n; i++ {
		runID := fmt.Sprintf("run-%02d", i)
		oc, ok, werr := svc.Wait(waitCtx, runID)
		if werr != nil {
			t.Fatalf("Wait %q: %v", runID, werr)
		}
		if !ok {
			t.Fatalf("run %q nao registou desfecho", runID)
		}
		if oc.Err != nil || oc.Panicked {
			t.Fatalf("run %q devia ter concluido limpo, veio err=%v panicked=%v", runID, oc.Err, oc.Panicked)
		}
		if !oc.Result.Terminated {
			t.Fatalf("run %q devia estar Terminated", runID)
		}
	}
	if got := atomic.LoadInt64(&model.calls); got != n {
		t.Fatalf("modelo chamado %d vezes; queria %d (nao-vacuoso: cada run executou)", got, n)
	}
	if c := svc.InProgressCount(); c != 0 {
		t.Fatalf("apos dreno InProgressCount=%d; queria 0", c)
	}
}

// ---------------------------------------------------------------------------
// (b) Um run em PANIC não derruba o nó nem afecta um run saudável; o nó continua a aceitar.
// ---------------------------------------------------------------------------

func TestServicePanicIsolation(t *testing.T) {
	const marker = "PANIC-ME-NOW"
	model := &panicOnMarkerModel{marker: marker}
	node := newTestNode(t, model)
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	defer func() { _ = node.Close() }()

	ctx := context.Background()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Submete concorrentemente um run que ENTRA EM PANIC e um SAUDÁVEL.
	if err := svc.Submit(ctx, svcGoal("run-panic", marker)); err != nil {
		t.Fatalf("Submit (panic): %v", err)
	}
	if err := svc.Submit(ctx, svcGoal("run-healthy", "trabalho normal")); err != nil {
		t.Fatalf("Submit (healthy): %v", err)
	}

	// O run saudável termina LIMPO — o panic do outro não o afectou.
	ocH, ok, werr := svc.Wait(waitCtx, "run-healthy")
	if werr != nil || !ok {
		t.Fatalf("Wait (healthy): ok=%v err=%v", ok, werr)
	}
	if ocH.Panicked || ocH.Err != nil || !ocH.Result.Terminated {
		t.Fatalf("run saudavel devia concluir limpo, veio %+v", ocH)
	}

	// O run em panic foi CAPTURADO e marcado falhado (não derrubou o processo).
	ocP, ok, werr := svc.Wait(waitCtx, "run-panic")
	if werr != nil || !ok {
		t.Fatalf("Wait (panic): ok=%v err=%v", ok, werr)
	}
	if !ocP.Panicked || !errors.Is(ocP.Err, ErrRunPanicked) {
		t.Fatalf("run em panic devia ser marcado panicked com ErrRunPanicked, veio %+v", ocP)
	}

	// O NÓ CONTINUA a aceitar novos runs após o panic (não ficou derrubado).
	if err := svc.Submit(ctx, svcGoal("run-after", "depois do panic")); err != nil {
		t.Fatalf("no devia continuar a aceitar apos o panic, Submit falhou: %v", err)
	}
	ocA, ok, werr := svc.Wait(waitCtx, "run-after")
	if werr != nil || !ok {
		t.Fatalf("Wait (after): ok=%v err=%v", ok, werr)
	}
	if ocA.Panicked || ocA.Err != nil || !ocA.Result.Terminated {
		t.Fatalf("run apos o panic devia concluir limpo, veio %+v", ocA)
	}
	if got := atomic.LoadInt64(&model.healthy); got != 2 {
		t.Fatalf("runs saudaveis concluidos=%d; queria 2 (healthy + after)", got)
	}
}

// ---------------------------------------------------------------------------
// (c) Submeter o mesmo RunID 2x => a 2ª é recusada (sem duplicação).
// ---------------------------------------------------------------------------

func TestServiceRejectsDuplicateRunID(t *testing.T) {
	// blockingModel mantém o 1º run VIVO enquanto submetemos o duplicado — garantindo que
	// a 2ª submissão colide com um run REALMENTE em curso (não uma corrida de término).
	model := &blockingModel{started: make(chan struct{})}
	node := newTestNode(t, model)
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	defer func() { _ = node.Close() }()

	ctx := context.Background()
	if err := svc.Submit(ctx, svcGoal("run-dup", "")); err != nil {
		t.Fatalf("1a submissao devia ser aceite: %v", err)
	}
	<-model.started // o 1º run está a correr (bloqueado no ctx)

	// 2ª submissão do MESMO RunID ⇒ recusada com ErrRunAlreadyInProgress.
	if err := svc.Submit(ctx, svcGoal("run-dup", "")); !errors.Is(err, ErrRunAlreadyInProgress) {
		t.Fatalf("2a submissao do mesmo RunID devia dar ErrRunAlreadyInProgress, veio: %v", err)
	}
	if c := svc.InProgressCount(); c != 1 {
		t.Fatalf("InProgressCount=%d; queria 1 (o duplicado nao foi hospedado)", c)
	}

	// Encerra (cancela o run bloqueado limpo).
	shCtx, shCancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer shCancel()
	_ = svc.Shutdown(shCtx)
}

// ---------------------------------------------------------------------------
// (d) Lease detido por OUTRA réplica => o run não é hospedado (sem roubo).
// ---------------------------------------------------------------------------

func TestServiceDoesNotStealLeaseHeldElsewhere(t *testing.T) {
	// Event Store PARTILHADO entre "réplicas" (o substrato do nó). Uma autoridade de lease
	// EXTERNA (simula outra réplica) reclama o lease do run ANTES de o submetermos.
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer func() { _ = es.Close() }()

	const runID = "run-owned-elsewhere"
	clk := svcClock()
	otherReplica, err := durable.NewLeaseManager(es, time.Minute, durable.WithLeaseClock(clk), durable.WithWorkerID("replica-B"))
	if err != nil {
		t.Fatalf("NewLeaseManager (externa): %v", err)
	}
	if _, err := otherReplica.Claim(context.Background(), runID); err != nil {
		t.Fatalf("a outra replica devia reclamar o lease: %v", err)
	}

	// O nó desta réplica partilha o MESMO Event Store.
	model := &countingModel{}
	cfg := Config{
		IssuerID: tnIssuerID,
		Humans:   []string{tnHuman},
		IssuerClasses: map[string]identity.ClassPolicy{
			tnClass: {TTL: 15 * time.Minute, Scope: []string{tnCap}},
		},
		IssuerClock:   tnClock(),
		VerifierClock: tnClock(),
		Model:         model,
		EventStore:    es, // <-- substrato partilhado
	}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	// O nó não é dono do ES (foi fornecido) — não o fecha.
	svc, err := NewNodeService(node, WithLeaseClock(clk), WithLeaseTTL(time.Minute), WithServiceWorkerID("replica-A"))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}

	// Submeter o run cujo lease é detido pela réplica B ⇒ NÃO é hospedado (sem roubo).
	if err := svc.Submit(context.Background(), svcGoal(runID, "")); !errors.Is(err, ErrRunLeaseHeldElsewhere) {
		t.Fatalf("run com lease detido por outra replica devia dar ErrRunLeaseHeldElsewhere, veio: %v", err)
	}
	if c := svc.InProgressCount(); c != 0 {
		t.Fatalf("InProgressCount=%d; queria 0 (o run nao foi hospedado)", c)
	}
	if got := atomic.LoadInt64(&model.calls); got != 0 {
		t.Fatalf("o modelo NAO devia ter sido chamado (run nao hospedado), veio %d", got)
	}
}

// ---------------------------------------------------------------------------
// (e) Shutdown gracioso drena os em curso e liberta os leases; após Shutdown, Submit é recusado.
// ---------------------------------------------------------------------------

func TestServiceGracefulShutdownDrainsAndRejects(t *testing.T) {
	// Runs curtos que concluem sozinhos: o shutdown deve drená-los SEM cancelar (retorna nil).
	model := &countingModel{}
	node := newTestNode(t, model)
	logs := &bytes.Buffer{}
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute), WithServiceLog(logs))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	defer func() { _ = node.Close() }()

	ctx := context.Background()
	const n = 8
	for i := 0; i < n; i++ {
		if err := svc.Submit(ctx, svcGoal(fmt.Sprintf("run-%d", i), "")); err != nil {
			t.Fatalf("Submit run-%d: %v", i, err)
		}
	}

	// Shutdown com deadline generoso: runs curtos drenam a tempo ⇒ nil (dreno gracioso,
	// nunca cancelamento).
	shCtx, shCancel := context.WithTimeout(ctx, 5*time.Second)
	defer shCancel()
	if err := svc.Shutdown(shCtx); err != nil {
		t.Fatalf("shutdown gracioso de runs curtos devia drenar sem erro, veio: %v", err)
	}
	if c := svc.InProgressCount(); c != 0 {
		t.Fatalf("apos shutdown InProgressCount=%d; queria 0 (drenado)", c)
	}
	if got := atomic.LoadInt64(&model.calls); got != n {
		t.Fatalf("modelo chamado %d vezes; queria %d (todos os runs drenaram executando)", got, n)
	}

	// Após Shutdown, Submit é RECUSADO fail-closed.
	if err := svc.Submit(ctx, svcGoal("run-late", "")); !errors.Is(err, ErrServiceShuttingDown) {
		t.Fatalf("Submit apos Shutdown devia dar ErrServiceShuttingDown, veio: %v", err)
	}
	// Shutdown é idempotente.
	if err := svc.Shutdown(shCtx); err != nil {
		t.Fatalf("segundo Shutdown devia ser no-op, veio: %v", err)
	}
	if !strings.Contains(logs.String(), "shutdown gracioso iniciado") {
		t.Fatalf("log de shutdown em falta: %q", logs.String())
	}
}

// TestServiceShutdownCancelsBlockedRuns prova que o shutdown, quando o dreno não conclui a
// tempo, CANCELA os runs em curso de forma LIMPA (cooperativa) — nunca kill cego — e ainda
// assim retorna (o run desenrola e liberta o lease). O blockingModel só termina quando o
// ctx do run é cancelado pelo Shutdown.
func TestServiceShutdownCancelsBlockedRuns(t *testing.T) {
	model := &blockingModel{started: make(chan struct{})}
	node := newTestNode(t, model)
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	defer func() { _ = node.Close() }()

	ctx := context.Background()
	if err := svc.Submit(ctx, svcGoal("run-block", "")); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-model.started // o run está bloqueado (não termina sozinho)

	// Deadline curto ⇒ o dreno não conclui ⇒ Shutdown cancela limpo e devolve o erro do ctx.
	shCtx, shCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer shCancel()
	err = svc.Shutdown(shCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown com run bloqueado devia devolver DeadlineExceeded (cancelou limpo), veio: %v", err)
	}
	// Mesmo tendo devolvido o erro do ctx, o run desenrolou e saiu do registo (lease liberto).
	if c := svc.InProgressCount(); c != 0 {
		t.Fatalf("apos shutdown InProgressCount=%d; queria 0 (run cancelado desenrolou)", c)
	}
	oc, ok := svc.Outcome("run-block")
	if !ok {
		t.Fatal("run cancelado devia registar desfecho")
	}
	if !errors.Is(oc.Err, context.Canceled) {
		t.Fatalf("run cancelado devia registar erro de cancelamento, veio: %v", oc.Err)
	}
}

// ---------------------------------------------------------------------------
// (f) Heartbeat de posse: um run longo cuja posse EXPIRA (relógio avança para lá do TTL,
//     apesar da renovação) é CANCELADO cooperativamente — não fica órfão a correr sobre um
//     lease que outra réplica pode reclamar (fecha a janela de dupla-execução).
// ---------------------------------------------------------------------------

// mutableClock é um relógio injectável que se pode AVANÇAR em teste (determinismo sem
// depender do wall-clock para a expiração do lease).
type mutableClock struct{ nanos atomic.Int64 }

func (c *mutableClock) Now() time.Time  { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *mutableClock) set(t time.Time) { c.nanos.Store(t.UnixNano()) }

func TestServiceHeartbeatCancelsRunOnLostLease(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	clk := &mutableClock{}
	clk.set(base)

	model := &blockingModel{started: make(chan struct{})}
	node := newTestNode(t, model)
	// TTL de 1min; heartbeat de 5ms (real) para o loop reagir depressa ao avanço do relógio.
	svc, err := NewNodeService(node,
		WithLeaseClock(clk),
		WithLeaseTTL(time.Minute),
		WithLeaseHeartbeat(5*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	defer func() { _ = node.Close() }()

	ctx := context.Background()
	if err := svc.Submit(ctx, svcGoal("run-longa", "")); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-model.started // o run está vivo (bloqueado no ctx)

	// Avança o relógio para LÁ da expiração do lease: o próximo heartbeat vê ErrLeaseExpired
	// e CANCELA o run (perdeu a posse — sem 2ª execução concorrente possível).
	clk.set(base.Add(2 * time.Minute))

	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	oc, ok, werr := svc.Wait(waitCtx, "run-longa")
	if werr != nil {
		t.Fatalf("Wait: %v (o heartbeat devia ter cancelado o run ao perder a posse)", werr)
	}
	if !ok {
		t.Fatal("run devia registar desfecho apos cancelamento por perda de posse")
	}
	if !errors.Is(oc.Err, context.Canceled) {
		t.Fatalf("run devia ter sido cancelado (context.Canceled) ao perder a posse do lease, veio: %v", oc.Err)
	}
	if c := svc.InProgressCount(); c != 0 {
		t.Fatalf("InProgressCount=%d; queria 0 (run cancelado desenrolou e libertou o lease)", c)
	}

	// Shutdown idempotente (nada em curso).
	_ = svc.Shutdown(waitCtx)
}

// ---------------------------------------------------------------------------
// (g) Re-submeter um RunID JÁ TERMINADO (desfecho retido) é recusado explicitamente
//     (ErrRunAlreadyCompleted) — não re-executa nem sobrescreve o desfecho, nem devolve o
//     enganador ErrRunLeaseHeldElsewhere (o lease residual é DESTA réplica).
// ---------------------------------------------------------------------------

func TestServiceRejectsResubmitCompletedRunID(t *testing.T) {
	model := &countingModel{}
	node := newTestNode(t, model)
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	defer func() { _ = node.Close() }()

	ctx := context.Background()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := svc.Submit(ctx, svcGoal("run-once", "")); err != nil {
		t.Fatalf("1a submissao: %v", err)
	}
	if _, ok, werr := svc.Wait(waitCtx, "run-once"); werr != nil || !ok {
		t.Fatalf("Wait: ok=%v err=%v", ok, werr)
	}

	// Re-submissão do MESMO RunID já terminado ⇒ ErrRunAlreadyCompleted (não re-executa).
	if err := svc.Submit(ctx, svcGoal("run-once", "")); !errors.Is(err, ErrRunAlreadyCompleted) {
		t.Fatalf("re-submissao de run terminado devia dar ErrRunAlreadyCompleted, veio: %v", err)
	}
	if got := atomic.LoadInt64(&model.calls); got != 1 {
		t.Fatalf("modelo chamado %d vezes; queria 1 (a re-submissao NAO re-executou)", got)
	}
}

// ---------------------------------------------------------------------------
// (h) Retenção limitada dos desfechos: o registo de terminados é PODADO (FIFO) ao exceder o
//     teto — sem fuga de memória monotónica num loop de serviço long-running.
// ---------------------------------------------------------------------------

func TestServiceCompletedRetentionPrunesOldest(t *testing.T) {
	model := &countingModel{}
	node := newTestNode(t, model)
	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute), WithCompletedRetention(3))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	defer func() { _ = node.Close() }()

	ctx := context.Background()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	const n = 5
	for i := 0; i < n; i++ {
		runID := fmt.Sprintf("run-%d", i)
		if err := svc.Submit(ctx, svcGoal(runID, "")); err != nil {
			t.Fatalf("Submit %q: %v", runID, err)
		}
		if _, ok, werr := svc.Wait(waitCtx, runID); werr != nil || !ok {
			t.Fatalf("Wait %q: ok=%v err=%v", runID, ok, werr)
		}
	}

	// Os 2 mais antigos foram podados; só os 3 mais recentes permanecem retidos.
	for i := 0; i < 2; i++ {
		runID := fmt.Sprintf("run-%d", i)
		if _, ok := svc.Outcome(runID); ok {
			t.Fatalf("desfecho de %q devia ter sido podado (retencao=3)", runID)
		}
	}
	for i := 2; i < n; i++ {
		runID := fmt.Sprintf("run-%d", i)
		if _, ok := svc.Outcome(runID); !ok {
			t.Fatalf("desfecho de %q devia estar retido (dos 3 mais recentes)", runID)
		}
	}
	if c := svc.InProgressCount(); c != 0 {
		t.Fatalf("InProgressCount=%d; queria 0", c)
	}
}
