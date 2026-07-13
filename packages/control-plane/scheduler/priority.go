// priority.go — SCHEDULING PRIORITY-AWARE + AGING (AOS-032).
//
// Prioridade sem aging provoca STARVATION de trabalho de baixa prioridade sob
// carga sustentada de alta prioridade. Este componente adiciona AMBOS: despacho
// por CLASSE DE PRIORIDADE sobre as filas particionadas (AOS-030) E aging que
// PROMOVE trabalho antigo, garantindo ZERO starvation.
//
// PRIORIDADE EFECTIVA = f(prioridade_base, idade, SLO), MONÓTONA na idade: quanto
// mais tempo um item espera, maior a sua prioridade efectiva. Como o termo de
// aging cresce SEM TECTO com a idade, qualquer trabalho de base baixa acaba por
// ultrapassar trabalho NOVO de base mais alta — a garantia anti-starvation. Esta
// garantia é FAIL-CLOSED por construção: o aging por omissão TEM de estar activo
// (AgingStep>0) e todo o override de classe/tenant que o omita HERDA-o em vez de o
// desligar (ver [NewDispatcher]) — não pode ser silenciosamente desactivado por um
// zero-value. A decisão é também LATENCY-AWARE por PRAZO (EDF): além da idade
// nominal, o SLO da tarefa entra na prioridade efectiva pela FOLGA ABSOLUTA até ao
// prazo (slack = SLO − idade) — quanto menor a folga, mais urgente —, pelo que uma
// tarefa NOVA de SLO apertado pode ultrapassar uma ANTIGA de SLO folgado sem
// inversão de prazo, e a decisão não depende só da prioridade nominal.
//
// COMPOSIÇÃO (não reimplementa nada):
//   - Filas particionadas tenant:priority (AOS-030, queue.go) dão o bounding +
//     backpressure. O Dispatcher ORDENA sobre elas por prioridade efectiva; a
//     contabilidade de profundidade/saturação continua a ser das filas.
//   - Admission control global (AOS-027, admission.go) decide o que é ADMITIDO. O
//     Dispatcher SÓ despacha trabalho ADMITIDO (com headroom reservado): compõe o
//     Admit pela porta [AdmissionGate], não o reimplementa. Serve primeiro a MAIOR
//     prioridade efectiva ADMISSÍVEL.
//
// DETERMINISMO / REPLAY (ADR-001/010): o relógio da idade é INJECTÁVEL (sem
// time.Now na decisão); a selecção ordena um SLICE (nunca ordem de mapa Go) com
// TIE-BREAK ESTÁVEL (prioridade efectiva desc → timestamp de entrada asc →
// task_id asc, um total order porque o task_id é único); a serialização dos
// eventos é estável (structs, sem mapas). Mesmos inputs ⇒ mesma ordem e MESMOS
// bytes de evento.
//
// EVENTOS append-only: task_scheduled (com prioridade efectiva/idade) em cada
// despacho; priority_aged quando o aging elevou a prioridade efectiva acima da
// base (promoção). O replay reconstrói a ordem de despacho.
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

// Tipos de evento append-only do scheduling (AOS-032). Contrato observável.
const (
	// EventTaskScheduled marca o despacho de uma tarefa, com a prioridade efectiva
	// e a idade no momento da decisão.
	EventTaskScheduled = "scheduling.task_scheduled"
	// EventPriorityAged marca uma PROMOÇÃO por aging: a prioridade efectiva da
	// tarefa despachada excedeu a sua prioridade base por ter envelhecido em espera.
	EventPriorityAged = "scheduling.priority_aged"
)

// DefaultDispatchNHI é a NHI por omissão do dispatcher nos eventos que emite.
const DefaultDispatchNHI = "nhi:control-plane/scheduler/priority-dispatch"

// dispatchStreamPrefix é o prefixo do stream de eventos de despacho (um por
// instância nomeada).
const dispatchStreamPrefix = "scheduling/dispatch/"

// Atributos de span (OTel, porta zero-dep).
const (
	attrSchedTask      = "aos.scheduling.task_id"
	attrSchedPartition = "aos.scheduling.partition"
	attrSchedClass     = "aos.scheduling.class"
	attrSchedBasePrio  = "aos.scheduling.base_priority"
	attrSchedEffPrio   = "aos.scheduling.effective_priority"
	attrSchedWaitMs    = "aos.scheduling.wait_ms"
	attrSchedAged      = "aos.scheduling.aged"
	attrSchedAdmitted  = "aos.scheduling.admitted"
)

const opDispatch = "priority_dispatch"

// ErrInvalidAgingParams sinaliza parâmetros de aging inválidos (fail-closed na
// construção).
var ErrInvalidAgingParams = errors.New("scheduler: parâmetros de aging inválidos")

