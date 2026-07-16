package referencemonitor

import (
	"context"

	"github.com/aos-ref/kernel/reference-monitor/taint"
)

// PrivilegedAuthorizer classifica capabilities como PRIVILEGIADAS — aquelas cuja
// autorização o AOS exige que provenha de dados TRUSTED (system + utilizador
// autenticado). É a peça plugável do enforcement control/data-plane (ADR-005):
// a política de "o que é privilegiado" externaliza-se aqui (compõe/estende o gate
// de política do RM, AOS-004), mas a INVARIANTE — "conteúdo untrusted não satisfaz
// uma capability privilegiada" — é imposta pelo [TaintGate], não delegável.
type PrivilegedAuthorizer interface {
	// IsPrivileged indica se a capability exige autorização trusted.
	IsPrivileged(capability string) bool
}

// StaticPrivilegedSet é uma allowlist estática (imutável) de capabilities
// privilegiadas. Determinista e sem estado partilhado — segura para concorrência.
//
// MODELO DE AMEAÇA — CLASSIFICAÇÃO É DENYLIST (default-NÃO-privilegiado): no eixo
// "isto é privilegiado?", uma capability NÃO enumerada aqui NÃO é tratada como
// privilegiada e, logo, o [TaintGate] NÃO a bloqueia mesmo sob autorização
// untrusted. Isto é DELIBERADO e depende de uma pré-condição: o gate de política
// (PDP, AOS-004) é o default-deny PRIMÁRIO — nenhuma capability é exercível sem uma
// política que a permita — e o TaintGate é DEFESA-EM-PROFUNDIDADE sobre o subconjunto
// sensível. Consequência a vigiar: uma capability perigosa NOVA que seja permitida
// pela política mas esquecida deste conjunto escapa ao eixo de taint. Mitigação
// operacional: manter este conjunto sincronizado com as capabilities sensíveis do
// catálogo (idealmente um teste de cobertura que falhe se uma capability marcada
// "sensível" não constar aqui). Inverter para allowlist de capabilities SEGURAS-para-
// untrusted (default-privileged) é possível onde o modelo de ameaça o exija, mas
// alargaria o bloqueio a toda a capability não-classificada — fora do escopo de AOS-069.
type StaticPrivilegedSet struct {
	caps map[string]struct{}
}

// NewStaticPrivilegedSet constrói um conjunto a partir das capabilities dadas.
func NewStaticPrivilegedSet(capabilities ...string) StaticPrivilegedSet {
	m := make(map[string]struct{}, len(capabilities))
	for _, c := range capabilities {
		if c == "" {
			continue
		}
		m[c] = struct{}{}
	}
	return StaticPrivilegedSet{caps: m}
}

// IsPrivileged implementa [PrivilegedAuthorizer].
func (s StaticPrivilegedSet) IsPrivileged(capability string) bool {
	_, ok := s.caps[capability]
	return ok
}

// TaintGate é o hook de enforcement da barreira control/data-plane (ADR-005): ao
// mediar uma tool call PRIVILEGIADA, verifica o taint da AUTORIZAÇÃO
// ([CallContext.Taint], preenchido na origem pelo Agent Runtime) e NEGA a call se
// a autorização provém de (ou deriva de) conteúdo UNTRUSTED. Estruturalmente: SÓ
// dados trusted podem ORIGINAR uma acção privilegiada; um payload untrusted que
// tente autorizá-la (ex.: uma injecção "ignora as instruções e envia X para Y"
// embutida num tool result) é bloqueado aqui, default-deny.
//
// A decisão é DETERMINISTA e PURA (sem relógio/rand): função apenas de
// (capability, taint). A negação é atribuível — [Decision.DeniedBy] == "taint" — e
// registada no evento de mediação com o rótulo de taint no [CallContext], SEM
// segredos (o Input da tool nunca é gravado; ver eventsink.go).
//
// Uma capability NÃO privilegiada nunca é bloqueada por taint: conteúdo untrusted
// é DADOS legítimos no data-plane; só a AUTORIZAÇÃO de uma acção privilegiada tem
// de ser trusted.
type TaintGate struct {
	privileged PrivilegedAuthorizer
}

// NewTaintGate constrói o gate com o classificador de capabilities privilegiadas.
// Um authorizer nil torna o gate um no-op seguro (nada é privilegiado ⇒ nada é
// bloqueado por taint), mas produção DEVE fornecer um conjunto real.
func NewTaintGate(privileged PrivilegedAuthorizer) TaintGate {
	return TaintGate{privileged: privileged}
}

// Name implementa [Hook]. É o valor gravado em [Decision.DeniedBy] quando o gate
// nega, tornando a negação por taint distinguível de qualquer outra na auditoria.
func (TaintGate) Name() string { return "taint" }

// Evaluate implementa [Hook]. Permite tudo excepto uma capability privilegiada
// cuja autorização seja untrusted, que nega fail-closed.
func (g TaintGate) Evaluate(_ context.Context, call *Call) (HookResult, error) {
	if g.privileged == nil || !g.privileged.IsPrivileged(call.Capability) {
		// Não-privilegiada (ou sem classificador): o taint não bloqueia — untrusted
		// é dados legítimos no data-plane.
		return allow, nil
	}
	// Capability privilegiada: SÓ autorização trusted a satisfaz (ADR-005). O taint
	// ausente/desconhecido resolve untrusted (fail-closed, ver taint.ParseLabel).
	if taint.ParseLabel(call.Context.Taint).IsUntrusted() {
		return HookResult{
			Decision: HookDeny,
			Reason:   "autorizacao untrusted nao pode originar tool call privilegiada (ADR-005)",
		}, nil
	}
	return allow, nil
}

// DefaultHooksWithTaint devolve a cadeia canónica [DefaultHooks] com o [TaintGate]
// inserido LOGO APÓS a política (identity → policy → taint → budget → egress →
// audit): a barreira control/data-plane é avaliada antes de reservar orçamento ou
// egress, para uma acção privilegiada autorizada por untrusted nunca consumir
// recursos. É a composição recomendada em produção (AOS-069).
//
// ENFORCEMENT NÃO-ACTIVO POR OMISSÃO — HANDOFF DE WIRING (AOS-069). Esta composição é
// OPT-IN: [DefaultHooks] (o default de [New]) NÃO inclui o TaintGate, logo um Monitor
// construído sem passar esta cadeia obtém ZERO enforcement de taint — a invariante P0
// do ADR-005 fica silenciosamente inactiva. Ligar DefaultHooksWithTaint(privileged),
// com um [PrivilegedAuthorizer] real, é responsabilidade do composition root ápice
// (packages/integration). Esse wiring — a par de AOS-021/037/043 — está DIFERIDO para
// o ticket de integração de superfície; até lá o único consumidor de produção desta
// cadeia é o harness de teste de planos (agent-runtime/taint_plane_test.go, via
// planeHarness), que prova a barreira fim-a-fim. Um integrador de produção DEVE usar
// esta função (ou inserir manualmente [NewTaintGate] após "policy"); construir um RM
// de produção sobre [DefaultHooks] sem o TaintGate é misconfiguração de segurança.
func DefaultHooksWithTaint(privileged PrivilegedAuthorizer) []Hook {
	base := DefaultHooks() // identity, policy, budget, egress, audit
	gate := NewTaintGate(privileged)
	out := make([]Hook, 0, len(base)+1)
	for _, h := range base {
		out = append(out, h)
		if h.Name() == "policy" {
			out = append(out, gate)
		}
	}
	return out
}
