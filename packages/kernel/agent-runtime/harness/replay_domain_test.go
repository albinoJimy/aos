package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/replay"
)

// ---------------------------------------------------------------------------
// EPIC-11 · AOS-111 — suite de replay determinístico sobre as trajectórias de
// DOMÍNIO (em particular a MULTI-PASSO COM SUB-AGENTE, [BuildDelegationGolden]).
// CONSOME o harness de AOS-024 (Verify / replay.ReplayEngine) — não reimplementa
// a mecânica de replay/fidelidade/idempotência.
// ---------------------------------------------------------------------------

// domainFixtures constrói as trajectórias de domínio de AOS-111 (com sub-agente) e
// devolve-as com um closer. É o material das suites positiva/negativa.
func domainFixtures(t *testing.T) ([]*Fixture, func()) {
	t.Helper()
	f, err := BuildDelegationGolden("golden_delegation_domain")
	if err != nil {
		t.Fatalf("BuildDelegationGolden: %v", err)
	}
	fixtures := []*Fixture{f}
	return fixtures, func() {
		for _, fx := range fixtures {
			fx.Close()
		}
	}
}

// TestReplayResumeFromStepDomainSuite prova o AC1/AC2/AC3: a suite carrega uma
// trajectória de domínio conhecida (o supervisor que delega no sub-agente) e
// reproduz-na RESUME-FROM-STEP a partir de um passo ARBITRÁRIO. O estado reconstruído
// no ponto de retoma, o estado FINAL e o desfecho coincidem 100% com o replay
// completo, e a fidelidade é 1.0 sem divergência — a prova de que TODOS os inputs
// não-determinísticos (resposta do modelo, resultado da tool = achado do sub-agente,
// seed, relógio) foram capturados e são REINJECTADOS do log (o EventReader é só-Read:
// nenhuma chamada externa nem re-execução do sub-agente ocorre no replay).
func TestReplayResumeFromStepDomainSuite(t *testing.T) {
	ctx := context.Background()
	fixtures, closer := domainFixtures(t)
	defer closer()

	for _, f := range fixtures {
		f := f
		t.Run(f.Name, func(t *testing.T) {
			eng, err := replay.NewEngine(f.trajectory)
			if err != nil {
				t.Fatalf("NewEngine: %v", err)
			}

			// (1) Replay COMPLETO — a referência de estado.
			full, err := eng.Replay(ctx, f.RunID, replay.Options{Spec: f.spec})
			if err != nil {
				t.Fatalf("replay completo: %v", err)
			}
			if full.Divergence != nil || full.Fidelity != 1.0 {
				t.Fatalf("replay completo devia ser fiel: fidelity=%v div=%+v", full.Fidelity, full.Divergence)
			}
			if !full.Terminated {
				t.Fatalf("replay completo devia terminar")
			}

			// (2) RESUME-FROM-STEP a partir de um passo ARBITRÁRIO (o turno 2, a meio da
			// trajectória, DEPOIS da delegação no sub-agente). O motor re-dobra os turnos
			// anteriores do log (zero efeitos) e verifica a partir daqui.
			const arbitraryStep = "step-000002"
			resumed, err := eng.Replay(ctx, f.RunID, replay.Options{Spec: f.spec, FromStepID: arbitraryStep})
			if err != nil {
				t.Fatalf("replay resume-from-step %s: %v", arbitraryStep, err)
			}
			if resumed.Divergence != nil {
				t.Fatalf("resume divergiu: %+v", resumed.Divergence)
			}
			if resumed.Fidelity != 1.0 {
				t.Fatalf("resume: fidelidade %v, esperava 1.0", resumed.Fidelity)
			}
			if resumed.ResumedFromStepID != arbitraryStep {
				t.Fatalf("resume: ResumedFromStepID=%q, esperava %q", resumed.ResumedFromStepID, arbitraryStep)
			}
			// O estado FINAL reconstruído tem de ser IDÊNTICO entre o completo e o resume —
			// a prova de equivalência do resume-from-step.
			if resumed.FinalStateHash != full.FinalStateHash {
				t.Fatalf("resume: FinalStateHash=%q != completo %q", resumed.FinalStateHash, full.FinalStateHash)
			}
			if resumed.Terminated != full.Terminated || resumed.FinalText != full.FinalText {
				t.Fatalf("resume: desfecho diverge (term=%v/%v text=%q/%q)", resumed.Terminated, full.Terminated, resumed.FinalText, full.FinalText)
			}
			// O segmento retomado começa MESMO no passo pedido.
			if len(resumed.Steps) == 0 || resumed.Steps[0].StepID != arbitraryStep {
				t.Fatalf("resume: primeiro passo do segmento = %+v, esperava %q", resumed.Steps, arbitraryStep)
			}

			// (3) Verify completo (replay + idempotência + fault-injection resume-from-step
			// nos pontos de crash da fixture) tem de PASSAR a 100%.
			rep, err := Verify(ctx, f.Case())
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if !rep.Pass || rep.ReplayFidelity != 1.0 {
				t.Fatalf("trajectória de domínio devia passar a 100%%:\n%s", rep.JSON())
			}
			if rep.ResumeMismatches != 0 || rep.ResumePoints == 0 {
				t.Fatalf("fault-injection resume devia ser correcta e não-vazia: %+v", rep)
			}
		})
	}
}

