package referencemonitor

import (
	"context"
	"strings"

	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// networkCapabilityPrefixes são os prefixos de capability que denotam EGRESS de
// rede (a acção fala para fora). Modela o "egress-class" como propriedade da acção,
// DERIVÁVEL da capability SEM importar packages/substrate/sandbox/network (que
// importa este pacote — importá-lo criaria um ciclo).
//
// Estrutura ESPELHA network.IsNetworkCapability (AOS-067): cada prefixo casa a forma
// NUA (cap == prefixo, ex.: "cap:http") E a sub-acção (prefixo+".", ex.:
// "cap:http.post") — ver [isNetworkCapability]. É um SUPERSET DELIBERADO da fonte
// (AOS-067 governa só http/net na filtragem IP; aqui o eixo de risco cobre também
// mail/smtp/msg/webhook, que são egress mas fora do filtro de rede). A divergência é
// intencional e fixada por um teste de coerência local (egress_internal_test.go), já
// que importar a fonte criaria um ciclo de módulo.
var networkCapabilityPrefixes = []string{
	"cap:http",
	"cap:https",
	"cap:net",
	"cap:mail",
	"cap:smtp",
	"cap:msg",
	"cap:webhook",
}

// localCapabilityPrefixes são as capabilities PROVADAMENTE LOCAIS (leitura/computação
// sobre recursos locais, sem falar para fora). É uma ALLOWLIST explícita: só o que
// aqui consta resolve [risk.EgressNone]. Tudo o que não é claramente local nem
// claramente de rede resolve [risk.EgressUnknown] (fail-closed → externo em
// [risk.Classify]), fechando o vector de exfiltração via tool não-catalogada como
// rede (ex.: cap:s3.upload, cap:slack.post, cap:ftp.put) — ver [egressForCall].
var localCapabilityPrefixes = []string{
	"cap:fs.",
	"cap:file.",
	"cap:doc.",
	"cap:mem.",
	"cap:cache.",
	"cap:log.",
	"cap:compute.",
	"cap:math.",
	"cap:time.",
	"cap:clock.",
}

// networkResourceTypes são os tipos de [Resource] que denotam um destino de rede
// (egress externo). Espelha network.DestinationFromResource sem o importar.
var networkResourceTypes = map[string]struct{}{
	"url":  {},
	"http": {},
	"net":  {},
	"host": {},
}

// isNetworkCapability indica se a capability é de EGRESS de rede. Casa a forma nua
// (cap == prefixo) E a sub-acção (prefixo+"."), à imagem de network.IsNetworkCapability
// (AOS-067): "cap:http" nu conta como rede, tal como "cap:http.post". Case-insensitive.
func isNetworkCapability(capability string) bool {
	c := strings.ToLower(strings.TrimSpace(capability))
	if c == "" {
		return false
	}
	for _, p := range networkCapabilityPrefixes {
		if c == p || strings.HasPrefix(c, p+".") {
			return true
		}
	}
	return false
}

// isLocalCapability indica se a capability está na allowlist de capabilities
// provadamente locais (sem egress). Case-insensitive.
func isLocalCapability(capability string) bool {
	c := strings.ToLower(strings.TrimSpace(capability))
	for _, p := range localCapabilityPrefixes {
		if strings.HasPrefix(c, p) {
			return true
		}
	}
	return false
}

// egressForCall deriva o eixo de EGRESS de um call SEM importar o sandbox, com
// DEFAULT FAIL-CLOSED (SAROC-02): uma capability de rede ou um recurso de rede ⇒
// egress EXTERNO; uma capability provadamente local ⇒ egress NENHUM; TUDO O RESTO ⇒
// egress INDETERMINADO ([risk.EgressUnknown]), que [risk.Classify] trata como externo
// (o pior caso). Assim uma tool de egress não-catalogada como rede (cap:s3.upload,
// cap:ftp.put, cap:dns.exfil) NÃO cai no extremo seguro por omissão — não escapa à
// classe danger por não estar numa allowlist de rede.
func egressForCall(call *Call) risk.Egress {
	if isNetworkCapability(call.Capability) {
		return risk.EgressExternal
	}
	if _, ok := networkResourceTypes[strings.ToLower(call.Resource.Type)]; ok {
		return risk.EgressExternal
	}
	if isLocalCapability(call.Capability) {
		return risk.EgressNone
	}
	// Nem claramente local nem claramente de rede ⇒ fail-closed (egress potencial).
	return risk.EgressUnknown
}

// irreversibleActionTokens são os VERBOS de acção inerentemente IRREVERSÍVEIS: uma
// capability cuja acção seja destes (delete/send/transfer/...) NÃO pode ser desfeita,
// independentemente do que o chamador declara. É o PISO de reversibilidade derivado
// da capability (não mentível), à imagem do eixo de egress (SAROC-01).
var irreversibleActionTokens = map[string]struct{}{
	"delete": {}, "del": {}, "remove": {}, "rm": {}, "destroy": {},
	"drop": {}, "purge": {}, "wipe": {}, "erase": {}, "truncate": {},
	"send": {}, "transfer": {}, "transmit": {}, "revoke": {},
	"terminate": {}, "kill": {}, "unlink": {}, "expire": {}, "deactivate": {},
}

// capabilityIrreversible indica se a capability denota uma acção inerentemente
// irreversível (algum segmento da capability é um verbo destrutivo/de envio). É o
// piso: quando true, a reversibilidade é IRREVERSÍVEL por derivação, o texto do
// chamador não a pode baixar.
func capabilityIrreversible(capability string) bool {
	c := strings.ToLower(strings.TrimSpace(capability))
	c = strings.TrimPrefix(c, "cap:")
	segs := strings.FieldsFunc(c, func(r rune) bool {
		return r == '.' || r == ':' || r == '/' || r == '-' || r == '_'
	})
	for _, seg := range segs {
		if _, ok := irreversibleActionTokens[seg]; ok {
			return true
		}
	}
	return false
}

// sensitivityFromText mapeia o [CallContext.Sensitivity] textual no eixo do
// classificador. FAIL-CLOSED: um valor vazio/desconhecido resolve
// [risk.SensitivityUnknown], que o classificador trata como o TOPO (sensível) — a
// sensibilidade indeterminada nunca é tratada como pública.
func sensitivityFromText(s string) risk.Sensitivity {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "public":
		return risk.SensitivityPublic
	case "internal":
		return risk.SensitivityInternal
	case "confidential", "sensitive", "secret", "pii":
		return risk.SensitivitySensitive
	default:
		return risk.SensitivityUnknown
	}
}

