package planner_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planner"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	rm "github.com/aos-ref/kernel/reference-monitor"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/substrate/eventstore"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ---------------------------------------------------------------------------
// Harness determinístico: emissor NHI real, orçamento CAS real, RM real. Só o
// Decomposer (o "LLM") é um stub — é a INTERFACE injectada exigida pelo CA.
// ---------------------------------------------------------------------------

var baseTime = time.Unix(1_700_000_000, 0).UTC()

func newIssuer(t *testing.T) *identity.Issuer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	var seq atomic.Int64
	iss, err := identity.NewIssuer("aos-issuer-aos234", priv, map[string]identity.ClassPolicy{
		"coordinator": {TTL: 30 * time.Minute, Scope: []string{"cap:agent.spawn", "cap:plan", "cap:work"}},
		"worker":      {TTL: 30 * time.Minute, Scope: []string{"cap:plan", "cap:work"}},
	},
		identity.WithIssuerClock(func() time.Time { return baseTime }),
		identity.WithIDSource(func() string { return "jti-" + itoa(seq.Add(1)) }),
	)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	return iss
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// runToken emite o token NHI da RAIZ do run (agt-run), on-behalf-of um humano, com
// autoridade para planear. É o pai on-behalf-of da NHI agent:planner.
func runToken(t *testing.T, iss *identity.Issuer) identity.Token {
	t.Helper()
	tok, err := iss.Issue(context.Background(), identity.IssueRequest{
		UserID: "human:alice", AgentID: "agt-run", AgentClass: "coordinator",
		PolicyRef: "policy://run@1", UserAuthority: []string{"cap:plan", "cap:work"},
	})
	if err != nil {
		t.Fatalf("Issue run token: %v", err)
	}
	return tok
}

// plannerChildReq pede a NHI agent:planner dentro do escopo do run.
func plannerChildReq() identity.ChildRequest {
	return identity.ChildRequest{
		AgentID: "agent:planner", AgentClass: "worker", PolicyRef: "policy://planner@1",
		Authority: []string{"cap:plan"},
	}
}

