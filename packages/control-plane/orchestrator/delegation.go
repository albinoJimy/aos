package orchestrator

// Delegação a sub-agentes com orçamento herdado (AOS-026, EPIC-03).
//
// O [Delegator] estende o grafo de tarefas de AOS-025 com a operação de SPAWN de
// um sub-agente. NÃO reimplementa nenhuma primitiva: COMPÕE três fundações já
// existentes (ADR-002/003/008):
//
//   - ORÇAMENTO (AOS-008, packages/control-plane/budget): a reserva atómica por
//     compare-and-swap sobre a árvore de orçamento hierárquica. É a reserva do
//     budget — nunca um contador partilhado com corrida — que IMPÕE o invariante
//     "soma das reservas dos filhos ≤ orçamento do pai" (0 overshoot) e a herança
//     "sub-orçamento ≤ remanescente do pai", em TOKENS e USD (nunca iterações).
//   - IDENTIDADE (AOS-005/006, packages/platform/identity): cada sub-agente é uma
//     identidade não-humana ÚNICA, emitida on-behalf-of o pai por
//     [identity.Issuer.IssueChild], estendendo a cadeia de delegação hash-linked
//     (autoridade = pedido ∩ classe, ⊆ pai; raiz humana preservada).
//   - REFERENCE MONITOR (AOS-003, packages/kernel/reference-monitor): a criação da
//     identidade filha e o débito de orçamento são MEDIADOS — só ocorrem sob um
//     Permit não-forjável de [rm.Monitor.Mediate] (ADR-002). Uma mediação que
//     negue liberta a reserva (sem leak) e recusa o spawn fail-closed.
//
// Modelo de contabilidade (por que não há dupla contagem). A árvore de orçamento
// espelha a árvore de delegação: cada agente tem um NÓ cujo LIMITE é o orçamento
// do seu SUBÁRVORE (o próprio + descendentes). No spawn reserva-se a FATIA DE
// CONSUMO PRÓPRIO do filho no NÓ DO FILHO; como [budget.Budget.Reserve] debita em
// CASCATA por toda a linhagem (filho→pai→raiz), a reserva é atomicamente validada
// contra o limite do pai (e da raiz) — é ISTO a "reserva CAS antes do spawn". A
// fatia própria de cada agente é contada UMA vez e sobe a cascata uma vez: a soma
// na raiz é o consumo real total, sem dupla contagem. O limite do nó do filho
// (≥ fatia própria) deixa headroom para o próprio filho delegar (map-reduce
// recursivo). No fim consolida-se: Commit (consumo real = fatia reservada) em
// sucesso, Release (liberta a fatia) em falha — ambos IDEMPOTENTES por
// reservation.ID (ADR-001), com a contabilidade durável a viver no log do budget.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator/contract"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	rm "github.com/aos-ref/kernel/reference-monitor"
	identity "github.com/aos-ref/platform/identity"
	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento append-only da delegação (AOS-026). Gravados no mesmo stream
// (run_id) do Event Store, permitem reconstruir a ÁRVORE de delegação e o
// burn-down por replay. A contabilidade AUTORITATIVA do orçamento (reserved/
// committed/released por nó) é a do próprio budget (AOS-008), reconstruível por
// [budget.Rebuild]; estes factos projectam a árvore de sub-agentes por cima.
const (
	// EventBudgetReserved — o pai reservou atomicamente (CAS) a fatia do filho.
	EventBudgetReserved = "subagent.budget_reserved"
	// EventSubagentSpawned — o sub-agente foi criado (identidade NHI filha mediada).
	EventSubagentSpawned = "subagent.spawned"
	// EventBudgetConsumed — consolidação do consumo real do sub-agente (Commit).
	EventBudgetConsumed = "subagent.budget_consumed"
	// EventBudgetReleased — a reserva foi libertada (falha/cancelamento; sem leak).
	EventBudgetReleased = "subagent.budget_released"
	// EventSpawnDeniedNoBudget — spawn recusado por falta de orçamento remanescente
	// (fail-closed, sem deadlock). É o evento explícito exigido pelo critério.
	EventSpawnDeniedNoBudget = "subagent.spawn_denied_no_budget"
	// EventSpawnDenied — spawn recusado por limite de profundidade/fan-out ou por
	// negação da mediação do RM (fail-closed). O motivo vai no payload.
	EventSpawnDenied = "subagent.spawn_denied"
)

// Motivos de recusa de spawn (payload.Reason), estáveis e legíveis-por-máquina.
const (
	ReasonNoBudget        = "no_budget"
	ReasonMaxDepth        = "max_depth"
	ReasonMaxFanOut       = "max_fanout"
	ReasonMediationDenied = "mediation_denied"
	// ReasonDepthMismatch — a profundidade auto-declarada (req.Depth) é MENOR do que
	// a profundidade autoritativa derivada da cadeia de delegação do pai: uma
	// subdeclaração que tentaria contornar o gate de profundidade (fail-closed).
	ReasonDepthMismatch = "depth_mismatch"
)

