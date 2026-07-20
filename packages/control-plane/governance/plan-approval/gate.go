package planapproval

import (
	"context"
	"fmt"

	"github.com/aos-ref/control-plane/governance/autonomy"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// PlanReviewer é a PORTA (opcional) da superfície de EDIÇÃO do plano: apresenta o
// [PlanCard] ao humano e RECOLHE o veredicto RICO (approve/edit/reject) + o grafo
// revisto. É o que o [risk.ConfirmationChannel] binário NÃO modela (a edição). Sem um
// reviewer, o gate cai no gate binário puro: o [risk.ConfirmationChannel] decide
// aprovar/rejeitar (não há edição possível). Await/Review DEVE respeitar o ctx.
type PlanReviewer interface {
	Review(ctx context.Context, card PlanCard) (PlanDecision, error)
}

// PlanGate é o cerne de AOS-121: apresenta o GRAFO proposto ANTES DO SPAWN e devolve a
// [PlanDecision]. COMPÕE (não reimplementa):
//   - [autonomy.Oracle] — CONSULTA o nível do par (agente, domínio) e
//     [autonomy.Oversight]().Runs() para AUTO-APROVAR a níveis altos (consumo de AOS-089).
//   - [risk.ConfirmationChannel] (o [hitl.Channel]) — DEVOLVE a decisão binária assinada
//     (assinatura/autoridade/anti-replay/4-eyes/audit de AOS-095).
//   - [PlanReviewer] (opcional) — recolhe a EDIÇÃO rica (grafo revisto).
//   - [SpawnGuard] (opcional) — regista o run como aprovado, para a prova estrutural do
//     AC1 (nenhum spawn antes da aprovação).
//
// Seguro para concorrência na medida em que os colaboradores o forem. Construir com
// [NewPlanGate].
type PlanGate struct {
	oracle   autonomy.Oracle
	channel  risk.ConfirmationChannel
	reviewer PlanReviewer
	tracer   agentruntime.Tracer
	guard    *SpawnGuard
}

// GateOption configura o [PlanGate].
type GateOption func(*PlanGate)

// WithReviewer liga a superfície de edição rica (approve/edit/reject + grafo revisto).
// Sem ela, o gate opera em modo binário (o canal decide aprovar/rejeitar).
func WithReviewer(r PlanReviewer) GateOption {
	return func(g *PlanGate) {
		if r != nil {
			g.reviewer = r
		}
	}
}

// WithTracer injecta a porta de observabilidade do span [OpPlanApproval] (AC5). Por
// omissão não emite (tracer nil no-op).
func WithTracer(t agentruntime.Tracer) GateOption {
	return func(g *PlanGate) {
		if t != nil {
			g.tracer = t
		}
	}
}

// WithSpawnGuard liga o [SpawnGuard] que o gate marca como aprovado após uma decisão
// approve/edit — a prova ESTRUTURAL do AC1 (o guard recusa Spawn de run não-aprovado).
func WithSpawnGuard(sg *SpawnGuard) GateOption {
	return func(g *PlanGate) {
		if sg != nil {
			g.guard = sg
		}
	}
}

// NewPlanGate constrói o gate. oracle e channel são OBRIGATÓRIOS — a sua ausência é
// fail-closed ([ErrNilOracle]/[ErrNilChannel]): sem porta para consultar o nível ou para
// devolver a decisão assinada, um plano não pode ser aprovado com segurança.
func NewPlanGate(oracle autonomy.Oracle, channel risk.ConfirmationChannel, opts ...GateOption) (*PlanGate, error) {
	if oracle == nil {
		return nil, ErrNilOracle
	}
	if channel == nil {
		return nil, ErrNilChannel
	}
	g := &PlanGate{oracle: oracle, channel: channel}
	for _, o := range opts {
		o(g)
	}
	return g, nil
}

// Approve apresenta o plano e devolve a decisão — o GATE que se interpõe ANTES DO SPAWN.
// O fluxo:
//
//  1. CONSULTA autonomia: level := oracle.LevelFor(plan.Agent, plan.Domain); classe
//     AGREGADA dos nós (a mais severa). Se autonomy.Oversight(level, classe).Runs() →
//     AUTO-APROVA (sem gate humano, sem chamar o canal) — CONSOME o nível, NÃO decide.
//  2. Senão, constrói o [PlanCard] e recolhe a decisão: um [PlanReviewer] (se ligado)
//     devolve o veredicto rico (approve/edit/reject + grafo revisto); a decisão binária
//     (approve/reject) é DEVOLVIDA ao [risk.ConfirmationChannel] (assinatura/não-repúdio).
//  3. Só numa decisão approve/edit é que o run é registado como aprovado no [SpawnGuard]
//     — e só então o orquestrador pode spawnar. Uma edição devolve o grafo revisto para
//     o orquestrador RECONSTRUIR o DAG.
//
// Emite o span [OpPlanApproval] ligado ao run (AC5). Fail-closed: um plano inválido, um
// grafo com ciclo, um erro do reviewer/canal, ou uma recusa REJEITAM (nunca libertam o
// spawn).
func (g *PlanGate) Approve(ctx context.Context, plan Plan) (PlanDecision, error) {
	if err := plan.Validate(); err != nil {
		return PlanDecision{Verdict: VerdictReject, Reason: "plano invalido (fail-closed)"}, err
	}

	level := g.oracle.LevelFor(plan.Agent, plan.Domain)
	class := aggregateClass(plan.Nodes)
	mode := autonomy.Oversight(level, class)

	// (1) AUTO-APROVAÇÃO a níveis altos: consome autonomy.Oversight().Runs() — sem gate
	// humano, sem chamar o canal. NÃO decide/promove o nível (só o lê).
	if mode.Runs() {
		dec := PlanDecision{
			Verdict:      VerdictApprove,
			AutoApproved: true,
			Reason:       fmt.Sprintf("auto-aprovado: nivel %s, oversight %s (corre)", level.String(), mode.String()),
		}
		g.markApproved(plan.RunID, dec.Verdict)
		g.emit(ctx, plan, level, class, mode, dec)
		return dec, nil
	}

	// (2) GATE HUMANO: constrói o plan-card e recolhe a decisão.
	card, err := BuildPlanCard(plan)
	if err != nil {
		dec := PlanDecision{Verdict: VerdictReject, Reason: "falha a construir o plan-card (fail-closed)"}
		g.emit(ctx, plan, level, class, mode, dec)
		return dec, err
	}

	verdict := VerdictApprove
	effective := plan
	var revNodes []PlanNode
	var revEdges [][2]string

	// Edição rica (opcional): o reviewer devolve approve/edit/reject + grafo revisto.
	if g.reviewer != nil {
		rev, rerr := g.reviewer.Review(ctx, card)
		if rerr != nil {
			dec := PlanDecision{Verdict: VerdictReject, Reason: "reviewer falhou (fail-closed)"}
			g.emit(ctx, plan, level, class, mode, dec)
			return dec, rerr
		}
		switch rev.Verdict {
		case VerdictReject:
			dec := PlanDecision{Verdict: VerdictReject, Approver: rev.Approver, Reason: "plano rejeitado na revisao"}
			g.emit(ctx, plan, level, class, mode, dec)
			return dec, nil
		case VerdictEdit:
			verdict = VerdictEdit
			revNodes = rev.RevisedNodes
			revEdges = rev.RevisedEdges
			effective = rev.RevisedPlan(plan)
		default:
			verdict = VerdictApprove
		}
	}

	// Decisão binária assinada: DEVOLVE ao risk.ConfirmationChannel (o hitl.Channel) o
	// pedido AGREGADO do plano (efectivo — revisto se editado). Sem segredos no preview
	// (só a topologia: run + contagem de nós + domínio). O canal assina/verifica/sela.
	req := planConfirmationRequest(effective)
	resp, cerr := g.channel.Confirm(ctx, req)
	if cerr != nil || !resp.Approved {
		dec := PlanDecision{Verdict: VerdictReject, Reason: "nao aprovado pelo canal HITL (fail-closed)"}
		g.emit(ctx, plan, level, class, mode, dec)
		return dec, nil
	}

	dec := PlanDecision{
		Verdict:      verdict,
		Approver:     resp.Approver,
		RevisedNodes: revNodes,
		RevisedEdges: revEdges,
		Reason:       "plano aprovado pelo gate humano",
	}
	g.markApproved(plan.RunID, dec.Verdict)
	g.emit(ctx, plan, level, class, mode, dec)
	return dec, nil
}

// markApproved regista o run como aprovado no [SpawnGuard], se ligado, quando o veredicto
// LIBERTA o spawn (approve/edit). É o elo entre a decisão e a defesa estrutural do AC1.
func (g *PlanGate) markApproved(runID string, v Verdict) {
	if g.guard != nil && v.Approved() {
		g.guard.markApproved(runID)
	}
}

// emit emite o span de aprovação-de-plano (AC5), tolerante a tracer nil.
func (g *PlanGate) emit(ctx context.Context, plan Plan, level autonomy.Level, class risk.Class, mode autonomy.OversightMode, dec PlanDecision) {
	emitPlanSpan(ctx, g.tracer, plan, level, class, mode, dec)
}

// planConfirmationRequest constrói o pedido de confirmação AGREGADO do plano que o gate
// DEVOLVE ao [risk.ConfirmationChannel]. A classe é a AGREGADA (a mais severa dos nós); o
// preview é um resumo de TOPOLOGIA (run + contagem + domínio) — NUNCA segredos nem o
// input de qualquer nó. O Principal é o agente-raiz (base do 4-eyes do canal).
func planConfirmationRequest(plan Plan) risk.ConfirmationRequest {
	domain := plan.Domain
	if domain == "" {
		domain = autonomy.DomainUnknown
	}
	return risk.ConfirmationRequest{
		Class:        aggregateClass(plan.Nodes),
		Irreversible: aggregateIrreversible(plan.Nodes),
		Preview:      fmt.Sprintf("plano run=%s nos=%d dominio=%s", plan.RunID, len(plan.Nodes), domain),
		Principal:    plan.Agent,
		Capability:   "plan:" + domain,
		Resource:     plan.RunID,
	}
}
