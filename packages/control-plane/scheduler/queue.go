// queue.go — FILAS LIMITADAS por partição + BACKPRESSURE (AOS-030).
//
// O plano-base acumulava trabalho de forma ILIMITADA, produzindo cascatas de
// timeouts. A resolução é backpressure REAL: filas com LIMITE EXPLÍCITO
// (comprimento E/OU idade) por partição tenant:priority. O enchimento é
// DETECTADO e sinalizado a montante — NUNCA silenciosamente absorvido. Ao atingir
// o limite, aplica-se a POLÍTICA DECLARATIVA (policy.go) que SELECCIONA uma acção
// de degradação, em vez de a fila crescer indefinidamente (sem acumulação
// ilimitada). A EXECUÇÃO das acções é o AOS-031.
//
// HISTERESE (evita flapping): cada partição tem watermarks high/low. Satura ao
// cruzar o high; só SAI de "saturado" ao descer ABAIXO do low. Entre os dois, o
// estado latched mantém-se — sem oscilação a cada enqueue/dequeue junto ao limite.
//
// BACKPRESSURE → ADMIT (AOS-027): [PartitionedQueues] implementa
// [BackpressureSource]. Injectada no Admission via WithBackpressure, faz o admit
// ADIAR (defer) mais agressivamente para o tenant saturado — o sinal propaga-se a
// montante em vez de deixar a fila crescer.
//
// DETERMINISMO: o relógio (idade dos itens) é injectável; a iteração de partições
// é ordenada; a serialização dos eventos é estável (structs, sem mapas). Sem
// time.Now/rand nas decisões.
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento append-only das filas (AOS-030). Contrato observável.
const (
	// EventQueueSaturated marca a transição de uma partição para SATURADA (cruzou o
	// high watermark ou o limite de idade).
	EventQueueSaturated = "backpressure.queue_saturated"
	// EventBackpressureSignalled marca a propagação do sinal de backpressure a
	// montante (o admit passará a adiar mais).
	EventBackpressureSignalled = "backpressure.backpressure_signalled"
	// EventBackpressureCleared marca a saída de saturação (desceu abaixo do low
	// watermark) — a histerese relaxou o sinal.
	EventBackpressureCleared = "backpressure.backpressure_cleared"
)

// DefaultQueueNHI é a NHI por omissão das filas nos eventos que emitem.
const DefaultQueueNHI = "nhi:control-plane/scheduler/backpressure-queue"

// queueStreamPrefix é o prefixo do stream de eventos de fila (um por instância).
const queueStreamPrefix = "backpressure/queue/"

// Atributos de span (OTel, porta zero-dep).
const (
	attrQueuePartition = "aos.backpressure.partition"
	attrQueueDepth     = "aos.backpressure.depth"
	attrQueueCapacity  = "aos.backpressure.capacity"
	attrQueueOldestMs  = "aos.backpressure.oldest_age_ms"
	attrQueueSaturated = "aos.backpressure.saturated"
	attrQueueAdmitted  = "aos.backpressure.admitted"
)

const opEnqueue = "backpressure_enqueue"

// ErrInvalidQueueLimits sinaliza limites de fila inválidos (fail-closed na
// construção/registo de partição).
var ErrInvalidQueueLimits = errors.New("scheduler: limites de fila inválidos")

// Partition é a chave de partição das filas: tenant:priority.
type Partition struct {
	Tenant   string
	Priority string
}

// String devolve a forma canónica "tenant:priority" (estável, para chave de mapa,
// stream e eventos).
func (p Partition) String() string { return p.Tenant + ":" + p.Priority }

// QueueLimits são os limites EXPLÍCITOS de uma partição. O comprimento (MaxLen) é
// o tecto duro que a fila NUNCA ultrapassa; a idade (MaxAge, opcional) é um tecto
// alternativo — um item mais velho que MaxAge conta como partição no limite,
// mesmo com comprimento abaixo de MaxLen. Os watermarks high/low dão a histerese.
type QueueLimits struct {
	// MaxLen é o comprimento máximo (tecto duro > 0). Ao atingi-lo aplica-se a
	// política em vez de crescer.
	MaxLen int
	// MaxAge é o tecto de idade do item mais antigo (0 = sem limite de idade).
	MaxAge time.Duration
	// HighWatermark é o limiar de SATURAÇÃO (0 < High <= MaxLen).
	HighWatermark int
	// LowWatermark é o limiar de ALÍVIO da histerese (0 <= Low < High).
	LowWatermark int
}

