package replan

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// ---------------------------------------------------------------------------
// Fakes de porta (in-memory, observáveis). Nenhum toca no modelo nem no store
// real: as portas são o seam que o wiring liga em produção.
// ---------------------------------------------------------------------------

// fakeBudget modela a árvore de orçamento. denyOverUSD > 0 recusa qualquer reserva
// cujo custo em micro-dólares o exceda — o fail-closed do residual da árvore.
type fakeBudget struct {
	denyOverUSD int64
	reserves    int
	commits     int
	releases    int
	lastResID   string
}

func (b *fakeBudget) Reserve(_ context.Context, treeID string, amt Amount) (Reservation, error) {
	if b.denyOverUSD > 0 && amt.CostMicroUSD > b.denyOverUSD {
		return Reservation{}, errors.New("sem headroom")
	}
	b.reserves++
	b.lastResID = treeID + "-r"
	return Reservation{ID: b.lastResID}, nil
}
func (b *fakeBudget) Commit(_ context.Context, _ Reservation) error  { b.commits++; return nil }
func (b *fakeBudget) Release(_ context.Context, _ Reservation) error { b.releases++; return nil }

// fakeGate captura o último pedido e devolve uma decisão configurável. Para o teto
// esgotado, humanDeclines simula um humano que recusa o auto-replan.
type fakeGate struct {
	approve       bool
	humanDeclines bool
	calls         int
	lastReq       GateRequest
}

func (g *fakeGate) Review(_ context.Context, req GateRequest) (GateOutcome, error) {
	g.calls++
	g.lastReq = req
	approved := g.approve
	if req.RequireHuman && g.humanDeclines {
		approved = false
	}
	return GateOutcome{Approved: approved, HumanReviewed: req.RequireHuman}, nil
}

// fakeSched regista os node-sets suspensos/retomados, na ordem. failResumeContaining,
// se não-vazio, faz Resume falhar (sem registar) quando o node-set contém esse id — o
// seam para exercitar a compensação pós-commit (Resume do subgrafo ORIGINAL) sem que a
// própria compensação falhe (ela retoma o original, que não contém esse id).
type fakeSched struct {
	suspended            [][]string
	resumed              [][]string
	failResumeContaining string
}

func (s *fakeSched) Suspend(_ context.Context, _ string, nodes []string) error {
	s.suspended = append(s.suspended, append([]string(nil), nodes...))
	return nil
}
func (s *fakeSched) Resume(_ context.Context, _ string, nodes []string) error {
	if s.failResumeContaining != "" {
		for _, n := range nodes {
			if n == s.failResumeContaining {
				return errors.New("resume falhou (injectado)")
			}
		}
	}
	s.resumed = append(s.resumed, append([]string(nil), nodes...))
	return nil
}

// resumedContains diz se algum Resume recebeu o node_id dado.
func (s *fakeSched) resumedContains(id string) bool {
	for _, set := range s.resumed {
		for _, n := range set {
			if n == id {
				return true
			}
		}
	}
	return false
}

// fakeRecorder regista as fases emitidas, na ordem. failOnPhase, se definido, faz a
// emissão dessa fase falhar (sem registar) — o seam para exercitar a compensação
// pós-commit quando a emissão de `applied` falha DEPOIS do commit já ter debitado.
type fakeRecorder struct {
	phases      []plannerevents.ReplanPhase
	failOnPhase plannerevents.ReplanPhase
	failArmed   bool
}

func (r *fakeRecorder) RecordReplan(_ context.Context, p plannerevents.ReplanPayload) (uint64, error) {
	if r.failArmed && p.Phase == r.failOnPhase {
		return 0, errors.New("emissao falhou (injectado)")
	}
	r.phases = append(r.phases, p.Phase)
	return uint64(len(r.phases)), nil
}

// harness constrói um Coordinator com fakes e devolve-os para inspecção.
type harness struct {
	c    *Coordinator
	bud  *fakeBudget
	gate *fakeGate
	sch  *fakeSched
	rec  *fakeRecorder
}