// ErrDuplicateTask sinaliza uma submissão com um task_id já pendente.
var ErrDuplicateTask = errors.New("scheduler: task_id já pendente no dispatcher")

// ErrAdmissionNotConfigured sinaliza uma construção sem decisão explícita sobre a
// admissão (nem [WithAdmission] nem [WithoutAdmission]). Fail-closed: a admissão
// nunca é desligada por esquecimento.
var ErrAdmissionNotConfigured = errors.New("scheduler: admissão não configurada (use WithAdmission ou WithoutAdmission)")

// AgingParams são os parâmetros de aging de uma classe de prioridade (por classe
// e opcionalmente por tenant — configuráveis, NUNCA constantes). Definem a
// prioridade base e a taxa a que a espera a promove.
type AgingParams struct {
	// Base é a prioridade base da classe (maior ⇒ servido primeiro em igualdade de
	// idade). É o que dá a ordenação por prioridade P0 > P1 > P2.
	Base int64
	// AgingStep é quanto a prioridade efectiva sobe por cada AgingInterval de espera
	// (>0 activa o aging; 0 desliga-o para a classe). Monótono na idade.
	AgingStep int64
	// AgingInterval é o quantum de idade do aging (>0 se AgingStep>0). Ex.: 1s ⇒ a
	// prioridade sobe AgingStep a cada segundo em espera.
	AgingInterval time.Duration
	// SLOWeight pondera o termo LATENCY-AWARE por PRAZO (EDF): a PROXIMIDADE do prazo
	// (−slack = idade−SLO, em ms) multiplicada por SLOWeight entra na prioridade
	// efectiva (0 = ignora o SLO). Ordena por FOLGA ABSOLUTA até ao prazo, NÃO pela
	// fracção consumida (idade/SLO) — evita a inversão de prazo em que uma tarefa
	// antiga de SLO folgado ultrapassaria uma nova de SLO apertado prestes a falhar.
	SLOWeight int64
}

// validate impõe as invariantes fail-closed dos parâmetros.
func (p AgingParams) validate() error {
	if p.AgingStep < 0 {
		return fmt.Errorf("%w: AgingStep negativo (%d)", ErrInvalidAgingParams, p.AgingStep)
	}
	if p.AgingStep > 0 && p.AgingInterval <= 0 {
		return fmt.Errorf("%w: AgingStep>0 exige AgingInterval>0 (obtido %v)", ErrInvalidAgingParams, p.AgingInterval)
	}
	if p.AgingInterval < 0 {
		return fmt.Errorf("%w: AgingInterval negativo (%v)", ErrInvalidAgingParams, p.AgingInterval)
	}
	if p.SLOWeight < 0 {
		return fmt.Errorf("%w: SLOWeight negativo (%d)", ErrInvalidAgingParams, p.SLOWeight)
	}
	return nil
}

// sloCompCeil limita o termo de SLO em AMBOS os sentidos (folga grande ⇒ termo
// muito negativo; overdue prolongado ⇒ termo muito positivo). O clamp com
// multiplicação SATURADA garante que nem um SLOWeight grande nem uma espera longa
// transbordam o int64 e INVERTEM a ordem. 2^40 fica muito abaixo do overflow e
// muito acima de prioridades base/aging reais, pelo que não altera a ordenação.
const sloCompCeil = int64(1) << 40

// effective calcula a prioridade efectiva e as suas componentes para uma idade e
// um SLO (em nanos). É PURA e determinística: mesmos inputs ⇒ mesmo resultado
// (aritmética inteira, sem relógio, sem float — replay byte-a-byte). MONÓTONA na
// idade: idade↑ ⇒ agingComp↑ e sloComp↑ (folga↓), logo eff↑ (anti-starvation).
func (p AgingParams) effective(ageNanos, sloNanos int64) (eff, agingComp, sloComp int64) {
	if ageNanos < 0 {
		ageNanos = 0
	}
	if p.AgingStep > 0 && p.AgingInterval > 0 {
		// Divisão-primeiro (idade/intervalo) antes de escalar: não transborda.
		agingComp = p.AgingStep * (ageNanos / p.AgingInterval.Nanoseconds())
	}
	if p.SLOWeight > 0 && sloNanos > 0 {
		// LATENCY-AWARE por PRAZO (EDF), NÃO por fracção consumida. A urgência é
		// função da FOLGA ABSOLUTA até ao prazo (slack = SLO − idade): quanto menor a
		// folga (ou mais overdue), maior a urgência. Usa a PROXIMIDADE do prazo
		// (−slack = idade − SLO) em MILISSEGUNDOS, com DIVISÃO-PRIMEIRO por 1e6 para
		// não transbordar antes de escalar, e multiplicação SATURADA a ±sloCompCeil
		// para que o overflow nunca inverta a ordem. Assim uma tarefa nova de SLO
		// apertado (pouca folga) ultrapassa uma antiga de SLO folgado (muita folga).
		proximityMs := (ageNanos - sloNanos) / 1_000_000
		sloComp = clampedSLOComp(p.SLOWeight, proximityMs)
	}
	eff = p.Base + agingComp + sloComp
	return eff, agingComp, sloComp
}

