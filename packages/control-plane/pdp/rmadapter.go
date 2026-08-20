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

	return paraRM(dec), nil
}

// paraRM converte a decisão do PDP para o modelo do Reference Monitor.
//
// Está extraída do [PolicyCheck.Evaluate] porque cada ramo encerra uma DECISÃO — o que
// atravessa a fronteira e o que fica — e essas decisões merecem ser exercitáveis sem montar
// um PDP inteiro. Foi num destes ramos que a obrigação da escalada se perdia.
func paraRM(dec Decision) rm.HookResult {
	switch dec.Effect {
	case Permit:
		return rm.HookResult{
			Decision:      rm.HookAllow,
			Reason:        dec.Reason,
			Obligations:   toRMObligations(dec.Obligations),
			PolicyVersion: dec.PolicyVersion,
		}
	case Escalate:
		return rm.HookResult{
			Decision: rm.HookEscalate,
			Reason:   dec.Reason,
			// AS OBRIGAÇÕES VIAJAM TAMBÉM NA ESCALADA. A de autonomia traz o nível, o domínio, o
			// modo de oversight e a classe de risco — a resposta ESTRUTURADA ao «porquê» que esta
			// decisão precisa de deixar no trilho. `applyAutonomy` anexa-a ANTES de rebaixar o
			// efeito, e este ramo deitava-a fora: o selo saía com `Obligations: null` e a razão só
			// em TEXTO LIVRE. Observado em produção a 2026-08-19 — um auditor que percorra
			// obrigações via as autorizações e NÃO via as escaladas.
			//
			// Registar NÃO é impor: no RM, uma escalada regista e devolve ANTES de
			// `enforceObligations`, que só corre no caminho de permit. Ver [Monitor.fail].
			//
			// O ramo de DENY continua a NÃO as levar, e é deliberado: `applyAutonomy` só corre
			// sobre uma base permit, pelo que ali não existe obrigação de autonomia — e registar as
			// da base (redacção, ttl) sobre uma acção que NÃO aconteceu sugeriria que algo lhes foi
			// aplicado.
			Obligations:   toRMObligations(dec.Obligations),
			PolicyVersion: dec.PolicyVersion,
		}
	default: // Deny e qualquer efeito não reconhecido → fail-closed.
		return rm.HookResult{
			Decision:      rm.HookDeny,
			Reason:        dec.Reason,
			PolicyVersion: dec.PolicyVersion,
		}
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
			ID:         call.Principal.NHIID,
			AgentClass: call.Principal.AgentClass,
			// Board propaga a CHAVE da soberania por board (AOS-094) para o Input do PDP,
			// fechando a cadeia PDP-emite → PEP-impõe no adaptador de PRODUÇÃO (e não só
			// num hook de teste). Como AgentClass, ASSUME-SE já resolvido de uma NHI
			// verificada: o hook de identidade real (identity.IdentityCheck, AOS-005)
			// substitui o Principal inteiro a partir do token ANTES deste adaptador. Sob um
			// IdentityStub pass-through o board vem do Call bruto e é forjável — a mesma
			// fronteira de confiança documentada para a agent_class no gate default-deny.
			Board:           call.Principal.Board,
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
			// A CLASSE DE RISCO tem de atravessar. O RiskGate calcula-a e escreve-a em
			// call.Context.RiskClass; o overlay de autonomia lê-a de in.Context.RiskClass; e
			// este adaptador — o único fio entre os dois — não a copiava.
			//
			// A consequência não era um deny visível: riskClassFromString("") resolve para
			// ClassDanger (fail-closed), portanto TODA a acção era tratada como danger e a
			// taxonomia L0–L5 colapsava em dois estados — L5 corre, tudo o resto escala.
			// Um mecanismo graduado presente, documentado, e semanticamente inerte.
			RiskClass:   call.Context.RiskClass,
			Sensitivity: call.Context.Sensitivity,
			// AOS-021: prova NÃO-FORJÁVEL de que o gate humano exigido pela autonomia já
			// ocorreu para esta acção. Vem de um campo não-exportado do Call que só o
			// ApprovalGate escreve após verificar a evidência contra o broker (amarra à
			// preview, uso-único, TTL) — nunca do conteúdo do pedido.
			HumanGateSatisfied: call.HumanApproval() != nil,
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
