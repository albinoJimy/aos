package main

import (
	"bytes"
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	broker "github.com/aos-ref/platform/broker"
)

// AOS-342 — O BANNER DESCREVIA COMO IMPOSTO UM EIXO QUE O CURINGA DEIXA ABERTO.
//
// A linha do eixo provider (AOS-332) derivava só da POSTURA, e a postura é função da
// NULIDADE do mapa de política: qualquer mapa não-nil é `enforced`. As três políticas
// abaixo são todas `enforced` e materialmente opostas —
//
//   - `{"payments": {"*"}}` não impõe NADA por conjunto;
//   - `{}` nega TUDO;
//   - `{"payments": {"stripe"}}` impõe.
//
// — e o banner descrevia as três com a mesma frase: «o provedor pedido tem de constar da
// autoridade efectiva da classe». Falsa na primeira, vácua na segunda. E é sobre essa
// frase que o operador decide se a pré-condição do `DEF-218` está satisfeita.

// TestAOS342_BannerDistingueAsFormasDoEnforced: as três formas de `enforced` produzem
// descrições DISTINTAS, e só a dos conjuntos concretos diz que a pré-condição está
// satisfeita.
func TestAOS342_BannerDistingueAsFormasDoEnforced(t *testing.T) {
	t.Parallel()
	casos := []struct {
		nome    string
		postura posturaDaPoliticaDoBroker
		quer    []string
		naoQuer []string
	}{
		{
			nome: "curinga: enforced no nome, aberto no efeito",
			postura: posturaDaPoliticaDoBroker{
				Composto: true, Provider: broker.ProviderPostureEnforced,
				FormaProvider: broker.ProviderPolicyShapeWildcard, ClassesComCuringa: []string{"payments"},
			},
			// A classe é NOMEADA: sem isso o operador sabe que há um buraco e não onde.
			quer: []string{"ABERTA POR CURINGA", "payments", "NAO SATISFAZ a pre-condicao do DEF-218"},
		},
		{
			nome: "declarada e vazia: deny-all",
			postura: posturaDaPoliticaDoBroker{
				Composto: true, Provider: broker.ProviderPostureEnforced,
				FormaProvider: broker.ProviderPolicyShapeEmpty,
			},
			quer:    []string{"VAZIA (deny-all)", "NENHUMA troca passa o eixo", "mapa vazio por acidente"},
			naoQuer: []string{"CURINGA"},
		},
		{
			nome: "conjuntos concretos: a unica forma que satisfaz o DEF-218",
			postura: posturaDaPoliticaDoBroker{
				Composto: true, Provider: broker.ProviderPostureEnforced,
				FormaProvider: broker.ProviderPolicyShapeByClass,
			},
			quer:    []string{"conjuntos CONCRETOS por classe", "unica forma que satisfaz a pre-condicao do DEF-218"},
			naoQuer: []string{"CURINGA", "deny-all", "NAO SATISFAZ"},
		},
		{
			nome: "sem politica declarada",
			postura: posturaDaPoliticaDoBroker{
				Composto: true, Provider: broker.ProviderPostureUnset,
				FormaProvider: broker.ProviderPolicyShapeNone,
			},
			quer:    []string{"politica NAO declarada", "nao e imposto por conjunto"},
			naoQuer: []string{"CURINGA", "deny-all"},
		},
	}

	linhas := make([]string, len(casos))
	for i, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			texto := strings.Join(brokerPolicyPostureBanner(tc.postura), "\n")
			for _, q := range tc.quer {
				if !strings.Contains(texto, q) {
					t.Errorf("banner nao contem %q:\n%s", q, texto)
				}
			}
			for _, n := range tc.naoQuer {
				if strings.Contains(texto, n) {
					t.Errorf("banner contem %q, que este estado nao pode afirmar:\n%s", n, texto)
				}
			}
			linhas[i] = texto
		})
	}

	// O controlo: se duas formas produzissem a mesma linha, cada caso passaria isolado e
	// o operador continuaria sem saber qual das políticas tem.
	for i := range linhas {
		for j := i + 1; j < len(linhas); j++ {
			if linhas[i] != "" && linhas[i] == linhas[j] {
				t.Fatalf("os estados %q e %q produzem a MESMA linha", casos[i].nome, casos[j].nome)
			}
		}
	}
}

