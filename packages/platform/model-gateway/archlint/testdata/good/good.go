// Caso BOM: código de consumidor externo que respeita o no-bypass do GW. Fala
// SÓ com a porta do Model Gateway; não importa nenhum SDK de provedor nem
// referencia um endpoint de provider. O analisador NÃO deve sinalizar nada aqui.
package good

import (
	"context"
	"fmt"
)

// gateway espelha a porta do Model Gateway (localmente, para o testdata parsear
// sem dependências). É a ÚNICA via legítima de invocação de modelo.
type gateway interface {
	Chat(ctx context.Context, prompt string) (string, error)
}

// invocarViaGW despacha a chamada de modelo pela porta — sem tocar num provedor.
func invocarViaGW(ctx context.Context, gw gateway, prompt string) (string, error) {
	return gw.Chat(ctx, prompt)
}

// endpointInterno é um endpoint do próprio sistema, não de um provedor — não deve
// ser sinalizado.
const endpointInterno = "https://internal.aos.local/v1/health"

func log(s string) { fmt.Println(s) }
