package sandbox

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"
)

// DefaultColdStartTarget é o alvo canónico do driver não-funcional (AOS-065): o
// tempo de disponibilização de uma sandbox pronta a executar deve manter-se
// < 125 ms (p95). Acima, o alerta dispara. É CONFIGURÁVEL via [WithColdStartTarget]
// — nunca um número mágico disperso pelo código.
const DefaultColdStartTarget = 125 * time.Millisecond

// DefaultColdStartPercentile é o percentil do SLI de cold-start (p95). O alvo é
// sobre a CAUDA, não a média — uma latência de cauda alta degrada a experiência
// interactiva mesmo com média baixa.
const DefaultColdStartPercentile = 0.95

// defaultColdStartMinSamples é o mínimo de amostras agregadas antes de o alerta ser
// avaliado (anti-flapping): uma única reserva ruidosa não deve disparar um alerta.
const defaultColdStartMinSamples = 1

// defaultColdStartWindow é o tamanho da janela deslizante de amostras por chave
// sobre a qual o percentil é calculado. Limita o crescimento de memória e mantém o
// SLI sensível à latência RECENTE (ao contrário de um cumulativo que dilui a cauda).
const defaultColdStartWindow = 4096

// Nomes e atributos das métricas OTel do SLI de cold-start. NENHUM transporta
// segredo (ADR-006): só durações e eixos não-secretos (versão de imagem, driver,
// resultado de aprovisionamento). O adaptador OTel real (EPIC-08) mapeia estes
// nomes para o SDK sem renomear.
const (
	// MetricColdStart é a métrica do tempo de disponibilização (ms). Emitida por
	// amostra (scope "sample") e como agregado p95 (scope "p95").
	MetricColdStart = "aos.sandbox.cold_start_ms"
	// MetricRestore é a métrica da duração de restore modelada (ms).
	MetricRestore = "aos.sandbox.restore_ms"
	// MetricPoolExhausted é o contador de esgotamentos observados por política.
	MetricPoolExhausted = "aos.sandbox.pool_exhausted"
	// MetricWarmReplenish é o contador de restores de reposição do warm pool — o
	// custo de restore pago FORA do caminho crítico (pré-aquecimento) após um consumo.
	// Torna honesto o SLI: sob carga 100% warm o p95 de cold_start pode ser ≈0, mas
	// este contador (+ [MetricRestore] scope "replenish") mostra que o restore real
	// não desapareceu — foi apenas deslocado para o pré-aquecimento. Um p95=0 não
	// deve ser lido como "sem custo".
	MetricWarmReplenish = "aos.sandbox.warm_replenish"

	// MetricPoolOccupancy é o SLI de OCUPAÇÃO do pool (AOS-103): a fracção de VMs
	// vivas em uso, em percentagem (100·in_use/(warm+in_use)). Gauge emitido por
	// reserva/libertação/resize. Ocupação perto de 100% sustentada = pressão sobre o
	// pool (candidato a crescer via headroom); perto de 0% = pool sobredimensionado.
	MetricPoolOccupancy = "aos.sandbox.pool_occupancy"
	// MetricPoolRecycle é o SLI de TAXA DE RECICLAGEM (AOS-103): contador cumulativo
	// de VMs recicladas (overlay efémero descartado no fim de uma execução e VM
	// destruída). O backend de séries temporais (EPIC-08) deriva a TAXA; um pico de
	// reciclagem correlaciona com rotação de carga e com o custo de reposição warm.
	MetricPoolRecycle = "aos.sandbox.pool_recycle"
	// MetricPoolResize é o gauge dos ALVOS de dimensionamento após um [Pool.Resize]
	// (AOS-103): warm/max derivados do headroom. Prova, no fluxo de métricas, que o
	// tamanho do pool NÃO é uma constante — acompanha o headroom ao longo do tempo.
	MetricPoolResize = "aos.sandbox.pool_resize"

	// AttrImageVersion — versão de imagem do snapshot base (eixo de agregação).
	AttrImageVersion = "aos.sandbox.image_version"
	// AttrProvisionOutcome — resultado do aprovisionamento (warm_hit|expanded|waited).
	AttrProvisionOutcome = "aos.sandbox.provision_outcome"
	// AttrColdStartScope — âmbito da métrica ("sample"|"p95").
	AttrColdStartScope = "aos.sandbox.cold_start_scope"
	// AttrExhaustionPolicy — política aplicada no esgotamento (wait|expand|reject).
	AttrExhaustionPolicy = "aos.sandbox.exhaustion_policy"
	// AttrColdStartMs — anotação no span com o cold-start desta reserva (ms).
	AttrColdStartMs = "aos.sandbox.cold_start_ms"
	// AttrColdStartP95Ms — anotação no span com o p95 agregado corrente (ms).
	AttrColdStartP95Ms = "aos.sandbox.cold_start_p95_ms"
	// AttrPoolWarm — VMs pré-aquecidas (na fila) no instante da amostra de pool.
	AttrPoolWarm = "aos.sandbox.pool_warm"
	// AttrPoolInUse — VMs reservadas (em uso) no instante da amostra de pool.
	AttrPoolInUse = "aos.sandbox.pool_in_use"
	// AttrPoolMax — tecto corrente de VMs vivas (o alvo max dinâmico).
	AttrPoolMax = "aos.sandbox.pool_max"
	// AttrPoolScope — âmbito da amostra de resize ("warm"|"max").
	AttrPoolScope = "aos.sandbox.pool_scope"
)