func newHarness(t *testing.T, cfg Config, approve bool) *harness {
	t.Helper()
	h := &harness{
		bud:  &fakeBudget{},
		gate: &fakeGate{approve: approve},
		sch:  &fakeSched{},
		rec:  &fakeRecorder{},
	}
	c, err := NewCoordinator(h.bud, h.gate, h.sch, h.rec, cfg)
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}
	h.c = c
	return h
}

// baseReq é um pedido válido de re-plano (subgrafo pendente, novo subgrafo
// pendente, sem concluídos).
func baseReq() ReplanRequest {
	return ReplanRequest{
		PlanID:        "plan-1",
		TreeID:        "tree-1",
		OriginalLevel: L3,
		Level:         L2,
		Subgraph:      []string{"n2", "n3"},
		NewSubgraph:   []string{"n2b", "n3b"},
		NodeStatuses: map[string]NodeStatus{
			"n1": NodeCompleted, // histórico
			"n2": NodePending,
			"n3": NodePending,
		},
		EstimatedCost:   Amount{Tokens: 100, CostMicroUSD: 100},
		TreeBudgetTotal: Amount{Tokens: 10000, CostMicroUSD: 10000},
		NewPlanHash:     "hash-new",
	}
}

// ---------------------------------------------------------------------------
// AC: nós CONCLUÍDOS são INTOCÁVEIS — o re-plano não re-despacha o histórico.
// Falha-antes: sem o guard de imutabilidade (guardImmutable), um node_id concluído
// em NewSubgraph chegaria ao SCH.Resume e seria re-despachado.
// ---------------------------------------------------------------------------

func TestReplan_CompletedNodeInNewSubgraph_Rejected(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, true)
	req := baseReq()
	req.NewSubgraph = []string{"n1", "n2b"} // n1 está CONCLUÍDO

	_, err := h.c.Replan(context.Background(), req)
	if !errors.Is(err, ErrCompletedNodeImmutable) {
		t.Fatalf("esperava ErrCompletedNodeImmutable, obtive %v", err)
	}
	// O guard corta ANTES de qualquer efeito: nada reservado, nada suspenso/retomado.
	if h.sch.resumedContains("n1") {
		t.Fatal("no concluido n1 foi retomado — historico re-despachado")
	}
	if h.bud.reserves != 0 || len(h.sch.suspended) != 0 || len(h.rec.phases) != 0 {
		t.Fatalf("efeitos colaterais apos guard: reserves=%d suspended=%d phases=%d",
			h.bud.reserves, len(h.sch.suspended), len(h.rec.phases))
	}
}

func TestReplan_CompletedNodeInSubgraph_Rejected(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, true)
	req := baseReq()
	req.Subgraph = []string{"n1", "n2"} // n1 CONCLUÍDO no subgrafo a substituir

	_, err := h.c.Replan(context.Background(), req)
	if !errors.Is(err, ErrCompletedNodeImmutable) {
		t.Fatalf("esperava ErrCompletedNodeImmutable, obtive %v", err)
	}
}

// Controlo (contraste falsificável): um re-plano só sobre nós pendentes aplica e
// retoma APENAS o novo subgrafo — nunca o nó concluído n1.
func TestReplan_PendingOnly_AppliesAndNeverResumesHistory(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, true)
	res, err := h.c.Replan(context.Background(), baseReq())
	if err != nil {
		t.Fatalf("Replan: %v", err)
	}
	if !res.Applied {
		t.Fatal("esperava Applied=true")
	}
	if h.sch.resumedContains("n1") {
		t.Fatal("no concluido n1 foi retomado num re-plano valido")
	}
	if !h.sch.resumedContains("n2b") || !h.sch.resumedContains("n3b") {
		t.Fatalf("novo subgrafo nao foi retomado: %+v", h.sch.resumed)
	}
}

// ---------------------------------------------------------------------------
// AC: re-planos ANINHADOS contam para o MESMO tecto (mesmo contador por-árvore).
// Falha-antes: um contador por-invocação daria count=1 em ambos e criaria dois
// estados de árvore; aqui o segundo re-plano (mesmo tree_id) tem de dar count=2.
// ---------------------------------------------------------------------------