// Step* derivam step_ids DETERMINÍSTICOS e DISTINTOS por facto de delegação, para
// que a idempotency_key (run_id:step_id) seja única no stream e não colida com os
// passos do DAG (Step* de dag_events.go) nem do ciclo de vida da tarefa. São a
// base da idempotência por passo (ADR-001): reemitir o mesmo facto é deduplicado.
//
// Os factos de MOVIMENTO de orçamento (reserva/consumo/libertação) incluem o
// reservation.ID no step: reservas DISTINTAS do mesmo filho (ex.: um spawn negado
// e re-tentado com o mesmo ChildTaskID) obtêm step_ids distintos e NÃO colidem na
// projecção — reflectindo o log autoritativo do budget, cujo step é por-reserva
// (events.go). O mesmo reservation.ID reproduz o mesmo step (idempotência de
// retry preservada: refinalizar a MESMA reserva deduplica).
func StepBudgetReserved(childTaskID, reservationID string) string {
	return "sa-res:" + childTaskID + ":" + reservationID
}
func StepSubagentSpawned(childTaskID string) string { return "sa-spawn:" + childTaskID }
func StepBudgetConsumed(childTaskID, reservationID string) string {
	return "sa-consume:" + childTaskID + ":" + reservationID
}
func StepBudgetReleased(childTaskID, reservationID string) string {
	return "sa-release:" + childTaskID + ":" + reservationID
}
func StepSpawnDenied(childTaskID string) string { return "sa-deny:" + childTaskID }

// Sentinelas de erro da delegação (comparáveis por errors.Is — fail-closed).
var (
	// ErrNoDelegationBudget — spawn recusado por falta de orçamento remanescente do
	// pai (a reserva CAS não coube). Fail-closed, sem deadlock nem espera.
	ErrNoDelegationBudget = errors.New("orchestrator: spawn recusado — sem orçamento remanescente (fail-closed)")
	// ErrMaxDepthExceeded — a profundidade de delegação pedida excede o máximo.
	ErrMaxDepthExceeded = errors.New("orchestrator: spawn recusado — profundidade máxima de delegação excedida")
	// ErrMaxFanOutExceeded — o número REAL de filhos vivos do pai atingiu o máximo
	// configurado (imposto sobre um contador mediado, não sobre um índice
	// auto-reportado).
	ErrMaxFanOutExceeded = errors.New("orchestrator: spawn recusado — fan-out máximo de delegação excedido")
	// ErrDepthMismatch — a profundidade auto-declarada é MENOR do que a autoritativa
	// derivada da cadeia de delegação do pai (subdeclaração para contornar o gate).
	ErrDepthMismatch = errors.New("orchestrator: spawn recusado — profundidade declarada abaixo da autoritativa (fail-closed)")
	// ErrSpawnMediationDenied — o Reference Monitor negou a criação da identidade
	// filha / o débito (ADR-002). A reserva é libertada e o spawn recusado.
	ErrSpawnMediationDenied = errors.New("orchestrator: spawn recusado — mediação do RM negou a criação/débito")
	// ErrInvalidSpawn — pedido de spawn malformado (campos obrigatórios em falta ou
	// fatia inválida face ao orçamento herdado).
	ErrInvalidSpawn = errors.New("orchestrator: pedido de spawn inválido")
	// ErrDelegatorDeps — dependências obrigatórias do Delegator em falta.
	ErrDelegatorDeps = errors.New("orchestrator: dependências do Delegator em falta (reserver/mediator/issuer)")
	// ErrNilHandle — Finish chamado com handle nil.
	ErrNilHandle = errors.New("orchestrator: SpawnHandle nil")
)

// Reserver é o seam do orçamento hierárquico com reserva CAS (AOS-008).
// *budget.Budget satisfá-lo. NÃO é reimplementado aqui — é a fonte da atomicidade
// e do invariante soma-dos-filhos ≤ pai.
type Reserver interface {
	AddNode(nodeID, parentID string, limit budget.Amount) error
	Reserve(ctx context.Context, nodeID string, amt budget.Amount) (budget.Reservation, error)
	Commit(ctx context.Context, r budget.Reservation) error
	Release(ctx context.Context, r budget.Reservation) error
}

// Mediator é o seam do Reference Monitor (AOS-003). *rm.Monitor satisfá-lo. A
// mediação é OBRIGATÓRIA (ADR-002): sem um Permit não se cria identidade nem se
// consuma o débito.
type Mediator interface {
	Mediate(ctx context.Context, call rm.Call) (rm.Decision, error)
}

// ChildIssuer é o seam de emissão de identidade NHI filha (AOS-005/006).
// *identity.Issuer satisfá-lo via IssueChild.
type ChildIssuer interface {
	IssueChild(ctx context.Context, parentCompact string, req identity.ChildRequest) (identity.Token, error)
}

// NodeSink é o seam de admissão de um nó-tarefa no DAG (AOS-025). *GraphBuilder
// satisfá-lo. Opcional: se ligado, o sub-agente é admitido como nó-tarefa filho
// com a sua identidade, coerente com a cadeia de delegação (ADR-003) e
// reconstruível por RebuildDAG.
type NodeSink interface {
	AddNode(ctx context.Context, spec NodeSpec) error
}

