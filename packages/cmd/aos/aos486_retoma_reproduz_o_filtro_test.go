package main

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	audit "github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-486 — a retoma e o crash-resume reproduzem o MESMO filtro da oferta.
//
// O filtro fixa-se no arranque do run a partir de `Goal.AllowedTools`. Um run re-hospedado
// (retoma de AOS-021, varredura de arranque de AOS-253) reconstitui o goal do registo de retoma
// e volta a passar por `submit` — e o turno seguinte tem de oferecer exactamente o que o run
// ofereceria sem interrupção: os mesmos schemas no pedido, o mesmo prefixo, o mesmo `prompt_hash`.
//
// PORQUE O GATEWAY É UM DUPLO E O ADAPTADOR NÃO. O crash-resume re-hospeda com a credencial
// VAZIA (um crash não tem humano), e o estágio authn do gateway real nega esse turno de modelo
// antes de chegar ao provider. Para medir o que o pedido LEVARIA, o cliente de modelo é o
// adaptador real ([modelgateway.ModelClientAdapter], com a mesma opção que o nó liga em
// newGatewayModelClient e o mesmo enriquecedor de governação) sobre um [port.Gateway] que grava
// o pedido. O caminho com o gateway de produção inteiro está em aos486_oferta_no_composto_test.go.

// aos486Gravador é o [port.Gateway] que grava cada pedido. Um prompt sem resultado de tool é o
// turno 1 e responde com a tool call `counter`; os seguintes concluem.
type aos486Gravador struct {
	port.Gateway
	mu      sync.Mutex
	pedidos []port.ChatRequest
}

func (g *aos486Gravador) Chat(_ context.Context, req port.ChatRequest) (port.ChatResponse, error) {
	g.mu.Lock()
	g.pedidos = append(g.pedidos, req)
	g.mu.Unlock()
	uso := port.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}
	if len(req.Messages) == 1 && !strings.Contains(req.Messages[0].Content, "<"+string(agentruntime.TailToolResult)) {
		return port.ChatResponse{Model: "modelo-486", Usage: uso, Choices: []port.Choice{{
			Message: port.Message{Role: port.RoleAssistant, ToolCalls: []port.ToolCall{{
				ID: "call-1", Type: "function", Function: port.FunctionCall{Name: "counter", Arguments: "tick"},
			}}},
			FinishReason: "tool_calls",
		}}}, nil
	}
	return port.ChatResponse{Model: "modelo-486", Usage: uso, Choices: []port.Choice{{
		Message: port.Message{Role: port.RoleAssistant, Content: "feito"}, FinishReason: "stop",
	}}}, nil
}

func (g *aos486Gravador) vistos() []port.ChatRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]port.ChatRequest(nil), g.pedidos...)
}

// aos486ClienteSobre compõe o cliente de modelo do nó sobre o gateway dado: o adaptador real
// com o tool set do nó e a fonte de ctx da lista-branca, por dentro do enriquecedor.
func aos486ClienteSobre(gw port.Gateway) agentruntime.ModelClient {
	var tools []port.Tool
	for _, nome := range aos486OrdemDoFicheiro {
		tools = append(tools, port.Tool{Type: "function", Function: port.FunctionDef{Name: nome, Description: "tool " + nome}})
	}
	return &toolEnrichingClient{
		inner: modelgateway.NewModelClient(gw, "modelo-486",
			modelgateway.WithTools(tools),
			modelgateway.WithToolOfferFromContext(runToolOfferFromContext)),
		bindings: map[string]toolBinding{"counter": {capability: durCap}},
	}
}

// aos486Incarnacao é uma incarnação do nó sobre um substrato dado.
type aos486Incarnacao struct {
	node    *Node
	cred    string
	gw      *aos486Gravador
	counter *int64
}

