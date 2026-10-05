package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-493 — OS ACHADOS DA REVISÃO ADVERSARIAL, cada um com o teste que o fecha.
//
// A revisão mediu seis mutações que sobreviviam à suite: a variável de ambiente ignorada (R30), o
// banner por imprimir (R18), o run não cumprido fora da saga (R28), as duas do contador de runs
// sem tool call (R15, R15b) e o motor de replay a ler o modo do primeiro turno (R4, no kernel).
// Os testes deste ficheiro são os que as matam do lado do nó, mais os dos achados I3, I5, I6,
// M3 e M4.

// registoDeRetomaDeTeste projecta um Goal de teste no registo de retoma. Um Goal sem modo do
// veredicto é gravado com `off` EXPLÍCITO — o que os testes anteriores ao AOS-493 sempre
// semearam, quando a projecção ainda punha `off` por omissão. A projecção de produção recusa o
// modo vazio ([errResumeRecordSemModo]); é aqui, à vista, que o teste decide.
func registoDeRetomaDeTeste(t testing.TB, goal agentruntime.Goal) integration.ResumeRecord {
	t.Helper()
	if goal.CompletionMode == "" {
		goal.CompletionMode = agentruntime.CompletionOff
	}
	rec, err := resumeRecordFromGoal(goal)
	if err != nil {
		t.Fatalf("resumeRecordFromGoal: %v", err)
	}
	return rec
}

// aos493Guiao é o modelo que responde pelo turno: a resposta i é a do turno i+1. Conta as vezes
// que foi interrogado.
func aos493Guiao(interrogacoes *int64, turnos ...agentruntime.ModelResponse) agentruntime.ModelClient {
	return agentruntime.ModelClientFunc(func(_ context.Context, v agentruntime.PromptView) (agentruntime.ModelResponse, error) {
		if interrogacoes != nil {
			atomic.AddInt64(interrogacoes, 1)
		}
		if v.Turn < 1 || v.Turn > len(turnos) {
			return agentruntime.ModelResponse{}, errors.New("guiao esgotado")
		}
		r := turnos[v.Turn-1]
		r.Usage = agentruntime.Usage{InputTokens: 9, OutputTokens: 4}
		return r, nil
	})
}

func aos493Counter(in string) agentruntime.ToolInvocation {
	return agentruntime.ToolInvocation{ToolID: "counter", Capability: durCap, Input: []byte(in)}
}

// aos493No é o nó composto de produção (cadeia de permit, execução durável, cifra por-titular)
// com o modo do veredicto dado, a tool `counter` registada e o log do serviço capturado.
type aos493No struct {
	node    *Node
	cred    string
	svc     *NodeService
	registo *syncBuf
	execs   *int64
}

func (n aos493No) linhas() string {
	n.registo.mu.Lock()
	defer n.registo.mu.Unlock()
	return n.registo.b.String()
}

