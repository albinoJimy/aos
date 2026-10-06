package main

// AOS-502 — O NÓ ACEITA A NOVA TENTATIVA DE UM NÓ DO PLANO, E PROVA NO SEU LOG QUE A ANTERIOR NÃO
// PEDIU TOOLS (ADR-039).
//
// Os testes correm pela API HTTP do nó composto de produção: gate soberano, fila de pedidos de
// plano com reclamação, cadeia de mediação real, execução durável, e o ADAPTADOR REAL do Model
// Gateway contra um provider falso — é ele que diz o motivo de paragem de cada turno. A forma é
// a de produção: nó `read_notes`, tool `doc_read`.
//
// Cada causa de recusa tem o LOG REAL de um run nesse estado sempre que um run real o consegue
// produzir. As que nenhum run produz a pedido (mais de um turno sem tool calls, `timed_out`, uma
// origem de outro pedido sob o mesmo id) partem dos eventos de um run real e mudam UM facto:
// estão em [TestAOS502_AProvaRecusa_UmFactoDeCadaVez].

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

const (
	aos502Submissor = "sub-alice-502"
	aos502Leitura   = "doc_read"
	aos502Escrita   = "doc_write"
	aos502No_       = "read_notes"
	aos502PlanID    = "plano-do-aos-orq-502"
)

// aos502No é o nó composto destes testes: o [aos486No] soberano, com o tecto de tentativas dado.
type aos502No struct {
	*aos486No
	tecto int
}

// aos502Compor levanta o nó com as tools dadas (por omissão, só `doc_read`), o modo do veredicto
// e o tecto de tentativas a mais. Tecto zero ⇒ a opção não se compõe, como no arranque.
func aos502Compor(t *testing.T, modo agentruntime.CompletionMode, tecto int, tools ...string) *aos502No {
	t.Helper()
	return aos502ComporCom(t, modo, tecto, nil, tools...)
}

// aos502ComporCom é o [aos502Compor] com um ajuste à configuração do nó antes do arranque.
func aos502ComporCom(t *testing.T, modo agentruntime.CompletionMode, tecto int, ajuste func(*Config), tools ...string) *aos502No {
	t.Helper()
	pinBreakerEnv(t, "0", "0", "0", "0")
	if len(tools) == 0 {
		tools = []string{aos502Leitura}
	}
	ordem, extra := aos486OrdemDoFicheiro, aos486CamposExtraDaSpec
	aos486OrdemDoFicheiro = tools
	aos486CamposExtraDaSpec = `,"egress":"none","reversibility":"reversible","mutation":"none"`
	t.Cleanup(func() { aos486OrdemDoFicheiro, aos486CamposExtraDaSpec = ordem, extra })
	n := aos486ComporCom(t, "native", func(cfg *Config) {
		cfg.CompletionVerdict = modo
		cfg.BoardRegions = map[string]string{govBoard: govRegion}
		if ajuste != nil {
			ajuste(cfg)
		}
	})
	cat, err := catalogoDeToolsDoAmbiente()
	if err != nil {
		t.Fatalf("catalogoDeToolsDoAmbiente: %v", err)
	}
	opts := []APIOption{WithToolCatalog(cat)}
	if tecto > 0 {
		opts = append(opts, WithRunRetryMax(tecto))
	}
	if n.h, err = NewAPIHandler(n.svc, n.node, opts...); err != nil {
		t.Fatalf("NewAPIHandler: %v", err)
	}
	return &aos502No{aos486No: n, tecto: tecto}
}

