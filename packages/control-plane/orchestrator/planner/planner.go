package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	rm "github.com/aos-ref/kernel/reference-monitor"
	identity "github.com/aos-ref/platform/identity"
)

// ---------------------------------------------------------------------------
// Seams (portas) — o planeador COMPÕE as fundações, não as reimplementa.
// ---------------------------------------------------------------------------

// Reserver é o seam do orçamento hierárquico com reserva CAS (AOS-008).
// *budget.Budget satisfá-lo. É a fonte da atomicidade e do invariante
// "reserva ≤ remanescente do pai" — a reserva de planeamento cascateia por ele.
type Reserver interface {
	AddNode(nodeID, parentID string, limit budget.Amount) error
	Reserve(ctx context.Context, nodeID string, amt budget.Amount) (budget.Reservation, error)
	Commit(ctx context.Context, r budget.Reservation) error
	Release(ctx context.Context, r budget.Reservation) error
}

// Mediator é o seam do Reference Monitor (AOS-003). *rm.Monitor satisfá-lo. A
// mediação é OBRIGATÓRIA (ADR-002): sem um Permit não se admite o planeador.
type Mediator interface {
	Mediate(ctx context.Context, call rm.Call) (rm.Decision, error)
}

// PlannerIssuer é o seam de emissão da NHI `agent:planner` (AOS-005/006).
// *identity.Issuer satisfá-lo via IssueChild (on-behalf-of o token do run).
type PlannerIssuer interface {
	IssueChild(ctx context.Context, parentCompact string, req identity.ChildRequest) (identity.Token, error)
}

// Decomposer é o passo de decomposição APOIADO EM LLM, INJECTADO como INTERFACE
// (NÃO um LLM real): recebe o objectivo e o snapshot de capabilities e devolve um
// [plan.PlanDocument] — DADOS UNTRUSTED (ADR-005), nunca executado aqui. Isolá-lo
// atrás desta porta é o que torna a decomposição testável e determinística nos
// testes (o Decomposer real vive fora deste pacote).
type Decomposer interface {
	Decompose(ctx context.Context, in DecomposeInput) (plan.PlanDocument, error)
}

// AdmissionEmitter apensa o facto durável `plan.planner_admitted` ao stream do
// plano. *plannerevents.Recorder satisfá-lo — e é ELE que detém a constante do
// tipo de evento ([plannerevents.EventPlannerAdmitted]): este pacote nunca declara
// um tipo de evento novo, apenas encaminha o payload pelo emissor sancionado.
type AdmissionEmitter interface {
	RecordPlannerAdmitted(ctx context.Context, p plannerevents.PlannerAdmittedPayload) (uint64, error)
}

// ---------------------------------------------------------------------------
// Modelo de custo — função PURA e DETERMINÍSTICA (snapshot como argumento).
// ---------------------------------------------------------------------------

// CostModel estima o custo de UMA tentativa de decomposição a partir do contexto
// de planeamento (tabela de custo, §CA "contexto × tabela de custo"). É PURO: sem
// I/O vivo, mesmo input → mesma estimativa (determinismo exigido pelo CA). A
// reserva total admitida é esta estimativa × factor de retry (ver [Planner.Decompose]).
type CostModel struct {
	// Version é a versão da tabela de preços — carimbada no evento de admissão
	// (proveniência/reprodutibilidade), não um segredo.
	Version string
	// BaseTokens/BaseCostMicroUSD é o custo fixo de arranque de uma tentativa.
	BaseTokens       int64
	BaseCostMicroUSD int64
	// TokensPerContextUnit/CostMicroUSDPerContextUnit escalam com o tamanho do
	// contexto (ContextUnits): o preço por unidade de contexto a decompor.
	TokensPerContextUnit       int64
	CostMicroUSDPerContextUnit int64
}

// PerAttempt estima o custo de uma tentativa de decomposição para o contexto pc.
// Função pura (sem I/O): o CA de determinismo exige que o mesmo contexto produza
// sempre a mesma estimativa.
func (m CostModel) PerAttempt(pc PlanningContext) budget.Amount {
	units := pc.ContextUnits
	if units < 0 {
		units = 0
	}
	return budget.Amount{
		Tokens:       m.BaseTokens + m.TokensPerContextUnit*units,
		CostMicroUSD: m.BaseCostMicroUSD + m.CostMicroUSDPerContextUnit*units,
	}
}

