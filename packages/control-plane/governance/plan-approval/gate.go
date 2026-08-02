package planapproval

import (
	"context"
	"fmt"

	approvalcard "github.com/aos-ref/control-plane/governance/approval-card"
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

// Revalidator é a PORTA de REVALIDAÇÃO (AOS-231) do plano EDITADO. Após uma edição na
// superfície de revisão, o gate DEVOLVE o grafo revisto a esta porta ANTES de aprovar —
// e SEM qualquer round-trip ao LLM (a edição/revalidação é local e determinista). O
// wiring liga-a a um adaptador do orchestrator/planvalidate — o gate NÃO importa o
// orquestrador: a revalidação entra SEMPRE por esta interface, preservando o
// zero-acoplamento. Revalidate DEVE respeitar o ctx e devolver erro fail-closed se o
// grafo revisto for inválido/inseguro (o gate recusa a aprovação, [ErrRevalidationFailed]).
type Revalidator interface {
	Revalidate(ctx context.Context, plan Plan) error
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
	oracle        autonomy.Oracle
	channel       risk.ConfirmationChannel
	reviewer      PlanReviewer
	revalidator   Revalidator
	tracer        agentruntime.Tracer
	guard         *SpawnGuard
	metrics       *risk.Metrics
	enforceForced bool
	perEffectDC   bool
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

// WithRevalidator liga a porta de REVALIDAÇÃO (AOS-231): após uma EDIÇÃO, o gate
// devolve-lhe o grafo revisto ANTES de aprovar. Uma revalidação falhada RECUSA a
// aprovação fail-closed ([ErrRevalidationFailed]). Sem ela, uma edição só passa pela
// revalidação estrutural LOCAL (validate/topo do grafo revisto) — o adaptador
// planvalidate (semântico/política) entra por esta porta no wiring.
func WithRevalidator(r Revalidator) GateOption {
	return func(g *PlanGate) {
		if r != nil {
			g.revalidator = r
		}
	}
}

// WithMetrics liga o registo do OVERRIDE-RATE (AOS-095): reutiliza o [risk.Metrics] do
// gate de risco (o mesmo SLI anti rubber-stamping de AOS-074/095). Cada passagem pelo
// gate HUMANO conta Prompted; uma aprovação humana (approve/edit) contra o piso de risco
// conta Overrides; uma recusa conta Denials. A auto-aprovação por nível NÃO conta (não
// houve prompt). Sem ela, não se regista override-rate (nil no-op).
func WithMetrics(m *risk.Metrics) GateOption {
	return func(g *PlanGate) {
		if m != nil {
			g.metrics = m
		}
	}
}

// WithForcedReview LIGA a imposição da triagem por risco: aprovar um plano sem rever
// item-a-item TODO nó cuja revisão é FORÇADA (Class >= gray ou capability_gap) é recusado
// fail-closed ([ErrForcedReviewMissing]). O [PlanReviewer] evidencia a revisão em
// [PlanDecision.ReviewedNodes]. Por omissão DESLIGADA (retrocompatível): o card modela a
// triagem, mas o gate só a IMPÕE quando esta opção é dada.
func WithForcedReview() GateOption {
	return func(g *PlanGate) { g.enforceForced = true }
}

// WithPerEffectDualControl LIGA a imposição INLINE do dual-control POR-EFEITO: ANTES da
// decisão agregada assinada, o gate constrói os cards INDIVIDUAIS por-efeito dos nós
// danger ([PlanCard.DangerEffectCards]) e devolve CADA um ao [approvalcard.DualControlCollector]
// (sobre o MESMO [risk.ConfirmationChannel] do gate) — um nó danger irreversível exige
// dois aprovadores DISTINTOS, por-efeito, sem qualquer round-trip ao LLM. Um efeito danger
// que não obtenha dual-control RECUSA o plano fail-closed ([ErrEffectDualControlFailed]).
// Por omissão DESLIGADA (retrocompatível): sem esta opção, o dual-control aplica-se à
// granularidade agregada do plano (o 4-eyes do canal), e os cards por-efeito ficam
// disponíveis ao wiring via [PlanCard.DangerEffectCards]. Compõe AOS-120 por porta — não
// importa o orquestrador.
func WithPerEffectDualControl() GateOption {
	return func(g *PlanGate) { g.perEffectDC = true }
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

	// (2) GATE HUMANO: a partir daqui HOUVE prompt humano — conta para o denominador do
	// override-rate (AOS-095). Constrói o plan-card e recolhe a decisão.
	g.countPrompted()

	card, err := BuildPlanCard(plan)
	if err != nil {
		dec := PlanDecision{Verdict: VerdictReject, Reason: "falha a construir o plan-card (fail-closed)"}
		g.countDenial()
		g.emit(ctx, plan, level, class, mode, dec)
		return dec, err
	}

	verdict := VerdictApprove
	effective := plan
	var revNodes []PlanNode
	var revEdges [][2]string
	var reviewedNodes []string
	var diff *PlanDiff

	// Edição rica (opcional): o reviewer devolve approve/edit/reject + grafo revisto + os
	// nós revistos item-a-item.
	if g.reviewer != nil {
		rev, rerr := g.reviewer.Review(ctx, card)
		if rerr != nil {
			dec := PlanDecision{Verdict: VerdictReject, Reason: "reviewer falhou (fail-closed)"}
			g.countDenial()
			g.emit(ctx, plan, level, class, mode, dec)
			return dec, rerr
		}
		reviewedNodes = rev.ReviewedNodes
		switch rev.Verdict {
		case VerdictReject:
			dec := PlanDecision{Verdict: VerdictReject, Approver: rev.Approver, Reason: "plano rejeitado na revisao"}
			g.countDenial()
			g.emit(ctx, plan, level, class, mode, dec)
			return dec, nil
		case VerdictEdit:
			verdict = VerdictEdit
			revNodes = rev.RevisedNodes
			revEdges = rev.RevisedEdges
			effective = rev.RevisedPlan(plan)

			// REVALIDAÇÃO (AOS-231) da edição ANTES de aprovar, SEM round-trip ao LLM:
			// (a) revalidação estrutural LOCAL do grafo revisto (validate + topo/ciclos);
			// (b) a porta [Revalidator] (adaptador planvalidate no wiring), se ligada.
			// Qualquer falha RECUSA fail-closed — uma edição que introduz invalidez nunca
			// chega a ser assinada/aprovada.
			if verr := g.revalidateEdit(ctx, effective); verr != nil {
				dec := PlanDecision{Verdict: VerdictReject, Approver: rev.Approver, Reason: "edicao nao revalidou (fail-closed)"}
				g.countDenial()
				g.emit(ctx, plan, level, class, mode, dec)
				return dec, verr
			}
			d := DiffPlans(plan, effective)
			diff = &d
		default:
			verdict = VerdictApprove
		}
	}

	// TRIAGEM POR RISCO (imposição opcional): TODO nó FORÇADO (Class >= gray ou
	// capability_gap) do plano EFECTIVO tem de ter sido revisto item-a-item — senão a
	// aprovação é recusada fail-closed, ANTES de qualquer assinatura.
	if g.enforceForced {
		if ferr := g.enforceForcedReview(effective, reviewedNodes); ferr != nil {
			dec := PlanDecision{Verdict: VerdictReject, Reason: "no forcado nao revisto (fail-closed)"}
			g.countDenial()
			g.emit(ctx, plan, level, class, mode, dec)
			return dec, ferr
		}
	}

	// DUAL-CONTROL POR-EFEITO (imposição inline opcional): ANTES da decisão agregada, cada
	// nó danger do plano EFECTIVO tem o seu card por-efeito autorizado pelo
	// [approvalcard.DualControlCollector] — um danger irreversível exige dois aprovadores
	// DISTINTOS por-efeito. Um efeito sem dual-control recusa fail-closed, ANTES de assinar
	// a decisão do plano. Sem round-trip ao LLM.
	if g.perEffectDC {
		if derr := g.enforcePerEffectDualControl(ctx, effective); derr != nil {
			dec := PlanDecision{Verdict: VerdictReject, Reason: "efeito danger sem dual-control (fail-closed)"}
			g.countDenial()
			g.emit(ctx, plan, level, class, mode, dec)
			return dec, derr
		}
	}

	// Decisão binária assinada: DEVOLVE ao risk.ConfirmationChannel (o hitl.Channel) o
	// pedido AGREGADO do plano (efectivo — revisto se editado). Sem segredos no preview
	// (só a topologia: run + contagem de nós + domínio). O canal assina/verifica/sela.
	req := planConfirmationRequest(effective)
	resp, cerr := g.channel.Confirm(ctx, req)
	if cerr != nil || !resp.Approved {
		dec := PlanDecision{Verdict: VerdictReject, Reason: "nao aprovado pelo canal HITL (fail-closed)"}
		g.countDenial()
		g.emit(ctx, plan, level, class, mode, dec)
		return dec, nil
	}

	// OVERRIDE-RATE (AOS-095): uma aprovação HUMANA de um plano gatado é um OVERRIDE do
	// piso de risco — conta para o numerador do override-rate (anti rubber-stamping).
	g.countOverride()

	dec := PlanDecision{
		Verdict:       verdict,
		Approver:      resp.Approver,
		RevisedNodes:  revNodes,
		RevisedEdges:  revEdges,
		ReviewedNodes: reviewedNodes,
		Diff:          diff,
		Reason:        "plano aprovado pelo gate humano",
	}
	g.markApproved(plan.RunID, dec.Verdict)
	g.emit(ctx, plan, level, class, mode, dec)
	return dec, nil
}

// revalidateEdit revalida o grafo EDITADO antes de aprovar (AOS-231), SEM round-trip ao
// LLM: primeiro a revalidação estrutural LOCAL (o grafo revisto tem de ser válido e
// acíclico — usa [Plan.Validate]/[Plan.TopoOrder], não importa o orquestrador), depois a
// porta [Revalidator] (o adaptador planvalidate do wiring), se ligada. Qualquer falha é
// [ErrRevalidationFailed] (embrulhando a causa) — fail-closed.
func (g *PlanGate) revalidateEdit(ctx context.Context, effective Plan) error {
	if err := effective.Validate(); err != nil {
		return fmt.Errorf("%w: grafo revisto invalido: %v", ErrRevalidationFailed, err)
	}
	if _, err := effective.TopoOrder(); err != nil {
		return fmt.Errorf("%w: %v", ErrRevalidationFailed, err)
	}
	if g.revalidator != nil {
		if err := g.revalidator.Revalidate(ctx, effective); err != nil {
			return fmt.Errorf("%w: %v", ErrRevalidationFailed, err)
		}
	}
	return nil
}

// enforcePerEffectDualControl impõe, INLINE, o dual-control POR-EFEITO dos nós danger do
// plano EFECTIVO: constrói o card do plano (fail-closed se inválido), extrai os cards
// individuais por-efeito danger ([PlanCard.DangerEffectCards]) e devolve CADA um ao
// [approvalcard.DualControlCollector] sobre o MESMO canal do gate. Um card por-efeito não
// autorizado (falta de quórum de dois aprovadores distintos, recusa ou erro do canal)
// devolve [ErrEffectDualControlFailed]. Sem nós danger, é no-op. Não importa o orquestrador
// (compõe AOS-120 por porta).
func (g *PlanGate) enforcePerEffectDualControl(ctx context.Context, effective Plan) error {
	card, err := BuildPlanCard(effective)
	if err != nil {
		return err
	}
	danger := card.DangerEffectCards()
	if len(danger) == 0 {
		return nil
	}
	coll, err := approvalcard.NewDualControlCollector(g.channel)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEffectDualControlFailed, err)
	}
	for i := range danger {
		dec, aerr := coll.Authorize(ctx, danger[i])
		if aerr != nil {
			return fmt.Errorf("%w: %v", ErrEffectDualControlFailed, aerr)
		}
		if !dec.Authorized {
			return fmt.Errorf("%w: efeito %q", ErrEffectDualControlFailed, danger[i].Resource)
		}
	}
	return nil
}

