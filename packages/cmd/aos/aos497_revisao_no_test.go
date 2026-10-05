package main

// AOS-497 — O QUE A REVISÃO ADVERSARIAL ENCONTROU, PELO NÓ COMPOSTO.
//
// Cada teste fixa um achado da revisão (os identificadores são os dela):
//
//   - I1: um nome de tool que o tool set aceita e a âncora selada recusaria recusa o ARRANQUE;
//     nunca um run termina em memória e fica sem transição terminal;
//   - I2: o `GET /runs/{id}` em memória responde `failed` a um run recusado por origem
//     impossível ou mal formada, como o log durável;
//   - I3: o digest selado é o `result_hash` do step-ledger — numa só vida, depois de escalada e
//     aprovação, e depois de falha e retoma. Nos dois últimos a captura do turno NÃO tem os
//     bytes designados: quem os servir tem de os ler de onde o digest confere;
//   - M9/R27: a métrica da origem só conta um run cujo desfecho ficou selado.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/replay"
	"github.com/aos-ref/kernel/agent-runtime/state"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/platform/registry/domain"
)

// aos497ResultHashes devolve o `result_hash` de cada `step.ledger.applied` do run, pela chave
// de idempotência (`<run_id>:<step_id>`).
func aos497ResultHashes(t *testing.T, store state.EventStore, runID string) map[string]string {
	t.Helper()
	evs, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	out := map[string]string{}
	for _, ev := range evs {
		if ev.Type != "step.ledger.applied" {
			continue
		}
		var r struct {
			Key  string `json:"key"`
			Hash string `json:"result_hash"`
		}
		if err := json.Unmarshal(ev.Payload, &r); err != nil {
			t.Fatalf("step.ledger.applied ilegivel: %v", err)
		}
		out[r.Key] = r.Hash
	}
	return out
}

// aos497DigestsDaCaptura devolve o digest dos bytes de cada resultado de tool que a captura do
// run devolve, decifrada por-titular.
func aos497DigestsDaCaptura(t *testing.T, node *Node, runID string) map[string]bool {
	t.Helper()
	eng, err := replay.NewEngine(node.EventStore, replay.WithContentOpener(node.contentOpener, replay.Accessor{
		Principal: "nhi:leitor-aos497", Scopes: []string{replay.DefaultSovereignContentScope},
	}))
	if err != nil {
		t.Fatalf("replay.NewEngine: %v", err)
	}
	turnos, err := eng.Reconstruct(context.Background(), runID)
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}
	if len(turnos) == 0 {
		t.Fatal("a captura do run nao tem turnos")
	}
	out := map[string]bool{}
	for _, turno := range turnos {
		for _, r := range turno.ToolResults {
			out[aos497Digest(string(r.Value))] = true
		}
	}
	return out
}

// aos497ExigirDigestDoLedger: a âncora é designada, é a que está selada na transição terminal,
// e o seu digest é `"sha256:" + result_hash` do step-ledger do passo designado.
func aos497ExigirDigestDoLedger(t *testing.T, store state.EventStore, runID string, a *agentruntime.OutputSource) {
	t.Helper()
	if a == nil || a.State != agentruntime.OutputSourceDesignated {
		t.Fatalf("ancora = %+v; quero designada", a)
	}
	if _, selada := aos497AncoraDoLog(t, store, runID); selada == nil || *selada != *a {
		t.Fatalf("ancora selada = %+v; a do Result e %+v", selada, a)
	}
	hashes := aos497ResultHashes(t, store, runID)
	hash, ok := hashes[runID+":"+a.StepID]
	if !ok {
		t.Fatalf("o step-ledger nao tem o passo designado %q: %v", a.StepID, hashes)
	}
	if "sha256:"+hash != a.Digest {
		t.Fatalf("o digest selado (%s) nao e o result_hash do step-ledger do passo %s (%s): quem servir os bytes do ledger deixa de os conseguir conferir",
			a.Digest, a.StepID, hash)
	}
}