// permittingRM constrói um RM REAL cuja cadeia neutra permite e cujo tool de
// planeamento está registado (default-deny satisfeito). O Permit é GENUÍNO — o
// stub externo não conseguiria forjar um.
func permittingRM(t *testing.T) *rm.Monitor {
	t.Helper()
	m := rm.New()
	if err := m.Register("agent.plan", func(context.Context, []byte) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return m
}

// denyHook nega sempre (fail-closed) — para exercitar a recusa da mediação.
type denyHook struct{}

func (denyHook) Name() string { return "test-deny" }
func (denyHook) Evaluate(context.Context, *rm.Call) (rm.HookResult, error) {
	return rm.HookResult{Decision: rm.HookDeny, Reason: "negado pelo teste"}, nil
}

func denyingRM(t *testing.T) *rm.Monitor {
	t.Helper()
	m := rm.New(rm.WithHooks(denyHook{}))
	if err := m.Register("agent.plan", func(context.Context, []byte) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return m
}

// countingDecomposer é o "LLM" injectado: conta as invocações (prova por CONTADOR
// do fail-closed) e, opcionalmente, falha as primeiras `failFirst` tentativas para
// exercitar as N tentativas. Thread-safe (-race).
type countingDecomposer struct {
	calls     atomic.Int64
	failFirst int64
	err       error
}

func (d *countingDecomposer) Decompose(_ context.Context, in planner.DecomposeInput) (plan.PlanDocument, error) {
	n := d.calls.Add(1)
	if d.err != nil {
		return plan.PlanDocument{}, d.err
	}
	if n <= d.failFirst {
		return plan.PlanDocument{}, errFakeDecompose
	}
	return validDoc(), nil
}

var errFakeDecompose = &decomposeErr{}

type decomposeErr struct{}

func (*decomposeErr) Error() string { return "decompose failed (test)" }

// validDoc devolve um PlanDocument de forma válida (passa plan.Decode).
func validDoc() plan.PlanDocument {
	return plan.PlanDocument{
		PlanVersion: plan.CurrentPlanVersion,
		Objective:   "meta-objective",
		BudgetTotal: plan.BudgetEstimate{Tokens: 1000, CostMicroUSD: 2000},
		PlannerMeta: plan.PlannerMeta{Model: "m-1", PromptVersion: "1.0.0", CapabilitiesHash: "sha256:cap"},
		Nodes: []plan.Node{
			{NodeID: "n1", Role: "worker", Objective: "do-x"},
			{NodeID: "n2", Role: "worker", Objective: "do-y", DependsOn: []string{"n1"}},
		},
	}
}

// capturingAppender captura os EventInput apensos (para asserir o TIPO do evento
// emitido) e satisfaz plannerevents.Appender. Thread-safe.
type capturingAppender struct {
	mu     atomic.Int64
	types  []string
	stream []string
}

func (a *capturingAppender) Append(_ context.Context, streamID string, in eventstore.EventInput, _ ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	a.mu.Add(1)
	a.types = append(a.types, in.Type)
	a.stream = append(a.stream, streamID)
	return eventstore.AppendResult{Seq: uint64(a.mu.Load())}, nil
}

func amt(tokens, microUSD int64) budget.Amount {
	return budget.Amount{Tokens: tokens, CostMicroUSD: microUSD}
}

// newBudget devolve um orçamento com o nó-raiz (id = runID) e o limite dado.
func newBudget(t *testing.T, runID string, root budget.Amount) *budget.Budget {
	t.Helper()
	b, err := budget.New(runID, root)
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	return b
}

const (
	runID       = "run-234"
	parentNode  = runID // o nó-raiz do budget tem id = treeID = runID
	plannerNode = "budget:planner"
)

// seedRunTrace abre um span-raiz do RUN com o mesmo tracer e devolve o seu
// traceparent W3C — o pai sob que os spans de planeamento devem abrir.
func seedRunTrace(tracer *otelgenai.RecordingTracer) (string, otelgenai.SpanContext) {
	_, runSpan := tracer.StartSpan(context.Background(), otelgenai.OpInvokeAgent)
	sc := runSpan.SpanContext()
	runSpan.End()
	return otelgenai.FormatTraceParent(sc), sc
}

// ---------------------------------------------------------------------------
// CA (fail-closed REAL): SEM reserva admitida ⇒ decomposição NÃO arranca. Prova
// por CONTADOR — o Decomposer injectado NÃO é invocado. O caminho de erro é o CAS
// REAL do budget (root minúsculo), não um mock.
//
// Falha-antes: se o Planner invocasse o Decomposer antes/independente da reserva
// (ou reservasse depois de decompor), decomposer.calls seria ≥ 1 e o teste falha.
// ---------------------------------------------------------------------------
func TestDecompose_NoBudget_FailClosed_DecomposerNeverCalled(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	// Root minúsculo: a reserva de planeamento (centenas de tokens) NÃO cabe. O
	// CAS do budget recusa fail-closed — caminho de erro REAL exercido.
	b := newBudget(t, runID, amt(1, 1))
	dec := &countingDecomposer{}

	p, err := planner.NewPlanner(b, permittingRM(t), iss, dec)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	tracer := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	p, _ = planner.NewPlanner(b, permittingRM(t), iss, dec, planner.WithTracer(tracer))

	_, err = p.Decompose(context.Background(), planner.DecomposeRequest{
		RunID: runID, ParentBudgetNode: parentNode, PlannerBudgetNode: plannerNode,
		ParentToken: runToken(t, iss).Compact, Child: plannerChildReq(),
		Context: planner.PlanningContext{Goal: "g", ContextUnits: 4},
	})

	if err == nil {
		t.Fatal("esperado erro fail-closed sem reserva admitida, got nil")
	}
	if got := dec.calls.Load(); got != 0 {
		t.Fatalf("FAIL-CLOSED VIOLADO: Decomposer invocado %d vezes sem reserva admitida (esperado 0)", got)
	}
	// A decomposição não arrancou: nenhum span chat (tentativa) foi aberto.
	if spans := tracer.SpansByOperation(otelgenai.OpChat); len(spans) != 0 {
		t.Fatalf("nenhuma tentativa devia ter aberto span chat, got %d", len(spans))
	}
}

// ---------------------------------------------------------------------------
// CA (mediação ADR-002): mediação NEGADA ⇒ decomposição NÃO arranca e o orçamento
// NÃO é tocado (nenhuma reserva). Prova por contador + headroom intacto.
// ---------------------------------------------------------------------------
func TestDecompose_MediationDenied_FailClosed(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	b := newBudget(t, runID, amt(1_000_000, 1_000_000_000))
	dec := &countingDecomposer{}

	p, err := planner.NewPlanner(b, denyingRM(t), iss, dec)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	_, err = p.Decompose(context.Background(), planner.DecomposeRequest{
		RunID: runID, ParentBudgetNode: parentNode, PlannerBudgetNode: plannerNode,
		ParentToken: runToken(t, iss).Compact, Child: plannerChildReq(),
		Context: planner.PlanningContext{Goal: "g", ContextUnits: 4},
	})
	if err == nil {
		t.Fatal("esperado erro fail-closed com mediação negada, got nil")
	}
	if got := dec.calls.Load(); got != 0 {
		t.Fatalf("FAIL-CLOSED VIOLADO: Decomposer invocado %d vezes com mediação negada (esperado 0)", got)
	}
	// Orçamento intacto: a mediação nega ANTES de qualquer reserva.
	avail, aerr := b.Available(parentNode)
	if aerr != nil {
		t.Fatalf("Available: %v", aerr)
	}
	if avail.Tokens != 1_000_000 {
		t.Fatalf("mediação negada não devia reservar orçamento; headroom=%d (esperado 1000000)", avail.Tokens)
	}
}

// ---------------------------------------------------------------------------
// CA (observabilidade + admissão): a decomposição admitida emite spans FILHOS do
// traceparent do run — span-âncora invoke_agent + N spans chat (tentativas) + gate —
// reserva o orçamento, emite plan.planner_admitted e produz o PlanDocument.
//
// Falha-antes: se os spans não parenteassem o run (trace_id distinto) ou se o
// número de tentativas não batesse com N, os asserts de topologia/contagem falham;
// se o evento emitido não fosse plan.planner_admitted, o assert do tipo falha.
// ---------------------------------------------------------------------------
func TestDecompose_Admitted_SpansChildrenOfRun_AndEvent(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	b := newBudget(t, runID, amt(1_000_000, 1_000_000_000))
	dec := &countingDecomposer{}
	tracer := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	app := &capturingAppender{}
	rec, err := plannerevents.NewRecorder(app)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	p, err := planner.NewPlanner(b, permittingRM(t), iss, dec,
		planner.WithTracer(tracer), planner.WithAdmissionEmitter(rec), planner.WithMaxAttempts(3))
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}

	runTP, runSC := seedRunTrace(tracer)

	res, err := p.Decompose(context.Background(), planner.DecomposeRequest{
		RunID: runID, PlanID: runID, ParentBudgetNode: parentNode, PlannerBudgetNode: plannerNode,
		ParentToken: runToken(t, iss).Compact, Child: plannerChildReq(),
		ParentTraceParent: runTP,
		Context:           planner.PlanningContext{Goal: "decompose me", ContextUnits: 8, CapabilitiesHash: "sha256:snap"},
	})
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if res.PlannerNHI != "agent:planner" {
		t.Fatalf("PlannerNHI=%q, esperado agent:planner", res.PlannerNHI)
	}
	if res.Attempts != 1 {
		t.Fatalf("Attempts=%d, esperado 1 (sucesso à primeira)", res.Attempts)
	}
	if dec.calls.Load() != 1 {
		t.Fatalf("Decomposer invocado %d vezes, esperado 1", dec.calls.Load())
	}

	// plan.planner_admitted emitido — o TIPO tem de ser a constante do plannerevents.
	if len(app.types) != 1 {
		t.Fatalf("esperado 1 evento emitido, got %d (%v)", len(app.types), app.types)
	}
	if app.types[0] != plannerevents.EventPlannerAdmitted {
		t.Fatalf("tipo de evento=%q, esperado %q", app.types[0], plannerevents.EventPlannerAdmitted)
	}
	if app.stream[0] != runID {
		t.Fatalf("stream do evento=%q, esperado %q", app.stream[0], runID)
	}

	// Spans FILHOS do run: o span-âncora invoke_agent partilha o trace_id do run e
	// parenteia o span do run.
	anchors := tracer.SpansByOperation(otelgenai.OpInvokeAgent)
	// [0] é o próprio span-raiz do run (seedRunTrace); o do planeador é o seguinte.
	var anchor *otelgenai.RecordedSpan
	for _, s := range anchors {
		if s.ParentSpanID == runSC.SpanID {
			anchor = s
			break
		}
	}
	if anchor == nil {
		t.Fatal("span-âncora do planeador não encontrado como filho do span do run")
	}
	if anchor.SpanContext.TraceID != runSC.TraceID {
		t.Fatalf("span-âncora do planeador num trace distinto do run (%x vs %x)", anchor.SpanContext.TraceID, runSC.TraceID)
	}

	// Uma tentativa ⇒ um span chat, filho do span-âncora, com custo contabilizado.
	chats := tracer.SpansByOperation(otelgenai.OpChat)
	if len(chats) != 1 {
		t.Fatalf("esperado 1 span chat (1 tentativa), got %d", len(chats))
	}
	if chats[0].ParentSpanID != anchor.SpanContext.SpanID {
		t.Fatal("span da tentativa não é filho do span-âncora do planeador")
	}
	if chats[0].SpanContext.TraceID != runSC.TraceID {
		t.Fatal("span da tentativa num trace distinto do run")
	}
	if _, ok := chats[0].Attributes[otelgenai.AttrCostUSD]; !ok {
		t.Fatal("span da tentativa sem custo (AttrCostUSD): o planeamento seria um ponto cego")
	}
	if v, ok := chats[0].Attributes[otelgenai.AttrInputTokens]; !ok || v.(int64) <= 0 {
		t.Fatalf("span da tentativa sem tokens contabilizados (AttrInputTokens=%v)", v)
	}

	// Gate presente no trace E FILHO do span-âncora (parentagem ao run por
	// transitividade — não apenas a contagem). Falha-antes: um gate aberto sobre um
	// ctx sem o SpanContext do planeador (ex.: `ctx` cru em vez de `planCtx`) teria
	// ParentSpanID != anchor e este assert falharia.
	gates := tracer.SpansByOperation("planner.gate")
	if len(gates) != 1 {
		t.Fatalf("esperado 1 span de gate, got %d", len(gates))
	}
	if gates[0].ParentSpanID != anchor.SpanContext.SpanID {
		t.Fatal("span do gate não é filho do span-âncora do planeador (não parenteia o run)")
	}
	if gates[0].SpanContext.TraceID != runSC.TraceID {
		t.Fatal("span do gate num trace distinto do run")
	}

	// CA-1 (cadeia de delegação hash-linked): não basta o AgentID — a cadeia embutida
	// no token emitido tem de PRESERVAR A RAIZ HUMANA e ENCADEAR o run até agent:planner.
	// Falha-antes: se IssueChild não estendesse a cadeia do run (ou emitisse uma NHI
	// órfã), a raiz não seria human:alice ou a folha não seria agent:planner.
	chain := res.PlannerToken.Claims.DelegationChain
	if len(chain) < 2 {
		t.Fatalf("cadeia de delegação do planeador demasiado curta (%d elos); esperado raiz humana → run → agent:planner", len(chain))
	}
	root, ok := chain.Root()
	if !ok || root.Sub != "human:alice" {
		t.Fatalf("raiz da cadeia de delegação=%q, esperado human:alice (a raiz humana tem de sobreviver à emissão on-behalf-of)", root.Sub)
	}
	leaf, ok := chain.Leaf()
	if !ok || leaf.ActAs != "agent:planner" {
		t.Fatalf("folha da cadeia de delegação=%q, esperado agent:planner", leaf.ActAs)
	}
	// O run (agt-run) tem de estar na cadeia entre a raiz humana e o planeador: é o
	// on-behalf-of genuíno, não uma emissão directa do humano.
	if leaf.Sub != "agt-run" {
		t.Fatalf("o elo folha delega de %q; esperado agt-run (a NHI do planeador é filha do run, não do humano)", leaf.Sub)
	}

	// DoD ("planeamento custa tokens contabilizados na árvore"): APÓS o Commit, o
	// headroom do run está REDUZIDO exactamente pela reserva admitida — a contabilidade
	// não é só um span, cascateou por CAS até à raiz. Falha-antes: se o Commit não
	// consolidasse (ou reservasse a zero), o headroom ficaria intacto e isto falharia.
	avail, aerr := b.Available(parentNode)
	if aerr != nil {
		t.Fatalf("Available: %v", aerr)
	}
	if avail.Tokens != 1_000_000-res.Reserved.Tokens {
		t.Fatalf("headroom do run pós-Commit=%d, esperado %d (1000000 − reservado %d)", avail.Tokens, 1_000_000-res.Reserved.Tokens, res.Reserved.Tokens)
	}
	if res.Reserved.Tokens <= 0 {
		t.Fatalf("reserva consolidada não-positiva (%d): o planeamento não custou tokens", res.Reserved.Tokens)
	}
}

// ---------------------------------------------------------------------------
// CA (N tentativas): com falhas transitórias, abrem-se N spans de tentativa (uma
// por tentativa até ao sucesso), todos filhos do span-âncora e no trace do run.
//
// Falha-antes: um loop que não reabrisse span por tentativa (ou parasse à
// primeira) produziria contagem != N.
// ---------------------------------------------------------------------------
func TestDecompose_NAttempts_EmitOneSpanEach(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	b := newBudget(t, runID, amt(1_000_000, 1_000_000_000))
	dec := &countingDecomposer{failFirst: 2} // falha 2, sucede à 3ª
	tracer := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})

	p, err := planner.NewPlanner(b, permittingRM(t), iss, dec,
		planner.WithTracer(tracer), planner.WithMaxAttempts(3))
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	runTP, runSC := seedRunTrace(tracer)

	res, err := p.Decompose(context.Background(), planner.DecomposeRequest{
		RunID: runID, ParentBudgetNode: parentNode, PlannerBudgetNode: plannerNode,
		ParentToken: runToken(t, iss).Compact, Child: plannerChildReq(),
		ParentTraceParent: runTP,
		Context:           planner.PlanningContext{Goal: "g", ContextUnits: 2},
	})
	if err != nil {
		t.Fatalf("Decompose: %v", err)
	}
	if res.Attempts != 3 {
		t.Fatalf("Attempts=%d, esperado 3", res.Attempts)
	}
	chats := tracer.SpansByOperation(otelgenai.OpChat)
	if len(chats) != 3 {
		t.Fatalf("esperado 3 spans de tentativa (N=3), got %d", len(chats))
	}
	for i, s := range chats {
		if s.SpanContext.TraceID != runSC.TraceID {
			t.Fatalf("span da tentativa %d num trace distinto do run", i+1)
		}
	}
}

