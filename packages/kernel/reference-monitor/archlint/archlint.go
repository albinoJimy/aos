// Package archlint é o analisador de arquitectura do no-bypass do Reference
// Monitor (AOS-003): detecta despacho DIRECTO de tools fora do RM, isto é,
// qualquer caminho de código que execute uma tool sem passar por
// referencemonitor.Monitor.Mediate.
//
// Corre como TESTE Go (ver archlint_test.go) — falha se houver violação, logo
// fica activo via `go test`. É stdlib puro (go/ast, go/parser, go/token):
// zero dependências externas, análise offline.
//
// # Natureza (defesa-em-profundidade, NÃO prova)
//
// Este analisador é uma HEURÍSTICA sintáctica (por nome/tipo textual), não uma
// prova de no-bypass. A garantia FORTE de no-bypass é ESTRUTURAL, não vem daqui:
// o permit não-forjável (campo não-exportado, uso único) e o dispatcher
// não-exportado tornam impossível a um consumidor executar uma tool sem passar
// por [referencemonitor.Monitor.Mediate] (ver decision.go / monitor.go). O lint
// apanha os enganos ÓBVIOS (um dispatcher directo, uma invocação de um valor
// literalmente tipado ToolFunc), mas é contornável por renomeação: um tipo com
// outra designação ou um dispatcher com outro nome não é detectado. Para robustez
// real seria preciso análise com type-info (go/types, identidade de tipo em vez
// de nome) — fora do escopo AOS-003; aqui a heurística é a segunda camada, a
// estrutura é a primeira.
//
// # Regra
//
// Fora do pacote reference-monitor, é violação:
//  1. invocar directamente um valor de tipo ToolFunc (a assinatura de tool do
//     RM), ex.: `tool(ctx, input)` onde `tool` foi declarado como ToolFunc; ou
//  2. chamar uma função/método cujo nome pertence ao conjunto reservado do
//     dispatcher interno (dispatch/dispatchTool/runTool/executeTool/invokeTool).
//
// A via legítima — `rm.Mediate(ctx, call)` — nunca é sinalizada.
//
// # Escopo de varrimento
//
// [AnalyzeDir] é NÃO-recursivo e o teste corre-o só sobre o próprio RM e os
// testdata (em AOS-003 ainda não há consumidores do RM). Quando o Agent Runtime
// (AOS-012) e outros consumidores existirem, o teste deve ser estendido para
// percorrer recursivamente os packages consumidores, mantendo o RM isento.
package archlint

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

// forbiddenCallNames são nomes de função/método que representam despacho
// directo de tools (o dispatcher interno do RM e sinónimos). Chamá-los fora do
// RM contorna a mediação.
var forbiddenCallNames = map[string]struct{}{
	"dispatch":     {},
	"dispatchTool": {},
	"runTool":      {},
	"executeTool":  {},
	"invokeTool":   {},
}

// toolFuncTypeName é o nome do tipo cuja invocação directa é proibida fora do RM.
const toolFuncTypeName = "ToolFunc"

// Violation descreve uma infracção da regra de no-bypass.
type Violation struct {
	File    string
	Line    int
	Col     int
	Kind    string // "tool-func-invocation" | "forbidden-dispatch"
	Message string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s:%d:%d: %s (%s)", v.File, v.Line, v.Col, v.Message, v.Kind)
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

// analyzeFile aplica a regra a um ficheiro já parseado. A detecção de invocação
// directa de ToolFunc é feita por ÂMBITO DE FUNÇÃO (para não confundir um `fn`
// de uma função com um `fn` homónimo de outra), acrescida dos identificadores
// ToolFunc de nível de pacote. As chamadas a nomes reservados de dispatcher são
// detectadas em todo o ficheiro (independentes de âmbito).
func analyzeFile(fset *token.FileSet, path string, file *ast.File) []Violation {
	pkgTool := collectPackageToolFuncIdents(file)

	var violations []Violation
	inspectCalls := func(root ast.Node, toolIdents map[string]struct{}) {
		ast.Inspect(root, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				if _, bad := forbiddenCallNames[fn.Name]; bad {
					violations = append(violations, mkViolation(fset, path, call.Pos(), "forbidden-dispatch",
						fmt.Sprintf("chamada directa a %q contorna o Reference Monitor", fn.Name)))
				} else if _, isTool := toolIdents[fn.Name]; isTool {
					violations = append(violations, mkViolation(fset, path, call.Pos(), "tool-func-invocation",
						fmt.Sprintf("invocacao directa de ToolFunc %q fora do RM; use Mediate", fn.Name)))
				}
			case *ast.SelectorExpr:
				if _, bad := forbiddenCallNames[fn.Sel.Name]; bad {
					violations = append(violations, mkViolation(fset, path, call.Pos(), "forbidden-dispatch",
						fmt.Sprintf("chamada directa a %q contorna o Reference Monitor", fn.Sel.Name)))
				}
			}
			return true
		})
	}

	// Uma passagem por cada função, com o conjunto de idents ToolFunc do seu
	// âmbito (parâmetros + locais) unido ao de nível de pacote.
	seenFuncBody := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		seenFuncBody = true
		scope := mergeSets(pkgTool, collectScopeToolFuncIdents(fn.Type, fn.Body))
		inspectCalls(fn.Body, scope)
	}
	// Se não há funções com corpo, ainda assim varremos o ficheiro para nomes
	// reservados de dispatcher a nível de pacote.
	if !seenFuncBody {
		inspectCalls(file, pkgTool)
	}
	return violations
}

