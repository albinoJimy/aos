package main

// AOS-255 — A DECLARAÇÃO DE ALCANCE DO ORÇAMENTO (tool-only, token-only).
//
// O ticket é de TEXTO, mas o texto é a única coisa que separa um orçamento honesto de uma
// CAPACIDADE-FANTASMA: o hook de orçamento vive na cadeia do Reference Monitor, que só é
// atravessada por tool call, enquanto o turno de modelo é invocado directamente
// (`kernel/agent-runtime/loop.go`, `rt.model.Call`). Ligar o hook sem o dizer produziria um
// banner a anunciar "orçamento" a quem inferiria — legitimamente — que o gasto do agente passou
// a ter tecto, quando a LINHA DE CUSTO DOMINANTE ficou de fora.
//
// Por isso o texto é gateado, e em três eixos:
//
//  1. a FRASE aprovada ([BudgetScopeDeclaration]) está nos DOIS estados do banner e no README do
//     operador e no relatório de prontidão (os dois critérios de aceitação do ticket);
//  2. nenhum dos estados usa uma formulação TRANQUILIZADORA que a composição não sustenta;
//  3. [TestAOS255CallSiteMatchesComposition] — o argumento do banner continua a dizer a verdade.
//     Depois de AOS-256/257 o argumento passou a DERIVAR do estado real (`runBudget != nil`),
//     pelo que o que o gate protege hoje é o simétrico: nenhum LITERAL volta ao call-site (nem
//     `false`, que negaria por escrito um orçamento composto, nem `true`, que anunciaria um
//     tecto que o wiring pode já não ter) e a linha CONTINUA a ser emitida.

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// budgetScopeDeclarationAccented é a MESMA frase de [BudgetScopeDeclaration] na forma acentuada
// — a que vive nos documentos (o banner é ASCII, a prosa do repo não é). Duplicar a frase nas
// duas formas é deliberado: o gate compara TEXTO, e uma normalização automática esconderia uma
// reescrita que mudasse o sentido.
const budgetScopeDeclarationAccented = "orçamento: cobre tool calls em TOKENS; o gasto de inferência é travado por tempo (wall-clock), não por tecto"

// budgetProntidaoReport é o relatório de prontidão, o segundo documento nomeado pelo critério de
// aceitação de AOS-255 (a §7 é onde o plano de billing token-only está escrito).
const budgetProntidaoReport = "../../../docs/reports/prontidao-modelos-agenticos.md"

// budgetOverclaimPhrases são formulações que fariam a linha prometer COBERTURA TOTAL do gasto —
// exactamente a leitura errada que a declaração existe para impedir. Nenhuma pode aparecer em
// nenhum dos estados do banner enquanto o turno de modelo estiver fora da cadeia. Estão em
// minúsculas e sem acentos porque o banner também está.
var budgetOverclaimPhrases = []string{
	"todo o gasto",
	"todo o custo",
	"gasto total",
	"custo total",
	"cobre a inferencia",
	"cobre o turno de modelo",
	"tecto global",
}

