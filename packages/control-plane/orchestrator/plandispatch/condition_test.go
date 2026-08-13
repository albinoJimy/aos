package plandispatch_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planbudget"
	"github.com/aos-ref/control-plane/orchestrator/plandispatch"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	"github.com/aos-ref/substrate/eventstore"
)

// condition_test.go — ARESTAS CONDICIONAIS NO DESPACHO, sobre a CADEIA REAL
// (AOS-270, ADR-022 §2.1/§5).
//
// A montagem é deliberadamente SEM DOUBLES nas peças novas: Event Store real
// (`eventstore.Store`), emissor real (`plannerevents.Recorder`), reconstrução real
// (`plannerevents.Reconstruct`, via [plandispatch.EventJournal]), orçamento real
// (`budget.Budget` com a hierarquia CAS de AOS-008, via
// [planbudget.TreeBudgetMeter]) e o [plandispatch.Dispatcher] de produção. As
// únicas peças simuladas são as portas que JÁ eram portas em AOS-238 (gate,
// ciclo de vida, headroom, cartões, sink) — e mesmo essas só para pôr o cenário.
// Substituir o log ou o orçamento por doubles esconderia exactamente os defeitos
// de encaixe que este teste existe para apanhar.

const evalTokens = 5

// --- portas de cenário (as pré-existentes de AOS-238) -----------------------

type okGate struct{}

func (okGate) Materialized(context.Context, string) (bool, error) { return true, nil }

// stateMap é a vista do ciclo de vida: um estado FIXO por nó.
type stateMap map[string]plandispatch.NodeState

func (m stateMap) State(_ context.Context, _, nodeID string) (plandispatch.NodeState, error) {
	st, ok := m[nodeID]
	if !ok {
		return plandispatch.NodeUnknown, nil
	}
	return st, nil
}

type freeHeadroom struct{}

func (freeHeadroom) Available(context.Context) (int, error) { return 100, nil }
func (freeHeadroom) Acquire(context.Context) (bool, error)  { return true, nil }
func (freeHeadroom) Release(context.Context) error          { return nil }

type openCards struct{}

func (openCards) Cleared(context.Context, string, string) (bool, error) { return true, nil }

type recordingSink struct{ nodes []string }

func (s *recordingSink) Dispatch(_ context.Context, n plandispatch.Node) error {
	s.nodes = append(s.nodes, n.NodeID)
	return nil
}

// --- porta do RESULTADO REGISTADO -------------------------------------------

// resultMap é a vista dos resultados registados. `calls` conta as leituras: é a
// sonda de «o replay NÃO re-avalia» — numa passagem de replay este contador NÃO se
// move, porque o avaliador nem chega a ser alcançado.
type resultMap struct {
	recs  map[string]plandispatch.NodeResultRecord
	calls int
	fail  error
}

func (r *resultMap) Result(_ context.Context, _, nodeID string) (plandispatch.NodeResultRecord, bool, error) {
	r.calls++
	if r.fail != nil {
		return plandispatch.NodeResultRecord{}, false, r.fail
	}
	rec, ok := r.recs[nodeID]
	return rec, ok, nil
}

// --- montagem da cadeia real -------------------------------------------------

type rig struct {
	store   *eventstore.Store
	journal *plandispatch.EventJournal
	bud     *budget.Budget
	meter   *planbudget.TreeBudgetMeter
	sink    *recordingSink
	results *resultMap
	states  stateMap
}

// newRig monta a cadeia REAL: store → recorder → journal, e budget → meter. Os
// nós do plano recebem um nó de orçamento sob a raiz (a hierarquia de AOS-008).
func newRig(t *testing.T, planID string, root budget.Amount, nodeIDs []string) *rig {
	t.Helper()
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	rec, err := plannerevents.NewRecorder(store)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	journal, err := plandispatch.NewEventJournal(rec, store)
	if err != nil {
		t.Fatalf("NewEventJournal: %v", err)
	}
	bud, err := budget.New(planID, root)
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	for _, id := range nodeIDs {
		if err := bud.AddNode(id, planID, root); err != nil {
			t.Fatalf("budget.AddNode(%q): %v", id, err)
		}
	}
	meter, err := planbudget.NewTreeBudgetMeter(bud, budget.Amount{Tokens: evalTokens, CostMicroUSD: 1})
	if err != nil {
		t.Fatalf("NewTreeBudgetMeter: %v", err)
	}
	return &rig{store: store, journal: journal, bud: bud, meter: meter, sink: &recordingSink{}, results: &resultMap{recs: map[string]plandispatch.NodeResultRecord{}}}
}

