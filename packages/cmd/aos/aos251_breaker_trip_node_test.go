package main

// AOS-251 — BREAKER EFECTIVO (achado F3, severidade ALTA).
//
// Dois mecanismos independentes mantinham o disjuntor do agente vivo INERTE no run comum:
//
//	(a) [runBreakers.observeAction] tinha ZERO chamadores ⇒ o detector de acções
//	    repetidas nunca observava ⇒ MadeProgress() sempre true ⇒ o sinal no-progress
//	    (ligado por omissão, MaxStaleIterations=3) nunca disparava;
//	(b) [breaker.Breaker.Observe] é no-op fora de `running` e o lazy-claim de AOS-218 só
//	    transitava ready→running no primeiro steer/escalada ⇒ um run sem steer ficava em
//	    `ready` do princípio ao fim e o breaker nunca acumulava.
//
// Este ficheiro tranca as duas correcções:
//
//   - [TestAOS251_ObserveActionHasProductionCaller] — guarda AST anti-regressão do
//     mecanismo (a): falha se observeAction voltar a ficar sem chamador de produção
//     (molde de env_surface_test.go / aos222_fencing_truthfulness_test.go);
//   - [TestAOS251_BreakerTripsOnDeniedLoop] — a prova de NÓ (molde de
//     approval_cycle_node_test.go): a MESMA tool call negada, repetida, dispara o breaker
//     ANTES de MaxTurns, com o veredicto atribuído e selado no log durável. Resolve a
//     divergência A1↔A2/A4 do relatório de prontidão §9 — é não-vacuoso: assere o TRIP,
//     não apenas o fim do run;
//   - o caso de CONTROLO do mesmo teste (run com progresso) prova a outra metade: o claim
//     ready→running no arranque (mecanismo b) acontece e NÃO dispara falsos positivos.

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// ---------------------------------------------------------------------------
// Guarda AST (mecanismo a): observeAction tem de ter um chamador de produção
// ---------------------------------------------------------------------------

// TestAOS251_ObserveActionHasProductionCaller falha se [runBreakers.observeAction] voltar
// a ficar sem chamadores — o defeito exacto de F3(a). A varredura é SINTÁCTICA (não regex):
// conta as REFERÊNCIAS `*.observeAction` nos ficheiros NÃO-teste do pacote (a ligação
// canónica é uma method value — WithActionObserver(breakers.observeAction) — que não é um
// CallExpr; procurar só chamadas deixaria a guarda cega à forma da ligação). A
// não-vacuidade é garantida exigindo também a DECLARAÇÃO do método: se alguém renomear o
// método e a guarda ficar cega, ela falha em vez de passar vacuamente.
func TestAOS251_ObserveActionHasProductionCaller(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("readdir do pacote do nó: %v", err)
	}
	fset := token.NewFileSet()
	references, declared := 0, false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parser.ParseFile(%q): %v", name, perr)
		}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "observeAction" {
				declared = true
				continue // a declaração não é uma referência
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "observeAction" {
					references++
				}
				return true
			})
		}
	}
	if !declared {
		t.Fatal("guarda cega: o método observeAction deixou de existir — actualiza a âncora desta guarda")
	}
	if references == 0 {
		t.Fatal("F3(a) REGREDIU: observeAction voltou a ter ZERO chamadores de produção — " +
			"o detector de no-progress fica cego e o breaker nunca dispara (AOS-251). " +
			"A ligação canónica é agentruntime.WithActionObserver(breakers.observeAction) no bootstrap.")
	}
}

// ---------------------------------------------------------------------------
// Prova de nó (mecanismos a+b): deny-loop ⇒ trip antes de MaxTurns
// ---------------------------------------------------------------------------

// bktDenyLoopModel pede a MESMA tool call em todos os turnos — o loop patológico observado
// ao vivo (a call é negada em cada turno e o agente reemite-a às cegas). Conta as idas ao
// modelo para a asserção não-vacuosa de quantos turnos o run viveu.
type bktDenyLoopModel struct{ hits int64 }

func (m *bktDenyLoopModel) Call(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	atomic.AddInt64(&m.hits, 1)
	return agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{{
		ToolID: "sonda", Capability: tnCap, Input: []byte(`{"alvo":"x"}`),
		ResourceType: "doc", ResourceValue: "x", ResourceRegion: "eu",
	}}}, nil
}

func (m *bktDenyLoopModel) chamadas() int64 { return atomic.LoadInt64(&m.hits) }

// bktProgressModel faz PROGRESSO: cada turno pede uma call distinta (hash diferente) e
// conclui no 3.º turno. É o controlo que prova que o claim no arranque não gera falsos
// positivos — um run saudável NÃO dispara o breaker.
type bktProgressModel struct{ hits int64 }