// clampedSLOComp devolve weight*proximityMs SATURADO a ±sloCompCeil. É uma
// multiplicação segura: nunca transborda o int64, logo o overflow nunca inverte a
// urgência (finding AOS-032 integer-overflow). weight é >=0 (validado); o sinal do
// resultado é o de proximityMs (>0 overdue, <0 com folga).
func clampedSLOComp(weight, proximityMs int64) int64 {
	if weight == 0 || proximityMs == 0 {
		return 0
	}
	mag := proximityMs
	if mag < 0 {
		mag = -mag
	}
	// Saturação ANTES de multiplicar: se weight*mag excederia o tecto, devolve o
	// tecto com o sinal certo (evita o produto que transbordaria).
	if weight > sloCompCeil/mag {
		if proximityMs > 0 {
			return sloCompCeil
		}
		return -sloCompCeil
	}
	v := weight * proximityMs
	if v > sloCompCeil {
		return sloCompCeil
	}
	if v < -sloCompCeil {
		return -sloCompCeil
	}
	return v
}

// agingKey é a chave (tenant, classe) para override por tenant.
type agingKey struct {
	tenant string
	class  string
}

// agingResolver resolve os parâmetros de aging por especificidade: (tenant,
// classe) > classe > omissão. É a configurabilidade por classe/tenant exigida
// pelo ticket.
type agingResolver struct {
	def      AgingParams
	byClass  map[string]AgingParams
	byTenant map[agingKey]AgingParams
}

// params devolve os parâmetros mais específicos para (tenant, classe).
func (r agingResolver) params(tenant, class string) AgingParams {
	if p, ok := r.byTenant[agingKey{tenant: tenant, class: class}]; ok {
		return p
	}
	if p, ok := r.byClass[class]; ok {
		return p
	}
	return r.def
}

// Task é uma unidade de trabalho SCHEDULÁVEL. Carrega a classe de prioridade, as
// dimensões de partição, o custo estimado (para a admissão) e um SLO opcional
// (latency-aware). É opaca ao dispatcher fora destes campos.
type Task struct {
	// ID é o identificador único (chave de dedup e tie-break estável).
	ID string
	// Tenant e Class são as dimensões de partição tenant:priority. Class é a classe
	// de prioridade (ex.: "P0"/"P1"/"P2"), mapeada à Priority da partição.
	Tenant string
	Class  string
	// Key é o bucket de admissão (provider:model:region). Se zero, usa a chave por
	// omissão do dispatcher.
	Key ProviderKey
	// Board é a sub-dimensão opcional de admissão (quota multidimensional).
	Board string
	// Cost é o custo estimado em tokens para a admissão (0 ⇒ o estimador do Admit
	// decide).
	Cost int64
	// SLO é o objectivo de latência da tarefa (0 = sem SLO). Alimenta o termo
	// latency-aware da prioridade efectiva.
	SLO time.Duration
}

// partition devolve a chave de partição da tarefa.
func (t Task) partition() Partition { return Partition{Tenant: t.Tenant, Priority: t.Class} }

// candidate é uma tarefa pendente com o instante de entrada (para a idade). A
// ordem TOTAL do despacho é dada pela prioridade efectiva e, em empate, pelo
// timestamp de entrada (asc) e depois pelo task_id (único, asc) — ver [Dispatcher.Dispatch].
type candidate struct {
	task    Task
	enqueue int64 // UnixNano da submissão (relógio injectável)
}

// AdmissionGate é a PORTA que o dispatcher consulta para saber se um trabalho é
// ADMITIDO (integração com AOS-027 sem o reimplementar). *[Admission] satisfá-la.
// Injectada via [WithAdmission]; sem ela, o dispatcher trata tudo como admissível
// (modo de ordenação pura, útil em testes de prioridade/aging).
type AdmissionGate interface {
	Admit(ctx context.Context, req AdmitRequest) (AdmitResult, error)
}

