package routingtests

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/weights"
	"github.com/aos-ref/platform/model-gateway/routing/router"
	"github.com/aos-ref/platform/model-gateway/routing/scoring"
	"github.com/aos-ref/platform/model-gateway/routing/sovereignty"
	"github.com/aos-ref/platform/model-gateway/routing/tiering"
)

// ===========================================================================
// AOS-269 / ADR-021 — SCORING PONDERADO DETERMINÍSTICO.
//
// Este ficheiro ALARGA a suite adversarial de AOS-063 aos cenários que o scoring
// introduz. Continua a NÃO reimplementar controlo nenhum: orquestra o router REAL
// com o scorer REAL (routing/scoring) e a tabela de pesos REAL (policy/weights,
// assinada + trust anchor pinado). Os três cenários novos provam, por adversário:
//
//  6. GUARDAS PRIMEIRO — nenhum peso, por mais extremo, elege um candidato
//     cross-border ou um modelo fora da allowlist do board (ADR-021 regra 1);
//  7. FUNÇÃO PURA — a decisão pontuada é função pura dos inputs (sem rand, sem
//     relógio, sem floats: provado por REPETIÇÃO e por análise AST do código do
//     caminho de decisão) e o replay reproduz o mesmo candidato e o mesmo score;
//  8. FAIL-CLOSED DOS PESOS — com o scoring COMPOSTO mas sem tabela válida/assinada,
//     o router RECUSA (nunca pesos implícitos) — ADR-021 regra 3.
//
// Cada um tem o seu META-TESTE de não-vacuidade (mais abaixo).
// ===========================================================================

// rogueModel é um modelo que NÃO está na allowlist regional de teste
// (testAllowlistJSON) — o isco do cenário 6: dá-se-lhe o melhor sinal possível em
// TODOS os factores e prova-se que continua a ser inelegível.
const rogueModel = "rogue-omni"

// scoringLadder é a escada de teste ACRESCIDA do tier "rogue" (modelo fora da
// allowlist, barato e Frontier — o candidato que um score ganancioso elegeria).
func scoringLadder() *tiering.Ladder {
	return tiering.NewLadder(
		tiering.Tier{Name: "economy", Model: modelEconomy, CostRank: 1, Capability: tiering.CapabilityBasic},
		tiering.Tier{Name: "basic-fast", Model: modelBasicFast, CostRank: 2, Capability: tiering.CapabilityBasic, Fast: true},
		tiering.Tier{Name: "standard", Model: modelStandard, CostRank: 3, Capability: tiering.CapabilityStandard},
		tiering.Tier{Name: "standard-fast", Model: modelStdFast, CostRank: 4, Capability: tiering.CapabilityStandard, Fast: true},
		tiering.Tier{Name: "frontier-batch", Model: modelFrontBatch, CostRank: 5, Capability: tiering.CapabilityFrontier},
		tiering.Tier{Name: "frontier", Model: modelFrontier, CostRank: 6, Capability: tiering.CapabilityFrontier, Fast: true},
		// FORA DA ALLOWLIST: barato E frontier E rápido — o melhor candidato possível
		// para qualquer perfil de pesos. Nunca pode ser eleito.
		tiering.Tier{Name: "rogue", Model: rogueModel, CostRank: 0, Capability: tiering.CapabilityFrontier, Fast: true},
	)
}

// greedyTableJSON é uma tabela de pesos VÁLIDA e assinada (com a chave de teste)
// desenhada para ser MÁXIMAMENTE gananciosa: o perfil "adversario" põe todo o peso
// em health + task-fit, os dois factores que o cenário 6 satura a favor do
// candidato PROIBIDO. Se as guardas fossem "factores", este perfil elegia-o.
const greedyTableJSON = `{"version":"aos269-adversarial/v1","semver":"1.0.0","default_profile":"adversario","profiles":[` +
	`{"name":"adversario","weights":{"health":500,"headroom":0,"cost":0,"latency":0,"task_fit":500,"stability":0}},` +
	`{"name":"barato","weights":{"health":0,"headroom":0,"cost":1000,"latency":0,"task_fit":0,"stability":0}}]}`

// signTable assina uma tabela de pesos com uma seed DETERMINISTA de teste e
// carrega-a pelo caminho de verificação REAL (weights.LoadSignedTable) — a mesma
// verificação de assinatura da produção; só o anchor é de teste (o anchor PINADO é
// exigido por LoadTable, exercitado noutro teste).
func signTable(t *testing.T, seedLabel, tableJSON string) *weights.Table {
	t.Helper()
	var seed [32]byte
	copy(seed[:], []byte(seedLabel))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	dig, err := weights.Digest([]byte(tableJSON))
	if err != nil {
		t.Fatalf("Digest da tabela de pesos: %v", err)
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(dig)))
	tab, err := weights.LoadSignedTable([]byte(tableJSON), sig, pub)
	if err != nil {
		t.Fatalf("LoadSignedTable: %v", err)
	}
	return tab
}

