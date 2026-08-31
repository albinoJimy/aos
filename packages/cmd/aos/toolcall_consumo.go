package main

import (
	"context"
	"encoding/json"
	"fmt"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/substrate/eventstore"
)

// toolcall_consumo.go — O REGISTO DURÁVEL DO CONSUMO DE UMA TOOL CALL (AOS-287).
//
// # A metade que faltava
//
// O AOS-256 fez o tecto por-run sobreviver à re-incarnação: o nó do run nasce com
// `tecto − já consumido`, lido do ledger de turnos. Mas esse ledger conta TURNOS DE MODELO
// e só eles — as tool calls reservam do MESMO nó e não deixavam rasto durável, pelo que
// cada incarnação as esquecia. Um run que escale/retome N vezes podia gastar até
// `tecto + N × (tool calls por incarnação)`, e escalar/retomar é o fluxo NORMAL de tudo o
// que exige aprovação humana.
//
// Este ficheiro é a ESCRITA; a leitura é o `ConsumedByRun` do burn-down, que passa a somar
// este facto ao lado dos turnos.
//
// # Porque um evento PRÓPRIO, e não os eventos do Budget
//
// O `budget` tem `WithEmitter`/`Rebuild` e ambos existem sem chamadores. Ligá-los era a via
// óbvia e está ERRADA: eles registam TODAS as movimentações — incluindo as do turno de
// modelo, que o ledger de turnos JÁ conta. Somar as duas fontes contaria os turnos DUAS
// VEZES, e a divergência entre duas contabilidades do mesmo tecto seria silenciosa.
//
// Este evento é estreito de propósito: só o que o ledger de turnos NÃO vê.

// EventTypeToolCallBudget é o facto durável do consumo CONFIRMADO de uma tool call.
// Constante junto do emissor (disciplina do event-catalog, tecnica/13 §3.3) — nunca um
// literal no caminho de emissão.
const EventTypeToolCallBudget = "budget.toolcall.committed"

// toolCallBudgetPayload é o corpo do facto. Sem segredos: só a quantia debitada.
//
// Os nomes dos campos espelham os de [budget.Amount] para que a soma no burn-down leia o
// mesmo vocabulário que o resto do orçamento.
type toolCallBudgetPayload struct {
	Tokens       int64 `json:"tokens"`
	CostMicroUSD int64 `json:"cost_micro_usd"`
}

// registoDeConsumoDeToolCall constrói o registo durável sobre o Event Store.
//
// ESCREVE NO STREAM DO RUN (`stream_id == run_id`), que é onde o ledger de turnos já lê —
// é isso que torna a soma possível sem uma segunda leitura noutro store.
//
// IDEMPOTENTE por `run_id:step_id`: é a mesma chave que o Reference Monitor usa para
// correlacionar a mediação, e é o que garante que uma tool call re-tentada não conta duas
// vezes. Um `StatusDuplicate` do Event Store é sucesso, não erro — o facto já lá está.
func registoDeConsumoDeToolCall(es eventstore.EventStore, producer eventstore.Producer) func(context.Context, string, string, budget.Amount) error {
	if es == nil {
		return nil
	}
	return func(ctx context.Context, runID, stepID string, amt budget.Amount) error {
		if runID == "" || stepID == "" {
			return fmt.Errorf("aos: consumo de tool call sem run_id/step_id (run=%q step=%q)", runID, stepID)
		}
		// Uma confirmação de quantia nula não é um facto: não muda o consumo e escrevê-la
		// só encheria o stream. O caminho normal nunca a produz (o estimador tem piso 1).
		if amt.Tokens == 0 && amt.CostMicroUSD == 0 {
			return nil
		}
		raw, err := json.Marshal(toolCallBudgetPayload{Tokens: amt.Tokens, CostMicroUSD: amt.CostMicroUSD})
		if err != nil {
			return err
		}
		_, err = es.Append(ctx, runID, eventstore.EventInput{
			Type:     EventTypeToolCallBudget,
			Payload:  raw,
			RunID:    runID,
			StepID:   stepID,
			Producer: producer,
		})
		return err
	}
}

// toolCallBudgetNHI é a identidade emissora deste facto. Segue o mesmo molde do
// `retentionSchedulerNHI`: um sub-papel nomeado do próprio nó, para que a atribuição no log
// diga QUEM escreveu sem se confundir com o run que consumiu.
const toolCallBudgetNHI = "nhi:aos-node/budget-toolcall"

// producerDoConsumoDeToolCall é a identidade emissora do facto de consumo.
func producerDoConsumoDeToolCall() eventstore.Producer {
	return eventstore.Producer{NHIID: toolCallBudgetNHI}
}
