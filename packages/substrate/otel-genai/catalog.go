package otelgenai

// catalog.go — DASHBOARDS OPERACIONAIS por PLANO (controlo vs dados) e por RUN, como
// DASHBOARD-AS-CODE (AOS-104, tecnica/10 §7). Estende slo.go/wide_event.go/
// cost_aggregation.go SEM alterar a sua API pública: um CATÁLOGO versionado de
// painéis de SLI (descritor estático, JSON-serializável, round-trip reproduzível)
// mais a RENDERIZAÇÃO query-time desses painéis sobre os wide events/spans JÁ
// emitidos. Nenhuma instrumentação nova, nenhuma filtragem no emit-time.
//
// # Módulo FOLHA — só os NOMES dos produtores, nunca os módulos
//
// Os SLIs canónicos são produzidos por subsistemas que este módulo NÃO pode
// importar (sandbox, scheduler, replay, audit, reference-monitor) sob pena de
// partir o zero-dep. Seguindo o mesmo padrão de slo.go/semconv.go ("reimplementado
// localmente — módulo FOLHA"), as strings canónicas de métrica desses produtores
// são REPLICADAS aqui (byte-idênticas às fontes reais) e lidas do bag do wide event
// no query-time. Nunca se importa o produtor — só se consome o NOME que ele emite.
//
// # Os sete SLIs canónicos (AC2) e o mapeamento de PLANO (AC1)
//
// Dois já existem em slo.go e são REUTILIZADOS via os seus derivadores puros
// (cache-hit-rate, overhead de mediação p95). Os restantes cinco derivam-se aqui a
// partir dos wide events (cold-start de sandbox, headroom de tokens/$, fidelidade de
// replay) ou são INJECTADOS honestamente quando não há produtor (disponibilidade do
// plano de controlo, integridade do audit WORM — ver [OperationalInputs]).
//
// Cada painel declara o seu PLANO (controlo/dados), coerente com os rótulos
// aos.plane=control/data da IaC de AOS-098 (o atributo aos.plane não existe no span;
// o plano é um mapeamento ESTÁTICO produtor→plano, não uma dimensão lida do bag).
//
// # Anti-vacuidade e fail-closed herdados
//
// A renderização reutiliza a semântica de [SLIValue]: um painel sem amostras fica
// com Samples == 0 (não avaliado — nem breach nem cumprimento por vacuidade). O
// catálogo é VERSIONADO (SemVer) e valida-se fail-closed (molde de [SLOConfig]).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Plano operacional (AC1) — dimensão estática controlo vs dados.
// ---------------------------------------------------------------------------

// Plane é o plano operacional de um painel: o plano de CONTROLO (decide — admission,
// escalonamento, PDP, mediação) ou o plano de DADOS (executa e regista — workers,
// sandbox, Event Store, audit). É coerente com os rótulos aos.plane=control/data da
// IaC de AOS-098. NÃO é um atributo lido do span (não existe aos.plane no
// vocabulário): é um mapeamento ESTÁTICO produtor→plano, fixado neste catálogo.
type Plane string

const (
	// PlaneControl — plano de controlo (baixo volume, alta criticidade de decisão).
	PlaneControl Plane = "control"
	// PlaneData — plano de dados (workers stateless, sandbox, Event Store, audit).
	PlaneData Plane = "data"
)

// valid indica se p é um plano conhecido.
func (p Plane) valid() bool { return p == PlaneControl || p == PlaneData }

// Produtores canónicos dos SLIs (rótulos estáveis; NUNCA se importam os módulos).
// Usados só pelo mapeamento estático [PlaneForProducer].
const (
	// ProducerReferenceMonitor — o Reference Monitor (mediação de tool calls).
	ProducerReferenceMonitor = "reference-monitor"
	// ProducerScheduler — o Escalonador/admission control (headroom de tokens/$).
	ProducerScheduler = "scheduler"
	// ProducerPDP — o Policy Decision Point (disponibilidade do plano de controlo).
	ProducerPDP = "pdp"
	// ProducerSandbox — o pool de microVMs pré-aquecidas (cold-start).
	ProducerSandbox = "sandbox"
	// ProducerWorker — os workers stateless / replay determinista (fidelidade de replay).
	ProducerWorker = "worker"
	// ProducerAudit — o audit WORM encadeado (integridade da hash-chain).
	ProducerAudit = "audit"
	// ProducerModelGateway — o Model Gateway (cache-hit-rate de prompt).
	ProducerModelGateway = "model-gateway"
)

// producerPlane é o mapeamento ESTÁTICO produtor→plano (AOS-104 item 3): os que
// DECIDEM ficam no plano de controlo; os que EXECUTAM/REGISTAM no plano de dados.
var producerPlane = map[string]Plane{
	ProducerReferenceMonitor: PlaneControl,
	ProducerScheduler:        PlaneControl,
	ProducerPDP:              PlaneControl,
	ProducerSandbox:          PlaneData,
	ProducerWorker:           PlaneData,
	ProducerAudit:            PlaneData,
	ProducerModelGateway:     PlaneData,
}

// PlaneForProducer devolve o plano do produtor e ok=false se o produtor é
// desconhecido (nesse caso devolve "" — o chamador decide). Determinista.
func PlaneForProducer(producer string) (Plane, bool) {
	p, ok := producerPlane[producer]
	return p, ok
}

// ---------------------------------------------------------------------------
// Strings canónicas dos produtores (REPLICADAS — módulo FOLHA, nunca importadas).
// ---------------------------------------------------------------------------

