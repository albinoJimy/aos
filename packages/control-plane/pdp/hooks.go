package pdp

import (
	rm "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/kernel/reference-monitor/authz"
)

// CADEIA SANCIONADA DE PRODUÇÃO (AOS-087, AC1 — mediação total).
//
// O [rm.PolicyStub] da cadeia default do RM ([rm.DefaultHooks]) é um PLACEHOLDER
// default-allow, existente só para o RM ser exercitável em isolamento (AOS-003). A
// produção NÃO o pode usar: a política REAL é default-deny e vive no PDP (AOS-004/
// 007). Estas funções compõem a cadeia canónica com o [PolicyCheck] REAL no ponto
// de injecção da política, de modo que CADA tool call é decidida pelo PDP no ponto
// único de mediação ([rm.Monitor.Mediate]) — uma capability sem concessão explícita
// na allowlist assinada ⇒ deny.
//
// CAMADAS. O wiring vive AQUI (control-plane/pdp), não no RM (kernel): o RM não pode
// importar o PDP (o PDP é que importa o RM). O composition root ápice
// (packages/integration) consome estas funções — com um [rm.WithEventSink] durável
// e o hook de identidade REAL (platform/identity) no lugar do IdentityStub — para
// obter o RM de produção. Sob o IdentityStub pass-through a agent_class é forjável;
// ver a "Fronteira de confiança" em capabilities.go e identity_gate_integration_test.go.

// DefaultHooksWithPDP devolve a cadeia canónica de mediação com o [PolicyCheck] real
// no lugar do [rm.PolicyStub]: identity → policy(PDP) → budget → egress → audit. É a
// cadeia mínima que satisfaz a mediação total de AOS-087 (o PDP decide cada tool
// call, default-deny). Um PDP nil deixa o [PolicyCheck] fail-closed (todo o Evaluate
// nega) — nunca abre uma exceção default-allow.
func DefaultHooksWithPDP(p *PDP) []rm.Hook {
	return []rm.Hook{
		rm.IdentityStub{},
		NewPolicyCheck(p),
		rm.BudgetStub{},
		rm.EgressStub{},
		rm.AuditStub{},
	}
}

// DefaultHooksWithPDPAndScope devolve a cadeia canónica com o [PolicyCheck] real E o
// [rm.ScopeGate] (AOS-071) inseridos após a política: identity → policy(PDP) → scope
// → budget → egress → audit. Compõe as duas fronteiras de AOS-087:
//
//   - policy(PDP): default-deny da allowlist capability-scoped por agent_class (AC2);
//   - scope: autoridade EFECTIVA = utilizador ∩ classe (AC3) — uma capability
//     concedida pela CLASSE mas fora do escopo do UTILIZADOR é negada (DeniedBy=="scope"),
//     mesmo que a allowlist da classe a conceda.
//
// authority é a [authz.AuthoritySource] real (o directório de identidade/RBAC do GOV;
// em teste, [authz.StaticAuthoritySource]). Uma fonte nil torna o ScopeGate
// fail-closed (toda a call escopada nega). Wiring de produção é do composition root.
func DefaultHooksWithPDPAndScope(p *PDP, authority authz.AuthoritySource) []rm.Hook {
	base := DefaultHooksWithPDP(p)
	scope := rm.NewScopeGate(authority)
	out := make([]rm.Hook, 0, len(base)+1)
	for _, h := range base {
		out = append(out, h)
		if h.Name() == "policy" {
			out = append(out, scope)
		}
	}
	return out
}
