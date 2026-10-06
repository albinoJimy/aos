package plannerprompt

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// aos500_goldenset_guard_test.go — A EXCEPÇÃO DO PROMPT E O GOLDEN-SET ANDAM JUNTOS (AOS-500,
// revisão adversarial, M7).
//
// O AOS-500 subiu a linha do schema a 1.3.0 sem mexer no prompt nem no golden-set, e para isso
// trocou a regra das fixtures de «carimbam a linha corrente» por «carimbam 1.2.0»
// ([loadExtensionCandidate]). A regra antiga obrigava a mexer no golden-set sempre que a linha
// subia; a nova, sozinha, não obrigava a nada no dia em que o prompt passasse a pedir a origem
// de uma saída — o AOS-501 podia nomear `from_tool` no template, retirar a excepção
// [camposAindaForaDoPrompt], e o golden-set continuava sem um único plano com o campo.
//
// Esta guarda liga as duas coisas:
//
//   - ENQUANTO a excepção existir, nenhuma fixture do golden-set declara `from_tool` nem carimba
//     a linha 1.3.0 ou acima: o golden-set descreve o que o planeador é instruído a produzir, e
//     ele não é instruído a produzir isso;
//   - QUANDO a excepção for retirada, o golden-set tem de ter pelo menos um caso com `from_tool`
//     carimbado 1.3.0. Retirar a excepção sem o acrescentar falha aqui.
//
// O AOS-501 RETIROU A EXCEPÇÃO (o prompt 1.5.0 nomeia o campo), e acrescentou os casos
// `testdata/adr038-output-source/` e `testdata/adr038-reject/`. A guarda corre agora pelo
// segundo ramo, e quem lhe diz que a excepção já não existe é o próprio template
// ([excepcaoDoPromptActiva]): enquanto houver um prompt publicado que nomeie o campo, o
// golden-set tem de o medir.

// linhaDaOrigem é o `plan_version` que introduziu `from_tool` (AOS-500).
var linhaDaOrigem = plan.PlanVersion{Major: 1, Minor: 3, Patch: 0}

// fixtureDoGoldenSet é o que a guarda precisa de saber de um plano-candidato do golden-set.
type fixtureDoGoldenSet struct {
	caminho   string
	carimbo   plan.PlanVersion
	comOrigem bool
}

// fixturesDoGoldenSet lê TODOS os planos-candidatos de `testdata/<caso>/*.json`. Uma fixture que
// não descodifique falha o teste: a guarda não pode passar por não ter conseguido ler.
func fixturesDoGoldenSet(t *testing.T) []fixtureDoGoldenSet {
	t.Helper()
	caminhos, err := filepath.Glob(filepath.Join("testdata", "*", "*.json"))
	if err != nil {
		t.Fatalf("glob do golden-set: %v", err)
	}
	sort.Strings(caminhos)
	var out []fixtureDoGoldenSet
	for _, c := range caminhos {
		raw, err := os.ReadFile(c)
		if err != nil {
			t.Fatalf("ler %s: %v", c, err)
		}
		doc, err := plan.Decode(raw)
		if err != nil {
			t.Fatalf("a fixture %s nao descodifica, e a guarda nao a pode julgar: %v", c, err)
		}
		out = append(out, fixtureDoGoldenSet{caminho: filepath.ToSlash(c), carimbo: doc.PlanVersion, comOrigem: doc.DeclaresOutputSource()})
	}
	return out
}

// exigenciaDoGoldenSet é a regra, pura: recebe se a excepção do prompt para `from_tool` ainda
// existe e o retrato das fixtures, e devolve nil ou o que falta.
func exigenciaDoGoldenSet(excepcaoActiva bool, fixtures []fixtureDoGoldenSet) error {
	if excepcaoActiva {
		for _, f := range fixtures {
			if f.comOrigem {
				return fmt.Errorf("%s declara `from_tool` e o prompt ainda nao nomeia o campo (a excepcao camposAindaForaDoPrompt existe): o golden-set descreveria um plano que o planeador nao e instruido a produzir", f.caminho)
			}
			if f.carimbo.Major == linhaDaOrigem.Major && f.carimbo.Minor >= linhaDaOrigem.Minor {
				return fmt.Errorf("%s carimba %s e o prompt ainda manda carimbar ate 1.2.0 (a excepcao camposAindaForaDoPrompt existe)", f.caminho, f.carimbo)
			}
		}
		return nil
	}
	for _, f := range fixtures {
		if f.comOrigem && f.carimbo.Equal(linhaDaOrigem) {
			return nil
		}
	}
	return fmt.Errorf("a excepcao do prompt para `from_tool` foi retirada e o golden-set nao tem nenhum caso com `from_tool` carimbado %s: acrescenta-o em testdata/ (AOS-501) — sem ele o planeador passa a ser instruido a emitir um campo que nenhum caso mede", linhaDaOrigem)
}

// excepcaoDoPromptActiva diz se NENHUM prompt publicado por este módulo nomeia `from_tool`. Era
// a entrada `Output.from_tool` do mapa `camposAindaForaDoPrompt`, que o AOS-501 apagou; a
// pergunta faz-se agora aos templates, que são a verdade de que o mapa era um resumo.
func excepcaoDoPromptActiva() bool {
	return !strings.Contains(Current.Template, "from_tool") && !strings.Contains(WithOutputSource.Template, "from_tool")
}

