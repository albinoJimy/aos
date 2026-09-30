package main

// AOS-462 — UM SENSOR PARA A CLASSE «GODOC SEQUESTRADO», e não para a instância.
//
// EM GO, um bloco de comentário imediatamente antes de uma declaração — sem linha em branco — **é** o
// godoc dessa declaração. Inserir uma função entre um comentário e o símbolo que ele documenta
// transfere o doc para o símbolo errado e deixa o original **sem documentação nenhuma**. Nada disto é
// sintacticamente errado, pelo que `gofmt`, `go vet` e `staticcheck` calam-se (o ST1020 só cobre
// identificadores exportados).
//
// ACONTECEU CINCO VEZES nesta sessão, sempre igual, sempre corrigido só na instância:
// `WithCompletedRetention` e `newReadGovernance` (AOS-456a), `WithMaxTrajectoryConns` e o doc de
// `handleTrajectory` (AOS-459), `WithControlRateLimit` (AOS-458, reposto no AOS-461) e —
// **no próprio commit que declarou que corrigir a instância e não a classe garantia uma quinta vez** —
// `ingressPostureBanner`, o maior documento de contrato do `ingress_env.go`, que passou a ser o godoc de
// um helper de quatro linhas.
//
// A REGRA DESTE SENSOR, e porque é esta. Uma varredura ingénua («a primeira palavra do doc não é o
// nome do símbolo») produz falsos positivos em massa: docs de GRUPO antes de blocos `var`/`const`
// («// Erros …»), asserções de interface (`var _ = …`, «// Assegura …») e prosa PT-PT que começa por
// maiúscula («// O …», «// PRODUTOR …»). O discriminante que separa o hijack real da prosa é **existir
// como símbolo declarado no pacote**: se a primeira palavra do doc nomeia OUTRA declaração deste
// pacote, o doc é dela e está no lugar errado.
//
// O IDENTIFICADOR BRANCO ESTÁ FORA, e é correcção do próprio sensor. Uma asserção de interface
// (`var _ Porta = (*tipo)(nil)`) declara o símbolo `_`, que não é documentável, e o comentário que a
// acompanha nomeia legitimamente o tipo afirmado — `dsar.go` tem um. Tratá-lo como sequestro seria o
// sensor a mandar corrigir código correcto, que é pior do que não vigiar: ensina a ignorá-lo.
//
// LIMITE DECLARADO: não apanha um doc cuja primeira palavra nomeie um símbolo de outro pacote, nem um
// que descreva o símbolo errado sem o nomear. Apanha a forma que custou cinco ocorrências.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ficheirosDoPacote parseia os `.go` deste directório, um a um.
//
// NÃO usa `parser.ParseDir`: está deprecado desde Go 1.25 (SA1019) e o gate `lint` recusa a descoberta
// — corretamente, e baseliná-la seria usar este ficheiro para abrir uma excepção a um gate. A
// alternativa recomendada (`golang.org/x/tools/go/packages`) traria uma dependência nova a um módulo que
// monta o grafo offline com `replace`, e o que este sensor precisa é da AST, não de tipos.
func ficheirosDoPacote(t *testing.T, fset *token.FileSet) map[string]*ast.File {
	t.Helper()
	entradas, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	out := map[string]*ast.File{}
	for _, e := range entradas {
		if e.IsDir() || filepath.Ext(e.Name()) != ".go" {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", e.Name(), err)
		}
		out[e.Name()] = f
	}
	if len(out) < 20 {
		t.Fatalf("so %d ficheiros .go parseados — a varredura deixou de cobrir o pacote", len(out))
	}
	return out
}

