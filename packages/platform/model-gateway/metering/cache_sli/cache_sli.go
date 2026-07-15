// Package cache_sli eleva o CACHE-HIT-RATE a SLI do Model Gateway (AOS-061,
// tecnica/06 §7, ADR-009/ADR-010). Impor o layout cache-estável (AOS-060) não
// basta se a sua eficácia não for medida: o cache thrash é uma explosão de custo
// SILENCIOSA. Este pacote torna-a visível.
//
// # O que faz
//
//  1. CÁLCULO por chamada — a fracção de tokens de PROMPT servidos por cache de
//     prefixo, a partir dos tokens de cache read/write reportados pelo provider
//     (port.Usage, campos de AOS-055/056). Ver [CallRate] para a fórmula.
//  2. AGREGAÇÃO por RUN e por TENANT — um [Recorder] acumula (read, prompt) por
//     [Key]{RunID, Tenant} e expõe o rate agregado como SLI. Runs/tenants
//     distintos NUNCA se contaminam (chaves distintas — isolamento).
//  3. EMISSÃO OTel ligada à trajectória — cada observação emite uma métrica
//     ([Metric]) por uma PORTA [MetricSink], com run/tenant/região como atributos
//     (correlação de trajectória, ADR-010) e anota o span da chamada. SEM segredo.
//  4. ALERTA < 80% — quando o rate AGREGADO por run/tenant desce abaixo do limiar
//     (configurável; default o alvo canónico [DefaultThreshold]), dispara um alerta
//     por uma PORTA [AlertSink]. O alerta é sobre o AGREGADO (não uma chamada ruidosa)
//     e é anti-flapping (dispara na TRANSIÇÃO para incumprimento, re-arma na recuperação;
//     só após um mínimo de amostras — ver [WithMinSamples]).
//
// # GW stateless — estado externo por porta
//
// O Gateway é stateless (AOS-055): o [Recorder] é o estado de agregação EXTERNO,
// injectado por porta ([modelgateway.WithCacheSLI]), à imagem das outras métricas.
// O EPIC-08 liga os sinks reais (OTel SDK, alertmanager); aqui há impls de
// referência in-memory ([MemoryMetricSink], [MemoryAlertSink]).
//
// # Âmbito da agregação/alerta: POR-RÉPLICA (não global) — ver EPIC-08
//
// ATENÇÃO ao escalonamento horizontal: a agregação ([Recorder.aggs]) e a DECISÃO
// de alerta (minSamples + transição [Aggregate.Breached]) são estado in-memory
// POR-PROCESSO. Sob um Gateway stateless replicado, cada réplica só vê a FRACÇÃO
// das chamadas de um run/tenant que aterram nela (sem afinidade garantida). Logo:
//   - o agregado por-réplica pode nunca atingir minSamples (sub-contagem → fail-silent);
//   - na quebra, cada réplica dispara a SUA transição → alertas duplicados (flapping
//     entre réplicas).
//
// Este [Recorder] é, por isso, a implementação de REFERÊNCIA cujo SLI/alerta é
// exacto apenas quando todas as chamadas do run aterram na mesma réplica (ex.: um
// único processo, ou testes). A avaliação GLOBAL de limiar+minSamples é do pilar de
// alerta central do EPIC-08: as réplicas emitem a métrica por-chamada/por-réplica
// (via [MetricSink]) para o backend OTel, e o alertmanager decide sobre a SÉRIE
// TEMPORAL PARTILHADA (o agregado global). O eixo de alerta local aqui serve de
// referência determinista e de rede de segurança por-processo, não de verdade global.
//
// # SLI lifetime-cumulative — a sensibilidade a thrash decai com a idade do run
//
// O agregado é um rácio CUMULATIVO ao longo de toda a vida do (run, tenant), SEM
// janela deslizante nem reset ([Aggregate] só soma). Consequência: um thrash que
// começa TARDE num run longo é diluído pela história saudável — depois de acumular
// muito prompt saudável, é preciso um grande volume de cache-miss para arrastar o
// agregado abaixo do limiar, e re-armar após uma quebra exige recuperar >= limiar
// CUMULATIVO (num run já poluído, pode ser praticamente inatingível). A detecção é
// portanto mais sensível CEDO no run. Uma janela (temporal ou por-N-amostras) para
// o rate de alerta fica para o EPIC-08 (sobre a série temporal partilhada); aqui o
// rate cumulativo é a referência determinista.
//
// # Zero dependências externas / determinismo (ADR-006)
//
// Só stdlib. Sem rand na decisão; relógio injectável para os timestamps de
// métrica/alerta ([WithClock]). A métrica leva APENAS contadores/rates + run/tenant/
// região NÃO-SECRETOS — nunca o prompt, nunca uma chave (ADR-006).
package cache_sli

