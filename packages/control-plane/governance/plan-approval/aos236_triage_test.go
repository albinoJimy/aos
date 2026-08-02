package planapproval

import (
	"context"
	"errors"
	"sync"
	"testing"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// fakeRevalidator é a porta [Revalidator] programável (o adaptador planvalidate no
// wiring). Regista as chamadas e o grafo revisto que recebeu, devolvendo um erro fixo.
type fakeRevalidator struct {
	mu    sync.Mutex
	calls int
	seen  []Plan
	err   error
}

func (r *fakeRevalidator) Revalidate(_ context.Context, p Plan) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.seen = append(r.seen, p)
	return r.err
}

func (r *fakeRevalidator) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// editAddingNode devolve uma [PlanDecision] de EDIÇÃO que acrescenta um nó "e" (com a
// aresta d→e) ao samplePlan — ESTRUTURALMENTE VÁLIDO (sem ciclo/dangling). Só uma
// revalidação SEMÂNTICA (a porta) pode reprová-lo; a estrutural local deixa passar.
func editAddingNode(class risk.Class) PlanDecision {
	return PlanDecision{
		Verdict:  VerdictEdit,
		Approver: "human-editor",
		RevisedNodes: []PlanNode{
			{TaskID: "a", Class: class, Capability: "http.get", Preview: "cap:http.get -> https://x/a", Resource: "https://x/a"},
			{TaskID: "b", Class: class, Capability: "http.get", Preview: "cap:http.get -> https://x/b", Resource: "https://x/b"},
			{TaskID: "c", Class: class, Capability: "http.get", Preview: "cap:http.get -> https://x/c", Resource: "https://x/c"},
			{TaskID: "d", Class: class, Capability: "http.get", Preview: "cap:http.get -> https://x/d", Resource: "https://x/d"},
			{TaskID: "e", Class: class, Capability: "http.post", Preview: "cap:http.post -> https://x/e", Resource: "https://x/e"},
		},
		RevisedEdges:  [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}, {"d", "e"}},
		ReviewedNodes: []string{"a", "b", "c", "d", "e"},
	}
}

// TEST (AOS-236 / AOS-231) REVALIDAÇÃO PELA PORTA: uma edição que introduz um nó que a
// revalidação SEMÂNTICA reprova é RECUSADA fail-closed ANTES de assinar — e o Revalidator
// recebe o grafo REVISTO (não o original). FALHA-ANTES: sem a porta ligada, a MESMA edição
// (estruturalmente válida) seria APROVADA pelo canal.
func TestEdit_RevalidatesViaPortBeforeApprove(t *testing.T) {
	ctx := context.Background()
	plan := samplePlan("run-reval", risk.ClassGray)

	// --- FALHA-ANTES: sem Revalidator, a edição estruturalmente-válida é aprovada. ---
	reviewerA := &fakeReviewer{dec: editAddingNode(risk.ClassGray)}
	chA := &spyChannel{approved: true, approver: "human-editor"}
	gateA, err := NewPlanGate(fakeOracle{level: autonomy.L1}, chA, WithReviewer(reviewerA))
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}
	decA, err := gateA.Approve(ctx, plan)
	if err != nil {
		t.Fatalf("Approve (sem reval): %v", err)
	}
	if decA.Verdict != VerdictEdit {
		t.Fatalf("FALHA-ANTES esperada: sem revalidacao a edicao invalida seria aprovada; verdict=%s", decA.Verdict)
	}

	// --- COM Revalidator que REPROVA: a mesma edição é recusada fail-closed. ---
	reval := &fakeRevalidator{err: errors.New("planvalidate: no 'e' viola politica de egress")}
	reviewerB := &fakeReviewer{dec: editAddingNode(risk.ClassGray)}
	chB := &spyChannel{approved: true, approver: "human-editor"}
	gateB, err := NewPlanGate(fakeOracle{level: autonomy.L1}, chB, WithReviewer(reviewerB), WithRevalidator(reval))
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}
	decB, err := gateB.Approve(ctx, plan)
	if !errors.Is(err, ErrRevalidationFailed) {
		t.Fatalf("edicao nao-revalidada: esperava ErrRevalidationFailed, obtive %v", err)
	}
	if decB.Verdict != VerdictReject {
		t.Fatalf("edicao nao-revalidada: verdict=%s (esperava reject)", decB.Verdict)
	}
	// O canal NUNCA foi chamado (recusa ANTES de assinar) — nenhum round-trip ao LLM.
	if chB.callCount() != 0 {
		t.Fatalf("canal chamado apesar da revalidacao falhada: %d (esperava 0)", chB.callCount())
	}
	// O Revalidator recebeu o grafo REVISTO (5 nós), não o original (4).
	if reval.count() != 1 {
		t.Fatalf("Revalidator chamado %d vezes (esperava 1)", reval.count())
	}
	reval.mu.Lock()
	got := reval.seen[0]
	reval.mu.Unlock()
	if len(got.Nodes) != 5 {
		t.Fatalf("Revalidator recebeu %d nos (esperava 5 — o grafo REVISTO)", len(got.Nodes))
	}

	// --- COM Revalidator que APROVA: a edição válida passa e o diff regista o nó novo. ---
	ok := &fakeRevalidator{}
	reviewerC := &fakeReviewer{dec: editAddingNode(risk.ClassGray)}
	chC := &spyChannel{approved: true, approver: "human-editor"}
	gateC, err := NewPlanGate(fakeOracle{level: autonomy.L1}, chC, WithReviewer(reviewerC), WithRevalidator(ok))
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}
	decC, err := gateC.Approve(ctx, plan)
	if err != nil {
		t.Fatalf("Approve (reval ok): %v", err)
	}
	if decC.Verdict != VerdictEdit || decC.Diff == nil {
		t.Fatalf("edicao revalidada: verdict=%s diff=%v", decC.Verdict, decC.Diff)
	}
	if len(decC.Diff.AddedNodes) != 1 || decC.Diff.AddedNodes[0] != "e" {
		t.Fatalf("diff estrutural: AddedNodes=%v (esperava [e])", decC.Diff.AddedNodes)
	}
	if len(decC.Diff.AddedEdges) != 1 || decC.Diff.AddedEdges[0] != [2]string{"d", "e"} {
		t.Fatalf("diff estrutural: AddedEdges=%v (esperava [[d e]])", decC.Diff.AddedEdges)
	}
}