// TestReplayDomainNegativeDetected prova o AC5 no molde de
// [TestHarnessDetectsTamperedTrajectory], mas sobre a trajectória de DOMÍNIO com
// sub-agente: uma mudança que quebra a fidelidade — reordenar/alterar o PREFIXO do
// prompt (system/objectivo) OU trocar o SEED — é DETECTADA e falha em VERMELHO
// controlado (Pass=false, Err()!=nil, Diverged), com um DIFF LEGÍVEL. O teste em si
// PASSA por detectar a falha.
func TestReplayDomainNegativeDetected(t *testing.T) {
	cases := []struct {
		name       string
		tamper     func(c *Case)
		wantStepID string
		wantReason string
	}{
		{
			name:       "prefixo do prompt evoluído (system)",
			tamper:     func(c *Case) { c.Spec.System = "SYSTEM DO SUPERVISOR EVOLUÍDO" },
			wantStepID: "step-000001",
			wantReason: "prompt_hash",
		},
		{
			name:       "prefixo do prompt reordenado (objectivo)",
			tamper:     func(c *Case) { c.Spec.Objective = "Consolida primeiro e delega depois." },
			wantStepID: "step-000001",
			wantReason: "prompt_hash",
		},
		{
			name:       "seed do modelo trocado (invisível ao prompt_hash)",
			tamper:     func(c *Case) { c.Spec.Model.Seed = 999 },
			wantStepID: "step-000001",
			wantReason: "model",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f, err := BuildDelegationGolden("golden_deleg_neg_" + sanitize(tc.name))
			if err != nil {
				t.Fatalf("BuildDelegationGolden: %v", err)
			}
			defer f.Close()

			c := f.Case()
			tc.tamper(&c)

			rep, err := Verify(context.Background(), c)
			if err != nil {
				t.Fatalf("Verify (operacional): %v", err)
			}
			// A trajectória adulterada TEM de ser reprovada em vermelho controlado.
			if rep.Pass {
				t.Fatalf("harness aceitou trajectória de domínio adulterada:\n%s", rep.JSON())
			}
			if !rep.Diverged {
				t.Fatalf("esperava Diverged=true, obtive %+v", rep)
			}
			if rep.DivergenceStepID != tc.wantStepID {
				t.Fatalf("divergência localizada em %q, esperava %q", rep.DivergenceStepID, tc.wantStepID)
			}
			if rep.DivergenceReason != tc.wantReason {
				t.Fatalf("razão da divergência = %q, esperava %q", rep.DivergenceReason, tc.wantReason)
			}
			if rep.ReplayFidelity == 1.0 {
				t.Fatalf("fidelidade não devia ser 1.0 numa adulteração")
			}
			// DIFF LEGÍVEL: o erro fail-closed localiza o passo, a razão e a fidelidade.
			err = rep.Err()
			if err == nil {
				t.Fatalf("Err() devia ser não-nil numa trajectória adulterada")
			}
			msg := err.Error()
			for _, want := range []string{"divergiu no passo", tc.wantStepID, tc.wantReason} {
				if !strings.Contains(msg, want) {
					t.Fatalf("diff do erro %q não contém %q", msg, want)
				}
			}
		})
	}
}

