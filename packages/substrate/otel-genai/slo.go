package otelgenai

// slo.go — DASHBOARD de SLIs/SLOs da MEDIAÇÃO em CONFIG VERSIONADA, derivado por
// AGREGAÇÃO QUERY-TIME sobre os wide events/spans JÁ emitidos (AOS-085). SEM
// instrumentação nova: reutiliza os wide events (AOS-082) e a contabilidade de
// custo por trajectória (AOS-078). Módulo FOLHA — não importa scheduler nem o
// Model Gateway; segue o MOLDE de packages/control-plane/scheduler/slo.go
// (AOS-034) com tipos próprios, sem o importar.
//
// # Os quatro SLIs (tecnica/08 §7, tecnica/10)
//
//   - CACHE-HIT-RATE — média PONDERADA (por prompt tokens) do atributo
//     [AttrCacheHitRate] dos wide events. Torna o CACHE THRASH visível como SLI
//     (ADR-009): um run com cache-hit baixo aparece como SLI degradado. SLO: > 0.80.
//   - OVERHEAD DE MEDIAÇÃO p95 — percentil 95 da [WideEvent.LatencyNanos] dos spans
//     [OpExecuteTool] (o ponto ÚNICO de mediação; a sua latência É o overhead que a
//     mediação acrescenta). SLO: p95 < 15 ms.
//   - CUSTO POR TRAJECTÓRIA — custo agregado por trace, reutilizando
//     [AggregateByTrace] (AOS-078) SEM dupla-contagem (conta só os spans `chat`). O
//     SLI é o PIOR (máximo) custo por trajectória — a restrição vinculativa. SLO: um
//     tecto por trajectória.
//   - OVERRIDE-RATE — fracção de decisões de mediação com escalada/override,
//     derivada de [AttrDecision] == [DecisionEscalate] (não há atributo de override
//     dedicado; documenta-se a derivação). Denominador guardado contra zero. SLO: um
//     tecto de fracção.
//
// # Cada SLI é AVALIADO vs. o seu SLO e é CONSUMÍVEL pelos alertas (AOS-086)
//
// [BuildDashboard] devolve um [DashboardSnapshot] com um [SLIValue] por SLI
// (Value observado, SLO, Met, direcção). O snapshot expõe [DashboardSnapshot.SLIs]
// e [DashboardSnapshot.Breaches] — o tipo ESTÁVEL que AOS-086 consome para os
// alertas. NÃO se implementam alertas aqui (é AOS-086) — só o SLI vs. SLO.
//
// # Drill-down do agregado até ao trace (aggregate → trace)
//
// Cada SLI DEGRADADO guarda em [SLIValue.Offenders] os trace_ids responsáveis (os
// traces abaixo do limiar de cache-hit; os execute_tool acima do tecto de
// overhead; os traces acima do tecto de custo; os traces com escalada). Um SLI que
// cumpre o SLO não guarda ofensores (superfície limpa).
//
// # Semântica de "sem dados" (anti-vacuidade)
//
// Um SLI sem amostras (ex. um run sem nenhum wide event com [AttrCacheHitRate], ou
// sem decisões de mediação) fica com Samples == 0 e Met == true, mas NÃO conta como
// violação nem como cumprimento afirmado: [SLIValue.Evaluated] devolve false e
// [DashboardSnapshot.Breaches] ignora-o. Assim evita-se os dois modos de falha:
// marcar o SLI cronicamente degradado (Value 0) OU afirmar Met por vacuidade.

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// SLOConfig — objectivos de nível de serviço da mediação, VERSIONADOS (SemVer).
// ---------------------------------------------------------------------------

