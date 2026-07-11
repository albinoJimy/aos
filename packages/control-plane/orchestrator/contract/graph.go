package contract

// TaskSpec descreve a tool call de uma tarefa: o que o Escalonador submeterá ao
// Reference Monitor (AOS-003). É a unidade de trabalho de um nó do grafo.
type TaskSpec struct {
	ToolID     string
	Capability string
	Resource   ResourceSpec
	Input      []byte
}

// Goal é o objectivo submetido a Orchestrator.Submit. No esqueleto AOS-012 o
// objectivo transporta directamente a (única) tool call de brinquedo — NÃO há
// decomposição real de linguagem natural em grafo.
//
// PONTO DE EXTENSÃO (EPIC-03): substituir Task por uma descrição de alto nível
// que o Orquestrador decompõe num grafo acíclico multi-nó (planeamento real).
type Goal struct {
	Objective string
	// Task é a especificação da tool call do nó único do grafo mínimo (stub).
	Task TaskSpec
}

// TaskNode é um nó do grafo de tarefas.
type TaskNode struct {
	TaskID string
	Spec   TaskSpec
	State  State
	// Deps são os task_ids de que este nó depende. VAZIO no esqueleto (grafo
	// trivial de 1 nó). PONTO DE EXTENSÃO (EPIC-03): arestas do DAG real.
	Deps []TaskID
}

// Graph é o grafo de tarefas acíclico de um run. No esqueleto AOS-012 tem
// exactamente 1 nó, sem arestas.
type Graph struct {
	RunID string
	Nodes []TaskNode
}

// MinimalTaskID é o task_id do nó único do grafo mínimo.
const MinimalTaskID = "t1"

// NewMinimalGraph constrói o grafo acíclico TRIVIAL (1 nó, sem arestas) a partir
// de um Goal. É o stub de decomposição: mapeia o objectivo directamente para uma
// única tarefa em estado ready. EPIC-03 substitui isto por decomposição real.
func NewMinimalGraph(runID string, goal Goal) Graph {
	return Graph{
		RunID: runID,
		Nodes: []TaskNode{{
			TaskID: MinimalTaskID,
			Spec:   goal.Task,
			State:  StateReady,
		}},
	}
}
