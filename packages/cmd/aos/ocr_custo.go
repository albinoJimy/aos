package main

// ocr_custo.go — O CUSTO DA `ocr_read` (AOS-530, peça 3b; ADR-042 §2.6): a reserva no tecto do run
// ANTES do efeito e o evento durável do custo DEPOIS dele.
//
// # A reserva
//
// Depois de a imagem ser lida e validada (as recusas da leitura não custam nada) e antes do pedido
// ao gateway, a tool reserva no nó de orçamento do run o MÁXIMO que uma chamada pode custar:
//
//	tokens = ocrTokensDeEntradaMax + ocrMaxTokens
//	custo  = tabela de preços do nó (alias, região) sobre esses tokens — os de entrada ao preço de
//	         entrada, os de saída ao de saída —, pelo MESMO calculador que o gateway usa no custo
//	         medido ([cost.Calculator]); zero sem a contabilidade de custo composta
//
// Se não couber no que resta ao run — tokens ou dólares, como para um turno —, a tool é negada
// com `tecto_do_run` e o provider recebe zero pedidos. Depois da resposta salda-se pelo MEDIDO
// ([integration.ReservaDeEfeito.Saldar]); uma chamada que falhou sem resposta liberta a reserva.
//
// SEM ORÇAMENTO COMPOSTO, NÃO HÁ RESERVA — exactamente como para os turnos: a admissão do turno só
// existe com `AOS_BUDGET_MAX_TOKENS` (o orçamento por-run); sem `AOS_BUDGET_MAX_COST_MICRO_USD` a
// dimensão em dólares é medida e nunca nega ([integration.UnlimitedCostMicroUSD]), e a reserva da
// tool segue a mesma regra.
//
// Uma chamada SEM RESPOSTA (erro antes do provider, prazo, cancelamento, panic no gateway) liberta
// a reserva. Uma resposta NÃO MEDIDA (sem `usage`, custo não calculável, corpo que o adaptador não
// interpreta) cobra a reserva inteira — ver [ligacaoDeModelo.fecharPago].
//
// # O evento durável
//
// Cada resposta do provider grava UM evento [EventTypeToolModelCost] no stream do run — também
// quando a tool recusa a resposta (cortada, vazia, com uma tool call) e quando o gateway a recusa
// depois de o provider responder (o modelo servido diferente em `enforce`, uma resposta sem
// `usage` com a contabilidade composta, um corpo ilegível), que chega num
// [modelgateway.PaidRejectionError] com o custo ou com a marca de não medido. Só números e nomes de vocabulário fechado: o custo, os
// tokens, o alias, o modelo servido, o resultado da tool; nunca o texto nem os bytes.
//
// IDEMPOTENTE por (`run_id`, `step_id`) da tool call. A chave do envelope é
// `run_id:custo-modelo-<step_id>` e não `run_id:<step_id>`: no stream do run, `run_id:<step_id>` é já
// a chave do `tool.call.mediated` da mesma call (AOS-379), e o Event Store deduplica por chave
// dentro do stream, QUALQUER que seja o tipo — o evento de custo seria engolido como duplicado. É o
// precedente do step-ledger (`ledger-<step_id>`). O `step_id` da call vai no payload.
//
// SOMA AO CONSUMO DURÁVEL do run ao lado dos turnos ([turnLedgerBurndown.ConsumedByRun]), e é por
// essa soma que a incarnação seguinte de um run retomado nasce com `tecto − já consumido`
// ([integration.RunBudget]) e que a reserva da chamada seguinte o vê.
//
// O REPLAY E A DEDUP NÃO GRAVAM: o replay não executa a tool (o resultado vem do step-ledger e da
// captura), e um despacho repetido do mesmo passo bate no `already-applied` do step-ledger antes do
// efeito. Uma queda entre a resposta e o step-ledger repete o efeito (paga-se duas vezes, declarado
// no ADR-042 §5); o segundo evento é deduplicado pela chave e o registo guarda um.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/integration"
	"github.com/aos-ref/platform/model-gateway/metering/cost"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/pricing"
	"github.com/aos-ref/substrate/eventstore"
)