// PoolKey é a chave de agregação do SLI de cold-start: (versão de imagem, driver).
// Chaves distintas são ISOLADAS — uma versão/driver nunca contamina outra.
type PoolKey struct {
	ImageVersion ImageVersion
	Driver       DriverKind
}

// ColdStartSample é a observação de UMA reserva de sandbox para o SLI. Transporta
// a duração de disponibilização (reserva+restore modelados) e o eixo de agregação.
// NÃO transporta segredo.
type ColdStartSample struct {
	ImageVersion ImageVersion
	Driver       DriverKind
	Outcome      ProvisionOutcome
	// ColdStart é o tempo de disponibilização observado (o SLI).
	ColdStart time.Duration
	// Restore é a duração de restore modelada da VM entregue (5–30 ms). Observacional.
	Restore time.Duration
}

// ColdStartAggregate é o estado agregado de uma [PoolKey]. Obtido por cópia via
// [ColdStartRecorder.SnapshotAgg].
type ColdStartAggregate struct {
	// Samples é o número de reservas na janela corrente.
	Samples int
	// P95 é o percentil configurado sobre a janela (o SLI).
	P95 time.Duration
	// Max é o pior cold-start na janela.
	Max time.Duration
	// Exhaustions é o total de esgotamentos observados para a chave (cumulativo).
	Exhaustions int
	// WarmRestores é o total de restores de reposição do warm pool para a chave
	// (cumulativo) — o custo de restore pago off-path no pré-aquecimento. Sob carga
	// warm o p95 de cold_start pode ser ≈0; este contador prova que o restore real
	// continua a acontecer (não é "sem custo"). Ver [MetricWarmReplenish].
	WarmRestores int
	// Recycles é o total de VMs recicladas para a chave (cumulativo): cada execução
	// que termina descarta o overlay efémero e destrói a VM (AOS-103). É a base do SLI
	// de taxa de reciclagem. Ver [MetricPoolRecycle].
	Recycles int
	// Breached indica se o SLI está actualmente em incumprimento (p95 > alvo). É o
	// estado que torna o alerta anti-flapping (dispara só na transição).
	Breached bool
}

// ColdStartMetric é uma amostra de métrica OTel emitida pelo SLI. Forma mínima e
// própria (zero-dep). Value em milissegundos; Attributes leva versão/driver/
// resultado NÃO-SECRETOS.
type ColdStartMetric struct {
	Name       string
	Value      float64
	Attributes map[string]any
	Timestamp  time.Time
}

