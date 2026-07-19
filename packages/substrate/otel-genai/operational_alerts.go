package otelgenai

// operational_alerts.go — ALERTAS OPERACIONAIS sobre os SETE SLIs canónicos
// (AOS-105, EPIC-10, tecnica/10 §7). Fecha o ciclo do dashboard-as-code de AOS-104:
// os SLIs canónicos só protegem se DISPARAREM acção. O plano de controlo em baixo, o
// cold-start a degradar-se, o headroom a esgotar-se, a fidelidade de replay a cair e
// a hash-chain do audit a quebrar passam a produzir SINAL ACCIONÁVEL — com limiar,
// severidade e ENCAMINHAMENTO para runbook — em vez de padrões só visíveis
// post-mortem.
//
// # ALERTING-AS-CODE que COMPÕE, não reimplementa
//
// Este ficheiro NÃO reimplementa nada: COMPÕE (mesmo módulo FOLHA, zero-dep)
//
//   - alerts.go (AOS-086): reutiliza [Route], [AlertRule] (embebido em
//     [OperationalAlertRule]), [Severity]/[SevWarning]/[SevCritical], [Alert],
//     [DefaultRoute] e o construtor [buildAlert]. A janela SUSTENTADA (streak por
//     regra) segue o molde de [AlertEvaluator] com os mesmos invariantes
//     deterministas.
//   - catalog.go (AOS-104): consome o [OperationalSnapshot] renderizado dos SETE
//     SLIs canónicos ([DashboardCatalog.Render]). Os alertas disparam a partir de
//     [OperationalSnapshot.Breaches] — o SLI JÁ avaliado QUERY-TIME (AC3), nunca de
//     filtragem no emit-time. Um SLI não avaliado (Samples == 0) NÃO dispara
//     (anti-vacuidade — não inventa breach).
//
// Porquê um config PRÓPRIO e não [AlertConfig]: a [AlertConfig.validate] de AOS-086
// só conhece os QUATRO SLIs da mediação (knownSLIs) e rejeitaria os sete canónicos.
// [OperationalAlertConfig] valida contra [canonicalSLIs] (AOS-104), mas REUTILIZA o
// tipo [AlertRule] e toda a maquinaria de rota/severidade/alerta — zero duplicação
// de Route/AlertRule/Alert/janela-sustentada.
//
// # Os limiares DERIVAM do SLO (AC1), não são números mágicos
//
// Nenhuma regra carrega um limiar solto: o limiar e a janela de cada alerta são o
// SLO e a Window do painel correspondente do catálogo de AOS-104 (a regra dispara
// sse [SLIValue.Breached], calculado contra o [SLIPanel.SLO]). A única política que
// vive na regra é a severidade, a rota (runbook/owner) e a janela sustentada.
//
// # RB-01 vs RB-03 no headroom (AC4, ADR-008)
//
// O SLI de headroom liga-se ao admission control (ADR-008) e DISTINGUE duas causas
// por DUAS regras/rotas (ver [classifyHeadroomCause]): headroom fixado no zero =
// COLAPSO por RATE LIMIT (a admissão bate na parede da capacidade → RB-01);
// headroom NEGATIVO (deficit) = ESGOTAMENTO de ORÇAMENTO (comprometido além do
// envelope → RB-03). Nunca se colapsam os dois num alerta genérico.
//
// # Sem órfãos, verificado no CI (AC2)
//
// [OperationalAlertConfig.Validate] é fail-closed e exige (a) uma regra para CADA um
// dos sete SLIs canónicos e (b) um Runbook NÃO-vazio em TODA a regra. O teste de CI
// falha se algum SLI ficar sem alerta OU algum alerta sem runbook.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// ---------------------------------------------------------------------------
// Runbooks e procedimentos (ids ESTÁVEIS; os runbooks em si são AOS-106/107/102).
// ---------------------------------------------------------------------------

