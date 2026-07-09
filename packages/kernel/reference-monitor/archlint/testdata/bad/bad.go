// Caso MAU: código de consumidor externo que CONTORNA o Reference Monitor,
// executando tools directamente sem passar por Mediate. O analisador DEVE
// sinalizar as duas violações abaixo.
package bad

import "context"

// ToolFunc espelha a assinatura do RM.
type ToolFunc func(ctx context.Context, input []byte) ([]byte, error)

// bypassPorInvocacaoDirecta invoca uma ToolFunc directamente — VIOLAÇÃO
// (tool-func-invocation): a tool executa sem mediação.
func bypassPorInvocacaoDirecta(ctx context.Context, tool ToolFunc, in []byte) ([]byte, error) {
	return tool(ctx, in) // want: invocacao directa de ToolFunc
}

// bypassPorDispatcher chama um dispatcher directo — VIOLAÇÃO
// (forbidden-dispatch): salta o gate de política.
func bypassPorDispatcher(ctx context.Context, in []byte) ([]byte, error) {
	return dispatchTool(ctx, in) // want: chamada directa a dispatcher
}

// dispatchTool simula um despacho directo proibido.
func dispatchTool(ctx context.Context, in []byte) ([]byte, error) {
	return in, nil
}