func (m *bktProgressModel) Call(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	atomic.AddInt64(&m.hits, 1)
	if view.Turn < 3 {
		return agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{{
			ToolID: "sonda", Capability: tnCap, Input: []byte(`{"alvo":"doc-` + string(rune('a'+view.Turn)) + `"}`),
			ResourceType: "doc", ResourceValue: "x", ResourceRegion: "eu",
		}}}, nil
	}
	return agentruntime.ModelResponse{Text: "concluido", Final: true}, nil
}

// bktNode sobe um nó REAL (Bootstrap) com os limiares do breaker PINADOS — o teste é sobre
// o sinal no-progress com os defaults de produção (3 iterações estéreis); o wall-clock fica
// folgado para nunca interferir.
func bktNode(t *testing.T, model agentruntime.ModelClient) (*Node, *NodeService) {
	t.Helper()
	t.Setenv("AOS_BREAKER_MAX_STALE_ITERATIONS", "3")
	t.Setenv("AOS_BREAKER_MAX_WALL_CLOCK", "1h")
	t.Setenv("AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC", "0")
	t.Setenv("AOS_BREAKER_MAX_TOKENS_PER_SEC", "0")

	cfg := tnBaseConfig()
	cfg.Model = model
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	svc, err := NewNodeService(node)
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	return node, svc
}

func bktToken(t *testing.T, node *Node) string {
	t.Helper()
	tok, err := node.Authority.MintForHuman(context.Background(), tnHuman, tnAgent, tnClass, []string{tnCap})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return tok.Compact
}

