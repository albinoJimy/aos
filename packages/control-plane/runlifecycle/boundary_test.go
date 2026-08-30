package runlifecycle

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// boundary_test.go — OS GUARDAS DA DECISÃO (AOS-281, ADR-023 §5).
//
// Uma decisão de arquitectura que só vive num documento é uma decisão que o próximo
// implementador contorna de boa-fé. Estes guardas tornam-na FALSIFICÁVEL: cada um
// falha se a regra correspondente for quebrada, e a mensagem diz qual é a regra.
//
// São de propósito guardas SOBRE O SOURCE deste pacote (e sobre o grafo de build do
// nó), não sobre comportamento: o comportamento já é exercido pelos testes de posse e
// re-hidratação. O que estes apanham é o que aqueles não podem apanhar — uma via NOVA
// acrescentada amanhã que escape ao mecanismo.

// productionFiles devolve os ficheiros .go de PRODUÇÃO deste pacote (exclui _test.go).
func productionFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir do pacote: %v", err)
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		t.Fatal("nenhum ficheiro de produção encontrado — os guardas ficariam vacuosos")
	}
	return out
}

// ---------------------------------------------------------------------------
// GUARDA 1 (ADR-023 §5.3) — NÃO HÁ VIA PARA O BUILDER CEGO.
//
// A re-hidratação obrigatória é a metade do AC4 que não se prova por teste de
// comportamento: um teste mostra que a via ACTUAL re-hidrata; só um guarda impede que
// alguém acrescente amanhã uma via que não re-hidrate.
// ---------------------------------------------------------------------------

func TestGuard_SemBuilderCego(t *testing.T) {
	for _, f := range productionFiles(t) {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("leitura de %s: %v", f, err)
		}
		// `NewGraphBuilderFromLog` contém `NewGraphBuilder` como prefixo: procura-se a
		// chamada EXACTA do construtor cego, com o parêntese.
		if strings.Contains(string(src), "orchestrator.NewGraphBuilder(") {
			t.Errorf("%s chama orchestrator.NewGraphBuilder( — VIOLA ADR-023 §2.3: este pacote só constrói grafos RE-HIDRATADOS (NewGraphBuilderFromLog). Um builder sobre um DAG vazio num run que já existe no log admite arestas cegas às duráveis, e o RebuildDAG passa a falhar PARA SEMPRE", f)
		}
	}
}

// ---------------------------------------------------------------------------
// GUARDA 2 (ADR-023 §5.4) — TODA A ESCRITA PASSA PELO ENFORCEMENT DE FENCING.
//
// Enumera, por AST, TODAS as chamadas a `.Append(` no source de produção e exige que
// cada uma esteja na lista das vias conhecidas. A lista é a documentação executável de
// porque cada escrita é segura; acrescentar uma via nova obriga a justificá-la aqui.
//
// NÃO é uma prova de que nenhuma escrita escapa — é um TRIPWIRE, e é honesto dizê-lo:
// o que ele garante é que nenhuma via nova entra em silêncio.
// ---------------------------------------------------------------------------

func TestGuard_EscritasSaoFenced(t *testing.T) {
	// Cada entrada é `<selector da chamada>` → razão por que é segura.
	permitidas := map[string]string{
		// A via canónica: o enforcement de AOS-018 em pessoa.
		"t.fenced.Append": "Tenure.Append — o FencedAppender do run (ADR-023 §2.4)",
		"a.fenced.Append": "tenureAppender.Append — o FencedAppender do run, a escrever no stream do plano (ver planFenced)",
		// A via indirecta: o Tenure delega, e o Tenure é fenced.
		"f.t.Append": "fencedStore.Append — delega em Tenure.Append, que é fenced",
		// O DESTINO do FencedAppender. Só é alcançável ATRAVÉS dele (é o EventStore que
		// lhe foi injectado em NewPlanRecorder), nunca directamente: nenhum caminho
		// exportado deste pacote devolve um planFenced.
		"p.store.Append": "planFenced.Append — é o destino redireccionado DO FencedAppender, não uma via paralela",
	}
	fset := token.NewFileSet()
	visto := 0
	for _, f := range productionFiles(t) {
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("parse de %s: %v", f, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Append" {
				return true
			}
			nome := renderSelector(sel)
			visto++
			if _, permitida := permitidas[nome]; !permitida {
				t.Errorf("%s:%d chama %s( — VIA DE ESCRITA NÃO DECLARADA. VIOLA ADR-023 §2.4: toda a escrita de ciclo de vida deste pacote passa pelo durable.FencedAppender. Se esta via é legítima, acrescenta-a à lista de %s COM a razão pela qual é segura",
					f, fset.Position(sel.Pos()).Line, nome, t.Name())
			}
			return true
		})
	}
	if visto == 0 {
		t.Fatal("nenhuma chamada a Append encontrada no source de produção — o guarda ficou vacuoso (o pacote deixou de escrever, ou o AST deixou de as ver)")
	}
}

