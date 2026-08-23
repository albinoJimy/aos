package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"
)

// anunciaSubcomando casa SÓ as linhas do usage com exactamente dois espaços de indentação e o
// prefixo `aos-issuer ` — as que anunciam um comando.
var anunciaSubcomando = regexp.MustCompile(`^  aos-issuer ([a-z][a-z-]*)`)

// ---------------------------------------------------------------------------------------------
// TODO O SUBCOMANDO QUE O DISPATCHER ACEITA TEM DE ESTAR DECLARADO — TAMBÉM NESTE BINÁRIO.
//
// Achado da verificação de completude de 2026-08-23. O `cmd/aos` tem esta guarda desde o nono
// caso do mesmo padrão, e ela lê `cli.go` por CAMINHO RELATIVO — logo cobre o seu pacote e mais
// nada. O binário irmão repetiu o defeito sem que nada o dissesse:
//
//	dispatcher   7 case
//	usage        4 linhas
//
// Faltavam `delegation-nonce`, `autonomy-sign` e `worm-seal`. O pior era o `autonomy-sign`: não
// aparecia em NENHUM ficheiro do repositório fora da própria fonte — nem no README do nó, nem no
// do servidor. E é ele que produz o corpo assinado do `POST /autonomy`, a cerimónia que a
// varredura de 2026-08-21 usou como exemplo de porta bem guardada.
//
// Uma cerimónia cuja ferramenta canónica é indescobrível empurra o operador para reimplementar o
// tuplo assinado à mão — que é a divergência que o `autonomysign.go` diz existir para evitar.
// ---------------------------------------------------------------------------------------------

// casesDoDispatcher extrai, do AST de `main.go`, os literais de todos os `case` do switch sobre
// `args[0]` dentro de [run].
func casesDoDispatcher(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse de main.go: %v", err)
	}
	var nomes []string
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "run" {
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

func TestTodoSubcomandoDoIssuerEstaDeclarado(t *testing.T) {
	nomes := casesDoDispatcher(t)

	// CONTROLO DO PRÓPRIO TESTE: se o parser não encontrar nada, tudo o que vem a seguir passa
	// por vacuidade. Um teste que lê a fonte tem de provar primeiro que a leu.
	if len(nomes) < 5 {
		t.Fatalf("o parser so encontrou %d case(s) em run (%v) — a leitura da fonte falhou e as "+
			"asercoes seguintes seriam vacuas", len(nomes), nomes)
	}

	for _, nome := range nomes {
		if !strings.Contains(usage, "aos-issuer "+nome+" ") {
			t.Errorf("o subcomando %q existe no dispatcher e NAO aparece no usage — funciona e "+
				"ninguem o descobre", nome)
		}
	}
}

// TestOUsageDoIssuerNaoPrometeOQueNaoExiste é a direcção INVERSA, e é a que apanha um subcomando
// removido.
//
// Sem ela, apagar um `case` e esquecer a linha do usage deixaria a ajuda a prometer um comando
// que responde «subcomando desconhecido» — pior do que não o listar, porque quem a lê confia.
func TestOUsageDoIssuerNaoPrometeOQueNaoExiste(t *testing.T) {
	nomes := map[string]bool{}
	for _, n := range casesDoDispatcher(t) {
		nomes[n] = true
	}
	if len(nomes) < 5 {
		t.Fatalf("o parser so encontrou %d case(s) — leitura da fonte falhou", len(nomes))
	}

	// CONTADOR, e não é decoração: no `cmd/aos` o teste gémeo esteve COMPLETAMENTE VACUOSO
	// porque a expressão regular não casava com linha nenhuma, e o controlo que existia — «o
	// parser encontrou N case» — passava na mesma. UM TESTE QUE ITERA TEM DE PROVAR QUE ITEROU.
	var examinadas int
	for _, linha := range strings.Split(usage, "\n") {
		m := anunciaSubcomando.FindStringSubmatch(linha)
		if m == nil {
			continue
		}
		examinadas++
		if !nomes[m[1]] {
			t.Errorf("o usage anuncia %q e o dispatcher NAO o aceita — a ajuda promete um comando "+
				"que responde «subcomando desconhecido», e quem a le confia", m[1])
		}
	}
	if examinadas < len(nomes) {
		t.Fatalf("o ciclo so examinou %d linha(s) para %d subcomando(s) — o filtro esta a saltar "+
			"linhas e as asercoes acima nao correram sobre todas", examinadas, len(nomes))
	}
}
