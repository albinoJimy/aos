package plannerprompt

// AOS-279 — O GOLDEN-SET DO PLANEADOR PASSA A TER SINAL NO GATE DE CI (fecha DEF-276).
//
// O PROBLEMA QUE ISTO FECHA. O AC2 de AOS-273 pedia que os casos das extensões de
// ADR-022 «passassem no eval-gate». Passavam — no eval-gate do PLANEADOR
// ([Evaluate], AOS-241), que a CI corre como testes deste pacote. Mas o gate de CI
// chamado `evalgate.sh` corre OUTRO harness (`packages/platform/eval`) e não conhecia
// este conjunto: o pass-rate do planeador não entrava no relatório do gate, pelo que
// uma regressão de cobertura aqui não tinha sinal COM ESSE NOME. Metade honesta do AC
// ficou por isso marcada «NÃO ENTREGUE» e registada em DEF-276 — é essa metade que
// este ficheiro entrega.
//
// PORQUE UM MARCADOR PRÓPRIO, E NÃO O `AOS_EVAL_REPORT`. O gate captura a linha do
// harness de `platform/eval` por esse marcador; emitir o MESMO daqui faria duas linhas
// diferentes competir pelo mesmo `grep | head -1` — o gate passaria a medir uma delas
// por acidente de ordem. [PlannerEvalReportMarker] é distinto de propósito: são dois
// sinais com significados distintos (o harness de acções vs o golden-set do planeador)
// e o gate impõe-lhes limiares distintos.
//
// PORQUE AQUI E NÃO NO HARNESS DE `platform/eval`. Ligar [Evaluate] a esse harness
// faria `platform/eval` importar `control-plane/orchestrator` — acoplamento entre
// módulos que o layer-lint guarda e que o `go.mod` teria de declarar. O gate corre os
// dois módulos SEPARADAMENTE e agrega no script: o sinal chega ao mesmo sítio sem
// criar uma dependência que ninguém quer manter.

import (
	"encoding/json"
	"strings"
	"testing"
)

// PlannerEvalReportMarker prefixa a linha de relatório que `scripts/ci/evalgate.sh`
// captura do output `-v` deste pacote. Distinto do marcador do harness de acções
// (`platform/eval`) — ver a nota no cabeçalho.
const PlannerEvalReportMarker = "AOS_PLANNER_EVAL_REPORT"

// plannerEvalReport é o que o gate lê. Números INTEIROS (passadas/totais) e não uma
// fracção pré-calculada: o gate faz a sua própria aritmética e, sobretudo, consegue
// distinguir «100% de zero avaliações» de «100% de doze» — a armadilha que uma fracção
// sozinha esconderia.
type plannerEvalReport struct {
	GoldenSetVersion string `json:"goldenset_version"`
	Cases            int    `json:"cases"`
	SecurityPassed   int    `json:"security_passed"`
	SecurityTotal    int    `json:"security_total"`
	QualityPassed    int    `json:"quality_passed"`
	QualityTotal     int    `json:"quality_total"`
	SecurityViols    int    `json:"security_violations"`
	QualityViols     int    `json:"quality_violations"`
	Passed           bool   `json:"passed"`
}

func (r plannerEvalReport) line() string {
	b, err := json.Marshal(r)
	if err != nil {
		// Impossível para esta struct (só escalares); se acontecesse, é melhor uma
		// linha malformada que o gate RECUSA do que uma linha ausente que ele não vê.
		return PlannerEvalReportMarker + " {}"
	}
	return PlannerEvalReportMarker + " " + string(b)
}

// TestPlannerEvalReportEmitted emite a linha marcada que o gate de CI consome, e
// tranca-a nas duas pontas: o conteúdo tem de ser o que [TestADR022GoldenSetPassesEvalGate]
// já assere (mesma fonte, mesma política), e a FORMA tem de ser parseável — um relatório
// que o gate não consegue ler é indistinguível, do lado dele, de um gate sem sinal.
func TestPlannerEvalReportEmitted(t *testing.T) {
	gs := adr022GoldenSet()
	rep, err := Evaluate(gs, adr022Samples(t), adr022Policy)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	secPassed, secTotal := rep.PassRate(Security)
	qualPassed, qualTotal := rep.PassRate(Quality)

	out := plannerEvalReport{
		GoldenSetVersion: gs.Version.String(),
		Cases:            len(gs.Cases),
		SecurityPassed:   secPassed,
		SecurityTotal:    secTotal,
		QualityPassed:    qualPassed,
		QualityTotal:     qualTotal,
		SecurityViols:    len(rep.Security),
		QualityViols:     len(rep.Quality),
		Passed:           rep.Passed(),
	}

	// A linha, capturada pelo gate a partir do `-v`.
	t.Logf("%s", out.line())

	// (1) COBERTURA NÃO-VAZIA. Um relatório com totais a zero passaria todos os
	// limiares do gate por AUSÊNCIA DE EVIDÊNCIA — é a armadilha que o próprio
	// [Evaluate] fecha com ErrNoSamplesForCase, e que aqui se re-tranca na fronteira
	// do relatório (o gate lê números, não a intenção de quem os produziu).
	if out.SecurityTotal == 0 || out.QualityTotal == 0 {
		t.Fatalf("relatório com cobertura VAZIA (sec=%d/%d qual=%d/%d): o gate passaria sem avaliar nada",
			out.SecurityPassed, out.SecurityTotal, out.QualityPassed, out.QualityTotal)
	}
	// (2) A REGRA DE SEGURANÇA É 100%, não um limiar — é o ponto onde um limiar seria
	// inseguro (ver o doc de [Evaluate]).
	if out.SecurityPassed != out.SecurityTotal {
		t.Fatalf("segurança %d/%d — a regra 100%% exige a totalidade", out.SecurityPassed, out.SecurityTotal)
	}
	// (3) O veredicto agregado tem de coincidir com as violações: um `passed:true` com
	// violações seria uma linha que MENTE ao gate.
	if out.Passed != (out.SecurityViols == 0 && out.QualityViols == 0) {
		t.Fatalf("veredicto incoerente com as violações: passed=%v sec=%d qual=%d",
			out.Passed, out.SecurityViols, out.QualityViols)
	}
	// (4) FORMA: o gate faz `grep MARKER | sed` e depois lê os campos. Se a linha não
	// parsear, o gate não tem sinal — e um gate sem sinal é um gate que não protege.
	raw := strings.TrimPrefix(out.line(), PlannerEvalReportMarker+" ")
	var back plannerEvalReport
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("a linha do relatório não parseia (%v): %q", err, out.line())
	}
	if back != out {
		t.Fatalf("round-trip do relatório perdeu informação:\n  ida=%+v\n  volta=%+v", out, back)
	}
	if !strings.HasPrefix(out.line(), PlannerEvalReportMarker+" {") {
		t.Fatalf("linha mal-formada para o grep do gate: %q", out.line())
	}
}
