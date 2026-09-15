package agentruntime

import "context"

// modelCallKey é a chave privada do anexo de correlação da chamada ao modelo (AOS-394).
type modelCallKey struct{}

// modelCall é o par (run, passo) de UMA chamada ao modelo.
type modelCall struct {
	runID  string
	stepID string
}

// ContextWithModelCall anexa ao ctx o run e o passo da chamada ao modelo que se vai fazer.
//
// O runtime escreve-o em [Runtime.callModel], imediatamente antes de [ModelClient.Call], com o
// MESMO stepID dos checkpoints, do span `chat` e do `turn.recorded`. Um [ModelClient] que tenha de
// ligar a chamada à trajectória — o adaptador do Model Gateway, para os selos de governação
// `modelgw-gov` — lê-o com [ModelCallFromContext].
//
// Viaja no ctx e não na [PromptView] de propósito: a porta [ModelClient] fica igual, e a vista que
// a captura e o replay tratam não ganha campos de correlação. É a mesma mecânica por-chamada com
// que o nó já faz chegar ao gateway a credencial do run (AOS-278).
func ContextWithModelCall(ctx context.Context, runID, stepID string) context.Context {
	return context.WithValue(ctx, modelCallKey{}, modelCall{runID: runID, stepID: stepID})
}

// ModelCallFromContext devolve o run e o passo anexados por [ContextWithModelCall], e se havia
// anexo. Sem anexo devolve ("", "", false): quem lê não inventa uma correlação que não recebeu.
//
// O par devolve-se JUNTO e o `ok` é sobre o PAR, não sobre cada campo. Quem tenha um valor de
// recurso (um run fixado na construção, por exemplo) só o deve usar quando `ok` é falso: misturar
// o run de uma fonte com o passo de outra produziria uma correlação que nunca existiu.
func ModelCallFromContext(ctx context.Context) (runID, stepID string, ok bool) {
	if c, achou := ctx.Value(modelCallKey{}).(modelCall); achou {
		return c.runID, c.stepID, true
	}
	return "", "", false
}
