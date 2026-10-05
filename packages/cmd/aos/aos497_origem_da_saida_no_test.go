package main

// AOS-497 — A ORIGEM DA SAÍDA, PELO NÓ COMPOSTO.
//
// A regra de designação é do kernel e está fixada lá (`aos497_origem_da_saida_test.go` e o
// diferencial do pacote `replay`). Aqui prova-se o que só o nó composto mostra: a âncora chega à
// transição terminal pelo selo do nó, na via durável; conta-se no `/metrics`; uma declaração «só
// medição» não muda o desfecho de um run com o nó em imposição; e a declaração sobrevive a um
// crash, com o digest da chamada da primeira vida.
//
// Nenhum chamador declara a origem pela API (o campo no `POST /runs` é do ticket seguinte): os
// Goals destes testes são compostos em código.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
)

func aos497Digest(valor string) string {
	soma := sha256.Sum256([]byte(valor))
	return "sha256:" + hex.EncodeToString(soma[:])
}

// aos497AncoraDoLog lê a âncora da última transição do run, como fica no log.
func aos497AncoraDoLog(t *testing.T, store state.EventStore, runID string) (aos493Transicao, *agentruntime.OutputSource) {
	t.Helper()
	tr := aos493UltimaTransicao(t, store, runID)
	var corpo struct {
		OutputSource *agentruntime.OutputSource `json:"output_source"`
	}
	if err := json.Unmarshal(tr.cru, &corpo); err != nil {
		t.Fatalf("transicao ilegivel: %v", err)
	}
	return tr, corpo.OutputSource
}

