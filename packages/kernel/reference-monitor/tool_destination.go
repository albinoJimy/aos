package referencemonitor

import (
	"sort"
	"strings"
)

// ToolDestination são os eixos de DESTINO que o registo de tools (config trusted do operador,
// nunca saída do modelo) declara para UMA tool (AOS-538, emenda de 2026-10-19 ao ADR-034).
//
// Cada eixo guarda o valor TAL COMO DECLARADO, normalizado (minúsculas, sem espaços). O vazio é
// IMPLÍCITO — o manifesto não o declarou — e nunca é lido como benigno: é a distinção que a
// emenda exige («os três explícitos no manifesto»), e que o registo perde se se guardar só o valor
// já resolvido (o `egress` ausente resolve `none` no contrato do digest; a `mutation` ausente
// resolve `mutates`).
//
// Capability é a capability do binding REGISTADO. A classificação só vale para uma call cuja
// capability é essa: uma call que diga o nome da tool com outra capability não veio daquele
// binding, e fica com a regra de antes.
type ToolDestination struct {
	Capability    string
	Egress        string // none | internal | external; "" implícito
	Mutation      string // none | mutates; "" implícito
	Reversibility string // reversible | irreversible; "" implícito
}

func normalizeAxis(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// IsLocalRead diz se a tool se DECLARA leitura local: `egress: "none"`, `mutation: "none"` e
// `reversibility: "reversible"`, os três explícitos. Um eixo implícito dá false. É condição
// necessária e não suficiente para a tool ficar fora do gate de taint: a capability e o recurso da
// call também não podem denunciar egress ([DestinationPrivileged.ClassifyCall]).
func (d ToolDestination) IsLocalRead() bool {
	return normalizeAxis(d.Egress) == "none" &&
		normalizeAxis(d.Mutation) == "none" &&
		normalizeAxis(d.Reversibility) == "reversible"
}

// DeclaresEffect diz se a tool DECLARA um efeito: egress diferente de `none`, mutação diferente
// de `none`, ou reversibilidade diferente de `reversible` — só eixos explícitos contam. Com o gate
// armado, uma tool que declara um efeito é privilegiada mesmo que a sua capability não esteja em
// AOS_PRIVILEGED_CAPS.
func (d ToolDestination) DeclaresEffect() bool {
	eg, mu, rv := normalizeAxis(d.Egress), normalizeAxis(d.Mutation), normalizeAxis(d.Reversibility)
	return (eg != "" && eg != "none") || (mu != "" && mu != "none") || (rv != "" && rv != "reversible")
}

// ToolDestinations é o mapa IMUTÁVEL nome-da-tool → [ToolDestination], construído no arranque a
// partir do registo de tools. A chave é o [Call.ToolID] — o campo que já atravessa as duas vias de
// despacho (a directa e a Activity durável) —, pelo que classificar por ele não acrescenta nada à
// via durável (a classe de defeito do ADR-034 §2.8: um campo novo que uma das vias esquece).
// Seguro para concorrência: só leitura depois de construído.
type ToolDestinations struct {
	m map[string]ToolDestination
}

// NewToolDestinations copia e normaliza o mapa dado. Entradas com nome vazio são ignoradas.
func NewToolDestinations(in map[string]ToolDestination) ToolDestinations {
	m := make(map[string]ToolDestination, len(in))
	for name, d := range in {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		m[name] = ToolDestination{
			Capability:    strings.TrimSpace(d.Capability),
			Egress:        normalizeAxis(d.Egress),
			Mutation:      normalizeAxis(d.Mutation),
			Reversibility: normalizeAxis(d.Reversibility),
		}
	}
	return ToolDestinations{m: m}
}

// Len devolve o número de tools com destino declarado.
func (t ToolDestinations) Len() int { return len(t.m) }

// forCall devolve o destino declarado da tool desta call, só quando a capability da call é a do
// binding registado. Tool desconhecida, ou capability diferente ⇒ (zero, false): a regra de antes.
func (t ToolDestinations) forCall(call *Call) (ToolDestination, bool) {
	if call == nil || len(t.m) == 0 {
		return ToolDestination{}, false
	}
	d, ok := t.m[call.ToolID]
	if !ok || d.Capability == "" || d.Capability != strings.TrimSpace(call.Capability) {
		return ToolDestination{}, false
	}
	return d, true
}

// LocalReads devolve, ordenados, os nomes das tools que se declaram leitura local e cuja capability
// não denuncia egress — as que o gate de taint deixa passar com o contexto untrusted. É o que o
// banner de arranque nomeia. O recurso de cada call ainda pode tirar-lhe a excepção (um recurso de
// rede), e esse só se conhece na call.
func (t ToolDestinations) LocalReads() []string {
	var out []string
	for name, d := range t.m {
		if d.IsLocalRead() && !derivesEgress(&Call{Capability: d.Capability}) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// Razões, em vocabulário FECHADO, com que o [TaintGate] carimba [CallContext.TaintPrivilege] — o
// porquê de a call ter sido (ou não) tratada como privilegiada, selado no evento de mediação
// (AOS-538). Só são escritas quando o classificador do gate conhece o destino das tools
// ([ToolPrivilegeClassifier]) e o gate está armado.
const (
	// TaintPrivilegeLocalRead — NÃO privilegiada: a tool declara-se leitura local (os três eixos
	// explícitos), seja qual for a capability.
	TaintPrivilegeLocalRead = "local_read"
	// TaintPrivilegeNotListed — NÃO privilegiada: a capability não está em AOS_PRIVILEGED_CAPS e a
	// tool não declara efeito (ou não tem entrada no registo).
	TaintPrivilegeNotListed = "capability_not_listed"
	// TaintPrivilegeCapability — privilegiada pela capability (a regra de antes).
	TaintPrivilegeCapability = "capability"
	// TaintPrivilegeDeclaredEffect — privilegiada pelo efeito que a tool declara (egress, mutação
	// ou irreversibilidade), com a capability fora de AOS_PRIVILEGED_CAPS.
	TaintPrivilegeDeclaredEffect = "declared_effect"
)

// ToolPrivilegeClassifier é a extensão OPCIONAL de [PrivilegedAuthorizer] que classifica a CALL
// (tool + capability) e não só a capability, e diz porquê. O [TaintGate] usa-a quando o
// classificador a implementa; sem ela, a regra é a de antes ([PrivilegedAuthorizer.IsPrivileged]).
type ToolPrivilegeClassifier interface {
	PrivilegedAuthorizer
	// ClassifyCall devolve se a call é privilegiada para o gate de taint e a razão (uma das
	// constantes TaintPrivilege*; vazia com o gate desarmado).
	ClassifyCall(call *Call) (privileged bool, reason string)
}

// DestinationPrivileged compõe o conjunto de capabilities privilegiadas (AOS_PRIVILEGED_CAPS) com
// o destino que o registo de tools declara (AOS-538, emenda de 2026-10-19 ao ADR-034):
//
//   - gate desarmado (o conjunto base não classifica nada) ⇒ nada é privilegiado, como antes;
//   - tool que se declara leitura local ⇒ NUNCA privilegiada, seja qual for a capability;
//   - capability no conjunto base ⇒ privilegiada (a regra de antes);
//   - tool que declara um efeito ⇒ privilegiada;
//   - tudo o resto ⇒ não privilegiada (a regra de antes).
//
// Uma tool sem entrada no registo, com um eixo implícito, ou pedida com uma capability diferente
// da do seu binding, fica com a regra de antes (fail-closed: nunca ganha a excepção).
type DestinationPrivileged struct {
	base  PrivilegedAuthorizer
	dests ToolDestinations
}

var _ ToolPrivilegeClassifier = DestinationPrivileged{}
var _ EffectivePrivilegedAuthorizer = DestinationPrivileged{}

// NewDestinationPrivileged compõe o classificador. `base` nil ⇒ desarmado.
func NewDestinationPrivileged(base PrivilegedAuthorizer, dests ToolDestinations) DestinationPrivileged {
	return DestinationPrivileged{base: base, dests: dests}
}

// IsPrivileged implementa [PrivilegedAuthorizer] pela capability apenas (a regra de antes). O
// [TaintGate] não a usa quando o classificador implementa [ToolPrivilegeClassifier].
func (d DestinationPrivileged) IsPrivileged(capability string) bool {
	return d.base != nil && d.base.IsPrivileged(capability)
}

// HasPrivileged implementa [EffectivePrivilegedAuthorizer]: a eficácia é a do conjunto base. Os
// destinos declarados não armam um gate desarmado — com AOS_PRIVILEGED_CAPS vazia nada muda.
func (d DestinationPrivileged) HasPrivileged() bool { return privilegedIsEffective(d.base) }

// LocalReads devolve as tools que ficam livres do gate por serem leitura local (para o banner).
func (d DestinationPrivileged) LocalReads() []string { return d.dests.LocalReads() }

// ClassifyCall implementa [ToolPrivilegeClassifier].
func (d DestinationPrivileged) ClassifyCall(call *Call) (bool, string) {
	if call == nil || !privilegedIsEffective(d.base) {
		return false, ""
	}
	dest, known := d.dests.forCall(call)
	if known && dest.IsLocalRead() && !derivesEgress(call) {
		return false, TaintPrivilegeLocalRead
	}
	if d.base.IsPrivileged(call.Capability) {
		return true, TaintPrivilegeCapability
	}
	if known && dest.DeclaresEffect() {
		return true, TaintPrivilegeDeclaredEffect
	}
	return false, TaintPrivilegeNotListed
}

// derivesEgress diz se a capability ou o recurso da call DENUNCIAM egress, pela mesma derivação
// que o classificador de risco usa ([egressForCall]): uma capability de rede, um recurso de rede, ou
// uma capability que não está na lista das provadamente locais (indeterminada conta como externa,
// fail-closed). Uma declaração `egress: "none"` não desfaz isto: um `cap:http.post` declarado
// leitura local continua privilegiado (AOS-538, achado M-4 da revisão).
func derivesEgress(call *Call) bool { return egressForCall(call).IsExternal() }
