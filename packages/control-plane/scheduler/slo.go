// slo.go — SLIs/SLOs em CONFIG VERSIONADA + ALERTAS + DASHBOARD mínimo agregado
// (AOS-034).
//
// SLIs/SLOs VERSIONADOS. À imagem da política declarativa versionada do AOS-030
// (SemVer monótono, validação fail-closed), os objectivos de nível de serviço
// vivem numa config versionada ([SLOConfig] carregável de JSON — stdlib, zero
// deps). Os SLIs são derivados por AGREGAÇÃO sobre os wide events (AOS-034); cada
// SLI tem um SLO explícito (utilização-alvo de headroom, defer-rate máximo,
// janelas de saturação sustentada).
//
// ALERTAS DETERMINISTAS. A avaliação de alerta é uma função PURA do
// [DashboardSnapshot] + [SLOConfig] (+ o streak de saturação, avançado só por
// Observe explícito — sem time.Now/rand). Dispara em SATURAÇÃO SUSTENTADA (uma
// partição saturada em N observações consecutivas) e em HEADROOM CRITICAMENTE
// BAIXO (fracção livre abaixo do limiar crítico). Determinismo total: mesma
// sequência de observações ⇒ mesmos alertas.
//
// DASHBOARD MÍNIMO AGREGADO. Um DESCRITOR (struct) do estado AGREGADO — profundidade
// total, defer-rate, headroom livre, alertas activos — construído por agregação
// QUERY-TIME sobre as medições captadas ([RecordingMeter]). Evita o modo de falha
// "individualmente ok, agregadamente colapsa": o colapso agregado só é visível na
// soma, não em cada partição isolada. Não é um Grafana real — é o descritor + a
// agregação determinística.
package scheduler

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ---------------------------------------------------------------------------
// SLOConfig — objectivos de nível de serviço, VERSIONADOS (SemVer).
// ---------------------------------------------------------------------------

// SLOConfig são os SLOs do plano de controlo. É VERSIONADA (SemVer) e
// serializável (JSON, stdlib). Os valores são thresholds dos alertas e os alvos
// dos SLIs.
type SLOConfig struct {
	// Version é o SemVer do artefacto (ex.: "1.0.0"). Monótono no hot-reload.
	Version string `json:"version"`
	// HeadroomCriticalFreeRatio ∈ (0,1): fracção LIVRE de headroom abaixo da qual a
	// chave está CRITICAMENTE baixa (dispara alerta crítico). Ex.: 0.05 = 5% livre.
	HeadroomCriticalFreeRatio float64 `json:"headroom_critical_free_ratio"`
	// HeadroomUtilizationTarget ∈ (0,1): utilização-ALVO do headroom (SLO). Acima
	// deste alvo dispara aviso (aproximação do tecto). Ex.: 0.85.
	HeadroomUtilizationTarget float64 `json:"headroom_utilization_target"`
	// MaxDeferRate ∈ [0,1]: defer-rate máximo tolerado (SLO). Acima dispara aviso.
	MaxDeferRate float64 `json:"max_defer_rate"`
	// SustainedSaturationWindows >= 1: nº de OBSERVAÇÕES consecutivas com uma
	// partição saturada que constitui saturação SUSTENTADA (dispara crítico). Evita
	// alertar em picos transitórios de uma única observação.
	SustainedSaturationWindows int `json:"sustained_saturation_windows"`
}

// DefaultSLOConfig devolve SLOs por omissão sãos (alinhados aos drivers do epic).
func DefaultSLOConfig() SLOConfig {
	return SLOConfig{
		Version:                    "1.0.0",
		HeadroomCriticalFreeRatio:  0.05,
		HeadroomUtilizationTarget:  0.85,
		MaxDeferRate:               0.20,
		SustainedSaturationWindows: 3,
	}
}

