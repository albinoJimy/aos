package main

// AOS-532 — O `budget.toolcall.committed` TEM CHAVE PRÓPRIA E CHEGA AO STREAM DO RUN.
//
// O facto do AOS-287 gravava-se com a chave de envelope `run_id:<step_id>`, a mesma do
// `tool.call.mediated` da mesma call (AOS-379). O Event Store deduplica por chave dentro do
// stream, seja qual for o tipo, e devolve `StatusDuplicate` sem erro: o facto era engolido e o
// consumo durável das tool calls não existia. Estes testes provam a CONSEQUÊNCIA — o facto no
// stream, um por tool call, e a soma que a incarnação seguinte lê —, no nó composto.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// aos532Consumo é um facto `budget.toolcall.committed` lido do stream, com o `step_id` do envelope.
type aos532Consumo struct {
	envelope string
	p        toolCallBudgetPayload
}

func aos532Consumos(t *testing.T, evs []eventstore.Event) []aos532Consumo {
	t.Helper()
	var out []aos532Consumo
	for _, e := range evs {
		if e.Type != EventTypeToolCallBudget {
			continue
		}
		var p toolCallBudgetPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("payload de %s: %v", EventTypeToolCallBudget, err)
		}
		out = append(out, aos532Consumo{envelope: e.StepID, p: p})
	}
	return out
}

// aos532Turnos soma os tokens dos `turn.recorded` do stream.
func aos532Turnos(t *testing.T, evs []eventstore.Event) int64 {
	t.Helper()
	var soma int64
	for _, e := range evs {
		if e.Type != agentruntime.EventTypeTurnRecorded {
			continue
		}
		var p turnLedgerPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("payload de turn.recorded: %v", err)
		}
		soma += p.InputTokens + p.OutputTokens
	}
	return soma
}