// ---------------------------------------------------------------------------
// Determinismo do modelo de custo: mesmo contexto ⇒ mesma estimativa (função pura).
// ---------------------------------------------------------------------------
func TestCostModel_Deterministic(t *testing.T) {
	t.Parallel()
	m := planner.DefaultCostModel
	pc := planner.PlanningContext{Goal: "x", ContextUnits: 7}
	a := m.PerAttempt(pc)
	bb := m.PerAttempt(pc)
	if a != bb {
		t.Fatalf("estimativa não-determinística: %v vs %v", a, bb)
	}
	if a.Tokens <= 0 || a.CostMicroUSD <= 0 {
		t.Fatalf("estimativa devia ser positiva, got %v", a)
	}
	// Contexto maior ⇒ custo ≥ (monotonia da tabela).
	bigger := m.PerAttempt(planner.PlanningContext{ContextUnits: 100})
	if bigger.Tokens < a.Tokens {
		t.Fatalf("custo não monótono no contexto: %d < %d", bigger.Tokens, a.Tokens)
	}
}

// ---------------------------------------------------------------------------
// Deps obrigatórias em falta ⇒ fail-closed na construção.
// ---------------------------------------------------------------------------
func TestNewPlanner_MissingDeps(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	b := newBudget(t, runID, amt(10, 10))
	if _, err := planner.NewPlanner(nil, permittingRM(t), iss, &countingDecomposer{}); err == nil {
		t.Fatal("reserver nil devia falhar")
	}
	if _, err := planner.NewPlanner(b, nil, iss, &countingDecomposer{}); err == nil {
		t.Fatal("mediator nil devia falhar")
	}
	if _, err := planner.NewPlanner(b, permittingRM(t), nil, &countingDecomposer{}); err == nil {
		t.Fatal("issuer nil devia falhar")
	}
	if _, err := planner.NewPlanner(b, permittingRM(t), iss, nil); err == nil {
		t.Fatal("decomposer nil devia falhar")
	}
}