// validate é FAIL-CLOSED: rejeita config sem sentido (thresholds fora de [0,1],
// janelas < 1, SemVer inválido). Um artefacto inválido NÃO substitui o anterior.
func (c SLOConfig) validate() error {
	if !validSemVer(c.Version) {
		return fmt.Errorf("%w: version %q não é SemVer", ErrInvalidSLOConfig, c.Version)
	}
	if c.HeadroomCriticalFreeRatio <= 0 || c.HeadroomCriticalFreeRatio >= 1 {
		return fmt.Errorf("%w: headroom_critical_free_ratio deve estar em (0,1)", ErrInvalidSLOConfig)
	}
	if c.HeadroomUtilizationTarget <= 0 || c.HeadroomUtilizationTarget >= 1 {
		return fmt.Errorf("%w: headroom_utilization_target deve estar em (0,1)", ErrInvalidSLOConfig)
	}
	if c.MaxDeferRate < 0 || c.MaxDeferRate > 1 {
		return fmt.Errorf("%w: max_defer_rate deve estar em [0,1]", ErrInvalidSLOConfig)
	}
	if c.SustainedSaturationWindows < 1 {
		return fmt.Errorf("%w: sustained_saturation_windows deve ser >= 1", ErrInvalidSLOConfig)
	}
	return nil
}

// validSemVer reutiliza o parser de SemVer do motor de política (AOS-030) —
// mesma convenção de versão em todo o scheduler.
func validSemVer(s string) bool {
	_, ok := parseSemVer(s)
	return ok
}

// ErrInvalidSLOConfig sinaliza uma config de SLO inválida (fail-closed).
var ErrInvalidSLOConfig = fmt.Errorf("scheduler: config de SLO inválida")

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

// ---------------------------------------------------------------------------
// Alertas — nomes/severidades estáveis.
// ---------------------------------------------------------------------------

// Severity é a severidade de um alerta.
type Severity string

const (
	// SevWarning — aproximação de um SLO (aviso accionável, não crítico).
	SevWarning Severity = "warning"
	// SevCritical — violação crítica (headroom criticamente baixo, saturação
	// sustentada).
	SevCritical Severity = "critical"
)

// Nomes de alerta ESTÁVEIS (contrato).
const (
	// AlertHeadroomCritical dispara quando a fracção LIVRE de headroom de alguma
	// chave está abaixo de HeadroomCriticalFreeRatio.
	AlertHeadroomCritical = "headroom_critically_low"
	// AlertSaturationSustained dispara quando alguma partição está saturada há
	// SustainedSaturationWindows observações consecutivas.
	AlertSaturationSustained = "saturation_sustained"
	// AlertDeferRateHigh dispara quando o defer-rate agregado excede MaxDeferRate.
	AlertDeferRateHigh = "defer_rate_high"
	// AlertHeadroomUtilizationHigh dispara quando a utilização de alguma chave
	// excede HeadroomUtilizationTarget.
	AlertHeadroomUtilizationHigh = "headroom_utilization_high"
)

// Alert é um alerta avaliado. Fired indica se disparou; Detail é a evidência
// (chave/partição + valor observado vs. limiar).
type Alert struct {
	Name     string
	Severity Severity
	Fired    bool
	Value    float64
	Detail   string
}

// ---------------------------------------------------------------------------
// Dashboard mínimo agregado — descritor + agregação query-time.
// ---------------------------------------------------------------------------

// PartitionStat é o estado agregado de uma partição no dashboard.
type PartitionStat struct {
	Partition   string
	Tenant      string
	Priority    string
	Depth       int
	Capacity    int
	OldestAgeMs int64
	Saturated   bool
}

// HeadroomStat é o estado agregado de headroom de uma chave no dashboard. A
// identidade é (Key, Tenant): a mesma provider_key restringida a tenants
// distintos vive em séries separadas (não colapsa numa só), à imagem das
// partições.
type HeadroomStat struct {
	Key                 string
	Tenant              string
	FreeTokens          int64
	FreeRequests        int64
	ReservedTokens      int64
	LimitTokens         int64
	UtilizationTokens   float64
	UtilizationRequests float64
	FreeRatioTokens     float64
	FreeRatioRequests   float64
}

// DashboardSnapshot é o DESCRITOR do estado AGREGADO do plano de controlo — o
// antídoto ao "individualmente ok, agregadamente colapsa". Construído por
// [AggregateDashboard] sobre os wide events.
type DashboardSnapshot struct {
	// Agregados de saturação.
	Partitions          []PartitionStat
	TotalQueueDepth     int
	MaxQueueDepth       int
	SaturatedPartitions int
	// Defer-rate agregado = deferred / (admitted + deferred).
	Admitted           int64
	Deferred           int64
	DeferRate          float64
	DegradationActions int64
	SpawnsDeferred     int64
	// Agregados de headroom.
	Headroom             []HeadroomStat
	TotalFreeTokens      int64
	MinHeadroomFreeRatio float64
	MaxUtilization       float64
	// Alertas activos (preenchidos por [SLOEvaluator.Evaluate]).
	Alerts []Alert
}

