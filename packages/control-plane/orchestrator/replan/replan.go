package replan

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// ---------------------------------------------------------------------------
// Tipos de domínio
// ---------------------------------------------------------------------------

// Level é o nível de autonomia L0–L5 (governance/autonomy). Aqui é apenas o
// ORDINAL comparável que sustenta o invariante "autonomia do re-plano ≤ original";
// a taxonomia completa e o oversight vivem no seu pacote (porta [Gate]).
type Level uint8

const (
	L0 Level = iota
	L1
	L2
	L3
	L4
	L5
)

const maxLevel = L5

// Valid indica se l está no intervalo fechado L0–L5. Fail-closed: um nível fora
// do intervalo é recusado ([ErrInvalidLevel]), nunca tratado como L0.
func (l Level) Valid() bool { return l <= maxLevel }

func (l Level) String() string {
	if !l.Valid() {
		return "L?"
	}
	return fmt.Sprintf("L%d", uint8(l))
}

// NodeStatus é o estado corrente de um nó do DAG, do ponto de vista do re-plano.
// Só a distinção CONCLUÍDO vs. o resto é load-bearing: um nó concluído é histórico
// imutável.
type NodeStatus uint8

const (
	// NodePending — nó ainda não despachado (futuro; alvo legítimo do re-plano).
	NodePending NodeStatus = iota
	// NodeRunning — nó em execução (futuro; não concluído).
	NodeRunning
	// NodeCompleted — nó concluído: histórico IMUTÁVEL, intocável pelo re-plano.
	NodeCompleted
)

// Amount é o custo em tokens/micro-dólares (inteiros — round-trip determinístico,
// sem vírgula flutuante). Espelha a forma do orçamento (AOS-008) sem acoplar ao
// seu módulo: o wiring adapta a árvore real de budget a [ResidualBudget].
type Amount struct {
	Tokens       int64
	CostMicroUSD int64
}

func (a Amount) add(b Amount) Amount {
	return Amount{Tokens: a.Tokens + b.Tokens, CostMicroUSD: a.CostMicroUSD + b.CostMicroUSD}
}

// Reservation é o recibo opaco de uma reserva na árvore de orçamento (porta
// [ResidualBudget]). Só o ID é observado por este pacote.
type Reservation struct {
	ID string
}

// ---------------------------------------------------------------------------
// Portas (dependências de OUTROS módulos entram por interface; o wiring liga-as)
// ---------------------------------------------------------------------------

// ResidualBudget é a porta para a ÁRVORE de orçamento partilhada (AOS-008). O
// re-plano debita o RESIDUAL da árvore: Reserve (CAS) antes de aplicar, Commit no
// applied, Release em qualquer recusa/falha. Fail-closed: Reserve DEVE devolver
// erro quando o residual da árvore não cobre amt — é assim que "orçamento residual
// respeitado" deixa de ser convenção e passa a ser enforcement.
type ResidualBudget interface {
	Reserve(ctx context.Context, treeID string, amt Amount) (Reservation, error)
	Commit(ctx context.Context, r Reservation) error
	Release(ctx context.Context, r Reservation) error
}

// GateRequest é o pedido submetido ao gate de aprovação-de-plano (AOS-121).
type GateRequest struct {
	PlanID string
	// Level é a autonomia do re-plano — já validada ≤ à do plano original.
	Level Level
	// RequireHuman força revisão humana independentemente do nível: activo quando
	// o tecto de re-planos foi esgotado ou o custo acumulado excedeu a fracção.
	RequireHuman bool
	// ResidualCost é o custo residual do re-plano submetido à decisão.
	ResidualCost Amount
}

// GateOutcome é a decisão do gate.
type GateOutcome struct {
	// Approved — o re-plano pode ser aplicado.
	Approved bool
	// HumanReviewed — a decisão passou por revisão humana (informativo/auditoria).
	HumanReviewed bool
}

// Gate é a porta para o MESMO gate que o plano original atravessou (AOS-121). O
// re-plano não tem gate próprio: reutiliza este, ao nível do plano original.
type Gate interface {
	Review(ctx context.Context, req GateRequest) (GateOutcome, error)
}