// maxSensitivity devolve a MAIS ALTA das duas sensibilidades no reticulado (por
// [risk.Sensitivity.Level], que resolve o desconhecido para o topo). É como o texto
// do chamador só pode ELEVAR o piso derivado, nunca baixá-lo.
func maxSensitivity(a, b risk.Sensitivity) risk.Sensitivity {
	if b.Level() > a.Level() {
		return b
	}
	return a
}

// sensitivityForCall resolve a sensibilidade EFECTIVA aplicando um PISO derivado do
// egress (SAROC-01): uma acção com egress externo classifica-se com sensibilidade >=
// interna. O texto auto-declarado do chamador só pode ELEVAR este piso (via
// [maxSensitivity]), nunca baixá-lo — um cap de egress declarado "public" não
// consegue rebaixar o eixo abaixo do mínimo derivado da acção.
func sensitivityForCall(call *Call, egress risk.Egress) risk.Sensitivity {
	declared := sensitivityFromText(call.Context.Sensitivity)
	if egress.IsExternal() {
		declared = maxSensitivity(declared, risk.SensitivityInternal)
	}
	return declared
}

// reversibilityForCall resolve a reversibilidade EFECTIVA com um PISO derivado da
// capability (SAROC-01): uma capability inerentemente irreversível (delete/send/
// transfer/...) é SEMPRE irreversível, independentemente do texto do chamador. O
// texto só pode ELEVAR o risco (reversível → irreversível), nunca o BAIXAR — assim
// um cap:fs.delete declarado "reversible" não escapa à classe danger. FAIL-CLOSED no
// texto: só "reversible" resolve reversível; vazio/desconhecido resolve irreversível.
func reversibilityForCall(call *Call) risk.Reversibility {
	if capabilityIrreversible(call.Capability) {
		return risk.Irreversible
	}
	if strings.EqualFold(strings.TrimSpace(call.Context.Reversibility), "reversible") {
		return risk.Reversible
	}
	return risk.ReversibilityUnknown
}

// buildPreview constrói o PREVIEW do efeito CONCRETO RESOLVIDO da acção (ADR-013):
// capability + recurso resolvido (tipo/valor/região), NÃO um genérico. É o que o
// utilizador vê antes de aprovar uma acção danger. Sem segredos (o Input da tool
// nunca entra).
func buildPreview(call *Call) string {
	var b strings.Builder
	b.WriteString(call.Capability)
	if call.Resource.Value != "" {
		b.WriteString(" -> ")
		if call.Resource.Type != "" {
			b.WriteString(call.Resource.Type)
			b.WriteString(":")
		}
		b.WriteString(call.Resource.Value)
		if call.Resource.Region != "" {
			b.WriteString(" [")
			b.WriteString(call.Resource.Region)
			b.WriteString("]")
		}
	}
	return b.String()
}

