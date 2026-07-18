package otelgenai

// alerts.go — REGRAS DE ALERTA a partir dos SLIs/SLOs de AOS-085 (AOS-086). Fecha o
// ciclo operacional: os SLIs só protegem se dispararem acção. O cache thrash
// invisível, a explosão de custo silenciosa, o overhead de mediação a degradar-se e
// o override-rate a subir (approval theater) passam a produzir SINAL ACCIONÁVEL —
// com limiar, severidade e ENCAMINHAMENTO para runbook — em vez de padrões que só
// se descobrem post-mortem.
//
// Módulo FOLHA (zero-dep, só stdlib): NÃO importa scheduler nem o Model Gateway.
// Segue o MOLDE de packages/control-plane/scheduler/slo.go (AOS-034) — avaliação de
// alerta como FUNÇÃO PURA do snapshot + janela SUSTENTADA por streak explícito, sem
// time.Now/rand — com tipos PRÓPRIOS, sem o importar.
//
// # Os alertas são uma função PURA do DashboardSnapshot (AOS-085)
//
// Não há métricas novas: cada regra observa um [SLIValue] JÁ derivado por AOS-085
// ([DashboardSnapshot.SLIs]) e dispara sse ele estiver em breach ([SLIValue.Breached]).
// [EvaluateAlerts] é o caso de OBSERVAÇÃO ÚNICA (determinista: mesmo snapshot ⇒
// mesmos alertas). [AlertEvaluator.Observe] é o caso com JANELA SUSTENTADA: mantém
// um streak por SLI e só dispara quando a violação PERSISTE N observações
// consecutivas (anti picos transitórios / fadiga de alerta). Determinismo total:
// mesma sequência de snapshots ⇒ mesmos alertas.
//
// # Encaminhamento para runbook (cross-ref tecnica/10 §7 e §8)
//
// Cada regra carrega uma [Route] — runbook id + owner, rótulos ESTÁVEIS, NUNCA
// segredos. O encaminhamento é um mapa AlertName→Route com uma rota DEFAULT
// fail-safe ([DefaultRoute]): um alerta sem rota configurada nunca fica órfão. Os
// [SLIValue.Offenders] (trace_ids) viajam no alerta — o drill-down chega ao runbook
// já com o trace ofensor.

import (
	"fmt"
	"sort"
)

// ---------------------------------------------------------------------------
// Severity — severidade estável de um alerta (contrato).
// ---------------------------------------------------------------------------

// Severity é a severidade de um alerta (rótulo estável do contrato).
type Severity string

const (
	// SevWarning — aproximação/degradação accionável, ainda não crítica. Usada nos
	// indicadores AVANÇADOS (cache thrash, approval theater) que alertam ANTES do
	// impacto significativo.
	SevWarning Severity = "warning"
	// SevCritical — violação crítica de um SLO vinculativo (explosão de custo por
	// trajectória, overhead de mediação a degradar-se).
	SevCritical Severity = "critical"
)

// ---------------------------------------------------------------------------
// Nomes de alerta ESTÁVEIS (contrato consumido pela rotação/runbooks).
// ---------------------------------------------------------------------------

const (
	// AlertCacheHitRateLow — cache-hit-rate abaixo do alvo (cache thrash). Indicador
	// AVANÇADO da explosão de custo silenciosa: dispara ao cruzar o limiar, antes do
	// impacto no custo. Deriva do SLI [SLICacheHitRate].
	AlertCacheHitRateLow = "cache_hit_rate_low"
	// AlertCostPerTrajectoryHigh — custo por trajectória acima do orçamento (explosão
	// de custo). Deriva do SLI [SLICostPerTrajectory].
	AlertCostPerTrajectoryHigh = "cost_per_trajectory_high"
	// AlertMediationOverheadHigh — overhead de mediação p95 acima do tecto (a mediação
	// a degradar-se). Deriva do SLI [SLIMediationOverheadP95].
	AlertMediationOverheadHigh = "mediation_overhead_high"
	// AlertOverrideRateHigh — override-rate acima do tecto (approval theater: a
	// escalada/override a subir). Deriva do SLI [SLIOverrideRate].
	AlertOverrideRateHigh = "override_rate_high"
)