func (r *rig) dispatcher(t *testing.T) *plandispatch.Dispatcher {
	t.Helper()
	d, err := plandispatch.NewDispatcher(okGate{}, r.states, freeHeadroom{}, openCards{}, r.sink,
		plandispatch.WithConditionalBranches(r.results, r.journal, r.meter))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return d
}

// branchPlan é o organigrama de ADR-022 §2.1: `a` produz, `v` verifica, `ok` e
// `fix` são os dois ramos DECLARADOS À PRIORI, e `after` é a continuação do ramo
// feliz. Acíclico — como o validador puro exige.
func branchPlan() plandispatch.Plan {
	pass := []plan.Predicate{{Subject: plan.SubjectVerdict, Op: plan.OpEq, Enum: plan.EnumPass}}
	fail := []plan.Predicate{{Subject: plan.SubjectVerdict, Op: plan.OpEq, Enum: plan.EnumFail}}
	return plandispatch.Plan{
		PlanID: "plan-cond",
		Nodes: []plandispatch.Node{
			{NodeID: "a"},
			{NodeID: "v", DependsOn: []string{"a"}},
			{NodeID: "ok", ConditionalOn: []plan.ConditionalEdge{{From: "v", When: pass}}},
			{NodeID: "fix", ConditionalOn: []plan.ConditionalEdge{{From: "v", When: fail}}},
			{NodeID: "after", DependsOn: []string{"ok"}},
		},
	}
}

func outcomes(res plandispatch.Result) map[string]plandispatch.Outcome {
	out := make(map[string]plandispatch.Outcome, len(res.Results))
	for _, r := range res.Results {
		out[r.NodeID] = r.Outcome
	}
	return out
}

// branchEvents projecta as decisões de ramo REGISTADAS no stream, relendo o Event
// Store pela reconstrução REAL do domínio.
func branchEvents(t *testing.T, store *eventstore.Store, planID string) []plannerevents.PlanEvent {
	t.Helper()
	evs, err := plannerevents.Reconstruct(context.Background(), store, planID)
	if err != nil {
		if plannerevents.IsStreamAbsent(err) {
			return nil // stream ainda sem factos: zero decisões registadas
		}
		t.Fatalf("Reconstruct: %v", err)
	}
	out := make([]plannerevents.PlanEvent, 0, len(evs))
	for _, e := range evs {
		if e.Type == plannerevents.EventBranchDecided {
			out = append(out, e)
		}
	}
	return out
}

