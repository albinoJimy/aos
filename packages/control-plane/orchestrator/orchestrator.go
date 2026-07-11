// Package orchestrator implementa o Orquestrador (ORQ) do plano de controlo do
// AOS — o esqueleto contratual de AOS-012.
//
// O ORQ recebe um objectivo (Submit), constrói um grafo de tarefas acíclico e
// publica os eventos de coordenação no barramento (AOS-009): run.created e, por
// cada nó pronto, task.ready. O Escalonador (pacote scheduler, módulo separado)
// consome esses eventos e despacha via Reference Monitor (AOS-003).
//
// NÃO-PRODUTIVO (esqueleto AOS-012): a decomposição é um STUB — o grafo tem
// sempre 1 nó derivado directamente do Goal, sem planeamento real. Não há
// re-planeamento, dependências, nem prioridade. Ver os pontos de extensão em
// contract/graph.go e no README.
package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/aos-ref/control-plane/orchestrator/contract"
	"github.com/aos-ref/substrate/bus"
	"github.com/aos-ref/substrate/eventstore"
)

// DefaultProducerNHI é a identidade não-humana (NHI) por omissão atribuída ao
// Orquestrador nos eventos que emite. Em produção resolve-se da identidade real
// do serviço (AOS-005).
const DefaultProducerNHI = "nhi:control-plane/orchestrator"

// Orchestrator é a implementação do ORQ. Construir com [New]. Implementa
// [contract.Orchestrator].
type Orchestrator struct {
	bus      *bus.Bus
	producer eventstore.Producer
	counter  atomic.Uint64
	newRunID func(n uint64) contract.RunID
}

// Option configura o Orchestrator.
type Option func(*Orchestrator)

// WithProducer injecta a identidade emissora (NHI) dos eventos do ORQ.
func WithProducer(p eventstore.Producer) Option {
	return func(o *Orchestrator) { o.producer = p }
}

// WithRunIDFunc injecta o gerador de run_id (uso em testes determinísticos). n é
// um contador monotónico por instância. Por omissão os run_ids são
// "run-<n:08d>".
func WithRunIDFunc(f func(n uint64) contract.RunID) Option {
	return func(o *Orchestrator) { o.newRunID = f }
}

// New constrói um Orchestrator que publica no barramento b. Falha se b for nil.
func New(b *bus.Bus, opts ...Option) (*Orchestrator, error) {
	if b == nil {
		return nil, fmt.Errorf("orchestrator: barramento nil")
	}
	o := &Orchestrator{
		bus:      b,
		producer: eventstore.Producer{NHIID: DefaultProducerNHI},
		newRunID: defaultRunID,
	}
	for _, opt := range opts {
		opt(o)
	}
	if o.newRunID == nil {
		o.newRunID = defaultRunID
	}
	return o, nil
}

// defaultRunID formata um run_id determinístico por instância. NÃO-PRODUTIVO:
// só é único dentro de uma instância do ORQ; produção usa um ULID/UUID global.
func defaultRunID(n uint64) contract.RunID {
	return contract.RunID(fmt.Sprintf("run-%08d", n))
}

// Submit cria um run a partir de goal, publica run.created seguido de task.ready
// (grafo mínimo de 1 nó) no barramento e devolve o run_id. Os dois eventos vão
// para o stream = run_id, com step_ids distintos (ver contract.Step*), de modo a
// serem correlacionáveis e a não colidirem na idempotency_key do Event Store.
//
// Fail-closed (run.created): se a publicação de run.created falhar, Submit
// devolve o erro e NÃO tenta task.ready — não fica um run meio-criado observável.
//
// FAN-OUT PARCIAL (esqueleto AOS-012): se run.created FOR durável mas a
// publicação de um task.ready falhar a seguir, Submit devolve o erro mas o
// run.created PERMANECE no stream — fica um run sem tarefa observável, SEM
// compensação nem rollback (não há saga neste esqueleto; ver README). É benigno
// no grafo de 1 nó (não há task.ready parcial que fique órfão), mas torna-se um
// risco de fan-out parcial no grafo multi-nó de EPIC-03, onde alguns task.ready
// podem ficar publicados e outros não. EPIC-03 deve emitir um run.failed
// terminal (ou compensar os task.ready já publicados) neste caminho, para que os
// consumidores observem um run abortado em vez de um run preso.
func (o *Orchestrator) Submit(ctx context.Context, goal contract.Goal) (contract.RunID, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	runID := o.newRunID(o.counter.Add(1))
	graph := contract.NewMinimalGraph(string(runID), goal)

	// 1) run.created
	created := contract.RunCreatedPayload{
		RunID:     string(runID),
		Objective: goal.Objective,
		TaskCount: len(graph.Nodes),
	}
	if err := o.publish(ctx, string(runID), contract.EventRunCreated, contract.StepRunCreated(), created); err != nil {
		return "", fmt.Errorf("orchestrator: publicar run.created: %w", err)
	}

	// 2) task.ready por cada nó (no esqueleto, exactamente 1).
	for _, node := range graph.Nodes {
		ready := contract.TaskPayload{
			RunID:      string(runID),
			TaskID:     node.TaskID,
			StepID:     contract.StepReady(node.TaskID),
			State:      string(contract.StateReady),
			ToolID:     node.Spec.ToolID,
			Capability: node.Spec.Capability,
			Resource:   node.Spec.Resource,
			Input:      node.Spec.Input,
		}
		if err := o.publish(ctx, string(runID), contract.EventTaskReady, ready.StepID, ready); err != nil {
			// FAN-OUT PARCIAL: run.created já é durável; devolvemos o erro sem
			// compensar (ver doc de Submit). No grafo de 1 nó não há task.ready
			// anterior órfão; EPIC-03 tem de tratar o caso multi-nó.
			return "", fmt.Errorf("orchestrator: publicar task.ready: %w", err)
		}
	}
	return runID, nil
}

// publish serializa payload e escreve um evento no barramento (que delega no
// Event Store). stream = run_id; stepID compõe a idempotency_key run_id:step_id.
func (o *Orchestrator) publish(ctx context.Context, runID, evType, stepID string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = o.bus.Publish(ctx, runID, eventstore.EventInput{
		Type:     evType,
		Payload:  raw,
		RunID:    runID,
		StepID:   stepID,
		Producer: o.producer,
	})
	return err
}

// Verificação estática de conformidade com a porta estável.
var _ contract.Orchestrator = (*Orchestrator)(nil)
