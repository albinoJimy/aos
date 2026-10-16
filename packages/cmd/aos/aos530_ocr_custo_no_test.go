package main

// AOS-530 (peça 3b) — O CUSTO DA `ocr_read` NO NÓ COMPOSTO: o gateway real, a tabela de preços, o
// orçamento por-run do nó (`AOS_BUDGET_MAX_TOKENS` + `AOS_BUDGET_MAX_COST_MICRO_USD`), a retoma, o
// apagamento do titular e o Event Store JetStream. O molde é o de aos530_ocr_read_no_test.go.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	autonomy "github.com/aos-ref/control-plane/governance/autonomy"
	dsar "github.com/aos-ref/control-plane/governance/dsar"
	pdp "github.com/aos-ref/control-plane/pdp"
	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
	"github.com/aos-ref/platform/audit"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/platform/model-gateway/metering/cost"
	"github.com/aos-ref/platform/model-gateway/pricing"
	"github.com/aos-ref/platform/registry/domain"
	"github.com/aos-ref/substrate/eventstore/jetstream"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// aos530Orcamento é o ambiente do orçamento por-run com o tecto em dólares dado.
func aos530Orcamento(dolares string) map[string]string {
	return map[string]string{"AOS_BUDGET_MAX_TOKENS": "1000000", "AOS_BUDGET_MAX_COST_MICRO_USD": dolares}
}

// aos530CustoEsperado é o custo que a tabela dos testes dá a uma chamada da tool com estes tokens,
// pelo calculador do gateway.
func aos530CustoEsperado(t *testing.T, entrada, saida int64) int64 {
	t.Helper()
	tabela, err := pricing.LoadTable([]byte(aos530PrecoComTool))
	if err != nil {
		t.Fatal(err)
	}
	amt, err := cost.NewCalculator(tabela).Cost(cost.TokenCounts{PromptTokens: entrada, CompletionTokens: saida}, aos530AliasTool, "eu")
	if err != nil {
		t.Fatal(err)
	}
	return amt.CostMicroUSD
}