// TestConditionalBranchDecidesDebitsAndReplays é o teste de CADEIA de AOS-270 e
// cobre, numa só trajectória, os CA 2 e 3:
//
//   - a condição é avaliada como função pura do RESULTADO REGISTADO (o veredicto
//     `fail` de `v` poda o ramo `ok` e abre o ramo `fix`);
//   - a decisão de ramo é EMITIDA como facto (`plan.branch_decided`) no Event Store
//     real;
//   - a avaliação DEBITA o orçamento da árvore real (ADR-008);
//   - a SEGUNDA passagem — o replay — reproduz o MESMO ramo SEM re-avaliar: nenhuma
//     leitura de resultado, nenhum débito novo, nenhum facto novo. A prova é
//     destrutiva: a vista de resultados é posta a FALHAR e o despacho continua a
//     dar o mesmo, porque nunca a consulta.
//
// FALHA-ANTES: sem a leitura-do-registo-primeiro, a segunda passagem re-avaliaria
// (erro da vista ⇒ passagem abortada) e re-debitaria — e o «replay» seria uma
// re-execução com outro custo.
func TestConditionalBranchDecidesDebitsAndReplays(t *testing.T) {
	ctx := context.Background()
	p := branchPlan()
	r := newRig(t, p.PlanID, budget.Amount{Tokens: 1000, CostMicroUSD: 1000}, []string{"a", "v", "ok", "fix", "after"})
	r.states = stateMap{
		"a": plandispatch.NodeComplete, "v": plandispatch.NodeComplete,
		"ok": plandispatch.NodePending, "fix": plandispatch.NodePending, "after": plandispatch.NodePending,
	}
	// O veredicto REGISTADO de `v` é `fail`: o ramo de reprovação é o que abre.
	r.results.recs["v"] = plandispatch.NodeResultRecord{Terminal: plandispatch.TerminalComplete, Verdict: plandispatch.VerdictFail}

	before, err := r.bud.Available(p.PlanID)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}

	res, err := r.dispatcher(t).Dispatch(ctx, p)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	got := outcomes(res)
	want := map[string]plandispatch.Outcome{
		"a": plandispatch.OutcomeInflightOrDone, "v": plandispatch.OutcomeInflightOrDone,
		"ok":  plandispatch.OutcomeBranchNotTaken, // veredicto fail ⇒ ramo pass não é tomado
		"fix": plandispatch.OutcomeDispatched,     // ramo de reprovação, declarado à priori
		// `after` depende de `ok`: podado por herança, e SURFACED como tal em vez de
		// ficar eternamente em waiting_deps.
		"after": plandispatch.OutcomeBranchNotTaken,
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("outcome[%s] = %q; queria %q (todos: %+v)", id, got[id], w, got)
		}
	}
	if len(r.sink.nodes) != 1 || r.sink.nodes[0] != "fix" {
		t.Fatalf("despachados = %v; só o ramo tomado devia despachar", r.sink.nodes)
	}
	if res.BranchesEvaluated != 2 {
		t.Fatalf("BranchesEvaluated = %d; queria 2 (ok e fix)", res.BranchesEvaluated)
	}

	// CA(2) — o FACTO ficou no log real, um por nó decidido.
	evs := branchEvents(t, r.store, p.PlanID)
	if len(evs) != 2 {
		t.Fatalf("eventos plan.branch_decided = %d; queria 2", len(evs))
	}

	// CA(3) — a avaliação DEBITOU a árvore: duas decisões × custo, na raiz.
	after, err := r.bud.Available(p.PlanID)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if delta := before.Tokens - after.Tokens; delta != 2*evalTokens {
		t.Fatalf("débito na raiz = %d tokens; queria %d (2 avaliações)", delta, 2*evalTokens)
	}

	// --- SEGUNDA PASSAGEM: replay ---------------------------------------------
	callsBefore := r.results.calls
	r.results.fail = errors.New("a vista de resultados NÃO pode ser consultada no replay")
	r.sink.nodes = nil

	res2, err := r.dispatcher(t).Dispatch(ctx, p)
	if err != nil {
		t.Fatalf("replay abortou — o despachante RE-AVALIOU em vez de ler a decisão registada: %v", err)
	}
	if r.results.calls != callsBefore {
		t.Fatalf("a vista de resultados foi consultada %d vezes no replay; queria 0", r.results.calls-callsBefore)
	}
	if res2.BranchesEvaluated != 0 {
		t.Fatalf("BranchesEvaluated no replay = %d; queria 0", res2.BranchesEvaluated)
	}
	if got2 := outcomes(res2); got2["ok"] != plandispatch.OutcomeBranchNotTaken || got2["fix"] != plandispatch.OutcomeDispatched {
		t.Fatalf("replay reproduziu OUTRO ramo: %+v", got2)
	}
	if len(branchEvents(t, r.store, p.PlanID)) != 2 {
		t.Fatal("o replay apensou factos novos: a decisão de ramo deixou de ser um facto único")
	}
	afterReplay, err := r.bud.Available(p.PlanID)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if afterReplay != after {
		t.Fatalf("o replay DEBITOU orçamento (%+v → %+v): re-avaliou", after, afterReplay)
	}
}

// TestConditionUndecidedWaitsWithoutCostOrFact — enquanto a origem não tem
// resultado registado, o nó ESPERA: nada é debitado e nada é registado. É o que
// impede que re-invocações do escalonador durante a espera drenem a árvore e
// encham o log de decisões provisórias.
func TestConditionUndecidedWaitsWithoutCostOrFact(t *testing.T) {
	ctx := context.Background()
	p := branchPlan()
	r := newRig(t, p.PlanID, budget.Amount{Tokens: 1000, CostMicroUSD: 1000}, []string{"a", "v", "ok", "fix", "after"})
	r.states = stateMap{
		"a": plandispatch.NodeComplete, "v": plandispatch.NodeRunning,
		"ok": plandispatch.NodePending, "fix": plandispatch.NodePending, "after": plandispatch.NodePending,
	}
	before, _ := r.bud.Available(p.PlanID)

	for i := 0; i < 3; i++ { // várias passagens: o escalonador re-invoca
		res, err := r.dispatcher(t).Dispatch(ctx, p)
		if err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if got := outcomes(res); got["ok"] != plandispatch.OutcomeWaitingCondition {
			t.Fatalf("outcome[ok] = %q; queria waiting_condition", got["ok"])
		}
		if res.BranchesEvaluated != 0 {
			t.Fatalf("passagem %d decidiu %d ramos sobre uma origem por terminar", i, res.BranchesEvaluated)
		}
	}
	if after, _ := r.bud.Available(p.PlanID); after != before {
		t.Fatalf("esperar por uma condição DEBITOU orçamento (%+v → %+v)", before, after)
	}
	if n := len(branchEvents(t, r.store, p.PlanID)); n != 0 {
		t.Fatalf("%d decisões registadas sobre uma origem por terminar", n)
	}
	if len(r.sink.nodes) != 0 {
		t.Fatalf("despachou %v com a condição por decidir", r.sink.nodes)
	}
}

