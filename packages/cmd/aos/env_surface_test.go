package main

// AOS-203 (achados ORF-03/04/05 da auditoria v4) — a SUPERFÍCIE DE AMBIENTE do nó.
//
// Este ficheiro tem dois eixos, e ambos são gates, não descrições:
//
//  1. [TestAOS203EnvSurfaceIsDocumented] — o conjunto de variáveis de ambiente LIDAS pelo
//     código do nó é SUBCONJUNTO do documentado, COM Default e Efeito preenchidos, na secção
//     "Superfície de configuração" de `deploy/node/README.md`. Uma variável nova sem
//     documentação parte o teste. É a anti-recorrência do achado que apanhou `AOS_HUMANS` e
//     `AOS_ISSUER_ID` sem UMA linha de documentação em todo o repo: sem gate, a próxima
//     variável repete o defeito no PR seguinte.
//
//  2. [TestAOS203SovereignReadKillSwitchIsVisible] e vizinhos — o kill-switch de
//     `AOS_BOARD_REGIONS` definido-vazio deixa de ser SILENCIOSO fora de produção, SEM
//     que o fail-closed de produção (que já existia) seja enfraquecido.

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// nodeREADME é o documento do OPERADOR — o artefacto que quem corre a imagem lê. É
// deliberadamente ESTE ficheiro e não um doc.go: a superfície de ambiente só é alcançável
// por quem faz o deployment, e é lá que a documentação tem de estar para servir de alguma
// coisa.
const nodeREADME = "../../../deploy/node/README.md"

// readmeEnvSection é o cabeçalho da secção do README onde o índice COMPLETO das variáveis
// vive. O gate extrai as linhas de tabela SÓ daqui: sem esta delimitação, uma variável
// documentada em qualquer outra tabela do ficheiro (estado durável, plano de controlo,
// exemplos) passaria o gate enquanto o índice — a coisa que o operador lê primeiro — ficava
// incompleto. O próprio README afirma que este teste protege ESTA secção; o gate tem de ser
// tão forte quanto a frase que o anuncia.
const readmeEnvSection = "Superfície de configuração"

// envSourceRoots são as árvores de CÓDIGO cujas leituras de ambiente este gate exige
// documentadas no README do nó. São duas porque a IMAGEM entrega dois binários e o operador
// não distingue um do outro ao escrever o `docker run`:
//
//   - "." — o nó (`packages/cmd/aos`), o âmbito literal do critério de aceitação;
//   - o `aos-healthprobe` do HEALTHCHECK, que lê `AOS_HEALTH_URL`/`AOS_API_ADDR`. Está fora
//     do módulo, mas uma variável nova ali é exactamente o mesmo defeito para quem opera a
//     imagem — e incluí-la aqui custa uma linha, enquanto um gate em CI (AOS-190/198) custa
//     um ticket com propriedade de scripts/**.
//
// A varredura é RECURSIVA (WalkDir, não ReadDir): um subpacote acrescentado amanhã não
// escapa ao gate em silêncio.
var envSourceRoots = []string{".", "../../../deploy/node/healthprobe"}

// internalEnvAllowlist é a válvula EXPLÍCITA para uma variável deliberadamente INTERNA
// (não destinada ao operador) que, por isso, não aparece no README do nó. Cada entrada é
// name → JUSTIFICAÇÃO, e a justificação é obrigatória: uma entrada sem razão escrita é
// indistinguível de um skip silencioso, que é exactamente o mecanismo que este gate vem
// substituir.
//
// ESTÁ VAZIA DE PROPÓSITO. Hoje NENHUMA variável lida pelo nó é interna: todas vieram de uma
// receita de deployment ou de uma flag de CLI, logo todas pertencem ao README. Acrescentar
// uma entrada aqui é uma decisão de âmbito que fica registada no diff — que é o ponto.
var internalEnvAllowlist = map[string]string{}

