package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------------------------
// UM PARSER FAIL-CLOSED QUE NINGUÉM OUVE É UM PARSER DECORATIVO.
//
// Achado colateral da verificação de completude de 2026-08-23. Ao fechar o
// `AOS_APPROVAL_SWEEP_INTERVAL`, a mutação de CABLAGEM — o arranque a ignorar o erro com
// `algo, _ := xFromEnv()` — NÃO caía. E não caía para NENHUMA das cadências: os testes das irmãs
// (retenção, SLOs) exercitam a função `*FromEnv` e nunca provam que o `serveAPI` propaga o erro.
//
// A lacuna é anterior a esta correcção e vale para todas, por isso a guarda também vale.
//
// LÊ A FONTE, e é deliberado. A alternativa seria arrancar o servidor num teste — que liga porta,
// compõe o nó inteiro e transforma um invariante de três linhas num teste de integração frágil.
// É o mesmo molde do `cli_subcomandos_test.go`, e pela mesma razão: uma tabela que o `serveAPI`
// consultasse seria mais bonita e teria o defeito noutro sítio, porque nada garantiria que o
// código a usava.
// ---------------------------------------------------------------------------------------------

// chamadasFromEnvEmServeAPI devolve, do AST de `main.go`, os nomes das funções `*FromEnv`
// invocadas dentro de [serveAPI] e se o ERRO de cada uma é descartado (`_`).
func chamadasFromEnvEmServeAPI(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse de main.go: %v", err)
	}
	descartado := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "serveAPI" {
			return true
		}
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			as, ok := m.(*ast.AssignStmt)
			if !ok || len(as.Rhs) != 1 {
				return true
			}
			call, ok := as.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || !strings.HasSuffix(id.Name, "FromEnv") {
				return true
			}
			// O erro é o ÚLTIMO valor de retorno.
			ultimo := as.Lhs[len(as.Lhs)-1]
			if b, ok := ultimo.(*ast.Ident); ok && b.Name == "_" {
				descartado[id.Name] = true
			} else if _, existe := descartado[id.Name]; !existe {
				descartado[id.Name] = false
			}
			return true
		})
		return false
	})
	return descartado
}

func TestNenhumaCadenciaTemOErroDESCARTADO(t *testing.T) {
	chamadas := chamadasFromEnvEmServeAPI(t)

	// CONTROLO DO PRÓPRIO TESTE: se o parser não encontrar nada, tudo o que vem a seguir passa
	// por vacuidade. Um teste que lê a fonte tem de provar primeiro que a leu.
	if len(chamadas) < 3 {
		t.Fatalf("o parser so encontrou %d chamada(s) *FromEnv em serveAPI (%v) — a leitura da "+
			"fonte falhou e as asercoes seguintes seriam vacuas", len(chamadas), chamadas)
	}

	for nome, foiDescartado := range chamadas {
		if foiDescartado {
			t.Errorf("o erro de %s() e DESCARTADO em serveAPI — o parser e fail-closed e ninguem o "+
				"ouve: um operador que pede uma cadencia e fica com outra nao tem como o notar", nome)
		}
	}
}