// TestConditionalWithoutPortsIsRefused — FAIL-CLOSED POR OMISSÃO. Um despachante
// sem as portas de ramos (o de AOS-238) NÃO ignora os guardas: recusa o plano.
// FALHA-ANTES: ignorar `conditional_on` despacharia `ok` E `fix` — os dois ramos
// de uma decisão que ninguém tomou.
func TestConditionalWithoutPortsIsRefused(t *testing.T) {
	sink := &recordingSink{}
	states := stateMap{"a": plandispatch.NodeComplete, "v": plandispatch.NodeComplete,
		"ok": plandispatch.NodePending, "fix": plandispatch.NodePending, "after": plandispatch.NodePending}
	d, err := plandispatch.NewDispatcher(okGate{}, states, freeHeadroom{}, openCards{}, sink)
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if _, err := d.Dispatch(context.Background(), branchPlan()); !errors.Is(err, plandispatch.ErrConditionalUnsupported) {
		t.Fatalf("erro = %v; queria ErrConditionalUnsupported", err)
	}
	if len(sink.nodes) != 0 {
		t.Fatalf("despachou %v num plano condicional que não sabe avaliar", sink.nodes)
	}
}

// TestPlanWithoutConditionsTouchesNoBranchPort — o caminho de AOS-238 fica
// INTACTO: um plano sem condições não lê o registo, não avalia e não debita.
func TestPlanWithoutConditionsTouchesNoBranchPort(t *testing.T) {
	ctx := context.Background()
	p := plandispatch.Plan{PlanID: "plan-plain", Nodes: []plandispatch.Node{
		{NodeID: "a"}, {NodeID: "b", DependsOn: []string{"a"}},
	}}
	r := newRig(t, p.PlanID, budget.Amount{Tokens: 100, CostMicroUSD: 100}, []string{"a", "b"})
	r.states = stateMap{"a": plandispatch.NodePending, "b": plandispatch.NodePending}
	before, _ := r.bud.Available(p.PlanID)

	res, err := r.dispatcher(t).Dispatch(ctx, p)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res.BranchesEvaluated != 0 || res.NotTaken != 0 {
		t.Fatalf("plano sem condições produziu decisões de ramo: %+v", res)
	}
	if r.results.calls != 0 {
		t.Fatalf("plano sem condições leu %d resultados", r.results.calls)
	}
	if after, _ := r.bud.Available(p.PlanID); after != before {
		t.Fatal("plano sem condições debitou orçamento")
	}
	if got := outcomes(res); got["a"] != plandispatch.OutcomeDispatched {
		t.Fatalf("outcome[a] = %q; queria dispatched", got["a"])
	}
}

// TestBranchDigestMismatchFailsClosed — a decisão registada está AMARRADA à
// expressão que a produziu. Editar a condição depois de o ramo estar decidido não
// é um replay: é um plano diferente, que volta ao gate. Fail-closed.
func TestBranchDigestMismatchFailsClosed(t *testing.T) {
	ctx := context.Background()
	p := branchPlan()
	r := newRig(t, p.PlanID, budget.Amount{Tokens: 1000, CostMicroUSD: 1000}, []string{"a", "v", "ok", "fix", "after"})
	r.states = stateMap{
		"a": plandispatch.NodeComplete, "v": plandispatch.NodeComplete,
		"ok": plandispatch.NodePending, "fix": plandispatch.NodePending, "after": plandispatch.NodePending,
	}
	r.results.recs["v"] = plandispatch.NodeResultRecord{Terminal: plandispatch.TerminalComplete, Verdict: plandispatch.VerdictPass}
	if _, err := r.dispatcher(t).Dispatch(ctx, p); err != nil {
		t.Fatalf("primeira passagem: %v", err)
	}

	// Edição silenciosa da condição do MESMO nó, com a decisão já registada.
	edited := branchPlan()
	for i := range edited.Nodes {
		if edited.Nodes[i].NodeID == "ok" {
			edited.Nodes[i].ConditionalOn[0].When[0].Enum = plan.EnumFail
		}
	}
	r.sink.nodes = nil
	if _, err := r.dispatcher(t).Dispatch(ctx, edited); !errors.Is(err, plandispatch.ErrBranchDigestMismatch) {
		t.Fatalf("erro = %v; queria ErrBranchDigestMismatch", err)
	}
	if len(r.sink.nodes) != 0 {
		t.Fatalf("despachou %v sobre uma condição editada depois da decisão", r.sink.nodes)
	}
}