// validate impõe as invariantes fail-closed dos limites.
func (l QueueLimits) validate() error {
	if l.MaxLen <= 0 {
		return fmt.Errorf("%w: MaxLen deve ser > 0 (obtido %d)", ErrInvalidQueueLimits, l.MaxLen)
	}
	if l.HighWatermark <= 0 || l.HighWatermark > l.MaxLen {
		return fmt.Errorf("%w: HighWatermark deve estar em ]0,MaxLen] (obtido %d, MaxLen=%d)", ErrInvalidQueueLimits, l.HighWatermark, l.MaxLen)
	}
	if l.LowWatermark < 0 || l.LowWatermark >= l.HighWatermark {
		return fmt.Errorf("%w: LowWatermark deve estar em [0,High[ (obtido %d, High=%d)", ErrInvalidQueueLimits, l.LowWatermark, l.HighWatermark)
	}
	if l.MaxAge < 0 {
		return fmt.Errorf("%w: MaxAge negativo (%v)", ErrInvalidQueueLimits, l.MaxAge)
	}
	return nil
}

// DefaultQueueLimits deriva watermarks razoáveis (high=80%, low=50%) de um MaxLen,
// para conveniência. Os valores são sempre >=1 e respeitam Low<High<=MaxLen.
func DefaultQueueLimits(maxLen int) QueueLimits {
	if maxLen < 1 {
		maxLen = 1
	}
	high := (maxLen * 8) / 10
	if high < 1 {
		high = 1
	}
	low := (maxLen * 5) / 10
	if low >= high {
		low = high - 1
	}
	if low < 0 {
		low = 0
	}
	return QueueLimits{MaxLen: maxLen, HighWatermark: high, LowWatermark: low}
}

// WorkItem é uma unidade de trabalho enfileirável. É opaco para as filas (só o ID
// e a partição importam à contabilidade de backpressure).
type WorkItem struct {
	ID       string
	Tenant   string
	Priority string
}

// partition devolve a chave de partição do item.
func (w WorkItem) partition() Partition { return Partition{Tenant: w.Tenant, Priority: w.Priority} }

// EnqueueResult é o veredicto de [PartitionedQueues.Enqueue].
type EnqueueResult struct {
	// Admitted indica se o item foi aceite na fila. Se false, a fila estava no
	// limite e aplicou-se a política (Action) em vez de crescer.
	Admitted bool
	// Partition é a chave "tenant:priority".
	Partition string
	// Depth é a profundidade da fila após a operação.
	Depth int
	// Capacity é o MaxLen da partição.
	Capacity int
	// Saturated é o estado latched de histerese após a operação.
	Saturated bool
	// Action é a acção de degradação SELECCIONADA quando Admitted=false (executada
	// em AOS-031). Vazia quando Admitted=true.
	Action DegradationAction
	// PolicyVersion é a versão da política que seleccionou a acção (vazia se não
	// houve selecção ou se não há motor de política).
	PolicyVersion string
}

// queued é um item em fila com o instante de entrada (para a idade).
type queued struct {
	id string
	at int64 // UnixNano do enqueue (relógio injectável)
}

// partitionState é o estado bounded de uma partição.
type partitionState struct {
	part      Partition // dimensões desserializadas (tenant/priority) — não derivar da chave
	limits    QueueLimits
	items     []queued
	saturated bool // latched (histerese)
}

// PartitionedQueues é o conjunto de filas limitadas por partição tenant:priority,
// com watermarks/histerese, deteção de saturação e sinalização de backpressure.
// Também é a [BackpressureSource] consultada pelo admission control. É seguro para
// concorrência (um mutex protege o mapa de partições e os seus estados).
type PartitionedQueues struct {
	mu        sync.Mutex
	parts     map[string]*partitionState
	defLimits QueueLimits
	perPart   map[string]QueueLimits
	policy    *PolicyEngine
	log       EventLog
	now       func() time.Time
	tracer    agentruntime.Tracer
	producer  eventstore.Producer
	name      string
	bpRetry   time.Duration
	nEvents   uint64
}