// scorerOver constrói o scorer REAL sobre uma tabela assinada, com as SEIS portas de
// factor ligadas às impls de referência determinísticas. O headroom é derivado da
// porta de CARGA já existente do harness (router.HeadroomReaderFrom) — o scoring não
// tem fonte de carga própria.
func (h *harness) scorerOver(t *testing.T, tab *weights.Table, profile string, extra ...scoring.Option) *scoring.Scorer {
	t.Helper()
	opts := []scoring.Option{
		scoring.WithCost(scoring.CostFromLadder(h.ladder)),
		scoring.WithHeadroom(scoring.HeadroomFromReader(router.HeadroomReaderFrom(h.load))),
		scoring.WithLatency(scoring.NewStaticLatency(true)),
		scoring.WithHealth(scoring.NewStaticHealth(500)),
		scoring.WithTaskFit(scoring.NewStaticTaskFit(0)),
		scoring.WithStability(scoring.NewStaticStability(500)),
	}
	s, err := scoring.NewScorer(scoring.TableFrom(tab), profile, append(opts, extra...)...)
	if err != nil {
		t.Fatalf("NewScorer: %v", err)
	}
	return s
}

// ===========================================================================
// CENÁRIO 6 — GUARDAS PRIMEIRO: nenhum peso elege cross-border nem fora da allowlist.
// ===========================================================================

func TestScenario6_Scoring_GuardsFirstNeverElectsCrossBorderOrNonAllowlisted(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.ladder = scoringLadder() // acrescenta o tier PROIBIDO (fora da allowlist)

	// O adversário satura os dois factores que o perfil pesa, a favor do candidato
	// PROIBIDO: saúde perfeita em us-east (cross-border) e task-fit perfeito para o
	// modelo fora da allowlist; tudo o que é LEGAL fica com o pior sinal possível.
	health := scoring.NewStaticHealth(0).
		Set(provider, regUSEast, scoring.Scale).
		Set(provider, regEUWest, 0).
		Set(provider, regEUCentral, 0)
	fit := scoring.NewStaticTaskFit(0).
		Set(rogueModel, tiering.CapabilityFrontier, scoring.Scale).
		Set(modelFrontier, tiering.CapabilityFrontier, 0).
		Set(modelFrontBatch, tiering.CapabilityFrontier, 0)
	sc := h.scorerOver(t, signTable(t, "aos269-adversarial-weights-seed!", greedyTableJSON), "adversario",
		scoring.WithHealth(health), scoring.WithTaskFit(fit))
	r := h.router(router.WithScoring(sc))

	// (A) O ÚNICO candidato com capacidade é cross-border: REJEITA. O score de 1000
	// em saúde não o ressuscita — ele nunca chega à lista pontuada.
	dec := mustRoute(t, r, req(regEUWest, tiering.CapabilityFrontier, tiering.ClassInteractive, 10, ep("k-us", regUSEast)))
	if dec.Outcome != router.OutcomeRejected {
		t.Fatalf("cross-border com score máximo: esperava REJECT, obtive %s (region=%s model=%s score=%d)",
			dec.Outcome, dec.Region, dec.Model, dec.Score)
	}
	if len(dec.Dropped) != 1 || dec.Dropped[0].Region != regUSEast {
		t.Fatalf("o candidato cross-border devia ser DESCARTADO pela guarda, obtive %v", dec.Dropped)
	}
	if !strings.Contains(dec.Reason, "soberania") {
		t.Fatalf("a rejeição não atribui a soberania: %q", dec.Reason)
	}

	// (B) Com um candidato intra-fronteira ao lado do cross-border, a rota é INTRA —
	// apesar de o intra ter saúde 0 e o cross-border saúde 1000.
	dec = mustRoute(t, r, req(regEUWest, tiering.CapabilityFrontier, tiering.ClassInteractive, 10,
		ep("k-euw", regEUWest), ep("k-us", regUSEast)))
	if dec.Outcome == router.OutcomeRejected || dec.Region != regEUWest {
		t.Fatalf("esperava rota INTRA-fronteira (%s), obtive %s(region=%s)", regEUWest, dec.Outcome, dec.Region)
	}
	if !dec.Scored {
		t.Fatal("a decisão devia estar marcada como pontuada (Scored)")
	}

	// (C) O modelo FORA DA ALLOWLIST tem task-fit 1000 (o único factor que ainda pesa
	// dentro da fronteira) e é o mais barato e o mais rápido — e mesmo assim NUNCA é
	// escolhido: a allowlist do board é guarda, não factor.
	if dec.Model == rogueModel {
		t.Fatalf("um modelo FORA da allowlist foi eleito pelo score (%s) — a guarda virou factor", dec.Model)
	}
	// ... e continua fora mesmo com o perfil que pesa só o custo (onde ele é o melhor).
	scCheap := h.scorerOver(t, signTable(t, "aos269-adversarial-weights-seed!", greedyTableJSON), "barato",
		scoring.WithHealth(health), scoring.WithTaskFit(fit))
	dec2 := mustRoute(t, h.router(router.WithScoring(scCheap)),
		req(regEUWest, tiering.CapabilityFrontier, tiering.ClassBatch, 10, ep("k-euw", regEUWest)))
	if dec2.Model == rogueModel {
		t.Fatalf("perfil 'barato' elegeu o modelo fora da allowlist (%s)", dec2.Model)
	}
	if dec2.Outcome == router.OutcomeRejected {
		t.Fatalf("devia existir rota LEGAL: %s / %s", dec2.Outcome, dec2.Reason)
	}

	// (D) OBSERVABILIDADE (AC4): a decisão pontuada regista perfil, versão dos pesos,
	// score e factores no DecisionSink — e a razão traz tudo isso (é ela que a
	// pipeline propaga para a variância model_swap).
	last, ok := h.sink.last()
	if !ok {
		t.Fatal("a decisão pontuada não foi registada no DecisionSink")
	}
	if last.ScoreProfile != "barato" || last.WeightsVersion == "" {
		t.Fatalf("perfil/versão dos pesos não registados: perfil=%q versao=%q", last.ScoreProfile, last.WeightsVersion)
	}
	for _, want := range []string{"perfil=barato", "pesos=aos269-adversarial/v1#", "score=", "factores[health="} {
		if !strings.Contains(last.Reason, want) {
			t.Fatalf("a razão registada não contém %q: %q", want, last.Reason)
		}
	}
}