// ---------------------------------------------------------------------------
// Route — encaminhamento accionável (runbook + owner), rótulos estáveis.
// ---------------------------------------------------------------------------

// Route é o destino accionável de um alerta: um runbook id e o owner responsável.
// São RÓTULOS ESTÁVEIS (contrato), NUNCA segredos nem PII — só o suficiente para o
// operador chegar ao procedimento certo (cross-ref tecnica/10 §7 e §8).
type Route struct {
	// Runbook é o id estável do runbook operacional (ex. "RB-03", tecnica/10 §8).
	Runbook string
	// Owner é o rótulo do responsável/escalonamento (ex. "DevOps/SRE").
	Owner string
}

// Zero indica uma rota vazia (sem runbook nem owner) — usada para detectar regras
// sem encaminhamento e cair na [DefaultRoute] fail-safe.
func (r Route) Zero() bool { return r.Runbook == "" && r.Owner == "" }

// String dá a forma canónica "runbook@owner" (rótulo, sem segredos).
func (r Route) String() string { return r.Runbook + "@" + r.Owner }

// DefaultRoute é a rota FAIL-SAFE: um alerta sem rota configurada nunca fica órfão
// (todo o alerta liga a um runbook accionável — tecnica/10 §7, "alertas sem runbook
// são ruído"). Aponta ao runbook geral e ao owner de operação de baixo nível.
var DefaultRoute = Route{Runbook: "geral", Owner: "DevOps/SRE"}

// ---------------------------------------------------------------------------
// AlertRule / AlertConfig — regras declarativas por SLO, VERSIONADAS (fail-closed).
// ---------------------------------------------------------------------------

// AlertRule é a regra DECLARATIVA de UM SLO: qual SLI observa ([SLIValue.Name]), o
// nome de alerta estável a emitir, a severidade e a rota (runbook/owner). O LIMIAR e
// a DIRECÇÃO não se repetem aqui — vivem no [SLIValue] (o SLO de AOS-085), evitando
// dupla-fonte-de-verdade: a regra dispara sse o SLI está em breach.
type AlertRule struct {
	// SLI é o nome do SLI observado (ex. [SLICacheHitRate]).
	SLI string `json:"sli"`
	// Name é o nome de alerta ESTÁVEL emitido (ex. [AlertCacheHitRateLow]).
	Name string `json:"name"`
	// Severity é a severidade do alerta.
	Severity Severity `json:"severity"`
	// Route é o encaminhamento (runbook + owner). Vazia ⇒ [DefaultRoute] fail-safe.
	Route Route `json:"route"`
}

// AlertConfig são as regras de alerta da mediação, VERSIONADAS (SemVer, à imagem da
// [SLOConfig]) e serializáveis (JSON, stdlib — zero deps). SustainedWindows é a
// janela anti-ruído partilhada (nº de observações consecutivas em breach antes de
// disparar). É fail-closed: uma config sem sentido é rejeitada.
type AlertConfig struct {
	// Version é o SemVer do artefacto (ex.: "1.0.0"). Monótono no hot-reload.
	Version string `json:"version"`
	// SustainedWindows >= 1: nº de OBSERVAÇÕES consecutivas com o SLI em breach que
	// constitui violação SUSTENTADA (dispara). 1 = dispara à primeira observação.
	// Molde de SustainedSaturationWindows (AOS-034): evita alertar em picos
	// transitórios de uma única observação.
	SustainedWindows int `json:"sustained_windows"`
	// Rules são as regras declarativas, uma por SLO crítico.
	Rules []AlertRule `json:"rules"`
}

