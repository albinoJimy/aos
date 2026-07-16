package referencemonitor

import (
	"context"

	"github.com/aos-ref/kernel/reference-monitor/authz"
)

// humanRootPrefix é o prefixo obrigatório do sujeito da RAIZ da cadeia de
// delegação: a raiz é SEMPRE um humano responsável ("human:<user_id>", ADR-003).
// Reimplementado localmente (zero-dep) para não importar platform/identity —
// espelha delegation.HumanPrefix sem acoplar o layer (evita o ciclo
// kernel→platform). O valor "human:" está FIXADO por
// TestScope_HumanRootPrefix_PinnedToDelegationSource como guarda de drift face à
// fonte canónica delegation.HumanPrefix (AOS-071 F3).
const humanRootPrefix = "human:"

// agentClassPrefix é o prefixo do sujeito da CLASSE de um agente
// ("agent:<class>") na [authz.AuthoritySource]. O eixo CLASSE do escopo efectivo
// deriva SEMPRE da classe do PRINCIPAL AUTENTICADO ([Principal.AgentClass]), não
// da cadeia (opcional e manipulável pelo chamador) — ver AOS-071 F1.
const agentClassPrefix = "agent:"

// agentClassSubject devolve o sujeito canónico da classe do agente actual
// ("agent:<class>"), a resolver via [authz.AuthoritySource] para obter o TECTO de
// menor privilégio da classe. Devolve "" se a classe não está definida — caso em
// que o gate NÃO tem tecto de classe e fail-closes (o eixo CLASSE não pode ser
// omitido).
func agentClassSubject(class string) string {
	if class == "" {
		return ""
	}
	return agentClassPrefix + class
}

// ScopeGate é o hook de enforcement da AUTORIDADE ESCOPADA AO PRINCIPAL (AOS-071):
// ao mediar uma tool call, computa a autoridade EFECTIVA do principal —
// utilizador ∩ classe(s) ao longo da cadeia on-behalf-of (ADR-003) — e NEGA a
// call se a capability pedida cai FORA desse escopo (default-deny, ADR-011). É a
// contramedida estrutural ao confused deputy: um agente nunca age com autoridade
// que não é a intersecção do utilizador que representa com a sua classe, mesmo
// quando um sub-agente ou conteúdo untrusted o solicita.
//
// PROPRIEDADES IMPOSTAS:
//   - Intersecção utilizador ∩ classe: o escopo efectivo é a dobra de
//     intersecções ([authz.FoldScope]) da autoridade-fonte de cada sujeito da
//     cadeia (raiz humana → agente actual) INTERSECTADA SEMPRE com a classe do
//     PRINCIPAL AUTENTICADO ([Principal.AgentClass]), resolvida via
//     [authz.AuthoritySource]. O eixo CLASSE está amarrado ao principal, não à
//     cadeia (opcional/manipulável): mesmo que a cadeia seja degenerada ou declare
//     uma classe-folha diferente da autenticada, a autoridade nunca excede
//     utilizador ∩ classe-real. Nenhuma tool call excede esse escopo.
//   - Restrição monotónica (sub-agente não escala): a dobra só intersecta — a
//     folha nunca vê mais do que a raiz permite. Além disso, uma autoridade
//     RECLAMADA pelo principal ([Call.Principal.Authority]) que exceda o escopo
//     efectivo é uma tentativa de alargamento explícita, NEGADA
//     ([authz.ErrScopeEscalation]).
//   - Untrusted não eleva: o escopo efectivo deriva EXCLUSIVAMENTE da identidade
//     (utilizador + classe), nunca do conteúdo/taint do pedido. Seja qual for o
//     rótulo de taint, a intersecção é a mesma — conteúdo untrusted não pode
//     elevar a autoridade. Compõe (não duplica) o [TaintGate] de AOS-069.
//   - Confused deputy negado e registado: qualquer capability fora da intersecção
//     (ou autoridade reclamada acima dela) é negada com [Decision.DeniedBy] ==
//     "scope" e registada no evento de mediação (atribuível, sem segredos).
//   - Escopo efectivo observável: o gate ANOTA [Call.Principal.Authority] com o
//     escopo efectivo computado (forma canónica), pelo que o audit da mediação
//     regista a autoridade REALMENTE em vigor (menor privilégio), não a reclamada.
//     A metade SPAN/OTel deste critério (ADR-002/010) está deliberadamente
//     DIFERIDA para EPIC-08 (observabilidade — ver monitor.go: sem SDK OTel neste
//     ticket); o canal AUDIT já satisfaz a rastreabilidade fail-closed hoje.
//
// A decisão é DETERMINISTA e PURA (sem relógio/rand): função apenas de
// (autoridade-fonte, cadeia de delegação, capability).
type ScopeGate struct {
	authority authz.AuthoritySource
}

