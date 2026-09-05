package broker

import (
	"net/url"
	"sort"
	"strings"
)

// AOS-331 — O PROVEDOR AUTORIZADO PASSA A ESTAR AMARRADO AO RECURSO DE DESTINO.
//
// O eixo provider (AOS-324) decide QUEM pode trocar para QUE provedor. Não decide para ONDE a
// credencial desse provedor vai ser apresentada. Um principal autorizado a trocar para `stripe`
// podia pedir a troca com `ResourceValue = https://evil.example/colector` e nada na cadeia
// relacionava as duas coisas: o `ScopeGate` olhava para a capability e para o provedor, e o
// `Resource` atravessava intacto até ao selo.
//
// # PORQUÊ ALLOWLIST DE HOST POR PROVEDOR, E NÃO AS OUTRAS DUAS VIAS DO TICKET
//
// O AC oferecia três. As outras duas não fecham este eixo:
//
//   - **O `EgressGate` real não amarra provedor a recurso.** O `network.EgressHook` deriva o
//     destino do `Resource` e compara-o com uma allowlist de DEPLOYMENT — não sabe o que é um
//     provedor. `Provider=stripe` com destino `evil.example` PASSA se `evil.example` estiver na
//     allowlist do deployment, e é negado com `DeniedBy="egress"`, indistinguível de qualquer
//     outro egress fora da lista. Resolve o egress, não a confusão de deputado.
//   - **Uma obrigação de egress do PDP** exigiria o bundle de política assinado — que é
//     precisamente o bloqueador declarado do `DEF-218`. Fazer este ticket depender dele seria
//     circular: o AOS-331 é pré-condição do wiring, não consequência dele.
//
// A allowlist por provedor é a única que exprime a relação que falta, e exprime-a no vocabulário
// que o operador já usa para o eixo provider.
//
// # ESTADO POR OMISSÃO — DECLARADO, NÃO SILENCIOSO
//
// Sem allowlist, [ResourceBindingUnset]: o eixo não é imposto. O AC proíbe explicitamente um
// deny-all silencioso, e a razão é a mesma do eixo provider — um default que negasse tudo
// partiria todos os deployments que não configuram a coisa nova, e um eixo que ninguém consegue
// ligar não é segurança, é uma avaria. A postura é INTERROGÁVEL e vai ao banner (AOS-332), que é
// o que a distingue de um silêncio.

// ResourceBindingPosture é a postura do eixo recurso↔provedor, no molde de [ProviderPosture].
type ResourceBindingPosture string

const (
	// ResourceBindingUnset — nenhuma allowlist declarada: o eixo não é imposto. Estado
	// DECLARADO e interrogável, não um deny-all silencioso.
	ResourceBindingUnset ResourceBindingPosture = "unset"
	// ResourceBindingEnforced — allowlist declarada: o host do recurso TEM de constar da
	// lista do provedor pedido.
	ResourceBindingEnforced ResourceBindingPosture = "enforced"
)

// resourceBindingPosture deriva a postura do ESTADO, não da intenção: `nil` é «não declarada»;
// um mapa vazio NÃO-nil é uma declaração de que nenhum provedor alcança recurso nenhum, e essa é
// uma escolha legítima de quem quer o eixo fechado. É a mesma distinção de `providerPosture`.
func resourceBindingPosture(providerHosts map[string][]string) ResourceBindingPosture {
	if providerHosts == nil {
		return ResourceBindingUnset
	}
	return ResourceBindingEnforced
}

// authorizeResource verifica que o recurso pedido pertence ao provedor autorizado.
//
// Devolve [ErrResourceOutOfScope] quando o host não consta da lista do provedor, e
// [ErrResourceUndetermined] quando o recurso não permite decidir — um valor que não se analisa,
// ou um tipo que não é de rede. As duas recusas são DISTINTAS de [ErrProviderOutOfScope], que é
// o AC: negar «este provedor não é teu» e negar «este destino não é deste provedor» são
// diagnósticos diferentes e levam o operador a sítios diferentes.
func authorizeResource(providerHosts map[string][]string, provider, resourceType, resourceValue string) error {
	if resourceBindingPosture(providerHosts) == ResourceBindingUnset {
		return nil
	}
	// FAIL-CLOSED SOB POLÍTICA DECLARADA. Um recurso que não se sabe interpretar não pode ser
	// dado como pertencente ao provedor: sob `enforced`, informação insuficiente é recusa —
	// a mesma postura do envelope ilegível no eixo provider.
	if !strings.EqualFold(strings.TrimSpace(resourceType), resourceTypeURL) {
		return ErrResourceUndetermined
	}
	host, ok := hostDeRecurso(resourceValue)
	if !ok {
		return ErrResourceUndetermined
	}
	for _, h := range providerHosts[provider] {
		if strings.EqualFold(strings.TrimSpace(h), host) {
			return nil
		}
	}
	return ErrResourceOutOfScope
}

// resourceTypeURL é o tipo de recurso do contrato C1 que este eixo sabe interpretar.
const resourceTypeURL = "url"

// hostDeRecurso extrai o host (sem porta) de um valor de recurso.
//
// NÃO ACEITA UM VALOR SEM ESQUEMA: `api.stripe.com/x` seria analisado como caminho, com host
// vazio, e um host vazio a comparar com uma lista vazia daria uma coincidência acidental. Exige
// forma absoluta, que é a que o contrato C1 documenta.
func hostDeRecurso(v string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(v))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	h := strings.ToLower(u.Hostname())
	if h == "" {
		return "", false
	}
	return h, true
}

// copyProviderHosts copia a allowlist (nil preserva-se como nil — é a distinção entre «não
// declarada» e «declarada vazia»), no molde de `copyProviderPolicy`.
func copyProviderHosts(m map[string][]string) map[string][]string {
	if m == nil {
		return nil
	}
	cp := make(map[string][]string, len(m))
	for k, v := range m {
		hosts := append([]string(nil), v...)
		sort.Strings(hosts)
		cp[k] = hosts
	}
	return cp
}