// Delegator faz o spawn de sub-agentes com orçamento herdado. Construir com
// [NewDelegator]. Um Delegator é seguro para uso concorrente na medida em que os
// seus colaboradores o são: [budget.Budget] (CAS) e [rm.Monitor] são
// thread-safe; o Event Store é append-only concorrente. O estado do Delegator é
// imutável após a construção.
type Delegator struct {
	reserver Reserver
	mediator Mediator
	issuer   ChildIssuer
	graph    NodeSink // opcional (integração com o DAG de AOS-025)

	store    EventStore // opcional (projecção durável da árvore de delegação)
	producer eventstore.Producer
	tracer   agentruntime.Tracer

	maxDepth        int
	maxFanOut       int
	spawnToolID     string
	spawnCapability string

	// mu protege children (contador de fan-out mediado). O restante estado do
	// Delegator é imutável após a construção.
	mu sync.Mutex
	// children conta os filhos ADMITIDOS por nó-pai (chave = run_id\x00
	// parent_budget_node), impondo maxFanOut sobre o número REAL de filhos em vez
	// de confiar num índice auto-reportado pelo caller. Um slot é reservado
	// atomicamente antes dos efeitos e libertado se o spawn for recusado a jusante
	// (o log de subagent.spawned permite reconstruí-lo por replay).
	children map[string]int
}

// DelegatorOption configura o Delegator.
type DelegatorOption func(*Delegator)

// WithMaxDepth define a profundidade máxima de delegação (nº de elos abaixo da
// raiz). Configurável, NÃO uma constante mágica. <=0 mantém o default.
func WithMaxDepth(n int) DelegatorOption {
	return func(d *Delegator) {
		if n > 0 {
			d.maxDepth = n
		}
	}
}

// WithMaxFanOut define o fan-out máximo (nº de filhos por pai). Configurável.
// <=0 mantém o default.
func WithMaxFanOut(n int) DelegatorOption {
	return func(d *Delegator) {
		if n > 0 {
			d.maxFanOut = n
		}
	}
}

// WithDelegationStore liga a projecção durável dos eventos de delegação ao Event
// Store (stream = run_id), com o produtor (NHI do plano de controlo).
func WithDelegationStore(store EventStore, producer eventstore.Producer) DelegatorOption {
	return func(d *Delegator) {
		d.store = store
		d.producer = producer
	}
}

// WithDelegationGraph liga a integração com o DAG (AOS-025): cada sub-agente é
// admitido como nó-tarefa filho.
func WithDelegationGraph(g NodeSink) DelegatorOption {
	return func(d *Delegator) {
		if g != nil {
			d.graph = g
		}
	}
}

// WithDelegationTracer injecta a porta de observabilidade (spans OTel GenAI do
// spawn). Default: [agentruntime.NoopTracer].
func WithDelegationTracer(t agentruntime.Tracer) DelegatorOption {
	return func(d *Delegator) {
		if t != nil {
			d.tracer = t
		}
	}
}

// WithSpawnCapability define o ToolID e a Capability apresentados ao RM na
// mediação do spawn (default: "agent.spawn" / "cap:agent.spawn").
func WithSpawnCapability(toolID, capability string) DelegatorOption {
	return func(d *Delegator) {
		if toolID != "" {
			d.spawnToolID = toolID
		}
		if capability != "" {
			d.spawnCapability = capability
		}
	}
}

// NewDelegator constrói um Delegator. reserver (orçamento CAS), mediator (RM) e
// issuer (identidade filha) são OBRIGATÓRIOS — a sua ausência é fail-closed. Os
// defaults (profundidade 8, fan-out 16) são configuráveis por opção.
func NewDelegator(reserver Reserver, mediator Mediator, issuer ChildIssuer, opts ...DelegatorOption) (*Delegator, error) {
	if reserver == nil || mediator == nil || issuer == nil {
		return nil, ErrDelegatorDeps
	}
	d := &Delegator{
		reserver:        reserver,
		mediator:        mediator,
		issuer:          issuer,
		tracer:          agentruntime.NoopTracer{},
		maxDepth:        8,
		maxFanOut:       16,
		spawnToolID:     "agent.spawn",
		spawnCapability: "cap:agent.spawn",
		children:        make(map[string]int),
	}
	for _, o := range opts {
		o(d)
	}
	if d.tracer == nil {
		d.tracer = agentruntime.NoopTracer{}
	}
	return d, nil
}

