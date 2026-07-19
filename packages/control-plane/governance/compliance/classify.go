package compliance

import "github.com/aos-ref/platform/audit"

// EventClass é a classe de um [audit.AuditRecord] para efeitos de projecção. NÃO é
// um campo novo no audit — é DERIVADA query-time dos rótulos estáveis que cada
// ticket já sela (Resource.Type / Capability / Obligation.Type / ToolID). O relatório
// projecta cada classe na sua secção sem duplicar dados.
type EventClass int

const (
	// EventAction é uma mediação de tool call (a ACÇÃO de um agente) — o caso por
	// omissão: qualquer registo que NÃO seja um evento de governação rotulado. É a
	// classe sujeita à verificação de completude do principal (AC1) e à contagem de
	// decisões PDP (AC3).
	EventAction EventClass = iota
	// EventHITL é uma decisão do gate HITL (AOS-095).
	EventHITL
	// EventDSAR é um evento do fluxo DSAR/crypto-shredding (AOS-093).
	EventDSAR
	// EventPolicyChange é um changelog de política (AOS-088).
	EventPolicyChange
	// EventRetention é um evento de retenção/TTL (AOS-092).
	EventRetention
)

// Rótulos estáveis dos eventos dos vários tickets, replicados aqui como o
// VOCABULÁRIO de projecção (não se importam os módulos GOV/PDP produtores — isso
// acoplaria o relatório a todo o control-plane e arriscaria ciclos; os rótulos são a
// costura estável, documentada em cada produtor). Cross-ref:
//   - policy.changed                     — control-plane/pdp (AOS-088)
//   - retention.expired/config.changed   — platform/audit (AOS-092)
//   - dsar.*                             — governance/dsar (AOS-093)
//   - hitl_*                             — governance/hitl (AOS-095)
//   - region                             — control-plane/pdp (AOS-094)
//
// FRONTEIRA DE CONFIANÇA (AC1): SÓ [toolDSAR]/[toolHITL] são discriminadores de
// governação — são a ToolID PRODUCER-BOUND (atribuída pelo mediador/produtor, não
// pelo agente). Os rótulos derivados do CONTEÚDO da própria tool call
// (Resource.Type/Capability/Obligation.Type) são action-controlled e NÃO isentam um
// registo da completude: usam-se apenas para ROTULAR a secção de projecção de
// eventos de governação genuínos (que carregam a ToolField do produtor), nunca para
// decidir se um registo é uma acção sujeita à raiz humana.
const (
	labelPolicyChanged   = "policy.changed"
	labelRetentionExpire = "retention.expired"
	labelRetentionConfig = "retention.config.changed"

	labelDSARSubjectType = "dsar.subject"
	labelDSARStores      = "dsar.stores"
	labelDSARReceived    = "dsar.received"
	labelDSARDestroyed   = "dsar.key_destroyed"
	labelDSARBlocked     = "dsar.blocked"
	toolDSAR             = "gov.dsar"

	obHITLDecision      = "hitl_decision"
	obHITLSignature     = "hitl_signature"
	obHITLUnauthed      = "hitl_unauthenticated"
	toolHITL            = "governance.hitl"
	paramHITLApprover   = "approver"
	paramHITLReason     = "reason"
	paramHITLRequestID  = "request_id"
	paramHITLClass      = "class"
	paramHITLAuthed     = "authenticated"
	paramHITLClaimedApp = "claimed_approver"

	obRegion    = "region"
	paramRegion = "region"
)

// isAgentAction indica se rec é uma ACÇÃO de agente — uma mediação de tool call — e é
// por isso sujeito à verificação de completude do principal (AC1), à contagem PDP e à
// atribuição. O discriminador é PRODUCER-BOUND e NÃO pode ser branqueado pelos rótulos
// action-controlled do próprio registo (Resource.Type/Capability/Obligation.Type):
//
//   - ToolID de produtor de governação ([toolDSAR]/[toolHITL]): é um evento
//     administrativo (modelo autor/aprovador, sem cadeia de delegação) — EXCEPTO se
//     ainda assim carregar uma cadeia de delegação, i.e. a FORMA de uma acção mediada;
//     nesse caso É uma acção a verificar (defesa-em-profundidade: um rótulo de
//     governação nunca isenta uma acção com cadeia de delegação da completude).
//   - ToolID vazia: evento administrativo/de sistema (policy.changed/retention) — não
//     há tool executada, logo não há execução a atribuir.
//   - Qualquer outra ToolID: transporta uma tool call de agente (uma tool real) e É uma
//     acção, INDEPENDENTEMENTE de qualquer rótulo de governação que também exiba.
//
// É esta a correcção de AC1: a exempção da completude deriva SÓ da ToolID
// producer-bound, nunca dos campos que a própria tool call controla.
func isAgentAction(rec audit.AuditRecord) bool {
	switch rec.ToolID {
	case toolDSAR, toolHITL:
		return len(rec.Principal.DelegationChain) > 0
	case "":
		return false
	default:
		return true
	}
}

// classify deriva a [EventClass] de um registo. A ACÇÃO tem prioridade absoluta
// ([isAgentAction]) — um registo que transporta uma tool call de agente é sempre
// EventAction, mesmo que exiba um rótulo de governação (não se deixa branquear). Só
// depois se ROTULA um evento de governação genuíno, reconhecido pela ToolID
// producer-bound ([toolHITL]/[toolDSAR]) ou, para eventos sem tool (policy/retention),
// pelo Resource.Type do produtor administrativo.
func classify(rec audit.AuditRecord) EventClass {
	if isAgentAction(rec) {
		return EventAction
	}
	switch {
	case rec.ToolID == toolHITL:
		return EventHITL
	case rec.ToolID == toolDSAR:
		return EventDSAR
	case rec.Resource.Type == labelPolicyChanged:
		return EventPolicyChange
	case rec.Resource.Type == labelRetentionExpire, rec.Resource.Type == labelRetentionConfig:
		return EventRetention
	default:
		return EventAction
	}
}

// hasObligation indica se o registo carrega uma obrigação do tipo dado.
func hasObligation(rec audit.AuditRecord, obType string) bool {
	for _, ob := range rec.Obligations {
		if ob.Type == obType {
			return true
		}
	}
	return false
}

// obligationOf devolve a primeira obrigação do tipo dado (e ok).
func obligationOf(rec audit.AuditRecord, obType string) (audit.Obligation, bool) {
	for _, ob := range rec.Obligations {
		if ob.Type == obType {
			return ob, true
		}
	}
	return audit.Obligation{}, false
}
