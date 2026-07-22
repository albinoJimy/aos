// Command aos-demo é o ÁPICE MÍNIMO do AOS: um único processo, ZERO rede, que
// compõe os pilares in-process e conduz um fluxo de demonstração end-to-end,
// imprimindo cada passo em stdout (AOS-149).
//
// É deliberadamente o "hello world" da composição — a contrapartida single-process
// do composition root securizado de packages/integration. Prova que os pilares
// (Event Store, Reference Monitor, Agent Runtime, canal de controlo, máquina de
// estados durável, reflexão de estado e adaptador de superfície) encaixam sem
// transporte de rede, sem infra externa e sem dependências novas.
//
// LIMITAÇÕES ASSUMIDAS (marcadas no código e reimpressas no fim do demo):
//
//	(a) O loop base do Agent Runtime NÃO consome o SteerChannel: aqui o ciclo
//	    pause/steer/resume é EXCLUSIVAMENTE out-of-band (despachado e relido fora do
//	    loop). Ligar o canal ao loop é o ticket AOS-158.
//	(b) O Reference Monitor usa os STUBS NEUTROS (identity/policy/budget/egress/audit)
//	    — aceitável no ápice mínimo, mas SEM enforcement real. O enforcement de
//	    produção é PR-0.c (AOS-152/153/154).
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"time"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	controlsurface "github.com/aos-ref/control-plane/governance/control-surface"
	surfaceadapter "github.com/aos-ref/control-plane/governance/surface-adapter"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/substrate/eventstore"
)

// demoRunID é o identificador do run da demonstração (stream_id no Event Store).
const demoRunID = "run-demo-0001"

// demoEmitterID é a identidade do emissor de controlo da demo. A chave HMAC
// demo-grade correspondente é GERADA EM RUNTIME (crypto/rand) — nada de segredos
// no código (DoD de AOS-149) — e é efémera ao processo. Em produção a identidade
// real (ed25519, AOS-005) liga-se por um adaptador ao [control.Authenticator].
const demoEmitterID = "operator:demo"

// fakeModel é um [agentruntime.ModelClient] FAKE, in-process e ZERO-REDE: devolve
// sempre uma resposta final fixa e termina o run no primeiro turno (Final=true, sem
// tool calls). Substitui o Model Gateway real (EPIC-06) no ápice mínimo. Como não
// pede tools, o Reference Monitor nunca é invocado para mediação neste demo.
type fakeModel struct {
	reply string
}

// Call implementa [agentruntime.ModelClient]. Ignora a [agentruntime.PromptView] (a
// resposta é determinística) e conclui a tarefa de imediato.
func (m fakeModel) Call(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{
		Text:  m.reply,
		Final: true,
		Usage: agentruntime.Usage{InputTokens: 8, OutputTokens: 4},
	}, nil
}

func main() {
	if err := runDemo(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "aos-demo: %v\n", err)
		os.Exit(1)
	}
}