// batchKeyForCall deriva a chave de LOTE gray de um call. Agrupa apenas acções
// EQUIVALENTES — mesmo run + mesma capability + mesmo destino resolvido (tipo/valor/
// região) — para que UMA confirmação de lote cubra só acções materialmente iguais e
// qualquer acção gray diferente RE-SOLICITE confirmação (SAROC-03). Um run vazio ⇒
// chave vazia (cada acção gray é a sua própria confirmação, nunca partilha decisão).
func batchKeyForCall(call *Call) string {
	if call.RunID == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(call.RunID)
	b.WriteString("|")
	b.WriteString(call.Capability)
	b.WriteString("|")
	b.WriteString(call.Resource.Type)
	b.WriteString(":")
	b.WriteString(call.Resource.Value)
	b.WriteString("|")
	b.WriteString(call.Resource.Region)
	return b.String()
}

// batchSummaryForCall descreve o ÂMBITO que uma confirmação de lote gray cobre — a
// classe de equivalência (capability + destino) dentro do run — para que o humano
// aprove um lote com âmbito EXPLÍCITO, não um "run inteiro" opaco (SAROC-03).
func batchSummaryForCall(call *Call) string {
	return "lote gray (run " + call.RunID + "): " + buildPreview(call)
}

// principalSubject devolve um identificador atribuível do principal para o audit
// (a NHI, ou o agente). Sem segredos.
func principalSubject(p Principal) string {
	if p.NHIID != "" {
		return p.NHIID
	}
	return p.AgentID
}

// RiskGate é o hook de enforcement do GATE DE RISCO SA-ROC (AOS-074, ADR-013): ao
// mediar uma tool call, CLASSIFICA a acção num eixo composto — sensibilidade dos
// dados + egress + reversibilidade — e aplica a FRICÇÃO PROPORCIONAL. Muda o eixo
// do gate de "destrutivo" para o risco REAL de exfiltração (CamoLeak): uma leitura
// de dados sensíveis seguida de egress externo é DANGER mesmo sem operação
// destrutiva.
//
// PROPRIEDADES IMPOSTAS:
//   - Classe de risco por três eixos: cada acção recebe uma [risk.Class] derivada
//     de sensibilidade (do [CallContext], ELEVADA pelo taint untrusted de AOS-069),
//     egress (derivado da capability/recurso, sem importar o sandbox) e
//     reversibilidade (do [CallContext]). A política é policy-as-code VERSIONADA
//     (digest), selada no audit via [HookResult.PolicyVersion].
//   - SA-ROC: SAFE corre sem gate (HookAllow imediato); GRAY agrupa numa
//     confirmação de LOTE; DANGER/IRREVERSÍVEL escala para confirmação INDIVIDUAL
//     com PREVIEW concreto resolvido. A ausência de aprovação numa acção
//     irreversível dentro do timeout NEGA (fail-closed).
//   - Anti-fatigue sem bypass: auto-aprovação por classe/maturidade (safe/gray),
//     MAS danger/irreversível NUNCA sem confirmação (prova estrutural em
//     [risk.AutoApprovePolicy.Allows]).
//   - Override-rate medido e exposto ([Gate.Metrics]) — anti rubber-stamping.
//   - Classe + decisão SELADAS no audit: o gate ANOTA [CallContext.RiskClass] ANTES
//     de qualquer negação, pelo que o evento de mediação (permit OU deny) regista a
//     classe; a decisão do gate resolve em HookAllow/HookDeny atribuível
//     ([Decision.DeniedBy] == "risk").
//
// A confirmação HITL é feita via a porta [risk.ConfirmationChannel] (o EPIC-09 liga
// o HITL real por trás; a impl de referência é síncrona). O gate resolve a
// escalada em allow/deny — o timeout garante o fail-closed independentemente da
// latência do canal.
type RiskGate struct {
	policy *risk.Policy
	gate   *risk.Gate
}

// NewRiskGate constrói o hook com a política de classificação e o gate SA-ROC
// (que carrega o canal HITL, a auto-aprovação, a maturidade e o timeout). Uma
// policy nil usa [risk.DefaultPolicy]; um gate nil torna o hook FAIL-CLOSED (sem
// gate configurado, toda a acção não-safe é negada — nunca um no-op permissivo).
func NewRiskGate(policy *risk.Policy, gate *risk.Gate) RiskGate {
	if policy == nil {
		policy = risk.DefaultPolicy()
	}
	return RiskGate{policy: policy, gate: gate}
}