// ===========================================================================
// Ramos FAIL-CLOSED de erro (antes a 0 execuções): cada teste exercita um
// caminho de erro REAL e prova que a reserva de planeamento é LIBERTADA (sem
// leak de orçamento) ou nunca tocada. Todos são falsificáveis.
// ===========================================================================

// malformedDecomposer devolve, SEM erro, um PlanDocument de forma inválida (zero
// value: plan_version {0,0,0} + 0 nós) — passa o loop de tentativas mas o gate de
// forma (plan.Decode) TEM de o rejeitar.
type malformedDecomposer struct{ calls atomic.Int64 }

func (d *malformedDecomposer) Decompose(context.Context, planner.DecomposeInput) (plan.PlanDocument, error) {
	d.calls.Add(1)
	return plan.PlanDocument{}, nil // forma inválida, mas sem erro
}

// errIssuer falha SEMPRE a emissão da NHI agent:planner (exercita o Release em :4).
type errIssuer struct{}

func (errIssuer) IssueChild(context.Context, string, identity.ChildRequest) (identity.Token, error) {
	return identity.Token{}, errStub
}

// errEmitter falha SEMPRE a gravação durável de plan.planner_admitted (Release em :5).
type errEmitter struct{}

func (errEmitter) RecordPlannerAdmitted(context.Context, plannerevents.PlannerAdmittedPayload) (uint64, error) {
	return 0, errStub
}