// aos502Completion é a resposta do provider com o texto e o motivo de paragem dados, sem tool
// calls. `finish` vazio ⇒ o provider não reporta o motivo.
func aos502Completion(texto, finish string) []byte {
	conteudo, _ := json.Marshal(texto)
	motivo := `null`
	if finish != "" {
		motivo = `"` + finish + `"`
	}
	return []byte(`{"id":"cmpl-502","object":"chat.completion","model":"gpt-4o",` +
		`"choices":[{"index":0,"message":{"role":"assistant","content":` + string(conteudo) + `},"finish_reason":` + motivo + `}],` +
		`"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
}

// modeloNaoChama põe o provider a responder texto sem tool call, com o motivo de paragem dado.
func (n *aos502No) modeloNaoChama(texto, finish string) {
	n.upstream.mu.Lock()
	defer n.upstream.mu.Unlock()
	n.upstream.pede = ""
	n.upstream.responde = func(bool, string) []byte { return aos502Completion(texto, finish) }
}

// modeloChama põe o provider a pedir `tool` no primeiro turno de cada run e a concluir depois.
func (n *aos502No) modeloChama(tool string) {
	n.upstream.mu.Lock()
	defer n.upstream.mu.Unlock()
	n.upstream.pede, n.upstream.responde = tool, nil
}

// pedir submete um plano como o submissor e reclama-o como o drenador. Devolve a geração.
func (n *aos502No) pedir(t *testing.T, plano string) int {
	t.Helper()
	if r := postReq(n.h, "/plans", map[string]any{"run_id": plano, "objective": "ler o documento notes"}, aos439Headers(aos502Submissor)); r.Code != http.StatusCreated {
		t.Fatalf("POST /plans: %d %s", r.Code, r.Body.String())
	}
	return n.reclamar(t, plano)
}

// reclamar reclama o pedido `plano` como o drenador e devolve a geração.
func (n *aos502No) reclamar(t *testing.T, plano string) int {
	t.Helper()
	r := postReq(n.h, "/plans/claim", nil, govHeaders())
	if r.Code != http.StatusOK {
		t.Fatalf("POST /plans/claim: %d %s", r.Code, r.Body.String())
	}
	var p respostaDeReclamo
	if err := json.Unmarshal(r.Body.Bytes(), &p); err != nil || p.RunID != plano {
		t.Fatalf("reclamado %+v (%v), esperava %s", p, err, plano)
	}
	return p.Geracao
}

// vinculo é o `plan_request` da tentativa `n` do nó `no` do plano (0 ⇒ a primeira, sem o campo).
func aos502Vinculo(plano, no string, geracao, tentativa int) *vinculoAoPedido {
	return &vinculoAoPedido{RunID: plano, Geracao: geracao, PlanID: aos502PlanID, NodeID: no, Attempt: tentativa}
}

// submeter envia o `POST /runs` de um nó do plano como o drenador, com a forma de produção.
func (n *aos502No) submeter(t *testing.T, runID string, v *vinculoAoPedido, tools, contrato []string) (int, string) {
	t.Helper()
	corpo := map[string]any{
		"run_id": runID, "objective": "Le o documento notes", "principal_nhi": durAgent, "credential": n.tok,
		"tools": tools, "inputs": []any{},
	}
	if v != nil {
		corpo["plan_request"] = v
	}
	if len(contrato) > 0 {
		corpo["completion_requires"] = contrato
	}
	r := postReq(n.h, "/runs", corpo, govHeaders())
	return r.Code, strings.TrimSpace(r.Body.String())
}

// esperar espera o fim de um run hospedado.
func (n *aos502No) esperar(t *testing.T, runID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, ok, err := n.svc.Wait(ctx, runID); err != nil || !ok {
		t.Fatalf("Wait(%s): ok=%t err=%v", runID, ok, err)
	}
}

// ler devolve o `GET /runs/{id}` como o drenador.
func (n *aos502No) ler(t *testing.T, runID string) ([]byte, aos494Resposta) {
	t.Helper()
	get := getReq(n.h, "/runs/"+runID, govHeaders())
	if get.Code != http.StatusOK {
		t.Fatalf("GET /runs/%s: %d (%s)", runID, get.Code, get.Body.String())
	}
	var r aos494Resposta
	if err := json.Unmarshal(get.Body.Bytes(), &r); err != nil {
		t.Fatalf("resposta ilegivel: %v", err)
	}
	return get.Body.Bytes(), r
}

// primeira corre a PRIMEIRA tentativa do nó do plano, com o provider no estado em que estiver, e
// devolve o desfecho. O contrato é a lista-branca inteira, como o `aos-orq` o declara.
func (n *aos502No) primeira(t *testing.T, plano string, geracao int, tools ...string) aos494Resposta {
	t.Helper()
	if len(tools) == 0 {
		tools = []string{aos502Leitura}
	}
	runID := idDaTentativa(plano, aos502No_, 1)
	if codigo, corpo := n.submeter(t, runID, aos502Vinculo(plano, aos502No_, geracao, 0), tools, tools); codigo != http.StatusCreated {
		t.Fatalf("a primeira tentativa tinha de ser aceite (201), veio %d %s", codigo, corpo)
	}
	n.esperar(t, runID)
	_, r := n.ler(t, runID)
	return r
}

// falharSemChamar corre a primeira tentativa com a resposta de produção (um turno, sem tool
// call, motivo `stop`) e confirma o estado de que a prova precisa.
func (n *aos502No) falharSemChamar(t *testing.T, plano string, geracao int) {
	t.Helper()
	n.modeloNaoChama(aos493RespostasDeProducao(t)["plan-e2e-v0145-1791115918"], "stop")
	r := n.primeira(t, plano, geracao)
	if r.Status != "failed" || r.OutcomeReason != string(agentruntime.OutcomeContractNoCall) || r.Verdict == nil || r.Verdict.ToolCallsRequested != 0 {
		t.Fatalf("pre-condicao: a primeira tentativa tinha de fechar failed por contract_unmet_no_call sem tool calls; veio %+v %+v", r, r.Verdict)
	}
}

// pedirTentativa envia o pedido da tentativa `n` com a forma de produção.
func (n *aos502No) pedirTentativa(t *testing.T, plano string, geracao, tentativa int) (int, string) {
	t.Helper()
	return n.submeter(t, idDaTentativa(plano, aos502No_, tentativa), aos502Vinculo(plano, aos502No_, geracao, tentativa),
		[]string{aos502Leitura}, []string{aos502Leitura})
}

// metricas devolve o `/metrics` do nó.
func (n *aos502No) metricas(t *testing.T) string {
	t.Helper()
	r := getReq(n.h, "/metrics", govHeaders())
	if r.Code != http.StatusOK {
		t.Fatalf("GET /metrics: %d", r.Code)
	}
	return r.Body.String()
}

// aos502TemSerie diz se o `/metrics` tem a série com esse valor, numa linha inteira.
func aos502TemSerie(metricas, serie string, valor int) bool {
	return strings.Contains("\n"+metricas, fmt.Sprintf("\n%s %d\n", serie, valor))
}

// existe diz se o nó tem algum evento no stream desse run.
func (n *aos502No) existe(t *testing.T, runID string) bool {
	t.Helper()
	evs, err := n.node.EventStore.Read(context.Background(), runID, 1)
	return err == nil && len(evs) > 0
}

// aos502SoRetry devolve só as linhas das séries da nova tentativa, para as mensagens de erro.
func aos502SoRetry(metricas string) string {
	var linhas []string
	for _, l := range strings.Split(metricas, "\n") {
		if strings.HasPrefix(l, "aos_runs_retry_") {
			linhas = append(linhas, l)
		}
	}
	return strings.Join(linhas, "\n")
}

// origemDe lê o `run.plan_origin` do run.
func (n *aos502No) origemDe(t *testing.T, runID string) (origemDoRunFilho, []byte) {
	t.Helper()
	evs, err := n.node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("ler o stream de %s: %v", runID, err)
	}
	for _, ev := range evs {
		if ev.Type == EventTypeRunPlanOrigin {
			var o origemDoRunFilho
			if err := json.Unmarshal(ev.Payload, &o); err != nil {
				t.Fatalf("run.plan_origin ilegivel: %v", err)
			}
			return o, ev.Payload
		}
	}
	t.Fatalf("o run %s nao tem run.plan_origin", runID)
	return origemDoRunFilho{}, nil
}

// TestAOS502_FalhaFalhaSucesso é o caminho inteiro no nó: a primeira e a segunda tentativas
// fecham sem chamar a tool (a resposta real de produção), a terceira chama-a e conclui. O nó
// admite as duas tentativas a mais porque provou, de cada vez, que a anterior não pediu tools.
func TestAOS502_FalhaFalhaSucesso(t *testing.T) {
	n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
	const plano = "plano-502-ffs"
	ger := n.pedir(t, plano)
	n.falharSemChamar(t, plano, ger)

	// TENTATIVA 2: admitida, e volta a falhar sem chamar.
	if codigo, corpo := n.pedirTentativa(t, plano, ger, 2); codigo != http.StatusCreated {
		t.Fatalf("a tentativa 2 tinha de ser admitida (201); veio %d %s", codigo, corpo)
	}
	id2 := idDaTentativa(plano, aos502No_, 2)
	n.esperar(t, id2)
	if _, r := n.ler(t, id2); r.Status != "failed" || r.OutcomeReason != string(agentruntime.OutcomeContractNoCall) {
		t.Fatalf("a tentativa 2 repete a resposta de producao e tem de fechar failed por contract_unmet_no_call; veio %+v", r)
	}
	o2, _ := n.origemDe(t, id2)
	if o2.Attempt != 2 || o2.RetryOf != idDaTentativa(plano, aos502No_, 1) || o2.NodeID != aos502No_ || o2.Pedido.RunID != plano {
		t.Fatalf("o run.plan_origin da tentativa 2 leva attempt=2 e retry_of=<a primeira>; veio %+v", o2)
	}

	// TENTATIVA 3: admitida sobre a 2, e desta vez o modelo chama a tool.
	n.modeloChama(aos502Leitura)
	if codigo, corpo := n.pedirTentativa(t, plano, ger, 3); codigo != http.StatusCreated {
		t.Fatalf("a tentativa 3 tinha de ser admitida (201); veio %d %s", codigo, corpo)
	}
	id3 := idDaTentativa(plano, aos502No_, 3)
	n.esperar(t, id3)
	_, r3 := n.ler(t, id3)
	if r3.Status != "completed" || !r3.Terminated || r3.Verdict == nil || !r3.Verdict.Fulfilled {
		t.Fatalf("a tentativa 3 chama a tool e conclui; veio %+v %+v", r3, r3.Verdict)
	}
	o3, _ := n.origemDe(t, id3)
	if o3.Attempt != 3 || o3.RetryOf != id2 {
		t.Fatalf("o run.plan_origin da tentativa 3 leva attempt=3 e retry_of=<a 2>; veio %+v", o3)
	}
	if execs := *n.execs[aos502Leitura]; execs != 1 {
		t.Fatalf("a tool correu exactamente uma vez em tres runs (as duas primeiras nao a pediram); correu %d", execs)
	}

	// As três tentativas são três runs com três streams; a primeira não leva os campos novos.
	_, cru1 := n.origemDe(t, idDaTentativa(plano, aos502No_, 1))
	if strings.Contains(string(cru1), "attempt") || strings.Contains(string(cru1), "retry_of") {
		t.Fatalf("o run.plan_origin de um run SEM tentativa tem de ter os bytes de sempre; veio %s", cru1)
	}
	m := n.metricas(t)
	if !aos502TemSerie(m, "aos_runs_retry_admitted_total", 2) {
		t.Fatalf("duas tentativas admitidas; metricas:\n%s", aos502SoRetry(m))
	}
	for _, causa := range causasDeRecusaDaTentativa {
		if !aos502TemSerie(m, `aos_runs_retry_refused_total{causa="`+causa+`"}`, 0) {
			t.Fatalf("nenhuma recusa; a causa %q nao esta a zero:\n%s", causa, aos502SoRetry(m))
		}
	}
	// O PEDIDO REPETIDO É O MESMO: o hash do prompt das três tentativas é igual, e a medição do
	// nó (assíncrona) não conta diferença nenhuma.
	hashes := map[string]bool{}
	for i := 1; i <= 3; i++ {
		evs, _ := n.node.EventStore.Read(context.Background(), idDaTentativa(plano, aos502No_, i), 1)
		for _, tr := range aos486LerTurnos(t, evs)[:1] {
			hashes[tr.promptHash] = true
		}
	}
	if len(hashes) != 1 {
		t.Fatalf("o primeiro turno das tres tentativas tem de ter o MESMO prompt_hash; vieram %d distintos", len(hashes))
	}
	// A medição corre fora do pedido; o caso em que ela CONTA está em
	// [TestAOS502_OPromptDiferenteConta]. Aqui dá-se-lhe tempo e confirma-se que ficou a zero.
	time.Sleep(300 * time.Millisecond)
	if !aos502TemSerie(n.metricas(t), "aos_runs_retry_prompt_hash_diferente_total", 0) {
		t.Fatalf("o prompt das tentativas e o mesmo: a medicao tem de ficar a zero:\n%s", aos502SoRetry(n.metricas(t)))
	}
}

// TestAOS502_OPromptDiferenteConta prova que a medição do `prompt_hash` conta quando a tentativa
// NÃO repete o pedido: o mesmo nó do plano, com outro objectivo. É medição — a tentativa é
// hospedada na mesma.
func TestAOS502_OPromptDiferenteConta(t *testing.T) {
	n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
	const plano = "plano-502-prompt"
	ger := n.pedir(t, plano)
	n.falharSemChamar(t, plano, ger)
	id2 := idDaTentativa(plano, aos502No_, 2)
	corpo := map[string]any{
		"run_id": id2, "objective": "Um objectivo que nao e o da primeira tentativa", "principal_nhi": durAgent, "credential": n.tok,
		"tools": []string{aos502Leitura}, "inputs": []any{}, "completion_requires": []string{aos502Leitura},
		"plan_request": aos502Vinculo(plano, aos502No_, ger, 2),
	}
	if r := postReq(n.h, "/runs", corpo, govHeaders()); r.Code != http.StatusCreated {
		t.Fatalf("a tentativa e hospedada mesmo com outro objectivo (o hash so existe depois); veio %d %s", r.Code, r.Body.String())
	}
	n.esperar(t, id2)
	prazo := time.Now().Add(10 * time.Second)
	for !aos502TemSerie(n.metricas(t), "aos_runs_retry_prompt_hash_diferente_total", 1) {
		if time.Now().After(prazo) {
			t.Fatalf("um prompt diferente do da tentativa anterior tinha de contar:\n%s", aos502SoRetry(n.metricas(t)))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAOS502_AProvaRecusa_ComOLogReal tem um caso por causa de recusa que um run REAL produz:
// prepara-se o run anterior nesse estado, pede-se a tentativa, e a resposta é a 403 uniforme,
// com a causa contada e nenhum run hospedado.
func TestAOS502_AProvaRecusa_ComOLogReal(t *testing.T) {
	producao := aos493RespostasDeProducao(t)["plan-e2e-v0145-1791115918"]
	for _, c := range []struct {
		nome  string
		modo  agentruntime.CompletionMode
		tecto int
		tools []string
		// preparar deixa o run anterior no estado do caso; devolve a tentativa a pedir (0 ⇒ 2).
		preparar func(t *testing.T, n *aos502No, plano string, ger int) int
		causa    string
	}{
		{nome: "AnteriorInexistente", modo: agentruntime.CompletionEnforce, tecto: 2, causa: causaRetryInexistente,
			preparar: func(*testing.T, *aos502No, string, int) int { return 0 }},
		{nome: "Tentativa3SemA2", modo: agentruntime.CompletionEnforce, tecto: 2, causa: causaRetryInexistente,
			preparar: func(t *testing.T, n *aos502No, plano string, ger int) int {
				n.falharSemChamar(t, plano, ger)
				return 3
			}},
		{nome: "AnteriorConcluiu", modo: agentruntime.CompletionEnforce, tecto: 2, causa: causaRetryNaoFalhou,
			preparar: func(t *testing.T, n *aos502No, plano string, ger int) int {
				n.modeloChama(aos502Leitura)
				if r := n.primeira(t, plano, ger); r.Status != "completed" {
					t.Fatalf("pre-condicao: completed; veio %+v", r)
				}
				return 0
			}},
		{nome: "NoEmObservacaoDoVeredicto", modo: agentruntime.CompletionObserve, tecto: 2, causa: causaRetryNaoFalhou,
			preparar: func(t *testing.T, n *aos502No, plano string, ger int) int {
				n.modeloNaoChama(producao, "stop")
				if r := n.primeira(t, plano, ger); r.Status != "completed" || r.OutcomeReason != string(agentruntime.OutcomeContractNoCall) {
					t.Fatalf("pre-condicao: em observacao o run conclui com a razao observada; veio %+v", r)
				}
				return 0
			}},
		{nome: "RespostaTruncada", modo: agentruntime.CompletionEnforce, tecto: 2, causa: causaRetryOutraRazao,
			preparar: func(t *testing.T, n *aos502No, plano string, ger int) int {
				n.modeloNaoChama(producao, "length")
				if r := n.primeira(t, plano, ger); r.Status != "failed" || r.OutcomeReason != string(agentruntime.OutcomeTruncated) {
					t.Fatalf("pre-condicao: failed por truncated; veio %+v", r)
				}
				return 0
			}},
		{nome: "MotivoContentFilter", modo: agentruntime.CompletionEnforce, tecto: 2, causa: causaRetryParagem,
			preparar: func(t *testing.T, n *aos502No, plano string, ger int) int {
				n.modeloNaoChama(producao, "content_filter")
				if r := n.primeira(t, plano, ger); r.Status != "failed" || r.OutcomeReason != string(agentruntime.OutcomeContractNoCall) {
					t.Fatalf("pre-condicao: failed por contract_unmet_no_call com o motivo content_filter; veio %+v", r)
				}
				return 0
			}},
		{nome: "MotivoNaoReportado", modo: agentruntime.CompletionEnforce, tecto: 2, causa: causaRetryParagem,
			preparar: func(t *testing.T, n *aos502No, plano string, ger int) int {
				n.modeloNaoChama(producao, "")
				if r := n.primeira(t, plano, ger); r.Status != "failed" || r.OutcomeReason != string(agentruntime.OutcomeContractNoCall) {
					t.Fatalf("pre-condicao: failed por contract_unmet_no_call sem motivo de paragem; veio %+v", r)
				}
				return 0
			}},
		{nome: "MotivoOutro", modo: agentruntime.CompletionEnforce, tecto: 2, causa: causaRetryParagem,
			preparar: func(t *testing.T, n *aos502No, plano string, ger int) int {
				n.modeloNaoChama(producao, "um_motivo_que_o_no_nao_conhece")
				if r := n.primeira(t, plano, ger); r.Status != "failed" || r.OutcomeReason != string(agentruntime.OutcomeContractNoCall) {
					t.Fatalf("pre-condicao: failed por contract_unmet_no_call com o motivo other; veio %+v", r)
				}
				return 0
			}},
		// UMA TOOL CALL MEDIADA E EFECTIVA, e a razão é `contract_unmet_no_call` na mesma: o
		// contrato exige as duas tools, o modelo chamou a de leitura e parou. É o caso que a
		// razão sozinha deixava passar — e o run anterior TEM um efeito aplicado.
		{nome: "UmaToolCallMediada", modo: agentruntime.CompletionEnforce, tecto: 2, tools: []string{aos502Leitura, aos502Escrita}, causa: causaRetryPediuTools,
			preparar: func(t *testing.T, n *aos502No, plano string, ger int) int {
				n.modeloChama(aos502Leitura)
				r := n.primeira(t, plano, ger, aos502Leitura, aos502Escrita)
				if r.Status != "failed" || r.OutcomeReason != string(agentruntime.OutcomeContractNoCall) || r.Verdict.ToolCallsRequested != 1 || *n.execs[aos502Leitura] != 1 {
					t.Fatalf("pre-condicao: failed por contract_unmet_no_call COM uma tool call efectiva; veio %+v %+v", r, r.Verdict)
				}
				if !aos502TemEvento(t, n, idDaTentativa(plano, aos502No_, 1), referencemonitor.EventTypeMediated) {
					t.Fatal("pre-condicao: o stream do run anterior tem um tool.call.mediated")
				}
				return 0
			}},
		// UMA TOOL CALL NEGADA: o modelo pediu a tool de escrita, que a lista-branca do run não
		// tem. Nenhum efeito, e mesmo assim não se repete — uma nova tentativa empurrava o
		// modelo a pedir outra vez o que lhe foi negado.
		{nome: "UmaToolCallNegada", modo: agentruntime.CompletionEnforce, tecto: 2, tools: []string{aos502Leitura, aos502Escrita}, causa: causaRetryPediuTools,
			preparar: func(t *testing.T, n *aos502No, plano string, ger int) int {
				n.modeloChama(aos502Escrita)
				r := n.primeira(t, plano, ger, aos502Leitura)
				if r.Status != "failed" || r.Verdict == nil || r.Verdict.ToolCallsRequested == 0 || *n.execs[aos502Escrita] != 0 {
					t.Fatalf("pre-condicao: failed com uma tool call pedida e negada; veio %+v %+v", r, r.Verdict)
				}
				if !aos502TemEvento(t, n, idDaTentativa(plano, aos502No_, 1), referencemonitor.EventTypeDenied) {
					t.Fatal("pre-condicao: o stream do run anterior tem um tool.call.denied")
				}
				return 0
			}},
		// OUTRO SUBMISSOR: o run com o id da primeira tentativa foi hospedado por um `POST /runs`
		// SEM vínculo ao pedido. Fechou do mesmo modo, e não é trabalho do plano: o nó nunca lhe
		// escreveu a origem.
		{nome: "AnteriorDeOutroSubmissor", modo: agentruntime.CompletionEnforce, tecto: 2, causa: causaRetrySemOrigem,
			preparar: func(t *testing.T, n *aos502No, plano string, _ int) int {
				n.modeloNaoChama(producao, "stop")
				id := idDaTentativa(plano, aos502No_, 1)
				if codigo, corpo := n.submeter(t, id, nil, []string{aos502Leitura}, []string{aos502Leitura}); codigo != http.StatusCreated {
					t.Fatalf("POST /runs sem vinculo: %d %s", codigo, corpo)
				}
				n.esperar(t, id)
				if _, r := n.ler(t, id); r.Status != "failed" || r.OutcomeReason != string(agentruntime.OutcomeContractNoCall) {
					t.Fatalf("pre-condicao: failed por contract_unmet_no_call; veio %+v", r)
				}
				return 0
			}},
		{nome: "TectoAZero", modo: agentruntime.CompletionEnforce, tecto: 0, causa: causaRetryTecto,
			preparar: func(t *testing.T, n *aos502No, plano string, ger int) int {
				n.falharSemChamar(t, plano, ger)
				return 0
			}},
		{nome: "AcimaDoTecto", modo: agentruntime.CompletionEnforce, tecto: 1, causa: causaRetryTecto,
			preparar: func(t *testing.T, n *aos502No, plano string, ger int) int {
				n.falharSemChamar(t, plano, ger)
				if codigo, corpo := n.pedirTentativa(t, plano, ger, 2); codigo != http.StatusCreated {
					t.Fatalf("com o tecto a 1 a tentativa 2 e admitida; veio %d %s", codigo, corpo)
				}
				n.esperar(t, idDaTentativa(plano, aos502No_, 2))
				return 3
			}},
	} {
		t.Run(c.nome, func(t *testing.T) {
			n := aos502Compor(t, c.modo, c.tecto, c.tools...)
			plano := "plano-502-" + strings.ToLower(c.nome)
			ger := n.pedir(t, plano)
			tentativa := c.preparar(t, n, plano, ger)
			if tentativa == 0 {
				tentativa = 2
			}
			admitidasAntes := 0
			if c.nome == "AcimaDoTecto" {
				admitidasAntes = 1
			}
			// A tentativa pedida corria bem se fosse hospedada: o modelo chamava a tool.
			n.modeloChama(aos502Leitura)
			execsAntes := *n.execs[aos502Leitura]
			codigo, corpo := n.pedirTentativa(t, plano, ger, tentativa)
			if codigo != http.StatusForbidden || corpo != `{"error":"nao autorizado"}` {
				t.Fatalf("a tentativa tinha de ser recusada com a 403 uniforme; veio %d %s", codigo, corpo)
			}
			m := n.metricas(t)
			if !aos502TemSerie(m, `aos_runs_retry_refused_total{causa="`+c.causa+`"}`, 1) {
				t.Fatalf("a recusa tinha de contar na causa %q; metricas:\n%s", c.causa, aos502SoRetry(m))
			}
			if !aos502TemSerie(m, "aos_runs_retry_admitted_total", admitidasAntes) {
				t.Fatalf("a tentativa recusada nao conta como admitida:\n%s", aos502SoRetry(m))
			}
			if id := idDaTentativa(plano, aos502No_, tentativa); n.existe(t, id) {
				t.Fatalf("uma tentativa recusada nao pode deixar run nenhum; o stream %s existe", id)
			}
			if *n.execs[aos502Leitura] != execsAntes {
				t.Fatal("a tool nao pode correr por causa de uma tentativa recusada")
			}
		})
	}
}

// aos502TemEvento diz se o stream do run tem pelo menos um evento do tipo dado.
func aos502TemEvento(t *testing.T, n *aos502No, runID, tipo string) bool {
	t.Helper()
	evs, err := n.node.EventStore.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("ler o stream de %s: %v", runID, err)
	}
	for _, ev := range evs {
		if ev.Type == tipo {
			return true
		}
	}
	return false
}

// TestAOS502_AnteriorEmCurso: a tentativa pedida com a anterior ainda a correr é recusada. O
// provider fica preso até o teste o soltar.
func TestAOS502_AnteriorEmCurso(t *testing.T) {
	n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
	const plano = "plano-502-em-curso"
	ger := n.pedir(t, plano)
	soltar := make(chan struct{})
	var uma sync.Once
	libertar := func() { uma.Do(func() { close(soltar) }) }
	defer libertar()
	producao := aos493RespostasDeProducao(t)["plan-e2e-v0145-1791115918"]
	n.upstream.mu.Lock()
	n.upstream.pede = ""
	n.upstream.responde = func(bool, string) []byte {
		<-soltar
		return aos502Completion(producao, "stop")
	}
	n.upstream.mu.Unlock()

	id1 := idDaTentativa(plano, aos502No_, 1)
	if codigo, corpo := n.submeter(t, id1, aos502Vinculo(plano, aos502No_, ger, 0), []string{aos502Leitura}, []string{aos502Leitura}); codigo != http.StatusCreated {
		t.Fatalf("a primeira tentativa: %d %s", codigo, corpo)
	}
	// O run está hospedado e à espera do modelo: a origem pode ainda não estar gravada, e o
	// estado não é terminal. Qualquer das duas recusa; espera-se pela origem para medir o estado.
	prazo := time.Now().Add(10 * time.Second)
	for !aos502TemEvento(t, n, id1, EventTypeRunPlanOrigin) {
		if time.Now().After(prazo) {
			t.Fatal("o run.plan_origin da primeira tentativa nao apareceu")
		}
		time.Sleep(10 * time.Millisecond)
	}
	codigo, corpo := n.pedirTentativa(t, plano, ger, 2)
	if codigo != http.StatusForbidden || corpo != `{"error":"nao autorizado"}` {
		t.Fatalf("com a anterior em curso a tentativa e recusada (403); veio %d %s", codigo, corpo)
	}
	if m := n.metricas(t); !aos502TemSerie(m, `aos_runs_retry_refused_total{causa="`+causaRetryEmCurso+`"}`, 1) {
		t.Fatalf("a causa e anterior_em_curso:\n%s", aos502SoRetry(m))
	}
	libertar()
	n.esperar(t, id1)
	// Com a anterior acabada — failed, sem tool calls — o MESMO pedido passa a ser admitido.
	n.modeloChama(aos502Leitura)
	if codigo, corpo := n.pedirTentativa(t, plano, ger, 2); codigo != http.StatusCreated {
		t.Fatalf("depois de a anterior fechar failed sem tool calls, a tentativa e admitida; veio %d %s", codigo, corpo)
	}
	n.esperar(t, idDaTentativa(plano, aos502No_, 2))
}

// TestAOS502_AFormaDoIdEDoCampo: sem `attempt` o nó recusa o id com segundo `~` como sempre; com
// `attempt` só aceita EXACTAMENTE `<plano>~<nó>~<n>`.
func TestAOS502_AFormaDoIdEDoCampo(t *testing.T) {
	n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
	const plano = "plano-502-forma"
	ger := n.pedir(t, plano)
	n.falharSemChamar(t, plano, ger)
	n.modeloChama(aos502Leitura)
	base := idDaTentativa(plano, aos502No_, 1)
	for _, c := range []struct {
		nome  string
		runID string
		v     vinculoAoPedido
	}{
		{"SemAttempt_IdComSufixo", base + "~2", vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: aos502PlanID, NodeID: aos502No_}},
		{"SemAttempt_SemNodeID_IdComSufixo", base + "~2", vinculoAoPedido{RunID: plano, Geracao: ger}},
		{"Attempt1", base, vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: aos502PlanID, NodeID: aos502No_, Attempt: 1}},
		{"AttemptNegativo", base + "~-2", vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: aos502PlanID, NodeID: aos502No_, Attempt: -2}},
		{"Attempt2_IdSemSufixo", base, vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: aos502PlanID, NodeID: aos502No_, Attempt: 2}},
		{"Attempt2_IdDaTentativa3", base + "~3", vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: aos502PlanID, NodeID: aos502No_, Attempt: 2}},
		{"Attempt2_ZeroAEsquerda", base + "~02", vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: aos502PlanID, NodeID: aos502No_, Attempt: 2}},
		{"Attempt2_Sinal", base + "~+2", vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: aos502PlanID, NodeID: aos502No_, Attempt: 2}},
		{"Attempt2_OutroNo", plano + "~outro~2", vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: aos502PlanID, NodeID: aos502No_, Attempt: 2}},
		{"Attempt2_SemNodeID", base + "~2", vinculoAoPedido{RunID: plano, Geracao: ger, Attempt: 2}},
		{"Attempt2_SufixoAMais", base + "~2~2", vinculoAoPedido{RunID: plano, Geracao: ger, PlanID: aos502PlanID, NodeID: aos502No_, Attempt: 2}},
	} {
		t.Run(c.nome, func(t *testing.T) {
			v := c.v
			codigo, corpo := n.submeter(t, c.runID, &v, []string{aos502Leitura}, []string{aos502Leitura})
			// Um id que não é um `stream_id` (o `+` do sinal é representável; o resto também) pára
			// na 403 do vínculo, como todas as recusas da rota.
			if codigo != http.StatusForbidden || corpo != `{"error":"nao autorizado"}` {
				t.Fatalf("a forma %q com %+v tinha de ser recusada (403 uniforme); veio %d %s", c.runID, c.v, codigo, corpo)
			}
			if c.runID != base && n.existe(t, c.runID) {
				t.Fatalf("uma forma recusada nao hospeda nada; o stream %s existe", c.runID)
			}
		})
	}
	if m := n.metricas(t); !aos502TemSerie(m, "aos_runs_retry_admitted_total", 0) {
		t.Fatalf("nenhuma forma errada e admitida:\n%s", aos502SoRetry(m))
	}
	// E a forma certa, no mesmo nó e sobre o mesmo run anterior, é admitida.
	if codigo, corpo := n.pedirTentativa(t, plano, ger, 2); codigo != http.StatusCreated {
		t.Fatalf("a forma certa e admitida; veio %d %s", codigo, corpo)
	}
	n.esperar(t, base+"~2")
}

// TestAOS502_Injectividade: nenhum par (nó, tentativa) produz o id de outro par do mesmo pedido,
// incluindo `node_id` com `~`, `:`, `.`, `+` e sufixos numéricos.
func TestAOS502_Injectividade(t *testing.T) {
	nos := []string{"n1", "n1~2", "n1+7e2", "n1.2", "n1:2", "n1-2", "n1_2", "n", "2", "n1~", "n1~2~3", "a.b", "a+2eb", "~", "~2", "+", "++"}
	vistos := map[string]string{}
	for _, no := range nos {
		for tentativa := 1; tentativa <= 3; tentativa++ {
			id := idDaTentativa("plano", no, tentativa)
			par := fmt.Sprintf("(%q, %d)", no, tentativa)
			if outro, ja := vistos[id]; ja {
				t.Fatalf("os pares %s e %s produzem o mesmo id %q", outro, par, id)
			}
			vistos[id] = par
			if err := eventstore.ValidarStreamID(id); err != nil {
				t.Fatalf("o id %q do par %s nao e um stream_id: %v", id, par, err)
			}
			if resto := strings.TrimPrefix(id, "plano~"); strings.Count(resto, "~") != map[bool]int{true: 0, false: 1}[tentativa == 1] {
				t.Fatalf("o id %q do par %s tem de ter %d separador(es) depois do no", id, par, map[bool]int{true: 0, false: 1}[tentativa == 1])
			}
		}
	}
	if idDaTentativa("plano", "n1", 1) != idDoRunFilho("plano", "n1") || idDaTentativa("plano", "n1", 0) != idDoRunFilho("plano", "n1") {
		t.Fatal("a primeira tentativa tem o id do run filho de sempre")
	}
}

// TestAOS502_OutraGeracaoDaReclamacao: a primeira tentativa foi submetida numa geração; o `serve`
// morreu, o pedido voltou à fila e outro retomou-o. A tentativa 2 leva a geração NOVA (a viva), e
// o nó admite-a: a prova é sobre o que o run anterior fez, não sobre quem o reclamava.
func TestAOS502_OutraGeracaoDaReclamacao(t *testing.T) {
	n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
	const plano = "plano-502-geracao"
	ger1 := n.pedir(t, plano)
	n.falharSemChamar(t, plano, ger1)
	// O desfecho TRANSITÓRIO devolve o pedido à fila; a reclamação seguinte é outra geração.
	if r := postReq(n.h, "/plans/outcome", map[string]any{"run_id": plano, "generation": ger1, "classe": "transitorio", "codigo_saida": 1}, govHeaders()); r.Code != http.StatusNoContent {
		t.Fatalf("POST /plans/outcome: %d %s", r.Code, r.Body.String())
	}
	ger2 := n.reclamar(t, plano)
	if ger2 <= ger1 {
		t.Fatalf("pre-condicao: a segunda reclamacao e outra geracao; vieram %d e %d", ger1, ger2)
	}
	n.modeloChama(aos502Leitura)
	// Com a geração ANTIGA o vínculo já não é o vivo: recusa-se como sempre (AOS-439).
	if codigo, _ := n.pedirTentativa(t, plano, ger1, 2); codigo != http.StatusForbidden {
		t.Fatalf("a geracao antiga ja nao e a viva: 403; veio %d", codigo)
	}
	if codigo, corpo := n.pedirTentativa(t, plano, ger2, 2); codigo != http.StatusCreated {
		t.Fatalf("a tentativa 2 na geracao seguinte e admitida; veio %d %s", codigo, corpo)
	}
	id2 := idDaTentativa(plano, aos502No_, 2)
	n.esperar(t, id2)
	if o, _ := n.origemDe(t, id2); o.Pedido.Geracao != ger2 || o.Attempt != 2 {
		t.Fatalf("a origem da tentativa leva a geracao em que foi pedida; veio %+v", o)
	}
}

// TestAOS502_DoisPedidosDaMesmaTentativa: dois pedidos concorrentes da mesma tentativa — um
// hospeda, e o outro recebe o que uma re-submissão do mesmo id recebe hoje. Há UM run.
func TestAOS502_DoisPedidosDaMesmaTentativa(t *testing.T) {
	n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
	const plano = "plano-502-concorrencia"
	ger := n.pedir(t, plano)
	n.falharSemChamar(t, plano, ger)
	n.modeloChama(aos502Leitura)
	const concorrentes = 6
	codigos := make([]int, concorrentes)
	var wg sync.WaitGroup
	for i := 0; i < concorrentes; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codigos[i], _ = n.pedirTentativa(t, plano, ger, 2)
		}(i)
	}
	wg.Wait()
	for _, c := range codigos {
		// A re-submissão do mesmo id responde 201 idempotente (ou 409 com credencial forte e a
		// mesma região): nunca um erro, e nunca um segundo run.
		if c != http.StatusCreated && c != http.StatusConflict {
			t.Fatalf("os pedidos concorrentes recebem 201 ou 409; vieram %v", codigos)
		}
	}
	id2 := idDaTentativa(plano, aos502No_, 2)
	n.esperar(t, id2)
	if m := n.metricas(t); !aos502TemSerie(m, "aos_runs_retry_admitted_total", 1) {
		t.Fatalf("UMA tentativa hospedada, por mais pedidos que cheguem:\n%s", aos502SoRetry(m))
	}
	if execs := *n.execs[aos502Leitura]; execs != 1 {
		t.Fatalf("a tool correu uma vez (um run); correu %d", execs)
	}
	// E pedida outra vez depois de acabada: a mesma resposta de uma re-submissão, sem novo run.
	if codigo, _ := n.pedirTentativa(t, plano, ger, 2); codigo != http.StatusCreated && codigo != http.StatusConflict {
		t.Fatalf("a re-submissao de uma tentativa acabada responde como hoje (201/409); veio %d", codigo)
	}
	if execs := *n.execs[aos502Leitura]; execs != 1 {
		t.Fatalf("a re-submissao nao corre nada; a tool correu %d vezes", execs)
	}
}

// TestAOS502_LogIlegivelNaoHospeda: se o stream da tentativa anterior não se lê AGORA, o nó
// responde 503 e não hospeda; não dá a tentativa por recusada.
func TestAOS502_LogIlegivelNaoHospeda(t *testing.T) {
	n := aos502Compor(t, agentruntime.CompletionEnforce, 2)
	const plano = "plano-502-ilegivel"
	ger := n.pedir(t, plano)
	n.falharSemChamar(t, plano, ger)
	n.modeloChama(aos502Leitura)
	q := *aos502Vinculo(plano, aos502No_, ger, 2)
	chamador := readerIdentity{principal: govReader, board: govBoard, region: govRegion}
	original := n.node.EventStore
	anterior := idDaTentativa(plano, aos502No_, 1)
	// A prova, com o Event Store a falhar na leitura do run anterior.
	api := aos502Interno(t, n, aos502StoreQueFalha{EventStorePort: original, falha: anterior})
	if _, causa, transitoria := api.provarTentativa(context.Background(), chamador, q); causa != causaRetryIndisponivel || !transitoria {
		t.Fatalf("um log que nao se le agora e indisponivel e TRANSITORIO; veio causa=%q transitoria=%t", causa, transitoria)
	}
	// E com o stream do run anterior AUSENTE (podado): recusa definitiva.
	api = aos502Interno(t, n, aos502StoreQueFalha{EventStorePort: original, ausente: anterior})
	if _, causa, transitoria := api.provarTentativa(context.Background(), chamador, q); causa != causaRetryInexistente || transitoria {
		t.Fatalf("um stream que ja nao existe e uma recusa; veio causa=%q transitoria=%t", causa, transitoria)
	}
	// Outra região: a residência do run anterior não é a de quem pede.
	api = aos502Interno(t, n, original)
	deFora := readerIdentity{principal: govReader, board: govBoard, region: "us"}
	if _, causa, _ := api.provarTentativa(context.Background(), deFora, q); causa != causaRetryResidencia {
		t.Fatalf("a residencia do run anterior tem de ser a regiao de quem pede; veio %q", causa)
	}
	if _, causa, _ := api.provarTentativa(context.Background(), chamador, q); causa != "" {
		t.Fatalf("controlo: com o log legivel e a regiao certa a prova passa; veio %q", causa)
	}
}
