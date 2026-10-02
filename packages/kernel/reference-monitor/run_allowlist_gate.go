package referencemonitor

import "context"

// RunAllowlistHookName é o [Hook.Name] do [RunAllowlistGate] e o `denied_by` de TODA a
// recusa pela lista-branca do run — venha do gate ou do backstop de [Monitor.evaluate]. É
// contrato: o modelo lê-o no tail (`denied_by=run_tool_allowlist`) desde o AOS-413, e o
// evento e o selo passam a levá-lo desde o AOS-485.
const RunAllowlistHookName = "run_tool_allowlist"

// runAllowlistReason é o motivo, em texto, da recusa. Fixo e sem dados da call: a tool pedida
// já vai no `tool_id` do registo, e a lista não se grava (é do goal do run, não do evento).
const runAllowlistReason = "tool fora da lista-branca do run"

// RunAllowsTool diz se a lista-branca do run admite a tool: sem lista (nil), sim; com ela —
// MESMO VAZIA —, só se o nome lá estiver. É a ÚNICA definição da regra: usam-na o
// [RunAllowlistGate], o backstop de [Monitor.evaluate] e o Agent Runtime, que não constrói
// como efeito uma tool que o RM vai negar.
//
// A distinção nil/vazia é o que a regra tem de mais frágil: `len(allowed) == 0` no lugar de
// `allowed == nil` abria todas as tools a um nó do plano sem tools pinadas.
func RunAllowsTool(allowed []string, toolID string) bool {
	if allowed == nil {
		return true
	}
	for _, t := range allowed {
		if t == toolID {
			return true
		}
	}
	return false
}

// RunAllowlistGate é o hook que impõe a lista-branca do run ([Call.AllowedTools], AOS-413,
// ADR-027 §2.3) DENTRO do Reference Monitor (AOS-485).
//
// PORQUE É UM HOOK. Até ao AOS-485 o Agent Runtime negava a call antes de a entregar ao RM: a
// tool não corria, mas a recusa não deixava `tool.call.denied`, selo nem contador — só se
// reconstituía contando as tool calls pedidas num turno e reparando que faltava a mediação.
// Aqui a recusa sai por [Monitor.fail], como todas as outras.
//
// ONDE VAI NA CADEIA. Logo a seguir ao hook de identidade: o registo leva o principal que o
// token VERIFICADO resolveu, com a cadeia de delegação, e não o que o chamador declarou. E
// antes da revalidação, da política e do orçamento: uma call que vai ser negada pela lista
// não sela uma revalidação nem reserva orçamento.
//
// NÃO É A ÚNICA IMPOSIÇÃO. [Monitor.evaluate] tem um backstop com o mesmo código e o mesmo
// `denied_by`: uma cadeia montada sem este gate nega na mesma, só que mais tarde na cadeia.
// O gate decide a POSIÇÃO da recusa, não a sua existência.
//
// Não tem estado nem dependências: o valor-zero é utilizável.
type RunAllowlistGate struct{}

// NewRunAllowlistGate constrói o gate. Existe pela forma — a composição do nó escreve os
// hooks como `referencemonitor.NewX(...)`.
func NewRunAllowlistGate() RunAllowlistGate { return RunAllowlistGate{} }

// Name implementa [Hook].
func (RunAllowlistGate) Name() string { return RunAllowlistHookName }

// Evaluate implementa [Hook]: nega, com [CodeToolOutsideRunAllowlist], a call cuja tool não
// está na lista do run; deixa passar tudo o resto sem anotar nada.
func (RunAllowlistGate) Evaluate(_ context.Context, call *Call) (HookResult, error) {
	if RunAllowsTool(call.AllowedTools, call.ToolID) {
		return allow, nil
	}
	return HookResult{
		Decision: HookDeny,
		Code:     CodeToolOutsideRunAllowlist,
		Reason:   runAllowlistReason,
	}, nil
}
