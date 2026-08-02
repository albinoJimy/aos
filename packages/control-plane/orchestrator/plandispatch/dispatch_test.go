package plandispatch

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// ---------------------------------------------------------------------------
// Fakes das portas (falsificáveis: capturam CONTAGENS e efeitos observáveis).
// ---------------------------------------------------------------------------

// fakeGate — gate observável. materialized controla a jusante do gate.
type fakeGate struct {
	materialized bool
	err          error
	calls        int32
}

func (g *fakeGate) Materialized(_ context.Context, _ string) (bool, error) {
	atomic.AddInt32(&g.calls, 1)
	return g.materialized, g.err
}

// fakeLifecycle — vista de estado, LIDA (nunca escrita) por este pacote.
type fakeLifecycle struct {
	mu     sync.Mutex
	states map[string]NodeState
	err    error
}

func newLifecycle(states map[string]NodeState) *fakeLifecycle {
	return &fakeLifecycle{states: states}
}

func (l *fakeLifecycle) State(_ context.Context, _, nodeID string) (NodeState, error) {
	if l.err != nil {
		return NodeUnknown, l.err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if st, ok := l.states[nodeID]; ok {
		return st, nil
	}
	return NodePending, nil
}

// countingHeadroom — headroom TOCTOU-safe: Available() é o snapshot ADVISORY
// (advertised), mas Acquire() só concede até um limite REAL e vivo (live). É a
// divergência advertised>live que falsifica a re-verificação TOCTOU: um despacho que
// confiasse em Available oversubscreveria.
type countingHeadroom struct {
	advertised   int   // o que Available() anuncia (pode estar obsoleto)
	live         int64 // slots REAIS restantes que Acquire concede
	acquireCalls int32 // nº de tentativas de Acquire
	granted      int32 // nº de Acquire concedidos
	released     int32 // nº de Release
	acquireErr   error // se != nil, Acquire falha (fail-closed)
	availErr     error // se != nil, Available falha
}

func (h *countingHeadroom) Available(_ context.Context) (int, error) {
	if h.availErr != nil {
		return 0, h.availErr
	}
	return h.advertised, nil
}

func (h *countingHeadroom) Acquire(_ context.Context) (bool, error) {
	atomic.AddInt32(&h.acquireCalls, 1)
	if h.acquireErr != nil {
		return false, h.acquireErr
	}
	// Decremento atómico: concede só enquanto houver slot VIVO.
	for {
		cur := atomic.LoadInt64(&h.live)
		if cur <= 0 {
			return false, nil
		}
		if atomic.CompareAndSwapInt64(&h.live, cur, cur-1) {
			atomic.AddInt32(&h.granted, 1)
			return true, nil
		}
	}
}

func (h *countingHeadroom) Release(_ context.Context) error {
	atomic.AddInt32(&h.released, 1)
	atomic.AddInt64(&h.live, 1)
	return nil
}

// fakeCards — cartões (danger/capability). cleared por nó; default fail-closed=false.
type fakeCards struct {
	cleared map[string]bool
	err     error
	calls   int32
}

func (c *fakeCards) Cleared(_ context.Context, _, nodeID string) (bool, error) {
	atomic.AddInt32(&c.calls, 1)
	if c.err != nil {
		return false, c.err
	}
	return c.cleared[nodeID], nil
}

// fakeSink — captura os nós despachados (ordem e id).
type fakeSink struct {
	mu         sync.Mutex
	dispatched []string
	failOn     map[string]error
}

func (s *fakeSink) Dispatch(_ context.Context, node Node) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.failOn[node.NodeID]; ok {
		return err
	}
	s.dispatched = append(s.dispatched, node.NodeID)
	return nil
}

func (s *fakeSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.dispatched)
}

// harness monta um Dispatcher com fakes injectáveis.
type harness struct {
	gate *fakeGate
	life *fakeLifecycle
	head *countingHeadroom
	card *fakeCards
	sink *fakeSink
	d    *Dispatcher
}

