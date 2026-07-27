package main

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	identity "github.com/aos-ref/platform/identity"
	memadapters "github.com/aos-ref/platform/memory/adapters"
	"github.com/aos-ref/platform/memory/domain"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// PII SINTÉTICA — valor INVENTADO (domínio example.com reservado por RFC 2606), nunca
// real. A prova de AOS-208 assenta em que este valor em claro NUNCA aparece no Event
// Store nem nos spans exportados quando o motor está ligado (e o Bootstrap liga-o
// SEMPRE).
const synthNodeEmail = "carol.payroll@example.com"

// spans de ingestão (literais só no TESTE — o gate event-catalog não varre _test.go).
const (
	nodeIngestOp        = "aos.ingest.redacted"
	nodeIngestObjAttr   = "aos.ingest.objective_redacted"
	memoryEpisodicStrea = "memory.episodic"
)

// capturingModel regista o prompt MATERIALIZADO de cada turno e conclui o run no 1º
// turno. É o probe do caminho «→run» da substituição: o objectivo que o LOOP consome
// chega ao [agentruntime.ModelClient] materializado no prompt (é o mesmo mecanismo que
// panicOnMarkerModel exercita ao semear o marcador no Objective e detectá-lo em
// view.Materialized). Concorrente-seguro (o run corre numa goroutine).
type capturingModel struct {
	mu       sync.Mutex
	prompts_ []string
}

func (m *capturingModel) Call(_ context.Context, view agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	m.mu.Lock()
	m.prompts_ = append(m.prompts_, string(view.Materialized))
	m.mu.Unlock()
	return agentruntime.ModelResponse{
		Text:         "run concluido (captura AOS-208)",
		Final:        true,
		Usage:        agentruntime.Usage{InputTokens: 1, OutputTokens: 1},
		CostMicroUSD: 1,
	}, nil
}

func (m *capturingModel) prompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts_...)
}