func isToolFuncType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == toolFuncTypeName
	case *ast.SelectorExpr:
		return t.Sel.Name == toolFuncTypeName
	}
	return false
}

// collectPackageToolFuncIdents devolve os identificadores de nível de pacote
// cujo tipo é ToolFunc (var x ToolFunc no topo do ficheiro).
func collectPackageToolFuncIdents(file *ast.File) map[string]struct{} {
	idents := make(map[string]struct{})
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			collectFromValueSpec(vs, idents)
		}
	}
	return idents
}

// collectScopeToolFuncIdents devolve os identificadores ToolFunc do âmbito de
// uma função: parâmetros e declarações locais (var e conversões em `:=`).
func collectScopeToolFuncIdents(ft *ast.FuncType, body *ast.BlockStmt) map[string]struct{} {
	idents := make(map[string]struct{})
	if ft != nil && ft.Params != nil {
		for _, field := range ft.Params.List {
			if isToolFuncType(field.Type) {
				for _, name := range field.Names {
					idents[name.Name] = struct{}{}
				}
			}
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ValueSpec:
			collectFromValueSpec(node, idents)
		case *ast.AssignStmt:
			for i, rhs := range node.Rhs {
				if isToolFuncConversion(rhs) && i < len(node.Lhs) {
					if id, ok := node.Lhs[i].(*ast.Ident); ok {
						idents[id.Name] = struct{}{}
					}
				}
			}
		case *ast.FuncLit:
			// parâmetros ToolFunc de closures locais.
			if node.Type != nil && node.Type.Params != nil {
				for _, field := range node.Type.Params.List {
					if isToolFuncType(field.Type) {
						for _, name := range field.Names {
							idents[name.Name] = struct{}{}
						}
					}
				}
			}
		}
		return true
	})
	return idents
}

// collectFromValueSpec extrai idents ToolFunc de `var x ToolFunc` e
// `var x = ToolFunc(...)`.
func collectFromValueSpec(vs *ast.ValueSpec, idents map[string]struct{}) {
	if vs.Type != nil && isToolFuncType(vs.Type) {
		for _, name := range vs.Names {
			idents[name.Name] = struct{}{}
		}
	}
	for i, val := range vs.Values {
		if isToolFuncConversion(val) && i < len(vs.Names) {
			idents[vs.Names[i].Name] = struct{}{}
		}
	}
}

func mergeSets(a, b map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		out[k] = struct{}{}
	}
	for k := range b {
		out[k] = struct{}{}
	}
	return out
}

// isToolFuncConversion reconhece uma conversão ToolFunc(...) ou pkg.ToolFunc(...).
func isToolFuncConversion(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name == toolFuncTypeName
	case *ast.SelectorExpr:
		return fn.Sel.Name == toolFuncTypeName
	}
	return false
}

func mkViolation(fset *token.FileSet, path string, pos token.Pos, kind, msg string) Violation {
	p := fset.Position(pos)
	return Violation{File: path, Line: p.Line, Col: p.Column, Kind: kind, Message: msg}
}

// AnalyzeTree analisa RECURSIVAMENTE todos os pacotes sob `root`, e existe porque
// [AnalyzeDir] não desce: durante muito tempo a única imposição desta regra correu sobre UM
// directório — o do próprio Reference Monitor — enquanto a propriedade afirmada («as tools só
// entram pelo caminho mediado») é do SISTEMA INTEIRO.
//
// Uma auditoria ao inventário de conceitos em 2026-08-19 apanhou a discrepância: o que se
// afirmava era mais largo do que o que se verificava. A regra não estava errada; estava a olhar
// para um canto.
//
// Salta `.git`, `vendor`, `node_modules` e `testdata` — o último de propósito: os ficheiros de
// caso do próprio analisador VIOLAM a regra por construção, e contá-los faria o gate acusar-se a
// si mesmo.
func AnalyzeTree(root string) ([]Violation, error) {
	var out []Violation
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		switch d.Name() {
		case ".git", "vendor", "node_modules", "testdata":
			return filepath.SkipDir
		}
		vs, aerr := AnalyzeDir(p)
		if aerr != nil {
			return aerr
		}
		out = append(out, vs...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}