// TestAOS255DeclaracaoDeAlcanceNosDoisEstados prova o critério (a): a frase aprovada entra no
// banner — nos DOIS estados, porque o operador precisa de saber o que o orçamento cobrirá ANTES
// de o ligar, e não só depois.
func TestAOS255DeclaracaoDeAlcanceNosDoisEstados(t *testing.T) {
	t.Parallel()

	naoComposto := strings.Join(budgetPostureBanner(false), "\n")
	composto := strings.Join(budgetPostureBanner(true), "\n")

	for nome, linha := range map[string]string{"nao-composto": naoComposto, "composto": composto} {
		if !strings.Contains(linha, BudgetScopeDeclaration) {
			t.Errorf("o estado %s do banner nao contem a declaracao de alcance APROVADA (%q) — e a frase, nao uma parafrase dela, que o criterio de aceitacao de AOS-255 fixa:\n%s", nome, BudgetScopeDeclaration, linha)
		}
		baixa := strings.ToLower(linha)
		for _, frase := range budgetOverclaimPhrases {
			if strings.Contains(baixa, frase) {
				t.Errorf("o estado %s do banner usa %q — uma promessa de cobertura TOTAL que a composicao nao sustenta enquanto o turno de modelo for invocado fora da cadeia do RM:\n%s", nome, frase, linha)
			}
		}
	}

	// Os dois estados têm de ser DISTINGUÍVEIS: uma linha que dissesse o mesmo nos dois casos
	// tornaria o parâmetro decorativo e devolveria o defeito que AOS-248 fechou.
	if !strings.Contains(naoComposto, "NAO COMPOSTO") {
		t.Errorf("o estado nao-composto tem de continuar a declara-lo (e o estado do binario SEM AOS_BUDGET_MAX_TOKENS, que continua a ser o default; com a env definida o binario compoe, e esse e o outro ramo):\n%s", naoComposto)
	}
	if strings.Contains(composto, "NAO COMPOSTO") {
		t.Errorf("o estado composto NAO pode declarar-se nao-composto:\n%s", composto)
	}
	// "POR-INCARNACAO" está aqui pela mesma razão que os outros quatro, e não por zelo: um
	// banner que diga só "POR-RUN" faz o operador ler um tecto que o run não excede, quando
	// uma re-hospedagem (retoma apos escalada, restart) recebe o tecto INTEIRO e um run em
	// ciclo pode gastar N x AOS_BUDGET_MAX_TOKENS. O FACTO que esta linha afirma está selado
	// do outro lado, em integration.TestAOS256_TectoEPorIncarnacaoComoODeclarado.
	for _, marcador := range []string{"TOOL-ONLY", "TOKEN-ONLY", "POR-RUN", "POR-INCARNACAO", "AOS_BREAKER_MAX_WALL_CLOCK"} {
		if !strings.Contains(composto, marcador) {
			t.Errorf("o estado composto devia conter %q — o alcance (so tool calls), a dimensao (tokens), a granularidade (por-run E por-incarnacao) e o travao REAL da inferencia (tempo) sao as coisas que o operador tem de ler:\n%s", marcador, composto)
		}
	}
}

// TestAOS255DeclaracaoNosDocumentosDoOperador prova o critério (b): a declaração está no README
// do nó — o documento de quem corre a imagem — e no relatório de prontidão, que é onde o plano
// de billing token-only vive. Um banner que diga a verdade e uma doc que a contradiga deixam o
// operador a acreditar na doc.
func TestAOS255DeclaracaoNosDocumentosDoOperador(t *testing.T) {
	t.Parallel()

	for _, caminho := range []string{nodeREADME, budgetProntidaoReport} {
		conteudo, err := os.ReadFile(caminho)
		if err != nil {
			t.Fatalf("ler %q: %v (e um dos dois documentos nomeados pelo criterio de aceitacao de AOS-255; se mudou de sitio, actualize a constante)", caminho, err)
		}
		if !strings.Contains(string(conteudo), budgetScopeDeclarationAccented) {
			t.Errorf("%s nao contem a declaracao de alcance aprovada (%q) — o criterio (b) de AOS-255 exige que os documentos REFIRAM a declaracao, com a mesma frase que o banner imprime", caminho, budgetScopeDeclarationAccented)
		}
	}
}

