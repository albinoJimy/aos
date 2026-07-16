package network

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

//go:embed egress_policy.json
var embeddedPolicy []byte

// Prefixos dos selectors de principal na allowlist. O escopo é por principal OU por
// classe de agente: uma regra lista selectors "nhi:<id>" e/ou "class:<classe>" e só
// se aplica a um principal cujo NHIID ou AgentClass casem. A allowlist de A não
// permite B (escopo por principal).
const (
	selectorNHIPrefix   = "nhi:"
	selectorClassPrefix = "class:"
)

// Effect é o veredicto da allowlist de egress.
type Effect string

const (
	// EffectAllow — existe uma regra do principal/classe que permite o destino.
	EffectAllow Effect = "allow"
	// EffectDeny — nenhuma regra aplicável (default-deny) ou destino inválido.
	EffectDeny Effect = "deny"
)

// Erros fail-closed do carregamento/avaliação da allowlist.
var (
	// ErrPolicyMalformed — o documento é inválido, a versão é vazia, o default não é
	// "deny", ou uma regra é ambígua/incompleta (sem principal, sem localizador de
	// destino, porta/CIDR inválidos). Fail-closed: a sandbox recusa arrancar sobre uma
	// allowlist fail-open ou ambígua — nunca allow por omissão.
	ErrPolicyMalformed = errors.New("network: allowlist de egress malformada (default tem de ser deny; regras sem ambiguidade)")
)

// DestinationRule é um conjunto de destinos permitidos por uma [Rule]: hosts
// (nome exacto) e/ou CIDRs (pertença de IP), sempre com uma lista de portas. Um
// destino casa esta regra se a PORTA estiver em Ports E (o host casar um de Hosts
// OU o IP pertencer a um de CIDRs). Uma regra sem localizador (Hosts e CIDRs
// vazios) ou sem portas é malformada (fail-closed).
type DestinationRule struct {
	Hosts []string `json:"hosts,omitempty"`
	CIDRs []string `json:"cidrs,omitempty"`
	Ports []int    `json:"ports"`
}

// Rule é uma regra da allowlist de UM ou MAIS principais/classes: os destinos de
// egress permitidos dentro do seu escopo. Principals lista selectors
// "nhi:<id>"/"class:<classe>".
type Rule struct {
	ID           string            `json:"id"`
	Principals   []string          `json:"principals"`
	Destinations []DestinationRule `json:"destinations"`
}

// Policy é a allowlist de egress carregada, versionada e verificada. É IMUTÁVEL
// após [Load]/[Parse]: os CIDRs são pré-parseados e o digest calculado uma vez.
// Segura para leitura concorrente (sem estado mutável) — o -race guarda
// [Policy.Evaluate] em paralelo.
type Policy struct {
	VersionTag string `json:"version"`
	Default    string `json:"default"`
	Rules      []Rule `json:"rules"`

	compiled []compiledRule // regras pré-parseadas (CIDRs → net.IPNet)
	digest   string         // sha256 hex do conteúdo canónico (versão tamper-evident)
}

// compiledRule é a forma pré-parseada de uma [Rule] para avaliação determinista e
// sem alocação de rede no caminho quente.
type compiledRule struct {
	principals []string
	dests      []compiledDest
}

type compiledDest struct {
	hosts map[string]struct{}
	cidrs []*net.IPNet
	ports map[int]struct{}
}

// Load carrega a allowlist EMBEBIDA (egress_policy.json). Fail-closed se malformada
// ou se o default não for deny — a rede da sandbox nunca fica default-allow.
func Load() (*Policy, error) {
	return Parse(embeddedPolicy)
}

// Parse desserializa e valida a allowlist a partir do JSON, impõe o default-deny e a
// ausência de ambiguidade, pré-parseia os CIDRs e calcula o digest canónico (versão
// tamper-evident). É o único construtor: não há caminho que produza uma [Policy] sem
// validar o default-deny (fail-closed por construção).
func Parse(data []byte) (*Policy, error) {
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPolicyMalformed, err)
	}
	if p.VersionTag == "" {
		return nil, ErrPolicyMalformed
	}
	// FAIL-CLOSED: o default TEM de ser deny. Uma allowlist allow-por-omissão é recusada.
	if p.Default != string(EffectDeny) {
		return nil, ErrPolicyMalformed
	}
	compiled, err := compileRules(p.Rules)
	if err != nil {
		return nil, err
	}
	p.compiled = compiled
	p.digest = canonicalDigest(&p)
	return &p, nil
}