// renderSelector reconstrói o texto de um selector encadeado (`a.b.Append`).
func renderSelector(sel *ast.SelectorExpr) string {
	var partes []string
	var walk func(ast.Expr) bool
	walk = func(e ast.Expr) bool {
		switch v := e.(type) {
		case *ast.Ident:
			partes = append([]string{v.Name}, partes...)
			return true
		case *ast.SelectorExpr:
			partes = append([]string{v.Sel.Name}, partes...)
			return walk(v.X)
		default:
			partes = append([]string{"?"}, partes...)
			return false
		}
	}
	walk(sel)
	return strings.Join(partes, ".")
}

// ---------------------------------------------------------------------------
// GUARDA 3 (ADR-023 §5.1) — O NÓ NÃO ARRASTA ESTE MÓDULO.
//
// É o ESPELHO, deste lado, do guarda transitivo do ADR-018 §5. Aquele verifica que o
// nó não contém ORQ/SCH; este verifica que o nó não contém a COMPOSIÇÃO que os junta.
// Se um dia alguém ligar este módulo ao nó, o guarda do ADR-018 dispara por arrasto —
// mas dispara com uma mensagem sobre o orquestrador, que é a causa errada. Este
// dispara com a causa certa.
// ---------------------------------------------------------------------------

func TestBoundary_NoNaoArrastaEstaComposicao(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("toolchain `go` indisponível — verificação do grafo de build do nó saltada: %v", err)
	}
	noDir := filepath.Join("..", "..", "cmd", "aos")
	if _, serr := os.Stat(filepath.Join(noDir, "go.mod")); serr != nil {
		t.Skipf("pacote do nó não encontrado em %s: %v", noDir, serr)
	}
	cmd := exec.Command(goBin, "list", "-deps", ".")
	cmd.Dir = noDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("`go list -deps .` no pacote do nó falhou: %v\n%s", err, out)
	}
	const meuModulo = "github.com/aos-ref/control-plane/runlifecycle"
	seen := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		seen++
		if p == meuModulo || strings.HasPrefix(p, meuModulo+"/") {
			t.Errorf("o grafo de build do nó inclui %q — VIOLA ADR-023 §2.6: esta composição é um PROCESSO à parte (cmd/aos-orq). Ligá-la ao nó reabre a forma do produto v1 (Carta §7) e arrasta o ORQ/SCH para dentro do processo do nó, contra o ADR-018 §5", p)
		}
	}
	if seen == 0 {
		t.Fatal("`go list -deps .` não devolveu pacotes — o guarda ficou vacuoso")
	}
}

// ---------------------------------------------------------------------------
// GUARDA 4 (ADR-023 §5.2) — A DIRECÇÃO DA DEPENDÊNCIA.
//
// O que mantém `TestBoundary_ProductionImportsAreAllowlisted` do despachante VERDE e
// INALTERADO é a direcção: este pacote importa o `plandispatch`; o `plandispatch` não
// importa nada disto. Se alguém inverter a seta, o guarda do despachante dispara —
// mas só quando alguém correr os testes DELE. Este dispara aqui, junto de quem fez a
// mudança.
// ---------------------------------------------------------------------------

func TestBoundary_DespachanteNaoImportaEstaComposicao(t *testing.T) {
	dispatchDir := filepath.Join("..", "orchestrator", "plandispatch")
	entries, err := os.ReadDir(dispatchDir)
	if err != nil {
		t.Skipf("pacote plandispatch não encontrado em %s: %v", dispatchDir, err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(dispatchDir, n), nil, parser.ImportsOnly)
		if perr != nil {
			t.Fatalf("parse de %s: %v", n, perr)
		}
		checked++
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(p, "github.com/aos-ref/control-plane/runlifecycle") {
				t.Errorf("plandispatch/%s importa %q — SETA INVERTIDA. VIOLA ADR-023 §2.2/§2.6: o despachante DERIVA do log e não conhece o Event Store; a composição depende dele, nunca o contrário", n, p)
			}
		}
	}
	if checked == 0 {
		t.Fatal("nenhum ficheiro de produção do plandispatch verificado — o guarda ficou vacuoso")
	}
}