// TEST (AOS-236) REVALIDAÇÃO ESTRUTURAL LOCAL: uma edição que introduz um CICLO é recusada
// fail-closed mesmo SEM porta Revalidator (a revalidação estrutural local — validate/topo
// — apanha-a). Prova que o grafo editado é revalidado (o gate original só validava o plano
// de entrada, não o editado).
func TestEdit_LocalStructuralRevalidationRejectsCycle(t *testing.T) {
	ctx := context.Background()
	plan := samplePlan("run-cycle", risk.ClassGray)

	// Edição que introduz um ciclo a↔b (estruturalmente inválido).
	cyclic := PlanDecision{
		Verdict:  VerdictEdit,
		Approver: "human-editor",
		RevisedNodes: []PlanNode{
			{TaskID: "a", Class: risk.ClassGray, Capability: "http.get", Preview: "p", Resource: "https://x/a"},
			{TaskID: "b", Class: risk.ClassGray, Capability: "http.get", Preview: "p", Resource: "https://x/b"},
		},
		RevisedEdges:  [][2]string{{"a", "b"}, {"b", "a"}},
		ReviewedNodes: []string{"a", "b"},
	}
	reviewer := &fakeReviewer{dec: cyclic}
	ch := &spyChannel{approved: true, approver: "human-editor"}
	gate, err := NewPlanGate(fakeOracle{level: autonomy.L1}, ch, WithReviewer(reviewer))
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}
	dec, err := gate.Approve(ctx, plan)
	if !errors.Is(err, ErrRevalidationFailed) {
		t.Fatalf("edicao ciclica: esperava ErrRevalidationFailed, obtive %v", err)
	}
	if dec.Verdict != VerdictReject {
		t.Fatalf("edicao ciclica: verdict=%s (esperava reject)", dec.Verdict)
	}
	if ch.callCount() != 0 {
		t.Fatalf("canal chamado apesar do ciclo: %d (esperava 0)", ch.callCount())
	}
}