import (
	"context"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

// DefaultThreshold é o alvo canónico do driver não-funcional (ADR-009): o
// cache-hit-rate agregado deve manter-se >= 80%. Abaixo, o alerta dispara. É
// CONFIGURÁVEL via [WithThreshold] — nunca um número mágico disperso pelo código.
const DefaultThreshold = 0.80

// defaultMinSamples é o mínimo de chamadas agregadas antes de o alerta ser
// avaliado (anti-flapping): uma única chamada ruidosa (ex.: o primeiro turno de
// um run, todo cache-write) não deve disparar um alerta. Configurável via
// [WithMinSamples].
const defaultMinSamples = 1

// Atributos OTel da métrica de cache-hit-rate. Ligam a métrica à TRAJECTÓRIA
// (run) e ao TENANT (board/humano responsável, reutilizado de AOS-057), SEM
// revelar segredos. Alinhados com a semconv usada nos spans gen_ai.* (AOS-057).
const (
	// MetricCacheHitRate é o nome da métrica OTel do SLI (fracção [0,1]).
	MetricCacheHitRate = "gen_ai.cache.hit_rate"
	// AttrRunID — correlação da trajectória (run_id → trace), como no agent-runtime.
	AttrRunID = agentruntime.AttrRunID
	// AttrTenant — tenant = board/humano responsável (eixo de agregação de AOS-057).
	AttrTenant = "aos.tenant"
	// AttrRegion — região efectiva da chamada (soberania), como na atribuição.
	AttrRegion = "aos.region"
	// AttrCacheHitRate — anotação no span da chamada com o cache-hit-rate agregado.
	AttrCacheHitRate = "aos.cache.hit_rate"
	// AttrCallCacheHitRate — anotação no span com o cache-hit-rate DESTA chamada.
	AttrCallCacheHitRate = "aos.cache.call_hit_rate"
	// AttrScope — âmbito da métrica ("aggregate"|"call").
	AttrScope = "aos.cache.scope"
	// AttrIncompleteKey sinaliza que a chave de agregação está INCOMPLETA (RunID
	// ou Tenant vazios): a métrica DA CHAMADA é emitida para observabilidade mas a
	// amostra NÃO é agregada nem avaliada para alerta (não polui um balde global
	// Key{"",""} que misturaria runs/tenants distintos). Presente só quando true.
	AttrIncompleteKey = "aos.cache.incomplete_key"
)

// Sample é a observação de UMA model call para o SLI de cache. Os campos de
// tokens vêm de [port.Usage] (reportados pelo provider); run/tenant/região são o
// eixo de agregação/correlação (reutilizados da atribuição de AOS-057). NÃO
// transporta prompt nem segredo.
type Sample struct {
	// RunID e Tenant são a CHAVE de agregação (isolamento). Tenant é o board/humano
	// responsável (reutilizado de AOS-057). Um deles vazio ainda agrega (a chave é o
	// par exacto); a isolação entre pares distintos mantém-se.
	RunID  string
	Tenant string
	// Region é observacional (atributo da métrica); não entra na chave.
	Region string
	// PromptTokens é o DENOMINADOR: os tokens de prompt (input) da chamada.
	PromptTokens int64
	// CacheReadTokens é o NUMERADOR: os tokens de prompt servidos por cache de prefixo.
	CacheReadTokens int64
	// CacheWriteTokens é transportado para observabilidade (custo de escrita de cache);
	// NÃO entra no denominador do rate (ver [CallRate]).
	CacheWriteTokens int64
}

// SampleFromUsage constrói uma [Sample] a partir do usage de uma chamada e do
// eixo de agregação (run/tenant/região). Centraliza a projecção port.Usage →
// Sample para o wiring do Gateway.
func SampleFromUsage(runID, tenant, region string, u port.Usage) Sample {
	return Sample{
		RunID:            runID,
		Tenant:           tenant,
		Region:           region,
		PromptTokens:     u.PromptTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
	}
}

// CallRate calcula o cache-hit-rate de UMA chamada.
//
// # Fórmula (documentada)
//
//	cache_hit_rate = CacheReadTokens / PromptTokens
//
// O DENOMINADOR é PromptTokens — a fracção de tokens de PROMPT (input) servidos a
// partir de cache de prefixo (semântica OpenAI/Anthropic: os cached tokens são um
// SUBCONJUNTO dos prompt tokens). O CacheWriteTokens (escrita de cache) NÃO entra
// no denominador: é o custo de POPULAR a cache, não um hit.
//
// PromptTokens == 0 ⇒ o SLI é INDEFINIDO (defined=false) — nunca 0 nem pânico
// (sem divisão por zero). Uma chamada sem prompt (ex.: só embeddings triviais) é
// OMITIDA da agregação, não conta como 0% (que envenenaria o rate).
//
// O rate é fixado ao intervalo [0,1] por defesa-em-profundidade: um provider que
// reporte CacheReadTokens > PromptTokens (inconsistência) nunca produz um rate > 1.
func CallRate(s Sample) (rate float64, defined bool) {
	if s.PromptTokens <= 0 {
		return 0, false
	}
	read := s.CacheReadTokens
	if read < 0 {
		read = 0
	}
	r := float64(read) / float64(s.PromptTokens)
	if r > 1 {
		r = 1
	}
	return r, true
}

// Aggregate é o estado acumulado de um [Key] (run, tenant). Imutável para o
// exterior — obtido por cópia via [Recorder.Snapshot].
type Aggregate struct {
	// CacheReadTokens/PromptTokens são os acumuladores do rate agregado.
	CacheReadTokens  int64
	PromptTokens     int64
	CacheWriteTokens int64
	// Samples é o número de chamadas DEFINIDAS agregadas (PromptTokens>0).
	Samples int
	// Breached indica se a chave está actualmente em incumprimento (< limiar). É o
	// estado que torna o alerta anti-flapping (dispara só na transição).
	Breached bool
}

// Rate devolve o cache-hit-rate agregado da chave, ou defined=false se ainda não
// há prompt tokens agregados (indefinido — não 0).
//
// O rácio é fixado a [0,1] por DEFESA-EM-PROFUNDIDADE, o mesmo invariante que
// [CallRate] garante por-chamada: um numerador negativo (provider a reportar
// read<0) nunca deprime o rate abaixo de 0, e um numerador sobre-reportado nunca
// o eleva acima de 1. Combina com o saneamento na acumulação ([Recorder.Observe])
// para que o EIXO DO ALERTA (agregado) tenha a mesma protecção do eixo por-chamada.
func (a Aggregate) Rate() (rate float64, defined bool) {
	if a.PromptTokens <= 0 {
		return 0, false
	}
	read := a.CacheReadTokens
	if read < 0 {
		read = 0
	}
	r := float64(read) / float64(a.PromptTokens)
	if r > 1 {
		r = 1
	}
	return r, true
}

// Key é a chave de agregação: o par (run, tenant). Pares distintos são ISOLADOS —
// um run/tenant nunca contamina outro.
type Key struct {
	RunID  string
	Tenant string
}

// Metric é uma amostra de métrica OTel emitida pelo SLI. Forma mínima e própria
// (zero-dep): o adaptador OTel real (EPIC-08) mapeia Name/Attributes para o SDK
// sem renomear. Value é a fracção [0,1]; Attributes leva run/tenant/região
// NÃO-SECRETOS (ligação à trajectória, ADR-010).
type Metric struct {
	Name       string
	Value      float64
	Attributes map[string]any
	Timestamp  time.Time
}

// MetricSink é a PORTA de emissão de métricas (o pilar OTel de EPIC-08 liga o
// real; aqui há [MemoryMetricSink] de referência).
type MetricSink interface {
	Record(ctx context.Context, m Metric)
}

// Alert é o evento de incumprimento do SLI: o rate agregado de uma chave desceu
// abaixo do limiar. Atribuível ao run/tenant (nunca segredo).
type Alert struct {
	Key       Key
	Region    string
	Rate      float64
	Threshold float64
	Samples   int
	Timestamp time.Time
}

// AlertSink é a PORTA de alertas (EPIC-08 liga o alertmanager real; aqui há
// [MemoryAlertSink] de referência).
type AlertSink interface {
	Fire(ctx context.Context, a Alert)
}

// Reading é o resultado de [Recorder.Observe]: o rate da chamada e do agregado, e
// se esta observação DISPAROU um alerta (transição para incumprimento).
type Reading struct {
	Key              Key
	CallRate         float64
	CallDefined      bool
	AggregateRate    float64
	AggregateDefined bool
	Samples          int
	Alerted          bool
	// IncompleteKey indica que RunID ou Tenant estavam vazios: a chamada foi
	// metrada (métrica por-chamada) mas NÃO agregada nem avaliada para alerta,
	// para não poluir um balde global Key{"",""} (ver [AttrIncompleteKey]).
	IncompleteKey bool
}

// Recorder é o AGREGADOR de SLI de cache-hit-rate por run/tenant — o estado
// externo, stateless-friendly para o Gateway (injectado por porta). Concorrente-
// seguro. Construir com [NewRecorder].
type Recorder struct {
	threshold  float64
	minSamples int
	metrics    MetricSink
	alerts     AlertSink
	clock      func() time.Time

	mu   sync.Mutex
	aggs map[Key]*Aggregate
}

// Option configura o [Recorder].
type Option func(*Recorder)

// WithThreshold define o limiar de alerta (fracção [0,1]). Default [DefaultThreshold]
// (0.80, o alvo canónico). Valores fora de (0,1] são ignorados (mantém o default).
func WithThreshold(t float64) Option {
	return func(r *Recorder) {
		if t > 0 && t <= 1 {
			r.threshold = t
		}
	}
}

// WithMinSamples define o mínimo de chamadas DEFINIDAS agregadas antes de o alerta
// ser avaliado (anti-flapping). Default [defaultMinSamples]. Valores < 1 são ignorados.
func WithMinSamples(n int) Option {
	return func(r *Recorder) {
		if n >= 1 {
			r.minSamples = n
		}
	}
}

// WithMetricSink liga a porta de métricas. Default: descarta (nopMetric).
func WithMetricSink(s MetricSink) Option {
	return func(r *Recorder) {
		if s != nil {
			r.metrics = s
		}
	}
}

// WithAlertSink liga a porta de alertas. Default: descarta (nopAlert).
func WithAlertSink(s AlertSink) Option {
	return func(r *Recorder) {
		if s != nil {
			r.alerts = s
		}
	}
}

// WithClock injecta o relógio determinista dos timestamps de métrica/alerta.
// Default: time.Now. Nunca usado numa DECISÃO (só carimba eventos).
func WithClock(clock func() time.Time) Option {
	return func(r *Recorder) {
		if clock != nil {
			r.clock = clock
		}
	}
}

// NewRecorder constrói o agregador de SLI. Sem sinks, a agregação corre na mesma
// (introspecção por [Recorder.Snapshot]); os sinks são o ponto de extensão OTel.
func NewRecorder(opts ...Option) *Recorder {
	r := &Recorder{
		threshold:  DefaultThreshold,
		minSamples: defaultMinSamples,
		metrics:    nopMetric{},
		alerts:     nopAlert{},
		clock:      time.Now,
		aggs:       make(map[Key]*Aggregate),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Threshold devolve o limiar configurado (introspecção).
func (r *Recorder) Threshold() float64 { return r.threshold }

// Observe regista UMA chamada: calcula o rate da chamada, AGREGA por (run, tenant),
// emite a métrica OTel (agregado + chamada) e avalia o alerta sobre o AGREGADO.
// Anota o span (se não-nil) com os rates — ligação à trajectória, sem segredo.
//
// Uma chamada INDEFINIDA (PromptTokens==0) é omitida da agregação (não conta como
// 0%) mas ainda emite a métrica da chamada como indefinida? Não: sem denominador
// não há rate — a chamada é simplesmente ignorada para o SLI (nem métrica de rate
// nem agregação), evitando poluir o sinal. Devolve a [Reading] resultante.
func (r *Recorder) Observe(ctx context.Context, span agentruntime.Span, s Sample) Reading {
	key := Key{RunID: s.RunID, Tenant: s.Tenant}
	callRate, callDefined := CallRate(s)

	rd := Reading{Key: key, CallRate: callRate, CallDefined: callDefined}
	if !callDefined {
		// SLI indefinido: não agrega nem emite rate (sem divisão por zero, sem 0%
		// envenenado). A chamada continua atribuída/metrada noutros eixos (AOS-057/062).
		return rd
	}

	now := r.clock()

	// Chave INCOMPLETA (RunID ou Tenant vazios): não agrega (evitaria um balde
	// global Key{"",""} que misturaria runs/tenants distintos, falsificando o SLI
	// e o alerta) nem avalia alerta. Emite apenas a métrica DA CHAMADA, sinalizada
	// como incompleta ([AttrIncompleteKey]), para observabilidade sem a confundir
	// com um run real. O SLI agregado por run/tenant exige a chave completa.
	if key.RunID == "" || key.Tenant == "" {
		rd.IncompleteKey = true
		r.emitMetric(ctx, key, s.Region, 0, false, callRate, now, true)
		r.annotate(span, 0, false, callRate)
		return rd
	}

	// Saneamento do NUMERADOR antes de acumular (defesa-em-profundidade no eixo do
	// alerta, espelhando [CallRate]): read<0 → 0 e read>PromptTokens → PromptTokens.
	// Assim um provider a sobre-reportar (read>prompt) nunca infla o agregado acima
	// de 1 (alerta suprimido/fail-silent), e um read negativo nunca deprime o
	// agregado nem envenena a contribuição das outras amostras da mesma chave.
	read := s.CacheReadTokens
	if read < 0 {
		read = 0
	}
	if read > s.PromptTokens {
		read = s.PromptTokens
	}

	r.mu.Lock()
	agg := r.aggs[key]
	if agg == nil {
		agg = &Aggregate{}
		r.aggs[key] = agg
	}
	agg.CacheReadTokens += read
	agg.PromptTokens += s.PromptTokens
	agg.CacheWriteTokens += s.CacheWriteTokens
	agg.Samples++

	aggRate, aggDefined := agg.Rate()
	samples := agg.Samples

	// Alerta anti-flapping sobre o AGREGADO: só após minSamples, dispara na TRANSIÇÃO
	// para incumprimento (healthy/warming → breached) e re-arma quando recupera (>=
	// limiar). Evita um alerta por cada chamada ruidosa e o flapping repetido.
	alerted := false
	if aggDefined && samples >= r.minSamples {
		below := aggRate < r.threshold
		if below && !agg.Breached {
			agg.Breached = true
			alerted = true
		} else if !below && agg.Breached {
			agg.Breached = false
		}
	}
	region := s.Region
	r.mu.Unlock()

	// Emissão FORA do lock (os sinks são I/O de referência; não segurar o lock).
	r.emitMetric(ctx, key, region, aggRate, aggDefined, callRate, now, false)
	r.annotate(span, aggRate, aggDefined, callRate)
	if alerted {
		r.alerts.Fire(ctx, Alert{
			Key:       key,
			Region:    region,
			Rate:      aggRate,
			Threshold: r.threshold,
			Samples:   samples,
			Timestamp: now,
		})
	}

	rd.AggregateRate = aggRate
	rd.AggregateDefined = aggDefined
	rd.Samples = samples
	rd.Alerted = alerted
	return rd
}

// emitMetric emite a métrica OTel do SLI (agregado) + a métrica da chamada. Só
// contadores/rates + run/tenant/região NÃO-SECRETOS (ADR-006/ADR-010).
func (r *Recorder) emitMetric(ctx context.Context, key Key, region string, aggRate float64, aggDefined bool, callRate float64, now time.Time, incomplete bool) {
	base := func(scope string) map[string]any {
		m := map[string]any{
			AttrRunID:  key.RunID,
			AttrTenant: key.Tenant,
			AttrScope:  scope,
		}
		if region != "" {
			m[AttrRegion] = region
		}
		if incomplete {
			m[AttrIncompleteKey] = true
		}
		return m
	}
	if aggDefined {
		r.metrics.Record(ctx, Metric{
			Name:       MetricCacheHitRate,
			Value:      aggRate,
			Attributes: base("aggregate"),
			Timestamp:  now,
		})
	}
	r.metrics.Record(ctx, Metric{
		Name:       MetricCacheHitRate,
		Value:      callRate,
		Attributes: base("call"),
		Timestamp:  now,
	})
}

// annotate anota o span da chamada com os rates (ligação à trajectória). Seguro
// com span nil. Nunca emite segredo.
func (r *Recorder) annotate(span agentruntime.Span, aggRate float64, aggDefined bool, callRate float64) {
	if span == nil {
		return
	}
	span.SetAttribute(AttrCallCacheHitRate, callRate)
	if aggDefined {
		span.SetAttribute(AttrCacheHitRate, aggRate)
	}
}

// Snapshot devolve uma CÓPIA do agregado de uma chave (introspecção/testes). ok é
// false se a chave nunca foi observada.
func (r *Recorder) Snapshot(key Key) (Aggregate, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	agg, ok := r.aggs[key]
	if !ok {
		return Aggregate{}, false
	}
	return *agg, true
}

// RateFor devolve o cache-hit-rate agregado de uma chave (defined=false se
// indefinido ou nunca observada). É a leitura do SLI por run/tenant.
func (r *Recorder) RateFor(key Key) (rate float64, defined bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	agg, ok := r.aggs[key]
	if !ok {
		return 0, false
	}
	return agg.Rate()
}

// --- Sinks de referência in-memory (EPIC-08 liga os reais) ---

type nopMetric struct{}

func (nopMetric) Record(context.Context, Metric) {}

type nopAlert struct{}

func (nopAlert) Fire(context.Context, Alert) {}

// MemoryMetricSink acumula métricas em memória (introspecção/testes). Concorrente-
// seguro.
type MemoryMetricSink struct {
	mu      sync.Mutex
	metrics []Metric
}

// Record implementa [MetricSink].
func (s *MemoryMetricSink) Record(_ context.Context, m Metric) {
	s.mu.Lock()
	s.metrics = append(s.metrics, m)
	s.mu.Unlock()
}

// Metrics devolve uma cópia das métricas registadas.
func (s *MemoryMetricSink) Metrics() []Metric {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Metric, len(s.metrics))
	copy(out, s.metrics)
	return out
}

// MemoryAlertSink acumula alertas em memória (introspecção/testes). Concorrente-
// seguro.
type MemoryAlertSink struct {
	mu     sync.Mutex
	alerts []Alert
}

// Fire implementa [AlertSink].
func (s *MemoryAlertSink) Fire(_ context.Context, a Alert) {
	s.mu.Lock()
	s.alerts = append(s.alerts, a)
	s.mu.Unlock()
}

// Alerts devolve uma cópia dos alertas disparados.
func (s *MemoryAlertSink) Alerts() []Alert {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Alert, len(s.alerts))
	copy(out, s.alerts)
	return out
}

// Len devolve o número de alertas disparados.
func (s *MemoryAlertSink) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.alerts)
}