// TestAOS497_No_AncoraNoSeloENasMetricas: os guiões × dois vínculos, com o nó em IMPOSIÇÃO —
// o modo de produção. «Só medição» conclui sempre como o run sem declaração; «vinculativa»
// fecha `failed` quando a origem não é designável. A âncora é a mesma nos dois.
//
// Os dois últimos guiões são runs com ENTRADAS (um `plan_input`; memória): o contexto do turno 1
// já é untrusted e a âncora leva o estado próprio `inapplicable`, que a métrica conta à parte de
// `missing`. Vinculada em imposição fecha com a razão `output_source_missing`.
func TestAOS497_No_AncoraNoSeloENasMetricas(t *testing.T) {
	leitura := agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{aos493Counter("tick")}, StopReason: agentruntime.StopToolCalls}
	duas := agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{aos493Counter("tick"), aos493Counter("tock")}, StopReason: agentruntime.StopToolCalls}
	fim := agentruntime.ModelResponse{Text: "um resumo", StopReason: agentruntime.StopStop}
	material := []byte("material de outro no")
	entradas := []agentruntime.PlanInput{{From: "leitura", Output: "notas", Digest: aos497Digest(string(material)), Content: material}}
	for i, c := range []struct {
		nome    string
		guiao   []agentruntime.ModelResponse
		inputs  []agentruntime.PlanInput
		memoria []byte
		estado  agentruntime.OutputSourceState
		razao   agentruntime.OutcomeReason
	}{
		{"designada", []agentruntime.ModelResponse{leitura, fim}, nil, nil, agentruntime.OutputSourceDesignated, agentruntime.OutcomeFulfilled},
		{"em falta", []agentruntime.ModelResponse{fim}, nil, nil, agentruntime.OutputSourceMissing, agentruntime.OutcomeOutputSourceMissing},
		{"ambigua", []agentruntime.ModelResponse{duas, fim}, nil, nil, agentruntime.OutputSourceAmbiguous, agentruntime.OutcomeOutputSourceAmbiguous},
		{"nao aplicavel, plan_input", []agentruntime.ModelResponse{leitura, fim}, entradas, nil, agentruntime.OutputSourceInapplicable, agentruntime.OutcomeOutputSourceMissing},
		{"nao aplicavel, memoria", []agentruntime.ModelResponse{leitura, fim}, nil, []byte("uma memoria qualquer"), agentruntime.OutputSourceInapplicable, agentruntime.OutcomeOutputSourceMissing},
	} {
		for k, vinculo := range agentruntime.OutputSourceBindings() {
			t.Run(c.nome+"/"+string(vinculo), func(t *testing.T) {
				store := aos493Store(t)
				n := aos493Compor(t, agentruntime.CompletionEnforce, aos493Guiao(nil, c.guiao...), store)
				runID := "run-497-no-" + string(rune('a'+i)) + string(rune('a'+k))
				oc := aos493Desfecho(t, n, agentruntime.Goal{
					RunID: runID, Objective: "le o documento", MaxTurns: 4,
					Inputs: c.inputs, MemoryContext: c.memoria,
					OutputFromTool: "counter", OutputSourceBinding: vinculo,
				})
				res := oc.Result
				if oc.Err != nil {
					t.Fatalf("erro de loop: %v", oc.Err)
				}
				falha := vinculo == agentruntime.OutputSourceBinds && c.razao != agentruntime.OutcomeFulfilled

				// (1) A âncora no Result.
				if res.OutputSource == nil || res.OutputSource.State != c.estado || res.OutputSource.Binding != vinculo || res.OutputSource.Tool != "counter" {
					t.Fatalf("ancora no Result = %+v; quero counter, %s, %s", res.OutputSource, vinculo, c.estado)
				}
				if c.estado == agentruntime.OutputSourceDesignated {
					if res.OutputSource.Digest != aos497Digest("pong") || res.OutputSource.Bytes != len("pong") {
						t.Fatalf("o digest tem de ser o do que a counter devolveu (\"pong\"): %+v", res.OutputSource)
					}
					// O passo designado é o de uma mediação deste run.
					if !aos497PassoMediado(t, store, runID, res.OutputSource.StepID) {
						t.Fatalf("o passo designado %q nao e o de nenhuma mediacao do run", res.OutputSource.StepID)
					}
				}

				// (2) O desfecho. Só «vinculativa» com origem não designável falha.
				if res.Unfulfilled != falha || res.Terminated == falha {
					t.Fatalf("desfecho: Unfulfilled=%v Terminated=%v; quero falha=%v (vinculo %s, estado %s)", res.Unfulfilled, res.Terminated, falha, vinculo, c.estado)
				}
				querRazao := agentruntime.OutcomeFulfilled
				if falha {
					querRazao = c.razao
				}
				if res.Verdict == nil || res.Verdict.Reason != querRazao {
					t.Fatalf("veredicto = %+v; quero a razao %q", res.Verdict, querRazao)
				}
				if !falha && res.FinalText != "um resumo" {
					t.Fatalf("o texto final continua a existir: %q", res.FinalText)
				}

				// (3) A âncora SELADA na transição terminal, igual à do Result, e sem conteúdo.
				tr, selada := aos497AncoraDoLog(t, store, runID)
				if !reflect.DeepEqual(selada, res.OutputSource) {
					t.Fatalf("ancora selada = %+v; a do Result e %+v", selada, res.OutputSource)
				}
				estado := map[bool]string{true: "failed", false: "complete"}[falha]
				if tr.To != estado || tr.OutcomeReason != string(querRazao) {
					t.Fatalf("transicao terminal = %s; quero →%s com outcome_reason %q", tr.cru, estado, querRazao)
				}
				if bytes.Contains(tr.cru, []byte("pong")) || bytes.Contains(tr.cru, []byte("resumo")) {
					t.Fatalf("a transicao terminal leva conteudo: %s", tr.cru)
				}
				// E lê-se de volta pela máquina de estados, do mesmo evento.
				m, err := state.NewMachine(store, runID)
				if err != nil {
					t.Fatal(err)
				}
				if _, doLog, err := m.RebuildOutcome(context.Background()); err != nil || !reflect.DeepEqual(doLog.OutputSource, res.OutputSource) {
					t.Fatalf("RebuildOutcome: ancora=%+v err=%v", doLog.OutputSource, err)
				}

				// (4) A métrica: uma amostra por par, e só a deste run a 1.
				corpo := metricasDe(t, &apiHandler{node: n.node, svc: n.svc})
				if quero := `aos_runs_output_source_total{binding="` + string(vinculo) + `",state="` + string(c.estado) + `"} 1`; !strings.Contains(corpo, quero+"\n") {
					t.Fatalf("faltou %q:\n%s", quero, amostrasDe(corpo, "aos_runs_output_source_total"))
				}
				if got := strings.Count(corpo, "aos_runs_output_source_total{"); got != 8 {
					t.Fatalf("queria 8 amostras (2 vinculos x 4 estados), vieram %d", got)
				}
				uns := 0
				for _, l := range strings.Split(corpo, "\n") {
					if strings.HasPrefix(l, "aos_runs_output_source_total{") && !strings.HasSuffix(l, " 0") {
						uns++
					}
				}
				if uns != 1 {
					t.Fatalf("so a amostra deste run podia ser diferente de zero:\n%s", amostrasDe(corpo, "aos_runs_output_source_total"))
				}
			})
		}
	}
}