// envNameWrappers são as funções DESTE pacote que recebem o nome da variável por
// PARÂMETRO e o lêem por dentro. Dentro delas, um `os.Getenv(name)` com argumento
// não-literal é esperado e não é uma leitura opaca — a leitura REAL está no chamador, que
// o extractor apanha pelo literal. Fora delas, um argumento não-literal FALHA o teste (uma
// variável de ambiente cujo nome o gate não consegue determinar é uma variável que o gate
// não consegue exigir documentada).
var envNameWrappers = map[string]bool{
	"envOr": true,
	// AOS-080/081 — os limiares do disjuntor partilham a validação (inteiro/duração/
	// número, >= 0, vazio ⇒ default). Os nomes REAIS são literais nos chamadores
	// (breakerThresholdsFromEnv), que é onde o extractor os apanha.
	"envInt":      true,
	"envDuration": true,
	"envFloat":    true,
}

// envReadFuncs são as leituras de ambiente reconhecidas: as duas da stdlib mais os
// wrappers locais. `os.LookupEnv` conta como leitura tanto quanto `os.Getenv` — foi
// precisamente por LookupEnv (e não Getenv) que `AOS_BOARD_REGIONS` distingue os três
// estados, e um extractor que só olhasse para Getenv perderia a variável mais sensível
// da superfície.
var envReadFuncs = map[string]bool{
	"os.Getenv": true, "os.LookupEnv": true, "envOr": true,
	"envInt": true, "envDuration": true, "envFloat": true,
}

// envEnumerationFuncs são as leituras de ambiente POR ENUMERAÇÃO — a única forma conhecida
// de ler o ambiente sem nomear a variável, e portanto de escapar a este gate por construção.
// São PROIBIDAS no código do nó: quem precisar de uma variável nova nomeia-a (e documenta-a).
var envEnumerationFuncs = map[string]bool{"os.Environ": true}

// TestAOS203EnvSurfaceIsDocumented prova que toda a variável de ambiente lida pelo binário
// do nó está documentada, com Default e Efeito PREENCHIDOS, na secção de índice do README do
// operador.
//
// NÃO-VACUOSIDADE: o teste falha se o extractor não encontrar variável nenhuma (um refactor
// que mudasse a forma das chamadas tornaria o gate vazio e verde), se a secção do README não
// existir ou não tiver linhas de tabela, se alguma leitura tiver um nome não-literal, e se
// alguém ler o ambiente por enumeração.
func TestAOS203EnvSurfaceIsDocumented(t *testing.T) {
	t.Parallel()

	read := envVarsReadBySources(t, envSourceRoots)
	if len(read) == 0 {
		t.Fatal("o extractor nao encontrou NENHUMA leitura de ambiente nas arvores do no — o gate ficaria vacuamente verde; verifique se as chamadas mudaram de forma (os.Getenv/os.LookupEnv/envOr)")
	}
	documented := envVarsDocumentedInREADME(t, nodeREADME)
	if len(documented) == 0 {
		t.Fatalf("a seccao %q de %s nao tem NENHUMA linha de tabela cuja primeira celula seja uma variavel AOS_ em backticks — o gate ficaria vacuamente verde", readmeEnvSection, nodeREADME)
	}

	var undocumented []string
	for name := range read {
		row, ok := documented[name]
		switch {
		case ok && row.complete:
			continue
		case ok:
			// A linha existe mas está DEGENERADA. Não conta como documentada: o critério de
			// aceitação pede "efeito, default e impacto de segurança", não a presença do nome.
			t.Errorf("%s tem linha de tabela em %s:%d mas com a celula Default ou Efeito em BRANCO (ou trivial) — preencha as tres celulas: o criterio e \"efeito, default e impacto de seguranca\", nao a mencao do nome", name, nodeREADME, row.line)
			continue
		}
		if why, ok := internalEnvAllowlist[name]; ok {
			if strings.TrimSpace(why) == "" {
				t.Errorf("%s esta na internalEnvAllowlist SEM justificacao — uma allowlist sem razao escrita e um skip silencioso", name)
			}
			continue
		}
		undocumented = append(undocumented, name)
	}
	sort.Strings(undocumented)
	for _, name := range undocumented {
		t.Errorf("variavel de ambiente %s e LIDA em %s mas NAO esta documentada em %s.\n"+
			"Acrescente uma linha na tabela da seccao %q: primeira celula com %s entre backticks, depois o DEFAULT e o EFEITO (com o impacto de SEGURANCA/CONFORMIDADE explicito, quando exista).\n"+
			"Se a variavel for deliberadamente INTERNA (nao destinada ao operador), acrescente-a a internalEnvAllowlist COM justificacao.",
			name, strings.Join(read[name], ", "), nodeREADME, readmeEnvSection, name)
	}

	// Higiene da allowlist: uma entrada que JÁ está documentada é lixo que mascararia uma
	// regressão futura (a variável podia sair do README sem o gate se queixar).
	for name := range internalEnvAllowlist {
		if _, ok := documented[name]; ok {
			t.Errorf("%s esta na internalEnvAllowlist E documentada em %s — remova-a da allowlist (uma entrada obsoleta mascara uma regressao futura)", name, nodeREADME)
		}
		if _, ok := read[name]; !ok {
			t.Errorf("%s esta na internalEnvAllowlist mas NAO e lida por nenhum ficheiro do no — remova-a", name)
		}
	}
}