// DefaultCostModel é uma tabela de custo de arranque não-trivial (custo positivo
// para qualquer contexto ≥ 0), para que a reserva seja sempre > 0 (uma reserva
// nula não exercita o fail-closed).
var DefaultCostModel = CostModel{
	Version:                    "planner-pricing-v1",
	BaseTokens:                 256,
	BaseCostMicroUSD:           500,
	TokensPerContextUnit:       32,
	CostMicroUSDPerContextUnit: 64,
}

// PlanningContext é o input pinado do planeamento: o objectivo a decompor e as
// dimensões que alimentam a estimativa de custo e a proveniência. O objectivo é
// DADOS UNTRUSTED — alimenta o [Decomposer], NUNCA é gravado nos eventos.
type PlanningContext struct {
	// Goal é o objectivo de meta-nível a decompor (untrusted; não vai a eventos).
	Goal string
	// ContextUnits é a medida de tamanho do contexto (ex.: milhares de tokens de
	// contexto) que escala a estimativa de custo. ≥ 0.
	ContextUnits int64
	// CapabilitiesHash é o hash do snapshot de capabilities contra o qual a
	// decomposição é pinada (proveniência, §3.6). Id opaco, sem PII.
	CapabilitiesHash string
}

// DecomposeInput é o que o [Decomposer] recebe por tentativa: a identidade sob que
// corre, o número da tentativa e o contexto pinado.
type DecomposeInput struct {
	RunID      string
	PlanID     string
	PlannerNHI string
	Attempt    int
	Context    PlanningContext
}

// ---------------------------------------------------------------------------
// Erros — fail-closed, comparáveis por errors.Is.
// ---------------------------------------------------------------------------

var (
	// ErrPlannerDeps — dependências obrigatórias em falta na construção.
	ErrPlannerDeps = errors.New("planner: dependências obrigatórias em falta (reserver/mediator/issuer/decomposer)")
	// ErrInvalidRequest — pedido de decomposição malformado.
	ErrInvalidRequest = errors.New("planner: pedido de decomposição inválido")
	// ErrMediationDenied — o RM negou a admissão do planeador (ADR-002). A
	// decomposição NÃO arranca; nenhum orçamento é tocado.
	ErrMediationDenied = errors.New("planner: admissão negada pela mediação do RM (fail-closed)")
	// ErrNoPlanningBudget — a reserva de planeamento não foi admitida (sem headroom).
	// FAIL-CLOSED: o Decomposer NÃO é invocado, a decomposição não arranca.
	ErrNoPlanningBudget = errors.New("planner: reserva de planeamento não admitida — decomposição não arranca (fail-closed)")
	// ErrIssueIdentity — falhou a emissão da NHI agent:planner.
	ErrIssueIdentity = errors.New("planner: emissão da identidade agent:planner falhou")
	// ErrDecomposition — todas as N tentativas de decomposição falharam.
	ErrDecomposition = errors.New("planner: decomposição falhou em todas as tentativas")
	// ErrGate — o gate de admissibilidade de forma rejeitou o documento produzido.
	ErrGate = errors.New("planner: gate de forma rejeitou o PlanDocument")
)

// ---------------------------------------------------------------------------
// Constantes de span/atributo — sem literais no caminho de emissão de eventos;
// os spans usam a semconv GenAI + atributos aos.* locais (como observability.go).
// ---------------------------------------------------------------------------

const (
	// opPlannerGate — span do gate de admissibilidade de forma (não é chamada ao modelo).
	opPlannerGate = "planner.gate"

	attrPlanID          = "aos.plan.id"
	attrPlannerNHI      = "aos.planner.nhi"
	attrPlannerAttempt  = "aos.planner.attempt"
	attrPlannerMaxTries = "aos.planner.max_attempts"
	attrPlanNodeCount   = "aos.plan.node_count"
	attrGateAdmitted    = "aos.plan.gate_admitted"
)

// ---------------------------------------------------------------------------
// Planner.
// ---------------------------------------------------------------------------

