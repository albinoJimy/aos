package modelgateway_test

// AOS-259 — O CANAL DE CUSTO PONTA A PONTA, EM COMPOSIÇÃO REAL.
//
// Não simula nenhuma das peças vizinhas: monta o Agent Runtime REAL (loop, span `chat`,
// TurnRecorder sobre um Event Store REAL), o Model Gateway REAL (pipeline, metering,
// contabilidade de custo sobre a tabela de preços versionada) e o adaptador canónico
// RT→GW entre os dois, com o MESMO tracer nas duas camadas. É a única maneira de a
// asserção significar alguma coisa: cada uma das lacunas que este ticket fecha vivia
// EXACTAMENTE na costura entre duas peças que, isoladas, estavam certas — o gateway
// calculava o custo e não o devolvia; o runtime tinha o campo e recebia zero.
//
// O que a composição prova, na MESMA passagem:
//
//	(1) o custo derivado pela tabela de preços chega ao runtime (Result.TotalCostMicroUSD);
//	(2) chega ao evento DURÁVEL `turn.recorded`, que é a fonte do burn-down do nó (AOS-261);
//	(3) a dupla observação do turno (span `chat` do RT + span `chat` do GW) é REAL nesta
//	    topologia — o teste conta os dois spans, não o assume;
//	(4) e mesmo assim a contabilidade por trajectória dá TOKENS 1x COM CUSTO REAL.
//
// (3) é a razão de (4) existir: sem a dedup por parentesco, ligar o canal de custo teria
// duplicado tokens e custo numa leitura que hoje está correcta.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/internal/adapters"
	"github.com/aos-ref/platform/model-gateway/metering/cost"
	"github.com/aos-ref/platform/model-gateway/port"
	"github.com/aos-ref/platform/model-gateway/pricing"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// O par (modelo, região) TEM de existir na tabela de preços embebida: é essa a fonte do
// número. Escolhe-se um par de referência real do documento, não um inventado para o teste.
const (
	costChannelModel  = "claude-sonnet"
	costChannelRegion = "eu-west"

	// Tokens que o provider (fake adapter) ecoa nesta chamada.
	costChannelPromptTokens     = int64(1200)
	costChannelCompletionTokens = int64(340)

	// GÉMEO INDEPENDENTE do cálculo de custo: derivado À MÃO dos rates publicados em
	// pricing_table.json para (claude-sonnet, eu-west) — input 3_000_000 micro-USD/Mtok e
	// output 15_000_000 micro-USD/Mtok — e não pedido ao cost.Calculator que está sob
	// teste. Sem cache read/write, o input facturável é o prompt inteiro:
	//
	//	1200 * 3_000_000/1_000_000 + 340 * 15_000_000/1_000_000 = 3600 + 5100 = 8700
	//
	// Se a tabela mudar, este número tem de mudar com ela — é essa a intenção: o custo é
	// um facto pinado, não o que a função devolver.
	costChannelWantMicroUSD = int64(1200*3 + 340*15)
)