// ===========================================================================
// CENÁRIO 7 — FUNÇÃO PURA: sem rand, sem relógio, sem floats; replay byte-a-byte.
// ===========================================================================

func TestScenario7_Scoring_PureFunctionDeterministicNoFloats(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.ladder = scoringLadder()
	// Carga assimétrica entre as duas regiões intra: o headroom é um factor REAL a
	// discriminar (e não um empate que esconderia não-determinismo).
	h.load.Set(provider, regEUWest, router.Headroom{WorstUsed: 10, WorstLimit: 100})
	h.load.Set(provider, regEUCentral, router.Headroom{WorstUsed: 70, WorstLimit: 100})
	sc := h.scorerOver(t, mustEmbeddedTable(t), "balanced",
		scoring.WithTaskFit(scoring.NewStaticTaskFit(0).
			Set(modelStandard, tiering.CapabilityStandard, 800).
			Set(modelStdFast, tiering.CapabilityStandard, 400)))
	r := h.router(router.WithScoring(sc))
	rq := req(regEUWest, tiering.CapabilityStandard, tiering.ClassInteractive, 10,
		ep("k-euc", regEUCentral), ep("k-euw", regEUWest))

	first := mustRoute(t, r, rq)
	if !first.Scored || first.Outcome == router.OutcomeRejected {
		t.Fatalf("esperava rota pontuada, obtive %s (%s)", first.Outcome, first.Reason)
	}
	// REPETIÇÃO: mesmos inputs ⇒ MESMO candidato, MESMO score, MESMOS factores,
	// MESMA razão. É a condição do replay (ADR-010) para uma decisão ponderada.
	for i := 0; i < 64; i++ {
		got := mustRoute(t, r, rq)
		if got.Model != first.Model || got.Tier != first.Tier || got.Region != first.Region {
			t.Fatalf("iteração %d divergiu do candidato: %s/%s/%s != %s/%s/%s",
				i, got.Model, got.Tier, got.Region, first.Model, first.Tier, first.Region)
		}
		if got.Score != first.Score || got.ScoreFactors != first.ScoreFactors {
			t.Fatalf("iteração %d divergiu do score: %d%s != %d%s", i, got.Score, got.ScoreFactors, first.Score, first.ScoreFactors)
		}
		if got.Reason != first.Reason || got.ScoreProfile != first.ScoreProfile || got.WeightsVersion != first.WeightsVersion {
			t.Fatalf("iteração %d divergiu da razão/perfil/versão", i)
		}
	}
	// A ORDEM dos candidatos na lista não muda a decisão (desempate total estável).
	rev := req(regEUWest, tiering.CapabilityStandard, tiering.ClassInteractive, 10,
		ep("k-euw", regEUWest), ep("k-euc", regEUCentral))
	if got := mustRoute(t, r, rev); got.Model != first.Model || got.Region != first.Region || got.Score != first.Score {
		t.Fatalf("a decisão depende da ORDEM dos candidatos: %s@%s(%d) != %s@%s(%d)",
			got.Model, got.Region, got.Score, first.Model, first.Region, first.Score)
	}

	// ZERO FLOATS / ZERO RAND / ZERO RELÓGIO no caminho de decisão — provado sobre o
	// CÓDIGO (AST), não por inspecção humana: um float64 introduzido no scorer, na
	// tabela de pesos ou no router faz este teste ficar vermelho. A LISTA DE
	// DIRECTÓRIOS é DERIVADA do fecho transitivo de imports do router (ver
	// [decisionPathDirs]) e não fixada à mão: um pacote NOVO no caminho de decisão
	// passa a ser analisado sem ninguém se lembrar de o acrescentar aqui.
	dirs := decisionPathDirs(t, "../routing/router")
	if len(dirs) < 5 {
		t.Fatalf("fecho de imports do caminho de decisão suspeitosamente pequeno (%d): %v", len(dirs), dirs)
	}
	for _, dir := range dirs {
		if viol := scanDeterminismViolations(t, dir); len(viol) > 0 {
			t.Fatalf("caminho de decisão contaminado em %s: %s", dir, strings.Join(viol, "; "))
		}
	}
	// NÃO-VACUIDADE, UM CASO POR REGRA. Um scanner partido numa regra qualquer daria
	// verde-vazio sobre o caminho de decisão, e o self-check antigo (só floats
	// contra metering/cost) não o apanharia. Cada fixture abaixo viola EXACTAMENTE
	// uma regra e TEM de ser acusada.
	for _, probe := range []struct{ name, src, want string }{
		{"float_literal", "package p\n\nvar X = 1.5\n", "flutuante"},
		{"float_type", "package p\n\nfunc F(v float64) float64 { return v }\n", "float64"},
		{"rand", "package p\n\nimport \"math/rand\"\n\nfunc F() int { return rand.Intn(3) }\n", "rand"},
		{"clock_now", "package p\n\nimport \"time\"\n\nfunc F() int64 { return time.Now().Unix() }\n", "time.Now"},
		{"clock_alias", "package p\n\nimport clk \"time\"\n\nfunc F() int64 { return clk.Now().Unix() }\n", "Now"},
		{"clock_since", "package p\n\nimport \"time\"\n\nfunc F(t time.Time) time.Duration { return time.Since(t) }\n", "time.Since"},
		{"math_pkg", "package p\n\nimport \"math\"\n\nfunc F() uint64 { return math.Float64bits(2) }\n", "math"},
		{"gen_sem_build_ignore", "package p\n\nvar X = 2.5\n", "flutuante"},
	} {
		dir := t.TempDir()
		name := "probe.go"
		if probe.name == "gen_sem_build_ignore" {
			// Um ficheiro `gen_*.go` SEM `//go:build ignore` ENTRA no binário: a
			// heurística antiga saltava-o pelo prefixo do nome e deixava-o passar.
			name = "gen_helper.go"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(probe.src), 0o600); err != nil {
			t.Fatalf("escrever fixture %s: %v", probe.name, err)
		}
		viol := scanDeterminismViolations(t, dir)
		if len(viol) == 0 {
			t.Fatalf("guard de determinismo vácuo na regra %q: nada detectado", probe.name)
		}
		if !strings.Contains(strings.Join(viol, "; "), probe.want) {
			t.Fatalf("regra %q detectada pela razão errada: %v", probe.name, viol)
		}
	}
	// ... e um ficheiro `gen_*.go` COM `//go:build ignore` continua (correctamente)
	// fora da análise: é ferramenta offline, não entra no binário.
	{
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "gen_offline.go"),
			[]byte("//go:build ignore\n\npackage main\n\nvar X = 1.5\n"), 0o600); err != nil {
			t.Fatalf("escrever fixture gen_offline: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "real.go"), []byte("package p\n\nvar Y = 1\n"), 0o600); err != nil {
			t.Fatalf("escrever fixture real: %v", err)
		}
		if viol := scanDeterminismViolations(t, dir); len(viol) > 0 {
			t.Fatalf("um gen_*.go com //go:build ignore não devia ser analisado: %v", viol)
		}
	}
	// NÃO-VACUIDADE clássica (mantida): um pacote que LEGITIMAMENTE usa vírgula
	// flutuante (metering/cost — contabilidade em USD, FORA do caminho de decisão).
	if viol := scanDeterminismViolations(t, "../metering/cost"); len(viol) == 0 {
		t.Fatal("guard de determinismo vácuo: não detectou floats num pacote que sabidamente os usa")
	}
}

