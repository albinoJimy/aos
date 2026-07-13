// metrics.go — MÉTRICAS de saturação e reserva de headroom (AOS-034).
//
// Este é o ÚLTIMO ticket do EPIC-03: fecha o ciclo de controlo instrumentando o
// plano de controlo com métricas OTel de SATURAÇÃO (profundidade/idade de fila,
// defer-rate, taxa de degradação, backpressure) e de HEADROOM (headroom livre por
// provider:model:region, reservas activas, utilização TPM/RPM, spawns adiados).
//
// PORTA METER ZERO-DEP. À imagem de [agentruntime.Tracer]/[agentruntime.Span]
// (spans OTel GenAI sem SDK), definimos aqui uma PORTA [Meter] própria com
// Counter/Gauge/Histogram. NÃO puxamos o OTel SDK (go.opentelemetry.io/otel) —
// isso é EPIC-08. Os NOMES e ATRIBUTOS são OTel-ESTÁVEIS: o adaptador OTel real
// mapeia estas strings para instrument.Name/attribute.Key sem renomear. O default
// é [NoopMeter] (sem observabilidade); [RecordingMeter] capta tudo para asserção
// em teste E é o substrato dos WIDE EVENTS.
//
// WIDE EVENTS (capturar tudo, filtrar no query-time). O [RecordingMeter] NUNCA
// filtra no emit-time: cada medição é guardada com TODOS os seus atributos, de
// alta cardinalidade. A filtragem é uma operação de QUERY-TIME ([RecordingMeter.Query]
// / [FilterByAttr]). Prova concreta (teste): um atributo NÃO usado como filtro no
// emit continua disponível para query — nada se perde no emit-time.
//
// INSTRUMENTAÇÃO ADITIVA. As fontes (admission/filas/degradação/spawn) recebem uma
// opção With*Meter; sem ela, o comportamento é bit-a-bit o dos AOS-027..033
// (nenhum teste existente muda). A emissão é sempre nil-safe: um [SchedulerMetrics]
// nil é um no-op.
//
// SEM SEGREDOS. Os atributos expõem apenas dimensões operacionais (provider,
// model, region, tenant, partition, acção) — NUNCA tokens, chaves ou PII (ADR-010).
package scheduler