// errMediator devolve um ERRO de mediação (não uma decisão de negar) — exercita o
// ramo `mErr != nil` distinto do `!Permitted()`. É o seam Mediator; o RM real só
// devolve erro em ctx cancelado, que o Decompose curto-circuita à cabeça.
type errMediator struct{}

func (errMediator) Mediate(context.Context, rm.Call) (rm.Decision, error) {
	return rm.Decision{}, errStub
}

// commitFailReserver embrulha um *budget.Budget REAL mas força o Commit a falhar —
// exercita o ramo fail-closed de consolidação (:8) sem mockar a reserva CAS.
type commitFailReserver struct{ *budget.Budget }

func (commitFailReserver) Commit(context.Context, budget.Reservation) error { return errStub }

var errStub = errors.New("erro de teste (falha injectada)")

// baseReq devolve um pedido de decomposição bem-formado reutilizável.
func baseReq(t *testing.T, iss *identity.Issuer) planner.DecomposeRequest {
	t.Helper()
	return planner.DecomposeRequest{
		RunID: runID, ParentBudgetNode: parentNode, PlannerBudgetNode: plannerNode,
		ParentToken: runToken(t, iss).Compact, Child: plannerChildReq(),
		Context: planner.PlanningContext{Goal: "g", ContextUnits: 4},
	}
}

