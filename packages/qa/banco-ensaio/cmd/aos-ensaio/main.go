// Command aos-ensaio é o BANCO DE ENSAIO da fronteira runtime↔modelo (AOS-512).
//
// É uma ferramenta de medição, fora do nó: NÃO faz parte do binário `aos`, não vai na imagem de
// produção e não toca em produção. Corre uma bateria de casos sintéticos contra uma rota e
// escreve um relatório de taxas. Ver `packages/qa/banco-ensaio/README.md`.
package main

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"

	bancoensaio "github.com/aos-ref/qa/banco-ensaio"
)

func main() {
	// Ctrl-C pára a corrida entre dois runs: o relatório parcial é escrito e o proxy efémero
	// é desmontado.
	ctx, parar := signal.NotifyContext(context.Background(), os.Interrupt)
	amb := bancoensaio.Ambiente{Getenv: os.Getenv}
	if casa, err := os.UserHomeDir(); err == nil {
		amb.PastaDoDono = filepath.Join(casa, ".aos-ensaio")
	}
	codigo := bancoensaio.Executar(ctx, os.Args[1:], os.Stdout, os.Stderr, amb)
	parar()
	os.Exit(codigo)
}
