package identity

import (
	"context"

	rm "github.com/aos-ref/kernel/reference-monitor"

	"github.com/aos-ref/platform/identity/delegation"
)

// IdentityCheck adapta o [Verifier] à interface Hook do Reference Monitor
// (AOS-003), ocupando o ponto de injecção do hook "identity" (o antigo
// IdentityStub). É a materialização da fundação identidade-antes-de-autoridade:
//
//   - lê o token NHI de Call.Credential;
//   - verifica-o (assinatura, janela temporal, revogação);
//   - impõe a fronteira de escopo (a Capability pedida tem de estar no token);
//   - em sucesso RESOLVE o Principal (mutação do *Call partilhado) para os hooks
//     seguintes (PDP, AOS-004) e devolve permit.
//
// Proibição de identidade anónima (ADR-003): SEM token, ou com token inválido/
// expirado/fora-de-escopo/revogado, devolve DENY — a chamada mediada NÃO
// prossegue. Nunca entra em panic; qualquer condição que não seja uma NHI válida
// e no escopo é fail-closed.
type IdentityCheck struct {
	verifier *Verifier
}

// NewIdentityCheck constrói o hook sobre um verificador. Um verificador nil deixa
// o hook sem forma de resolver identidade: fail-closed (todo o Evaluate nega).
func NewIdentityCheck(v *Verifier) *IdentityCheck {
	return &IdentityCheck{verifier: v}
}

// Name é o identificador estável do hook (usado em DeniedBy e nos eventos).
func (*IdentityCheck) Name() string { return "identity" }

// Evaluate implementa rm.Hook.
func (c *IdentityCheck) Evaluate(ctx context.Context, call *rm.Call) (rm.HookResult, error) {
	if c == nil || c.verifier == nil {
		return deny(ErrNoCredential.Error()), nil
	}
	// Proibição de anónimo: sem credencial não há autoridade.
	if call.Credential == "" {
		return deny(ErrNoCredential.Error()), nil
	}

	principal, err := c.verifier.Verify(ctx, call.Credential)
	if err != nil {
		// Assinatura inválida, alg/none, expirado, ainda-não-válido, emissor
		// desconhecido ou revogado: tudo fail-closed.
		return deny(err.Error()), nil
	}

	// Fronteira de escopo: a capability pedida tem de estar no escopo do token.
	// (O PDP aplica ainda a sua política por cima; aqui garante-se que o token
	// nunca autoriza fora do que lhe foi concedido.)
	//
	// NOTA (fronteira intencional): esta verificação SÓ corre quando a Call
	// declara uma Capability. Uma chamada mediada SEM Capability contorna a
	// fronteira de escopo deste hook — a NHI é resolvida e o hook devolve permit
	// independentemente do escopo do token (mesmo escopo vazio). A autorização
	// fica então inteiramente a cargo do PDP a jusante (AOS-004), que é o gate de
	// autorização e é default-deny. Este hook impõe identidade-antes-de-autoridade
	// e a fronteira de escopo por-capability, NÃO a política de autorização.
	if call.Capability != "" && !principal.Allows(call.Capability) {
		return deny(ErrOutOfScope.Error()), nil
	}

	// Resolve o Principal para os hooks seguintes (mecanismo de mutação do *Call
	// já existente no RM). NHIID = agent_id; Authority = escopo do token; a cadeia
	// de delegação COMPLETA (raiz humana → agente actual, já verificada por
	// Verify) liga a NHI ao humano responsável e propaga a cada evento de tool
	// call, permitindo reconstruir "quem autorizou" (AOS-006).
	call.Principal = rm.Principal{
		NHIID:           principal.AgentID,
		AgentID:         principal.AgentID,
		AgentClass:      principal.AgentClass,
		DelegationChain: toRMChain(principal.DelegationChain),
		Authority:       principal.Scope,
		// Autoridade de escopo derivada da IDENTIDADE (AOS-156): o grant ASSINADO pelo
		// issuer, por-sujeito, para o ScopeGate (AOS-071) resolver — incl. o agente
		// por-mint, que nenhum directório estático pode conhecer. Ver [subjectAuthorityFromScope].
		SubjectAuthority: subjectAuthorityFromScope(principal),
	}
	return rm.HookResult{Decision: rm.HookAllow}, nil
}

// subjectAuthorityFromScope deriva a autoridade-fonte por-SUJEITO (raiz humana, cada
// agente da cadeia, "agent:<classe>") a partir do escopo VERIFICADO do token. O issuer
// já computou Scope = UserAuthority ∩ ClassPolicy.Scope no mint e o Verify validou-o
// (assinatura + cadeia + Scope ⊆ folha.Authority), pelo que atribuir o escopo verificado
// a cada sujeito faz o fold do [rm.ScopeGate] reproduzir EXACTAMENTE o grant assinado —
// nunca o amplia. É o ÚNICO sítio que conhece a autoridade do agente POR-MINT (dinâmica),
// impossível num directório estático externo (AOS-156). Os sujeitos correspondem 1:1 aos
// que o ScopeGate dobra (chainSubjects + "agent:"+AgentClass).
func subjectAuthorityFromScope(p Principal) map[string][]string {
	scope := append([]string(nil), p.Scope...) // cópia partilhada read-only pelas chaves
	out := make(map[string][]string)
	chain := p.DelegationChain
	if len(chain) > 0 {
		out[chain[0].Sub] = scope // raiz humana (eixo UTILIZADOR)
		for _, l := range chain {
			if l.ActAs != "" {
				out[l.ActAs] = scope // cada agente delegatário
			}
		}
	}
	if p.AgentClass != "" {
		out["agent:"+p.AgentClass] = scope // tecto da CLASSE do agente autenticado
	}
	return out
}

// toRMChain projecta a cadeia de delegação verificada para os hops (sub/act_as)
// do Reference Monitor, que os grava no Producer de cada evento de mediação. A
// ordem dos elos (raiz humana primeiro) é preservada.
func toRMChain(chain delegation.Chain) []rm.DelegationHop {
	if len(chain) == 0 {
		return nil
	}
	out := make([]rm.DelegationHop, len(chain))
	for i, l := range chain {
		out[i] = rm.DelegationHop{Sub: l.Sub, ActAs: l.ActAs}
	}
	return out
}

// deny constrói um HookResult de negação com a razão dada.
func deny(reason string) rm.HookResult {
	return rm.HookResult{Decision: rm.HookDeny, Reason: reason}
}

// Assegura em compile-time que IdentityCheck satisfaz o contrato Hook do RM.
var _ rm.Hook = (*IdentityCheck)(nil)
