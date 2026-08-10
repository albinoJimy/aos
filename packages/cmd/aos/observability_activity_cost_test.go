package main

// AOS-212 — o custo por EFEITO REAL no `aos.activity` exportado pelo nó (fecha DEF-810,
// a metade que AOS-211 deferiu).
//
// # O defeito que este ficheiro fecha
//
// AOS-211 pôs o `aos.activity` na árvore exportada e nomeou o custo do efeito como
// deferido: nenhum caminho da via durável o preenchia (lia o `Activity` de entrada, 0 na
// via do nó). AOS-212 dá-lhe FONTE: a tool reporta o custo MEDIDO do efeito ao Reference
// Monitor (`referencemonitor.Decision.CostMicroUSD`), o `activity.Dispatcher` capta-o por
// CANAL LATERAL na closure de `Apply` e anota o span a partir do DESFECHO — só no efeito
// real (`applied`), nunca em dedup/replay, e NUNCA gravando o custo no ledger.
//
// # O que estes testes provam (contra um colector OTLP httptest, com -race)
//
//  1. CA2 (applied): um run cuja tool de referência reporta custo C exporta um
//     `aos.activity` com `gen_ai.usage.cost_usd`/`aos.cost.micro_usd == C`, UMA vez.
//     Falha-antes: hoje lê o `Activity` de entrada (0).
//  2. CA2 (dedup, o eixo de risco #1 — fidelidade de replay): reiniciado o nó sobre o
//     MESMO WAL, o MESMO step re-pedido produz um `aos.activity` `dedup` com ZERO custo
//     (o efeito não re-incorre; o custo nunca esteve no ledger).
//  3. CA3 (sem dupla-contagem): o custo-de-MODELO fica no `chat` e no agregado do
//     `invoke_agent`; o custo-de-EFEITO fica no `aos.activity`; o `execute_tool` NÃO
//     carrega custo — o agregado do modelo NÃO absorve o custo do efeito.

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/activity"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// costedToolEmittingModel emite UMA tool call no 1.º turno e conclui no 2.º, reportando
// um custo de MODELO não-nulo por turno — para provar que o custo do efeito (no
// aos.activity) e o do modelo (no chat) coexistem sem se somarem no span errado (CA3).
type costedToolEmittingModel struct {
	inv          agentruntime.ToolInvocation
	costMicroUSD int64
	turns        int64
}

func (m *costedToolEmittingModel) Call(_ context.Context, _ agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.turns++
	if m.turns == 1 {
		return agentruntime.ModelResponse{
			ToolCalls:    []agentruntime.ToolInvocation{m.inv},
			Usage:        agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
			CostMicroUSD: m.costMicroUSD,
		}, nil
	}
	return agentruntime.ModelResponse{
		Text:         "run concluido",
		Final:        true,
		Usage:        agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
		CostMicroUSD: m.costMicroUSD,
	}, nil
}

// TestAOS212_ExportedActivitySpanCarriesEffectCost é a prova ao NÍVEL DO NÓ do CA2
// (caminho applied): a tool de referência reporta custo C e o `aos.activity` exportado
// traz esse custo EXACTAMENTE uma vez; o `execute_tool` do RM NÃO o carrega.
func TestAOS212_ExportedActivitySpanCarriesEffectCost(t *testing.T) {
	const effectCostMicroUSD = int64(750_000) // 0.75 USD reportado pela tool de referência

	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	got := runDurableCostingToolRun(t, srv.URL, "run-obs-durable-cost", effectCostMicroUSD)
	assertDurableRunHealthy(t, got)

	spans := col.spans(t)
	if len(spans) == 0 {
		t.Fatal("nenhum span exportado — a observabilidade nao fluiu ponta-a-ponta")
	}

	acts := obsSpansNamed(spans, activity.OpActivity)
	if len(acts) != 1 {
		t.Fatalf("esperava EXACTAMENTE 1 span %q, vieram %d; nomes vistos: %v", activity.OpActivity, len(acts), names(spans))
	}
	act := acts[0]

	// O efeito correu AGORA (permit), não dedup nem replay: a pré-condição para o custo.
	if v, ok := act.attr(activity.AttrDecision); !ok || v != "permit" {
		t.Fatalf("%s.%s = %q (ok=%v), esperava permit", activity.OpActivity, activity.AttrDecision, v, ok)
	}

	// A PROVA (falha-antes): o custo por efeito real aparece no aos.activity exportado.
	// Fonte de verdade = micro-USD inteiro; o USD float sai em paralelo.
	if v, ok := act.attr(otelgenai.AttrCostMicroUSD); !ok || v != strconv.FormatInt(effectCostMicroUSD, 10) {
		t.Fatalf("%s.%s = %q (ok=%v), esperava %d — o custo por efeito real de AOS-212 (antes: lia o Activity de entrada, 0)",
			activity.OpActivity, otelgenai.AttrCostMicroUSD, v, ok, effectCostMicroUSD)
	}
	if _, ok := act.attr(otelgenai.AttrCostUSD); !ok {
		t.Errorf("%s SEM %s — o USD float de conveniência devia sair em paralelo", activity.OpActivity, otelgenai.AttrCostUSD)
	}

	// SEM DUPLA-CONTAGEM (parte 1): o custo do efeito vive SÓ no aos.activity. O
	// execute_tool do RM (a operação semconv) NÃO o carrega — senão um agregador
	// por-operação contaria o efeito duas vezes.
	tools := obsSpansNamed(spans, otelgenai.OpExecuteTool)
	if len(tools) != 1 {
		t.Fatalf("esperava 1 span %q, vieram %d", otelgenai.OpExecuteTool, len(tools))
	}
	if v, ok := tools[0].attr(otelgenai.AttrCostMicroUSD); ok {
		t.Errorf("%s NAO devia carregar custo (%q) — o custo do efeito e do aos.activity, nao do execute_tool", otelgenai.OpExecuteTool, v)
	}
	if v, ok := tools[0].attr(otelgenai.AttrCostUSD); ok {
		t.Errorf("%s NAO devia carregar custo USD (%q)", otelgenai.OpExecuteTool, v)
	}

	assertNoSecrets(t, spans)
}