// EventTypeToolModelCost é o facto durável do custo de uma tool cujo efeito é uma chamada a um
// modelo (ADR-042 §2.6). Constante junto do emissor (disciplina do event-catalog, tecnica/13 §3.3).
const EventTypeToolModelCost = "budget.toolcall.model_cost"

// ocrCustoStepPrefix é o prefixo do `step_id` do envelope do evento de custo (ver o cabeçalho).
const ocrCustoStepPrefix = "custo-modelo-"

// toolModelCostNHI é a identidade emissora do facto: um sub-papel nomeado do nó, no molde de
// [toolCallBudgetNHI].
const toolModelCostNHI = "nhi:aos-node/budget-toolmodel"

// ocrTokensDeEntradaMax é o tecto de tokens de ENTRADA de uma chamada da `ocr_read`: a instrução
// fixa (~70 tokens) mais UMA imagem.
//
// PORQUE 8192. Os fornecedores de modelos de visão contam uma imagem por blocos de pixels e
// REDIMENSIONAM as imagens grandes antes de as contar; a ordem de grandeza que documentam para a
// maior imagem que aceitam sem a reduzir mais é de ~1 600 tokens (Anthropic: ~largura×altura/750,
// até ~1,15 megapixels; OpenAI, em alta definição: 85 + 170 por bloco de 512, até ~1 450). 8192 é
// ~5× essa ordem: cobre um fornecedor que conte com mais resolução e a instrução, sem que a
// reserva negue uma chamada num run com folga real. Não é uma medição: o `usage` que o proxy
// devolve para um pedido com imagem fica por confirmar na série de produção (Notas do AOS-530), e
// um custo medido ACIMA da reserva grava-se como foi — a reserva só decide se a chamada sai.
const ocrTokensDeEntradaMax = 8192

// Causas de falha da reserva. Vocabulário fechado, como as outras ([ocrCausas]).
const (
	// ocrCausaTecto — o máximo da chamada não cabe no que resta ao run (tokens ou dólares).
	ocrCausaTecto = "tecto_do_run"
	// ocrCausaOrcamento — a reserva falhou por outra razão que não o tecto (o run sem nó de
	// orçamento, uma quantia inválida): um defeito de composição, fail-closed.
	ocrCausaOrcamento = "orcamento_indisponivel"
)

// custoDeToolPayload é o corpo do evento [EventTypeToolModelCost]. Sem texto, sem bytes, sem o
// `doc_id`: só o que mede a chamada.
type custoDeToolPayload struct {
	// Tool é o nome da tool (`ocr_read`).
	Tool string `json:"tool"`
	// StepID é o `step_id` da tool call (o do envelope leva o prefixo [ocrCustoStepPrefix]).
	StepID string `json:"step_id"`
	// Alias é o alias pedido; ModeloServido, o que o proxy declarou ter servido (vazio se não
	// declarou).
	Alias         string `json:"alias"`
	ModeloServido string `json:"modelo_servido,omitempty"`
	// Resultado é o desfecho da tool, no vocabulário de [ocrCausas] (`transcrito` ou a causa).
	Resultado string `json:"resultado"`
	// Tokens e custo MEDIDOS pelo gateway (o custo pela tabela de preços; zero sem contabilidade) —
	// ou, com UsageAusente, os COBRADOS: a reserva inteira, ou zeros sem orçamento composto.
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CostMicroUSD int64 `json:"cost_micro_usd"`
	// UsageAusente marca uma resposta paga e NÃO medida (sem usage, custo não calculável, corpo
	// ilegível): os números acima não são uma medição, são o que se cobrou ([ligacaoDeModelo.fecharPago]).
	UsageAusente bool `json:"usage_ausente,omitempty"`
}

