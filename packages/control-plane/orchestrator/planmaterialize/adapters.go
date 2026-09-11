package planmaterialize

import (
	"context"

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

// O ADAPTADOR DE SPAWN SAIU DAQUI (AOS-390, ADR-024). `delegatorSpawner`/
// `NewDelegatorSpawner` — que ligavam a porta `Spawner` (removida) a
// *orchestrator.Delegator — deixaram de existir: o spawn de papéis já não acontece na
// materialização. O `DispatchSink` do despacho governado (composto no composition root
// aos-orq) usa o *orchestrator.Delegator directamente, disparado por elegibilidade.
//
// As correcções habilitadoras do AOS-393 são PRESERVADAS no sink (ADR-024): declarar a
// profundidade autoritativa do token do pai (`orchestrator.ChainDepth` → SpawnRequest.Depth,
// senão o gate anti-subdeclaração do Delegator recusa com ErrDepthMismatch) e a autoridade
// com escopo de tools no token do run/classe worker. Muda o MOMENTO do spawn, não a sua
// disciplina de identidade/profundidade.
