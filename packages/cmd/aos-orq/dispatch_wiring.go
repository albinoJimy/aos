package main

// dispatch_wiring.go — T4 do AOS-390 (ADR-024): compõe o DESPACHO GOVERNADO no serve.
//
// A materialização é admissão-pura (admite os nós no DAG, sem efeito). Este ficheiro
// compõe o plandispatch.Dispatcher sob a MESMA posse e corre o laço de re-invocação: o
// Dispatcher decide a elegibilidade de cada nó (gate + estado do ciclo de vida +
// depends_on + arestas condicionais com poda branch_not_taken + cartão + headroom) e
// entrega os nós ELEGÍVEIS ao DispatchSink — papel→Delegator.Spawn, folha→arranque.
//
// PORTAS DE PRODUÇÃO reutilizadas (derivadas do log): Gate←GateReader,
// LifecycleView←LifecycleReader, ResultView←(LifecycleResults ∪ ResultReader),
// BranchJournal←PlanRecorder.BranchJournal, BranchBudget←planbudget.TreeBudgetMeter.
// PORTAS deste composition root: Headroom (semáforo bounded — v1.1; a integração com o
// scheduler.SpawnCoordinator de AOS-028 é follow-up), CardOracle (fail-closed) e o Sink.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"

	budget "github.com/aos-ref/control-plane/budget"
	orchestrator "github.com/aos-ref/control-plane/orchestrator"
	plan "github.com/aos-ref/control-plane/orchestrator/plan"
	planbudget "github.com/aos-ref/control-plane/orchestrator/planbudget"
	plandispatch "github.com/aos-ref/control-plane/orchestrator/plandispatch"
	plannerevents "github.com/aos-ref/control-plane/orchestrator/plannerevents"
	runlifecycle "github.com/aos-ref/control-plane/runlifecycle"
	identity "github.com/aos-ref/platform/identity"
	eventstore "github.com/aos-ref/substrate/eventstore"
)

const (
	// dispatchMaxConcurrency é o tecto de concorrência do despacho neste binário — um
	// semáforo bounded (v1.1). NÃO é o max_spawn=f(headroom) de AOS-028: essa autoridade
	// (scheduler.SpawnCoordinator) tem forma incompatível (RequestSpawn/MaxSpawn) e a sua
	// integração é follow-up. Declarado, não escondido.
	dispatchMaxConcurrency = 16
	// dispatchMaxPasses limita o laço de re-invocação nesta execução one-shot. Sem um
	// executor a concluir nós, o ponto fixo alcança-se numa ou duas passagens (despacha os
	// elegíveis, depois nada muda); o tecto é uma rede de segurança, não a cadência.
	dispatchMaxPasses = 64
	// dispatchNodeBudgetTokens é o limite do nó de orçamento criado por nó do plano, para
	// o débito da avaliação de condições (o TreeBudgetMeter reserva contra o node_id). Um
	// tecto local generoso; o tecto real vem do plano de controlo.
	dispatchNodeBudgetTokens = 100_000
)

// clampU64ToInt64 satura um uint64 UNTRUSTED em MaxInt64 em vez de transbordar para
// negativo — o mesmo clamp de planmaterialize, para a estimativa de orçamento do nó que
// alimenta a reserva do spawn do papel.
func clampU64ToInt64(u uint64) int64 {
	if u > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(u)
}

// boundedHeadroom é um tecto de concorrência in-memory (semáforo). Satisfaz
// plandispatch.Headroom com a disciplina TOCTOU que a porta exige: Available é advisory,
// Acquire é a autoridade atómica.
type boundedHeadroom struct {
	mu   sync.Mutex
	max  int
	used int
}

func (h *boundedHeadroom) Available(context.Context) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.max - h.used, nil
}

func (h *boundedHeadroom) Acquire(context.Context) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.used >= h.max {
		return false, nil
	}
	h.used++
	return true, nil
}

func (h *boundedHeadroom) Release(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.used > 0 {
		h.used--
	}
	return nil
}

// cardsFailClosed satisfaz plandispatch.CardOracle recusando (fail-closed). Só é
// consultada para nós marcados RequiresCard; esta composição passa needsCard=false (o
// gating de cartão danger/gap no despacho é follow-up — o gate de aprovação AOS-236 é o
// ponto onde o danger é autorizado, a montante), pelo que não é consultada em prática.
type cardsFailClosed struct{}

func (cardsFailClosed) Cleared(context.Context, string, string) (bool, error) { return false, nil }

