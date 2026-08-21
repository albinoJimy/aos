package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"
)

// anunciaSubcomando casa SO as linhas do usage com exactamente dois espacos de indentacao — as
// que anunciam um comando. As continuacoes de descricao ficam alinhadas muito mais a direita.
var anunciaSubcomando = regexp.MustCompile("^  [a-z][a-z-]*")

// ---------------------------------------------------------------------------------------------
// TODO O SUBCOMANDO QUE O DISPATCHER ACEITA TEM DE ESTAR DECLARADO — E VICE-VERSA.
//
// COMO APARECEU, e é o nono caso do mesmo padrão. Acrescentei o `wal-summary`, escrevi o `case`
// no dispatcher, escrevi os testes, corri as mutações — e o subcomando ficou INVISÍVEL: não
// aparecia no `printUsage` nem na lista da mensagem de «subcomando desconhecido». Um comando que
// funciona e que ninguém descobre é quase o mesmo que não existir.
//
// A disciplina de «acrescentar nos três sítios» já falhou uma vez à primeira tentativa, comigo a
// prestar atenção. Não é uma disciplina que se possa confiar à memória de quem vier a seguir.
//
// ESTE TESTE LÊ O PRÓPRIO `cli.go` e extrai os `case` do switch do dispatcher. Não é elegante, e
// é deliberado: a alternativa — uma tabela de subcomandos que o switch e o usage consultassem —
// seria mais bonita e teria o mesmo defeito noutro sítio, porque nada garantiria que o `case`
// usava a tabela. Ler a fonte é a única forma de a verificação não depender da mesma convenção
// que existe para verificar.
// ---------------------------------------------------------------------------------------------

// casesDoDispatcher extrai, do AST de `cli.go`, os literais de todos os `case` do switch sobre
// `args[0]` dentro de [dispatch].
func casesDoDispatcher(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "cli.go", nil, 0)
	if err != nil {
		t.Fatalf("parse de cli.go: %v", err)
	}
	var nomes []string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "dispatch" {
			return true
		}
		ast.Inspect(fn.Body, func(m ast.Node) bool {
			cl, ok := m.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cl.List {
				if bl, ok := e.(*ast.BasicLit); ok && bl.Kind == token.STRING {
					nomes = append(nomes, strings.Trim(bl.Value, `"`))
				}
			}
			return true
		})
		return false
	})
	return nomes
}

func TestTodoSubcomandoEstaDeclarado(t *testing.T) {
	nomes := casesDoDispatcher(t)

	// CONTROLO DO PRÓPRIO TESTE: se o parser não encontrar nada, tudo o que vem a seguir passa
	// por vacuidade. Um teste que lê a fonte tem de provar primeiro que a leu.
	if len(nomes) < 8 {
		t.Fatalf("o parser so encontrou %d case(s) em dispatch (%v) — a leitura da fonte falhou "+
			"e as asercoes seguintes seriam vacuas", len(nomes), nomes)
	}

	var usage bytes.Buffer
	printUsage(&usage)
	textoUsage := usage.String()

	// A mensagem de «subcomando desconhecido» também lista os subcomandos, e envelhece pela
	// mesma razão.
	var lixo bytes.Buffer
	err := dispatch([]string{"isto-nao-existe"}, &lixo)
	if err == nil {
		t.Fatal("um subcomando inexistente devia dar erro")
	}
	textoErro := err.Error()

	// Os aliases de ajuda não precisam de constar da própria ajuda.
	aliases := map[string]bool{"-h": true, "--help": true}

	for _, nome := range nomes {
		if aliases[nome] {
			continue
		}
		if !strings.Contains(textoUsage, nome) {
			t.Errorf("o subcomando %q existe no dispatcher e NAO aparece no printUsage — funciona "+
				"e ninguem o descobre", nome)
		}
		if !strings.Contains(textoErro, nome) {
			t.Errorf("o subcomando %q existe no dispatcher e NAO aparece na lista da mensagem de "+
				"«subcomando desconhecido» — quem se enganar a escrever nao e encaminhado para ele", nome)
		}
	}
}

// TestOUsageNaoPrometeOQueNaoExiste é a direcção INVERSA, e é a que apanha um subcomando removido.
//
// Sem ela, apagar um `case` e esquecer a linha do usage deixaria a ajuda a prometer um comando
// que responde «subcomando desconhecido» — pior do que não o listar, porque quem o lê confia.
func TestOUsageNaoPrometeOQueNaoExiste(t *testing.T) {
	nomes := map[string]bool{}
	for _, n := range casesDoDispatcher(t) {
		nomes[n] = true
	}
	if len(nomes) < 8 {
		t.Fatalf("o parser so encontrou %d case(s) — leitura da fonte falhou", len(nomes))
	}

	var usage bytes.Buffer
	printUsage(&usage)

	// CONTADOR, e nao e decoracao: este teste ESTEVE COMPLETAMENTE VACUOSO e nada o denunciava.
	// A expressao regular tinha um byte de BACKSPACE no fim (um escape que uma substituicao minha
	// converteu em caracter literal), pelo que nao casava com linha nenhuma e o corpo do ciclo
	// NUNCA corria. O controlo que existia — «o parser encontrou >= 8 case» — passava, e o teste
	// parecia saudavel.
	//
	// So a mutacao «o usage promete um comando inexistente» o revelou. UM TESTE QUE ITERA TEM DE
	// PROVAR QUE ITEROU.
	var examinadas int

	// Cada linha do usage que comece por um identificador em minúsculas anuncia um subcomando.
	for _, linha := range strings.Split(usage.String(), "\n") {
		campos := strings.Fields(linha)
		if len(campos) == 0 {
			continue
		}
		cand := campos[0]
		// SO as linhas com EXACTAMENTE dois espacos de indentacao anunciam um subcomando. As
		// continuacoes de descricao sao alinhadas muito mais a direita, e a primeira versao deste
		// teste apanhava-as como se fossem comandos — acusava «(responde» e «quantos;» de nao
		// existirem. Um teste que le texto formatado tem de respeitar o formato.
		if !anunciaSubcomando.MatchString(linha) || strings.HasSuffix(cand, ":") {
			continue
		}
		examinadas++
		if !nomes[cand] {
			t.Errorf("o usage anuncia %q e o dispatcher NAO o aceita — a ajuda promete um comando "+
				"que responde «subcomando desconhecido», e quem a le confia", cand)
		}
	}
	if examinadas < len(nomes)-2 {
		t.Fatalf("o ciclo so examinou %d linha(s) para %d subcomando(s) — o filtro esta a saltar "+
			"quase tudo e as asercoes acima nao correram", examinadas, len(nomes))
	}
}