// TestAOS259_CanalDeCusto_PontaAPonta_TokensUmaVezComCustoReal é o teste da AC do ticket.
func TestAOS259_CanalDeCusto_PontaAPonta_TokensUmaVezComCustoReal(t *testing.T) {
	t.Parallel()

	// --- Model Gateway REAL, com contabilidade de custo sobre a tabela EMBEBIDA ---
	table, err := pricing.LoadEmbedded()
	if err != nil {
		t.Fatalf("pricing.LoadEmbedded: %v", err)
	}
	if _, ok := table.RateFor(costChannelModel, costChannelRegion); !ok {
		t.Fatalf("pre-condicao do teste falhou: a tabela embebida nao tem preco para (%s, %s)", costChannelModel, costChannelRegion)
	}
	recorder := cost.NewRecorder(cost.NewCalculator(table))

	fake := adapters.NewFakeAdapter("fake")
	fake.SetChatResponse(costChannelModel, port.ChatResponse{
		Choices: []port.Choice{{
			Message:      port.Message{Role: port.RoleAssistant, Content: "concluído"},
			FinishReason: "stop",
		}},
		// O provider ecoa TOKENS. Não ecoa custo — nenhum provider compatível OpenAI o faz;
		// é o gateway que o deriva. Repare-se que Usage.CostMicroUSD fica a ZERO aqui.
		Usage: port.Usage{
			PromptTokens:     costChannelPromptTokens,
			CompletionTokens: costChannelCompletionTokens,
			TotalTokens:      costChannelPromptTokens + costChannelCompletionTokens,
		},
	})

	// UM tracer para as DUAS camadas — é o que torna a dupla observação possível, e é a
	// topologia que qualquer composição com observabilidade ligada produz.
	tracer := otelgenai.NewRecordingTracer(nil)

	creds := adapters.NewStaticCredentialSource()
	creds.Set(fake.Provider(), costChannelRegion, "sk-teste")
	gw := modelgateway.New(fake,
		modelgateway.WithCredentialSource(creds),
		modelgateway.WithDefaultRegion(costChannelRegion),
		modelgateway.WithClock(func() time.Time { return time.Unix(1_700_000_000, 0) }),
		modelgateway.WithTracer(tracer),
		modelgateway.WithCost(recorder),
	)

	// --- Adaptador canónico RT→GW + Agent Runtime REAL ---
	const runID = "run-aos259"
	mc := modelgateway.NewModelClient(gw, costChannelModel,
		modelgateway.WithRegionBoard(costChannelRegion, "board-teste"),
		modelgateway.WithRun(runID),
	)

	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	rt := agentruntime.New(mc, referencemonitor.New(), agentruntime.NewTurnRecorder(store),
		agentruntime.WithTracer(tracer))

	res, err := rt.Run(context.Background(), agentruntime.Goal{
		RunID:     runID,
		Principal: referencemonitor.Principal{NHIID: "nhi:teste"},
		Model:     agentruntime.ModelConfig{ModelID: costChannelModel},
		System:    "sistema de teste",
		Objective: "um turno, final",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Turns != 1 {
		t.Fatalf("esperava 1 turno, veio %d (a aritmetica de custo abaixo assume um turno)", res.Turns)
	}

	// (1) O CUSTO CHEGA AO RUNTIME. Antes deste ticket este valor era ZERO com o metering
	// do gateway a funcionar: o número existia e não tinha por onde voltar.
	if res.TotalCostMicroUSD != costChannelWantMicroUSD {
		t.Errorf("Result.TotalCostMicroUSD = %d, quer %d (custo derivado da tabela %s)",
			res.TotalCostMicroUSD, costChannelWantMicroUSD, table.Version())
	}

	// (2) O CUSTO CHEGA AO LEDGER DURÁVEL — a fonte do burn-down do nó (AOS-261 lê
	// `cost_micro_usd` dos eventos `turn.recorded`). Ler o evento REAL, e não o campo em
	// memória, é o que prova que a dimensão de dólares do burn-down deixou de ser zero.
	events, err := store.Read(context.Background(), runID, 0)
	if err != nil {
		t.Fatalf("store.Read: %v", err)
	}
	var ledgerCost, ledgerIn, ledgerOut int64
	var turns int
	for _, ev := range events {
		if ev.Type != agentruntime.EventTypeTurnRecorded {
			continue
		}
		var p struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
			CostMicroUSD int64 `json:"cost_micro_usd"`
		}
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("payload de turn.recorded ilegivel: %v", err)
		}
		turns++
		ledgerIn += p.InputTokens
		ledgerOut += p.OutputTokens
		ledgerCost += p.CostMicroUSD
	}
	if turns != 1 {
		t.Fatalf("esperava 1 evento turn.recorded, vieram %d", turns)
	}
	if ledgerCost != costChannelWantMicroUSD {
		t.Errorf("turn.recorded.cost_micro_usd = %d, quer %d — o burn-down leria custo zero", ledgerCost, costChannelWantMicroUSD)
	}
	if ledgerIn != costChannelPromptTokens || ledgerOut != costChannelCompletionTokens {
		t.Errorf("tokens no ledger = in:%d out:%d, quer in:%d out:%d", ledgerIn, ledgerOut, costChannelPromptTokens, costChannelCompletionTokens)
	}

	// (3) A DUPLA OBSERVAÇÃO É REAL NESTA TOPOLOGIA. Com o mesmo tracer nas duas camadas, um
	// turno produz DOIS spans `chat` (o do runtime e o do gateway aninhado nele). Verifica-se
	// que existem mesmo — se um dia deixarem de existir, o teste da dedup passaria a provar
	// o vazio e este guarda avisa.
	chats := tracer.SpansByOperation(agentruntime.OpChat)
	if len(chats) != 2 {
		t.Fatalf("esperava 2 spans chat (runtime + gateway) para UM turno, vieram %d — a topologia que a dedup existe para tratar deixou de se reproduzir", len(chats))
	}

	// (4) E MESMO ASSIM: TOKENS 1x, COM CUSTO REAL. As duas asserções na MESMA passagem —
	// um agregador que zerasse o custo também daria "tokens 1x", e um que somasse os dois
	// spans daria "custo real" a dobrar.
	totals := otelgenai.AggregateRecordedByTrace(tracer.Spans())
	if len(totals) != 1 {
		t.Fatalf("esperava 1 trajectoria, vieram %d", len(totals))
	}
	var got otelgenai.UsageTotals
	for _, v := range totals {
		got = v
	}
	if got.InputTokens != costChannelPromptTokens || got.OutputTokens != costChannelCompletionTokens {
		t.Errorf("TOKENS A DOBRAR na agregacao por trajectoria: got in=%d out=%d, quer in=%d out=%d",
			got.InputTokens, got.OutputTokens, costChannelPromptTokens, costChannelCompletionTokens)
	}
	if got.CostMicroUSD != costChannelWantMicroUSD {
		t.Errorf("custo agregado = %d, quer %d (custo REAL contado UMA vez)", got.CostMicroUSD, costChannelWantMicroUSD)
	}
	if got.CostMicroUSD == 0 {
		t.Error("custo ZERO na agregacao: o canal nao pode entregar tokens 1x a troco de perder o custo")
	}

	// COERÊNCIA ENTRE AS TRÊS LEITURAS do mesmo turno. São três caminhos independentes —
	// o acumulado em memória do runtime, o evento durável e os spans — e discordarem seria
	// tão mau como estarem todos errados.
	if res.TotalCostMicroUSD != ledgerCost || ledgerCost != got.CostMicroUSD {
		t.Errorf("as tres leituras do custo divergem: runtime=%d ledger=%d spans=%d",
			res.TotalCostMicroUSD, ledgerCost, got.CostMicroUSD)
	}
}