func newHarness(t *testing.T, gate *fakeGate, life *fakeLifecycle, head *countingHeadroom, card *fakeCards, sink *fakeSink) *harness {
	t.Helper()
	d, err := NewDispatcher(gate, life, head, card, sink)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return &harness{gate: gate, life: life, head: head, card: card, sink: sink, d: d}
}

func outcomeOf(res Result, nodeID string) Outcome {
	for _, r := range res.Results {
		if r.NodeID == nodeID {
			return r.Outcome
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// TESTE 1 — TOCTOU: um plano aprovado que já NÃO cabe fica em espera de headroom.
// Falha-antes: sem a re-verificação atómica (confiando em Available), oversubscrevia.
// ---------------------------------------------------------------------------

func TestDispatch_TOCTOU_DoesNotOversubscribe(t *testing.T) {
	// Três nós independentes, todos elegíveis. O headroom ANUNCIA 3 (Available), mas o
	// headroom VIVO só concede 1 (encolheu entre o snapshot e o spawn). A implementação
	// correcta despacha 1 e adia 2 — nunca 3.
	nodes := []Node{{NodeID: "a"}, {NodeID: "b"}, {NodeID: "c"}}
	head := &countingHeadroom{advertised: 3, live: 1}
	h := newHarness(t,
		&fakeGate{materialized: true},
		newLifecycle(map[string]NodeState{"a": NodePending, "b": NodePending, "c": NodePending}),
		head,
		&fakeCards{cleared: map[string]bool{}},
		&fakeSink{failOn: map[string]error{}},
	)

	res, err := h.d.Dispatch(context.Background(), Plan{PlanID: "p1", Nodes: nodes})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res.Dispatched != 1 {
		t.Fatalf("despachados = %d, quer 1 (oversubscrição: o headroom vivo só tinha 1 slot)", res.Dispatched)
	}
	if h.sink.count() != 1 {
		t.Fatalf("sink recebeu %d nós, quer 1 — OVERSUBSCRIÇÃO (a re-verificação TOCTOU falhou)", h.sink.count())
	}
	if res.Deferred != 2 {
		t.Fatalf("diferidos = %d, quer 2", res.Deferred)
	}
	// Nunca se concederam mais slots do que os vivos.
	if got := atomic.LoadInt32(&head.granted); got != 1 {
		t.Fatalf("headroom concedeu %d slots, quer 1 (nunca oversubscrever)", got)
	}
	// Determinismo: 'a' (primeiro por ordem canónica) é o despachado.
	if outcomeOf(res, "a") != OutcomeDispatched {
		t.Errorf("nó 'a' outcome = %q, quer dispatched", outcomeOf(res, "a"))
	}
	if outcomeOf(res, "b") != OutcomeDeferredHeadroom || outcomeOf(res, "c") != OutcomeDeferredHeadroom {
		t.Errorf("b/c deviam estar deferred_headroom, ficaram %q/%q", outcomeOf(res, "b"), outcomeOf(res, "c"))
	}
}

// ---------------------------------------------------------------------------
// TESTE 2 — nenhum despacho antes do gate. Falha-antes: sem a checagem de gate,
// despacharia com o plano ainda não materializado (a montante da aprovação).
// ---------------------------------------------------------------------------

func TestDispatch_NoDispatchBeforeGate(t *testing.T) {
	nodes := []Node{{NodeID: "a"}, {NodeID: "b"}}
	head := &countingHeadroom{advertised: 10, live: 10}
	h := newHarness(t,
		&fakeGate{materialized: false}, // gate NÃO passou
		newLifecycle(map[string]NodeState{"a": NodePending, "b": NodePending}),
		head,
		&fakeCards{cleared: map[string]bool{}},
		&fakeSink{failOn: map[string]error{}},
	)

	res, err := h.d.Dispatch(context.Background(), Plan{PlanID: "p1", Nodes: nodes})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res.Dispatched != 0 || h.sink.count() != 0 {
		t.Fatalf("despachou %d nós antes do gate — a jusante do gate violada", h.sink.count())
	}
	if res.Materialized {
		t.Error("Result.Materialized deveria ser false")
	}
	// Espera no gate NÃO consome headroom.
	if got := atomic.LoadInt32(&head.acquireCalls); got != 0 {
		t.Fatalf("Acquire chamado %d vezes antes do gate — a espera de gate NÃO pode consumir headroom", got)
	}
	for _, n := range nodes {
		if outcomeOf(res, n.NodeID) != OutcomeWaitingGate {
			t.Errorf("nó %q outcome = %q, quer waiting_gate", n.NodeID, outcomeOf(res, n.NodeID))
		}
	}
}

// ---------------------------------------------------------------------------
// TESTE 3 — nó com depends_on por satisfazer NÃO despacha (e não consome headroom).
// Falha-antes: sem a checagem de deps, 'b' despacharia antes de 'a' concluir.
// ---------------------------------------------------------------------------

func TestDispatch_UnsatisfiedDependencyBlocks(t *testing.T) {
	// b depends_on a. a ainda a correr (não concluído) ⇒ b espera; a não é pendente.
	nodes := []Node{
		{NodeID: "a"},
		{NodeID: "b", DependsOn: []string{"a"}},
	}
	head := &countingHeadroom{advertised: 10, live: 10}
	h := newHarness(t,
		&fakeGate{materialized: true},
		newLifecycle(map[string]NodeState{"a": NodeRunning, "b": NodePending}),
		head,
		&fakeCards{cleared: map[string]bool{}},
		&fakeSink{failOn: map[string]error{}},
	)

	res, err := h.d.Dispatch(context.Background(), Plan{PlanID: "p1", Nodes: nodes})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if h.sink.count() != 0 {
		t.Fatalf("despachou %d nós — 'b' não devia despachar (dep 'a' não concluída), 'a' está running", h.sink.count())
	}
	if outcomeOf(res, "b") != OutcomeWaitingDeps {
		t.Errorf("nó 'b' outcome = %q, quer waiting_deps", outcomeOf(res, "b"))
	}
	if outcomeOf(res, "a") != OutcomeInflightOrDone {
		t.Errorf("nó 'a' outcome = %q, quer inflight_or_done", outcomeOf(res, "a"))
	}
	// Nenhum elegível ⇒ zero Acquire.
	if got := atomic.LoadInt32(&head.acquireCalls); got != 0 {
		t.Fatalf("Acquire chamado %d vezes — nenhum nó elegível deveria tocar no headroom", got)
	}

	// Agora 'a' concluída: 'b' torna-se despachável.
	h.life.mu.Lock()
	h.life.states["a"] = NodeComplete
	h.life.mu.Unlock()
	res2, err := h.d.Dispatch(context.Background(), Plan{PlanID: "p1", Nodes: nodes})
	if err != nil {
		t.Fatalf("Dispatch (2): %v", err)
	}
	if outcomeOf(res2, "b") != OutcomeDispatched {
		t.Fatalf("nó 'b' outcome = %q após 'a' concluir, quer dispatched", outcomeOf(res2, "b"))
	}
	if res2.Dispatched != 1 {
		t.Fatalf("despachados (2) = %d, quer 1", res2.Dispatched)
	}
}

// ---------------------------------------------------------------------------
// TESTE 4 — espera no cartão (danger/capability) NÃO consome headroom.
// Falha-antes: se a checagem de cartão fosse feita DEPOIS do Acquire, um nó bloqueado
// consumiria (e possivelmente vazaria) um slot de concorrência.
// ---------------------------------------------------------------------------

func TestDispatch_WaitingOnCardDoesNotConsumeHeadroom(t *testing.T) {
	// 'g' exige cartão e NÃO está resolvido ⇒ espera. 's' é livre ⇒ despacha.
	nodes := []Node{
		{NodeID: "g", RequiresCard: true},
		{NodeID: "s"},
	}
	head := &countingHeadroom{advertised: 10, live: 10}
	h := newHarness(t,
		&fakeGate{materialized: true},
		newLifecycle(map[string]NodeState{"g": NodePending, "s": NodePending}),
		head,
		&fakeCards{cleared: map[string]bool{"g": false}}, // gap por resolver
		&fakeSink{failOn: map[string]error{}},
	)

	res, err := h.d.Dispatch(context.Background(), Plan{PlanID: "p1", Nodes: nodes})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if outcomeOf(res, "g") != OutcomeWaitingCard {
		t.Errorf("nó 'g' outcome = %q, quer waiting_card", outcomeOf(res, "g"))
	}
	if outcomeOf(res, "s") != OutcomeDispatched {
		t.Errorf("nó 's' outcome = %q, quer dispatched", outcomeOf(res, "s"))
	}
	// Exactamente 1 Acquire (só 's'); 'g' esperou no cartão SEM tocar no headroom.
	if got := atomic.LoadInt32(&head.acquireCalls); got != 1 {
		t.Fatalf("Acquire chamado %d vezes, quer 1 — a espera no cartão NÃO pode consumir headroom", got)
	}
	if got := atomic.LoadInt32(&head.granted); got != 1 {
		t.Fatalf("slots concedidos = %d, quer 1", got)
	}

	// Resolver o cartão de 'g' torna-o despachável na passagem seguinte.
	h.card.cleared["g"] = true
	h.life.mu.Lock()
	h.life.states["s"] = NodeComplete // 's' já concluído; não re-despacha
	h.life.mu.Unlock()
	res2, err := h.d.Dispatch(context.Background(), Plan{PlanID: "p1", Nodes: nodes})
	if err != nil {
		t.Fatalf("Dispatch (2): %v", err)
	}
	if outcomeOf(res2, "g") != OutcomeDispatched {
		t.Fatalf("nó 'g' outcome = %q após resolver cartão, quer dispatched", outcomeOf(res2, "g"))
	}
}

// ---------------------------------------------------------------------------
// TESTE 5 — falha do sink DEVOLVE o slot (sem leak) e é SURFACED (nunca silenciosa).
// ---------------------------------------------------------------------------

func TestDispatch_SinkFailureReleasesSlotAndSurfaces(t *testing.T) {
	nodes := []Node{{NodeID: "a"}}
	head := &countingHeadroom{advertised: 1, live: 1}
	sinkErr := errors.New("spawn recusado pelo substrato")
	h := newHarness(t,
		&fakeGate{materialized: true},
		newLifecycle(map[string]NodeState{"a": NodePending}),
		head,
		&fakeCards{cleared: map[string]bool{}},
		&fakeSink{failOn: map[string]error{"a": sinkErr}},
	)

	res, err := h.d.Dispatch(context.Background(), Plan{PlanID: "p1", Nodes: nodes})
	if err == nil {
		t.Fatal("Dispatch devia propagar o erro do sink (nunca spawn parcial silencioso)")
	}
	if !errors.Is(err, ErrDispatchSink) {
		t.Fatalf("erro = %v, quer envolver ErrDispatchSink", err)
	}
	if res.Dispatched != 0 {
		t.Errorf("despachados = %d, quer 0 (o sink falhou)", res.Dispatched)
	}
	// Slot devolvido: Acquire==1, Release==1, live volta a 1 (sem leak).
	if got := atomic.LoadInt32(&head.granted); got != 1 {
		t.Errorf("granted = %d, quer 1", got)
	}
	if got := atomic.LoadInt32(&head.released); got != 1 {
		t.Fatalf("released = %d, quer 1 — o slot foi VAZADO em falha do sink", got)
	}
	if got := atomic.LoadInt64(&head.live); got != 1 {
		t.Fatalf("live = %d, quer 1 (slot devolvido)", got)
	}
}

// ---------------------------------------------------------------------------
// TESTE 6 — fail-closed em erros de porta e construção.
// ---------------------------------------------------------------------------

func TestNewDispatcher_FailClosedOnMissingPorts(t *testing.T) {
	gate := &fakeGate{materialized: true}
	life := newLifecycle(nil)
	head := &countingHeadroom{}
	card := &fakeCards{}
	sink := &fakeSink{}
	if _, err := NewDispatcher(nil, life, head, card, sink); !errors.Is(err, ErrDeps) {
		t.Errorf("gate nil: err = %v, quer ErrDeps", err)
	}
	if _, err := NewDispatcher(gate, nil, head, card, sink); !errors.Is(err, ErrDeps) {
		t.Errorf("lifecycle nil: err = %v, quer ErrDeps", err)
	}
	if _, err := NewDispatcher(gate, life, nil, card, sink); !errors.Is(err, ErrDeps) {
		t.Errorf("headroom nil: err = %v, quer ErrDeps", err)
	}
	if _, err := NewDispatcher(gate, life, head, nil, sink); !errors.Is(err, ErrDeps) {
		t.Errorf("cards nil: err = %v, quer ErrDeps", err)
	}
	if _, err := NewDispatcher(gate, life, head, card, nil); !errors.Is(err, ErrDeps) {
		t.Errorf("sink nil: err = %v, quer ErrDeps", err)
	}
}

func TestDispatch_GateErrorIsFailClosed(t *testing.T) {
	gateErr := errors.New("gate indisponível")
	head := &countingHeadroom{advertised: 5, live: 5}
	h := newHarness(t,
		&fakeGate{err: gateErr},
		newLifecycle(map[string]NodeState{"a": NodePending}),
		head,
		&fakeCards{},
		&fakeSink{failOn: map[string]error{}},
	)
	_, err := h.d.Dispatch(context.Background(), Plan{PlanID: "p1", Nodes: []Node{{NodeID: "a"}}})
	if err == nil {
		t.Fatal("erro do gate devia propagar (fail-closed)")
	}
	if got := atomic.LoadInt32(&head.acquireCalls); got != 0 {
		t.Fatalf("Acquire chamado %d vezes apesar do erro de gate — fail-closed violado", got)
	}
}

// ---------------------------------------------------------------------------
// TESTE 7 — PlanFrom: o conjunto despachável é EXACTAMENTE o materializado; deps e
// risco vêm do documento aprovado; danger exige cartão.
// ---------------------------------------------------------------------------

func TestPlanFrom_ProjectsMaterializedSetWithDepsAndRisk(t *testing.T) {
	doc := plan.PlanDocument{
		Nodes: []plan.Node{
			{NodeID: "root", Role: "r", Objective: "o"},
			{NodeID: "child", Role: "r", Objective: "o", DependsOn: []string{"root"}, RiskClass: plan.RiskDanger},
		},
	}
	mat := plannerevents.MaterializedPayload{
		PlanID: "p1",
		Nodes: []plannerevents.MaterializedNode{
			{NodeID: "root", Kind: plannerevents.SpawnRole},
			{NodeID: "child", Kind: plannerevents.SpawnLeaf},
		},
	}
	// needsCard marca adicionalmente 'root' como waiting_on_capability.
	needsCard := func(id string) bool { return id == "root" }

	p, err := PlanFrom(mat, doc, needsCard)
	if err != nil {
		t.Fatalf("PlanFrom: %v", err)
	}
	if p.PlanID != "p1" || len(p.Nodes) != 2 {
		t.Fatalf("plano = %+v, quer plan_id p1 com 2 nós", p)
	}
	byID := map[string]Node{}
	for _, n := range p.Nodes {
		byID[n.NodeID] = n
	}
	if !byID["child"].RequiresCard {
		t.Error("'child' é danger — devia exigir cartão")
	}
	if !byID["root"].RequiresCard {
		t.Error("'root' foi marcado por needsCard (capability gap) — devia exigir cartão")
	}
	if len(byID["child"].DependsOn) != 1 || byID["child"].DependsOn[0] != "root" {
		t.Errorf("'child'.DependsOn = %v, quer [root]", byID["child"].DependsOn)
	}
}

func TestPlanFrom_FailClosedOnMaterializedNodeAbsentFromDoc(t *testing.T) {
	doc := plan.PlanDocument{Nodes: []plan.Node{{NodeID: "root", Role: "r", Objective: "o"}}}
	mat := plannerevents.MaterializedPayload{
		PlanID: "p1",
		Nodes:  []plannerevents.MaterializedNode{{NodeID: "ghost", Kind: plannerevents.SpawnLeaf}},
	}
	if _, err := PlanFrom(mat, doc, nil); !errors.Is(err, ErrNodeNotMaterialized) {
		t.Fatalf("err = %v, quer ErrNodeNotMaterialized (nó materializado sem correspondência no doc)", err)
	}
}

// ---------------------------------------------------------------------------
// TESTE 8 — GUARD DE FRONTEIRA ADR-018 (Handoff): o pacote de produção NÃO importa o
// módulo de ciclo de vida/kernel — só plan+plannerevents. Torna a DoD EXECUTÁVEL (não
// só documental): um futuro edit que importe kernel/agent-runtime, reference-monitor,
// scheduler ou qualquer porta de ESCRITA de ciclo de vida FAZ este teste falhar.
// Falha-antes: se dispatch.go passasse a importar, p.ex.,
// "github.com/aos-ref/kernel/agent-runtime", o allowlist rejeitava-o.
// ---------------------------------------------------------------------------

func TestBoundary_ProductionImportsAreAllowlisted(t *testing.T) {
	const modulePrefix = "github.com/aos-ref/"
	// ÚNICOS imports internos permitidos ao pacote de produção. Qualquer outro import
	// sob o módulo (em especial ciclo de vida/kernel/scheduler) quebra a fronteira.
	allowed := map[string]bool{
		"github.com/aos-ref/control-plane/orchestrator/plan":          true,
		"github.com/aos-ref/control-plane/orchestrator/plannerevents": true,
	}
	fset := token.NewFileSet()
	// Só ficheiros de PRODUÇÃO (exclui *_test.go): a fronteira aplica-se ao binário.
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse do diretório do pacote: %v", err)
	}
	sawFile := false
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			sawFile = true
			for _, imp := range file.Imports {
				p := strings.Trim(imp.Path.Value, `"`)
				if strings.HasPrefix(p, modulePrefix) && !allowed[p] {
					t.Errorf("%s importa %q — PROIBIDO: plandispatch só pode importar plan+plannerevents (fronteira ADR-018: o SCH despacha, nunca é autoridade concorrente do ciclo de vida)", filepath.Base(path), p)
				}
			}
		}
	}
	if !sawFile {
		t.Fatal("nenhum ficheiro de produção lido — o guard de imports não exerceu nada")
	}
}