// DefaultAlertConfig devolve as regras por omissão dos quatro SLOs críticos de
// AOS-085, com severidades e encaminhamento cruzados com tecnica/10 §7/§8:
//
//   - cache-hit-rate baixo (cache thrash) → warning → RB-03 (observação de custo).
//     Indicador avançado: dispara antes do impacto no custo.
//   - custo por trajectória alto (explosão de custo) → critical → RB-03 (orçamento).
//   - overhead de mediação p95 alto → critical → RB-04 (mediação/PDP).
//   - override-rate alto (approval theater) → warning → RB-05 (drift de governação).
//
// Janela sustentada por omissão = 3 observações (baixo ruído, molde AOS-034).
func DefaultAlertConfig() AlertConfig {
	return AlertConfig{
		Version:          "1.0.0",
		SustainedWindows: 3,
		Rules: []AlertRule{
			{
				SLI:      SLICacheHitRate,
				Name:     AlertCacheHitRateLow,
				Severity: SevWarning,
				Route:    Route{Runbook: "RB-03", Owner: "DevOps/SRE"},
			},
			{
				SLI:      SLICostPerTrajectory,
				Name:     AlertCostPerTrajectoryHigh,
				Severity: SevCritical,
				Route:    Route{Runbook: "RB-03", Owner: "DevOps/SRE"},
			},
			{
				SLI:      SLIMediationOverheadP95,
				Name:     AlertMediationOverheadHigh,
				Severity: SevCritical,
				Route:    Route{Runbook: "RB-04", Owner: "DevOps/SRE"},
			},
			{
				SLI:      SLIOverrideRate,
				Name:     AlertOverrideRateHigh,
				Severity: SevWarning,
				Route:    Route{Runbook: "RB-05", Owner: "Governação/Segurança"},
			},
		},
	}
}

// ErrInvalidAlertConfig sinaliza uma config de alertas inválida (fail-closed).
var ErrInvalidAlertConfig = fmt.Errorf("otelgenai: config de alertas inválida")

// knownSLIs é o conjunto dos SLIs de AOS-085 que uma regra pode observar. Uma regra
// que aponte a um SLI desconhecido é rejeitada (fail-closed: nunca uma regra que
// nunca dispara por erro de nome).
var knownSLIs = map[string]bool{
	SLICacheHitRate:         true,
	SLIMediationOverheadP95: true,
	SLICostPerTrajectory:    true,
	SLIOverrideRate:         true,
}

// validSeverity aceita só as severidades do contrato.
func validSeverity(s Severity) bool { return s == SevWarning || s == SevCritical }

// validate é FAIL-CLOSED: rejeita SemVer inválido, janela < 1, regras vazias, SLI
// desconhecido, nome/severidade/rota em falta ou nomes de alerta duplicados. Um
// artefacto inválido NÃO substitui o anterior nem corre.
func (c AlertConfig) validate() error {
	if !validSemVer(c.Version) {
		return fmt.Errorf("%w: version %q não é SemVer", ErrInvalidAlertConfig, c.Version)
	}
	if c.SustainedWindows < 1 {
		return fmt.Errorf("%w: sustained_windows deve ser >= 1", ErrInvalidAlertConfig)
	}
	if len(c.Rules) == 0 {
		return fmt.Errorf("%w: sem regras", ErrInvalidAlertConfig)
	}
	seen := make(map[string]bool, len(c.Rules))
	for i, r := range c.Rules {
		if !knownSLIs[r.SLI] {
			return fmt.Errorf("%w: regra %d observa SLI desconhecido %q", ErrInvalidAlertConfig, i, r.SLI)
		}
		if r.Name == "" {
			return fmt.Errorf("%w: regra %d (%s) sem nome de alerta", ErrInvalidAlertConfig, i, r.SLI)
		}
		if !validSeverity(r.Severity) {
			return fmt.Errorf("%w: regra %d (%s) com severidade inválida %q", ErrInvalidAlertConfig, i, r.Name, r.Severity)
		}
		if r.Route.Zero() {
			return fmt.Errorf("%w: regra %d (%s) sem rota (runbook/owner)", ErrInvalidAlertConfig, i, r.Name)
		}
		if seen[r.Name] {
			return fmt.Errorf("%w: nome de alerta duplicado %q", ErrInvalidAlertConfig, r.Name)
		}
		seen[r.Name] = true
	}
	return nil
}

// Validate expõe a validação fail-closed (para o chamador rejeitar uma config antes
// de a activar). Devolve nil sse a config é válida.
func (c AlertConfig) Validate() error { return c.validate() }

// RouteFor devolve a rota da regra do alerta dado; se o alerta não tem regra (ou a
// rota está vazia), devolve a [DefaultRoute] fail-safe e ok=false. É determinista.
func (c AlertConfig) RouteFor(alertName string) (Route, bool) {
	for _, r := range c.Rules {
		if r.Name == alertName {
			if r.Route.Zero() {
				return DefaultRoute, false
			}
			return r.Route, true
		}
	}
	return DefaultRoute, false
}

