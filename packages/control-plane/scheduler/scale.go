// scale.go — ESCALA HORIZONTAL dirigida por SLIs + ESCADA de degradação GLOBAL
// (AOS-107).
//
// Este ficheiro NÃO reimplementa nenhuma das peças de escala/degradação já feitas
// no scheduler — COMPÕE-as (ADR-008, tecnica/10 §5):
//
//   - a ESCADA shed→defer→downgrade→reject é o [Degrader] (AOS-031);
//   - a POLÍTICA declarativa versionada que SELECCIONA o degrau é o [PolicyEngine]
//     (AOS-030);
//   - o max_spawn derivado do headroom é [deriveMaxSpawn] (AOS-028);
//   - as FILAS limitadas + backpressure são as [PartitionedQueues] (AOS-030);
//   - o p95 de wait deriva dos WaitMs do [Dispatcher] (AOS-032);
//   - o dimensionamento do POOL pré-aquecido é o Autoscaler de AOS-103 (substrate/
//     sandbox), ligado POR SINAL no ápice — este ficheiro emite o alvo de réplicas
//     como um [ReplicaTarget] numa porta [ReplicaSink]; a APLICAÇÃO real das réplicas
//     de worker e o link ao pool vivem no composition root (packages/integration,
//     diferido). O AOS-107 fornece o SINAL de escala + a escada, não o mecanismo de
//     posse (o Assigner de AOS-099 garante a posse sem rebalancing disruptivo).
//
// O NÚCLEO NOVO é: (1) [deriveDesiredReplicas] — a fórmula pura, monótona, limitada
// pelo headroom e fail-closed (0-crescimento sob headroom nulo); (2) o
// [WaitP95Recorder] — o SLI de p95 de wait; (3) o [HorizontalScaler] — o laço Tick
// determinista que lê os SLIs e ou faz scale-out (com headroom) ou conduz a escada
// global (sem headroom), com o degrau activo a subir conforme a pressão agregada e a
// reversão ao normalizar via [Degrader.Normalize].
//
// DETERMINISMO: relógio/ticker injectáveis; [deriveDesiredReplicas] e o recorder são
// puros/testáveis sem relógio real; iteração ordenada (via os snapshots já ordenados
// das filas). Sem time.Now/rand nas decisões.
package scheduler

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// ErrScalerDeps sinaliza dependências obrigatórias do [HorizontalScaler] em falta
// (filas de SLI de profundidade ou recorder de p95 de wait). Fail-closed.
var ErrScalerDeps = errors.New("scheduler: dependências do HorizontalScaler em falta (queues/wait)")

// opScaleTick é o nome de operação do span de uma avaliação de escala/degradação.
const opScaleTick = "scale_tick"

// Atributos de span (OTel, porta zero-dep).
const (
	attrScaleQueueDepth  = "aos.scale.queue_depth"
	attrScaleWaitP95Ms   = "aos.scale.wait_p95_ms"
	attrScaleHeadroomT   = "aos.scale.headroom_tokens"
	attrScaleMaxSpawn    = "aos.scale.max_spawn"
	attrScaleDesired     = "aos.scale.desired_replicas"
	attrScaleActual      = "aos.scale.actual_replicas"
	attrScaleDegraded    = "aos.scale.degraded"
	attrScaleDegLevel    = "aos.scale.degradation_level"
	attrScaleDegAction   = "aos.scale.degradation_action"
	attrScaleReasonAttr  = "aos.scale.reason"
	attrScalePolicyVersn = "aos.scale.policy_version"
)

// Motivos estáveis da decisão de escala (valor de [AttrMetricScaleReason]).
const (
	ScaleReasonScaleOut          = "scale_out"
	ScaleReasonScaleIn           = "scale_in"
	ScaleReasonSteady            = "steady"
	ScaleReasonHeadroomExhausted = "headroom_exhausted"
)

// ---------------------------------------------------------------------------
// ReplicaScalerConfig + deriveDesiredReplicas — a fórmula pura (núcleo de AC1/AC3).
// ---------------------------------------------------------------------------