// ColdStartMetricSink é a PORTA de emissão de métricas (o pilar OTel de EPIC-08
// liga o real; aqui há [MemoryColdStartMetricSink] de referência).
type ColdStartMetricSink interface {
	Record(ctx context.Context, m ColdStartMetric)
}

// ColdStartAlert é o evento de incumprimento do SLI: o p95 agregado de uma chave
// ultrapassou o alvo. Atribuível à versão/driver (nunca segredo).
type ColdStartAlert struct {
	Key       PoolKey
	P95       time.Duration
	Target    time.Duration
	Samples   int
	Timestamp time.Time
}

// ColdStartAlertSink é a PORTA de alertas (EPIC-08 liga o alertmanager real; aqui
// há [MemoryColdStartAlertSink] de referência).
type ColdStartAlertSink interface {
	Fire(ctx context.Context, a ColdStartAlert)
}

// ColdStartReading é o resultado de [ColdStartRecorder.Observe]: o cold-start da
// reserva, o p95 agregado e se esta observação DISPAROU um alerta (transição para
// incumprimento).
type ColdStartReading struct {
	Key       PoolKey
	ColdStart time.Duration
	P95       time.Duration
	Samples   int
	Alerted   bool
}

// ColdStartRecorder é o AGREGADOR do SLI de cold-start por (versão de imagem,
// driver) — segue o molde de AOS-061 (cálculo por chamada + agregação + emissão
// OTel por porta + alerta por limiar anti-flapping). Concorrente-seguro. Construir
// com [NewColdStartRecorder].
//
// Âmbito por-processo (como AOS-061): a agregação e a decisão de alerta (minSamples
// + transição) são estado in-memory por-réplica. Sob escalonamento horizontal a
// avaliação GLOBAL de limiar é do pilar de alerta central (EPIC-08) sobre a série
// temporal partilhada; este recorder é a referência determinista por-processo.
type ColdStartRecorder struct {
	target     time.Duration
	percentile float64
	minSamples int
	window     int
	metrics    ColdStartMetricSink
	alerts     ColdStartAlertSink
	clock      func() time.Time

	mu   sync.Mutex
	aggs map[PoolKey]*coldAgg
}

// coldAgg é o estado interno mutável de uma chave.
type coldAgg struct {
	samples      []time.Duration // janela deslizante
	exhaustions  int
	warmRestores int
	recycles     int // VMs recicladas (overlay descartado no fim de execução), AOS-103
	breached     bool
}

// ColdStartOption configura o [ColdStartRecorder].
type ColdStartOption func(*ColdStartRecorder)

// WithColdStartTarget define o alvo de p95 do cold-start. Default
// [DefaultColdStartTarget] (125 ms). Valores <= 0 são ignorados.
func WithColdStartTarget(t time.Duration) ColdStartOption {
	return func(r *ColdStartRecorder) {
		if t > 0 {
			r.target = t
		}
	}
}

// WithColdStartPercentile define o percentil do SLI (fracção (0,1]). Default
// [DefaultColdStartPercentile] (0.95). Valores fora de (0,1] são ignorados.
func WithColdStartPercentile(p float64) ColdStartOption {
	return func(r *ColdStartRecorder) {
		if p > 0 && p <= 1 {
			r.percentile = p
		}
	}
}

// WithColdStartMinSamples define o mínimo de amostras antes de o alerta ser avaliado
// (anti-flapping). Default [defaultColdStartMinSamples]. Valores < 1 são ignorados.
func WithColdStartMinSamples(n int) ColdStartOption {
	return func(r *ColdStartRecorder) {
		if n >= 1 {
			r.minSamples = n
		}
	}
}

// WithColdStartWindow define o tamanho da janela deslizante de amostras. Default
// [defaultColdStartWindow]. Valores < 1 são ignorados.
func WithColdStartWindow(n int) ColdStartOption {
	return func(r *ColdStartRecorder) {
		if n >= 1 {
			r.window = n
		}
	}
}