// QueueOption configura as [PartitionedQueues].
type QueueOption func(*PartitionedQueues)

// WithQueuePolicy injecta o motor de política que selecciona a acção de degradação
// quando uma partição atinge o limite.
func WithQueuePolicy(p *PolicyEngine) QueueOption {
	return func(q *PartitionedQueues) {
		if p != nil {
			q.policy = p
		}
	}
}

// WithQueueLog injecta o Event Store para os eventos de fila observáveis.
func WithQueueLog(log EventLog) QueueOption {
	return func(q *PartitionedQueues) {
		if log != nil {
			q.log = log
		}
	}
}

// WithQueueClock injecta o relógio da idade dos itens (determinismo/replay).
func WithQueueClock(now func() time.Time) QueueOption {
	return func(q *PartitionedQueues) {
		if now != nil {
			q.now = now
		}
	}
}

// WithQueueTracer injecta a porta OTel (spans de profundidade/idade). Zero-dep.
func WithQueueTracer(t agentruntime.Tracer) QueueOption {
	return func(q *PartitionedQueues) {
		if t != nil {
			q.tracer = t
		}
	}
}

// WithQueueProducer injecta a NHI emissora dos eventos.
func WithQueueProducer(p eventstore.Producer) QueueOption {
	return func(q *PartitionedQueues) {
		if p.NHIID != "" {
			q.producer = p
		}
	}
}

// WithQueueName nomeia a instância (usada no stream de eventos). Permite vários
// conjuntos de filas independentes no mesmo Event Store.
func WithQueueName(name string) QueueOption {
	return func(q *PartitionedQueues) {
		if name != "" {
			q.name = name
		}
	}
}

// WithQueueLimitsFor fixa limites específicos para uma partição (sobrepõem os
// limites por omissão). Fail-closed: limites inválidos são ignorados aqui e
// reprovados na construção via [NewPartitionedQueues] (que revalida).
func WithQueueLimitsFor(p Partition, l QueueLimits) QueueOption {
	return func(q *PartitionedQueues) {
		q.perPart[p.String()] = l
	}
}

// WithBackpressureRetry fixa o retry_after aconselhado ao admit quando o tenant
// está saturado (0 = deixa o admit derivar do refill). Um valor positivo evita
// que o chamador re-tente de imediato contra uma fila cheia.
func WithBackpressureRetry(d time.Duration) QueueOption {
	return func(q *PartitionedQueues) {
		if d > 0 {
			q.bpRetry = d
		}
	}
}

// NewPartitionedQueues constrói as filas com limites por omissão (aplicados a
// partições sem override). Valida todos os limites fail-closed.
func NewPartitionedQueues(defLimits QueueLimits, opts ...QueueOption) (*PartitionedQueues, error) {
	if err := defLimits.validate(); err != nil {
		return nil, err
	}
	q := &PartitionedQueues{
		parts:     make(map[string]*partitionState),
		defLimits: defLimits,
		perPart:   make(map[string]QueueLimits),
		now:       time.Now,
		tracer:    agentruntime.NoopTracer{},
		producer:  eventstore.Producer{NHIID: DefaultQueueNHI},
		name:      "default",
	}
	for _, opt := range opts {
		opt(q)
	}
	for name, l := range q.perPart {
		if err := l.validate(); err != nil {
			return nil, fmt.Errorf("partição %q: %w", name, err)
		}
	}
	return q, nil
}

// limitsFor devolve os limites de uma partição (override ou omissão).
func (q *PartitionedQueues) limitsFor(p Partition) QueueLimits {
	if l, ok := q.perPart[p.String()]; ok {
		return l
	}
	return q.defLimits
}

// stateFor devolve (criando se preciso) o estado de uma partição. Requer q.mu.
func (q *PartitionedQueues) stateFor(p Partition) *partitionState {
	key := p.String()
	st, ok := q.parts[key]
	if !ok {
		st = &partitionState{part: p, limits: q.limitsFor(p)}
		q.parts[key] = st
	}
	return st
}

