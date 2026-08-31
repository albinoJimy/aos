package main

import (
	"context"
	"errors"
	"testing"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/substrate/eventstore"
)

// aos287_toolcall_consumo_test.go — O CONSUMO DE TOOL CALLS SOBREVIVE À RE-INCARNAÇÃO.
//
// O AOS-256 fez o tecto por-run sobreviver, mas a fonte que ele lê conta TURNOS DE MODELO e
// só eles: as tool calls reservavam do mesmo nó e cada incarnação esquecia-as. Um run que
// escale/retome N vezes podia gastar `tecto + N × (tool calls por incarnação)`.
//
// A propriedade sob prova é a CONSEQUÊNCIA — o consumo lido na incarnação seguinte —, não o
// facto de um evento ter sido escrito.

func storeDeTeste(t *testing.T) *eventstore.Store {
	t.Helper()
	s, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// TESTE — o que a tool call confirmou é lido pela incarnação seguinte.
//
// Falha-antes: sem o registo durável, `ConsumedByRun` não vê tool call nenhuma e devolve
// ErrBurndownNoLedger (que o adaptador traduz para consumo ZERO) — a fuga.
// ---------------------------------------------------------------------------

func TestAOS287_ConsumoDeToolCallSobreviveAReincarnacao(t *testing.T) {
	ctx := context.Background()
	es := storeDeTeste(t)
	const run = "run-287"

	registar := registoDeConsumoDeToolCall(es, producerDoConsumoDeToolCall())
	if registar == nil {
		t.Fatal("o registo não foi construído com um store válido")
	}
	if err := registar(ctx, run, "step-1", budget.Amount{Tokens: 120, CostMicroUSD: 7}); err != nil {
		t.Fatalf("registar: %v", err)
	}
	if err := registar(ctx, run, "step-2", budget.Amount{Tokens: 30}); err != nil {
		t.Fatalf("registar: %v", err)
	}

	// A incarnação seguinte lê pela MESMA via que o tecto usa.
	fonte := newTurnLedgerBurndown(es)
	c, err := fonte.ConsumedByRun(ctx, run)
	if err != nil {
		t.Fatalf("ConsumedByRun = %v — um run que só fez tool calls tem turns=0 LEGITIMAMENTE; tratá-lo como «sem ledger» descarta o consumo e a fuga do AOS-287 não fecha", err)
	}
	if c.Consumed.Tokens != 150 {
		t.Fatalf("tokens = %d, quer 150 (120+30)", c.Consumed.Tokens)
	}
	if c.Consumed.CostMicroUSD != 7 {
		t.Fatalf("custo = %d, quer 7", c.Consumed.CostMicroUSD)
	}
	// E o ADAPTADOR que o tecto consome vê o mesmo — é por aqui que a incarnação
	// seguinte nasce com `tecto − já consumido`. A verificação do limite efectivo é do
	// teste de AOS-256; o que este sela é que a FONTE passou a incluir as tool calls.
	amt, err := consumoDuravelParaOrcamento(fonte)(ctx, run)
	if err != nil {
		t.Fatalf("consumoDuravelParaOrcamento: %v", err)
	}
	if amt.Tokens != 150 {
		t.Fatalf("consumo visto pelo tecto = %d tokens, quer 150 — a incarnação seguinte não veria as tool calls", amt.Tokens)
	}
}

// ---------------------------------------------------------------------------
// TESTE — IDEMPOTENTE: a mesma tool call re-tentada não conta duas vezes.
//
// A chave é `run_id:step_id`, a mesma que o RM usa para correlacionar a mediação. Sem
// isto, um retry inflaria o consumo e o run seria estrangulado por trabalho que não fez.
// ---------------------------------------------------------------------------

func TestAOS287_RetryDaMesmaToolCallNaoContaDuasVezes(t *testing.T) {
	ctx := context.Background()
	es := storeDeTeste(t)
	const run = "run-retry"

	registar := registoDeConsumoDeToolCall(es, producerDoConsumoDeToolCall())
	for i := 0; i < 3; i++ {
		if err := registar(ctx, run, "step-unico", budget.Amount{Tokens: 50}); err != nil {
			t.Fatalf("registo #%d: %v", i, err)
		}
	}
	c, err := newTurnLedgerBurndown(es).ConsumedByRun(ctx, run)
	if err != nil {
		t.Fatalf("ConsumedByRun: %v", err)
	}
	if c.Consumed.Tokens != 50 {
		t.Fatalf("tokens = %d, quer 50 — três registos da MESMA tool call contaram mais do que uma vez", c.Consumed.Tokens)
	}
}

// ---------------------------------------------------------------------------
// TESTE — OS DOIS GUARDAS FAIL-CLOSED CONTINUAM A DENUNCIAR.
//
// É o risco real desta mudança: somar tool calls ao mesmo acumulado podia CALAR dois
// detectores que existem para apanhar composições partidas. Cada um pergunta uma coisa
// sobre o MODELO, e por isso olha para as grandezas do modelo, não para o total.
// ---------------------------------------------------------------------------

func TestAOS287_GuardasFailClosedNaoForamMascarados(t *testing.T) {
	ctx := context.Background()

	t.Run("stream vazio continua a ser sem-ledger", func(t *testing.T) {
		es := storeDeTeste(t)
		_, err := newTurnLedgerBurndown(es).ConsumedByRun(ctx, "run-vazio")
		if !errors.Is(err, ErrBurndownNoLedger) {
			t.Fatalf("err = %v, quer ErrBurndownNoLedger", err)
		}
	})

	t.Run("turnos a zero tokens continuam a denunciar, MESMO com tool calls a somar", func(t *testing.T) {
		es := storeDeTeste(t)
		const run = "run-sem-usage"
		// Um turno gravado SEM usage (o provider não ecoou) …
		if _, err := es.Append(ctx, run, eventstore.EventInput{
			Type: "turn.recorded", RunID: run, StepID: "t1",
			Payload: []byte(`{"turn":1,"input_tokens":0,"output_tokens":0,"cost_micro_usd":0}`),
		}); err != nil {
			t.Fatalf("append do turno: %v", err)
		}
		// … e uma tool call que soma tokens ao MESMO acumulado.
		if err := registoDeConsumoDeToolCall(es, producerDoConsumoDeToolCall())(ctx, run, "s1", budget.Amount{Tokens: 999}); err != nil {
			t.Fatalf("registo da tool call: %v", err)
		}

		_, err := newTurnLedgerBurndown(es).ConsumedByRun(ctx, run)
		if !errors.Is(err, ErrBurndownNoUsage) {
			t.Fatalf("err = %v, quer ErrBurndownNoUsage — os tokens da tool call MASCARARAM o silêncio do provider e o detector morreu. É a regressão que esta mudança podia introduzir", err)
		}
	})
}

// ---------------------------------------------------------------------------
// TESTE — quantia nula não escreve facto.
// ---------------------------------------------------------------------------

func TestAOS287_QuantiaNulaNaoEscreveFacto(t *testing.T) {
	ctx := context.Background()
	es := storeDeTeste(t)
	if err := registoDeConsumoDeToolCall(es, producerDoConsumoDeToolCall())(ctx, "run-zero", "s1", budget.Amount{}); err != nil {
		t.Fatalf("registo de quantia nula: %v", err)
	}
	if _, err := newTurnLedgerBurndown(es).ConsumedByRun(ctx, "run-zero"); !errors.Is(err, ErrBurndownNoLedger) {
		t.Fatalf("err = %v — uma confirmação de zero escreveu um facto que não muda nada", err)
	}
}