import (
	"context"
	"sort"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------
// Nomes de métrica OTel-ESTÁVEIS (contrato observável — não renomear).
// ---------------------------------------------------------------------------

const (
	// --- SATURAÇÃO ---

	// MetricAdmitted (counter) — admissões concedidas. Denominador do defer-rate.
	MetricAdmitted = "aos.scheduler.admission.admitted"
	// MetricDeferred (counter) — admissões ADIADAS (defer). Numerador do defer-rate.
	// Atributo aos.scheduler.defer_reason: no_headroom|backpressure|unsatisfiable.
	MetricDeferred = "aos.scheduler.admission.deferred"
	// MetricDegradation (counter) — acções de degradação executadas (shed/defer/
	// downgrade/reject/tier_restored). Atributo aos.scheduler.action.
	MetricDegradation = "aos.scheduler.degradation.actions"
	// MetricSpawnDeferred (counter) — spawns adiados por falta de headroom (AOS-028).
	MetricSpawnDeferred = "aos.scheduler.spawn.deferred"
	// MetricQueueDepth (gauge) — profundidade de fila por partição.
	MetricQueueDepth = "aos.scheduler.queue.depth"
	// MetricQueueOldestAge (gauge, ms) — idade do item mais antigo por partição.
	MetricQueueOldestAge = "aos.scheduler.queue.oldest_age_ms"
	// MetricQueueSaturated (gauge, 0|1) — backpressure ACTIVO por partição.
	MetricQueueSaturated = "aos.scheduler.queue.saturated"

	// --- HEADROOM ---

	// MetricHeadroomFreeTokens (gauge) — headroom LIVRE em tokens por
	// provider:model:region (e tenant, se restrito).
	MetricHeadroomFreeTokens = "aos.scheduler.headroom.free_tokens"
	// MetricHeadroomFreeRequests (gauge) — headroom LIVRE em requests.
	MetricHeadroomFreeRequests = "aos.scheduler.headroom.free_requests"
	// MetricHeadroomReservedTokens (gauge) — reservas ACTIVAS em tokens
	// (limite − livre).
	MetricHeadroomReservedTokens = "aos.scheduler.headroom.reserved_tokens"
	// MetricHeadroomUtilization (gauge, 0..1) — utilização do TPM/RPM real
	// (reservado/limite). Atributo aos.scheduler.dimension: tokens|requests.
	MetricHeadroomUtilization = "aos.scheduler.headroom.utilization"
)

// ---------------------------------------------------------------------------
// Atributos ESTÁVEIS (OTel attribute keys). Sem segredos.
// ---------------------------------------------------------------------------

const (
	// AttrMetricKey — provider:model:region (chave do bucket).
	AttrMetricKey = "aos.scheduler.provider_key"
	// AttrMetricProvider/Model/Region — dimensões decompostas da chave.
	AttrMetricProvider = "aos.scheduler.provider"
	AttrMetricModel    = "aos.scheduler.model"
	AttrMetricRegion   = "aos.scheduler.region"
	// AttrMetricTenant — dimensão de partição por tenant (quota multidimensional).
	AttrMetricTenant = "aos.scheduler.tenant"
	// AttrMetricPartition — chave de partição tenant:priority das filas.
	AttrMetricPartition = "aos.scheduler.partition"
	// AttrMetricPriority — classe de prioridade da partição.
	AttrMetricPriority = "aos.scheduler.priority"
	// AttrMetricCapacity — MaxLen da partição (tecto duro).
	AttrMetricCapacity = "aos.scheduler.capacity"
	// AttrMetricAction — acção de degradação (shed/defer/downgrade/reject/...).
	AttrMetricAction = "aos.scheduler.action"
	// AttrMetricDeferReason — motivo do defer (no_headroom|backpressure|unsatisfiable).
	AttrMetricDeferReason = "aos.scheduler.defer_reason"
	// AttrMetricDimension — tokens|requests (utilização por dimensão).
	AttrMetricDimension = "aos.scheduler.dimension"
	// AttrMetricCostMicroUSD — custo em micro-USD do pedido, emitido ONDE é
	// conhecido (ex.: spawn com sub-orçamento reservado). Torna a dimensão $ do
	// orçamento visível na observabilidade (ADR-010).
	AttrMetricCostMicroUSD = "aos.scheduler.cost_micro_usd"
)

// Motivos de defer estáveis (valores do atributo AttrMetricDeferReason).
const (
	DeferReasonNoHeadroom    = "no_headroom"
	DeferReasonBackpressure  = "backpressure"
	DeferReasonUnsatisfiable = "unsatisfiable"
)

// ---------------------------------------------------------------------------
// PORTA Meter (zero-dep) — análoga a agentruntime.Tracer.
// ---------------------------------------------------------------------------

// Attr é um par chave/valor de atributo de métrica. A ordem em que os atributos
// são passados é preservada; a identidade canónica de série ordena por chave
// (serialização estável para replay/agregação determinística).
type Attr struct {
	Key   string
	Value any
}

// Counter é um instrumento monotónico (acumula). Sync (event-driven).
type Counter interface {
	Add(ctx context.Context, value float64, attrs ...Attr)
}

// Gauge é um instrumento de valor instantâneo (última leitura). Usado por
// amostragem (async), à imagem dos observable gauges do OTel.
type Gauge interface {
	Set(ctx context.Context, value float64, attrs ...Attr)
}

// Histogram é um instrumento de distribuição (ex.: idade de fila). Zero-dep.
type Histogram interface {
	Record(ctx context.Context, value float64, attrs ...Attr)
}

// Meter é a PORTA de métricas. Devolve instrumentos nomeados (nomes OTel
// estáveis). O SDK OTel real é EPIC-08; aqui há [NoopMeter] (default) e
// [RecordingMeter] (testes + wide events).
type Meter interface {
	Counter(name string) Counter
	Gauge(name string) Gauge
	Histogram(name string) Histogram
}

// ---------------------------------------------------------------------------
// NoopMeter — default (sem observabilidade).
// ---------------------------------------------------------------------------

// NoopMeter descarta todas as medições. É o default (sem opção With*Meter).
type NoopMeter struct{}

// Counter implementa [Meter].
func (NoopMeter) Counter(string) Counter { return noopInstrument{} }

// Gauge implementa [Meter].
func (NoopMeter) Gauge(string) Gauge { return noopInstrument{} }

// Histogram implementa [Meter].
func (NoopMeter) Histogram(string) Histogram { return noopInstrument{} }

type noopInstrument struct{}

func (noopInstrument) Add(context.Context, float64, ...Attr)    {}
func (noopInstrument) Set(context.Context, float64, ...Attr)    {}
func (noopInstrument) Record(context.Context, float64, ...Attr) {}

// ---------------------------------------------------------------------------
// RecordingMeter — capta todas as medições (asserção em teste + wide events).
// ---------------------------------------------------------------------------

// InstrumentKind distingue o tipo de instrumento de uma medição.
type InstrumentKind string

const (
	KindCounter   InstrumentKind = "counter"
	KindGauge     InstrumentKind = "gauge"
	KindHistogram InstrumentKind = "histogram"
)

// Measurement é uma medição captada — o WIDE EVENT: instrumento + tipo + valor +
// TODOS os atributos (nada filtrado no emit). Seq é a ordem de emissão (estável).
type Measurement struct {
	Seq        int
	Instrument string
	Kind       InstrumentKind
	Value      float64
	Attrs      []Attr
}

// Attr devolve o valor do atributo com a chave dada (e se existe). É a operação de
// QUERY-TIME: qualquer atributo captado no emit é recuperável, mesmo que nenhuma
// métrica o "filtre" no emit-time.
func (m Measurement) Attr(key string) (any, bool) {
	for _, a := range m.Attrs {
		if a.Key == key {
			return a.Value, true
		}
	}
	return nil, false
}

// SeriesKey é a identidade canónica de série (instrumento + atributos ORDENADOS
// por chave, excluindo o valor). Estável para agregação/replay determinístico.
func (m Measurement) SeriesKey() string {
	kv := make([]string, 0, len(m.Attrs))
	for _, a := range m.Attrs {
		kv = append(kv, a.Key+"="+attrString(a.Value))
	}
	sort.Strings(kv)
	return m.Instrument + "{" + strings.Join(kv, ",") + "}"
}

// attrString serializa um valor de atributo de forma estável (sem fmt reflect
// custoso; cobre os tipos que emitimos: string/bool/int*/float).
func attrString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int:
		return itoa(int64(t))
	case int64:
		return itoa(t)
	case float64:
		return ftoa(t)
	default:
		return "?"
	}
}