// fullRoot é um root de orçamento generoso (a reserva cabe folgadamente).
func fullRoot(t *testing.T) *budget.Budget { return newBudget(t, runID, amt(1_000_000, 1_000_000_000)) }

// assertHeadroomRestored confirma que NENHUM orçamento ficou reservado (a reserva
// foi libertada) — o headroom do run está de volta ao limite total.
func assertHeadroomRestored(t *testing.T, b *budget.Budget, wantTokens int64) {
	t.Helper()
	avail, err := b.Available(parentNode)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if avail.Tokens != wantTokens {
		t.Fatalf("LEAK DE ORÇAMENTO: headroom=%d, esperado %d (a reserva devia ter sido libertada)", avail.Tokens, wantTokens)
	}
}

// Gate rejeita forma inválida ⇒ ErrGate + Release (planner.go:445-450).
// Falha-antes: se o gate admitisse um doc malformado (ou não libertasse a reserva),
// o erro não seria ErrGate ou o headroom ficaria consumido.
func TestDecompose_GateRejectsMalformed_FailClosed_ReleasesReserve(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	b := fullRoot(t)
	dec := &malformedDecomposer{}
	p, err := planner.NewPlanner(b, permittingRM(t), iss, dec)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	_, err = p.Decompose(context.Background(), baseReq(t, iss))
	if !errors.Is(err, planner.ErrGate) {
		t.Fatalf("esperado ErrGate para doc malformado, got %v", err)
	}
	if dec.calls.Load() == 0 {
		t.Fatal("o decomposer devia ter corrido (o gate é a jusante), got 0 chamadas")
	}
	assertHeadroomRestored(t, b, 1_000_000)
}

