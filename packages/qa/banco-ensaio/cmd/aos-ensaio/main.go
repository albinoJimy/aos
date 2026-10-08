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
	"syscall"

	bancoensaio "github.com/aos-ref/qa/banco-ensaio"
)

func main() {
	// Ctrl-C e SIGTERM param a corrida entre dois runs: o relatório parcial é escrito e o proxy
	// efémero é desmontado. No Windows, o fecho da janela da consola, o fim de sessão e o
	// encerramento chegam ao processo como SIGTERM — mas o sistema só lhe dá uns segundos, e a
	// limpeza pode não acabar. Para esse caso (e para uma morte à força) o proxy tem um vigia
	// que o mata sozinho quando deixa de receber o sinal de vida do banco.
	ctx, parar := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	amb := bancoensaio.Ambiente{Getenv: os.Getenv}
	if casa, err := os.UserHomeDir(); err == nil {
		amb.PastaDoDono = filepath.Join(casa, ".aos-ensaio")
	}
	codigo := bancoensaio.Executar(ctx, os.Args[1:], os.Stdout, os.Stderr, amb)
	parar()
	os.Exit(codigo)
}
