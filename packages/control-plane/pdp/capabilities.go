package pdp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// AOS-007 — Capability allowlist default-deny.
//
// A allowlist é um RECURSO DE POLÍTICA declarativo, keyed por agent_class (a
// classe da NHI, claim `agent_class` do token — AOS-005), que enumera
// EXPLICITAMENTE as capabilities que cada classe pode exercer. Vive no bundle
// assinado do PDP (AOS-004) em `policies/capabilities/*.json`: entra no
// content_hash canónico e na assinatura ed25519, logo ADICIONAR/ALTERAR uma
// capability EXIGE re-assinatura (sem allow implícito — AC#4/DoD).
//
// GATE DEFAULT-DENY. [PDP.Decide] impõe, ANTES de qualquer regra Cedar, que a
// (agent_class, capability) esteja explicitamente na allowlist; o que não está
// concedido é negado (fail-closed). A blocklist anterior falhava ABERTA a cada
// tool nova; aqui uma capability/tool nova sem entrada é negada até ser
// explicitamente permitida por política assinada.
//
// SEM WILDCARDS PERIGOSOS POR OMISSÃO. Uma entrada "*" só concede se trouxer uma
// `justification` não-vazia; um wildcard sem justificação é IGNORADO (não
// concede nada) — nunca abre um buraco default-allow silencioso.

// capabilitiesDir é o subdirectório do bundle que aloja os ficheiros de
// allowlist de capabilities (parte do bundle assinado).
const capabilitiesDir = "capabilities"

// wildcardCap é a entrada especial que representa "qualquer capability" para uma
// classe. Só é honrada com justificação explícita (ver [allowlistDoc]).
const wildcardCap = "*"

// capEntry é uma concessão de capability numa classe. `Justification` é
// OBRIGATÓRIA para a entrada wildcard ("*") e opcional (documental) nas demais.
type capEntry struct {
	Cap           string `json:"cap"`
	Justification string `json:"justification,omitempty"`
}

// classEntry são as concessões de uma agent_class.
type classEntry struct {
	Capabilities []capEntry `json:"capabilities"`
}

// allowlistDoc é a forma on-disk (JSON) do recurso de allowlist. `SchemaVersion`
// versiona o formato do recurso (distinto da policy_version do bundle).
type allowlistDoc struct {
	SchemaVersion int                   `json:"schema_version"`
	Classes       map[string]classEntry `json:"classes"`
}

// Allowlist é a allowlist COMPILADA em memória (imutável após construção, logo
// segura para avaliação concorrente e pura). É materializada uma vez no load do
// bundle e trocada atomicamente com o motor em hot-reload.
type Allowlist struct {
	// byClass mapeia agent_class → conjunto de capabilities explicitamente
	// concedidas.
	byClass map[string]map[string]struct{}
	// wildcard é o conjunto de agent_class com wildcard JUSTIFICADO (concede
	// qualquer capability). Vazio por omissão (sem wildcards perigosos).
	wildcard map[string]struct{}
}

// parseAllowlist funde todos os ficheiros de allowlist (`capabilities/*.json`)
// presentes no bundle numa única [Allowlist] compilada. A ausência de qualquer
// ficheiro produz uma allowlist VAZIA — que, por default-deny, nega toda a
// capability (fail-closed): a allowlist ausente nunca é tratada como
// permissiva. Entradas wildcard sem justificação são ignoradas.
//
// files são os ficheiros do bundle já verificados pela assinatura (as chaves
// usam sempre "/" — ver bundle.go); só as que estão sob `capabilities/` e
// terminam em `.json` são consideradas.
func parseAllowlist(files map[string][]byte) (*Allowlist, error) {
	al := &Allowlist{
		byClass:  make(map[string]map[string]struct{}),
		wildcard: make(map[string]struct{}),
	}

	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names) // ordem determinística da fusão (reason estável)

	for _, n := range names {
		if !strings.HasPrefix(n, capabilitiesDir+"/") || !strings.HasSuffix(n, ".json") {
			continue
		}
		var doc allowlistDoc
		if err := json.Unmarshal(files[n], &doc); err != nil {
			return nil, fmt.Errorf("%w: allowlist de capabilities %q invalida: %v", ErrPolicyUnavailable, n, err)
		}
		for class, ce := range doc.Classes {
			if class == "" {
				continue // classe degenerada nunca concede (fail-closed)
			}
			set := al.byClass[class]
			if set == nil {
				set = make(map[string]struct{})
				al.byClass[class] = set
			}
			for _, e := range ce.Capabilities {
				cap := strings.TrimSpace(e.Cap)
				if cap == "" {
					continue
				}
				if cap == wildcardCap {
					// Sem wildcards perigosos por omissão: só com justificação
					// explícita. Sem ela, a entrada é IGNORADA (não concede nada).
					if strings.TrimSpace(e.Justification) == "" {
						continue
					}
					al.wildcard[class] = struct{}{}
					continue
				}
				set[cap] = struct{}{}
			}
		}
	}
	return al, nil
}

// permits decide, por default-deny, se a agent_class pode exercer a capability.
// Devolve (autorizado, motivo). O motivo de negação contém "default-deny" para
// ser inequívoco no audit e nos eventos de negação (AC#2).
//
// FRONTEIRA DE CONFIANÇA. agentClass é assumida RESOLVIDA de uma NHI verificada
// (hook de identidade real, AOS-005/006), NÃO um valor bruto vindo do caller. Sob
// um IdentityStub pass-through a classe é forjável e este gate é contornável — ver
// a nota no cabeçalho deste ficheiro e em [PDP.Decide].
//
// Regras (todas fail-closed):
//   - agent_class vazia ⇒ deny (uma NHI sem classe não tem allowlist);
//   - classe com wildcard JUSTIFICADO ⇒ allow (concessão ampla, mas explícita e
//     assinada);
//   - capability explicitamente listada na classe ⇒ allow;
//   - caso contrário ⇒ deny (a ausência de concessão é recusa).
func (a *Allowlist) permits(agentClass, capability string) (bool, string) {
	if a == nil {
		return false, fmt.Sprintf("capability %q negada: allowlist indisponivel (default-deny)", capability)
	}
	if agentClass == "" {
		return false, fmt.Sprintf(
			"capability %q negada: principal sem agent_class, nao consta de nenhuma allowlist (default-deny)",
			capability)
	}
	if _, ok := a.wildcard[agentClass]; ok {
		return true, fmt.Sprintf("capability %q permitida por wildcard justificado da classe %q", capability, agentClass)
	}
	if set, ok := a.byClass[agentClass]; ok {
		if _, ok := set[capability]; ok {
			return true, fmt.Sprintf("capability %q consta da allowlist da classe %q", capability, agentClass)
		}
	}
	return false, fmt.Sprintf(
		"capability %q nao consta da allowlist da classe %q (default-deny)",
		capability, agentClass)
}

// Classes devolve as agent_class conhecidas na allowlist (ordenadas). Auxiliar
// de introspecção/teste; não participa na decisão.
func (a *Allowlist) Classes() []string {
	if a == nil {
		return nil
	}
	out := make([]string, 0, len(a.byClass))
	for c := range a.byClass {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}