// Os ids batem com o catálogo de runbooks de tecnica/10 §8 (os runbooks em si são
// AOS-106; os procedimentos de escala/DR são AOS-107/AOS-102). O encaminhamento
// segue a tabela de tecnica/10 §7.
const (
	// RunbookRateLimitCollapse — RB-01: colapso de rate limit agregado (headroom fixado
	// no zero, admissão a bater na parede da capacidade partilhada). ADR-008.
	RunbookRateLimitCollapse = "RB-01"
	// RunbookBudgetExhaustion — RB-03: esgotamento de orçamento em tokens/$ (headroom
	// negativo / cache thrash como indicador avançado de custo). ADR-008.
	RunbookBudgetExhaustion = "RB-03"
	// RunbookPDPFailure — RB-04: falha de PDP / degradação do plano de controlo
	// (disponibilidade do plano de controlo e overhead de mediação p95).
	RunbookPDPFailure = "RB-04"
	// RunbookAutoModRollback — RB-05: rollback de auto-modificação (misevolution/drift)
	// — a fidelidade de replay abaixo do alvo é o sinal de regressão determinista.
	RunbookAutoModRollback = "RB-05"
	// ProcScaleOut — procedimento de ESCALA (secção 5 / AOS-107): pressão do pool /
	// cold-start a subir → escalar a reserva pré-aquecida.
	ProcScaleOut = "PROC-ESCALA"
	// ProcDisasterRecovery — procedimento de DR (secção 6 / AOS-102, platform/dr):
	// quebra da hash-chain do audit WORM → DR + escalar segurança.
	ProcDisasterRecovery = "PROC-DR"
)

// Owners estáveis (rótulos de escalonamento; nunca segredos nem PII).
const (
	ownerDevOps     = "DevOps/SRE"
	ownerGovernance = "Governação/Segurança"
)

// ---------------------------------------------------------------------------
// Nomes de alerta ESTÁVEIS (contrato; um por breach roteável dos sete SLIs).
// ---------------------------------------------------------------------------

const (
	// AlertControlPlaneAvailabilityLow — disponibilidade do plano de controlo abaixo do
	// alvo (99,9%). Deriva de [SLIControlPlaneAvailability]. → RB-04 (falha de PDP).
	AlertControlPlaneAvailabilityLow = "control_plane_availability_low"
	// AlertMediationOverheadP95High — overhead de mediação p95 acima do tecto (15 ms
	// sustentado). Deriva de [SLIMediationOverheadP95]. → RB-04.
	AlertMediationOverheadP95High = "mediation_overhead_p95_high"
	// AlertSandboxColdStartP95High — cold-start de sandbox p95 acima do tecto (125 ms).
	// Deriva de [SLISandboxColdStartP95]. → escala do pool.
	AlertSandboxColdStartP95High = "sandbox_cold_start_p95_high"
	// AlertOpCacheHitRateLow — cache-hit-rate abaixo do alvo (cache thrash, indicador
	// avançado de custo). Deriva de [SLICacheHitRate]. → RB-03.
	AlertOpCacheHitRateLow = "cache_hit_rate_low_op"
	// AlertHeadroomRateLimitCollapse — headroom fixado no zero: COLAPSO por rate limit
	// (RB-01). Deriva de [SLIHeadroomTokens] com causa [CauseRateLimitCollapse]. ADR-008.
	AlertHeadroomRateLimitCollapse = "headroom_rate_limit_collapse"
	// AlertHeadroomBudgetExhaustion — headroom NEGATIVO: ESGOTAMENTO de orçamento
	// (RB-03). Deriva de [SLIHeadroomTokens] com causa [CauseBudgetExhaustion]. ADR-008.
	AlertHeadroomBudgetExhaustion = "headroom_budget_exhaustion"
	// AlertReplayFidelityLow — fidelidade de replay abaixo do alvo (falha de reprodução;
	// sinal de misevolution). Deriva de [SLIReplayFidelity]. → RB-05 + DR.
	AlertReplayFidelityLow = "replay_fidelity_low"
	// AlertAuditWORMIntegrityBroken — hash-chain do audit WORM quebrada (adulteração).
	// Deriva de [SLIAuditWORMIntegrity]. → DR + escalar segurança.
	AlertAuditWORMIntegrityBroken = "audit_worm_integrity_broken"
)

// ---------------------------------------------------------------------------
// Causa do breach de headroom (AC4) — o discriminador RB-01 vs RB-03.
// ---------------------------------------------------------------------------