func TestReplan_NestedIncrementsSameCounter(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, true)

	r1, err := h.c.Replan(context.Background(), baseReq())
	if err != nil {
		t.Fatalf("re-plano 1: %v", err)
	}
	if r1.ReplanCount != 1 {
		t.Fatalf("re-plano 1: count=%d, quer 1", r1.ReplanCount)
	}

	// Re-plano ANINHADO: mesma árvore (tree-1).
	r2, err := h.c.Replan(context.Background(), baseReq())
	if err != nil {
		t.Fatalf("re-plano 2 (aninhado): %v", err)
	}
	if r2.ReplanCount != 2 {
		t.Fatalf("re-plano aninhado: count=%d, quer 2 (mesmo tecto)", r2.ReplanCount)
	}
	// Um ÚNICO estado de árvore — não um contador novo por invocação.
	if got := len(h.c.trees); got != 1 {
		t.Fatalf("estados de arvore=%d, quer 1 (aninhados partilham o tecto)", got)
	}
	if got := h.c.ReplanCount("tree-1"); got != 2 {
		t.Fatalf("ReplanCount(tree-1)=%d, quer 2", got)
	}

	// Uma árvore DIFERENTE tem contador independente (prova que a chave é o tree_id).
	other := baseReq()
	other.TreeID = "tree-2"
	r3, err := h.c.Replan(context.Background(), other)
	if err != nil {
		t.Fatalf("re-plano tree-2: %v", err)
	}
	if r3.ReplanCount != 1 {
		t.Fatalf("tree-2: count=%d, quer 1 (arvore distinta)", r3.ReplanCount)
	}
}

// ---------------------------------------------------------------------------
// AC: esgotamento do tecto FORÇA revisão humana (e trava o loop permanente — DoD).
// Falha-antes: sem o gatilho de tecto, o gate nunca receberia RequireHuman=true e o
// re-plano automático poderia repetir-se indefinidamente.
// ---------------------------------------------------------------------------

func TestReplan_CeilingExhaustionForcesHumanReview(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 1}, true)

	// 1º re-plano: dentro do tecto → sem revisão humana forçada.
	r1, err := h.c.Replan(context.Background(), baseReq())
	if err != nil {
		t.Fatalf("re-plano 1: %v", err)
	}
	if r1.RequiredHuman {
		t.Fatal("re-plano 1 nao devia forcar revisao humana (dentro do tecto)")
	}
	if h.gate.lastReq.RequireHuman {
		t.Fatal("gate recebeu RequireHuman=true dentro do tecto")
	}

	// 2º re-plano: count=2 > tecto=1 → revisão humana FORÇADA no gate.
	r2, err := h.c.Replan(context.Background(), baseReq())
	if err != nil {
		t.Fatalf("re-plano 2: %v", err)
	}
	if !r2.RequiredHuman {
		t.Fatal("re-plano 2 devia forcar revisao humana (tecto esgotado)")
	}
	if !h.gate.lastReq.RequireHuman {
		t.Fatal("gate NAO recebeu RequireHuman=true apos esgotar o tecto")
	}
}

// DoD: sem loop de re-plano permanente. Uma vez esgotado o tecto, um humano que
// recuse trava o re-plano automático (não aplica, liberta o orçamento).
func TestReplan_NoPermanentLoop_HumanCanStop(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 1}, true)
	h.gate.humanDeclines = true // humano recusa quando a revisão é forçada

	if _, err := h.c.Replan(context.Background(), baseReq()); err != nil {
		t.Fatalf("re-plano 1: %v", err)
	}
	// 2º re-plano força humano; humano recusa → rejeitado, sem applied.
	_, err := h.c.Replan(context.Background(), baseReq())
	if !errors.Is(err, ErrReplanRejected) {
		t.Fatalf("esperava ErrReplanRejected (humano travou o loop), obtive %v", err)
	}
	// applied só foi emitido no 1º re-plano (requested+applied); o 2º só requested.
	// phases: [requested, applied, requested]
	if got := countPhase(h.rec.phases, plannerevents.ReplanApplied); got != 1 {
		t.Fatalf("applied emitido %d vezes, quer 1 (2o re-plano nao aplica)", got)
	}
	// Orçamento do 2º re-plano libertado (sem leak).
	if h.bud.releases < 1 {
		t.Fatalf("reserva do re-plano recusado nao foi libertada: releases=%d", h.bud.releases)
	}
}

