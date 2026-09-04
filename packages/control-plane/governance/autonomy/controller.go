package autonomy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// AOS-090 — o CONTROLADOR de autonomia que AUTOMATIZA a taxonomia L0–L5 (AOS-089):
// PROMOVE um par (agente, domínio) só por fiabilidade SUSTENTADA (janela deslizante)
// e DEMOVE-o AUTOMÁTICA e IMEDIATAMENTE em anomalia, para um nível mais
// supervisionado, sem gate humano (ADR-014). É ADITIVO: COMPÕE o [LevelRegistry]
// (chama o [LevelRegistry.SetLevel] auditável) e os sinais por PORTA
// ([ReliabilitySource] + [Controller.OnAnomaly]) — não reimplementa o Level/Registry/
// Oversight nem o breaker/hitl de EPIC-08.

// ControllerActor é o principal atribuído às transições que o [Controller] efectua
// (o actor selado no evento autonomy.level_changed — AC4). Distingue uma transição
// AUTOMÁTICA de uma alteração manual de um operador humano na cadeia de audit.
const ControllerActor = "autonomy-controller"

// Nomes de operação e atributos do span OTel de TRANSIÇÃO (AC4/DoD). Reutilizam os
// atributos de par de [span.go] (AttrAutonomyAgent/Domain) e acrescentam os da
// transição. NUNCA transportam segredos — só identificadores, níveis e o motivo
// (métrica) que a justificou.
const (
	// OpAutonomyTransition é o nome do span que cobre uma promoção ou demoção
	// automática.
	OpAutonomyTransition = "aos.autonomy.transition"

	// AttrAutonomyOldLevel — o nível ANTERIOR da transição ("L0".."L5").
	AttrAutonomyOldLevel = "aos.autonomy.old_level"
	// AttrAutonomyNewLevel — o nível RESULTANTE da transição.
	AttrAutonomyNewLevel = "aos.autonomy.new_level"
	// AttrAutonomyDirection — o sentido ("promotion" | "demotion").
	AttrAutonomyDirection = "aos.autonomy.direction"
	// AttrAutonomyReason — o motivo/métrica que justificou a transição (AC4).
	AttrAutonomyReason = "aos.autonomy.reason"
	// AttrAutonomyAnomalyKind — o tipo de anomalia que disparou a demoção (só em
	// demoções).
	AttrAutonomyAnomalyKind = "aos.autonomy.anomaly_kind"
	// AttrAutonomyConfigVersion — a versão da policy-as-code em vigor na transição.
	AttrAutonomyConfigVersion = "aos.autonomy.config_version"
)

// Sentidos de transição gravados em [AttrAutonomyDirection].
const (
	directionPromotion = "promotion"
	directionDemotion  = "demotion"
)

// AnomalyKind é o TIPO de anomalia que dispara uma demoção automática (AC2). É um
// rótulo legível gravado no motivo selado e no span — nunca um segredo.
type AnomalyKind string

const (
	// AnomalyOverrideRateSpike — pico de override-rate (rubber-stamping): o sinal
	// [github.com/aos-ref/control-plane/governance/hitl.Metrics.Exceeds] de AOS-095.
	AnomalyOverrideRateSpike AnomalyKind = "override_rate_spike"
	// AnomalyUnsafeAction — acção insegura sinalizada: o trip multi-sinal do circuit
	// breaker de AOS-080 (velocity/wall-clock/no-progress).
	AnomalyUnsafeAction AnomalyKind = "unsafe_action"
	// AnomalyDrift — deriva medida: o trace-diffing de AOS-084 (a trajectória afasta-se
	// da baseline aprovada).
	AnomalyDrift AnomalyKind = "drift"
)

// String devolve a forma textual do tipo de anomalia (fail-closed: um valor vazio
// devolve "unspecified", nunca confundível com um tipo conhecido).
func (k AnomalyKind) String() string {
	if k == "" {
		return "unspecified"
	}
	return string(k)
}