// HeadroomCause é a CAUSA de um breach de headroom, o que distingue o encaminhamento
// (AC4, ADR-008). Uma regra de headroom fixa a causa que serve; a avaliação só a
// dispara quando [classifyHeadroomCause] do valor observado bate certo. As regras
// não-headroom têm causa vazia ([CauseNone]) e disparam por breach direto.
type HeadroomCause string

const (
	// CauseNone — sem discriminação de causa (regras não-headroom).
	CauseNone HeadroomCause = ""
	// CauseRateLimitCollapse — headroom fixado no zero: a admissão bate na parede da
	// capacidade (rate limit). → RB-01.
	CauseRateLimitCollapse HeadroomCause = "rate_limit_collapse"
	// CauseBudgetExhaustion — headroom NEGATIVO (deficit): comprometido além do
	// envelope de orçamento. → RB-03.
	CauseBudgetExhaustion HeadroomCause = "budget_exhaustion"
)

// classifyHeadroomCause mapeia o valor observado do headroom (mínimo free_tokens
// reservável) na causa do breach, determinista e derivada do sinal de admission
// control (ADR-008). Só se aplica quando o SLI já está em breach (value < SLO de
// reserva): value == 0 (parede da capacidade, admissões estranguladas) ⇒ colapso por
// RATE LIMIT (RB-01); value < 0 (deficit, sobre-comprometido além do orçamento) ⇒
// ESGOTAMENTO de orçamento (RB-03). Assim os dois modos NÃO colapsam num alerta
// genérico.
func classifyHeadroomCause(value float64) HeadroomCause {
	if value < 0 {
		return CauseBudgetExhaustion
	}
	return CauseRateLimitCollapse
}

// ---------------------------------------------------------------------------
// OperationalAlertRule / OperationalAlertConfig — alerting-as-code VERSIONADO.
// ---------------------------------------------------------------------------

// OperationalAlertRule é a regra declarativa de UM alerta sobre um SLI canónico. EMBEBE
// [AlertRule] (SLI, Name, Severity, Route — reutilizados de AOS-086, sem duplicação) e
// acrescenta o discriminador de causa (AC4) e uma janela sustentada POR REGRA
// (opcional). O limiar e a direcção NÃO vivem aqui — vivem no painel do catálogo
// (AOS-104): a regra dispara sse o [SLIValue] do painel está em breach.
type OperationalAlertRule struct {
	AlertRule
	// Cause é o discriminador de causa do headroom ([CauseRateLimitCollapse]/
	// [CauseBudgetExhaustion]); vazio ([CauseNone]) nas restantes (dispara por breach
	// direto). Só uma regra com a causa correspondente ao valor observado dispara (AC4).
	Cause HeadroomCause `json:"cause,omitempty"`
	// SustainedWindows é a janela sustentada ESPECÍFICA desta regra (nº de observações
	// consecutivas em breach antes de disparar). 0 ⇒ herda [OperationalAlertConfig.SustainedWindows].
	// Os alertas ACUTOS (audit WORM quebrado, falha de reprodução, plano de controlo em
	// baixo) usam 1 (dispara à primeira); os indicadores ruidosos (overhead/cold-start/
	// cache p95) usam janela maior — a janela deriva da natureza do SLO (AC1).
	SustainedWindows int `json:"sustained_windows,omitempty"`
}

// effectiveWindow devolve a janela sustentada efectiva da regra (a específica, ou a
// default do config; nunca < 1).
func (r OperationalAlertRule) effectiveWindow(configDefault int) int {
	w := r.SustainedWindows
	if w <= 0 {
		w = configDefault
	}
	if w < 1 {
		w = 1
	}
	return w
}

// matchesCause indica se a regra é aplicável ao SLI em breach: as regras não-headroom
// ([CauseNone]) aplicam-se sempre; as de headroom só quando [classifyHeadroomCause] do
// valor observado é a causa da regra (AC4).
func (r OperationalAlertRule) matchesCause(sli SLIValue) bool {
	if r.Cause == CauseNone {
		return true
	}
	return classifyHeadroomCause(sli.Value) == r.Cause
}

