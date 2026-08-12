package main

// AOS-252 — ESTADOS TERMINAIS DURÁVEIS + caller de CheckDeadlines (achado F4, alta).
//
// Antes deste ticket: (a) o desfecho de um run vivia só no mapa em memória do serviço —
// um run acabado por erro/panic/MaxTurns era, no log durável, indistinguível de um crash;
// (b) [state.Machine.CheckDeadlines] tinha ZERO chamadores de produção — o backstop
// running→timed_out era letra morta; (c) o GET /runs/{id} lia apenas os baldes em memória,
// pelo que após um restart todo o run era 404.
//
// Estes testes trancam os três critérios de aceitação, todos AO NÍVEL DO NÓ (Bootstrap
// real, cadeia real) e não-vacuosos (asserem o CONTEÚDO do log durável, não só o desfecho
// em memória):
//
//   - [TestAOS252_TerminalStatesSealedInLog] — CA1: sucesso ⇒ complete; MaxTurns ⇒ timed_out
//     (tecto defensivo, razão própria — NÃO failed, que é a falha recuperável da saga);
//     panic recuperado ⇒ failed (razão própria). E a metade negativa: um crash simulado
//     (claim sem desfecho) NÃO tem evento terminal — crash e fim normal distinguem-se no log.
//   - [TestAOS252_DeadlineSweepMaterializesTimedOut] — CA2: um run preso A MEIO de um turno
//     (onde o breaker não chega) é morto pelo varrimento de deadlines: running→timed_out
//     com a razão wall_clock_exceeded (e NÃO a do breaker — são condutores distintos).
//   - [TestAOS252_GetReflectsDurableOutcomeAfterRestart] — CA3: após restart sobre o MESMO
//     substrato, GET /runs/{id} reflecte o desfecho durável (completed) sem nada em memória;
//     o crash simulado continua 404 (órfão sem desfecho — a retoma é AOS-253).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// pinBreakerEnv fixa TODA a superfície AOS_BREAKER_* — sem isto, uma env herdada da máquina
// de quem corre os testes mudaria a composição do breaker/deadline sob teste.
func pinBreakerEnv(t *testing.T, stale, wall, cost, tokens string) {
	t.Helper()
	t.Setenv("AOS_BREAKER_MAX_STALE_ITERATIONS", stale)
	t.Setenv("AOS_BREAKER_MAX_WALL_CLOCK", wall)
	t.Setenv("AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC", cost)
	t.Setenv("AOS_BREAKER_MAX_TOKENS_PER_SEC", tokens)
}

// trRecord é a projecção do payload de um evento run.state.transition (ver
// state.transitionRecord — as chaves JSON são o contrato do log).
type trRecord struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// readTransitions lê do log as transições de estado do run, por ordem. Um stream
// inexistente devolve lista vazia (nunca erro — é o caso "run desconhecido").
func readTransitions(t *testing.T, store state.EventStore, runID string) []trRecord {
	t.Helper()
	events, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil
		}
		t.Fatalf("Read do stream do run %q: %v", runID, err)
	}
	var out []trRecord
	for i := range events {
		if events[i].Type != state.EventTypeTransition {
			continue
		}
		var rec trRecord
		if err := json.Unmarshal(events[i].Payload, &rec); err != nil {
			t.Fatalf("payload de transição ilegível no run %q: %v", runID, err)
		}
		out = append(out, rec)
	}
	return out
}

// assertLastTransition exige que a ÚLTIMA transição durável do run seja from→to com a
// razão dada — é a asserção de desfecho selado (não-vacuosa: lê o log, não a memória).
func assertLastTransition(t *testing.T, store state.EventStore, runID, from, to, reason string) {
	t.Helper()
	trs := readTransitions(t, store, runID)
	if len(trs) == 0 {
		t.Fatalf("run %q sem NENHUMA transição no log — esperava ...→%s (%s)", runID, to, reason)
	}
	last := trs[len(trs)-1]
	if last.From != from || last.To != to || last.Reason != reason {
		t.Fatalf("última transição do run %q = %s→%s (%s), esperava %s→%s (%s). Cadeia: %+v",
			runID, last.From, last.To, last.Reason, from, to, reason, trs)
	}
}

// finalModel252 conclui no primeiro turno (sem tool calls).
type finalModel252 struct{}

func (finalModel252) Call(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{Text: "feito", Final: true}, nil
}

// panicModel252 panicou na chamada ao modelo — o isolamento por-run recupera e o selo tem
// de gravar failed com a razão de panic.
type panicModel252 struct{}

