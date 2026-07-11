// Package scheduler implementa o Escalonador (SCH) do plano de controlo do AOS —
// o esqueleto contratual de AOS-012.
//
// O SCH subscreve os eventos task.ready no barramento (AOS-009), transita a
// tarefa ready→running (emite task.running), e despacha a tool call do nó
// SEMPRE via Reference Monitor (AOS-003): rm.Mediate é a ÚNICA superfície de
// execução. Conforme a Decision e o resultado, emite task.complete ou
// task.failed, correlacionados por run_id. O estado transita como eventos no
// Event Store (via barramento).
//
// NO-BYPASS (estrutural, reutiliza a garantia do AOS-003): o SCH só detém um
// *referencemonitor.Monitor — NUNCA a ToolFunc. A tool é registada no RM pelo
// compositor; o SCH não tem qualquer via de a invocar fora de rm.Mediate. Um RM
// que NEGA leva o fluxo a task.failed sem efeito (ver testes).
//
// NÃO-PRODUTIVO (esqueleto AOS-012): sem leases/fencing/heartbeat, sem detecção
// de zombies, sem prioridade nem backpressure avançada. O despacho é sequencial
// e direto. A durabilidade do consumo é PARCIAL: a subscrição (cursor+ACK)
// re-entrega, at-least-once, eventos JÁ OBSERVADOS mas ainda não confirmados,
// mas NÃO faz catch-up multi-stream — um task.ready publicado enquanto o SCH
// está em baixo (ou antes de Start) NÃO é re-entregue no reinício. Por isso o
// SCH deve Start ANTES do Submit. Ver os pontos de extensão no README.
//
// IDEMPOTÊNCIA DE EFEITO: como a entrega é at-least-once, dispatch tem um guard
// que lê o stream do run e recusa re-despachar uma tarefa que já emitiu
// task.running/terminal — a dedup do Event Store por step_id protege só os
// EVENTOS, não o efeito (não-idempotente) da tool. Exactly-once forte sob
// workers concorrentes fica para EPIC-03 (leases/fencing).
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/aos-ref/control-plane/orchestrator/contract"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/bus"
	"github.com/aos-ref/substrate/eventstore"
)

// DefaultSubscriberName é o nome durável por omissão da subscrição do SCH no
// barramento (chave do cursor).
const DefaultSubscriberName = "control-plane/scheduler"

// DefaultProducerNHI é a NHI por omissão do Escalonador nos eventos que emite e
// nas tool calls que media.
const DefaultProducerNHI = "nhi:control-plane/scheduler"

// StreamReader lê o stream durável de um run no Event Store (AOS-002). O SCH
// usa-a APENAS em leitura, para o guard de idempotência de EFEITO do despacho
// (ver dispatch). *eventstore.Store satisfá-la; a interface mínima não acopla o
// SCH ao tipo concreto nem lhe dá qualquer via de escrita.
type StreamReader interface {
	// Read devolve os eventos committed do stream com seq >= fromSeq.
	Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error)
}

// Scheduler é a implementação do SCH. Construir com [New]. Implementa
// [contract.Scheduler].
type Scheduler struct {
	bus       *bus.Bus
	rm        *referencemonitor.Monitor
	reader    StreamReader
	name      string
	producer  eventstore.Producer
	principal referencemonitor.Principal

	// mu guarda baseCtx e sub, partilhados entre Start/Stop (chamador) e a
	// goroutine de entrega da subscrição (handle). Sem isto há uma corrida latente
	// se Stop correr concorrente com Start ou com uma entrega activa.
	mu      sync.Mutex
	baseCtx context.Context
	sub     bus.Subscription
}

// Option configura o Scheduler.
type Option func(*Scheduler)

// WithSubscriberName define o nome durável da subscrição (cursor).
func WithSubscriberName(name string) Option {
	return func(s *Scheduler) { s.name = name }
}

// WithProducer injecta a identidade emissora (NHI) dos eventos de estado do SCH.
func WithProducer(p eventstore.Producer) Option {
	return func(s *Scheduler) { s.producer = p }
}

// WithPrincipal injecta o Principal usado nas tool calls submetidas ao RM. No
// esqueleto é um principal neutro; o hook de identidade (AOS-005) resolve/valida
// o real em produção.
func WithPrincipal(p referencemonitor.Principal) Option {
	return func(s *Scheduler) { s.principal = p }
}