// Name implementa [Hook]. É o valor gravado em [Decision.DeniedBy] quando o gate
// nega, tornando a negação por risco distinguível na auditoria.
func (RiskGate) Name() string { return "risk" }

// Evaluate implementa [Hook]. Classifica a acção, sela a classe no contexto (para o
// audit) e aplica o SA-ROC.
func (g RiskGate) Evaluate(ctx context.Context, call *Call) (HookResult, error) {
	egress := egressForCall(call)
	action := risk.Action{
		Sensitivity:   sensitivityForCall(call, egress),
		Egress:        egress,
		Reversibility: reversibilityForCall(call),
		Taint:         taint.ParseLabel(call.Context.Taint),
	}
	classification := risk.Classify(g.policy, action)

	// SELAGEM (audit): a classe de risco é anotada no contexto ANTES de qualquer
	// decisão, para que o evento de mediação — permit OU deny — registe a
	// classificação em vigor (ADR-013). A política versionada segue em PolicyVersion.
	call.Context.RiskClass = classification.Class.String()

	// SAROC-04: uma acção DANGER com EGRESS mas SEM destino concreto resolvido
	// (Resource.Value vazio) não pode ser apresentada para aprovação com um preview
	// GENÉRICO (só a capability) — o utilizador aprovaria egress sem ver o destino. É
	// negada fail-closed: exige-se o destino resolvido para poder confirmar egress danger.
	if classification.Class == risk.ClassDanger && egress.IsExternal() && call.Resource.Value == "" {
		call.Context.RiskDecisionMode = "denied"
		return HookResult{
			Decision:      HookDeny,
			Reason:        "risco danger com egress sem destino concreto resolvido: negado (fail-closed)",
			PolicyVersion: classification.PolicyVersion,
		}, nil
	}

	// Gate ausente ⇒ fail-closed: sem SA-ROC configurado, só o que é seguro corre;
	// tudo o resto é negado (nunca um no-op permissivo que anularia a governação).
	if g.gate == nil {
		if classification.Class == risk.ClassSafe {
			return HookResult{Decision: HookAllow, PolicyVersion: classification.PolicyVersion}, nil
		}
		call.Context.RiskDecisionMode = "denied"
		return HookResult{
			Decision:      HookDeny,
			Reason:        "gate de risco nao configurado: accao nao-safe negada (fail-closed)",
			PolicyVersion: classification.PolicyVersion,
		}, nil
	}

	res := g.gate.Evaluate(ctx, risk.Request{
		Classification: classification,
		// Lote gray por acção EQUIVALENTE (run + capability + destino), não pelo run
		// inteiro: uma aprovação de lote não dá boleia a acções gray diferentes (SAROC-03).
		BatchKey:     batchKeyForCall(call),
		BatchSummary: batchSummaryForCall(call),
		Preview:      buildPreview(call),
		Principal:    principalSubject(call.Principal),
		Capability:   call.Capability,
		Resource:     call.Resource.Value,
	})

	// SELAGEM (audit): quem autorizou e por que via (auto/batch/human/timeout/denied),
	// para que cada permit/deny gray/danger seja atribuível no log tamper-evident (SAROC-05).
	call.Context.RiskApprover = res.Approver
	call.Context.RiskDecisionMode = res.DecisionMode()

	if res.Outcome == risk.OutcomeAllow {
		return HookResult{Decision: HookAllow, PolicyVersion: classification.PolicyVersion}, nil
	}
	return HookResult{
		Decision:      HookDeny,
		Reason:        res.Reason,
		PolicyVersion: classification.PolicyVersion,
	}, nil
}

// DefaultHooksWithRisk devolve a cadeia canónica [DefaultHooks] com o [RiskGate]
// inserido LOGO APÓS a política (identity → policy → risk → budget → egress →
// audit): a classe de risco é avaliada antes de reservar orçamento ou egress, para
// uma acção que exige confirmação nunca consumir recursos antes de aprovada.
// Composição OPT-IN (à imagem de [DefaultHooksWithTaint]/[DefaultHooksWithScope]):
// [DefaultHooks] NÃO inclui o RiskGate — ligar esta composição, com um
// [risk.ConfirmationChannel] real (o HITL do EPIC-09), é responsabilidade do
// composition root ápice (packages/integration).
func DefaultHooksWithRisk(policy *risk.Policy, gate *risk.Gate) []Hook {
	base := DefaultHooks() // identity, policy, budget, egress, audit
	riskGate := NewRiskGate(policy, gate)
	out := make([]Hook, 0, len(base)+1)
	for _, h := range base {
		out = append(out, h)
		if h.Name() == "policy" {
			out = append(out, riskGate)
		}
	}
	return out
}

