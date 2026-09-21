package main

// aos417_banner_test.go — A LINHA DE POSTURA DO INGRESSO NÃO PODE SOBREVIVER À SUA PRÓPRIA VERDADE.
//
// O banner do ingresso afirma hoje que NINGUÉM consome a fila. É verdade, é importante — um
// `201` significa «o pedido está durável» e não «a corrida começou» — e é exactamente o tipo de
// afirmação que apodrece em silêncio: quando o trabalhador do `aos-orq` for escrito, ninguém
// passa por `bootstrap.go`, e o nó continuaria a anunciar no arranque uma coisa que deixou de
// ser verdade.
//
// O repositório já tinha decidido que isto se resolve com um teste, e não com um comentário: o
// `aos255_budget_scope_test.go` existe só para falhar quando o composition-root passa um literal
// a um banner de postura. Este ficheiro é a mesma ideia aplicada à fronteira que o AOS-417
// introduziu, com uma diferença que importa: o eixo do AOS-255 era um import DENTRO da árvore, e
// aqui o consumidor nasce noutro MÓDULO. O detector tem de olhar para lá.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// O LITERAL `false` DO CONSUMIDOR TEM DE MORRER QUANDO O CONSUMIDOR NASCER.
//
// Varre a árvore do `aos-orq` à procura de quem nomeie o stream da fila. Enquanto ninguém o
// nomear, o literal está correcto. No instante em que alguém o ler, este teste fica VERMELHO e
// obriga a corrigir a linha de arranque — que é precisamente o momento em que ela passaria a
// mentir.
func TestAOS417BannerDoConsumidorNaoApodrece(t *testing.T) {
	fonte, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatalf("ler bootstrap.go: %v", err)
	}
	src := string(fonte)

	// A linha tem de continuar a ser EMITIDA. Sem isto, apagar a chamada inteira passaria neste
	// teste (nenhum literal, nenhuma incoerência) e o operador ficaria sem declaração nenhuma
	// sobre o ingresso — o silêncio que o AOS-248 fechou para a autonomia.
	if !strings.Contains(src, "planIngressPostureBanner(") {
		t.Fatal("o composition-root deixou de chamar planIngressPostureBanner: o ingresso do " +
			"caminho do plano voltou a ser uma superficie SILENCIOSA no banner de arranque")
	}

	declaraSemConsumidor := strings.Contains(src, "esMediationDurable, false)")

	// Procura quem, no `aos-orq`, nomeia o stream da fila. É a evidência de que um consumidor
	// existe: ninguém lê uma fila sem a nomear.
	var consumidor string
	raiz := filepath.Join("..", "aos-orq")
	err = filepath.Walk(raiz, func(caminho string, info os.FileInfo, erro error) error {
		if erro != nil || info.IsDir() || !strings.HasSuffix(caminho, ".go") {
			// Um `aos-orq` ausente não é falha deste teste: devolve-se nil e o veredicto fica
			// «sem consumidor», que é o estado que o banner declara.
			return nil //nolint:nilerr // a ausência da árvore é um estado válido, não um erro
		}
		b, e := os.ReadFile(caminho)
		if e != nil {
			return nil //nolint:nilerr // idem: um ficheiro ilegível não inventa um consumidor
		}
		if strings.Contains(string(b), planRequestStream) {
			consumidor = caminho
		}
		return nil
	})
	if err != nil {
		t.Fatalf("varrer %s: %v", raiz, err)
	}

	if consumidor != "" && declaraSemConsumidor {
		t.Errorf("%s ja nomeia o stream da fila (%q), mas o composition-root continua a passar "+
			"`false` a planIngressPostureBanner:\n"+
			"o no anuncia no arranque que NINGUEM consome a fila quando ja alguem consome. "+
			"Corrija bootstrap.go — e, se o consumidor passou a ser detectavel de forma mais "+
			"directa, derive o argumento disso em vez do literal.", consumidor, planRequestStream)
	}
	if consumidor == "" && !declaraSemConsumidor {
		t.Error("o composition-root deixou de declarar `false` para o consumidor da fila, mas " +
			"ninguem no aos-orq nomeia o stream:\n" +
			"ou o argumento passou a ser derivado (bom, e entao actualize este teste), ou o " +
			"banner passou a afirmar um consumidor que nao existe (mau, e e a mentira mais " +
			"perigosa das duas).")
	}
}

// O QUE A LINHA DIZ TEM DE SER VERDADE. A função nunca era exercitada — podia ser código morto,
// ou afirmar o contrário do que o código faz, e nada avisava.
func TestAOS417BannerDeclaraAPosturaReal(t *testing.T) {
	casos := []struct {
		nome                string
		composto, duravel   bool
		temConsumidor       bool
		exigeQueContenha    []string
		exigeQueNaoContenha []string
	}{
		{
			nome: "sem substrato", composto: false, duravel: false, temConsumidor: false,
			// Sem store, a rota recusa TUDO: a linha tem de o dizer, e não pode anunciar uma
			// fila que não existe.
			exigeQueContenha:    []string{"SEM SUBSTRATO", "503"},
			exigeQueNaoContenha: []string{"ROTA ACTIVA"},
		},
		{
			nome: "composto, duravel, sem consumidor", composto: true, duravel: true, temConsumidor: false,
			// O estado de HOJE. As três afirmações que o operador precisa: a rota funciona, o
			// nó não corre o plano, e ninguém consome a fila.
			exigeQueContenha:    []string{"ROTA ACTIVA", planRequestStream, "SEM CONSUMIDOR", "ADR-018"},
			exigeQueNaoContenha: []string{"SUBSTRATO VOLATIL"},
		},
		{
			nome: "composto, volatil", composto: true, duravel: false, temConsumidor: false,
			exigeQueContenha: []string{"ROTA ACTIVA", "SUBSTRATO VOLATIL"},
		},
		{
			nome: "composto, duravel, com consumidor", composto: true, duravel: true, temConsumidor: true,
			// Quando o consumidor existir, a linha NÃO pode continuar a dizer que ninguém lê.
			exigeQueContenha:    []string{"ROTA ACTIVA"},
			exigeQueNaoContenha: []string{"SEM CONSUMIDOR", "NINGUEM"},
		},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			linhas := planIngressPostureBanner(c.composto, c.duravel, c.temConsumidor)
			if len(linhas) == 0 {
				t.Fatal("a postura do ingresso nunca pode ser silenciosa")
			}
			todo := strings.Join(linhas, " ")
			for _, exig := range c.exigeQueContenha {
				if !strings.Contains(todo, exig) {
					t.Errorf("a linha de postura devia conter %q:\n%s", exig, todo)
				}
			}
			for _, proib := range c.exigeQueNaoContenha {
				if strings.Contains(todo, proib) {
					t.Errorf("a linha de postura NAO devia conter %q:\n%s", proib, todo)
				}
			}
		})
	}
}