// New constrói um Scheduler que consome de b, despacha via rm e usa reader para
// o guard de idempotência de efeito. Falha se b, rm ou reader forem nil (o RM é
// obrigatório: não há caminho de execução sem ele; o reader é obrigatório: sem
// leitura do stream não se pode impedir a re-execução de uma tool sob
// re-entrega at-least-once).
func New(b *bus.Bus, rm *referencemonitor.Monitor, reader StreamReader, opts ...Option) (*Scheduler, error) {
	if b == nil {
		return nil, fmt.Errorf("scheduler: barramento nil")
	}
	if rm == nil {
		// Sem RM não há despacho possível — o SCH não executa tools por outra via.
		return nil, fmt.Errorf("scheduler: reference monitor nil (execução só via RM)")
	}
	if reader == nil {
		// Sem leitor do Event Store não há guard de idempotência de efeito: uma
		// re-entrega at-least-once re-executaria uma tool não-idempotente.
		return nil, fmt.Errorf("scheduler: stream reader nil (guard de idempotência exige leitura do Event Store)")
	}
	s := &Scheduler{
		bus:       b,
		rm:        rm,
		reader:    reader,
		name:      DefaultSubscriberName,
		producer:  eventstore.Producer{NHIID: DefaultProducerNHI},
		principal: referencemonitor.Principal{NHIID: DefaultProducerNHI},
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.name == "" {
		s.name = DefaultSubscriberName
	}
	return s, nil
}

// Start inicia o consumo durável de task.ready. A subscrição filtra por Type
// (task.ready) em TODOS os streams (sem Streams no filtro → entrega live de
// qualquer run). Deve ser chamado ANTES de o Orquestrador submeter, para que a
// entrega live capte o evento (o esqueleto não faz catch-up multi-stream — ver
// README). Idempotente-o-suficiente: uma segunda chamada devolve erro se já
// houver subscrição activa.
func (s *Scheduler) Start(ctx context.Context) error {
	// O lock cobre toda a Start: Subscribe apenas ARRANCA a goroutine de entrega
	// (go sub.run) e devolve sem a aguardar, pelo que uma entrega que chegue fica
	// a bloquear em handle → s.mu até esta Start libertar — sem deadlock e com
	// Start mutuamente exclusiva (nunca duas subscrições em simultâneo).
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sub != nil {
		return fmt.Errorf("scheduler: já iniciado")
	}
	s.baseCtx = ctx
	sub, err := s.bus.Subscribe(ctx, bus.SubConfig{
		Name:    s.name,
		Filter:  bus.Filter{Types: []string{contract.EventTaskReady}},
		Handler: s.handle,
	})
	if err != nil {
		return err
	}
	s.sub = sub
	return nil
}

// Stop cancela a subscrição. Idempotente.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	sub := s.sub
	s.sub = nil
	s.mu.Unlock()
	// Unsubscribe JUNTA a goroutine de entrega (espera por <-done); nunca a chamar
	// sob s.mu, pois essa goroutine pode estar em handle à espera do mesmo lock —
	// seria um deadlock.
	if sub != nil {
		sub.Unsubscribe()
	}
}

// handle é o Handler do barramento (corre na goroutine de entrega da
// subscrição). Delega a lógica em [Scheduler.dispatch] e traduz o seu resultado
// na confirmação da entrega:
//   - sucesso (evento terminal publicado, seja complete ou failed) → Ack: o
//     task.ready FOI processado; uma falha de TAREFA é um resultado terminal
//     legítimo, não uma falha de entrega;
//   - erro de INFRAESTRUTURA (falha a publicar um evento de estado) → Nack, para
//     re-entrega at-least-once. A re-entrega é segura porque dispatch tem um
//     guard de idempotência de EFEITO (lê o stream do run e recusa re-despachar
//     uma tarefa com task.running/terminal já emitido) — a dedup do Event Store
//     por step_id protege só os EVENTOS, não o efeito da tool (ver dispatch).
func (s *Scheduler) handle(d *bus.Delivery) {
	s.mu.Lock()
	ctx := s.baseCtx
	s.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}

	var tp contract.TaskPayload
	if err := json.Unmarshal(d.Event.Payload, &tp); err != nil {
		// Payload corrompido: não é recuperável por re-entrega → Ack e segue.
		// (No esqueleto não há dead-letter dedicada do SCH; o barramento tem a sua.)
		d.Ack()
		return
	}

	if err := s.dispatch(ctx, tp); err != nil {
		d.Nack(err)
		return
	}
	d.Ack()
}