// ReplicaScalerConfig parametriza [deriveDesiredReplicas] e o [HorizontalScaler]. É
// declarativa (nunca constantes hard-coded no caminho de decisão): o alvo de réplicas
// deriva do headroom e dos SLIs através destes parâmetros.
type ReplicaScalerConfig struct {
	// MinReplicas é o piso de réplicas quando há headroom (baseline; >=0). Mesmo o
	// piso é LIMITADO pelo headroom: sob headroom nulo o alvo é 0 (fail-closed).
	MinReplicas int
	// MaxReplicas é o tecto absoluto de réplicas (>=1). Um SLI/headroom errado nunca
	// faz o alvo crescer para lá deste limite.
	MaxReplicas int
	// TargetQueueDepthPerReplica é a profundidade de fila que UMA réplica drena (>=1):
	// a procura cresce como ceil(queue_depth / este valor). Menor ⇒ escala mais cedo.
	TargetQueueDepthPerReplica int
	// P95WaitTarget é o SLO de latência de wait acima do qual a fila a crescer
	// justifica scale-out (>0). Abaixo dele, mantém-se o piso — o scale-out exige que
	// a profundidade E o p95 subam (AC1).
	P95WaitTarget time.Duration
	// CostPerReplicaTokens é o custo estimado em tokens que cada réplica consome do
	// headroom (>=1). Deriva o tecto de headroom via [deriveMaxSpawn] (ADR-008): o
	// alvo nunca ultrapassa o max_spawn real.
	CostPerReplicaTokens int64
}

// normalized devolve a config com os valores fixados a gamas seguras (fail-safe: um
// zero-value nunca provoca divisão por zero nem crescimento ilimitado).
func (c ReplicaScalerConfig) normalized() ReplicaScalerConfig {
	if c.MinReplicas < 0 {
		c.MinReplicas = 0
	}
	if c.MaxReplicas < 1 {
		c.MaxReplicas = 1
	}
	if c.MinReplicas > c.MaxReplicas {
		c.MinReplicas = c.MaxReplicas
	}
	if c.TargetQueueDepthPerReplica < 1 {
		c.TargetQueueDepthPerReplica = 1
	}
	if c.CostPerReplicaTokens < 1 {
		c.CostPerReplicaTokens = 1
	}
	if c.P95WaitTarget < 0 {
		c.P95WaitTarget = 0
	}
	return c
}

// DefaultReplicaScalerConfig devolve parâmetros de escala sãos (baseline conservador).
func DefaultReplicaScalerConfig() ReplicaScalerConfig {
	return ReplicaScalerConfig{
		MinReplicas:                1,
		MaxReplicas:                32,
		TargetQueueDepthPerReplica: 16,
		P95WaitTarget:              250 * time.Millisecond,
		CostPerReplicaTokens:       1000,
	}
}

// deriveDesiredReplicas é a FÓRMULA PURA do alvo de réplicas de worker a partir dos
// SLIs (profundidade de fila + p95 de wait) e do headroom. É análoga a
// [deriveMaxSpawn]/DefaultPoolSizer (AOS-028/103) e DETERMINÍSTICA (sem relógio nem
// estado). Propriedades (verificadas em teste):
//
//   - CRESCE quando a profundidade de fila E o p95 de wait sobem E HÁ headroom (AC1):
//     abaixo do alvo de p95 mantém-se o piso; acima, a procura cresce com a fila;
//   - LIMITADA pelo headroom (ADR-008): nunca ultrapassa o max_spawn real
//     ([deriveMaxSpawn]) — mesmo o piso é fixado ao headroom disponível;
//   - MONÓTONA não-decrescente em cada input (fila, p95, headroom);
//   - 0-CRESCIMENTO sob headroom nulo (fail-closed): max_spawn=0 ⇒ alvo 0 (não se
//     escala para lá do headroom — em vez disso entra a escada de degradação);
//   - fixada ao tecto absoluto (MaxReplicas): um SLI/headroom errado nunca a faz
//     crescer para lá do limite físico.
func deriveDesiredReplicas(queueDepth int, p95WaitNanos int64, headroom HeadroomSnapshot, cfg ReplicaScalerConfig) int {
	cfg = cfg.normalized()

	// Tecto de headroom (ADR-008): o alvo nunca ultrapassa o max_spawn real. Sob
	// headroom nulo é 0 — fail-closed: não se escala, degrada-se.
	headroomCap := deriveMaxSpawn(headroom.Tokens, headroom.Requests, cfg.CostPerReplicaTokens)
	if headroomCap <= 0 {
		return 0
	}

	desired := cfg.MinReplicas
	// Scale-out só quando AMBOS os SLIs indicam pressão: a fila cresceu E o p95 de
	// wait excedeu o alvo (AC1). A procura cresce monotonamente com a profundidade.
	if queueDepth > 0 && p95WaitNanos > cfg.P95WaitTarget.Nanoseconds() {
		demand := ceilDiv(queueDepth, cfg.TargetQueueDepthPerReplica)
		desired = cfg.MinReplicas + demand
	}

	// Fixa ao tecto absoluto e ao headroom (ADR-008). O min domina — o headroom
	// limita até o piso.
	if desired > cfg.MaxReplicas {
		desired = cfg.MaxReplicas
	}
	if desired > headroomCap {
		desired = headroomCap
	}
	if desired < 0 {
		desired = 0
	}
	return desired
}

