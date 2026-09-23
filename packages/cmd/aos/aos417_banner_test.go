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

	// O ARGUMENTO PASSOU A SER DERIVADO (AOS-423), e este guard mudou de pergunta.
	//
	// Ate ao AOS-423 o terceiro argumento era `false` literal e este teste procurava o consumidor
	// pelo NOME DO STREAM dentro do `aos-orq`: «ninguem le uma fila sem a nomear». O consumidor
	// que se escreveu **nunca nomeia o stream** — fala HTTP com o no (`POST /plans/claim`), pela
	// via do ADR-030. A heuristica antiga teria ficado CEGA em silencio: um consumidor a existir,
	// e o banner a continuar a dizer que ninguem le a fila.
	//
	// E uma licao sobre detectores por PROXY: o proxy («nomear o stream») era razoavel e deixou
	// de o ser por uma mudanca que ninguem ligou a ele.
	if strings.Contains(src, "esMediationDurable, false)") {
		t.Error("o composition-root voltou a passar `false` LITERAL para a reclamacao da fila:\n" +
			"desde o AOS-423 a rota `POST /plans/claim` existe, e o banner tem de DERIVAR se ela\n" +
			"serve (gate soberano composto) em vez de afirmar que ninguem consome a fila")
	}
	const predicado = "reclamavelNoArranque := wormForChain != nil && (readAuthority != nil || readRegions != nil)"
	if !strings.Contains(src, predicado) {
		t.Errorf("o predicado do banner mudou de forma no bootstrap.go (esperava %q).\n"+
			"Ele TEM de espelhar a auto-derivacao do `readGov` em newAPI: WORM composto MAIS uma\n"+
			"fonte de autoridade board->regiao. Se divergirem, o banner declara uma postura de\n"+
			"reclamacao que a rota nao tem. Ver [filaReclamavel], a forma canonica da mesma regra.",
			predicado)
	}

	// E O CONSUMIDOR NAO PODE VOLTAR A NOMEAR O STREAM.
	//
	// Se o fizesse, seria um segundo caminho de consumo a ler o Event Store directamente — que e
	// exactamente o que o ADR-030 rejeitou: o `aos-orq` nao partilha substrato com o no (DEF-282),
	// e sobre ficheiro o Event Store nao arbitra entre processos.
	var nomeiaStream string
	raiz := filepath.Join("..", "aos-orq")
	_ = filepath.Walk(raiz, func(caminho string, info os.FileInfo, erro error) error {
		if erro != nil || info.IsDir() || !strings.HasSuffix(caminho, ".go") {
			return nil //nolint:nilerr // arvore ausente e estado valido, nao erro
		}
		b, e := os.ReadFile(caminho)
		if e != nil {
			return nil //nolint:nilerr // ficheiro ilegivel nao inventa um consumidor
		}
		if strings.Contains(string(b), planRequestStream) {
			nomeiaStream = caminho
		}
		return nil
	})
	if nomeiaStream != "" {
		t.Errorf("%s nomeia o stream da fila (%q).\n"+
			"O consumidor do AOS-423 fala HTTP com o no e NAO conhece o nome do stream, de\n"+
			"proposito. Um segundo caminho a ler o Event Store directamente e o que o ADR-030\n"+
			"rejeitou.", nomeiaStream, planRequestStream)
	}
}

// O QUE A LINHA DIZ TEM DE SER VERDADE. A função nunca era exercitada — podia ser código morto,
// ou afirmar o contrário do que o código faz, e nada avisava.
func TestAOS417BannerDeclaraAPosturaReal(t *testing.T) {
	casos := []struct {
		nome                string
		composto, duravel   bool
		reclamavel          bool
		exigeQueContenha    []string
		exigeQueNaoContenha []string
	}{
		{
			nome: "sem substrato", composto: false, duravel: false, reclamavel: false,
			// Sem store, a rota recusa TUDO: a linha tem de o dizer, e não pode anunciar uma
			// fila que não existe.
			exigeQueContenha:    []string{"SEM SUBSTRATO", "503"},
			exigeQueNaoContenha: []string{"ROTA ACTIVA"},
		},
		{
			nome: "composto, duravel, SEM gate soberano", composto: true, duravel: true, reclamavel: false,
			// O ingresso funciona mas NINGUÉM pode drenar: a reclamação recusa 501 sem gate
			// soberano composto. O operador tem de saber que o `201` não leva a lado nenhum, e
			// tem de saber PORQUÊ — senão vai procurar o consumidor em vez de compor o gate.
			exigeQueContenha:    []string{"ROTA ACTIVA", planRequestStream, "FILA NAO DRENAVEL", "501", "ADR-018"},
			exigeQueNaoContenha: []string{"SUBSTRATO VOLATIL", "FILA DRENAVEL ("},
		},
		{
			nome: "composto, volatil", composto: true, duravel: false, reclamavel: false,
			exigeQueContenha: []string{"ROTA ACTIVA", "SUBSTRATO VOLATIL"},
		},
		{
			nome: "composto, duravel, RECLAMAVEL", composto: true, duravel: true, reclamavel: true,
			// Com a fila drenável, a linha não pode dizer que ninguém a lê — mas também NÃO pode
			// afirmar que alguém a está a ler. O nó sabe que a rota serve; não sabe se há um
			// consumidor a chamá-la, e a diferença entre as duas é o que manda o operador
			// procurar no sítio certo quando a fila encher.
			exigeQueContenha:    []string{"ROTA ACTIVA", "FILA DRENAVEL", "nao prova que alguem a chama"},
			exigeQueNaoContenha: []string{"FILA NAO DRENAVEL", "NINGUEM le"},
		},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			linhas := planIngressPostureBanner(c.composto, c.duravel, c.reclamavel)
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