// runDemo compõe os pilares in-process e conduz o fluxo de demonstração, escrevendo
// cada passo em w. É extraída de main para ser exercitável end-to-end por um teste
// (ver demo_test.go) sem processo nem rede. Devolve o primeiro erro fatal encontrado.
//
// A ORDEM DE COMPOSIÇÃO segue o critério de aceitação de AOS-149:
//
//	1. Event Store            (substrate/eventstore)
//	2. Authenticator (HMAC)   (agent-runtime/control) — demo-grade
//	3. SteerChannel           (agent-runtime/control)
//	4. State Machine por run  (agent-runtime/state)
//	5. StateProjector         (governance/control-surface) — reflexão do estado
//	6. Modelo FAKE            (implementa agent-runtime.ModelClient) — zero rede
//	7. Reference Monitor (stubs neutros) + TurnRecorder + Agent Runtime
func runDemo(w io.Writer) error {
	ctx := context.Background()
	step := stepPrinter(w)

	step("ápice mínimo AOS — single-process, zero-rede (AOS-149)")

	// (1) EVENT STORE — o substrato append-only durável in-memory (AOS-002). É a
	// espinha dorsal partilhada: turnos, transições de estado e sinais de controlo
	// vivem todos aqui, por stream_id = run_id.
	store, err := eventstore.New()
	if err != nil {
		return fmt.Errorf("compor event store: %w", err)
	}
	defer store.Close()
	step("1) Event Store composto (in-memory, append-only)")

	// (2) AUTHENTICATOR — a fronteira de segurança do canal de controlo. DEMO-GRADE:
	// HMAC-SHA256 com um segredo partilhado, só stdlib. Regista-se a chave do emissor
	// da demo (default-deny: sem registo, qualquer sinal é rejeitado). Em produção a
	// identidade ed25519 real (AOS-005) liga-se por um adaptador.
	auth := control.NewHMACAuthenticator()
	// Chave demo-grade EFÉMERA gerada em runtime (crypto/rand) — sem segredos no
	// código. Default-deny: sem registo, qualquer sinal é rejeitado.
	demoKey := make([]byte, 32)
	if _, err := rand.Read(demoKey); err != nil {
		return fmt.Errorf("gerar chave demo: %w", err)
	}
	auth.Register(demoEmitterID, demoKey)
	step("2) Authenticator HMAC composto (demo-grade, chave efémera) + chave do emissor registada")

	// (3) STEER CHANNEL — o canal de controlo OUT-OF-BAND (pause/steer/resume,
	// AOS-023). Persiste cada sinal aceite como evento append-only no mesmo Event Store.
	steer, err := control.NewChannel(store, auth)
	if err != nil {
		return fmt.Errorf("compor steer channel: %w", err)
	}
	step("3) SteerChannel composto sobre o Event Store")

	// (4) STATE MACHINE — a máquina de estados DURÁVEL do run (AOS-017). Cada
	// transição válida é um evento append-only; o estado corrente é reconstruível por
	// replay.
	machine, err := state.NewMachine(store, demoRunID)
	if err != nil {
		return fmt.Errorf("compor state machine: %w", err)
	}
	step("4) State Machine do run %q composta (estado inicial: %s)", demoRunID, machine.Current())

	// (5) STATE PROJECTOR — o read-model da reflexão de estado (control-surface).
	// SUBSCREVE as transições do run ANTES de as provocar, para as reflectir por push.
	projector, err := controlsurface.NewStateProjector(ctx, store, demoRunID)
	if err != nil {
		return fmt.Errorf("compor state projector: %w", err)
	}
	defer projector.Close()
	// Observa a reflexão num canal para esperar deterministicamente pela propagação
	// (a subscrição entrega numa goroutine dedicada).
	reflected := make(chan state.State, 8)
	cancelObserve := projector.Observe(func(s state.State) { reflected <- s })
	defer cancelObserve()
	step("5) StateProjector composto e subscrito ao estado do run")

	// (6) MODELO FAKE — implementa [agentruntime.ModelClient] in-process, sem rede.
	model := fakeModel{reply: "demonstração concluída pelo modelo fake"}
	step("6) Modelo FAKE composto (in-process, zero-rede)")

	// (7) REFERENCE MONITOR + TURN RECORDER + AGENT RUNTIME.
	// O RM é construído com os STUBS NEUTROS na ordem canónica de mediação. ACEITÁVEL
	// no ápice mínimo: os stubs permitem por omissão — NÃO há enforcement real. O
	// enforcement de produção é PR-0.c (AOS-152/153/154).
	rm := referencemonitor.New(referencemonitor.WithHooks(
		referencemonitor.IdentityStub{},
		referencemonitor.PolicyStub{},
		referencemonitor.BudgetStub{},
		referencemonitor.EgressStub{},
		referencemonitor.AuditStub{},
	))
	recorder := agentruntime.NewTurnRecorder(store)
	rt := agentruntime.New(model, rm, recorder)
	step("7) Reference Monitor (stubs neutros) + TurnRecorder + Agent Runtime compostos")

	// ---- FLUXO DE DEMONSTRAÇÃO -------------------------------------------------

	step("--- fluxo de demonstração ---")

	// Cria o run: transita a máquina durável ready → running (o claim exige um fencing
	// token válido; Uint64Token(1) serve o ápice mínimo). O StateProjector reflecte-o.
	step("a) a criar o run: transição durável ready → running")
	if err := machine.Transition(ctx, state.Running, state.TransitionEvent{
		Reason: "demo_claim",
		Token:  state.Uint64Token(1),
	}); err != nil {
		return fmt.Errorf("transição ready→running: %w", err)
	}

	// Espera a reflexão da transição (push via subscrição) antes de a ler.
	if err := awaitState(reflected, state.Running); err != nil {
		return err
	}
	step("b) StateProjector reflectiu o estado durável: Current() = %s", projector.Current())

	// Corre um turno via o Agent Runtime com o modelo fake. O loop monta o prompt,
	// chama o modelo (fake), grava o turno no Event Store e termina (resposta final).
	goal := agentruntime.Goal{
		RunID: demoRunID,
		Principal: referencemonitor.Principal{
			NHIID:      "nhi:demo-agent",
			AgentID:    "agent-demo",
			AgentClass: "demonstrator",
		},
		Model:     agentruntime.ModelConfig{ModelID: "fake-model", Seed: 1},
		System:    "és o agente de demonstração do ápice mínimo",
		Objective: "demonstra a composição in-process dos pilares",
	}
	res, err := rt.Run(ctx, goal)
	if err != nil {
		return fmt.Errorf("correr turno: %w", err)
	}
	step("c) turno corrido: turns=%d terminated=%t final=%q", res.Turns, res.Terminated, res.FinalText)

	// Renderiza uma SUPERFÍCIE via o Renderer do surface-adapter: constrói um
	// approval-card canónico e renderiza-o no componente desktop (data-only).
	if err := demoRenderSurface(w, step); err != nil {
		return err
	}

	// DEMONSTRA um STEER OUT-OF-BAND: despacha sinais de controlo no SteerChannel e
	// relê a projecção (o "eco"). LIMITAÇÃO (a): o loop acima NÃO consome estes sinais
	// — são puramente out-of-band aqui (a ligação ao loop é AOS-158).
	if err := demoSteerOutOfBand(ctx, w, step, steer, auth); err != nil {
		return err
	}

	// ---- LIMITAÇÕES (reimpressas) ---------------------------------------------
	step("--- limitações deste ápice mínimo ---")
	step("LIMITAÇÃO (a): o loop do Agent Runtime NÃO consome o SteerChannel; " +
		"pause/steer/resume é só OUT-OF-BAND aqui (ligação ao loop = AOS-158)")
	step("LIMITAÇÃO (b): o Reference Monitor usa STUBS NEUTROS (sem enforcement real); " +
		"o enforcement de produção é PR-0.c (AOS-152/153/154)")
	step("demo concluído com sucesso")
	return nil
}