// aos532Pico espera que o nó do run seja destruído (o instante em que o consumo da incarnação
// fecha) e devolve o pico de consumo em tokens que o RunBudget registou — o que o nó DEBITOU.
func aos532Pico(t *testing.T, n *aos530No) int64 {
	t.Helper()
	prazo := time.Now().Add(5 * time.Second)
	for {
		if pico, visto := n.node.orcamento.PicoDeConsumo(); visto {
			return pico
		}
		if time.Now().After(prazo) {
			t.Fatal("o no do run nao foi destruido: sem pico de consumo")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A CHAVE NÃO COLIDE COM A MEDIAÇÃO. O `tool.call.mediated` da call está no stream com a chave
// `run_id:<step_id>` — a ordem do nó composto — e o facto de consumo do MESMO passo chega na
// mesma, com a chave `run_id:consumo-<step_id>`; uma segunda escrita do passo é duplicada.
//
// Falha-antes: com a chave nua, o append devolvia `StatusDuplicate` sem erro e o stream ficava
// sem o facto — `ConsumedByRun` via só a mediação e devolvia ErrBurndownNoLedger.
func TestAOS532_ChaveNaoColideComAMediacao(t *testing.T) {
	ctx := context.Background()
	es := storeDeTeste(t)
	const run, step = "run-532", "step-000001-tool-0"
	if _, err := es.Append(ctx, run, eventstore.EventInput{
		Type: referencemonitor.EventTypeMediated, RunID: run, StepID: step, Payload: []byte(`{}`),
	}); err != nil {
		t.Fatalf("append da mediacao: %v", err)
	}
	registar := registoDeConsumoDeToolCall(es, producerDoConsumoDeToolCall())
	for i := 0; i < 2; i++ {
		if err := registar(ctx, run, step, budget.Amount{Tokens: 42}); err != nil {
			t.Fatalf("registo #%d: %v", i, err)
		}
	}
	evs, err := es.Read(ctx, run, 1)
	if err != nil {
		t.Fatal(err)
	}
	c := aos532Consumos(t, evs)
	if len(c) != 1 || c[0].envelope != toolCallBudgetStepPrefix+step || c[0].p.StepID != step || c[0].p.Tokens != 42 {
		t.Fatalf("quer UM facto de consumo com a chave propria e o step_id da call no payload; vieram %+v", c)
	}
	consumo, err := consumoDuravelParaOrcamento(newTurnLedgerBurndown(es))(ctx, run)
	if err != nil || consumo.Tokens != 42 {
		t.Fatalf("o consumo duravel ve o facto: %+v (%v)", consumo, err)
	}
}

// NO NÓ COMPOSTO, com o orçamento por-run e o canal de mediação durável: um facto de consumo
// por tool call, correlacionado com a mediação pelo `step_id` do payload; o consumo durável
// que a incarnação seguinte lê (uma fonte NOVA, sem cursor) é a soma dos turnos e das tool
// calls, e é igual ao que o nó do run DEBITOU nesta incarnação.
func TestAOS532_No_CommittedChegaAoStream(t *testing.T) {
	n := aos530Compor(t, aos530Opcoes{ambiente: map[string]string{"AOS_BUDGET_MAX_TOKENS": "1000000"}})
	const runID = "run-532-no"
	evs := n.correr(t, runID,
		[2]string{"doc_read", `{"doc_id":"a"}`},
		[2]string{"doc_read", `{"doc_id":"b"}`})

	med := aos530Mediacoes(t, evs, referencemonitor.EventTypeMediated)
	consumos := aos532Consumos(t, evs)
	if len(med) != 2 || len(consumos) != 2 || *n.docReads != 2 {
		t.Fatalf("quer 2 mediacoes, 2 factos de consumo e 2 execucoes; vieram %d, %d e %d", len(med), len(consumos), *n.docReads)
	}
	var tools int64
	for i, c := range consumos {
		if c.envelope != toolCallBudgetStepPrefix+med[i].step || c.p.StepID != med[i].step || c.p.Tokens <= 0 || c.p.CostMicroUSD != 0 {
			t.Fatalf("o facto %d (passo %s): %+v — a chave propria, o step_id da call, tokens > 0 e sem dolares", i, med[i].step, c)
		}
		tools += c.p.Tokens
	}
	t.Logf("reserva do hook por tool call: %+v; turnos %d tokens", consumos, aos532Turnos(t, evs))
	turnos := aos532Turnos(t, evs)
	consumo, err := consumoDuravelParaOrcamento(newTurnLedgerBurndown(n.node.EventStore))(context.Background(), runID)
	if err != nil || consumo.Tokens != turnos+tools {
		t.Fatalf("a incarnacao seguinte le turnos (%d) + tool calls (%d): %+v (%v)", turnos, tools, consumo, err)
	}
	if pico := aos532Pico(t, n); pico != consumo.Tokens {
		t.Fatalf("o no do run debitou %d tokens e o consumo duravel diz %d — a retoma nasceria com outro tecto", pico, consumo.Tokens)
	}
}

// A `ocr_read` SEM DUPLA CONTAGEM. A tool call grava o facto de consumo (a reserva do hook do
// Reference Monitor: a pegada dos argumentos, em tokens, sem dólares) e o custo da chamada ao
// modelo (`budget.toolcall.model_cost`, medido). São duas reservas do MESMO nó do run, ambas
// debitadas nesta incarnação: o consumo durável soma-as e iguala o que o nó debitou — nem mais
// (dupla contagem) nem menos (a fuga do AOS-287). Os dólares são só os do modelo.
func TestAOS532_No_OCRSemDuplaContagem(t *testing.T) {
	n := aos530Compor(t, aos530Opcoes{preco: aos530PrecoComTool, ambiente: aos530Orcamento("1000000")})
	const runID = "run-532-ocr"
	evs := n.correr(t, runID, [2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`})

	consumos := aos532Consumos(t, evs)
	custos := aos530Custos(t, evs)
	if len(consumos) != 1 || len(custos) != 1 || consumos[0].p.StepID != custos[0].p.StepID {
		t.Fatalf("quer 1 facto de consumo e 1 de custo, do mesmo passo; vieram %+v e %+v", consumos, custos)
	}
	estimativa := consumos[0].p
	if estimativa.Tokens <= 0 || estimativa.CostMicroUSD != 0 {
		t.Fatalf("a reserva do hook e so tokens: %+v", estimativa)
	}
	modelo := custos[0].p
	t.Logf("reserva do hook da ocr_read: %d tokens; chamada ao modelo: %d+%d tokens", estimativa.Tokens, modelo.InputTokens, modelo.OutputTokens)
	turnos := aos532Turnos(t, evs)
	consumo, err := consumoDuravelParaOrcamento(newTurnLedgerBurndown(n.node.EventStore))(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if consumo.Tokens != turnos+estimativa.Tokens+modelo.InputTokens+modelo.OutputTokens {
		t.Fatalf("consumo duravel %d != turnos %d + reserva do hook %d + chamada ao modelo %d", consumo.Tokens, turnos, estimativa.Tokens, modelo.InputTokens+modelo.OutputTokens)
	}
	if consumo.CostMicroUSD != modelo.CostMicroUSD || modelo.CostMicroUSD != 840 {
		t.Fatalf("os dolares do run sao os do modelo (840), sem os da reserva do hook: %+v", consumo)
	}
	if pico := aos532Pico(t, n); pico != consumo.Tokens {
		t.Fatalf("o no do run debitou %d tokens e o consumo duravel soma %d — contagem a dobrar ou fuga", pico, consumo.Tokens)
	}
}

// A REINCARNAÇÃO REAL LÊ O FACTO. O ciclo de aprovação do nó composto (autonomia L4, four-eyes)
// com o orçamento por-run: o `passo_um` escala na incarnação 1 e corre na 2 (onde confirma a sua
// estimativa); o `passo_dois` escala na 2 e corre na 3. Dentro do efeito do `passo_dois`, na
// incarnação 3, o que resta ao run tem de ser `tecto − turnos − estimativa do passo_um −
// estimativa do passo_dois` (esta última reservada pelo hook antes do efeito). As estimativas
// calculam-se aqui pelo estimador composto sobre a call, e não do stream — é o stream que está
// sob prova.
//
// Falha-antes: com a chave nua, o facto do `passo_um` é engolido pela mediação, a incarnação 3
// nasce sem ele e sobra-lhe a estimativa do `passo_um` a mais.
func TestAOS532_No_ReincarnacaoNasceComTectoMenosTurnosMenosTools(t *testing.T) {
	const tecto = 1_000_000
	t.Setenv("AOS_BUDGET_MAX_TOKENS", "1000000")
	h := newACNHarness(t)
	if h.node.orcamento == nil {
		t.Fatal("o orcamento por-run tinha de ficar composto com AOS_BUDGET_MAX_TOKENS")
	}
	h.model.guiao = func(v agentruntime.PromptView) agentruntime.ModelResponse {
		var r agentruntime.ModelResponse
		switch v.Turn {
		case 1:
			r = acnCall("passo_um", "doc-a")
		case 2:
			r = acnCall("passo_dois", "doc-b")
		default:
			r = agentruntime.ModelResponse{Text: "concluido", Final: true}
		}
		r.Usage = agentruntime.Usage{InputTokens: 100, OutputTokens: 7}
		return r
	}
	var restaNoPassoDois int64 = -1
	h.noEfeito = func(tool string) {
		if tool == "passo_dois" {
			restaNoPassoDois, _ = h.node.orcamento.AvailableTokens(acnRunID)
		}
	}

	ctx := context.Background()
	h.submete(t)
	for i, req := range []string{"req-532-1", "req-532-2"} {
		h.esperaFim(t, acnRunID)
		if _, susp := h.svc.Suspended(ctx, acnRunID); !susp {
			t.Fatalf("a tool %d tinha de escalar e o run ficar suspenso", i+1)
		}
		h.aprovaPendente(t, req)
		if err := h.svc.Resume(ctx, acnRunID, h.token(t)); err != nil {
			t.Fatalf("Resume %d: %v", i+1, err)
		}
	}
	h.esperaFim(t, acnRunID)
	if h.execucoes("passo_um") != 1 || h.execucoes("passo_dois") != 1 || restaNoPassoDois < 0 {
		t.Fatalf("as duas tools corriam uma vez, a segunda na incarnacao 3 (um=%d dois=%d resta=%d)",
			h.execucoes("passo_um"), h.execucoes("passo_dois"), restaNoPassoDois)
	}

	evs, err := h.node.EventStore.Read(ctx, acnRunID, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Os turnos ANTES do efeito do passo_dois: o 1 e o 2 (o 3 é a resposta final, depois dele).
	var turnosAntes int64
	for _, e := range evs {
		if e.Type != agentruntime.EventTypeTurnRecorded {
			continue
		}
		var p turnLedgerPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.Turn <= 2 {
			turnosAntes += p.InputTokens + p.OutputTokens
		}
	}
	estimativa := func(tool, res string) int64 {
		c := acnCall(tool, res).ToolCalls[0]
		return integration.TokenOnlyEstimator(&referencemonitor.Call{
			ToolID: c.ToolID, Capability: c.Capability, Input: c.Input,
			Resource: referencemonitor.Resource{Type: c.ResourceType, Value: c.ResourceValue, Region: c.ResourceRegion},
		}).Tokens
	}
	um, dois := estimativa("passo_um", "doc-a"), estimativa("passo_dois", "doc-b")
	t.Logf("turnos 1-2: %d tokens; estimativas: passo_um %d, passo_dois %d; resta no passo_dois: %d", turnosAntes, um, dois, restaNoPassoDois)
	if turnosAntes <= 0 || um <= 0 {
		t.Fatalf("turnos (%d) e estimativa (%d) tinham de ser positivos", turnosAntes, um)
	}
	if quer := tecto - turnosAntes - um - dois; restaNoPassoDois != quer {
		t.Fatalf("a incarnacao 3 resta %d tokens no efeito do passo_dois; quer %d = tecto %d - turnos %d - passo_um %d - passo_dois %d (sobram %d: a estimativa do passo_um esquecida?)",
			restaNoPassoDois, quer, tecto, turnosAntes, um, dois, restaNoPassoDois-quer)
	}
	// E as estimativas que o teste calculou são as que o nó gravou.
	c := aos532Consumos(t, evs)
	if len(c) != 2 || c[0].p.Tokens != um || c[1].p.Tokens != dois {
		t.Fatalf("os factos de consumo no stream (quer %d e %d): %+v", um, dois, c)
	}
}
