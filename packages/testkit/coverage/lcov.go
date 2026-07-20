// Package coverage converte coverprofiles do Go (o formato nativo de
// `go test -coverprofile`) num relatório MÁQUINA-LEGÍVEL LCOV, sem qualquer
// dependência externa (Go stdlib puro) — o cerne de AOS-109 AC1. O gate 3
// (scripts/ci/test.sh) invoca o binário cmd/cov2lcov para emitir coverage/lcov.info
// a partir dos perfis que já gera por módulo.
//
// # Formato de entrada (coverprofile)
//
// A primeira linha é "mode: set|count|atomic". Cada linha seguinte é:
//
//	name.go:startLine.startCol,endLine.endCol numStmts count
//
// onde `name.go` é o caminho qualificado pelo import path (ex.:
// github.com/aos-ref/kernel/reference-monitor/monitor.go). Um bloco cobre as linhas
// [startLine, endLine]; `count` é o número de execuções (0 = não coberto).
//
// # Formato de saída (LCOV)
//
// Por ficheiro:
//
//	SF:<ficheiro>
//	DA:<linha>,<count>
//	...
//	LF:<linhas instrumentadas>
//	LH:<linhas com count>0>
//	end_of_record
//
// Determinista: ficheiros ordenados alfabeticamente, linhas por ordem crescente, e
// o count de uma linha coberta por vários blocos é o MÁXIMO observado (uma linha
// conta como coberta se QUALQUER bloco que a inclui foi executado).
package coverage

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// block é um bloco de cobertura parseado de uma linha do coverprofile.
type block struct {
	file      string
	startLine int
	endLine   int
	count     int
}

// parseLine parseia uma linha de perfil (sem a linha "mode:"). Devolve ok=false
// para linhas vazias; devolve erro para linhas malformadas (fail-closed: um perfil
// corrompido não passa silenciosamente).
func parseLine(line string) (block, bool, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return block{}, false, nil
	}
	// Divide em "<file>:<pos>" <numStmts> <count>.
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return block{}, false, fmt.Errorf("linha malformada (esperava 3 campos): %q", line)
	}
	count, err := strconv.Atoi(fields[2])
	if err != nil {
		return block{}, false, fmt.Errorf("count invalido em %q: %w", line, err)
	}

	// fields[0] = <file>:<startLine>.<startCol>,<endLine>.<endCol>
	// O ficheiro pode conter ':' em teoria? Import paths não; mas a posição começa
	// no ÚLTIMO ':' seguido de dígitos. Recortamos no último ':'.
	colon := strings.LastIndex(fields[0], ":")
	if colon <= 0 {
		return block{}, false, fmt.Errorf("posicao sem ':' em %q", fields[0])
	}
	file := fields[0][:colon]
	pos := fields[0][colon+1:]

	comma := strings.IndexByte(pos, ',')
	if comma <= 0 {
		return block{}, false, fmt.Errorf("posicao sem ',' em %q", pos)
	}
	start := pos[:comma]
	end := pos[comma+1:]

	startLine, err := lineOf(start)
	if err != nil {
		return block{}, false, fmt.Errorf("startLine em %q: %w", line, err)
	}
	endLine, err := lineOf(end)
	if err != nil {
		return block{}, false, fmt.Errorf("endLine em %q: %w", line, err)
	}
	if endLine < startLine {
		return block{}, false, fmt.Errorf("endLine < startLine em %q", line)
	}
	return block{file: file, startLine: startLine, endLine: endLine, count: count}, true, nil
}

// lineOf extrai o número de linha de um "linha.coluna".
func lineOf(s string) (int, error) {
	dot := strings.IndexByte(s, '.')
	if dot <= 0 {
		return 0, fmt.Errorf("sem '.' em %q", s)
	}
	return strconv.Atoi(s[:dot])
}

// fileCov acumula a cobertura por-linha de um ficheiro (linha → count máximo).
type fileCov struct {
	lines map[int]int
}

// ConvertToLCOV lê um ou mais coverprofiles de `r` (concatenados; a linha "mode:"
// de cada um é ignorada) e escreve o relatório LCOV agregado em `w`. Determinista.
//
// Agregação: um count de linha é o MÁXIMO entre todos os blocos (e perfis) que a
// cobrem — replicando `go tool cover`, onde uma linha é coberta se algum bloco que
// a inclui executou.
func ConvertToLCOV(r io.Reader, w io.Writer) error {
	files := map[string]*fileCov{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(line), "mode:") {
			continue
		}
		b, ok, err := parseLine(line)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		fc := files[b.file]
		if fc == nil {
			fc = &fileCov{lines: map[int]int{}}
			files[b.file] = fc
		}
		for ln := b.startLine; ln <= b.endLine; ln++ {
			if cur, seen := fc.lines[ln]; !seen || b.count > cur {
				fc.lines[ln] = b.count
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return writeLCOV(files, w)
}

// writeLCOV emite o relatório determinista.
func writeLCOV(files map[string]*fileCov, w io.Writer) error {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	bw := bufio.NewWriter(w)
	for _, name := range names {
		fc := files[name]
		lineNums := make([]int, 0, len(fc.lines))
		for ln := range fc.lines {
			lineNums = append(lineNums, ln)
		}
		sort.Ints(lineNums)

		if _, err := fmt.Fprintf(bw, "TN:\nSF:%s\n", name); err != nil {
			return err
		}
		hit := 0
		for _, ln := range lineNums {
			count := fc.lines[ln]
			if count > 0 {
				hit++
			}
			if _, err := fmt.Fprintf(bw, "DA:%d,%d\n", ln, count); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(bw, "LF:%d\nLH:%d\nend_of_record\n", len(lineNums), hit); err != nil {
			return err
		}
	}
	return bw.Flush()
}