// aos497PassoMediado diz se o passo é o `step_id` de um evento do run que não é um turno.
func aos497PassoMediado(t *testing.T, store state.EventStore, runID, passo string) bool {
	t.Helper()
	evs, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, ev := range evs {
		if ev.StepID == passo && ev.Type != agentruntime.EventTypeTurnRecorded {
			return true
		}
	}
	return false
}

// TestAOS497_No_SoMedicaoEmImposicao_ODesfechoEODeSemDeclaracao: o caso que o ticket seguinte
// precisa para medir em produção. O MESMO guião (o modelo não chama a tool), com o nó em
// imposição: sem declaração e com declaração «só medição» dão o mesmo `Result` e a mesma
// transição terminal, salvo a âncora — que diz «em falta».
func TestAOS497_No_SoMedicaoEmImposicao_ODesfechoEODeSemDeclaracao(t *testing.T) {
	fim := agentruntime.ModelResponse{Text: "um resumo", StopReason: agentruntime.StopStop}
	correr := func(runID, origem string, vinculo agentruntime.OutputSourceBinding) (agentruntime.Result, aos493Transicao, aos493No) {
		store := aos493Store(t)
		n := aos493Compor(t, agentruntime.CompletionEnforce, aos493Guiao(nil, fim), store)
		oc := aos493Desfecho(t, n, agentruntime.Goal{RunID: runID, Objective: "le o documento", MaxTurns: 4, OutputFromTool: origem, OutputSourceBinding: vinculo})
		if oc.Err != nil {
			t.Fatalf("erro de loop: %v", oc.Err)
		}
		return oc.Result, aos493UltimaTransicao(t, store, runID), n
	}
	sem, trSem, nSem := correr("run-497-medicao", "", "")
	com, trCom, nCom := correr("run-497-medicao", "counter", agentruntime.OutputSourceMeasure)

	quer := &agentruntime.OutputSource{Tool: "counter", Binding: agentruntime.OutputSourceMeasure, State: agentruntime.OutputSourceMissing}
	if !reflect.DeepEqual(com.OutputSource, quer) || sem.OutputSource != nil {
		t.Fatalf("ancoras: com=%+v sem=%+v; quero %+v e nenhuma", com.OutputSource, sem.OutputSource, quer)
	}
	// O Result é o mesmo, salvo a âncora e os seq dos eventos.
	semAncora := com
	semAncora.OutputSource = nil
	semAncora.TurnSeqs, sem.TurnSeqs = nil, nil
	if !reflect.DeepEqual(semAncora, sem) {
		t.Fatalf("«so medicao» mudou o desfecho do run:\n  com %+v\n  sem %+v", semAncora, sem)
	}
	if !com.Terminated || com.Unfulfilled || com.FinalText != "um resumo" || com.Verdict == nil || !com.Verdict.Fulfilled {
		t.Fatalf("o run em «so medicao» tinha de concluir como sempre: %+v (veredicto %+v)", com, com.Verdict)
	}
	// A transição terminal é a mesma, byte a byte, depois de tirar a âncora (e o instante).
	const ancora = `,"output_source":{"tool":"counter","binding":"measure","state":"missing"}`
	if !bytes.Contains(trCom.cru, []byte(ancora)) || bytes.Contains(trSem.cru, []byte("output_source")) {
		t.Fatalf("transicoes:\n  com %s\n  sem %s", trCom.cru, trSem.cru)
	}
	semInstante := func(raw []byte) string {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		delete(m, "at")
		delete(m, "output_source")
		out, _ := json.Marshal(m)
		return string(out)
	}
	if semInstante(trCom.cru) != semInstante(trSem.cru) {
		t.Fatalf("a transicao terminal mudou para alem da ancora:\n  com %s\n  sem %s", trCom.cru, trSem.cru)
	}
	// As métricas de desfecho contam os dois da mesma maneira; a da origem só conta o declarado.
	mSem, mCom := metricasDe(t, &apiHandler{node: nSem.node, svc: nSem.svc}), metricasDe(t, &apiHandler{node: nCom.node, svc: nCom.svc})
	if a, b := amostrasDe(mSem, "aos_runs_finished_total"), amostrasDe(mCom, "aos_runs_finished_total"); a != b || !strings.Contains(a, `{outcome="complete",reason="none"} 1`) {
		t.Fatalf("aos_runs_finished_total difere entre os dois runs:\n%s\n---\n%s", a, b)
	}
	if !strings.Contains(mCom, `aos_runs_output_source_total{binding="measure",state="missing"} 1`+"\n") {
		t.Fatalf("a medicao nao contou:\n%s", amostrasDe(mCom, "aos_runs_output_source_total"))
	}
	if strings.Contains(amostrasDe(mSem, "aos_runs_output_source_total"), " 1") {
		t.Fatalf("um run sem declaracao contou na metrica da origem:\n%s", amostrasDe(mSem, "aos_runs_output_source_total"))
	}
}