// RiskClassifier ANOTA a classe de risco SA-ROC no contexto da call e NUNCA DECIDE.
//
// Existe porque o oráculo de autonomia (AOS-087) vive DENTRO da política, e a política corre
// ANTES de qualquer hook de risco na cadeia canónica. O overlay lê `Context.RiskClass`; se
// ninguém a tiver escrito até lá, `riskClassFromString("")` resolve para danger (fail-closed) e a
// taxonomia L0–L5 colapsa: a L3 comporta-se como a L1 e a L4 escala tudo.
//
// PORQUE NÃO SE RESOLVE PONDO O [RiskGate] ANTES DA POLÍTICA — duas razões, ambas fatais:
//
//   - A cadeia faz CURTO-CIRCUITO no primeiro não-permit. Com o RiskGate à frente, uma acção que
//     a política NEGA passaria a ser ESCALADA por risco — um deny transformado em pergunta a um
//     humano, e a existência de uma acção proibida revelada a quem a aprova.
//   - Sem um [risk.Gate] composto (o caso deste nó), o RiskGate NEGA tudo o que não seja `safe`.
//     E nada é `safe` enquanto a sensibilidade não for declarada. Acrescentá-lo pararia o nó por
//     inteiro, fail-closed, com a aparência de "ligar a classificação de risco".
//
// Este hook faz SÓ os dois primeiros passos do RiskGate — classificar e anotar — e devolve
// sempre [HookAllow]. Não pode negar, não pode escalar, não pode curto-circuitar: o pior que
// pode acontecer é a anotação estar presente. A imposição (SAROC-04, gate ausente, canal de
// confirmação) continua onde estava, com a precedência de sempre.
//
// A classificação é PURA e determinística, pelo que o RiskGate a recalcular a jusante obtém o
// mesmo valor — não há estado partilhado além da anotação, que é reescrita com o mesmo conteúdo.
type RiskClassifier struct{ policy *risk.Policy }

// NewRiskClassifier constrói o anotador. `policy` nil ⇒ [risk.DefaultPolicy] (o mesmo default de
// [risk.Classify]), para que a anotação exista mesmo sem política versionada composta.
func NewRiskClassifier(policy *risk.Policy) RiskClassifier { return RiskClassifier{policy: policy} }

// Name implementa [Hook].
func (RiskClassifier) Name() string { return "risk-classify" }

// Evaluate implementa [Hook]: classifica, anota, e PERMITE — sempre.
func (c RiskClassifier) Evaluate(_ context.Context, call *Call) (HookResult, error) {
	egress := egressForCall(call)
	classification := risk.Classify(c.policy, risk.Action{
		Sensitivity:   sensitivityForCall(call, egress),
		Egress:        egress,
		Reversibility: reversibilityForCall(call),
		Taint:         taint.ParseLabel(call.Context.Taint),
	})
	call.Context.RiskClass = classification.Class.String()
	return HookResult{Decision: HookAllow, PolicyVersion: classification.PolicyVersion}, nil
}

// CapabilityIrreversible expõe, como predicado PÚBLICO e puro, se uma capability é por si só
// IRREVERSÍVEL — o mesmo juízo que [reversibilityForCall] faz na mediação, e o único caminho que
// produz [risk.Irreversible] (o rótulo declarado no contexto só consegue baixar de `unknown` para
// `reversible`; nunca desfaz este).
//
// PORQUE É EXPORTADO. O plano de controlo precisa de decidir, na cerimónia de aprovação, se a
// acção EXIGE dois aprovadores — e até 2026-08-22 aceitava essa decisão do CORPO DO PEDIDO
// (`dual_control_required`), sem consultar coisa nenhuma. Reimplementar o juízo do lado do nó
// criaria uma SEGUNDA fonte de verdade que divergiria em silêncio na primeira capability nova.
//
// A classificação é pura e determinística — recalculá-la a jusante dá o MESMO valor, como a nota
// de [RiskClassifier] já declara. É por isso que este predicado pode ser consultado depois, sem
// arrastar a classificação por cinco camadas até à rota.
func CapabilityIrreversible(capability string) bool { return capabilityIrreversible(capability) }
