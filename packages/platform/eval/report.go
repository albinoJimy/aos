package eval

import (
	"context"
	"fmt"
	"strconv"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// EvalReportMarker é o prefixo ESTÁVEL, legível-por-máquina, da linha de relatório de
// eval-pass-rate (molde do AOS_REPLAY_REPORT de replay.sh). O gate 9 (evalgate.sh)
// extrai a linha e verifica o eval_pass_rate contra o alvo (>=90%).
const EvalReportMarker = "AOS_EVAL_REPORT"

// EvalReport é o relatório agregado de eval-pass-rate: a fracção de golden-sets
// (suites) cujo veredicto PASSOU o eval-gate à primeira. É o driver de qualidade do
// DoD (alvo >= 90%).
type EvalReport struct {
	// Suites é o nº de golden-sets avaliados.
	Suites int
	// Passed é o nº de golden-sets admitidos pelo eval-gate.
	Passed int
	// TotalCases é o nº total de casos avaliados sobre todas as suites.
	TotalCases int
	// TotalUnsafe é o nº total de casos unsafe (acção proibida) — alvo 0.
	TotalUnsafe int
	// EvalPassRate = Passed/Suites (0 se Suites==0 — fail-closed).
	EvalPassRate float64
}

// CompactJSON serializa o relatório numa linha JSON compacta e de ordem de campos
// FIXA (determinista, sem mapas). É a carga que segue o marcador [EvalReportMarker].
func (r EvalReport) CompactJSON() string {
	return "{" +
		`"suites":` + strconv.Itoa(r.Suites) + "," +
		`"passed":` + strconv.Itoa(r.Passed) + "," +
		`"total_cases":` + strconv.Itoa(r.TotalCases) + "," +
		`"total_unsafe":` + strconv.Itoa(r.TotalUnsafe) + "," +
		`"eval_pass_rate":` + strconv.FormatFloat(r.EvalPassRate, 'f', 3, 64) +
		"}"
}

// Line devolve a linha completa de relatório (marcador + JSON), para emitir tal como o
// replay emite AOS_REPLAY_REPORT.
func (r EvalReport) Line() string { return EvalReportMarker + " " + r.CompactJSON() }

// BuildReport agrega uma lista de [SetEvaluation] num [EvalReport], usando gate para
// decidir o que passou (admissão fail-closed). Determinista.
func BuildReport(evals []SetEvaluation, gate otelgenai.EvalGate) EvalReport {
	rep := EvalReport{Suites: len(evals)}
	for _, se := range evals {
		rep.TotalCases += se.Report.Total
		rep.TotalUnsafe += se.Report.UnsafeCount
		if gate.Admit(se.Result) {
			rep.Passed++
		}
	}
	if rep.Suites > 0 {
		rep.EvalPassRate = float64(rep.Passed) / float64(rep.Suites)
	}
	return rep
}

// ReferenceCandidateFor devolve o candidato de referência conhecido-bom da classe de
// artefacto dada (nil se a classe for desconhecida). É o candidato que demonstra um
// artefacto que PASSA os golden-sets embebidos (usado pelo relatório do gate 9).
func ReferenceCandidateFor(kind ArtifactKind) Candidate {
	switch kind {
	case ArtifactSkill:
		return GoodSkillCandidate()
	case ArtifactProceduralMemory:
		return GoodProceduralCandidate()
	default:
		return nil
	}
}

// ReferenceEvalReport avalia TODOS os golden-sets embebidos com o candidato de
// referência conhecido-bom da respectiva classe e devolve o [EvalReport] agregado. É a
// prova reprodutível de eval-pass-rate do gate 9 (determinista; sem I/O nem rand).
// Fail-closed: um golden-set embebido inválido ou uma classe sem candidato de
// referência devolve erro.
func ReferenceEvalReport(ctx context.Context, minScore float64) (EvalReport, error) {
	suites, err := EmbeddedSuites()
	if err != nil {
		return EvalReport{}, err
	}
	h := NewHarness(minScore)
	gate := h.Gate()
	evals := make([]SetEvaluation, 0, len(suites))
	for _, gs := range suites {
		c := ReferenceCandidateFor(gs.ArtifactKind)
		if c == nil {
			return EvalReport{}, fmt.Errorf("eval: sem candidato de referência para a classe %q", gs.ArtifactKind)
		}
		evals = append(evals, h.EvaluateSet(ctx, gs, c))
	}
	return BuildReport(evals, gate), nil
}