// dispatch executa a máquina de estados de uma tarefa e o despacho mediado. Todo
// o caminho de publicação passa por aqui, com um único ponto de retorno de erro
// de infraestrutura (a publicação falhou) — as falhas de TAREFA emitem
// task.failed e devolvem nil. Devolver erro sinaliza a handle para fazer Nack.
func (s *Scheduler) dispatch(ctx context.Context, tp contract.TaskPayload) error {
	// 0) Guard de idempotência de EFEITO. Sob re-entrega at-least-once (Nack, ou
	//    entrega duplicada do barramento), uma segunda passagem por este task.ready
	//    NÃO pode re-invocar a tool: rm.Mediate corre a ToolFunc e a dedup do Event
	//    Store por step_id só protege os EVENTOS, não o efeito. Consulta-se o
	//    stream do run:
	//      - terminal (task.complete/failed) já presente → run concluído: a
	//        entrega é um no-op → Ack (devolve nil);
	//      - task.running sem terminal → um despacho anterior passou o gate e não
	//        fechou (ex.: crash/erro de infra entre Mediate e o evento terminal):
	//        fail-closed com terminal indeterminado, SEM re-executar a tool.
	switch st, err := s.priorDispatchState(ctx, tp); {
	case err != nil:
		return err // erro de infra ao ler o stream → Nack, re-tenta
	case st == dispatchDone:
		return nil
	case st == dispatchInFlight:
		return s.emitFailed(ctx, tp,
			"re-entrega de task.ready após despacho anterior não concluído; não se re-executa a tool não-idempotente",
			"E_REDELIVERY_INDETERMINATE")
	}

	// 1) Máquina de estados mínima: ready → running. Uma transição inválida
	//    (evento fora de ordem ou estado inesperado) termina em task.failed sem
	//    despachar — nunca se salta para running a partir de um estado ilegal.
	if _, err := contract.Transition(contract.State(tp.State), contract.StateRunning); err != nil {
		return s.emitFailed(ctx, tp, "transição de estado inválida: "+err.Error(), "E_INVALID_TRANSITION")
	}

	// 2) Emite task.running (estado persistido como evento).
	if err := s.emitRunning(ctx, tp); err != nil {
		return err
	}

	// 3) Despacho SEMPRE via RM. Constrói o Call a partir do nó; o StepID de
	//    despacho é distinto dos step_ids dos eventos de estado (idempotency_key
	//    única no stream). O SCH não tem outra via de executar a tool.
	call := referencemonitor.Call{
		RunID:      tp.RunID,
		StepID:     contract.StepDispatch(tp.TaskID),
		ToolID:     tp.ToolID,
		Capability: tp.Capability,
		Resource: referencemonitor.Resource{
			Type:   tp.Resource.Type,
			Value:  tp.Resource.Value,
			Region: tp.Resource.Region,
		},
		Principal: s.principal,
		Input:     tp.Input,
	}
	dec, err := s.rm.Mediate(ctx, call)

	// 4) Resolução conforme a Decision. Só permit COM tool sem erro → complete.
	switch {
	case err != nil:
		// Erro de mediação (ex.: contexto cancelado): fail-closed.
		return s.emitFailed(ctx, tp, "mediação abortada: "+err.Error(), "E_MEDIATION_ABORTED")
	case !dec.Permitted():
		// Negado/escalado pelo RM: a tool NÃO foi despachada (sem efeito).
		return s.emitFailed(ctx, tp, decisionReason(dec), dec.Code)
	case dec.ToolErr != nil:
		// Permit, mas a tool falhou na execução downstream. A decisão foi permit
		// (o efeito pode ter ocorrido); a TAREFA falha na mesma.
		return s.emitFailed(ctx, tp, "erro de tool: "+dec.ToolErr.Error(), "E_TOOL_ERROR")
	default:
		// Permit + tool OK → running → complete, com o output.
		return s.emitComplete(ctx, tp, dec.Output)
	}
}

// dispatchState resume o que o stream do run já revela sobre o despacho de um
// task_id, para o guard de idempotência de efeito em dispatch.
type dispatchState int