// modulePrefix é o caminho de import do módulo do GW. O fecho de imports do caminho
// de decisão pára aqui: pacotes de OUTROS módulos (ex.: kernel/agent-runtime, a
// porta de tracing) não são código deste caminho e têm os seus próprios gates.
const modulePrefix = "github.com/aos-ref/platform/model-gateway/"

// decisionPathDirs DERIVA o caminho de decisão do grafo de imports, em vez de o
// fixar numa lista literal que envelhece em silêncio. Parte do pacote do router e
// fecha transitivamente sobre os imports DENTRO do módulo, devolvendo directórios
// relativos ordenados (determinístico).
//
// PORQUE DERIVAR. A lista literal anterior ("../routing/scoring", …) era correcta
// no dia em que foi escrita e passava a estar incompleta no dia em que alguém
// acrescentasse um pacote ao caminho de decisão — sem nada ficar vermelho. Com o
// fecho de imports, o guard cresce com o código: o único modo de escapar passa a
// ser não estar no caminho de decisão de todo.
func decisionPathDirs(t *testing.T, root string) []string {
	t.Helper()
	fset := token.NewFileSet()
	seen := map[string]bool{}
	queue := []string{filepath.ToSlash(filepath.Clean(root))}
	var out []string
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		if seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("ler %s no fecho de imports: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly|parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse de %s no fecho de imports: %v", name, err)
			}
			for _, imp := range f.Imports {
				if imp.Path == nil {
					continue
				}
				p := strings.Trim(imp.Path.Value, `"`)
				if !strings.HasPrefix(p, modulePrefix) {
					continue
				}
				queue = append(queue, "../"+strings.TrimPrefix(p, modulePrefix))
			}
		}
	}
	sort.Strings(out)
	return out
}