// AC (variante): custo acumulado acima da fracção do orçamento força revisão humana
// mesmo dentro do tecto de contagem.
func TestReplan_AccumulatedCostFractionForcesHumanReview(t *testing.T) {
	// Fracção = 150 bps = 1.5% de 10000 = 150. Custo por re-plano = 100 (USD).
	h := newHarness(t, Config{MaxReplansPerTree: 100, HumanReviewCostBasisPoints: 150}, true)

	r1, err := h.c.Replan(context.Background(), baseReq()) // acumulado 100 <= 150
	if err != nil {
		t.Fatalf("re-plano 1: %v", err)
	}
	if r1.RequiredHuman {
		t.Fatal("re-plano 1 nao devia forcar humano (100 <= 1.5%%)")
	}
	r2, err := h.c.Replan(context.Background(), baseReq()) // acumulado 200 > 150
	if err != nil {
		t.Fatalf("re-plano 2: %v", err)
	}
	if !r2.RequiredHuman {
		t.Fatal("re-plano 2 devia forcar humano (custo acumulado > fracao)")
	}
}

// ---------------------------------------------------------------------------
// AC: autonomia do re-plano <= original.
// Falha-antes: sem o guard, um re-plano poderia escalar autonomia (L2→L3) e
// contornar a supervisão do plano original.
// ---------------------------------------------------------------------------

func TestReplan_AutonomyNeverExceedsOriginal(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, true)
	req := baseReq()
	req.OriginalLevel = L2
	req.Level = L3 // pede MAIS autonomia que o original

	_, err := h.c.Replan(context.Background(), req)
	if !errors.Is(err, ErrAutonomyExceedsOriginal) {
		t.Fatalf("esperava ErrAutonomyExceedsOriginal, obtive %v", err)
	}
	// Rejeitado antes de qualquer efeito.
	if h.bud.reserves != 0 || len(h.rec.phases) != 0 {
		t.Fatalf("efeitos apos recusa de autonomia: reserves=%d phases=%d", h.bud.reserves, len(h.rec.phases))
	}
}

func TestReplan_AutonomyEqualOrLower_OK(t *testing.T) {
	for _, lvl := range []Level{L2, L1, L0} {
		h := newHarness(t, Config{MaxReplansPerTree: 5}, true)
		req := baseReq()
		req.OriginalLevel = L2
		req.Level = lvl
		if _, err := h.c.Replan(context.Background(), req); err != nil {
			t.Fatalf("Level %s <= original L2 devia passar: %v", lvl, err)
		}
		if h.gate.lastReq.Level != lvl {
			t.Fatalf("gate recebeu nivel %s, quer %s", h.gate.lastReq.Level, lvl)
		}
	}
}

// ---------------------------------------------------------------------------
// DoD: orçamento residual respeitado — a árvore recusa fail-closed.
// ---------------------------------------------------------------------------

func TestReplan_ResidualBudgetFailClosed(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, true)
	h.bud.denyOverUSD = 50 // residual só cobre 50; o re-plano pede 100
	_, err := h.c.Replan(context.Background(), baseReq())
	if !errors.Is(err, ErrNoResidualBudget) {
		t.Fatalf("esperava ErrNoResidualBudget, obtive %v", err)
	}
	// Sem reserva concedida → nada emitido, nada suspenso.
	if len(h.rec.phases) != 0 || len(h.sch.suspended) != 0 {
		t.Fatalf("efeitos apos recusa de orcamento: phases=%d suspended=%d", len(h.rec.phases), len(h.sch.suspended))
	}
}

// ---------------------------------------------------------------------------
// Emissão + SCH: requested ANTES de applied; suspende antes, retoma o novo depois.
// ---------------------------------------------------------------------------