// ceilDiv devolve ceil(a/b) para a>=0, b>=1 (aritmética inteira, sem float).
func ceilDiv(a, b int) int {
	if b < 1 {
		b = 1
	}
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// ---------------------------------------------------------------------------
// WaitP95Recorder — o SLI de p95 de wait/despacho (núcleo NOVO de AC1).
// ---------------------------------------------------------------------------

// DefaultWaitP95Window é a janela deslizante por omissão do [WaitP95Recorder].
const DefaultWaitP95Window = 256

// DefaultWaitP95Percentile é o percentil por omissão (p95).
const DefaultWaitP95Percentile = 0.95

// WaitP95Recorder é o AGREGADOR do SLI de p95 do TEMPO DE ESPERA/DESPACHO do
// scheduler, sobre os WaitMs do [Dispatcher] (AOS-032). Percentil nearest-rank
// (molde do ColdStartRecorder de AOS-103) sobre uma janela deslizante. É
// determinístico (a decisão não usa relógio) e concorrente-seguro. Construir com
// [NewWaitP95Recorder].
type WaitP95Recorder struct {
	mu         sync.Mutex
	window     int
	percentile float64
	samples    []int64 // wait em nanos (janela deslizante FIFO)
	metrics    *SchedulerMetrics
	extra      []Attr
}

// WaitP95Option configura o [WaitP95Recorder].
type WaitP95Option func(*WaitP95Recorder)

// WithWaitP95Window fixa o tamanho da janela deslizante (>=1). Valores < 1 ignorados.
func WithWaitP95Window(n int) WaitP95Option {
	return func(r *WaitP95Recorder) {
		if n >= 1 {
			r.window = n
		}
	}
}

// WithWaitP95Percentile fixa o percentil ∈ (0,1] (default p95). Fora de gama ignorado.
func WithWaitP95Percentile(p float64) WaitP95Option {
	return func(r *WaitP95Recorder) {
		if p > 0 && p <= 1 {
			r.percentile = p
		}
	}
}

// WithWaitP95Meter ACOPLA o recorder a uma [Meter] (AOS-034): cada [WaitP95Recorder.Observe]
// emite o gauge [MetricDispatchWaitP95] com o p95 corrente. Aditivo, nil-safe: sem
// meter, o recorder só agrega (introspecção por [WaitP95Recorder.P95]).
func WithWaitP95Meter(m Meter, extra ...Attr) WaitP95Option {
	return func(r *WaitP95Recorder) {
		if m != nil {
			r.metrics = NewSchedulerMetrics(m)
			r.extra = append([]Attr(nil), extra...)
		}
	}
}

// NewWaitP95Recorder constrói o recorder de p95 de wait.
func NewWaitP95Recorder(opts ...WaitP95Option) *WaitP95Recorder {
	r := &WaitP95Recorder{
		window:     DefaultWaitP95Window,
		percentile: DefaultWaitP95Percentile,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Observe regista uma amostra de tempo de espera (em nanos) na janela deslizante e,
// se houver meter acoplado, emite o gauge de p95. Amostras negativas são fixadas a 0.
func (r *WaitP95Recorder) Observe(ctx context.Context, wait time.Duration) {
	nanos := wait.Nanoseconds()
	if nanos < 0 {
		nanos = 0
	}
	r.mu.Lock()
	r.samples = append(r.samples, nanos)
	if len(r.samples) > r.window {
		// Descarta as mais antigas (janela deslizante FIFO).
		r.samples = r.samples[len(r.samples)-r.window:]
	}
	p95 := percentileNearestRank(r.samples, r.percentile)
	r.mu.Unlock()

	if r.metrics != nil {
		r.metrics.RecordWaitP95(ctx, float64(time.Duration(p95).Milliseconds()), r.extra...)
	}
}

// ObserveDispatch é a conveniência que regista o WaitMs de um [DispatchResult]
// despachado (no-op se não houve despacho). Liga o recorder ao [Dispatcher] sem o
// acoplar a este ficheiro.
func (r *WaitP95Recorder) ObserveDispatch(ctx context.Context, res DispatchResult) {
	if !res.Dispatched {
		return
	}
	r.Observe(ctx, time.Duration(res.WaitMs)*time.Millisecond)
}

// P95 devolve o p95 corrente do tempo de espera (0 sem amostras). Leitura pura.
func (r *WaitP95Recorder) P95() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return time.Duration(percentileNearestRank(r.samples, r.percentile))
}

// Samples devolve o nº de amostras na janela (observabilidade/teste).
func (r *WaitP95Recorder) Samples() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.samples)
}