// TestBudgetExhaustionKeepsNodeWaiting — sem headroom, a decisão NÃO se toma: o nó
// espera. Ficar sem orçamento não é a condição ter dado falso, e a diferença
// importa: uma poda é definitiva, uma espera não. Prova-se contra o orçamento REAL
// (raiz com menos tokens do que uma avaliação custa).
func TestBudgetExhaustionKeepsNodeWaiting(t *testing.T) {
	ctx := context.Background()
	p := branchPlan()
	r := newRig(t, p.PlanID, budget.Amount{Tokens: evalTokens - 1, CostMicroUSD: 1}, []string{"a", "v", "ok", "fix", "after"})
	r.states = stateMap{
		"a": plandispatch.NodeComplete, "v": plandispatch.NodeComplete,
		"ok": plandispatch.NodePending, "fix": plandispatch.NodePending, "after": plandispatch.NodePending,
	}
	r.results.recs["v"] = plandispatch.NodeResultRecord{Terminal: plandispatch.TerminalComplete, Verdict: plandispatch.VerdictPass}

	res, err := r.dispatcher(t).Dispatch(ctx, p)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	got := outcomes(res)
	if got["ok"] != plandispatch.OutcomeWaitingCondition || got["fix"] != plandispatch.OutcomeWaitingCondition {
		t.Fatalf("sem orçamento, os ramos deviam ESPERAR: %+v", got)
	}
	if res.NotTaken != 0 {
		t.Fatal("falta de orçamento PODOU um ramo: uma espera virou decisão")
	}
	if n := len(branchEvents(t, r.store, p.PlanID)); n != 0 {
		t.Fatalf("%d decisões registadas sem o débito ter passado", n)
	}
	if len(r.sink.nodes) != 0 {
		t.Fatalf("despachou %v sem ter decidido o ramo", r.sink.nodes)
	}
}

