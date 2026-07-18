package autonomy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AOS-090 — os LIMIARES do controlador de autonomia expressos como POLICY-AS-CODE
// versionada (AC3, molde da [github.com/aos-ref/platform/audit.RetentionConfig] de
// AOS-092 e do bundle do PDP de AOS-088): a fronteira promoção/demoção deixa de ser
// constantes Go hardcoded e passa a uma política SERIALIZÁVEL (JSON) com versão
// SemVer e conteúdo com hash — pelo que uma alteração de limiar é rastreável,
// AUDITÁVEL e altera o comportamento do [Controller] em vigor (AC3).

// AutonomyControlConfig são os limiares configuráveis do [Controller]: a fiabilidade
// SUSTENTADA exigida para PROMOVER (error-rate e override-rate máximos ao longo de
// uma janela deslizante) e a agressividade da DEMOÇÃO em anomalia (quantos níveis
// baixa e o piso mais supervisionado). É IMUTÁVEL após validação (só escalares).
//
// Fail-closed: uma versão SemVer inválida, uma taxa fora de [0,1], uma janela <= 0,
// um salto de demoção < 1 ou um nível (piso/tecto) inválido são recusados por
// [AutonomyControlConfig.Validate] e NENHUMA config entra em vigor — uma política
// malformada nunca relaxa a governação.
type AutonomyControlConfig struct {
	version string
	// errorRateMax é a taxa de erro MÁXIMA tolerada para promover (ex.: 0.02 = 2%).
	// A promoção exige ErrorRate <= errorRateMax SUSTENTADO durante toda a janela.
	errorRateMax float64
	// overrideRateMax é o override-rate MÁXIMO tolerado para promover (ex.: 0.40). A
	// promoção exige OverrideRate <= overrideRateMax SUSTENTADO durante toda a janela.
	overrideRateMax float64
	// window é a janela deslizante sobre a qual a fiabilidade tem de ser sustentada
	// (ex.: 720h = 30 dias). Só se promove se os limiares foram cumpridos ao longo de
	// toda a janela (não num instante) — ver [ReliabilitySource].
	window time.Duration
	// demotionDrop é quantos níveis uma anomalia baixa (>= 1). A demoção é
	// determinística: target = max(demotionFloor, current - demotionDrop).
	demotionDrop int
	// demotionFloor é o nível mais supervisionado até onde uma demoção pode descer
	// (nunca abaixo). Uma anomalia num par já no piso mantém-no (nunca promove).
	demotionFloor Level
	// promotionCeil é o tecto de promoção: o [Controller] nunca promove acima dele
	// (conservador). Por omissão [L5].
	promotionCeil Level
}

// autonomyControlConfigWire é a forma-de-fio (JSON) da [AutonomyControlConfig]: a
// janela como duração legível ("720h") e os níveis como rótulos ("L1"), para a
// política ser autorável e revisável como código. Ordem das chaves irrelevante: o
// [AutonomyControlConfig.ContentHash] é canónico.
type autonomyControlConfigWire struct {
	Version         string  `json:"version"`
	ErrorRateMax    float64 `json:"error_rate_max"`
	OverrideRateMax float64 `json:"override_rate_max"`
	Window          string  `json:"window"`
	DemotionDrop    int     `json:"demotion_drop"`
	DemotionFloor   string  `json:"demotion_floor"`
	PromotionCeil   string  `json:"promotion_ceil"`
}

// autonomyControlConfigDomain separa o domínio do conteúdo canónico do controlador
// de autonomia de qualquer outro subsistema (evita colisão de hashes) e versiona o
// formato.
const autonomyControlConfigDomain = "aos.autonomy.control.config.v1"

// ErrInvalidControlConfig — a [AutonomyControlConfig] é malformada (versão SemVer
// inválida, taxa fora de [0,1], janela <= 0, salto < 1, ou piso/tecto inválido). A
// política é RECUSADA e não entra em vigor (fail-closed).
var ErrInvalidControlConfig = errors.New("autonomy: config de controlo invalida")