// Planner corre a decomposição como agente governado. Construir com [NewPlanner].
// O estado é imutável após a construção; é seguro para uso concorrente na medida
// em que os colaboradores o são ([budget.Budget] CAS e [rm.Monitor] são
// thread-safe).
type Planner struct {
	reserver   Reserver
	mediator   Mediator
	issuer     PlannerIssuer
	decomposer Decomposer

	emitter AdmissionEmitter // opcional: projecção durável de plan.planner_admitted
	tracer  agentruntime.Tracer
	cost    CostModel

	maxAttempts int
	planToolID  string
	planCap     string
}

// Option configura o [Planner].
type Option func(*Planner)

// WithTracer injecta a porta de observabilidade (spans OTel GenAI). Default:
// [agentruntime.NoopTracer].
func WithTracer(t agentruntime.Tracer) Option {
	return func(p *Planner) {
		if t != nil {
			p.tracer = t
		}
	}
}

// WithAdmissionEmitter liga a projecção durável de `plan.planner_admitted` ao
// Event Store (via *plannerevents.Recorder). Sem emitter, a admissão é
// contabilizada só no orçamento e no trace (sem facto durável).
func WithAdmissionEmitter(e AdmissionEmitter) Option {
	return func(p *Planner) {
		if e != nil {
			p.emitter = e
		}
	}
}

// WithCostModel injecta a tabela de custo do planeamento. Default: [DefaultCostModel].
func WithCostModel(m CostModel) Option {
	return func(p *Planner) { p.cost = m }
}

// WithMaxAttempts define N — o número máximo de tentativas de decomposição e o
// FACTOR DE RETRY que dimensiona a reserva (reserva = custo-por-tentativa × N).
// <=0 mantém o default (3).
func WithMaxAttempts(n int) Option {
	return func(p *Planner) {
		if n > 0 {
			p.maxAttempts = n
		}
	}
}

// WithPlanCapability define o ToolID/Capability apresentados ao RM na mediação da
// admissão (default: "agent.plan" / "cap:agent.plan").
func WithPlanCapability(toolID, capability string) Option {
	return func(p *Planner) {
		if toolID != "" {
			p.planToolID = toolID
		}
		if capability != "" {
			p.planCap = capability
		}
	}
}

// NewPlanner constrói um Planner. reserver, mediator, issuer e decomposer são
// OBRIGATÓRIOS — a sua ausência é fail-closed ([ErrPlannerDeps]).
func NewPlanner(reserver Reserver, mediator Mediator, issuer PlannerIssuer, decomposer Decomposer, opts ...Option) (*Planner, error) {
	if reserver == nil || mediator == nil || issuer == nil || decomposer == nil {
		return nil, ErrPlannerDeps
	}
	p := &Planner{
		reserver:    reserver,
		mediator:    mediator,
		issuer:      issuer,
		decomposer:  decomposer,
		tracer:      agentruntime.NoopTracer{},
		cost:        DefaultCostModel,
		maxAttempts: 3,
		planToolID:  "agent.plan",
		planCap:     "cap:agent.plan",
	}
	for _, o := range opts {
		o(p)
	}
	if p.tracer == nil {
		p.tracer = agentruntime.NoopTracer{}
	}
	return p, nil
}

// DecomposeRequest descreve a admissão + decomposição de um meta-run.
type DecomposeRequest struct {
	// RunID é a árvore de execução (stream/treeID). Obrigatório.
	RunID string
	// PlanID é o stream do plano para os eventos aos.planner.v1. Default: RunID.
	PlanID string
	// ParentStepID correlaciona a admissão com o passo do run (opcional).
	ParentStepID string

	// ParentBudgetNode é o nó de orçamento do run (tem de existir). A reserva de
	// planeamento cascateia por ele e é CAS-validada contra o seu limite.
	ParentBudgetNode string
	// PlannerBudgetNode é o id do nó de orçamento do planeador (novo, filho do run).
	PlannerBudgetNode string

	// ParentToken é o token NHI compacto do run (Credential na mediação e pai da
	// emissão on-behalf-of da NHI agent:planner). Obrigatório.
	ParentToken string
	// Child é o pedido de emissão da NHI agent:planner (AgentID/classe/autoridade).
	// Obrigatório (AgentID não-vazio).
	Child identity.ChildRequest

	// ParentTraceParent é o traceparent W3C do span do run (AOS-077): os spans de
	// planeamento abrem COMO FILHOS deste. Vazio quando o ctx já carrega o
	// SpanContext do run (mesmo processo). Um traceparent malformado é ignorado
	// fail-open (os spans viram raiz de um trace novo) — não afecta orçamento/mediação.
	ParentTraceParent string

	// Context é o contexto de planeamento pinado (objectivo untrusted + dimensões).
	Context PlanningContext
}