// compileRules valida e pré-parseia todas as regras. Qualquer ambiguidade
// (regra sem id, sem principal, selector vazio, destino sem localizador, porta fora
// de [1,65535] ou CIDR inválido) é REJEITADA fail-closed.
func compileRules(rules []Rule) ([]compiledRule, error) {
	out := make([]compiledRule, 0, len(rules))
	for _, r := range rules {
		if r.ID == "" || len(r.Principals) == 0 || len(r.Destinations) == 0 {
			return nil, ErrPolicyMalformed
		}
		principals := make([]string, 0, len(r.Principals))
		for _, sel := range r.Principals {
			sel = strings.TrimSpace(sel)
			// Um selector tem de ser escopado (nhi:/class:) e não-vazio: um selector
			// ambíguo tornaria o escopo por principal indefinido (fail-closed).
			if sel == "" {
				return nil, ErrPolicyMalformed
			}
			// O selector tem de ter um ID a seguir ao prefixo. Um selector só-prefixo
			// ("nhi:"/"class:") é INERTE (nunca casa em principalInScope, logo deny) mas
			// passaria a validação anti-ambiguidade mascarando uma regra morta que o
			// autor julgaria activa — REJEITA-SE fail-closed no carregamento.
			var id string
			switch {
			case strings.HasPrefix(sel, selectorNHIPrefix):
				id = strings.TrimPrefix(sel, selectorNHIPrefix)
			case strings.HasPrefix(sel, selectorClassPrefix):
				id = strings.TrimPrefix(sel, selectorClassPrefix)
			default:
				return nil, ErrPolicyMalformed // selector não escopado
			}
			if id == "" {
				return nil, ErrPolicyMalformed // selector só-prefixo (id vazio) → inerte
			}
			principals = append(principals, sel)
		}
		dests := make([]compiledDest, 0, len(r.Destinations))
		for _, d := range r.Destinations {
			cd, err := compileDest(d)
			if err != nil {
				return nil, err
			}
			dests = append(dests, cd)
		}
		out = append(out, compiledRule{principals: principals, dests: dests})
	}
	return out, nil
}

// compileDest valida e pré-parseia um destino. Fail-closed: sem localizador
// (host/CIDR), sem porta, porta fora de [1,65535], host vazio ou CIDR inválido = erro.
func compileDest(d DestinationRule) (compiledDest, error) {
	if len(d.Hosts) == 0 && len(d.CIDRs) == 0 {
		return compiledDest{}, ErrPolicyMalformed // destino sem localizador → ambíguo
	}
	if len(d.Ports) == 0 {
		return compiledDest{}, ErrPolicyMalformed // sem porta → nada útil autoriza
	}
	hosts := make(map[string]struct{}, len(d.Hosts))
	for _, h := range d.Hosts {
		h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
		if h == "" {
			return compiledDest{}, ErrPolicyMalformed
		}
		hosts[h] = struct{}{}
	}
	cidrs := make([]*net.IPNet, 0, len(d.CIDRs))
	for _, c := range d.CIDRs {
		_, ipnet, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			return compiledDest{}, ErrPolicyMalformed // CIDR inválido → ambíguo → deny
		}
		// FAIL-CLOSED: um CIDR catch-all (0.0.0.0/0, ::/0) concederia egress
		// irrestrito ao principal nas portas listadas, anulando o propósito
		// anti-exfiltração e sendo indistinguível de uma allowlist restritiva. Um
		// prefixo /0 é REJEITADO no carregamento — egress irrestrito não é
		// exprimível por omissão silenciosa.
		if ones, _ := ipnet.Mask.Size(); ones == 0 {
			return compiledDest{}, ErrPolicyMalformed // CIDR catch-all → recusado
		}
		cidrs = append(cidrs, ipnet)
	}
	ports := make(map[int]struct{}, len(d.Ports))
	for _, port := range d.Ports {
		if port <= 0 || port > 65535 {
			return compiledDest{}, ErrPolicyMalformed
		}
		ports[port] = struct{}{}
	}
	return compiledDest{hosts: hosts, cidrs: cidrs, ports: ports}, nil
}