// SubgraphScheduler é a porta para o SCH (AOS-238): suspende o subgrafo no
// requested e retoma-o no applied. NÃO planeia — apenas despacha o que o
// Orquestrador materializou (fronteira ADR-018). Em recusa/falha, o coordenador
// retoma o subgrafo ORIGINAL para não o deixar preso.
type SubgraphScheduler interface {
	Suspend(ctx context.Context, planID string, nodes []string) error
	Resume(ctx context.Context, planID string, nodes []string) error
}

// ReplanRecorder é a porta de emissão dos factos `plan.replan_requested`/`applied`.
// A assinatura casa com *plannerevents.Recorder (ver a asserção de compile-time
// abaixo): o recorder real liga-se sem adaptador. Reutiliza as constantes de
// plannerevents — este pacote nunca declara literais de evento.
type ReplanRecorder interface {
	RecordReplan(ctx context.Context, p plannerevents.ReplanPayload) (uint64, error)
}

// ---------------------------------------------------------------------------
// Erros sentinela (comparáveis por errors.Is — fail-closed)
// ---------------------------------------------------------------------------

var (
	// ErrEmptyRequest — pedido sem plan_id/tree_id/subgrafo.
	ErrEmptyRequest = errors.New("replan: pedido invalido (exige plan_id, tree_id e subgrafo pendente)")
	// ErrInvalidLevel — nível de autonomia fora de L0–L5.
	ErrInvalidLevel = errors.New("replan: nivel de autonomia invalido (fora de L0-L5)")
	// ErrAutonomyExceedsOriginal — a autonomia pedida excede a do plano original.
	ErrAutonomyExceedsOriginal = errors.New("replan: autonomia do re-plano excede a do plano original (so pode reduzir)")
	// ErrCompletedNodeImmutable — um nó concluído foi incluído no re-plano.
	ErrCompletedNodeImmutable = errors.New("replan: no concluido e imutavel (historico intocavel)")
	// ErrNodeStatusUnknown — um nó do subgrafo a substituir está AUSENTE do snapshot:
	// o guard de imutabilidade não o consegue verificar, logo é recusado (fail-closed,
	// nunca tratado como pendente por omissão).
	ErrNodeStatusUnknown = errors.New("replan: no do subgrafo ausente do snapshot (fail-closed, nao verificavel)")
	// ErrNoResidualBudget — a árvore não tem orçamento residual para o re-plano.
	ErrNoResidualBudget = errors.New("replan: sem orcamento residual na arvore (fail-closed)")
	// ErrReplanRejected — o gate recusou o re-plano.
	ErrReplanRejected = errors.New("replan: gate recusou o re-plano")
	// ErrNilPort — uma porta obrigatória é nil.
	ErrNilPort = errors.New("replan: porta obrigatoria nil")
)

// ---------------------------------------------------------------------------
// Pedido e resultado
// ---------------------------------------------------------------------------

// ReplanRequest descreve um re-plano de subgrafo. O documento re-planeado (o novo
// subgrafo e o seu hash) é produzido a montante — este pacote governa, não planeia.
type ReplanRequest struct {
	// PlanID identifica o plano (stream do Event Store).
	PlanID string
	// TreeID é a ÁRVORE de orçamento/execução. O tecto e o contador de re-planos
	// são POR-ÁRVORE; re-planos aninhados partilham este id.
	TreeID string
	// OriginalLevel é o nível de autonomia L0–L5 do plano ORIGINAL (o tecto).
	OriginalLevel Level
	// Level é a autonomia pedida para o re-plano (DEVE ser ≤ OriginalLevel).
	Level Level
	// Subgraph são os nós PENDENTES a substituir. Não pode conter nós concluídos.
	Subgraph []string
	// NewSubgraph são os nós re-planeados a despachar. Não pode conter concluídos
	// (senão o re-plano re-despacharia histórico).
	NewSubgraph []string
	// NodeStatuses é o snapshot do estado corrente do DAG (node_id → estado). É a
	// FONTE do guard de imutabilidade.
	NodeStatuses map[string]NodeStatus
	// EstimatedCost é o custo residual a debitar da árvore.
	EstimatedCost Amount
	// TreeBudgetTotal é o orçamento total da árvore, base da fracção de custo que
	// força revisão humana. Registado na primeira invocação e mantido estável.
	TreeBudgetTotal Amount
	// NewPlanHash é o hash do subgrafo re-planeado (entra nos eventos).
	NewPlanHash string
}