// envVarsReadBySources corre [collectEnvReads] sobre cada árvore e junta os resultados.
func envVarsReadBySources(t *testing.T, roots []string) map[string][]string {
	t.Helper()

	out := make(map[string][]string)
	for _, root := range roots {
		collectEnvReads(t, root, out)
	}
	return out
}

// collectEnvReads extrai, por ANÁLISE SINTÁCTICA (go/parser — não regex sobre texto), os
// nomes das variáveis de ambiente lidas pelos ficheiros NÃO-teste da árvore root (recursiva),
// acumulando em out: name → "ficheiro:linha".
//
// Porquê AST e não grep: um `grep AOS_` apanharia menções em comentários, em strings de
// erro e em documentação embutida — este pacote está cheio delas —, e o gate passaria a
// exigir documentação de variáveis que ninguém lê. O que interessa é a LEITURA REAL.
//
// Ficheiros com `//go:build ignore` são varridos na mesma (o parser não avalia tags): é o
// lado conservador — exigir documentação a mais nunca esconde uma variável.
func collectEnvReads(t *testing.T, root string, out map[string][]string) {
	t.Helper()

	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		t.Fatalf("arvore de codigo %q inacessivel (%v) — se mudou de sitio, actualize envSourceRoots; o gate NAO pode simplesmente ignora-la", root, err)
	}
	fset := token.NewFileSet()
	files := 0
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
		name := d.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		files++
		rel := filepath.ToSlash(path)
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("parser.ParseFile(%q): %v", rel, perr)
		}
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			enclosing := ""
			if isFunc {
				enclosing = fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee := calleeName(call.Fun)
				if envEnumerationFuncs[callee] {
					pos := fset.Position(call.Pos())
					t.Errorf("%s:%d: %s — leitura de ambiente por ENUMERACAO e proibida no codigo do no: escapa ao gate de documentacao por construcao (nenhum nome literal para exigir no README). Leia a variavel por os.Getenv/os.LookupEnv e documente-a.", rel, pos.Line, callee)
					return true
				}
				if !envReadFuncs[callee] || len(call.Args) == 0 {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					if envNameWrappers[enclosing] {
						return true // a leitura real está no chamador do wrapper.
					}
					pos := fset.Position(call.Pos())
					t.Errorf("%s:%d: %s com nome NAO-LITERAL — o gate de documentacao nao consegue determinar a variavel; use um literal ou declare a funcao em envNameWrappers", rel, pos.Line, callee)
					return true
				}
				value, uerr := strconv.Unquote(lit.Value)
				if uerr != nil {
					t.Errorf("%s: literal %q ilegivel: %v", rel, lit.Value, uerr)
					return true
				}
				pos := fset.Position(call.Pos())
				out[value] = append(out[value], rel+":"+strconv.Itoa(pos.Line))
				return true
			})
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("WalkDir(%q): %v", root, walkErr)
	}
	if files == 0 {
		t.Fatalf("a arvore %q nao tem ficheiros .go nao-teste — o gate ficaria vacuamente verde sobre ela", root)
	}
}

