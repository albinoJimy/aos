package planmaterialize

import (
	"context"
	"fmt"
	"reflect"

	"github.com/aos-ref/control-plane/orchestrator"
	"github.com/aos-ref/control-plane/orchestrator/contract"
	"github.com/aos-ref/kernel/agent-runtime/state"
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

// AdmitLeaf admite o nó e persiste task.node.created — OU, se o nó já está no grafo
// re-hidratado da posse, CONFRONTA-O com o que o plano admitiria (AOS-476).
//
// # Porque a readmissão tem de ser idempotente
//
// A materialização escreve os nós, depois as arestas, e só no fim `plan.materialized`. Uma
// morte do processo (ou a perda da posse) entre o primeiro `task.node.created` e o
// `plan.materialized` deixava o run IRRECUPERÁVEL: a retoma só reconhece um plano
// materializado pelo `plan.materialized`, voltava a materializar, e a admissão do primeiro nó
// já durável falhava com [orchestrator.ErrNodeExists] — saída genérica, que o `consume`
// retentava até esgotar as gerações. Medido na revisão do AOS-476 com o WAL cortado depois
// dos nós.
//
// # Porque não basta engolir o «já existe»
//
// Um nó durável que NÃO coincide com o do plano não é o mesmo nó: aceitá-lo deixaria o grafo
// a afirmar uma tool call que o plano aprovado não tem. Compara-se tudo o que o
// `task.node.created` carrega e esta materialização escreveria — tool call, prioridade e
// identidade (vazias aqui: a NHI é do despacho) — e exige-se que o nó ainda esteja `ready`
// (sem `plan.materialized` nada o pode ter despachado). Divergência ⇒ [ErrNodeDiverges],
// fail-closed, sem escrever nada.
func (a graphLeafAdmitter) AdmitLeaf(ctx context.Context, node LeafNode) error {
	if !a.g.DAG().Has(node.NodeID) {
		return a.g.AddNode(ctx, especificacao(node))
	}
	// Segunda linha: o Materializer já confrontou o grafo inteiro antes de escrever
	// ([graphLeafAdmitter.confrontarTopologia]); esta porta não confia em quem a chama.
	return a.coincide(node)
}

// coincide confronta um nó já durável com o que o plano escreveria para ele: tudo o que o
// `task.node.created` carrega (tool call, prioridade, identidade) e o estado, que tem de ser
// ainda `ready`. Ver [graphLeafAdmitter.AdmitLeaf] e [graphLeafAdmitter.confrontarTopologia].
func (a graphLeafAdmitter) coincide(node LeafNode) error {
	spec := especificacao(node)
	dur, _ := a.g.DAG().Spec(node.NodeID)
	if !reflect.DeepEqual(dur, spec) {
		return fmt.Errorf("%w: %q no grafo tem tool=%q capability=%q prioridade=%d nhi=%q; o plano admite tool=%q capability=%q prioridade=0 sem nhi",
			ErrNodeDiverges, node.NodeID, dur.Task.ToolID, dur.Task.Capability, dur.Priority, dur.Agent.NHIID, spec.Task.ToolID, spec.Task.Capability)
	}
	if st, _ := a.g.DAG().State(node.NodeID); st != state.Ready {
		return fmt.Errorf("%w: %q já está %q no grafo sem plano materializado", ErrNodeDiverges, node.NodeID, st)
	}
	return nil
}

// especificacao é o NodeSpec que a materialização escreve para um nó.
func especificacao(node LeafNode) orchestrator.NodeSpec {
	return orchestrator.NodeSpec{
		TaskID: node.NodeID,
		Task:   contract.TaskSpec{ToolID: node.ToolID, Capability: node.Capability},
	}
}

// confrontarTopologia confronta o grafo re-hidratado da posse com o plano, ANTES de a
// materialização escrever o que quer que seja (AOS-476, revisão B2). Num run novo o grafo está
// vazio e não há nada a confrontar. Numa retoma, todo o nó durável tem de ser um nó do plano e
// toda a aresta durável uma aresta do plano: sem `plan.materialized` nada mais as pode ter
// escrito. Uma aresta a mais — invertida, por exemplo — faria a do plano fechar um ciclo no
// `AddEdge`, depois dos nós e com um `task.edge.rejected_cycle` no log; aqui recusa-se sem
// escrever, com [ErrEdgeDiverges].
func (a graphLeafAdmitter) confrontarTopologia(nos []LeafNode, arestas []planEdge) error {
	d := a.g.DAG()
	if d.Len() == 0 {
		return nil
	}
	doPlano := make(map[string]LeafNode, len(nos))
	for _, n := range nos {
		doPlano[n.NodeID] = n
	}
	duraveis, err := d.TopoOrder()
	if err != nil {
		return err
	}
	for _, id := range duraveis {
		n, ok := doPlano[id]
		if !ok {
			return fmt.Errorf("%w: %q está no grafo e não é do plano", ErrNodeDiverges, id)
		}
		if err := a.coincide(n); err != nil {
			return err
		}
	}
	doPlanoAresta := make(map[planEdge]bool, len(arestas))
	for _, e := range arestas {
		doPlanoAresta[e] = true
	}
	for _, de := range duraveis {
		for _, para := range duraveis {
			if d.HasEdge(de, para) && !doPlanoAresta[planEdge{from: de, to: para}] {
				return fmt.Errorf("%w: %s→%s está no grafo e o plano não a declara", ErrEdgeDiverges, de, para)
			}
		}
	}
	return nil
}

// AdmitEdge admite a dependência from→to e persiste task.edge.added (AOS-476). A
// aciclicidade é a do [orchestrator.GraphBuilder.AddEdge], contra o grafo re-hidratado da
// posse (ADR-023): uma aresta que feche ciclo é recusada, fica registada como
// task.edge.rejected_cycle e devolve [orchestrator.ErrEdgeClosesCycle].
//
// Numa materialização RETOMADA (ver [graphLeafAdmitter.AdmitLeaf]) uma aresta já durável é
// reemitida com a mesma chave de idempotência: o Event Store deduplica e o builder devolve
// nil, sem segundo `task.edge.added`.
func (a graphLeafAdmitter) AdmitEdge(ctx context.Context, from, to string) error {
	return a.g.AddEdge(ctx, from, to)
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
