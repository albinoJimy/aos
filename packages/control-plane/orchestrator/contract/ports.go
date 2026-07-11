package contract

import "context"

// Orchestrator é a porta ESTÁVEL do Orquestrador (ORQ). Decompõe um objectivo
// num grafo de tarefas acíclico e publica os eventos de criação/prontidão.
//
// Contrato (AOS-012, estável — EPIC-03 estende sem quebrar):
//   - Submit cria um run com run_id único, emite run.created + task.ready no
//     barramento (AOS-009) e devolve o run_id.
//   - No esqueleto o grafo é trivial (1 nó); EPIC-03 acrescenta decomposição
//     real, mas a assinatura de Submit não muda.
type Orchestrator interface {
	Submit(ctx context.Context, goal Goal) (RunID, error)
}

// Scheduler é a porta ESTÁVEL do Escalonador (SCH). Consome eventos task.ready
// do barramento e despacha a tool call SEMPRE via Reference Monitor (AOS-003),
// respeitando a máquina de estados mínima.
//
// Contrato (AOS-012, estável — EPIC-03 estende sem quebrar):
//   - Start inicia o consumo durável de task.ready (subscrição no barramento).
//   - Cada tarefa transita ready→running (emite task.running), é mediada pelo RM
//     e termina em task.complete|task.failed.
//   - Stop cancela a subscrição de forma idempotente.
//
// PONTO DE EXTENSÃO (EPIC-03): leases/fencing/heartbeat, prioridade,
// backpressure e detecção de deadlock acrescentam-se por trás desta porta —
// tipicamente novos métodos/opções, sem alterar Start/Stop.
type Scheduler interface {
	Start(ctx context.Context) error
	Stop()
}