// OperationalAlertConfig é o catálogo de alertas operacionais VERSIONADO (SemVer, à
// imagem de [AlertConfig]/[DashboardCatalog]) e serializável (JSON, stdlib — zero
// deps). Fail-closed: uma config sem sentido é rejeitada. SustainedWindows é a janela
// anti-ruído partilhada (default por regra sem janela própria).
type OperationalAlertConfig struct {
	// Version é o SemVer do artefacto (ex.: "1.0.0").
	Version string `json:"version"`
	// SustainedWindows >= 1: janela sustentada DEFAULT (nº de observações consecutivas
	// em breach) para as regras sem janela própria.
	SustainedWindows int `json:"sustained_windows"`
	// Rules são as regras declarativas — pelo menos uma por SLI canónico (AC1/AC2).
	Rules []OperationalAlertRule `json:"rules"`
}

// DefaultOperationalAlertConfig devolve o catálogo de referência: uma regra por CADA
// um dos SETE SLIs canónicos de AOS-104 (AC1), cada uma ligada a um runbook accionável
// (AC2), com o headroom desdobrado em RB-01 (colapso/rate limit) vs RB-03 (esgotamento
// de orçamento) (AC4). Os limiares/janelas derivam do SLO+Window dos painéis do
// catálogo — as regras só carregam severidade, rota e janela sustentada.
func DefaultOperationalAlertConfig() OperationalAlertConfig {
	rule := func(sli, name string, sev Severity, runbook, owner string, cause HeadroomCause, window int) OperationalAlertRule {
		return OperationalAlertRule{
			AlertRule: AlertRule{
				SLI:      sli,
				Name:     name,
				Severity: sev,
				Route:    Route{Runbook: runbook, Owner: owner},
			},
			Cause:            cause,
			SustainedWindows: window,
		}
	}
	return OperationalAlertConfig{
		Version:          "1.0.0",
		SustainedWindows: 3,
		Rules: []OperationalAlertRule{
			// Plano de controlo em baixo ⇒ RB-04 (falha de PDP), acuto: janela 1.
			rule(SLIControlPlaneAvailability, AlertControlPlaneAvailabilityLow, SevCritical,
				RunbookPDPFailure, ownerDevOps, CauseNone, 1),
			// Overhead de mediação p95 alto ⇒ RB-04 (sustentado: p95 ruidoso).
			rule(SLIMediationOverheadP95, AlertMediationOverheadP95High, SevCritical,
				RunbookPDPFailure, ownerDevOps, CauseNone, 3),
			// Cold-start de sandbox alto ⇒ escala do pool (sustentado).
			rule(SLISandboxColdStartP95, AlertSandboxColdStartP95High, SevWarning,
				ProcScaleOut, ownerDevOps, CauseNone, 3),
			// Cache thrash ⇒ RB-03 (indicador avançado de custo; sustentado).
			rule(SLICacheHitRate, AlertOpCacheHitRateLow, SevWarning,
				RunbookBudgetExhaustion, ownerDevOps, CauseNone, 3),
			// Headroom fixado no zero ⇒ COLAPSO por rate limit (RB-01, ADR-008).
			rule(SLIHeadroomTokens, AlertHeadroomRateLimitCollapse, SevCritical,
				RunbookRateLimitCollapse, ownerDevOps, CauseRateLimitCollapse, 2),
			// Headroom NEGATIVO ⇒ ESGOTAMENTO de orçamento (RB-03, ADR-008).
			rule(SLIHeadroomTokens, AlertHeadroomBudgetExhaustion, SevCritical,
				RunbookBudgetExhaustion, ownerDevOps, CauseBudgetExhaustion, 2),
			// Falha de reprodução ⇒ RB-05 (misevolution/rollback), acuto: janela 1.
			rule(SLIReplayFidelity, AlertReplayFidelityLow, SevCritical,
				RunbookAutoModRollback, ownerDevOps, CauseNone, 1),
			// Quebra da hash-chain do audit ⇒ DR + escalar segurança (acuto: adulteração).
			rule(SLIAuditWORMIntegrity, AlertAuditWORMIntegrityBroken, SevCritical,
				ProcDisasterRecovery, ownerGovernance, CauseNone, 1),
		},
	}
}