// TEST (AOS-236 / AOS-120) NÓ DANGER FORÇA CARD INDIVIDUAL POR EFEITO: cada nó danger tem
// o SEU card (nunca lote); um danger irreversível traz DualControlRequired, autorizado
// pelo [approvalcard.DualControlCollector] com dois aprovadores DISTINTOS — tudo local,
// SEM round-trip ao LLM.
func TestDanger_ForcesIndividualEffectCardWithDualControl(t *testing.T) {
	ctx := context.Background()
	plan := samplePlan("run-danger", risk.ClassGray)
	// O nó "d" é danger + irreversível (um efeito concreto perigoso).
	plan.Nodes[3].Class = risk.ClassDanger
	plan.Nodes[3].Irreversible = true
	plan.Nodes[3].Preview = "cap:http.post -> https://x/delete-all"
	plan.Nodes[3].Resource = "https://x/delete-all"

	card, err := BuildPlanCard(plan)
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	danger := card.DangerEffectCards()
	if len(danger) != 1 {
		t.Fatalf("cards danger: %d (esperava 1 — um card individual por efeito danger)", len(danger))
	}
	dc := danger[0]
	if dc.Batch {
		t.Fatal("o card danger e de LOTE (esperava individual — danger nunca agrupa, ADR-013)")
	}
	if !dc.DualControlRequired {
		t.Fatal("card danger irreversivel sem DualControlRequired (esperava true)")
	}
	// O card individual é o efeito CONCRETO resolvido do nó (redigido), não um genérico.
	if dc.Resource != "https://x/delete-all" {
		t.Fatalf("efeito concreto no card: resource=%q", dc.Resource)
	}

	// Autoriza o efeito danger via dual-control (AOS-120) — dois aprovadores DISTINTOS,
	// pelo hitl.Channel REAL. Nenhum LLM no caminho.
	ch, _ := realChannel(t,
		[]approvalStep{{approver: "approver-1", approved: true}, {approver: "approver-2", approved: true}},
		map[string]byte{"approver-1": 11, "approver-2": 22})
	coll, err := approvalcard.NewDualControlCollector(ch)
	if err != nil {
		t.Fatalf("NewDualControlCollector: %v", err)
	}
	decision, err := coll.Authorize(ctx, dc)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !decision.Authorized || len(decision.Approvers) != 2 {
		t.Fatalf("dual-control do efeito danger: authorized=%v approvers=%v", decision.Authorized, decision.Approvers)
	}
	if decision.Approvers[0] == decision.Approvers[1] {
		t.Fatalf("dual-control: aprovadores nao-distintos %v", decision.Approvers)
	}
}

// TEST (AOS-236) TRIAGEM POR RISCO — APROVAR SEM REVER UM NÓ FORÇADO É RECUSADO: com a
// imposição ligada, um nó Class >= gray (ou capability_gap) por rever recusa a aprovação
// fail-closed. FALHA-ANTES: sem a imposição, a mesma aprovação incompleta passaria.
func TestForcedReview_ApproveWithoutReviewingForcedNodeRejected(t *testing.T) {
	ctx := context.Background()
	plan := samplePlan("run-triage", risk.ClassGray) // a,b,c,d todos gray → todos forçados
	// Marca "a" como safe MAS com capability_gap → continua FORÇADO por gap.
	plan.Nodes[0].Class = risk.ClassSafe
	plan.Nodes[0].CapabilityGap = true

	// O card deve marcar todos forçados: a (gap), b/c/d (gray).
	card, err := BuildPlanCard(plan)
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	forced := card.ForcedTaskIDs()
	if len(forced) != 4 {
		t.Fatalf("nos forcados: %v (esperava 4 — a[gap]+b+c+d)", forced)
	}

	// Reviewer aprova mas SÓ reviu 3 dos 4 forçados (falta "d").
	incomplete := &fakeReviewer{dec: PlanDecision{
		Verdict:       VerdictApprove,
		Approver:      "human",
		ReviewedNodes: []string{"a", "b", "c"},
	}}
	ch := &spyChannel{approved: true, approver: "human"}
	gate, err := NewPlanGate(fakeOracle{level: autonomy.L0}, ch, WithReviewer(incomplete), WithForcedReview())
	if err != nil {
		t.Fatalf("NewPlanGate: %v", err)
	}
	dec, err := gate.Approve(ctx, plan)
	if !errors.Is(err, ErrForcedReviewMissing) {
		t.Fatalf("no forcado por rever: esperava ErrForcedReviewMissing, obtive %v", err)
	}
	if dec.Verdict != VerdictReject {
		t.Fatalf("no forcado por rever: verdict=%s (esperava reject)", dec.Verdict)
	}
	if ch.callCount() != 0 {
		t.Fatalf("canal chamado apesar de no forcado por rever: %d (esperava 0)", ch.callCount())
	}

	// FALHA-ANTES: o MESMO reviewer incompleto, SEM WithForcedReview, é aprovado.
	ch2 := &spyChannel{approved: true, approver: "human"}
	gateNoEnforce, _ := NewPlanGate(fakeOracle{level: autonomy.L0}, ch2, WithReviewer(&fakeReviewer{dec: PlanDecision{
		Verdict: VerdictApprove, Approver: "human", ReviewedNodes: []string{"a", "b", "c"},
	}}))
	decNo, err := gateNoEnforce.Approve(ctx, plan)
	if err != nil || decNo.Verdict != VerdictApprove {
		t.Fatalf("FALHA-ANTES: sem imposicao a aprovacao incompleta devia passar; verdict=%s err=%v", decNo.Verdict, err)
	}

	// COM todos os forçados revistos, a aprovação passa.
	complete := &fakeReviewer{dec: PlanDecision{
		Verdict: VerdictApprove, Approver: "human", ReviewedNodes: []string{"a", "b", "c", "d"},
	}}
	ch3 := &spyChannel{approved: true, approver: "human"}
	gateOK, _ := NewPlanGate(fakeOracle{level: autonomy.L0}, ch3, WithReviewer(complete), WithForcedReview())
	decOK, err := gateOK.Approve(ctx, plan)
	if err != nil {
		t.Fatalf("Approve (todos revistos): %v", err)
	}
	if decOK.Verdict != VerdictApprove {
		t.Fatalf("todos forcados revistos: verdict=%s (esperava approve)", decOK.Verdict)
	}
	if ch3.callCount() != 1 {
		t.Fatalf("canal nao chamado com todos revistos: %d (esperava 1)", ch3.callCount())
	}
}