// PlanResult é o resultado de uma decomposição admitida.
type PlanResult struct {
	// Doc é o PlanDocument produzido (untrusted; a materialização é o ORQ/AOS-237).
	Doc plan.PlanDocument
	// PlannerNHI é a identidade agent:planner emitida para este planeamento.
	PlannerNHI string
	// PlannerToken é o token NHI agent:planner emitido on-behalf-of o run: a sua
	// [identity.Token.Claims.DelegationChain] preserva a raiz humana e encadeia o run
	// (hash-linked). É devolvido para que o ORQ (AOS-237) invoque o materializador SOB
	// esta identidade e para que a cadeia de delegação seja auditável pelo chamador —
	// não apenas o AgentID, mas o encadeamento inteiro.
	PlannerToken identity.Token
	// Reservation é a reserva de planeamento consolidada (Commit no sucesso).
	Reservation budget.Reservation
	// Reserved é a quantia admitida (custo-por-tentativa × N).
	Reserved budget.Amount
	// Attempts é o número de tentativas efectivamente corridas até ao sucesso.
	Attempts int
}

// Decompose admite e corre a decomposição como agente governado. Passos
// (fail-closed em cada um):
//
//  1. estimativa de custo PURA (contexto × tabela) e reserva alvo = custo × N;
//  2. MEDIAÇÃO do RM (ADR-002) ANTES de qualquer efeito de orçamento — negada ⇒
//     [ErrMediationDenied], o [Decomposer] NÃO é invocado;
//  3. RESERVA CAS da reserva de planeamento (cascateia até ao run). Sem headroom ⇒
//     [ErrNoPlanningBudget], o [Decomposer] NÃO é invocado — a decomposição NÃO arranca;
//  4. emissão da NHI agent:planner (on-behalf-of o run) sob a admissão;
//  5. `plan.planner_admitted` durável (se houver emitter);
//  6. span-âncora invoke_agent do planeador (filho do run) + N spans chat (uma por
//     tentativa, com custo em tokens/USD) chamando o [Decomposer];
//  7. gate de admissibilidade de forma (span próprio) sobre o documento produzido;
//  8. consolidação: Commit da reserva no sucesso, Release na falha.
func (p *Planner) Decompose(ctx context.Context, req DecomposeRequest) (*PlanResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.RunID == "" || req.ParentBudgetNode == "" || req.PlannerBudgetNode == "" ||
		req.ParentToken == "" || req.Child.AgentID == "" {
		return nil, ErrInvalidRequest
	}
	planID := req.PlanID
	if planID == "" {
		planID = req.RunID
	}

	// 1) Estimativa PURA e reserva alvo = custo-por-tentativa × N (factor de retry).
	perAttempt := p.cost.PerAttempt(req.Context)
	reserve := scaleAmount(perAttempt, p.maxAttempts)
	if !amtPositive(reserve) {
		return nil, fmt.Errorf("%w: reserva de planeamento não-positiva", ErrInvalidRequest)
	}

	// 2) Mediação ANTES de qualquer efeito de orçamento (ADR-002). Negada ⇒ recusa
	// fail-closed SEM tocar no orçamento e SEM invocar o Decomposer.
	intent, _ := json.Marshal(struct {
		PlannerAgentID string        `json:"planner_agent_id"`
		Reserve        budget.Amount `json:"reserve"`
		MaxAttempts    int           `json:"max_attempts"`
	}{req.Child.AgentID, reserve, p.maxAttempts})
	call := rm.Call{
		RunID: req.RunID, StepID: "planstep:admit", ParentStepID: req.ParentStepID,
		ToolID: p.planToolID, Capability: p.planCap, Credential: req.ParentToken,
		Context: rm.CallContext{BudgetTokensRemaining: reserve.Tokens},
		Input:   intent,
	}
	dec, mErr := p.mediator.Mediate(ctx, call)
	if mErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrMediationDenied, mErr)
	}
	if !dec.Permitted() {
		return nil, fmt.Errorf("%w: effect=%s reason=%s", ErrMediationDenied, dec.Effect, dec.Reason)
	}

	// 3) Nó de orçamento do planeador + RESERVA CAS sob a admissão. FAIL-CLOSED: sem
	// headroom, devolve ErrNoPlanningBudget e a decomposição NÃO arranca — o
	// Decomposer nunca é invocado (é ESTE o gate do critério de aceitação).
	if err := p.reserver.AddNode(req.PlannerBudgetNode, req.ParentBudgetNode, reserve); err != nil && !errors.Is(err, budget.ErrNodeExists) {
		return nil, fmt.Errorf("planner: registar nó de orçamento do planeador: %w", err)
	}
	res, err := p.reserver.Reserve(ctx, req.PlannerBudgetNode, reserve)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoPlanningBudget, err)
	}

	// 4) Emissão da NHI agent:planner (on-behalf-of o run). Falha ⇒ liberta a reserva.
	tok, iErr := p.issuer.IssueChild(ctx, req.ParentToken, req.Child)
	if iErr != nil {
		_ = p.reserver.Release(ctx, res)
		return nil, fmt.Errorf("%w: %v", ErrIssueIdentity, iErr)
	}
	plannerNHI := tok.Claims.AgentID

	// 5) Facto durável plan.planner_admitted (se houver emitter). A constante do tipo
	// de evento é do plannerevents (via o Recorder) — não se declara tipo novo aqui.
	if p.emitter != nil {
		if _, eErr := p.emitter.RecordPlannerAdmitted(ctx, plannerevents.PlannerAdmittedPayload{
			PlanID:              planID,
			PlannerNHI:          plannerNHI,
			PricingTableVersion: p.cost.Version,
			RetryFactor:         p.maxAttempts,
			MaxAttempts:         p.maxAttempts,
		}); eErr != nil {
			// Fail-closed: se a admissão não pode ser gravada de forma durável, não se
			// prossegue com um planeamento cego. Liberta a reserva.
			_ = p.reserver.Release(ctx, res)
			return nil, fmt.Errorf("planner: gravar plan.planner_admitted: %w", eErr)
		}
	}

	// 6) Spans de planeamento — FILHOS do traceparent do run (AOS-077). Semeia o ctx
	// com o SpanContext do run quando ele não viaja no ctx (fronteira run→planeador).
	spanCtx := ctx
	if _, ok := agentruntime.SpanContextFromContext(ctx); !ok && req.ParentTraceParent != "" {
		if sc, perr := agentruntime.ParseTraceParent(req.ParentTraceParent); perr == nil {
			spanCtx = agentruntime.ContextWithSpanContext(ctx, sc)
		}
	}
	planCtx, planSpan := p.tracer.StartSpan(spanCtx, agentruntime.OpInvokeAgent)
	planSpan.SetAttribute(agentruntime.AttrOperationName, agentruntime.OpInvokeAgent)
	planSpan.SetAttribute(agentruntime.AttrRunID, req.RunID)
	planSpan.SetAttribute(attrPlanID, planID)
	planSpan.SetAttribute(attrPlannerNHI, plannerNHI)
	planSpan.SetAttribute(attrPlannerMaxTries, p.maxAttempts)

	// N tentativas de decomposição — cada uma um span chat FILHO do span-âncora, com
	// o custo em tokens/USD (o planeamento CUSTA tokens contabilizados; sem ponto cego).
	var doc plan.PlanDocument
	var dErr error
	attempts := 0
	for attempt := 1; attempt <= p.maxAttempts; attempt++ {
		attempts = attempt
		doc, dErr = p.runAttempt(planCtx, req, planID, plannerNHI, attempt, perAttempt)
		if dErr == nil {
			break
		}
	}
	if dErr != nil {
		planSpan.End()
		_ = p.reserver.Release(ctx, res)
		return nil, fmt.Errorf("%w: %v", ErrDecomposition, dErr)
	}

	// 7) Gate de admissibilidade de FORMA (span próprio). NÃO é a validação de grafo
	// (AOS-231/232) nem o gate HITL (AOS-121): reusa [plan.Decode] para recusar um
	// documento malformado antes do handoff ao materializador.
	admitted := p.gate(planCtx, doc)
	if !admitted {
		planSpan.End()
		_ = p.reserver.Release(ctx, res)
		return nil, ErrGate
	}
	planSpan.SetAttribute(attrPlanNodeCount, len(doc.Nodes))
	planSpan.End()

	// 8) Consolidação: Commit — o planeamento consumiu o seu envelope admitido (a
	// contabilidade durável fica no log do budget). Um erro de Commit é fail-closed.
	if cErr := p.reserver.Commit(ctx, res); cErr != nil {
		return nil, fmt.Errorf("planner: consolidar reserva de planeamento: %w", cErr)
	}

	return &PlanResult{
		Doc:          doc,
		PlannerNHI:   plannerNHI,
		PlannerToken: tok,
		Reservation:  res,
		Reserved:     reserve,
		Attempts:     attempts,
	}, nil
}