// combinedResults funde os dois observáveis de resultado de produção numa só ResultView:
// terminal_state (derivado da vista do ciclo de vida) e verdict (dos factos
// plan.verdict_recorded). Uma condição de ADR-022 §2.1 pode observar qualquer dos dois;
// o Dispatcher recebe UMA ResultView, logo funde-se aqui.
type combinedResults struct {
	terminal *plandispatch.LifecycleResults
	verdicts *runlifecycle.ResultSnapshot
}

func (c combinedResults) Result(ctx context.Context, planID, nodeID string) (plandispatch.NodeResultRecord, bool, error) {
	tr, tok, err := c.terminal.Result(ctx, planID, nodeID)
	if err != nil {
		return plandispatch.NodeResultRecord{}, false, err
	}
	vr, vok, err := c.verdicts.Result(ctx, planID, nodeID)
	if err != nil {
		return plandispatch.NodeResultRecord{}, false, err
	}
	if !tok && !vok {
		return plandispatch.NodeResultRecord{}, false, nil
	}
	return plandispatch.NodeResultRecord{
		Terminal: tr.Terminal,
		Verdict:  vr.Verdict,
		Subjects: vr.Subjects,
		Metrics:  vr.Metrics,
	}, true, nil
}

// dispatchSink é o efeito por-nó, a jusante de toda a elegibilidade. Papel-que-expande →
// Delegator.Spawn (cunha a NHI filha clampada e reserva o orçamento herdado) + MarkRunning;
// folha → MarkRunning (entrega à execução). O MarkRunning tira o nó de pending para não
// ser re-despachado na passagem seguinte.
type dispatchSink struct {
	del         *orchestrator.Delegator
	g           *orchestrator.GraphBuilder
	runID       string
	parentToken string
	kinds       map[string]plannerevents.SpawnKind
	authority   map[string][]string
	budgets     map[string]budget.Amount
}

// LIMITAÇÃO DE IDENTIDADE conhecida (papel): a NHI filha do papel pede Authority =
// tools clampadas do papel (cap:tool:*), mas o token do run neste binário traz cap:plan
// e o IssueChild exige Authority ⊆ folha-do-pai. Logo o spawn de um PAPEL falha
// fail-closed (loud) até o cutover de identidade (família AOS-278) dar ao token do run a
// autoridade com escopo de tools. O caminho de FOLHA (MarkRunning) não toca identidade e
// funciona. Fail-closed é a direcção certa: um papel que não pode cunhar NHI legítima não
// deve correr em silêncio.
func (s *dispatchSink) Dispatch(ctx context.Context, node plandispatch.Node) error {
	if s.kinds[node.NodeID] == plannerevents.SpawnRole {
		if _, err := s.del.Spawn(ctx, orchestrator.SpawnRequest{
			RunID:            s.runID,
			ParentBudgetNode: s.runID, // orçamento achatado à raiz (como a materialização fazia)
			ChildBudgetNode:  node.NodeID,
			InheritedBudget:  s.budgets[node.NodeID],
			ParentToken:      s.parentToken,
			Child: identity.ChildRequest{
				AgentID:    s.runID + "/" + node.NodeID,
				AgentClass: classeWorker,
				Authority:  s.authority[node.NodeID], // clampada, lida de plan.materialized
			},
			ChildTaskID: node.NodeID,
		}); err != nil {
			return fmt.Errorf("spawn do papel %q: %w", node.NodeID, err)
		}
		fmt.Printf("  despacho: papel %s spawnado\n", node.NodeID)
	} else {
		fmt.Printf("  despacho: folha %s a arrancar\n", node.NodeID)
	}
	// Marca o nó a correr — vale para papel e folha: sai de pending, não re-despacha.
	if err := s.g.MarkRunning(ctx, node.NodeID); err != nil {
		return fmt.Errorf("marcar %q a correr: %w", node.NodeID, err)
	}
	return nil
}

