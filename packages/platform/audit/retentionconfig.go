package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RetentionConfig é o TTL-por-classe expresso como POLICY-AS-CODE versionada
// (AOS-092/AC4): a correspondência classe→período deixa de ser um mapa Go
// hardcoded e passa a ser uma política SERIALIZÁVEL (JSON), com uma versão SemVer
// e um conteúdo canónico com hash — pelo que uma alteração de TTL é rastreável e
// AUDITÁVEL (ver [BuildRetentionConfigChangedRecord]). É o molde do bundle
// policy-as-code do PDP (AOS-088) aplicado à retenção: versão + conteúdo com hash.
//
// Fail-closed: uma versão SemVer inválida ou um período <= 0 são recusados por
// [RetentionConfig.Validate]; uma classe AUSENTE do mapa significa "sem período
// definido" — NUNCA expira (a mesma semântica conservadora de [RetentionPolicy]).
// A config é imutável após [LoadRetentionConfig] (a cópia do mapa é interna).
type RetentionConfig struct {
	version string
	periods map[DataClass]time.Duration
}

// retentionConfigWire é a forma-de-fio (JSON) da [RetentionConfig]: períodos como
// durações legíveis por humanos (ex. "24h", "720h") em vez de nanossegundos, para
// a política ser autorável e revisável como código. Ordem das chaves irrelevante:
// o hash canónico ([RetentionConfig.ContentHash]) ordena-as.
type retentionConfigWire struct {
	Version string            `json:"version"`
	Periods map[string]string `json:"periods"`
}

// retentionConfigDomain separa o domínio do conteúdo canónico da retenção de
// qualquer outro subsistema (evita colisão de hashes) e versiona o formato.
const retentionConfigDomain = "aos.retention.config.v1"

// LoadRetentionConfig lê e VALIDA uma [RetentionConfig] a partir do seu JSON
// (policy-as-code). Fail-closed: JSON malformado, versão SemVer inválida ou
// qualquer período <= 0 ou não-parseável devolvem [ErrInvalidRetentionConfig] e
// NENHUMA config é produzida — uma política inválida nunca entra em vigor.
func LoadRetentionConfig(raw []byte) (RetentionConfig, error) {
	var w retentionConfigWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return RetentionConfig{}, fmt.Errorf("%w: JSON invalido: %v", ErrInvalidRetentionConfig, err)
	}
	periods := make(map[DataClass]time.Duration, len(w.Periods))
	for cls, durStr := range w.Periods {
		d, err := time.ParseDuration(durStr)
		if err != nil {
			return RetentionConfig{}, fmt.Errorf("%w: periodo %q da classe %q nao e uma duracao valida: %v",
				ErrInvalidRetentionConfig, durStr, cls, err)
		}
		periods[DataClass(cls)] = d
	}
	cfg := RetentionConfig{version: w.Version, periods: periods}
	if err := cfg.Validate(); err != nil {
		return RetentionConfig{}, err
	}
	return cfg, nil
}

// NewRetentionConfig constrói uma [RetentionConfig] em código (equivalente a
// [LoadRetentionConfig] sem passar por JSON), validando-a fail-closed. Útil para
// composição programática e testes.
func NewRetentionConfig(version string, periods map[DataClass]time.Duration) (RetentionConfig, error) {
	cp := make(map[DataClass]time.Duration, len(periods))
	for k, v := range periods {
		cp[k] = v
	}
	cfg := RetentionConfig{version: version, periods: cp}
	if err := cfg.Validate(); err != nil {
		return RetentionConfig{}, err
	}
	return cfg, nil
}

// Validate confere a política fail-closed: a versão tem de ser SemVer válida
// (MAJOR.MINOR.PATCH) e cada período explícito tem de ser > 0. Uma classe sem
// período NÃO é um erro (significa "nunca expira"); listar uma classe com período
// <= 0 é ambíguo e é RECUSADO — para "nunca expira" omite-se a classe.
func (c RetentionConfig) Validate() error {
	if _, ok := parseSemver3(c.version); !ok {
		return fmt.Errorf("%w: versao %q nao e SemVer MAJOR.MINOR.PATCH", ErrInvalidRetentionConfig, c.version)
	}
	for cls, d := range c.periods {
		if d <= 0 {
			return fmt.Errorf("%w: periodo da classe %q e <= 0 (%v); para 'nunca expira' omite a classe",
				ErrInvalidRetentionConfig, cls, d)
		}
	}
	return nil
}