const (
	// MetricSandboxColdStartMs — aos.sandbox.cold_start_ms: o cold-start de uma reserva
	// de microVM, em MILISSEGUNDOS. Replicado de substrate/sandbox/metrics.go
	// (MetricColdStart) — o SLI lê-o do bag do wide event. Alvo < 125 ms (ADR-004).
	MetricSandboxColdStartMs = "aos.sandbox.cold_start_ms"
	// MetricSandboxPoolOccupancy — aos.sandbox.pool_occupancy: ocupação do pool
	// (pressão). Replicado de substrate/sandbox/metrics.go (MetricPoolOccupancy).
	MetricSandboxPoolOccupancy = "aos.sandbox.pool_occupancy"
	// MetricReplayFidelity — aos.replay.fidelity: fracção [0,1] de turnos verificados
	// cujo resultado reproduz. Replicado de kernel/agent-runtime/replay/engine.go
	// (AttrReplayFidelity) — no span "replay", ligado por aos.run_id. Alvo 1.0.
	MetricReplayFidelity = "aos.replay.fidelity"
	// MetricHeadroomFreeTokens — aos.scheduler.headroom.free_tokens: headroom de tokens
	// RESERVÁVEL do admission control. Replicado de control-plane/scheduler/metrics.go
	// (MetricHeadroomFreeTokens). Alvo: >= limiar de reserva (headroom > 0 reservável).
	MetricHeadroomFreeTokens = "aos.scheduler.headroom.free_tokens"

	// MetricControlPlaneAvailability — aos.control_plane.availability: disponibilidade
	// do plano de controlo (fracção [0,1]). NÃO há produtor dedicado (LACUNA honesta,
	// AOS-104 item 6a): o catálogo define o SLOT e o VALOR é INJECTADO por um
	// heartbeat/health fornecido pelo chamador ([OperationalInputs.ControlPlaneAvailability]).
	// Sem valor injectado ⇒ Samples == 0 (não avaliado) até ser cablado. Alvo 99,9%.
	MetricControlPlaneAvailability = "aos.control_plane.availability"
	// MetricAuditWORMIntegrity — aos.audit.worm_integrity: integridade da hash-chain do
	// audit WORM (booleano íntegro/adulterado). NÃO há métrica pré-emitida (LACUNA
	// honesta, AOS-104 item 6b): é o resultado de platform/audit Verify(), uma FUNÇÃO
	// sob-demanda, INJECTADO por [OperationalInputs.AuditWORMIntact]. O binding a
	// audit.Verify é wiring de composition-root (diferido). Sem valor ⇒ não avaliado.
	MetricAuditWORMIntegrity = "aos.audit.worm_integrity"

	// AttrTenantAlt — aos.tenant: a chave de tenant ALTERNATIVA que o Model Gateway
	// (metering/cache_sli, metering/cost) emite, DIVERGENTE de [AttrTenantID]
	// (aos.tenant_id, a chave de otel-genai). A agregação de custo por tenant
	// RECONCILIA ambas (ver [TenantOf]): lê aos.tenant_id e, na sua ausência, aos.tenant.
	AttrTenantAlt = "aos.tenant"
)

// ---------------------------------------------------------------------------
// Nomes ESTÁVEIS dos SLIs canónicos ADICIONAIS (os dois de slo.go reutilizam-se:
// SLICacheHitRate, SLIMediationOverheadP95).
// ---------------------------------------------------------------------------

const (
	// SLIControlPlaneAvailability — disponibilidade do plano de controlo (injectado).
	SLIControlPlaneAvailability = "control_plane_availability"
	// SLISandboxColdStartP95 — p95 do cold-start de sandbox (ms), dos wide events.
	SLISandboxColdStartP95 = "sandbox_cold_start_p95"
	// SLIHeadroomTokens — headroom de tokens reservável (do scheduler).
	SLIHeadroomTokens = "headroom_tokens"
	// SLIReplayFidelity — fidelidade de replay (fracção [0,1]).
	SLIReplayFidelity = "replay_fidelity"
	// SLIAuditWORMIntegrity — integridade do audit WORM (booleano, injectado).
	SLIAuditWORMIntegrity = "audit_worm_integrity"
)

// Unidades estáveis de um painel (rótulo de apresentação; a fonte-de-verdade é o
// Value numérico de [SLIValue]).
const (
	// UnitFraction — fracção [0,1] (cache-hit, override, disponibilidade, fidelidade).
	UnitFraction = "fraction"
	// UnitNanos — nanossegundos (overhead de mediação p95).
	UnitNanos = "nanos"
	// UnitMillis — milissegundos (cold-start de sandbox).
	UnitMillis = "ms"
	// UnitMicroUSD — micro-USD inteiro (custo por trajectória).
	UnitMicroUSD = "micro_usd"
	// UnitTokens — tokens (headroom reservável).
	UnitTokens = "tokens"
	// UnitBool — booleano 0/1 (integridade do audit WORM).
	UnitBool = "bool"
)

// ---------------------------------------------------------------------------
// SLIPanel — o descritor VERSIONÁVEL de UM painel (dashboard-as-code, AC5/AC6).
// ---------------------------------------------------------------------------

