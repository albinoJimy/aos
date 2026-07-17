package orchestrator_test

import (
	"context"
	"encoding/json"
	"testing"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// spawnedSeed lê o child_seed_traceparent do evento subagent.spawned de childNode.
func spawnedSeed(t *testing.T, store *eventstore.Store, runID, childNode string) string {
	t.Helper()
	events, err := store.Read(context.Background(), runID, 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, e := range events {
		if e.Type != orchestrator.EventSubagentSpawned {
			continue
		}
		var p orchestrator.DelegationEventPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("unmarshal spawned: %v", err)
		}
		if p.ChildNode == childNode {
			return p.ChildSeedTraceParent
		}
	}
	t.Fatalf("subagent.spawned de %s não encontrado", childNode)
	return ""
}

// anchorFor devolve o span invoke_agent-âncora cujo parent_span_id é o dado (a
// âncora aberta no Spawn abaixo desse pai).
func anchorFor(t *testing.T, tracer *otelgenai.RecordingTracer, parentSpanID [8]byte) *otelgenai.RecordedSpan {
	t.Helper()
	var found *otelgenai.RecordedSpan
	for _, s := range tracer.SpansByOperation(agentruntime.OpInvokeAgent) {
		if s.ParentSpanID == parentSpanID {
			if found != nil {
				t.Fatalf("mais de uma âncora sob o mesmo pai %x", parentSpanID)
			}
			found = s
		}
	}
	if found == nil {
		t.Fatalf("nenhuma âncora invoke_agent sob o pai %x", parentSpanID)
	}
	return found
}

// TestSpawnPropagatesParentSpanContextFromCtx prova a propagação cross-fronteira
// AOS-077 no caso MESMO-PROCESSO: o Spawn abre a âncora invoke_agent do filho COMO
// FILHO do SpanContext do pai propagado no ctx (trace_id comum + parent_span_id =
// span_id do invoke_agent do pai), e devolve o traceparent da âncora no SpawnHandle
// e no evento subagent.spawned.
func TestSpawnPropagatesParentSpanContextFromCtx(t *testing.T) {
	t.Parallel()
	const runID = "run-xprop"
	store := newStore(t)
	producer := eventstore.Producer{NHIID: "nhi:orq"}
	b, err := budget.New(runID, amt(1000, 1_000_000))
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	iss, _ := newIssuer(t)
	parent := issueParent(t, iss)
	tracer := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	del, err := orchestrator.NewDelegator(b, permittingRM(t), iss,
		orchestrator.WithDelegationTracer(tracer),
		orchestrator.WithDelegationStore(store, producer))
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}

	// O invoke_agent do PAI corre no mesmo processo: o seu SpanContext viaja no ctx.
	pctx, pspan := tracer.StartSpan(context.Background(), agentruntime.OpInvokeAgent)
	parentSC := pspan.SpanContext()

	h, err := del.Spawn(pctx, orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "nChild",
		InheritedBudget: amt(400, 400_000), SpawnReserve: amt(50, 50_000),
		Depth: 1, ParentToken: parent.Compact, Child: childReq("agt-child"), ChildTaskID: "agt-child",
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	pspan.End()

	// A âncora do filho parenteia sob o invoke_agent do pai (ligação NATIVA OTel).
	anchor := anchorFor(t, tracer, parentSC.SpanID)
	if anchor.SpanContext.TraceID != parentSC.TraceID {
		t.Fatalf("âncora não herda o trace_id do pai")
	}
	if anchor.ParentSpanID != parentSC.SpanID {
		t.Fatalf("âncora não aponta ParentSpanID ao span_id do pai")
	}

	// O SEED devolvido no handle é o traceparent da âncora.
	if h.ChildSeedTraceParent == "" {
		t.Fatal("SpawnHandle.ChildSeedTraceParent vazio")
	}
	seedSC, perr := otelgenai.ParseTraceParent(h.ChildSeedTraceParent)
	if perr != nil {
		t.Fatalf("ParseTraceParent(handle): %v", perr)
	}
	if seedSC != anchor.SpanContext {
		t.Fatalf("seed do handle ≠ SpanContext da âncora: %+v vs %+v", seedSC, anchor.SpanContext)
	}
	// O mesmo seed foi projectado no evento subagent.spawned (não-secreto, replayável).
	if got := spawnedSeed(t, store, runID, "nChild"); got != h.ChildSeedTraceParent {
		t.Fatalf("child_seed_traceparent no evento (%q) ≠ handle (%q)", got, h.ChildSeedTraceParent)
	}
}

// TestSpawnRecursiveTraceTreeAcrossBoundary prova a recursão cross-fronteira
// AOS-077: pai→filho→neto, onde cada nível seguinte é semeado APENAS pelo
// traceparent do handle do anterior (req.ParentTraceParent, ctx SEM SpanContext — o
// caso fronteira RT→ORQ). A árvore de âncoras resultante é acíclica, tem um só
// trace_id e cada não-raiz parenteia sob o span_id do nível acima.
func TestSpawnRecursiveTraceTreeAcrossBoundary(t *testing.T) {
	t.Parallel()
	const runID = "run-xtree"
	store := newStore(t)
	producer := eventstore.Producer{NHIID: "nhi:orq"}
	b, err := budget.New(runID, amt(1000, 1_000_000))
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	iss, _ := newIssuer(t)
	parent := issueParent(t, iss)
	tracer := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	del, err := orchestrator.NewDelegator(b, permittingRM(t), iss,
		orchestrator.WithDelegationTracer(tracer),
		orchestrator.WithDelegationStore(store, producer))
	if err != nil {
		t.Fatalf("NewDelegator: %v", err)
	}
	ctx := context.Background()

	// Nível 1: filho, semeado por um traceparent do pai (fronteira: ctx sem SC).
	// O "pai" aqui é a raiz do trace — abrimos o seu invoke_agent para obter o seed.
	_, rootSpan := tracer.StartSpan(ctx, agentruntime.OpInvokeAgent)
	rootSeed := agentruntime.FormatTraceParent(rootSpan.SpanContext())
	rootSpan.End()

	hChild, err := del.Spawn(ctx, orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: runID, ChildBudgetNode: "nA",
		InheritedBudget: amt(400, 400_000), SpawnReserve: amt(50, 50_000),
		Depth: 1, ParentToken: parent.Compact, Child: childReq("agt-A"), ChildTaskID: "agt-A",
		ParentTraceParent: rootSeed,
	})
	if err != nil {
		t.Fatalf("Spawn filho: %v", err)
	}

	// Nível 2: neto, semeado SÓ pelo traceparent do handle do filho (cross-fronteira).
	hGrand, err := del.Spawn(ctx, orchestrator.SpawnRequest{
		RunID: runID, ParentBudgetNode: "nA", ChildBudgetNode: "nA1",
		InheritedBudget: amt(150, 150_000), SpawnReserve: amt(30, 30_000),
		Depth: 2, ParentToken: hChild.ChildToken.Compact, Child: childReq("agt-A1"), ChildTaskID: "agt-A1",
		ParentTraceParent: hChild.ChildSeedTraceParent,
	})
	if err != nil {
		t.Fatalf("Spawn neto: %v", err)
	}

	rootSC := rootSpan.SpanContext()
	childSC, _ := otelgenai.ParseTraceParent(hChild.ChildSeedTraceParent)
	grandSC, _ := otelgenai.ParseTraceParent(hGrand.ChildSeedTraceParent)

	// Um só trace_id em toda a árvore.
	if childSC.TraceID != rootSC.TraceID || grandSC.TraceID != rootSC.TraceID {
		t.Fatalf("trace_id não é comum aos 3 níveis")
	}
	// Arestas por span_id: filho→raiz, neto→filho.
	childAnchor := anchorFor(t, tracer, rootSC.SpanID)
	if childAnchor.SpanContext != childSC {
		t.Fatalf("âncora do filho ≠ seed do handle do filho")
	}
	grandAnchor := anchorFor(t, tracer, childSC.SpanID)
	if grandAnchor.SpanContext != grandSC {
		t.Fatalf("âncora do neto ≠ seed do handle do neto")
	}

	// A árvore inteira de spans é acíclica e completamente ligada.
	assertConnectedAcyclicOrchestrator(t, tracer.Spans())
}

