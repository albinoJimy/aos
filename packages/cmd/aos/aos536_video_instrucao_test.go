package main

// AOS-536 (parte 2) — A INSTRUÇÃO DA `video_read` É A QUE O AOS-537 MEDIU.
//
// Como a da `ocr_read` (aos530_ocr_instrucao_test.go): a tool copia a instrução do banco em vez de o
// importar, e este teste lê do CÓDIGO-FONTE do banco as duas constantes medidas (`InstrucaoDoVideo`
// e `PedidoDoVideo`, em `packages/qa/banco-ensaio/video_visao.go`), avalia os literais — a instrução
// do vídeo é uma concatenação de literais, avaliada aqui — e compara o digest com o da tool.

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

const aos536FonteDoBanco = "../../qa/banco-ensaio/video_visao.go"

// aos536Literal avalia uma expressão que é um literal de texto ou uma concatenação deles.
func aos536Literal(t *testing.T, e ast.Expr, nome string) string {
	t.Helper()
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			t.Fatalf("%s no banco tem um literal que nao e texto", nome)
		}
		v, err := strconv.Unquote(x.Value)
		if err != nil {
			t.Fatal(err)
		}
		return v
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			t.Fatalf("%s no banco tem um operador que nao e a concatenacao", nome)
		}
		return aos536Literal(t, x.X, nome) + aos536Literal(t, x.Y, nome)
	case *ast.ParenExpr:
		return aos536Literal(t, x.X, nome)
	}
	t.Fatalf("%s no banco nao e um literal de texto nem uma concatenacao deles: a comparacao deixou de ser possivel", nome)
	return ""
}

// aos536ConstanteDoBanco devolve o valor de uma constante de texto do ficheiro do banco.
func aos536ConstanteDoBanco(t *testing.T, f *ast.File, nome string) string {
	t.Helper()
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			vs := s.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if n.Name == nome && i < len(vs.Values) {
					return aos536Literal(t, vs.Values[i], nome)
				}
			}
		}
	}
	t.Fatalf("%s nao existe em %s", nome, aos536FonteDoBanco)
	return ""
}

func TestAOS536_Video_AInstrucaoEADoBanco(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), aos536FonteDoBanco, nil, 0)
	if err != nil {
		t.Fatalf("ler o codigo do banco: %v", err)
	}
	sistema := aos536ConstanteDoBanco(t, f, "InstrucaoDoVideo")
	pedido := aos536ConstanteDoBanco(t, f, "PedidoDoVideo")
	if got, quer := videoDigestDaInstrucao(videoInstrucaoSistema, videoInstrucaoPedido), videoDigestDaInstrucao(sistema, pedido); got != quer {
		t.Fatalf("a instrucao da video_read divergiu da medida pelo AOS-537: digest %s, o do banco %s", got, quer)
	}
	if versao := aos536ConstanteDoBanco(t, f, "VersaoDaInstrucaoDoVideo"); versao != "1.0.0" {
		t.Fatalf("a versao da instrucao do banco mudou para %s: a copia do no e a medicao tem de ser revistas", versao)
	}
	// Controlo: o digest distingue uma mudança de um carácter em cada parte.
	if videoDigestDaInstrucao(sistema+" ", pedido) == videoDigestDaInstrucao(sistema, pedido) ||
		videoDigestDaInstrucao(sistema, pedido+".") == videoDigestDaInstrucao(sistema, pedido) {
		t.Fatal("o digest da instrucao nao distingue uma mudanca")
	}
	// A FÓRMULA é a do banco: o corpo de `DigestDaInstrucaoDoVideo` soma a instrução, uma quebra de
	// linha e o pedido — a da tool. Assim o digest do resultado é o que o relatório `video` declara.
	var corpo strings.Builder
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "DigestDaInstrucaoDoVideo" {
			if err := printer.Fprint(&corpo, token.NewFileSet(), fn.Body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !strings.Contains(corpo.String(), `sha256.Sum256([]byte(InstrucaoDoVideo + "\n" + PedidoDoVideo))`) {
		t.Fatalf("a formula do digest do banco mudou; a da tool tem de a seguir:\n%s", corpo.String())
	}
}