// TestAOS212_DedupExportsZeroEffectCost é a prova ao NÍVEL DO NÓ do CA2 (dedup) e do eixo
// de risco #1 (fidelidade de replay): reiniciado o nó sobre o MESMO WAL, o MESMO step
// re-pedido NÃO re-incorre o efeito e o `aos.activity` do dedup exporta ZERO custo — o
// custo nunca foi gravado no `durable.Result` do ledger.
func TestAOS212_DedupExportsZeroEffectCost(t *testing.T) {
	const effectCostMicroUSD = int64(2_100_000) // 2.1 USD no efeito REAL da 1.ª vida
	dir := t.TempDir()
	runID := "run-obs-durable-cost-dedup"

	// CUSTÓDIA DE KEK ESTÁVEL ENTRE AS DUAS VIDAS (AOS-215/AOS-245). O step-ledger sela o
	// Result.Payload sob a KEK POR-TITULAR (AOS-093), pelo que o registo canónico que a 2.ª
	// vida relê do WAL só é decifrável se a KEK sobreviver ao restart. O vault de REFERÊNCIA é
	// in-memory e morre com o processo — a configuração que [ErrProductionNeedsDurableKEK]
	// PROÍBE justamente quando o substrato é durável. Injectar a MESMA instância nas duas vidas
	// é o análogo de teste de AOS_DSAR_VAULT_ADDR (custódia externa); sem ela o teste mediria a
	// evaporação da KEK, não a deduplicação durável que é o seu objecto.
	kek := audit.NewInMemoryKeyVault(nil)
	durableTweak := func(cfg *Config) {
		cfg.DurableExecution = true
		cfg.EventStorePath = filepath.Join(dir, "events.wal")
		cfg.WORMPath = filepath.Join(dir, "worm.wal")
		cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed") // estável entre reinícios
		cfg.DSARVault = kek                                   // KEK por-titular partilhada pelas duas vidas
	}
	goalOf := func(credential string) agentruntime.Goal {
		return agentruntime.Goal{
			RunID:      runID,
			Principal:  referencemonitor.Principal{NHIID: durAgent},
			Credential: credential,
			Model:      agentruntime.ModelConfig{ModelID: "model:test-obs"},
			System:     obsSecretSystem,
			Objective:  obsSecretObjective,
			MaxTurns:   4,
		}
	}

	// ---- 1.ª VIDA: efeito real, custo C no aos.activity ----
	colA := &otlpCollector{}
	srvA := httptest.NewServer(colA)
	defer srvA.Close()

	model1 := &toolEmittingModel{inv: agentruntime.ToolInvocation{ToolID: "counter", Capability: durCap, Input: []byte("tick")}}
	node1, cred1 := obsPermitNodeWith(t, srvA.URL, model1, durableTweak)
	var execs1 int64
	if err := node1.Runtime.RegisterCosting("counter", referenceCostingCounter(&execs1, effectCostMicroUSD)); err != nil {
		t.Fatalf("RegisterCosting (vida 1): %v", err)
	}
	if _, _, err := node1.Runtime.Run(context.Background(), goalOf(cred1), nil); err != nil {
		_ = node1.Close()
		t.Fatalf("Run (vida 1): %v", err)
	}
	if atomic.LoadInt64(&execs1) != 1 {
		_ = node1.Close()
		t.Fatalf("a 1.ª vida devia ter corrido o efeito 1 vez, correu %d", atomic.LoadInt64(&execs1))
	}
	if err := node1.Close(); err != nil { // flush + simula crash
		t.Fatalf("Close (vida 1): %v", err)
	}

	actsA := obsSpansNamed(colA.spans(t), activity.OpActivity)
	if len(actsA) != 1 || func() bool { v, _ := actsA[0].attr(activity.AttrDecision); return v != "permit" }() {
		t.Fatalf("vida 1: esperava 1 aos.activity permit, veio %d", len(actsA))
	}
	if v, ok := actsA[0].attr(otelgenai.AttrCostMicroUSD); !ok || v != strconv.FormatInt(effectCostMicroUSD, 10) {
		t.Fatalf("vida 1: aos.activity.%s = %q (ok=%v), esperava %d", otelgenai.AttrCostMicroUSD, v, ok, effectCostMicroUSD)
	}

	// ---- 2.ª VIDA: mesmo WAL, mesmo step ⇒ dedup, ZERO custo ----
	colB := &otlpCollector{}
	srvB := httptest.NewServer(colB)
	defer srvB.Close()

	model2 := &toolEmittingModel{inv: agentruntime.ToolInvocation{ToolID: "counter", Capability: durCap, Input: []byte("tick")}}
	node2, cred2 := obsPermitNodeWith(t, srvB.URL, model2, durableTweak)
	defer node2.Close()
	// A tool da 2.ª vida AINDA reporta custo — se a via durável o lesse de novo (em vez de o
	// GATEAR em applied), o dedup emitiria custo. A prova é que NÃO emite.
	var execs2 int64
	if err := node2.Runtime.RegisterCosting("counter", referenceCostingCounter(&execs2, effectCostMicroUSD)); err != nil {
		t.Fatalf("RegisterCosting (vida 2): %v", err)
	}
	if _, _, err := node2.Runtime.Run(context.Background(), goalOf(cred2), nil); err != nil {
		t.Fatalf("Run (vida 2): %v", err)
	}
	// O nó NÃO chama StepLedger.Rebuild ao arrancar (o run_id só é conhecido no Run, não no
	// bootstrap): a dedup do 2.º arranque é a DURÁVEL, no COMMIT ao Event Store
	// (StatusDuplicate), não a in-memory fast-path (vazia num processo novo). O contrato
	// documentado do step-ledger é at-least-once nessa via — o efeito RE-CORRE 1 vez (a
	// closure executa e re-REPORTA o custo), e a segurança assenta na dedup do commit +
	// idempotência downstream. É exactamente ISSO que torna esta prova FORTE (e não vácua): a
	// variável de canal lateral FOI re-alimentada com C nesta vida, e mesmo assim o commit
	// devolve applied=false ⇒ o ramo que anota o custo NÃO corre ⇒ o span exporta ZERO custo.
	// O custo por efeito real é emitido EXACTAMENTE uma vez (na vida 1, applied), nunca
	// revivido do ledger. (Eliminar a re-execução exigiria Rebuild — crash-resume, AOS-016/
	// AOS-180 — fora do âmbito de AOS-212, que é a observabilidade do custo, não a dedup do
	// efeito.)
	if atomic.LoadInt64(&execs2) != 1 {
		t.Fatalf("DEDUP durável: a 2.ª vida re-corre o efeito 1 vez (at-least-once, sem Rebuild) e a dedup e no commit; correu %d vezes", atomic.LoadInt64(&execs2))
	}
	if err := node2.Close(); err != nil {
		t.Fatalf("Close (vida 2): %v", err)
	}

	actsB := obsSpansNamed(colB.spans(t), activity.OpActivity)
	if len(actsB) != 1 {
		t.Fatalf("vida 2: esperava 1 aos.activity, veio %d; nomes: %v", len(actsB), names(colB.spans(t)))
	}
	if v, ok := actsB[0].attr(activity.AttrDecision); !ok || v != "dedup" {
		t.Fatalf("vida 2: aos.activity.%s = %q (ok=%v), esperava dedup", activity.AttrDecision, v, ok)
	}
	// A PROVA: o dedup do MESMO step emite ZERO custo (o custo nunca esteve no ledger).
	if v, ok := actsB[0].attr(otelgenai.AttrCostMicroUSD); ok {
		t.Errorf("DEDUP nao pode emitir custo micro-USD (%q) — o custo e canal lateral, nunca gravado no durable.Result", v)
	}
	if v, ok := actsB[0].attr(otelgenai.AttrCostUSD); ok {
		t.Errorf("DEDUP nao pode emitir custo USD (%q)", v)
	}
}