// ---------------------------------------------------------------------------
// TESTE 9 — Available() é ADVISORY e NUNCA está no caminho de despacho. Prova: mesmo
// com Available a FALHAR (availErr) e a anunciar um número absurdo, o despacho respeita
// só o headroom VIVO (Acquire). Falha-antes: se Dispatch consultasse Available, o
// availErr propagaria (ou o advertised=999 oversubscreveria) — este teste falharia.
// ---------------------------------------------------------------------------

func TestDispatch_IgnoresAdvisoryAvailable(t *testing.T) {
	nodes := []Node{{NodeID: "a"}, {NodeID: "b"}}
	head := &countingHeadroom{
		advertised: 999,
		live:       1,
		availErr:   errors.New("snapshot advisory indisponível"),
	}
	h := newHarness(t,
		&fakeGate{materialized: true},
		newLifecycle(map[string]NodeState{"a": NodePending, "b": NodePending}),
		head,
		&fakeCards{cleared: map[string]bool{}},
		&fakeSink{failOn: map[string]error{}},
	)

	res, err := h.d.Dispatch(context.Background(), Plan{PlanID: "p1", Nodes: nodes})
	if err != nil {
		t.Fatalf("Dispatch propagou erro — Available NÃO devia ser consultado no despacho: %v", err)
	}
	if res.Dispatched != 1 {
		t.Fatalf("despachados = %d, quer 1 (respeitar o live=1, ignorar advertised=999)", res.Dispatched)
	}
	if res.Deferred != 1 {
		t.Fatalf("diferidos = %d, quer 1", res.Deferred)
	}
	// Acquire foi a única autoridade; Available nunca entrou no caminho.
	if got := atomic.LoadInt32(&head.granted); got != 1 {
		t.Fatalf("granted = %d, quer 1 (Acquire é a autoridade TOCTOU, não Available)", got)
	}
}

