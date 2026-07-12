package contract

// Vocabulário de eventos do GRAFO DE TAREFAS ACÍCLICO e da DETECÇÃO DE DEADLOCK
// (AOS-025). Estende o contrato de AOS-012 SEM renomear os tipos existentes: são
// factos append-only adicionais gravados no mesmo stream (run_id) do Event Store
// (ADR-007 — Event Store fonte de verdade). Todos os payloads são structs (nunca
// mapas) para serialização JSON ESTÁVEL e replay determinístico (ADR-010).
const (
	// EventTaskNodeCreated — o Orquestrador admitiu um nó-tarefa no DAG do run.
	EventTaskNodeCreated = "task.node.created"
	// EventTaskEdgeAdded — uma aresta de dependência foi admitida no DAG (não
	// fecha ciclo).
	EventTaskEdgeAdded = "task.edge.added"
	// EventEdgeRejectedCycle — uma aresta foi REJEITADA na admissão porque
	// fecharia um ciclo (aciclicidade fail-closed). O facto regista a razão.
	EventEdgeRejectedCycle = "task.edge.rejected_cycle"
	// EventTaskNodeStateChanged — o estado de execução de um nó transitou (p.ex.
	// ready→running no claim). É o facto DURÁVEL que torna o estado por-nó
	// reconstruível por replay (ADR-010): sem ele, RebuildDAG só reporia a
	// topologia e todos os nós regressariam a ready. Ver [StepNodeStateChanged].
	EventTaskNodeStateChanged = "task.node.state_changed"
	// EventDeadlockDetected — o detector encontrou espera circular no wait-for
	// graph; o payload transporta o CONJUNTO de tarefas envolvidas.
	EventDeadlockDetected = "deadlock.detected"
	// EventDeadlockResolved — a política de resolução determinística abortou a
	// vítima e libertou os recursos; o payload regista a vítima e os recursos.
	EventDeadlockResolved = "deadlock.resolved"
)

// DelegationHop é um elo da cadeia de delegação on-behalf-of (ADR-003): o sujeito
// (Sub) age como (ActAs) a identidade seguinte. Espelha eventstore.DelegationHop
// sem acoplar o contrato ao pacote do Event Store.
type DelegationHop struct {
	Sub   string `json:"sub"`
	ActAs string `json:"act_as"`
}

// AgentIdentity é a identidade não-humana (NHI) de um nó-tarefa e a sua cadeia de
// delegação, coerente com a cadeia on-behalf-of (ADR-003). Cada nó do DAG é
// delegável a um (sub-)agente com identidade própria; a cadeia termina num humano
// responsável.
type AgentIdentity struct {
	NHIID           string          `json:"nhi_id,omitempty"`
	DelegationChain []DelegationHop `json:"delegation_chain,omitempty"`
}

// TaskNodeCreatedPayload é o corpo de task.node.created.
//
// As DEPENDÊNCIAS de um nó NÃO são transportadas aqui: são factos próprios
// (task.edge.added) — cada aresta From→To é um evento independente e é dela que
// RebuildDAG reconstrói o grafo. Não há campo Deps para não haver duas fontes de
// verdade contraditórias para a mesma dependência.
type TaskNodeCreatedPayload struct {
	RunID  string        `json:"run_id"`
	TaskID string        `json:"task_id"`
	State  string        `json:"state"`
	Prio   int           `json:"priority"`
	Agent  AgentIdentity `json:"agent,omitzero"`

	ToolID     string `json:"tool_id,omitempty"`
	Capability string `json:"capability,omitempty"`
}

// TaskNodeStateChangedPayload é o corpo de task.node.state_changed: a transição
// de estado APLICADA e VALIDADA de um nó (From→To), pela tabela declarativa da
// máquina durável de AOS-017. É o registo append-only a partir do qual o replay
// repõe o estado de execução por-nó (não só a topologia).
type TaskNodeStateChangedPayload struct {
	RunID  string `json:"run_id"`
	TaskID string `json:"task_id"`
	From   string `json:"from"`
	To     string `json:"to"`
}

// TaskEdgeAddedPayload é o corpo de task.edge.added. A aresta exprime que To
// DEPENDE de From (From precede To na ordem de execução).
type TaskEdgeAddedPayload struct {
	RunID string `json:"run_id"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// EdgeRejectedCyclePayload é o corpo de task.edge.rejected_cycle: a aresta
// From→To foi recusada na admissão porque fecharia um ciclo. Reason é a razão
// explícita e legível exigida pelo critério de aceitação (fail-closed).
type EdgeRejectedCyclePayload struct {
	RunID  string `json:"run_id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

// DeadlockDetectedPayload é o corpo de deadlock.detected. Tasks é o conjunto
// (ordenado lexicograficamente, determinístico) de tarefas na espera circular;
// Resources são os recursos disputados no ciclo.
type DeadlockDetectedPayload struct {
	RunID     string   `json:"run_id"`
	Tasks     []string `json:"tasks"`
	Resources []string `json:"resources,omitempty"`
}

// DeadlockResolvedPayload é o corpo de deadlock.resolved: a política abortou
// Victim e libertou ReleasedResources; VictimFrom→VictimTo é a transição de
// estado aplicada ao nó vítima (validada pela máquina de estados de AOS-017).
type DeadlockResolvedPayload struct {
	RunID             string   `json:"run_id"`
	Tasks             []string `json:"tasks"`
	Victim            string   `json:"victim"`
	Policy            string   `json:"policy"`
	ReleasedResources []string `json:"released_resources,omitempty"`
	VictimFrom        string   `json:"victim_from"`
	VictimTo          string   `json:"victim_to"`
}

// Os Step* do DAG derivam StepIDs DETERMINÍSTICOS e DISTINTOS por facto, para que
// a idempotency_key (run_id + ":" + step_id) do Event Store seja única no stream —
// sem colidir com os passos do ciclo de vida da tarefa (Step* de events.go) nem
// com o namespacing "state-N" da máquina durável (AOS-017). São a base da
// idempotência por passo (ADR-001): reemitir o mesmo facto é deduplicado, nunca
// duplicado.
func StepNodeCreated(taskID string) string { return "node:" + taskID }
func StepNodeStateChanged(taskID, to string) string {
	return "node-st:" + taskID + ":" + to
}
func StepEdgeAdded(from, to string) string    { return "edge:" + from + ">" + to }
func StepEdgeRejected(from, to string) string { return "edge-x:" + from + ">" + to }
func StepDeadlockDetected(key string) string  { return "dl:" + key }
func StepDeadlockResolved(key string) string  { return "dl-fix:" + key }