// TestPerfectFractionExcludesBrokenTrajectory prova o COMPLEMENTAR do driver: um
// agregado que contém UMA trajectória infiel (prefixo do prompt adulterado) ao lado
// de trajectórias fiéis reprova em bloco (agg.Pass=false, agg.Err()!=nil) e a
// PerfectFraction cai ABAIXO de 1.0 — só os casos 100% reproduzíveis entram no
// numerador. Exercita o caminho de agregação fail-closed e a métrica sob falha.
func TestPerfectFractionExcludesBrokenTrajectory(t *testing.T) {
	ctx := context.Background()

	good, err := BuildDelegationGolden("golden_deleg_agg_good")
	if err != nil {
		t.Fatalf("BuildDelegationGolden (good): %v", err)
	}
	defer good.Close()
	bad, err := BuildDelegationGolden("golden_deleg_agg_bad")
	if err != nil {
		t.Fatalf("BuildDelegationGolden (bad): %v", err)
	}
	defer bad.Close()

	// Adultera o PREFIXO do prompt do caso "bad" — infidelidade detectável.
	badCase := bad.Case()
	badCase.Spec.System = "SYSTEM ADULTERADO"

	agg, err := VerifyAll(ctx, []Case{good.Case(), badCase})
	if err != nil {
		t.Fatalf("VerifyAll (operacional): %v", err)
	}
	if agg.Pass {
		t.Fatalf("agregado com trajectória infiel devia reprovar:\n%s", agg.JSON())
	}
	if agg.Err() == nil {
		t.Fatalf("agg.Err() devia ser não-nil com um caso infiel")
	}
	if agg.Runs != 2 || agg.PerfectRuns != 1 {
		t.Fatalf("esperava 2 runs / 1 perfeito, obtive runs=%d perfect=%d", agg.Runs, agg.PerfectRuns)
	}
	if agg.PerfectFraction != 0.5 {
		t.Fatalf("PerfectFraction = %v, esperava 0.5 (1 de 2 reproduzível)", agg.PerfectFraction)
	}
}

// TestPerfectFractionReported prova o driver do DoD "% de trajectórias 100%
// reproduzíveis": o [AggregateReport] emite PerfectRuns/PerfectFraction e, para o
// golden set (todas fiéis), a fracção é 1.0 e é serializada no AOS_REPLAY_REPORT.
func TestPerfectFractionReported(t *testing.T) {
	agg, closer, err := GoldenReport(context.Background())
	if err != nil {
		t.Fatalf("GoldenReport: %v", err)
	}
	defer closer()

	if agg.Runs == 0 {
		t.Fatalf("esperava casos no golden set")
	}
	if agg.PerfectRuns != agg.Runs {
		t.Fatalf("esperava todos os %d casos perfeitos, obtive %d", agg.Runs, agg.PerfectRuns)
	}
	if agg.PerfectFraction != 1.0 {
		t.Fatalf("PerfectFraction = %v, esperava 1.0", agg.PerfectFraction)
	}
	// A métrica entra na serialização canónica emitida ao gate.
	if !strings.Contains(string(agg.CompactJSON()), `"perfect_fraction":1`) {
		t.Fatalf("relatório compacto sem perfect_fraction: %s", agg.CompactJSON())
	}
	// Pass tem de permanecer o ÚLTIMO campo (âncora do gate 8: "pass":true}$).
	compact := string(agg.CompactJSON())
	if !strings.HasSuffix(compact, `"pass":true}`) {
		t.Fatalf("Pass deixou de ser o último campo (partiria a âncora do gate 8): %s", compact)
	}
}