// ErrInvalidOperationalAlertConfig sinaliza uma config de alertas operacionais inválida.
var ErrInvalidOperationalAlertConfig = fmt.Errorf("otelgenai: config de alertas operacionais inválida")

// canonicalSLISet é o conjunto dos sete SLIs canónicos (índice de [canonicalSLIs] de
// AOS-104) para a verificação de cobertura fail-closed.
func canonicalSLISet() map[string]bool {
	set := make(map[string]bool, len(canonicalSLIs))
	for _, s := range canonicalSLIs {
		set[s] = true
	}
	return set
}

// validate é FAIL-CLOSED e é a raiz do teste de NÃO-ÓRFÃOS (AC2, verificado no CI):
// rejeita SemVer inválido, janela < 1, regras vazias, SLI fora dos sete canónicos,
// nome/severidade em falta, nomes de alerta duplicados, e — os dois invariantes de
// AOS-105 —
//
//	(a) TODA a regra tem Route.Runbook NÃO-vazio (nenhum alerta órfão);
//	(b) CADA um dos sete SLIs canónicos tem PELO MENOS uma regra (cobertura completa).
//
// Remover o runbook de uma regra OU remover a única regra de um SLI faz esta validação
// (e o teste de CI) FALHAR.
func (c OperationalAlertConfig) validate() error {
	if !validSemVer(c.Version) {
		return fmt.Errorf("%w: version %q não é SemVer", ErrInvalidOperationalAlertConfig, c.Version)
	}
	if c.SustainedWindows < 1 {
		return fmt.Errorf("%w: sustained_windows deve ser >= 1", ErrInvalidOperationalAlertConfig)
	}
	if len(c.Rules) == 0 {
		return fmt.Errorf("%w: sem regras", ErrInvalidOperationalAlertConfig)
	}
	canonical := canonicalSLISet()
	covered := make(map[string]bool, len(canonical))
	seenName := make(map[string]bool, len(c.Rules))
	for i, r := range c.Rules {
		if !canonical[r.SLI] {
			return fmt.Errorf("%w: regra %d observa SLI não-canónico %q", ErrInvalidOperationalAlertConfig, i, r.SLI)
		}
		if r.Name == "" {
			return fmt.Errorf("%w: regra %d (%s) sem nome de alerta", ErrInvalidOperationalAlertConfig, i, r.SLI)
		}
		if !validSeverity(r.Severity) {
			return fmt.Errorf("%w: regra %d (%s) com severidade inválida %q", ErrInvalidOperationalAlertConfig, i, r.Name, r.Severity)
		}
		// (a) NÃO-ÓRFÃOS: runbook obrigatório em toda a regra (Route.Owner sozinho não
		// basta — um alerta sem runbook accionável é ruído, tecnica/10 §7).
		if r.Route.Runbook == "" {
			return fmt.Errorf("%w: regra %d (%s) sem runbook (alerta órfão)", ErrInvalidOperationalAlertConfig, i, r.Name)
		}
		if r.SustainedWindows < 0 {
			return fmt.Errorf("%w: regra %d (%s) com sustained_windows negativo", ErrInvalidOperationalAlertConfig, i, r.Name)
		}
		if seenName[r.Name] {
			return fmt.Errorf("%w: nome de alerta duplicado %q", ErrInvalidOperationalAlertConfig, r.Name)
		}
		seenName[r.Name] = true
		covered[r.SLI] = true
	}
	// (b) COBERTURA: cada SLI canónico tem de ter regra.
	for _, want := range canonicalSLIs {
		if !covered[want] {
			return fmt.Errorf("%w: SLI canónico %q sem regra de alerta (órfão)", ErrInvalidOperationalAlertConfig, want)
		}
	}
	return nil
}

// Validate expõe a validação fail-closed (para o chamador/CI rejeitar uma config antes
// de a activar). Devolve nil sse a config é válida (sem SLIs órfãos, sem alertas sem
// runbook).
func (c OperationalAlertConfig) Validate() error { return c.validate() }