// forbiddenImports são os pacotes que, sozinhos, contaminam o caminho de decisão:
// aleatoriedade em qualquer forma e aritmética não-inteira. `math` entra na lista
// (era um buraco: `math.Float64bits`/`math.Sqrt` não têm literal float nem
// identificador de tipo float, logo escapavam às outras regras); `math/bits` NÃO,
// porque é aritmética inteira pura.
var forbiddenImports = map[string]string{
	"math/rand":    "aleatoriedade",
	"math/rand/v2": "aleatoriedade",
	"crypto/rand":  "aleatoriedade",
	"math/big":     "aritmética não-inteira",
	"math":         "aritmética de vírgula flutuante",
}

// clockSelectors são as leituras de RELÓGIO proibidas no caminho de decisão. Não
// se proíbe o import de `time` — o router usa legitimamente `time.Duration` no
// retry_after — proíbe-se LER o relógio, que é o que quebra o replay (ADR-010).
var clockSelectors = map[string]bool{"Now": true, "Since": true, "Until": true}

// scanDeterminismViolations analisa (go/ast, stdlib) os ficheiros de um pacote que
// ENTRAM NO BINÁRIO e devolve as violações do determinismo do ADR-021: literais de
// vírgula flutuante, tipos float/complex, imports proibidos ([forbiddenImports]) e
// leituras de relógio ([clockSelectors]).
//
// DUAS CORRECÇÕES DE EVASÃO face à primeira versão (auditoria adversarial):
//
//   - um ficheiro `gen_*.go` deixa de ser saltado PELO NOME. A razão para o saltar
//     é ser ferramenta OFFLINE, e o que o torna offline é a directiva
//     `//go:build ignore` — que agora se VERIFICA. Um `gen_helper.go` sem a
//     directiva entra no binário e passa a ser analisado como qualquer outro;
//   - os selectores de relógio são resolvidos contra o ALIAS LOCAL do import
//     (`import clk "time"` ⇒ `clk.Now` é apanhado), em vez de casarem o
//     identificador literal `time`.
//
// Os testes do próprio pacote continuam fora (não entram no binário).
func scanDeterminismViolations(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ler %s: %v", dir, err)
	}
	var viol []string
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse de %s: %v", name, err)
		}
		if hasBuildIgnore(file) {
			continue // ferramenta offline PROVADA (directiva verificada, não presumida)
		}
		scanned++
		// Aliases locais dos imports sensíveis: é por eles que os selectores são
		// resolvidos (um alias não pode servir de disfarce).
		clockAliases := map[string]bool{}
		randAliases := map[string]bool{}
		for _, imp := range file.Imports {
			if imp.Path == nil {
				continue
			}
			p := strings.Trim(imp.Path.Value, `"`)
			if why, bad := forbiddenImports[p]; bad {
				viol = append(viol, name+": importa "+p+" ("+why+")")
			}
			local := p[strings.LastIndex(p, "/")+1:]
			if imp.Name != nil {
				local = imp.Name.Name
			}
			switch p {
			case "time":
				clockAliases[local] = true
			case "math/rand", "math/rand/v2", "crypto/rand":
				randAliases[local] = true
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BasicLit:
				if v.Kind == token.FLOAT || v.Kind == token.IMAG {
					viol = append(viol, name+": literal de vírgula flutuante "+v.Value)
				}
			case *ast.Ident:
				switch v.Name {
				case "float32", "float64", "complex64", "complex128":
					viol = append(viol, name+": tipo "+v.Name+" no caminho de decisão")
				}
			case *ast.SelectorExpr:
				x, ok := v.X.(*ast.Ident)
				if !ok {
					return true
				}
				if randAliases[x.Name] || x.Name == "rand" {
					viol = append(viol, name+": uso de "+x.Name+"."+v.Sel.Name+" (aleatoriedade)")
				}
				if (clockAliases[x.Name] || x.Name == "time") && clockSelectors[v.Sel.Name] {
					viol = append(viol, name+": leitura de relógio "+x.Name+"."+v.Sel.Name)
				}
			}
			return true
		})
	}
	if scanned == 0 {
		// Guarda anti-vacuidade do próprio guard: um caminho errado tornaria este
		// teste verde-vazio (nada analisado = nada encontrado).
		t.Fatalf("nenhum ficheiro analisado em %s — o guard de determinismo seria vácuo", dir)
	}
	return viol
}