// CUSTO NO RUN. O evento de custo da tool call tem o valor que o gateway calculou pela tabela de
// preços, o modelo servido e os tokens; o consumo durável do run (o que a retoma lê) soma-o aos
// turnos; uma resposta cortada grava o custo na mesma. O arranque declara a reserva.
func TestAOS530_No_CustoNoRun(t *testing.T) {
	for _, fim := range []string{"stop", "length"} {
		t.Run(fim, func(t *testing.T) {
			n := aos530Compor(t, aos530Opcoes{preco: aos530PrecoComTool, ambiente: aos530Orcamento("1000000")})
			n.upstream.ocrFim = fim
			runID := "run-530-custo-" + fim
			evs := n.correr(t, runID, [2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`})

			med := aos530Mediacoes(t, evs, referencemonitor.EventTypeMediated)
			custos := aos530Custos(t, evs)
			quer := aos530CustoEsperado(t, 900, 30)
			resultado := ocrResultadoTranscrito
			if fim == "length" {
				resultado = ocrCausaCortado
			}
			if len(med) != 1 || len(custos) != 1 {
				t.Fatalf("quer 1 mediacao e 1 evento de custo; vieram %d e %d", len(med), len(custos))
			}
			c := custos[0]
			if c.envelope != ocrCustoStepPrefix+med[0].step || c.p.StepID != med[0].step || c.p.CostMicroUSD != quer || quer != 840 ||
				c.p.InputTokens != 900 || c.p.OutputTokens != 30 || c.p.Alias != aos530AliasTool || c.p.ModeloServido != aos530Servido || c.p.Resultado != resultado {
				t.Fatalf("o evento de custo (quer %d micro-USD, resultado %s, passo %s): %+v", quer, resultado, med[0].step, c)
			}
			// O consumo durável: os turnos do titular custam 0 nesta tabela; o run soma o da tool.
			consumo, err := consumoDuravelParaOrcamento(newTurnLedgerBurndown(n.node.EventStore))(context.Background(), runID)
			if err != nil || consumo.CostMicroUSD != quer || consumo.Tokens < 930 {
				t.Fatalf("o consumo duravel do run soma o custo da tool: %+v (%v)", consumo, err)
			}
			if !strings.Contains(n.log.String(), "tecto em dolares 1000000 micro-USD por run") || !strings.Contains(n.log.String(), "12288 tokens (8192 de entrada + 4096 de saida) e 22938 micro-USD") {
				t.Fatalf("o arranque declara a reserva:\n%s", n.log.String())
			}
		})
	}
}

// COM O TECTO EM DÓLARES QUASE GASTO A RESERVA NÃO CABE: a tool é negada com `tecto_do_run`, o
// provider não recebe o pedido da tool e não há evento de custo.
func TestAOS530_No_TectoQuaseGastoNegaSemPedido(t *testing.T) {
	n := aos530Compor(t, aos530Opcoes{preco: aos530PrecoComTool, ambiente: aos530Orcamento("10000")})
	evs := n.correr(t, "run-530-tecto", [2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`})
	if _, tool := n.upstream.separar(t); len(tool) != 0 {
		t.Fatalf("a reserva nao cabia e o pedido saiu: %d", len(tool))
	}
	if n.node.ocr.lido(ocrCausaTecto) != 1 || len(aos530Custos(t, evs)) != 0 {
		t.Fatalf("quer a causa tecto_do_run e nenhum evento de custo (%d)", n.node.ocr.lido(ocrCausaTecto))
	}
	if !strings.Contains(aos530Metricas(t, n), `aos_ocr_read_total{resultado="tecto_do_run"} 1`) {
		t.Fatal("a metrica conta a causa")
	}
}

// UM CUSTO MEDIDO ACIMA DA RESERVA grava-se como foi, nunca truncado; o run fica sem folga e a
// chamada seguinte da tool no MESMO run é negada — a reserva vê o consumo.
func TestAOS530_No_CustoAcimaDaReservaGravaSeComoFoi(t *testing.T) {
	n := aos530Compor(t, aos530Opcoes{preco: aos530PrecoComTool, ambiente: aos530Orcamento("30000")})
	n.upstream.ocrEntrada = 50_000 // 50000 × 0,8 + 30 × 4 = 40120 micro-USD, acima dos 22938 reservados
	evs := n.correr(t, "run-530-acima",
		[2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`},
		[2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`})
	custos := aos530Custos(t, evs)
	quer := aos530CustoEsperado(t, 50_000, 30)
	if len(custos) != 1 || custos[0].p.CostMicroUSD != quer || quer != 40_120 {
		t.Fatalf("quer 1 evento com o custo medido %d; vieram %+v", quer, custos)
	}
	if _, tool := n.upstream.separar(t); len(tool) != 1 || n.node.ocr.lido(ocrCausaTecto) != 1 {
		t.Fatalf("a segunda chamada nao cabia no que restava: pedidos=%d tecto=%d", len(tool), n.node.ocr.lido(ocrCausaTecto))
	}
}

// O MODELO SERVIDO DIFERENTE EM `enforce`: o gateway recusa a resposta depois de a contar, a tool
// falha com `modelo_servido_diferente`, e o custo grava-se na mesma com o modelo que o proxy disse
// ter servido. Os turnos do titular (com a rota igual) não mudam.
func TestAOS530_No_ModeloServidoDiferenteGravaOCusto(t *testing.T) {
	amb := aos530Orcamento("1000000")
	amb["AOS_MODEL_ROUTE_GOVERNANCE"] = "enforce"
	amb["AOS_MODEL_ROUTE_API_HOST"] = aos505NoHostA
	n := aos530Compor(t, aos530Opcoes{preco: aos530PrecoComTool, ambiente: amb})
	n.upstream.apiBase = aos505NoAPIBaseA
	n.upstream.titularServido = aos505NoEsperado
	n.upstream.ocrServido = "openrouter/outro/modelo"
	evs := n.correr(t, "run-530-servido", [2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`})
	if n.node.ocr.lido(ocrCausaModeloServido) != 1 {
		t.Fatal("a tool falha com modelo_servido_diferente")
	}
	custos := aos530Custos(t, evs)
	if len(custos) != 1 || custos[0].p.CostMicroUSD != 840 || custos[0].p.ModeloServido != "openrouter/outro/modelo" || custos[0].p.Resultado != ocrCausaModeloServido {
		t.Fatalf("a recusa paga grava o custo: %+v", custos)
	}
	consumo, err := consumoDuravelParaOrcamento(newTurnLedgerBurndown(n.node.EventStore))(context.Background(), "run-530-servido")
	if err != nil || consumo.CostMicroUSD != 840 {
		t.Fatalf("e soma ao consumo duravel: %+v (%v)", consumo, err)
	}
}

// FUGA DO TEXTO E APAGAMENTO. O texto marcador do modelo e os bytes da imagem não aparecem em claro
// nos spans do nó, nem o texto no evento de custo (os outros sítios — eventos, WAL, WORM, log, /metrics, selos — estão no teste
// da tool mediada); está no step-ledger, cifrado sob o titular do run, e deixa de ser legível
// depois do apagamento do titular (crypto-shredding, AOS-093/AOS-245).
func TestAOS530_No_TextoIlegivelDepoisDoApagamento(t *testing.T) {
	ctx := context.Background()
	spans := &otelgenai.RecordingExporter{}
	n := aos530Compor(t, aos530Opcoes{preco: aos530PrecoComTool, exportador: spans})
	const runID = "run-530-apagamento"
	evs := n.correr(t, runID, [2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`})

	if len(spans.Spans()) == 0 || len(spans.SpansByName("execute_tool")) == 0 {
		t.Fatal("o no tinha de exportar spans (pre-condicao: sem eles a busca e vacua)")
	}
	b64 := base64.StdEncoding.EncodeToString(n.imagem)
	for _, s := range spans.Spans() {
		raw, _ := json.Marshal(s)
		if bytes.Contains(raw, []byte(aos530TextoMarcador)) {
			t.Fatalf("o texto do modelo aparece em claro num span: %.300s", raw)
		}
		if bytes.Contains(raw, []byte("MARCA-BINARIA-AOS530")) || bytes.Contains(raw, []byte(b64)) {
			t.Fatalf("os bytes da imagem aparecem num span: %.300s", raw)
		}
	}
	for _, c := range aos530Custos(t, evs) {
		raw, _ := json.Marshal(c.p)
		if bytes.Contains(raw, []byte(aos530TextoMarcador)) {
			t.Fatal("o texto do modelo aparece no evento de custo")
		}
	}

	selado, sealed, titular := aos245LedgerRecord(t, n.node.EventStore, runID)
	if !sealed || titular == "" {
		t.Fatalf("o resultado da ocr_read no step-ledger tinha de estar selado sob o titular (titular=%q)", titular)
	}
	claro, err := audit.OpenContent(n.node.DSARVault, titular, selado)
	if err != nil || !bytes.Contains(claro, []byte(aos530TextoMarcador)) {
		t.Fatalf("antes do apagamento o resultado decifra e tem o texto (%v)", err)
	}
	if _, err := n.node.DSAR.Receive(ctx, dsar.Request{RequestID: "req-530", SubjectID: titular}); err != nil {
		t.Fatalf("apagamento do titular: %v", err)
	}
	if _, err := audit.OpenContent(n.node.DSARVault, titular, selado); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("depois do apagamento o resultado e ilegivel (ErrDecrypt); veio %v", err)
	}
	wal, err := os.ReadFile(filepath.Join(n.dir, "events.wal"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wal, []byte(aos530TextoMarcador)) {
		t.Fatal("o texto do modelo esta em claro no WAL")
	}
}

// A RETOMA LÊ O CUSTO DURÁVEL. O run pede a `ocr_read` duas vezes e as duas escalam (autonomia L4):
// a primeira corre depois da primeira retoma e grava o seu custo; a segunda corre depois da segunda
// retoma, numa incarnação NOVA cujo nó de orçamento nasce com `tecto − já consumido`. O tecto em
// dólares é o máximo de uma chamada mais 562 micro-USD: a primeira cabe; depois do custo dela (840)
// a segunda já não cabe — e só não cabe porque a incarnação nova leu o evento de custo.
func TestAOS530_No_RetomaLeOCustoDuravel(t *testing.T) {
	ctx := context.Background()
	amb := aos530Orcamento("23500")
	up, _ := aos530AmbienteOCR(t, aos530Opcoes{preco: aos530PrecoComTool, ambiente: amb})
	cliente, binder, err := parseModelFromEnv(false)
	if err != nil {
		t.Fatalf("parseModelFromEnv: %v", err)
	}
	assinados := perfisDoCliente(cliente)
	if assinados == nil || assinados.ocr == nil || assinados.ocr.reservaMax.CostMicroUSD != 22938 {
		t.Fatal("a ligacao da ocr_read tinha de ficar composta com a reserva de 22938 micro-USD")
	}
	pedeOCR := agentruntime.ModelResponse{ToolCalls: []agentruntime.ToolInvocation{{
		ToolID: ocrToolName, Capability: acnCap, Input: []byte(`{"doc_id":"` + aos530Doc + `"}`),
		ResourceType: "file", ResourceValue: "doc://" + aos530Doc, ResourceRegion: "eu",
	}}, Usage: agentruntime.Usage{InputTokens: 10, OutputTokens: 5}}
	titular := &acnModel{guiao: func(v agentruntime.PromptView) agentruntime.ModelResponse {
		if v.Turn <= 2 {
			return pedeOCR
		}
		return agentruntime.ModelResponse{Text: "concluido", Final: true, Usage: agentruntime.Usage{InputTokens: 10, OutputTokens: 5}}
	}}
	levels := autonomy.NewLevelRegistry()
	if _, err := levels.SetLevel(ctx, acnAgent, "fs", autonomy.L4, "teste da retoma do custo da ocr_read", "operador"); err != nil {
		t.Fatal(err)
	}
	policyDP, err := pdp.Open(pdpPoliciesDir, pdp.WithAutonomyOracle(levels))
	if err != nil {
		t.Fatal(err)
	}
	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)
	signer := acnSigner(t)
	dir := t.TempDir()
	cfg := tnBaseConfig()
	cfg.DurableExecution = true
	cfg.EventStorePath = filepath.Join(dir, "events.wal")
	cfg.WORMPath = filepath.Join(dir, "worm.wal")
	cfg.IssuerKeyPath = filepath.Join(dir, "issuer.seed")
	cfg.Model = clienteComPerfis{inner: titular, perfis: assinados}
	cfg.ModelIdentityBinder = binder
	cfg.Catalog = catalogStub{entries: []domain.Entry{acnEntry(t, signer, ocrToolName), acnEntry(t, signer, "doc_read")}}
	cfg.SignedToolRegistry = nodeSignedRegistrySpec(signer, nil)
	cfg.IssuerClasses = map[string]identity.ClassPolicy{acnClass: {TTL: 15 * time.Minute, Scope: []string{acnCap, "model:invoke"}}}
	cfg.Policy = integration.StaticPolicy{MaxEgress: domain.EgressInternal}
	cfg.PDP = policyDP
	cfg.Authority = authz.NewStaticAuthoritySource().
		Set("human:"+tnHuman, acnCap, "model:invoke").Set(acnAgent, acnCap, "model:invoke").Set("agent:"+acnClass, acnCap, "model:invoke")
	cfg.Approvers = []ApproverConfig{
		{Principal: "human:alice", PubKey: pubA, Authority: []string{"approve:danger", "approve:gray"}},
		{Principal: "human:bob", PubKey: pubB, Authority: []string{"approve:danger", "approve:gray"}},
	}
	log := &bytes.Buffer{}
	node, err := Bootstrap(ctx, cfg, log)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	t.Cleanup(func() { _ = node.Close() })
	svc, err := NewNodeService(node)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	h := &acnHarness{node: node, svc: svc, model: titular, keys: [2]ed25519.PrivateKey{privA, privB}, execs: map[string]*int64{}}
	token := func() string {
		tok, err := node.Authority.MintForHuman(ctx, tnHuman, acnAgent, acnClass, []string{acnCap, "model:invoke"})
		if err != nil {
			t.Fatal(err)
		}
		return tok.Compact
	}
	if err := svc.Submit(ctx, agentruntime.Goal{RunID: acnRunID, Principal: referencemonitor.Principal{NHIID: acnAgent},
		Credential: token(), Objective: "le a fatura duas vezes", MaxTurns: 5}); err != nil {
		t.Fatal(err)
	}
	for i, req := range []string{"req-530-custo-1", "req-530-custo-2"} {
		h.esperaFim(t, acnRunID)
		if _, susp := svc.Suspended(ctx, acnRunID); !susp {
			t.Fatalf("a %da ocr_read tinha de escalar e o run ficar suspenso", i+1)
		}
		h.aprovaPendente(t, req)
		if err := svc.Resume(ctx, acnRunID, token()); err != nil {
			t.Fatalf("Resume %d: %v", i+1, err)
		}
	}
	h.esperaFim(t, acnRunID)
	if fim, done := svc.Outcome(acnRunID); !done || !fim.Result.Terminated {
		t.Fatalf("o run retomado tinha de terminar: %+v", fim)
	}

	if _, tool := up.separar(t); len(tool) != 1 {
		t.Fatalf("so a primeira ocr_read chega ao provider; vieram %d", len(tool))
	}
	if assinados.ocr.lido(ocrResultadoTranscrito) != 1 || assinados.ocr.lido(ocrCausaTecto) != 1 {
		t.Fatalf("a segunda ocr_read, na incarnacao nova, e negada pelo tecto (transcritas=%d tecto=%d)",
			assinados.ocr.lido(ocrResultadoTranscrito), assinados.ocr.lido(ocrCausaTecto))
	}
	evs, err := node.EventStore.Read(ctx, acnRunID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if c := aos530Custos(t, evs); len(c) != 1 || c[0].p.CostMicroUSD != 840 {
		t.Fatalf("um evento de custo, o da primeira chamada: %+v", c)
	}
	if strings.Contains(log.String(), "ILEGIVEL") {
		t.Fatalf("o consumo duravel tinha de ser legivel na retoma:\n%s", log.String())
	}
}

// SOBRE JETSTREAM. O run com a `ocr_read` sobre o Event Store replicado: a mediação, o step-ledger
// e o evento de custo ficam no stream do run no JetStream, com a chave do custo aceite pelo
// substrato, e o consumo durável lê-o de lá. Saltado sem cluster (corre no gate `nats`).
func TestAOS530_No_SobreJetStream(t *testing.T) {
	addr := clusterAOS100(t)
	n := aos530Compor(t, aos530Opcoes{preco: aos530PrecoComTool, ambiente: aos530Orcamento("1000000"),
		nats: addr, natsStream: "AOSOCR_" + sufixoAOS100(t)})
	js, ok := n.node.EventStore.(*jetstream.Store)
	if !ok {
		t.Fatalf("o no tinha de estar sobre o JetStream: %T", n.node.EventStore)
	}
	t.Cleanup(func() { _ = js.ApagarStream() })
	const runID = "run-530-jetstream"
	evs := n.correr(t, runID, [2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`})
	med := aos530Mediacoes(t, evs, referencemonitor.EventTypeMediated)
	custos := aos530Custos(t, evs)
	if len(med) != 1 || len(custos) != 1 || custos[0].p.CostMicroUSD != 840 || custos[0].envelope != ocrCustoStepPrefix+med[0].step {
		t.Fatalf("a mediacao e o custo no stream do run no JetStream: %+v %+v", med, custos)
	}
	aplicado := false
	for _, e := range evs {
		aplicado = aplicado || e.Type == "step.ledger.applied"
	}
	if !aplicado || n.node.ocr.lido(ocrResultadoTranscrito) != 1 {
		t.Fatal("o step-ledger da tool call e a transcricao")
	}
	consumo, err := consumoDuravelParaOrcamento(newTurnLedgerBurndown(n.node.EventStore))(context.Background(), runID)
	if err != nil || consumo.CostMicroUSD != 840 {
		t.Fatalf("o consumo duravel lido do JetStream: %+v (%v)", consumo, err)
	}
}

// CONFIRMADO (uma das «Por confirmar no PR» das Notas do AOS-530): UMA TOOL CALL QUE FALHOU
// CONSOME A LEITURA ÚNICA DE `cap:fs.read`. Com a capability armada, a primeira `ocr_read` pede um
// ficheiro que não existe e falha DEPOIS do permit (`ficheiro_em_falta`, sem pedido ao provider); a
// segunda, sobre a imagem que existe, é negada. O mecanismo é o taint e não um contador: o resultado
// de uma tool call — o de erro incluído — entra no contexto do run como `untrusted`, e uma
// capability privilegiada com o contexto `untrusted` é negada pelo hook de taint.
func TestAOS530_No_FalhaConsomeALeituraUnica(t *testing.T) {
	n := aos530Compor(t, aos530Opcoes{privilegiada: true})
	evs := n.correr(t, "run-530-falha-consome",
		[2]string{ocrToolName, `{"doc_id":"nao-existe.png"}`},
		[2]string{ocrToolName, `{"doc_id":"` + aos530Doc + `"}`})
	med := aos530Mediacoes(t, evs, referencemonitor.EventTypeMediated)
	neg := aos530Mediacoes(t, evs, referencemonitor.EventTypeDenied)
	if len(med) != 1 || med[0].Resource.Value != "doc://nao-existe.png" || med[0].Context.Taint != "trusted" || n.node.ocr.lido(ocrCausaEmFalta) != 1 {
		t.Fatalf("a primeira leitura passa o RM e falha no efeito: %+v", med)
	}
	if len(neg) != 1 || neg[0].Resource.Value != "doc://"+aos530Doc || neg[0].DeniedBy != "taint" || neg[0].Context.Taint != "untrusted" {
		t.Fatalf("a segunda leitura e negada pelo taint do resultado de erro: %+v", neg)
	}
	if _, tool := n.upstream.separar(t); len(tool) != 0 || n.node.ocr.lido(ocrResultadoTranscrito) != 0 {
		t.Fatal("nenhuma das duas chega ao provider")
	}
}