func TestAOS462NenhumGodocSequestrado(t *testing.T) {
	fset := token.NewFileSet()
	ficheiros := ficheirosDoPacote(t, fset)

	// PASSO 1 — o conjunto de símbolos declarados no pacote, testes incluídos: um doc pode ter sido
	// sequestrado de um símbolo cujo nome só aparece num ficheiro de teste.
	declarados := map[string]bool{}
	type alvo struct {
		doc *ast.CommentGroup
		sym string
		pos token.Pos
	}
	var alvos []alvo
	for nome, f := range ficheiros {
		eTeste := strings.HasSuffix(nome, "_test.go")
		for _, d := range f.Decls {
			for _, sym := range simbolosDe(d) {
				declarados[sym] = true
			}
			if eTeste {
				continue // o doc de um teste não é contrato; o SÍMBOLO conta, o doc não se vigia
			}
			if doc, sym := docESimboloDe(d); doc != nil && sym != "" {
				alvos = append(alvos, alvo{doc, sym, doc.Pos()})
			}
		}
	}
	if len(declarados) < 100 || len(alvos) < 100 {
		t.Fatalf("a varredura encontrou %d simbolos e %d docs — demasiado pouco para este pacote, "+
			"logo deixou de medir o que diz medir", len(declarados), len(alvos))
	}

	// PASSO 2 — um doc cuja primeira palavra nomeia OUTRA declaração do pacote está no símbolo errado.
	var achados []string
	for _, a := range alvos {
		primeira := primeiraPalavraDoDoc(a.doc)
		if primeira == "" || primeira == a.sym || !declarados[primeira] {
			continue
		}
		if a.sym == "_" {
			continue // asserção de interface: ver a nota no cabeçalho
		}
		achados = append(achados, fset.Position(a.pos).String()+": o doc de "+primeira+
			" esta no simbolo "+a.sym+" (que fica assim SEM doc)")
	}
	sort.Strings(achados)
	for _, a := range achados {
		t.Errorf("GODOC SEQUESTRADO — %s\n\tUma declaracao foi inserida entre este comentario e o "+
			"simbolo que ele documenta. Mova a declaracao para ANTES do bloco de comentario (uma linha "+
			"em branco NAO basta: deixaria o doc orfao). Ver o cabecalho deste ficheiro.", a)
	}
}

// simbolosDe devolve os nomes declarados por uma declaração de topo — todos, porque um `var`/`const`
// em bloco declara vários e qualquer deles pode ser o dono legítimo de um doc.
func simbolosDe(d ast.Decl) []string {
	switch v := d.(type) {
	case *ast.FuncDecl:
		return []string{v.Name.Name}
	case *ast.GenDecl:
		var out []string
		for _, sp := range v.Specs {
			switch s := sp.(type) {
			case *ast.TypeSpec:
				out = append(out, s.Name.Name)
			case *ast.ValueSpec:
				for _, n := range s.Names {
					out = append(out, n.Name)
				}
			}
		}
		return out
	}
	return nil
}

// docESimboloDe devolve o godoc de uma declaração e o símbolo a que ele está atribuído, e só quando há
// UM símbolo: um `var`/`const` em bloco tem legitimamente um doc de grupo, que não é sequestro.
func docESimboloDe(d ast.Decl) (*ast.CommentGroup, string) {
	switch v := d.(type) {
	case *ast.FuncDecl:
		return v.Doc, v.Name.Name
	case *ast.GenDecl:
		if v.Doc == nil || len(v.Specs) != 1 {
			return nil, ""
		}
		syms := simbolosDe(d)
		if len(syms) != 1 {
			return nil, ""
		}
		return v.Doc, syms[0]
	}
	return nil, ""
}

// primeiraPalavraDoDoc devolve a primeira palavra do comentário, sem pontuação de fim. Devolve vazio
// quando ela não tem forma de identificador Go — é o que mantém a prosa PT-PT fora do sensor.
func primeiraPalavraDoDoc(doc *ast.CommentGroup) string {
	if doc == nil || len(doc.List) == 0 {
		return ""
	}
	texto := strings.TrimSpace(strings.TrimPrefix(doc.List[0].Text, "//"))
	campos := strings.Fields(texto)
	if len(campos) == 0 {
		return ""
	}
	p := strings.TrimRight(campos[0], ",:;.—-")
	if p == "" {
		return ""
	}
	for i, r := range p {
		ok := r == '_' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(i > 0 && r >= '0' && r <= '9')
		if !ok {
			return ""
		}
	}
	return p
}
