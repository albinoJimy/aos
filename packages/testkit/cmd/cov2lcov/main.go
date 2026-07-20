// Command cov2lcov converte coverprofiles do Go em LCOV (relatório de cobertura
// MÁQUINA-LEGÍVEL) sem dependências externas — o conversor de AOS-109 AC1.
//
// Uso:
//
//	cov2lcov <perfil1.out> [perfil2.out ...] > coverage/lcov.info
//	go test ... -coverprofile=p.out && cov2lcov p.out > lcov.info
//	cat *.out | cov2lcov -            # lê de stdin
//
// Vários perfis são agregados (a mesma linha coberta por vários módulos conta o
// count máximo). Determinista: ficheiros ordenados, linhas crescentes. Fail-closed:
// um perfil malformado aborta com exit != 0 e mensagem em stderr.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aos-ref/testkit/coverage"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "cov2lcov:", err)
		os.Exit(1)
	}
}

// run agrega os perfis dados (ou stdin com "-"/sem args) e emite LCOV em out.
func run(args []string, stdin io.Reader, out io.Writer) error {
	readers, closers, err := openInputs(args, stdin)
	if err != nil {
		return err
	}
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()
	return coverage.ConvertToLCOV(io.MultiReader(readers...), out)
}

// openInputs resolve os argumentos em readers. Sem args (ou "-") lê de stdin.
func openInputs(args []string, stdin io.Reader) ([]io.Reader, []io.Closer, error) {
	if len(args) == 0 || (len(args) == 1 && args[0] == "-") {
		return []io.Reader{stdin}, nil, nil
	}
	var readers []io.Reader
	var closers []io.Closer
	for _, path := range args {
		f, err := os.Open(path)
		if err != nil {
			for _, c := range closers {
				_ = c.Close()
			}
			return nil, nil, err
		}
		readers = append(readers, f)
		// Separador de newline entre perfis (evita colar a última linha de um perfil
		// com a linha "mode:" do seguinte).
		readers = append(readers, strings.NewReader("\n"))
		closers = append(closers, f)
	}
	return readers, closers, nil
}
