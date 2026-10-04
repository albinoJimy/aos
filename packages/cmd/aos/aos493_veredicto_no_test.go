package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-493 — O VEREDICTO DE CONCLUSÃO NO NÓ COMPOSTO: a variável de ambiente, o selo terminal, os
// contadores do `/metrics`, e o contrato e o modo a sobreviverem à retoma.
//
// O contrato entra pelo `Goal` em código (o campo no `POST /runs` é do AOS-494).

// ---------------------------------------------------------------------------------------------
// A variável de ambiente
// ---------------------------------------------------------------------------------------------

func TestAOS493_ModoPorAmbiente_VocabularioFechado(t *testing.T) {
	for valor, quer := range map[string]agentruntime.CompletionMode{
		"":         agentruntime.CompletionObserve, // a omissão é observação
		"  ":       agentruntime.CompletionObserve,
		"observe":  agentruntime.CompletionObserve,
		"enforce":  agentruntime.CompletionEnforce,
		" enforce": agentruntime.CompletionEnforce,
		"off":      agentruntime.CompletionOff,
	} {
		t.Setenv("AOS_COMPLETION_VERDICT", valor)
		got, err := parseCompletionVerdictFromEnv()
		if err != nil || got != quer {
			t.Fatalf("AOS_COMPLETION_VERDICT=%q: modo=%q err=%v; quero %q", valor, got, err, quer)
		}
	}
	for _, valor := range []string{"Enforce", "OBSERVE", "on", "true", "1", "impor", "enforce,observe"} {
		t.Setenv("AOS_COMPLETION_VERDICT", valor)
		if _, err := parseCompletionVerdictFromEnv(); !errors.Is(err, ErrBadCompletionVerdict) {
			t.Fatalf("AOS_COMPLETION_VERDICT=%q tinha de recusar o arranque; veio %v", valor, err)
		}
		// E é o arranque do nó que falha, não só a função: a configuração por ambiente aborta.
		if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrBadCompletionVerdict) {
			t.Fatalf("nodeConfigFromEnv com AOS_COMPLETION_VERDICT=%q tinha de abortar com ErrBadCompletionVerdict; veio %v", valor, err)
		}
	}
	// Os três modos anunciam-se com textos diferentes.
	vistos := map[string]bool{}
	for _, m := range []agentruntime.CompletionMode{agentruntime.CompletionObserve, agentruntime.CompletionEnforce, agentruntime.CompletionOff} {
		vistos[completionVerdictBanner(m)] = true
	}
	if len(vistos) != 3 {
		t.Fatal("cada modo tem de ter o seu anuncio no arranque")
	}
}

func TestAOS493_ConfigEmCodigo_ValorDesconhecidoFalhaOBootstrap(t *testing.T) {
	cfg := tnBaseConfig()
	cfg.CompletionVerdict = "impor"
	if n, err := Bootstrap(context.Background(), cfg, io.Discard); !errors.Is(err, ErrBadCompletionVerdict) {
		if n != nil {
			_ = n.Close()
		}
		t.Fatalf("Config.CompletionVerdict fora do vocabulario tinha de falhar o Bootstrap; veio %v", err)
	}
	// Sem valor, o nó fica em observação.
	n, err := Bootstrap(context.Background(), tnBaseConfig(), io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	if n.completionVerdict != agentruntime.CompletionObserve {
		t.Fatalf("um no sem configuracao fica em observacao; ficou em %q", n.completionVerdict)
	}
	if got := n.fixarConclusao(agentruntime.Goal{}).CompletionMode; got != agentruntime.CompletionObserve {
		t.Fatalf("um run novo fica no modo do no; ficou em %q", got)
	}
	// Um Goal que já traz modo (o de uma retoma) não é tocado.
	if got := n.fixarConclusao(agentruntime.Goal{CompletionMode: agentruntime.CompletionOff}).CompletionMode; got != agentruntime.CompletionOff {
		t.Fatalf("o modo de um run retomado foi reescrito para %q", got)
	}
}

// ---------------------------------------------------------------------------------------------
// O selo terminal
// ---------------------------------------------------------------------------------------------

// aos493Transicao é a transição terminal como fica no log, com os campos do veredicto.
type aos493Transicao struct {
	From          string                `json:"from"`
	To            string                `json:"to"`
	Reason        string                `json:"reason"`
	OutcomeReason string                `json:"outcome_reason"`
	Verdict       *agentruntime.Verdict `json:"verdict"`
	cru           []byte
}

func aos493UltimaTransicao(t *testing.T, store state.EventStore, runID string) aos493Transicao {
	t.Helper()
	events, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read(%s): %v", runID, err)
	}
	var ultima *aos493Transicao
	for i := range events {
		if events[i].Type != state.EventTypeTransition {
			continue
		}
		tr := aos493Transicao{cru: events[i].Payload}
		if err := json.Unmarshal(events[i].Payload, &tr); err != nil {
			t.Fatalf("transicao ilegivel: %v", err)
		}
		ultima = &tr
	}
	if ultima == nil {
		t.Fatalf("o run %q nao tem transicoes no log", runID)
	}
	return *ultima
}