// assertConnectedAcyclicOrchestrator verifica a propriedade estrutural: um só
// trace_id, uma raiz, cada não-raiz com pai presente, sem ciclos, span_ids únicos.
func assertConnectedAcyclicOrchestrator(t *testing.T, spans []*otelgenai.RecordedSpan) {
	t.Helper()
	if len(spans) == 0 {
		t.Fatal("árvore vazia")
	}
	byID := make(map[[8]byte]*otelgenai.RecordedSpan, len(spans))
	var trace [16]byte
	roots := 0
	for i, s := range spans {
		if !s.SpanContext.IsValid() {
			t.Fatalf("span %d inválido", i)
		}
		if i == 0 {
			trace = s.SpanContext.TraceID
		} else if s.SpanContext.TraceID != trace {
			t.Fatalf("trace_id divergente (span %d)", i)
		}
		if _, dup := byID[s.SpanContext.SpanID]; dup {
			t.Fatalf("span_id duplicado: %x", s.SpanContext.SpanID)
		}
		byID[s.SpanContext.SpanID] = s
		if s.ParentSpanID == ([8]byte{}) {
			roots++
		}
	}
	if roots != 1 {
		t.Fatalf("esperava 1 raiz, obtive %d", roots)
	}
	for _, s := range spans {
		steps := 0
		for cur := s; cur.ParentSpanID != ([8]byte{}); {
			p, ok := byID[cur.ParentSpanID]
			if !ok {
				t.Fatalf("span %x aponta a pai ausente %x", cur.SpanContext.SpanID, cur.ParentSpanID)
			}
			cur = p
			if steps++; steps > len(spans) {
				t.Fatalf("ciclo a partir de %x", s.SpanContext.SpanID)
			}
		}
	}
}