// SLOConfig são os SLOs dos quatro SLIs da mediação. É VERSIONADA (SemVer,
// monótona no hot-reload) e serializável (JSON, stdlib — zero deps). Os alvos
// alinham-se aos drivers não-funcionais (cache > 0.80 ADR-009; overhead p95 < 15 ms).
type SLOConfig struct {
	// Version é o SemVer do artefacto (ex.: "1.0.0").
	Version string `json:"version"`
	// CacheHitRateTarget ∈ (0,1]: cache-hit-rate MÍNIMO aceitável (SLO cumprido se o
	// valor observado >= alvo). Driver ADR-009: > 0.80.
	CacheHitRateTarget float64 `json:"cache_hit_rate_target"`
	// MediationOverheadP95MaxNanos > 0: p95 MÁXIMO tolerado do overhead de mediação,
	// em nanos (SLO cumprido se p95 <= máximo). Driver: < 15 ms.
	MediationOverheadP95MaxNanos int64 `json:"mediation_overhead_p95_max_nanos"`
	// CostPerTrajectoryMaxMicroUSD > 0: custo MÁXIMO por trajectória, em micro-USD
	// inteiro (SLO cumprido se o pior trace <= máximo). Sem drift de vírgula flutuante.
	CostPerTrajectoryMaxMicroUSD int64 `json:"cost_per_trajectory_max_micro_usd"`
	// OverrideRateMax ∈ [0,1]: fracção MÁXIMA de decisões com override/escalada
	// tolerada (SLO cumprido se a fracção <= máximo).
	OverrideRateMax float64 `json:"override_rate_max"`
}

// DefaultSLOConfig devolve SLOs por omissão alinhados aos drivers do epic:
// cache-hit > 0.80 (ADR-009), overhead de mediação p95 < 15 ms, e tectos sãos de
// custo por trajectória (0.50 USD) e de override-rate (10%).
func DefaultSLOConfig() SLOConfig {
	return SLOConfig{
		Version:                      "1.0.0",
		CacheHitRateTarget:           0.80,
		MediationOverheadP95MaxNanos: int64(15 * time.Millisecond),
		CostPerTrajectoryMaxMicroUSD: 500_000,
		OverrideRateMax:              0.10,
	}
}

// ErrInvalidSLOConfig sinaliza uma config de SLO inválida (fail-closed).
var ErrInvalidSLOConfig = fmt.Errorf("otelgenai: config de SLO inválida")

// validate é FAIL-CLOSED: rejeita config sem sentido (SemVer inválido, alvos fora
// de gama). Um artefacto inválido NÃO substitui o anterior nem corre.
func (c SLOConfig) validate() error {
	if !validSemVer(c.Version) {
		return fmt.Errorf("%w: version %q não é SemVer", ErrInvalidSLOConfig, c.Version)
	}
	if c.CacheHitRateTarget <= 0 || c.CacheHitRateTarget > 1 {
		return fmt.Errorf("%w: cache_hit_rate_target deve estar em (0,1]", ErrInvalidSLOConfig)
	}
	if c.MediationOverheadP95MaxNanos <= 0 {
		return fmt.Errorf("%w: mediation_overhead_p95_max_nanos deve ser > 0", ErrInvalidSLOConfig)
	}
	if c.CostPerTrajectoryMaxMicroUSD <= 0 {
		return fmt.Errorf("%w: cost_per_trajectory_max_micro_usd deve ser > 0", ErrInvalidSLOConfig)
	}
	if c.OverrideRateMax < 0 || c.OverrideRateMax > 1 {
		return fmt.Errorf("%w: override_rate_max deve estar em [0,1]", ErrInvalidSLOConfig)
	}
	return nil
}

// Validate expõe a validação fail-closed (para o chamador rejeitar uma config
// antes de a activar). Devolve nil sse a config é válida.
func (c SLOConfig) Validate() error { return c.validate() }

// LoadSLOConfig desserializa e VALIDA uma config de SLO de JSON (fail-closed). Um
// JSON malformado ou valores fora de gama são rejeitados — o chamador mantém a
// config anterior (nunca cai numa config vazia/permissiva).
func LoadSLOConfig(raw []byte) (SLOConfig, error) {
	var c SLOConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return SLOConfig{}, fmt.Errorf("%w: JSON: %v", ErrInvalidSLOConfig, err)
	}
	if err := c.validate(); err != nil {
		return SLOConfig{}, err
	}
	return c, nil
}

// validSemVer aceita um SemVer estrito MAJOR.MINOR.PATCH (inteiros não-negativos,
// sem zeros à esquerda), com pré-lançamento/build opcionais ignorados para a
// comparação de validade. Reimplementado localmente (módulo FOLHA — não importa o
// parser do scheduler), mesma convenção de versão.
func validSemVer(s string) bool {
	// Descarta build metadata (+...) e pré-lançamento (-...) para a validação do core.
	core := s
	if i := strings.IndexByte(core, '+'); i >= 0 {
		core = core[:i]
	}
	if i := strings.IndexByte(core, '-'); i >= 0 {
		core = core[:i]
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if !validNumericID(p) {
			return false
		}
	}
	return true
}