// ---------------------------------------------------------------------------
// Alert — o alerta avaliado (rótulos e números, NUNCA segredos).
// ---------------------------------------------------------------------------

// Alert é um alerta avaliado a partir de um SLI. Transporta rótulos e números — o
// nome estável, a severidade, o SLI e o valor observado vs. o limiar, a rota
// (runbook/owner) e os trace_ids OFENSORES (drill-down) — NUNCA segredos nem PII.
type Alert struct {
	// Name é o nome de alerta ESTÁVEL (ex. [AlertCacheHitRateLow]).
	Name string
	// Severity é a severidade do alerta.
	Severity Severity
	// SLI é o nome do SLI que originou o alerta (ex. [SLICacheHitRate]).
	SLI string
	// Value é o valor observado do SLI (mesma unidade do SLO de AOS-085).
	Value float64
	// Threshold é o limiar-alvo (o SLO); Direction ([DirMin]/[DirMax]) diz o sentido.
	Threshold float64
	// Direction é [DirMin] (Value >= Threshold cumpre) ou [DirMax] (Value <= cumpre).
	Direction string
	// Route é o encaminhamento (runbook + owner). Nunca vazio: cai na [DefaultRoute].
	Route Route
	// Fired indica se o alerta disparou (janela sustentada satisfeita, ou breach na
	// observação única).
	Fired bool
	// Streak é o nº de observações consecutivas em breach que suportam este alerta
	// (0/1 na observação única; até SustainedWindows no avaliador sustentado).
	Streak int
	// Message é a evidência em RÓTULO (sem segredos): SLI, valor vs. limiar, rota.
	Message string
	// Offenders são os trace_ids ofensores herdados do [SLIValue.Offenders] — o
	// drill-down chega ao runbook já com o trace responsável.
	Offenders []string
}

// snapshotSLIs indexa os quatro SLIs do snapshot por nome (ordem estável irrelevante
// para o mapa; a ordem de emissão dos alertas segue as regras).
func snapshotSLIs(d DashboardSnapshot) map[string]SLIValue {
	m := make(map[string]SLIValue, 4)
	for _, s := range d.SLIs() {
		m[s.Name] = s
	}
	return m
}

// buildAlert monta o alerta-base de uma regra a partir do seu SLI, com Fired/Streak
// a definir pelo chamador. A Message é um rótulo determinista (sem segredos).
func buildAlert(r AlertRule, sli SLIValue, fired bool, streak int) Alert {
	route := r.Route
	if route.Zero() {
		route = DefaultRoute
	}
	a := Alert{
		Name:      r.Name,
		Severity:  r.Severity,
		SLI:       r.SLI,
		Value:     sli.Value,
		Threshold: sli.SLO,
		Direction: sli.Direction,
		Route:     route,
		Fired:     fired,
		Streak:    streak,
		Offenders: sli.Offenders,
	}
	cmp := "<="
	if sli.Direction == DirMin {
		cmp = ">="
	}
	a.Message = fmt.Sprintf("%s: %s observado %.4g não cumpre %s %.4g → %s",
		r.Name, r.SLI, sli.Value, cmp, sli.SLO, route.String())
	return a
}