// Reliability é a FOTOGRAFIA de fiabilidade SUSTENTADA de um par (agente, domínio)
// sobre a janela deslizante configurada — a entrada da decisão de promoção. É
// produzida por uma [ReliabilitySource] que AGREGA a janela: as taxas devolvidas
// representam o pior caso ao longo da janela, pelo que "taxa <= limiar" significa
// que o limiar foi cumprido DURANTE TODA a janela (não num instante).
type Reliability struct {
	// ErrorRate é a taxa de erro sustentada sobre a janela (em [0,1]).
	ErrorRate float64
	// OverrideRate é o override-rate sustentado sobre a janela (em [0,1]).
	OverrideRate float64
	// WindowOK indica se a janela tem COBERTURA suficiente para julgar a fiabilidade
	// sustentada (ex.: 30 dias de amostras). false ⇒ ainda não há histórico bastante
	// e a promoção é NEGADA (conservador — nunca promover por uma janela incompleta).
	WindowOK bool
}

// ReliabilitySource é a PORTA dos sinais de fiabilidade sustentada: agrega a janela
// deslizante `window` (a da política, passada pelo [Controller]) de um par (agente,
// domínio) e devolve a [Reliability] a comparar com os limiares. Passar `window`
// torna a janela CONFIGURÁVEL por policy-as-code autoritativa (AC3): alterar
// `window` na config muda a agregação, não apenas um campo declarativo. A impl
// concreta liga aos contadores de EPIC-08 (override-rate do hitl, unsafe/anomaly do
// breaker); o [Controller] é PURO e testável com fakes.
type ReliabilitySource interface {
	Reliability(agent, domain string, window time.Duration) Reliability
}

// ReliabilityFunc adapta uma função a [ReliabilitySource].
type ReliabilityFunc func(agent, domain string, window time.Duration) Reliability

// Reliability implementa [ReliabilitySource].
func (f ReliabilityFunc) Reliability(agent, domain string, window time.Duration) Reliability {
	return f(agent, domain, window)
}

// ErrNilRegistry — construção de um [Controller] sem [LevelRegistry]. O controlador
// COMPÕE o registo (é onde efectua as transições auditáveis); sem ele não há nada a
// controlar.
var ErrNilRegistry = errors.New("autonomy: controlador exige um LevelRegistry")

// Controller é o controlador de autonomia (AOS-090): promove por fiabilidade
// sustentada ([Controller.Evaluate]) e demove em anomalia ([Controller.OnAnomaly]),
// aplicando cada transição via [LevelRegistry.SetLevel] auditável. Os limiares são
// policy-as-code hot-swappable ([Controller.SetConfig]). É seguro para concorrência.
// Construir com [NewController].
//
// CONCORRÊNCIA (fail-safe): [Controller.Evaluate] (periódico) e [Controller.OnAnomaly]
// (reactivo) correm em simultâneo sobre o mesmo par. O par [Level]/registo não expõe
// compare-and-swap, pelo que uma decisão "lê-decide-escreve" ingénua permitiria um
// LOST UPDATE — uma promoção calculada sobre um nível já obsoleto a esmagar a demoção
// de emergência que uma anomalia acabou de aplicar, restaurando um agente inseguro a
// um nível elevado. [Controller.mu] SERIALIZA a secção crítica decisão+escrita de
// AMBOS os caminhos; a amostragem lenta da janela ([ReliabilitySource.Reliability])
// fica FORA do lock (não bloqueia a demoção imediata — AC2) e a promoção RE-LÊ a base
// sob o lock, ABANDONANDO-se se o nível mudou desde a amostragem. Assim uma anomalia
// vence sempre uma promoção em corrida.
type Controller struct {
	// mu serializa a secção crítica decisão+escrita de Evaluate e OnAnomaly (ver a
	// nota de CONCORRÊNCIA acima). NÃO cobre a amostragem lenta da janela, deixada
	// deliberadamente fora do lock.
	mu     sync.Mutex
	reg    *LevelRegistry
	src    ReliabilitySource
	cfg    atomic.Pointer[AutonomyControlConfig]
	tracer otelgenai.Tracer
	now    func() time.Time
}

