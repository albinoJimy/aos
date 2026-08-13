package cost

import (
	"context"
	"sync"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/port"
)

// Atributos OTel do custo (semconv-aligned). Ligam o custo à TRAJECTÓRIA (run/árvore)
// e ao eixo de atribuição (modelo/região/tenant de AOS-057), SEM revelar segredos.
const (
	// AttrCostUSD reexporta a chave GenAI de custo em USD (float, conveniência OTel).
	// O micro-USD INTEIRO ([AttrCostMicroUSD]) é a fonte de verdade emitida em paralelo.
	AttrCostUSD = agentruntime.AttrCostUSD
	// AttrCostMicroUSD — custo EXACTO da chamada em micro-USD int64 (sem float drift).
	// Alias da fonte única do vocabulário (otelgenai, via agentruntime): a MESMA chave
	// "aos.cost.micro_usd" que a agregação por trajectória de AOS-078 soma — garante que
	// o span de custo do GW e a agregação da camada otel-genai falam do mesmo atributo.
	AttrCostMicroUSD = agentruntime.AttrCostMicroUSD
	// AttrRunCostMicroUSD — custo AGREGADO do run em micro-USD (burn-down por run).
	AttrRunCostMicroUSD = "aos.cost.run_micro_usd"
	// AttrTreeCostMicroUSD — custo AGREGADO da árvore em micro-USD (burn-down por árvore).
	AttrTreeCostMicroUSD = "aos.cost.tree_micro_usd"
	// AttrCacheReadTokens — tokens de leitura de cache da chamada (gen_ai.usage.*).
	AttrCacheReadTokens = "gen_ai.usage.cache_read_tokens"
	// AttrCacheWriteTokens — tokens de escrita de cache da chamada.
	AttrCacheWriteTokens = "gen_ai.usage.cache_write_tokens"
	// AttrPricingVersion — versão tamper-evident da tabela de preços em vigor.
	AttrPricingVersion = "aos.cost.pricing_version"
	// AttrModel — modelo efectivo (ligação do custo ao modelo; espelha a atribuição).
	AttrModel = agentruntime.AttrRequestModel
	// AttrRegion — região efectiva (soberania), como na atribuição/SLI.
	AttrRegion = "aos.region"
	// AttrRunID — correlação da trajectória (run_id → trace).
	AttrRunID = agentruntime.AttrRunID
	// AttrTreeID — correlação da ÁRVORE de runs (eixo de burn-down/admission global).
	AttrTreeID = "aos.tree_id"
	// AttrTenant — tenant = board/humano responsável (eixo de agregação de AOS-057).
	AttrTenant = "aos.tenant"
)

// MetricCost é o nome da métrica OTel de custo. O Value é emitido em micro-USD
// INTEIRO (ver [Metric]) — dinheiro nunca em float, mesmo na telemetria.
const MetricCost = "gen_ai.usage.cost"

// microUSDToUSD converte micro-USD inteiro para USD (float) — usado APENAS para o
// atributo de span gen_ai.usage.cost_usd (conveniência OTel). A fonte de verdade é
// sempre o micro-USD inteiro (AttrCostMicroUSD), emitido em paralelo.
func microUSDToUSD(microUSD int64) float64 { return float64(microUSD) / float64(microPerUSD) }

// RunKey é a chave de agregação por RUN (trajectória) + tenant. Runs distintos são
// ISOLADOS.
type RunKey struct {
	RunID  string
	Tenant string
}

// TreeKey é a chave de agregação por ÁRVORE de runs (treeID) + tenant — o eixo do
// burn-down/admission GLOBAL (ADR-008). Árvores distintas são ISOLADAS.
type TreeKey struct {
	TreeID string
	Tenant string
}

// Sample é a observação de custo de UMA model call: os quatro tokens, o eixo de
// agregação (run/árvore/tenant) e a rota (modelo/região). NÃO transporta prompt nem
// segredo.
type Sample struct {
	RunID  string
	TreeID string
	Tenant string
	Region string
	Model  string
	Tokens TokenCounts
}

// SampleFromUsage projecta o usage de uma chamada ([port.Usage]) e o eixo de
// agregação numa [Sample]. Centraliza a conversão port.Usage → TokenCounts para o
// wiring do Gateway.
func SampleFromUsage(runID, treeID, tenant, region, model string, u port.Usage) Sample {
	return Sample{
		RunID:  runID,
		TreeID: treeID,
		Tenant: tenant,
		Region: region,
		Model:  model,
		Tokens: TokenCounts{
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			CacheReadTokens:  u.CacheReadTokens,
			CacheWriteTokens: u.CacheWriteTokens,
		},
	}
}

