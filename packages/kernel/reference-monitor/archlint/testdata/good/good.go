// Caso BOM: código de consumidor externo que respeita o no-bypass. A única via
// de execução de uma tool é através de rm.Mediate — nenhuma tool é invocada
// directamente. O analisador NÃO deve sinalizar nada aqui.
package good

import "context"

// ToolFunc espelha a assinatura do RM (localmente, para o testdata parsear sem
// dependências). Aqui NUNCA é invocada directamente.
type ToolFunc func(ctx context.Context, input []byte) ([]byte, error)

// mediator representa o Reference Monitor injectado.
type mediator interface {
	Mediate(ctx context.Context, call any) (any, error)
}

// executarViaRM despacha a tool call pela superfície legítima.
func executarViaRM(ctx context.Context, rm mediator, call any) error {
	_, err := rm.Mediate(ctx, call)
	return err
}

// registar demonstra que declarar/passar uma ToolFunc é permitido; só invocá-la
// directamente é que seria violação (o que aqui não acontece).
func registar(t ToolFunc) ToolFunc {
	return t
}