const (
	dispatchFresh    dispatchState = iota // sem task.running nem terminal: despachar
	dispatchInFlight                      // task.running sem terminal: despacho anterior não fechou
	dispatchDone                          // terminal (complete/failed) já presente
)

// priorDispatchState lê o stream do run e classifica o estado de despacho do
// task_id comparando STEP_IDs (sem desserializar payloads). Um stream ainda
// inexistente conta como fresh. Um erro de leitura (infra, ex.: sem quórum)
// propaga-se para provocar Nack — nunca se despacha às cegas se não se conseguiu
// confirmar que a tarefa ainda não foi despachada.
func (s *Scheduler) priorDispatchState(ctx context.Context, tp contract.TaskPayload) (dispatchState, error) {
	evs, err := s.reader.Read(ctx, tp.RunID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return dispatchFresh, nil
		}
		return dispatchFresh, err
	}
	running := contract.StepRunning(tp.TaskID)
	complete := contract.StepComplete(tp.TaskID)
	failed := contract.StepFailed(tp.TaskID)
	sawRunning := false
	for _, ev := range evs {
		switch ev.StepID {
		case complete, failed:
			return dispatchDone, nil
		case running:
			sawRunning = true
		}
	}
	if sawRunning {
		return dispatchInFlight, nil
	}
	return dispatchFresh, nil
}

// emitRunning publica task.running. A transição ready→running já foi validada
// por dispatch antes de chegar aqui.
func (s *Scheduler) emitRunning(ctx context.Context, tp contract.TaskPayload) error {
	return s.publish(ctx, tp.RunID, contract.EventTaskRunning, contract.StepRunning(tp.TaskID), contract.TaskPayload{
		RunID:      tp.RunID,
		TaskID:     tp.TaskID,
		StepID:     contract.StepRunning(tp.TaskID),
		State:      string(contract.StateRunning),
		ToolID:     tp.ToolID,
		Capability: tp.Capability,
		Resource:   tp.Resource,
	})
}

// emitComplete publica task.complete. running→complete é uma transição
// estaticamente válida (só se chega aqui a partir de running), pelo que não é
// re-verificada — o guard significativo da máquina de estados é ready→running,
// imposto e testado em dispatch.
func (s *Scheduler) emitComplete(ctx context.Context, tp contract.TaskPayload, output []byte) error {
	return s.publish(ctx, tp.RunID, contract.EventTaskComplete, contract.StepComplete(tp.TaskID), contract.TaskPayload{
		RunID:  tp.RunID,
		TaskID: tp.TaskID,
		StepID: contract.StepComplete(tp.TaskID),
		State:  string(contract.StateComplete),
		Output: output,
	})
}

// emitFailed publica task.failed com o diagnóstico. running→failed (e o atalho
// ready→failed numa transição inicial inválida) são transições terminais
// estaticamente válidas na máquina mínima.
func (s *Scheduler) emitFailed(ctx context.Context, tp contract.TaskPayload, reason, code string) error {
	return s.publish(ctx, tp.RunID, contract.EventTaskFailed, contract.StepFailed(tp.TaskID), contract.TaskPayload{
		RunID:  tp.RunID,
		TaskID: tp.TaskID,
		StepID: contract.StepFailed(tp.TaskID),
		State:  string(contract.StateFailed),
		Reason: reason,
		Code:   code,
	})
}

// publish serializa payload e escreve o evento no barramento (stream = run_id).
func (s *Scheduler) publish(ctx context.Context, runID, evType, stepID string, payload contract.TaskPayload) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.bus.Publish(ctx, runID, eventstore.EventInput{
		Type:     evType,
		Payload:  raw,
		RunID:    runID,
		StepID:   stepID,
		Producer: s.producer,
	})
	return err
}

// decisionReason compõe um motivo legível de uma negação/escalonamento do RM.
func decisionReason(dec referencemonitor.Decision) string {
	r := fmt.Sprintf("mediação %s", dec.Effect)
	if dec.DeniedBy != "" {
		r += " por " + dec.DeniedBy
	}
	if dec.Reason != "" {
		r += ": " + dec.Reason
	}
	return r
}

// Verificação estática de conformidade com a porta estável.
var _ contract.Scheduler = (*Scheduler)(nil)
