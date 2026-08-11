package progresssurface

// AOS-261 — A FONTE DO BURN-DOWN.
//
// # O defeito que este ficheiro fecha
//
// [ProgressSurface.Evaluate] recebia os spans POR PARÂMETRO e nada, em nenhum nó, os
// produzia ou retinha: o tracer por omissão é o `NoopTracer` e o `SpanTracer` real
// dispara-e-esquece (exporta e larga). O chamador só tinha uma fatia vazia para passar, e
// [ComputeBurndown] sobre uma fatia vazia devolve `Consumed{0,0}` e `Fraction 0` — uma
// SUPERFÍCIE VERDE A MENTIR: 0% consumido é indistinguível de «não há dados», e o prompt de
// exaustão nunca dispararia por não haver fonte, exactamente quando mais faria falta.
//
// # As DUAS alternativas, e porque esta
//
// (a) DECORADOR DE EXPORTER que RETÉM `SpanData` por-run, com política de retenção própria
// + resolvedores `runID→traceID` e `runID→treeID`.
//
// (b) FONTE SOBRE O LEDGER DE TURNOS — o evento `turn.recorded` que o [TurnRecorder] do
// agent-runtime já grava no Event Store por CADA turno, com `input_tokens`/`output_tokens`/
// `cost_micro_usd` no payload.
//
// ESCOLHIDA A (b), por quatro razões que não são de gosto:
//
//  1. IMUNIDADE À RE-EMISSÃO. Um retentor de spans conta o que lhe chega; um turno
//     re-emitido (retry do exportador, dois tracers ligados ao mesmo trace, uma retoma que
//     reabre o turno) é contado DUAS vezes e o burn-down passa a inflacionar — o modo de
//     falha exacto que o desafio A2 nomeia (tokens a 2×). O ledger de turnos é
//     DEDUPLICADO na origem pela idempotency_key `run_id:step_id` do Event Store: um
//     mesmo turno gravado duas vezes ocupa UMA entrada. A contagem é idempotente por
//     construção, não por cuidado do leitor.
//  2. NÃO É PRECISO INVENTAR RETENÇÃO. O retentor precisaria de uma política nova (tecto
//     de memória, despejo, TTL) — um subsistema a mais, com o seu próprio modo de falha
//     silencioso: despejar os spans de um run vivo devolve o burn-down a zero sem que
//     ninguém saiba. O ledger herda a retenção DURÁVEL que o nó já tem
//     (`AOS_RETENTION_*`) e sobrevive ao restart do processo.
//  3. O RESOLVEDOR `runID→traceID` DEIXA DE SER NECESSÁRIO. Era ele o problema
//     multi-incarnação: cada retoma abre um trace NOVO, pelo que agregar por trace conta
//     só a incarnação corrente e o burn-down RESSUSCITA a zero depois de um crash. A
//     chave do ledger é o `run_id`, que é o MESMO em todas as incarnações — ver a
//     política em [BurndownSource]. `runID→treeID` também desaparece do caminho crítico:
//     o nó de orçamento por-run é registado com o próprio RunID (`integration.RunBudget`),
//     pelo que a resolução é a identidade e está DECLARADA no adaptador, não adivinhada.
//  4. É O MESMO NÚMERO QUE O RUN PAGOU. O `cost_micro_usd`/`usage` do ledger é o que o
//     modelo devolveu (`resp.Usage`), gravado na mesma transacção que torna o turno
//     durável. Não há uma segunda contabilidade a divergir da primeira.
//
// O que se PERDE com (b), declarado: o ledger só tem o TURNO DE MODELO. O custo de uma
// tool call não está lá (nenhum evento de mediação carrega tokens), e por isso o
// [Burndown] desta fonte é um LIMITE INFERIOR do consumo do run — ver [RunConsumption].
// Um limite inferior faz o aviso disparar TARDE, nunca cedo por engano; a alternativa (a)
// não estava melhor servida (os spans `chat` são exactamente a mesma dimensão).

import (
	"context"

	"github.com/aos-ref/control-plane/budget"
)