// composeEDespachar compõe o plandispatch.Dispatcher com as portas de produção e corre o
// laço de re-invocação por passagem, sob a posse deste run. Cada passagem toma um retrato
// NOVO das vistas de leitura (coerência por passagem); o laço pára no ponto fixo (uma
// passagem que não despacha nada).
func composeEDespachar(
	ctx context.Context,
	ten *runlifecycle.Tenure,
	store runlifecycle.EventStore,
	rec *runlifecycle.PlanRecorder,
	del *orchestrator.Delegator,
	bud *budget.Budget,
	parentToken string,
	doc plan.PlanDocument,
	payload plannerevents.MaterializedPayload,
	worker string,
) error {
	runID := ten.RunID()
	planID := rec.PlanID()

	// Índices por nó a partir do documento aprovado e do facto plan.materialized (a fonte
	// de verdade da autoridade clampada e do kind).
	kinds := make(map[string]plannerevents.SpawnKind, len(payload.Nodes))
	authority := make(map[string][]string, len(payload.Nodes))
	for _, n := range payload.Nodes {
		kinds[n.NodeID] = n.Kind
		authority[n.NodeID] = n.Tools
	}
	budgets := make(map[string]budget.Amount, len(doc.Nodes))
	for _, n := range doc.Nodes {
		budgets[n.NodeID] = budget.Amount{
			Tokens:       clampU64ToInt64(n.BudgetEstimate.Tokens),
			CostMicroUSD: clampU64ToInt64(n.BudgetEstimate.CostMicroUSD),
		}
	}

	// Nós de orçamento por nó do plano — o TreeBudgetMeter reserva a avaliação de condição
	// CONTRA o node_id. Tolera ErrNodeExists (um papel já terá o seu por via do spawn).
	for _, n := range payload.Nodes {
		if err := bud.AddNode(n.NodeID, runID, budget.Amount{Tokens: dispatchNodeBudgetTokens, CostMicroUSD: dispatchNodeBudgetTokens}); err != nil && !errors.Is(err, budget.ErrNodeExists) {
			return fmt.Errorf("nó de orçamento %q: %w", n.NodeID, err)
		}
	}
	meter, err := planbudget.NewTreeBudgetMeter(bud, budget.Amount{Tokens: 1, CostMicroUSD: 1})
	if err != nil {
		return fmt.Errorf("meter de orçamento de ramos: %w", err)
	}

	// GraphBuilder desta posse para o efeito de arranque (MarkRunning), re-hidratado do log.
	g, err := ten.Graph(ctx, eventstore.Producer{NHIID: "nhi:" + worker})
	if err != nil {
		return fmt.Errorf("grafo para o despacho: %w", err)
	}

	// Readers de produção (bound; Snapshot fresco por passagem).
	gateR, err := runlifecycle.NewGateReader(store, planID)
	if err != nil {
		return fmt.Errorf("gate reader: %w", err)
	}
	lifeR, err := runlifecycle.NewLifecycleReader(store, planID, runID)
	if err != nil {
		return fmt.Errorf("lifecycle reader: %w", err)
	}
	resultR, err := runlifecycle.NewResultReader(store, planID)
	if err != nil {
		return fmt.Errorf("result reader: %w", err)
	}

	journal := rec.BranchJournal()
	headroom := &boundedHeadroom{max: dispatchMaxConcurrency}
	sink := &dispatchSink{del: del, g: g, runID: runID, parentToken: parentToken, kinds: kinds, authority: authority, budgets: budgets}

	// Plano despachável (do materializado + doc). needsCard=false: ver cardsFailClosed.
	p, err := plandispatch.PlanFrom(payload, doc, func(string) bool { return false })
	if err != nil {
		return fmt.Errorf("projecção do plano despachável: %w", err)
	}

	total := 0
	for pass := 0; pass < dispatchMaxPasses; pass++ {
		gate, err := gateR.Snapshot(ctx)
		if err != nil {
			return fmt.Errorf("retrato do gate (passagem %d): %w", pass, err)
		}
		vista, err := lifeR.Snapshot(ctx)
		if err != nil {
			return fmt.Errorf("retrato do ciclo de vida (passagem %d): %w", pass, err)
		}
		verds, err := resultR.Snapshot(ctx)
		if err != nil {
			return fmt.Errorf("retrato dos veredictos (passagem %d): %w", pass, err)
		}
		lcResults, err := plandispatch.NewLifecycleResults(vista)
		if err != nil {
			return fmt.Errorf("observável terminal_state (passagem %d): %w", pass, err)
		}
		results := combinedResults{terminal: lcResults, verdicts: verds}

		d, err := plandispatch.NewDispatcher(gate, vista, headroom, cardsFailClosed{}, sink,
			plandispatch.WithConditionalBranches(results, journal, meter))
		if err != nil {
			return fmt.Errorf("dispatcher (passagem %d): %w", pass, err)
		}
		res, err := d.Dispatch(ctx, p)
		if err != nil {
			return fmt.Errorf("despacho (passagem %d): %w", pass, err)
		}
		total += res.Dispatched
		// Ponto fixo: uma passagem que não despacha nada. Sem executor a concluir nós nesta
		// execução one-shot, os nós com deps/condições por satisfazer ficam a aguardar.
		if res.Dispatched == 0 {
			break
		}
	}
	fmt.Printf("despachado: plano=%s nos_despachados=%d\n", planID, total)
	return nil
}
