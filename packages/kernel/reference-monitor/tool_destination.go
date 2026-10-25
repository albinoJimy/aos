package referencemonitor

import (
	"sort"
	"strings"

	"github.com/aos-ref/kernel/reference-monitor/risk"
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
//
// Sensitivity é a SENSIBILIDADE DO RECURSO que a tool lê, declarada pelo operador (AOS-539): o
// eixo de sensibilidade do classificador de risco, no vocabulário dele (public | internal |
// sensitive). Não entra no gate de taint — só no classificador de risco ([RiskClassifier]).
type ToolDestination struct {
	Capability    string
	Egress        string // none | internal | external; "" implícito
	Mutation      string // none | run_artifact | mutates; "" implícito
	Reversibility string // reversible | irreversible; "" implícito
	Sensitivity   string // public | internal | sensitive; "" implícito (lê-se sensível)
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

// MutationRunArtifact é o valor do eixo de mutação de uma tool que ESCREVE SÓ no armazém de
// artefactos do PRÓPRIO run (AOS-549, emenda de 2026-10-22 ao ADR-034, decisão do dono): append-only,
// no âmbito do run, cifrado por titular e apagável pela DSAR, sem rede.
const MutationRunArtifact = "run_artifact"

// ArtifactWriteCapability é a ÚNICA capability que pode ser escrita local do run (AOS-549). O
// kernel fixa-a ele próprio, para que um consumidor de [NewToolDestinations] que não passe pelo
// validador do registo do nó não estenda a excepção a outra capability.
const ArtifactWriteCapability = "cap:artifact.write"

// IsLocalRunWrite diz se a tool se DECLARA escrita local do run: `egress: "none"`,
// `mutation: "run_artifact"` e `reversibility: "reversible"`, os três explícitos (AOS-549). Como em
// [IsLocalRead], é condição necessária e não suficiente: a capability e o recurso da call também não
// podem denunciar egress. O registo do nó só aceita este eixo na tool de código do nó que grava no
// armazém (`artifact_write`), com a capability dela.
func (d ToolDestination) IsLocalRunWrite() bool {
	return strings.ToLower(strings.TrimSpace(d.Capability)) == ArtifactWriteCapability &&
		normalizeAxis(d.Egress) == "none" &&
		normalizeAxis(d.Mutation) == MutationRunArtifact &&
		normalizeAxis(d.Reversibility) == "reversible"
}

// Classes de DESTINO de uma tool (AOS-556, ADR-044 §2.2): a classe do efeito que o registo de
// tools DECLARA, derivada dos TRÊS eixos que ele já exige explícitos. É o que a política de fluxo
// de dados interroga, e é config trusted do operador — nunca saída do modelo.
//
// PORQUE UMA CLASSE E NÃO OS TRÊS EIXOS CRUS. Três atributos novos no motor Cedar são três
// superfícies de regra mal-atributada (e uma regra mal-atributada assina e nega TUDO daquela
// capability), e a regra que os combinasse repetiria em Cedar a derivação que este ficheiro já faz
// — duas cópias da mesma regra, que é a forma de defeito que este repositório paga repetidamente.
//
// O `egress` SOZINHO não serve, e foi medido: a `ocr_read` e o `web_post` declaram os DOIS
// `egress: external`. O que os separa é a `mutation`, declarada ao lado.
const (
	// DestinationLocalRead — lê e não sai nem muda: [ToolDestination.IsLocalRead].
	DestinationLocalRead = "local_read"
	// DestinationRunArtifact — escreve só no armazém do próprio run:
	// [ToolDestination.IsLocalRunWrite].
	DestinationRunArtifact = "run_artifact"
	// DestinationExternalRead — SAI para fora e não muda nada: o conteúdo vai a um terceiro (o
	// fornecedor de um modelo, na `ocr_read` e na `video_read`) e volta como dados. Enviar continua
	// a ser enviar: o SA-ROC classifica-o pela sensibilidade e o egress continua default-deny com
	// allowlist — a classe não muda nada disso.
	DestinationExternalRead = "external_read"
	// DestinationExternalEffect — tudo o mais que declare egress ou mutação: muda o mundo, ou sai
	// para fora a mudar algo. É o `web_post`.
	DestinationExternalEffect = "external_effect"
	// DestinationUnknown — um eixo IMPLÍCITO, ou uma tool fora do registo. FAIL-CLOSED: não
	// satisfaz regra de fluxo nenhuma, pelo que a tool fica com a regra de antes. Um eixo ausente
	// nunca se lê como benigno — é a invariante do AOS-538, e esta classe herda-a.
	DestinationUnknown = "unknown"
)

// Class devolve a classe de destino desta tool ([DestinationLocalRead] e seguintes). Um eixo
// implícito dá [DestinationUnknown]: a ordem dos casos é a das garantias, e o `unknown` vem antes
// de qualquer leitura dos valores.
func (d ToolDestination) Class() string {
	eg, mu, rv := normalizeAxis(d.Egress), normalizeAxis(d.Mutation), normalizeAxis(d.Reversibility)
	if eg == "" || mu == "" || rv == "" {
		return DestinationUnknown
	}
	switch {
	case d.IsLocalRead():
		return DestinationLocalRead
	case d.IsLocalRunWrite():
		return DestinationRunArtifact
	case eg == "external" && mu == "none" && rv == "reversible":
		return DestinationExternalRead
	default:
		return DestinationExternalEffect
	}
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
			Sensitivity:   normalizeAxis(d.Sensitivity),
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

// LocalRunWrites devolve, ordenados, os nomes das tools que se declaram escrita local do run
// (AOS-549) e cuja capability não denuncia egress — as que o gate deixa gravar no armazém do run com
// o contexto untrusted. É o que o banner de arranque nomeia.
func (t ToolDestinations) LocalRunWrites() []string {
	var out []string
	for name, d := range t.m {
		if d.IsLocalRunWrite() && !derivesEgress(&Call{Capability: d.Capability}) {
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
	// TaintPrivilegeLocalRunWrite — NÃO privilegiada: a tool declara-se escrita local do run
	// ([ToolDestination.IsLocalRunWrite], AOS-549, decisão do dono de 2026-10-22).
	TaintPrivilegeLocalRunWrite = "local_run_write"
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

// LocalRunWrites devolve as tools livres do gate por serem escrita local do run (para o banner).
func (d DestinationPrivileged) LocalRunWrites() []string { return d.dests.LocalRunWrites() }

// ClassifyCall implementa [ToolPrivilegeClassifier].
func (d DestinationPrivileged) ClassifyCall(call *Call) (bool, string) {
	if call == nil || !privilegedIsEffective(d.base) {
		return false, ""
	}
	dest, known := d.dests.forCall(call)
	if known && dest.IsLocalRead() && !derivesEgress(call) {
		return false, TaintPrivilegeLocalRead
	}
	// AOS-549: gravar no armazém do PRÓPRIO run depois de o contexto ficar untrusted não é um efeito
	// privilegiado — não sai do nó, não muda estado de terceiros, e apaga-se com o titular.
	if known && dest.IsLocalRunWrite() && !derivesEgress(call) {
		return false, TaintPrivilegeLocalRunWrite
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

// declaredEgress projecta o egress DECLARADO no eixo do classificador de risco (AOS-539). Implícito
// ou fora do vocabulário ⇒ [risk.EgressUnknown]; quem o consome só o usa para ELEVAR (ver
// [raiseEgress]), pelo que um valor ilegível nunca baixa nada.
func (d ToolDestination) declaredEgress() risk.Egress {
	switch normalizeAxis(d.Egress) {
	case "none":
		return risk.EgressNone
	case "internal":
		return risk.EgressInternal
	case "external":
		return risk.EgressExternal
	default:
		return risk.EgressUnknown
	}
}

// raiseEgress aplica o egress DECLARADO ao derivado da capability e do recurso: só ELEVA, nunca
// baixa. Um derivado já externo (ou indeterminado, que conta como externo) fica; um declarado
// externo ou interno sobe o derivado. Um `none` declarado não desfaz um egress que a capability ou
// o recurso já denunciam — a declaração do operador não é mentível para baixo (SAROC-01).
func raiseEgress(derived, declared risk.Egress) risk.Egress {
	if derived.IsExternal() {
		return derived
	}
	switch declared {
	case risk.EgressExternal:
		return risk.EgressExternal
	case risk.EgressInternal:
		if derived == risk.EgressNone {
			return risk.EgressInternal
		}
	}
	return derived
}

// ResourceSensitivities é o vocabulário FECHADO da sensibilidade do recurso que o registo de tools
// pode declarar (AOS-539): as formas canónicas do eixo do classificador ([risk.Sensitivity.String]).
// Quem lê o registo recusa qualquer outro valor; o vazio é «não declarado» e lê-se sensível.
var ResourceSensitivities = []string{"public", "internal", "sensitive"}

// ResourceSensitivityApplies diz se a sensibilidade do recurso declarada pode valer para esta tool
// (AOS-539): só numa LEITURA — `mutation: "none"` explícito — cuja capability e tipo de recurso
// não denunciam egress (a mesma derivação do classificador de risco). É condição NECESSÁRIA e não
// suficiente: lê a mutação DECLARADA (um `none` mentiroso passa) e não sabe o que o egress
// declarado envia. O registo de tools no nó recusa no arranque uma declaração onde isto dá false e
// as formas que isto não vê (ver `validateResourceSensitivity` em cmd/aos); o classificador ignora-a
// quando dá false.
func (d ToolDestination) ResourceSensitivityApplies(resourceType string) bool {
	if normalizeAxis(d.Mutation) != "none" {
		return false
	}
	return !derivesEgress(&Call{Capability: d.Capability, Resource: Resource{Type: resourceType}})
}

// declaredSensitivity projecta a sensibilidade DECLARADA no eixo do classificador. Só as formas de
// [ResourceSensitivities] contam como declaração; o vazio ou um valor fora do vocabulário dão
// (_, false) e o classificador fica com o de antes — o texto da call, que vazio se lê sensível.
func (d ToolDestination) declaredSensitivity() (risk.Sensitivity, bool) {
	switch normalizeAxis(d.Sensitivity) {
	case "public":
		return risk.SensitivityPublic, true
	case "internal":
		return risk.SensitivityInternal, true
	case "sensitive":
		return risk.SensitivitySensitive, true
	default:
		return risk.SensitivityUnknown, false
	}
}

// Vocabulário FECHADO da proveniência do untrusted de uma call (AOS-556, ADR-044 §2.3), escrito em
// [CallContext.TaintProvenance] pelo Agent Runtime.
//
// `ProvenanceContext` DOMINA: se o piso do turno já era untrusted — media projectada para o modelo,
// um `tool_error`, memória, um steer —, a proveniência é essa, qualquer que seja o que a call
// consuma nos argumentos. É o que preserva a decisão do dono de 2026-10-23 («num run com media a
// fase R2 não levanta nada») SEM a política de fluxo ter de saber o que é media.
const (
	// ProvenanceClean — o piso é trusted e a call não resolveu handle nenhum.
	ProvenanceClean = "clean"
	// ProvenanceHandleArgument — o piso é trusted e a call resolveu ao menos um handle untrusted
	// nos seus argumentos. É a única proveniência que uma regra de fluxo pode autorizar sobre
	// contexto untrusted, e só para os destinos que a política nomear.
	ProvenanceHandleArgument = "handle_argument"
	// ProvenanceContext — o piso já era untrusted, por qualquer razão.
	ProvenanceContext = "context"
)