// LoadAutonomyControlConfig lê e VALIDA uma [AutonomyControlConfig] a partir do seu
// JSON (policy-as-code). Fail-closed: JSON malformado, duração/nível não-parseáveis
// ou qualquer limiar fora do domínio devolvem [ErrInvalidControlConfig] e NENHUMA
// config é produzida.
func LoadAutonomyControlConfig(raw []byte) (AutonomyControlConfig, error) {
	var w autonomyControlConfigWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return AutonomyControlConfig{}, fmt.Errorf("%w: JSON invalido: %v", ErrInvalidControlConfig, err)
	}
	win, err := time.ParseDuration(w.Window)
	if err != nil {
		return AutonomyControlConfig{}, fmt.Errorf("%w: janela %q nao e uma duracao valida: %v",
			ErrInvalidControlConfig, w.Window, err)
	}
	floor, ok := parseLevel(w.DemotionFloor)
	if !ok {
		return AutonomyControlConfig{}, fmt.Errorf("%w: piso de democao %q invalido (fora de L0-L5)",
			ErrInvalidControlConfig, w.DemotionFloor)
	}
	ceil, ok := parseLevel(w.PromotionCeil)
	if !ok {
		return AutonomyControlConfig{}, fmt.Errorf("%w: tecto de promocao %q invalido (fora de L0-L5)",
			ErrInvalidControlConfig, w.PromotionCeil)
	}
	cfg := AutonomyControlConfig{
		version:         w.Version,
		errorRateMax:    w.ErrorRateMax,
		overrideRateMax: w.OverrideRateMax,
		window:          win,
		demotionDrop:    w.DemotionDrop,
		demotionFloor:   floor,
		promotionCeil:   ceil,
	}
	if err := cfg.Validate(); err != nil {
		return AutonomyControlConfig{}, err
	}
	return cfg, nil
}

// NewAutonomyControlConfig constrói uma [AutonomyControlConfig] em código
// (equivalente a [LoadAutonomyControlConfig] sem passar por JSON), validando-a
// fail-closed. Útil para composição programática e testes.
func NewAutonomyControlConfig(version string, errorRateMax, overrideRateMax float64, window time.Duration, demotionDrop int, demotionFloor, promotionCeil Level) (AutonomyControlConfig, error) {
	cfg := AutonomyControlConfig{
		version:         version,
		errorRateMax:    errorRateMax,
		overrideRateMax: overrideRateMax,
		window:          window,
		demotionDrop:    demotionDrop,
		demotionFloor:   demotionFloor,
		promotionCeil:   promotionCeil,
	}
	if err := cfg.Validate(); err != nil {
		return AutonomyControlConfig{}, err
	}
	return cfg, nil
}

// DefaultAutonomyControlConfig é a política de referência do blueprint (tecnica/09
// §7, ADR-014): erro < 2% e override-rate < 40% sustentados por 30 dias promovem;
// uma anomalia baixa 2 níveis (L4→L2, L3→L1) com piso em [L1] e tecto em [L5].
func DefaultAutonomyControlConfig() AutonomyControlConfig {
	return AutonomyControlConfig{
		version:         "1.0.0",
		errorRateMax:    0.02,
		overrideRateMax: DefaultOverrideRateThreshold,
		window:          30 * 24 * time.Hour,
		demotionDrop:    2,
		demotionFloor:   L1,
		promotionCeil:   L5,
	}
}

// DefaultOverrideRateThreshold é o limiar anti-rubber-stamping por omissão (0.40),
// alinhado com o mesmo valor do gate HITL (AOS-095): acima dele a promoção é negada.
const DefaultOverrideRateThreshold = 0.40

// Validate confere a política fail-closed: versão SemVer válida; error/override-rate
// máximos em [0,1]; janela > 0; salto de demoção >= 1; piso e tecto no domínio
// L0–L5; e o tecto não abaixo do piso (uma escada coerente).
func (c AutonomyControlConfig) Validate() error {
	if _, ok := parseSemver3(c.version); !ok {
		return fmt.Errorf("%w: versao %q nao e SemVer MAJOR.MINOR.PATCH", ErrInvalidControlConfig, c.version)
	}
	if c.errorRateMax < 0 || c.errorRateMax > 1 {
		return fmt.Errorf("%w: error_rate_max %v fora de [0,1]", ErrInvalidControlConfig, c.errorRateMax)
	}
	if c.overrideRateMax < 0 || c.overrideRateMax > 1 {
		return fmt.Errorf("%w: override_rate_max %v fora de [0,1]", ErrInvalidControlConfig, c.overrideRateMax)
	}
	if c.window <= 0 {
		return fmt.Errorf("%w: janela %v tem de ser > 0", ErrInvalidControlConfig, c.window)
	}
	if c.demotionDrop < 1 {
		return fmt.Errorf("%w: demotion_drop %d tem de ser >= 1", ErrInvalidControlConfig, c.demotionDrop)
	}
	if !c.demotionFloor.Valid() {
		return fmt.Errorf("%w: piso de democao %v invalido", ErrInvalidControlConfig, c.demotionFloor)
	}
	if !c.promotionCeil.Valid() {
		return fmt.Errorf("%w: tecto de promocao %v invalido", ErrInvalidControlConfig, c.promotionCeil)
	}
	if c.promotionCeil < c.demotionFloor {
		return fmt.Errorf("%w: tecto %v abaixo do piso %v (escada incoerente)",
			ErrInvalidControlConfig, c.promotionCeil, c.demotionFloor)
	}
	return nil
}