// aos493Compor monta o nó. A `counter` falha quando o input é "boom" e conta as execuções.
func aos493Compor(t *testing.T, modo agentruntime.CompletionMode, modelo agentruntime.ModelClient, store EventStorePort) aos493No {
	t.Helper()
	pinBreakerEnv(t, "0", "0", "0", "0")
	approvers := crashResumeApprovers(t)
	node, cred := obsPermitNodeWith(t, "", modelo, func(cfg *Config) {
		cfg.EventStore = store
		cfg.DSARVault = audit.NewInMemoryKeyVault(nil)
		cfg.DurableExecution = true
		cfg.Approvers = approvers
		cfg.CompletionVerdict = modo
	})
	t.Cleanup(func() { _ = node.Close() })
	execs := new(int64)
	if err := node.Runtime.Register("counter", func(_ context.Context, in []byte) ([]byte, error) {
		atomic.AddInt64(execs, 1)
		if string(in) == "boom" {
			return nil, errors.New("falha da tool")
		}
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("Register(counter): %v", err)
	}
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
	return aos493No{node: node, cred: cred, svc: svc, registo: registo, execs: execs}
}

func aos493Store(t *testing.T) *eventstore.Store {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	return store
}

// aos493Desfecho submete o goal e espera o desfecho, sem exigir que o loop acabe sem erro.
func aos493Desfecho(t *testing.T, n aos493No, goal agentruntime.Goal) RunOutcome {
	t.Helper()
	goal.Principal = referencemonitor.Principal{NHIID: durAgent}
	goal.Credential = n.cred
	if err := n.svc.Submit(context.Background(), goal); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	wc, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	oc, ok, err := n.svc.Wait(wc, goal.RunID)
	if err != nil || !ok {
		t.Fatalf("Wait(%s): ok=%v err=%v", goal.RunID, ok, err)
	}
	return oc
}

// ---------------------------------------------------------------------------------------------
// I1 — a variável de ambiente chega ao run hospedado, e o banner declara o modo
// ---------------------------------------------------------------------------------------------

// TestAOS493_Rev_AVariavelChegaAoRunHospedado segue `AOS_COMPLETION_VERDICT` do ambiente até ao
// desfecho de um run: ambiente → [nodeConfigFromEnv] → [Config] → [Bootstrap] → nó → `hostRun` →
// manifesto do turno e transição terminal. O run é o mesmo nos quatro casos — um turno cortado
// pelo limite de tokens, sem contrato — e só a variável muda.
//
// Mata a mutação R30 da revisão (a variável lida e ignorada): com ela o nó ficava sempre em
// observação e os casos `enforce` e `off` falham aqui.
func TestAOS493_Rev_AVariavelChegaAoRunHospedado(t *testing.T) {
	for i, c := range []struct {
		valor string
		modo  agentruntime.CompletionMode
	}{
		{"", agentruntime.CompletionObserve}, // a omissão é observação
		{"observe", agentruntime.CompletionObserve},
		{"enforce", agentruntime.CompletionEnforce},
		{"off", agentruntime.CompletionOff},
	} {
		t.Run("AOS_COMPLETION_VERDICT="+c.valor, func(t *testing.T) {
			t.Setenv("AOS_COMPLETION_VERDICT", c.valor)
			doAmbiente, err := nodeConfigFromEnv()
			if err != nil {
				t.Fatalf("nodeConfigFromEnv: %v", err)
			}
			if doAmbiente.CompletionVerdict != c.modo {
				t.Fatalf("a configuracao por ambiente ficou com o modo %q; quero %q", doAmbiente.CompletionVerdict, c.modo)
			}

			store := aos493Store(t)
			modelo := aos493Guiao(nil, agentruntime.ModelResponse{Text: "cortado a mei", StopReason: agentruntime.StopLength})
			// O modo do nó é o que SAIU do ambiente, e não o que o teste espera.
			n := aos493Compor(t, doAmbiente.CompletionVerdict, modelo, store)
			if n.node.completionVerdict != c.modo {
				t.Fatalf("o no composto ficou em %q; quero %q", n.node.completionVerdict, c.modo)
			}
			runID := "run-493-ambiente-" + string(rune('a'+i))
			oc := aos493Desfecho(t, n, agentruntime.Goal{RunID: runID, Objective: "responde", MaxTurns: 2})
			if oc.Err != nil {
				t.Fatalf("o run acabou com erro de loop: %v", oc.Err)
			}
			res := oc.Result
			tr := aos493UltimaTransicao(t, store, runID)
			turnos := aos486TurnosDoRun(t, store, runID)
			if len(turnos) != 1 {
				t.Fatalf("queria 1 turno gravado, vieram %d", len(turnos))
			}
			temCompletion := bytes.Contains(turnos[0].manifesto, []byte(`"completion":{"mode":"`+string(c.modo)+`"}`))

			switch c.modo {
			case agentruntime.CompletionEnforce:
				if !res.Unfulfilled || res.Terminated || res.FinalText != "" {
					t.Fatalf("com enforce o run cortado nao cumpre: %+v", res)
				}
				if tr.To != "failed" || tr.Reason != reasonRunUnfulfilled || tr.OutcomeReason != string(agentruntime.OutcomeTruncated) {
					t.Fatalf("transicao terminal = %s; quero failed/%s/truncated", tr.cru, reasonRunUnfulfilled)
				}
				if !temCompletion {
					t.Fatalf("o manifesto do turno nao gravou o modo do no: %s", turnos[0].manifesto)
				}
			case agentruntime.CompletionObserve:
				if res.Unfulfilled || !res.Terminated || res.FinalText != "cortado a mei" {
					t.Fatalf("com observe o desfecho e o de sempre: %+v", res)
				}
				if tr.To != "complete" || tr.OutcomeReason != string(agentruntime.OutcomeTruncated) || tr.Verdict == nil || tr.Verdict.Mode != agentruntime.CompletionObserve {
					t.Fatalf("transicao terminal = %s; quero complete com o veredicto truncated observado", tr.cru)
				}
				if !temCompletion {
					t.Fatalf("o manifesto do turno nao gravou o modo do no: %s", turnos[0].manifesto)
				}
			case agentruntime.CompletionOff:
				if res.Unfulfilled || !res.Terminated || res.Verdict != nil {
					t.Fatalf("com off nao ha veredicto: %+v", res)
				}
				if tr.To != "complete" || bytes.Contains(tr.cru, []byte("verdict")) || bytes.Contains(tr.cru, []byte("outcome_reason")) {
					t.Fatalf("com off a transicao terminal tem os bytes de antes; veio %s", tr.cru)
				}
				if bytes.Contains(turnos[0].manifesto, []byte("completion")) {
					t.Fatalf("com off o manifesto nao ganha o campo: %s", turnos[0].manifesto)
				}
			}
			// E o registo de retoma fica com o mesmo modo: é dele que uma retoma o lê.
			rec, ok, err := n.node.ResumeRecords.Get(context.Background(), runID)
			if err != nil || !ok || rec.CompletionMode != c.modo {
				t.Fatalf("registo de retoma: modo=%q ok=%v err=%v; quero %q", rec.CompletionMode, ok, err, c.modo)
			}
		})
	}
}

// TestAOS493_Rev_OBannerDeclaraOModo arranca o nó pelo entrypoint ([run]) com cada valor da
// variável e exige a linha do banner desse modo — e de nenhum outro. Mata a mutação R18 (banner
// não impresso): sem a linha o operador não vê em que modo o nó ficou, e é esse o engano que o
// [ErrBadCompletionVerdict] diz evitar.
func TestAOS493_Rev_OBannerDeclaraOModo(t *testing.T) {
	modos := []agentruntime.CompletionMode{agentruntime.CompletionObserve, agentruntime.CompletionEnforce, agentruntime.CompletionOff}
	for _, c := range []struct {
		valor string
		modo  agentruntime.CompletionMode
	}{
		{"", agentruntime.CompletionObserve},
		{"observe", agentruntime.CompletionObserve},
		{"enforce", agentruntime.CompletionEnforce},
		{"off", agentruntime.CompletionOff},
	} {
		t.Run("AOS_COMPLETION_VERDICT="+c.valor, func(t *testing.T) {
			t.Setenv("AOS_COMPLETION_VERDICT", c.valor)
			var sb strings.Builder
			if err := run(&sb); err != nil {
				t.Fatalf("run: %v", err)
			}
			saida := sb.String()
			if quero := "[aos] " + completionVerdictBanner(c.modo) + "\n"; !strings.Contains(saida, quero) {
				t.Fatalf("o arranque nao declarou o modo %q do veredicto. Banner:\n%s", c.modo, saida)
			}
			for _, outro := range modos {
				if outro != c.modo && strings.Contains(saida, completionVerdictBanner(outro)) {
					t.Fatalf("o arranque em %q declarou tambem o modo %q", c.modo, outro)
				}
			}
			if n := strings.Count(saida, "veredicto de conclusao (EPIC-02/AOS-493)"); n != 1 {
				t.Fatalf("a linha do veredicto saiu %d vez(es); queria 1", n)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------
// I2 — um run não cumprido é `failed`, e segue o caminho de qualquer `failed`
// ---------------------------------------------------------------------------------------------

// aos493Incarnar é uma incarnação do nó sobre um substrato dado, no molde do AOS-486 (o cliente
// de modelo é o adaptador real sobre um gateway que grava os pedidos: turno 1 pede `counter`,
// os seguintes concluem), com a `counter` entregue pelo teste.
func aos493Incarnar(t *testing.T, store *eventstore.Store, vault audit.KeyVault, approvers []ApproverConfig, counter func([]byte) ([]byte, error)) aos486Incarnacao {
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
	if err := node.Runtime.Register("counter", func(_ context.Context, in []byte) ([]byte, error) { return counter(in) }); err != nil {
		t.Fatalf("Register(counter): %v", err)
	}
	return aos486Incarnacao{node: node, cred: cred, gw: gw}
}

func aos493ServicoComLog(t *testing.T, node *Node) (*NodeService, *syncBuf) {
	t.Helper()
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
	return svc, registo
}

// TestAOS493_Rev_NaoCumpridoEntraNaSaga fixa o que acontece HOJE a um run não cumprido em
// imposição que fez um efeito: o contrato exige `counter` e `beta`, a `counter` corre bem (o
// efeito está aplicado) e a `beta` nunca é pedida.
//
//   - o run sela `failed` (`objective_unfulfilled`, `contract_unmet_no_call`);
//   - `failed` é a origem da saga de compensação, e o run ENTRA nela como qualquer `failed`;
//   - o loop não regista compensações, pelo que a saga não desfaz nada: declara a AUSÊNCIA no
//     log e no WORM, e o run fica em `failed` com o efeito da `counter` APLICADO.
//
// É a decisão do ADR-037 §4 e o seu limite: quando houver compensações registadas, a saga passa
// a desfazer efeitos de tools que correram bem, e este teste é o que muda.
//
// Mata a mutação R28 da revisão (o não cumprido fora da saga).
func TestAOS493_Rev_NaoCumpridoEntraNaSaga(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	ctx := context.Background()
	const runID = "run-493-saga"
	store := aos493Store(t)
	var execs int64
	inc := aos493Incarnar(t, store, audit.NewInMemoryKeyVault(nil), crashResumeApprovers(t), func([]byte) ([]byte, error) {
		atomic.AddInt64(&execs, 1)
		return []byte("pong"), nil
	})
	t.Cleanup(func() { _ = inc.node.Close() })
	inc.node.completionVerdict = agentruntime.CompletionEnforce
	svc, registo := aos493ServicoComLog(t, inc.node)

	if inc.node.Compensations == nil || inc.node.Compensations.Len() != 0 {
		t.Fatalf("pre-condicao: o no tem o registo de compensacoes composto e VAZIO (o loop nao regista nenhuma); veio %+v", inc.node.Compensations)
	}
	goal := agentruntime.Goal{
		RunID:              runID,
		Principal:          referencemonitor.Principal{NHIID: durAgent},
		Credential:         inc.cred,
		Model:              agentruntime.ModelConfig{ModelID: "modelo-486"},
		Objective:          "le e arquiva",
		MaxTurns:           4,
		CompletionRequires: []string{"counter", "beta"},
	}
	if err := svc.Submit(ctx, goal); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	res := aos493Esperar(t, svc, runID).Result

	// (1) O run fez o efeito e não cumpriu.
	if execs != 1 {
		t.Fatalf("a counter tinha de executar uma vez (o efeito aplicado); executou %d", execs)
	}
	quer := &agentruntime.Verdict{
		Mode: agentruntime.CompletionEnforce, Reason: agentruntime.OutcomeContractNoCall, ToolCallsRequested: 1,
		Tools: []agentruntime.ToolEvidence{
			{Tool: "counter", Requested: 1, Effective: 1, Last: agentruntime.ToolOutcomeEffective},
			{Tool: "beta"},
		},
	}
	if !res.Unfulfilled || !reflect.DeepEqual(res.Verdict, quer) {
		t.Fatalf("desfecho = Unfulfilled=%v veredicto=%+v; quero nao cumprido com %+v", res.Unfulfilled, res.Verdict, quer)
	}

	// (2) O estado durável é `failed`, e FICA em `failed`: a saga não transitou para
	// `compensating`, porque não havia compensação a correr.
	tr := aos493UltimaTransicao(t, store, runID)
	if tr.To != "failed" || tr.Reason != reasonRunUnfulfilled {
		t.Fatalf("ultima transicao = %s; quero running→failed (%s) e mais nenhuma", tr.cru, reasonRunUnfulfilled)
	}

	// (3) O run ENTROU na saga: a ausência de compensação está declarada no log…
	registo.mu.Lock()
	linhas := registo.b.String()
	registo.mu.Unlock()
	if !strings.Contains(linhas, `saga (AOS-254): run "`+runID+`" FAILED sem compensacao registada`) {
		t.Fatalf("o run nao cumprido tinha de entrar na saga de compensacao como qualquer failed, e a ausencia de compensacao tinha de ser declarada:\n%s", linhas)
	}
	// …e no WORM, atribuída ao titular do run.
	head, err := inc.node.WORM.Head(ctx, sagaCompensationPartition)
	if err != nil || head == 0 {
		t.Fatalf("a particao da saga no WORM devia ter a declaracao de ausencia: head=%d err=%v", head, err)
	}
	recs, err := inc.node.WORM.Read(ctx, sagaCompensationPartition, 1, head)
	if err != nil {
		t.Fatalf("ler a particao da saga: %v", err)
	}
	declarada := 0
	for i := range recs {
		if recs[i].RunID == runID && recs[i].Reason == reasonSagaNoCompensation && recs[i].Decision == audit.DecisionDeny {
			declarada++
		}
	}
	if declarada != 1 {
		t.Fatalf("queria UMA declaracao de ausencia de compensacao para o run no WORM; vieram %d em %+v", declarada, recs)
	}

	// (4) A linha do run não cumprido diz o estado durável, e saiu DEPOIS do selo.
	if !strings.Contains(linhas, `run "`+runID+`" NAO CUMPRIDO (AOS-493)`) || !strings.Contains(linhas, "Estado duravel: failed ("+reasonRunUnfulfilled+")") {
		t.Fatalf("o run nao cumprido tem de dizer o estado duravel no log:\n%s", linhas)
	}
}

// ---------------------------------------------------------------------------------------------
// I3 / I4 — a retoma depois de uma falha de tool, nos dois ramos, e o que o replay faz deles
// ---------------------------------------------------------------------------------------------

// TestAOS493_Rev_RetomaDepoisDeFalhaDeTool fixa os dois ramos que a revisão mediu. O run tem o
// contrato `[counter]` e corre em imposição; a `counter` FALHA no turno 1; o processo morre; o
// run é re-hospedado.
//
//   - RETOMA POR CRASH, SEM CREDENCIAL (a varredura de arranque): o turno 1 é reproduzido, a
//     chamada que falhou não tem resultado memorizado e é re-mediada — sem credencial, o
//     Reference Monitor NEGA-A. A tool não volta a executar, o vector sai «pedida 1, negada 1» e
//     o run sela `failed` com `contract_unmet_after_denial`: a razão diz «recusa» de uma chamada
//     que na primeira vida foi permitida e falhou.
//   - RETOMA COM CREDENCIAL (a retoma por aprovação): a chamada volta a correr, tem êxito, e o
//     run conclui cumprido.
//
// A causa é anterior ao AOS-493 (a re-hospedagem por crash não tem credencial); passou a decidir
// o desfecho. Fica fixada para que uma mudança num dos ramos se veja.
//
// E O REPLAY, nos dois: a captura do turno 1 é a da primeira vida (falha de tool), o turno 2 foi
// gravado na segunda (com outro resultado no tail), e o motor PÁRA em divergência de
// `prompt_hash` no turno 2 — fidelidade parcial e NENHUM veredicto. Não «chega a outro vector».
func TestAOS493_Rev_RetomaDepoisDeFalhaDeTool(t *testing.T) {
	for _, c := range []struct {
		via    string
		execs  int64
		razao  agentruntime.OutcomeReason
		vector agentruntime.ToolEvidence
		sobre  string
	}{
		{
			via: "crash-resume", execs: 1, razao: agentruntime.OutcomeContractAfterDenial,
			vector: agentruntime.ToolEvidence{Tool: "counter", Requested: 1, Denied: 1, Last: agentruntime.ToolOutcomeDenied},
			sobre:  agentruntime.ToolOutcomeDenied,
		},
		{
			via: "retoma", execs: 2, razao: agentruntime.OutcomeFulfilled,
			vector: agentruntime.ToolEvidence{Tool: "counter", Requested: 1, Effective: 1, Last: agentruntime.ToolOutcomeEffective},
			sobre:  agentruntime.ToolOutcomeEffective,
		},
	} {
		t.Run(c.via, func(t *testing.T) {
			pinBreakerEnv(t, "0", "0", "0", "0")
			ctx := context.Background()
			const runID = "plan-493~falha-e-retoma"
			approvers := crashResumeApprovers(t)
			goal := agentruntime.Goal{
				RunID:              runID,
				Principal:          referencemonitor.Principal{NHIID: durAgent},
				Model:              agentruntime.ModelConfig{ModelID: "modelo-486"},
				Objective:          "o trabalho de um no do plano",
				MaxTurns:           4,
				AllowedTools:       []string{"beta", "counter"},
				CompletionRequires: []string{"counter"},
				CompletionMode:     agentruntime.CompletionEnforce,
			}
			// A counter falha na PRIMEIRA execução e tem êxito nas seguintes.
			var execs int64
			counter := func([]byte) ([]byte, error) {
				if atomic.AddInt64(&execs, 1) == 1 {
					return nil, errors.New("timeout a jusante")
				}
				return []byte("pong"), nil
			}

			// ===== INCARNAÇÃO 1: dá o turno 1 (a tool falha) e "crasha".
			store := aos493Store(t)
			vault := audit.NewInMemoryKeyVault(nil)
			inc1 := aos493Incarnar(t, store, vault, approvers, counter)
			prod := goal
			prod.Credential = inc1.cred
			prod.MaxTurns = 1
			if _, _, rerr := inc1.node.Runtime.Run(withRunToolAllowlist(ctx, goal.AllowedTools), prod, nil); !errors.Is(rerr, agentruntime.ErrMaxTurnsExceeded) {
				t.Fatalf("turno 1 antes do crash: %v", rerr)
			}
			if execs != 1 {
				t.Fatalf("o turno 1 tinha de executar a tool uma vez (e falhar); executou %d", execs)
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
			if err := inc1.node.ResumeRecords.Put(ctx, rec); err != nil {
				t.Fatalf("semear o registo de retoma: %v", err)
			}
			_ = inc1.node.Close()

			// ===== INCARNAÇÃO 2.
			inc2 := aos493Incarnar(t, store, vault, approvers, counter)
			t.Cleanup(func() { _ = inc2.node.Close() })
			svc2, _ := aos493ServicoComLog(t, inc2.node)
			switch c.via {
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

			if execs != c.execs {
				t.Fatalf("execucoes da tool nas duas vidas = %d; quero %d", execs, c.execs)
			}
			negativo := c.razao != agentruntime.OutcomeFulfilled
			quer := &agentruntime.Verdict{
				Mode: agentruntime.CompletionEnforce, Fulfilled: !negativo, Reason: c.razao,
				Tools: []agentruntime.ToolEvidence{c.vector}, ToolCallsRequested: 1,
			}
			if !reflect.DeepEqual(res.Verdict, quer) {
				t.Fatalf("veredicto da segunda vida = %+v, quero %+v", res.Verdict, quer)
			}
			if res.Unfulfilled != negativo || res.Terminated == negativo || res.LastToolOutcome != c.sobre {
				t.Fatalf("desfecho: Unfulfilled=%v Terminated=%v acabou sobre %q; quero Unfulfilled=%v e %q",
					res.Unfulfilled, res.Terminated, res.LastToolOutcome, negativo, c.sobre)
			}
			tr := aos493UltimaTransicao(t, store, runID)
			if estado := map[bool]string{true: "failed", false: "complete"}[negativo]; tr.To != estado || tr.OutcomeReason != string(c.razao) {
				t.Fatalf("transicao terminal = %s; quero →%s com outcome_reason %q", tr.cru, estado, c.razao)
			}

			// ===== O REPLAY do run retomado: pára em divergência, sem veredicto.
			turnos := aos486TurnosDoRun(t, store, runID)
			if len(turnos) != 2 {
				t.Fatalf("queria 2 turnos gravados, vieram %d", len(turnos))
			}
			eng, err := replay.NewEngine(inc2.node.EventStore, replay.WithContentOpener(inc2.node.contentOpener, replay.Accessor{
				Principal: "nhi:leitor-aos493", Scopes: []string{replay.DefaultSovereignContentScope},
			}))
			if err != nil {
				t.Fatalf("replay.NewEngine: %v", err)
			}
			rep, err := eng.Replay(ctx, runID, replay.Options{Spec: replay.TrajectorySpec{
				Objective: goal.Objective, Tools: turnos[0].specs,
				Model: agentruntime.ModelConfig{ModelID: "modelo-486"}, AssemblyVersion: agentruntime.AssemblyVersion,
			}})
			if err != nil {
				t.Fatalf("Replay: %v", err)
			}
			if rep.Divergence == nil || rep.Divergence.Turn != 2 || rep.Divergence.Reason != "prompt_hash" {
				t.Fatalf("o replay de um run retomado cujo turno re-executado mudou de desfecho tinha de PARAR em divergencia de prompt_hash no turno 2; veio divergencia=%+v fidelidade=%v", rep.Divergence, rep.Fidelity)
			}
			if rep.Verdict != nil || rep.Terminated || rep.Unfulfilled {
				t.Fatalf("o replay parado em divergencia nao reproduz veredicto nenhum: Verdict=%+v Terminated=%v Unfulfilled=%v", rep.Verdict, rep.Terminated, rep.Unfulfilled)
			}
			if rep.Fidelity >= 1.0 {
				t.Fatalf("a fidelidade de um replay divergente nao pode ser total; veio %v", rep.Fidelity)
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------
// I5 — um contrato impossível não arranca, pelo nó composto
// ---------------------------------------------------------------------------------------------

// TestAOS493_Rev_ContratoImpossivelNoNo: a validação é do kernel
// (`replay/aos493_diferencial_veredicto_test.go` cobre a tabela); aqui prova-se que ela vê o
// tool set e a lista-branca DEFINITIVOS do run — os que o nó compõe a partir do snapshot
// congelado e da lista-branca (AOS-486) — e o que o nó faz do run recusado.
func TestAOS493_Rev_ContratoImpossivelNoNo(t *testing.T) {
	casos := []struct {
		nome       string
		contrato   []string
		permitidas []string
		possivel   bool
	}{
		{nome: "tool que o catalogo nao tem", contrato: []string{"doc_read"}},
		{nome: "outra caixa", contrato: []string{"Counter"}},
		{nome: "com espaco", contrato: []string{" counter"}},
		{nome: "tool do catalogo fora da lista-branca do run", contrato: []string{"counter"}, permitidas: []string{"outra"}},
		{nome: "tool do catalogo, sem lista-branca", contrato: []string{"counter"}, possivel: true},
		{nome: "tool do catalogo, na lista-branca", contrato: []string{"counter"}, permitidas: []string{"counter"}, possivel: true},
	}
	i := 0
	for _, c := range casos {
		for _, modo := range []agentruntime.CompletionMode{agentruntime.CompletionObserve, agentruntime.CompletionEnforce, agentruntime.CompletionOff} {
			i++
			runID := "run-493-impossivel-" + string(rune('a'+i))
			t.Run(c.nome+"/"+string(modo), func(t *testing.T) {
				store := aos493Store(t)
				var interrogacoes int64
				n := aos493Compor(t, modo, aos493Guiao(&interrogacoes, agentruntime.ModelResponse{Text: "feito", Final: true, StopReason: agentruntime.StopStop}), store)
				oc := aos493Desfecho(t, n, agentruntime.Goal{
					RunID: runID, Objective: "le o documento", MaxTurns: 3,
					CompletionRequires: c.contrato, AllowedTools: c.permitidas,
				})
				// Em `off` o contrato não é lido; um contrato possível arranca em qualquer modo.
				if c.possivel || modo == agentruntime.CompletionOff {
					if oc.Err != nil || interrogacoes != 1 {
						t.Fatalf("o run tinha de arrancar: err=%v interrogacoes=%d", oc.Err, interrogacoes)
					}
					return
				}
				if !errors.Is(oc.Err, agentruntime.ErrImpossibleCompletionContract) {
					t.Fatalf("um contrato impossivel tinha de recusar o arranque com ErrImpossibleCompletionContract; veio err=%v res=%+v", oc.Err, oc.Result)
				}
				if interrogacoes != 0 || oc.Result.Turns != 0 || oc.Result.Verdict != nil {
					t.Fatalf("o run recusado nao gasta turnos nem tem veredicto: interrogacoes=%d res=%+v", interrogacoes, oc.Result)
				}
				if turnos := aos486TurnosDoRun(t, store, runID); len(turnos) != 0 {
					t.Fatalf("o run recusado gravou %d turno(s)", len(turnos))
				}
				// O nó sela-o como um erro de loop — `run_failed`, sem veredicto —, e não como
				// «não cumprido»: a razão `contract_unmet_no_call` diria «o modelo não chamou».
				tr := aos493UltimaTransicao(t, store, runID)
				if tr.To != "failed" || tr.Reason != reasonRunFailed || bytes.Contains(tr.cru, []byte("outcome_reason")) {
					t.Fatalf("transicao terminal = %s; quero failed (%s) sem outcome_reason", tr.cru, reasonRunFailed)
				}
				if got := n.svc.desfechos.lido(state.Failed, agentruntime.OutcomeFulfilled); got != 1 {
					t.Fatalf("aos_runs_finished_total{failed,none} = %d; quero 1", got)
				}
				for _, rz := range agentruntime.OutcomeReasons() {
					if n.svc.desfechos.lido(state.Failed, rz) != 0 || n.svc.desfechos.lido(state.Complete, rz) != 0 {
						t.Fatalf("o run recusado contou como veredicto negativo (%s)", rz)
					}
				}
			})
		}
	}
}

// ---------------------------------------------------------------------------------------------
// I6 — sobre o que o run acabou: medido, e não veredicto
// ---------------------------------------------------------------------------------------------

// TestAOS493_Rev_SobreOQueORunAcabou_Contador: o contador próprio, de cardinalidade fixa
// (3 estados × 2 × 4 = 24 séries), como função pura.
func TestAOS493_Rev_SobreOQueORunAcabou_Contador(t *testing.T) {
	d := novoDesfechosDeRuns()
	negativo := &agentruntime.Verdict{Mode: agentruntime.CompletionObserve, Reason: agentruntime.OutcomeContractAfterDenial}
	positivo := &agentruntime.Verdict{Mode: agentruntime.CompletionObserve, Fulfilled: true}

	d.contar(state.Complete, agentruntime.Result{LastToolOutcome: agentruntime.ToolOutcomeDenied})                    // sem veredicto
	d.contar(state.Complete, agentruntime.Result{LastToolOutcome: agentruntime.ToolOutcomeDenied, Verdict: positivo}) // contrato cumprido
	d.contar(state.Complete, agentruntime.Result{LastToolOutcome: agentruntime.ToolOutcomeDenied, Verdict: negativo}) // observado
	d.contar(state.Failed, agentruntime.Result{LastToolOutcome: agentruntime.ToolOutcomeDenied, Verdict: negativo})   // imposto
	d.contar(state.Complete, agentruntime.Result{LastToolOutcome: agentruntime.ToolOutcomeFailed})
	d.contar(state.Complete, agentruntime.Result{LastToolOutcome: agentruntime.ToolOutcomeEffective})
	d.contar(state.Failed, agentruntime.Result{})                                              // recusado antes do loop: none
	d.contar(state.TimedOut, agentruntime.Result{LastToolOutcome: "fora\ndo vocabulario"})     // não soma
	d.contar(state.Paused, agentruntime.Result{LastToolOutcome: agentruntime.ToolOutcomeNone}) // selo no-op: não soma
	d.contar("", agentruntime.Result{LastToolOutcome: agentruntime.ToolOutcomeDenied})         // selo falhado: não soma

	quer := map[chaveSobre]int64{
		{state.Complete, false, agentruntime.ToolOutcomeDenied}:    2,
		{state.Complete, true, agentruntime.ToolOutcomeDenied}:     1,
		{state.Failed, true, agentruntime.ToolOutcomeDenied}:       1,
		{state.Complete, false, agentruntime.ToolOutcomeFailed}:    1,
		{state.Complete, false, agentruntime.ToolOutcomeEffective}: 1,
		{state.Failed, false, agentruntime.ToolOutcomeNone}:        1,
	}
	if len(d.sobre) != 24 {
		t.Fatalf("a familia tem %d series; queria 3 estados x 2 x 4", len(d.sobre))
	}
	for chave := range d.sobre {
		if got := d.lidoSobre(chave.estado, chave.negativo, chave.ultimo); got != quer[chave] {
			t.Fatalf("%+v = %d, quero %d", chave, got, quer[chave])
		}
	}
	var nulo *desfechosDeRuns
	nulo.contar(state.Complete, agentruntime.Result{})
	if nulo.lidoSobre(state.Complete, false, agentruntime.ToolOutcomeNone) != 0 {
		t.Fatal("um contador nil le zero")
	}
}

// TestAOS493_Rev_SobreOQueORunAcabou_NoNo: a classe que o veredicto não apanha, pelo nó composto
// e até ao `/metrics`. Um run SEM contrato pede uma tool, o Reference Monitor recusa-a, e o
// modelo desiste com texto. O veredicto é positivo (`reason="none"`) e o run conclui; o que diz
// que ele acabou sobre uma recusa é a outra família.
func TestAOS493_Rev_SobreOQueORunAcabou_NoNo(t *testing.T) {
	for i, c := range []struct {
		nome  string
		pede  agentruntime.ToolInvocation
		sobre string
	}{
		{"recusa", agentruntime.ToolInvocation{ToolID: "nope", Capability: durCap, Input: []byte("x")}, agentruntime.ToolOutcomeDenied},
		{"falha de tool", aos493Counter("boom"), agentruntime.ToolOutcomeFailed},
		{"efectiva", aos493Counter("tick"), agentruntime.ToolOutcomeEffective},
	} {
		t.Run(c.nome, func(t *testing.T) {
			store := aos493Store(t)
			n := aos493Compor(t, agentruntime.CompletionObserve, aos493Guiao(nil,
				agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{c.pede}, StopReason: agentruntime.StopToolCalls},
				agentruntime.ModelResponse{Text: "e o que consegui", StopReason: agentruntime.StopStop},
			), store)
			runID := "run-493-sobre-" + string(rune('a'+i))
			oc := aos493Desfecho(t, n, agentruntime.Goal{RunID: runID, Objective: "faz", MaxTurns: 3})
			res := oc.Result
			if oc.Err != nil || !res.Terminated || res.Verdict == nil || !res.Verdict.Fulfilled {
				t.Fatalf("o run sem contrato conclui com veredicto positivo: err=%v res=%+v", oc.Err, res)
			}
			if res.LastToolOutcome != c.sobre {
				t.Fatalf("o run acabou sobre %q; quero %q", res.LastToolOutcome, c.sobre)
			}
			// Não é veredicto: a transição terminal não ganha campo nenhum por isto.
			tr := aos493UltimaTransicao(t, store, runID)
			if tr.To != "complete" || tr.OutcomeReason != "" || bytes.Contains(tr.cru, []byte("last")) {
				t.Fatalf("a medicao nao pode chegar ao evento: %s", tr.cru)
			}
			corpo := metricasDe(t, &apiHandler{node: n.node, svc: n.svc})
			if quero := `aos_runs_finished_total{outcome="complete",reason="none"} 1`; !strings.Contains(corpo, quero+"\n") {
				t.Fatalf("faltou %q:\n%s", quero, amostrasDe(corpo, "aos_runs_finished_total"))
			}
			if quero := `aos_runs_finished_by_last_tool_outcome_total{outcome="complete",verdict="none",last="` + c.sobre + `"} 1`; !strings.Contains(corpo, quero+"\n") {
				t.Fatalf("faltou %q:\n%s", quero, amostrasDe(corpo, "aos_runs_finished_by_last_tool_outcome_total"))
			}
			if got := strings.Count(corpo, "aos_runs_finished_by_last_tool_outcome_total{"); got != 24 {
				t.Fatalf("a familia tem %d amostras; queria 3 estados x 2 x 4", got)
			}
			if got := strings.Count(corpo, "# TYPE aos_runs_finished_by_last_tool_outcome_total counter"); got != 1 {
				t.Fatalf("a familia tem %d linhas TYPE; queria 1", got)
			}
			// As 23 outras séries estão a zero, e presentes.
			soma := 0
			for _, l := range strings.Split(corpo, "\n") {
				if strings.HasPrefix(l, "aos_runs_finished_by_last_tool_outcome_total{") && strings.HasSuffix(l, " 1") {
					soma++
				}
			}
			if soma != 1 {
				t.Fatalf("queria exactamente uma serie a 1; vieram %d:\n%s", soma, amostrasDe(corpo, "aos_runs_finished_by_last_tool_outcome_total"))
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------
// M2 — os runs concluídos sem nenhuma tool call, com tools no tool set
// ---------------------------------------------------------------------------------------------

// TestAOS493_Rev_ConcluidosSemToolCall fixa as três condições do contador, uma a uma. Mata as
// mutações R15 (conta sem tools no tool set) e R15b (conta runs que pediram tool calls).
func TestAOS493_Rev_ConcluidosSemToolCall(t *testing.T) {
	for _, c := range []struct {
		nome   string
		selado state.State
		res    agentruntime.Result
		conta  bool
	}{
		{"complete, nenhuma pedida, tools no tool set", state.Complete, agentruntime.Result{ToolsOffered: 3}, true},
		{"complete, nenhuma pedida, SEM tools no tool set", state.Complete, agentruntime.Result{ToolsOffered: 0}, false},
		{"complete, com tool calls pedidas", state.Complete, agentruntime.Result{ToolsOffered: 3, ToolCallsRequested: 2}, false},
		{"failed, nenhuma pedida, tools no tool set", state.Failed, agentruntime.Result{ToolsOffered: 3}, false},
		{"timed_out, nenhuma pedida, tools no tool set", state.TimedOut, agentruntime.Result{ToolsOffered: 3}, false},
		{"selo falhado", "", agentruntime.Result{ToolsOffered: 3}, false},
	} {
		d := novoDesfechosDeRuns()
		d.contar(c.selado, c.res)
		if got := d.semToolCall.Load() == 1; got != c.conta || d.semToolCall.Load() > 1 {
			t.Fatalf("%s: contou=%d; queria contar=%v", c.nome, d.semToolCall.Load(), c.conta)
		}
	}

	// E PELO NÓ: um run com a `counter` no tool set que conclui sem a pedir conta; o mesmo run a
	// pedi-la não conta. O `/metrics` diz o que a contagem é — o tool set do run, não o que o
	// gateway enviou.
	for i, c := range []struct {
		nome   string
		turnos []agentruntime.ModelResponse
		quer   string
	}{
		{"sem tool call", []agentruntime.ModelResponse{{Text: "feito", StopReason: agentruntime.StopStop}}, "1"},
		{"com tool call", []agentruntime.ModelResponse{
			{ToolCalls: []agentruntime.ToolInvocation{aos493Counter("tick")}, StopReason: agentruntime.StopToolCalls},
			{Text: "feito", StopReason: agentruntime.StopStop},
		}, "0"},
	} {
		t.Run(c.nome, func(t *testing.T) {
			store := aos493Store(t)
			n := aos493Compor(t, agentruntime.CompletionObserve, aos493Guiao(nil, c.turnos...), store)
			oc := aos493Desfecho(t, n, agentruntime.Goal{RunID: "run-493-m2-" + string(rune('a'+i)), Objective: "faz", MaxTurns: 3})
			if oc.Err != nil || !oc.Result.Terminated || oc.Result.ToolsOffered == 0 {
				t.Fatalf("o run tinha de concluir com tools no tool set: err=%v res=%+v", oc.Err, oc.Result)
			}
			corpo := metricasDe(t, &apiHandler{node: n.node, svc: n.svc})
			if quero := "aos_runs_completed_without_tool_call_total " + c.quer; !strings.Contains(corpo, quero+"\n") {
				t.Fatalf("faltou %q:\n%s", quero, amostrasDe(corpo, "aos_runs_completed_without_tool_call_total"))
			}
			if !strings.Contains(corpo, "# HELP aos_runs_completed_without_tool_call_total") || !strings.Contains(corpo, "TOOL SET DO RUN") || !strings.Contains(corpo, "NAO e o numero de schemas que o gateway enviou") {
				t.Fatalf("o HELP tem de dizer que a contagem e a do tool set do run e nao a do gateway:\n%s", amostrasDe(corpo, "aos_runs_completed_without_tool_call_total"))
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------
// M3 — o log só diz «Estado duravel: failed» depois de o selo ter sido escrito
// ---------------------------------------------------------------------------------------------

// aos493SeloFalha é o Event Store do nó com a escrita da transição para `failed` a falhar.
type aos493SeloFalha struct {
	*eventstore.Store
	falhou int64
}

func (s *aos493SeloFalha) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if in.Type == state.EventTypeTransition && bytes.Contains(in.Payload, []byte(`"to":"failed"`)) {
		atomic.AddInt64(&s.falhou, 1)
		return eventstore.AppendResult{}, errors.New("event store indisponivel (teste)")
	}
	return s.Store.Append(ctx, streamID, in, opts...)
}

// TestAOS493_Rev_OLogSoAfirmaOEstadoDuravelDepoisDoSelo: o mesmo run não cumprido, com o selo a
// passar e com o selo a falhar. A linha «Estado duravel: failed» só existe no primeiro; no
// segundo o log diz que o selo NÃO foi escrito, depois da linha que diz porquê, e o run não
// conta como `failed` no `/metrics`.
func TestAOS493_Rev_OLogSoAfirmaOEstadoDuravelDepoisDoSelo(t *testing.T) {
	cortado := agentruntime.ModelResponse{Text: "cortado a mei", StopReason: agentruntime.StopLength}
	const afirma = "Estado duravel: failed"

	t.Run("selo escrito", func(t *testing.T) {
		store := aos493Store(t)
		n := aos493Compor(t, agentruntime.CompletionEnforce, aos493Guiao(nil, cortado), store)
		oc := aos493Desfecho(t, n, agentruntime.Goal{RunID: "run-493-m3-ok", Objective: "responde", MaxTurns: 2})
		if oc.Err != nil || !oc.Result.Unfulfilled {
			t.Fatalf("o run tinha de sair nao cumprido: err=%v res=%+v", oc.Err, oc.Result)
		}
		if tr := aos493UltimaTransicao(t, store, "run-493-m3-ok"); tr.To != "failed" {
			t.Fatalf("o selo tinha de ter sido escrito: %s", tr.cru)
		}
		linhas := n.linhas()
		if !strings.Contains(linhas, `run "run-493-m3-ok" NAO CUMPRIDO (AOS-493)`) || !strings.Contains(linhas, afirma+" ("+reasonRunUnfulfilled+")") {
			t.Fatalf("com o selo escrito o log diz o estado duravel:\n%s", linhas)
		}
		if strings.Contains(linhas, "O SELO NAO FOI ESCRITO") {
			t.Fatalf("o selo foi escrito e o log diz que nao:\n%s", linhas)
		}
	})

	t.Run("selo falhado", func(t *testing.T) {
		base := aos493Store(t)
		store := &aos493SeloFalha{Store: base}
		n := aos493Compor(t, agentruntime.CompletionEnforce, aos493Guiao(nil, cortado), store)
		oc := aos493Desfecho(t, n, agentruntime.Goal{RunID: "run-493-m3-falha", Objective: "responde", MaxTurns: 2})
		if oc.Err != nil || !oc.Result.Unfulfilled {
			t.Fatalf("o run tinha de sair nao cumprido em memoria: err=%v res=%+v", oc.Err, oc.Result)
		}
		if atomic.LoadInt64(&store.falhou) == 0 {
			t.Fatal("pre-condicao: a escrita do selo tinha de ter sido tentada e falhado")
		}
		if tr := aos493UltimaTransicao(t, base, "run-493-m3-falha"); tr.To == "failed" {
			t.Fatalf("pre-condicao: o log duravel NAO pode ter o selo: %s", tr.cru)
		}
		linhas := n.linhas()
		if strings.Contains(linhas, afirma) {
			t.Fatalf("o selo FALHOU e o log do processo afirma um estado duravel que nao existe:\n%s", linhas)
		}
		iFalha := strings.Index(linhas, `selo do estado terminal do run "run-493-m3-falha" FALHOU`)
		iRun := strings.Index(linhas, `run "run-493-m3-falha" NAO CUMPRIDO (AOS-493)`)
		if iFalha < 0 || iRun < 0 || iRun < iFalha {
			t.Fatalf("queria a linha da falha do selo e, DEPOIS dela, a do run nao cumprido (falha=%d run=%d):\n%s", iFalha, iRun, linhas)
		}
		if !strings.Contains(linhas[iRun:], "O SELO NAO FOI ESCRITO: o log duravel NAO regista failed") {
			t.Fatalf("a linha do run nao cumprido tem de dizer que o selo nao foi escrito:\n%s", linhas[iRun:])
		}
		// Um selo que não foi escrito não conta como run terminado.
		for _, e := range n.svc.desfechos.estados {
			for _, rz := range n.svc.desfechos.razoes {
				if got := n.svc.desfechos.lido(e, rz); got != 0 {
					t.Fatalf("aos_runs_finished_total{%s,%s} = %d com o selo falhado; quero 0", e, rotuloDaRazao(rz), got)
				}
			}
		}
	})

	// O selo no-op: outro condutor já tinha fechado o run. A linha diz o estado que lá está.
	t.Run("selo no-op", func(t *testing.T) {
		var buf bytes.Buffer
		s := nodeServiceSoComLog(&buf)
		res := agentruntime.Result{Turns: 2, Unfulfilled: true, Verdict: aos493VeredictoNegativo(agentruntime.CompletionEnforce)}
		s.dizerNaoCumprido("run-493-m3-noop", res, state.TimedOut)
		if got := buf.String(); strings.Contains(got, afirma) || !strings.Contains(got, "o estado duravel e timed_out, e NAO failed") {
			t.Fatalf("com o selo no-op o log diz o estado que la esta: %q", got)
		}
		// E cala-se quando o run não é um não cumprido.
		buf.Reset()
		s.dizerNaoCumprido("r", agentruntime.Result{Terminated: true, Verdict: aos493VeredictoNegativo(agentruntime.CompletionObserve)}, state.Complete)
		s.dizerNaoCumprido("r", agentruntime.Result{}, state.Failed)
		if buf.Len() != 0 {
			t.Fatalf("a linha so sai para um run nao cumprido: %q", buf.String())
		}
	})
}

// ---------------------------------------------------------------------------------------------
// M4 — o registo de retoma não inventa o modo
// ---------------------------------------------------------------------------------------------

// TestAOS493_Rev_RegistoDeRetomaSemModoERecusado: um Goal sem o modo do veredicto fixado não é
// projectado num registo de retoma — nem com `off`, nem com o modo do nó. Gravar `off` por
// omissão punha o run a retomar sem veredicto, em silêncio; a recusa obriga quem escreve a
// passar por [Node.fixarConclusao].
func TestAOS493_Rev_RegistoDeRetomaSemModoERecusado(t *testing.T) {
	semModo := agentruntime.Goal{RunID: "run-493-m4", Principal: referencemonitor.Principal{NHIID: durAgent}, CompletionRequires: []string{"doc_read"}}
	if rec, err := resumeRecordFromGoal(semModo); !errors.Is(err, errResumeRecordSemModo) {
		t.Fatalf("um Goal sem modo tinha de ser recusado com errResumeRecordSemModo; veio rec=%+v err=%v", rec, err)
	}
	desconhecido := semModo
	desconhecido.CompletionMode = "impor"
	if _, err := resumeRecordFromGoal(desconhecido); !errors.Is(err, agentruntime.ErrUnknownCompletionMode) {
		t.Fatalf("um modo fora do vocabulario tinha de ser recusado; veio %v", err)
	}
	for _, modo := range []agentruntime.CompletionMode{agentruntime.CompletionOff, agentruntime.CompletionObserve, agentruntime.CompletionEnforce} {
		g := semModo
		g.CompletionMode = modo
		rec, err := resumeRecordFromGoal(g)
		if err != nil || rec.CompletionMode != modo || !reflect.DeepEqual(rec.CompletionRequires, []string{"doc_read"}) {
			t.Fatalf("modo %q: registo = %+v, err=%v", modo, rec, err)
		}
		if back := rec.GoalWith("cred"); back.CompletionMode != modo || !reflect.DeepEqual(back.CompletionRequires, []string{"doc_read"}) {
			t.Fatalf("modo %q: Goal de retoma = modo %q, contrato %v", modo, back.CompletionMode, back.CompletionRequires)
		}
	}

	// E a recusa chega ANTES do Event Store: a via única de escrita do nó não grava nada.
	store := aos493Store(t)
	n := aos493Compor(t, agentruntime.CompletionEnforce, aos493Guiao(nil), store)
	if err := n.svc.putResumeRecord(context.Background(), semModo); !errors.Is(err, errResumeRecordSemModo) {
		t.Fatalf("putResumeRecord de um Goal sem modo tinha de falhar com errResumeRecordSemModo; veio %v", err)
	}
	if _, ok, err := n.node.ResumeRecords.Get(context.Background(), semModo.RunID); ok || err != nil {
		t.Fatalf("o registo recusado nao podia ter sido gravado: ok=%v err=%v", ok, err)
	}
	// Com o modo fixado pela via de produção, grava — e grava o modo do nó.
	fixado := n.node.fixarConclusao(semModo)
	if err := n.svc.putResumeRecord(context.Background(), fixado); err != nil {
		t.Fatalf("putResumeRecord depois de fixarConclusao: %v", err)
	}
	rec, ok, err := n.node.ResumeRecords.Get(context.Background(), semModo.RunID)
	if err != nil || !ok || rec.CompletionMode != agentruntime.CompletionEnforce {
		t.Fatalf("registo gravado: modo=%q ok=%v err=%v; quero enforce", rec.CompletionMode, ok, err)
	}
}