// runAttempt abre o span chat de uma tentativa (filho do span-âncora), anota o
// custo por tentativa e invoca o [Decomposer]. O span fecha sempre (defer). A
// tentativa é DENTRO do span-âncora do planeador — filha do run por transitividade.
func (p *Planner) runAttempt(ctx context.Context, req DecomposeRequest, planID, plannerNHI string, attempt int, perAttempt budget.Amount) (plan.PlanDocument, error) {
	_, span := p.tracer.StartSpan(ctx, agentruntime.OpChat)
	defer span.End()
	span.SetAttribute(agentruntime.AttrOperationName, agentruntime.OpChat)
	span.SetAttribute(agentruntime.AttrRunID, req.RunID)
	span.SetAttribute(attrPlanID, planID)
	span.SetAttribute(attrPlannerNHI, plannerNHI)
	span.SetAttribute(attrPlannerAttempt, attempt)
	// O custo por tentativa vive no span chat (a agregação por trajectória soma-o no
	// invoke_agent-âncora): o planeamento deixa de ser um ponto cego no burn-down.
	span.SetAttribute(agentruntime.AttrInputTokens, perAttempt.Tokens)
	span.SetAttribute(agentruntime.AttrCostUSD, float64(perAttempt.CostMicroUSD)/1_000_000.0)

	return p.decomposer.Decompose(ctx, DecomposeInput{
		RunID:      req.RunID,
		PlanID:     planID,
		PlannerNHI: plannerNHI,
		Attempt:    attempt,
		Context:    req.Context,
	})
}