// NewScopeGate constrói o gate com a fonte de autoridade (o directório de
// identidade/RBAC do GOV; em testes, [authz.StaticAuthoritySource]). Uma fonte
// nil torna o gate FAIL-CLOSED: sem forma de resolver a autoridade-fonte, toda a
// call escopada é negada (nunca um no-op permissivo — ao contrário do TaintGate,
// que sem classificador não tem nada a bloquear, aqui a ausência de fonte
// significa "não sei o escopo" ⇒ deny).
func NewScopeGate(authority authz.AuthoritySource) ScopeGate {
	return ScopeGate{authority: authority}
}

// Name implementa [Hook]. É o valor gravado em [Decision.DeniedBy] quando o gate
// nega, tornando a negação por escopo distinguível na auditoria (confused deputy).
func (ScopeGate) Name() string { return "scope" }

// Evaluate implementa [Hook]. Computa o escopo efectivo e aplica a política de
// escopo default-deny, anotando o escopo efectivo no principal do call.
func (g ScopeGate) Evaluate(_ context.Context, call *Call) (HookResult, error) {
	// Fonte ausente ⇒ fail-closed: não há como computar utilizador ∩ classe.
	if g.authority == nil {
		call.Principal.Authority = nil
		return HookResult{
			Decision: HookDeny,
			Reason:   "fonte de autoridade ausente: escopo indeterminavel (fail-closed)",
		}, nil
	}

	subjects, ok := chainSubjects(call.Principal)
	if !ok {
		// Cadeia ausente, sem raiz humana atribuível ou mal-formada (elo vazio /
		// descontinuidade) ⇒ sem principal a quem escopar de forma fiável (ADR-003,
		// AOS-071 F2). Fail-closed.
		call.Principal.Authority = nil
		return HookResult{
			Decision: HookDeny,
			Reason:   authz.ErrOrphanChain.Error(),
		}, nil
	}

	// EIXO CLASSE AMARRADO AO PRINCIPAL AUTENTICADO (AOS-071 F1): a autoridade-fonte
	// da CLASSE do agente actual ([Principal.AgentClass]) é SEMPRE intersectada,
	// independentemente da cadeia. A cadeia é um input opcional e manipulável pelo
	// chamador; se o eixo CLASSE dependesse só dela, uma cadeia degenerada colapsaria
	// para a autoridade PLENA do utilizador (authority = user, não user∩classe) e uma
	// classe-folha forjada permitiria exceder o tecto da classe REAL. Sem classe
	// resolúvel não há tecto de menor privilégio ⇒ fail-closed (nunca no-op permissivo).
	classSubject := agentClassSubject(call.Principal.AgentClass)
	if classSubject == "" {
		call.Principal.Authority = nil
		return HookResult{
			Decision: HookDeny,
			Reason:   "classe do agente indeterminada: escopo sem tecto de menor privilegio (fail-closed)",
		}, nil
	}
	subjects = append(subjects, classSubject)

	// Escopo efectivo = dobra de intersecções da autoridade-fonte de cada sujeito
	// (raiz → folha). Um sujeito não resolvido conta como autoridade VAZIA
	// (fail-closed: a intersecção colapsa para ∅ ⇒ tudo negado).
	sets := make([][]string, 0, len(subjects))
	for _, s := range subjects {
		caps, _ := g.authority.Authority(s)
		sets = append(sets, caps)
	}
	effective := authz.FoldSets(sets)

	// ANOTAÇÃO (span/audit): o escopo efectivo em vigor substitui a autoridade
	// reclamada no principal. Faz-se ANTES de qualquer deny para que o evento de
	// negação registe também o escopo computado (o RM constrói o registo a partir
	// de call.Principal após a cadeia — ver monitor.go). A autoridade reclamada
	// original é preservada abaixo, apenas para a verificação de escalada.
	claimed := call.Principal.Authority
	call.Principal.Authority = effective

	// RESTRIÇÃO MONOTÓNICA / anti-escalada: uma autoridade reclamada que exceda o
	// escopo efectivo (utilizador ∩ classe) é uma tentativa de alargamento — um
	// sub-agente a reclamar mais do que o principal lhe concede, ou o vector do
	// confused deputy. Negada e atribuível.
	if err := authz.CheckNoEscalation(claimed, effective); err != nil {
		return HookResult{
			Decision: HookDeny,
			Reason:   err.Error(),
		}, nil
	}

	// POLÍTICA DE ESCOPO (policy-as-code, default-deny): a capability pedida tem de
	// pertencer ao escopo efectivo. A ausência de correspondência é DENY.
	//
	// UNTRUSTED NÃO ELEVA (composição com AOS-069): `effective` não depende de
	// call.Context.Taint — deriva só da identidade. Logo um pedido originado em
	// conteúdo untrusted obtém EXACTAMENTE o mesmo escopo; nunca o eleva.
	if dec := authz.Authorize(effective, call.Capability); !dec.Allow {
		return HookResult{
			Decision: HookDeny,
			Reason:   dec.Reason,
		}, nil
	}
	return allow, nil
}