// ---------------------------------------------------------------------------
// GUARDA 5 (ADR-023 §2.2) — O DESPACHANTE NÃO GANHA VIA DE ESCRITA DE ESTADO.
//
// As portas que este pacote implementa para o `plandispatch` são de LEITURA, com UMA
// excepção declarada: o [BranchJournal], que regista os factos de DECISÃO do
// despachante — nunca transições de estado.
//
// Este guarda verifica a propriedade que separa as duas coisas: os tipos de evento que
// o caminho do journal pode emitir. Um `state.transition` ou um
// `task.node.state_changed` escrito a partir daqui seria o despachante a mover a
// máquina de estados — exactamente o que a decisão proíbe.
// ---------------------------------------------------------------------------

func TestGuard_JournalDoDespachanteNaoEscreveEstado(t *testing.T) {
	proibidos := []string{
		"EventTaskNodeStateChanged", // task.node.state_changed (AOS-025, por-nó)
		"EventTypeTransition",       // state.transition (AOS-017, por-run)
		"EventTaskNodeCreated",      // topologia — é do ORQ sob posse, não do SCH
	}
	src, err := os.ReadFile("emitters.go")
	if err != nil {
		t.Fatalf("leitura de emitters.go: %v", err)
	}
	for _, p := range proibidos {
		if strings.Contains(string(src), p) {
			t.Errorf("emitters.go refere %s — VIOLA ADR-023 §2.2: o caminho de escrita do despachante regista DECISÕES (plan.branch_decided), nunca transições de estado de ciclo de vida", p)
		}
	}
	// Não-vacuidade: o ficheiro TEM de conter o tipo que ele legitimamente emite, ou o
	// guarda estaria a verificar a ausência de tudo num ficheiro que não escreve nada.
	if !strings.Contains(string(src), "RecordBranchDecided") {
		t.Fatal("emitters.go não regista decisões de ramo — o guarda ficou vacuoso")
	}
}

// ---------------------------------------------------------------------------
// GUARDA 6 (DEF-273) — O ORÁCULO DE EFEITO NÃO É OPCIONAL NESTA VIA.
//
// O materializador de AOS-237 clampa a autoridade do papel `verifier` retirando as
// tools DE EFEITO, e o predicado chega-lhe por `WithEffectOracle` ligado pelo
// composition root. Sem ele fica o `DefaultEffectOracle` — tudo é efeito —, e o
// verificador materializa com autoridade VAZIA: fail-closed, mas inútil, que é o
// estado que o DEF-273 registava.
//
// Este guarda impõe as DUAS metades da forma escolhida (ADR-023: tornar o errado
// inconstruível, em vez de verificar que alguém ligou o certo):
//
//  1. o oráculo desta via DERIVA sempre do snapshot pinado;
//  2. e é acrescentado DEPOIS das opções do chamador, para que um `WithEffectOracle`
//     permissivo vindo de fora não possa baixar o clamp.
// ---------------------------------------------------------------------------

func TestGuard_OraculoDeEfeitoNaoEOpcional(t *testing.T) {
	src, err := os.ReadFile("materialize.go")
	if err != nil {
		t.Fatalf("leitura de materialize.go: %v", err)
	}
	texto := string(src)

	// (1) A ÚNICA fonte do oráculo é o snapshot pinado.
	if !strings.Contains(texto, "planmaterialize.WithEffectOracle(snapshot.EffectOracle())") {
		t.Error("materialize.go já não liga `planmaterialize.WithEffectOracle(snapshot.EffectOracle())` — VIOLA DEF-273: sem o oráculo REAL o materializador cai no DefaultEffectOracle e o verificador materializa com autoridade VAZIA")
	}

	// (2) A ordem: o oráculo real é a ÚLTIMA opção. Como a última vence, um
	// `WithEffectOracle` que chegue por `opts` não pode substituí-lo. Verifica-se que
	// as opções do chamador entram ANTES na mesma expressão.
	i := strings.Index(texto, "opts...)")
	j := strings.Index(texto, "planmaterialize.WithEffectOracle(snapshot.EffectOracle())")
	if i < 0 || j < 0 || i > j {
		t.Error("as opções do chamador deixaram de ser acrescentadas ANTES do oráculo real — VIOLA DEF-273/ADR-023: a última opção vence, pelo que um WithEffectOracle permissivo do chamador passaria a baixar o clamp do verificador")
	}

	// (3) Não-vacuidade: a via tem de RECUSAR um snapshot vazio, que faria o oráculo
	// devolver «efeito» para tudo — reproduzindo o default que ela existe para eliminar.
	if !strings.Contains(texto, "ErrSemSnapshot") {
		t.Error("materialize.go já não recusa um snapshot vazio — um snapshot em que nada resolve é o DefaultEffectOracle por outro nome")
	}
}