// ---------------------------------------------------------------------------
// TESTE 10 — falha do sink COMPLETA o Result: cada nó tem outcome mesmo no erro (nunca
// resultado parcial silencioso). Dois elegíveis, o primeiro falha no sink ⇒ o segundo
// (não tentado) aparece como diferido, e o total de resultados cobre TODOS os nós.
// Falha-antes: o early-return devolvia um Result sem o segundo nó (parcial).
// ---------------------------------------------------------------------------

func TestDispatch_SinkFailureCompletesResults(t *testing.T) {
	nodes := []Node{{NodeID: "a"}, {NodeID: "b"}}
	head := &countingHeadroom{advertised: 5, live: 5}
	sinkErr := errors.New("spawn recusado")
	h := newHarness(t,
		&fakeGate{materialized: true},
		newLifecycle(map[string]NodeState{"a": NodePending, "b": NodePending}),
		head,
		&fakeCards{cleared: map[string]bool{}},
		&fakeSink{failOn: map[string]error{"a": sinkErr}}, // 'a' (primeiro) falha
	)

	res, err := h.d.Dispatch(context.Background(), Plan{PlanID: "p1", Nodes: nodes})
	if !errors.Is(err, ErrDispatchSink) {
		t.Fatalf("err = %v, quer envolver ErrDispatchSink", err)
	}
	// Result COMPLETO: um outcome por nó, mesmo abortando na falha de 'a'.
	if len(res.Results) != len(nodes) {
		t.Fatalf("len(Results) = %d, quer %d — Result parcial no caminho de erro", len(res.Results), len(nodes))
	}
	if outcomeOf(res, "a") != OutcomeDeferredHeadroom {
		t.Errorf("'a' outcome = %q, quer deferred_headroom (sink recusou, slot devolvido)", outcomeOf(res, "a"))
	}
	if outcomeOf(res, "b") != OutcomeDeferredHeadroom {
		t.Errorf("'b' outcome = %q, quer deferred_headroom (não tentado após o aborto)", outcomeOf(res, "b"))
	}
	// 'b' nunca chegou ao sink (aborto antes de o tentar).
	if h.sink.count() != 0 {
		t.Fatalf("sink recebeu %d nós, quer 0 (o aborto no primeiro impede o segundo)", h.sink.count())
	}
}