func aos486Incarnar(t *testing.T, store *eventstore.Store, vault audit.KeyVault, approvers []ApproverConfig, counter *int64) aos486Incarnacao {
	t.Helper()
	signer := durSigner(t)
	var entries []domain.Entry
	for _, nome := range aos486OrdemDoFicheiro {
		entries = append(entries, aos486Entry(signer, nome))
	}
	gw := &aos486Gravador{}
	node, cred := obsPermitNodeWith(t, "", aos486ClienteSobre(gw), func(cfg *Config) {
		cfg.EventStore = store
		cfg.DSARVault = vault
		cfg.DurableExecution = true
		cfg.Approvers = approvers
		cfg.Catalog = catalogStub{entries: entries}
	})
	if err := node.Runtime.Register("counter", func(context.Context, []byte) ([]byte, error) {
		atomic.AddInt64(counter, 1)
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("Register(counter): %v", err)
	}
	return aos486Incarnacao{node: node, cred: cred, gw: gw, counter: counter}
}

func aos486Servico(t *testing.T, node *Node) *NodeService {
	t.Helper()
	svc, err := NewNodeService(node, WithDeadlineSweepInterval(0), WithServiceLog(io.Discard))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	t.Cleanup(func() {
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.Shutdown(sc)
	})
	return svc
}

func aos486Esperar(t *testing.T, svc *NodeService, runID string) {
	t.Helper()
	wc, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	oc, ok, err := svc.Wait(wc, runID)
	if err != nil || !ok {
		t.Fatalf("Wait(%s): ok=%v err=%v", runID, ok, err)
	}
	if oc.Err != nil || !oc.Result.Terminated {
		t.Fatalf("o run %s devia completar; result=%+v err=%v", runID, oc.Result, oc.Err)
	}
}

// aos486Prefixo devolve o prefixo imutável do prompt (system + bloco TOOLSET).
func aos486Prefixo(t *testing.T, prompt string) string {
	t.Helper()
	const fecha = "=== CONTEXT (append-only) ===\n"
	i := strings.Index(prompt, fecha)
	if i < 0 {
		t.Fatalf("o prompt nao tem o cabecalho do tail:\n%s", prompt)
	}
	return prompt[:i+len(fecha)]
}

func aos486NomesDoPedido(req port.ChatRequest) []string {
	out := []string{}
	for _, tool := range req.Tools {
		out = append(out, tool.Function.Name)
	}
	return out
}

func aos486TurnosDoRun(t *testing.T, store *eventstore.Store, runID string) []aos486Turno {
	t.Helper()
	eventos, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read(%s): %v", runID, err)
	}
	return aos486LerTurnos(t, eventos)
}

