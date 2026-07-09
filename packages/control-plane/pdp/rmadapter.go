package pdp

import (
	"context"

	rm "github.com/aos-ref/kernel/reference-monitor"
)

// PolicyCheck adapta o [PDP] à interface Hook do Reference Monitor (AOS-003),
// ocupando o ponto de injecção do hook de política (o antigo PolicyStub). É a
// materialização do contrato C1 RM↔PDP: traduz o Call do RM em [Input], invoca
// [PDP.Decide] e traduz a [Decision] de volta em HookResult — propagando
// policy_version e obligations, que o RM leva ao evento de mediação.
//
// Fail-closed: qualquer erro de porta (política indisponível, assinatura
// inválida detectada no load, request malformado) resulta em HookDeny. Só um
// permit explícito autoriza.
type PolicyCheck struct {
	pdp decider
}

// decider é a superfície mínima do [PDP] de que o adaptador depende. Isola o
// adaptador do tipo concreto, permitindo exercitar directamente os ramos
// fail-closed e o mapeamento Escalate (contrato C1) com um duplo em teste.
type decider interface {
	Decide(context.Context, Input) (Decision, error)
}

// NewPolicyCheck constrói o adaptador sobre um PDP já carregado. Um PDP nil deixa
// o adaptador sem decisor: fail-closed (todo o Evaluate devolve HookDeny).
func NewPolicyCheck(p *PDP) *PolicyCheck {
	if p == nil {
		return &PolicyCheck{} // pdp nil ⇒ fail-closed (nil-guard em Evaluate)
	}
	return &PolicyCheck{pdp: p}
}

// Name é o identificador estável do hook (usado em DeniedBy e nos eventos).
func (PolicyCheck) Name() string { return "policy" }

// Evaluate implementa rm.Hook. Nunca entra em panic; devolve HookDeny em
// qualquer condição que não seja um permit explícito.
func (c *PolicyCheck) Evaluate(ctx context.Context, call *rm.Call) (rm.HookResult, error) {
	if c == nil || c.pdp == nil {
		return rm.HookResult{Decision: rm.HookDeny, Reason: ErrPolicyUnavailable.Error()}, nil
	}

	in := inputFromCall(call)
	dec, err := c.pdp.Decide(ctx, in)

	// Erro de porta (E_POLICY_UNAVAILABLE / E_SIGNATURE_INVALID /
	// E_MALFORMED_REQUEST): fail-closed → deny, carregando a policy_version
	// conhecida (se houver) para o evento de audit.
	if err != nil {
		return rm.HookResult{
			Decision:      rm.HookDeny,
			Reason:        dec.Reason,
			PolicyVersion: dec.PolicyVersion,
		}, nil
	}

	switch dec.Effect {
	case Permit:
		return rm.HookResult{
			Decision:      rm.HookAllow,
			Reason:        dec.Reason,
			Obligations:   toRMObligations(dec.Obligations),
			PolicyVersion: dec.PolicyVersion,
		}, nil
	case Escalate:
		return rm.HookResult{
			Decision:      rm.HookEscalate,
			Reason:        dec.Reason,
			PolicyVersion: dec.PolicyVersion,
		}, nil
	default: // Deny e qualquer efeito não reconhecido → fail-closed.
		return rm.HookResult{
			Decision:      rm.HookDeny,
			Reason:        dec.Reason,
			PolicyVersion: dec.PolicyVersion,
		}, nil
	}
}

// Assegura em compile-time que PolicyCheck satisfaz o contrato Hook do RM.
var _ rm.Hook = (*PolicyCheck)(nil)

// inputFromCall traduz o Call do RM (contrato C1) no [Input] do PDP.
func inputFromCall(call *rm.Call) Input {
	authority := make([]string, len(call.Principal.Authority))
	copy(authority, call.Principal.Authority)

	chain := make([]string, 0, len(call.Principal.DelegationChain))
	for _, h := range call.Principal.DelegationChain {
		chain = append(chain, h.Sub)
	}

	return Input{
		RequestID: call.RequestID,
		Principal: Principal{
			ID:              call.Principal.NHIID,
			AgentClass:      call.Principal.AgentClass,
			DelegationChain: chain,
			Authority:       authority,
		},
		Capability: call.Capability,
		Resource: Resource{
			Type:   call.Resource.Type,
			Value:  call.Resource.Value,
			Region: call.Resource.Region,
		},
		Context: DecisionContext{
			Taint:                 call.Context.Taint,
			BudgetTokensRemaining: call.Context.BudgetTokensRemaining,
			Reversibility:         call.Context.Reversibility,
			Sensitivity:           call.Context.Sensitivity,
		},
	}
}

// toRMObligations converte as obrigações do PDP para o modelo do RM.
func toRMObligations(obs []Obligation) []rm.Obligation {
	if len(obs) == 0 {
		return nil
	}
	out := make([]rm.Obligation, len(obs))
	for i, o := range obs {
		out[i] = rm.Obligation{Type: o.Type, Fields: o.Fields, Params: o.Params}
	}
	return out
}