// ---------------------------------------------------------------------------
// TESTE 11 — ciclo/auto-dependência em depths_on é SURFACED como plano inválido, não
// deixado a ficar preso em silêncio (deferido para sempre).
// Falha-antes: sem deteção de ciclo, um plano cíclico passava a validação e ficava
// eternamente em waiting_deps sem qualquer sinal.
// ---------------------------------------------------------------------------

func TestValidatePlan_RejectsSelfDependency(t *testing.T) {
	head := &countingHeadroom{advertised: 5, live: 5}
	h := newHarness(t,
		&fakeGate{materialized: true},
		newLifecycle(nil),
		head,
		&fakeCards{},
		&fakeSink{failOn: map[string]error{}},
	)
	p := Plan{PlanID: "p1", Nodes: []Node{{NodeID: "a", DependsOn: []string{"a"}}}}
	if _, err := h.d.Dispatch(context.Background(), p); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("err = %v, quer ErrInvalidPlan (auto-dependência 'a'→'a')", err)
	}
	// Fail-closed na validação: nunca se consultou o headroom.
	if got := atomic.LoadInt32(&head.acquireCalls); got != 0 {
		t.Fatalf("Acquire chamado %d vezes num plano inválido — devia rejeitar antes", got)
	}
}

func TestValidatePlan_RejectsCycle(t *testing.T) {
	head := &countingHeadroom{advertised: 5, live: 5}
	h := newHarness(t,
		&fakeGate{materialized: true},
		newLifecycle(nil),
		head,
		&fakeCards{},
		&fakeSink{failOn: map[string]error{}},
	)
	// a→b→c→a (ciclo). Nenhum nó seria jamais despachável.
	p := Plan{PlanID: "p1", Nodes: []Node{
		{NodeID: "a", DependsOn: []string{"b"}},
		{NodeID: "b", DependsOn: []string{"c"}},
		{NodeID: "c", DependsOn: []string{"a"}},
	}}
	if _, err := h.d.Dispatch(context.Background(), p); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("err = %v, quer ErrInvalidPlan (ciclo a→b→c→a)", err)
	}
}