// oldestAge devolve a idade do item mais antigo da partição (0 se vazia). Requer
// q.mu. O item mais antigo é sempre o da frente (FIFO por instante de entrada).
func (st *partitionState) oldestAge(nowNano int64) time.Duration {
	if len(st.items) == 0 {
		return 0
	}
	d := nowNano - st.items[0].at
	if d < 0 {
		d = 0
	}
	return time.Duration(d)
}

// overAge indica se o item mais antigo excede o tecto de idade (dimensão de idade
// do limite). Requer q.mu. É a condição de idade isolada, partilhada por atLimit
// (satura por idade) e pela histerese/propagação (a saturação por idade só pode
// aliviar/ser ignorada quando esta condição deixa de se verificar).
func (st *partitionState) overAge(nowNano int64) bool {
	return st.limits.MaxAge > 0 && st.oldestAge(nowNano) >= st.limits.MaxAge
}

// atLimit indica se a partição está no LIMITE (comprimento OU idade) e não pode
// crescer. Requer q.mu.
func (st *partitionState) atLimit(nowNano int64) bool {
	if len(st.items) >= st.limits.MaxLen {
		return true
	}
	return st.overAge(nowNano)
}

// condition monta a [SaturationCondition] corrente da partição (para a política).
// Requer q.mu.
func (q *PartitionedQueues) condition(p Partition, st *partitionState, nowNano int64) SaturationCondition {
	depth := len(st.items)
	fill := 0.0
	if st.limits.MaxLen > 0 {
		fill = float64(depth) / float64(st.limits.MaxLen)
	}
	return SaturationCondition{
		Tenant:    p.Tenant,
		Priority:  p.Priority,
		Depth:     depth,
		Capacity:  st.limits.MaxLen,
		FillRatio: fill,
		OldestAge: st.oldestAge(nowNano),
		Saturated: st.saturated,
	}
}

// Enqueue tenta enfileirar um item. Se a partição estiver no limite (comprimento
// ou idade), NÃO cresce a fila: consulta a política, SELECCIONA a acção de
// degradação (emite degradation_policy_selected via o motor) e devolve
// Admitted=false com a acção. Caso contrário aceita o item e, se cruzar o high
// watermark, transita para saturado (histerese) e sinaliza backpressure a
// montante.
func (q *PartitionedQueues) Enqueue(ctx context.Context, item WorkItem) (EnqueueResult, error) {
	ctx, span := q.tracer.StartSpan(ctx, opEnqueue)
	defer span.End()

	p := item.partition()
	nowNano := q.now().UnixNano()

	q.mu.Lock()
	st := q.stateFor(p)

	span.SetAttribute(attrQueuePartition, p.String())
	span.SetAttribute(attrQueueCapacity, st.limits.MaxLen)

	if st.atLimit(nowNano) {
		// SEM acumulação ilimitada: aplica a política em vez de crescer.
		cond := q.condition(p, st, nowNano)
		// Garante que o estado saturado está latched (estar no limite ⇒ saturado).
		saturationEvents := q.raiseSaturation(st) // decide, ainda sob lock
		depth := len(st.items)
		q.mu.Unlock()

		// Emissões e selecção de política FORA do lock (evita reentrância no motor).
		if err := q.emitSaturation(ctx, p, st, saturationEvents, nowNano); err != nil {
			return EnqueueResult{}, err
		}
		var action DegradationAction
		var polVer string
		if q.policy != nil {
			a, v, err := q.policy.Select(ctx, cond)
			if err != nil {
				return EnqueueResult{}, err
			}
			action, polVer = a, v
		}
		span.SetAttribute(attrQueueDepth, depth)
		span.SetAttribute(attrQueueSaturated, true)
		span.SetAttribute(attrQueueAdmitted, false)
		span.SetAttribute(attrPolicyAction, string(action))
		return EnqueueResult{
			Admitted:      false,
			Partition:     p.String(),
			Depth:         depth,
			Capacity:      st.limits.MaxLen,
			Saturated:     true,
			Action:        action,
			PolicyVersion: polVer,
		}, nil
	}

	// Há espaço: aceita o item.
	st.items = append(st.items, queued{id: item.ID, at: nowNano})
	depth := len(st.items)
	// Cruzou o high watermark? Transita para saturado (histerese).
	var saturationEvents bool
	if !st.saturated && depth >= st.limits.HighWatermark {
		saturationEvents = q.raiseSaturation(st)
	}
	saturated := st.saturated
	// Captura a idade do item mais antigo SOB o lock: st.oldestAge lê st.items, que
	// outra goroutine muta (sob q.mu) no seu próprio Enqueue. Lê-la depois do Unlock
	// era uma corrida de dados (detectada por -race). O valor é imutável após a
	// captura e usado no span já fora do lock.
	oldestMs := st.oldestAge(nowNano).Milliseconds()
	q.mu.Unlock()

	if err := q.emitSaturation(ctx, p, st, saturationEvents, nowNano); err != nil {
		return EnqueueResult{}, err
	}
	span.SetAttribute(attrQueueDepth, depth)
	span.SetAttribute(attrQueueSaturated, saturated)
	span.SetAttribute(attrQueueAdmitted, true)
	span.SetAttribute(attrQueueOldestMs, oldestMs)
	return EnqueueResult{
		Admitted:  true,
		Partition: p.String(),
		Depth:     depth,
		Capacity:  st.limits.MaxLen,
		Saturated: saturated,
	}, nil
}