// TestAOS255CallSiteMatchesComposition é o guard de POSTURA ANUNCIADA = POSTURA LIGADA aplicado
// ao argumento do banner: a relação entre QUEM COMPÕE o hook (o import de `control-plane/budget`
// numa das duas árvores) e o QUE O BANNER DIZ (o argumento no composition-root).
//
// Cobre as DUAS mentiras simétricas, e não só uma:
//
//   - `budgetPostureBanner(false)` com o hook composto — o banner NEGA por escrito um orçamento
//     que já está a decidir tool calls, e o operador deixa de procurar a causa das negações no
//     sítio certo. Era o risco do ticket seguinte a AOS-255, e concretizou-se em AOS-256/257;
//   - `budgetPostureBanner(true)` HARDCODED — a mentira mais cara das duas, porque anuncia
//     protecção onde pode não haver nenhuma. Um literal `true` sobrevive a alguém remover o
//     wiring e continua a jurar que o tecto está ligado.
//
// Depois de AOS-256/257 o argumento passou a ser derivado (`runBudget != nil`), pelo que o ramo
// do `false` deixou de disparar. Sem o ramo do `true` — e sem a exigência POSITIVA de que a
// chamada exista de todo — o teste ficava INERTE: verde por não ter nada que verificar, que é
// uma forma de cobertura aparente. A asserção positiva de que o argumento é o estado composto
// vive em [TestAOS257BannerDerivaDoOrcamentoComposto]; aqui exige-se apenas que a linha CONTINUE
// a ser emitida, para que apagá-la não passe em silêncio.
func TestAOS255CallSiteMatchesComposition(t *testing.T) {
	t.Parallel()

	// As DUAS árvores que podem compor o hook: o nó (que preenche a config) e a cadeia real
	// (`integration`, onde vive o ponto de injecção "budget" de secured.go).
	importado, onde := importaPacoteBudget(t, []string{".", "../../integration"})

	fonte, err := os.ReadFile("bootstrap.go")
	if err != nil {
		t.Fatalf("ler bootstrap.go: %v", err)
	}
	src := string(fonte)
	literalFalse := strings.Contains(src, "budgetPostureBanner(false)")
	literalTrue := strings.Contains(src, "budgetPostureBanner(true)")

	switch {
	case importado && literalFalse:
		t.Errorf("%s importa control-plane/budget mas o composition-root ainda chama budgetPostureBanner(false):\n"+
			"o banner passaria a NEGAR por escrito um orcamento que ja esta composto. Derive o argumento do estado REALMENTE composto (o campo de orcamento da config, molde de modelPostureBanner/autonomyPostureBanner).", onde)
	case literalTrue:
		t.Error("o composition-root chama budgetPostureBanner(true) com um LITERAL:\n" +
			"e a mentira mais perigosa das duas — anuncia o tecto ligado mesmo que o wiring desapareca. Derive o argumento do estado REALMENTE composto (runBudget != nil).")
	case !importado && !literalFalse:
		// Aceitável e provavelmente correcto (o argumento passou a ser derivado), mas então o
		// ramo composto ficou inalcançável sem que ninguém o declarasse: exige-se um olhar.
		t.Logf("o composition-root ja nao passa o literal false a budgetPostureBanner e nenhuma arvore importa control-plane/budget — confirme que o argumento deriva de estado REAL e nao de intencao de config")
	}

	// A linha tem de continuar a ser EMITIDA. Sem isto, apagar a chamada inteira do
	// composition-root passaria neste teste (nenhum literal, nenhuma incoerencia) — e o
	// operador ficaria sem declaracao nenhuma sobre o orcamento, que e o silencio que
	// AOS-248 fechou.
	if !strings.Contains(src, "budgetPostureBanner(") {
		t.Error("o composition-root deixou de chamar budgetPostureBanner: o orcamento voltou a ser uma superficie SILENCIOSA no banner de arranque")
	}
}

// importaPacoteBudget devolve se alguma árvore (recursivamente, ficheiros NÃO-teste) importa
// `control-plane/budget`, e o ficheiro:linha onde o import está.
//
// Por AST e não por grep, pela mesma razão do gate de AOS-203: uma menção em comentário ou numa
// string de banner (este pacote tem várias) não é uma composição, e um gate que as confundisse
// avermelhava sem defeito.
func importaPacoteBudget(t *testing.T, roots []string) (bool, string) {
	t.Helper()

	fset := token.NewFileSet()
	ficheiros := 0
	for _, root := range roots {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			t.Fatalf("arvore %q inacessivel (%v) — se o codigo mudou de sitio, actualize a lista; o gate NAO pode ignora-la em silencio", root, err)
		}
		var achado string
		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != root && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")) {
					return filepath.SkipDir
				}
				return nil
			}
			nome := d.Name()
			if !strings.HasSuffix(nome, ".go") || strings.HasSuffix(nome, "_test.go") {
				return nil
			}
			ficheiros++
			file, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if perr != nil {
				t.Fatalf("parser.ParseFile(%q): %v", filepath.ToSlash(path), perr)
			}
			for _, imp := range file.Imports {
				caminho := strings.Trim(imp.Path.Value, `"`)
				if strings.HasSuffix(caminho, "control-plane/budget") && achado == "" {
					pos := fset.Position(imp.Pos())
					achado = filepath.ToSlash(path) + ":" + strconv.Itoa(pos.Line)
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("WalkDir(%q): %v", root, walkErr)
		}
		if achado != "" {
			return true, achado
		}
	}
	if ficheiros == 0 {
		t.Fatal("nenhum ficheiro .go nao-teste varrido — o gate ficaria vacuamente verde")
	}
	return false, ""
}
