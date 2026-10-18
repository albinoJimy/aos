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
	// StepID é o `step_id` da tool call (AOS-532): o do envelope leva o prefixo
	// [toolCallBudgetStepPrefix], e é por este que o facto se correlaciona com o
	// `tool.call.mediated` da mesma call. Omitido nos testes que gravam o payload à mão.
	StepID string `json:"step_id,omitempty"`
}

// toolCallBudgetStepPrefix é o prefixo do `step_id` do ENVELOPE do facto (AOS-532).
//
// # Porque uma chave própria
//
// O Event Store deduplica pela chave `run_id:step_id` DENTRO do stream, QUALQUER que seja o tipo
// do evento, e devolve `StatusDuplicate` SEM erro. No stream do run, `run_id:<step_id>` é já a
// chave do `tool.call.mediated` da mesma call (o canal de mediação durável do AOS-379, sempre
// composto no nó). Com a chave nua, este facto chegava DEPOIS da mediação, era engolido como
// duplicado e o registo devolvia sucesso: medido no AOS-530, 0 de 19 eventos no stream — o
// consumo durável das tool calls que o AOS-287 prometia não existia, e a retoma esquecia-o.
//
// `consumo-` não colide com nenhum domínio de dedup do stream do run: os `step_id` do runtime
// (`step-NNNNNN-tool-N`), o step-ledger (`ledger-`), o checkpoint (`ckpt-`), a captura de replay
// (`cap-`), o custo de uma tool que chama um modelo (`custo-modelo-`, AOS-530), o controlo
// (`ctrl-`), as transições (`state-`), as compensações (`comp-`), as leases (`lease-`) e as
// decisões e o desfecho da própria tool call (AOS-481: o `tool.call.mediated` no `step_id` nu,
// `denied-`, `escalated-` e `outcome-` nos outros três; [referencemonitor.EventStepID]). O nome
// é o do vocabulário do ficheiro (o consumo da tool call), e não `committed-`, que descreve o
// ciclo da reserva e não o facto.
//
// COMPATIBILIDADE: nenhum leitor procura este facto pela chave — o [turnLedgerBurndown] filtra
// pelo TIPO ao ler o stream —, e nenhum run antigo tem o facto com a chave nua, porque nunca
// chegou ao stream num nó composto. A idempotência por (`run_id`, `step_id`) mantém-se: o
// prefixo é uma função fixa do `step_id`.
const toolCallBudgetStepPrefix = "consumo-"

// registoDeConsumoDeToolCall constrói o registo durável sobre o Event Store.
//
// ESCREVE NO STREAM DO RUN (`stream_id == run_id`), que é onde o ledger de turnos já lê —
// é isso que torna a soma possível sem uma segunda leitura noutro store.
//
// IDEMPOTENTE por (`run_id`, `step_id`) da tool call, com a chave de envelope
// `run_id:consumo-<step_id>` ([toolCallBudgetStepPrefix], AOS-532): uma tool call re-tentada
// não conta duas vezes, e o facto não colide com o `tool.call.mediated` da mesma call. Um
// `StatusDuplicate` do Event Store é sucesso, não erro — o facto já lá está.
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
		raw, err := json.Marshal(toolCallBudgetPayload{Tokens: amt.Tokens, CostMicroUSD: amt.CostMicroUSD, StepID: stepID})
		if err != nil {
			return err
		}
		_, err = es.Append(ctx, runID, eventstore.EventInput{
			Type:     EventTypeToolCallBudget,
			Payload:  raw,
			RunID:    runID,
			StepID:   toolCallBudgetStepPrefix + stepID,
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