// SLIPanel é o descritor ESTÁTICO de um painel de SLI: o que se visualiza, em que
// PLANO, com que SLO e em que JANELA de avaliação (AC5). NÃO carrega valores vivos —
// é dashboard-as-code, serializável e reproduzível (AC6). O valor observado produz-se
// na renderização ([DashboardCatalog.Render]) sobre os wide events/spans. Só rótulos
// e limiares — nunca segredos nem PII.
type SLIPanel struct {
	// SLI é o nome ESTÁVEL do SLI (ex. [SLICacheHitRate]).
	SLI string `json:"sli"`
	// Title é o título legível do painel.
	Title string `json:"title"`
	// Plane é o plano do painel ([PlaneControl]/[PlaneData]).
	Plane Plane `json:"plane"`
	// Producer é o produtor canónico do sinal (rótulo; nunca se importa).
	Producer string `json:"producer"`
	// MetricSource é a string canónica de métrica/atributo que o painel consome (o NOME
	// que o produtor emite; ex. [MetricSandboxColdStartMs]). Vazio para os SLIs
	// derivados de spans/decisões sem métrica dedicada.
	MetricSource string `json:"metric_source,omitempty"`
	// Unit é a unidade de apresentação ([UnitFraction], [UnitNanos], ...).
	Unit string `json:"unit"`
	// Direction é [DirMin] (Value >= SLO cumpre) ou [DirMax] (Value <= SLO cumpre).
	Direction string `json:"direction"`
	// SLO é o limiar-alvo do painel (mesma unidade de Value). É a FONTE do limiar usado
	// na renderização — o painel indica o seu SLO (AC5).
	SLO float64 `json:"slo"`
	// Window é o rótulo da JANELA de avaliação (ex. "5m", "per_run", "continuous") —
	// o painel indica a sua janela (AC5).
	Window string `json:"window"`
	// Injected marca os dois SLIs SEM produtor (disponibilidade do plano de controlo,
	// integridade do audit WORM): o valor é injectado honestamente pelo chamador, não
	// fabricado. Anti-vacuidade: sem injecção ⇒ não avaliado.
	Injected bool `json:"injected,omitempty"`
	// Description é a nota de apresentação (rótulo, sem segredos).
	Description string `json:"description,omitempty"`
}

// ---------------------------------------------------------------------------
// DashboardCatalog — o catálogo VERSIONADO dos painéis (AC6, round-trip JSON).
// ---------------------------------------------------------------------------

// DashboardCatalog é o dashboard-as-code: o conjunto VERSIONADO (SemVer) de painéis
// de SLI, serializável para JSON e reproduzível por round-trip (AC6). Molde de
// [SLOConfig]: validação fail-closed, JSON stdlib (zero-dep). É a fonte única do
// descritor de dashboards — os planos e as vistas por run renderizam-se a partir dele.
type DashboardCatalog struct {
	// Version é o SemVer do artefacto de dashboard (ex.: "1.0.0").
	Version string `json:"version"`
	// Panels são os painéis de SLI (um por SLI canónico), ordem estável.
	Panels []SLIPanel `json:"panels"`
}

// canonicalSLIs é o conjunto dos sete SLIs canónicos que o catálogo default DEVE
// cobrir (AC2). A validação exige que todos estejam presentes — um catálogo que
// omita um SLI canónico é rejeitado (reproduzível e completo por construção).
var canonicalSLIs = []string{
	SLIControlPlaneAvailability,
	SLIMediationOverheadP95,
	SLISandboxColdStartP95,
	SLICacheHitRate,
	SLIHeadroomTokens,
	SLIReplayFidelity,
	SLIAuditWORMIntegrity,
}

// DefaultDashboardCatalog devolve o catálogo de referência com os SETE painéis
// canónicos (AC2), cada um com plano, SLO e janela (AC5). Os alvos alinham-se a
// tecnica/10 §7 (disponibilidade 99,9%; overhead p95 < 15 ms; cold-start < 125 ms;
// cache-hit > 80%; headroom > 0 reservável; fidelidade de replay 100%; audit WORM
// íntegro).
func DefaultDashboardCatalog() DashboardCatalog {
	def := DefaultSLOConfig()
	return DashboardCatalog{
		Version: "1.0.0",
		Panels: []SLIPanel{
			{
				SLI:          SLIControlPlaneAvailability,
				Title:        "Disponibilidade do plano de controlo",
				Plane:        PlaneControl,
				Producer:     ProducerPDP,
				MetricSource: MetricControlPlaneAvailability,
				Unit:         UnitFraction,
				Direction:    DirMin,
				SLO:          0.999,
				Window:       "5m",
				Injected:     true,
				Description:  "Sem produtor dedicado: valor injectado por heartbeat/health (LACUNA honesta).",
			},
			{
				SLI:          SLIMediationOverheadP95,
				Title:        "Overhead de mediação (RM) p95",
				Plane:        PlaneControl,
				Producer:     ProducerReferenceMonitor,
				MetricSource: "",
				Unit:         UnitNanos,
				Direction:    DirMax,
				SLO:          float64(def.MediationOverheadP95MaxNanos),
				Window:       "5m",
				Description:  "p95 da latência dos spans execute_tool (o overhead da mediação).",
			},
			{
				SLI:          SLISandboxColdStartP95,
				Title:        "Cold-start de sandbox p95",
				Plane:        PlaneData,
				Producer:     ProducerSandbox,
				MetricSource: MetricSandboxColdStartMs,
				Unit:         UnitMillis,
				Direction:    DirMax,
				SLO:          125,
				Window:       "5m",
				Description:  "p95 de aos.sandbox.cold_start_ms dos wide events (alvo < 125 ms, ADR-004).",
			},
			{
				SLI:          SLICacheHitRate,
				Title:        "Cache-hit-rate de prompt",
				Plane:        PlaneData,
				Producer:     ProducerModelGateway,
				MetricSource: AttrCacheHitRate,
				Unit:         UnitFraction,
				Direction:    DirMin,
				SLO:          def.CacheHitRateTarget,
				Window:       "15m",
				Description:  "Média ponderada de aos.cache.hit_rate (cache thrash como SLI, ADR-009).",
			},
			{
				SLI:          SLIHeadroomTokens,
				Title:        "Headroom de tokens/$",
				Plane:        PlaneControl,
				Producer:     ProducerScheduler,
				MetricSource: MetricHeadroomFreeTokens,
				Unit:         UnitTokens,
				Direction:    DirMin,
				SLO:          1,
				Window:       "1m",
				Description:  "Headroom reservável do admission control (alerta abaixo do limiar de reserva).",
			},
			{
				SLI:          SLIReplayFidelity,
				Title:        "Fidelidade de replay",
				Plane:        PlaneData,
				Producer:     ProducerWorker,
				MetricSource: MetricReplayFidelity,
				Unit:         UnitFraction,
				Direction:    DirMin,
				SLO:          1.0,
				Window:       "per_run",
				Description:  "Fracção de turnos que reproduzem (aos.replay.fidelity), ligada por aos.run_id.",
			},
			{
				SLI:          SLIAuditWORMIntegrity,
				Title:        "Integridade do audit WORM",
				Plane:        PlaneData,
				Producer:     ProducerAudit,
				MetricSource: MetricAuditWORMIntegrity,
				Unit:         UnitBool,
				Direction:    DirMin,
				SLO:          1.0,
				Window:       "continuous",
				Injected:     true,
				Description:  "Resultado de audit.Verify() (íntegro=1/adulterado=0), injectado (LACUNA honesta).",
			},
		},
	}
}