// TestMetricAndTerminalObservables — a gramática avalia os TRÊS observáveis sobre o
// resultado registado, com a semântica fail-closed da ausência.
func TestMetricAndTerminalObservables(t *testing.T) {
	ctx := context.Background()
	eighty := int64(80)
	mkPlan := func(preds []plan.Predicate) plandispatch.Plan {
		return plandispatch.Plan{PlanID: "plan-obs", Nodes: []plandispatch.Node{
			{NodeID: "src"},
			{NodeID: "dst", ConditionalOn: []plan.ConditionalEdge{{From: "src", When: preds}}},
		}}
	}
	cases := []struct {
		name string
		rec  plandispatch.NodeResultRecord
		pred []plan.Predicate
		want plandispatch.Outcome
	}{
		{
			name: "metrica abaixo do limiar abre o ramo de reforço",
			rec:  plandispatch.NodeResultRecord{Terminal: plandispatch.TerminalComplete, Metrics: map[string]int64{"coverage": 40}},
			pred: []plan.Predicate{{Subject: plan.SubjectMetric, Metric: "coverage", Op: plan.OpLt, Number: &eighty}},
			want: plandispatch.OutcomeDispatched,
		},
		{
			name: "metrica acima do limiar poda o ramo",
			rec:  plandispatch.NodeResultRecord{Terminal: plandispatch.TerminalComplete, Metrics: map[string]int64{"coverage": 95}},
			pred: []plan.Predicate{{Subject: plan.SubjectMetric, Metric: "coverage", Op: plan.OpLt, Number: &eighty}},
			want: plandispatch.OutcomeBranchNotTaken,
		},
		{
			// AUSENCIA = INDECISAO, NAO FALSIDADE (correccao da auditoria da wave). Um
			// predicado falso NAO e uma espera: e branch_not_taken, que e TERMINAL, poda
			// a descendencia e fica REGISTADO como facto imutavel. Sobre um observavel
			// que ninguem produz, isso e podar o plano por causa de uma lacuna de
			// wiring — e registar a poda como se alguem a tivesse decidido.
			name: "metrica AUSENTE deixa o no a ESPERAR (nunca poda)",
			rec:  plandispatch.NodeResultRecord{Terminal: plandispatch.TerminalComplete},
			pred: []plan.Predicate{{Subject: plan.SubjectMetric, Metric: "coverage", Op: plan.OpLt, Number: &eighty}},
			want: plandispatch.OutcomeWaitingCondition,
		},
		{
			name: "estado terminal FAILED abre o ramo de recuperacao",
			rec:  plandispatch.NodeResultRecord{Terminal: plandispatch.TerminalFailed},
			pred: []plan.Predicate{{Subject: plan.SubjectTerminalState, Op: plan.OpEq, Enum: plan.EnumFailed}},
			want: plandispatch.OutcomeDispatched,
		},
		{
			// Nem sequer com `ne`: um observavel que nao existe nao se compara. A
			// alternativa (ausencia ⇒ diferente de tudo) deixaria um plano ramificar com
			// base em nada; a que existia (ausencia ⇒ falso) podava-o em silencio.
			name: "veredicto AUSENTE deixa o no a ESPERAR, mesmo com o operador ne",
			rec:  plandispatch.NodeResultRecord{Terminal: plandispatch.TerminalComplete},
			pred: []plan.Predicate{{Subject: plan.SubjectVerdict, Op: plan.OpNe, Enum: plan.EnumFail}},
			want: plandispatch.OutcomeWaitingCondition,
		},
		{
			name: "conjuncao: basta um predicado falso",
			rec:  plandispatch.NodeResultRecord{Terminal: plandispatch.TerminalComplete, Verdict: plandispatch.VerdictPass, Metrics: map[string]int64{"coverage": 10}},
			pred: []plan.Predicate{
				{Subject: plan.SubjectVerdict, Op: plan.OpEq, Enum: plan.EnumPass},
				{Subject: plan.SubjectMetric, Metric: "coverage", Op: plan.OpGte, Number: &eighty},
			},
			want: plandispatch.OutcomeBranchNotTaken,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mkPlan(tc.pred)
			r := newRig(t, p.PlanID, budget.Amount{Tokens: 1000, CostMicroUSD: 1000}, []string{"src", "dst"})
			srcState := plandispatch.NodeComplete
			if tc.rec.Terminal == plandispatch.TerminalFailed {
				srcState = plandispatch.NodeFailed
			}
			r.states = stateMap{"src": srcState, "dst": plandispatch.NodePending}
			r.results.recs["src"] = tc.rec
			res, err := r.dispatcher(t).Dispatch(ctx, p)
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if got := outcomes(res)["dst"]; got != tc.want {
				t.Fatalf("outcome[dst] = %q; queria %q", got, tc.want)
			}
		})
	}
}

// ===========================================================================
// REMEDIAÇÃO da auditoria adversarial da wave.
// ===========================================================================