// TestAOS342_ALinhaDoEixoProviderNomeiaAForma: a forma tem de aparecer no texto da
// rubrica e não só na descrição, para ser greppável no log de arranque com a mesma chave
// que o WORM usa.
func TestAOS342_ALinhaDoEixoProviderNomeiaAForma(t *testing.T) {
	t.Parallel()
	texto := strings.Join(brokerPolicyPostureBanner(posturaDaPoliticaDoBroker{
		Composto: true, Provider: broker.ProviderPostureEnforced,
		FormaProvider: broker.ProviderPolicyShapeWildcard, ClassesComCuringa: []string{"payments"},
	}), "\n")
	if !strings.Contains(texto, "enforced/wildcard") {
		t.Errorf("a rubrica nao nomeia o par postura/forma:\n%s", texto)
	}
}

// TestAOS342_BannerSaiNoArranqueReal prova que a linha continua a sair do `Bootstrap`.
// Hoje o nó não compõe broker nenhum, pelo que o ramo impresso é o NÃO-APLICÁVEL — e é
// isso que tem de ser verdade no arranque real.
func TestAOS342_BannerSaiNoArranqueReal(t *testing.T) {
	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}

	var banner bytes.Buffer
	node, err := Bootstrap(context.Background(), cfg, &banner)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()

	if !strings.Contains(banner.String(), "politica do broker (AOS-324/AOS-330/AOS-331): NAO-APLICAVEL") {
		t.Fatal("o arranque nao imprimiu a linha da politica do broker")
	}
}

// TestBoundary_BrokerNaoEComposto AMARRA a afirmação que o banner imprime ao operador:
// «o no NAO compoe o platform/broker (broker.New nao tem chamador de producao)».
//
// Sem esta guarda é um literal que ninguém mantém verdadeiro, e falha nos DOIS sentidos:
// o wiring do DEF-218 compõe o broker e esquece a linha, e o banner passa a dizer ao
// operador que não há troca mediada quando há — com a pré-condição do DEF-218 a ser
// assertada contra uma declaração falsa; ou o `Composto` do call-site fica a falso sobre
// um nó que compõe, e a linha declara NÃO-APLICABILIDADE sobre uma política em vigor.
//
// ÂMBITO, declarado: varre o código de PRODUÇÃO deste pacote — o composition-root do nó,
// o único sítio onde a composição do broker faria sentido. Não prova nada sobre outros
// módulos: `packages/security-tests` chama `broker.New` e é um teste.
func TestBoundary_BrokerNaoEComposto(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir do pacote do nó: %v", err)
	}
	fset := token.NewFileSet()
	verificados := 0
	for _, e := range entries {
		nome := e.Name()
		if e.IsDir() || !strings.HasSuffix(nome, ".go") || strings.HasSuffix(nome, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, nome, nil, 0)
		if err != nil {
			t.Fatalf("parse de %q: %v", nome, err)
		}
		alias := aliasDoImport(f, "github.com/aos-ref/platform/broker")
		if alias == "" {
			continue
		}
		verificados++
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "New" {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok || id.Name != alias {
				return true
			}
			t.Errorf("%s:%d: %s.New composto em codigo de PRODUCAO. A linha "+
				"`brokerPolicyPostureBanner` (NAO-APLICAVEL) passa a MENTIR: o campo "+
				"`Composto` do call-site em bootstrap.go TEM de passar a derivar do broker "+
				"real, e a postura e a FORMA com ele — ver DEF-218 e AOS-342.",
				nome, fset.Position(call.Pos()).Line, alias)
			return false
		})
	}
	if verificados == 0 {
		t.Fatal("nenhum ficheiro de producao importa platform/broker: a guarda deixou de guardar algo")
	}
}

// aliasDoImport devolve o nome sob o qual o ficheiro se refere a um caminho de import
// ("" se não o importa). Um import com alias explícito usa-o; sem alias, o último
// segmento do caminho.
func aliasDoImport(f *ast.File, caminho string) string {
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, `"`) != caminho {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		partes := strings.Split(caminho, "/")
		return partes[len(partes)-1]
	}
	return ""
}