// RunConsumption é o consumo ACUMULADO de um run, lido do LEDGER DE TURNOS.
//
// ALCANCE, declarado para não ser confundido com «tudo o que o run gastou»: conta os
// TURNOS DE MODELO (a linha de custo dominante) e SÓ eles. Não conta tool calls (o ledger
// de turnos não as pesa) nem sub-runs delegados (outro `run_id`, outro stream). É por isso
// um LIMITE INFERIOR do consumo — quem o lê não pode apresentá-lo como total.
type RunConsumption struct {
	// Consumed é o acumulado nas duas dimensões de [budget.Amount]. A dimensão
	// CostMicroUSD vale o que o produtor do ledger lá pôs: enquanto o canal de custo não
	// estiver ligado ponta a ponta (eixo AOS-259), é ZERO — e um zero honesto, porque a
	// fracção só usa dimensões com limite positivo (ver consumedFraction).
	//
	// A dimensão COM limite (Tokens) a ZERO já não é honesta: é a que decide a fracção, e
	// zero nela com Turns > 0 é ausência de dados, que a fonte tem de reportar como ERRO.
	Consumed budget.Amount
	// Turns é o nº de turnos que a fonte SOMOU. Zero turnos NÃO é um consumo de zero: é
	// ausência de dados, e a fonte tem de o reportar como ERRO (ver [BurndownSource]).
	// Turns > 0 NÃO chega para a leitura ser válida — ver a nota sobre a grandeza em
	// [BurndownSource].
	Turns int
	// LastTurn é o índice do último turno contado (o `turn` do payload). Serve a
	// correlação `(runID, turn)` do aviso — não entra em nenhuma aritmética.
	LastTurn int
}

// BurndownSource é a PORTA de LEITURA do consumo acumulado de um run. O adaptador de
// produção vive no NÓ (node-local, sobre o Event Store onde o [TurnRecorder] grava) — o
// core não importa o Event Store nem sabe o formato do evento.
//
// # CONTRATO FAIL-CLOSED (o critério duro de AOS-261)
//
// A ausência de dados é um ERRO, NUNCA um zero. Um adaptador que devolva
// `RunConsumption{}` e `nil` para um run sem ledger reintroduz exactamente a superfície
// verde a mentir que esta porta existe para remover: um burn-down que diz 0% por não ter
// fonte é PIOR do que não existir, porque parece protecção. Sem fonte ⇒ erro; com fonte e
// sem entradas ⇒ erro; com fonte ilegível ⇒ erro.
//
// E — a dobra que falta a quem põe o guarda só na contagem de entradas — COM ENTRADAS MAS
// COM A DIMENSÃO QUE DECIDE A ZERO ⇒ erro TAMBÉM. `Turns > 0` com `Consumed.Tokens == 0`
// não é «este run ainda não gastou nada»: um turno de modelo sem tokens é impossível (há
// sempre o prompt), logo é ausência de dados com a forma de uma leitura. O que alimenta
// [BurndownFromConsumption] é a GRANDEZA, não a contagem — é sobre ela que o fail-closed
// tem de morder, senão a fracção fica 0 para sempre e o aviso nunca dispara.
//
// # POLÍTICA MULTI-INCARNAÇÃO (AOS-261, critério 2)
//
// A chave é o `run_id` e o consumo é CUMULATIVO ao longo de TODAS as incarnações do run:
//
//   - PREFIXO T1 (a incarnação que crashou/pausou) — os turnos já gravados permanecem no
//     stream e CONTINUAM a contar. É o comportamento correcto para um burn-down: o
//     dinheiro/tokens de T1 foram gastos e não voltam por o processo ter reiniciado.
//     A alternativa (agregar por trace) zerava aqui, e um run que se retomasse em ciclo
//     nunca atingiria o limiar — o pior modo de falha possível para um aviso de exaustão.
//   - REPRODUÇÃO T2 (a retoma) — os turnos que o replay reproduz a partir do prefixo NÃO
//     acrescentam entradas: a idempotency_key `run_id:step_id` é a mesma e o Event Store
//     devolve `StatusDuplicate` sem escrever. Só o trabalho NOVO de T2 aumenta o
//     acumulado. Não há dupla contagem a corrigir a jusante porque nunca chega a existir.
//
// Um run DELEGADO é um `run_id` diferente e um stream diferente: o consumo do filho NÃO
// entra no do pai (a agregação por parentesco é o eixo AOS-259, não este).
type BurndownSource interface {
	// ConsumedByRun devolve o consumo acumulado do run. Erro (nunca um zero silencioso)
	// quando não há ledger, quando o ledger não tem turnos, ou quando é ilegível.
	ConsumedByRun(ctx context.Context, runID string) (RunConsumption, error)
}

// BurndownFromConsumption deriva o [Burndown] do consumo lido da fonte e do limite lido da
// porta [BudgetReader]. É a MESMA fórmula de fracção de [ComputeBurndown] (max sobre as
// dimensões com limite positivo) — a fonte muda, a aritmética não.
func BurndownFromConsumption(c RunConsumption, limit budget.Amount) Burndown {
	return Burndown{
		Consumed: c.Consumed,
		Limit:    limit,
		Fraction: consumedFraction(c.Consumed, limit),
	}
}