// ErrInvalidDashboardCatalog sinaliza um catálogo de dashboard inválido (fail-closed).
var ErrInvalidDashboardCatalog = fmt.Errorf("otelgenai: catálogo de dashboard inválido")

// validate é FAIL-CLOSED (molde de [SLOConfig.validate]): rejeita SemVer inválido,
// painéis vazios, plano/direcção/janela em falta, SLI duplicado, ou a AUSÊNCIA de um
// SLI canónico (o catálogo tem de cobrir os sete — AC2, reprodutível e completo).
func (c DashboardCatalog) validate() error {
	if !validSemVer(c.Version) {
		return fmt.Errorf("%w: version %q não é SemVer", ErrInvalidDashboardCatalog, c.Version)
	}
	if len(c.Panels) == 0 {
		return fmt.Errorf("%w: sem painéis", ErrInvalidDashboardCatalog)
	}
	seen := make(map[string]bool, len(c.Panels))
	for i, p := range c.Panels {
		if p.SLI == "" {
			return fmt.Errorf("%w: painel %d sem nome de SLI", ErrInvalidDashboardCatalog, i)
		}
		if !p.Plane.valid() {
			return fmt.Errorf("%w: painel %d (%s) com plano inválido %q", ErrInvalidDashboardCatalog, i, p.SLI, p.Plane)
		}
		if p.Direction != DirMin && p.Direction != DirMax {
			return fmt.Errorf("%w: painel %d (%s) com direcção inválida %q", ErrInvalidDashboardCatalog, i, p.SLI, p.Direction)
		}
		if p.Window == "" {
			return fmt.Errorf("%w: painel %d (%s) sem janela de avaliação", ErrInvalidDashboardCatalog, i, p.SLI)
		}
		if seen[p.SLI] {
			return fmt.Errorf("%w: SLI duplicado %q", ErrInvalidDashboardCatalog, p.SLI)
		}
		seen[p.SLI] = true
	}
	for _, want := range canonicalSLIs {
		if !seen[want] {
			return fmt.Errorf("%w: falta o SLI canónico %q", ErrInvalidDashboardCatalog, want)
		}
	}
	return nil
}

// Validate expõe a validação fail-closed (para o chamador rejeitar um catálogo antes
// de o activar). Devolve nil sse o catálogo é válido.
func (c DashboardCatalog) Validate() error { return c.validate() }