func (r ReplanRequest) validate() error {
	if r.PlanID == "" || r.TreeID == "" || len(r.Subgraph) == 0 {
		return ErrEmptyRequest
	}
	return nil
}

// ReplanResult é o desfecho de um re-plano.
type ReplanResult struct {
	// Applied — o re-plano foi aprovado e aplicado (SCH retomou o novo subgrafo).
	Applied bool
	// RequiredHuman — a revisão humana foi forçada (tecto esgotado ou fracção
	// excedida).
	RequiredHuman bool
	// ReplanCount é o contador de re-planos da ÁRVORE após esta invocação (inclui
	// os aninhados — todos contam para o mesmo tecto).
	ReplanCount int
	// NewPlanHash ecoa o hash aplicado.
	NewPlanHash string
}

// ---------------------------------------------------------------------------
// Estado por árvore (o tecto e o contador vivem aqui, partilhados por aninhados)
// ---------------------------------------------------------------------------

type treeState struct {
	treeID      string
	replanCount int
	accumCost   Amount
	budgetTotal Amount
	// originalLevel é o nível de autonomia do plano ORIGINAL, FIXADO na primeira
	// invocação da árvore (à imagem de budgetTotal). É o tecto de autonomia contra o
	// qual TODOS os re-planos aninhados são medidos: um re-plano aninhado não pode
	// re-declarar um OriginalLevel superior para escalar autonomia (ADR-005: o pedido
	// é untrusted — a supervisão vem da árvore fixada, não do que o pedido afirma).
	originalLevel Level
}

// ---------------------------------------------------------------------------
// Config e Coordinator
// ---------------------------------------------------------------------------

// Config parametriza o tecto e o gatilho de revisão humana.
type Config struct {
	// MaxReplansPerTree é o tecto de re-planos por ÁRVORE. Contagem POR-ÁRVORE:
	// re-planos aninhados incrementam o MESMO contador. Ao ser excedido, a revisão
	// humana é forçada (não há re-plano automático permanente — DoD).
	MaxReplansPerTree int
	// HumanReviewCostBasisPoints é a fracção do orçamento da árvore (em pontos-base,
	// /10000) acima da qual o custo acumulado dos re-planos força revisão humana.
	// <= 0 desliga este gatilho (fica só o tecto).
	HumanReviewCostBasisPoints int64
}

// Coordinator coordena re-planos de subgrafo, mantendo o estado por-árvore
// (contador + custo acumulado). Seguro para concorrência.
type Coordinator struct {
	budget ResidualBudget
	gate   Gate
	sched  SubgraphScheduler
	rec    ReplanRecorder
	cfg    Config

	mu    sync.Mutex
	trees map[string]*treeState
}

// NewCoordinator constrói um Coordinator. Todas as portas são obrigatórias
// (fail-closed: uma porta nil é um erro de construção, não um no-op silencioso).
func NewCoordinator(b ResidualBudget, g Gate, s SubgraphScheduler, r ReplanRecorder, cfg Config) (*Coordinator, error) {
	if b == nil || g == nil || s == nil || r == nil {
		return nil, ErrNilPort
	}
	return &Coordinator{
		budget: b,
		gate:   g,
		sched:  s,
		rec:    r,
		cfg:    cfg,
		trees:  make(map[string]*treeState),
	}, nil
}

// guardImmutable recusa fail-closed qualquer nó CONCLUÍDO no subgrafo a substituir
// ou no novo subgrafo. É este guard que impede o re-despacho do histórico: sem
// ele, um node_id concluído em NewSubgraph seria retomado pelo SCH.
//
// O subgrafo a substituir são nós EXISTENTES do DAG: cada um TEM de constar do
// snapshot. Um id ausente não é "pendente por omissão" — não é verificável, logo é
// recusado ([ErrNodeStatusUnknown]). Isto fecha o fail-open de um nó concluído que
// o chamador tivesse deixado fora de um snapshot incompleto. O novo subgrafo, por
// contraste, contém nós NOVOS (legitimamente ausentes do snapshot corrente); aí só
// se recusa um id que APAREÇA no snapshot já concluído (reuso de histórico).
func (c *Coordinator) guardImmutable(req ReplanRequest) error {
	for _, id := range req.Subgraph {
		st, ok := req.NodeStatuses[id]
		if !ok {
			return fmt.Errorf("%w: %q (subgrafo a substituir)", ErrNodeStatusUnknown, id)
		}
		if st == NodeCompleted {
			return fmt.Errorf("%w: %q (subgrafo a substituir)", ErrCompletedNodeImmutable, id)
		}
	}
	for _, id := range req.NewSubgraph {
		if req.NodeStatuses[id] == NodeCompleted {
			return fmt.Errorf("%w: %q (novo subgrafo)", ErrCompletedNodeImmutable, id)
		}
	}
	return nil
}