func TestAOS486_RetomaECrashResumeReproduzemOFiltro(t *testing.T) {
	listas := []struct {
		nome       string
		lista      []string
		querPedido []string // schemas no pedido, pela ordem do nó
		querPrompt []string // bloco TOOLSET e manifesto, pela ordem congelada
		querExecs  int64    // `counter` só executa se a lista a admitir
	}{
		{"lista vazia", []string{}, []string{}, []string{}, 0},
		{"lista com duas", []string{"beta", "counter"}, []string{"counter", "beta"}, []string{"beta", "counter"}, 1},
	}
	for _, l := range listas {
		for _, via := range []string{"crash-resume", "retoma"} {
			t.Run(l.nome+"/"+via, func(t *testing.T) {
				pinBreakerEnv(t, "0", "0", "0", "0")
				ctx := context.Background()
				const runID = "plan-486~retomado"
				approvers := crashResumeApprovers(t)
				goal := agentruntime.Goal{
					RunID:        runID,
					Principal:    referencemonitor.Principal{NHIID: durAgent},
					Model:        agentruntime.ModelConfig{ModelID: "modelo-486"},
					Objective:    "o trabalho de um no do plano",
					MaxTurns:     4,
					AllowedTools: l.lista,
				}

				// ===== REFERÊNCIA: o mesmo run, sem interrupção, pelo serviço do nó.
				storeRef, err := eventstore.New()
				if err != nil {
					t.Fatalf("eventstore.New: %v", err)
				}
				var contaRef int64
				ref := aos486Incarnar(t, storeRef, audit.NewInMemoryKeyVault(nil), approvers, &contaRef)
				t.Cleanup(func() { _ = ref.node.Close() })
				svcRef := aos486Servico(t, ref.node)
				inteiro := goal
				inteiro.Credential = ref.cred
				if err := svcRef.Submit(ctx, inteiro); err != nil {
					t.Fatalf("Submit (referencia): %v", err)
				}
				aos486Esperar(t, svcRef, runID)
				pedidosRef, turnosRef := ref.gw.vistos(), aos486TurnosDoRun(t, storeRef, runID)
				if len(pedidosRef) != 2 || len(turnosRef) != 2 {
					t.Fatalf("a referencia tinha de ter 2 pedidos e 2 turnos: %d e %d", len(pedidosRef), len(turnosRef))
				}
				// A referência já é o run filtrado — senão comparava-se a retoma com o defeito.
				for i, p := range pedidosRef {
					if got := aos486NomesDoPedido(p); !reflect.DeepEqual(got, l.querPedido) {
						t.Fatalf("referencia, pedido %d: schemas %v, quero %v", i+1, got, l.querPedido)
					}
					if got := aos486BlocoToolset(t, p.Messages[0].Content); !reflect.DeepEqual(got, l.querPrompt) {
						t.Fatalf("referencia, pedido %d: bloco TOOLSET %v, quero %v", i+1, got, l.querPrompt)
					}
				}

				// ===== INCARNAÇÃO 1: dá o turno 1 e "crasha" (molde do AOS-253).
				store, err := eventstore.New()
				if err != nil {
					t.Fatalf("eventstore.New: %v", err)
				}
				vault := audit.NewInMemoryKeyVault(nil)
				var conta int64
				inc1 := aos486Incarnar(t, store, vault, approvers, &conta)
				prod := goal
				prod.Credential = inc1.cred
				prod.MaxTurns = 1
				// O Runtime.Run directo não passa pelo `submit` do serviço: o ctx leva a lista
				// como o runCtx do serviço a levaria.
				if _, _, rerr := inc1.node.Runtime.Run(withRunToolAllowlist(ctx, goal.AllowedTools), prod, nil); rerr != nil && !errors.Is(rerr, agentruntime.ErrMaxTurnsExceeded) {
					t.Fatalf("turno 1 antes do crash: %v", rerr)
				}
				m, err := state.NewMachine(store, runID)
				if err != nil {
					t.Fatalf("NewMachine: %v", err)
				}
				if _, err := m.Rebuild(ctx); err != nil {
					t.Fatalf("Rebuild: %v", err)
				}
				if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1), Reason: "crash_simulado"}); err != nil {
					t.Fatalf("claim do crash simulado: %v", err)
				}
				// O registo de retoma é o que o hostRun de produção escreve no arranque.
				if err := inc1.node.ResumeRecords.Put(ctx, resumeRecordFromGoal(goal)); err != nil {
					t.Fatalf("semear o registo de retoma: %v", err)
				}
				_ = inc1.node.Close()

				// ===== INCARNAÇÃO 2: re-hospeda o run pela via em teste.
				inc2 := aos486Incarnar(t, store, vault, approvers, &conta)
				t.Cleanup(func() { _ = inc2.node.Close() })
				svc2 := aos486Servico(t, inc2.node)
				switch via {
				case "crash-resume":
					scanned, resumed, err := svc2.ResumeInterruptedRuns(ctx)
					if err != nil || scanned != 1 || resumed != 1 {
						t.Fatalf("a varredura devia ver 1 orfao e retomar 1: scanned=%d resumed=%d err=%v", scanned, resumed, err)
					}
				case "retoma":
					// O run finge-se suspenso nesta réplica (molde do AOS-263/AOS-440) e
					// retoma-se com uma credencial fresca do mesmo agente.
					svc2.mu.Lock()
					svc2.suspended[runID] = &runState{runID: runID, suspended: true, done: make(chan struct{})}
					svc2.mu.Unlock()
					if err := svc2.Resume(ctx, runID, inc2.cred); err != nil {
						t.Fatalf("Resume: %v", err)
					}
				}
				aos486Esperar(t, svc2, runID)

				// O turno 1 foi reproduzido da captura; só o turno 2 chegou ao modelo.
				vivos := inc2.gw.vistos()
				if len(vivos) != 1 {
					t.Fatalf("a re-hospedagem devia interrogar o modelo 1 vez (turno 2), interrogou %d", len(vivos))
				}
				if got := aos486NomesDoPedido(vivos[0]); !reflect.DeepEqual(got, l.querPedido) {
					t.Fatalf("turno 2 re-hospedado: schemas %v, quero %v", got, l.querPedido)
				}
				if !reflect.DeepEqual(vivos[0].Tools, pedidosRef[1].Tools) {
					t.Fatalf("turno 2 re-hospedado: os schemas não são os do run sem interrupção:\n veio: %+v\n quero: %+v", vivos[0].Tools, pedidosRef[1].Tools)
				}
				// A CAUDA de um dos quatro casos não é comparável, e não é pela oferta: o
				// crash-resume re-hospeda com a credencial vazia, e uma call NEGADA no turno 1
				// (lista vazia) não tem efeito aplicado para deduplicar — é re-mediada e sai
				// negada pelo hook de identidade, pelo que o resultado no tail muda de
				// `run_tool_allowlist` para `identity`. Aí compara-se o PREFIXO, que é onde a
				// oferta vive; nos outros três compara-se o prompt inteiro e o `prompt_hash`.
				caudaDiverge := via == "crash-resume" && l.querExecs == 0
				got, quer := vivos[0].Messages[0].Content, pedidosRef[1].Messages[0].Content
				if caudaDiverge {
					got, quer = aos486Prefixo(t, got), aos486Prefixo(t, quer)
				}
				if got != quer {
					t.Fatalf("turno 2 re-hospedado: o prompt não é o do run sem interrupção:\n veio: %s\n quero: %s", got, quer)
				}
				turnos := aos486TurnosDoRun(t, store, runID)
				if len(turnos) != 2 {
					t.Fatalf("o run re-hospedado tinha de ter 2 turnos gravados, tem %d", len(turnos))
				}
				for i := range turnos {
					nomes := turnos[i].tools
					if nomes == nil {
						nomes = []string{}
					}
					if !reflect.DeepEqual(nomes, l.querPrompt) {
						t.Fatalf("turno %d: manifest.tools %v, quero %v", i+1, nomes, l.querPrompt)
					}
					if caudaDiverge && i > 0 {
						continue
					}
					if turnos[i].promptHash != turnosRef[i].promptHash {
						t.Fatalf("turno %d: prompt_hash %s, o do run sem interrupção é %s", i+1, turnos[i].promptHash, turnosRef[i].promptHash)
					}
					if string(turnos[i].manifesto) != string(turnosRef[i].manifesto) {
						t.Fatalf("turno %d: manifesto diferente do do run sem interrupção:\n veio: %s\n quero: %s", i+1, turnos[i].manifesto, turnosRef[i].manifesto)
					}
				}
				// O efeito do turno 1 não repete na re-hospedagem, e só existe se a lista o admite.
				if got := atomic.LoadInt64(&conta); got != l.querExecs {
					t.Fatalf("counter executou %d vez(es), quero %d", got, l.querExecs)
				}
			})
		}
	}
}