// TestAOS497_No_DeclaracaoImpossivel: a tool declarada não está no tool set definitivo do run
// (o que o nó compõe do catálogo e da lista-branca). O run não arranca, nos dois vínculos, e o
// nó sela-o como um erro de loop — sem âncora.
func TestAOS497_No_DeclaracaoImpossivel(t *testing.T) {
	for i, c := range []struct {
		nome       string
		origem     string
		vinculo    agentruntime.OutputSourceBinding
		permitidas []string
		erro       error
	}{
		{"fora do catalogo, so medicao", "doc_read", agentruntime.OutputSourceMeasure, nil, agentruntime.ErrImpossibleOutputSource},
		{"fora da lista-branca, vinculativa", "counter", agentruntime.OutputSourceBinds, []string{"beta"}, agentruntime.ErrImpossibleOutputSource},
		{"origem sem vinculo", "counter", "", nil, agentruntime.ErrBadOutputSourceBinding},
	} {
		t.Run(c.nome, func(t *testing.T) {
			store := aos493Store(t)
			n := aos493Compor(t, agentruntime.CompletionEnforce, aos493Guiao(nil, agentruntime.ModelResponse{Text: "feito", StopReason: agentruntime.StopStop}), store)
			runID := "run-497-impossivel-" + string(rune('a'+i))
			oc := aos493Desfecho(t, n, agentruntime.Goal{RunID: runID, Objective: "le", MaxTurns: 2, AllowedTools: c.permitidas, OutputFromTool: c.origem, OutputSourceBinding: c.vinculo})
			if !errors.Is(oc.Err, c.erro) {
				t.Fatalf("o run tinha de ser recusado com %v; veio err=%v res=%+v", c.erro, oc.Err, oc.Result)
			}
			if oc.Result.Turns != 0 || oc.Result.OutputSource != nil {
				t.Fatalf("o run recusado nao da turnos nem tem ancora: %+v", oc.Result)
			}
			tr, selada := aos497AncoraDoLog(t, store, runID)
			if tr.To != "failed" || tr.Reason != reasonRunFailed || selada != nil {
				t.Fatalf("transicao terminal = %s; quero failed (%s) sem ancora", tr.cru, reasonRunFailed)
			}
		})
	}
}