// exceedsFraction indica se o custo acumulado ultrapassou a fracção (bps/10000) do
// orçamento total, em QUALQUER dimensão (tokens ou $). Inteiro exacto — sem vírgula
// flutuante. bps <= 0 desliga o gatilho.
func exceedsFraction(accum, total Amount, bps int64) bool {
	if bps <= 0 {
		return false
	}
	if total.Tokens > 0 && crossGreater(accum.Tokens, total.Tokens, bps) {
		return true
	}
	if total.CostMicroUSD > 0 && crossGreater(accum.CostMicroUSD, total.CostMicroUSD, bps) {
		return true
	}
	return false
}

// crossGreater reporta se accum*10000 > total*bps SEM overflow de int64. A
// comparação naive (accum*10000 > total*bps) transborda para orçamentos grandes
// (~9.2e14 tokens) e o produto pode inverter de sinal, fazendo o gatilho de revisão
// humana falhar-aberto. math/big torna a comparação exacta para qualquer int64.
func crossGreater(accum, total, bps int64) bool {
	lhs := new(big.Int).Mul(big.NewInt(accum), big.NewInt(10000))
	rhs := new(big.Int).Mul(big.NewInt(total), big.NewInt(bps))
	return lhs.Cmp(rhs) > 0
}

// bump incrementa, sob lock, o contador e o custo acumulado da ÁRVORE (criando o
// estado na primeira vez), e devolve o contador corrente e se a revisão humana é
// forçada. Re-planos ANINHADOS (mesmo tree_id) partilham este estado — contam para
// o MESMO tecto, nunca criam um contador novo.
func (c *Coordinator) bump(req ReplanRequest) (count int, forceHuman bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ts := c.trees[req.TreeID]
	if ts == nil {
		ts = &treeState{treeID: req.TreeID, budgetTotal: req.TreeBudgetTotal, originalLevel: req.OriginalLevel}
		c.trees[req.TreeID] = ts
	}
	ts.replanCount++
	ts.accumCost = ts.accumCost.add(req.EstimatedCost)
	count = ts.replanCount
	forceHuman = count > c.cfg.MaxReplansPerTree ||
		exceedsFraction(ts.accumCost, ts.budgetTotal, c.cfg.HumanReviewCostBasisPoints)
	return count, forceHuman
}

// ReplanCount devolve o contador de re-planos corrente de uma árvore (0 se
// desconhecida). Exposto para inspecção/auditoria.
func (c *Coordinator) ReplanCount(treeID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ts := c.trees[treeID]; ts != nil {
		return ts.replanCount
	}
	return 0
}

// PinnedLevel devolve o nível de autonomia FIXADO de uma árvore (o tecto que os
// re-planos aninhados não podem exceder) e se a árvore já foi fixada. Exposto para
// inspecção/auditoria (e para verificar a atomicidade de [Coordinator.pinAndCheckLevel]).
func (c *Coordinator) PinnedLevel(treeID string) (Level, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ts := c.trees[treeID]; ts != nil {
		return ts.originalLevel, true
	}
	return 0, false
}