// chainSubjects extrai a lista ORDENADA de sujeitos da cadeia on-behalf-of (raiz
// humana → agente actual), cujas autoridades-fonte se intersectam para formar o
// escopo efectivo. Devolve ok=false (fail-closed) se a cadeia é vazia, a raiz não
// é um humano atribuível, um elo não delega (ActAs vazio) ou a cadeia é
// DESCONTÍNUA (ADR-003, AOS-071 F2).
//
// A raiz (chain[0].Sub) é o humano (eixo UTILIZADOR); cada [DelegationHop.ActAs]
// é um agente delegatário, pela ordem da delegação. Numa cadeia BEM-FORMADA
// chain[i].ActAs == chain[i+1].Sub, pelo que a lista [root.Sub, hop_0.ActAs,
// hop_1.ActAs, …] cobre o humano e todos os agentes até à folha. A continuidade é
// VERIFICADA (não assumida): uma descontinuidade indicia uma cadeia
// forjada/adulterada e é rejeitada. Um elo com ActAs vazio NÃO é ignorado
// silenciosamente (ignorá-lo permitia colapsar a cadeia para só a raiz humana —
// autoridade PLENA do utilizador, sem o eixo agente; AOS-071 F1) mas rejeitado.
//
// NOTA: o eixo CLASSE do principal AUTENTICADO é intersectado à parte, em
// Evaluate ([Principal.AgentClass]); não depende desta cadeia. A raiz humana da
// cadeia continua a NÃO ser vinculável ao token autenticado dentro do gate — essa
// amarração raiz↔humano-autenticado é responsabilidade do hook de identidade
// (AOS-005), que valida/emite a cadeia a partir do token NHI a montante.
func chainSubjects(p Principal) ([]string, bool) {
	chain := p.DelegationChain
	if len(chain) == 0 {
		return nil, false
	}
	root := chain[0].Sub
	if !isHumanRoot(root) {
		return nil, false
	}
	subjects := make([]string, 0, len(chain)+1)
	subjects = append(subjects, root)
	for i, hop := range chain {
		// Cada elo delega EFECTIVAMENTE para um agente: ActAs vazio ⇒ cadeia
		// mal-formada (fail-closed), não um elo a saltar.
		if hop.ActAs == "" {
			return nil, false
		}
		// Continuidade on-behalf-of: o delegatário de um elo é o delegante do
		// seguinte. Uma quebra indicia adulteração ⇒ fail-closed.
		if i+1 < len(chain) && hop.ActAs != chain[i+1].Sub {
			return nil, false
		}
		subjects = append(subjects, hop.ActAs)
	}
	return subjects, true
}

// isHumanRoot indica se o sujeito é um humano responsável (prefixo "human:" com
// identificador não-vazio). Espelha delegation.IsHuman sem importar o layer.
func isHumanRoot(sub string) bool {
	return len(sub) > len(humanRootPrefix) && sub[:len(humanRootPrefix)] == humanRootPrefix
}

// DefaultHooksWithScope devolve a cadeia canónica [DefaultHooks] com o [ScopeGate]
// inserido LOGO APÓS a política (identity → policy → scope → budget → egress →
// audit): o escopo é avaliado antes de reservar orçamento ou egress, para uma
// call fora do escopo nunca consumir recursos. Composição OPT-IN (à imagem de
// [DefaultHooksWithTaint]): [DefaultHooks] NÃO inclui o ScopeGate, logo um Monitor
// construído sem esta cadeia obtém ZERO enforcement de escopo — ligar esta
// composição, com uma [authz.AuthoritySource] real, é responsabilidade do
// composition root ápice (packages/integration).
func DefaultHooksWithScope(authority authz.AuthoritySource) []Hook {
	base := DefaultHooks() // identity, policy, budget, egress, audit
	gate := NewScopeGate(authority)
	out := make([]Hook, 0, len(base)+1)
	for _, h := range base {
		out = append(out, h)
		if h.Name() == "policy" {
			out = append(out, gate)
		}
	}
	return out
}

// DefaultHooksWithTaintAndScope devolve a cadeia canónica com o [TaintGate] e o
// [ScopeGate] inseridos após a política, nesta ordem: identity → policy → taint →
// scope → budget → egress → audit. É a composição de segurança RECOMENDADA em
// produção (AOS-069 + AOS-071): primeiro corta-se a autorização untrusted de uma
// capability privilegiada (barreira control/data-plane), depois impõe-se o menor
// privilégio identitário (utilizador ∩ classe). As duas barreiras são
// independentes e complementares; o escopo não depende do taint (untrusted não
// eleva), o taint não depende do escopo (untrusted não autoriza privilegiado).
//
// Wiring de produção DIFERIDO para o ticket de integração de superfície (a par de
// AOS-021/037/043), tal como [DefaultHooksWithTaint]. Um integrador de produção
// DEVE usar esta função (ou inserir manualmente ambos os gates após "policy").
func DefaultHooksWithTaintAndScope(privileged PrivilegedAuthorizer, authority authz.AuthoritySource) []Hook {
	base := DefaultHooks() // identity, policy, budget, egress, audit
	taintGate := NewTaintGate(privileged)
	scopeGate := NewScopeGate(authority)
	out := make([]Hook, 0, len(base)+2)
	for _, h := range base {
		out = append(out, h)
		if h.Name() == "policy" {
			out = append(out, taintGate, scopeGate)
		}
	}
	return out
}