// TestValidatePlan_AcceptsAcyclicDAG garante que a deteção de ciclo NÃO é vácua: um DAG
// legítimo (diamante) passa a validação e despacha a raiz.
func TestValidatePlan_AcceptsAcyclicDAG(t *testing.T) {
	head := &countingHeadroom{advertised: 5, live: 5}
	h := newHarness(t,
		&fakeGate{materialized: true},
		newLifecycle(map[string]NodeState{"a": NodePending, "b": NodePending, "c": NodePending, "d": NodePending}),
		head,
		&fakeCards{cleared: map[string]bool{}},
		&fakeSink{failOn: map[string]error{}},
	)
	// Diamante acíclico: b→a, c→a, d→{b,c}. Só 'a' (sem deps) é despachável já.
	p := Plan{PlanID: "p1", Nodes: []Node{
		{NodeID: "a"},
		{NodeID: "b", DependsOn: []string{"a"}},
		{NodeID: "c", DependsOn: []string{"a"}},
		{NodeID: "d", DependsOn: []string{"b", "c"}},
	}}
	res, err := h.d.Dispatch(context.Background(), p)
	if err != nil {
		t.Fatalf("DAG acíclico devia validar: %v", err)
	}
	if outcomeOf(res, "a") != OutcomeDispatched {
		t.Fatalf("'a' outcome = %q, quer dispatched (raiz do DAG)", outcomeOf(res, "a"))
	}
}

func TestValidatePlan_RejectsDanglingDependency(t *testing.T) {
	head := &countingHeadroom{advertised: 5, live: 5}
	h := newHarness(t,
		&fakeGate{materialized: true},
		newLifecycle(nil),
		head,
		&fakeCards{},
		&fakeSink{failOn: map[string]error{}},
	)
	// 'b' depende de 'x', que não está no conjunto materializado.
	p := Plan{PlanID: "p1", Nodes: []Node{{NodeID: "b", DependsOn: []string{"x"}}}}
	if _, err := h.d.Dispatch(context.Background(), p); !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("err = %v, quer ErrInvalidPlan (dep 'x' fora do conjunto materializado)", err)
	}
}