// JSON serializa o catálogo de alertas em JSON indentado e determinista (ordem das
// regras preservada, sem escape HTML) — o artefacto versionado no repo prova a
// reprodutibilidade (AC5). Molde de [DashboardCatalog.JSON].
func (c OperationalAlertConfig) JSON() ([]byte, error) {
	type alias OperationalAlertConfig
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(alias(c)); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// LoadOperationalAlertConfig desserializa e VALIDA um catálogo de alertas de JSON
// (fail-closed): um JSON malformado, um SLI órfão ou um alerta sem runbook é rejeitado
// — o chamador nunca cai num conjunto de alertas incompleto/permissivo. É o outro lado
// do round-trip de [OperationalAlertConfig.JSON].
func LoadOperationalAlertConfig(raw []byte) (OperationalAlertConfig, error) {
	var c OperationalAlertConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return OperationalAlertConfig{}, fmt.Errorf("%w: JSON: %v", ErrInvalidOperationalAlertConfig, err)
	}
	if err := c.validate(); err != nil {
		return OperationalAlertConfig{}, err
	}
	return c, nil
}

// RouteFor devolve a rota da regra do alerta dado; se o alerta não tem regra (ou o
// runbook está vazio), devolve a [DefaultRoute] fail-safe e ok=false. Determinista.
func (c OperationalAlertConfig) RouteFor(alertName string) (Route, bool) {
	for _, r := range c.Rules {
		if r.Name == alertName {
			if r.Route.Runbook == "" {
				return DefaultRoute, false
			}
			return r.Route, true
		}
	}
	return DefaultRoute, false
}

// ---------------------------------------------------------------------------
// Avaliação QUERY-TIME sobre os sete SLIs (AC3) — observação única.
// ---------------------------------------------------------------------------

// panelIndex indexa os painéis renderizados por nome de SLI (query-time; a fonte já
// avaliada vs. o SLO do painel).
func panelIndex(snap OperationalSnapshot) map[string]RenderedPanel {
	m := make(map[string]RenderedPanel, len(snap.Panels))
	for _, rp := range snap.Panels {
		m[rp.Panel.SLI] = rp
	}
	return m
}