// percentileNearestRank calcula o percentil p (fracção (0,1]) sobre nanos, pelo
// método nearest-rank: ordena uma cópia e devolve o valor no rank ceil(p*n). Devolve
// 0 para amostra vazia. Não muta a entrada.
func percentileNearestRank(samples []int64, p float64) int64 {
	n := len(samples)
	if n == 0 {
		return 0
	}
	cp := make([]int64, n)
	copy(cp, samples)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	// rank = ceil(p*n), fixado a [1,n]. Aritmética inteira sem float na fronteira.
	rank := int((p*float64(n))-1e-9) + 1
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return cp[rank-1]
}

// ---------------------------------------------------------------------------
// Sinal de escala — ReplicaTarget + ReplicaSink + ReplicaCountSource (porta/ápice).
// ---------------------------------------------------------------------------

// ReplicaTarget é o SINAL de escala emitido pelo [HorizontalScaler]: quantas réplicas
// de worker o plano de dados deve ter. A APLICAÇÃO real (arrancar/parar processos
// Worker) vive no composition root (ápice, diferido) — o Assigner de AOS-099 garante
// que réplicas novas assumem partições sem rebalancing disruptivo.
type ReplicaTarget struct {
	// Desired é o alvo de réplicas derivado dos SLIs e limitado pelo headroom.
	Desired int
	// Reason é o motivo estável da decisão (scale_out|scale_in|steady|headroom_exhausted).
	Reason string
}

// ReplicaSink é a PORTA por onde o sinal de escala sai do scheduler para o ápice. É
// OPCIONAL: sem sink, o [HorizontalScaler] apenas calcula/observa o alvo (útil em
// testes puros).
type ReplicaSink interface {
	// SetReplicaTarget recebe o alvo de réplicas. Um erro é PROPAGADO (fail-closed:
	// o Tick reporta a falha de entrega do sinal).
	SetReplicaTarget(ctx context.Context, target ReplicaTarget) error
}

// ReplicaCountSource é a PORTA OPCIONAL que reporta as réplicas de worker CORRENTES
// (para o gap desejado-vs-actual). Sem ela, o actual é 0.
type ReplicaCountSource interface {
	ReplicaCount(ctx context.Context) int
}

// BacklogSource é a PORTA OPCIONAL que fornece os itens a degradar quando o headroom
// se esgota (ex.: o backlog corrente das filas). Sem ela, o [HorizontalScaler]
// SELECCIONA e observa o degrau activo (via [PolicyEngine]) mas não executa a
// degradação por-item — a execução por-item vive no caminho de enqueue das filas.
type BacklogSource interface {
	// PendingItems devolve os itens actualmente sujeitos a degradação. Deve ser
	// determinística (ordem estável) para replay.
	PendingItems(ctx context.Context) []DegradationItem
}

// ---------------------------------------------------------------------------
// HorizontalScaler — o orquestrador Tick(ctx) (núcleo de AC1/AC4/AC5/AC6).
// ---------------------------------------------------------------------------

// DefaultScaleInterval é o intervalo por omissão do laço [HorizontalScaler.Run].
const DefaultScaleInterval = 2 * time.Second