// TestAOS259_SemContabilidade_CustoZeroNaoMataORun fixa a postura fail-OPEN do CANAL
// (distinta do fail-closed do CÁLCULO): um gateway sem contabilidade de custo composta
// serve na mesma e entrega zero. O zero é ausência de dados — quem o consome tem a
// declaração no banner de postura do nó e o guarda fail-closed do burn-down sobre a
// dimensão que decide (tokens, AOS-261) — mas NÃO pode ser motivo para abortar o turno:
// isso tornaria a contabilidade de custo um ponto único de falha do caminho de modelo.
func TestAOS259_SemContabilidade_CustoZeroNaoMataORun(t *testing.T) {
	t.Parallel()

	fake := adapters.NewFakeAdapter("fake")
	fake.SetChatResponse(costChannelModel, port.ChatResponse{
		Choices: []port.Choice{{Message: port.Message{Role: port.RoleAssistant, Content: "ok"}, FinishReason: "stop"}},
		Usage:   port.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	})
	creds := adapters.NewStaticCredentialSource()
	creds.Set(fake.Provider(), costChannelRegion, "sk-teste")
	// SEM WithCost — o gateway não tem recorder.
	gw := modelgateway.New(fake,
		modelgateway.WithCredentialSource(creds),
		modelgateway.WithDefaultRegion(costChannelRegion),
	)

	resp, err := gw.Chat(context.Background(), port.ChatRequest{
		Model:    costChannelModel,
		Messages: []port.Message{{Role: port.RoleUser, Content: "oi"}},
	})
	if err != nil {
		t.Fatalf("sem contabilidade de custo a chamada TEM de servir: %v", err)
	}
	if resp.Usage.CostMicroUSD != 0 {
		t.Errorf("Usage.CostMicroUSD = %d sem recorder composto, quer 0 (o custo e DERIVADO, nao vem do provider)", resp.Usage.CostMicroUSD)
	}
	// E os tokens medidos continuam a fluir — a ausência de custo não contamina a medição.
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 {
		t.Errorf("tokens medidos perdidos: %+v", resp.Usage)
	}
}