// reservationReleaser é a capacidade OPCIONAL de DEVOLVER (rollback) uma reserva de
// headroom já concedida. *[Admission] satisfá-la via [Admission.Release]. O
// dispatcher usa-a para NÃO VAZAR headroom quando um erro ocorre DEPOIS de a
// admissão conceder a reserva (ex.: Dequeue ou emit de evento falham): a reserva é
// libertada antes de propagar o erro, senão o headroom admitido nunca regressa e o
// dispatcher acaba por estagnar (starvation global por falta de headroom).
type reservationReleaser interface {
	Release(ctx context.Context, key ProviderKey, reservationID string, costTokens, costRequests int64) error
}

// Dispatcher despacha tarefas por prioridade efectiva (base + aging + SLO), só
// despachando trabalho ADMITIDO, com ordem determinística e reproduzível em
// replay. É seguro para concorrência (um mutex protege o índice de candidatos).
// Construir com [NewDispatcher].
type Dispatcher struct {
	mu    sync.Mutex
	cands []*candidate
	byID  map[string]struct{}

	cfg agingResolver
	adm AdmissionGate
	// admConfigured regista uma DECISÃO EXPLÍCITA sobre a admissão (via
	// [WithAdmission] ou [WithoutAdmission]). [NewDispatcher] falha se ficar false —
	// a admissão nunca é desligada por esquecimento, só por opção declarada.
	admConfigured bool
	queues        *PartitionedQueues
	log           EventLog
	now           func() time.Time
	tracer        agentruntime.Tracer
	producer      eventstore.Producer
	name          string
	defKey        ProviderKey
	nEvents       uint64
}

// DispatcherOption configura o [Dispatcher].
type DispatcherOption func(*Dispatcher)

// WithDefaultAging fixa os parâmetros de aging por OMISSÃO (aplicados a classes
// sem override). Sem override, todas as classes partilham esta base.
func WithDefaultAging(p AgingParams) DispatcherOption {
	return func(d *Dispatcher) { d.cfg.def = p }
}

// WithClassAging fixa parâmetros de aging para uma CLASSE de prioridade (ex.:
// "P0"). Sobrepõe a omissão para essa classe.
func WithClassAging(class string, p AgingParams) DispatcherOption {
	return func(d *Dispatcher) {
		if class != "" {
			d.cfg.byClass[class] = p
		}
	}
}

// WithTenantClassAging fixa parâmetros de aging para (tenant, classe) — o nível
// MAIS específico. É a configurabilidade por classe/tenant do ticket.
func WithTenantClassAging(tenant, class string, p AgingParams) DispatcherOption {
	return func(d *Dispatcher) {
		if class != "" {
			d.cfg.byTenant[agingKey{tenant: tenant, class: class}] = p
		}
	}
}

// WithAdmission ACOPLA o dispatcher ao admission control (AOS-027): só despacha
// trabalho ADMITIDO (com headroom reservado). Regista a decisão explícita de
// admissão exigida por [NewDispatcher].
func WithAdmission(gate AdmissionGate) DispatcherOption {
	return func(d *Dispatcher) {
		if gate != nil {
			d.adm = gate
			d.admConfigured = true
		}
	}
}

// WithoutAdmission DECLARA EXPLICITAMENTE o modo de ORDENAÇÃO PURA (sem admission
// control): o dispatcher despacha o candidato de maior prioridade efectiva sem
// consultar headroom. É o oposto de [WithAdmission] e um dos dois TEM de ser
// escolhido — [NewDispatcher] recusa a construção se nenhum for dado, para que a
// admissão nunca fique desligada por esquecimento (fail-closed), só por decisão.
func WithoutAdmission() DispatcherOption {
	return func(d *Dispatcher) { d.admConfigured = true }
}

// WithQueues COMPÕE as filas particionadas (AOS-030) para bounding + backpressure:
// Submit enfileira (aplica-se a política se saturado) e Dispatch liberta um lugar
// da partição servida. A selecção de item é sempre do dispatcher.
func WithQueues(q *PartitionedQueues) DispatcherOption {
	return func(d *Dispatcher) {
		if q != nil {
			d.queues = q
		}
	}
}

// WithDispatchLog injecta o Event Store para os eventos de despacho.
func WithDispatchLog(log EventLog) DispatcherOption {
	return func(d *Dispatcher) {
		if log != nil {
			d.log = log
		}
	}
}

// WithDispatchClock injecta o relógio da idade (determinismo/replay).
func WithDispatchClock(now func() time.Time) DispatcherOption {
	return func(d *Dispatcher) {
		if now != nil {
			d.now = now
		}
	}
}

// WithDispatchTracer injecta a porta OTel (spans de tempo de espera por classe).
func WithDispatchTracer(t agentruntime.Tracer) DispatcherOption {
	return func(d *Dispatcher) {
		if t != nil {
			d.tracer = t
		}
	}
}

// WithDispatchProducer injecta a NHI emissora dos eventos.
func WithDispatchProducer(p eventstore.Producer) DispatcherOption {
	return func(d *Dispatcher) {
		if p.NHIID != "" {
			d.producer = p
		}
	}
}