// SpawnRequest descreve um spawn de sub-agente com orçamento herdado.
type SpawnRequest struct {
	// RunID é a árvore de execução (stream_id no Event Store; treeID no budget).
	RunID string
	// ParentStepID correlaciona o spawn com o passo do pai (opcional).
	ParentStepID string

	// ParentBudgetNode é o nó de orçamento do pai (tem de existir na árvore). A
	// reserva do filho cascateia por ele e é CAS-validada contra o seu limite.
	ParentBudgetNode string
	// ChildBudgetNode é o id do nó de orçamento do subárvore do filho (novo).
	ChildBudgetNode string
	// InheritedBudget é o orçamento herdado do subárvore do filho (o LIMITE do nó
	// do filho): o filho + descendentes nunca gastam mais do que isto. É ≤ ao
	// remanescente do pai — imposto pela reserva hierárquica em cascata.
	InheritedBudget budget.Amount
	// SpawnReserve é a fatia de consumo PRÓPRIO reservada atomicamente no spawn
	// (CAS antes do spawn). Se zero, assume-se folha e reserva-se todo o
	// InheritedBudget. Tem de ser > 0 e ≤ InheritedBudget.
	SpawnReserve budget.Amount

	// Depth é a profundidade de delegação do filho auto-declarada pelo caller. NÃO
	// é a fonte de verdade do gate: a profundidade AUTORITATIVA é derivada da cadeia
	// de delegação hash-linked do ParentToken (len(chain)), e o gate impõe o máximo
	// entre ambas — uma subdeclaração (req.Depth < autoritativa) é recusada
	// fail-closed. Quando o token do pai não é decodificável, cai-se para req.Depth
	// (a mediação do RM recusa credenciais inválidas de qualquer modo).
	Depth int
	// FanOutIndex é o índice 0-based do filho entre os irmãos. DESCRITIVO apenas
	// (projectado nos eventos): NÃO gate o fan-out — o limite é imposto sobre o
	// número REAL de filhos admitidos por nó-pai (contador mediado), não sobre este
	// índice auto-reportado.
	FanOutIndex int

	// ParentToken é o token NHI compacto do pai (Call.Credential na mediação).
	ParentToken string
	// Child é o pedido de emissão da identidade filha (AgentID, classe, escopo).
	Child identity.ChildRequest
	// ChildTaskID é o id do nó-tarefa do filho no DAG. Default: Child.AgentID.
	ChildTaskID string

	// ParentTraceParent é o SpanContext do invoke_agent do PAI, transportado como
	// traceparent W3C (AOS-077). Usado SÓ quando o ctx do Spawn NÃO carrega o
	// SpanContext do pai — o caso cross-fronteira RT-pai → Orquestrador (a propagação
	// EM-ctx não atravessa a serialização da delegação). Presente, o span
	// invoke_agent-âncora do filho abre COMO FILHO deste SpanContext (trace_id do pai
	// + parent_span_id correcto); ausente e sem SpanContext no ctx, a âncora é raiz de
	// um trace novo. Um traceparent malformado é ignorado fail-open (a âncora vira
	// raiz; a mediação/orçamento não são afectados). Vazio quando o pai corre no mesmo
	// processo e passa o SpanContext pelo ctx.
	ParentTraceParent string
}

// SpawnHandle é o comprovativo de um spawn bem-sucedido, consumido por
// [Delegator.Finish] para consolidar (Commit) ou libertar (Release) a reserva.
type SpawnHandle struct {
	RunID       string
	ChildTaskID string
	ChildNHI    string
	Reservation budget.Reservation
	Slice       budget.Amount
	ChildToken  identity.Token
	Agent       contract.AgentIdentity

	// ChildSeedTraceParent é o traceparent W3C do span invoke_agent-ÂNCORA do filho
	// aberto no Spawn (AOS-077). É o SEED que o RT-filho passa em
	// [agentruntime.Goal.ParentTraceParent] ao correr o sub-agente: o invoke_agent do
	// filho herda daqui o trace_id do pai e parenteia sob o span_id da âncora,
	// ligando a sub-árvore do filho ao pai pela mecânica NATIVA OTel (não por
	// atributos NHI). Vazio se o tracer é no-op (sem observabilidade). Também vai no
	// evento subagent.spawned (atributo não-secreto: só ids de trace/span).
	ChildSeedTraceParent string
}

// spawnIntent é o payload opaco entregue ao RM na mediação (Call.Input): descreve
// a intenção do spawn sem segredos (o token bearer vai só em Credential).
type spawnIntent struct {
	ChildAgentID string        `json:"child_agent_id"`
	ChildClass   string        `json:"child_class"`
	Slice        budget.Amount `json:"slice"`
	Depth        int           `json:"depth"`
}