// TEST (AOS-236 / AOS-095) OVERRIDE-RATE REGISTADO: a taxa de override (Overrides/Prompted)
// conta as aprovações HUMANAS contra o piso; a auto-aprovação por nível NÃO conta (não
// houve prompt). Reutiliza o [risk.Metrics] (o SLI anti rubber-stamping de AOS-074/095).
func TestOverrideRate_RecordedForHumanGateNotAutoApprove(t *testing.T) {
	ctx := context.Background()
	m := &risk.Metrics{}

	// (1) Aprovação humana (L0 gray → gate humano; canal aprova) → Prompted+1, Overrides+1.
	gApprove, _ := NewPlanGate(fakeOracle{level: autonomy.L0}, &spyChannel{approved: true, approver: "h"}, WithMetrics(m))
	if _, err := gApprove.Approve(ctx, samplePlan("run-ov1", risk.ClassGray)); err != nil {
		t.Fatalf("Approve aprovada: %v", err)
	}
	// (2) Recusa humana (canal nega) → Prompted+1, Denials+1 (NÃO conta override).
	gReject, _ := NewPlanGate(fakeOracle{level: autonomy.L0}, &spyChannel{approved: false}, WithMetrics(m))
	if _, err := gReject.Approve(ctx, samplePlan("run-ov2", risk.ClassGray)); err != nil {
		t.Fatalf("Approve recusada: %v", err)
	}
	// (3) Auto-aprovação por nível (L4 gray → corre, sem prompt) → NENHUM contador muda.
	gAuto, _ := NewPlanGate(fakeOracle{level: autonomy.L4}, &spyChannel{approved: true, approver: "h"}, WithMetrics(m))
	decAuto, err := gAuto.Approve(ctx, samplePlan("run-ov3", risk.ClassGray))
	if err != nil {
		t.Fatalf("Approve auto: %v", err)
	}
	if !decAuto.AutoApproved {
		t.Fatalf("esperava auto-aprovacao (L4 gray); dec=%+v", decAuto)
	}

	prompted, overrides, _, denials, _, rate := m.Snapshot()
	if prompted != 2 {
		t.Fatalf("Prompted=%d (esperava 2 — dois gates humanos, a auto-aprovacao nao conta)", prompted)
	}
	if overrides != 1 {
		t.Fatalf("Overrides=%d (esperava 1 — so a aprovacao humana)", overrides)
	}
	if denials != 1 {
		t.Fatalf("Denials=%d (esperava 1 — a recusa do canal)", denials)
	}
	if rate != 0.5 {
		t.Fatalf("OverrideRate=%v (esperava 0.5 = 1/2)", rate)
	}
}