// TestAOS212_NoDoubleCountingModelVsEffectCost é a prova do CA3: com o modelo a reportar
// custo M por turno E a tool a reportar custo de efeito E, o custo do MODELO agrega no
// `chat`/`invoke_agent` e o custo do EFEITO fica no `aos.activity` — o agregado do modelo
// NÃO absorve E (sem dupla-contagem), e cada custo aparece exactamente no seu span.
func TestAOS212_NoDoubleCountingModelVsEffectCost(t *testing.T) {
	const (
		modelCostMicroUSD  = int64(1_500) // custo de MODELO por turno (chat)
		effectCostMicroUSD = int64(640_000)
	)

	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	dir := t.TempDir()
	model := &costedToolEmittingModel{
		inv:          agentruntime.ToolInvocation{ToolID: "counter", Capability: durCap, Input: []byte("tick")},
		costMicroUSD: modelCostMicroUSD,
	}
	node, credential := obsPermitNodeWith(t, srv.URL, model, func(cfg *Config) {
		cfg.DurableExecution = true
		cfg.EventStorePath = filepath.Join(dir, "events.wal")
		cfg.WORMPath = filepath.Join(dir, "worm.wal")
		cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	})

	var execs int64
	if err := node.Runtime.RegisterCosting("counter", referenceCostingCounter(&execs, effectCostMicroUSD)); err != nil {
		t.Fatalf("RegisterCosting: %v", err)
	}

	res, _, err := node.Runtime.Run(context.Background(), agentruntime.Goal{
		RunID:      "run-obs-cost-aggregate",
		Principal:  referencemonitor.Principal{NHIID: durAgent},
		Credential: credential,
		Model:      agentruntime.ModelConfig{ModelID: "model:test-obs"},
		System:     obsSecretSystem,
		Objective:  obsSecretObjective,
		MaxTurns:   4,
	}, nil)
	if err != nil {
		_ = node.Close()
		t.Fatalf("Run: %v", err)
	}
	if atomic.LoadInt64(&execs) != 1 {
		_ = node.Close()
		t.Fatalf("o efeito devia correr 1 vez, correu %d", atomic.LoadInt64(&execs))
	}
	if err := node.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	spans := col.spans(t)

	// (a) o custo do EFEITO vive no aos.activity e é EXACTAMENTE E.
	acts := obsSpansNamed(spans, activity.OpActivity)
	if len(acts) != 1 {
		t.Fatalf("esperava 1 aos.activity, veio %d", len(acts))
	}
	if v, ok := acts[0].attr(otelgenai.AttrCostMicroUSD); !ok || v != strconv.FormatInt(effectCostMicroUSD, 10) {
		t.Fatalf("aos.activity.%s = %q (ok=%v), esperava %d (custo do EFEITO)", otelgenai.AttrCostMicroUSD, v, ok, effectCostMicroUSD)
	}

	// (b) o custo do MODELO agrega no invoke_agent = soma dos chats, e é o TotalCostMicroUSD
	//     do run — que NÃO inclui E. É a asserção anti-dupla-contagem: o agregado do modelo
	//     ignora o custo do efeito.
	sumChat := int64(0)
	for _, s := range obsSpansNamed(spans, otelgenai.OpChat) {
		if v, ok := s.attr(otelgenai.AttrCostMicroUSD); ok {
			n, perr := strconv.ParseInt(v, 10, 64)
			if perr != nil {
				t.Fatalf("chat.%s nao-inteiro: %q", otelgenai.AttrCostMicroUSD, v)
			}
			sumChat += n
		}
	}
	if sumChat != res.TotalCostMicroUSD {
		t.Errorf("soma dos chats (%d) != TotalCostMicroUSD do run (%d)", sumChat, res.TotalCostMicroUSD)
	}
	if res.TotalCostMicroUSD == 0 || res.TotalCostMicroUSD == effectCostMicroUSD || res.TotalCostMicroUSD >= effectCostMicroUSD {
		t.Errorf("o agregado do modelo (%d) nao pode incluir o custo do efeito (%d) — dupla-contagem", res.TotalCostMicroUSD, effectCostMicroUSD)
	}

	agents := obsSpansNamed(spans, otelgenai.OpInvokeAgent)
	if len(agents) != 1 {
		t.Fatalf("esperava 1 invoke_agent, veio %d", len(agents))
	}
	if v, ok := agents[0].attr(otelgenai.AttrCostMicroUSD); !ok || v != strconv.FormatInt(res.TotalCostMicroUSD, 10) {
		t.Errorf("invoke_agent.%s = %q (ok=%v), esperava %d (agregado do MODELO, sem o efeito)",
			otelgenai.AttrCostMicroUSD, v, ok, res.TotalCostMicroUSD)
	}

	assertNoSecrets(t, spans)
}