// hasBuildIgnore reporta se o ficheiro traz a directiva `//go:build ignore` — a
// PROVA de que é ferramenta offline e não entra no binário. Verificada, nunca
// presumida a partir do nome do ficheiro.
func hasBuildIgnore(f *ast.File) bool {
	for _, g := range f.Comments {
		for _, c := range g.List {
			if strings.HasPrefix(c.Text, "//go:build ") && strings.Contains(c.Text, "ignore") {
				return true
			}
		}
	}
	return false
}

// mustEmbeddedTable carrega a tabela de pesos EMBEBIDA e ASSINADA de produção
// (trust anchor PINADO) — o caminho real, não uma fixture.
func mustEmbeddedTable(t *testing.T) *weights.Table {
	t.Helper()
	tab, err := weights.LoadTable()
	if err != nil {
		t.Fatalf("weights.LoadTable (artefacto embebido assinado): %v", err)
	}
	return tab
}

// ===========================================================================
// CENÁRIO 8 — FAIL-CLOSED DOS PESOS: scoring composto sem tabela válida ⇒ RECUSA.
// ===========================================================================

func TestScenario8_Scoring_FailClosedWithoutSignedWeights(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	// (A) Sem tabela NÃO há scorer: o construtor recusa (não existe caminho para um
	// scorer com "pesos por omissão").
	if _, err := scoring.NewScorer(scoring.TableFrom(nil), "balanced"); err == nil {
		t.Fatal("NewScorer sem tabela devia falhar fail-closed")
	}

	// (B) Uma tabela ADULTERADA não carrega (assinatura sobre o digest) — logo não
	// existe scorer possível sobre ela. É aqui que a regra 3 se torna material: mudar
	// um peso sem reassinar deixa o gateway sem tabela válida.
	tampered := strings.Replace(greedyTableJSON, `"cost":1000`, `"cost":1`, 1)
	var seed [32]byte
	copy(seed[:], []byte("aos269-adversarial-weights-seed!"))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	dig, err := weights.Digest([]byte(greedyTableJSON))
	if err != nil {
		t.Fatal(err)
	}
	origSig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(dig)))
	if _, err := weights.LoadSignedTable([]byte(tampered), origSig, pub); err == nil {
		t.Fatal("tabela adulterada com a assinatura antiga devia ser recusada")
	}

	// (C) DEFESA EM PROFUNDIDADE: mesmo que alguém contorne o construtor e componha um
	// scorer NÃO-ARMADO (valor-zero), o router RECUSA cada rota em vez de rotear com
	// pesos implícitos — e a razão nomeia a causa (atribuível).
	r := h.router(router.WithScoring(&scoring.Scorer{}))
	dec := mustRoute(t, r, req(regEUWest, tiering.CapabilityBasic, tiering.ClassBatch, 10))
	if dec.Outcome != router.OutcomeRejected {
		t.Fatalf("scoring sem tabela devia REJEITAR, obtive %s (model=%s)", dec.Outcome, dec.Model)
	}
	if !strings.Contains(dec.Reason, "tabela de pesos") {
		t.Fatalf("a rejeição não nomeia a tabela de pesos ausente: %q", dec.Reason)
	}
	if dec.Scored {
		t.Fatal("uma rejeição por falta de pesos não pode marcar-se como pontuada")
	}

	// (D) COMPATIBILIDADE DECLARADA (postura deste ticket): o MESMO router SEM
	// WithScoring continua a rotear pelo caminho lexicográfico de AOS-059 — um nó já
	// implantado, sem tabela de pesos, NÃO deixa de rotear.
	legacy := mustRoute(t, h.router(), req(regEUWest, tiering.CapabilityBasic, tiering.ClassBatch, 10))
	if legacy.Outcome != router.OutcomeRouted || legacy.Model != modelEconomy {
		t.Fatalf("sem scoring composto o router tem de manter o comportamento AOS-059 (economy), obtive %s/%s",
			legacy.Outcome, legacy.Model)
	}
	if legacy.Scored || legacy.ScoreProfile != "" || legacy.WeightsVersion != "" {
		t.Fatal("o modo compatível não pode reportar campos de scoring")
	}
}

