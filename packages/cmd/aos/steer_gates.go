package main

import (
	"context"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// reasonSteerRunClaim é o motivo gravado na aresta ready→running reclamada pelo
// lazy-claim de AOS-218 (ver [runGate.Pause]) — rótulo de auditoria legível, não segredo.
const reasonSteerRunClaim = "steer_run_claim"

// reasonRunStartClaim é o motivo gravado na aresta ready→running reclamada NO ARRANQUE da
// execução (AOS-251, ver [runGate.claimRunning]) — distinto do lazy-claim de steer para a
// auditoria atribuir a causa certa a cada claim.
const reasonRunStartClaim = "run_start_claim"

// runStateGates é a COSTURA MÍNIMA (AOS-218) que dá ao canal de steer (AOS-023) uma
// FONTE de [control.StateGate] durável POR-RUN — sem inventar um mecanismo de estado
// novo. Reusa a máquina de estados de AOS-017 ([state.Machine]) sobre o MESMO Event Store
// do nó: a pausa graciosa materializa a aresta declarativa running→paused e a retoma a
// paused→running, gravadas como eventos append-only (reconstruíveis por replay, sobrevivem
// a crash). O loop de serviço ABRE um gate por run hospedado ([Open]) e LIBERTA-o no fim
// ([Close]); o loop base resolve-o por [Resolve] — o gates func de [control.NewLoopSteer].
//
// CLAIM NO ARRANQUE (AOS-251): o loop de serviço reclama a aresta ready→running — com o
// fencing token do LEASE já detido (AOS-018) — quando o run COMEÇA a executar
// ([runGate.claimRunning], chamado em hostRun). É o que torna o disjuntor do agente vivo
// eficaz no run comum: [breaker.Breaker.Observe] é no-op fora de `running`, e sem o claim
// um run sem steer nem escalada ficava em `ready` do princípio ao fim, com o disjuntor
// cego. O LAZY-CLAIM de AOS-218 (reclamar só no primeiro pause/escalada) MANTÉM-SE como
// fallback do caminho directo [Runtime.Run] sem serviço (testes, embedders): aí um run sem
// steer continua a não gerar NENHUMA transição e o seu stream fica byte-idêntico ao de
// antes de AOS-218. No caminho do serviço o claim no arranque torna-se a via primária e o
// lazy-claim é no-op (a máquina já está em `running`).
//
// Seguro para uso concorrente: o mutex serializa o mapa; cada [state.Machine] tem o seu
// próprio mutex interno.
type runStateGates struct {
	store  state.EventStore
	tracer agentruntime.Tracer
	// runWallClock é o tecto absoluto de tempo em `running` com que cada máquina é aberta
	// (AOS-252): é ele que permite ao varrimento de deadlines ([sweepDeadlines])
	// materializar running→timed_out num run preso A MEIO de um turno — o ponto que o
	// breaker (que avalia o MESMO tecto na fronteira de fim-de-turno) não alcança. É o
	// valor de AOS_BREAKER_MAX_WALL_CLOCK: UM só conceito operador, dois pontos de
	// enforcement. 0 ⇒ deadline desligado (CheckDeadlines é no-op para running).
	runWallClock time.Duration

	mu    sync.Mutex
	gates map[string]*runGate
}

// newRunStateGates constrói o registo sobre o Event Store do nó. tracer é reusado da
// observabilidade do nó (um span por transição confirmada); nil ⇒ [agentruntime.NoopTracer].
// runWallClock (AOS-252) é o tecto de tempo em `running` das máquinas abertas; <= 0 desliga
// o deadline (comportamento de antes de AOS-252).
//
// runWallClock é EXPLÍCITO (não variádico) de propósito: um parâmetro omissível tornaria o
// deadline desligável POR ESQUECIMENTO, em silêncio, num construtor onde o custo de o dizer
// são quatro call sites. Quem não quer deadline escreve 0 e a decisão fica no código.
func newRunStateGates(store state.EventStore, tracer agentruntime.Tracer, runWallClock time.Duration) *runStateGates {
	if tracer == nil {
		tracer = agentruntime.NoopTracer{}
	}
	return &runStateGates{store: store, tracer: tracer, runWallClock: runWallClock, gates: make(map[string]*runGate)}
}

// Open compõe (e RECONSTRÓI do log — para a retoma após crash) a máquina de estados do
// run e regista o seu gate. token é o fencing token do lease de posse: o MESMO que
// AOS-017 exige no claim ready→running, propagado sem o duplicar. NÃO transita nada aqui
// (o claim é [runGate.claimRunning]/lazy-claim, ver [runStateGates]). Fail-closed: um log
// de estado corrompido aborta — mas um run novo reconstrói para [state.Ready] e nunca falha.
func (g *runStateGates) Open(ctx context.Context, runID string, token state.FencingToken) error {
	var opts []state.Option
	if g.tracer != nil {
		opts = append(opts, state.WithTracer(g.tracer))
	}
	if g.runWallClock > 0 {
		opts = append(opts, state.WithRunWallClock(g.runWallClock))
	}
	m, err := state.NewMachine(g.store, runID, opts...)
	if err != nil {
		return err
	}
	if _, err := m.Rebuild(ctx); err != nil {
		return err
	}
	g.mu.Lock()
	g.gates[runID] = &runGate{m: m, token: token}
	g.mu.Unlock()
	return nil
}

// currentState reconstrói do log o estado DURÁVEL de um run sem abrir gate, sem registar
// nada e sem transitar nada — é uma LEITURA. Um run sem transições (ou sem stream) é
// [state.Ready], o estado inicial.
//
// É o que permite ao serviço saber que um run ficou À ESPERA DE HUMANO mesmo depois de um
// restart: a contabilidade em memória do nó é um cache, a máquina de estados é a verdade.
func (g *runStateGates) currentState(ctx context.Context, runID string) (state.State, error) {
	m, err := state.NewMachine(g.store, runID)
	if err != nil {
		return "", err
	}
	return m.Rebuild(ctx)
}

// Resolve devolve o [control.StateGate] do run — o gates func de [control.NewLoopSteer].
// Um run não aberto devolve nil: [control.LoopSteer.GracefulPause] trata nil como "sem
// gate" (a pausa desse run é um no-op fail-safe, o loop continua), nunca um panic.
func (g *runStateGates) Resolve(runID string) control.StateGate {
	g.mu.Lock()
	defer g.mu.Unlock()
	if gate, ok := g.gates[runID]; ok {
		return gate
	}
	return nil
}

// Close remove o gate do run (chamado quando o run sai do registo de em-curso). Um gate
// desconhecido é ignorado (idempotente).
func (g *runStateGates) Close(runID string) {
	g.mu.Lock()
	delete(g.gates, runID)
	g.mu.Unlock()
}

// runGate adapta a [state.Machine] de UM run ao [control.StateGate], com o claim de posse:
// NO ARRANQUE da execução ([claimRunning], AOS-251 — a via do serviço) ou, no caminho
// directo sem serviço, LAZY (AOS-218) — reclamando ready→running com o fencing token do
// lease apenas quando o PRIMEIRO pause/escalada é de facto pedido, para que esse caminho
// não gere transições num run sem steer.
type runGate struct {
	m     *state.Machine
	token state.FencingToken
}

// claimRunning materializa o claim ready→running no INÍCIO da execução do run (AOS-251).
// Usa o fencing token do LEASE já detido — a mesma autoridade do lazy-claim de AOS-218,
// exercida mais cedo. Sem ele, um run comum (sem steer nem escalada) ficava em `ready` do
// princípio ao fim e o disjuntor do agente vivo — no-op fora de `running` — nunca acumulava
// nem disparava, inclusive no deny-loop.
//
// IDEMPOTENTE por construção: só transita se a máquina reconstruída estiver em
// [state.Ready]. Numa retoma (rebuild devolve `running`, `waiting_on_human`, ...) é no-op —
// a retoma sob o lease já detido não re-reclama (AOS-017/018) e waiting_on_human é reposto
// por [runGate.resumeIfWaiting] logo a seguir. FAIL-CLOSED: uma falha da transição durável
// aborta o arranque do run — correr sem o claim é correr com o disjuntor cego.
func (g *runGate) claimRunning(ctx context.Context) error {
	if g.m.Current() != state.Ready {
		return nil
	}
	return g.m.Transition(ctx, state.Running, state.TransitionEvent{Token: g.token, Reason: reasonRunStartClaim})
}

// Pause materializa a pausa graciosa (running→paused). Se a máquina ainda está em
// [state.Ready] (nenhum pause anterior), reclama primeiro ready→running com o fencing
// token do lease — a aresta que AOS-017 exige antes de qualquer suspensão. Se já está em
// [state.Paused], a tabela declarativa recusa paused→paused ([state.ErrInvalidTransition])
// — devolvido ao canal, que não re-pausa um run já pausado.
func (g *runGate) Pause(ctx context.Context, reason string) error {
	if g.m.Current() == state.Ready {
		if err := g.m.Transition(ctx, state.Running, state.TransitionEvent{Token: g.token, Reason: reasonSteerRunClaim}); err != nil {
			return err
		}
	}
	return g.m.Pause(ctx, state.TransitionEvent{Reason: reason})
}

// Resume materializa paused→running (retoma sob o lease já detido — a retoma de suspensão
// NÃO re-exige fencing token, AOS-017/018).
func (g *runGate) Resume(ctx context.Context, reason string) error {
	return g.m.Resume(ctx, state.TransitionEvent{Reason: reason})
}

// EscalateToHuman materializa running→waiting_on_human (AOS-021): o run fica suspenso à
// espera de aval humano sobre uma tool call ESCALADA pelo Reference Monitor. Usa o MESMO
// lazy-claim do [runGate.Pause] — se a máquina ainda está em [state.Ready], reclama
// ready→running com o fencing token do lease, a aresta que AOS-017 exige antes de
// qualquer suspensão.
//
// Distinto de Pause: waiting_on_human é um gate HITL (com timeout fail-closed próprio),
// não uma pausa de steer. A tabela declarativa de AOS-017 já expõe ambas as arestas
// ({Running, WaitingOnHuman} e o regresso {WaitingOnHuman, Running}).
func (g *runGate) EscalateToHuman(ctx context.Context, reason string) error {
	if g.m.Current() == state.Ready {
		if err := g.m.Transition(ctx, state.Running, state.TransitionEvent{Token: g.token, Reason: reasonSteerRunClaim}); err != nil {
			return err
		}
	}
	return g.m.Transition(ctx, state.WaitingOnHuman, state.TransitionEvent{Reason: reason})
}

// resumeIfWaiting repõe em `running` um run que o log diz estar À ESPERA DE HUMANO. É o
// que fecha a RETOMA (AOS-021) do lado da máquina de estados: a suspensão é durável, pelo
// que a reconstrução no arranque devolve `waiting_on_human` — e sem esta reposição um run
// retomado que voltasse a escalar tentaria waiting_on_human→waiting_on_human, um par que a
// tabela não tem, e morreria como FALHADO em vez de suspender outra vez.
//
// Só transita QUANDO o estado é mesmo esse: um run novo (ready) ou já a correr não é
// tocado, e o stream/replay ficam byte-idênticos.
func (g *runGate) resumeIfWaiting(ctx context.Context) error {
	switch g.m.Current() {
	case state.WaitingOnHuman:
		return g.ResumeFromHuman(ctx, "retoma explicita apos aval humano (AOS-021)")
	case state.Paused:
		// A PAUSA TAMBÉM SE RETOMA, e é aqui que o condutor morto ganha o seu primeiro
		// chamador de produção. Até 2026-08-23 o [runGate.Resume] existia, estava ligado à
		// porta `control.StateGate`, e ninguém o abria: `paused` era absorvente de facto.
		//
		// Sem esta aresta, uma retoma de run pausado deixaria a máquina em `paused` durante
		// toda a nova hospedagem — e o disjuntor, o selo terminal e o deadline ficariam
		// todos desarmados, porque os três exigem `Running`.
		return g.Resume(ctx, "retoma explicita apos pausa graciosa")
	default:
		// Um run novo (ready) ou já a correr não é tocado, e o stream/replay ficam
		// byte-idênticos.
		return nil
	}
}

// ResumeFromHuman materializa waiting_on_human→running: o run volta a correr depois de o
// aval humano ser decidido (aprovado, ou expirado — decisão do dono: ao fim do TTL o run
// volta a running com a call negada, e o agente pode tentar outro caminho).
func (g *runGate) ResumeFromHuman(ctx context.Context, reason string) error {
	return g.m.Transition(ctx, state.Running, state.TransitionEvent{Reason: reason})
}

// resolveGate devolve o [runGate] concreto do run (nil se não aberto). Distinto de
// [runStateGates.Resolve], que devolve a porta control.StateGate: aqui o chamador precisa
// das transições HITL, que não fazem parte dessa porta.
func (g *runStateGates) resolveGate(runID string) *runGate {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gates[runID]
}

// Assegura em compile-time que runGate satisfaz a porta que o canal de steer consome.
var _ control.StateGate = (*runGate)(nil)