// validNumericID valida um identificador numérico de SemVer: dígitos, sem zeros à
// esquerda (excepto o próprio "0").
func validNumericID(p string) bool {
	if p == "" {
		return false
	}
	for _, r := range p {
		if r < '0' || r > '9' {
			return false
		}
	}
	if len(p) > 1 && p[0] == '0' {
		return false
	}
	if _, err := strconv.Atoi(p); err != nil {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// SLIValue — o valor de um SLI avaliado vs. o seu SLO (contrato p/ AOS-086).
// ---------------------------------------------------------------------------

// Nomes ESTÁVEIS dos SLIs (contrato consumido por AOS-086).
const (
	// SLICacheHitRate — média ponderada do cache-hit-rate.
	SLICacheHitRate = "cache_hit_rate"
	// SLIMediationOverheadP95 — p95 do overhead de mediação (execute_tool).
	SLIMediationOverheadP95 = "mediation_overhead_p95"
	// SLICostPerTrajectory — pior custo por trajectória.
	SLICostPerTrajectory = "cost_per_trajectory"
	// SLIOverrideRate — fracção de decisões com override/escalada.
	SLIOverrideRate = "override_rate"
)

// Direcção do SLO: o sentido em que o valor observado cumpre o alvo.
const (
	// DirMin — o valor observado deve ser >= SLO (ex. cache-hit-rate).
	DirMin = "min"
	// DirMax — o valor observado deve ser <= SLO (ex. overhead p95, custo, override).
	DirMax = "max"
)

// SLIValue é o valor de UM SLI avaliado contra o seu SLO. É o tipo ESTÁVEL que os
// alertas de AOS-086 consomem: Value observado, SLO (o limiar), Met (cumpre?),
// Direction (sentido), Samples (nº de amostras que suportam o valor) e Offenders
// (drill-down: trace_ids responsáveis quando degradado).
type SLIValue struct {
	// Name é o nome estável do SLI (ex. [SLICacheHitRate]).
	Name string
	// Value é o valor observado. Unidades por SLI: cache-hit/override são fracções
	// [0,1]; overhead p95 é NANOS; custo é MICRO-USD.
	Value float64
	// SLO é o limiar-alvo (mesma unidade de Value).
	SLO float64
	// Direction é [DirMin] (Value >= SLO cumpre) ou [DirMax] (Value <= SLO cumpre).
	Direction string
	// Met indica se o SLO é cumprido. Sem amostras (Samples == 0) é true por
	// convenção mas NÃO conta como violação (ver [SLIValue.Evaluated]).
	Met bool
	// Samples é o nº de amostras que suportam o valor (wide events/traces contados).
	// 0 ⇒ SLI não avaliado (sem dados) — nem breach nem cumprimento afirmado.
	Samples int
	// Offenders são os trace_ids responsáveis pela degradação (drill-down
	// aggregate→trace), preenchidos SÓ quando o SLI está em breach. Ordenados de
	// forma determinista (pior primeiro).
	Offenders []string
}

// Evaluated indica se o SLI tem dados que o suportem (Samples > 0). Um SLI não
// avaliado não é uma violação nem um cumprimento afirmado.
func (s SLIValue) Evaluated() bool { return s.Samples > 0 }

// Breached indica se o SLI foi AVALIADO e NÃO cumpre o SLO — a condição que AOS-086
// transforma em alerta. Um SLI sem dados nunca está em breach.
func (s SLIValue) Breached() bool { return s.Samples > 0 && !s.Met }

// ---------------------------------------------------------------------------
// DashboardSnapshot — os quatro SLIs agregados + drill-down + superfície p/ alertas.
// ---------------------------------------------------------------------------

// DashboardSnapshot é o descritor AGREGADO dos quatro SLIs da mediação num
// instante, com o SLO de cada um avaliado e o drill-down por SLI. É construído por
// [BuildDashboard]/[BuildDashboardFromSpans] por agregação query-time. Expõe
// [DashboardSnapshot.SLIs] e [DashboardSnapshot.Breaches] — a superfície estável
// que os alertas de AOS-086 consomem (sem que este módulo implemente alertas).
type DashboardSnapshot struct {
	// Config é a SLOConfig efectivamente aplicada (já validada; fail-closed para a
	// default se a config passada era inválida).
	Config SLOConfig
	// ConfigDefaulted é true quando a SLOConfig passada a [BuildDashboard] era
	// INVÁLIDA e foi substituída pela [DefaultSLOConfig] (que pode ser MAIS
	// permissiva). Torna o fallback OBSERVÁVEL — um consumidor/alerta (AOS-086) pode
	// detectar que a sua config estrita foi rejeitada e ignorada, em vez de correr
	// silenciosamente com os limiares errados. Para rejeição estrita antes de
	// construir, usar [LoadSLOConfig]/[SLOConfig.Validate].
	ConfigDefaulted bool
	// Os quatro SLIs, cada um avaliado vs. o seu SLO.
	CacheHitRate         SLIValue
	MediationOverheadP95 SLIValue
	CostPerTrajectory    SLIValue
	OverrideRate         SLIValue
}

// SLIs devolve os quatro SLIs por ordem estável (para iteração/serialização).
func (d DashboardSnapshot) SLIs() []SLIValue {
	return []SLIValue{d.CacheHitRate, d.MediationOverheadP95, d.CostPerTrajectory, d.OverrideRate}
}

// Breaches devolve os SLIs AVALIADOS que NÃO cumprem o SLO (ordem estável). É a
// entrada directa dos alertas de AOS-086: cada breach traz o valor observado, o
// SLO e os trace_ids ofensores (drill-down). Vazio ⇒ todos os SLIs com dados
// cumprem.
func (d DashboardSnapshot) Breaches() []SLIValue {
	var out []SLIValue
	for _, s := range d.SLIs() {
		if s.Breached() {
			out = append(out, s)
		}
	}
	return out
}

// BuildDashboard constrói o snapshot dos quatro SLIs por agregação query-time. Os
// SLIs de cache-hit, overhead e override derivam-se dos wide events (AOS-082); o de
// custo por trajectória reutiliza [AggregateByTrace] (AOS-078) sobre os spans, sem
// dupla-contagem. A config é VALIDADA fail-closed: uma config inválida é
// substituída pela [DefaultSLOConfig] (nunca corre com alvos sem sentido).
func BuildDashboard(events []WideEvent, spans []SpanData, cfg SLOConfig) DashboardSnapshot {
	defaulted := false
	if cfg.validate() != nil {
		cfg = DefaultSLOConfig()
		defaulted = true
	}
	return DashboardSnapshot{
		Config:               cfg,
		ConfigDefaulted:      defaulted,
		CacheHitRate:         cacheHitRateSLI(events, cfg.CacheHitRateTarget),
		MediationOverheadP95: overheadP95SLI(events, cfg.MediationOverheadP95MaxNanos),
		CostPerTrajectory:    costPerTrajectorySLI(spans, cfg.CostPerTrajectoryMaxMicroUSD),
		OverrideRate:         overrideRateSLI(events, cfg.OverrideRateMax),
	}
}

// BuildDashboardFromSpans é a conveniência que projecta os spans em wide events
// (via [WideEventFromSpanData]) e delega em [BuildDashboard]. Útil quando o
// chamador só tem os spans emitidos: a cache-hit/decisão vivem nos atributos do
// span, capturados no bag do wide event. Nota: o overhead p95 exige spans com
// relógio (Start/End) — um [RecordingTracer] sem clock dá latências 0.
func BuildDashboardFromSpans(spans []SpanData, cfg SLOConfig) DashboardSnapshot {
	events := make([]WideEvent, 0, len(spans))
	for _, sd := range spans {
		events = append(events, WideEventFromSpanData(sd))
	}
	return BuildDashboard(events, spans, cfg)
}

// ---------------------------------------------------------------------------
// Derivação dos SLIs (funções puras sobre wide events / spans).
// ---------------------------------------------------------------------------

// cacheHitRateSLI deriva o cache-hit-rate como média PONDERADA (por prompt tokens)
// do atributo [AttrCacheHitRate] dos wide events. Só contam os eventos que carregam
// o atributo (a fonte real, emitida pelo Model Gateway). O peso é [WideEvent.InputTokens]
// (prompt tokens — o denominador natural do rate); um evento sem tokens pesa 1 para
// não o descartar. Drill-down: quando degradado, os trace_ids cuja média por-trace
// fica abaixo do alvo (pior primeiro).
func cacheHitRateSLI(events []WideEvent, target float64) SLIValue {
	sli := SLIValue{Name: SLICacheHitRate, SLO: target, Direction: DirMin, Met: true}

	type acc struct{ weight, weightedRate float64 }
	perTrace := make(map[string]*acc)
	var order []string
	var totalWeight, totalWeightedRate float64
	samples := 0

	for _, e := range events {
		v, ok := e.Attributes[AttrCacheHitRate]
		if !ok {
			continue
		}
		rate, ok := attrFloat64(v)
		if !ok {
			continue
		}
		samples++
		w := float64(e.InputTokens)
		if w <= 0 {
			w = 1
		}
		totalWeight += w
		totalWeightedRate += rate * w

		a := perTrace[e.TraceIDHex]
		if a == nil {
			a = &acc{}
			perTrace[e.TraceIDHex] = a
			order = append(order, e.TraceIDHex)
		}
		a.weight += w
		a.weightedRate += rate * w
	}

	sli.Samples = samples
	if samples == 0 || totalWeight == 0 {
		// Sem amostras de cache: SLI não avaliado (não fabrica um 0 cronicamente
		// degradado nem afirma Met por vacuidade). Samples==0 ⇒ ignorado por Breaches.
		sli.Samples = 0
		return sli
	}
	sli.Value = totalWeightedRate / totalWeight
	sli.Met = sli.Value >= target
	if !sli.Met {
		// Drill-down: trace_ids cuja média ponderada por-trace fica abaixo do alvo,
		// ordenados por rate ascendente (pior primeiro), empate desfeito por trace_id.
		type tr struct {
			id   string
			rate float64
		}
		var below []tr
		for _, id := range order {
			a := perTrace[id]
			if a.weight == 0 || id == "" {
				continue
			}
			if r := a.weightedRate / a.weight; r < target {
				below = append(below, tr{id, r})
			}
		}
		sort.Slice(below, func(i, j int) bool {
			if below[i].rate != below[j].rate {
				return below[i].rate < below[j].rate
			}
			return below[i].id < below[j].id
		})
		for _, t := range below {
			sli.Offenders = append(sli.Offenders, t.id)
		}
	}
	return sli
}

// overheadP95SLI deriva o p95 do OVERHEAD DE MEDIAÇÃO: o percentil 95 da
// [WideEvent.LatencyNanos] dos eventos [OpExecuteTool] — o ponto único de mediação,
// cuja latência é o custo que a mediação acrescenta. Drill-down: quando degradado,
// os trace_ids dos execute_tool acima do tecto (pior latência primeiro).
func overheadP95SLI(events []WideEvent, maxNanos int64) SLIValue {
	sli := SLIValue{Name: SLIMediationOverheadP95, SLO: float64(maxNanos), Direction: DirMax, Met: true}

	type sample struct {
		trace string
		lat   int64
	}
	var lat []int64
	var samples []sample
	for _, e := range events {
		if e.Operation != OpExecuteTool {
			continue
		}
		lat = append(lat, e.LatencyNanos)
		samples = append(samples, sample{e.TraceIDHex, e.LatencyNanos})
	}

	sli.Samples = len(lat)
	if len(lat) == 0 {
		return sli
	}
	p95 := percentileNanos(lat, 95)
	sli.Value = float64(p95)
	sli.Met = p95 <= maxNanos
	if !sli.Met {
		sort.Slice(samples, func(i, j int) bool {
			if samples[i].lat != samples[j].lat {
				return samples[i].lat > samples[j].lat
			}
			return samples[i].trace < samples[j].trace
		})
		seen := make(map[string]bool)
		for _, s := range samples {
			if s.lat > maxNanos && s.trace != "" && !seen[s.trace] {
				seen[s.trace] = true
				sli.Offenders = append(sli.Offenders, s.trace)
			}
		}
	}
	return sli
}

// costPerTrajectorySLI deriva o SLI de custo por trajectória reutilizando
// [AggregateByTrace] (AOS-078): o custo agregado por trace, contando SÓ os spans
// `chat` (sem dupla-contagem do agregado do invoke_agent nem do execute_tool). O
// valor do SLI é o PIOR (máximo) custo por trajectória — a restrição vinculativa.
// Drill-down: quando degradado, os trace_ids acima do tecto (mais caro primeiro).
func costPerTrajectorySLI(spans []SpanData, ceilMicroUSD int64) SLIValue {
	sli := SLIValue{Name: SLICostPerTrajectory, SLO: float64(ceilMicroUSD), Direction: DirMax, Met: true}

	byTrace := AggregateByTrace(spans)
	sli.Samples = len(byTrace)
	if len(byTrace) == 0 {
		return sli
	}

	type tc struct {
		id   string
		cost int64
	}
	var maxCost int64
	var over []tc
	for id, u := range byTrace {
		if u.CostMicroUSD > maxCost {
			maxCost = u.CostMicroUSD
		}
		if u.CostMicroUSD > ceilMicroUSD && id != "" {
			over = append(over, tc{id, u.CostMicroUSD})
		}
	}
	sli.Value = float64(maxCost)
	sli.Met = maxCost <= ceilMicroUSD
	if !sli.Met {
		sort.Slice(over, func(i, j int) bool {
			if over[i].cost != over[j].cost {
				return over[i].cost > over[j].cost
			}
			return over[i].id < over[j].id
		})
		for _, t := range over {
			sli.Offenders = append(sli.Offenders, t.id)
		}
	}
	return sli
}

// overrideRateSLI deriva o OVERRIDE-RATE: a fracção de decisões de mediação com
// escalada/override. Como o vocabulário NÃO tem um atributo de override dedicado, o
// override deriva-se de [AttrDecision] == [DecisionEscalate] (a escalada explícita
// do efeito de mediação — ex. aprovação humana). O denominador são os eventos que
// carregam uma decisão ([WideEvent.Decision] != ""); é GUARDADO contra zero (um run
// sem decisões dá SLI não avaliado, não uma divisão por zero). Drill-down: quando
// degradado, os trace_ids com pelo menos uma escalada.
func overrideRateSLI(events []WideEvent, maxRate float64) SLIValue {
	sli := SLIValue{Name: SLIOverrideRate, SLO: maxRate, Direction: DirMax, Met: true}

	denom := 0
	num := 0
	overrideTraces := make(map[string]bool)
	var order []string
	for _, e := range events {
		if e.Decision == "" {
			continue
		}
		denom++
		if e.Decision == DecisionEscalate {
			num++
			if e.TraceIDHex != "" && !overrideTraces[e.TraceIDHex] {
				overrideTraces[e.TraceIDHex] = true
				order = append(order, e.TraceIDHex)
			}
		}
	}

	sli.Samples = denom
	if denom == 0 {
		return sli
	}
	sli.Value = float64(num) / float64(denom)
	sli.Met = sli.Value <= maxRate
	if !sli.Met {
		sort.Strings(order)
		sli.Offenders = order
	}
	return sli
}

// ---------------------------------------------------------------------------
// Percentil — helper puro, determinista.
// ---------------------------------------------------------------------------

// percentileNanos devolve o percentil p (0..100) de xs por INTERPOLAÇÃO LINEAR
// entre os dois ranks mais próximos (método "type-7", o default de numpy/Excel):
// rank fraccionário = p/100 * (n-1) sobre a amostra ORDENADA ascendente. É puro e
// determinista (ordena uma cópia; não muta xs). Vazio ⇒ 0; um elemento ⇒ esse
// elemento.
func percentileNanos(xs []int64, p float64) int64 {
	n := len(xs)
	if n == 0 {
		return 0
	}
	s := make([]int64, n)
	copy(s, xs)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	if n == 1 || p <= 0 {
		return s[0]
	}
	if p >= 100 {
		return s[n-1]
	}
	rank := (p / 100.0) * float64(n-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return s[lo]
	}
	frac := rank - float64(lo)
	return int64(math.Round(float64(s[lo]) + frac*float64(s[hi]-s[lo])))
}