// registoDeCustoDeTool constrói a escrita durável do custo sobre o Event Store do nó: no stream do
// run, idempotente por (`run_id`, `step_id`). nil com store nil.
func registoDeCustoDeTool(es eventstore.EventStore, producer eventstore.Producer) func(context.Context, string, custoDeToolPayload) error {
	if es == nil {
		return nil
	}
	return func(ctx context.Context, runID string, p custoDeToolPayload) error {
		if runID == "" || p.StepID == "" {
			return fmt.Errorf("aos: custo de tool sem run_id/step_id (run=%q step=%q)", runID, p.StepID)
		}
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		_, err = es.Append(ctx, runID, eventstore.EventInput{
			Type:     EventTypeToolModelCost,
			Payload:  raw,
			RunID:    runID,
			StepID:   ocrCustoStepPrefix + p.StepID,
			Producer: producer,
		})
		return err
	}
}

// producerDoCustoDeTool é a identidade emissora do facto de custo.
func producerDoCustoDeTool() eventstore.Producer {
	return eventstore.Producer{NHIID: toolModelCostNHI}
}

// reservaMaxDoOCR devolve o máximo por chamada (ver o cabeçalho). Sem tabela, o custo é zero e a
// reserva é só de tokens; com tabela e sem preço para o par, erro (o arranque já o recusa antes).
func reservaMaxDoOCR(tabela *pricing.Table, alias, region string) (budget.Amount, error) {
	tecto := budget.Amount{Tokens: ocrTokensDeEntradaMax + ocrMaxTokens}
	if tabela == nil {
		return tecto, nil
	}
	amt, err := cost.NewCalculator(tabela).Cost(cost.TokenCounts{PromptTokens: ocrTokensDeEntradaMax, CompletionTokens: ocrMaxTokens}, alias, region)
	if err != nil {
		return budget.Amount{}, fmt.Errorf("%w: alias %q, regiao %q: %v", ErrOCRSemPreco, alias, region, err)
	}
	tecto.CostMicroUSD = amt.CostMicroUSD
	return tecto, nil
}

// ligarCusto liga a ligação ao orçamento por-run do nó e à escrita durável do custo. Corre no
// Bootstrap, depois de o Event Store e o orçamento existirem (fase 2, como o AOS-287). Nil-safe.
func (l *ligacaoOCR) ligarCusto(rb *integration.RunBudget, registar func(context.Context, string, custoDeToolPayload) error, logf func(string, ...any)) {
	if l != nil {
		l.ligacaoDeModelo.ligarCusto(rb, registar, logf)
	}
}

// ligarCusto é a parte comum de ligar o custo de uma tool de modelo.
func (l *ligacaoDeModelo) ligarCusto(rb *integration.RunBudget, registar func(context.Context, string, custoDeToolPayload) error, logf func(string, ...any)) {
	l.orcamento = rb
	l.registarCusto = registar
	if logf != nil {
		l.logf = logf
	}
}

// reservar reserva o máximo da chamada no tecto do run. Devolve a reserva (nil sem orçamento
// composto) ou a causa da recusa.
func (l *ligacaoDeModelo) reservar(ctx context.Context, runID string, maximo budget.Amount) (*integration.ReservaDeEfeito, string) {
	if l.orcamento == nil {
		return nil, ""
	}
	r, err := l.orcamento.ReservarEfeitoDeTool(ctx, runID, maximo)
	switch {
	case err == nil:
		return r, ""
	case errors.Is(err, budget.ErrNoHeadroom):
		return nil, ocrCausaTecto
	default:
		return nil, ocrCausaOrcamento
	}
}