// calleeName reduz o alvo de uma chamada a "os.Getenv" / "envOr" (ou "" se for outra
// coisa). Deliberadamente literal: `os` é o pacote da stdlib importado sem alias em todos
// os ficheiros deste pacote.
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		if pkg, ok := f.X.(*ast.Ident); ok {
			return pkg.Name + "." + f.Sel.Name
		}
	}
	return ""
}

// readmeEnvRowName apanha a PRIMEIRA célula de uma linha de tabela quando ela é uma variável
// AOS_ em backticks.
var readmeEnvRowName = regexp.MustCompile("^\\|\\s*`(AOS_[A-Z0-9_]+)`\\s*\\|")

// readmeRow é o que o gate sabe de uma linha de tabela: onde está e se está COMPLETA.
type readmeRow struct {
	line     int
	complete bool
}

// minCellRunes é o mínimo de conteúdo que uma célula Default/Efeito tem de ter para a linha
// contar como documentação. Não mede qualidade — mede que a célula não está EM BRANCO: o
// objectivo é impedir a linha degenerada `| `AOS_X` |  |  |`, que satisfaria um gate que só
// olhasse para o nome e não satisfaz o critério de aceitação.
const minCellRunes = 3

// parseREADMEEnvRow decide se line é uma linha de tabela de variável e se as células Default
// e Efeito estão preenchidas. Devolve (nome, completa, éLinhaDeVariável).
func parseREADMEEnvRow(line string) (string, bool, bool) {
	m := readmeEnvRowName.FindStringSubmatch(line)
	if m == nil {
		return "", false, false
	}
	cells := strings.Split(strings.Trim(line, "|"), "|")
	if len(cells) < 3 {
		return m[1], false, true
	}
	def := strings.TrimSpace(cells[1])
	// O Efeito é o RESTO da linha: uma célula que contenha um '|' escapado não deve fazer o
	// gate acreditar que a linha acaba mais cedo.
	eff := strings.TrimSpace(strings.Join(cells[2:], "|"))
	return m[1], len([]rune(def)) >= minCellRunes && len([]rune(eff)) >= minCellRunes, true
}

// envVarsDocumentedInREADME extrai as variáveis documentadas em LINHA DE TABELA, DENTRO da
// secção [readmeEnvSection] do README do nó. Devolve name → [readmeRow].
//
// A delimitação por secção é deliberada: é ali que está o índice COMPLETO que o README
// promete, e é ali que o README diz que este teste avermelha. Uma variável documentada só
// numa tabela de outra secção não conta — o operador que lê o índice não a encontraria.
func envVarsDocumentedInREADME(t *testing.T, path string) map[string]readmeRow {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("abrir %q: %v (o README do operador e o artefacto que este gate protege — se mudou de sitio, actualize nodeREADME)", path, err)
	}
	defer func() { _ = f.Close() }()

	out := make(map[string]readmeRow)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // as linhas de tabela deste README são longas.
	inSection, sectionFound, n := false, false, 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ") {
			inSection = strings.HasPrefix(line, "### ") && strings.Contains(line, readmeEnvSection)
			if inSection {
				sectionFound = true
			}
			continue
		}
		if !inSection {
			continue
		}
		if name, complete, isRow := parseREADMEEnvRow(line); isRow {
			out[name] = readmeRow{line: n, complete: complete}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("ler %q: %v", path, err)
	}
	if !sectionFound {
		t.Fatalf("%s nao tem a seccao %q (cabecalho \"### … %s …\") — o gate nao tem onde procurar o indice de variaveis; se a seccao foi renomeada, actualize readmeEnvSection", path, readmeEnvSection, readmeEnvSection)
	}
	return out
}