// EvaluateOperationalAlerts avalia os alertas para uma OBSERVAÇÃO ÚNICA do
// [OperationalSnapshot] renderizado dos sete SLIs (AC3): cada regra cuja SLI está em
// breach ([SLIValue.Breached]) — e, para o headroom, cuja CAUSA bate certo (AC4) —
// dispara (Fired, Streak=1). É uma FUNÇÃO PURA e determinista (mesmo snapshot + config
// ⇒ mesmos alertas), sem estado nem relógio. Uma config inválida cai na
// [DefaultOperationalAlertConfig] fail-closed. Um SLI não avaliado (Samples == 0) NÃO
// dispara (anti-vacuidade — nunca inventa breach). Os alertas vêm por ordem de nome.
func EvaluateOperationalAlerts(snap OperationalSnapshot, cfg OperationalAlertConfig) []Alert {
	if cfg.validate() != nil {
		cfg = DefaultOperationalAlertConfig()
	}
	panels := panelIndex(snap)
	var out []Alert
	for _, r := range cfg.Rules {
		rp, ok := panels[r.SLI]
		if !ok {
			continue
		}
		fired := rp.SLI.Breached() && r.matchesCause(rp.SLI)
		streak := 0
		if fired {
			streak = 1
		}
		out = append(out, buildAlert(r.AlertRule, rp.SLI, fired, streak))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ---------------------------------------------------------------------------
// OperationalAlertEvaluator — janela SUSTENTADA (streak por regra, determinista).
// ---------------------------------------------------------------------------

// OperationalAlertEvaluator avalia as regras contra observações SUCESSIVAS do
// [OperationalSnapshot], mantendo o STREAK por REGRA (nº de observações consecutivas em
// breach applicável), avançado SÓ por [OperationalAlertEvaluator.Observe] — sem relógio
// nem aleatoriedade. Segue o molde de [AlertEvaluator] (AOS-086): um alerta só dispara
// (Fired) quando o breach PERSISTE a janela sustentada da regra ([OperationalAlertRule.effectiveWindow])
// observações consecutivas — um pico transitório NÃO alerta (anti fadiga). Para o
// headroom, o streak só avança na regra cuja CAUSA bate certo (AC4): mudar de causa
// (zero → deficit) ZERA o streak da regra anterior e começa o da nova. Mesma sequência
// de observações ⇒ mesmos alertas (determinismo total).
type OperationalAlertEvaluator struct {
	cfg       OperationalAlertConfig
	defaulted bool
	streak    map[string]int // Alert Name → observações consecutivas em breach aplicável
}

// NewOperationalAlertEvaluator constrói o avaliador com uma config validada. Uma config
// inválida é substituída pela [DefaultOperationalAlertConfig] (fail-closed: nunca corre
// com regras órfãs/sem sentido).
func NewOperationalAlertEvaluator(cfg OperationalAlertConfig) *OperationalAlertEvaluator {
	defaulted := false
	if cfg.validate() != nil {
		cfg = DefaultOperationalAlertConfig()
		defaulted = true
	}
	return &OperationalAlertEvaluator{cfg: cfg, defaulted: defaulted, streak: make(map[string]int)}
}

// ConfigDefaulted reporta se a config passada era INVÁLIDA e foi substituída pela
// default (torna o fallback OBSERVÁVEL, simétrico a [AlertEvaluator.ConfigDefaulted]).
func (e *OperationalAlertEvaluator) ConfigDefaulted() bool { return e.defaulted }

// Config devolve a config activa do avaliador.
func (e *OperationalAlertEvaluator) Config() OperationalAlertConfig { return e.cfg }

// RouteFor delega na config activa (encaminhamento AlertName→Route com fail-safe).
func (e *OperationalAlertEvaluator) RouteFor(alertName string) (Route, bool) {
	return e.cfg.RouteFor(alertName)
}

// Observe AVANÇA o streak de cada regra (uma observação) e devolve os alertas — Fired
// só nas regras cujo breach aplicável atingiu a janela sustentada da regra. Uma regra
// cujo SLI deixa de estar em breach (recupera), fica sem dados, ou cuja causa deixa de
// bater certo ZERA o seu streak. É determinista dada a sequência de chamadas. Ordena
// por nome.
func (e *OperationalAlertEvaluator) Observe(snap OperationalSnapshot) []Alert {
	panels := panelIndex(snap)
	var out []Alert
	for _, r := range e.cfg.Rules {
		rp, ok := panels[r.SLI]
		if !ok {
			e.streak[r.Name] = 0
			out = append(out, buildAlert(r.AlertRule, SLIValue{Name: r.SLI}, false, 0))
			continue
		}
		active := rp.SLI.Breached() && r.matchesCause(rp.SLI)
		if active {
			e.streak[r.Name]++
		} else {
			e.streak[r.Name] = 0
		}
		streak := e.streak[r.Name]
		fired := streak >= r.effectiveWindow(e.cfg.SustainedWindows)
		out = append(out, buildAlert(r.AlertRule, rp.SLI, fired, streak))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Reset limpa o estado de streak (recomeça a avaliação sustentada do zero).
func (e *OperationalAlertEvaluator) Reset() { e.streak = make(map[string]int) }

// ---------------------------------------------------------------------------
// Controlo de ruído (AC6) — AGRUPAMENTO por plano + SUPRESSÃO de correlacionados
// que NÃO esconde padrões sistémicos.
// ---------------------------------------------------------------------------

// AlertGroup é um grupo de alertas DISPARADOS correlacionados pelo mesmo plano
// operacional (controlo/dados) — a unidade de controlo de ruído (AC6). Os CRITICAL
// nunca são suprimidos; os WARNING correlacionados a um CRITICAL do MESMO plano são
// suprimidos como ruído downstream, MAS o [AlertGroup.Summary] reporta sempre a
// contagem TOTAL (incluindo os suprimidos) — a supressão reduz páginas, nunca esconde
// o padrão sistémico.
type AlertGroup struct {
	// Plane é o plano do grupo ([PlaneControl]/[PlaneData]); "" para SLIs sem plano
	// conhecido (grupo residual).
	Plane Plane
	// Active são os alertas SURFACED (todos os CRITICAL; os WARNING quando não há
	// CRITICAL no plano), ordenados por nome.
	Active []Alert
	// Suppressed são os WARNING suprimidos por correlação com um CRITICAL do mesmo plano
	// (ruído downstream), ordenados por nome. Nunca contém CRITICAL.
	Suppressed []Alert
	// Summary é o resumo textual do grupo (rótulo, sem segredos): sempre com a contagem
	// TOTAL, para o padrão sistémico permanecer visível mesmo com supressão.
	Summary string
}

// PlaneIndexOf constrói o mapa SLI→plano a partir de um snapshot renderizado (a fonte
// do agrupamento por plano, AC6/AC1).
func PlaneIndexOf(snap OperationalSnapshot) map[string]Plane {
	m := make(map[string]Plane, len(snap.Panels))
	for _, rp := range snap.Panels {
		m[rp.Panel.SLI] = rp.Panel.Plane
	}
	return m
}

// GroupAndSuppress agrupa os alertas DISPARADOS por plano e suprime os WARNING
// correlacionados a um CRITICAL do mesmo plano (AC6, controlo de ruído). Recebe SÓ os
// alertas já disparados (usar [FiredAlerts]) e o mapa SLI→plano (de [PlaneIndexOf]).
// INVARIANTES anti-ocultação de padrões sistémicos:
//
//   - um CRITICAL NUNCA é suprimido (fica sempre em Active);
//   - a supressão só apanha WARNING de um plano que JÁ tem um CRITICAL (ruído
//     downstream do mesmo incidente); sem CRITICAL no plano, os WARNING ficam activos;
//   - o Summary reporta sempre a contagem TOTAL (crit/warn/suprimidos) — o resumo do
//     grupo mantém o padrão visível mesmo quando os detalhes são suprimidos.
//
// É determinista (grupos ordenados por plano; alertas por nome).
func GroupAndSuppress(fired []Alert, planeOf map[string]Plane) []AlertGroup {
	byPlane := make(map[Plane][]Alert)
	var planeOrder []Plane
	for _, a := range fired {
		if !a.Fired {
			continue // defensivo: só alertas disparados entram no controlo de ruído.
		}
		pl := planeOf[a.SLI] // "" se desconhecido (grupo residual determinista).
		if _, seen := byPlane[pl]; !seen {
			planeOrder = append(planeOrder, pl)
		}
		byPlane[pl] = append(byPlane[pl], a)
	}
	sort.Slice(planeOrder, func(i, j int) bool { return planeOrder[i] < planeOrder[j] })

	var groups []AlertGroup
	for _, pl := range planeOrder {
		members := byPlane[pl]
		sort.Slice(members, func(i, j int) bool { return members[i].Name < members[j].Name })

		nCrit := 0
		for _, a := range members {
			if a.Severity == SevCritical {
				nCrit++
			}
		}
		g := AlertGroup{Plane: pl}
		for _, a := range members {
			// CRITICAL nunca suprimido; WARNING suprimido só se há um CRITICAL no plano.
			if a.Severity != SevCritical && nCrit > 0 {
				g.Suppressed = append(g.Suppressed, a)
			} else {
				g.Active = append(g.Active, a)
			}
		}
		g.Summary = fmt.Sprintf("%s: %d critical, %d warning (%d suprimido) — %d activo",
			planeLabel(pl), nCrit, len(members)-nCrit, len(g.Suppressed), len(g.Active))
		groups = append(groups, g)
	}
	return groups
}

// GroupFiredAlerts é a conveniência que corre o controlo de ruído directamente sobre um
// snapshot: filtra os disparados de um round de avaliação e agrupa/suprime por plano
// (AC6). Equivale a GroupAndSuppress(FiredAlerts(alerts), PlaneIndexOf(snap)).
func GroupFiredAlerts(alerts []Alert, snap OperationalSnapshot) []AlertGroup {
	return GroupAndSuppress(FiredAlerts(alerts), PlaneIndexOf(snap))
}

// planeLabel dá o rótulo textual de um plano (o residual "" fica "desconhecido").
func planeLabel(p Plane) string {
	if p == "" {
		return "desconhecido"
	}
	return string(p)
}