func aos493VeredictoNegativo(modo agentruntime.CompletionMode) *agentruntime.Verdict {
	return &agentruntime.Verdict{
		Mode: modo, Reason: agentruntime.OutcomeContractAfterDenial, ToolCallsRequested: 2,
		Tools: []agentruntime.ToolEvidence{{Tool: "doc_read", Requested: 2, Denied: 1, Failed: 1, Last: agentruntime.ToolOutcomeDenied}},
	}
}

func TestAOS493_SeloTerminal_LevaARazaoEOVector(t *testing.T) {
	ctx := context.Background()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	gates := newRunStateGates(es, nil, 0)
	abreEReclama := func(t *testing.T, runID string) *runGate {
		t.Helper()
		if err := gates.Open(ctx, runID, state.Uint64Token(1)); err != nil {
			t.Fatalf("gates.Open: %v", err)
		}
		gate := gates.resolveGate(runID)
		if gate == nil {
			t.Fatal("gate do run devia existir depois do Open")
		}
		if err := gate.claimRunning(ctx); err != nil {
			t.Fatalf("claimRunning: %v", err)
		}
		return gate
	}

	t.Run("imposto: failed com razao propria", func(t *testing.T) {
		const run = "run-493-selo-imposto"
		gate := abreEReclama(t, run)
		v := aos493VeredictoNegativo(agentruntime.CompletionEnforce)
		selado, err := gate.sealTerminal(ctx, agentruntime.Result{RunID: run, Turns: 2, Unfulfilled: true, Verdict: v}, nil, false)
		if err != nil || selado != state.Failed {
			t.Fatalf("um run nao cumprido sela failed; selou %q (err=%v)", selado, err)
		}
		tr := aos493UltimaTransicao(t, es, run)
		if tr.From != "running" || tr.To != "failed" || tr.Reason != reasonRunUnfulfilled {
			t.Fatalf("transicao = %s→%s (%s); quero running→failed (%s)", tr.From, tr.To, tr.Reason, reasonRunUnfulfilled)
		}
		if tr.OutcomeReason != string(agentruntime.OutcomeContractAfterDenial) || !reflect.DeepEqual(tr.Verdict, v) {
			t.Fatalf("o evento nao leva a razao e o vector: %s", tr.cru)
		}
	})

	t.Run("observado: complete com o veredicto registado", func(t *testing.T) {
		const run = "run-493-selo-observado"
		gate := abreEReclama(t, run)
		v := aos493VeredictoNegativo(agentruntime.CompletionObserve)
		selado, err := gate.sealTerminal(ctx, agentruntime.Result{RunID: run, Turns: 2, Terminated: true, FinalText: "x", Verdict: v}, nil, false)
		if err != nil || selado != state.Complete {
			t.Fatalf("em observacao o desfecho nao muda: selou %q (err=%v)", selado, err)
		}
		tr := aos493UltimaTransicao(t, es, run)
		if tr.To != "complete" || tr.Reason != reasonRunComplete {
			t.Fatalf("transicao = →%s (%s); quero complete (%s)", tr.To, tr.Reason, reasonRunComplete)
		}
		if tr.OutcomeReason != string(agentruntime.OutcomeContractAfterDenial) || !reflect.DeepEqual(tr.Verdict, v) {
			t.Fatalf("em observacao o veredicto tem de ficar registado no evento: %s", tr.cru)
		}
	})

	t.Run("sem veredicto: os bytes de antes", func(t *testing.T) {
		const run = "run-493-selo-sem-veredicto"
		gate := abreEReclama(t, run)
		if _, err := gate.sealTerminal(ctx, agentruntime.Result{RunID: run, Terminated: true}, nil, false); err != nil {
			t.Fatalf("sealTerminal: %v", err)
		}
		tr := aos493UltimaTransicao(t, es, run)
		if bytes.Contains(tr.cru, []byte("outcome_reason")) || bytes.Contains(tr.cru, []byte("verdict")) {
			t.Fatalf("uma transicao sem veredicto ganhou campos novos: %s", tr.cru)
		}
	})

	t.Run("erro de loop: nao leva veredicto", func(t *testing.T) {
		const run = "run-493-selo-erro"
		gate := abreEReclama(t, run)
		res := agentruntime.Result{RunID: run, Verdict: aos493VeredictoNegativo(agentruntime.CompletionEnforce)}
		selado, err := gate.sealTerminal(ctx, res, errors.New("falha do loop"), false)
		if err != nil || selado != state.Failed {
			t.Fatalf("selou %q (err=%v)", selado, err)
		}
		tr := aos493UltimaTransicao(t, es, run)
		if tr.Reason != reasonRunFailed || bytes.Contains(tr.cru, []byte("verdict")) {
			t.Fatalf("um erro de loop sela run_failed e sem veredicto: %s", tr.cru)
		}
	})
}