// TestNodeRedactsRunObjectiveEndToEnd é a prova de AOS-208 ao NÍVEL DO NÓ: um run
// submetido com um objectivo que contém PII sintética produz, NO Event Store do nó E
// nos spans exportados pela porta OTLP do nó, o valor REDIGIDO — e o valor em claro
// NUNCA aparece. Prova que o motor de redacção está LIGADO ao fecho transitivo do nó
// (não apenas disponível como biblioteca): o caminho real Submit→ingestão→run passa
// pelo motor. O passo «→run» — a substituição goal.Objective = ing.Redacted em
// service.go — é asserido DIRECTAMENTE via um modelo de captura que lê o objectivo que
// o LOOP consome (materializado no prompt): sem a substituição, o loop veria o CRU e a
// asserção falha (falsificabilidade da própria substituição, não só da ingestão).
func TestNodeRedactsRunObjectiveEndToEnd(t *testing.T) {
	rec := &otelgenai.RecordingExporter{}
	capModel := &capturingModel{}
	cfg := Config{
		IssuerID: tnIssuerID,
		Humans:   []string{tnHuman},
		IssuerClasses: map[string]identity.ClassPolicy{
			tnClass: {TTL: 15 * time.Minute, Scope: []string{tnCap}},
		},
		IssuerClock:   tnClock(),
		VerifierClock: tnClock(),
		// Porta OTLP injectada ⇒ tracer REAL (SpanTracer) ⇒ o span de ingestão exporta.
		OTLPExporter: rec,
		// Modelo de captura ⇒ observa o objectivo que o LOOP do run realmente consome.
		Model: capModel,
	}
	node, err := Bootstrap(context.Background(), cfg, io.Discard)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()

	if node.Ingestion == nil || !node.Ingestion.Enabled() {
		t.Fatalf("AOS-208: o Bootstrap tem de ligar o motor de redacção SEMPRE (Ingestion.Enabled)")
	}

	svc, err := NewNodeService(node, WithLeaseClock(svcClock()), WithLeaseTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	defer func() { _ = svc.Shutdown(context.Background()) }()

	const runID = "run-pii-1"
	goal := agentruntime.Goal{
		RunID:     runID,
		Principal: referencemonitor.Principal{NHIID: "nhi:" + runID},
		Objective: "Processa o pagamento e notifica " + synthNodeEmail,
	}
	ctx := context.Background()
	if err := svc.Submit(ctx, goal); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, ok, err := svc.Wait(wctx, runID); err != nil || !ok {
		t.Fatalf("Wait(%s): ok=%v err=%v", runID, ok, err)
	}

	// PORTA Event Store (via memory.episodic): o objectivo persistido está REDIGIDO.
	esGoal := readNodeEpisodicGoal(t, node, runID)
	assertNodeRedacted(t, "Event Store (memory.episodic)", esGoal)

	// PORTA otel-genai: o span de ingestão EXPORTADO transporta o valor redigido.
	spans := rec.SpansByName(nodeIngestOp)
	if len(spans) != 1 {
		t.Fatalf("esperava 1 span %q exportado, obtive %d", nodeIngestOp, len(spans))
	}
	v, ok := spans[0].Attribute(nodeIngestObjAttr)
	if !ok {
		t.Fatalf("span de ingestao sem atributo %q", nodeIngestObjAttr)
	}
	assertNodeRedacted(t, "span exportado", v.(string))

	// SEM PII em claro em NENHUM span exportado (invariante de todo o trace do run).
	for _, sp := range rec.Spans() {
		for _, kv := range sp.Attributes {
			if s, ok := kv.Value.(string); ok && strings.Contains(s, synthNodeEmail) {
				t.Fatalf("PII em claro vazou no span %q atributo %q: %q", sp.Name, kv.Key, s)
			}
		}
	}

	// PORTA do LOOP do run (a substituição goal.Objective = ing.Redacted): o objectivo
	// que o loop CONSOME chega ao modelo materializado no prompt. Exige-se que o valor
	// MINIMIZADO — não o cru — seja o que o loop vê. Sem a substituição em service.go, o
	// loop receberia o objectivo CRU e a primeira asserção (PII em claro) dispararia; a
	// segunda garante que a redacção não é vácua neste ponto. É a prova falsificável do
	// passo «→run», que as portas próprias do IngestionGateway (memory.episodic/span de
	// ingestão) não cobrem porque IngestObjective as escreve independentemente da
	// substituição.
	prompts := capModel.prompts()
	if len(prompts) == 0 {
		t.Fatalf("o modelo de captura nao recebeu nenhum turno — o loop do run nao correu")
	}
	var sawRedactedInLoop bool
	for _, p := range prompts {
		if strings.Contains(p, synthNodeEmail) {
			t.Fatalf("PII em claro alcancou o LOOP do run (prompt materializado) — a substituicao goal.Objective=ing.Redacted nao foi aplicada: %q", p)
		}
		if strings.Contains(p, "[REDACTED:email]") {
			sawRedactedInLoop = true
		}
	}
	if !sawRedactedInLoop {
		t.Fatalf("o LOOP do run nunca viu o objectivo redigido no prompt materializado — a minimizacao nao alcancou o loop")
	}
}

func readNodeEpisodicGoal(t *testing.T, node *Node, runID string) string {
	t.Helper()
	events, err := node.EventStore.Read(context.Background(), memoryEpisodicStrea, 1)
	if err != nil {
		t.Fatalf("EventStore.Read(%s): %v", memoryEpisodicStrea, err)
	}
	for _, ev := range events {
		if ev.Type != memadapters.EventTypeWritten {
			continue
		}
		r, derr := domain.UnmarshalRecord(ev.Payload)
		if derr != nil {
			t.Fatalf("UnmarshalRecord: %v", derr)
		}
		if r.Metadata.RunID != runID {
			continue
		}
		if b, ok := r.Body.(domain.EpisodicBody); ok {
			return b.Goal
		}
	}
	t.Fatalf("nenhum registo episodico para run %q no Event Store do no", runID)
	return ""
}

func assertNodeRedacted(t *testing.T, where, value string) {
	t.Helper()
	if strings.Contains(value, synthNodeEmail) {
		t.Fatalf("%s: PII EM CLARO presente (esperava redigido): %q", where, value)
	}
	if !strings.Contains(value, "[REDACTED:email]") {
		t.Fatalf("%s: sem marcador [REDACTED:email] (esperava redigido): %q", where, value)
	}
}