// TestPruningStaysMonotonicAcrossLifecycleChanges — a poda de um ramo não-tomado
// continua a propagar-se DEPOIS de o nó podado sair de `NodePending`.
//
// O CENÁRIO. Passagem 1 com veredicto `fail`: `ok` fica branch_not_taken, `fix`
// despacha, `after` (que depende de `ok`) é podado por herança — e o facto fica no
// Event Store. O ciclo de vida marca então `ok` como FAILED (disposição plausível
// para um nó que nunca correrá) e `fix` como COMPLETE. Passagem 2, sobre o MESMO
// plano e o MESMO journal, tem de dar EXACTAMENTE a mesma disposição.
//
// FALHA-ANTES: a fase de decisão saltava o nó ANTES de ler `recorded`, pelo que
// `ok` deixava de aparecer em `decisions`, [propagateNotTaken] ficava sem semente e
// `after` caía em `waiting_deps` com NotTaken=0 — a disposição do mesmo plano
// deixava de ser monótona apesar de o facto durável não ter mudado. É a podridão
// silenciosa que a propagação existe para evitar.
func TestPruningStaysMonotonicAcrossLifecycleChanges(t *testing.T) {
	ctx := context.Background()
	p := branchPlan()
	r := newRig(t, p.PlanID, budget.Amount{Tokens: 1000, CostMicroUSD: 1000}, []string{"a", "v", "ok", "fix", "after"})
	r.states = stateMap{
		"a": plandispatch.NodeComplete, "v": plandispatch.NodeComplete,
		"ok": plandispatch.NodePending, "fix": plandispatch.NodePending, "after": plandispatch.NodePending,
	}
	r.results.recs["v"] = plandispatch.NodeResultRecord{Terminal: plandispatch.TerminalComplete, Verdict: plandispatch.VerdictFail}

	first, err := r.dispatcher(t).Dispatch(ctx, p)
	if err != nil {
		t.Fatalf("passagem 1: %v", err)
	}
	if got := outcomes(first)["after"]; got != plandispatch.OutcomeBranchNotTaken {
		t.Fatalf("passagem 1: after = %s; queria branch_not_taken", got)
	}
	if first.NotTaken == 0 {
		t.Fatal("passagem 1: NotTaken = 0 — a poda não foi contada")
	}

	// O ciclo de vida evolui: o nó podado é marcado FAILED, o ramo tomado COMPLETE.
	r.states["ok"] = plandispatch.NodeFailed
	r.states["fix"] = plandispatch.NodeComplete

	second, err := r.dispatcher(t).Dispatch(ctx, p)
	if err != nil {
		t.Fatalf("passagem 2: %v", err)
	}
	if got := outcomes(second)["after"]; got != plandispatch.OutcomeBranchNotTaken {
		t.Fatalf("passagem 2: after = %s; queria branch_not_taken (o facto registado não mudou)", got)
	}
	// A CONTAGEM não tem de ser igual, e exigi-lo seria exigir a coisa errada: a
	// passagem 1 conta `ok` (podado E ainda pendente) + `after`; na passagem 2 o `ok`
	// já saiu do conjunto DESPACHÁVEL — o ciclo de vida marcou-o FAILED e o laço de
	// disposições de AOS-238 reporta-o como `inflight_or_done`, que é a sua disposição
	// VERDADEIRA (comportamento que PRECEDE as condicionais). O que tem de se manter
	// monótono é a PODA DERIVADA: o descendente `after`, esse ainda pendente, continua
	// podado (asserido acima) e NÃO ressuscita para `waiting_deps`.
	if second.NotTaken < 1 {
		t.Fatalf("a poda derivada deixou de propagar: NotTaken = %d (esperado >= 1 — `after` continua podado por herança)", second.NotTaken)
	}
	// E o `ok` NÃO volta a ser reportado como poda: um nó que já falhou tem a
	// disposição do seu desfecho, não a do ramo. Reportá-lo como `branch_not_taken`
	// seria contar duas vezes a mesma poda e mascarar o desfecho real.
	for _, nr := range second.Results {
		if nr.NodeID == "ok" && nr.Outcome == plandispatch.OutcomeBranchNotTaken {
			t.Fatalf("`ok` já FALHOU: a disposição desta passagem tem de ser o desfecho dele, não `branch_not_taken`")
		}
	}
	// E continua a ser um REPLAY: nenhum facto novo apenso.
	if evs := branchEvents(t, r.store, p.PlanID); len(evs) != 2 {
		t.Fatalf("decisões registadas = %d; queria 2 (a passagem 2 não pode apensar factos novos)", len(evs))
	}
}

// TestLifecycleResultsProjectsTerminalState — o adaptador de PRODUÇÃO da porta
// [plandispatch.ResultView]: projecta `terminal_state` a partir da MESMA vista do
// ciclo de vida que o despachante já tem ligada.
//
// PORQUE IMPORTA: sem ele, das três portas de ramos condicionais só duas tinham
// adaptador real e a terceira — a fonte do observável — só existia como double de
// teste. Um wiring a vivo não tinha o que passar e o primeiro plano com
// `conditional_on` era recusado com ErrConditionalUnsupported.
func TestLifecycleResultsProjectsTerminalState(t *testing.T) {
	ctx := context.Background()
	states := stateMap{
		"done": plandispatch.NodeComplete,
		"bust": plandispatch.NodeFailed,
		"run":  plandispatch.NodeRunning,
	}
	rv, err := plandispatch.NewLifecycleResults(states)
	if err != nil {
		t.Fatalf("NewLifecycleResults: %v", err)
	}
	for _, tc := range []struct {
		node string
		want plandispatch.TerminalOutcome
		ok   bool
	}{
		{"done", plandispatch.TerminalComplete, true},
		{"bust", plandispatch.TerminalFailed, true},
		{"run", plandispatch.TerminalUnset, false},   // não-terminal ⇒ INDECIDO, nunca falso
		{"ghost", plandispatch.TerminalUnset, false}, // desconhecido ⇒ fail-closed
	} {
		rec, ok, err := rv.Result(ctx, "plan", tc.node)
		if err != nil {
			t.Fatalf("Result(%q): %v", tc.node, err)
		}
		if ok != tc.ok || rec.Terminal != tc.want {
			t.Fatalf("Result(%q) = (%q,%v); queria (%q,%v)", tc.node, rec.Terminal, ok, tc.want, tc.ok)
		}
		// O que este adaptador NAO projecta (declarado): veredicto e metricas vem do
		// facto `plan.verdict_recorded` (AOS-271), projectado por ResultFromVerdict. A
		// admissao JA NAO recusa em bloco os ramos sobre `verdict`, pelo que o
		// fail-closed LOUD vive agora no despacho: ausencia ⇒ INDECIDO, nunca falso.
		if rec.Verdict != plandispatch.VerdictAbsent || rec.Metrics != nil {
			t.Fatalf("Result(%q) inventou observáveis que o nó não produz: %+v", tc.node, rec)
		}
	}
	if _, err := plandispatch.NewLifecycleResults(nil); !errors.Is(err, plandispatch.ErrLifecycleResultsDeps) {
		t.Fatalf("construção com vista nil: err = %v; queria ErrLifecycleResultsDeps", err)
	}
}