// WithDispatchName nomeia a instância (usada no stream de eventos).
func WithDispatchName(name string) DispatcherOption {
	return func(d *Dispatcher) {
		if name != "" {
			d.name = name
		}
	}
}

// WithDefaultKey fixa a [ProviderKey] por omissão da admissão (para tarefas sem
// Key própria).
func WithDefaultKey(k ProviderKey) DispatcherOption {
	return func(d *Dispatcher) { d.defKey = k }
}

// NewDispatcher constrói o dispatcher com parâmetros de aging por omissão. Valida
// todos os parâmetros fail-closed.
func NewDispatcher(def AgingParams, opts ...DispatcherOption) (*Dispatcher, error) {
	d := &Dispatcher{
		byID: make(map[string]struct{}),
		cfg: agingResolver{
			def:      def,
			byClass:  make(map[string]AgingParams),
			byTenant: make(map[agingKey]AgingParams),
		},
		now:      time.Now,
		tracer:   agentruntime.NoopTracer{},
		producer: eventstore.Producer{NHIID: DefaultDispatchNHI},
		name:     "default",
	}
	for _, opt := range opts {
		opt(d)
	}
	if err := d.cfg.def.validate(); err != nil {
		return nil, fmt.Errorf("aging por omissão: %w", err)
	}
	// FAIL-CLOSED anti-starvation: o aging por OMISSÃO tem de estar ACTIVO
	// (AgingStep>0, AgingInterval>0). É o termo que cresce sem tecto com a idade e
	// garante que trabalho antigo ultrapassa SEMPRE trabalho novo de base mais alta.
	// Um default de aging desligado (zero-value) permitiria starvation permanente por
	// construção — a garantia central "ZERO starvation" não pode ser silenciosamente
	// desactivada.
	if d.cfg.def.AgingStep <= 0 || d.cfg.def.AgingInterval <= 0 {
		return nil, fmt.Errorf("%w: aging por omissão tem de ter AgingStep>0 e AgingInterval>0 (garantia anti-starvation; obtido step=%d interval=%v)",
			ErrInvalidAgingParams, d.cfg.def.AgingStep, d.cfg.def.AgingInterval)
	}
	// Overrides de classe/tenant que NÃO especificam aging (AgingStep==0) HERDAM o
	// aging por omissão em vez de o DESLIGAR silenciosamente. Assim nenhuma classe
	// efectiva fica sem aging (starvation) por um zero-value; um override só altera o
	// aging se o pedir explicitamente (AgingStep>0). Um AgingStep<0 (inválido) NÃO é
	// herdado — cai na validação abaixo (fail-closed).
	for class, p := range d.cfg.byClass {
		if p.AgingStep == 0 {
			p.AgingStep = d.cfg.def.AgingStep
			p.AgingInterval = d.cfg.def.AgingInterval
			d.cfg.byClass[class] = p
		}
		if err := p.validate(); err != nil {
			return nil, fmt.Errorf("classe %q: %w", class, err)
		}
	}
	for k, p := range d.cfg.byTenant {
		if p.AgingStep == 0 {
			p.AgingStep = d.cfg.def.AgingStep
			p.AgingInterval = d.cfg.def.AgingInterval
			d.cfg.byTenant[k] = p
		}
		if err := p.validate(); err != nil {
			return nil, fmt.Errorf("tenant %q classe %q: %w", k.tenant, k.class, err)
		}
	}
	// FAIL-CLOSED admissão: exige uma decisão EXPLÍCITA — [WithAdmission] (acoplar
	// AOS-027, só admitido despachado) ou [WithoutAdmission] (ordenação pura). Sem
	// nenhuma, recusa a construção para que a admissão não fique desligada por
	// esquecimento.
	if !d.admConfigured {
		return nil, ErrAdmissionNotConfigured
	}
	return d, nil
}

// SubmitResult é o veredicto de [Dispatcher.Submit].
type SubmitResult struct {
	// Queued indica se a tarefa foi aceite para escalonamento. Se false, a fila
	// (AOS-030) estava saturada e a política seleccionou Action.
	Queued bool
	// Action é a acção de degradação seleccionada quando Queued=false (só com filas
	// acopladas). Vazia caso contrário.
	Action DegradationAction
	// PolicyVersion é a versão da política que seleccionou a acção (se houve).
	PolicyVersion string
}