// EvaluateAlerts avalia os alertas para uma OBSERVAÇÃO ÚNICA do snapshot: cada regra
// cuja SLI está em breach ([SLIValue.Breached]) dispara (Fired, Streak=1). É uma
// FUNÇÃO PURA e determinista (mesmo snapshot + config ⇒ mesmos alertas), sem estado,
// sem relógio. Uma config inválida cai na [DefaultAlertConfig] fail-closed. Os
// alertas vêm por ordem de nome (estável). Um SLI não avaliado (sem dados) NÃO
// dispara (nem breach nem cumprimento por vacuidade — semântica de AOS-085).
func EvaluateAlerts(d DashboardSnapshot, cfg AlertConfig) []Alert {
	if cfg.validate() != nil {
		cfg = DefaultAlertConfig()
	}
	slis := snapshotSLIs(d)
	var out []Alert
	for _, r := range cfg.Rules {
		sli, ok := slis[r.SLI]
		if !ok {
			continue
		}
		breached := sli.Breached()
		streak := 0
		if breached {
			streak = 1
		}
		out = append(out, buildAlert(r, sli, breached, streak))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FiredAlerts filtra os alertas que dispararam (query-time; preserva a ordem).
func FiredAlerts(alerts []Alert) []Alert {
	var out []Alert
	for _, a := range alerts {
		if a.Fired {
			out = append(out, a)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// AlertEvaluator — avaliação com JANELA SUSTENTADA (streak por SLI, determinista).
// ---------------------------------------------------------------------------

// AlertEvaluator avalia as regras contra observações SUCESSIVAS do dashboard,
// mantendo o STREAK por REGRA (nº de observações consecutivas em breach), avançado SÓ
// por [AlertEvaluator.Observe] — sem relógio nem aleatoriedade. Duas regras que
// partilhem o mesmo SLI têm streaks INDEPENDENTES (indexados pelo nome de alerta,
// único por validate()): cada Observe avança cada streak UMA vez, sem double-count.
// Um alerta só dispara
// (Fired) quando o breach PERSISTE SustainedWindows observações consecutivas: um
// pico transitório de uma única observação NÃO alerta (anti fadiga de alerta). Mesma
// sequência de observações ⇒ mesmos alertas (determinismo total). Segue o molde do
// SLOEvaluator do scheduler (AOS-034) com tipos próprios.
type AlertEvaluator struct {
	cfg       AlertConfig
	defaulted bool           // true se a config passada era inválida e caiu na default
	streak    map[string]int // Alert Name → observações consecutivas em breach (uma streak POR REGRA)
}

// ConfigDefaulted reporta se a [AlertConfig] passada a [NewAlertEvaluator] era
// INVÁLIDA e foi substituída pela [DefaultAlertConfig] (rotas/severidades/janela
// possivelmente diferentes das pretendidas). Torna o fallback OBSERVÁVEL — simétrico
// a [DashboardSnapshot.ConfigDefaulted] de AOS-085 — para um operador detectar que a
// sua config de alertas estrita foi rejeitada em vez de correr silenciosamente com a
// default. Para rejeição estrita antes de construir, usar [AlertConfig.Validate].
func (e *AlertEvaluator) ConfigDefaulted() bool { return e.defaulted }

// NewAlertEvaluator constrói o avaliador com uma config validada. Uma config
// inválida é substituída pela [DefaultAlertConfig] (fail-closed: nunca corre com
// regras sem sentido).
func NewAlertEvaluator(cfg AlertConfig) *AlertEvaluator {
	defaulted := false
	if cfg.validate() != nil {
		cfg = DefaultAlertConfig()
		defaulted = true
	}
	return &AlertEvaluator{cfg: cfg, defaulted: defaulted, streak: make(map[string]int)}
}

// Config devolve a config activa do avaliador.
func (e *AlertEvaluator) Config() AlertConfig { return e.cfg }

// RouteFor delega na config activa (encaminhamento AlertName→Route com fail-safe).
func (e *AlertEvaluator) RouteFor(alertName string) (Route, bool) { return e.cfg.RouteFor(alertName) }

// Observe AVANÇA o streak de cada REGRA (uma observação) e devolve os alertas — Fired
// só nas regras cujo breach atingiu SustainedWindows observações consecutivas. Uma
// regra cujo SLI deixa de estar em breach (ou fica sem dados) ZERA o seu streak. É
// determinista dada a sequência de chamadas (sem time.Now/rand). Ordena por nome.
func (e *AlertEvaluator) Observe(d DashboardSnapshot) []Alert {
	slis := snapshotSLIs(d)
	var out []Alert
	for _, r := range e.cfg.Rules {
		sli, ok := slis[r.SLI]
		if !ok {
			// SLI ausente do snapshot: trata como não-breach (zera o streak).
			e.streak[r.Name] = 0
			continue
		}
		if sli.Breached() {
			e.streak[r.Name]++
		} else {
			e.streak[r.Name] = 0
		}
		streak := e.streak[r.Name]
		fired := streak >= e.cfg.SustainedWindows
		out = append(out, buildAlert(r, sli, fired, streak))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Reset limpa o estado de streak (recomeça a avaliação sustentada do zero).
func (e *AlertEvaluator) Reset() { e.streak = make(map[string]int) }