// HorizontalScaler é o orquestrador determinista da escala horizontal + degradação
// global. A cada [HorizontalScaler.Tick] lê os SLIs (profundidade de fila via
// [PartitionedQueues.Snapshot], p95 de wait via [WaitP95Recorder], headroom via
// [HeadroomController.Headroom]) e:
//
//   - COM headroom: calcula desiredReplicas ([deriveDesiredReplicas], limitado pelo
//     headroom) e emite o [ReplicaTarget] no [ReplicaSink] (scale-out); se vinha de
//     degradação, NORMALIZA (reverte downgrades via [Degrader.Normalize]);
//   - SEM headroom (esgotado): conduz a ESCADA de degradação GLOBAL na ordem
//     shed→defer→downgrade→reject — [PolicyEngine.Select] escolhe o degrau conforme a
//     pressão agregada (declarativo) e [Degrader.ExecuteChain] executa-o por-item a
//     partir desse degrau (fail-closed); NUNCA deixa a fila crescer ilimitada (a
//     escada + o backpressure das filas limitam-na — AC6).
//
// É seguro para uso concorrente. Construir com [NewHorizontalScaler].
type HorizontalScaler struct {
	key      ProviderKey
	tenant   string
	headroom HeadroomController
	queues   *PartitionedQueues
	wait     *WaitP95Recorder
	policy   *PolicyEngine // opcional: selecciona o degrau da escada
	degrader *Degrader     // opcional: executa a escada por-item
	backlog  BacklogSource // opcional: itens a degradar
	sink     ReplicaSink   // opcional: recebe o alvo de réplicas
	actual   ReplicaCountSource
	metrics  *SchedulerMetrics
	tracer   agentruntime.Tracer
	cfg      ReplicaScalerConfig

	mu          sync.Mutex
	degradedNow bool // se o último Tick entrou na escada (para Normalize ao recuperar)
	lastDesired int
	haveLast    bool
}

// HorizontalScalerOption configura o [HorizontalScaler].
type HorizontalScalerOption func(*HorizontalScaler)

// WithScalePolicy injecta o motor de política declarativa (AOS-030) que SELECCIONA o
// degrau da escada conforme a pressão agregada.
func WithScalePolicy(p *PolicyEngine) HorizontalScalerOption {
	return func(s *HorizontalScaler) {
		if p != nil {
			s.policy = p
		}
	}
}

// WithScaleDegrader injecta o executor da escada (AOS-031). Sem backlog acoplado, o
// degrader ainda serve a NORMALIZAÇÃO (reversão de downgrades ao recuperar headroom).
func WithScaleDegrader(d *Degrader) HorizontalScalerOption {
	return func(s *HorizontalScaler) {
		if d != nil {
			s.degrader = d
		}
	}
}

// WithScaleBacklog injecta a fonte de itens a degradar por-item quando o headroom se
// esgota. Exige um degrader acoplado ([WithScaleDegrader]) para ter efeito.
func WithScaleBacklog(b BacklogSource) HorizontalScalerOption {
	return func(s *HorizontalScaler) {
		if b != nil {
			s.backlog = b
		}
	}
}

// WithReplicaSink injecta a porta que recebe o sinal de escala (o alvo de réplicas).
func WithReplicaSink(sink ReplicaSink) HorizontalScalerOption {
	return func(s *HorizontalScaler) {
		if sink != nil {
			s.sink = sink
		}
	}
}

// WithReplicaCountSource injecta a porta que reporta as réplicas correntes.
func WithReplicaCountSource(src ReplicaCountSource) HorizontalScalerOption {
	return func(s *HorizontalScaler) {
		if src != nil {
			s.actual = src
		}
	}
}

// WithScaleMeter ACOPLA o scaler a uma [Meter] (AOS-034): emite os gauges de
// desired/actual réplicas e do degrau de degradação corrente (AC5). Aditivo, nil-safe.
func WithScaleMeter(m Meter) HorizontalScalerOption {
	return func(s *HorizontalScaler) {
		if m != nil {
			s.metrics = NewSchedulerMetrics(m)
		}
	}
}

// WithScaleTracer injecta a porta OTel (span por Tick). Zero-dep.
func WithScaleTracer(t agentruntime.Tracer) HorizontalScalerOption {
	return func(s *HorizontalScaler) {
		if t != nil {
			s.tracer = t
		}
	}
}