// Submit regista uma tarefa para escalonamento. Se as filas (AOS-030) estiverem
// acopladas, enfileira primeiro (bounding + backpressure): saturada ⇒ devolve a
// acção de degradação e NÃO indexa a tarefa. Caso contrário, indexa-a com o
// instante de entrada (para a idade). Fail-closed: task_id duplicado é rejeitado.
func (d *Dispatcher) Submit(ctx context.Context, t Task) (SubmitResult, error) {
	if t.ID == "" {
		return SubmitResult{}, fmt.Errorf("scheduler: task sem ID")
	}
	if t.Class == "" {
		return SubmitResult{}, fmt.Errorf("scheduler: task %q sem classe de prioridade", t.ID)
	}
	nowNano := d.now().UnixNano()

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.byID[t.ID]; ok {
		return SubmitResult{}, fmt.Errorf("%w: %q", ErrDuplicateTask, t.ID)
	}

	if d.queues != nil {
		res, err := d.queues.Enqueue(ctx, WorkItem{ID: t.ID, Tenant: t.Tenant, Priority: t.Class})
		if err != nil {
			return SubmitResult{}, err
		}
		if !res.Admitted {
			// Fila saturada: a política seleccionou uma acção; não se indexa.
			return SubmitResult{Queued: false, Action: res.Action, PolicyVersion: res.PolicyVersion}, nil
		}
	}

	d.cands = append(d.cands, &candidate{task: t, enqueue: nowNano})
	d.byID[t.ID] = struct{}{}
	return SubmitResult{Queued: true}, nil
}

// DispatchResult é o veredicto de [Dispatcher.Dispatch].
type DispatchResult struct {
	// Dispatched indica se uma tarefa foi despachada.
	Dispatched bool
	// Task é a tarefa despachada (válida se Dispatched).
	Task Task
	// BasePriority e EffectivePriority são as prioridades base e efectiva da tarefa
	// no instante da decisão.
	BasePriority      int64
	EffectivePriority int64
	// Aged indica que o aging/SLO elevou a prioridade efectiva acima da base
	// (houve promoção; emitiu-se priority_aged).
	Aged bool
	// WaitMs é o tempo de espera (idade) da tarefa despachada, em ms.
	WaitMs int64
	// ReservationID é a reserva de headroom do admit (para posterior Release). Vazia
	// se não há admission gate acoplado.
	ReservationID string
	// RetryAfter, quando !Dispatched e havia candidatos ADIADOS pela admissão, é o
	// menor retry_after aconselhado (quando voltar a haver headroom). 0 se não havia
	// candidatos de todo.
	RetryAfter time.Duration
}