// --- Kill-switch da soberania de leitura (ORF-05) --------------------------------------

// killSwitchMarkers são os elementos que o aviso TEM de conter para servir ao operador:
// o rótulo proeminente, o que fica desligado (os DOIS controlos), como se religa, e a
// NEUTRALIZAÇÃO do ponteiro morto que o banner do composition-root ainda imprime.
var killSwitchMarkers = []string{
	"AVISO KILL-SWITCH",
	"SOBERANIA DE LEITURA",
	"AUTHZ POR-CHAMADOR",
	"X-Aos-Reader/X-Aos-Board",
	"SELO WORM",
	"PARA RELIGAR",
	"AOS_BOARD_REGIONS",
	"board:aos-demo=eu",
	"IGNORE a linha",
	"Config.BoardRegions",
	"ErrProductionNeedsSovereignRead",
}

// TestAOS203SovereignReadKillSwitchIsVisible prova o eixo ORF-05: fora de produção,
// AOS_BOARD_REGIONS DEFINIDO-VAZIO continua a desligar o read-path soberano (retro-
// compatibilidade) mas o nó passa a DIZÊ-LO, de forma proeminente, no banner de arranque.
//
// Antes deste ticket a única pista era a linha descritiva do composition-root ("read-path
// LEGADO … defina Config.BoardRegions"), que aponta para um campo de `package main` que o
// operador do binário NÃO consegue escrever — o remédio era inalcançável a partir do
// sintoma. Essa linha continua a ser impressa (bootstrap.go está fora da propriedade deste
// ticket), pelo que o aviso tem de a NEUTRALIZAR pelo nome; é o que a asserção final exige.
func TestAOS203SovereignReadKillSwitchIsVisible(t *testing.T) {
	banner := runWithCleanEnv(t, "") // AOS_BOARD_REGIONS DEFINIDA-VAZIA

	for _, marker := range killSwitchMarkers {
		if !strings.Contains(banner, marker) {
			t.Errorf("o banner do kill-switch devia conter %q — sem isso o operador nao sabe o que perdeu nem como o recupera.\nBanner:\n%s", marker, banner)
		}
	}
	// O aviso tem de ser COERENTE com o estado composto: o composition-root declara o
	// read-path legado, e o aviso explica-o. Se um dia o vazio deixar de desligar a
	// soberania, esta asserção apanha a incoerência.
	if !strings.Contains(banner, "read-path LEGADO") {
		t.Errorf("o banner devia continuar a declarar o read-path LEGADO composto (o aviso explica-o, nao o substitui).\nBanner:\n%s", banner)
	}
	// O ponteiro morto do composition-root ("defina Config.BoardRegions") continua lá; o que
	// não pode acontecer é ficar SOZINHO. Se um dia bootstrap.go for corrigido, esta asserção
	// não parte (a linha de neutralização continua verdadeira e inofensiva) — mas enquanto o
	// ponteiro existir, ele não é a última palavra que o operador lê sobre o assunto.
	deadPointer := strings.Index(banner, "defina Config.BoardRegions")
	remedy := strings.Index(banner, "IGNORE a linha")
	if deadPointer >= 0 && !(remedy > deadPointer) {
		t.Errorf("o banner imprime o ponteiro inalcancavel \"defina Config.BoardRegions\" sem que a linha de neutralizacao venha DEPOIS dele — o operador segue a instrucao morta.\nBanner:\n%s", banner)
	}
}

