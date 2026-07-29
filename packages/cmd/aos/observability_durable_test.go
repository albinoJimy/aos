package main

// AOS-210 — a ÁRVORE de spans do nó COM `AOS_DURABLE_EXECUTION` ligado.
//
// # O defeito que este ficheiro fecha
//
// O residual nomeado por AOS-204 em docs/reports/AOS-169-aceitacao-sistemica.md §6.3:
// `integration/secured.go` compunha o dispatcher durável com
// `activity.NewDispatcher(rm, cfg.Ledger)` — SEM tracer — e o default de
// [activity.Dispatcher] é [agentruntime.NoopTracer]. Logo, no nó com execução durável, o
// span `aos.activity` NÃO era exportado. O ramo `execute_tool` saía na mesma (o RM recebe
// o tracer do RT), pelo que §13.6 não reabria; o que faltava era a camada INTERMÉDIA da
// árvore — a que carrega dedup, replay e o desfecho durável do efeito real.
//
// # O que estes testes provam (contra um colector OTLP httptest, com -race)
//
//  1. TOPOLOGIA: a árvore exportada PELO NÓ contém `aos.activity` e ele é PAI do
//     `execute_tool` — `parentSpanId(execute_tool) == spanId(aos.activity)`, no MESMO
//     `traceId`. A asserção é sobre a TOPOLOGIA do wire OTLP, não sobre a presença de
//     nomes soltos.
//  2. NÃO-DUPLICAÇÃO: `execute_tool` aparece EXACTAMENTE UMA VEZ. É o risco que o
//     comentário de [activity.OpActivity] nomeia — se partilhar o tracer com o RM fizesse
//     o span sair duas vezes, o remédio seria pior que a doença (duplo-contar em
//     agregadores por-operação). Não sai.
//  3. RETRO-COMPATIBILIDADE: o MESMO cenário durável sem `AOS_OTLP_ENDPOINT` produz o
//     MESMO desfecho observável (mesma execução, mesmos bytes, mesmos turnos) e ZERO
//     tráfego para o colector. A prova de que o dispatcher fica com NoopTracer quando
//     `SecuredConfig.Tracer` é nil está em
//     packages/integration/durable_activity_tracing_test.go (prova negativa directa).

