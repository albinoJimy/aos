// Package separation é o analisador de arquitectura da SEPARAÇÃO do Agent Runtime
// (AOS-021): detecta um EFEITO EXTERNO (I/O de ficheiro, rede, processo) escrito
// DIRECTAMENTE na lógica determinística do loop — isto é, fora de uma activity.
//
// A invariante que protege: "a lógica determinística do loop não contém efeitos
// externos fora de activities" (critério de aceitação de AOS-021). Todo o efeito
// tem de ser encapsulado numa [activity.Activity] e despachado por
// [activity.Dispatcher.Dispatch] — que o medeia pelo Reference Monitor e o memoriza
// no ledger. Um `http.Get`, um `os.Open` ou um `exec.Command` no meio do loop
// contorna esse contrato (efeito não-mediado, não-registado, não-reproduzível em
// replay) — e é isso que este lint apanha.
//
// Corre como TESTE Go (ver separation_test.go): falha se houver violação, ficando
// activo via `go test`. É stdlib puro (go/ast, go/parser, go/token): zero
// dependências externas, análise offline.
//
// # Natureza (defesa-em-profundidade, NÃO prova)
//
// Como o archlint de AOS-003, este é uma HEURÍSTICA sintáctica (por pacote/função,
// via textual), não uma prova. A garantia FORTE — que um efeito só corre sob permit
// do RM — é ESTRUTURAL: o [activity.Dispatcher] nunca detém uma função de efeito
// directamente invocável; o efeito é uma tool registada no RM e a única via é
// Mediate (ver activity/doc.go). O lint apanha o engano ÓBVIO (chamar directamente
// uma primitiva de I/O no loop) mas é contornável por indirecção; a robustez real
// exigiria type-info (go/types). Aqui a heurística é a segunda camada.
//
// # Limite EXACTO do que o lint apanha (AOS021-Q3)
//
// Casa APENAS a forma sintáctica trivial `pkgIdent.Fn(...)` — um selector cujo lado
// esquerdo é o IDENT do import tal como aparece na chamada — contra um conjunto fixo.
// NÃO apanha (e o testdata/evasion documenta-o com testes que asseveram 0 violações):
//   - import ALIASADO: `import h "net/http"; h.Get(url)` (o ident é "h", não "http");
//   - método sobre VALOR de cliente: `client.Do(req)` ou `(&http.Client{}).Do(req)`
//     (o selector é sobre um valor, não sobre o ident do pacote) — a forma idiomática
//     REAL do efeito HTTP em Go;
//   - VALOR de função: `f := os.Open; f(p)`.
//
// Estas evasões NÃO são um buraco de segurança: a garantia é ESTRUTURAL (o Dispatcher
// não expõe efeito invocável), sendo este lint apenas a segunda camada contra o engano
// óbvio. Fechar as evasões exigiria go/types (resolver o pacote real do selector).
//
// # Regra
//
// Fora dos ficheiros de activity, é violação chamar uma função de EFEITO EXTERNO de
// um pacote de I/O conhecido (net/http, os, os/exec, net, io/ioutil, database/sql),
// p.ex. `http.Get(...)`, `os.OpenFile(...)`, `exec.Command(...)`. A via legítima —
// construir uma [activity.Activity] e chamar `dispatcher.Dispatch(ctx, act)` — nunca
// é sinalizada (Dispatch não pertence ao conjunto de efeitos externos).
package separation

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// externalEffectCalls é o conjunto RESERVADO de chamadas `pkg.Fn` que representam um
// efeito externo directo. A chave é "pkgIdent.FnName" (o nome do import tal como
// aparece na chamada). Não pretende ser exaustivo — cobre as primitivas mais comuns
// de I/O de ficheiro, rede e processo que um loop nunca deve invocar directamente.
var externalEffectCalls = map[string]struct{}{
	// net/http — rede (o efeito externo por excelência). Só funções DE PACOTE que
	// fazem I/O: http.NewRequest (constrói o pedido, sem I/O) e o método
	// (*http.Client).Do (selector sobre VALOR, não sobre o ident do pacote) NÃO estão
	// aqui — o primeiro seria falso positivo, o segundo é inalcançável por esta forma
	// sintáctica (ver limite documentado no cabeçalho do pacote).
	"http.Get": {}, "http.Post": {}, "http.PostForm": {}, "http.Head": {},
	// os — I/O de ficheiro e processo.
	"os.Open": {}, "os.OpenFile": {}, "os.Create": {}, "os.Remove": {},
	"os.RemoveAll": {}, "os.ReadFile": {}, "os.WriteFile": {}, "os.Mkdir": {},
	"os.MkdirAll": {}, "os.Rename": {}, "os.StartProcess": {},
	// os/exec — execução de processos.
	"exec.Command": {}, "exec.CommandContext": {}, "exec.LookPath": {},
	// net — sockets crus.
	"net.Dial": {}, "net.DialTimeout": {}, "net.Listen": {},
	// io/ioutil — legado de I/O de ficheiro.
	"ioutil.ReadFile": {}, "ioutil.WriteFile": {}, "ioutil.ReadAll": {},
	// database/sql — I/O de base de dados.
	"sql.Open": {},
}