// Dequeue remove o item mais antigo de uma partição (FIFO). Se a profundidade
// descer ATÉ AO/ABAIXO do low watermark e a partição estava saturada, a histerese
// ALIVIA o estado (emite backpressure_cleared). Devolve ok=false se vazia.
func (q *PartitionedQueues) Dequeue(ctx context.Context, p Partition) (WorkItem, bool, error) {
	nowNano := q.now().UnixNano()
	q.mu.Lock()
	st, ok := q.parts[p.String()]
	if !ok || len(st.items) == 0 {
		q.mu.Unlock()
		return WorkItem{}, false, nil
	}
	head := st.items[0]
	st.items = st.items[1:]
	depth := len(st.items)
	var cleared bool
	// HISTERESE em AMBAS as dimensões: o alívio exige descer ATÉ AO/ABAIXO do low
	// watermark E que a condição de idade já NÃO se verifique. Sem o segundo termo,
	// drenar um backlog envelhecido através do low faria o estado latched oscilar a
	// cada par enqueue(re-satura por idade)/dequeue(limpa por profundidade) — o
	// flapping que os watermarks devem justamente evitar. O latch só limpa quando
	// nenhuma dimensão do limite persiste.
	if st.saturated && depth <= st.limits.LowWatermark && !st.overAge(nowNano) {
		st.saturated = false
		cleared = true
	}
	q.mu.Unlock()

	if cleared {
		if err := q.emitEvent(ctx, EventBackpressureCleared, p, st, depth, nowNano); err != nil {
			return WorkItem{}, false, err
		}
	}
	return WorkItem{ID: head.id, Tenant: p.Tenant, Priority: p.Priority}, true, nil
}

// raiseSaturation faz a transição para saturado se ainda não estava. Requer q.mu.
// Devolve true se HOUVE transição (para o chamador emitir os eventos uma só vez).
func (q *PartitionedQueues) raiseSaturation(st *partitionState) bool {
	if st.saturated {
		return false
	}
	st.saturated = true
	return true
}

// emitSaturation emite queue_saturated + backpressure_signalled quando houve
// transição para saturado. Idempotente por step_id na dedup do Event Store.
func (q *PartitionedQueues) emitSaturation(ctx context.Context, p Partition, st *partitionState, transitioned bool, nowNano int64) error {
	if !transitioned || q.log == nil {
		return nil
	}
	q.mu.Lock()
	depth := len(st.items)
	q.mu.Unlock()
	if err := q.emitEvent(ctx, EventQueueSaturated, p, st, depth, nowNano); err != nil {
		return err
	}
	return q.emitEvent(ctx, EventBackpressureSignalled, p, st, depth, nowNano)
}