// ControllerOption configura um [Controller].
type ControllerOption func(*Controller)

// WithControllerTracer injecta o [otelgenai.Tracer] que emite o span de cada
// transição (AC4/DoD). Por omissão [otelgenai.NoopTracer] (sem custo).
func WithControllerTracer(t otelgenai.Tracer) ControllerOption {
	return func(c *Controller) {
		if t != nil {
			c.tracer = t
		}
	}
}

// WithControllerClock injecta o relógio (testes deterministas). Por omissão
// [time.Now] em UTC. Reservado para extensões que datam decisões do controlador —
// as transições em si são datadas pelo relógio do [LevelRegistry].
func WithControllerClock(f func() time.Time) ControllerOption {
	return func(c *Controller) {
		if f != nil {
			c.now = f
		}
	}
}

// NewController constrói o controlador sobre o registo dado, com a política inicial
// (validada fail-closed) e uma [ReliabilitySource] para a decisão de promoção. Um
// registo nil devolve [ErrNilRegistry]; uma config inválida devolve o erro de
// [AutonomyControlConfig.Validate]. Uma fonte nil é tolerada: a promoção nunca
// dispara (fail-safe), mas a demoção por anomalia continua a funcionar.
func NewController(reg *LevelRegistry, src ReliabilitySource, cfg AutonomyControlConfig, opts ...ControllerOption) (*Controller, error) {
	if reg == nil {
		return nil, ErrNilRegistry
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	c := &Controller{
		reg:    reg,
		src:    src,
		tracer: otelgenai.NoopTracer{},
		now:    func() time.Time { return time.Now().UTC() },
	}
	cp := cfg
	c.cfg.Store(&cp)
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// Config devolve a política em vigor (a última fixada por [NewController] ou
// [Controller.SetConfig]). Seguro para concorrência.
func (c *Controller) Config() AutonomyControlConfig { return *c.cfg.Load() }

// SetConfig substitui, atomicamente, a policy-as-code em vigor (AC3: uma alteração
// de limiar altera o comportamento — o que promovia deixa de promover, o que não
// demovia passa a demover). Fail-closed: uma config inválida é REJEITADA e a política
// anterior mantém-se.
func (c *Controller) SetConfig(cfg AutonomyControlConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	cp := cfg
	c.cfg.Store(&cp)
	return nil
}

// Evaluate é o CAMINHO DE PROMOÇÃO (AC1): consulta a fiabilidade sustentada do par e
// PROMOVE UM nível (conservador) se — e só se — os limiares foram cumpridos DURANTE
// TODA a janela (WindowOK e ambas as taxas <= limiar); caso contrário o nível
// MANTÉM-SE. Nunca sobe acima do tecto configurado. É idempotente e chamável
// periodicamente pelo scheduler de governação.
//
// CONCORRÊNCIA/FAIL-SAFE: a fiabilidade é amostrada FORA do lock (é o ponto lento —
// agrega a janela deslizante — e não pode bloquear uma demoção por anomalia
// concorrente, AC2). A decisão+escrita corre sob [Controller.mu] e RE-LÊ a base: se o
// nível mudou desde a amostragem (ex.: uma demoção por anomalia interveio) a promoção
// é ABANDONADA — nunca se promove sobre uma base obsoleta, pelo que uma anomalia em
// corrida vence sempre (não há lost update que restaure um agente inseguro).
//
// AUDITÁVEL/FAIL-CLOSED (AC4): a promoção só vale COM registo selado. Se a selagem no
// audit falhar, a concessão é REVERTIDA em memória para o nível anterior (o [Level]
// que o PDP lê nunca fica elevado sem audit) e o erro é devolvido — ao contrário da
// demoção, uma promoção não-selada NÃO se mantém em vigor.
//
// Devolve a [LevelChange] aplicada e changed=true SÓ quando promoveu e selou;
// changed=false (com [LevelChange] zero) quando manteve, abandonou ou reverteu por
// falha de selagem (neste último caso o erro de selagem acompanha).
func (c *Controller) Evaluate(ctx context.Context, agent, domain string) (LevelChange, bool, error) {
	cfg := c.cfg.Load()

	// Fail-safe: sem fonte de fiabilidade não há evidência de fiabilidade sustentada
	// — nunca promover por omissão.
	if c.src == nil {
		return LevelChange{}, false, nil
	}
	// Base pré-amostragem (fora do lock). Já no tecto: conservador, não há para onde
	// promover — evita amostrar a janela em vão.
	base := c.reg.LevelFor(agent, domain)
	if base >= cfg.promotionCeil {
		return LevelChange{}, false, nil
	}

	// Amostragem da janela FORA de qualquer lock (ponto lento; não bloqueia OnAnomaly).
	rel := c.src.Reliability(agent, domain, cfg.window)
	// A fiabilidade tem de ser SUSTENTADA (janela com cobertura) E ambos os limiares
	// cumpridos. Qualquer falha ⇒ MANTÉM (nunca promove por um instante bom).
	if !rel.WindowOK || rel.ErrorRate > cfg.errorRateMax || rel.OverrideRate > cfg.overrideRateMax {
		return LevelChange{}, false, nil
	}

	// Secção crítica: serializa decisão+escrita com OnAnomaly.
	c.mu.Lock()
	defer c.mu.Unlock()

	// RE-LÊ a base sob o lock. Se mudou desde a amostragem, uma transição interveio
	// (tipicamente uma demoção por anomalia) — ABANDONA a promoção para não a esmagar.
	cur := c.reg.LevelFor(agent, domain)
	if cur != base {
		return LevelChange{}, false, nil
	}

	next := cur + 1 // conservador: sobe exactamente um nível
	reason := fmt.Sprintf(
		"promocao sustentada: error_rate=%.4f<=%.4f e override_rate=%.4f<=%.4f durante janela=%s (policy v%s)",
		rel.ErrorRate, cfg.errorRateMax, rel.OverrideRate, cfg.overrideRateMax, cfg.window, cfg.version,
	)

	ch, err := c.reg.SetLevel(ctx, agent, domain, next, reason, ControllerActor)
	if err != nil {
		// Fail-closed: a promoção não pode vigorar sem registo selado. Desde AOS-306
		// [LevelRegistry.SetLevel] sela ANTES de aplicar, pelo que uma selagem falhada
		// já deixa o nível anterior em vigor — não há concessão a reverter (e um
		// SetLevel de reversão selaria um evento espúrio se o WORM entretanto voltasse).
		// Devolve o erro de selagem e changed=false (a promoção não se deu).
		return LevelChange{}, false, err
	}
	c.emitTransition(ctx, ch, directionPromotion, "", cfg.version)
	return ch, true, nil
}

// OnAnomaly é o CAMINHO DE DEMOÇÃO (AC2/AC5): event-driven e REACTIVO — ao detectar
// uma anomalia (pico de override-rate, acção insegura, deriva) DEMOVE JÁ o par para
// um nível MAIS SUPERVISIONADO, de forma DETERMINÍSTICA (target = max(piso,
// corrente - salto)), SEM gate humano. FAIL-SAFE: uma anomalia NUNCA promove — se o
// par já está no piso (ou abaixo), mantém-se.
//
// Devolve a [LevelChange] aplicada e changed=true quando demoveu; changed=false (com
// [LevelChange] zero) quando o par já estava no piso. Um erro de selagem é propagado
// (a demoção já baixou o nível em memória — fail-safe: mantém-se ainda que sem audit).
//
// CONCORRÊNCIA/FAIL-SAFE: corre sob [Controller.mu] (o mesmo lock de [Controller.Evaluate])
// para que a demoção de emergência não possa ser esmagada por uma promoção em corrida
// (AC2/AC5). A secção crítica não contém I/O lento além da própria selagem.
func (c *Controller) OnAnomaly(ctx context.Context, agent, domain string, kind AnomalyKind) (LevelChange, bool, error) {
	cfg := c.cfg.Load()

	// Secção crítica: serializa decisão+escrita com Evaluate (anti lost-update).
	c.mu.Lock()
	defer c.mu.Unlock()

	cur := c.reg.LevelFor(agent, domain)
	target := demote(cur, cfg.demotionDrop, cfg.demotionFloor)

	// Determinismo/fail-safe: só aplica se DESCE. target >= cur (par já no piso, ou o
	// piso está acima do corrente) ⇒ NUNCA sobe numa anomalia; mantém-se.
	if target >= cur {
		return LevelChange{}, false, nil
	}

	reason := fmt.Sprintf(
		"anomalia %s: democao determinista %s->%s (salto=%d, piso=%s) (policy v%s)",
		kind.String(), cur.String(), target.String(), cfg.demotionDrop, cfg.demotionFloor.String(), cfg.version,
	)

	ch, err := c.reg.SetLevel(ctx, agent, domain, target, reason, ControllerActor)
	if err != nil {
		// Fail-closed, e a metade que faltava a AOS-306. Desde que [LevelRegistry.SetLevel] sela
		// ANTES de aplicar, uma selagem falhada devolve `LevelChange{}` e NADA muda — pelo que
		// devolver `changed=true` diria que a demoção aconteceu quando não aconteceu, e
		// `emitTransition` emitiria um span de uma transição L0→L0 com agente vazio. É a
		// assinatura exacta do defeito que AOS-306 fechou na promoção («o sistema diz que fez, e
		// não fez»), no ramo vizinho. A promoção já o trata em [Controller.Evaluate].
		return LevelChange{}, false, err
	}
	c.emitTransition(ctx, ch, directionDemotion, kind, cfg.version)
	return ch, true, err
}

// demote calcula o nível de destino DETERMINÍSTICO de uma demoção: baixa `drop`
// níveis a partir de `cur`, sem nunca descer abaixo de `floor`. Um drop < 1 é
// tratado como 1 (defesa em profundidade; a config já o valida). Ex.: (L4,2,L1)→L2,
// (L3,2,L1)→L1, (L5,2,L1)→L3.
func demote(cur Level, drop int, floor Level) Level {
	if drop < 1 {
		drop = 1
	}
	t := int(cur) - drop
	if t < int(floor) {
		return floor
	}
	return Level(t)
}

// emitTransition abre e fecha o span [OpAutonomyTransition] da transição aplicada
// (AC4/DoD). Só emite quando houve transição efectiva (New != Old é garantido pelos
// chamadores). NUNCA transporta segredos — só o par, os níveis, o sentido, o motivo
// (métrica) e a versão da política. Um tracer nil já foi normalizado para Noop.
func (c *Controller) emitTransition(ctx context.Context, ch LevelChange, direction string, kind AnomalyKind, cfgVersion string) {
	_, span := c.tracer.StartSpan(ctx, OpAutonomyTransition)
	span.SetAttribute(otelgenai.AttrOperationName, OpAutonomyTransition)
	span.SetAttribute(AttrAutonomyAgent, ch.Agent)
	span.SetAttribute(AttrAutonomyDomain, ch.Domain)
	span.SetAttribute(AttrAutonomyOldLevel, ch.Old.String())
	span.SetAttribute(AttrAutonomyNewLevel, ch.New.String())
	span.SetAttribute(AttrAutonomyDirection, direction)
	span.SetAttribute(AttrAutonomyReason, ch.Reason)
	span.SetAttribute(AttrAutonomyConfigVersion, cfgVersion)
	if direction == directionDemotion {
		span.SetAttribute(AttrAutonomyAnomalyKind, kind.String())
	}
	span.End()
}