// ===========================================================================
// META-TESTES do scoring (não-vacuidade). Com o controlo CONTORNADO, o ataque PASSA.
// ===========================================================================

// META 9 — com a soberania e a allowlist CONTORNADAS no router, os MESMOS pesos
// gananciosos elegem o candidato cross-border. Prova que o cenário 6 detecta as
// guardas (e não um acaso dos pesos escolhidos).
func TestMetaDetects_ScoringElectsCrossBorderWhenGuardsBypassed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.ladder = scoringLadder()
	health := scoring.NewStaticHealth(0).
		Set(provider, regUSEast, scoring.Scale).
		Set(provider, regEUWest, 0)
	fit := scoring.NewStaticTaskFit(0).Set(rogueModel, tiering.CapabilityFrontier, scoring.Scale)
	sc := h.scorerOver(t, signTable(t, "aos269-adversarial-weights-seed!", greedyTableJSON), "adversario",
		scoring.WithHealth(health), scoring.WithTaskFit(fit))
	collapsed := sovereignty.NewGuard(
		sovereignty.WithBoundary(regEUWest, "global"),
		sovereignty.WithBoundary(regUSEast, "global"), // fronteira colapsada
	)
	r := router.New(h.ladder,
		router.WithGuard(collapsed),
		router.WithAllowlist(allowAll{}), // allowlist contornada
		router.WithLoadProvider(h.load),
		router.WithAdmission(h.adm),
		router.WithBudget(h.budget),
		router.WithKeyPool(h.keys),
		router.WithScoring(sc),
	)
	dec := mustRoute(t, r, req(regEUWest, tiering.CapabilityFrontier, tiering.ClassInteractive, 10,
		ep("k-euw", regEUWest), ep("k-us", regUSEast)))
	if dec.Outcome == router.OutcomeRejected || dec.Region != regUSEast {
		t.Fatalf("meta não-vácua falhou: com as guardas contornadas esperava rota cross-border para %s, obtive %s(region=%s)",
			regUSEast, dec.Outcome, dec.Region)
	}
	if dec.Model != rogueModel {
		t.Fatalf("meta não-vácua falhou: com a allowlist contornada esperava o modelo proibido %s, obtive %s", rogueModel, dec.Model)
	}
}

// META 10 — a ASSINATURA da tabela é LOAD-BEARING: com pesos trocados (o que um
// atacante conseguiria se a assinatura fosse ignorada), a MESMA situação elege outro
// modelo. Prova que o fail-closed do cenário 8 protege uma decisão que de facto muda
// — não é um controlo decorativo sobre um artefacto irrelevante.
func TestMetaDetects_TamperedWeightsFlipDecisionWhenSignatureIgnored(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	// Duas tabelas VÁLIDAS e assinadas que diferem APENAS nos pesos: a honesta pesa só
	// o custo; a "adulterada" (assinada por quem a adulterou — o cenário que o trust
	// anchor impede) pesa só o task-fit.
	honest := signTable(t, "aos269-meta-weights-honest-seed!!", greedyTableJSON)
	fit := scoring.NewStaticTaskFit(0).
		Set(modelStdFast, tiering.CapabilityStandard, scoring.Scale).
		Set(modelStandard, tiering.CapabilityStandard, 0)

	cheapDec := mustRoute(t, h.router(router.WithScoring(h.scorerOver(t, honest, "barato", scoring.WithTaskFit(fit)))),
		req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 10, ep("k-euw", regEUWest)))
	qualityDec := mustRoute(t, h.router(router.WithScoring(h.scorerOver(t, honest, "adversario", scoring.WithTaskFit(fit)))),
		req(regEUWest, tiering.CapabilityStandard, tiering.ClassBatch, 10, ep("k-euw", regEUWest)))

	if cheapDec.Model == qualityDec.Model {
		t.Fatalf("meta não-vácua falhou: trocar os pesos tinha de MUDAR a decisão, ambos deram %s", cheapDec.Model)
	}
	if cheapDec.Model != modelStandard {
		t.Fatalf("meta: com peso só no custo esperava o standard barato, obtive %s", cheapDec.Model)
	}
	if qualityDec.Model != modelStdFast {
		t.Fatalf("meta: com peso só no task-fit esperava o std-fast (task-fit 1000), obtive %s", qualityDec.Model)
	}
}