// Metric é uma amostra de métrica OTel de custo. Forma mínima e própria (zero-dep):
// o adaptador OTel real (EPIC-08) mapeia Name/Attributes para o SDK. O custo é
// int64 micro-USD (sem float); Tokens é o volume facturável.
type Metric struct {
	Name         string
	CostMicroUSD int64
	Tokens       int64
	Scope        string // "call" | "run" | "tree"
	Attributes   map[string]any
	Timestamp    time.Time
}

// MetricSink é a PORTA de emissão de métricas de custo (EPIC-08 liga o real; aqui
// há [MemoryMetricSink] de referência).
type MetricSink interface {
	Record(ctx context.Context, m Metric)
}

// BurndownEntry é o incremento de custo entregue ao burn-down/admission por um eixo
// (run ou árvore): o delta DESTA chamada e o cumulativo do eixo. O consumidor
// (EPIC-03) usa-o para o burn-down e a admissão global (ADR-008). Este ticket produz
// a MEDIDA; o enforcement é EPIC-03.
type BurndownEntry struct {
	// Axis é "run" ou "tree".
	Axis string
	// ID é o RunID (axis=run) ou o TreeID (axis=tree).
	ID string
	// Tenant é o board/humano responsável (isolamento).
	Tenant string
	// Delta é o custo+tokens DESTA chamada.
	Delta Amount
	// Cumulative é o custo+tokens AGREGADO do eixo após este delta.
	Cumulative Amount
	// Timestamp carimba o incremento (relógio injectável).
	Timestamp time.Time
}

// BurndownSink é a PORTA do burn-down: recebe cada incremento de custo por eixo
// (run/árvore). O admission global de EPIC-03 liga o real; aqui há
// [MemoryBurndownSink] de referência.
type BurndownSink interface {
	Add(ctx context.Context, e BurndownEntry)
}

// Reading é o resultado de [Recorder.Observe]: o custo da chamada, a decomposição e
// os cumulativos por run/árvore. Err != nil se o custo foi NÃO-calculável
// (fail-closed) — nesse caso nada é agregado nem emitido.
type Reading struct {
	Amount         Amount
	Breakdown      Breakdown
	PricingVersion string
	RunCumulative  Amount
	TreeCumulative Amount
	Err            error
}

// Recorder é o AGREGADOR de custo por run/árvore — o estado externo do Gateway
// stateless (injectado por porta). Concorrente-seguro. Calcula o custo (via
// [Calculator]), agrega por run e por árvore, emite a métrica OTel, anota o span e
// alimenta o burn-down. Construir com [NewRecorder].
type Recorder struct {
	calc     *Calculator
	metrics  MetricSink
	burndown BurndownSink
	clock    func() time.Time

	mu       sync.Mutex
	runAggs  map[RunKey]*Amount
	treeAggs map[TreeKey]*Amount
}

// Option configura o [Recorder].
type Option func(*Recorder)

// WithMetricSink liga a porta de métricas. Default: descarta.
func WithMetricSink(s MetricSink) Option {
	return func(r *Recorder) {
		if s != nil {
			r.metrics = s
		}
	}
}

// WithBurndownSink liga a porta de burn-down/admission (EPIC-03). Default: descarta.
func WithBurndownSink(s BurndownSink) Option {
	return func(r *Recorder) {
		if s != nil {
			r.burndown = s
		}
	}
}

// WithClock injecta o relógio determinista dos timestamps de métrica/burn-down.
// Default: time.Now. Nunca usado numa decisão de custo.
func WithClock(clock func() time.Time) Option {
	return func(r *Recorder) {
		if clock != nil {
			r.clock = clock
		}
	}
}

