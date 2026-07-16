package network

import (
	"context"
	"errors"
	"strconv"
	"time"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
)

// SecurityDecision é o veredicto que um [SecurityEvent] regista no audit WORM.
type SecurityDecision string

const (
	// SecurityBlocked — um egress FORA da allowlist foi BLOQUEADO (o evento de
	// segurança central de AOS-067; liga a AOS-072).
	SecurityBlocked SecurityDecision = "blocked"
	// SecurityAllowed — um egress permitido (registo opcional de observabilidade).
	SecurityAllowed SecurityDecision = "allowed"
)

// SecurityEvent é o registo NÃO-SECRETO de uma decisão de egress a selar no audit
// WORM: atribuível ao PRINCIPAL e ao DESTINO tentado, com a versão da allowlist em
// vigor e a razão. Não transporta segredos (o destino/decisão não são segredos).
type SecurityEvent struct {
	// Principal é a NHI/classe que originou a tentativa de egress (atribuição).
	Principal referencemonitor.Principal
	// Destination é o destino de rede tentado (host/IP + porta).
	Destination Destination
	// Decision é o veredicto (blocked/allowed).
	Decision SecurityDecision
	// Reason é a razão estável (ex.: "destino fora da allowlist (default-deny)").
	Reason string
	// PolicyVersion é a versão tamper-evident da allowlist em vigor na decisão.
	PolicyVersion string
	// RunID/StepID correlacionam com a trajectória (opcionais).
	RunID  string
	StepID string
	// Timestamp é observacional (a ordem no WORM é o AuditSeq).
	Timestamp time.Time
}

// SecurityAuditSink é a porta por onde o [EgressFilter] SELA um evento de segurança
// (egress bloqueado) de forma tamper-evident. A impl de referência
// ([WORMSecuritySink]) escreve na hash-chain WORM de AOS-072; os testes usam um
// fake que também permite verificar a cadeia real.
type SecurityAuditSink interface {
	// Seal sela o evento e devolve erro se a selagem falhar. FAIL-CLOSED no
	// [EgressFilter]: uma decisão que não se consegue selar degrada para deny — um
	// egress não-auditável não é permitido (audit-before-effect, ADR-010).
	Seal(ctx context.Context, ev SecurityEvent) error
}

// ErrNoAttribution — o evento de segurança não tem principal (nem NHI nem classe).
// Fail-closed: um bloqueio de egress anónimo é inaceitável (ADR-010) — o audit tem
// de ser atribuível a QUEM tentou o egress.
var ErrNoAttribution = errors.New("network: evento de egress sem principal (fail-closed)")

// Atributos de audit (obligation) do evento de segurança de egress.
const (
	obligationEgress       = "egress_security"
	capabilityNetEgress    = "net:egress"
	toolIDSandboxNetwork   = "sandbox.network"
	resourceTypeNet        = "net"
	auditPartitionEgressNS = "sbx-egress:"
)

// WORMSecuritySink é a impl de referência de [SecurityAuditSink]: sela cada evento
// de egress na hash-chain WORM tamper-evident de AOS-072 (packages/platform/audit),
// numa partição POR PRINCIPAL (a responsabilização de egress é contígua por
// principal). Zero-dep (o audit é módulo local zero-dep).
type WORMSecuritySink struct {
	store audit.Store
}

// NewWORMSecuritySink constrói o sink sobre um audit [audit.Store] (WORM).
func NewWORMSecuritySink(store audit.Store) *WORMSecuritySink {
	return &WORMSecuritySink{store: store}
}

// Seal implementa [SecurityAuditSink]: mapeia o evento para um [audit.AuditRecord] e
// sela-o na cadeia. Fail-closed: um evento sem principal (não-atribuível) é RECUSADO
// ([ErrNoAttribution]) antes de qualquer escrita.
func (s *WORMSecuritySink) Seal(ctx context.Context, ev SecurityEvent) error {
	principalID := principalID(ev.Principal)
	if principalID == "" {
		return ErrNoAttribution
	}
	ar := audit.AuditRecord{
		Partition:     auditPartitionEgressNS + principalID,
		Timestamp:     ev.Timestamp,
		Decision:      auditDecision(ev.Decision),
		Principal:     audit.Principal{NHIID: principalID, DelegationChain: toAuditHops(ev.Principal.DelegationChain)},
		Capability:    capabilityNetEgress,
		PolicyVersion: ev.PolicyVersion,
		RunID:         ev.RunID,
		StepID:        ev.StepID,
		ToolID:        toolIDSandboxNetwork,
		Resource:      audit.Resource{Type: resourceTypeNet, Value: ev.Destination.String()},
		// O contexto sela o taint untrusted (o resultado da sandbox é sempre untrusted).
		Context: audit.CallContext{Taint: "untrusted"},
		// A obligation sela a razão, a classe e o destino decomposto na cadeia
		// (tamper-evident): o evento é atribuível a partir de UM registo. Sem segredos.
		Obligations: []audit.Obligation{{
			Type: obligationEgress,
			Params: map[string]string{
				"reason":         ev.Reason,
				"decision":       string(ev.Decision),
				"agent_class":    ev.Principal.AgentClass,
				"dest_host":      ev.Destination.Host,
				"dest_ip":        ev.Destination.IP,
				"dest_port":      strconv.Itoa(ev.Destination.Port),
				"policy_version": ev.PolicyVersion,
			},
		}},
	}
	_, err := s.store.Append(ctx, ar)
	return err
}

// principalID devolve o identificador NÃO-SECRETO do principal (a resposta a
// "quem"). Prefere a NHI; cai para a classe de agente.
func principalID(p referencemonitor.Principal) string {
	if p.NHIID != "" {
		return p.NHIID
	}
	if p.AgentClass != "" {
		return "class:" + p.AgentClass
	}
	return ""
}

// auditDecision mapeia o veredicto de segurança para o vocabulário de audit.
func auditDecision(d SecurityDecision) audit.Decision {
	if d == SecurityAllowed {
		return audit.DecisionAllow
	}
	return audit.DecisionDeny
}

// toAuditHops projecta a cadeia de delegação do principal para o modelo do audit.
func toAuditHops(hops []referencemonitor.DelegationHop) []audit.DelegationHop {
	if len(hops) == 0 {
		return nil
	}
	out := make([]audit.DelegationHop, len(hops))
	for i, h := range hops {
		out[i] = audit.DelegationHop{Sub: h.Sub, ActAs: h.ActAs}
	}
	return out
}

// EgressAuditPartition devolve a partição WORM onde os eventos de egress de um
// principal são encadeados (para verificação da cadeia: [audit.Verify]).
func EgressAuditPartition(p referencemonitor.Principal) string {
	id := principalID(p)
	if id == "" {
		return ""
	}
	return auditPartitionEgressNS + id
}