func TestReplan_EmitsRequestedThenApplied_AndSuspendsResumes(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, true)
	res, err := h.c.Replan(context.Background(), baseReq())
	if err != nil || !res.Applied {
		t.Fatalf("Replan applied=%v err=%v", res.Applied, err)
	}
	if len(h.rec.phases) != 2 ||
		h.rec.phases[0] != plannerevents.ReplanRequested ||
		h.rec.phases[1] != plannerevents.ReplanApplied {
		t.Fatalf("fases emitidas = %v, quer [requested, applied]", h.rec.phases)
	}
	// Suspende o subgrafo original; retoma o novo.
	if len(h.sch.suspended) != 1 || len(h.sch.resumed) != 1 {
		t.Fatalf("suspend=%d resume=%d, quer 1 e 1", len(h.sch.suspended), len(h.sch.resumed))
	}
	if h.bud.commits != 1 {
		t.Fatalf("commit=%d, quer 1", h.bud.commits)
	}
}

func TestReplan_GateRejection_ReleasesBudget_NoApplied(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, false) // gate recusa
	_, err := h.c.Replan(context.Background(), baseReq())
	if !errors.Is(err, ErrReplanRejected) {
		t.Fatalf("esperava ErrReplanRejected, obtive %v", err)
	}
	if countPhase(h.rec.phases, plannerevents.ReplanApplied) != 0 {
		t.Fatal("emitiu applied num re-plano recusado")
	}
	if h.bud.releases != 1 || h.bud.commits != 0 {
		t.Fatalf("orcamento: releases=%d commits=%d, quer 1 e 0", h.bud.releases, h.bud.commits)
	}
	// O subgrafo original é retomado (não fica preso suspenso).
	if len(h.sch.resumed) != 1 {
		t.Fatalf("resume=%d, quer 1 (retomar original apos recusa)", len(h.sch.resumed))
	}
}

// ---------------------------------------------------------------------------
// Guardas de construção e validação.
// ---------------------------------------------------------------------------

func TestNewCoordinator_NilPortFailClosed(t *testing.T) {
	if _, err := NewCoordinator(nil, &fakeGate{}, &fakeSched{}, &fakeRecorder{}, Config{}); !errors.Is(err, ErrNilPort) {
		t.Fatalf("porta nil devia falhar: %v", err)
	}
}

func TestReplan_EmptyRequestRejected(t *testing.T) {
	h := newHarness(t, Config{}, true)
	if _, err := h.c.Replan(context.Background(), ReplanRequest{}); !errors.Is(err, ErrEmptyRequest) {
		t.Fatalf("pedido vazio devia falhar: %v", err)
	}
}

func TestReplan_InvalidLevelRejected(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, true)
	req := baseReq()
	req.OriginalLevel = Level(9) // fora de L0-L5
	if _, err := h.c.Replan(context.Background(), req); !errors.Is(err, ErrInvalidLevel) {
		t.Fatalf("nivel invalido devia falhar: %v", err)
	}
}

// ---------------------------------------------------------------------------
// AC1 (furo central): a autonomia "<= original" está ANCORADA À ÁRVORE, não ao
// pedido. Um re-plano ANINHADO não pode re-declarar um OriginalLevel superior ao
// FIXADO na primeira invocação para escalar autonomia.
// Falha-antes: sem checkPinnedLevel, o guard per-request só compara Level vs
// OriginalLevel DENTRO do mesmo pedido; um aninhado com OriginalLevel=L5,Level=L5
// passa o guard, reserva orçamento e chega ao gate a L5 — escalada por aninhamento.
// ---------------------------------------------------------------------------

func TestReplan_NestedCannotEscalateAutonomyAbovePinnedLevel(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, true)

	// 1ª invocação da árvore: fixa o nível original em L2.
	first := baseReq()
	first.OriginalLevel = L2
	first.Level = L2
	if _, err := h.c.Replan(context.Background(), first); err != nil {
		t.Fatalf("re-plano 1 (fixa L2): %v", err)
	}
	if h.gate.lastReq.Level != L2 {
		t.Fatalf("gate do 1o re-plano recebeu %s, quer L2", h.gate.lastReq.Level)
	}
	reservesAfterFirst := h.bud.reserves

	// 2º re-plano ANINHADO (mesma árvore) tenta escalar: OriginalLevel=L5, Level=L5.
	// O guard per-request (Level>OriginalLevel) NÃO apanha isto — só a âncora à árvore.
	esc := baseReq()
	esc.OriginalLevel = L5
	esc.Level = L5
	_, err := h.c.Replan(context.Background(), esc)
	if !errors.Is(err, ErrAutonomyExceedsOriginal) {
		t.Fatalf("aninhado a escalar autonomia devia dar ErrAutonomyExceedsOriginal, obtive %v", err)
	}
	// Recusa ANTES de qualquer débito: nenhuma reserva nova, gate nunca viu L5.
	if h.bud.reserves != reservesAfterFirst {
		t.Fatalf("escalada recusada mas reservou: reserves=%d, quer %d", h.bud.reserves, reservesAfterFirst)
	}
	if h.gate.lastReq.Level == L5 {
		t.Fatal("gate recebeu L5 — autonomia escalou por aninhamento")
	}
}