// NewRecorder constrói o agregador sobre um [Calculator]. Sem sinks, a agregação
// corre na mesma (introspecção por [Recorder.CostForRun]/[Recorder.CostForTree]).
func NewRecorder(calc *Calculator, opts ...Option) *Recorder {
	r := &Recorder{
		calc:     calc,
		metrics:  nopMetric{},
		burndown: nopBurndown{},
		clock:    time.Now,
		runAggs:  make(map[RunKey]*Amount),
		treeAggs: make(map[TreeKey]*Amount),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// HasPrice reexpõe a COBERTURA de preço do [Calculator] subjacente (leitura pura,
// sem agregação nem efeito): o composition root que declara uma escada de modelos
// cruza-a com esta porta no ARRANQUE, em vez de descobrir a lacuna com [ErrNoPrice]
// depois de o provider já ter sido invocado. Recorder ou calculador nil ⇒ nada tem
// preço (fail-closed).
func (r *Recorder) HasPrice(model, region string) bool {
	if r == nil {
		return false
	}
	return r.calc.HasPrice(model, region)
}

// Observe calcula o custo de UMA chamada, agrega por run e por árvore, emite a
// métrica OTel (chamada + agregados), anota o span e alimenta o burn-down. Devolve a
// [Reading]. Fail-closed: se o custo for NÃO-calculável (sem preço, tokens negativos,
// overflow), Reading.Err é preenchido e NADA é agregado/emitido (nunca um 0
// silencioso que falsificaria o burn-down).
func (r *Recorder) Observe(ctx context.Context, span agentruntime.Span, s Sample) Reading {
	amt, bd, err := r.calc.CostBreakdown(s.Tokens, s.Model, s.Region)
	if err != nil {
		return Reading{Err: err}
	}
	now := r.clock()
	version := r.calc.PricingVersion()

	// Agregação overflow-checked por run E por árvore. Um eixo sem chave (RunID/TreeID
	// vazio) NÃO é agregado (evita um balde global que misturaria trajectórias), mas o
	// custo DA CHAMADA é sempre emitido/anotado.
	var runCum, treeCum Amount
	var runOK, treeOK bool
	r.mu.Lock()
	if s.RunID != "" {
		rk := RunKey{RunID: s.RunID, Tenant: s.Tenant}
		acc := r.runAggs[rk]
		if acc == nil {
			acc = &Amount{}
			r.runAggs[rk] = acc
		}
		if sum, ok := acc.AddChecked(amt); ok {
			*acc = sum
			runCum, runOK = sum, true
		} else {
			r.mu.Unlock()
			return Reading{Amount: amt, Breakdown: bd, PricingVersion: version, Err: ErrOverflow}
		}
	}
	if s.TreeID != "" {
		tk := TreeKey{TreeID: s.TreeID, Tenant: s.Tenant}
		acc := r.treeAggs[tk]
		if acc == nil {
			acc = &Amount{}
			r.treeAggs[tk] = acc
		}
		if sum, ok := acc.AddChecked(amt); ok {
			*acc = sum
			treeCum, treeOK = sum, true
		} else {
			r.mu.Unlock()
			return Reading{Amount: amt, Breakdown: bd, PricingVersion: version, Err: ErrOverflow}
		}
	}
	r.mu.Unlock()

	// Emissão FORA do lock (os sinks são I/O de referência).
	r.emitMetrics(ctx, s, amt, runCum, runOK, treeCum, treeOK, version, now)
	r.annotate(span, s, amt, bd, runCum, runOK, treeCum, treeOK, version)
	r.feedBurndown(ctx, s, amt, runCum, runOK, treeCum, treeOK, now)

	return Reading{
		Amount:         amt,
		Breakdown:      bd,
		PricingVersion: version,
		RunCumulative:  runCum,
		TreeCumulative: treeCum,
	}
}

// emitMetrics emite a métrica de custo da chamada e dos agregados (run/árvore). Só
// custo/tokens + eixos NÃO-SECRETOS (ADR-006/ADR-010).
func (r *Recorder) emitMetrics(ctx context.Context, s Sample, call, runCum Amount, runOK bool, treeCum Amount, treeOK bool, version string, now time.Time) {
	attrs := func() map[string]any {
		m := map[string]any{
			AttrModel:          s.Model,
			AttrPricingVersion: version,
		}
		if s.RunID != "" {
			m[AttrRunID] = s.RunID
		}
		if s.TreeID != "" {
			m[AttrTreeID] = s.TreeID
		}
		if s.Tenant != "" {
			m[AttrTenant] = s.Tenant
		}
		if s.Region != "" {
			m[AttrRegion] = s.Region
		}
		return m
	}
	r.metrics.Record(ctx, Metric{Name: MetricCost, CostMicroUSD: call.CostMicroUSD, Tokens: call.Tokens, Scope: "call", Attributes: attrs(), Timestamp: now})
	if runOK {
		r.metrics.Record(ctx, Metric{Name: MetricCost, CostMicroUSD: runCum.CostMicroUSD, Tokens: runCum.Tokens, Scope: "run", Attributes: attrs(), Timestamp: now})
	}
	if treeOK {
		r.metrics.Record(ctx, Metric{Name: MetricCost, CostMicroUSD: treeCum.CostMicroUSD, Tokens: treeCum.Tokens, Scope: "tree", Attributes: attrs(), Timestamp: now})
	}
}

// annotate anota o span da chamada com o custo (USD + micro-USD exacto), tokens de
// cache, versão de preços e os cumulativos por run/árvore — ligando o custo ao
// modelo/região/trajectória (o principal é anotado pela atribuição de AOS-057 no
// MESMO span). Seguro com span nil. Nunca emite segredo.
func (r *Recorder) annotate(span agentruntime.Span, s Sample, call Amount, bd Breakdown, runCum Amount, runOK bool, treeCum Amount, treeOK bool, version string) {
	if span == nil {
		return
	}
	span.SetAttribute(AttrCostMicroUSD, call.CostMicroUSD)
	span.SetAttribute(AttrCostUSD, microUSDToUSD(call.CostMicroUSD))
	span.SetAttribute(AttrCacheReadTokens, s.Tokens.CacheReadTokens)
	span.SetAttribute(AttrCacheWriteTokens, s.Tokens.CacheWriteTokens)
	span.SetAttribute(AttrPricingVersion, version)
	span.SetAttribute(AttrModel, s.Model)
	if s.Region != "" {
		span.SetAttribute(AttrRegion, s.Region)
	}
	if runOK {
		span.SetAttribute(AttrRunCostMicroUSD, runCum.CostMicroUSD)
	}
	if treeOK {
		span.SetAttribute(AttrTreeCostMicroUSD, treeCum.CostMicroUSD)
	}
}

// feedBurndown entrega os incrementos de custo por eixo (run/árvore) à porta de
// burn-down/admission (EPIC-03). Só emite para um eixo com chave presente.
func (r *Recorder) feedBurndown(ctx context.Context, s Sample, call, runCum Amount, runOK bool, treeCum Amount, treeOK bool, now time.Time) {
	if runOK {
		r.burndown.Add(ctx, BurndownEntry{Axis: "run", ID: s.RunID, Tenant: s.Tenant, Delta: call, Cumulative: runCum, Timestamp: now})
	}
	if treeOK {
		r.burndown.Add(ctx, BurndownEntry{Axis: "tree", ID: s.TreeID, Tenant: s.Tenant, Delta: call, Cumulative: treeCum, Timestamp: now})
	}
}

// CostForRun devolve o custo AGREGADO de um run (ok=false se nunca observado). É a
// leitura de burn-down por run.
func (r *Recorder) CostForRun(k RunKey) (Amount, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	acc, ok := r.runAggs[k]
	if !ok {
		return Amount{}, false
	}
	return *acc, true
}

// CostForTree devolve o custo AGREGADO de uma árvore (ok=false se nunca observada).
// É a leitura de burn-down/admission GLOBAL por árvore (ADR-008).
func (r *Recorder) CostForTree(k TreeKey) (Amount, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	acc, ok := r.treeAggs[k]
	if !ok {
		return Amount{}, false
	}
	return *acc, true
}

// --- Sinks de referência in-memory (EPIC-08/EPIC-03 ligam os reais) ---

type nopMetric struct{}

func (nopMetric) Record(context.Context, Metric) {}

type nopBurndown struct{}

func (nopBurndown) Add(context.Context, BurndownEntry) {}

// MemoryMetricSink acumula métricas de custo em memória (introspecção/testes).
// Concorrente-seguro.
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

// MemoryBurndownSink acumula os incrementos de burn-down em memória (introspecção/
// testes) E mantém os cumulativos por eixo — a impl de referência do burn-down que o
// admission global de EPIC-03 substitui. Concorrente-seguro.
type MemoryBurndownSink struct {
	mu      sync.Mutex
	entries []BurndownEntry
	runs    map[string]Amount
	trees   map[string]Amount
}

// NewMemoryBurndownSink constrói o sink de referência.
func NewMemoryBurndownSink() *MemoryBurndownSink {
	return &MemoryBurndownSink{runs: make(map[string]Amount), trees: make(map[string]Amount)}
}

// Add implementa [BurndownSink]: regista o incremento e memoriza o cumulativo por
// eixo/ID+tenant.
func (s *MemoryBurndownSink) Add(_ context.Context, e BurndownEntry) {
	s.mu.Lock()
	s.entries = append(s.entries, e)
	key := e.ID + "\x00" + e.Tenant
	if e.Axis == "tree" {
		s.trees[key] = e.Cumulative
	} else {
		s.runs[key] = e.Cumulative
	}
	s.mu.Unlock()
}

// Entries devolve uma cópia dos incrementos registados.
func (s *MemoryBurndownSink) Entries() []BurndownEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]BurndownEntry, len(s.entries))
	copy(out, s.entries)
	return out
}

// Run devolve o cumulativo de custo de um run (via o burn-down), ok=false se ausente.
func (s *MemoryBurndownSink) Run(runID, tenant string) (Amount, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.runs[runID+"\x00"+tenant]
	return a, ok
}

// Tree devolve o cumulativo de custo de uma árvore (via o burn-down), ok=false se
// ausente.
func (s *MemoryBurndownSink) Tree(treeID, tenant string) (Amount, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.trees[treeID+"\x00"+tenant]
	return a, ok
}
