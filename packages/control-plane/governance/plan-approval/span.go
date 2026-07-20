package planapproval

import (
	"context"

	"github.com/aos-ref/control-plane/governance/autonomy"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// Vocabulário de span da APROVAÇÃO-DE-PLANO (AC5). O span [OpPlanApproval] liga-se ao
// RUN (via [agentruntime.AttrRunID]) e carrega a dimensão que só o gate de plano conhece
// (aos.plan.approval.*): a contagem de nós do grafo, o nível de autonomia consultado, se
// foi auto-aprovado, o veredicto e a versão de schema. O NÍVEL/oversight são também
// expostos pelo vocabulário de AOS-089 ([autonomy.ExposeLevel]/[autonomy.AnnotateOversight]),
// para que o span de plano e o de autonomia sejam consultáveis em conjunto.
//
// NENHUM atributo transporta segredo/PII: NUNCA o preview de um nó, o input de uma tool,
// nem qualquer chave. Contagem, nível, classe, veredicto e versão não são segredos.
const (
	// OpPlanApproval é o nome do span de aprovação-de-plano (plano apresentado + decisão).
	// É DISTINTO do OpApprovalCard de AOS-120 (que é por-acção) — opera sobre o GRAFO.
	OpPlanApproval = "plan_approval"

	// AttrPlanNodeCount — aos.plan.approval.node_count: nº de nós do grafo proposto.
	AttrPlanNodeCount = "aos.plan.approval.node_count"
	// AttrPlanAutonomyLevel — aos.plan.approval.autonomy_level: o nível consultado ("L0".."L5").
	AttrPlanAutonomyLevel = "aos.plan.approval.autonomy_level"
	// AttrPlanAutoApproved — aos.plan.approval.auto_approved: a decisão foi auto-aprovada
	// por nível (sem gate humano).
	AttrPlanAutoApproved = "aos.plan.approval.auto_approved"
	// AttrPlanVerdict — aos.plan.approval.verdict: approve/edit/reject.
	AttrPlanVerdict = "aos.plan.approval.verdict"
	// AttrPlanSchemaVersion — aos.plan.approval.schema_version: a linha de contrato do plan-card.
	AttrPlanSchemaVersion = "aos.plan.approval.schema_version"
)

// emitPlanSpan abre e fecha o span de aprovação-de-plano (AC5) ligado ao trace do run
// (via ctx + AttrRunID): a contagem de nós, o nível de autonomia, se foi auto-aprovado, o
// veredicto e a versão de schema. Adicionalmente EXPÕE o nível corrente do par pelo
// vocabulário de AOS-089 ([autonomy.ExposeLevel] + [autonomy.AnnotateOversight]) — o
// mesmo nível/classe/oversight consultado, tornando a decisão observável ponta-a-ponta.
// Sem segredos/PII. Um tracer nil não emite nada.
func emitPlanSpan(ctx context.Context, tracer agentruntime.Tracer, plan Plan, level autonomy.Level, class risk.Class, mode autonomy.OversightMode, dec PlanDecision) {
	if tracer == nil {
		return
	}
	ctx, span := tracer.StartSpan(ctx, OpPlanApproval)
	span.SetAttribute(agentruntime.AttrOperationName, OpPlanApproval)
	span.SetAttribute(agentruntime.AttrRunID, plan.RunID)
	span.SetAttribute(AttrPlanNodeCount, len(plan.Nodes))
	span.SetAttribute(AttrPlanAutonomyLevel, level.String())
	span.SetAttribute(AttrPlanAutoApproved, dec.AutoApproved)
	span.SetAttribute(AttrPlanVerdict, dec.Verdict.String())
	span.SetAttribute(AttrPlanSchemaVersion, CurrentVersion.String())
	span.End()

	// Expõe o nível/oversight consultado pelo vocabulário partilhado de AOS-089, ligado
	// ao mesmo trace pelo ctx do span de plano (não duplica a decisão — anota o nível).
	_, lvlSpan := autonomy.ExposeLevel(ctx, tracer, plan.Agent, plan.Domain, level)
	autonomy.AnnotateOversight(lvlSpan, class, mode)
	lvlSpan.End()
}