// AggregateDashboard constrói o dashboard AGREGADO por agregação QUERY-TIME sobre
// as medições captadas (wide events). Determinística: gauges ⇒ ÚLTIMO valor por
// série (a medição de maior Seq vence); counters ⇒ SOMA. Iteração ordenada por
// chave de série. Nada foi descartado no emit — a agregação responde-se aqui.
func AggregateDashboard(measurements []Measurement) DashboardSnapshot {
	// Última medição por série (para gauges) — a de maior Seq.
	latest := make(map[string]Measurement)
	var admitted, deferred, degradation, spawnDeferred int64

	for _, m := range measurements {
		switch m.Kind {
		case KindCounter:
			switch m.Instrument {
			case MetricAdmitted:
				admitted += int64(m.Value)
			case MetricDeferred:
				deferred += int64(m.Value)
			case MetricDegradation:
				degradation += int64(m.Value)
			case MetricSpawnDeferred:
				spawnDeferred += int64(m.Value)
			}
		case KindGauge, KindHistogram:
			sk := m.SeriesKey()
			if prev, ok := latest[sk]; !ok || m.Seq > prev.Seq {
				latest[sk] = m
			}
		}
	}

	d := DashboardSnapshot{
		Admitted:           admitted,
		Deferred:           deferred,
		DegradationActions: degradation,
		SpawnsDeferred:     spawnDeferred,
	}
	if tot := admitted + deferred; tot > 0 {
		d.DeferRate = float64(deferred) / float64(tot)
	}

	// Reconstrói partições e headroom a partir dos gauges "latest". Agrupa as
	// séries por identidade de partição/chave (as três métricas de fila partilham
	// a partição; as de headroom partilham a chave).
	parts := make(map[string]*PartitionStat)
	heads := make(map[string]*HeadroomStat)

	// Ordena as séries por chave para agregação determinística.
	keys := make([]string, 0, len(latest))
	for k := range latest {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		m := latest[k]
		switch m.Instrument {
		case MetricQueueDepth, MetricQueueOldestAge, MetricQueueSaturated:
			part, _ := m.Attr(AttrMetricPartition)
			ps := partStat(parts, attrStr(part))
			tenant, _ := m.Attr(AttrMetricTenant)
			prio, _ := m.Attr(AttrMetricPriority)
			capacity, _ := m.Attr(AttrMetricCapacity)
			ps.Tenant = attrStr(tenant)
			ps.Priority = attrStr(prio)
			ps.Capacity = int(attrFloat(capacity))
			switch m.Instrument {
			case MetricQueueDepth:
				ps.Depth = int(m.Value)
			case MetricQueueOldestAge:
				ps.OldestAgeMs = int64(m.Value)
			case MetricQueueSaturated:
				ps.Saturated = m.Value >= 0.5
			}
		case MetricHeadroomFreeTokens, MetricHeadroomFreeRequests, MetricHeadroomReservedTokens, MetricHeadroomUtilization:
			key, _ := m.Attr(AttrMetricKey)
			tenant, _ := m.Attr(AttrMetricTenant)
			// Chaveia pela identidade COMPLETA (key, tenant): múltiplos tenants na
			// mesma provider_key não colapsam numa só HeadroomStat (falso-negativo do
			// alerta crítico). Cada (key,tenant) é uma série própria, à imagem das
			// partições (chaveadas por tenant:priority).
			hs := headStat(heads, attrStr(key), attrStr(tenant))
			switch m.Instrument {
			case MetricHeadroomFreeTokens:
				hs.FreeTokens = int64(m.Value)
			case MetricHeadroomFreeRequests:
				hs.FreeRequests = int64(m.Value)
			case MetricHeadroomReservedTokens:
				hs.ReservedTokens = int64(m.Value)
			case MetricHeadroomUtilization:
				dim, _ := m.Attr(AttrMetricDimension)
				if attrStr(dim) == "requests" {
					hs.UtilizationRequests = m.Value
				} else {
					hs.UtilizationTokens = m.Value
				}
			}
		}
	}

	// Materializa partições (ordenadas).
	pkeys := sortedKeys(parts)
	d.MinHeadroomFreeRatio = 1
	for _, pk := range pkeys {
		ps := parts[pk]
		d.Partitions = append(d.Partitions, *ps)
		d.TotalQueueDepth += ps.Depth
		if ps.Depth > d.MaxQueueDepth {
			d.MaxQueueDepth = ps.Depth
		}
		if ps.Saturated {
			d.SaturatedPartitions++
		}
	}

	// Materializa headroom (ordenado) + limite derivado (livre+reservado).
	hkeys := sortedHeadKeys(heads)
	for _, hk := range hkeys {
		hs := heads[hk]
		hs.LimitTokens = hs.FreeTokens + hs.ReservedTokens
		if hs.LimitTokens > 0 {
			hs.FreeRatioTokens = float64(hs.FreeTokens) / float64(hs.LimitTokens)
		} else {
			hs.FreeRatioTokens = 1
		}
		// Fracção livre de REQUESTS (RPM) derivada da utilização (livre/limite =
		// 1 − reservado/limite). Sem gauge de limite de requests, a utilização é a
		// fonte de verdade já emitida por dimensão. Clamp a [0,1].
		hs.FreeRatioRequests = 1 - hs.UtilizationRequests
		if hs.FreeRatioRequests < 0 {
			hs.FreeRatioRequests = 0
		} else if hs.FreeRatioRequests > 1 {
			hs.FreeRatioRequests = 1
		}
		d.Headroom = append(d.Headroom, *hs)
		d.TotalFreeTokens += hs.FreeTokens
		// A fracção livre mínima considera AMBAS as dimensões (tokens E requests):
		// uma chave esgotada em RPM mas folgada em TPM dispara o alerta crítico.
		if hs.FreeRatioTokens < d.MinHeadroomFreeRatio {
			d.MinHeadroomFreeRatio = hs.FreeRatioTokens
		}
		if hs.FreeRatioRequests < d.MinHeadroomFreeRatio {
			d.MinHeadroomFreeRatio = hs.FreeRatioRequests
		}
		if hs.UtilizationTokens > d.MaxUtilization {
			d.MaxUtilization = hs.UtilizationTokens
		}
		if hs.UtilizationRequests > d.MaxUtilization {
			d.MaxUtilization = hs.UtilizationRequests
		}
	}
	if len(d.Headroom) == 0 {
		d.MinHeadroomFreeRatio = 1
	}
	return d
}