// RecordingMeter capta todas as medições num log ordenado (wide events). É seguro
// para concorrência (carga sintética com -race). Construir com [NewRecordingMeter].
type RecordingMeter struct {
	mu      sync.Mutex
	seq     int
	records []Measurement
}

// NewRecordingMeter constrói um meter de captura.
func NewRecordingMeter() *RecordingMeter { return &RecordingMeter{} }

// Counter implementa [Meter].
func (m *RecordingMeter) Counter(name string) Counter {
	return recInstrument{m: m, name: name, kind: KindCounter}
}

// Gauge implementa [Meter].
func (m *RecordingMeter) Gauge(name string) Gauge {
	return recInstrument{m: m, name: name, kind: KindGauge}
}

// Histogram implementa [Meter].
func (m *RecordingMeter) Histogram(name string) Histogram {
	return recInstrument{m: m, name: name, kind: KindHistogram}
}

// record acrescenta uma medição SEM filtrar atributos (wide event). Copia o slice
// de atributos para isolar o registo de mutações do chamador.
func (m *RecordingMeter) record(name string, kind InstrumentKind, value float64, attrs []Attr) {
	cp := make([]Attr, len(attrs))
	copy(cp, attrs)
	m.mu.Lock()
	m.seq++
	m.records = append(m.records, Measurement{
		Seq:        m.seq,
		Instrument: name,
		Kind:       kind,
		Value:      value,
		Attrs:      cp,
	})
	m.mu.Unlock()
}

// Measurements devolve uma cópia de TODAS as medições captadas, por ordem de
// emissão (estável).
func (m *RecordingMeter) Measurements() []Measurement {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Measurement, len(m.records))
	copy(out, m.records)
	return out
}

// Query devolve as medições que satisfazem o predicado — filtragem QUERY-TIME
// sobre os wide events. Nada foi descartado no emit, por isso qualquer pergunta
// nova se responde aqui sem reinstrumentar.
func (m *RecordingMeter) Query(pred func(Measurement) bool) []Measurement {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Measurement
	for _, r := range m.records {
		if pred(r) {
			out = append(out, r)
		}
	}
	return out
}

// ByInstrument devolve as medições de um instrumento (query-time).
func (m *RecordingMeter) ByInstrument(name string) []Measurement {
	return m.Query(func(r Measurement) bool { return r.Instrument == name })
}

// FilterByAttr devolve as medições cujo atributo key == value (query-time). Prova
// dos wide events: um atributo captado no emit — mesmo não sendo o "eixo" da
// métrica — filtra-se aqui.
func (m *RecordingMeter) FilterByAttr(key string, value any) []Measurement {
	want := attrString(value)
	return m.Query(func(r Measurement) bool {
		if v, ok := r.Attr(key); ok {
			return attrString(v) == want
		}
		return false
	})
}

type recInstrument struct {
	m    *RecordingMeter
	name string
	kind InstrumentKind
}