// enforceForcedReview impõe a triagem por risco sobre o plano EFECTIVO: constrói o card
// do plano (fail-closed se inválido) e exige que TODO task_id forçado (Class >= gray ou
// capability_gap) conste dos nós revistos item-a-item. Um nó forçado por rever devolve
// [ErrForcedReviewMissing].
func (g *PlanGate) enforceForcedReview(effective Plan, reviewed []string) error {
	card, err := BuildPlanCard(effective)
	if err != nil {
		return err
	}
	if !coversAll(reviewed, card.ForcedTaskIDs()) {
		return ErrForcedReviewMissing
	}
	return nil
}

// coversAll indica se todos os required constam de reviewed (contenção de conjunto).
func coversAll(reviewed, required []string) bool {
	set := make(map[string]bool, len(reviewed))
	for _, r := range reviewed {
		set[r] = true
	}
	for _, req := range required {
		if !set[req] {
			return false
		}
	}
	return true
}

// countPrompted/countOverride/countDenial actualizam o [risk.Metrics] do override-rate
// (AOS-095), tolerantes a metrics nil (no-op). Prompted = passou pelo gate humano;
// Overrides = aprovação humana contra o piso; Denials = recusa.
func (g *PlanGate) countPrompted() {
	if g.metrics != nil {
		g.metrics.Prompted.Add(1)
	}
}

func (g *PlanGate) countOverride() {
	if g.metrics != nil {
		g.metrics.Overrides.Add(1)
	}
}

func (g *PlanGate) countDenial() {
	if g.metrics != nil {
		g.metrics.Denials.Add(1)
	}
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