// Todas as N tentativas falham ⇒ ErrDecomposition + Release (planner.go:436-440).
// Prova por contador que TODAS as N tentativas correram (não só a primeira).
// Falha-antes: um loop que parasse cedo daria calls<N; um caminho sem Release
// deixaria o headroom consumido.
func TestDecompose_AllAttemptsFail_ErrDecomposition_ReleasesReserve(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	b := fullRoot(t)
	dec := &countingDecomposer{err: errFakeDecompose} // falha SEMPRE
	p, err := planner.NewPlanner(b, permittingRM(t), iss, dec, planner.WithMaxAttempts(3))
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	_, err = p.Decompose(context.Background(), baseReq(t, iss))
	if !errors.Is(err, planner.ErrDecomposition) {
		t.Fatalf("esperado ErrDecomposition, got %v", err)
	}
	if got := dec.calls.Load(); got != 3 {
		t.Fatalf("esperado 3 tentativas (N=3) todas falhadas, got %d", got)
	}
	assertHeadroomRestored(t, b, 1_000_000)
}

// Falha de emissão da NHI ⇒ ErrIssueIdentity + Release (planner.go:386-389).
// A reserva já foi admitida (Reserve corre antes da emissão) — TEM de ser libertada.
func TestDecompose_IdentityIssueFails_ReleasesReserve(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	b := fullRoot(t)
	dec := &countingDecomposer{}
	p, err := planner.NewPlanner(b, permittingRM(t), errIssuer{}, dec)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	_, err = p.Decompose(context.Background(), baseReq(t, iss))
	if !errors.Is(err, planner.ErrIssueIdentity) {
		t.Fatalf("esperado ErrIssueIdentity, got %v", err)
	}
	if dec.calls.Load() != 0 {
		t.Fatalf("decomposição não devia arrancar sem identidade, got %d chamadas", dec.calls.Load())
	}
	assertHeadroomRestored(t, b, 1_000_000)
}

// Falha da gravação durável de plan.planner_admitted ⇒ fail-closed + Release
// (planner.go:401-406). Um planeamento cuja admissão não pode ser registada NÃO
// prossegue às cegas.
func TestDecompose_EmitterFails_ReleasesReserve(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	b := fullRoot(t)
	dec := &countingDecomposer{}
	p, err := planner.NewPlanner(b, permittingRM(t), iss, dec, planner.WithAdmissionEmitter(errEmitter{}))
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	_, err = p.Decompose(context.Background(), baseReq(t, iss))
	if err == nil {
		t.Fatal("esperado erro quando a gravação durável falha, got nil")
	}
	if dec.calls.Load() != 0 {
		t.Fatalf("decomposição não devia arrancar sem facto durável, got %d chamadas", dec.calls.Load())
	}
	assertHeadroomRestored(t, b, 1_000_000)
}

// Ramo mErr != nil da mediação ⇒ ErrMediationDenied, SEM tocar no orçamento
// (planner.go:366-368). Distinto do !Permitted() (já coberto pelo RM real).
func TestDecompose_MediationError_FailClosed(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	b := fullRoot(t)
	dec := &countingDecomposer{}
	p, err := planner.NewPlanner(b, errMediator{}, iss, dec)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	_, err = p.Decompose(context.Background(), baseReq(t, iss))
	if !errors.Is(err, planner.ErrMediationDenied) {
		t.Fatalf("esperado ErrMediationDenied (erro de mediação), got %v", err)
	}
	if dec.calls.Load() != 0 {
		t.Fatalf("decomposição não devia arrancar com erro de mediação, got %d chamadas", dec.calls.Load())
	}
	// Mediação corre ANTES de qualquer reserva: o orçamento fica intacto.
	assertHeadroomRestored(t, b, 1_000_000)
}

// Commit falha ⇒ fail-closed na consolidação (planner.go:456-458). Usa um reserver
// que embrulha o budget REAL (Reserve/AddNode/Release genuínos) e só falha o Commit.
func TestDecompose_CommitFails_FailClosed(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	b := fullRoot(t)
	dec := &countingDecomposer{}
	p, err := planner.NewPlanner(commitFailReserver{b}, permittingRM(t), iss, dec)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	_, err = p.Decompose(context.Background(), baseReq(t, iss))
	if err == nil {
		t.Fatal("esperado erro fail-closed quando o Commit falha, got nil")
	}
	if dec.calls.Load() != 1 {
		t.Fatalf("a decomposição corre até ao gate antes do Commit; esperado 1 chamada, got %d", dec.calls.Load())
	}
}