// TestQualityBranchWithoutEmitterRecordsNoDecision — O TESTE QUE FALTAVA, do lado do
// DESPACHO (blocker da auditoria adversarial da wave).
//
// O CENARIO EXACTO que a auditoria demonstrou ao vivo: um plano APROVADO com um ramo de
// qualidade (`ok` condicionado ao `verdict` de `v`) despachado com o adaptador de
// PRODUCAO `NewLifecycleResults` — o unico que existe —, que devolve ok=true com
// Verdict="" para um no COMPLETE. Antes, `evalPredicate` tratava a ausencia como FALSO,
// `OutcomeBranchNotTaken` e TERMINAL, e a decisao ficava APENSA: o ramo de qualidade de
// um plano aprovado morria em silencio e o log guardava a poda como facto.
//
// O que este teste fixa: enquanto nao houver emissor de veredicto ligado, o no ESPERA
// (`waiting_condition`), NENHUM facto `plan.branch_decided` e apenso, e nada e
// despachado. Falha ruidosamente — que e a direccao honesta — em vez de mutilar o plano.
func TestQualityBranchWithoutEmitterRecordsNoDecision(t *testing.T) {
	ctx := context.Background()
	p := branchPlan()
	r := newRig(t, p.PlanID, budget.Amount{Tokens: 1000, CostMicroUSD: 1000}, []string{"a", "v", "ok", "fix", "after"})
	states := stateMap{
		"a": plandispatch.NodeComplete, "v": plandispatch.NodeComplete,
		"ok": plandispatch.NodePending, "fix": plandispatch.NodePending, "after": plandispatch.NodePending,
	}
	r.states = states

	// A porta de resultados e o ADAPTADOR DE PRODUCAO, nao o double: projecta
	// terminal_state a partir do ciclo de vida e NUNCA veredicto.
	prod, err := plandispatch.NewLifecycleResults(states)
	if err != nil {
		t.Fatalf("NewLifecycleResults: %v", err)
	}
	d, err := plandispatch.NewDispatcher(okGate{}, r.states, freeHeadroom{}, openCards{}, r.sink,
		plandispatch.WithConditionalBranches(prod, r.journal, r.meter))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	before, _ := r.bud.Available(p.PlanID)

	res, err := d.Dispatch(ctx, p)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	got := outcomes(res)
	for _, id := range []string{"ok", "fix"} {
		if got[id] != plandispatch.OutcomeWaitingCondition {
			t.Fatalf("outcome[%s] = %q; queria waiting_condition — um ramo de qualidade SEM EMISSOR nao pode ser decidido", id, got[id])
		}
	}
	if res.NotTaken != 0 {
		t.Fatalf("PODA SILENCIOSA: %d nos podados por um veredicto que ninguem emitiu", res.NotTaken)
	}
	if res.BranchesEvaluated != 0 {
		t.Fatalf("BranchesEvaluated = %d; nenhuma decisao pode ser tomada sem observavel", res.BranchesEvaluated)
	}
	if n := len(branchEvents(t, r.store, p.PlanID)); n != 0 {
		t.Fatalf("%d decisoes de ramo REGISTADAS sobre um veredicto inexistente — o facto e imutavel", n)
	}
	if len(r.sink.nodes) != 0 {
		t.Fatalf("despachou %v com o ramo por decidir", r.sink.nodes)
	}
	if after, _ := r.bud.Available(p.PlanID); after != before {
		t.Fatal("esperar por um veredicto inexistente DEBITOU orcamento")
	}
}