// Dispatch selecciona e despacha a tarefa de MAIOR prioridade efectiva que a
// admissão ADMITE. Fluxo:
//  1. calcula a prioridade efectiva de cada candidato (base + aging + SLO) com o
//     relógio injectável;
//  2. ORDENA um slice (nunca mapa) por prioridade efectiva desc, tie-break estável
//     (timestamp de entrada asc, depois task_id asc);
//  3. percorre por ordem: o PRIMEIRO candidato que a admissão CONCEDE é despachado
//     (serve a maior prioridade ADMISSÍVEL); um candidato ADIADO é saltado (o seu
//     retry_after é agregado); um candidato REJEITADO permanentemente é ignorado;
//  4. emite task_scheduled (+ priority_aged se houve promoção) e liberta um lugar
//     da fila da partição servida.
//
// Se nenhum candidato for admitido, devolve Dispatched=false com o menor
// retry_after — NUNCA descarta trabalho.
func (d *Dispatcher) Dispatch(ctx context.Context) (DispatchResult, error) {
	ctx, span := d.tracer.StartSpan(ctx, opDispatch)
	defer span.End()

	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.cands) == 0 {
		return DispatchResult{Dispatched: false}, nil
	}
	nowNano := d.now().UnixNano()

	order := make([]*candidate, len(d.cands))
	copy(order, d.cands)
	sort.SliceStable(order, func(i, j int) bool {
		pi := d.cfg.params(order[i].task.Tenant, order[i].task.Class)
		pj := d.cfg.params(order[j].task.Tenant, order[j].task.Class)
		ei, _, _ := pi.effective(nowNano-order[i].enqueue, order[i].task.SLO.Nanoseconds())
		ej, _, _ := pj.effective(nowNano-order[j].enqueue, order[j].task.SLO.Nanoseconds())
		if ei != ej {
			return ei > ej // maior prioridade efectiva primeiro
		}
		if order[i].enqueue != order[j].enqueue {
			return order[i].enqueue < order[j].enqueue // mais antigo primeiro
		}
		return order[i].task.ID < order[j].task.ID // tie-break estável (total order)
	})

	var minRetry time.Duration
	haveRetry := false

	for _, c := range order {
		var resID string
		if d.adm != nil {
			ar, err := d.adm.Admit(ctx, AdmitRequest{
				Key:             d.keyFor(c.task),
				Tenant:          c.task.Tenant,
				Board:           c.task.Board,
				EstimatedTokens: c.task.Cost,
				RequestID:       "sched:" + c.task.ID,
			})
			if err != nil {
				return DispatchResult{}, err
			}
			if ar.Rejected {
				// Rejeição PERMANENTE (custo > tecto): nunca admissível; salta.
				continue
			}
			if !ar.Granted {
				if !haveRetry || ar.RetryAfter < minRetry {
					minRetry = ar.RetryAfter
					haveRetry = true
				}
				continue
			}
			resID = ar.ReservationID
		}

		// Candidato admitido: despacha.
		p := d.cfg.params(c.task.Tenant, c.task.Class)
		age := nowNano - c.enqueue
		if age < 0 {
			age = 0
		}
		eff, agingComp, sloComp := p.effective(age, c.task.SLO.Nanoseconds())
		aged := (agingComp + sloComp) > 0

		d.removeLocked(c.task.ID)
		if d.queues != nil {
			// Liberta UM lugar da partição servida (bounding/backpressure por
			// contagem; a identidade do item é do dispatcher, não da fila FIFO).
			if _, _, derr := d.queues.Dequeue(ctx, c.task.partition()); derr != nil {
				// A reserva já foi concedida: devolve o headroom antes de propagar o
				// erro, senão ele fica preso (leak) e o dispatcher acaba por estagnar.
				d.releaseReservation(ctx, c.task, resID)
				return DispatchResult{}, derr
			}
		}

		waitMs := time.Duration(age).Milliseconds()
		if err := d.emitScheduled(ctx, c.task, p.Base, eff, agingComp, sloComp, waitMs, resID, nowNano); err != nil {
			d.releaseReservation(ctx, c.task, resID)
			return DispatchResult{}, err
		}
		if aged {
			if err := d.emitAged(ctx, c.task, p.Base, eff, agingComp, sloComp, waitMs, nowNano); err != nil {
				d.releaseReservation(ctx, c.task, resID)
				return DispatchResult{}, err
			}
		}

		span.SetAttribute(attrSchedTask, c.task.ID)
		span.SetAttribute(attrSchedPartition, c.task.partition().String())
		span.SetAttribute(attrSchedClass, c.task.Class)
		span.SetAttribute(attrSchedBasePrio, p.Base)
		span.SetAttribute(attrSchedEffPrio, eff)
		span.SetAttribute(attrSchedWaitMs, waitMs)
		span.SetAttribute(attrSchedAged, aged)
		span.SetAttribute(attrSchedAdmitted, true)

		return DispatchResult{
			Dispatched:        true,
			Task:              c.task,
			BasePriority:      p.Base,
			EffectivePriority: eff,
			Aged:              aged,
			WaitMs:            waitMs,
			ReservationID:     resID,
		}, nil
	}

	// Nenhum candidato admitido: adia (nunca descarta).
	span.SetAttribute(attrSchedAdmitted, false)
	return DispatchResult{Dispatched: false, RetryAfter: minRetry}, nil
}

// removeLocked remove um candidato pelo task_id, preservando a ordem relativa dos
// restantes (estabilidade). Requer d.mu.
func (d *Dispatcher) removeLocked(id string) {
	for i, c := range d.cands {
		if c.task.ID == id {
			d.cands = append(d.cands[:i], d.cands[i+1:]...)
			break
		}
	}
	delete(d.byID, id)
}

// releaseReservation devolve (rollback) uma reserva de headroom concedida após um
// erro NO despacho, para não a VAZAR. Best-effort: no-op se não houve reserva
// (resID vazio / sem gate) ou se o gate não suporta Release. Devolve o custo
// estimado reservado (>=1, como no Admit); sobre-libertar é seguro (o Release faz
// clamp em 0 por reserva, nunca abre headroom além do reservado). Requer d.mu.
func (d *Dispatcher) releaseReservation(ctx context.Context, t Task, resID string) {
	if resID == "" || d.adm == nil {
		return
	}
	rel, ok := d.adm.(reservationReleaser)
	if !ok {
		return
	}
	tokens := t.Cost
	if tokens < 1 {
		tokens = 1
	}
	_ = rel.Release(ctx, d.keyFor(t), resID, tokens, 1)
}

// keyFor devolve a chave de admissão da tarefa (a sua própria, ou a por omissão).
func (d *Dispatcher) keyFor(t Task) ProviderKey {
	if (t.Key == ProviderKey{}) {
		return d.defKey
	}
	return t.Key
}

// Pending devolve o número de tarefas pendentes (por despachar).
func (d *Dispatcher) Pending() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.cands)
}

// DispatchStreamID devolve o stream de eventos de despacho desta instância (útil
// para replay/asserção byte-a-byte).
func (d *Dispatcher) DispatchStreamID() string { return dispatchStreamPrefix + d.name }