// WithScaleTenant restringe os SLIs de headroom a um tenant (o global domina sempre).
func WithScaleTenant(tenant string) HorizontalScalerOption {
	return func(s *HorizontalScaler) { s.tenant = tenant }
}

// NewHorizontalScaler constrói o orquestrador. headroom (SLI de headroom), queues
// (SLI de profundidade) e wait (SLI de p95) são OBRIGATÓRIOS — a sua ausência é
// fail-closed. A config é normalizada.
func NewHorizontalScaler(key ProviderKey, headroom HeadroomController, queues *PartitionedQueues, wait *WaitP95Recorder, cfg ReplicaScalerConfig, opts ...HorizontalScalerOption) (*HorizontalScaler, error) {
	if headroom == nil {
		return nil, ErrSpawnCoordinatorDeps
	}
	if queues == nil || wait == nil {
		return nil, ErrScalerDeps
	}
	s := &HorizontalScaler{
		key:      key,
		headroom: headroom,
		queues:   queues,
		wait:     wait,
		tracer:   agentruntime.NoopTracer{},
		cfg:      cfg.normalized(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// ScaleDecision é o veredicto de um [HorizontalScaler.Tick] — o wide event da decisão
// de escala/degradação, observável e reconstruível.
type ScaleDecision struct {
	// SLIs observados.
	QueueDepth int
	P95Wait    time.Duration
	Headroom   HeadroomSnapshot
	// MaxSpawn é o max_spawn derivado do headroom (ADR-008): o tecto do alvo.
	MaxSpawn int

	// Scale-out (quando há headroom).
	DesiredReplicas int
	ActualReplicas  int
	ScaleReason     string
	TargetEmitted   bool

	// Degradação global (quando o headroom se esgotou).
	Degraded         bool
	DegradationLevel int // 0=nenhum..4=reject
	Action           DegradationAction
	PolicyVersion    string
	// Results são os resultados da escada executada por-item (com backlog acoplado).
	Results []DegradationResult
	// Normalized indica que este Tick reverteu degradações ao recuperar headroom.
	Normalized bool
}

// Tick é a unidade DETERMINISTA da escala/degradação (testável sem relógio real). Lê
// os SLIs uma vez e decide. Propaga erros de leitura de headroom/execução SEM tocar no
// pool nem emitir um alvo inconsistente. Devolve a [ScaleDecision].
func (s *HorizontalScaler) Tick(ctx context.Context) (ScaleDecision, error) {
	if err := ctx.Err(); err != nil {
		return ScaleDecision{}, err
	}

	ctx, span := s.tracer.StartSpan(ctx, opScaleTick)
	defer span.End()

	// SLIs: headroom (fonte de verdade do enforcement), profundidade de fila, p95 wait.
	snap, err := s.headroom.Headroom(ctx, s.key, s.tenant)
	if err != nil {
		return ScaleDecision{}, err
	}
	depth, worst := s.aggregateQueues()
	p95 := s.wait.P95()
	maxSpawn := deriveMaxSpawn(snap.Tokens, snap.Requests, s.cfg.CostPerReplicaTokens)

	actual := 0
	if s.actual != nil {
		actual = s.actual.ReplicaCount(ctx)
	}

	dec := ScaleDecision{
		QueueDepth:     depth,
		P95Wait:        p95,
		Headroom:       snap,
		MaxSpawn:       maxSpawn,
		ActualReplicas: actual,
	}

	span.SetAttribute(attrScaleQueueDepth, int64(depth))
	span.SetAttribute(attrScaleWaitP95Ms, p95.Milliseconds())
	span.SetAttribute(attrScaleHeadroomT, snap.Tokens)
	span.SetAttribute(attrScaleMaxSpawn, int64(maxSpawn))
	span.SetAttribute(attrScaleActual, int64(actual))

	if maxSpawn > 0 {
		// COM headroom: scale-out dirigido por SLIs, limitado pelo headroom (AC1/AC3).
		if err := s.scaleOut(ctx, &dec, span); err != nil {
			return dec, err
		}
	} else {
		// SEM headroom (esgotado): escada de degradação global (AC4/AC6).
		if err := s.degrade(ctx, &dec, worst, span); err != nil {
			return dec, err
		}
	}

	// Métricas observáveis (AC5): desired/actual + degrau de degradação corrente.
	if s.metrics != nil {
		s.metrics.RecordDesiredReplicas(ctx, s.key, s.tenant, dec.DesiredReplicas, dec.ScaleReason)
		s.metrics.RecordActualReplicas(ctx, s.key, s.tenant, dec.ActualReplicas)
		s.metrics.RecordDegradationLevel(ctx, dec.DegradationLevel, dec.Action, s.tenant)
	}
	return dec, nil
}

// scaleOut executa o caminho COM headroom: deriva o alvo de réplicas, emite o sinal e
// normaliza a degradação se vinha de um período de esgotamento. Requer maxSpawn>0.
func (s *HorizontalScaler) scaleOut(ctx context.Context, dec *ScaleDecision, span agentruntime.Span) error {
	desired := deriveDesiredReplicas(dec.QueueDepth, dec.P95Wait.Nanoseconds(), dec.Headroom, s.cfg)
	dec.DesiredReplicas = desired
	dec.DegradationLevel = 0
	dec.Action = ""

	// Se vínhamos de degradação, normaliza (reverte downgrades) — a carga baixou.
	s.mu.Lock()
	wasDegraded := s.degradedNow
	prevDesired := s.lastDesired
	hadLast := s.haveLast
	s.degradedNow = false
	s.lastDesired = desired
	s.haveLast = true
	s.mu.Unlock()

	if wasDegraded && s.degrader != nil {
		if _, err := s.degrader.Normalize(ctx, "headroom_recovered"); err != nil {
			return err
		}
		dec.Normalized = true
	}

	// Motivo estável da decisão (direcção do sinal).
	reason := ScaleReasonSteady
	if !hadLast || desired > prevDesired {
		reason = ScaleReasonScaleOut
	} else if desired < prevDesired {
		reason = ScaleReasonScaleIn
	}
	dec.ScaleReason = reason

	span.SetAttribute(attrScaleDesired, int64(desired))
	span.SetAttribute(attrScaleDegraded, false)
	span.SetAttribute(attrScaleReasonAttr, reason)

	// Emite o SINAL de escala (a aplicação real é do ápice).
	if s.sink != nil {
		if err := s.sink.SetReplicaTarget(ctx, ReplicaTarget{Desired: desired, Reason: reason}); err != nil {
			return err
		}
		dec.TargetEmitted = true
	}
	return nil
}

// degrade executa o caminho SEM headroom: conduz a escada global. Selecciona o degrau
// via a política (declarativa) conforme a pressão agregada e executa-o por-item via o
// degrader (fail-closed). NÃO emite scale-out — mantém as réplicas correntes (o alvo é
// o actual: não se escala para lá do headroom, mas também não se colapsa a zero).
func (s *HorizontalScaler) degrade(ctx context.Context, dec *ScaleDecision, worst SaturationCondition, span agentruntime.Span) error {
	dec.Degraded = true
	dec.ScaleReason = ScaleReasonHeadroomExhausted
	// Mantém as réplicas correntes (hold): sem headroom não se escala, mas não se
	// colapsa para zero — a resposta é degradar, não desligar o plano de dados.
	dec.DesiredReplicas = dec.ActualReplicas

	s.mu.Lock()
	s.degradedNow = true
	s.lastDesired = dec.DesiredReplicas
	s.haveLast = true
	s.mu.Unlock()

	// SELECÇÃO do degrau conforme a pressão agregada (declarativo, AOS-030). Sem
	// política acoplada, o degrau canónico de arranque é o topo da escada (shed).
	action := ActionShed
	version := ""
	if s.policy != nil {
		a, v, err := s.policy.Select(ctx, worst)
		if err != nil {
			return err
		}
		action, version = a, v
	}
	dec.Action = action
	dec.PolicyVersion = version
	dec.DegradationLevel = degradationLevel(action)

	span.SetAttribute(attrScaleDesired, int64(dec.DesiredReplicas))
	span.SetAttribute(attrScaleDegraded, true)
	span.SetAttribute(attrScaleDegLevel, int64(dec.DegradationLevel))
	span.SetAttribute(attrScaleDegAction, string(action))
	span.SetAttribute(attrScaleReasonAttr, ScaleReasonHeadroomExhausted)
	span.SetAttribute(attrScalePolicyVersn, version)

	// EXECUÇÃO por-item da escada (com backlog + degrader acoplados). A partir do
	// degrau SELECCIONADO, [Degrader.ExecuteChain] aplica o primeiro degrau APLICÁVEL
	// ao item (fail-closed: reject é o terminal). Isto substitui a acumulação ilimitada
	// de fila por uma resposta previsível (AC6) — cada item é descartado/adiado/
	// degradado/rejeitado, nunca deixado a crescer.
	if s.degrader != nil && s.backlog != nil {
		order := preferenceOrderFrom(action)
		trigger := TriggerFromCondition(worst, version, "headroom_exhausted")
		for _, item := range s.backlog.PendingItems(ctx) {
			res, err := s.degrader.ExecuteChain(ctx, item, trigger, order)
			// ErrWorkRejected/ErrChainExhausted são o resultado ESPERADO do degrau
			// terminal (fail-closed com sinal), não uma falha do Tick: regista o
			// resultado e continua. Um erro de INFRAESTRUTURA (ex.: Event Store)
			// propaga.
			if err != nil && !isTerminalDegradation(err) {
				return err
			}
			dec.Results = append(dec.Results, res)
		}
	}
	return nil
}

// aggregateQueues devolve a profundidade TOTAL das filas e a [SaturationCondition] da
// partição mais pressionada (maior fill ratio; desempate por partição para
// determinismo). O snapshot já vem ordenado por partição.
func (s *HorizontalScaler) aggregateQueues() (int, SaturationCondition) {
	total := 0
	var worst SaturationCondition
	worstFill := -1.0
	for _, qs := range s.queues.Snapshot() {
		total += qs.Depth
		fill := 0.0
		if qs.Capacity > 0 {
			fill = float64(qs.Depth) / float64(qs.Capacity)
		}
		if fill > worstFill {
			worstFill = fill
			worst = SaturationCondition{
				Tenant:    qs.Tenant,
				Priority:  qs.Priority,
				Depth:     qs.Depth,
				Capacity:  qs.Capacity,
				FillRatio: fill,
				OldestAge: time.Duration(qs.OldestAgeMs) * time.Millisecond,
				Saturated: qs.Saturated,
			}
		}
	}
	return total, worst
}

// Run corre o laço de escala/degradação até o ctx terminar. Avalia imediatamente e
// depois a cada `interval` (<=0 herda [DefaultScaleInterval]). Um erro de Tick NÃO
// aborta o laço — mantém-se a última decisão e re-tenta no tick seguinte (a fonte de
// verdade do enforcement é o admission control; a escala é uma resposta). Devolve o
// motivo de cancelamento do ctx.
func (s *HorizontalScaler) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = DefaultScaleInterval
	}
	_, _ = s.Tick(ctx)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			_, _ = s.Tick(ctx)
		}
	}
}