// ---------------------------------------------------------------------------
// PROBES do relatório da suite (AOS_ROUTING_REPORT) — sem *testing.T.
// ---------------------------------------------------------------------------

// probeScoringGuardsFirst: com pesos gananciosos a favor do cross-border, a rota
// cross-border NÃO acontece (rejeita) — a guarda vence o score.
func probeScoringGuardsFirst() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	h.ladder = scoringLadder()
	tab, err := loadAdversarialTable()
	if err != nil {
		return false
	}
	sc, err := scoring.NewScorer(scoring.TableFrom(tab), "adversario",
		scoring.WithHealth(scoring.NewStaticHealth(0).Set(provider, regUSEast, scoring.Scale)),
		scoring.WithTaskFit(scoring.NewStaticTaskFit(0).Set(rogueModel, tiering.CapabilityFrontier, scoring.Scale)))
	if err != nil {
		return false
	}
	dec, err := h.router(router.WithScoring(sc)).Route(context.Background(),
		req(regEUWest, tiering.CapabilityFrontier, tiering.ClassInteractive, 10, ep("k-us", regUSEast)))
	return err == nil && dec.Outcome == router.OutcomeRejected
}

// probeScoringFailClosedWeights: scoring composto sem tabela válida ⇒ REJECT.
func probeScoringFailClosedWeights() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	dec, err := h.router(router.WithScoring(&scoring.Scorer{})).Route(context.Background(),
		req(regEUWest, tiering.CapabilityBasic, tiering.ClassBatch, 10))
	return err == nil && dec.Outcome == router.OutcomeRejected && !dec.Scored
}

// probeScoringDeterministic: duas execuções com os mesmos sinais ⇒ mesmo candidato
// e mesmo score (replay).
func probeScoringDeterministic() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	tab, err := weights.LoadTable()
	if err != nil {
		return false
	}
	sc, err := scoring.NewScorer(scoring.TableFrom(tab), "balanced",
		scoring.WithCost(scoring.CostFromLadder(h.ladder)),
		scoring.WithHeadroom(scoring.HeadroomFromReader(router.HeadroomReaderFrom(h.load))),
		scoring.WithLatency(scoring.NewStaticLatency(true)))
	if err != nil {
		return false
	}
	r := h.router(router.WithScoring(sc))
	rq := req(regEUWest, tiering.CapabilityStandard, tiering.ClassInteractive, 10, ep("k-euw", regEUWest))
	a, err1 := r.Route(context.Background(), rq)
	b, err2 := r.Route(context.Background(), rq)
	return err1 == nil && err2 == nil && a.Scored && a.Model == b.Model && a.Score == b.Score && a.ScoreFactors == b.ScoreFactors
}

// probeScoringCompatLexicographic: sem scoring composto, o router mantém AOS-059.
func probeScoringCompatLexicographic() bool {
	h, err := newHarnessErr()
	if err != nil {
		return false
	}
	dec, err := h.router().Route(context.Background(), req(regEUWest, tiering.CapabilityBasic, tiering.ClassBatch, 10))
	return err == nil && dec.Outcome == router.OutcomeRouted && dec.Model == modelEconomy && !dec.Scored
}

// loadAdversarialTable assina+carrega a tabela adversarial pelo caminho de
// verificação real (sem *testing.T, para as probes do relatório).
func loadAdversarialTable() (*weights.Table, error) {
	var seed [32]byte
	copy(seed[:], []byte("aos269-adversarial-weights-seed!"))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
	dig, err := weights.Digest([]byte(greedyTableJSON))
	if err != nil {
		return nil, err
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(dig)))
	return weights.LoadSignedTable([]byte(greedyTableJSON), sig, pub)
}