// Contraste: um aninhado que RESPEITA o tecto fixado (Level <= originalLevel da
// árvore) passa — prova que a âncora não bloqueia re-planos legítimos.
func TestReplan_NestedAtOrBelowPinnedLevel_OK(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, true)
	first := baseReq()
	first.OriginalLevel = L3
	first.Level = L3
	if _, err := h.c.Replan(context.Background(), first); err != nil {
		t.Fatalf("re-plano 1: %v", err)
	}
	nested := baseReq()
	nested.OriginalLevel = L3
	nested.Level = L1 // reduz autonomia — legítimo
	if _, err := h.c.Replan(context.Background(), nested); err != nil {
		t.Fatalf("aninhado a reduzir autonomia devia passar: %v", err)
	}
	if h.gate.lastReq.Level != L1 {
		t.Fatalf("gate recebeu %s, quer L1", h.gate.lastReq.Level)
	}
}

// ---------------------------------------------------------------------------
// CA2 (fecho do fail-open): um nó do subgrafo a substituir AUSENTE do snapshot é
// recusado fail-closed — não tratado como pendente por omissão.
// Falha-antes: sem o check `!ok`, um id ausente resolvia para NodePending (iota 0),
// passava o guard e chegava ao SCH re-despachando potencial histórico.
// ---------------------------------------------------------------------------

func TestReplan_AbsentNodeInSnapshot_FailClosed(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, true)
	req := baseReq()
	// n3 está no subgrafo a substituir mas AUSENTE do snapshot.
	req.NodeStatuses = map[string]NodeStatus{
		"n1": NodeCompleted,
		"n2": NodePending,
		// "n3" ausente de propósito
	}
	_, err := h.c.Replan(context.Background(), req)
	if !errors.Is(err, ErrNodeStatusUnknown) {
		t.Fatalf("no ausente do snapshot devia dar ErrNodeStatusUnknown, obtive %v", err)
	}
	// Recusa antes de qualquer efeito.
	if h.bud.reserves != 0 || len(h.sch.suspended) != 0 || len(h.rec.phases) != 0 {
		t.Fatalf("efeitos apos recusa fail-closed: reserves=%d suspended=%d phases=%d",
			h.bud.reserves, len(h.sch.suspended), len(h.rec.phases))
	}
}

// Contraste: snapshot nil também recusa (não é "tudo pendente por omissão").
func TestReplan_NilSnapshot_FailClosed(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, true)
	req := baseReq()
	req.NodeStatuses = nil
	if _, err := h.c.Replan(context.Background(), req); !errors.Is(err, ErrNodeStatusUnknown) {
		t.Fatalf("snapshot nil devia dar ErrNodeStatusUnknown, obtive %v", err)
	}
}

// ---------------------------------------------------------------------------
// DoD/doc (fecho do estado preso pós-commit): APÓS o commit, uma falha em retomar o
// NOVO subgrafo compensa retomando o subgrafo ORIGINAL — o DAG não fica preso.
// Falha-antes: sem a compensação, o original ficava suspenso (passo 6) sem Resume;
// resumed viria vazio e o DAG preso, contradizendo a garantia do doc.
// ---------------------------------------------------------------------------