// TEST (AOS-236) CUSTO POR RAMO VISÍVEL NO CARD: o custo por-nó (embutido em [PlanNode.Cost]
// ou via [WithNodeCost]) aparece no card por-nó correspondente; a opção tem precedência
// sobre o embutido. FALHA-ANTES: um nó sem custo não exibe EstimatedCost.
func TestCostPerBranch_VisibleInCard(t *testing.T) {
	plan := samplePlan("run-cost", risk.ClassGray)
	// Custo POR RAMO embutido no nó "b".
	plan.Nodes[1].Cost = &CostEstimate{EstimatedTokens: 500, MicroUSD: 21}
	// O nó "c" recebe custo via opção (deve ter precedência mesmo se embutido existir).
	plan.Nodes[2].Cost = &CostEstimate{EstimatedTokens: 1, MicroUSD: 1}

	card, err := BuildPlanCard(plan,
		WithEstimatedCost(9000, 999), // agregado do plano
		WithNodeCost("c", 800, 33),   // por-ramo do nó c (precedência sobre o embutido)
	)
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	byID := map[string]approvalcard.ApprovalCard{}
	for i, id := range card.Order {
		byID[id] = card.NodeCards[i]
	}
	// Nó "b": custo embutido visível.
	if byID["b"].EstimatedCost == nil || byID["b"].EstimatedCost.EstimatedTokens != 500 {
		t.Fatalf("custo por ramo do no b: %+v (esperava 500 tokens)", byID["b"].EstimatedCost)
	}
	// Nó "c": a opção WithNodeCost tem precedência sobre o embutido (800, não 1).
	if byID["c"].EstimatedCost == nil || byID["c"].EstimatedCost.EstimatedTokens != 800 || byID["c"].EstimatedCost.MicroUSD != 33 {
		t.Fatalf("custo por ramo do no c: %+v (esperava 800/33 — precedencia da opcao)", byID["c"].EstimatedCost)
	}
	// Nó "a": sem custo por ramo → nil (falha-antes: nao inventa custo).
	if byID["a"].EstimatedCost != nil {
		t.Fatalf("no a sem custo por ramo deveria ter EstimatedCost nil, obtive %+v", byID["a"].EstimatedCost)
	}
	// O agregado do plano continua distinto do por-ramo.
	if card.EstimatedCost == nil || card.EstimatedCost.EstimatedTokens != 9000 {
		t.Fatalf("custo agregado do plano: %+v (esperava 9000)", card.EstimatedCost)
	}
}

// TEST (AOS-236) COLAPSÁVEL vs FORÇADO: nós safe sem gap são colapsáveis; gray/danger/gap
// são forçados. Modela o estado de triagem por-nó no card.
func TestTriage_CollapsibleVsForcedModelled(t *testing.T) {
	plan := Plan{
		RunID: "run-tri", Agent: "agent:planner", Domain: "http",
		Nodes: []PlanNode{
			{TaskID: "safe", Class: risk.ClassSafe, Capability: "fs.read", Preview: "p", Resource: "/tmp/x"},
			{TaskID: "gap", Class: risk.ClassSafe, CapabilityGap: true, Capability: "fs.read", Preview: "p", Resource: "/tmp/y"},
			{TaskID: "gray", Class: risk.ClassGray, Capability: "http.get", Preview: "p", Resource: "https://x"},
			{TaskID: "danger", Class: risk.ClassDanger, Capability: "http.post", Preview: "p", Resource: "https://y"},
		},
	}
	card, err := BuildPlanCard(plan)
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	want := map[string]bool{"safe": false, "gap": true, "gray": true, "danger": true}
	got := map[string]bool{}
	for _, r := range card.NodeReviews {
		got[r.TaskID] = r.Forced
		if r.Forced == r.Collapsible() {
			t.Fatalf("no %q: Forced e Collapsible coincidem (%v)", r.TaskID, r.Forced)
		}
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("triagem do no %q: Forced=%v (esperava %v)", id, got[id], w)
		}
	}
	if len(card.NodeReviews) != 4 {
		t.Fatalf("node_reviews: %d (esperava 4)", len(card.NodeReviews))
	}
}