// TestAOS500_OGoldenSetAcompanhaAExcepcaoDoPrompt aplica a regra ao golden-set e aos prompts
// reais. Desde o AOS-501 passa pelo segundo ramo: o prompt 1.5.0 nomeia o campo, e o golden-set
// tem de ter um caso com `from_tool` carimbado 1.3.0.
func TestAOS500_OGoldenSetAcompanhaAExcepcaoDoPrompt(t *testing.T) {
	fixtures := fixturesDoGoldenSet(t)
	if len(fixtures) < 10 {
		t.Fatalf("pre-condicao: o golden-set tem mais de dez planos-candidatos; a guarda leu %d", len(fixtures))
	}
	if excepcaoDoPromptActiva() {
		t.Fatal("nenhum prompt publicado nomeia from_tool: o AOS-501 publicou o 1.5.0, que o nomeia")
	}
	if err := exigenciaDoGoldenSet(excepcaoDoPromptActiva(), fixtures); err != nil {
		t.Fatal(err)
	}
	// E o caso que a guarda exige está no golden-set AVALIADO, não só em `testdata/`: um ficheiro
	// que nenhum caso carrega não mede nada.
	avaliados := map[string]bool{}
	for _, s := range adr022Samples(t) {
		for _, c := range s.Candidates {
			if c.DeclaresOutputSource() && c.PlanVersion.Equal(linhaDaOrigem) {
				avaliados[s.CaseID] = true
			}
		}
	}
	if !avaliados[caseOutputSource] || !avaliados[caseOutputSourceReject] || !avaliados[caseOutputSourceMixedReject] {
		t.Fatalf("os casos de ADR-038 tem de entrar na avaliacao do golden-set com planos que declaram a origem; entraram %v", avaliados)
	}
}

// TestAOS501_ARubricaDaOrigemNaoEVacuosa: `declares-output-source` tem contra-exemplos. O plano
// do caso do payload (ADR-022), que não declara origem nenhuma, falha-a; e um plano que declara
// a origem numa saída que ninguém consome também.
func TestAOS501_ARubricaDaOrigemNaoEVacuosa(t *testing.T) {
	if !declaresOutputSource(loadStampedCandidate(t, "adr038-output-source", "origem-1.json", linhaDaOrigemDeSaida)) {
		t.Fatal("o plano com a origem declarada e consumida tinha de satisfazer a rubrica")
	}
	if declaresOutputSource(loadExtensionCandidate(t, "adr022-payload", "payload-1.json")) {
		t.Fatal("um plano sem from_tool satisfez a rubrica da origem: ela nao mede nada")
	}
	orfa := loadStampedCandidate(t, "adr038-output-source", "origem-1.json", linhaDaOrigemDeSaida)
	orfa.Nodes[1].Consumes = nil
	if declaresOutputSource(orfa) {
		t.Fatal("uma origem declarada numa saida que ninguem consome satisfez a rubrica")
	}
}

// TestAOS500_ARegraDoGoldenSetNosDoisRamos exercita a regra com retratos montados à mão: o ramo
// «excepção retirada» não corre sobre o golden-set real antes do AOS-501, e uma regra que só se
// lê no dia em que é precisa não está testada.
func TestAOS500_ARegraDoGoldenSetNosDoisRamos(t *testing.T) {
	v := func(minor int) plan.PlanVersion { return plan.PlanVersion{Major: 1, Minor: minor, Patch: 0} }
	deHoje := []fixtureDoGoldenSet{
		{caminho: "testdata/a/1.json", carimbo: v(0)},
		{caminho: "testdata/b/1.json", carimbo: v(2)},
	}
	comCasoNovo := append(append([]fixtureDoGoldenSet(nil), deHoje...),
		fixtureDoGoldenSet{caminho: "testdata/origem/1.json", carimbo: v(3), comOrigem: true})

	for _, c := range []struct {
		nome     string
		excepcao bool
		fixtures []fixtureDoGoldenSet
		falha    string // vazio ⇒ passa; senão, um troço da mensagem
	}{
		{"excepcao activa, golden-set de hoje", true, deHoje, ""},
		{"excepcao activa, fixture com origem", true, comCasoNovo, "declara `from_tool`"},
		{"excepcao activa, fixture carimbada 1.3.0 sem origem", true,
			append(append([]fixtureDoGoldenSet(nil), deHoje...), fixtureDoGoldenSet{caminho: "testdata/c/1.json", carimbo: v(3)}), "carimba 1.3.0"},
		{"excepcao retirada, golden-set de hoje", false, deHoje, "nao tem nenhum caso"},
		{"excepcao retirada, golden-set vazio", false, nil, "nao tem nenhum caso"},
		{"excepcao retirada, caso com origem carimbado 1.3.0", false, comCasoNovo, ""},
		// Um caso carimbado 1.3.0 SEM o campo não serve: o que se exige é um plano com a origem.
		{"excepcao retirada, so o carimbo", false,
			append(append([]fixtureDoGoldenSet(nil), deHoje...), fixtureDoGoldenSet{caminho: "testdata/c/1.json", carimbo: v(3)}), "nao tem nenhum caso"},
		// E a origem com outro carimbo também não: o validador recusava essa fixture.
		{"excepcao retirada, origem carimbada 1.2.0", false,
			append(append([]fixtureDoGoldenSet(nil), deHoje...), fixtureDoGoldenSet{caminho: "testdata/c/1.json", carimbo: v(2), comOrigem: true}), "nao tem nenhum caso"},
	} {
		err := exigenciaDoGoldenSet(c.excepcao, c.fixtures)
		switch {
		case c.falha == "" && err != nil:
			t.Errorf("%s: devia passar; deu %v", c.nome, err)
		case c.falha != "" && err == nil:
			t.Errorf("%s: devia falhar (%q) e passou", c.nome, c.falha)
		case c.falha != "" && !strings.Contains(err.Error(), c.falha):
			t.Errorf("%s: falhou por outra razao: %v (queria %q)", c.nome, err, c.falha)
		}
	}
}
