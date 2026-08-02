package planvalidate

import (
	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// REGRA 6 — RISCO DERIVADO (AOS-232, tecnica/18 §3.3).
//
// O risco de um nó é DERIVADO das FERRAMENTAS PINADAS que ele resolve — nunca
// lido do rótulo `risk_class` do documento (que é uma PROPOSTA untrusted do LLM,
// ADR-005). A derivação combina os eixos de risco das tools resolvidas no PIOR
// caso por eixo (sensibilidade máxima, egress mais externo, irreversível se
// alguma tool o for) e classifica-os com [risk.Classify] (policy-as-code do
// ADR-013). Um efeito irreversível ou egress externo de dados sensíveis ⇒
// `danger` (nunca auto-aprovável — o approval-card por efeito concreto é do gate
// AOS-236).
//
// O rótulo do LLM só é aceite se ELEVAR o piso derivado (ver [elevateOnly]): um
// downgrade — declarar `safe` num nó cujas tools derivam `danger` — é IGNORADO,
// e o risco RESOLVIDO fica no piso. NÃO há caminho em que o rótulo do modelo
// BAIXE o risco (DoD). Puro e determinístico.

// egressRank ordena o eixo de egress (None < Internal < External) para a
// combinação por pior-caso. FAIL-CLOSED: qualquer valor que não seja
// explicitamente None/Internal (incluindo [risk.EgressUnknown]) conta como
// externo (rank 2), o pior caso de exfiltração.
func egressRank(e risk.Egress) int {
	switch e {
	case risk.EgressNone:
		return 0
	case risk.EgressInternal:
		return 1
	default:
		return 2 // External ou Unknown (fail-closed)
	}
}

// canonicalEgress normaliza um rank de egress de volta ao valor canónico, para
// alimentar [risk.Classify] com um eixo determinístico (Unknown colapsa em
// External, o seu equivalente fail-closed).
func canonicalEgress(rank int) risk.Egress {
	switch rank {
	case 0:
		return risk.EgressNone
	case 1:
		return risk.EgressInternal
	default:
		return risk.EgressExternal
	}
}

// canonicalSensitivity normaliza um nível de sensibilidade (0=público … 2=sensível)
// de volta ao valor canónico. [risk.Sensitivity.Level] já resolve o valor-zero
// desconhecido para o topo (2), pelo que a combinação é fail-closed.
func canonicalSensitivity(level int) risk.Sensitivity {
	switch level {
	case 0:
		return risk.SensitivityPublic
	case 1:
		return risk.SensitivityInternal
	default:
		return risk.SensitivitySensitive
	}
}

// deriveNodeAction combina os eixos de risco das capabilities RESOLVIDAS de um nó
// numa única [risk.Action] por PIOR-CASO em cada eixo. Baseline mínima (público,
// sem egress, reversível): um nó SEM tools não tem efeito nem egress ⇒ deriva
// `safe`. Cada tool só pode ELEVAR os eixos — os valores-zero fail-closed de uma
// capability por classificar dominam (uma tool não-classificada empurra o nó para
// `danger`).
//
// O taint é [taint.Trusted]: a derivação reflecte a propriedade INERENTE (pinada,
// trusted) das ferramentas, não a untrustedness do documento — essa é tratada por
// [elevateOnly] (o rótulo do LLM nunca baixa o piso). Puro.
func deriveNodeAction(caps []Capability) risk.Action {
	sensLevel := risk.SensitivityPublic.Level()
	egRank := egressRank(risk.EgressNone)
	irreversible := false
	for _, c := range caps {
		if l := c.Sensitivity.Level(); l > sensLevel {
			sensLevel = l
		}
		if r := egressRank(c.Egress); r > egRank {
			egRank = r
		}
		if c.Reversibility.IsIrreversible() {
			irreversible = true
		}
	}
	rev := risk.Reversible
	if irreversible {
		rev = risk.Irreversible
	}
	return risk.Action{
		Sensitivity:   canonicalSensitivity(sensLevel),
		Egress:        canonicalEgress(egRank),
		Reversibility: rev,
		Taint:         taint.Trusted,
	}
}

// classToPlanRisk mapeia a [risk.Class] do classificador SA-ROC para o enum
// [plan.RiskClass] do documento. FAIL-CLOSED: o valor-zero de [risk.Class] é
// [risk.ClassDanger], pelo que qualquer classe não-safe/não-gray resolve `danger`.
func classToPlanRisk(c risk.Class) plan.RiskClass {
	switch c {
	case risk.ClassSafe:
		return plan.RiskSafe
	case risk.ClassGray:
		return plan.RiskGray
	default:
		return plan.RiskDanger
	}
}

// riskRank é a ordem SEMÂNTICA do enum de risco do documento (unset < safe < gray
// < danger). É DISTINTA da ordem numérica de [risk.Class] (cujo zero é danger):
// serve o «só eleva» de [elevateOnly]. [plan.RiskUnset] (rótulo ausente) fica no
// fundo — um advisory omitido nunca eleva nada.
func riskRank(r plan.RiskClass) int {
	switch r {
	case plan.RiskSafe:
		return 1
	case plan.RiskGray:
		return 2
	case plan.RiskDanger:
		return 3
	default:
		return 0 // RiskUnset / ausente — o fundo do reticulado
	}
}

// elevateOnly aplica a regra ADVISORY (§3.3, regra 6): o rótulo `declared` do LLM
// só é aceite se for >= ao piso `floor` DERIVADO das tools. Um downgrade (declared
// abaixo do piso) é IGNORADO — devolve o piso. Assim o rótulo do modelo só pode
// SUBIR o risco, NUNCA baixá-lo (DoD). Puro.
func elevateOnly(floor, declared plan.RiskClass) plan.RiskClass {
	if riskRank(declared) > riskRank(floor) {
		return declared
	}
	return floor
}

// NodeRisk é o risco RESOLVIDO de um nó — o artefacto que o gate (AOS-236)
// consome. Carrega o piso DERIVADO das tools pinadas, o rótulo DECLARADO pelo LLM
// (advisory) e o RESOLVIDO (o piso, elevado pelo rótulo apenas se este for
// superior). Inclui a [risk.Classification] completa (eixos efectivos + versão da
// política) para o audit e o approval-card do gate.
type NodeRisk struct {
	NodeID         string
	Derived        plan.RiskClass
	Declared       plan.RiskClass
	Resolved       plan.RiskClass
	Classification risk.Classification
}

// AutoApprovable indica se o risco resolvido pode dispensar o approval-card do
// gate. FAIL-CLOSED: `danger` NUNCA é auto-aprovável (efeito irreversível ou
// egress externo sensível), independentemente do que o LLM tenha declarado. O
// gate AOS-236 é a autoridade final; este é o sinal que ele consome.
func (r NodeRisk) AutoApprovable() bool { return r.Resolved != plan.RiskDanger }

// resolveNodeRisk deriva o risco de UM nó a partir das suas capabilities
// resolvidas e aplica o «só eleva» sobre o rótulo declarado. Puro e determinístico
// (função apenas dos eixos pinados e do rótulo). `pol` nil cai na [risk.DefaultPolicy].
func resolveNodeRisk(node plan.Node, caps []Capability, pol *risk.Policy) NodeRisk {
	cls := risk.Classify(pol, deriveNodeAction(caps))
	derived := classToPlanRisk(cls.Class)
	declared := node.RiskClass
	return NodeRisk{
		NodeID:         node.NodeID,
		Derived:        derived,
		Declared:       declared,
		Resolved:       elevateOnly(derived, declared),
		Classification: cls,
	}
}