// gate abre o span do gate e devolve se o documento é admissível de FORMA. Reusa
// [plan.Encode]+[plan.Decode] — o validador de forma sancionado do pacote plan —
// em vez de reimplementar validação. Fail-closed: qualquer erro ⇒ não admitido.
func (p *Planner) gate(ctx context.Context, doc plan.PlanDocument) bool {
	_, span := p.tracer.StartSpan(ctx, opPlannerGate)
	defer span.End()
	span.SetAttribute(agentruntime.AttrOperationName, opPlannerGate)
	admitted := false
	if raw, err := plan.Encode(doc); err == nil {
		if _, dErr := plan.Decode(raw); dErr == nil {
			admitted = true
		}
	}
	span.SetAttribute(attrGateAdmitted, admitted)
	return admitted
}

// ---------------------------------------------------------------------------
// Helpers de Amount (locais — o pacote não muta o budget).
// ---------------------------------------------------------------------------

// scaleAmount devolve a × n (n ≥ 1 na prática; o factor de retry). Componente-a-componente.
func scaleAmount(a budget.Amount, n int) budget.Amount {
	if n < 1 {
		n = 1
	}
	return budget.Amount{Tokens: a.Tokens * int64(n), CostMicroUSD: a.CostMicroUSD * int64(n)}
}

// amtPositive indica que a quantia é não-negativa e não-nula (reserva legítima).
func amtPositive(a budget.Amount) bool {
	return a.Tokens >= 0 && a.CostMicroUSD >= 0 && !a.IsZero()
}