// TestAOS203DefaultSovereignReadDoesNotWarn é a PROVA NEGATIVA do teste anterior: sem
// AOS_BOARD_REGIONS o nó aplica o default de referência, a soberania de leitura fica
// LIGADA e o aviso NÃO sai. Sem esta metade, um aviso incondicional passaria o teste de
// cima e treinaria o operador a ignorá-lo.
func TestAOS203DefaultSovereignReadDoesNotWarn(t *testing.T) {
	t.Setenv("AOS_BOARD_REGIONS", "") // regista o cleanup; o Unsetenv abaixo é que vale.
	if err := os.Unsetenv("AOS_BOARD_REGIONS"); err != nil {
		t.Fatalf("Unsetenv: %v", err)
	}
	sb := runWithoutTouchingBoardRegions(t)

	if strings.Contains(sb, "AVISO KILL-SWITCH") {
		t.Errorf("sem AOS_BOARD_REGIONS a soberania de leitura fica LIGADA pelo default — o aviso de kill-switch NAO devia sair.\nBanner:\n%s", sb)
	}
	if !strings.Contains(sb, "READ-PATH SOBERANO FAIL-CLOSED ligado") {
		t.Errorf("sem AOS_BOARD_REGIONS o banner devia declarar o read-path SOBERANO ligado (default de referencia).\nBanner:\n%s", sb)
	}
}

// TestAOS203ProductionStillRefusesInsteadOfWarning é o GUARD-TEST da mudança: o aviso é
// para FORA de produção. Em AOS_MODE=production o estado vazio continua a RECUSAR o
// arranque — o aviso não pode ter-se tornado a resposta "suave" que substitui o
// fail-closed. É a regressão mais provável de quem tocar neste caminho a seguir.
func TestAOS203ProductionStillRefusesInsteadOfWarning(t *testing.T) {
	t.Setenv("AOS_MODE", "production")
	t.Setenv("AOS_BOARD_REGIONS", "")
	t.Setenv("AOS_API_ADDR", "")
	t.Setenv("AOS_ISSUER_PUBKEY", "") // irrelevante: a soberania aborta primeiro.

	var sb strings.Builder
	err := run(&sb)
	if err == nil {
		t.Fatal("AOS_MODE=production com AOS_BOARD_REGIONS vazia devia RECUSAR o arranque — nao basta avisar")
	}
	if !strings.Contains(err.Error(), "AOS_BOARD_REGIONS") {
		t.Fatalf("o erro devia nomear AOS_BOARD_REGIONS, veio: %v", err)
	}
	if strings.Contains(sb.String(), "AVISO KILL-SWITCH") {
		t.Errorf("em producao o no nao chega a compor-se: o aviso nao devia sair (a recusa e a resposta).\nBanner:\n%s", sb.String())
	}
}

// TestSovereignReadKillSwitchBannerStates cobre a FUNÇÃO pura nos seus estados, incluindo os
// dois inalcançáveis pelo binário (Config composta in-process sem entradas; WORM ausente):
// a mensagem tem de dizer a verdade sobre a causa em vez de culpar uma variável que ninguém
// definiu — ou de se calar.
func TestSovereignReadKillSwitchBannerStates(t *testing.T) {
	t.Parallel()

	if lines := sovereignReadKillSwitchBanner(true, true, "", true); lines != nil {
		t.Errorf("com a soberania LIGADA (registo + WORM compostos) nao sai aviso nenhum, vieram %d linhas: %v", len(lines), lines)
	}
	cases := []struct {
		name       string
		regions    bool
		worm       bool
		raw        string
		defined    bool
		wantSubstr string
	}{
		{"definida vazia", false, true, "", true, "DEFINIDA-VAZIA"},
		{"definida so com espacos", false, true, "   ", true, "DEFINIDA-VAZIA"},
		{"nao definida (config in-process)", false, true, "", false, "nao esta definida"},
		// O gate REAL do read-path é a CONJUNÇÃO registo ∧ WORM (api.go, newReadGovernance).
		// Com o registo configurado e o WORM ausente, o nó serve o read-path LEGADO: o aviso
		// tem de sair na mesma, senão o banner anunciaria uma soberania que não aplica — a
		// promessa falsa que este ticket vem eliminar.
		{"registo composto mas WORM ausente", true, false, "board:prod=eu", true, "WORM nao esta composto"},
	}
	for _, c := range cases {
		lines := sovereignReadKillSwitchBanner(c.regions, c.worm, c.raw, c.defined)
		if len(lines) == 0 {
			t.Fatalf("%s: devia produzir aviso", c.name)
		}
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, c.wantSubstr) {
			t.Errorf("%s: o aviso devia nomear a causa (%q), veio:\n%s", c.name, c.wantSubstr, joined)
		}
		for _, marker := range killSwitchMarkers {
			if !strings.Contains(joined, marker) {
				t.Errorf("%s: falta o marcador %q no aviso:\n%s", c.name, marker, joined)
			}
		}
	}
}

