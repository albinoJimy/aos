package contract

// Tipos de evento canónicos do plano de controlo (AOS-012). São o vocabulário
// estável trocado no barramento (AOS-009) entre Orquestrador (produtor) e
// Escalonador (consumidor). Todos correlacionados por run_id (stream_id) e,
// quando aplicável, task_id/step_id.
//
// PONTO DE EXTENSÃO (EPIC-03): novos tipos (ex.: task.waiting_on_tool,
// task.escalated) acrescentam-se sem renomear os existentes.
const (
	// EventRunCreated — o Orquestrador criou um run (grafo de tarefas mínimo).
	EventRunCreated = "run.created"
	// EventTaskReady — uma tarefa está pronta para escalonamento (estado ready).
	EventTaskReady = "task.ready"
	// EventTaskRunning — o Escalonador reclamou a tarefa (transição ready→running).
	EventTaskRunning = "task.running"
	// EventTaskComplete — a tarefa terminou com sucesso (running→complete).
	EventTaskComplete = "task.complete"
	// EventTaskFailed — a tarefa falhou (running→failed): negada pelo RM, erro de
	// tool, ou transição de estado inválida.
	EventTaskFailed = "task.failed"
)

// RunID identifica um run de forma estável. É o stream_id no Event Store e a
// chave de correlação de todos os eventos de um run.
type RunID string

// TaskID identifica uma tarefa (nó do grafo) dentro de um run.
type TaskID string

// RunCreatedPayload é o corpo do evento run.created.
type RunCreatedPayload struct {
	RunID     string `json:"run_id"`
	Objective string `json:"objective"`
	// TaskCount é o número de nós do grafo. No esqueleto AOS-012 é sempre 1
	// (grafo acíclico trivial); EPIC-03 emite o grafo real decomposto.
	TaskCount int `json:"task_count"`
}

// ResourceSpec é o alvo concreto da tool call de uma tarefa (espelha
// referencemonitor.Resource sem acoplar o contrato ao pacote do RM).
type ResourceSpec struct {
	Type   string `json:"type,omitempty"`
	Value  string `json:"value,omitempty"`
	Region string `json:"region,omitempty"`
}

// TaskPayload é o corpo dos eventos task.* (ready/running/complete/failed). Um
// único tipo cobre todo o ciclo de vida da tarefa; o campo State (e o Type do
// envelope) distinguem a fase. Correlacionado por RunID + TaskID + StepID.
type TaskPayload struct {
	RunID  string `json:"run_id"`
	TaskID string `json:"task_id"`
	// StepID é o passo durável desta transição (idempotency_key = run_id:step_id
	// no Event Store). Cada fase usa um StepID distinto para não colidir com o
	// evento de mediação do RM no mesmo stream (ver contract.Step*).
	StepID string `json:"step_id"`
	State  string `json:"state"`

	// Especificação da tool call (presente em ready; ecoada nas fases seguintes).
	ToolID     string       `json:"tool_id,omitempty"`
	Capability string       `json:"capability,omitempty"`
	Resource   ResourceSpec `json:"resource,omitzero"`
	Input      []byte       `json:"input,omitempty"`

	// Resultado (só em complete).
	Output []byte `json:"output,omitempty"`
	// Diagnóstico de falha (só em failed).
	Reason string `json:"reason,omitempty"`
	Code   string `json:"code,omitempty"`
}

// Os Step* derivam StepIDs determinísticos e DISTINTOS por fase a partir do
// task_id. São distintos entre si E do StepID que o Escalonador usa no Call ao
// RM (StepDispatch), para que cada evento produza uma idempotency_key única no
// mesmo stream (run_id) — de outro modo o Append do Event Store deduplicava a
// segunda escrita (run_id:step_id repetido) e o evento perdia-se.
func StepRunCreated() string            { return "run" }
func StepReady(taskID string) string    { return taskID + ":ready" }
func StepRunning(taskID string) string  { return taskID + ":running" }
func StepDispatch(taskID string) string { return taskID + ":dispatch" }
func StepComplete(taskID string) string { return taskID + ":complete" }
func StepFailed(taskID string) string   { return taskID + ":failed" }
