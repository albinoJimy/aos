package agentruntime

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// AOS-492 — a regra de terminação do run vive em [TurnEndsRun] e em mais lado nenhum.

// TestAOS492_TurnEndsRun_TabelaNosDoisLayouts fixa a regra tal como estava escrita no loop e no
// motor de replay antes da extracção — `resp.Final || len(resp.ToolCalls) == 0` —, nos dois
// layouts que o assembler monta. O ticket não muda comportamento: a tabela é a regra antiga.
func TestAOS492_TurnEndsRun_TabelaNosDoisLayouts(t *testing.T) {
	t.Parallel()
	umaCall := []ToolInvocation{{ToolID: "t"}}
	casos := []struct {
		nome string
		resp ModelResponse
		quer bool
	}{
		{"final sem tool calls", ModelResponse{Final: true}, true},
		{"final com tool calls", ModelResponse{Final: true, ToolCalls: umaCall}, true},
		{"nao final sem tool calls", ModelResponse{}, true},
		{"nao final com tool calls", ModelResponse{ToolCalls: umaCall}, false},
		{"slice vazio nao-nil conta como sem tool calls", ModelResponse{ToolCalls: []ToolInvocation{}}, true},
	}
	for _, versao := range SupportedAssemblyVersions() {
		for _, c := range casos {
			got, err := TurnEndsRun(c.resp, versao)
			if err != nil {
				t.Fatalf("layout %s, %s: erro inesperado: %v", versao, c.nome, err)
			}
			if got != c.quer {
				t.Fatalf("layout %s, %s: TurnEndsRun = %v, quero %v", versao, c.nome, got, c.quer)
			}
		}
	}
}

// TestAOS492_TurnEndsRun_LayoutDesconhecidoEErro: uma versão que o assembler não monta — a
// vazia incluída — não é decidida por omissão.
func TestAOS492_TurnEndsRun_LayoutDesconhecidoEErro(t *testing.T) {
	t.Parallel()
	for _, versao := range []string{"", "9.9.9"} {
		termina, err := TurnEndsRun(ModelResponse{Final: true}, versao)
		if !errors.Is(err, ErrUnknownAssemblyVersion) {
			t.Fatalf("layout %q: erro = %v, quero ErrUnknownAssemblyVersion", versao, err)
		}
		if termina {
			t.Fatalf("layout %q: devolveu termina=true com erro", versao)
		}
	}
}

// TestAOS492_ARegraDeTerminacaoNaoEstaEscritaAMao é o teste ESTRUTURAL: lê o código do loop e
// do motor de replay e falha se a condição de terminação voltar a aparecer escrita localmente
// num dos dois, ou se um deles deixar de chamar [TurnEndsRun].
//
// O QUE CONTA COMO «ESCRITA À MÃO». A regra tem dois ingredientes, e qualquer um deles numa
// decisão destes ficheiros é a cópia a renascer:
//
//   - o campo `Final` de alguma coisa lido numa CONDIÇÃO — operando de `||`, `&&` ou `!`, ou a
//     condição inteira de um `if`/`for`/`switch`. Copiá-lo para outro registo
//     (`Final: resp.Final`, no `turn.recorded`) não é decidir, e é permitido;
//   - `len(x.ToolCalls)` comparado com o literal 0 ou 1, por qualquer operador (`== 0`, `> 0`,
//     `< 1`, …). Comparar com outro comprimento (`len(a.ToolCalls) > len(b.ToolResults)`, a
//     admissão do replay) é outra pergunta, e é permitido.
func TestAOS492_ARegraDeTerminacaoNaoEstaEscritaAMao(t *testing.T) {
	t.Parallel()
	for _, caminho := range []string{"loop.go", "replay/engine.go"} {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, caminho, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", caminho, err)
		}
		chama := false
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				if nomeDaFuncao(x.Fun) == "TurnEndsRun" {
					chama = true
				}
			case *ast.BinaryExpr:
				switch x.Op {
				case token.LOR, token.LAND:
					for _, lado := range []ast.Expr{x.X, x.Y} {
						if leCampoFinal(lado) {
							t.Errorf("%s: o campo Final entra numa condicao (%s) — a regra de terminacao e a de TurnEndsRun", fset.Position(lado.Pos()), x.Op)
						}
					}
				case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
					if (lenDeToolCalls(x.X) && zeroOuUm(x.Y)) || (lenDeToolCalls(x.Y) && zeroOuUm(x.X)) {
						t.Errorf("%s: len(...ToolCalls) comparado com um literal — a regra de terminacao e a de TurnEndsRun", fset.Position(x.Pos()))
					}
				}
			case *ast.UnaryExpr:
				if x.Op == token.NOT && leCampoFinal(x.X) {
					t.Errorf("%s: !…Final — a regra de terminacao e a de TurnEndsRun", fset.Position(x.Pos()))
				}
			case *ast.IfStmt:
				if leCampoFinal(x.Cond) {
					t.Errorf("%s: if …Final — a regra de terminacao e a de TurnEndsRun", fset.Position(x.Cond.Pos()))
				}
			case *ast.ForStmt:
				if x.Cond != nil && leCampoFinal(x.Cond) {
					t.Errorf("%s: for …Final — a regra de terminacao e a de TurnEndsRun", fset.Position(x.Cond.Pos()))
				}
			case *ast.SwitchStmt:
				if x.Tag != nil && leCampoFinal(x.Tag) {
					t.Errorf("%s: switch …Final — a regra de terminacao e a de TurnEndsRun", fset.Position(x.Tag.Pos()))
				}
			case *ast.CaseClause:
				for _, e := range x.List {
					if leCampoFinal(e) {
						t.Errorf("%s: case …Final — a regra de terminacao e a de TurnEndsRun", fset.Position(e.Pos()))
					}
				}
			}
			return true
		})
		if !chama {
			t.Errorf("%s: nao chama TurnEndsRun — a decisao de terminacao tem de passar pela funcao unica", caminho)
		}
	}
}

// nomeDaFuncao devolve o nome chamado: `F(...)` ou `pkg.F(...)`.
func nomeDaFuncao(e ast.Expr) string {
	switch f := e.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// leCampoFinal: a expressão é `x.Final` (com ou sem parênteses).
func leCampoFinal(e ast.Expr) bool {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			break
		}
		e = p.X
	}
	s, ok := e.(*ast.SelectorExpr)
	return ok && s.Sel.Name == "Final"
}

// lenDeToolCalls: a expressão é `len(x.ToolCalls)`.
func lenDeToolCalls(e ast.Expr) bool {
	c, ok := e.(*ast.CallExpr)
	if !ok || len(c.Args) != 1 {
		return false
	}
	if id, ok := c.Fun.(*ast.Ident); !ok || id.Name != "len" {
		return false
	}
	s, ok := c.Args[0].(*ast.SelectorExpr)
	return ok && s.Sel.Name == "ToolCalls"
}

// zeroOuUm: o literal inteiro 0 ou 1.
func zeroOuUm(e ast.Expr) bool {
	l, ok := e.(*ast.BasicLit)
	return ok && l.Kind == token.INT && (l.Value == "0" || l.Value == "1")
}