func TestReplan_ResumeNewSubgraphFailsAfterCommit_ResumesOriginal(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, true)
	h.sch.failResumeContaining = "n2b" // Resume(NewSubgraph) falha; Resume(original) não

	res, err := h.c.Replan(context.Background(), baseReq())
	if err == nil {
		t.Fatal("esperava erro da falha de Resume pos-commit")
	}
	if res.Applied {
		t.Fatal("Applied devia ser false quando o Resume do novo subgrafo falha")
	}
	// O débito já foi committed (o re-plano foi aprovado) — não se reverte.
	if h.bud.commits != 1 {
		t.Fatalf("commits=%d, quer 1 (debito definitivo pos-aprovacao)", h.bud.commits)
	}
	// Compensação: o subgrafo ORIGINAL foi retomado — DAG não fica preso.
	if !h.sch.resumedContains("n2") || !h.sch.resumedContains("n3") {
		t.Fatalf("subgrafo original nao foi retomado em compensacao: %+v", h.sch.resumed)
	}
	// O novo subgrafo NÃO foi retomado com sucesso (a sua Resume falhou).
	if h.sch.resumedContains("n2b") {
		t.Fatal("n2b consta em resumed apesar da falha de Resume")
	}
}

func TestReplan_AppliedEmitFailsAfterCommit_ResumesOriginal(t *testing.T) {
	h := newHarness(t, Config{MaxReplansPerTree: 5}, true)
	h.rec.failOnPhase = plannerevents.ReplanApplied
	h.rec.failArmed = true

	res, err := h.c.Replan(context.Background(), baseReq())
	if err == nil {
		t.Fatal("esperava erro da falha de emissao de applied pos-commit")
	}
	if res.Applied {
		t.Fatal("Applied devia ser false quando a emissao de applied falha")
	}
	if h.bud.commits != 1 {
		t.Fatalf("commits=%d, quer 1", h.bud.commits)
	}
	// requested foi emitido; applied falhou (não consta).
	if countPhase(h.rec.phases, plannerevents.ReplanRequested) != 1 {
		t.Fatalf("requested emitido %d vezes, quer 1", countPhase(h.rec.phases, plannerevents.ReplanRequested))
	}
	if countPhase(h.rec.phases, plannerevents.ReplanApplied) != 0 {
		t.Fatal("applied consta apesar da falha de emissao")
	}
	// Compensação: original retomado.
	if !h.sch.resumedContains("n2") || !h.sch.resumedContains("n3") {
		t.Fatalf("subgrafo original nao foi retomado em compensacao: %+v", h.sch.resumed)
	}
}

// ---------------------------------------------------------------------------
// CA3 (edge de overflow): o gatilho de fracção de custo é EXACTO para orçamentos
// grandes — não falha-aberto por overflow de int64.
// Falha-antes: a comparação naive accum*10000 > total*bps transborda para tokens
// ~1e15 (produto > 9.2e18), o produto inverte de sinal e o gatilho podia dar false
// (fail-open). crossGreater (math/big) mantém a comparação correcta.
// ---------------------------------------------------------------------------

func TestExceedsFraction_NoInt64Overflow(t *testing.T) {
	const big15 = int64(1_000_000_000_000_000) // 1e15; *10000 = 1e19 > MaxInt64
	// accum == total, fracção 50% (5000 bps): accum claramente excede 50% do total.
	total := Amount{Tokens: big15}
	accum := Amount{Tokens: big15}
	if !exceedsFraction(accum, total, 5000) {
		t.Fatal("exceedsFraction deu false por overflow (fail-open) para orcamento grande")
	}
	// Abaixo da fracção: 40% do total, fracção 50% → NÃO excede (prova não-vacuosa).
	accumBelow := Amount{Tokens: big15 / 10 * 4} // 4e14 = 40%
	if exceedsFraction(accumBelow, total, 5000) {
		t.Fatal("exceedsFraction deu true abaixo da fracao (deteccao invertida)")
	}
	// Mesma verificação na dimensão monetária.
	if !exceedsFraction(Amount{CostMicroUSD: big15}, Amount{CostMicroUSD: big15}, 5000) {
		t.Fatal("exceedsFraction (USD) deu false por overflow")
	}
}

func countPhase(phases []plannerevents.ReplanPhase, want plannerevents.ReplanPhase) int {
	n := 0
	for _, p := range phases {
		if p == want {
			n++
		}
	}
	return n
}