// emitEvent serializa e escreve um evento de fila no stream da instância, com
// step_id "q-N" monotónico (idempotente por (run_id, step_id) na dedup do Event
// Store). Fail-closed: um erro do store propaga.
func (q *PartitionedQueues) emitEvent(ctx context.Context, evType string, p Partition, st *partitionState, depth int, nowNano int64) error {
	if q.log == nil {
		return nil
	}
	q.mu.Lock()
	fill := 0.0
	if st.limits.MaxLen > 0 {
		fill = float64(depth) / float64(st.limits.MaxLen)
	}
	pl := queuePayload{
		Type:          evType,
		Partition:     p.String(),
		Tenant:        p.Tenant,
		Priority:      p.Priority,
		Depth:         depth,
		Capacity:      st.limits.MaxLen,
		HighWatermark: st.limits.HighWatermark,
		LowWatermark:  st.limits.LowWatermark,
		OldestAgeMs:   st.oldestAge(nowNano).Milliseconds(),
		FillRatio:     fill,
		TSUnixNano:    nowNano,
	}
	q.nEvents++
	stepID := "q-" + strconv.FormatUint(q.nEvents, 10)
	q.mu.Unlock()

	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	streamID := queueStreamPrefix + q.name
	_, err = q.log.Append(ctx, streamID, eventstore.EventInput{
		Type:     evType,
		Payload:  raw,
		RunID:    streamID,
		StepID:   stepID,
		Producer: q.producer,
	})
	return err
}

// Depth devolve a profundidade corrente de uma partição (0 se inexistente).
func (q *PartitionedQueues) Depth(p Partition) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if st, ok := q.parts[p.String()]; ok {
		return len(st.items)
	}
	return 0
}

// IsSaturated devolve se a partição está saturada. Cobre AMBAS as origens: o
// estado latched de histerese (por profundidade) E a condição de idade reavaliada
// no instante — a saturação por idade avança sem depender de um novo Enqueue, logo
// uma partição over-age é visível mesmo entre enqueues.
func (q *PartitionedQueues) IsSaturated(p Partition) bool {
	nowNano := q.now().UnixNano()
	q.mu.Lock()
	defer q.mu.Unlock()
	if st, ok := q.parts[p.String()]; ok {
		return st.saturated || st.overAge(nowNano)
	}
	return false
}

// QueueSnapshot é o estado observável de UMA partição num instante (para a
// amostragem de métricas de saturação, AOS-034). É read-only e não muta estado.
type QueueSnapshot struct {
	// Partition é a chave "tenant:priority".
	Partition string
	Tenant    string
	Priority  string
	// Depth é a profundidade corrente; Capacity é o MaxLen.
	Depth    int
	Capacity int
	// OldestAgeMs é a idade do item mais antigo em ms (relógio injectável).
	OldestAgeMs int64
	// Saturated é o estado latched de histerese OU a condição de idade no instante
	// (coerente com [PartitionedQueues.IsSaturated]).
	Saturated bool
}