func (panicModel252) Call(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	panic("boom determinista (AOS-252)")
}

// submitSimple252 submete um run sem tool calls planeadas e espera o desfecho.
func submitSimple252(t *testing.T, svc *NodeService, runID string, maxTurns int) RunOutcome {
	t.Helper()
	if err := svc.Submit(context.Background(), agentruntime.Goal{
		RunID:     runID,
		Principal: referencemonitor.Principal{NHIID: "nhi:aos252"},
		Objective: "trabalho de referencia",
		MaxTurns:  maxTurns,
	}); err != nil {
		t.Fatalf("Submit(%s): %v", runID, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	oc, ok, err := svc.Wait(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("Wait(%s): ok=%v err=%v", runID, ok, err)
	}
	return oc
}

// TestAOS252_TerminalStatesSealedInLog (CA1) — cada caminho de saída do loop sela o seu
// estado terminal no log durável, com a razão que atribui a causa.
func TestAOS252_TerminalStatesSealedInLog(t *testing.T) {
	// Breaker totalmente DESLIGADO: este teste isola o selo de AOS-252 (com o breaker
	// ligado, o deny-loop dispararia o trip de AOS-251 antes de MaxTurns — coberto lá).
	pinBreakerEnv(t, "0", "0", "0", "0")

	newNode := func(t *testing.T, model agentruntime.ModelClient) (*Node, *NodeService) {
		t.Helper()
		cfg := tnBaseConfig()
		cfg.Model = model
		node, err := Bootstrap(context.Background(), cfg, io.Discard)
		if err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		t.Cleanup(func() { _ = node.Close() })
		svc, err := NewNodeService(node, WithDeadlineSweepInterval(0))
		if err != nil {
			t.Fatalf("NewNodeService: %v", err)
		}
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = svc.Shutdown(ctx)
		})
		return node, svc
	}

	t.Run("sucesso sela complete", func(t *testing.T) {
		node, svc := newNode(t, finalModel252{})
		oc := submitSimple252(t, svc, "run-252-ok", 8)
		if oc.Err != nil || !oc.Result.Terminated {
			t.Fatalf("desfecho em memória inesperado: %+v err=%v", oc.Result, oc.Err)
		}
		assertLastTransition(t, node.EventStore, "run-252-ok", "running", "complete", reasonRunComplete)
	})

	// O ESGOTAMENTO DO ORÇAMENTO DE TURNOS É `timed_out`, NÃO `failed`.
	//
	// A distinção não é de rótulo, e esta asserção existe para a impedir de regredir. Na
	// tabela declarativa de AOS-017, `failed` é a falha RECUPERÁVEL cuja ÚNICA aresta de
	// saída é failed→compensating — a saga de rollback (AOS-254). Um run que apenas ficou
	// sem turnos não precisa de compensação: precisa de mais orçamento. Selá-lo como
	// `failed` arma a recuperação errada no momento em que a saga for ligada.
	//
	// `timed_out` é o estado dos TECTOS DEFENSIVOS excedidos, absorvente por construção, e
	// é para onde o disjuntor já manda o seu próprio tecto de wall-clock — mandar MaxTurns
	// para `failed` deixaria dois tectos irmãos em estados duráveis diferentes. O que
	// distingue as causas é o RÓTULO ([reasonMaxTurnsExhausted] vs o wall_clock do varredor
	// de deadlines), não o estado.
	t.Run("MaxTurns sela timed_out e nao failed", func(t *testing.T) {
		node, svc := newNode(t, &bktDenyLoopModel{})
		oc := submitSimple252(t, svc, "run-252-maxturns", 4)
		if !errors.Is(oc.Err, agentruntime.ErrMaxTurnsExceeded) {
			t.Fatalf("com o breaker desligado o deny-loop esgota MaxTurns; err=%v", oc.Err)
		}
		assertLastTransition(t, node.EventStore, "run-252-maxturns", "running", "timed_out", reasonMaxTurnsExhausted)
	})

	t.Run("panic recuperado sela failed com razao propria", func(t *testing.T) {
		node, svc := newNode(t, panicModel252{})
		oc := submitSimple252(t, svc, "run-252-panic", 8)
		if !oc.Panicked {
			t.Fatalf("o desfecho devia marcar o panic recuperado: %+v", oc)
		}
		assertLastTransition(t, node.EventStore, "run-252-panic", "running", "failed", reasonRunPanicked)
	})

	t.Run("crash simulado distingue-se do fim normal", func(t *testing.T) {
		node, svc := newNode(t, finalModel252{})
		submitSimple252(t, svc, "run-252-ok", 8)
		// CRASH SIMULADO: um worker que reclamou o run (ready→running) e MORREU a meio —
		// o log fica sem desfecho. É exactamente o que um run pré-AOS-252 deixava para
		// QUALQUER fim (a ambiguidade de F4).
		m, err := state.NewMachine(node.EventStore, "run-252-crash")
		if err != nil {
			t.Fatalf("NewMachine: %v", err)
		}
		if err := m.Transition(context.Background(), state.Running,
			state.TransitionEvent{Token: state.Uint64Token(1), Reason: "crash_simulado"}); err != nil {
			t.Fatalf("claim do crash simulado: %v", err)
		}
		// O fim normal TEM evento terminal; o crash NÃO — a distinção que F4 apagava.
		okTrs := readTransitions(t, node.EventStore, "run-252-ok")
		if last := okTrs[len(okTrs)-1]; last.To != string(state.Complete) {
			t.Fatalf("fim normal devia terminar em complete, veio %+v", last)
		}
		for _, rec := range readTransitions(t, node.EventStore, "run-252-crash") {
			if state.IsTerminal(state.State(rec.To)) || rec.To == string(state.Failed) {
				t.Fatalf("um crash NÃO pode ter desfecho terminal no log; veio %+v", rec)
			}
		}
	})
}

