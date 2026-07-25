package rm

import "context"

// HookDecision é o veredicto parcial de um hook da cadeia de mediação.
type HookDecision int

const (
	// HookAllow — o hook não se opõe; a cadeia prossegue.
	HookAllow HookDecision = iota
	// HookDeny — o hook nega; a mediação termina em Deny (fail-closed).
	HookDeny
	// HookEscalate — o hook requere gate humano; a mediação termina em Escalate.
	HookEscalate
)

// HookResult é o retorno de um hook. Obligations acumulam-se ao longo da cadeia
// e são impostas apenas se a decisão final for Permit.
type HookResult struct {
	Decision    HookDecision
	Reason      string
	Obligations []Obligation
	// PolicyVersion é a versão (SemVer) da política que produziu este veredicto.
	// Só o hook de política (PDP, AOS-004) a preenche; o RM propaga-a ao evento
	// de mediação para que cada decisão de audit registe a política em vigor
	// (contrato C1, tecnica/12 §4 — campo policy_version). Vazia nos stubs.
	PolicyVersion string
}

// Hook é um ponto de decisão plugável da cadeia de mediação. A cadeia é
// invocada pela ordem em que é fornecida; a ordem canónica de mediação —
// identity → policy → budget → egress → audit — é da responsabilidade do
// composition root. O call é passado por ponteiro para que o hook de identidade
// possa resolver/validar o principal e propagar contexto aos hooks seguintes.
//
// Contrato: um Hook NÃO deve entrar em panic; se o fizer, o RM trata-o como
// fail-closed (deny). Um erro devolvido é igualmente fail-closed.
type Hook interface {
	// Name é o identificador estável do hook (usado em DeniedBy e nos spies).
	Name() string
	// Evaluate avalia o call e devolve o veredicto parcial.
	Evaluate(ctx context.Context, call *Call) (HookResult, error)
}
