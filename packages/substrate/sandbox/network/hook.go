package network

import (
	"context"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
)

// EgressHook é o ponto de COMPOSIÇÃO com o Reference Monitor: implementa
// [referencemonitor.Hook] e ocupa o slot "egress" da cadeia canónica de mediação
// (identity → policy → budget → egress → audit). Assim É O RM QUE APLICA a decisão
// de egress — a execução permanece MEDIADA e nenhum caminho a salta (ADR-002). O
// filtro apenas DECIDE ([EgressFilter.Decide]); o hook traduz a decisão para o
// veredicto da cadeia e o RM impõe-o (deny fail-closed corta o efeito antes do
// dispatch).
//
// O hook deriva o destino de rede da [referencemonitor.Resource] do call. Recursos
// que NÃO são egress de rede (ex.: "file", "db") não são competência deste hook: ele
// abstém-se (HookAllow) e deixa-os para os outros pontos de decisão. Um recurso de
// rede com valor malformado é tratado como egress inválido e NEGADO fail-closed.
//
// A competência NÃO depende só do Resource.Type (que o chamador controla e pode
// omitir/mislabelar). Um call que declara uma CAPABILITY de rede (cap:http.*,
// cap:net.*) mas cujo Resource.Type não é de rede EXERCE egress cujo destino não é
// verificável contra a allowlist: o hook NÃO se abstém — NEGA fail-closed. Fecha o
// vector de exfiltração via tool benigna com tipo mislabelado (CamoLeak).
type EgressHook struct {
	filter *EgressFilter
}

// NewEgressHook liga o hook ao filtro de egress. Substitui o [referencemonitor.EgressStub]
// neutro por enforcement REAL: passa-se a [referencemonitor.WithHooks] no lugar do stub.
func NewEgressHook(filter *EgressFilter) (*EgressHook, error) {
	if filter == nil {
		return nil, ErrNilFilter
	}
	return &EgressHook{filter: filter}, nil
}

// Name implementa [referencemonitor.Hook]: o nome canónico do slot de egress.
func (*EgressHook) Name() string { return "egress" }

// Evaluate implementa [referencemonitor.Hook]. Deriva o destino do call, consulta o
// filtro e devolve o veredicto parcial:
//
//   - recurso não-egress → HookAllow (não é competência deste hook);
//   - egress permitido pela allowlist → HookAllow (com a policy_version propagada);
//   - egress fora da allowlist / inválido / audit indisponível → HookDeny (fail-closed).
//
// A correlação (run_id/step_id) do call é propagada ao filtro para o evento de
// segurança selado no WORM ser atribuível à trajectória.
func (h *EgressHook) Evaluate(ctx context.Context, call *referencemonitor.Call) (referencemonitor.HookResult, error) {
	dest, isNet := DestinationFromResource(call.Resource)
	if !isNet {
		// Resource.Type não é de rede. Se o call ainda assim declara uma capability de
		// rede (cap:http.*, cap:net.*), é uma tentativa de egress cujo destino não se
		// consegue derivar/validar contra a allowlist → DENY fail-closed (nunca abster,
		// que a deixaria exfiltrar sem verificação — vector CamoLeak).
		if IsNetworkCapability(call.Capability) {
			return referencemonitor.HookResult{
				Decision: referencemonitor.HookDeny,
				Reason:   ReasonUnverifiableEgress,
			}, nil
		}
		// Não é egress de rede: este hook abstém-se (a cadeia prossegue).
		return referencemonitor.HookResult{Decision: referencemonitor.HookAllow}, nil
	}
	ctx = WithCorrelation(ctx, call.RunID, call.StepID)
	dec, err := h.filter.Decide(ctx, call.Principal, dest)
	if err != nil {
		// Selagem do bloqueio no WORM falhou: fail-closed (audit-before-effect). Nega
		// a acção não-auditável — nunca a deixa passar por o audit estar indisponível.
		return referencemonitor.HookResult{
			Decision:      referencemonitor.HookDeny,
			Reason:        ReasonAuditFailed,
			PolicyVersion: dec.PolicyVersion,
		}, nil
	}
	if !dec.Allow {
		return referencemonitor.HookResult{
			Decision:      referencemonitor.HookDeny,
			Reason:        dec.Reason,
			PolicyVersion: dec.PolicyVersion,
		}, nil
	}
	return referencemonitor.HookResult{
		Decision:      referencemonitor.HookAllow,
		PolicyVersion: dec.PolicyVersion,
	}, nil
}