// Evaluate decide allow/deny para (principal, destino). DEFAULT-DENY: só devolve
// allow se existir uma regra cujo escopo inclua o principal/classe E que case o
// destino explicitamente (host exacto OU IP em CIDR) E a porta. A ausência de
// correspondência — ou um destino inválido — é deny (fail-closed). O escopo é por
// principal: a regra de A nunca autoriza o principal B.
func (p *Policy) Evaluate(principal referencemonitor.Principal, dest Destination) Effect {
	if !dest.valid() {
		return EffectDeny // destino sem porta/localizador → jurisdição indefinida
	}
	for _, r := range p.compiled {
		if !principalInScope(r.principals, principal) {
			continue
		}
		for _, d := range r.dests {
			if _, ok := d.ports[dest.Port]; !ok {
				continue
			}
			if destMatches(d, dest) {
				return EffectAllow
			}
		}
	}
	return EffectDeny
}

// principalInScope indica se algum selector da regra casa o principal por NHIID ou
// por AgentClass. Um principal sem NHIID e sem AgentClass nunca casa (fail-closed:
// identidade indefinida ⇒ deny).
func principalInScope(selectors []string, principal referencemonitor.Principal) bool {
	for _, sel := range selectors {
		if id := strings.TrimPrefix(sel, selectorNHIPrefix); id != sel {
			if principal.NHIID != "" && id == principal.NHIID {
				return true
			}
			continue
		}
		if cls := strings.TrimPrefix(sel, selectorClassPrefix); cls != sel {
			if principal.AgentClass != "" && cls == principal.AgentClass {
				return true
			}
		}
	}
	return false
}

// destMatches indica se o destino casa o localizador da regra: host exacto OU IP
// pertencente a um dos CIDRs. A porta já foi validada pelo chamador.
func destMatches(d compiledDest, dest Destination) bool {
	if dest.Host != "" {
		if _, ok := d.hosts[dest.Host]; ok {
			return true
		}
	}
	if dest.IP != "" {
		ip := net.ParseIP(dest.IP)
		if ip != nil {
			for _, n := range d.cidrs {
				if n.Contains(ip) {
					return true
				}
			}
		}
	}
	return false
}

// Version devolve a identidade VERSIONADA e tamper-evident da allowlist:
// "versão#digest12". É o valor selado no audit WORM e no span em cada decisão, para
// ligar a decisão à versão EXACTA da allowlist em vigor (policy-as-code, ADR-011).
func (p *Policy) Version() string {
	return p.VersionTag + "#" + p.digest[:12]
}

// Hash devolve o digest sha256 COMPLETO (hex) do conteúdo canónico da allowlist —
// estável call-a-call e sensível a qualquer alteração de regra (tamper-evident).
func (p *Policy) Hash() string { return p.digest }

// canonicalDigest calcula um sha256 determinista do CONTEÚDO da allowlist (versão,
// default e regras normalizadas: principals/hosts/cidrs/ports ordenados, regras
// ordenadas por id), independente de espaços/ordem no JSON original. É a base da
// versão tamper-evident. Stdlib apenas.
func canonicalDigest(p *Policy) string {
	type canonDest struct {
		Hosts []string `json:"hosts"`
		CIDRs []string `json:"cidrs"`
		Ports []int    `json:"ports"`
	}
	type canonRule struct {
		ID         string      `json:"id"`
		Principals []string    `json:"principals"`
		Dests      []canonDest `json:"dests"`
	}
	rules := make([]canonRule, 0, len(p.Rules))
	for _, r := range p.Rules {
		dests := make([]canonDest, 0, len(r.Destinations))
		for _, d := range r.Destinations {
			dests = append(dests, canonDest{
				Hosts: sortedLowerCopy(d.Hosts),
				CIDRs: sortedCopy(d.CIDRs),
				Ports: sortedIntCopy(d.Ports),
			})
		}
		sort.Slice(dests, func(i, j int) bool { return canonDestKey(dests[i]) < canonDestKey(dests[j]) })
		rules = append(rules, canonRule{
			ID:         r.ID,
			Principals: sortedCopy(r.Principals),
			Dests:      dests,
		})
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

func canonDestKey(d struct {
	Hosts []string `json:"hosts"`
	CIDRs []string `json:"cidrs"`
	Ports []int    `json:"ports"`
}) string {
	return strings.Join(d.Hosts, ",") + "|" + strings.Join(d.CIDRs, ",") + "|" + fmt.Sprint(d.Ports)
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sortedLowerCopy(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
	}
	sort.Strings(out)
	return out
}

func sortedIntCopy(in []int) []int {
	out := append([]int(nil), in...)
	sort.Ints(out)
	return out
}