// Spawn cria um sub-agente com orçamento herdado. Passos (fail-closed em cada um):
//
//  1. limites derivados de ESTADO AUTORITATIVO: profundidade da cadeia de
//     delegação hash-linked do pai (não um campo auto-declarado) e fan-out sobre o
//     nº REAL de filhos admitidos por nó-pai (contador mediado). Excedidos →
//     subagent.spawn_denied, sem deadlock;
//  2. MEDIAÇÃO do RM (ADR-002) ANTES de qualquer efeito de orçamento: autoriza a
//     criação da identidade E o débito. Se negar → recusa fail-closed sem jamais
//     tocar no orçamento do pai (sem headroom especulativo, sem inversão de
//     prioridade);
//  3. regista o nó de orçamento do subárvore do filho (limite = InheritedBudget);
//  4. RESERVA ATÓMICA (CAS) da fatia própria SOB o Permit — cascateia por toda a
//     linhagem e é validada contra o limite do pai (0 overshoot). Sem headroom →
//     subagent.spawn_denied_no_budget (fail-closed), sem espera;
//  5. emite a IDENTIDADE NHI filha (on-behalf-of o pai) sob o Permit;
//  6. (opcional) admite o sub-agente como nó-tarefa filho no DAG (AOS-025);
//  7. abre o span invoke_agent (ligado ao pai, custo USD por span) e emite
//     subagent.spawned.
//
// Devolve um [SpawnHandle] a consolidar com [Delegator.Finish].
func (d *Delegator) Spawn(ctx context.Context, req SpawnRequest) (*SpawnHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.RunID == "" || req.ParentBudgetNode == "" || req.ChildBudgetNode == "" || req.Child.AgentID == "" {
		return nil, ErrInvalidSpawn
	}
	childTask := req.ChildTaskID
	if childTask == "" {
		childTask = req.Child.AgentID
	}

	slice := req.SpawnReserve
	if slice.IsZero() {
		slice = req.InheritedBudget
	}
	// A fatia própria tem de ser positiva e caber no orçamento herdado.
	if !amtPositive(slice) || !amtNonNegative(req.InheritedBudget) || !amtFits(slice, req.InheritedBudget) {
		return nil, fmt.Errorf("%w: fatia %s não cabe no orçamento herdado %s", ErrInvalidSpawn, slice, req.InheritedBudget)
	}

	// 1a) Profundidade AUTORITATIVA: derivada da cadeia de delegação hash-linked do
	// pai (selada na assinatura, validada pelo RM), NÃO do campo req.Depth que o
	// caller preenche. O gate impõe o máximo entre a autoritativa e a declarada; uma
	// subdeclaração (req.Depth < autoritativa, para contornar o limite) é recusada.
	// Se o token do pai não for decodificável cai-se para req.Depth — a mediação do
	// RM recusa credenciais inválidas de qualquer modo.
	effDepth := req.Depth
	if authDepth, ok := parentChainDepth(req.ParentToken); ok {
		if req.Depth < authDepth {
			d.emitDenied(ctx, req, childTask, ReasonDepthMismatch)
			return nil, fmt.Errorf("%w: declarada=%d autoritativa=%d", ErrDepthMismatch, req.Depth, authDepth)
		}
		if authDepth > effDepth {
			effDepth = authDepth
		}
	}
	if effDepth > d.maxDepth {
		d.emitDenied(ctx, req, childTask, ReasonMaxDepth)
		return nil, fmt.Errorf("%w: depth=%d max=%d", ErrMaxDepthExceeded, effDepth, d.maxDepth)
	}

	// 1b) Fan-out sobre o nº REAL de filhos do pai (contador mediado sob mutex),
	// não sobre um índice auto-reportado. Reserva um slot atomicamente; qualquer
	// recusa a jusante liberta-o (releaseFanout). Fail-closed, sem deadlock.
	fanKey := req.RunID + "\x00" + req.ParentBudgetNode
	if !d.reserveFanout(fanKey) {
		d.emitDenied(ctx, req, childTask, ReasonMaxFanOut)
		return nil, fmt.Errorf("%w: parent=%s max=%d", ErrMaxFanOutExceeded, req.ParentBudgetNode, d.maxFanOut)
	}

	// 2) Mediação do RM (ADR-002) ANTES de qualquer efeito de orçamento: a criação
	// da identidade E o débito só procedem sob um Permit não-forjável. Mediar antes
	// de reservar garante que um spawn NEGADO nunca chega a segurar headroom do pai
	// (o débito é gated pelo Permit, não um efeito especulativo compensado) e fecha
	// o vector de contenção transitória. O pai apresenta o seu token (Credential);
	// o hook de identidade verifica-o e resolve o principal do pai.
	intent, _ := json.Marshal(spawnIntent{
		ChildAgentID: req.Child.AgentID, ChildClass: req.Child.AgentClass, Slice: slice, Depth: effDepth,
	})
	call := rm.Call{
		RunID: req.RunID, StepID: StepSubagentSpawned(childTask), ParentStepID: req.ParentStepID,
		ToolID: d.spawnToolID, Capability: d.spawnCapability, Credential: req.ParentToken,
		Context: rm.CallContext{BudgetTokensRemaining: slice.Tokens},
		Input:   intent,
	}
	dec, mErr := d.mediator.Mediate(ctx, call)
	if mErr != nil || !dec.Permitted() {
		// Negado ANTES de qualquer reserva: nada a libertar no budget; só o slot de
		// fan-out. Recusa fail-closed.
		d.releaseFanout(fanKey)
		d.emitDenied(ctx, req, childTask, ReasonMediationDenied)
		if mErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrSpawnMediationDenied, mErr)
		}
		return nil, fmt.Errorf("%w: effect=%s reason=%s", ErrSpawnMediationDenied, dec.Effect, dec.Reason)
	}

	// 3) Nó de orçamento do subárvore do filho (limite = orçamento herdado), só sob
	// o Permit. Um re-registo (retry) é idempotente (ErrNodeExists é benigno).
	if err := d.reserver.AddNode(req.ChildBudgetNode, req.ParentBudgetNode, req.InheritedBudget); err != nil && !errors.Is(err, budget.ErrNodeExists) {
		d.releaseFanout(fanKey)
		return nil, fmt.Errorf("orchestrator: registar nó de orçamento do filho: %w", err)
	}

	// 4) Reserva atómica CAS SOB o Permit: debita a fatia própria em cascata por
	// toda a linhagem (filho→pai→raiz). É o budget que impõe sub-orçamento ≤
	// remanescente do pai e 0 overshoot — nunca um contador partilhado com corrida.
	res, err := d.reserver.Reserve(ctx, req.ChildBudgetNode, slice)
	if err != nil {
		// Fail-closed: sem orçamento remanescente (ou erro de backend) → recusa
		// explícita, SEM deadlock nem espera indefinida.
		d.releaseFanout(fanKey)
		d.emitDenied(ctx, req, childTask, ReasonNoBudget)
		return nil, fmt.Errorf("%w: %v", ErrNoDelegationBudget, err)
	}
	d.emitBudget(ctx, req, childTask, EventBudgetReserved, StepBudgetReserved(childTask, res.ID), res, slice, "")

	// 5) Emissão da identidade NHI filha sob o Permit (on-behalf-of o pai).
	childTok, iErr := d.issuer.IssueChild(ctx, req.ParentToken, req.Child)
	if iErr != nil {
		d.release(ctx, req, childTask, res, slice)
		d.releaseFanout(fanKey)
		return nil, fmt.Errorf("orchestrator: emitir identidade filha: %w", iErr)
	}
	agent := agentIdentityFromToken(childTok)

	// 6) Integração com o DAG (opcional): o sub-agente é um nó-tarefa filho com a
	// sua NHI/cadeia (reconstruível por RebuildDAG). Idempotente em retry.
	if d.graph != nil {
		if err := d.graph.AddNode(ctx, NodeSpec{TaskID: childTask, Agent: agent}); err != nil && !errors.Is(err, ErrNodeExists) {
			d.release(ctx, req, childTask, res, slice)
			d.releaseFanout(fanKey)
			return nil, fmt.Errorf("orchestrator: admitir nó-tarefa do filho no DAG: %w", err)
		}
	}

	// 7) Span invoke_agent-ÂNCORA do filho + custo USD; subagent.spawned.
	//
	// AOS-077 (propagação cross-fronteira): a âncora abre COMO FILHO do SpanContext do
	// PAI. Este vem do ctx quando o pai corre no mesmo processo (propagação EM-ctx);
	// na fronteira RT-pai → Orquestrador (onde o ctx não viaja), vem do traceparent
	// explícito em req.ParentTraceParent. Assim a âncora herda o trace_id do pai e
	// aponta ParentSpanID ao invoke_agent do pai. O traceparent da PRÓPRIA âncora é o
	// SEED que devolvemos: o RT-filho enraíza o seu invoke_agent sob ela. A âncora é
	// o ancoradouro da sub-árvore do filho; o RT-filho CONTINUA a mesma árvore um
	// nível abaixo (custo real na execução; a âncora é a decomposição, custo já
	// anotado). Mantêm-se os atributos NHI como reforço da aresta.
	spanCtx := ctx
	if _, ok := agentruntime.SpanContextFromContext(ctx); !ok && req.ParentTraceParent != "" {
		if psc, perr := agentruntime.ParseTraceParent(req.ParentTraceParent); perr == nil {
			spanCtx = agentruntime.ContextWithSpanContext(ctx, psc)
		}
	}
	childSeed := d.spawnSpan(spanCtx, req, childTask, childTok, slice)
	d.emitSpawned(ctx, req, childTask, res, slice, agent, childSeed)

	return &SpawnHandle{
		RunID: req.RunID, ChildTaskID: childTask, ChildNHI: childTok.Claims.AgentID,
		Reservation: res, Slice: slice, ChildToken: childTok, Agent: agent,
		ChildSeedTraceParent: childSeed,
	}, nil
}