// blockingModel252 fica PRESO a meio do turno 1 (nunca chega à fronteira de fim-de-turno,
// onde o breaker avalia) até o teste o libertar OU o contexto do run ser cancelado — a
// segunda via é a que o varrimento de deadlines acciona (F-A5: o kill cancela o run).
type blockingModel252 struct {
	started chan struct{}
	release chan struct{}
}

func (m *blockingModel252) Call(ctx context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	close(m.started)
	select {
	case <-m.release:
		return agentruntime.ModelResponse{Text: "libertado", Final: true}, nil
	case <-ctx.Done():
		return agentruntime.ModelResponse{}, ctx.Err()
	}
}

// TestAOS252_DeadlineSweepMaterializesTimedOut (CA2) — o varrimento de deadlines corre
// CheckDeadlines sobre os runs em curso e materializa running→timed_out num run preso a
// meio de um turno (o buraco que o breaker, só chamado na fronteira de fim-de-turno, não
// cobre), com a razão wall_clock_exceeded — a atribuição que o distingue do kill do
// breaker (breaker_wall_clock_exceeded). E o selo terminal NÃO reescreve o timed_out
// quando o run desenrola a seguir.
//
// NOTA de determinismo: o relógio da máquina é WALL-CLOCK sem componente monotónica (ver
// [state.Machine.EnteredAt]) e o relógio do sistema pode ter granularidade grosseira
// (~15ms no Windows): um sweep IMEDIATO ao claim pode cair dentro do mesmo tick e ainda
// não ver o deadline excedido. Por isso o teste CONDUZ o varrimento até o kill aterrar
// (com prazo), em vez de assumir que um único sweep basta.
func TestAOS252_DeadlineSweepMaterializesTimedOut(t *testing.T) {
	pinBreakerEnv(t, "0", "1ms", "0", "0")

	model := &blockingModel252{started: make(chan struct{}), release: make(chan struct{})}
	cfg := tnBaseConfig()
	cfg.Model = model
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	svc, err := NewNodeService(node) // varrimento periódico com o DEFAULT — composto de verdade
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	// Shutdown com DEADLINE (nunca Background): se uma asserção falhar antes do run sair,
	// um drain sem prazo penduraria o teste até ao timeout do `go test`, escondendo a
	// falha real (molde de aos252_deadline_interrupt_test.go).
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Shutdown(ctx)
	})
	if svc.deadlineSweepInterval <= 0 {
		t.Fatal("o varrimento de deadlines devia estar composto com o período default")
	}

	const runID = "run-252-stuck"
	if err := svc.Submit(context.Background(), agentruntime.Goal{
		RunID: runID, Principal: referencemonitor.Principal{NHIID: "nhi:aos252"}, Objective: "preso a meio do turno",
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	select {
	case <-model.started:
	case <-time.After(15 * time.Second):
		t.Fatal("o modelo nunca foi chamado — o run não arrancou")
	}
	// O run está EM CURSO, preso a meio do turno 1, com a máquina em `running` (claim de
	// AOS-251) e o deadline (1ms) a expirar num tick de relógio. Conduz o varrimento até o
	// kill aterrar (ver a nota de determinismo no cabeçalho).
	killDeadline := time.Now().Add(15 * time.Second)
	for {
		svc.SweepDeadlinesNow(context.Background())
		if trs := readTransitions(t, node.EventStore, runID); len(trs) > 1 {
			break
		}
		if time.Now().After(killDeadline) {
			t.Fatal("o varrimento nunca materializou o timed_out — CheckDeadlines sem efeito (CA2)")
		}
		time.Sleep(10 * time.Millisecond)
	}
	assertLastTransition(t, node.EventStore, runID, "running", "timed_out", state.ReasonWallClockTimeout)

	// O kill CANCELA o run (F-A5): o modelo sai pelo ctx e o run desenrola. O selo terminal
	// NÃO reescreve o timed_out — o kill fail-closed é o facto durável final.
	close(model.release)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, _, err := svc.Wait(ctx, runID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	assertLastTransition(t, node.EventStore, runID, "running", "timed_out", state.ReasonWallClockTimeout)
}

// TestAOS252_GetReflectsDurableOutcomeAfterRestart (CA3) — com o substrato em ficheiro,
// um SEGUNDO nó (memória vazia) sobre o MESMO Event Store/WORM reflecte no GET /runs/{id}
// o desfecho durável; o run "crashado" (claim sem desfecho) continua 404.
func TestAOS252_GetReflectsDurableOutcomeAfterRestart(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	ctx := context.Background()
	dir := t.TempDir()

	cfg := tnBaseConfig()
	cfg.Model = finalModel252{}
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")

	node1, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap node1: %v", err)
	}
	svc1, err := NewNodeService(node1, WithDeadlineSweepInterval(0))
	if err != nil {
		t.Fatalf("NewNodeService node1: %v", err)
	}
	h1, err := NewAPIHandler(svc1, node1)
	if err != nil {
		t.Fatalf("NewAPIHandler node1: %v", err)
	}

	const okRun = "run-252-ok"
	submitSimple252(t, svc1, okRun, 8)

	// Crash simulado no mesmo substrato: claim sem desfecho.
	m, err := state.NewMachine(node1.EventStore, "run-252-crash")
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if err := m.Transition(ctx, state.Running,
		state.TransitionEvent{Token: state.Uint64Token(1), Reason: "crash_simulado"}); err != nil {
		t.Fatalf("claim do crash simulado: %v", err)
	}

	// Antes do restart, o GET vem do balde em memória (controlo do molde).
	if rec := postJSON(h1, "GET", "/runs/"+okRun, nil); rec.Code != http.StatusOK {
		t.Fatalf("GET pré-restart devia dar 200, veio %d", rec.Code)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := svc1.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown node1: %v", err)
	}
	if err := node1.Close(); err != nil {
		t.Fatalf("Close node1: %v", err)
	}

	// RESTART: nó NOVO sobre o mesmo substrato — baldes em memória vazios.
	node2, err := Bootstrap(ctx, cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap node2: %v", err)
	}
	t.Cleanup(func() { _ = node2.Close() })
	svc2, err := NewNodeService(node2, WithDeadlineSweepInterval(0))
	if err != nil {
		t.Fatalf("NewNodeService node2: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc2.Shutdown(ctx)
	})
	h2, err := NewAPIHandler(svc2, node2)
	if err != nil {
		t.Fatalf("NewAPIHandler node2: %v", err)
	}

	rec := postJSON(h2, "GET", "/runs/"+okRun, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("após restart o GET do run terminado devia reflectir o desfecho DURÁVEL, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var st runStateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("GET não descodifica: %v", err)
	}
	if st.Status != "completed" || !st.Terminated {
		t.Fatalf("o desfecho durável devia ser completed/terminated, veio %+v", st)
	}

	// O crash simulado NÃO tem desfecho: o GET continua 404 (órfão — a retoma é AOS-253),
	// e o log distingue os dois: complete vs running.
	if rec := postJSON(h2, "GET", "/runs/run-252-crash", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("um run crashado (sem desfecho durável) devia continuar 404, veio %d", rec.Code)
	}
	assertLastTransition(t, node2.EventStore, okRun, "running", "complete", reasonRunComplete)
	assertLastTransition(t, node2.EventStore, "run-252-crash", "ready", "running", "crash_simulado")
}