// Version devolve a versão SemVer da política em vigor. É a `ttl_version` que
// participa na idempotency key do job de expiração e no changelog auditável.
func (c RetentionConfig) Version() string { return c.version }

// Period devolve o período de retenção de uma classe e se está definido (>0),
// espelhando [RetentionPolicy.Period] a partir da policy-as-code.
func (c RetentionConfig) Period(class DataClass) (time.Duration, bool) {
	d, ok := c.periods[class]
	if !ok || d <= 0 {
		return 0, false
	}
	return d, true
}

// ToPolicy projecta a config na [RetentionPolicy] já existente (AOS-083) — a
// mesma semântica fail-closed de [RetentionPolicy.Expired] é reutilizada pelo
// [ExpirationJob] e pelo [Shredder], sem duplicar a regra de expiração.
func (c RetentionConfig) ToPolicy() RetentionPolicy {
	return NewRetentionPolicy(c.periods)
}

// canonicalBytes devolve a serialização canónica e determinística da política
// (domínio versionado + versão + classes ordenadas com o seu período). É a base
// do [RetentionConfig.ContentHash]: independente da ordem de iteração do mapa.
func (c RetentionConfig) canonicalBytes() []byte {
	classes := make([]string, 0, len(c.periods))
	for cls := range c.periods {
		classes = append(classes, string(cls))
	}
	sort.Strings(classes)
	var b strings.Builder
	b.WriteString(retentionConfigDomain)
	b.WriteByte('\n')
	b.WriteString(c.version)
	b.WriteByte('\n')
	for _, cls := range classes {
		b.WriteString(cls)
		b.WriteByte('=')
		b.WriteString(c.periods[DataClass(cls)].String())
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// ContentHash devolve o SHA-256 hex canónico da política — liga uma versão ao
// conteúdo EXACTO que ela declara (molde do content_hash do bundle do PDP). Uma
// alteração de TTL muda o hash, tornando a transição detectável no changelog.
func (c RetentionConfig) ContentHash() string {
	sum := sha256.Sum256(c.canonicalBytes())
	return hex.EncodeToString(sum[:])
}

// Marshal serializa a config na sua forma-de-fio canónica (JSON com períodos
// legíveis). Reproduzível: as chaves de mapa do encoder JSON da stdlib são
// ordenadas, pelo que o mesmo conteúdo produz sempre os mesmos bytes.
func (c RetentionConfig) Marshal() ([]byte, error) {
	w := retentionConfigWire{Version: c.version, Periods: make(map[string]string, len(c.periods))}
	for cls, d := range c.periods {
		w.Periods[string(cls)] = d.String()
	}
	return json.Marshal(w)
}

// RetentionConfigDiff resume, de forma DETERMINISTA (ordenada por classe), o que
// mudou entre duas políticas de retenção: classes adicionadas, removidas ou com
// período alterado. É o "diff" do changelog `retention.config.changed` (AC4),
// análogo ao [PolicyDiff] do PDP.
func RetentionConfigDiff(old, cur RetentionConfig) []string {
	classes := make(map[string]struct{})
	for cls := range old.periods {
		classes[string(cls)] = struct{}{}
	}
	for cls := range cur.periods {
		classes[string(cls)] = struct{}{}
	}
	names := make([]string, 0, len(classes))
	for cls := range classes {
		names = append(names, cls)
	}
	sort.Strings(names)

	var diff []string
	for _, name := range names {
		cls := DataClass(name)
		oldD, inOld := old.periods[cls]
		curD, inCur := cur.periods[cls]
		switch {
		case inOld && !inCur:
			diff = append(diff, "removed:"+name+"="+oldD.String())
		case !inOld && inCur:
			diff = append(diff, "added:"+name+"="+curD.String())
		case inOld && inCur && oldD != curD:
			diff = append(diff, "changed:"+name+"="+oldD.String()+"->"+curD.String())
		}
	}
	return diff
}

// parseSemver3 valida uma versão MAJOR.MINOR.PATCH (prefixo "v" opcional,
// pré-release/build descartados). Fail-closed: uma versão malformada devolve
// ok=false. É a mesma regra do PDP (AOS-088) replicada aqui — o audit (platform)
// não importa o pdp (control-plane), pelo que a evitar a inversão de camada.
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