// degradationLevel mapeia uma acção da escada para o seu DEGRAU numérico (0=nenhum,
// 1=shed, 2=defer, 3=downgrade, 4=reject). É o eixo do gauge [MetricDegradationLevel]
// e reflecte a subida da pressão pela ordem canónica.
func degradationLevel(a DegradationAction) int {
	switch a {
	case ActionShed:
		return 1
	case ActionDefer:
		return 2
	case ActionDowngrade:
		return 3
	case ActionReject:
		return 4
	default:
		return 0
	}
}

// isTerminalDegradation reconhece o resultado ESPERADO do degrau terminal da escada
// (reject/cadeia esgotada): é a resposta fail-closed com sinal ao utilizador, não uma
// falha do Tick. Um erro de INFRAESTRUTURA (Event Store, política) NÃO casa aqui e
// propaga.
func isTerminalDegradation(err error) bool {
	return errors.Is(err, ErrWorkRejected) || errors.Is(err, ErrChainExhausted)
}

// preferenceOrderFrom devolve a ordem de preferência canónica ([DefaultPreferenceOrder])
// a partir do degrau seleccionado (inclusive): a pressão agregada determina o PONTO DE
// ENTRADA na escada, e o [Degrader.ExecuteChain] escala a partir daí. Se a acção não
// pertencer à ordem canónica, devolve a ordem completa (rede de segurança).
func preferenceOrderFrom(a DegradationAction) []DegradationAction {
	for i, act := range DefaultPreferenceOrder {
		if act == a {
			return DefaultPreferenceOrder[i:]
		}
	}
	return DefaultPreferenceOrder
}
