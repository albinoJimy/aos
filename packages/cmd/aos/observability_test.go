package main

// Testes NÃO-VACUOSOS da observabilidade OTLP do nó (AOS-173, EPIC-15 §13), com -race:
//
//   - end-to-end: um run instrumentado produz spans ⇒ o exporter OTLP/HTTP faz POST de
//     OTLP/JSON BEM-FORMADO a um httptest.Server (colector) ⇒ o documento recebido tem
//     resourceSpans/scopeSpans/spans, os atributos gen_ai/aos, o CUSTO, e NENHUM segredo;
//   - FAIL-OPEN: colector a devolver 500 / em baixo ⇒ o run COMPLETA na mesma (erro não
//     propagado; sem bloqueio; sem fuga de goroutine no shutdown);
//   - GATING: sem OTLPEndpoint ⇒ NoopTracer (nada exportado; zero overhead);
//   - determinismo: SequentialIDGenerator + WithClock ⇒ ids/timestamps estáveis;
//   - WORM↔trajectória: cada selo de audit emite um span de ligação (só ids/metadados).

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	audit "github.com/aos-ref/platform/audit"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// --- decodificação mínima do wire OTLP/JSON (espelha otel-genai/otlp.go) ---

type otlpDoc struct {
	ResourceSpans []struct {
		ScopeSpans []struct {
			Scope struct {
				Name string `json:"name"`
			} `json:"scope"`
			Spans []otlpSpanWire `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

type otlpSpanWire struct {
	TraceID      string `json:"traceId"`
	SpanID       string `json:"spanId"`
	ParentSpanID string `json:"parentSpanId"`
	Name         string `json:"name"`
	Attributes   []struct {
		Key   string `json:"key"`
		Value struct {
			StringValue *string  `json:"stringValue"`
			IntValue    *string  `json:"intValue"`
			DoubleValue *float64 `json:"doubleValue"`
			BoolValue   *bool    `json:"boolValue"`
		} `json:"value"`
	} `json:"attributes"`
}

func (s otlpSpanWire) attr(key string) (string, bool) {
	for _, a := range s.Attributes {
		if a.Key != key {
			continue
		}
		switch {
		case a.Value.StringValue != nil:
			return *a.Value.StringValue, true
		case a.Value.IntValue != nil:
			return *a.Value.IntValue, true
		case a.Value.DoubleValue != nil:
			return "double", true
		case a.Value.BoolValue != nil:
			return "bool", true
		default:
			return "", true // presente mas AnyValue vazio
		}
	}
	return "", false
}

// otlpCollector é um colector OTLP/HTTP de teste: guarda os corpos POSTados a
// /v1/traces e responde com um status configurável (default 200).
type otlpCollector struct {
	mu     sync.Mutex
	bodies [][]byte
	paths  []string
	ctypes []string
	status int
}

func (c *otlpCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.paths = append(c.paths, r.URL.Path)
	c.ctypes = append(c.ctypes, r.Header.Get("Content-Type"))
	if r.Method == http.MethodPost {
		c.bodies = append(c.bodies, body)
	}
	st := c.status
	c.mu.Unlock()
	if st == 0 {
		st = http.StatusOK
	}
	w.WriteHeader(st)
}

// spans devolve TODOS os spans OTLP recebidos até agora (achatados).
func (c *otlpCollector) spans(t *testing.T) []otlpSpanWire {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []otlpSpanWire
	for _, b := range c.bodies {
		var doc otlpDoc
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("corpo OTLP mal-formado (não é JSON válido): %v", err)
		}
		if len(doc.ResourceSpans) == 0 {
			t.Fatalf("corpo OTLP sem resourceSpans: %s", string(b))
		}
		for _, rs := range doc.ResourceSpans {
			if len(rs.ScopeSpans) == 0 {
				t.Fatalf("resourceSpans sem scopeSpans: %s", string(b))
			}
			for _, ss := range rs.ScopeSpans {
				if ss.Scope.Name == "" {
					t.Errorf("scopeSpans sem scope.name")
				}
				out = append(out, ss.Spans...)
			}
		}
	}
	return out
}

// obsGoal constrói um goal válido com modelo pinado e um payload de OBJETIVO/SISTEMA
// deliberadamente "secreto" para o teste de ausência de segredos.
func obsGoal(runID string) agentruntime.Goal {
	return agentruntime.Goal{
		RunID:     runID,
		Principal: referencemonitor.Principal{NHIID: "nhi:" + runID},
		Model:     agentruntime.ModelConfig{ModelID: "model:test-obs"},
		System:    obsSecretSystem,
		Objective: obsSecretObjective,
		MaxTurns:  2,
	}
}

const (
	obsSecretObjective = "SEGREDO_OBJETIVO_NAO_DEVE_APARECER_EM_NENHUM_SPAN"
	obsSecretSystem    = "SEGREDO_SYSTEM_PROMPT_NAO_DEVE_VAZAR"
)

// obsConfig deriva a config de teste do nó com observabilidade ligada a endpoint.
func obsConfig(endpoint string) Config {
	cfg := tnBaseConfig()
	cfg.OTLPEndpoint = endpoint
	return cfg
}

// assertNoSecrets falha se qualquer valor string de atributo contiver um dos segredos
// do payload — a prova de que os spans só transportam ids/metadados/hashes.
func assertNoSecrets(t *testing.T, spans []otlpSpanWire) {
	t.Helper()
	for _, s := range spans {
		for _, a := range s.Attributes {
			if a.Value.StringValue == nil {
				continue
			}
			v := *a.Value.StringValue
			if strings.Contains(v, obsSecretObjective) || strings.Contains(v, obsSecretSystem) {
				t.Fatalf("SEGREDO vazou no atributo %q do span %q: %q", a.Key, s.Name, v)
			}
		}
	}
}

// TestObservabilityEndToEndExportsWellFormedOTLPWithCost prova o caminho feliz: um run
// real através do nó produz spans que o exporter OTLP/HTTP entrega BEM-FORMADOS ao
// colector, com os atributos gen_ai/aos e o CUSTO presentes, e SEM segredos.
func TestObservabilityEndToEndExportsWellFormedOTLPWithCost(t *testing.T) {
	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	node, err := Bootstrap(context.Background(), obsConfig(srv.URL), io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	if node.otlp == nil {
		t.Fatal("esperava um exporter OTLP aberto pelo nó (observabilidade ligada)")
	}

	res, _, err := node.Runtime.Run(context.Background(), obsGoal("run-obs-1"), nil)
	if err != nil {
		t.Fatalf("Run (o run não pode falhar por causa da observabilidade): %v", err)
	}
	if res.TotalCostMicroUSD == 0 {
		t.Fatal("o run de referência devia acumular custo não-nulo (modelo de referência)")
	}

	// Close drena o exporter SINCRONAMENTE (flush final): após ele, tudo o que foi
	// enfileirado já foi POSTado.
	if err := node.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// O colector recebeu POSTs a /v1/traces com Content-Type application/json.
	col.mu.Lock()
	for i, p := range col.paths {
		if p != "/v1/traces" {
			t.Errorf("POST[%d] a %q, esperava /v1/traces", i, p)
		}
		if col.ctypes[i] != "application/json" {
			t.Errorf("Content-Type[%d] = %q, esperava application/json", i, col.ctypes[i])
		}
	}
	col.mu.Unlock()

	spans := col.spans(t)
	if len(spans) == 0 {
		t.Fatal("nenhum span exportado — a observabilidade não fluiu ponta-a-ponta")
	}

	byName := map[string]otlpSpanWire{}
	for _, s := range spans {
		byName[s.Name] = s
	}

	// (a) invoke_agent (o run inteiro) presente.
	if _, ok := byName[otelgenai.OpInvokeAgent]; !ok {
		t.Errorf("faltou o span %q; nomes vistos: %v", otelgenai.OpInvokeAgent, names(spans))
	}
	// (b) freeze do tool set (prova o wiring do FreezeOptions.WithTracer).
	if _, ok := byName["registry.freeze_toolset"]; !ok {
		t.Errorf("faltou o span de freeze do tool set; nomes vistos: %v", names(spans))
	}
	// (c) chat com CUSTO e atributos gen_ai/aos (o coração do §13).
	chat, ok := byName[otelgenai.OpChat]
	if !ok {
		t.Fatalf("faltou o span %q; nomes vistos: %v", otelgenai.OpChat, names(spans))
	}
	if v, ok := chat.attr(otelgenai.AttrRequestModel); !ok || v != "model:test-obs" {
		t.Errorf("chat.%s = %q (ok=%v), esperava model:test-obs", otelgenai.AttrRequestModel, v, ok)
	}
	if v, ok := chat.attr(otelgenai.AttrPrincipalNHI); !ok || v == "" {
		t.Errorf("chat.%s ausente/vazio (%q, ok=%v)", otelgenai.AttrPrincipalNHI, v, ok)
	}
	// CUSTO: micro-USD inteiro = 1500 (fonte de verdade) + o USD float em paralelo.
	if v, ok := chat.attr(otelgenai.AttrCostMicroUSD); !ok || v != "1500" {
		t.Errorf("chat.%s = %q (ok=%v), esperava \"1500\" (custo exportado)", otelgenai.AttrCostMicroUSD, v, ok)
	}
	if _, ok := chat.attr(otelgenai.AttrCostUSD); !ok {
		t.Errorf("chat.%s ausente (custo USD de conveniência)", otelgenai.AttrCostUSD)
	}

	// (d) NENHUM segredo em nenhum span.
	assertNoSecrets(t, spans)

	// Stats: exportou, não largou/falhou nada.
	st := stExporter(t, node)
	if st.Exported == 0 {
		t.Errorf("stats.Exported == 0, esperava > 0")
	}
	if st.Failed != 0 || st.Dropped != 0 {
		t.Errorf("stats esperava 0 falhas/drops, veio Failed=%d Dropped=%d", st.Failed, st.Dropped)
	}
}

// TestObservabilityFailOpenOnCollectorError prova o invariante FAIL-OPEN: com o colector
// a devolver 500 (ou em baixo), o run COMPLETA na mesma (erro não propagado), o shutdown
// é limpo (sem fuga de goroutine, garantido por -race) e a falha é CONTABILIZADA.
func TestObservabilityFailOpenOnCollectorError(t *testing.T) {
	t.Run("colector devolve 500", func(t *testing.T) {
		col := &otlpCollector{status: http.StatusInternalServerError}
		srv := httptest.NewServer(col)
		defer srv.Close()

		node, err := Bootstrap(context.Background(), obsConfig(srv.URL), io.Discard)
		if err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		// O run NÃO pode falhar por causa de um colector a 500.
		if _, _, err := node.Runtime.Run(context.Background(), obsGoal("run-fail-500"), nil); err != nil {
			t.Fatalf("Run devia completar apesar do 500 do colector: %v", err)
		}
		if err := node.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		st := stExporter(t, node)
		if st.Failed == 0 {
			t.Errorf("esperava falhas de export contabilizadas (Failed>0) com o colector a 500; stats=%+v", st)
		}
		if st.Exported != 0 {
			t.Errorf("nada devia ter sido exportado com sucesso (Exported=%d)", st.Exported)
		}
	})

	t.Run("colector em baixo (connection refused)", func(t *testing.T) {
		col := &otlpCollector{}
		srv := httptest.NewServer(col)
		endpoint := srv.URL
		srv.Close() // derruba o colector ANTES do run: os POSTs falham no transporte

		node, err := Bootstrap(context.Background(), obsConfig(endpoint), io.Discard)
		if err != nil {
			t.Fatalf("Bootstrap: %v", err)
		}
		if _, _, err := node.Runtime.Run(context.Background(), obsGoal("run-down"), nil); err != nil {
			t.Fatalf("Run devia completar apesar do colector em baixo: %v", err)
		}
		if err := node.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		st := stExporter(t, node)
		if st.Failed == 0 {
			t.Errorf("esperava falhas de export contabilizadas com o colector em baixo; stats=%+v", st)
		}
	})
}

// TestObservabilityGatingNoEndpointUsesNoop prova o GATING: sem OTLPEndpoint o nó usa o
// NoopTracer — nenhum exporter é aberto, nada é exportado e o comportamento é o de antes.
func TestObservabilityGatingNoEndpointUsesNoop(t *testing.T) {
	node, err := Bootstrap(context.Background(), tnBaseConfig(), io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()

	if node.otlp != nil {
		t.Error("sem OTLPEndpoint não devia existir exporter OTLP aberto")
	}
	if _, ok := node.Tracer.(otelgenai.NoopTracer); !ok {
		t.Errorf("sem OTLPEndpoint o tracer devia ser NoopTracer, é %T", node.Tracer)
	}
	// O run continua a correr normalmente (zero overhead de observabilidade).
	if _, _, err := node.Runtime.Run(context.Background(), obsGoal("run-noop"), nil); err != nil {
		t.Fatalf("Run com NoopTracer: %v", err)
	}
}

// TestObservabilityMalformedEndpointFailsClosed prova o fail-closed de CONFIG: um endpoint
// malformado aborta o arranque (o nó não sobe a fingir que exporta).
func TestObservabilityMalformedEndpointFailsClosed(t *testing.T) {
	cfg := obsConfig("://nao-e-url")
	if _, err := Bootstrap(context.Background(), cfg, io.Discard); err == nil {
		t.Fatal("esperava fail-closed com endpoint OTLP malformado")
	}
}

// TestOTLPExporterDeterministicWireFormat prova, com SequentialIDGenerator + relógio
// fixo, que o exporter POSTa um documento OTLP/JSON determinista e bem-formado, com o
// custo e sem segredos — o teste ao NÍVEL do exporter (isola-o do run).
func TestOTLPExporterDeterministicWireFormat(t *testing.T) {
	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	exp, err := NewOTLPHTTPExporter(srv.URL, WithOTLPHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	fixed := time.Unix(1_700_000_000, 0).UTC()
	tracer := otelgenai.NewTracer(exp,
		otelgenai.WithIDGenerator(&otelgenai.SequentialIDGenerator{}),
		otelgenai.WithClock(func() time.Time { return fixed }),
	)

	_, span := tracer.StartSpan(context.Background(), otelgenai.OpChat)
	span.SetAttribute(otelgenai.AttrOperationName, otelgenai.OpChat)
	span.SetAttribute(otelgenai.AttrRequestModel, "model:det")
	span.SetAttribute(otelgenai.AttrPrincipalNHI, "nhi:det")
	span.SetAttribute(otelgenai.AttrCostMicroUSD, int64(1500))
	span.SetAttribute(otelgenai.AttrCostUSD, otelgenai.MicroUSDToUSD(1500))
	span.End()

	if err := exp.Close(); err != nil {
		t.Fatalf("exporter Close: %v", err)
	}

	spans := col.spans(t)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span exportado, veio %d", len(spans))
	}
	s := spans[0]
	// Ids deterministas do SequentialIDGenerator (trace=1, span=1 nos bytes baixos).
	if s.TraceID != "00000000000000000000000000000001" {
		t.Errorf("traceId determinista errado: %q", s.TraceID)
	}
	if s.SpanID != "0000000000000001" {
		t.Errorf("spanId determinista errado: %q", s.SpanID)
	}
	if v, ok := s.attr(otelgenai.AttrCostMicroUSD); !ok || v != "1500" {
		t.Errorf("custo micro-USD = %q (ok=%v), esperava \"1500\"", v, ok)
	}
	if v, ok := s.attr(otelgenai.AttrRequestModel); !ok || v != "model:det" {
		t.Errorf("modelo = %q (ok=%v)", v, ok)
	}
}

// TestAuditTracingStoreEmitsSealSpanLinkingWORM prova a ligação WORM↔trajectória: cada
// selo de audit emite um span de observabilidade com run_id/step_id + audit_seq +
// entry_hash + veredicto/tool, e NUNCA o payload/recurso (invariante sem-segredos). O
// store decorado continua a delegar Append/Head/At no WORM real.
func TestAuditTracingStoreEmitsSealSpanLinkingWORM(t *testing.T) {
	rec := &otelgenai.RecordingExporter{}
	tracer := otelgenai.NewTracer(rec)

	inner := audit.NewMemStore()
	store := newAuditTracingStore(inner, tracer)

	const secretResource = "https://api.example.com/SEGREDO_DO_RECURSO"
	sealed, err := store.Append(context.Background(), audit.AuditRecord{
		Partition: "run-worm-1",
		RunID:     "run-worm-1",
		StepID:    "step-7",
		Decision:  audit.DecisionAllow,
		ToolID:    "tool.http",
		Resource:  audit.Resource{Type: "url", Value: secretResource, Region: "eu"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if sealed.AuditSeq != 1 || len(sealed.EntryHash) == 0 {
		t.Fatalf("o selo real devia atribuir seq/hash: seq=%d hashLen=%d", sealed.AuditSeq, len(sealed.EntryHash))
	}

	seals := rec.SpansByName(opAuditSeal)
	if len(seals) != 1 {
		t.Fatalf("esperava 1 span de selo %q, veio %d", opAuditSeal, len(seals))
	}
	sp := seals[0]

	mustAttr := func(key, want string) {
		v, ok := sp.Attribute(key)
		if !ok {
			t.Errorf("selo sem o atributo %q", key)
			return
		}
		if want != "" {
			if s, _ := v.(string); s != want {
				t.Errorf("selo.%s = %v, esperava %q", key, v, want)
			}
		}
	}
	mustAttr(attrAuditPartition, "run-worm-1")
	mustAttr(otelgenai.AttrRunID, "run-worm-1")
	mustAttr(otelgenai.AttrStepID, "step-7")
	mustAttr(otelgenai.AttrDecision, string(audit.DecisionAllow))
	mustAttr(otelgenai.AttrToolName, "tool.http")
	if v, ok := sp.Attribute(attrAuditSeq); !ok || v.(int64) != 1 {
		t.Errorf("selo.%s = %v (ok=%v), esperava 1", attrAuditSeq, v, ok)
	}
	if v, ok := sp.Attribute(attrAuditEntryHash); !ok {
		t.Errorf("selo sem %s (âncora tamper-evident)", attrAuditEntryHash)
	} else if s, _ := v.(string); s == "" {
		t.Errorf("selo.%s vazio", attrAuditEntryHash)
	}

	// SEM segredos: nenhum atributo do selo transporta o recurso/payload.
	for _, kv := range sp.Attributes {
		if s, ok := kv.Value.(string); ok && strings.Contains(s, "SEGREDO_DO_RECURSO") {
			t.Fatalf("o recurso secreto vazou no atributo %q do selo: %q", kv.Key, s)
		}
	}

	// O store decorado continua a ser um WORM funcional (delegação).
	head, err := store.Head(context.Background(), "run-worm-1")
	if err != nil || head != 1 {
		t.Errorf("Head delegado = %d, err=%v, esperava 1", head, err)
	}
	if _, ok, err := store.At(context.Background(), "run-worm-1", 1); err != nil || !ok {
		t.Errorf("At delegado devia encontrar o selo 1 (ok=%v, err=%v)", ok, err)
	}
}

// TestAuditSealFlowsToOTLPCollector fecha a lacuna apontada na auditoria de COMPLETUDE: a
// ligação WORM↔OTLP passa a ser provada PONTA-A-PONTA pela via OTLP/HTTP REAL do nó (não
// só ao nível de unidade com o RecordingExporter). Um selo de audit, decorado por
// [newAuditTracingStore] sobre um SpanTracer que exporta pelo [OTLPHTTPExporter], chega ao
// colector httptest como um span audit_seal BEM-FORMADO, com a âncora tamper-evident
// (entry_hash) e o seq/run_id/decisão/tool — e SEM o recurso secreto no corpo OTLP.
func TestAuditSealFlowsToOTLPCollector(t *testing.T) {
	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	exp, err := NewOTLPHTTPExporter(srv.URL, WithOTLPHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	tracer := otelgenai.NewTracer(exp)

	// Mesma composição que o nó liga em bootstrap.go quando a observabilidade está ligada:
	// o WORM real decorado com emissão de span de selo, sobre o tracer→exporter OTLP.
	store := newAuditTracingStore(audit.NewMemStore(), tracer)
	const secretResource = "https://api.example.com/SEGREDO_DO_RECURSO"
	sealed, err := store.Append(context.Background(), audit.AuditRecord{
		Partition: "run-seal-otlp",
		RunID:     "run-seal-otlp",
		StepID:    "step-3",
		Decision:  audit.DecisionAllow,
		ToolID:    "tool.http",
		Resource:  audit.Resource{Type: "url", Value: secretResource, Region: "eu"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if sealed.AuditSeq != 1 || len(sealed.EntryHash) == 0 {
		t.Fatalf("o selo real devia atribuir seq/hash: seq=%d hashLen=%d", sealed.AuditSeq, len(sealed.EntryHash))
	}

	// Close drena o exporter SINCRONAMENTE: ao retornar, o POST do span de selo já ocorreu.
	if err := exp.Close(); err != nil {
		t.Fatalf("exporter Close: %v", err)
	}

	spans := col.spans(t)
	var seal *otlpSpanWire
	for i := range spans {
		if spans[i].Name == opAuditSeal {
			seal = &spans[i]
			break
		}
	}
	if seal == nil {
		t.Fatalf("o span de selo %q não chegou ao colector OTLP; nomes vistos: %v", opAuditSeal, names(spans))
	}
	if v, ok := seal.attr(attrAuditEntryHash); !ok || v == "" {
		t.Errorf("selo sem %s no wire OTLP (âncora tamper-evident)", attrAuditEntryHash)
	}
	if v, ok := seal.attr(otelgenai.AttrRunID); !ok || v != "run-seal-otlp" {
		t.Errorf("selo.%s = %q (ok=%v), esperava run-seal-otlp", otelgenai.AttrRunID, v, ok)
	}
	if v, ok := seal.attr(otelgenai.AttrDecision); !ok || v != string(audit.DecisionAllow) {
		t.Errorf("selo.%s = %q (ok=%v), esperava %q", otelgenai.AttrDecision, v, ok, audit.DecisionAllow)
	}
	if v, ok := seal.attr(attrAuditSeq); !ok || v != "1" {
		t.Errorf("selo.%s = %q (ok=%v), esperava \"1\"", attrAuditSeq, v, ok)
	}

	// SEM segredos no CORPO OTLP recebido (o recurso concreto nunca entra no wire).
	col.mu.Lock()
	for _, b := range col.bodies {
		if strings.Contains(string(b), "SEGREDO_DO_RECURSO") {
			col.mu.Unlock()
			t.Fatal("o recurso secreto vazou no corpo OTLP exportado")
		}
	}
	col.mu.Unlock()
}

// TestOTLPExporterCloseIsIdempotent prova que Close pode ser chamado múltiplas vezes (e
// concorrentemente) sem pânico — o closeOnce fecha `done` uma só vez e a goroutine de
// espera é criada uma só vez lá dentro (finding: goroutine-redundante). -race cobre a
// ausência de corrida entre as invocações.
func TestOTLPExporterCloseIsIdempotent(t *testing.T) {
	exp, err := NewOTLPHTTPExporter("http://127.0.0.1:0",
		WithOTLPHTTPClient(&http.Client{Timeout: 200 * time.Millisecond}))
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	if err := exp.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}
	if err := exp.Close(); err != nil {
		t.Fatalf("Close #2: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = exp.Close() }()
	}
	wg.Wait()
}

// TestOTLPExporterShutdownStatsReconcile prova a reconciliação de stats no shutdown
// (finding: contabilizacao-shutdown): após Close, todo span aceite em Enqueued foi
// contabilizado em Exported+Dropped+Failed — nenhum fica preso na fila sem contabilização.
func TestOTLPExporterShutdownStatsReconcile(t *testing.T) {
	col := &otlpCollector{}
	srv := httptest.NewServer(col)
	defer srv.Close()

	exp, err := NewOTLPHTTPExporter(srv.URL, WithOTLPHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	tracer := otelgenai.NewTracer(exp)
	for i := 0; i < 50; i++ {
		_, span := tracer.StartSpan(context.Background(), otelgenai.OpChat)
		span.SetAttribute(otelgenai.AttrOperationName, otelgenai.OpChat)
		span.SetAttribute(otelgenai.AttrRequestModel, "model:recon")
		span.SetAttribute(otelgenai.AttrPrincipalNHI, "nhi:recon")
		span.End()
	}
	if err := exp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	st := exp.Stats()
	if st.Enqueued != st.Exported+st.Dropped+st.Failed {
		t.Errorf("stats não reconciliam no shutdown: Enqueued=%d, Exported+Dropped+Failed=%d (%+v)",
			st.Enqueued, st.Exported+st.Dropped+st.Failed, st)
	}
}

// --- helpers ---

func names(spans []otlpSpanWire) []string {
	out := make([]string, 0, len(spans))
	for _, s := range spans {
		out = append(out, s.Name)
	}
	return out
}

func stExporter(t *testing.T, node *Node) OTLPStats {
	t.Helper()
	if node.otlp == nil {
		t.Fatal("nó sem exporter OTLP (esperava observabilidade ligada)")
	}
	return node.otlp.Stats()
}