func bktSubmitAndWait(t *testing.T, svc *NodeService, runID string, maxTurns int) RunOutcome {
	t.Helper()
	if err := svc.Submit(context.Background(), agentruntime.Goal{
		RunID:      runID,
		Principal:  referencemonitor.Principal{NHIID: tnAgent},
		Credential: bktToken(t, svc.node),
		Objective:  "sonda governada",
		MaxTurns:   maxTurns,
	}); err != nil {
		t.Fatalf("Submit(%s): %v", runID, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	oc, ok, err := svc.Wait(ctx, runID)
	if err != nil {
		t.Fatalf("Wait(%s): %v", runID, err)
	}
	if !ok {
		t.Fatalf("Wait(%s): run sem desfecho registado", runID)
	}
	return oc
}

// TestAOS251_BreakerTripsOnDeniedLoop é o teste de nó do CA3: a mesma tool call NEGADA
// (a tool não está registada — default-deny do RM — e o modelo reemite-a todos os turnos)
// faz o breaker disparar por NO-PROGRESS muito antes de MaxTurns, com a transição durável
// atribuída ao breaker (razão breaker_no_progress) e relida do log por uma máquina fresca.
func TestAOS251_BreakerTripsOnDeniedLoop(t *testing.T) {
	model := &bktDenyLoopModel{}
	node, svc := bktNode(t, model)

	const runID = "run-breaker-trip"
	const maxTurns = 12
	oc := bktSubmitAndWait(t, svc, runID, maxTurns)

	// O veredicto é o TRIP do breaker — não o esgotamento cego de MaxTurns (não-vacuidade:
	// antes do fix o run vivia os 12 turnos e saía com ErrMaxTurnsExceeded).
	if oc.Err != nil {
		t.Fatalf("um run parado pelo breaker NÃO é um erro (antes do fix era ErrMaxTurnsExceeded): %v", oc.Err)
	}
	if !oc.Result.Tripped {
		t.Fatalf("o breaker NÃO disparou no deny-loop — F3 presente. Result: %+v", oc.Result)
	}
	if oc.Result.BreakerTarget != string(state.Paused) {
		t.Fatalf("o alvo do trip de no-progress é paused, veio %q", oc.Result.BreakerTarget)
	}
	if oc.Result.Turns >= maxTurns {
		t.Fatalf("o trip tem de ocorrer ANTES de MaxTurns (%d); o run viveu %d turnos", maxTurns, oc.Result.Turns)
	}
	// Mecânica exacta (detector: 3.ª repetição marca loop; breaker: 3 iterações estéreis
	// consecutivas disparam): turnos 1-2 progridem, 3-5 estéreis ⇒ trip no turno 5.
	if oc.Result.Turns != 5 {
		t.Fatalf("com os defaults pinados o trip ocorre no turno 5; veio %d", oc.Result.Turns)
	}
	if got := model.chamadas(); got != int64(oc.Result.Turns) {
		t.Fatalf("o modelo foi interrogado %d vezes mas o run reporta %d turnos", got, oc.Result.Turns)
	}

	// VEREDICTO ATRIBUÍDO E SELADO: uma máquina FRESCA reconstruída do log durável está em
	// paused, e o evento de transição carrega a razão que atribui a causa AO BREAKER.
	assertMachineState(t, node.EventStore, runID, state.Paused)
	tripEvent := bktTransicao(t, node, runID, func(_, to, _ string) bool { return to == string(state.Paused) })
	if tripEvent == nil {
		t.Fatal("o log durável NÃO regista a transição para paused — o veredicto do breaker não está selado")
	}
	if tripEvent.From != string(state.Running) || tripEvent.Reason != "breaker_no_progress" {
		t.Fatalf("a transição de trip tem de ser running→paused atribuída ao breaker (breaker_no_progress); veio %+v", *tripEvent)
	}
}

// TestAOS251_RunWithProgressDoesNotTrip é o CONTROLO não-vacuoso: um run que progride
// (hash distinto a cada turno) NÃO dispara o breaker e termina normalmente. Prova também o
// mecanismo (b): o claim ready→running no arranque ACONTECEU.
//
// A PROVA DO CLAIM É A ARESTA, NÃO O ESTADO FINAL (correcção do achado F-A1 da auditoria
// da W0). A versão original desta asserção exigia que a máquina fresca ficasse em `running`
// no fim — o que era verdade quando o claim era a ÚNICA transição que um run bem-sucedido
// escrevia. Com o selo terminal de AOS-252, o mesmo caminho de saída escreve a seguir
// running→complete (e escreve-o ANTES de o Wait devolver: o defer do selo está a montante
// do `defer s.finish(rs)` na pilha LIFO de hostRun). Exigir `running` no fim passou a ser
// exigir que o desfecho do run NÃO ficasse no log durável — o defeito exacto (F4) que
// AOS-252 fecha. O invariante de AOS-251 é que a aresta ready→running foi materializada com
// a razão do claim de arranque; é isso que se assere aqui, relido do log, mais o estado
// final `complete` que prova que os dois mecanismos coexistem sem se atropelarem.
func TestAOS251_RunWithProgressDoesNotTrip(t *testing.T) {
	node, svc := bktNode(t, &bktProgressModel{})

	const runID = "run-breaker-control"
	oc := bktSubmitAndWait(t, svc, runID, 12)

	if oc.Err != nil {
		t.Fatalf("run com progresso devia terminar sem erro: %v", oc.Err)
	}
	if oc.Result.Tripped {
		t.Fatalf("FALSO POSITIVO: um run com progresso NÃO pode disparar o breaker. Result: %+v", oc.Result)
	}
	if !oc.Result.Terminated {
		t.Fatalf("run com progresso devia TERMINAR: %+v", oc.Result)
	}

	// Mecanismo (b), relido do log durável: existe uma transição ready→running com a razão
	// do CLAIM DE ARRANQUE. Antes do fix de AOS-251 um run sem steer nem escalada não gerava
	// transição NENHUMA e o disjuntor ficava cego — esta asserção cai nesse mundo.
	claim := bktTransicao(t, node, runID, func(from, to, reason string) bool {
		return from == string(state.Ready) && to == string(state.Running) && reason == reasonRunStartClaim
	})
	if claim == nil {
		t.Fatalf("o log durável NÃO regista ready→running com a razão %q — o claim no arranque (AOS-251) não aconteceu e o disjuntor correu cego", reasonRunStartClaim)
	}
	// E o desfecho ficou selado (AOS-252): running→complete. Um run bem-sucedido tem de ser
	// distinguível de um crash no log, e o selo NÃO pode ter apagado o claim acima.
	if selo := bktTransicao(t, node, runID, func(from, to, reason string) bool {
		return from == string(state.Running) && to == string(state.Complete) && reason == reasonRunComplete
	}); selo == nil {
		t.Fatalf("o log durável NÃO regista running→complete com a razão %q — o selo terminal (AOS-252) não correu neste caminho", reasonRunComplete)
	}
	assertMachineState(t, node.EventStore, runID, state.Complete)
}

// bktTransicao devolve a PRIMEIRA transição do stream durável do run que satisfaz o
// predicado (nil se nenhuma). Lê o Event Store directamente — a prova é do log, não da
// contabilidade em memória do serviço.
func bktTransicao(t *testing.T, node *Node, runID string, ok func(from, to, reason string) bool) *bktTransitionPayload {
	t.Helper()
	events, err := node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read do stream do run: %v", err)
	}
	for i := range events {
		if events[i].Type != state.EventTypeTransition {
			continue
		}
		var rec bktTransitionPayload
		if err := json.Unmarshal(events[i].Payload, &rec); err != nil {
			t.Fatalf("payload de transição ilegível: %v", err)
		}
		if ok(rec.From, rec.To, rec.Reason) {
			rec := rec
			return &rec
		}
	}
	return nil
}

// bktTransitionPayload é a projecção mínima do evento de transição de AOS-017 que estas
// provas leem (from/to/reason) — o resto do payload não interessa à asserção.
type bktTransitionPayload struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}