// ---------------------------------------------------------------------------------------------
// As duas respostas de produção, pelo nó composto
// ---------------------------------------------------------------------------------------------

// aos493RespostasDeProducao lê as fixtures que o kernel também usa.
func aos493RespostasDeProducao(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join("..", "..", "kernel", "agent-runtime", "replay", "testdata", "aos493_producao")
	out := map[string]string{}
	for _, plano := range []string{"plan-e2e-v0145-1791115918", "plan-e2e-v0145-1791117087"} {
		raw, err := os.ReadFile(filepath.Join(dir, plano+".txt"))
		if err != nil || len(bytes.TrimSpace(raw)) == 0 {
			t.Fatalf("fixture de producao %s: err=%v bytes=%d", plano, err, len(raw))
		}
		out[plano] = string(raw)
	}
	return out
}

func aos493Esperar(t *testing.T, svc *NodeService, runID string) RunOutcome {
	t.Helper()
	wc, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	oc, ok, err := svc.Wait(wc, runID)
	if err != nil || !ok {
		t.Fatalf("Wait(%s): ok=%v err=%v", runID, ok, err)
	}
	if oc.Err != nil {
		t.Fatalf("o run %s acabou com erro de loop: %v", runID, oc.Err)
	}
	return oc
}

// TestAOS493_No_RespostasDeProducao: cada uma das duas respostas de 2026-10-04, num run cujo
// contrato exige a tool que o modelo escreveu como texto. Em imposição o run termina `failed`
// com a razão «sem nenhuma chamada pedida» e sem texto final; em observação termina como hoje,
// com o veredicto no evento, no log e nos contadores.
func TestAOS493_No_RespostasDeProducao(t *testing.T) {
	i := 0
	for plano, texto := range aos493RespostasDeProducao(t) {
		for _, modo := range []agentruntime.CompletionMode{agentruntime.CompletionObserve, agentruntime.CompletionEnforce} {
			i++
			runID := "run-493-producao-" + string(rune('a'+i))
			t.Run(plano+"/"+string(modo), func(t *testing.T) {
				pinBreakerEnv(t, "0", "0", "0", "0")
				ctx := context.Background()
				store, err := eventstore.New()
				if err != nil {
					t.Fatalf("eventstore.New: %v", err)
				}
				approvers := crashResumeApprovers(t)
				modelo := agentruntime.ModelClientFunc(func(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
					return agentruntime.ModelResponse{Text: texto, Final: true, StopReason: agentruntime.StopStop, Usage: agentruntime.Usage{InputTokens: 9, OutputTokens: 4}}, nil
				})
				node, cred := obsPermitNodeWith(t, "", modelo, func(cfg *Config) {
					cfg.EventStore = store
					cfg.DSARVault = audit.NewInMemoryKeyVault(nil)
					cfg.DurableExecution = true
					cfg.Approvers = approvers
					cfg.CompletionVerdict = modo
				})
				t.Cleanup(func() { _ = node.Close() })
				registo := &syncBuf{}
				svc, err := NewNodeService(node, WithDeadlineSweepInterval(0), WithServiceLog(registo))
				if err != nil {
					t.Fatalf("NewNodeService: %v", err)
				}
				t.Cleanup(func() {
					sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					_ = svc.Shutdown(sc)
				})

				goal := agentruntime.Goal{
					RunID:              runID,
					Principal:          referencemonitor.Principal{NHIID: durAgent},
					Credential:         cred,
					Objective:          "Le o documento notes e devolve o conteudo",
					MaxTurns:           3,
					CompletionRequires: []string{"counter"},
				}
				if err := svc.Submit(ctx, goal); err != nil {
					t.Fatalf("Submit: %v", err)
				}
				oc := aos493Esperar(t, svc, runID)
				res := oc.Result
				imposto := modo == agentruntime.CompletionEnforce

				quer := &agentruntime.Verdict{
					Mode: modo, Reason: agentruntime.OutcomeContractNoCall,
					Tools: []agentruntime.ToolEvidence{{Tool: "counter"}},
				}
				if !reflect.DeepEqual(res.Verdict, quer) {
					t.Fatalf("veredicto = %+v, quero %+v", res.Verdict, quer)
				}
				if res.ToolsOffered == 0 {
					t.Fatal("o run tinha de ter tools na oferta: e o cenario do verde falso")
				}
				tr := aos493UltimaTransicao(t, store, runID)
				if tr.OutcomeReason != string(agentruntime.OutcomeContractNoCall) || !reflect.DeepEqual(tr.Verdict, quer) {
					t.Fatalf("a transicao terminal nao leva a razao e o vector: %s", tr.cru)
				}
				registo.mu.Lock()
				linhas := registo.b.String()
				registo.mu.Unlock()

				if imposto {
					if !res.Unfulfilled || res.Terminated || res.FinalText != "" {
						t.Fatalf("em imposicao o run nao cumpre e nao tem texto final: %+v", res)
					}
					if tr.To != "failed" || tr.Reason != reasonRunUnfulfilled {
						t.Fatalf("estado duravel = %s (%s); quero failed (%s)", tr.To, tr.Reason, reasonRunUnfulfilled)
					}
					if svc.desfechos.lido(state.Failed, agentruntime.OutcomeContractNoCall) != 1 || svc.desfechos.semToolCall.Load() != 0 {
						t.Fatalf("contadores: failed/no_call=%d sem_tool_call=%d; queria 1 e 0",
							svc.desfechos.lido(state.Failed, agentruntime.OutcomeContractNoCall), svc.desfechos.semToolCall.Load())
					}
					if !strings.Contains(linhas, `run "`+runID+`" NAO CUMPRIDO (AOS-493)`) {
						t.Fatalf("o run nao cumprido tem de se dizer no log:\n%s", linhas)
					}
				} else {
					if res.Unfulfilled || !res.Terminated || res.FinalText != texto {
						t.Fatalf("em observacao o desfecho e o de hoje: %+v", res)
					}
					if tr.To != "complete" || tr.Reason != reasonRunComplete {
						t.Fatalf("estado duravel = %s (%s); em observacao quero complete (%s)", tr.To, tr.Reason, reasonRunComplete)
					}
					if svc.desfechos.lido(state.Complete, agentruntime.OutcomeContractNoCall) != 1 || svc.desfechos.semToolCall.Load() != 1 {
						t.Fatalf("contadores: complete/no_call=%d sem_tool_call=%d; queria 1 e 1",
							svc.desfechos.lido(state.Complete, agentruntime.OutcomeContractNoCall), svc.desfechos.semToolCall.Load())
					}
					if svc.desfechos.lido(state.Failed, agentruntime.OutcomeContractNoCall) != 0 {
						t.Fatal("em observacao nenhum run pode contar como failed pelo veredicto")
					}
					if !strings.Contains(linhas, `run "`+runID+`" concluido com VEREDICTO NEGATIVO em observacao (AOS-493)`) {
						t.Fatalf("o veredicto observado tem de se dizer no log:\n%s", linhas)
					}
				}

				// O registo de retoma e o manifesto do turno guardam o contrato e o modo.
				rec, ok, err := node.ResumeRecords.Get(ctx, runID)
				if err != nil || !ok || rec.CompletionMode != modo || !reflect.DeepEqual(rec.CompletionRequires, []string{"counter"}) {
					t.Fatalf("registo de retoma: modo=%q contrato=%v ok=%v err=%v", rec.CompletionMode, rec.CompletionRequires, ok, err)
				}
				turnos := aos486TurnosDoRun(t, store, runID)
				if len(turnos) != 1 {
					t.Fatalf("queria 1 turno gravado, vieram %d", len(turnos))
				}
				var m agentruntime.Manifest
				if err := json.Unmarshal(turnos[0].manifesto, &m); err != nil {
					t.Fatalf("manifesto ilegivel: %v", err)
				}
				if m.Completion == nil || m.Completion.Mode != modo || !reflect.DeepEqual(m.Completion.Requires, []string{"counter"}) {
					t.Fatalf("manifest.completion = %+v", m.Completion)
				}

				// O `/metrics` publica a família, com uma amostra por par dos dois vocabulários.
				corpo := metricasDe(t, &apiHandler{node: node, svc: svc})
				estado := map[bool]string{true: "failed", false: "complete"}[imposto]
				if quero := `aos_runs_finished_total{outcome="` + estado + `",reason="contract_unmet_no_call"} 1`; !strings.Contains(corpo, quero+"\n") {
					t.Fatalf("faltou %q:\n%s", quero, amostrasDe(corpo, "aos_runs_finished_total"))
				}
				if n := strings.Count(corpo, "aos_runs_finished_total{"); n != 18 {
					t.Fatalf("a familia tem %d amostras; queria 3 estados x 6 razoes", n)
				}
				semTool := map[bool]string{true: "0", false: "1"}[imposto]
				if quero := "aos_runs_completed_without_tool_call_total " + semTool; !strings.Contains(corpo, quero+"\n") {
					t.Fatalf("faltou %q:\n%s", quero, amostrasDe(corpo, "aos_runs_completed_without_tool_call_total"))
				}
			})
		}
	}
}

