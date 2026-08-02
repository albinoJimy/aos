package planmaterialize

import (
	"context"

	budget "github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/contract"
)

// Adaptadores de wiring (composition root). Ligam as PORTAS deste pacote aos tipos
// CONCRETOS já commitados do módulo (importados, nunca editados): o [LeafAdmitter] a
// *orchestrator.GraphBuilder (AOS-025) e o [Spawner] a *orchestrator.Delegator
// (AOS-026). A admissão global (AOS-027/028) e a projecção de `plan.materialized`
// (*plannerevents.Recorder satisfaz [MaterializeRecorder] directamente) são ligadas
// pelo root — não têm adaptador aqui.

// graphLeafAdmitter liga [LeafAdmitter] a *orchestrator.GraphBuilder: AdmitLeaf
// admite o nó no DAG e persiste task.node.created (AOS-025). Side-effect completo.
//
// FOLHA MULTI-TOOL (fronteira honesta, §5). contract.TaskSpec (AOS-025, tipo irmão
// congelado) carrega UMA tool call (ToolID+Capability). Uma folha com >1 tool
// materializa no DAG apenas a PRIMEIRA (LeafNode.ToolID/Capability, em ordem do
// documento). NÃO há perda de autoridade: o conjunto coarse COMPLETO da folha viaja
// em LeafNode.Capabilities e fica registado AUTORITATIVAMENTE em
// plan.materialized.Nodes[].Tools (a projecção que governa a autoridade). O DAG de
// AOS-025 é single-tool por construção; representá-lo multi-tool exige estender o
// tipo irmão (fora de AOS-237). Esta redução é DETERMINÍSTICA (primeira em ordem
// canónica), nunca silenciosa face ao registo.
type graphLeafAdmitter struct{ g *orchestrator.GraphBuilder }

// NewGraphLeafAdmitter adapta um *orchestrator.GraphBuilder à porta [LeafAdmitter].
func NewGraphLeafAdmitter(g *orchestrator.GraphBuilder) LeafAdmitter {
	return graphLeafAdmitter{g: g}
}

func (a graphLeafAdmitter) AdmitLeaf(ctx context.Context, node LeafNode) error {
	return a.g.AddNode(ctx, orchestrator.NodeSpec{
		TaskID: node.NodeID,
		Task:   contract.TaskSpec{ToolID: node.ToolID, Capability: node.Capability},
	})
}

// delegatorSpawner liga [Spawner] a *orchestrator.Delegator: Spawn cria o sub-agente
// com orçamento herdado e a NHI filha, cuja Authority JÁ vem clampada às tools do
// papel (RoleSpawn.Child.Authority). O issuer_child intersecta ainda com o escopo da
// classe e recusa escalada (defesa-em-profundidade).
//
// LIFECYCLE (fronteira honesta, §5). Delegator.Spawn reserva orçamento e devolve um
// *SpawnHandle a CONSOLIDAR com Delegator.Finish no fim do sub-run — isso é do
// ciclo-de-vida do run, não da materialização. O handle é entregue ao onHandle
// fornecido pelo root (que o rastreia para o Finish); um onHandle nil descarta-o
// (útil só em cenários sem consolidação).
type delegatorSpawner struct {
	d        *orchestrator.Delegator
	onHandle func(*orchestrator.SpawnHandle)
}

// NewDelegatorSpawner adapta um *orchestrator.Delegator à porta [Spawner]. onHandle
// recebe o *SpawnHandle de cada spawn para o root o consolidar (Finish); pode ser
// nil.
func NewDelegatorSpawner(d *orchestrator.Delegator, onHandle func(*orchestrator.SpawnHandle)) Spawner {
	return delegatorSpawner{d: d, onHandle: onHandle}
}

func (s delegatorSpawner) Spawn(ctx context.Context, req RoleSpawn) error {
	h, err := s.d.Spawn(ctx, orchestrator.SpawnRequest{
		RunID:            req.RunID,
		ParentBudgetNode: req.ParentBudgetNode,
		ChildBudgetNode:  req.ChildBudgetNode,
		InheritedBudget:  budget.Amount{Tokens: req.InheritedTokens, CostMicroUSD: req.InheritedCostMicroUSD},
		ParentToken:      req.ParentToken,
		Child:            req.Child,
		ChildTaskID:      req.NodeID,
	})
	if err != nil {
		return err
	}
	if s.onHandle != nil {
		s.onHandle(h)
	}
	return nil
}