// dispatchPayload é o corpo serializado (estável, sem mapas) dos eventos de
// despacho — determinismo/replay.
type dispatchPayload struct {
	Type              string `json:"type"`
	TaskID            string `json:"task_id"`
	Tenant            string `json:"tenant,omitempty"`
	Class             string `json:"class"`
	Partition         string `json:"partition"`
	BasePriority      int64  `json:"base_priority"`
	EffectivePriority int64  `json:"effective_priority"`
	AgingComponent    int64  `json:"aging_component,omitempty"`
	SLOComponent      int64  `json:"slo_component,omitempty"`
	WaitMs            int64  `json:"wait_ms"`
	SLOms             int64  `json:"slo_ms,omitempty"`
	ReservationID     string `json:"reservation_id,omitempty"`
	TSUnixNano        int64  `json:"ts_unix_nano"`
}

// emitScheduled persiste task_scheduled no stream de despacho.
func (d *Dispatcher) emitScheduled(ctx context.Context, t Task, base, eff, agingComp, sloComp, waitMs int64, resID string, nowNano int64) error {
	return d.emitEvent(ctx, EventTaskScheduled, dispatchPayload{
		Type:              EventTaskScheduled,
		TaskID:            t.ID,
		Tenant:            t.Tenant,
		Class:             t.Class,
		Partition:         t.partition().String(),
		BasePriority:      base,
		EffectivePriority: eff,
		AgingComponent:    agingComp,
		SLOComponent:      sloComp,
		WaitMs:            waitMs,
		SLOms:             t.SLO.Milliseconds(),
		ReservationID:     resID,
		TSUnixNano:        nowNano,
	})
}

// emitAged persiste priority_aged (promoção por aging) no stream de despacho.
func (d *Dispatcher) emitAged(ctx context.Context, t Task, base, eff, agingComp, sloComp, waitMs int64, nowNano int64) error {
	return d.emitEvent(ctx, EventPriorityAged, dispatchPayload{
		Type:              EventPriorityAged,
		TaskID:            t.ID,
		Tenant:            t.Tenant,
		Class:             t.Class,
		Partition:         t.partition().String(),
		BasePriority:      base,
		EffectivePriority: eff,
		AgingComponent:    agingComp,
		SLOComponent:      sloComp,
		WaitMs:            waitMs,
		SLOms:             t.SLO.Milliseconds(),
		TSUnixNano:        nowNano,
	})
}

// emitEvent serializa e escreve um evento de despacho no stream da instância, com
// step_id "sched-N" monotónico (idempotente por (run_id, step_id) na dedup do
// Event Store). Fail-closed: um erro do store propaga. Requer d.mu (nEvents é
// mutado sob o lock do Dispatch).
func (d *Dispatcher) emitEvent(ctx context.Context, evType string, pl dispatchPayload) error {
	if d.log == nil {
		return nil
	}
	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	d.nEvents++
	stepID := "sched-" + strconv.FormatUint(d.nEvents, 10)
	streamID := d.DispatchStreamID()
	_, err = d.log.Append(ctx, streamID, eventstore.EventInput{
		Type:     evType,
		Payload:  raw,
		RunID:    streamID,
		StepID:   stepID,
		Producer: d.producer,
	})
	return err
}

// ScheduleRecord é um evento de despacho reconstruído do log (para replay).
type ScheduleRecord struct {
	Type              string
	TaskID            string
	Class             string
	Partition         string
	BasePriority      int64
	EffectivePriority int64
	Aged              bool
	WaitMs            int64
	Seq               uint64
}

// ReplaySchedule reconstrói a sequência de despachos da instância a partir do
// Event Store (append-only, por ordem de seq). Prova de que a ordem de despacho
// se reconstrói do log (ADR-001/010).
func (d *Dispatcher) ReplaySchedule(ctx context.Context) ([]ScheduleRecord, error) {
	if d.log == nil {
		return nil, nil
	}
	streamID := d.DispatchStreamID()
	evs, err := d.log.Read(ctx, streamID, 1)
	if err != nil {
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]ScheduleRecord, 0, len(evs))
	for _, ev := range evs {
		var pl dispatchPayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			return nil, err
		}
		out = append(out, ScheduleRecord{
			Type:              pl.Type,
			TaskID:            pl.TaskID,
			Class:             pl.Class,
			Partition:         pl.Partition,
			BasePriority:      pl.BasePriority,
			EffectivePriority: pl.EffectivePriority,
			Aged:              ev.Type == EventPriorityAged,
			WaitMs:            pl.WaitMs,
			Seq:               ev.Seq,
		})
	}
	return out, nil
}