// TestAOS497_No_RetomaDepoisDeCrash: o run declara a origem, dá o turno 1 (a tool corre e
// devolve o resultado da PRIMEIRA vida) e o nó morre. A segunda incarnação retoma-o pela
// varredura de arranque:
//
//   - a declaração e o vínculo vêm do registo de retoma;
//   - a tool NÃO volta a correr: o resultado da primeira vida está memorizado no step-ledger;
//   - a âncora selada é a dessa chamada — o digest dos bytes da primeira vida. A tool da
//     segunda incarnação devolveria outros bytes, e o digest não é o deles;
//   - o replay do run, sobre a captura cifrada por-titular, designa a mesma âncora.
func TestAOS497_No_RetomaDepoisDeCrash(t *testing.T) {
	for _, vinculo := range agentruntime.OutputSourceBindings() {
		t.Run(string(vinculo), func(t *testing.T) {
			pinBreakerEnv(t, "0", "0", "0", "0")
			ctx := context.Background()
			const runID = "plan-497~leitura"
			approvers := crashResumeApprovers(t)
			goal := agentruntime.Goal{
				RunID:               runID,
				Principal:           referencemonitor.Principal{NHIID: durAgent},
				Model:               agentruntime.ModelConfig{ModelID: "modelo-486"},
				Objective:           "o trabalho de um no de leitura",
				MaxTurns:            4,
				AllowedTools:        []string{"beta", "counter"},
				CompletionRequires:  []string{"counter"},
				CompletionMode:      agentruntime.CompletionEnforce,
				OutputFromTool:      "counter",
				OutputSourceBinding: vinculo,
			}
			var execs int64
			counterDaVida := func(vida string) func([]byte) ([]byte, error) {
				return func([]byte) ([]byte, error) {
					atomic.AddInt64(&execs, 1)
					return []byte("documento lido na " + vida), nil
				}
			}

			// ===== INCARNAÇÃO 1: dá o turno 1 (a tool corre) e "crasha".
			store := aos493Store(t)
			vault := audit.NewInMemoryKeyVault(nil)
			inc1 := aos493Incarnar(t, store, vault, approvers, counterDaVida("vida 1"))
			prod := goal
			prod.Credential = inc1.cred
			prod.MaxTurns = 1
			if _, _, rerr := inc1.node.Runtime.Run(withRunToolAllowlist(ctx, goal.AllowedTools), prod, nil); !errors.Is(rerr, agentruntime.ErrMaxTurnsExceeded) {
				t.Fatalf("turno 1 antes do crash: %v", rerr)
			}
			if execs != 1 {
				t.Fatalf("o turno 1 tinha de executar a tool uma vez; executou %d", execs)
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
			rec, err := resumeRecordFromGoal(goal)
			if err != nil {
				t.Fatalf("resumeRecordFromGoal: %v", err)
			}
			// A projecção do nó leva a declaração e o vínculo.
			if rec.OutputFromTool != "counter" || rec.OutputSourceBinding != vinculo {
				t.Fatalf("o registo de retoma perdeu a declaracao: %q / %q", rec.OutputFromTool, rec.OutputSourceBinding)
			}
			if err := inc1.node.ResumeRecords.Put(ctx, rec); err != nil {
				t.Fatalf("semear o registo de retoma: %v", err)
			}
			_ = inc1.node.Close()

			// ===== INCARNAÇÃO 2: a tool devolveria OUTROS bytes se voltasse a correr.
			inc2 := aos493Incarnar(t, store, vault, approvers, counterDaVida("vida 2"))
			t.Cleanup(func() { _ = inc2.node.Close() })
			svc2, _ := aos493ServicoComLog(t, inc2.node)
			var doRegisto integration.ResumeRecord
			if got, ok, err := inc2.node.ResumeRecords.Get(ctx, runID); err != nil || !ok {
				t.Fatalf("registo de retoma: ok=%v err=%v", ok, err)
			} else {
				doRegisto = got
			}
			if doRegisto.OutputFromTool != "counter" || doRegisto.OutputSourceBinding != vinculo {
				t.Fatalf("a segunda incarnacao le outra declaracao: %q / %q", doRegisto.OutputFromTool, doRegisto.OutputSourceBinding)
			}
			scanned, resumed, err := svc2.ResumeInterruptedRuns(ctx)
			if err != nil || scanned != 1 || resumed != 1 {
				t.Fatalf("a varredura devia ver 1 orfao e retomar 1: scanned=%d resumed=%d err=%v", scanned, resumed, err)
			}
			res := aos493Esperar(t, svc2, runID).Result

			if execs != 1 {
				t.Fatalf("a tool voltou a correr na retoma (%d execucoes): o resultado memorizado nao foi usado", execs)
			}
			if !res.Terminated || res.Unfulfilled {
				t.Fatalf("o run retomado tinha de concluir: %+v (veredicto %+v)", res, res.Verdict)
			}
			vida1 := "documento lido na vida 1"
			a := res.OutputSource
			if a == nil || a.State != agentruntime.OutputSourceDesignated || a.Binding != vinculo ||
				a.Digest != aos497Digest(vida1) || a.Bytes != len(vida1) {
				t.Fatalf("ancora depois da retoma = %+v; quero designada, com o digest dos bytes da PRIMEIRA vida (%s)", a, aos497Digest(vida1))
			}
			if a.Digest == aos497Digest("documento lido na vida 2") {
				t.Fatal("o digest selado e o de uma re-execucao")
			}
			if !aos497PassoMediado(t, store, runID, a.StepID) {
				t.Fatalf("o passo designado %q nao e o de nenhuma mediacao do run", a.StepID)
			}
			tr, selada := aos497AncoraDoLog(t, store, runID)
			if tr.To != "complete" || !reflect.DeepEqual(selada, a) {
				t.Fatalf("transicao terminal = %s; quero complete com a ancora %+v", tr.cru, a)
			}
			if bytes.Contains(tr.cru, []byte("documento lido")) {
				t.Fatalf("a transicao terminal leva conteudo: %s", tr.cru)
			}

			// ===== O REPLAY, sobre a captura cifrada por-titular, designa a mesma âncora.
			turnos := aos486TurnosDoRun(t, store, runID)
			eng, err := replay.NewEngine(inc2.node.EventStore, replay.WithContentOpener(inc2.node.contentOpener, replay.Accessor{
				Principal: "nhi:leitor-aos497", Scopes: []string{replay.DefaultSovereignContentScope},
			}))
			if err != nil {
				t.Fatalf("replay.NewEngine: %v", err)
			}
			rep, err := eng.Replay(ctx, runID, replay.Options{Spec: replay.TrajectorySpec{
				Objective: goal.Objective, Tools: turnos[0].specs,
				Model: agentruntime.ModelConfig{ModelID: "modelo-486"}, AssemblyVersion: agentruntime.AssemblyVersion,
			}})
			if err != nil || rep.Divergence != nil || rep.Fidelity != 1.0 {
				t.Fatalf("Replay: err=%v divergencia=%+v fidelidade=%v", err, rep.Divergence, rep.Fidelity)
			}
			if !reflect.DeepEqual(rep.OutputSource, a) {
				t.Fatalf("ancora:\n  replay %+v\n  selada %+v", rep.OutputSource, a)
			}
		})
	}
}