// pinAndCheckLevel ancora o invariante "autonomia ≤ original" à ÁRVORE de forma
// ATÓMICA: sob um ÚNICO lock, FIXA o originalLevel na primeira invocação (criando o
// treeState com o budgetTotal) E valida o pedido contra o nível fixado. Fazer o
// check-and-pin numa só secção crítica fecha o TOCTOU em que duas PRIMEIRAS invocações
// concorrentes da mesma árvore veriam ambas ts==nil (check separado do pin, feito antes
// em bump) e uma escaparia à fixação da outra — uma escalada de autonomia por corrida.
// Um re-plano ANINHADO (ts já existe) é validado contra o originalLevel fixado: nem o
// OriginalLevel re-declarado nem o Level pedido podem excedê-lo (bloqueia a escalada por
// aninhamento — OriginalLevel=L5,Level=L5 sobre uma árvore fixada em L2 é recusado ANTES
// de qualquer débito). Criar o treeState aqui (replanCount=0) é benigno: fixa só o
// nível/orçamento da árvore — não reserva orçamento nem conta re-planos (isso é bump, a
// jusante da reserva). O originalLevel é uma propriedade estável da árvore, correcta
// mesmo que esta primeira tentativa falhe depois na reserva/gate.
func (c *Coordinator) pinAndCheckLevel(req ReplanRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ts := c.trees[req.TreeID]
	if ts == nil {
		ts = &treeState{treeID: req.TreeID, budgetTotal: req.TreeBudgetTotal, originalLevel: req.OriginalLevel}
		c.trees[req.TreeID] = ts
	}
	if req.OriginalLevel > ts.originalLevel || req.Level > ts.originalLevel {
		return fmt.Errorf("%w: arvore fixada em %s, pedido tentou nivel %s (original re-declarado %s)",
			ErrAutonomyExceedsOriginal, ts.originalLevel, req.Level, req.OriginalLevel)
	}
	return nil
}