// ---------------------------------------------------------------------------------------------
// A retoma preserva o contrato, o modo e os contadores
// ---------------------------------------------------------------------------------------------

// TestAOS493_RetomaPreservaContratoModoEContadores: um run que dá o turno 1 (a tool call
// `counter`, efectiva), morre, e é re-hospedado por outra incarnação do nó.
//
//   - o contrato e o modo vêm do registo de retoma, e não do nó que retoma (que aqui está
//     configurado com OUTRO modo);
//   - os contadores refazem-se: o turno 1 é reproduzido e a chamada efectiva volta a contar,
//     sem a tool voltar a executar;
//   - um registo escrito por um binário anterior (sem modo) é retomado sem veredicto.
func TestAOS493_RetomaPreservaContratoModoEContadores(t *testing.T) {
	casos := []struct {
		nome     string
		contrato []string
		// modoDoRun é o modo com que o run começou; vazio ⇒ registo de um binário anterior.
		modoDoRun agentruntime.CompletionMode
		// modoDoNo é o modo do nó que retoma — deliberadamente outro.
		modoDoNo agentruntime.CompletionMode
		razao    agentruntime.OutcomeReason
		vector   []agentruntime.ToolEvidence
	}{
		{
			nome: "contrato cumprido pelo turno reproduzido", contrato: []string{"counter"},
			modoDoRun: agentruntime.CompletionEnforce, modoDoNo: agentruntime.CompletionOff,
			vector: []agentruntime.ToolEvidence{{Tool: "counter", Requested: 1, Effective: 1, Last: agentruntime.ToolOutcomeEffective}},
		},
		{
			nome: "contrato por cumprir continua imposto num no em observacao", contrato: []string{"beta"},
			modoDoRun: agentruntime.CompletionEnforce, modoDoNo: agentruntime.CompletionObserve,
			razao:  agentruntime.OutcomeContractNoCall,
			vector: []agentruntime.ToolEvidence{{Tool: "beta"}},
		},
		{
			nome: "registo anterior ao AOS-493 retoma sem veredicto num no que impoe", contrato: nil,
			modoDoRun: "", modoDoNo: agentruntime.CompletionEnforce,
		},
	}
	for _, c := range casos {
		for _, via := range []string{"crash-resume", "retoma"} {
			t.Run(c.nome+"/"+via, func(t *testing.T) {
				pinBreakerEnv(t, "0", "0", "0", "0")
				ctx := context.Background()
				const runID = "plan-493~retomado"
				approvers := crashResumeApprovers(t)
				goal := agentruntime.Goal{
					RunID:              runID,
					Principal:          referencemonitor.Principal{NHIID: durAgent},
					Model:              agentruntime.ModelConfig{ModelID: "modelo-486"},
					Objective:          "o trabalho de um no do plano",
					MaxTurns:           4,
					AllowedTools:       []string{"beta", "counter"},
					CompletionRequires: c.contrato,
					CompletionMode:     c.modoDoRun,
				}

				// ===== INCARNAÇÃO 1: dá o turno 1 e "crasha" (molde do AOS-253/AOS-489).
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
				if _, _, rerr := inc1.node.Runtime.Run(withRunToolAllowlist(ctx, goal.AllowedTools), prod, nil); !errors.Is(rerr, agentruntime.ErrMaxTurnsExceeded) {
					t.Fatalf("turno 1 antes do crash: %v", rerr)
				}
				if conta != 1 {
					t.Fatalf("o turno 1 tinha de executar a tool uma vez; executou %d", conta)
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
				rec := resumeRecordFromGoal(goal)
				if c.modoDoRun == "" {
					rec = aos493RegistoComoOBinarioAntigo(t, goal)
				} else if rec.CompletionMode != c.modoDoRun || !reflect.DeepEqual(rec.CompletionRequires, c.contrato) {
					t.Fatalf("o registo de retoma nao guardou o contrato e o modo: %+v", rec)
				}
				if err := inc1.node.ResumeRecords.Put(ctx, rec); err != nil {
					t.Fatalf("semear o registo de retoma: %v", err)
				}
				_ = inc1.node.Close()

				// ===== INCARNAÇÃO 2: outro nó, com OUTRO modo configurado.
				inc2 := aos486Incarnar(t, store, vault, approvers, &conta)
				t.Cleanup(func() { _ = inc2.node.Close() })
				inc2.node.completionVerdict = c.modoDoNo
				svc2 := aos486Servico(t, inc2.node)
				switch via {
				case "crash-resume":
					scanned, resumed, err := svc2.ResumeInterruptedRuns(ctx)
					if err != nil || scanned != 1 || resumed != 1 {
						t.Fatalf("a varredura devia ver 1 orfao e retomar 1: scanned=%d resumed=%d err=%v", scanned, resumed, err)
					}
				case "retoma":
					svc2.mu.Lock()
					svc2.suspended[runID] = &runState{runID: runID, suspended: true, done: make(chan struct{})}
					svc2.mu.Unlock()
					if err := svc2.Resume(ctx, runID, inc2.cred); err != nil {
						t.Fatalf("Resume: %v", err)
					}
				}
				res := aos493Esperar(t, svc2, runID).Result

				// A tool NÃO voltou a executar: o turno 1 foi reproduzido, e é dessa reprodução
				// que os contadores saem.
				if conta != 1 {
					t.Fatalf("a re-hospedagem voltou a executar a tool (%d execucoes)", conta)
				}
				if len(inc2.gw.vistos()) != 1 {
					t.Fatalf("a re-hospedagem devia interrogar o modelo 1 vez (turno 2), interrogou %d", len(inc2.gw.vistos()))
				}

				if c.modoDoRun == "" {
					if res.Verdict != nil || !res.Terminated || res.Unfulfilled {
						t.Fatalf("um run anterior ao AOS-493 e retomado sem veredicto, mesmo num no que impoe: %+v", res)
					}
					for _, tr := range aos486TurnosDoRun(t, store, runID) {
						if bytes.Contains(tr.manifesto, []byte("completion")) {
							t.Fatalf("o manifesto de um run anterior ganhou o campo do veredicto: %s", tr.manifesto)
						}
					}
					return
				}
				quer := &agentruntime.Verdict{
					Mode: c.modoDoRun, Fulfilled: c.razao == agentruntime.OutcomeFulfilled, Reason: c.razao,
					Tools: c.vector, ToolCallsRequested: 1,
				}
				if !reflect.DeepEqual(res.Verdict, quer) {
					t.Fatalf("veredicto depois da retoma = %+v, quero %+v", res.Verdict, quer)
				}
				negativo := c.razao != agentruntime.OutcomeFulfilled
				if res.Unfulfilled != negativo || res.Terminated == negativo {
					t.Fatalf("desfecho depois da retoma: Unfulfilled=%v Terminated=%v; o modo do run e %s", res.Unfulfilled, res.Terminated, c.modoDoRun)
				}
				tr := aos493UltimaTransicao(t, store, runID)
				estado := map[bool]string{true: "failed", false: "complete"}[negativo]
				if tr.To != estado || !reflect.DeepEqual(tr.Verdict, quer) {
					t.Fatalf("transicao terminal = →%s com %s; quero %s", tr.To, tr.cru, estado)
				}
				// Os dois turnos gravaram o modo e o contrato do RUN, não os do nó que retomou.
				for i, t2 := range aos486TurnosDoRun(t, store, runID) {
					var mf agentruntime.Manifest
					if err := json.Unmarshal(t2.manifesto, &mf); err != nil {
						t.Fatalf("manifesto ilegivel: %v", err)
					}
					if mf.Completion == nil || mf.Completion.Mode != c.modoDoRun || !reflect.DeepEqual(mf.Completion.Requires, c.contrato) {
						t.Fatalf("turno %d: manifest.completion = %+v", i+1, mf.Completion)
					}
				}
			})
		}
	}
}

// aos493RegistoComoOBinarioAntigo devolve o registo de retoma tal como um binário ANTERIOR ao
// AOS-493 o escreveu: sem contrato e sem modo. Confirma que a serialização os omite.
func aos493RegistoComoOBinarioAntigo(t *testing.T, goal agentruntime.Goal) integration.ResumeRecord {
	t.Helper()
	rec := resumeRecordFromGoal(goal)
	rec.CompletionMode = ""
	rec.CompletionRequires = nil
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(raw), "Completion") {
		t.Fatalf("um registo sem contrato nem modo tem de serializar SEM os campos (omitempty): %s", raw)
	}
	if rec.ModoDeConclusao() != agentruntime.CompletionOff || rec.GoalWith("").CompletionMode != agentruntime.CompletionOff {
		t.Fatal("um registo sem modo e um run sem veredicto: o Goal de retoma tem de trazer off, nunca vazio")
	}
	return rec
}

// Um registo NOVO leva sempre o modo explícito — mesmo que o Goal ainda não o tenha fixado.
func TestAOS493_RegistoDeRetoma_ModoSempreExplicito(t *testing.T) {
	rec := resumeRecordFromGoal(agentruntime.Goal{RunID: "r", CompletionRequires: []string{"doc_read"}})
	if rec.CompletionMode != agentruntime.CompletionOff || !reflect.DeepEqual(rec.CompletionRequires, []string{"doc_read"}) {
		t.Fatalf("registo = modo %q, contrato %v", rec.CompletionMode, rec.CompletionRequires)
	}
	g := rec.GoalWith("cred")
	if g.CompletionMode != agentruntime.CompletionOff || !reflect.DeepEqual(g.CompletionRequires, []string{"doc_read"}) {
		t.Fatalf("Goal de retoma = modo %q, contrato %v", g.CompletionMode, g.CompletionRequires)
	}
}
