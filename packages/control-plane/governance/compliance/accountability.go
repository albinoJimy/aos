package compliance

import (
	"strings"

	"github.com/aos-ref/platform/audit"
)

// HumanRootPrefix é o prefixo por convenção que identifica a RAIZ HUMANA da cadeia
// de delegação (ADR-003). Espelha o valor FIXADO no Reference Monitor
// (referencemonitor.humanRootPrefix = "human:") e em delegation.IsHuman — a costura
// é o VALOR partilhado, não um import (evita acoplar o control-plane ao kernel só
// para esta constante). Uma cadeia cuja raiz não tem este prefixo (com id não-vazio)
// não é atribuível a um humano ⇒ acção anónima.
const HumanRootPrefix = "human:"

// AnonymousAction identifica um registo de ACÇÃO cujo principal NÃO é completo até
// um humano (cadeia vazia, raiz não-humana, elo órfão ou cadeia descontínua). São
// SÓ identificadores de responsabilização — Partition/AuditSeq/Capability/ToolID —
// NUNCA PII nem payload. É o output fail-closed do [AccountabilityVerifier] (AC1).
type AnonymousAction struct {
	// Partition e AuditSeq localizam o registo na hash-chain (forense).
	Partition string
	AuditSeq  uint64
	// Capability e ToolID descrevem a acção não-atribuível (sem PII).
	Capability string
	ToolID     string
	// Reason descreve por que o principal falhou a completude.
	Reason string
}

// AccountabilityVerifier verifica a COMPLETUDE do principal (AC1): sobre um conjunto
// de [audit.AuditRecord], confirma que cada registo de ACÇÃO tem um principal
// rastreável até um humano. Devolve as acções ANÓNIMAS (fail-closed: se houver
// alguma, o sistema sinaliza-a). É determinista e stateless.
type AccountabilityVerifier struct {
	humanPrefix string
}

// VerifierOption configura o [AccountabilityVerifier].
type VerifierOption func(*AccountabilityVerifier)

// WithHumanRootPrefix sobrepõe o prefixo da raiz humana (default [HumanRootPrefix]).
// Exposto para composição/teste; produção usa o default fixado.
func WithHumanRootPrefix(prefix string) VerifierOption {
	return func(v *AccountabilityVerifier) { v.humanPrefix = prefix }
}

// NewAccountabilityVerifier constrói o verificador com o prefixo de raiz humana por
// omissão ([HumanRootPrefix]).
func NewAccountabilityVerifier(opts ...VerifierOption) *AccountabilityVerifier {
	v := &AccountabilityVerifier{humanPrefix: HumanRootPrefix}
	for _, o := range opts {
		o(v)
	}
	if v.humanPrefix == "" {
		v.humanPrefix = HumanRootPrefix
	}
	return v
}

// Verify percorre os registos e devolve as ACÇÕES ANÓNIMAS — os registos
// classificados como acção (mediação de tool call, ver [isAgentAction]) cujo principal
// NÃO é completo até um humano. Registos de eventos administrativos/governação
// (policy.changed, retention, DSAR, HITL) NÃO são acções de agente e são ignorados
// pela verificação de completude (têm outro modelo de principal — autor/aprovador).
//
// A discriminação acção↔governação é PRODUCER-BOUND (a ToolID, atribuída pelo
// mediador), NUNCA derivada dos campos que a própria tool call controla
// (Resource.Type/Capability/Obligation.Type). Isto fecha o branqueamento de AC1: uma
// acção anónima que exiba um rótulo de governação reservado continua a ser uma acção e
// É sinalizada (ver [isAgentAction]).
//
// Devolve slice vazio (não nil) quando não há anonimato — a condição normal de um
// sistema conforme. Uma slice não-vazia é a prova fail-closed de execuções anónimas
// (AC1): o [GenerateReport] devolve-a em [ComplianceReport.Anomalies] e sinaliza
// [ErrAnonymousAction].
func (v *AccountabilityVerifier) Verify(records []audit.AuditRecord) []AnonymousAction {
	out := make([]AnonymousAction, 0)
	for _, rec := range records {
		if classify(rec) != EventAction {
			continue // eventos de governação/administração não são acções de agente
		}
		if ok, reason := v.principalComplete(rec.Principal); !ok {
			out = append(out, AnonymousAction{
				Partition:  rec.Partition,
				AuditSeq:   rec.AuditSeq,
				Capability: rec.Capability,
				ToolID:     rec.ToolID,
				Reason:     reason,
			})
		}
	}
	return out
}

// PrincipalComplete indica se um principal é rastreável até um humano responsável:
// cadeia não-vazia, raiz humana, cada elo delega efectivamente (ActAs não-vazio) e a
// cadeia é CONTÍNUA (chain[i].ActAs == chain[i+1].Sub). Espelha exactamente a
// disciplina de referencemonitor.chainSubjects (AOS-071): a completude verificada no
// audit é a MESMA que o kernel impõe no enforcement. Devolve o motivo da falha
// (string estável, sem PII) para forense.
func (v *AccountabilityVerifier) principalComplete(p audit.Principal) (bool, string) {
	chain := p.DelegationChain
	if len(chain) == 0 {
		return false, "cadeia de delegacao vazia (sem raiz humana atribuivel)"
	}
	if !v.isHumanRoot(chain[0].Sub) {
		return false, "raiz da cadeia nao e um humano responsavel (sem prefixo human:)"
	}
	for i, hop := range chain {
		if hop.ActAs == "" {
			return false, "elo da cadeia nao delega (ActAs vazio): cadeia mal-formada"
		}
		if i+1 < len(chain) && hop.ActAs != chain[i+1].Sub {
			return false, "cadeia de delegacao descontinua (elo orfao): possivel adulteracao"
		}
	}
	return true, ""
}

// isHumanRoot indica se sub é um humano responsável: o prefixo configurado seguido
// de um identificador NÃO-vazio. Mirror de referencemonitor.isHumanRoot.
func (v *AccountabilityVerifier) isHumanRoot(sub string) bool {
	return len(sub) > len(v.humanPrefix) && strings.HasPrefix(sub, v.humanPrefix)
}