// Violation descreve um efeito externo escrito fora de uma activity.
type Violation struct {
	File    string
	Line    int
	Col     int
	Call    string // ex.: "http.Get"
	Message string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s:%d:%d: %s (%s)", v.File, v.Line, v.Col, v.Message, v.Call)
}

// AnalyzeDir analisa todos os ficheiros .go (não-recursivo) do directório dado e
// devolve as violações encontradas, ordenadas por posição. Ficheiros de teste
// (_test.go) são ignorados.
func AnalyzeDir(dir string) ([]Violation, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var violations []Violation
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		violations = append(violations, analyzeFile(fset, path, file)...)
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})
	return violations, nil
}

// AnalyzeTree analisa RECURSIVAMENTE a árvore enraizada em root (AOS021-Q4), para que
// a varredura da lógica determinística do núcleo alcance TODOS os subpacotes (durable,
// saga, state, liveness, replay, …) e não só o pacote raiz — alinhando a cobertura do
// guard com a alegação "a lógica do loop não contém efeitos externos fora de
// activities". Salta sempre directórios chamados "testdata" (fixtures do próprio lint,
// que CONTÊM efeitos de propósito) e ficheiros _test.go; skipDirs acrescenta nomes de
// directório a ignorar (p.ex. "separation" — o próprio analisador, que faz I/O de
// ficheiro por construção). As violações vêm ordenadas por posição.
func AnalyzeTree(root string, skipDirs ...string) ([]Violation, error) {
	skip := map[string]struct{}{"testdata": {}}
	for _, s := range skipDirs {
		skip[s] = struct{}{}
	}
	fset := token.NewFileSet()
	var violations []Violation
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if _, skipped := skip[d.Name()]; skipped {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		file, perr := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		violations = append(violations, analyzeFile(fset, path, file)...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})
	return violations, nil
}

// analyzeFile aplica a regra a um ficheiro já parseado: qualquer chamada
// `pkg.Fn` do conjunto reservado de efeitos externos é uma violação.
func analyzeFile(fset *token.FileSet, path string, file *ast.File) []Violation {
	var violations []Violation
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		qualified := pkgIdent.Name + "." + sel.Sel.Name
		if _, bad := externalEffectCalls[qualified]; bad {
			p := fset.Position(call.Pos())
			violations = append(violations, Violation{
				File: path, Line: p.Line, Col: p.Column, Call: qualified,
				Message: fmt.Sprintf("efeito externo directo %q fora de activity; encapsule numa activity.Activity e despache via Dispatcher.Dispatch", qualified),
			})
		}
		return true
	})
	return violations
}