// runWithCleanEnv corre [run] com um ambiente FIXADO e AOS_BOARD_REGIONS no valor dado.
// Devolve o banner. Não abre socket: AOS_API_ADDR fica vazia.
func runWithCleanEnv(t *testing.T, boardRegions string) string {
	t.Helper()
	t.Setenv("AOS_BOARD_REGIONS", boardRegions)
	return runWithoutTouchingBoardRegions(t)
}

// runWithoutTouchingBoardRegions é a metade de [runWithCleanEnv] que NÃO mexe em
// AOS_BOARD_REGIONS — necessária para o caso "variável ausente", que t.Setenv não sabe
// exprimir.
//
// Fixa TODA a superfície de ambiente do nó excepto AOS_BOARD_REGIONS (a variável sob teste).
// Sem isto, uma variável herdada da máquina de quem corre os testes entraria no banner —
// `AOS_ISSUER_ID` aparece literalmente na primeira linha, e um `AOS_HUMANS=","` no ambiente
// do developer faria [run] devolver ErrNoHumans e este helper falhar com uma mensagem
// enganadora sobre produção.
//
// A LISTA É DERIVADA, NÃO ESCRITA À MÃO (correcção da auditoria da W0: o comentário afirmava
// exaustividade sobre uma lista de 14 nomes, e o nó lê mais de 50 — AOS_MODEL_*,
// AOS_AUTONOMY_LEVELS, AOS_POLICY_BUNDLE_DIR e AOS_BREAKER_* ficavam de fora, tornando os
// testes de banner dependentes do ambiente da máquina). Reusa-se o MESMO extractor AST do
// gate de AOS-203 ([envVarsReadBySources]) que já conhece a superfície completa: uma variável
// nova passa a ser fixada automaticamente, sem ninguém se lembrar de a acrescentar aqui.
func runWithoutTouchingBoardRegions(t *testing.T) string {
	t.Helper()

	// (1) LIMPA tudo o que o nó lê, menos a variável sob teste. Todos os nomes da superfície
	// são AOS_* (o gate de AOS-203 fá-lo-ia falhar se não fossem documentados no README),
	// pelo que isto não toca no ambiente da máquina fora do prefixo do produto.
	for name := range envVarsReadBySources(t, envSourceRoots) {
		if name == "AOS_BOARD_REGIONS" {
			continue
		}
		t.Setenv(name, "")
	}
	// (2) REPÕE os poucos valores sem os quais [run] não arranca — ou arrancaria a dizer
	// outra coisa que não a postura sob teste.
	t.Setenv("AOS_ISSUER_ID", "iss:aos-node") // o default — o issuer é ecoado no banner.
	t.Setenv("AOS_HUMANS", "operator")        // o default — uma lista sem entradas válidas abortaria.

	var sb strings.Builder
	if err := run(&sb); err != nil {
		t.Fatalf("run devia arrancar fora de producao, veio: %v", err)
	}
	return sb.String()
}
