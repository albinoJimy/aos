package main

import (
	"context"
	"sync"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	control "github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
)

// reasonSteerRunClaim é o motivo gravado na aresta ready→running reclamada pelo
// lazy-claim de AOS-218 (ver [runGate.Pause]) — rótulo de auditoria legível, não segredo.
const reasonSteerRunClaim = "steer_run_claim"

// runStateGates é a COSTURA MÍNIMA (AOS-218) que dá ao canal de steer (AOS-023) uma
// FONTE de [control.StateGate] durável POR-RUN — sem inventar um mecanismo de estado
// novo. Reusa a máquina de estados de AOS-017 ([state.Machine]) sobre o MESMO Event Store
// do nó: a pausa graciosa materializa a aresta declarativa running→paused e a retoma a
// paused→running, gravadas como eventos append-only (reconstruíveis por replay, sobrevivem
// a crash). O loop de serviço ABRE um gate por run hospedado ([Open]) e LIBERTA-o no fim
// ([Close]); o loop base resolve-o por [Resolve] — o gates func de [control.NewLoopSteer].
//
// LAZY-CLAIM (retro-compatibilidade estrita): o gate NÃO transita nada ao abrir. A aresta
// ready→running só é reclamada — com o fencing token do LEASE já detido (AOS-018) — no
// PRIMEIRO pause de facto pedido. Um run SEM steer nunca chama Pause, logo não gera
// NENHUM evento de transição: o seu stream (e o seu replay/resume) fica byte-idêntico ao
// de antes de AOS-218. É a razão de não se conduzir a máquina eagerly no arranque.
//
// Seguro para uso concorrente: o mutex serializa o mapa; cada [state.Machine] tem o seu
// próprio mutex interno.
type runStateGates struct {
	store  state.EventStore
	tracer agentruntime.Tracer

	mu    sync.Mutex
	gates map[string]*runGate
}

// newRunStateGates constrói o registo sobre o Event Store do nó. tracer é reusado da
// observabilidade do nó (um span por transição confirmada); nil ⇒ [agentruntime.NoopTracer].
func newRunStateGates(store state.EventStore, tracer agentruntime.Tracer) *runStateGates {
	if tracer == nil {
		tracer = agentruntime.NoopTracer{}
	}
	return &runStateGates{store: store, tracer: tracer, gates: make(map[string]*runGate)}
}

// Open compõe (e RECONSTRÓI do log — para a retoma após crash) a máquina de estados do
// run e regista o seu gate. token é o fencing token do lease de posse: o MESMO que
// AOS-017 exige no claim ready→running, propagado sem o duplicar. NÃO transita nada aqui
// (lazy-claim, ver [runStateGates]). Fail-closed: um log de estado corrompido aborta —
// mas um run novo reconstrói para [state.Ready] e nunca falha.
func (g *runStateGates) Open(ctx context.Context, runID string, token state.FencingToken) error {
	m, err := state.NewMachine(g.store, runID, state.WithTracer(g.tracer))
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

// runGate adapta a [state.Machine] de UM run ao [control.StateGate], com o LAZY-CLAIM de
// AOS-218: materializa running→paused (e paused→running na retoma) usando as arestas já
// EXPOSTAS por AOS-017, reclamando ready→running — com o fencing token do lease — apenas
// quando o PRIMEIRO pause é de facto pedido, para que um run sem steer não gere transições.
type runGate struct {
	m     *state.Machine
	token state.FencingToken
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
	if g.m.Current() != state.WaitingOnHuman {
		return nil
	}
	return g.ResumeFromHuman(ctx, "retoma explicita apos aval humano (AOS-021)")
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