// Snapshot devolve o estado observável de TODAS as partições, ORDENADO por chave
// (determinismo/replay). É uma leitura pura (não muta nem satura) usada pela
// amostragem de métricas do AOS-034 ([SchedulerMetrics.SampleQueues]). A saturação
// reportada cobre a histerese latched E a condição de idade reavaliada no instante.
func (q *PartitionedQueues) Snapshot() []QueueSnapshot {
	nowNano := q.now().UnixNano()
	q.mu.Lock()
	defer q.mu.Unlock()
	keys := make([]string, 0, len(q.parts))
	for k := range q.parts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]QueueSnapshot, 0, len(keys))
	for _, k := range keys {
		st := q.parts[k]
		out = append(out, QueueSnapshot{
			Partition:   k,
			Tenant:      st.part.Tenant,
			Priority:    st.part.Priority,
			Depth:       len(st.items),
			Capacity:    st.limits.MaxLen,
			OldestAgeMs: st.oldestAge(nowNano).Milliseconds(),
			Saturated:   st.saturated || st.overAge(nowNano),
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// BackpressureSource — o seam que acopla as filas ao admission control (AOS-027).
// ---------------------------------------------------------------------------

// BackpressureSignal é o sinal devolvido por uma [BackpressureSource]: se o
// tenant/chave está saturado e o retry_after aconselhado.
type BackpressureSignal struct {
	// Saturated indica que uma fila do tenant (nesta chave) está saturada; o admit
	// deve ADIAR mais agressivamente.
	Saturated bool
	// RetryAfter é o adiamento aconselhado sob backpressure (0 = admit deriva do
	// refill).
	RetryAfter time.Duration
}

// BackpressureSource é a PORTA que o admission control consulta para saber se deve
// adiar mais sob saturação. É injectada via [WithBackpressure] — acoplamento
// ADITIVO: sem source, o admit comporta-se exactamente como no AOS-027.
type BackpressureSource interface {
	// Backpressure devolve o sinal de saturação para uma chave/tenant. Deve ser
	// determinística e barata (é consultada no caminho de admissão).
	Backpressure(ctx context.Context, key ProviderKey, tenant string) BackpressureSignal
}

// Backpressure implementa [BackpressureSource]: o tenant está saturado se ALGUMA
// das suas partições (qualquer prioridade) estiver saturada — por histerese
// (latched) OU por idade reavaliada no instante (a saturação por idade propaga-se
// ao admit sem depender de um novo Enqueue). A iteração é ordenada (determinismo).
// A correspondência de tenant é por CAMPO desserializado (st.part.Tenant), não por
// prefixo textual da chave "tenant:priority": um ':' no identificador de tenant ou
// priority não provoca match cruzado entre tenants, e um tenant vazio é uma
// partição legítima (não contorna o backpressure). A [ProviderKey] não particiona
// as filas mas faz parte da porta para futura afinação por provider.
func (q *PartitionedQueues) Backpressure(_ context.Context, _ ProviderKey, tenant string) BackpressureSignal {
	nowNano := q.now().UnixNano()
	q.mu.Lock()
	defer q.mu.Unlock()

	keys := make([]string, 0, len(q.parts))
	for k := range q.parts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		st := q.parts[k]
		if st.part.Tenant != tenant {
			continue
		}
		if st.saturated || st.overAge(nowNano) {
			return BackpressureSignal{Saturated: true, RetryAfter: q.bpRetry}
		}
	}
	return BackpressureSignal{}
}

// queuePayload é o corpo serializado (estável, sem mapas) dos eventos de fila —
// determinismo/replay.
type queuePayload struct {
	Type          string  `json:"type"`
	Partition     string  `json:"partition"`
	Tenant        string  `json:"tenant,omitempty"`
	Priority      string  `json:"priority,omitempty"`
	Depth         int     `json:"depth"`
	Capacity      int     `json:"capacity"`
	HighWatermark int     `json:"high_watermark"`
	LowWatermark  int     `json:"low_watermark"`
	OldestAgeMs   int64   `json:"oldest_age_ms,omitempty"`
	FillRatio     float64 `json:"fill_ratio"`
	TSUnixNano    int64   `json:"ts_unix_nano"`
}

// QueueRecord é um evento de fila reconstruído do log (para replay).
type QueueRecord struct {
	Type      string
	Partition string
	Depth     int
	Capacity  int
	Saturated bool
	Seq       uint64
}

// ReplayQueue reconstrói a sequência de eventos de fila da instância a partir do
// Event Store (append-only, por ordem de seq). Prova de que o estado de fila/
// política se reconstrói do log (ADR-001/010).
func (q *PartitionedQueues) ReplayQueue(ctx context.Context) ([]QueueRecord, error) {
	if q.log == nil {
		return nil, nil
	}
	streamID := queueStreamPrefix + q.name
	evs, err := q.log.Read(ctx, streamID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]QueueRecord, 0, len(evs))
	for _, ev := range evs {
		var pl queuePayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return nil, err
		}
		out = append(out, QueueRecord{
			Type:      pl.Type,
			Partition: pl.Partition,
			Depth:     pl.Depth,
			Capacity:  pl.Capacity,
			Saturated: ev.Type == EventQueueSaturated || ev.Type == EventBackpressureSignalled,
			Seq:       ev.Seq,
		})
	}
	return out, nil
}