// Replan governa um re-plano de subgrafo fail-closed, pela ordem:
//
//	guards (imutabilidade + autonomia) → reserva do residual na árvore →
//	contagem por-árvore (aninhados no mesmo tecto) → emit requested →
//	SCH.Suspend → gate (com revisão humana forçada se preciso) →
//	commit → emit applied → SCH.Resume(novo subgrafo).
//
// Dois regimes de recuperação:
//
//   - ANTES do commit (recusa do gate, ou falha em reserva/emit-requested/suspend/
//     commit): a reserva é LIBERTADA (sem leak) e o subgrafo original é retomado —
//     nunca fica preso suspenso.
//   - APÓS o commit (re-plano já aprovado e debitado): uma falha em emitir o
//     `applied` ou em retomar o novo subgrafo NÃO reverte o débito já committed (o
//     re-plano foi aprovado), mas ainda assim RETOMA o subgrafo original em
//     compensação — o DAG não fica preso. O erro sobe com Applied=false para
//     reconciliação a jusante.
//
// Um re-plano recusado NÃO emite `applied`.
func (c *Coordinator) Replan(ctx context.Context, req ReplanRequest) (ReplanResult, error) {
	if err := req.validate(); err != nil {
		return ReplanResult{}, err
	}
	// 1. Histórico intocável: nós concluídos são imutáveis.
	if err := c.guardImmutable(req); err != nil {
		return ReplanResult{}, err
	}
	// 2. Autonomia do re-plano ≤ original (só reduz, nunca escala).
	if !req.OriginalLevel.Valid() || !req.Level.Valid() {
		return ReplanResult{}, ErrInvalidLevel
	}
	if req.Level > req.OriginalLevel {
		return ReplanResult{}, fmt.Errorf("%w: %s > %s", ErrAutonomyExceedsOriginal, req.Level, req.OriginalLevel)
	}
	// 2-bis. Autonomia ancorada à ÁRVORE, ATÓMICA: fixa o originalLevel na 1ª invocação e
	// valida o pedido na MESMA secção crítica (fecha o TOCTOU de duas primeiras invocações
	// concorrentes). Um aninhado não re-declara um original superior ao fixado. Fail-closed
	// ANTES de qualquer débito/efeito.
	if err := c.pinAndCheckLevel(req); err != nil {
		return ReplanResult{}, err
	}

	// 3. Débito na ÁRVORE: reserva CAS do residual (fail-closed).
	resv, err := c.budget.Reserve(ctx, req.TreeID, req.EstimatedCost)
	if err != nil {
		return ReplanResult{}, fmt.Errorf("%w: %v", ErrNoResidualBudget, err)
	}

	// 4. Contagem por-árvore (aninhados → mesmo tecto) + decisão de revisão humana.
	count, forceHuman := c.bump(req)

	// 5. Facto: `plan.replan_requested`.
	if _, err := c.rec.RecordReplan(ctx, c.payload(req, plannerevents.ReplanRequested)); err != nil {
		_ = c.budget.Release(ctx, resv)
		return ReplanResult{ReplanCount: count, RequiredHuman: forceHuman}, err
	}

	// 6. SCH suspende o subgrafo (AOS-238).
	if err := c.sched.Suspend(ctx, req.PlanID, req.Subgraph); err != nil {
		_ = c.budget.Release(ctx, resv)
		return ReplanResult{ReplanCount: count, RequiredHuman: forceHuman}, err
	}

	// 7. MESMO gate, ao nível do original; revisão humana forçada se preciso.
	out, err := c.gate.Review(ctx, GateRequest{
		PlanID:       req.PlanID,
		Level:        req.Level,
		RequireHuman: forceHuman,
		ResidualCost: req.EstimatedCost,
	})
	if err != nil {
		_ = c.sched.Resume(ctx, req.PlanID, req.Subgraph)
		_ = c.budget.Release(ctx, resv)
		return ReplanResult{ReplanCount: count, RequiredHuman: forceHuman}, err
	}
	if !out.Approved {
		// Recusa: retoma o subgrafo original e liberta a reserva. Sem `applied`.
		_ = c.sched.Resume(ctx, req.PlanID, req.Subgraph)
		_ = c.budget.Release(ctx, resv)
		return ReplanResult{ReplanCount: count, RequiredHuman: forceHuman}, ErrReplanRejected
	}

	// 8. Aprovado: commit do débito na árvore.
	if err := c.budget.Commit(ctx, resv); err != nil {
		_ = c.sched.Resume(ctx, req.PlanID, req.Subgraph)
		return ReplanResult{ReplanCount: count, RequiredHuman: forceHuman}, err
	}

	// 9. Facto: `plan.replan_applied`.
	if _, err := c.rec.RecordReplan(ctx, c.payload(req, plannerevents.ReplanApplied)); err != nil {
		// O débito já está committed (aprovado). A emissão do applied falhou:
		// compensa retomando o subgrafo ORIGINAL para o DAG não ficar preso suspenso
		// (o subgrafo original só tem nós pendentes — passo 1 —, nunca histórico).
		// O erro sobe (Applied=false) para reconciliação a jusante.
		_ = c.sched.Resume(ctx, req.PlanID, req.Subgraph)
		return ReplanResult{ReplanCount: count, RequiredHuman: forceHuman}, err
	}

	// 10. SCH retoma o NOVO subgrafo (nunca contém concluídos — guardado no passo 1).
	if err := c.sched.Resume(ctx, req.PlanID, req.NewSubgraph); err != nil {
		// Falha ao retomar o novo subgrafo após o commit: compensa retomando o
		// subgrafo ORIGINAL (só pendentes) para o DAG não ficar preso suspenso. O
		// erro sobe (Applied=false) para reconciliação a jusante.
		_ = c.sched.Resume(ctx, req.PlanID, req.Subgraph)
		return ReplanResult{ReplanCount: count, RequiredHuman: forceHuman}, err
	}

	return ReplanResult{
		Applied:       true,
		RequiredHuman: forceHuman,
		ReplanCount:   count,
		NewPlanHash:   req.NewPlanHash,
	}, nil
}

// payload monta o corpo do evento reutilizando o tipo/constantes de plannerevents.
// O residual reportado é o custo em micro-dólares (dimensão monetária da árvore).
func (c *Coordinator) payload(req ReplanRequest, phase plannerevents.ReplanPhase) plannerevents.ReplanPayload {
	return plannerevents.ReplanPayload{
		PlanID:         req.PlanID,
		Phase:          phase,
		Subgraph:       req.Subgraph,
		ResidualBudget: req.EstimatedCost.CostMicroUSD,
		NewPlanHash:    req.NewPlanHash,
	}
}

// Asserção de compile-time: o Recorder real de plannerevents satisfaz a porta de
// emissão — o wiring liga-o sem adaptador (prova não-vacuosa da fronteira).
var _ ReplanRecorder = (*plannerevents.Recorder)(nil)