// reserveFanout reserva atomicamente um slot de fan-out para o pai identificado por
// key, impondo maxFanOut sobre o número REAL de filhos admitidos. Devolve false se
// o limite já foi atingido (fail-closed).
func (d *Delegator) reserveFanout(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.children[key] >= d.maxFanOut {
		return false
	}
	d.children[key]++
	return true
}

// releaseFanout devolve um slot de fan-out reservado por [reserveFanout] quando o
// spawn é recusado a jusante do gate (mediação/reserva/emissão/DAG). Nunca desce
// abaixo de zero.
func (d *Delegator) releaseFanout(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.children[key] > 0 {
		d.children[key]--
	}
}

// parentChainDepth espreita (SEM verificar) a profundidade da cadeia de delegação
// embutida no token compacto do pai: o número de elos é a profundidade autoritativa
// do pai, e o filho fica um elo abaixo. Devolve (0,false) se o token não for
// decodificável ou não tiver cadeia — nesse caso o gate cai para req.Depth e a
// mediação do RM (que VERIFICA a credencial) recusa tokens inválidos. A verificação
// criptográfica autoritativa é do RM/verificador; esta leitura é só para o gate de
// profundidade não confiar num campo auto-declarado.
func parentChainDepth(compact string) (int, bool) {
	if compact == "" {
		return 0, false
	}
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		return 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, false
	}
	var c struct {
		DelegationChain []json.RawMessage `json:"delegation_chain"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return 0, false
	}
	if len(c.DelegationChain) == 0 {
		return 0, false
	}
	return len(c.DelegationChain), true
}

// Finish consolida o consumo do sub-agente ao fim (sucesso OU falha), de forma
// IDEMPOTENTE (ADR-001):
//
//   - success=true  → Commit da reserva: consolida o consumo real (a fatia) no
//     orçamento da árvore; emite subagent.budget_consumed;
//   - success=false → Release da reserva: liberta a fatia (sem leak); emite
//     subagent.budget_released.
//
// [budget.Budget.Commit]/[budget.Budget.Release] são idempotentes por
// reservation.ID (um segundo Finish com o mesmo resultado é no-op nos contadores)
// e os eventos deduplicam por (run_id, step_id) no Event Store — logo um retry
// NÃO duplica efeitos. Chamar Finish com sucesso após uma falha (ou vice-versa)
// devolve o erro do budget ([budget.ErrCommitAfterRelease]/
// [budget.ErrReleaseAfterCommit]): a reserva consome-se exactamente uma vez.
func (d *Delegator) Finish(ctx context.Context, h *SpawnHandle, success bool) error {
	if h == nil {
		return ErrNilHandle
	}
	if success {
		if err := d.reserver.Commit(ctx, h.Reservation); err != nil {
			return err
		}
		d.emit(ctx, h.RunID, EventBudgetConsumed, StepBudgetConsumed(h.ChildTaskID, h.Reservation.ID), DelegationEventPayload{
			RunID: h.RunID, ChildTaskID: h.ChildTaskID, ChildNHI: h.ChildNHI,
			ReservationID: h.Reservation.ID, Amount: h.Slice,
		})
		return nil
	}
	if err := d.reserver.Release(ctx, h.Reservation); err != nil {
		return err
	}
	d.emit(ctx, h.RunID, EventBudgetReleased, StepBudgetReleased(h.ChildTaskID, h.Reservation.ID), DelegationEventPayload{
		RunID: h.RunID, ChildTaskID: h.ChildTaskID, ChildNHI: h.ChildNHI,
		ReservationID: h.Reservation.ID, Amount: h.Slice,
	})
	return nil
}

// release liberta uma reserva no caminho de recusa (mediação/emissão falhada) e
// projecta o evento de libertação. A libertação do budget é idempotente.
func (d *Delegator) release(ctx context.Context, req SpawnRequest, childTask string, res budget.Reservation, slice budget.Amount) {
	_ = d.reserver.Release(ctx, res)
	d.emitBudget(ctx, req, childTask, EventBudgetReleased, StepBudgetReleased(childTask, res.ID), res, slice, "")
}

// spawnSpan abre e fecha o span invoke_agent-ÂNCORA do sub-agente, ligado ao pai
// pelo SpanContext propagado em ctx (AOS-077: trace_id comum + parent_span_id do
// invoke_agent do pai) E reforçado pelos atributos NHI, com o custo USD da fatia por
// span (DoD OTel GenAI). Reusa a porta zero-dep [agentruntime.Tracer] (como
// AOS-025), sem puxar o SDK OTel. Devolve o traceparent da própria âncora — o SEED
// com que o RT-filho enraíza o seu invoke_agent sob esta (a sub-árvore continua um
// nível abaixo). Devolve "" se o SpanContext da âncora for inválido (tracer no-op).
func (d *Delegator) spawnSpan(ctx context.Context, req SpawnRequest, childTask string, tok identity.Token, slice budget.Amount) string {
	_, span := d.tracer.StartSpan(ctx, agentruntime.OpInvokeAgent)
	span.SetAttribute(agentruntime.AttrOperationName, agentruntime.OpInvokeAgent)
	span.SetAttribute(agentruntime.AttrRunID, req.RunID)
	span.SetAttribute(agentruntime.AttrStepID, StepSubagentSpawned(childTask))
	span.SetAttribute(attrNodeTaskID, childTask)
	span.SetAttribute(attrNodeAgentNHI, tok.Claims.AgentID)
	span.SetAttribute(attrNodeDelegationDepth, len(tok.Claims.DelegationChain))
	// Ligação EXPLÍCITA ao pai a partir do span isolado: o NHI do pai imediato é o
	// Sub da folha da cadeia do filho (penúltimo sujeito), e o passo do pai vem de
	// req.ParentStepID quando presente. Torna a aresta filho→pai reconstruível sem
	// depender só da agregação por run_id (reforço dos ids nativos de trace/span).
	if leaf, ok := tok.Claims.DelegationChain.Leaf(); ok {
		span.SetAttribute(attrNodeParentNHI, leaf.Sub)
	}
	if req.ParentStepID != "" {
		span.SetAttribute(attrParentStepID, req.ParentStepID)
	}
	span.SetAttribute(agentruntime.AttrInputTokens, slice.Tokens)
	span.SetAttribute(agentruntime.AttrCostUSD, float64(slice.CostMicroUSD)/1_000_000.0)
	// Captura o SpanContext da âncora ANTES de fechar: é o seed cross-fronteira.
	seed := ""
	if sc := span.SpanContext(); sc.IsValid() {
		seed = agentruntime.FormatTraceParent(sc)
	}
	span.End()
	return seed
}

// DelegationEventPayload é o corpo dos eventos de delegação (struct para
// serialização JSON estável e replay determinístico). Carrega o suficiente para
// reconstruir a árvore de sub-agentes e correlacionar com o burn-down do budget.
type DelegationEventPayload struct {
	RunID         string        `json:"run_id"`
	ParentNode    string        `json:"parent_node,omitempty"`
	ChildNode     string        `json:"child_node,omitempty"`
	ChildTaskID   string        `json:"child_task_id"`
	ChildNHI      string        `json:"child_nhi,omitempty"`
	Depth         int           `json:"depth"`
	FanOutIndex   int           `json:"fan_out_index"`
	ReservationID string        `json:"reservation_id,omitempty"`
	Amount        budget.Amount `json:"amount,omitzero"`
	Reason        string        `json:"reason,omitempty"`
	// ChildSeedTraceParent — traceparent W3C da âncora invoke_agent do filho (AOS-077).
	// Só em subagent.spawned. Não-secreto: apenas ids de trace/span.
	ChildSeedTraceParent string `json:"child_seed_traceparent,omitempty"`
}

// emitBudget projecta um evento de movimento de orçamento da delegação.
func (d *Delegator) emitBudget(ctx context.Context, req SpawnRequest, childTask, evType, stepID string, res budget.Reservation, amt budget.Amount, reason string) {
	d.emit(ctx, req.RunID, evType, stepID, DelegationEventPayload{
		RunID: req.RunID, ParentNode: req.ParentBudgetNode, ChildNode: req.ChildBudgetNode,
		ChildTaskID: childTask, Depth: req.Depth, FanOutIndex: req.FanOutIndex,
		ReservationID: res.ID, Amount: amt, Reason: reason,
	})
}

// emitSpawned projecta subagent.spawned (identidade filha admitida). Inclui o
// childSeed (traceparent da âncora, AOS-077): um atributo NÃO-SECRETO (só ids de
// trace/span) que deixa o RT-filho reconstruir a raiz da sua sub-árvore por replay
// do stream, sem depender do SpawnHandle em memória.
func (d *Delegator) emitSpawned(ctx context.Context, req SpawnRequest, childTask string, res budget.Reservation, amt budget.Amount, agent contract.AgentIdentity, childSeed string) {
	d.emit(ctx, req.RunID, EventSubagentSpawned, StepSubagentSpawned(childTask), DelegationEventPayload{
		RunID: req.RunID, ParentNode: req.ParentBudgetNode, ChildNode: req.ChildBudgetNode,
		ChildTaskID: childTask, ChildNHI: agent.NHIID, Depth: req.Depth, FanOutIndex: req.FanOutIndex,
		ReservationID: res.ID, Amount: amt, ChildSeedTraceParent: childSeed,
	})
}

// emitDenied projecta uma recusa de spawn. O motivo no_budget usa o evento
// explícito exigido pelo critério; os restantes usam subagent.spawn_denied.
func (d *Delegator) emitDenied(ctx context.Context, req SpawnRequest, childTask, reason string) {
	evType := EventSpawnDenied
	if reason == ReasonNoBudget {
		evType = EventSpawnDeniedNoBudget
	}
	d.emit(ctx, req.RunID, evType, StepSpawnDenied(childTask), DelegationEventPayload{
		RunID: req.RunID, ParentNode: req.ParentBudgetNode, ChildNode: req.ChildBudgetNode,
		ChildTaskID: childTask, Depth: req.Depth, FanOutIndex: req.FanOutIndex, Reason: reason,
	})
}

// emit projecta um evento de delegação no Event Store (stream = run_id). É
// best-effort: a contabilidade AUTORITATIVA e fail-closed do orçamento é a do
// próprio budget (reserved/committed/released, com rollback se a emissão durável
// falhar); estes factos são uma projecção da árvore por cima dela. No-op sem
// store (caminho puramente in-memory dos testes de CAS).
func (d *Delegator) emit(ctx context.Context, runID, evType, stepID string, payload DelegationEventPayload) {
	if d.store == nil {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = d.store.Append(ctx, runID, eventstore.EventInput{
		Type: evType, Payload: raw, RunID: runID, StepID: stepID, Producer: d.producer,
	})
}

// agentIdentityFromToken projecta a identidade filha (NHI + cadeia de delegação)
// para o contrato do DAG, coerente com a cadeia on-behalf-of (ADR-003).
func agentIdentityFromToken(tok identity.Token) contract.AgentIdentity {
	chain := tok.Claims.DelegationChain
	hops := make([]contract.DelegationHop, len(chain))
	for i, l := range chain {
		hops[i] = contract.DelegationHop{Sub: l.Sub, ActAs: l.ActAs}
	}
	return contract.AgentIdentity{NHIID: tok.Claims.AgentID, DelegationChain: hops}
}

// amtNonNegative indica que nenhuma dimensão é negativa.
func amtNonNegative(a budget.Amount) bool { return a.Tokens >= 0 && a.CostMicroUSD >= 0 }

// amtPositive indica que a quantia é não-negativa e não-nula (reserva legítima).
func amtPositive(a budget.Amount) bool { return amtNonNegative(a) && !a.IsZero() }

// amtFits indica se a cabe em capacity nas DUAS dimensões (tokens e custo).
func amtFits(a, capacity budget.Amount) bool {
	return a.Tokens <= capacity.Tokens && a.CostMicroUSD <= capacity.CostMicroUSD
}