// AddNode com pai inexistente ⇒ erro NÃO-ErrNodeExists é fail-closed (planner.go:376-378).
// A decomposição não arranca.
func TestDecompose_UnknownParentBudgetNode_FailClosed(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	b := fullRoot(t)
	dec := &countingDecomposer{}
	p, err := planner.NewPlanner(b, permittingRM(t), iss, dec)
	if err != nil {
		t.Fatalf("NewPlanner: %v", err)
	}
	req := baseReq(t, iss)
	req.ParentBudgetNode = "nó-de-orçamento-inexistente" // AddNode ⇒ ErrUnknownParent
	_, err = p.Decompose(context.Background(), req)
	if err == nil {
		t.Fatal("esperado erro fail-closed com pai de orçamento inexistente, got nil")
	}
	if errors.Is(err, budget.ErrNodeExists) {
		t.Fatal("o erro não devia ser o ramo idempotente ErrNodeExists")
	}
	if dec.calls.Load() != 0 {
		t.Fatalf("decomposição não devia arrancar, got %d chamadas", dec.calls.Load())
	}
}

// Pedidos malformados ⇒ ErrInvalidRequest, ANTES de qualquer efeito (planner.go:336-350).
// Cobre cada campo obrigatório em falta E a reserva não-positiva (modelo de custo zero).
func TestDecompose_InvalidRequest(t *testing.T) {
	t.Parallel()
	iss := newIssuer(t)
	good := baseReq(t, iss)

	mut := func(f func(r *planner.DecomposeRequest)) planner.DecomposeRequest {
		r := good
		f(&r)
		return r
	}
	cases := map[string]planner.DecomposeRequest{
		"runID vazio":         mut(func(r *planner.DecomposeRequest) { r.RunID = "" }),
		"parentNode vazio":    mut(func(r *planner.DecomposeRequest) { r.ParentBudgetNode = "" }),
		"plannerNode vazio":   mut(func(r *planner.DecomposeRequest) { r.PlannerBudgetNode = "" }),
		"parentToken vazio":   mut(func(r *planner.DecomposeRequest) { r.ParentToken = "" }),
		"child agentID vazio": mut(func(r *planner.DecomposeRequest) { r.Child.AgentID = "" }),
	}
	for name, req := range cases {
		req := req
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dec := &countingDecomposer{}
			p, err := planner.NewPlanner(fullRoot(t), permittingRM(t), iss, dec)
			if err != nil {
				t.Fatalf("NewPlanner: %v", err)
			}
			_, err = p.Decompose(context.Background(), req)
			if !errors.Is(err, planner.ErrInvalidRequest) {
				t.Fatalf("esperado ErrInvalidRequest, got %v", err)
			}
			if dec.calls.Load() != 0 {
				t.Fatalf("nenhum efeito devia ocorrer, got %d chamadas", dec.calls.Load())
			}
		})
	}

	// Reserva não-positiva: um modelo de custo tudo-a-zero + contexto 0 ⇒ reserva {0,0}
	// ⇒ ErrInvalidRequest (uma reserva nula não exercita o fail-closed do orçamento).
	t.Run("reserva não-positiva", func(t *testing.T) {
		t.Parallel()
		dec := &countingDecomposer{}
		zero := planner.CostModel{Version: "zero"}
		p, err := planner.NewPlanner(fullRoot(t), permittingRM(t), iss, dec, planner.WithCostModel(zero))
		if err != nil {
			t.Fatalf("NewPlanner: %v", err)
		}
		req := baseReq(t, iss)
		req.Context = planner.PlanningContext{Goal: "g", ContextUnits: 0}
		_, err = p.Decompose(context.Background(), req)
		if !errors.Is(err, planner.ErrInvalidRequest) {
			t.Fatalf("esperado ErrInvalidRequest para reserva não-positiva, got %v", err)
		}
		if dec.calls.Load() != 0 {
			t.Fatalf("nenhum efeito devia ocorrer, got %d chamadas", dec.calls.Load())
		}
	})
}