func (r recInstrument) Add(_ context.Context, value float64, attrs ...Attr) {
	r.m.record(r.name, r.kind, value, attrs)
}

func (r recInstrument) Set(_ context.Context, value float64, attrs ...Attr) {
	r.m.record(r.name, r.kind, value, attrs)
}

func (r recInstrument) Record(_ context.Context, value float64, attrs ...Attr) {
	r.m.record(r.name, r.kind, value, attrs)
}

// ---------------------------------------------------------------------------
// SchedulerMetrics — a FACHADA de emissão (nomes/atributos estáveis).
// ---------------------------------------------------------------------------

// SchedulerMetrics é a fachada tipada que emite as métricas do plano de controlo
// sobre uma [Meter]. Todos os métodos são nil-safe: um *SchedulerMetrics nil é um
// no-op (as fontes guardam-no nil por omissão — instrumentação ADITIVA). Construir
// com [NewSchedulerMetrics].
type SchedulerMetrics struct {
	admitted      Counter
	deferred      Counter
	degradation   Counter
	spawnDeferred Counter
	queueDepth    Gauge
	queueAge      Gauge
	queueSat      Gauge
	hrFreeTokens  Gauge
	hrFreeReq     Gauge
	hrReserved    Gauge
	hrUtil        Gauge
}

// NewSchedulerMetrics constrói a fachada sobre uma [Meter]. Meter nil ⇒ [NoopMeter].
func NewSchedulerMetrics(m Meter) *SchedulerMetrics {
	if m == nil {
		m = NoopMeter{}
	}
	return &SchedulerMetrics{
		admitted:      m.Counter(MetricAdmitted),
		deferred:      m.Counter(MetricDeferred),
		degradation:   m.Counter(MetricDegradation),
		spawnDeferred: m.Counter(MetricSpawnDeferred),
		queueDepth:    m.Gauge(MetricQueueDepth),
		queueAge:      m.Gauge(MetricQueueOldestAge),
		queueSat:      m.Gauge(MetricQueueSaturated),
		hrFreeTokens:  m.Gauge(MetricHeadroomFreeTokens),
		hrFreeReq:     m.Gauge(MetricHeadroomFreeRequests),
		hrReserved:    m.Gauge(MetricHeadroomReservedTokens),
		hrUtil:        m.Gauge(MetricHeadroomUtilization),
	}
}

// keyAttrs decompõe uma ProviderKey nos atributos estáveis (chave + dimensões).
func keyAttrs(key ProviderKey, tenant string) []Attr {
	attrs := []Attr{
		{Key: AttrMetricKey, Value: key.String()},
		{Key: AttrMetricProvider, Value: key.Provider},
		{Key: AttrMetricModel, Value: key.Model},
		{Key: AttrMetricRegion, Value: key.Region},
	}
	if tenant != "" {
		attrs = append(attrs, Attr{Key: AttrMetricTenant, Value: tenant})
	}
	return attrs
}

// RecordAdmitted emite o counter de admissões concedidas.
func (s *SchedulerMetrics) RecordAdmitted(ctx context.Context, key ProviderKey, tenant string, extra ...Attr) {
	if s == nil {
		return
	}
	s.admitted.Add(ctx, 1, append(keyAttrs(key, tenant), extra...)...)
}

// RecordDeferred emite o counter de admissões adiadas com o MOTIVO estável.
func (s *SchedulerMetrics) RecordDeferred(ctx context.Context, key ProviderKey, tenant, reason string, extra ...Attr) {
	if s == nil {
		return
	}
	attrs := append(keyAttrs(key, tenant), Attr{Key: AttrMetricDeferReason, Value: reason})
	s.deferred.Add(ctx, 1, append(attrs, extra...)...)
}

// RecordDegradation emite o counter de acções de degradação (com acção e
// partição). Não carrega um atributo "applied": o único call-site de produção
// emite após a persistência do evento (a acção FOI aplicada), pelo que a
// dimensão seria constante e não consultável — omite-se para não sugerir um eixo
// de query que na prática não distingue nada.
func (s *SchedulerMetrics) RecordDegradation(ctx context.Context, action DegradationAction, tenant, priority string, extra ...Attr) {
	if s == nil {
		return
	}
	attrs := []Attr{
		{Key: AttrMetricAction, Value: string(action)},
		{Key: AttrMetricTenant, Value: tenant},
		{Key: AttrMetricPriority, Value: priority},
	}
	s.degradation.Add(ctx, 1, append(attrs, extra...)...)
}