// JSON serializa o catálogo em JSON indentado e determinista (ordem dos painéis
// preservada, sem escape HTML de `<`/`>`) — o ficheiro versionado no repo prova a
// reprodutibilidade (AC6).
func (c DashboardCatalog) JSON() ([]byte, error) {
	type alias DashboardCatalog
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(alias(c)); err != nil {
		return nil, err
	}
	// Encoder.Encode acrescenta um '\n' final; devolve-se sem ele (o ficheiro no repo
	// acrescenta o seu próprio newline no fim).
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// LoadDashboardCatalog desserializa e VALIDA um catálogo de JSON (fail-closed). Um
// JSON malformado ou um catálogo incompleto (falta um SLI canónico) é rejeitado — o
// chamador nunca cai num dashboard vazio/permissivo. É o outro lado do round-trip de
// [DashboardCatalog.JSON].
func LoadDashboardCatalog(raw []byte) (DashboardCatalog, error) {
	var c DashboardCatalog
	if err := json.Unmarshal(raw, &c); err != nil {
		return DashboardCatalog{}, fmt.Errorf("%w: JSON: %v", ErrInvalidDashboardCatalog, err)
	}
	if err := c.validate(); err != nil {
		return DashboardCatalog{}, err
	}
	return c, nil
}

// PanelsByPlane devolve os painéis do plano dado, na ordem do catálogo — a base dos
// dashboards distintos por plano de controlo e por plano de dados (AC1).
func (c DashboardCatalog) PanelsByPlane(plane Plane) []SLIPanel {
	var out []SLIPanel
	for _, p := range c.Panels {
		if p.Plane == plane {
			out = append(out, p)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// OperationalInputs — os sinais de entrada da renderização (query-time + injecção).
// ---------------------------------------------------------------------------

// OperationalInputs agrega os sinais que alimentam a renderização dos painéis. Os
// SLIs derivam-se dos wide events/spans JÁ emitidos (query-time); os DOIS sem
// produtor injectam-se honestamente (ponteiros nil ⇒ não avaliado, anti-vacuidade):
//
//   - ControlPlaneAvailability — heartbeat/health do plano de controlo (LACUNA 6a).
//   - AuditWORMIntact — resultado de audit.Verify() (LACUNA 6b, wiring diferido).
//
// Os restantes (cold-start, headroom, fidelidade) preferem o wide event; se não vier
// nenhum, o ponteiro de fallback injectado é usado. Nada é FABRICADO: sem wide event
// nem injecção, o painel fica não avaliado.
type OperationalInputs struct {
	// Events são os wide events (fonte query-time de cache-hit, overhead, cold-start,
	// headroom, replay, custo).
	Events []WideEvent
	// Spans são os spans emitidos (fonte do custo por trajectória, via AggregateByTrace).
	Spans []SpanData

	// ControlPlaneAvailability — valor injectado da disponibilidade [0,1]. nil ⇒ o SLI
	// fica não avaliado (Samples 0) até ser cablado.
	ControlPlaneAvailability *float64
	// AuditWORMIntact — resultado injectado de audit.Verify(): true=íntegro, false=
	// adulterado. nil ⇒ não avaliado.
	AuditWORMIntact *bool

	// ColdStartFallbackP95Ms — fallback injectado do p95 de cold-start (ms) quando não
	// há wide event com [MetricSandboxColdStartMs]. nil ⇒ só o wide event conta.
	ColdStartFallbackP95Ms *float64
	// HeadroomFallbackTokens — fallback injectado do headroom reservável quando não há
	// wide event com [MetricHeadroomFreeTokens]. nil ⇒ só o wide event conta.
	HeadroomFallbackTokens *float64
	// ReplayFidelityFallback — fallback injectado da fidelidade de replay quando não há
	// wide event com [MetricReplayFidelity]. nil ⇒ só o wide event conta.
	ReplayFidelityFallback *float64
}

// ---------------------------------------------------------------------------
// RenderedPanel / OperationalSnapshot — os painéis com valor vivo (renderização).
// ---------------------------------------------------------------------------

// RenderedPanel é um painel do catálogo COM o seu valor observado avaliado vs. o SLO.
// Junta o descritor estático (plano, SLO, janela — AC5) ao [SLIValue] vivo (valor,
// Met, Samples, Offenders — drill-down).
type RenderedPanel struct {
	// Panel é o descritor estático (dashboard-as-code).
	Panel SLIPanel
	// SLI é o valor observado avaliado vs. o SLO do painel.
	SLI SLIValue
}

// OperationalSnapshot é o dashboard operacional renderizado: os painéis do catálogo
// com valores vivos, organizáveis por plano (AC1). NÃO se serializa como o artefacto
// versionado — o dashboard-as-code é o [DashboardCatalog]; isto é o resultado da
// agregação query-time sobre um instante de dados.
type OperationalSnapshot struct {
	// CatalogVersion é a versão do catálogo que produziu este snapshot.
	CatalogVersion string
	// Panels são os painéis renderizados, na ordem do catálogo.
	Panels []RenderedPanel
}

// ByPlane devolve os painéis renderizados do plano dado (dashboards por plano, AC1).
func (s OperationalSnapshot) ByPlane(plane Plane) []RenderedPanel {
	var out []RenderedPanel
	for _, rp := range s.Panels {
		if rp.Panel.Plane == plane {
			out = append(out, rp)
		}
	}
	return out
}

// Breaches devolve os painéis AVALIADOS que não cumprem o SLO (ordem do catálogo). Um
// painel não avaliado (sem dados/injecção) nunca está em breach (anti-vacuidade).
func (s OperationalSnapshot) Breaches() []RenderedPanel {
	var out []RenderedPanel
	for _, rp := range s.Panels {
		if rp.SLI.Breached() {
			out = append(out, rp)
		}
	}
	return out
}

// Panel devolve o painel renderizado do SLI dado e ok=false se ausente.
func (s OperationalSnapshot) Panel(sli string) (RenderedPanel, bool) {
	for _, rp := range s.Panels {
		if rp.Panel.SLI == sli {
			return rp, true
		}
	}
	return RenderedPanel{}, false
}

// Render produz o [OperationalSnapshot] avaliando cada painel do catálogo contra os
// [OperationalInputs] por agregação query-time (AC4). Cada painel usa o SEU SLO e a
// sua direcção (do descritor); os dois SLIs sem produtor usam a injecção honesta. É
// determinista e puro (sem relógio, sem aleatoriedade).
func (c DashboardCatalog) Render(in OperationalInputs) OperationalSnapshot {
	snap := OperationalSnapshot{CatalogVersion: c.Version, Panels: make([]RenderedPanel, 0, len(c.Panels))}
	for _, p := range c.Panels {
		snap.Panels = append(snap.Panels, RenderedPanel{Panel: p, SLI: renderPanel(p, in)})
	}
	return snap
}

// renderPanel despacha a derivação de UM painel pelo seu nome de SLI, usando o SLO e a
// direcção do descritor. Reutiliza os derivadores puros de slo.go onde existem.
func renderPanel(p SLIPanel, in OperationalInputs) SLIValue {
	switch p.SLI {
	case SLICacheHitRate:
		return cacheHitRateSLI(in.Events, p.SLO)
	case SLIMediationOverheadP95:
		return overheadP95SLI(in.Events, int64(p.SLO))
	case SLISandboxColdStartP95:
		return coldStartP95SLI(in.Events, p.SLO, in.ColdStartFallbackP95Ms)
	case SLIHeadroomTokens:
		return headroomSLI(in.Events, p.SLO, in.HeadroomFallbackTokens)
	case SLIReplayFidelity:
		return replayFidelitySLI(in.Events, p.SLO, in.ReplayFidelityFallback)
	case SLIControlPlaneAvailability:
		return injectedFractionSLI(SLIControlPlaneAvailability, in.ControlPlaneAvailability, p.SLO)
	case SLIAuditWORMIntegrity:
		return auditWORMIntegritySLI(in.AuditWORMIntact, p.SLO)
	default:
		// SLI desconhecido no catálogo: painel não avaliado (nunca fabrica valor).
		return SLIValue{Name: p.SLI, SLO: p.SLO, Direction: p.Direction, Met: true}
	}
}

// ---------------------------------------------------------------------------
// Derivadores adicionais (puros, sobre wide events / injecção) — módulo FOLHA.
// ---------------------------------------------------------------------------

// coldStartP95SLI deriva o p95 do cold-start de sandbox (ms) a partir dos wide events
// que carregam [MetricSandboxColdStartMs] no bag. Reutiliza [percentileNanos] (o
// percentil type-7, aqui sobre ms inteiros). Sem wide events, cai no fallback
// injectado (fallbackP95Ms); sem nenhum dos dois, fica não avaliado (Samples 0).
// Drill-down: os run_ids acima do tecto (pior cold-start primeiro).
func coldStartP95SLI(events []WideEvent, maxMs float64, fallbackP95Ms *float64) SLIValue {
	sli := SLIValue{Name: SLISandboxColdStartP95, SLO: maxMs, Direction: DirMax, Met: true}

	type sample struct {
		run string
		ms  int64
	}
	var vals []int64
	var samples []sample
	for _, e := range events {
		v, ok := e.Attributes[MetricSandboxColdStartMs]
		if !ok {
			continue
		}
		ms, ok := attrNumeric(v)
		if !ok {
			continue
		}
		vals = append(vals, ms)
		samples = append(samples, sample{runKeyOf(e), ms})
	}

	if len(vals) == 0 {
		// Sem wide event: fallback injectado (uma amostra), se fornecido.
		if fallbackP95Ms != nil {
			sli.Samples = 1
			sli.Value = *fallbackP95Ms
			sli.Met = *fallbackP95Ms <= maxMs
		}
		return sli
	}

	sli.Samples = len(vals)
	p95 := percentileNanos(vals, 95)
	sli.Value = float64(p95)
	sli.Met = float64(p95) <= maxMs
	if !sli.Met {
		sort.Slice(samples, func(i, j int) bool {
			if samples[i].ms != samples[j].ms {
				return samples[i].ms > samples[j].ms
			}
			return samples[i].run < samples[j].run
		})
		seen := make(map[string]bool)
		ceil := int64(maxMs)
		for _, s := range samples {
			if s.ms > ceil && s.run != "" && !seen[s.run] {
				seen[s.run] = true
				sli.Offenders = append(sli.Offenders, s.run)
			}
		}
	}
	return sli
}

// headroomSLI deriva o headroom de tokens/$ reservável. O valor é o MÍNIMO headroom
// observado nos wide events que carregam [MetricHeadroomFreeTokens] (a restrição
// vinculativa — o pior momento de pressão); direcção DirMin (headroom >= limiar de
// reserva cumpre). Sem wide events, cai no fallback injectado; sem nenhum, não
// avaliado. Drill-down: os run_ids abaixo do limiar (menor headroom primeiro).
func headroomSLI(events []WideEvent, minTokens float64, fallbackTokens *float64) SLIValue {
	sli := SLIValue{Name: SLIHeadroomTokens, SLO: minTokens, Direction: DirMin, Met: true}

	type sample struct {
		run    string
		tokens int64
	}
	var samples []sample
	haveMin := false
	var minVal int64
	for _, e := range events {
		v, ok := e.Attributes[MetricHeadroomFreeTokens]
		if !ok {
			continue
		}
		tk, ok := attrNumeric(v)
		if !ok {
			continue
		}
		samples = append(samples, sample{runKeyOf(e), tk})
		if !haveMin || tk < minVal {
			minVal = tk
			haveMin = true
		}
	}

	if !haveMin {
		if fallbackTokens != nil {
			sli.Samples = 1
			sli.Value = *fallbackTokens
			sli.Met = *fallbackTokens >= minTokens
		}
		return sli
	}

	sli.Samples = len(samples)
	sli.Value = float64(minVal)
	sli.Met = float64(minVal) >= minTokens
	if !sli.Met {
		sort.Slice(samples, func(i, j int) bool {
			if samples[i].tokens != samples[j].tokens {
				return samples[i].tokens < samples[j].tokens
			}
			return samples[i].run < samples[j].run
		})
		seen := make(map[string]bool)
		floor := int64(minTokens)
		for _, s := range samples {
			if s.tokens < floor && s.run != "" && !seen[s.run] {
				seen[s.run] = true
				sli.Offenders = append(sli.Offenders, s.run)
			}
		}
	}
	return sli
}

// replayFidelitySLI deriva a fidelidade de replay como a MÉDIA das fracções
// [MetricReplayFidelity] dos wide events que a carregam (ligadas por aos.run_id ao
// span replay). Direcção DirMin (fidelidade >= alvo cumpre; alvo tipicamente 1.0).
// Sem wide events, cai no fallback injectado; sem nenhum, não avaliado. Drill-down:
// os run_ids abaixo do alvo (menor fidelidade primeiro).
func replayFidelitySLI(events []WideEvent, target float64, fallback *float64) SLIValue {
	sli := SLIValue{Name: SLIReplayFidelity, SLO: target, Direction: DirMin, Met: true}

	type sample struct {
		run  string
		frac float64
	}
	var samples []sample
	var sum float64
	n := 0
	for _, e := range events {
		v, ok := e.Attributes[MetricReplayFidelity]
		if !ok {
			continue
		}
		frac, ok := attrFloat64(v)
		if !ok {
			continue
		}
		sum += frac
		n++
		samples = append(samples, sample{runKeyOf(e), frac})
	}

	if n == 0 {
		if fallback != nil {
			sli.Samples = 1
			sli.Value = *fallback
			sli.Met = *fallback >= target
		}
		return sli
	}

	sli.Samples = n
	sli.Value = sum / float64(n)
	sli.Met = sli.Value >= target
	if !sli.Met {
		sort.Slice(samples, func(i, j int) bool {
			if samples[i].frac != samples[j].frac {
				return samples[i].frac < samples[j].frac
			}
			return samples[i].run < samples[j].run
		})
		seen := make(map[string]bool)
		for _, s := range samples {
			if s.frac < target && s.run != "" && !seen[s.run] {
				seen[s.run] = true
				sli.Offenders = append(sli.Offenders, s.run)
			}
		}
	}
	return sli
}

// injectedFractionSLI constrói um SLI de fracção [0,1] a partir de um valor INJECTADO
// (ponteiro; nil ⇒ não avaliado, Samples 0 — anti-vacuidade). Direcção DirMin (valor
// >= SLO cumpre). Usado na disponibilidade do plano de controlo (LACUNA 6a): o valor
// não é fabricado, é fornecido pelo chamador (heartbeat/health) ou fica ausente.
func injectedFractionSLI(name string, value *float64, slo float64) SLIValue {
	sli := SLIValue{Name: name, SLO: slo, Direction: DirMin, Met: true}
	if value == nil {
		return sli // Samples 0 ⇒ não avaliado.
	}
	sli.Samples = 1
	sli.Value = *value
	sli.Met = *value >= slo
	return sli
}

// auditWORMIntegritySLI constrói o SLI BOOLEANO de integridade do audit WORM a partir
// do resultado INJECTADO de audit.Verify() (ponteiro bool; nil ⇒ não avaliado). O
// binding a audit.Verify é wiring de composition-root (diferido). true ⇒ Value 1
// (íntegro, cumpre); false ⇒ Value 0 (adulterado, breach). Direcção DirMin, SLO 1.0.
func auditWORMIntegritySLI(intact *bool, slo float64) SLIValue {
	sli := SLIValue{Name: SLIAuditWORMIntegrity, SLO: slo, Direction: DirMin, Met: true}
	if intact == nil {
		return sli // não avaliado até o Verify ser cablado.
	}
	sli.Samples = 1
	if *intact {
		sli.Value = 1
		sli.Met = 1 >= slo
	} else {
		sli.Value = 0
		sli.Met = false
	}
	return sli
}

// runKeyOf devolve a chave de run de um wide event: o aos.run_id se presente, senão o
// trace_id (correlação da trajectória → run). "" se nenhum — o drill-down ignora-o.
func runKeyOf(e WideEvent) string {
	if e.RunID != "" {
		return e.RunID
	}
	return e.TraceIDHex
}

// attrNumeric lê um atributo numérico como int64, aceitando inteiros ([attrInt64]) OU
// floats (aos.sandbox.cold_start_ms/headroom podem vir como float do produtor),
// truncando o float para inteiro. false se não-numérico.
func attrNumeric(v any) (int64, bool) {
	if n, ok := attrInt64(v); ok {
		return n, true
	}
	if f, ok := attrFloat64(v); ok {
		return int64(f), true
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Custo por RUN e por TENANT (AC3) — helpers sobre AggregateUsage, reconciliando
// aos.tenant vs aos.tenant_id.
// ---------------------------------------------------------------------------

// TenantOf devolve o tenant de um wide event RECONCILIANDO as duas chaves divergentes
// do vocabulário: prefere [AttrTenantID] (aos.tenant_id, otel-genai — já derivado em
// [WideEvent.TenantID]) e, na sua ausência, lê [AttrTenantAlt] (aos.tenant, emitido
// pelo Model Gateway). "" se nenhuma presente. É a normalização que a agregação de
// custo por tenant exige para não partir o mesmo tenant em duas linhas.
func TenantOf(e WideEvent) string {
	if e.TenantID != "" {
		return e.TenantID
	}
	if v, ok := e.Attributes[AttrTenantAlt]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// CostByRun agrega o custo/tokens por RUN (aos.run_id; fallback trace_id) sobre os
// wide events, reutilizando [AggregateUsage]. É a agregação query-time do custo por
// run (AC3) — o custo por span soma-se sem reinstrumentar.
func CostByRun(events []WideEvent) map[string]UsageTotals {
	return AggregateUsage(events, runKeyOf)
}

// CostByTenant agrega o custo/tokens por TENANT sobre os wide events, RECONCILIANDO
// aos.tenant_id/aos.tenant via [TenantOf] (AC3). Eventos sem tenant caem na chave ""
// (não se inventa um tenant).
func CostByTenant(events []WideEvent) map[string]UsageTotals {
	return AggregateUsage(events, TenantOf)
}

// CostByRunAndTenant agrega o custo/tokens pela chave COMPOSTA run+tenant (AC3): a
// pergunta "custo por run E por tenant" respondida por agregação ad-hoc, sem
// reinstrumentar. A chave é "run\x1ftenant" (separador de unidade, sem colisão com
// ids hex/rótulos); ver [SplitRunTenantKey] para a decompor.
func CostByRunAndTenant(events []WideEvent) map[string]UsageTotals {
	return AggregateUsage(events, func(e WideEvent) string {
		return runKeyOf(e) + runTenantSep + TenantOf(e)
	})
}

// runTenantSep é o separador da chave composta run+tenant (US, ASCII 0x1F — nunca
// ocorre num id hex ou num rótulo de tenant).
const runTenantSep = "\x1f"

// SplitRunTenantKey decompõe uma chave de [CostByRunAndTenant] em (run, tenant).
func SplitRunTenantKey(key string) (run, tenant string) {
	if i := strings.IndexByte(key, runTenantSep[0]); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

// ---------------------------------------------------------------------------
// Vista por RUN com drill-down span→tool call e custo por span (AC1, AC3).
// ---------------------------------------------------------------------------

// SpanCost é o custo de UM span da trajectória (drill-down até à tool call
// individual): a sua identidade/topologia, a operação, o nome da tool (se
// execute_tool) e o custo em micro-USD/USD. O custo por span é VISÍVEL aqui (AC3);
// os spans execute_tool não têm custo de modelo (custo 0) mas aparecem na árvore.
type SpanCost struct {
	// SpanIDHex é o span_id (hex) do span.
	SpanIDHex string `json:"span_id"`
	// ParentSpanIDHex é o parent_span_id (hex), "" se raiz.
	ParentSpanIDHex string `json:"parent_span_id,omitempty"`
	// Operation é a operação do span (invoke_agent|chat|execute_tool).
	Operation string `json:"operation"`
	// ToolName é o nome da tool (só nos spans execute_tool — a tool call individual).
	ToolName string `json:"tool_name,omitempty"`
	// CostMicroUSD é o custo do span em micro-USD (fonte-de-verdade inteira).
	CostMicroUSD int64 `json:"cost_micro_usd"`
	// CostUSD é o custo em USD (só apresentação).
	CostUSD float64 `json:"cost_usd"`
}

// RunView é a vista por RUN (AC1): os SLIs desse run, a árvore de spans com o rollup
// de custo por sub-árvore (OwnByAgent/SubtreeByAgent, drill-down até à tool call) e o
// custo por span. Um run corresponde a uma trajectória (aos.run_id → trace_id).
type RunView struct {
	// RunID é o identificador do run.
	RunID string `json:"run_id"`
	// TraceIDs são os trace_ids da trajectória do run (normalmente um).
	TraceIDs []string `json:"trace_ids"`
	// Dashboard é o snapshot dos quatro SLIs de mediação DESTE run (reutiliza
	// [BuildDashboard]) — cache-hit, overhead, custo, override restritos ao run.
	Dashboard DashboardSnapshot `json:"-"`
	// Rollup é o rollup de custo por trace (drill-down por sub-árvore de delegação,
	// reutilizando [RollupByTrace]).
	Rollup []TraceRollup `json:"-"`
	// Spans são os custos por span da árvore do run, ordenados (span_id) — o drill-down
	// span→tool call com custo por span (AC3).
	Spans []SpanCost `json:"spans"`
	// TotalCostMicroUSD é o custo total do run (soma dos chats, sem dupla-contagem).
	TotalCostMicroUSD int64 `json:"total_cost_micro_usd"`
	// TotalCostUSD é o custo total em USD (apresentação).
	TotalCostUSD float64 `json:"total_cost_usd"`
}

// BuildRunView monta a vista por run (AC1): resolve os trace_ids do run a partir dos
// wide events (aos.run_id → trace), filtra os spans desses traces, e compõe os SLIs
// (via [BuildDashboard]), o rollup por sub-árvore (via [RollupByTrace]) e o custo por
// span. Reutiliza os agregadores existentes — nenhuma instrumentação nova.
func BuildRunView(runID string, events []WideEvent, spans []SpanData, cfg SLOConfig) RunView {
	rv := RunView{RunID: runID}

	// (1) Traces do run: de aos.run_id nos wide events; fallback — trata runID como
	// trace_id se nenhum wide event o mapear (um run == um trace, o caso comum).
	traceSet := make(map[string]bool)
	for _, e := range events {
		if e.RunID == runID && e.TraceIDHex != "" {
			traceSet[e.TraceIDHex] = true
		}
	}
	if len(traceSet) == 0 {
		traceSet[runID] = true
	}
	for t := range traceSet {
		rv.TraceIDs = append(rv.TraceIDs, t)
	}
	sort.Strings(rv.TraceIDs)

	// (2) Spans e wide events restritos ao run.
	var runSpans []SpanData
	for _, sd := range spans {
		if traceSet[sd.SpanContext.TraceIDHex()] {
			runSpans = append(runSpans, sd)
		}
	}
	runEvents := Filter(events, func(e WideEvent) bool {
		return traceSet[e.TraceIDHex] || e.RunID == runID
	})

	// (3) SLIs do run + rollup por sub-árvore (drill-down).
	rv.Dashboard = BuildDashboard(runEvents, runSpans, cfg)
	for _, t := range rv.TraceIDs {
		if r, ok := RollupByTrace(runSpans)[t]; ok {
			rv.Rollup = append(rv.Rollup, r)
		}
	}

	// (4) Custo por span (drill-down span→tool call). Cada span traz o seu custo; os
	// chat carregam o custo de modelo, os execute_tool a tool call (custo 0).
	for _, sd := range runSpans {
		micro := costMicroUSDOf(sd)
		sc := SpanCost{
			SpanIDHex:       sd.SpanContext.SpanIDHex(),
			ParentSpanIDHex: parentHexOf(sd),
			Operation:       operationOf(sd),
			CostMicroUSD:    micro,
			CostUSD:         MicroUSDToUSD(micro),
		}
		if tn, ok := sd.Attribute(AttrToolName); ok {
			if s, ok := tn.(string); ok {
				sc.ToolName = s
			}
		}
		rv.Spans = append(rv.Spans, sc)
	}
	sort.Slice(rv.Spans, func(i, j int) bool { return rv.Spans[i].SpanIDHex < rv.Spans[j].SpanIDHex })

	// (5) Custo total do run (sem dupla-contagem — só chats, via AggregateByTrace).
	for _, t := range rv.TraceIDs {
		rv.TotalCostMicroUSD += AggregateByTrace(runSpans)[t].CostMicroUSD
	}
	rv.TotalCostUSD = MicroUSDToUSD(rv.TotalCostMicroUSD)

	return rv
}