// WithColdStartMetricSink liga a porta de métricas. Default: descarta.
func WithColdStartMetricSink(s ColdStartMetricSink) ColdStartOption {
	return func(r *ColdStartRecorder) {
		if s != nil {
			r.metrics = s
		}
	}
}

// WithColdStartAlertSink liga a porta de alertas. Default: descarta.
func WithColdStartAlertSink(s ColdStartAlertSink) ColdStartOption {
	return func(r *ColdStartRecorder) {
		if s != nil {
			r.alerts = s
		}
	}
}

// WithColdStartClock injecta o relógio determinista dos timestamps de métrica/
// alerta. Default: time.Now. Nunca usado numa DECISÃO (só carimba eventos).
func WithColdStartClock(clock func() time.Time) ColdStartOption {
	return func(r *ColdStartRecorder) {
		if clock != nil {
			r.clock = clock
		}
	}
}

// NewColdStartRecorder constrói o agregador de SLI de cold-start. Sem sinks, a
// agregação corre na mesma (introspecção por [ColdStartRecorder.SnapshotAgg]); os
// sinks são o ponto de extensão OTel/alertmanager de EPIC-08.
func NewColdStartRecorder(opts ...ColdStartOption) *ColdStartRecorder {
	r := &ColdStartRecorder{
		target:     DefaultColdStartTarget,
		percentile: DefaultColdStartPercentile,
		minSamples: defaultColdStartMinSamples,
		window:     defaultColdStartWindow,
		metrics:    nopColdMetric{},
		alerts:     nopColdAlert{},
		clock:      time.Now,
		aggs:       make(map[PoolKey]*coldAgg),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Target devolve o alvo de p95 configurado (introspecção).
func (r *ColdStartRecorder) Target() time.Duration { return r.target }

// Observe regista UMA reserva: acumula o cold-start na janela da chave, calcula o
// p95, emite a métrica OTel (amostra + p95 + restore) e avalia o alerta sobre o p95.
// Anota o span (se não-nil) com o cold-start/p95 — ligação à trajectória, sem
// segredo. Devolve a [ColdStartReading] resultante.
func (r *ColdStartRecorder) Observe(ctx context.Context, span Span, s ColdStartSample) ColdStartReading {
	key := PoolKey{ImageVersion: s.ImageVersion, Driver: s.Driver}
	now := r.clock()

	r.mu.Lock()
	agg := r.aggs[key]
	if agg == nil {
		agg = &coldAgg{}
		r.aggs[key] = agg
	}
	agg.samples = append(agg.samples, s.ColdStart)
	if len(agg.samples) > r.window {
		// Recorta para a janela (nova alocação: não segura o array de suporte antigo).
		trimmed := make([]time.Duration, r.window)
		copy(trimmed, agg.samples[len(agg.samples)-r.window:])
		agg.samples = trimmed
	}
	p95 := percentileOf(agg.samples, r.percentile)
	samples := len(agg.samples)

	// Alerta anti-flapping sobre o AGREGADO (p95): só após minSamples, dispara na
	// TRANSIÇÃO para incumprimento (healthy → breached, p95 > alvo) e re-arma quando
	// recupera (p95 <= alvo). Evita um alerta por cada reserva ruidosa e o flapping.
	alerted := false
	if samples >= r.minSamples {
		over := p95 > r.target
		if over && !agg.breached {
			agg.breached = true
			alerted = true
		} else if !over && agg.breached {
			agg.breached = false
		}
	}
	r.mu.Unlock()

	// Emissão FORA do lock (os sinks são I/O de referência).
	r.emit(ctx, key, s, p95, now)
	r.annotate(span, s.ColdStart, p95)
	if alerted {
		r.alerts.Fire(ctx, ColdStartAlert{
			Key:       key,
			P95:       p95,
			Target:    r.target,
			Samples:   samples,
			Timestamp: now,
		})
	}

	return ColdStartReading{Key: key, ColdStart: s.ColdStart, P95: p95, Samples: samples, Alerted: alerted}
}

// ObserveExhaustion regista um esgotamento de pool sob a política dada (o resultado
// observável exigido pelo DoD: esperou/expandiu/rejeitou visível). Incrementa o
// contador da chave e emite a métrica. NUNCA implica reutilização de estado sujo —
// é apenas o registo de que a política de degradação actuou.
func (r *ColdStartRecorder) ObserveExhaustion(ctx context.Context, key PoolKey, policy ExhaustionPolicy) {
	r.mu.Lock()
	agg := r.aggs[key]
	if agg == nil {
		agg = &coldAgg{}
		r.aggs[key] = agg
	}
	agg.exhaustions++
	total := agg.exhaustions
	r.mu.Unlock()

	now := r.clock()
	r.metrics.Record(ctx, ColdStartMetric{
		Name:  MetricPoolExhausted,
		Value: float64(total),
		Attributes: map[string]any{
			AttrImageVersion:     string(key.ImageVersion),
			AttrDriver:           string(key.Driver),
			AttrExhaustionPolicy: policy.String(),
		},
		Timestamp: now,
	})
}

// ObserveWarmRestore regista um restore de REPOSIÇÃO do warm pool: o custo de
// restore pago FORA do caminho crítico (pré-aquecimento) para repor uma VM consumida.
// É o SLI de depleção do warm pool que torna honesto o cold-start: sob carga 100%
// warm o p95 de cold_start pode ser ≈0, mas cada warm hit desloca um restore para
// aqui — este contador (+ a métrica de restore scope "replenish") mostra que o custo
// real não desapareceu. NUNCA transporta segredo (só durações + eixos versão/driver).
func (r *ColdStartRecorder) ObserveWarmRestore(ctx context.Context, key PoolKey, restore time.Duration) {
	r.mu.Lock()
	agg := r.aggs[key]
	if agg == nil {
		agg = &coldAgg{}
		r.aggs[key] = agg
	}
	agg.warmRestores++
	total := agg.warmRestores
	r.mu.Unlock()

	now := r.clock()
	r.metrics.Record(ctx, ColdStartMetric{
		Name:  MetricWarmReplenish,
		Value: float64(total),
		Attributes: map[string]any{
			AttrImageVersion:   string(key.ImageVersion),
			AttrDriver:         string(key.Driver),
			AttrColdStartScope: "replenish",
		},
		Timestamp: now,
	})
	if restore > 0 {
		r.metrics.Record(ctx, ColdStartMetric{
			Name:  MetricRestore,
			Value: msOf(restore),
			Attributes: map[string]any{
				AttrImageVersion:   string(key.ImageVersion),
				AttrDriver:         string(key.Driver),
				AttrColdStartScope: "replenish",
			},
			Timestamp: now,
		})
	}
}

// ObserveOccupancy emite o SLI de OCUPAÇÃO do pool (AOS-103): a fracção de VMs vivas
// em uso, em percentagem. É um GAUGE (não acumula estado): reflecte o instante da
// amostra (inUse/(warm+inUse)). Emitido por reserva/libertação/resize. Sem VMs vivas
// a ocupação é 0. NUNCA transporta segredo (só contagens + eixos versão/driver).
func (r *ColdStartRecorder) ObserveOccupancy(ctx context.Context, key PoolKey, inUse, warm, max int) {
	live := warm + inUse
	var occ float64
	if live > 0 {
		occ = 100 * float64(inUse) / float64(live)
	}
	r.metrics.Record(ctx, ColdStartMetric{
		Name:  MetricPoolOccupancy,
		Value: occ,
		Attributes: map[string]any{
			AttrImageVersion: string(key.ImageVersion),
			AttrDriver:       string(key.Driver),
			AttrPoolInUse:    inUse,
			AttrPoolWarm:     warm,
			AttrPoolMax:      max,
		},
		Timestamp: r.clock(),
	})
}

// ObserveRecycle regista a RECICLAGEM de uma VM (AOS-103): no fim de uma execução o
// overlay efémero é descartado e a VM destruída (nunca reciclada para outra execução).
// Incrementa o contador cumulativo da chave e emite a métrica; o backend de séries
// temporais (EPIC-08) deriva a TAXA de reciclagem. NUNCA transporta segredo.
func (r *ColdStartRecorder) ObserveRecycle(ctx context.Context, key PoolKey) {
	r.mu.Lock()
	agg := r.aggs[key]
	if agg == nil {
		agg = &coldAgg{}
		r.aggs[key] = agg
	}
	agg.recycles++
	total := agg.recycles
	r.mu.Unlock()

	r.metrics.Record(ctx, ColdStartMetric{
		Name:  MetricPoolRecycle,
		Value: float64(total),
		Attributes: map[string]any{
			AttrImageVersion: string(key.ImageVersion),
			AttrDriver:       string(key.Driver),
		},
		Timestamp: r.clock(),
	})
}

// ObserveResize emite os ALVOS de dimensionamento após um [Pool.Resize] (AOS-103):
// warm e max derivados do headroom. Gauge (dois pontos, scope warm/max). Torna
// visível, no fluxo de métricas, que o tamanho do pool acompanha o headroom e NÃO é
// uma constante. NUNCA transporta segredo.
func (r *ColdStartRecorder) ObserveResize(ctx context.Context, key PoolKey, warmTarget, maxTarget int) {
	now := r.clock()
	base := func(scope string) map[string]any {
		return map[string]any{
			AttrImageVersion: string(key.ImageVersion),
			AttrDriver:       string(key.Driver),
			AttrPoolScope:    scope,
		}
	}
	r.metrics.Record(ctx, ColdStartMetric{
		Name:       MetricPoolResize,
		Value:      float64(warmTarget),
		Attributes: base("warm"),
		Timestamp:  now,
	})
	r.metrics.Record(ctx, ColdStartMetric{
		Name:       MetricPoolResize,
		Value:      float64(maxTarget),
		Attributes: base("max"),
		Timestamp:  now,
	})
}

// emit emite a métrica OTel do SLI: p95 (agregado), a amostra e a duração de
// restore. Só durações + eixos NÃO-SECRETOS (ADR-006/ADR-010).
func (r *ColdStartRecorder) emit(ctx context.Context, key PoolKey, s ColdStartSample, p95 time.Duration, now time.Time) {
	base := func(scope string) map[string]any {
		return map[string]any{
			AttrImageVersion:     string(key.ImageVersion),
			AttrDriver:           string(key.Driver),
			AttrProvisionOutcome: s.Outcome.String(),
			AttrColdStartScope:   scope,
		}
	}
	r.metrics.Record(ctx, ColdStartMetric{
		Name:       MetricColdStart,
		Value:      msOf(p95),
		Attributes: base("p95"),
		Timestamp:  now,
	})
	r.metrics.Record(ctx, ColdStartMetric{
		Name:       MetricColdStart,
		Value:      msOf(s.ColdStart),
		Attributes: base("sample"),
		Timestamp:  now,
	})
	if s.Restore > 0 {
		r.metrics.Record(ctx, ColdStartMetric{
			Name:       MetricRestore,
			Value:      msOf(s.Restore),
			Attributes: base("sample"),
			Timestamp:  now,
		})
	}
}

// annotate anota o span da reserva com o cold-start/p95 (ligação à trajectória).
// Seguro com span nil. Nunca emite segredo.
func (r *ColdStartRecorder) annotate(span Span, coldStart, p95 time.Duration) {
	if span == nil {
		return
	}
	span.SetAttribute(AttrColdStartMs, msOf(coldStart))
	span.SetAttribute(AttrColdStartP95Ms, msOf(p95))
}

// SnapshotAgg devolve uma CÓPIA do agregado de uma chave (introspecção/testes). ok
// é falso se a chave nunca foi observada.
func (r *ColdStartRecorder) SnapshotAgg(key PoolKey) (ColdStartAggregate, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	agg, ok := r.aggs[key]
	if !ok {
		return ColdStartAggregate{}, false
	}
	return ColdStartAggregate{
		Samples:      len(agg.samples),
		P95:          percentileOf(agg.samples, r.percentile),
		Max:          maxOf(agg.samples),
		Exhaustions:  agg.exhaustions,
		WarmRestores: agg.warmRestores,
		Recycles:     agg.recycles,
		Breached:     agg.breached,
	}, true
}

// P95For devolve o p95 corrente de uma chave (0 se nunca observada). É a leitura do
// SLI por versão/driver.
func (r *ColdStartRecorder) P95For(key PoolKey) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	agg, ok := r.aggs[key]
	if !ok {
		return 0
	}
	return percentileOf(agg.samples, r.percentile)
}

// percentileOf calcula o percentil p (fracção (0,1]) sobre as durações dadas, pelo
// método nearest-rank: ordena uma cópia e devolve o valor no rank ceil(p*n). Devolve
// 0 para uma amostra vazia. Não muta a fatia de entrada.
func percentileOf(samples []time.Duration, p float64) time.Duration {
	n := len(samples)
	if n == 0 {
		return 0
	}
	cp := make([]time.Duration, n)
	copy(cp, samples)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	rank := int(math.Ceil(p * float64(n)))
	if rank < 1 {
		rank = 1
	}
	if rank > n {
		rank = n
	}
	return cp[rank-1]
}

// maxOf devolve a maior duração (0 se vazio).
func maxOf(samples []time.Duration) time.Duration {
	var m time.Duration
	for _, d := range samples {
		if d > m {
			m = d
		}
	}
	return m
}

// msOf converte uma duração para milissegundos em vírgula flutuante (unidade da
// métrica/anotação de span).
func msOf(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// --- Sinks de referência in-memory (EPIC-08 liga os reais) ---

type nopColdMetric struct{}

func (nopColdMetric) Record(context.Context, ColdStartMetric) {}

type nopColdAlert struct{}

func (nopColdAlert) Fire(context.Context, ColdStartAlert) {}

// MemoryColdStartMetricSink acumula métricas em memória (introspecção/testes).
// Concorrente-seguro.
type MemoryColdStartMetricSink struct {
	mu      sync.Mutex
	metrics []ColdStartMetric
}

// Record implementa [ColdStartMetricSink].
func (s *MemoryColdStartMetricSink) Record(_ context.Context, m ColdStartMetric) {
	s.mu.Lock()
	s.metrics = append(s.metrics, m)
	s.mu.Unlock()
}

// Metrics devolve uma cópia das métricas registadas.
func (s *MemoryColdStartMetricSink) Metrics() []ColdStartMetric {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ColdStartMetric, len(s.metrics))
	copy(out, s.metrics)
	return out
}

// Len devolve o número de métricas registadas.
func (s *MemoryColdStartMetricSink) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.metrics)
}

// MemoryColdStartAlertSink acumula alertas em memória (introspecção/testes).
// Concorrente-seguro.
type MemoryColdStartAlertSink struct {
	mu     sync.Mutex
	alerts []ColdStartAlert
}

// Fire implementa [ColdStartAlertSink].
func (s *MemoryColdStartAlertSink) Fire(_ context.Context, a ColdStartAlert) {
	s.mu.Lock()
	s.alerts = append(s.alerts, a)
	s.mu.Unlock()
}

// Alerts devolve uma cópia dos alertas disparados.
func (s *MemoryColdStartAlertSink) Alerts() []ColdStartAlert {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ColdStartAlert, len(s.alerts))
	copy(out, s.alerts)
	return out
}

// Len devolve o número de alertas disparados.
func (s *MemoryColdStartAlertSink) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.alerts)
}