// Version devolve a versão SemVer da política em vigor (participa no motivo selado
// e no changelog auditável de cada transição).
func (c AutonomyControlConfig) Version() string { return c.version }

// ErrorRateMax devolve a taxa de erro máxima tolerada para promover.
func (c AutonomyControlConfig) ErrorRateMax() float64 { return c.errorRateMax }

// OverrideRateMax devolve o override-rate máximo tolerado para promover.
func (c AutonomyControlConfig) OverrideRateMax() float64 { return c.overrideRateMax }

// Window devolve a janela deslizante de fiabilidade sustentada.
func (c AutonomyControlConfig) Window() time.Duration { return c.window }

// DemotionDrop devolve quantos níveis uma anomalia baixa.
func (c AutonomyControlConfig) DemotionDrop() int { return c.demotionDrop }

// DemotionFloor devolve o nível mais supervisionado até onde a demoção pode descer.
func (c AutonomyControlConfig) DemotionFloor() Level { return c.demotionFloor }

// PromotionCeil devolve o tecto de promoção.
func (c AutonomyControlConfig) PromotionCeil() Level { return c.promotionCeil }

// canonicalBytes devolve a serialização canónica e determinística da política
// (domínio versionado + versão + limiares por ordem fixa). É a base do
// [AutonomyControlConfig.ContentHash]: independente da ordem de serialização JSON.
func (c AutonomyControlConfig) canonicalBytes() []byte {
	var b strings.Builder
	b.WriteString(autonomyControlConfigDomain)
	b.WriteByte('\n')
	b.WriteString(c.version)
	b.WriteByte('\n')
	b.WriteString("error_rate_max=")
	b.WriteString(strconv.FormatFloat(c.errorRateMax, 'g', -1, 64))
	b.WriteByte('\n')
	b.WriteString("override_rate_max=")
	b.WriteString(strconv.FormatFloat(c.overrideRateMax, 'g', -1, 64))
	b.WriteByte('\n')
	b.WriteString("window=")
	b.WriteString(c.window.String())
	b.WriteByte('\n')
	b.WriteString("demotion_drop=")
	b.WriteString(strconv.Itoa(c.demotionDrop))
	b.WriteByte('\n')
	b.WriteString("demotion_floor=")
	b.WriteString(c.demotionFloor.String())
	b.WriteByte('\n')
	b.WriteString("promotion_ceil=")
	b.WriteString(c.promotionCeil.String())
	b.WriteByte('\n')
	return []byte(b.String())
}

// ContentHash devolve o SHA-256 hex canónico da política — liga uma versão ao
// conteúdo EXACTO que ela declara (molde do content_hash do bundle do PDP). Uma
// alteração de limiar muda o hash, tornando a transição de política detectável.
func (c AutonomyControlConfig) ContentHash() string {
	sum := sha256.Sum256(c.canonicalBytes())
	return hex.EncodeToString(sum[:])
}

// Marshal serializa a config na sua forma-de-fio canónica (JSON com janela/níveis
// legíveis). As chaves de struct do encoder JSON da stdlib são estáveis, pelo que o
// mesmo conteúdo produz sempre os mesmos bytes.
func (c AutonomyControlConfig) Marshal() ([]byte, error) {
	w := autonomyControlConfigWire{
		Version:         c.version,
		ErrorRateMax:    c.errorRateMax,
		OverrideRateMax: c.overrideRateMax,
		Window:          c.window.String(),
		DemotionDrop:    c.demotionDrop,
		DemotionFloor:   c.demotionFloor.String(),
		PromotionCeil:   c.promotionCeil.String(),
	}
	return json.Marshal(w)
}

// parseLevel converte um rótulo canónico ("L0".."L5") num [Level]. Fail-closed: um
// rótulo desconhecido devolve ok=false (nunca um nível arbitrário).
func parseLevel(s string) (Level, bool) {
	switch s {
	case "L0":
		return L0, true
	case "L1":
		return L1, true
	case "L2":
		return L2, true
	case "L3":
		return L3, true
	case "L4":
		return L4, true
	case "L5":
		return L5, true
	default:
		return L0, false
	}
}

// parseSemver3 valida uma versão MAJOR.MINOR.PATCH (prefixo "v" opcional,
// pré-release/build descartados). Fail-closed: uma versão malformada devolve
// ok=false. É a mesma regra do PDP (AOS-088) / retenção (AOS-092), replicada aqui
// para não inverter camadas.
func parseSemver3(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
