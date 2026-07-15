package authn

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// A POLÍTICA DE VALIDAÇÃO DO TOKEN como policy-as-code VERSIONADA e default-deny
// (AOS-057, ADR-011, ADR-002). É a camada de AUTORIZAÇÃO que corre DEPOIS de o
// token ter passado a validação criptográfica/temporal/cadeia do Verifier de
// identidade (AOS-005/006): dado o (operação, classe de agente, autoridade
// efectiva), decide allow/deny. A AUSÊNCIA de uma regra aplicável é DENY
// (default-deny, fail-closed) — nunca allow por omissão.
//
// A política vive num ficheiro VERSIONADO (token_policy.json), embebido no
// binário via go:embed, com um digest de conteúdo (sha256) que a torna
// tamper-evident: [Policy.Version] devolve "versão#digest", que o registo de
// atribuição sela no audit WORM (PolicyVersion) — a decisão fica ligada à versão
// EXACTA da política em vigor.
//
// Decisão de desenho (porquê NÃO cedar aqui): o PDP/cedar (control-plane/pdp)
// impõe a política das TOOL CALLS e traria cedar-go + golang.org/x/exp ao módulo
// data-plane do gateway. A validação do token do principal é um predicado simples
// (operação × classe × capabilities ⊆ autoridade) com o MESMO default-deny; é
// expressa aqui como uma policy-as-code equivalente, versionada e testada
// (allow/deny), mantendo o gateway zero-dep. A semântica default-deny é idêntica
// à do cedar (sem permit explícito ⇒ deny).

//go:embed token_policy.json
var embeddedPolicy []byte

// wildcard corresponde a qualquer valor num campo de correspondência da regra.
const wildcard = "*"

// Effect é o veredicto da política.
type Effect string

const (
	// EffectAllow — existe uma regra aplicável que autoriza.
	EffectAllow Effect = "allow"
	// EffectDeny — nenhuma regra aplicável (default-deny) ou default explícito.
	EffectDeny Effect = "deny"
)

// ErrPolicyMalformed — o documento de política é inválido ou o seu default não é
// "deny". Uma política cujo default não seja deny é REJEITADA no carregamento
// (fail-closed): o gateway recusa arrancar com uma política fail-open.
var ErrPolicyMalformed = errors.New("authn: politica de token malformada (default tem de ser deny)")

// Rule é uma regra de autorização de token. Uma chamada é autorizada por esta
// regra se a operação e a classe de agente corresponderem (valor exacto ou "*")
// E todas as require_capabilities estiverem na autoridade efectiva do principal.
type Rule struct {
	ID                  string   `json:"id"`
	Operations          []string `json:"operations"`
	AgentClasses        []string `json:"agent_classes"`
	RequireCapabilities []string `json:"require_capabilities"`
}

// Policy é a política de validação de token carregada e versionada.
type Policy struct {
	VersionTag string `json:"version"`
	Default    string `json:"default"`
	Rules      []Rule `json:"rules"`

	digest string // sha256 hex do conteúdo canónico (integridade/versão)
}

// PolicyInput é o pedido de decisão: a operação (chat/embeddings), a classe do
// agente do token validado e a AUTORIDADE EFECTIVA (utilizador ∩ classe) já
// computada pelo estágio de authn.
type PolicyInput struct {
	Operation  string
	AgentClass string
	Authority  []string
}

// LoadPolicy carrega a política EMBEBIDA (token_policy.json). Fail-closed se
// malformada ou se o default não for deny.
func LoadPolicy() (*Policy, error) { return LoadPolicyFromBytes(embeddedPolicy) }

// LoadPolicyFromBytes carrega e valida uma política a partir de bytes JSON (uso
// de teste/wiring alternativo). Calcula o digest de conteúdo (versão
// tamper-evident) e impõe o default-deny.
func LoadPolicyFromBytes(b []byte) (*Policy, error) {
	var p Policy
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPolicyMalformed, err)
	}
	if p.Default != string(EffectDeny) {
		return nil, ErrPolicyMalformed
	}
	if p.VersionTag == "" {
		return nil, ErrPolicyMalformed
	}
	p.digest = canonicalDigest(&p)
	return &p, nil
}

// Version devolve a identidade VERSIONADA e tamper-evident da política:
// "versão#digest12". É o valor selado no audit WORM (PolicyVersion) para ligar
// cada decisão à versão EXACTA da política.
func (p *Policy) Version() string {
	return p.VersionTag + "#" + p.digest[:12]
}

// Evaluate decide allow/deny para o input. DEFAULT-DENY: só devolve allow se
// existir uma regra aplicável; a ausência de correspondência é deny.
func (p *Policy) Evaluate(in PolicyInput) Effect {
	authSet := make(map[string]struct{}, len(in.Authority))
	for _, a := range in.Authority {
		authSet[a] = struct{}{}
	}
	for _, r := range p.Rules {
		if !matches(r.Operations, in.Operation) {
			continue
		}
		if !matches(r.AgentClasses, in.AgentClass) {
			continue
		}
		if !capabilitiesSatisfied(r.RequireCapabilities, authSet) {
			continue
		}
		return EffectAllow
	}
	return EffectDeny
}

// matches indica se value corresponde a algum elemento de list (valor exacto ou
// wildcard "*"). Uma lista vazia NÃO corresponde a nada (fail-closed: uma regra
// sem operações/classes nunca autoriza).
func matches(list []string, value string) bool {
	for _, item := range list {
		if item == wildcard || item == value {
			return true
		}
	}
	return false
}

// capabilitiesSatisfied indica se todas as capabilities exigidas estão na
// autoridade efectiva (require ⊆ authority).
func capabilitiesSatisfied(require []string, authority map[string]struct{}) bool {
	for _, c := range require {
		if _, ok := authority[c]; !ok {
			return false
		}
	}
	return true
}

// canonicalDigest calcula um sha256 determinista do CONTEÚDO da política (versão,
// default e regras normalizadas), independente de espaços/ordem do JSON original.
// É a base da versão tamper-evident.
func canonicalDigest(p *Policy) string {
	type canonRule struct {
		ID    string   `json:"id"`
		Ops   []string `json:"operations"`
		Class []string `json:"agent_classes"`
		Caps  []string `json:"require_capabilities"`
	}
	rules := make([]canonRule, 0, len(p.Rules))
	for _, r := range p.Rules {
		cr := canonRule{
			ID:    r.ID,
			Ops:   sortedCopy(r.Operations),
			Class: sortedCopy(r.AgentClasses),
			Caps:  sortedCopy(r.RequireCapabilities),
		}
		rules = append(rules, cr)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	canon := struct {
		Version string      `json:"version"`
		Default string      `json:"default"`
		Rules   []canonRule `json:"rules"`
	}{Version: p.VersionTag, Default: p.Default, Rules: rules}
	b, _ := json.Marshal(canon)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