// RecordSpawnDeferred emite o counter de spawns adiados por falta de headroom.
func (s *SchedulerMetrics) RecordSpawnDeferred(ctx context.Context, key ProviderKey, tenant string, extra ...Attr) {
	if s == nil {
		return
	}
	s.spawnDeferred.Add(ctx, 1, append(keyAttrs(key, tenant), extra...)...)
}

// ObserveHeadroom emite os gauges de headroom (livre, reservado, utilização) de
// uma chave a partir de um [HeadroomSnapshot]. A utilização é emitida por
// dimensão (tokens|requests). Determinística (só o snapshot decide).
func (s *SchedulerMetrics) ObserveHeadroom(ctx context.Context, key ProviderKey, tenant string, snap HeadroomSnapshot, extra ...Attr) {
	if s == nil {
		return
	}
	base := append(keyAttrs(key, tenant), extra...)
	s.hrFreeTokens.Set(ctx, float64(snap.Tokens), base...)
	s.hrFreeReq.Set(ctx, float64(snap.Requests), base...)

	reservedTokens := snap.LimitTokens - snap.Tokens
	if reservedTokens < 0 {
		reservedTokens = 0
	}
	s.hrReserved.Set(ctx, float64(reservedTokens), base...)

	s.hrUtil.Set(ctx, utilization(snap.LimitTokens, snap.Tokens),
		append(base, Attr{Key: AttrMetricDimension, Value: "tokens"})...)
	s.hrUtil.Set(ctx, utilization(snap.LimitRequests, snap.Requests),
		append(base, Attr{Key: AttrMetricDimension, Value: "requests"})...)
}

// ObserveQueuePartition emite os gauges de saturação de uma partição a partir de
// um [QueueSnapshot].
func (s *SchedulerMetrics) ObserveQueuePartition(ctx context.Context, qs QueueSnapshot, extra ...Attr) {
	if s == nil {
		return
	}
	attrs := append([]Attr{
		{Key: AttrMetricPartition, Value: qs.Partition},
		{Key: AttrMetricTenant, Value: qs.Tenant},
		{Key: AttrMetricPriority, Value: qs.Priority},
		{Key: AttrMetricCapacity, Value: qs.Capacity},
	}, extra...)
	s.queueDepth.Set(ctx, float64(qs.Depth), attrs...)
	s.queueAge.Set(ctx, float64(qs.OldestAgeMs), attrs...)
	s.queueSat.Set(ctx, boolToF(qs.Saturated), attrs...)
}

// SampleHeadroom lê o headroom de uma chave pela porta [HeadroomController] (a
// MESMA projecção determinística que o Admit decide) e emite os gauges. É o padrão
// OTel de OBSERVABLE GAUGE (amostragem periódica), fora do caminho quente do Admit.
func (s *SchedulerMetrics) SampleHeadroom(ctx context.Context, hc HeadroomController, key ProviderKey, tenant string, extra ...Attr) error {
	if s == nil || hc == nil {
		return nil
	}
	snap, err := hc.Headroom(ctx, key, tenant)
	if err != nil {
		return err
	}
	s.ObserveHeadroom(ctx, key, tenant, snap, extra...)
	return nil
}

// SampleQueues lê o snapshot agregado das filas ([PartitionedQueues.Snapshot]) e
// emite os gauges de cada partição. Amostragem async; iteração ordenada (o
// Snapshot já vem ordenado por partição — replay determinístico).
func (s *SchedulerMetrics) SampleQueues(ctx context.Context, q *PartitionedQueues, extra ...Attr) {
	if s == nil || q == nil {
		return
	}
	for _, qs := range q.Snapshot() {
		s.ObserveQueuePartition(ctx, qs, extra...)
	}
}

// utilization = reservado/limite ∈ [0,1]. Limite <= 0 ⇒ 0 (indeterminado, não
// divide por zero).
func utilization(limit, free int64) float64 {
	if limit <= 0 {
		return 0
	}
	reserved := limit - free
	if reserved < 0 {
		reserved = 0
	}
	if reserved > limit {
		reserved = limit
	}
	return float64(reserved) / float64(limit)
}

func boolToF(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// itoa/ftoa: serialização estável mínima (evita fmt no caminho de série).
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func ftoa(f float64) string {
	// Estável e suficiente para atributos (3 casas). Evita strconv.FormatFloat com
	// notação científica variável.
	scaled := int64(f*1000 + 0.5)
	if f < 0 {
		scaled = int64(f*1000 - 0.5)
	}
	whole := scaled / 1000
	frac := scaled % 1000
	if frac < 0 {
		frac = -frac
	}
	fs := itoa(frac)
	for len(fs) < 3 {
		fs = "0" + fs
	}
	return itoa(whole) + "." + fs
}
