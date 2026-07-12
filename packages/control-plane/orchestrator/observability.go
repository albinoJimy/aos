package orchestrator

import (
	"context"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// Instrumentação OTel do plano de controlo (DoD de AOS-025 / pilar de métricas do
// EPIC-08). Reusa a PORTA de observabilidade leve do Agent Runtime
// ([agentruntime.Tracer]/[agentruntime.Span]) — os mesmos spans OTel GenAI e a
// semconv já usada pelo loop de AOS-013 — SEM puxar o SDK OTel (zero-dep). O
// default é [agentruntime.NoopTracer] (sem overhead); os testes injectam um
// [agentruntime.RecordingTracer].
//
// A admissão de um nó no DAG é a DECOMPOSIÇÃO do objectivo num (sub-)agente
// delegável: por isso o span do nó é invoke_agent (a mesma operação que o Agent
// Runtime abrirá ao executar o nó), portador dos atributos de decomposição e do
// custo por span. O custo DIRECTO da decomposição é 0 (não se invoca modelo ao
// construir o grafo); o custo real acumula na execução do nó, sob o mesmo
// invoke_agent, e é agregado por run_id → trace.

const (
	// opAddEdge — span da admissão de uma aresta de dependência no DAG.
	opAddEdge = "orchestrator.add_edge"
	// opResolveDeadlock — span da detecção+resolução de uma espera circular.
	opResolveDeadlock = "orchestrator.resolve_deadlock"

	// attrNodeTaskID — task_id do nó decomposto (atributo de decomposição).
	attrNodeTaskID = "aos.node.task_id"
	// attrNodePriority — prioridade do nó (governa a escolha da vítima).
	attrNodePriority = "aos.node.priority"
	// attrNodeAgentNHI — identidade não-humana delegada ao nó (cadeia on-behalf-of).
	attrNodeAgentNHI = "aos.node.agent_nhi"
	// attrNodeDelegationDepth — profundidade da cadeia de delegação do nó.
	attrNodeDelegationDepth = "aos.node.delegation_depth"

	// attrEdgeFrom/attrEdgeTo — a aresta admitida (From precede To).
	attrEdgeFrom = "aos.edge.from"
	attrEdgeTo   = "aos.edge.to"

	// attrDeadlockTaskCount — cardinalidade da espera circular.
	attrDeadlockTaskCount = "aos.deadlock.task_count"
	// attrDeadlockVictim — a tarefa abortada pela política.
	attrDeadlockVictim = "aos.deadlock.victim"
	// attrDeadlockPolicy — o rótulo da política de resolução aplicada.
	attrDeadlockPolicy = "aos.deadlock.policy"
)

// startNodeSpan abre o span invoke_agent da decomposição de um nó e anota os
// atributos de decomposição e o custo (0 na decomposição; ver nota no topo). O
// chamador fecha com span.End().
func startNodeSpan(ctx context.Context, tracer agentruntime.Tracer, runID, stepID string, spec NodeSpec) (context.Context, agentruntime.Span) {
	ctx, span := tracer.StartSpan(ctx, agentruntime.OpInvokeAgent)
	span.SetAttribute(agentruntime.AttrOperationName, agentruntime.OpInvokeAgent)
	span.SetAttribute(agentruntime.AttrRunID, runID)
	span.SetAttribute(agentruntime.AttrStepID, stepID)
	span.SetAttribute(attrNodeTaskID, spec.TaskID)
	span.SetAttribute(attrNodePriority, spec.Priority)
	span.SetAttribute(attrNodeAgentNHI, spec.Agent.NHIID)
	span.SetAttribute(attrNodeDelegationDepth, len(spec.Agent.DelegationChain))
	if spec.Task.ToolID != "" {
		span.SetAttribute(agentruntime.AttrToolName, spec.Task.ToolID)
	}
	span.SetAttribute(agentruntime.AttrCostUSD, 0.0)
	return ctx, span
}