// fecharPago salda a reserva e grava o evento durável de uma chamada que o provider respondeu,
// qualquer que seja o resultado da tool, e devolve o custo cobrado (micro-USD). Corre sem o
// cancelamento do contexto do run: o caminho cancelado é o que mais precisa de deixar o custo
// gravado.
//
// MEDIDA, salda-se pelo medido. NÃO MEDIDA — o provider não ecoou `usage`, o custo não se calculou,
// ou a resposta não se interpretou —, cobra-se a RESERVA INTEIRA, o molde da admissão do turno
// (`integration/model_admission.go`, «usage a zero ⇒ fica a provisão cobrada»): saldar por zero
// faria uma chamada paga não descer o tecto. O evento leva então `usage_ausente` e os valores
// cobrados (os tectos de tokens da chamada, `cobrar`, e o custo reservado), para que o consumo
// durável os some. Sem orçamento composto não houve reserva: o evento sai com `usage_ausente` e zeros.
func (l *ligacaoDeModelo) fecharPago(ctx context.Context, reserva *integration.ReservaDeEfeito, in entradaOCR, cobrar tectoDaChamada, usage port.Usage, servido, resultado string) int64 {
	ctx = context.WithoutCancel(ctx)
	p := custoDeToolPayload{
		Tool: l.tool, StepID: in.StepID, Alias: l.alias, ModeloServido: servido, Resultado: resultado,
		InputTokens: usage.PromptTokens, OutputTokens: usage.CompletionTokens, CostMicroUSD: usage.CostMicroUSD,
	}
	if !usage.Definido() {
		p.UsageAusente = true
		p.InputTokens, p.OutputTokens, p.CostMicroUSD = 0, 0, 0
		if reserva != nil {
			p.InputTokens, p.OutputTokens, p.CostMicroUSD = cobrar.entrada, cobrar.saida, reserva.Reservado().CostMicroUSD
		}
	}
	real := budget.Amount{Tokens: p.InputTokens + p.OutputTokens, CostMicroUSD: p.CostMicroUSD}
	if _, err := reserva.Saldar(ctx, real); err != nil {
		l.logf("[aos] %s: saldar a reserva do passo %q do run %q FALHOU (%v) — o tecto do run nesta incarnacao pode nao refletir o custo; o evento duravel do custo segue", l.rotulo, in.StepID, in.RunID, err)
	}
	if l.registarCusto == nil {
		return p.CostMicroUSD
	}
	if err := l.registarCusto(ctx, in.RunID, p); err != nil {
		// FAIL-SOFT DECLARADO, a escolha do AOS-287: o efeito já aconteceu e foi pago; falhar a tool
		// agora trocaria um custo por gravar por um run partido. Não se degrada em silêncio.
		l.logf("[aos] %s: gravar o custo do passo %q do run %q FALHOU (%v) — a chamada foi paga (%d micro-USD) e a incarnacao seguinte NAO o vera no consumo duravel. Degradacao declarada, nao silenciosa", l.rotulo, in.StepID, in.RunID, err, p.CostMicroUSD)
	}
	return p.CostMicroUSD
}

// libertar devolve a reserva de uma chamada que não teve resposta (erro antes do provider, prazo,
// cancelamento, panic no gateway). Sobre uma reserva já saldada não faz nada.
func (l *ligacaoDeModelo) libertar(ctx context.Context, reserva *integration.ReservaDeEfeito, in entradaOCR) {
	if err := reserva.Libertar(context.WithoutCancel(ctx)); err != nil {
		l.logf("[aos] %s: libertar a reserva do passo %q do run %q FALHOU (%v)", l.rotulo, in.StepID, in.RunID, err)
	}
}

// posturaDoCusto declara, no arranque, o que acontece ao custo de cada chamada (ADR-042 §2.6), com
// a descrição da reserva da tool.
func (l *ligacaoDeModelo) posturaDoCusto(reserva string) string {
	evento := "o custo medido pelo gateway grava-se no evento duravel " + EventTypeToolModelCost + " do run, por (run_id, step_id), tambem quando a resposta e recusada, e soma ao consumo duravel do run ao lado dos turnos"
	if l.registarCusto == nil {
		evento = "SEM Event Store composto o custo medido NAO se grava no run (vai so ao span aos.activity)"
	}
	if l.orcamento == nil {
		return evento + "; SEM orcamento por-run composto (AOS_BUDGET_MAX_TOKENS por definir) NAO ha reserva no tecto, como para os turnos"
	}
	dolares := "a dimensao em dolares e medida e nao nega (AOS_BUDGET_MAX_COST_MICRO_USD por definir)"
	if tecto, ok := l.orcamento.MaxCostMicroUSDPerRun(); ok {
		dolares = fmt.Sprintf("tecto em dolares %d micro-USD por run", tecto)
	}
	return fmt.Sprintf("%s; antes de cada chamada reserva no tecto do run %s, e sem folga a tool e negada com %s sem pedido ao provider (%s)",
		evento, reserva, ocrCausaTecto, dolares)
}