// TestAOS497_No_DigestSelado_EOResultHashDoLedger_NumaVida: uma só vida, via durável, captura
// selada. Os três batem: a âncora, o step-ledger e os bytes que a captura decifrada devolve.
func TestAOS497_No_DigestSelado_EOResultHashDoLedger_NumaVida(t *testing.T) {
	leitura := agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{aos493Counter("tick")}, StopReason: agentruntime.StopToolCalls}
	fim := agentruntime.ModelResponse{Text: "um resumo", StopReason: agentruntime.StopStop}
	store := aos493Store(t)
	n := aos493Compor(t, agentruntime.CompletionEnforce, aos493Guiao(nil, leitura, fim), store)
	const runID = "run-497-ledger-uma-vida"
	oc := aos493Desfecho(t, n, agentruntime.Goal{RunID: runID, Objective: "le", MaxTurns: 4, OutputFromTool: "counter", OutputSourceBinding: agentruntime.OutputSourceBinds})
	if oc.Err != nil {
		t.Fatalf("erro de loop: %v", oc.Err)
	}
	a := oc.Result.OutputSource
	aos497ExigirDigestDoLedger(t, store, runID, a)
	if !aos497DigestsDaCaptura(t, n.node, runID)[a.Digest] {
		t.Fatalf("numa so vida a captura decifrada tinha de devolver os bytes designados (%s)", a.Digest)
	}
}

