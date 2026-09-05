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

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	broker "github.com/aos-ref/platform/broker"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-332 — O BANNER DECLARA O EIXO PROVIDER, E DERIVA-O DO BROKER COMPOSTO.
//
// O AOS-324 fechou o eixo por configuração e tornou a postura auditável selando-a em
// cada troca. Mas um nó que ainda não emitiu troca nenhuma era indistinguível nas duas
// posturas — e a postura por omissão é justamente a que NÃO impõe o eixo por conjunto.
// O `DEF-218` exige assertar `enforced` como PRÉ-CONDIÇÃO do wiring; sem esta linha,
// essa asserção só era observável DEPOIS da primeira troca bem-sucedida.
//
// O argumento da função é o `*broker.Broker` COMPOSTO, e não um bool nem uma postura
// solta: assim não há como escrever "ENFORCED" no banner sem que alguém o tenha
// realmente imposto. É a mesma disciplina que o `materialPrivadoDoNo` impôs ao banner
// do credential broker depois de ele ter mentido.

// brokerCompostoParaBanner constrói um broker REAL com a política pedida. É o que torna
// os estados do banner não-forjáveis: a linha vem de `Broker.ProviderPosture()` e de
// `Broker.ProviderPolicyShape()`.
func brokerCompostoParaBanner(t *testing.T, classProviders map[string][]string) *broker.Broker {
	t.Helper()
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	opts := []broker.Option{broker.WithClassScopes(map[string][]string{"payments": {"cap:pay.charge"}})}
	if classProviders != nil {
		opts = append(opts, broker.WithClassProviders(classProviders))
	}
	b, err := broker.New(referencemonitor.New(), broker.NewMemoryVault(), es, opts...)
	if err != nil {
		t.Fatalf("broker.New: %v", err)
	}
	return b
}

// TestAOS332_BannerDoEixoProvider_CincoEstadosDistintos exercita a função pura em cada
// estado que o nó pode ter, com o controlo de distinguibilidade que torna o resto
// não-vácuo — se dois estados produzissem a mesma linha, cada caso passaria isolado e o
// operador continuaria sem saber qual tem.
//
// SÃO CINCO, E NÃO TRÊS, POR ACHADO DE REVISÃO ADVERSARIAL. A postura é função da
// NULIDADE do mapa de política: `{"payments": {"*"}}` é `enforced` e não impõe nada por
// conjunto; um mapa declarado e vazio é `enforced` e nega tudo. Um banner que dissesse
// "ENFORCED" nos dois declararia fechado o eixo que o AOS-324 abriu, e a pré-condição do
// DEF-218 passaria sobre exactamente esse estado.
func TestAOS332_BannerDoEixoProvider_CincoEstadosDistintos(t *testing.T) {
	// O ESTADO DECLARADO é o token logo a seguir à rubrica, e é sobre ele que se
	// assere — não sobre a palavra em qualquer sítio da linha. A distinção não é
	// pedantismo: a linha do estado NÃO COMPOSTO nomeia `ENFORCED` no REMÉDIO («tem
	// de passar a dizer ENFORCED antes de qualquer credencial ser trocada»), e um
	// teste que proibisse a palavra proibiria o remédio. Um banner que declara o
	// estado e cala o remédio é metade da disciplina deste ficheiro.
	const rubrica = "eixo provider da troca (AOS-324/AOS-332, EPIC-07): "
	casos := []struct {
		nome    string
		broker  func(*testing.T) *broker.Broker
		estado  string
		quer    []string
		naoQuer []string
	}{
		{
			nome:   "nao composto (o estado de hoje)",
			broker: func(*testing.T) *broker.Broker { return nil },
			estado: "NAO COMPOSTO",
			quer:   []string{"broker.New nao tem chamador de producao", "DEF-218"},
			// A distinção que o ticket existe para fazer: sem broker, a postura NÃO
			// é `unset` — `unset` descreveria um broker a operar sem política.
			naoQuer: []string{rubrica + "UNSET", rubrica + "ENFORCED"},
		},
		{
			nome:    "composto SEM politica de provedores",
			broker:  func(t *testing.T) *broker.Broker { return brokerCompostoParaBanner(t, nil) },
			estado:  "UNSET",
			quer:    []string{"NAO e imposto por conjunto", "confusao de deputado", "DEF-218"},
			naoQuer: []string{rubrica + "NAO COMPOSTO", rubrica + "ENFORCED"},
		},
		{
			nome: "composto com CURINGA: enforced no nome, aberto no efeito",
			broker: func(t *testing.T) *broker.Broker {
				return brokerCompostoParaBanner(t, map[string][]string{"payments": {"*"}, "billing": {"stripe"}})
			},
			estado: "ENFORCED MAS ABERTO POR CURINGA",
			// A classe com curinga é NOMEADA: sem isso o operador saberia que há um
			// buraco e não onde.
			quer: []string{"payments", "NAO impoe nada por conjunto", "NAO SATISFAZ a pre-condicao do DEF-218"},
			// E a classe que TEM conjunto concreto não pode ser dada como aberta.
			naoQuer: []string{"as classes payments, billing"},
		},
		{
			nome: "composto com politica DECLARADA E VAZIA: deny-all",
			broker: func(t *testing.T) *broker.Broker {
				return brokerCompostoParaBanner(t, map[string][]string{})
			},
			estado: "ENFORCED MAS VAZIO (DENY-ALL)",
			quer:   []string{"NENHUMA troca passa o eixo provider", "mapa vazio por acidente"},
		},
		{
			nome: "composto com conjuntos CONCRETOS",
			broker: func(t *testing.T) *broker.Broker {
				return brokerCompostoParaBanner(t, map[string][]string{"payments": {"stripe"}})
			},
			estado:  "ENFORCED",
			quer:    []string{"conjuntos CONCRETOS por classe", "autoridade efectiva do principal", "provider_policy"},
			naoQuer: []string{"CURINGA", "DENY-ALL", rubrica + "NAO COMPOSTO"},
		},
	}

	linhas := make([]string, len(casos))
	for i, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			texto := strings.Join(provedorPostureBanner(tc.broker(t)), "\n")
			if quer := rubrica + tc.estado + " —"; !strings.Contains(texto, quer) {
				t.Errorf("estado declarado errado: esperado %q em:\n%s", quer, texto)
			}
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

	for i := range linhas {
		for j := i + 1; j < len(linhas); j++ {
			if linhas[i] != "" && linhas[i] == linhas[j] {
				t.Fatalf("os estados %q e %q produzem a MESMA linha", casos[i].nome, casos[j].nome)
			}
		}
	}
}