import (
	"context"
	"io"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/activity"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// durObsOutcome é o desfecho OBSERVÁVEL de um run durável — o que tem de ficar
// byte-a-byte igual com e sem observabilidade (retro-compatibilidade).
type durObsOutcome struct {
	terminated bool
	turns      int
	toolResult string
	execs      int64
	permits    uint64
	denials    uint64
}

// runDurableToolRun compõe o nó com a cadeia de PERMIT e a execução DURÁVEL ligada
// (step-ledger sobre um Event Store em ficheiro) e corre um run cujo modelo emite UMA
// tool call. endpoint vazio ⇒ observabilidade DESLIGADA (o caso de retro-compatibilidade).
// Devolve o desfecho observável.
func runDurableToolRun(t *testing.T, endpoint, runID string) durObsOutcome {
	return runDurableToolRunCore(t, endpoint, runID, false, 0)
}

// runDurableCostingToolRun é [runDurableToolRun] em que a tool de referência "counter" é
// registada via RegisterCosting e REPORTA o custo medido do seu efeito (costMicroUSD) —
// o produtor de custo ROTULADO da prova de AOS-212 (o produtor real por-tool é EPIC-06).
func runDurableCostingToolRun(t *testing.T, endpoint, runID string, costMicroUSD int64) durObsOutcome {
	return runDurableToolRunCore(t, endpoint, runID, true, costMicroUSD)
}

// referenceCostingCounter é o PRODUTOR DE CUSTO DE REFERÊNCIA (rotulado, AOS-212 CA5): uma
// tool que reporta um custo medido não-nulo do efeito para provar o fio desfecho→span
// ponta-a-ponta. NÃO é o produtor real: o custo real por-tool (Model Gateway / tools pagas)
// é EPIC-06, e as tools de referência de PRODUÇÃO do nó reportam 0 (via Register). Existe
// só no teste, exactamente para não forjar custo no caminho de produção.
func referenceCostingCounter(execs *int64, costMicroUSD int64) referencemonitor.CostingToolFunc {
	return func(_ context.Context, _ []byte) ([]byte, int64, error) {
		atomic.AddInt64(execs, 1)
		return []byte("pong"), costMicroUSD, nil
	}
}

func runDurableToolRunCore(t *testing.T, endpoint, runID string, costing bool, costMicroUSD int64) durObsOutcome {
	t.Helper()
	dir := t.TempDir()

	model := &toolEmittingModel{inv: agentruntime.ToolInvocation{
		ToolID:     "counter", // a entry ASSINADA do catálogo (counterEntry)
		Capability: durCap,    // cap:fs.read — permitida pelo bundle Cedar assinado
		Input:      []byte("tick"),
	}}
	node, credential := obsPermitNodeWith(t, endpoint, model, func(cfg *Config) {
		// É ISTO que interpõe o activity.Dispatcher entre o loop e o RM — a configuração
		// que o residual §6.3 declarava NÃO re-verificada a partir do nó.
		cfg.DurableExecution = true
		cfg.EventStorePath = filepath.Join(dir, "events.wal")
		cfg.WORMPath = filepath.Join(dir, "worm.wal")
		cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	})

	var execs int64
	if costing {
		// AOS-212: a tool de referência reporta o custo medido do efeito ao RM.
		if err := node.Runtime.RegisterCosting("counter", referenceCostingCounter(&execs, costMicroUSD)); err != nil {
			t.Fatalf("RegisterCosting(counter): %v", err)
		}
	} else if err := node.Runtime.Register("counter", func(_ context.Context, _ []byte) ([]byte, error) {
		atomic.AddInt64(&execs, 1)
		return []byte("pong"), nil
	}); err != nil {
		t.Fatalf("Register(counter): %v", err)
	}

	res, _, err := node.Runtime.Run(context.Background(), agentruntime.Goal{
		RunID:      runID,
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

	// ANTI-VACUIDADE: sem tool call EMITIDA e EXECUTADA, "não vi aos.activity" seria
	// indistinguível de "nunca houve nada a instrumentar" — o defeito VAC-01 de AOS-192.
	if model.turns < 2 {
		_ = node.Close()
		t.Fatalf("o modelo devia ter EMITIDO a tool call (turno 1) e concluido (turno 2); turnos=%d", model.turns)
	}
	permits, denials, _ := node.Runtime.Monitor().Metrics().Snapshot()
	out := durObsOutcome{
		terminated: res.Terminated,
		turns:      res.Turns,
		execs:      atomic.LoadInt64(&execs),
		permits:    permits,
		denials:    denials,
	}
	if len(res.ToolResults) == 1 {
		out.toolResult = string(res.ToolResults[0].Value)
	}

	// Close drena o exporter SINCRONAMENTE (quando existe): ao retornar, tudo o que foi
	// enfileirado já foi POSTado ao colector.
	if err := node.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return out
}

// assertDurableRunHealthy verifica que o run durável fez o que devia — a pré-condição
// sem a qual qualquer asserção sobre spans seria vacuosa.
func assertDurableRunHealthy(t *testing.T, got durObsOutcome) {
	t.Helper()
	if !got.terminated {
		t.Fatalf("o run devia ter concluido, veio %+v", got)
	}
	if got.execs != 1 {
		t.Fatalf("a tool devia ter EXECUTADO exactamente 1 vez sob permit (via o dispatcher DURAVEL), correu %d", got.execs)
	}
	if got.toolResult != "pong" {
		t.Fatalf("o resultado devolvido ao loop devia ser \"pong\", veio %q", got.toolResult)
	}
	if got.permits < 1 || got.denials != 0 {
		t.Fatalf("esperava a call PERMITIDA pela cadeia real (permits=%d, denials=%d)", got.permits, got.denials)
	}
}

// TestAOS210_DurableExecutionExportsActivitySpanAsParentOfExecuteTool é a prova da
// ÁRVORE pedida pelo residual §6.3: com AOS_DURABLE_EXECUTION ligado e a observabilidade
// activa, o nó exporta `aos.activity` e ele é PAI do `execute_tool`, no MESMO trace — e o
// `execute_tool` sai EXACTAMENTE UMA VEZ.
func TestAOS210_DurableExecutionExportsActivitySpanAsParentOfExecuteTool(t *testing.T) {
	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	got := runDurableToolRun(t, srv.URL, "run-obs-durable-permit")
	assertDurableRunHealthy(t, got)

	spans := col.spans(t)
	if len(spans) == 0 {
		t.Fatal("nenhum span exportado — a observabilidade nao fluiu ponta-a-ponta")
	}

	// (i) RAIZ do trace: o invoke_agent do run.
	agents := obsSpansNamed(spans, otelgenai.OpInvokeAgent)
	if len(agents) != 1 {
		t.Fatalf("esperava exactamente 1 span %q exportado, vieram %d; nomes vistos: %v", otelgenai.OpInvokeAgent, len(agents), names(spans))
	}
	root := agents[0]

	// (ii) A CAMADA QUE FALTAVA: o span de escopo durável. Se este assert falhar, o
	// defeito de §6.3 voltou (dispatcher composto sem tracer ⇒ NoopTracer).
	acts := obsSpansNamed(spans, activity.OpActivity)
	if len(acts) != 1 {
		t.Fatalf("esperava EXACTAMENTE 1 span %q na arvore EXPORTADA PELO NO com execucao duravel, vieram %d; nomes vistos: %v — e a camada de dedup/replay/desfecho duravel que o residual §6.3 nomeia",
			activity.OpActivity, len(acts), names(spans))
	}
	act := acts[0]

	// (iii) NÃO-DUPLICAÇÃO: partilhar o tracer com o RM não pode fazer sair dois
	// execute_tool. É o risco que dispatch.go nomeia; se falhar, o remédio é pior que a
	// doença e a correcção tem de ser revertida.
	tools := obsSpansNamed(spans, otelgenai.OpExecuteTool)
	if len(tools) != 1 {
		t.Fatalf("esperava EXACTAMENTE 1 span %q exportado (a tool correu 1 vez), vieram %d; nomes vistos: %v — DUPLICAR o execute_tool faria os agregadores por-operacao contar o efeito duas vezes",
			otelgenai.OpExecuteTool, len(tools), names(spans))
	}
	tool := tools[0]

	// (iv) TOPOLOGIA (o que o ticket exige asserir): aos.activity é FILHO do invoke_agent
	// e PAI do execute_tool — mesmo traceId, parentSpanId encadeado.
	obsAssertChildOf(t, act, root)
	obsAssertChildOf(t, tool, act)

	// (v) O desfecho DURÁVEL vive no span certo: é o que o RM não conhece.
	if v, ok := act.attr(activity.AttrDecision); !ok || v != "permit" {
		t.Errorf("%s.%s = %q (ok=%v), esperava \"permit\" (o efeito correu agora, nao foi dedup nem replay)", activity.OpActivity, activity.AttrDecision, v, ok)
	}
	if v, ok := act.attr(otelgenai.AttrToolName); !ok || v != "counter" {
		t.Errorf("%s.%s = %q (ok=%v), esperava \"counter\"", activity.OpActivity, otelgenai.AttrToolName, v, ok)
	}
	if v, ok := act.attr(otelgenai.AttrRunID); !ok || v != "run-obs-durable-permit" {
		t.Errorf("%s.%s = %q (ok=%v), esperava run-obs-durable-permit", activity.OpActivity, otelgenai.AttrRunID, v, ok)
	}
	if v, ok := act.attr(otelgenai.AttrStepID); !ok || v == "" {
		t.Errorf("%s.%s ausente/vazio — sem step_id a entrada do ledger nao e correlacionavel com o span", activity.OpActivity, otelgenai.AttrStepID)
	}

	// (vi) O execute_tool continua a ser o span do RM, com os atributos de CA2 que SÓ ele
	// anota — a separação de operações mantém-se DELIBERADA, não colapsada.
	if v, ok := tool.attr(otelgenai.AttrDecision); !ok || v != string(referencemonitor.EffectPermit) {
		t.Errorf("%s.%s = %q (ok=%v), esperava %q", otelgenai.OpExecuteTool, otelgenai.AttrDecision, v, ok, referencemonitor.EffectPermit)
	}
	if v, ok := tool.attr(otelgenai.AttrToolCallHash); !ok || v == "" {
		t.Errorf("%s.%s ausente/vazio — a ancora hash(tool+args) e anotada SO pelo RM", otelgenai.OpExecuteTool, otelgenai.AttrToolCallHash)
	}
	// O PAR de CA2 é hash(tool+args) + result_taint (otelgenai.requiredAttrs[OpExecuteTool]):
	// asserir só o hash deixaria passar uma regressão que largasse o taint no caminho
	// DURÁVEL — apanhada hoje apenas ao nível de COMPONENTE (activity/dispatch_test.go).
	// O taint é SEMPRE untrusted por ADR-005: um resultado de tool não é dado de confiança.
	// ("untrusted" é literal aqui de propósito: a constante do RM é não-exportada, e o
	// valor faz parte do CONTRATO observável — se mudar, este teste tem de o ver mudar.)
	if v, ok := tool.attr(otelgenai.AttrResultTaint); !ok || v != "untrusted" {
		t.Errorf("%s.%s = %q (ok=%v), esperava \"untrusted\" — o outro atributo obrigatorio de CA2, que SO o RM anota (ADR-005)",
			otelgenai.OpExecuteTool, otelgenai.AttrResultTaint, v, ok)
	}

	// (vii) O selo WORM continua a aninhar-se no execute_tool: a interposição do
	// aos.activity NÃO parte a ligação trajectória↔hash-chain de §13.6.
	var seal *otlpSpanWire
	for _, s := range obsSpansNamed(spans, opAuditSeal) {
		if s.ParentSpanID == tool.SpanID {
			sc := s
			seal = &sc
			break
		}
	}
	if seal == nil {
		t.Fatalf("nenhum span %q filho do execute_tool — a interposicao do %q nao pode partir a ligacao trajectoria↔hash-chain; nomes vistos: %v", opAuditSeal, activity.OpActivity, names(spans))
	}
	if v, ok := seal.attr(attrAuditEntryHash); !ok || v == "" {
		t.Errorf("selo sem %s (ancora tamper-evident da hash-chain)", attrAuditEntryHash)
	}

	// (viii) SEM segredos em nenhum span da árvore durável.
	assertNoSecrets(t, spans)
}

// TestAOS210_DurableExecutionWithoutTracerKeepsBehaviourAndExportsNothing é a prova de
// RETRO-COMPATIBILIDADE ao nível do NÓ: o MESMO cenário durável, num nó SEM
// AOS_OTLP_ENDPOINT, (a) não abre exporter, (b) não envia UM ÚNICO byte ao colector que
// está a correr ao lado, e (c) produz um desfecho observável IDÊNTICO ao do nó
// instrumentado. Ou seja: quem não liga a observabilidade não muda de comportamento.
func TestAOS210_DurableExecutionWithoutTracerKeepsBehaviourAndExportsNothing(t *testing.T) {
	// Colector A CORRER, mas NUNCA configurado no nó: se a mudança de AOS-210 tivesse
	// ligado o tracer incondicionalmente, isto continuaria a zero — por isso a asserção
	// forte é a (c), a igualdade de desfecho com o nó instrumentado.
	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	off := runDurableToolRun(t, "", "run-obs-durable-notracer")
	assertDurableRunHealthy(t, off)

	col.mu.Lock()
	posted := len(col.bodies)
	col.mu.Unlock()
	if posted != 0 {
		t.Fatalf("o no SEM AOS_OTLP_ENDPOINT nao pode exportar nada, chegaram %d corpos ao colector", posted)
	}

	// (c) desfecho IDÊNTICO ao do nó com observabilidade ligada.
	colOn := &otlpCollector{}
	srvOn := httptest.NewServer(colOn)
	defer srvOn.Close()
	on := runDurableToolRun(t, srvOn.URL, "run-obs-durable-notracer")
	assertDurableRunHealthy(t, on)

	if off != on {
		t.Fatalf("ligar a observabilidade MUDOU o desfecho do run duravel:\n sem = %+v\n com = %+v\n— AOS-210 so pode acrescentar spans, nunca alterar comportamento", off, on)
	}
}

// TestAOS210_NodeWithoutObservabilityOpensNoExporter sela a pre-condicao da retro-
// compatibilidade: sem endpoint, o no nem sequer abre um exporter (o tracer fica
// NoopTracer e SecuredConfig.Tracer fica nil ⇒ o dispatcher duravel mantem o default).
func TestAOS210_NodeWithoutObservabilityOpensNoExporter(t *testing.T) {
	dir := t.TempDir()
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")

	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap (no duravel sem observabilidade): %v", err)
	}
	defer node.Close()
	if node.otlp != nil {
		t.Fatal("sem AOS_OTLP_ENDPOINT o no NAO pode abrir um exporter OTLP")
	}
}