// TestAOS497_No_DigestSelado_EOResultHashDoLedger_EscaladaEAprovacao (revisão, I3): o turno
// designado ESCALA na primeira vida e só executa depois da aprovação four-eyes, numa retoma. A
// âncora é designada e o seu digest é o `result_hash` do step-ledger. A captura do turno é a da
// primeira vida (a escalada, sem resultado): os bytes designados NÃO estão lá.
func TestAOS497_No_DigestSelado_EOResultHashDoLedger_EscaladaEAprovacao(t *testing.T) {
	ctx := context.Background()
	h := newACNHarness(t)
	h.node.completionVerdict = agentruntime.CompletionEnforce
	if err := h.svc.Submit(ctx, agentruntime.Goal{
		RunID: acnRunID, Principal: referencemonitor.Principal{NHIID: acnAgent}, Credential: h.token(t),
		Objective: "dois passos governados", MaxTurns: 5,
		OutputFromTool: "passo_um", OutputSourceBinding: agentruntime.OutputSourceBinds,
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	h.esperaFim(t, acnRunID)
	if _, susp := h.svc.Suspended(ctx, acnRunID); !susp {
		t.Fatal("o turno 1 tinha de escalar e suspender o run")
	}
	for i, pedido := range []string{"req-497-1", "req-497-2"} {
		h.aprovaPendente(t, pedido)
		if err := h.svc.Resume(ctx, acnRunID, h.token(t)); err != nil {
			t.Fatalf("Resume %d: %v", i+1, err)
		}
		h.esperaFim(t, acnRunID)
	}
	fim, done := h.svc.Outcome(acnRunID)
	if !done || !fim.Result.Terminated || fim.Result.Unfulfilled {
		t.Fatalf("o run tinha de concluir depois das duas aprovacoes: done=%v res=%+v (veredicto %+v)", done, fim.Result, fim.Result.Verdict)
	}
	if h.execucoes("passo_um") != 1 {
		t.Fatalf("o passo designado tinha de executar uma so vez; executou %d", h.execucoes("passo_um"))
	}
	a := fim.Result.OutputSource
	aos497ExigirDigestDoLedger(t, h.node.EventStore, acnRunID, a)
	// O LIMITE (ADR-038 §5): a captura do turno designado é a da vida em que escalou. Se este
	// passo falhar por a captura passar a ter os bytes, o limite deixou de existir — actualiza
	// o ADR-038 e este teste.
	if aos497DigestsDaCaptura(t, h.node, acnRunID)[a.Digest] {
		t.Fatal("a captura passou a ter os bytes designados de um turno que escalou: o limite do ADR-038 §5 mudou")
	}
}

// TestAOS497_No_DigestSelado_EOResultHashDoLedger_FalhaERetoma (revisão, I3): a tool FALHA no
// turno 1 da primeira vida; o nó morre; a segunda vida volta a despachar a chamada e ela tem
// êxito. A âncora é designada, com o digest dos bytes da segunda vida — que é o `result_hash`
// do step-ledger. A captura do turno é a da primeira vida (a falha).
func TestAOS497_No_DigestSelado_EOResultHashDoLedger_FalhaERetoma(t *testing.T) {
	pinBreakerEnv(t, "0", "0", "0", "0")
	ctx := context.Background()
	const runID = "plan-497~falha-e-retoma"
	approvers := crashResumeApprovers(t)
	goal := agentruntime.Goal{
		RunID: runID, Principal: referencemonitor.Principal{NHIID: durAgent},
		Model: agentruntime.ModelConfig{ModelID: "modelo-486"}, Objective: "o trabalho de um no do plano", MaxTurns: 4,
		AllowedTools: []string{"beta", "counter"}, CompletionRequires: []string{"counter"}, CompletionMode: agentruntime.CompletionEnforce,
		OutputFromTool: "counter", OutputSourceBinding: agentruntime.OutputSourceBinds,
	}
	const vida2 = "documento da vida 2"
	var execs int64
	counter := func([]byte) ([]byte, error) {
		if atomic.AddInt64(&execs, 1) == 1 {
			return nil, errors.New("timeout a jusante")
		}
		return []byte(vida2), nil
	}
	store := aos493Store(t)
	vault := audit.NewInMemoryKeyVault(nil)
	inc1 := aos493Incarnar(t, store, vault, approvers, counter)
	prod := goal
	prod.Credential = inc1.cred
	prod.MaxTurns = 1
	if _, _, rerr := inc1.node.Runtime.Run(withRunToolAllowlist(ctx, goal.AllowedTools), prod, nil); !errors.Is(rerr, agentruntime.ErrMaxTurnsExceeded) {
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
	rec, err := resumeRecordFromGoal(goal)
	if err != nil {
		t.Fatalf("resumeRecordFromGoal: %v", err)
	}
	if err := inc1.node.ResumeRecords.Put(ctx, rec); err != nil {
		t.Fatalf("semear o registo de retoma: %v", err)
	}
	_ = inc1.node.Close()

	inc2 := aos493Incarnar(t, store, vault, approvers, counter)
	t.Cleanup(func() { _ = inc2.node.Close() })
	svc2, _ := aos493ServicoComLog(t, inc2.node)
	svc2.mu.Lock()
	svc2.suspended[runID] = &runState{runID: runID, suspended: true, done: make(chan struct{})}
	svc2.mu.Unlock()
	if err := svc2.Resume(ctx, runID, inc2.cred); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	res := aos493Esperar(t, svc2, runID).Result
	if execs != 2 || !res.Terminated || res.Unfulfilled {
		t.Fatalf("a segunda vida tinha de voltar a despachar a chamada e concluir: execs=%d res=%+v", execs, res)
	}
	a := res.OutputSource
	aos497ExigirDigestDoLedger(t, store, runID, a)
	if a.Digest != aos497Digest(vida2) || a.Bytes != len(vida2) {
		t.Fatalf("o digest selado tinha de ser o dos bytes que o despacho devolveu NESTA vida (%s): %+v", aos497Digest(vida2), a)
	}
	// O LIMITE (ADR-038 §5), como no teste da escalada.
	if aos497DigestsDaCaptura(t, inc2.node, runID)[a.Digest] {
		t.Fatal("a captura passou a ter os bytes designados de um turno que falhou na primeira vida: o limite do ADR-038 §5 mudou")
	}
}

// TestAOS497_No_NomeQueOSeloRecusa_RecusaOArranque (revisão, I1): a tool declarada existe no
// catálogo com um nome que a âncora selada não admite (um espaço; mais de 128 bytes). Antes, o
// run corria, terminava em memória e a transição terminal era recusada pela máquina de estados:
// o log ficava em `running`. Agora é recusado no arranque, nos dois vínculos, e selado `failed`.
func TestAOS497_No_NomeQueOSeloRecusa_RecusaOArranque(t *testing.T) {
	for _, nome := range []string{strings.Repeat("a", 129), "ler documento"} {
		for _, vinculo := range agentruntime.OutputSourceBindings() {
			t.Run(nome[:3]+"/"+string(vinculo), func(t *testing.T) {
				pinBreakerEnv(t, "0", "0", "0", "0")
				store := aos493Store(t)
				var interrogacoes int64
				call := agentruntime.ToolInvocation{ToolID: nome, Capability: durCap, Input: []byte("x")}
				modelo := aos493Guiao(&interrogacoes,
					agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{call}, StopReason: agentruntime.StopToolCalls},
					agentruntime.ModelResponse{Text: "um resumo", StopReason: agentruntime.StopStop})
				signer := durSigner(t)
				node, cred := obsPermitNodeWith(t, "", modelo, func(cfg *Config) {
					cfg.EventStore = store
					cfg.DSARVault = audit.NewInMemoryKeyVault(nil)
					cfg.DurableExecution = true
					cfg.Approvers = crashResumeApprovers(t)
					cfg.CompletionVerdict = agentruntime.CompletionEnforce
					cfg.Catalog = catalogStub{entries: []domain.Entry{aos486Entry(signer, nome)}}
				})
				t.Cleanup(func() { _ = node.Close() })
				if err := node.Runtime.Register(nome, func(context.Context, []byte) ([]byte, error) { return []byte("pong"), nil }); err != nil {
					t.Fatalf("Register: %v", err)
				}
				svc, _ := aos493ServicoComLog(t, node)
				runID := "run-497-nome-" + string(vinculo)
				if err := svc.Submit(context.Background(), agentruntime.Goal{RunID: runID, Principal: referencemonitor.Principal{NHIID: durAgent}, Credential: cred,
					Objective: "le", MaxTurns: 4, OutputFromTool: nome, OutputSourceBinding: vinculo}); err != nil {
					t.Fatalf("Submit: %v", err)
				}
				wc, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				oc, ok, err := svc.Wait(wc, runID)
				if err != nil || !ok {
					t.Fatalf("Wait: ok=%v err=%v", ok, err)
				}
				if !errors.Is(oc.Err, agentruntime.ErrImpossibleOutputSource) {
					t.Fatalf("o run tinha de ser recusado no arranque com ErrImpossibleOutputSource; veio err=%v res=%+v", oc.Err, oc.Result)
				}
				if strings.Contains(oc.Err.Error(), nome) {
					t.Fatalf("o erro repete o nome recusado: %q", oc.Err)
				}
				if oc.Result.Terminated || oc.Result.Turns != 0 || oc.Result.OutputSource != nil || atomic.LoadInt64(&interrogacoes) != 0 {
					t.Fatalf("o run recusado nao da turnos, nao interroga o modelo e nao tem ancora: %+v (interrogacoes %d)", oc.Result, interrogacoes)
				}
				// O LOG DURÁVEL TEM A TRANSIÇÃO TERMINAL. É o que faltava: `running` para sempre.
				tr, selada := aos497AncoraDoLog(t, store, runID)
				if tr.To != "failed" || tr.Reason != reasonRunFailed || selada != nil {
					t.Fatalf("transicao terminal = %s; quero failed (%s) sem ancora", tr.cru, reasonRunFailed)
				}
			})
		}
	}
}

// TestAOS497_OrigemImpossivelOuMalFormada_RespondeFailed (revisão, I2): o `GET /runs/{id}` em
// memória de um run que o kernel recusou por origem impossível ou mal formada. Respondia
// `completed` enquanto o log durável dizia `failed` — o defeito que o AOS-494 corrigiu para o
// contrato impossível (`TestAOS494_ContratoImpossivel_RespondeFailed`), repetido para os dois
// erros novos.
func TestAOS497_OrigemImpossivelOuMalFormada_RespondeFailed(t *testing.T) {
	for _, c := range []struct {
		nome, origem string
		vinculo      agentruntime.OutputSourceBinding
		erro         error
		diz          string
	}{
		{"fora do tool set, so medicao", "doc_read", agentruntime.OutputSourceMeasure, agentruntime.ErrImpossibleOutputSource, "origem da saida impossivel"},
		{"fora do tool set, vinculativa", "doc_read", agentruntime.OutputSourceBinds, agentruntime.ErrImpossibleOutputSource, "origem da saida impossivel"},
		{"nome que o selo recusa", "ler documento", agentruntime.OutputSourceMeasure, agentruntime.ErrImpossibleOutputSource, "origem da saida impossivel"},
		{"origem sem vinculo", "doc_read", "", agentruntime.ErrBadOutputSourceBinding, "declaracao de origem da saida mal formada"},
		{"vinculo sem origem", "", agentruntime.OutputSourceBinds, agentruntime.ErrBadOutputSourceBinding, "declaracao de origem da saida mal formada"},
	} {
		t.Run(c.nome, func(t *testing.T) {
			var chamadas int32
			modelo := agentruntime.ModelClientFunc(func(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
				atomic.AddInt32(&chamadas, 1)
				return agentruntime.ModelResponse{Text: "nao devia ter sido interrogado", Final: true, StopReason: agentruntime.StopStop}, nil
			})
			node, _ := newAPINode(t, modelo, false)
			node.completionVerdict = agentruntime.CompletionEnforce
			svc, h := newAPI(t, node)
			const runID = "aos497-origem-recusada"
			if err := svc.Submit(context.Background(), agentruntime.Goal{RunID: runID, Objective: "ler", Principal: referencemonitor.Principal{NHIID: "nhi:agent-1"},
				MaxTurns: 3, OutputFromTool: c.origem, OutputSourceBinding: c.vinculo}); err != nil {
				t.Fatalf("Submit: %v", err)
			}
			wc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			oc, ok, err := svc.Wait(wc, runID)
			if err != nil || !ok || !errors.Is(oc.Err, c.erro) {
				t.Fatalf("Wait: ok=%v err=%v erro de loop=%v; quero %v", ok, err, oc.Err, c.erro)
			}
			var r aos494Resposta
			get := postJSON(h, http.MethodGet, "/runs/"+runID, nil)
			if err := json.Unmarshal(get.Body.Bytes(), &r); err != nil || get.Code != http.StatusOK {
				t.Fatalf("GET: %d %v (%s)", get.Code, err, get.Body.String())
			}
			if r.Status != "failed" || r.Terminated || r.FinalText != "" || !strings.Contains(r.Error, c.diz) {
				t.Fatalf("a origem recusada responde failed, com o erro do kernel e sem texto; veio %+v", r)
			}
			if n := atomic.LoadInt32(&chamadas); n != 0 {
				t.Fatalf("o modelo foi interrogado %d vez(es) num run que o kernel recusou a partida", n)
			}
			// E o log durável, quando o nó o tem, diz o mesmo.
			if node.EventStore != nil {
				m, err := state.NewMachine(node.EventStore, runID)
				if err != nil {
					t.Fatalf("NewMachine: %v", err)
				}
				if st, _, err := m.RebuildOutcome(context.Background()); err != nil || st != state.Failed {
					t.Fatalf("estado duravel = %q, %v; quero failed", st, err)
				}
			}
		})
	}
}

// TestAOS497_Metrica_SoContaComSelo (revisão, M9/R27): `aos_runs_output_source_total` conta os
// runs que o processo SELOU. Um desfecho com âncora cujo selo foi no-op (o run ficou parado, ou
// a transição não aconteceu) não conta: a âncora não está no log, e a métrica não pode dizer
// mais do que o registo. Um par fora dos vocabulários também não cria série.
func TestAOS497_Metrica_SoContaComSelo(t *testing.T) {
	ancora := func(v agentruntime.OutputSourceBinding, e agentruntime.OutputSourceState) agentruntime.Result {
		return agentruntime.Result{OutputSource: &agentruntime.OutputSource{Tool: "doc_read", Binding: v, State: e}}
	}
	d := novoDesfechosDeRuns()
	for _, semSelo := range []state.State{state.Running, state.Paused, state.Ready, ""} {
		d.contar(semSelo, ancora(agentruntime.OutputSourceMeasure, agentruntime.OutputSourceMissing))
	}
	for _, vinculo := range agentruntime.OutputSourceBindings() {
		for _, estado := range agentruntime.OutputSourceStates() {
			if got := d.lidoOrigem(vinculo, estado); got != 0 {
				t.Fatalf("um run sem selo terminal contou em {%s,%s}: %d", vinculo, estado, got)
			}
		}
	}
	// Com selo, nos três estados terminais contados, e em todos os estados da designação.
	d.contar(state.Complete, ancora(agentruntime.OutputSourceMeasure, agentruntime.OutputSourceMissing))
	d.contar(state.Failed, ancora(agentruntime.OutputSourceBinds, agentruntime.OutputSourceAmbiguous))
	d.contar(state.TimedOut, ancora(agentruntime.OutputSourceMeasure, agentruntime.OutputSourceDesignated))
	d.contar(state.Complete, ancora(agentruntime.OutputSourceMeasure, agentruntime.OutputSourceInapplicable))
	d.contar(state.Failed, ancora(agentruntime.OutputSourceBinds, agentruntime.OutputSourceInapplicable))
	// Fora dos vocabulários, e sem âncora: nada.
	d.contar(state.Complete, ancora("enforce", agentruntime.OutputSourceMissing))
	d.contar(state.Complete, ancora(agentruntime.OutputSourceMeasure, "Designated"))
	d.contar(state.Complete, agentruntime.Result{})
	quer := map[chaveOrigem]int64{
		{agentruntime.OutputSourceMeasure, agentruntime.OutputSourceMissing}:      1,
		{agentruntime.OutputSourceBinds, agentruntime.OutputSourceAmbiguous}:      1,
		{agentruntime.OutputSourceMeasure, agentruntime.OutputSourceDesignated}:   1,
		{agentruntime.OutputSourceMeasure, agentruntime.OutputSourceInapplicable}: 1,
		{agentruntime.OutputSourceBinds, agentruntime.OutputSourceInapplicable}:   1,
	}
	total := 0
	for _, vinculo := range agentruntime.OutputSourceBindings() {
		for _, estado := range agentruntime.OutputSourceStates() {
			total++
			if got := d.lidoOrigem(vinculo, estado); got != quer[chaveOrigem{vinculo, estado}] {
				t.Fatalf("{%s,%s} = %d; quero %d", vinculo, estado, got, quer[chaveOrigem{vinculo, estado}])
			}
		}
	}
	if total != 8 || len(d.origens) != 8 {
		t.Fatalf("queria 8 series (2 vinculos x 4 estados): percorridas %d, contadores %d", total, len(d.origens))
	}
}