// demoRenderSurface constrói um approval-card canónico e renderiza-o na superfície
// desktop via o [surfaceadapter.Renderer], imprimindo o [surfaceadapter.DesktopComponent]
// resultante (estrutura de dados, não uma chamada a API real).
func demoRenderSurface(w io.Writer, step func(string, ...any)) error {
	card, err := approvalcard.BuildCard(risk.ConfirmationRequest{
		Class:        risk.ClassDanger,
		Irreversible: true,
		Preview:      "cap:demo.publish -> superfície desktop",
		Principal:    "agent-demo",
		Capability:   "cap:demo.publish",
		Resource:     "surface:desktop",
	}, approvalcard.WithRequestID("card-demo-0001"))
	if err != nil {
		return fmt.Errorf("construir approval-card: %w", err)
	}

	renderer, err := surfaceadapter.RendererFor(surfaceadapter.PlatformDesktop)
	if err != nil {
		return fmt.Errorf("obter renderer desktop: %w", err)
	}
	rendered, err := renderer.Render(card)
	if err != nil {
		return fmt.Errorf("renderizar card: %w", err)
	}
	comp := rendered.DesktopComponent
	if comp == nil {
		return fmt.Errorf("render desktop sem DesktopComponent")
	}
	step("d) superfície renderizada (desktop): título=%q corpo=%q botões=%d",
		comp.Title, comp.Body, len(comp.Buttons))
	for _, b := range comp.Buttons {
		step("     botão: %q (action=%s danger=%t)", b.Label, b.Action, b.Danger)
	}
	return nil
}

// demoSteerOutOfBand despacha uma mensagem de controlo (steer) e uma pausa no
// SteerChannel e relê a projecção corrente — o "eco". Assina cada sinal com o emissor
// demo-grade registado (a fronteira de não-repúdio de ADR-013).
//
// LIMITAÇÃO (a): estes sinais são OUT-OF-BAND — o loop base não os consome; aqui só
// se prova o caminho de despacho/persistência/releitura do canal.
func demoSteerOutOfBand(ctx context.Context, w io.Writer, step func(string, ...any), steer *control.SteerChannel, auth *control.HMACAuthenticator) error {
	// STEER: injecta uma correcção confiável, assinada pelo emissor demo.
	correction := []byte("corrige o rumo: prioriza a superfície desktop")
	steerEmitter, err := auth.Sign(demoRunID, control.SignalSteer, correction, demoEmitterID)
	if err != nil {
		return fmt.Errorf("assinar steer: %w", err)
	}
	if err := steer.Steer(ctx, demoRunID, correction, steerEmitter); err != nil {
		return fmt.Errorf("despachar steer: %w", err)
	}
	echo, ok := steer.PendingCorrection(demoRunID)
	if !ok {
		return fmt.Errorf("steer despachado mas sem correcção pendente")
	}
	step("e) steer OUT-OF-BAND despachado; eco da correcção pendente: %q", string(echo))

	// PAUSE: pede a pausa graciosa (materializar-se-ia no fim do turno se o loop
	// consumisse o canal — LIMITAÇÃO (a)).
	pauseEmitter, err := auth.Sign(demoRunID, control.SignalPause, nil, demoEmitterID)
	if err != nil {
		return fmt.Errorf("assinar pause: %w", err)
	}
	if err := steer.Pause(ctx, demoRunID, pauseEmitter); err != nil {
		return fmt.Errorf("despachar pause: %w", err)
	}
	step("f) pause OUT-OF-BAND despachado; eco de pausa pendente: %t", steer.PendingPause(demoRunID))
	return nil
}

// awaitState espera (com timeout) que a reflexão do StateProjector atinja want,
// consumindo o canal de observação. Falha fail-closed se o estado não propagar.
func awaitState(reflected <-chan state.State, want state.State) error {
	timeout := time.After(5 * time.Second)
	for {
		select {
		case s := <-reflected:
			if s == want {
				return nil
			}
		case <-timeout:
			return fmt.Errorf("estado %s não reflectido a tempo", want)
		}
	}
}

// stepPrinter devolve um impressor de passos numerados que escreve uma linha por
// passo em w (o marcador "[demo]" torna o output fácil de asserir no teste).
func stepPrinter(w io.Writer) func(string, ...any) {
	return func(format string, args ...any) {
		fmt.Fprintf(w, "[demo] "+format+"\n", args...)
	}
}