func partStat(m map[string]*PartitionStat, part string) *PartitionStat {
	if ps, ok := m[part]; ok {
		return ps
	}
	ps := &PartitionStat{Partition: part}
	m[part] = ps
	return ps
}

// headStat devolve (ou cria) a HeadroomStat de uma identidade (key, tenant). A
// chave do mapa é COMPOSTA (key\x00tenant) para que tenants distintos na mesma
// provider_key não colidam no mesmo ponteiro; Key/Tenant preservam os valores
// limpos para o descritor.
func headStat(m map[string]*HeadroomStat, key, tenant string) *HeadroomStat {
	mk := key + "\x00" + tenant
	if hs, ok := m[mk]; ok {
		return hs
	}
	hs := &HeadroomStat{Key: key, Tenant: tenant}
	m[mk] = hs
	return hs
}

func sortedKeys(m map[string]*PartitionStat) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func sortedHeadKeys(m map[string]*HeadroomStat) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func attrStr(v any) string {
	if v == nil {
		return ""
	}
	return attrString(v)
}

func attrFloat(v any) float64 {
	switch t := v.(type) {
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case float64:
		return t
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// SLOEvaluator — avaliação de alerta DETERMINISTA (streak de saturação com estado).
// ---------------------------------------------------------------------------

// SLOEvaluator avalia os SLOs contra observações sucessivas do dashboard. Mantém
// o STREAK de saturação por partição (nº de observações consecutivas saturadas),
// avançado SÓ por [SLOEvaluator.Evaluate] — sem relógio nem aleatoriedade. Mesma
// sequência de observações ⇒ mesmos alertas (determinismo total).
type SLOEvaluator struct {
	cfg    SLOConfig
	streak map[string]int // partição → observações consecutivas saturadas
}

// NewSLOEvaluator constrói o avaliador com uma config validada. Uma config
// inválida é substituída pela [DefaultSLOConfig] (fail-closed: nunca corre com
// thresholds sem sentido).
func NewSLOEvaluator(cfg SLOConfig) *SLOEvaluator {
	if cfg.validate() != nil {
		cfg = DefaultSLOConfig()
	}
	return &SLOEvaluator{cfg: cfg, streak: make(map[string]int)}
}

// Config devolve a config activa do avaliador.
func (e *SLOEvaluator) Config() SLOConfig { return e.cfg }

// Evaluate AVANÇA o streak de saturação (uma observação) e devolve os alertas
// activos, ordenados por nome (estável). É determinística dada a sequência de
// chamadas. NÃO usa time.Now/rand.
func (e *SLOEvaluator) Evaluate(d DashboardSnapshot) []Alert {
	var alerts []Alert

	// 1) Saturação SUSTENTADA: avança o streak de cada partição saturada; zera as
	// que já não estão. Dispara se ALGUMA atinge o limiar de janelas.
	seen := make(map[string]bool, len(d.Partitions))
	// Ordena as partições para avanço determinístico do streak.
	parts := make([]PartitionStat, len(d.Partitions))
	copy(parts, d.Partitions)
	sort.Slice(parts, func(i, j int) bool { return parts[i].Partition < parts[j].Partition })

	worstStreak := 0
	worstPart := ""
	for _, ps := range parts {
		seen[ps.Partition] = true
		if ps.Saturated {
			e.streak[ps.Partition]++
		} else {
			e.streak[ps.Partition] = 0
		}
		if e.streak[ps.Partition] > worstStreak {
			worstStreak = e.streak[ps.Partition]
			worstPart = ps.Partition
		}
	}
	// Zera partições ausentes desta observação (deixaram de reportar ⇒ não saturadas).
	for p := range e.streak {
		if !seen[p] {
			e.streak[p] = 0
		}
	}
	satFired := worstStreak >= e.cfg.SustainedSaturationWindows
	alerts = append(alerts, Alert{
		Name:     AlertSaturationSustained,
		Severity: SevCritical,
		Fired:    satFired,
		Value:    float64(worstStreak),
		Detail: fmt.Sprintf("partição %q saturada em %d/%d observações consecutivas",
			worstPart, worstStreak, e.cfg.SustainedSaturationWindows),
	})

	// 2) Headroom CRITICAMENTE baixo: fracção livre mínima abaixo do limiar.
	critFired := len(d.Headroom) > 0 && d.MinHeadroomFreeRatio < e.cfg.HeadroomCriticalFreeRatio
	alerts = append(alerts, Alert{
		Name:     AlertHeadroomCritical,
		Severity: SevCritical,
		Fired:    critFired,
		Value:    d.MinHeadroomFreeRatio,
		Detail: fmt.Sprintf("fracção livre mínima %.3f < limiar crítico %.3f",
			d.MinHeadroomFreeRatio, e.cfg.HeadroomCriticalFreeRatio),
	})

	// 3) Utilização de headroom acima do alvo (aviso).
	utilFired := d.MaxUtilization > e.cfg.HeadroomUtilizationTarget
	alerts = append(alerts, Alert{
		Name:     AlertHeadroomUtilizationHigh,
		Severity: SevWarning,
		Fired:    utilFired,
		Value:    d.MaxUtilization,
		Detail: fmt.Sprintf("utilização máxima %.3f > alvo %.3f",
			d.MaxUtilization, e.cfg.HeadroomUtilizationTarget),
	})

	// 4) Defer-rate acima do máximo (aviso).
	deferFired := d.DeferRate > e.cfg.MaxDeferRate
	alerts = append(alerts, Alert{
		Name:     AlertDeferRateHigh,
		Severity: SevWarning,
		Fired:    deferFired,
		Value:    d.DeferRate,
		Detail:   fmt.Sprintf("defer-rate %.3f > máximo %.3f", d.DeferRate, e.cfg.MaxDeferRate),
	})

	sort.Slice(alerts, func(i, j int) bool { return alerts[i].Name < alerts[j].Name })
	return alerts
}

// FiredAlerts devolve apenas os alertas que dispararam (query-time; ordenados).
func FiredAlerts(alerts []Alert) []Alert {
	var out []Alert
	for _, a := range alerts {
		if a.Fired {
			out = append(out, a)
		}
	}
	return out
}

// BuildDashboard é o atalho: agrega as medições e anexa os alertas avaliados.
// O avaliador AVANÇA o seu streak (uma observação) — chamar em cadência regular.
func BuildDashboard(measurements []Measurement, ev *SLOEvaluator) DashboardSnapshot {
	d := AggregateDashboard(measurements)
	if ev != nil {
		d.Alerts = ev.Evaluate(d)
	}
	return d
}