// TestAOS486_ListaNoContexto fixa a travessia do contexto, que é onde a distinção entre «sem
// lista» e «lista vazia» se podia perder: nil não anexa nada (run sem restrição), e uma lista
// não-nil — MESMO VAZIA — anexa-se e sai do outro lado como restrição.
func TestAOS486_ListaNoContexto(t *testing.T) {
	base := context.Background()
	if _, restrito := runToolOfferFromContext(base); restrito {
		t.Fatal("um ctx sem lista tinha de sair como run sem restricao")
	}
	if ctx := withRunToolAllowlist(base, nil); ctx != base {
		t.Fatal("uma lista nil nao pode anexar nada ao ctx")
	}
	if _, restrito := runToolOfferFromContext(withRunToolAllowlist(base, nil)); restrito {
		t.Fatal("uma lista nil tinha de sair como run sem restricao")
	}

	permite, restrito := runToolOfferFromContext(withRunToolAllowlist(base, []string{}))
	if !restrito || permite == nil || permite("counter") {
		t.Fatalf("a lista VAZIA tinha de sair como restricao que nao admite nenhuma tool (restrito=%v)", restrito)
	}

	lista := []string{"counter"}
	ctx := withRunToolAllowlist(base, lista)
	lista[0] = "arquivo" // o chamador mexe na sua lista depois de a anexar
	permite, restrito = runToolOfferFromContext(ctx)
	if !restrito || !permite("counter") || permite("arquivo") {
		t.Fatal("a lista anexada tinha de admitir so o que la estava quando foi anexada")
	}

	// Um valor construído à mão com a lista nil continua a ser restrição, e não «sem lista».
	manual := context.WithValue(base, runToolAllowlistKey{}, runToolAllowlist{})
	if permite, restrito := runToolOfferFromContext(manual); !restrito || permite("counter") {
		t.Fatal("a presenca do valor no ctx e que marca a restricao, nao a nil-ness da lista")
	}
}
