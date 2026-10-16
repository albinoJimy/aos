package main

// AOS-530 (peça 3a) — A INSTRUÇÃO DA `ocr_read` É A QUE O AOS-529 MEDIU.
//
// A tool copia a instrução do banco de ensaio em vez de o importar: o banco é um módulo-folha que
// ninguém importa, e o nó não passa a depender dele. A cópia só vale enquanto for igual, e é isso
// que este teste prova: lê do CÓDIGO-FONTE do banco as duas constantes medidas (`InstrucaoDaVisao`
// e `PedidoDaVisao`, em `packages/qa/banco-ensaio/ocr_visao.go`), avalia os literais e compara o
// digest com o da tool. Mudar uma das cópias sem a outra parte o teste — e mudar a instrução muda
// a medição, o que pede uma nova corrida do banco e um registo assinado novo.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

const aos530FonteDoBanco = "../../qa/banco-ensaio/ocr_visao.go"

// aos530ConstanteDoBanco devolve o valor do literal de texto de uma constante do ficheiro do banco.
func aos530ConstanteDoBanco(t *testing.T, f *ast.File, nome string) string {
	t.Helper()
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			vs := s.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if n.Name != nome || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s no banco nao e um literal de texto: a comparacao deixou de ser possivel", nome)
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				return v
			}
		}
	}
	t.Fatalf("%s nao existe em %s", nome, aos530FonteDoBanco)
	return ""
}

func TestAOS530_OCR_AInstrucaoEADoBanco(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), aos530FonteDoBanco, nil, 0)
	if err != nil {
		t.Fatalf("ler o codigo do banco: %v", err)
	}
	sistema := aos530ConstanteDoBanco(t, f, "InstrucaoDaVisao")
	pedido := aos530ConstanteDoBanco(t, f, "PedidoDaVisao")
	if got, quer := ocrDigestDaInstrucao(ocrInstrucaoSistema, ocrInstrucaoPedido), ocrDigestDaInstrucao(sistema, pedido); got != quer {
		t.Fatalf("a instrucao da ocr_read divergiu da medida pelo AOS-529: digest %s, o do banco %s", got, quer)
	}
	// Controlo: o digest distingue uma mudança de um carácter em cada parte.
	if ocrDigestDaInstrucao(sistema+" ", pedido) == ocrDigestDaInstrucao(sistema, pedido) ||
		ocrDigestDaInstrucao(sistema, pedido+".") == ocrDigestDaInstrucao(sistema, pedido) {
		t.Fatal("o digest da instrucao nao distingue uma mudanca")
	}
}