// TestAOS332_BannerDoEixoProviderSaiNoArranqueReal prova que a função pura é REALMENTE
// chamada pelo `Bootstrap` — sem isto, uma linha correcta que ninguém imprime passaria
// todos os testes acima e o operador continuaria a ler um arranque calado sobre o eixo.
//
// Cobre APENAS o estado de hoje, e não por comodidade: o call-site passa `nil` literal
// porque o nó não compõe broker nenhum (ver `TestBoundary_BrokerNaoEComposto`). Não há
// caminho de configuração por onde injectar um — e criar um só para o teste seria criar
// exactamente o seam que a revisão adversarial rejeitou, porque permitiria ao banner
// declarar a postura de um broker que o nó não usa.
func TestAOS332_BannerDoEixoProviderSaiNoArranqueReal(t *testing.T) {
	cfg := tnBaseConfig()
	cfg.Model = &countingModel{}

	var banner bytes.Buffer
	node, err := Bootstrap(context.Background(), cfg, &banner)
	if err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}
	defer func() { _ = node.Close() }()

	const quer = "eixo provider da troca (AOS-324/AOS-332, EPIC-07): NAO COMPOSTO"
	if !strings.Contains(banner.String(), quer) {
		t.Fatalf("o arranque nao imprimiu a linha do eixo provider (%q)", quer)
	}
}

// TestBoundary_BrokerNaoEComposto AMARRA a afirmação que as linhas de banner imprimem
// ao operador: «este no NAO compoe o platform/broker» e «broker.New nao tem chamador de
// producao». Sem esta guarda, as duas linhas são literais que ninguém mantém verdadeiros
// — e falham nos DOIS sentidos:
//
//   - o wiring do DEF-218 compõe o broker e esquece as linhas ⇒ o banner passa a dizer
//     ao operador que não há troca mediada quando há, e a pré-condição do DEF-218 é
//     assertada contra uma declaração falsa;
//   - as duas linhas divergem entre si ⇒ o mesmo banner diz "AUSENTE" e "ENFORCED" com
//     uma linha de intervalo, que é o defeito F14 que `posture_banner.go` existe para
//     impedir.
//
// ÂMBITO, declarado: varre o código de PRODUÇÃO deste pacote (o composition-root do nó),
// que é o único sítio onde a composição do broker faria sentido. Não prova nada sobre
// outros módulos — `packages/security-tests` chama `broker.New` e é um teste.
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
		// O alias sob o qual ESTE ficheiro importa o broker (pode não o importar).
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
			t.Errorf("%s:%d: %s.New composto em codigo de PRODUCAO. As linhas de banner "+
				"`provedorPostureBanner` (NAO COMPOSTO) e `credentialBrokerPostureBanner` "+
				"(AUSENTE) passam a MENTIR e TEM de ser actualizadas na MESMA alteracao, "+
				"junto com este teste — ver DEF-218.", nome, fset.Position(call.Pos()).Line, alias)
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
