package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/durable"
)

// ---------------------------------------------------------------------------
// Positivo — o golden set corre VERDE: replay-fidelity 100%, 0 efeitos
// duplicados, retoma sem divergência. É a prova de que o harness passa nas
// trajectórias de referência (e o denominador dos meta-testes negativos).
// ---------------------------------------------------------------------------

func TestGoldenReplayIdempotency(t *testing.T) {
	cases, closer, err := GoldenSet()
	if err != nil {
		t.Fatalf("GoldenSet: %v", err)
	}
	defer closer()

	agg, err := VerifyAll(context.Background(), cases)
	if err != nil {
		t.Fatalf("VerifyAll: %v", err)
	}
	if err := agg.Err(); err != nil {
		t.Fatalf("golden set devia passar: %v\n%s", err, agg.JSON())
	}
	if !agg.Pass {
		t.Fatalf("agregado devia passar:\n%s", agg.JSON())
	}
	if agg.MeanReplayFidelity != 1.0 {
		t.Fatalf("replay-fidelity média = %v, esperava 1.0", agg.MeanReplayFidelity)
	}
	if agg.TotalDuplicatedEffects != 0 {
		t.Fatalf("esperava 0 efeitos duplicados, obtive %d", agg.TotalDuplicatedEffects)
	}
	if agg.TotalResumeMismatches != 0 {
		t.Fatalf("esperava 0 retomas divergentes, obtive %d", agg.TotalResumeMismatches)
	}
	// Verificação por caso: fidelidade 1.0 e Pass.
	for _, c := range agg.Cases {
		if !c.Pass || c.ReplayFidelity != 1.0 {
			t.Fatalf("caso %q não passou: %+v", c.Name, c)
		}
	}
	// O caso echo tem de ter exercitado 2 efeitos e 2 pontos de retoma.
	echo := findCase(t, agg, "echo-3turns")
	if echo.EffectsVerified != 2 {
		t.Fatalf("echo: esperava 2 efeitos verificados, obtive %d", echo.EffectsVerified)
	}
	if echo.ResumePoints != 2 {
		t.Fatalf("echo: esperava 2 pontos de retoma, obtive %d", echo.ResumePoints)
	}
	if echo.Turns != 3 {
		t.Fatalf("echo: esperava 3 turnos, obtive %d", echo.Turns)
	}
}

// ---------------------------------------------------------------------------
// META-TESTE 1 — trajectória ADULTERADA → o harness FALHA (divergência localizada).
// Cobre as várias formas de "evolução de código" que quebram a fidelidade.
// ---------------------------------------------------------------------------

func TestHarnessDetectsTamperedTrajectory(t *testing.T) {
	cases := []struct {
		name       string
		tamper     func(c *Case)
		wantStepID string
		wantReason string
	}{
		{
			name:       "system prompt evoluído",
			tamper:     func(c *Case) { c.Spec.System = "SYSTEM PROMPT EVOLUÍDO" },
			wantStepID: "step-000001",
			wantReason: "prompt_hash",
		},
		{
			name:       "objectivo alterado",
			tamper:     func(c *Case) { c.Spec.Objective = "objectivo diferente" },
			wantStepID: "step-000001",
			wantReason: "prompt_hash",
		},
		{
			name: "digest do tool set congelado alterado",
			tamper: func(c *Case) {
				tools := append([]agentruntime.ToolSpec(nil), c.Spec.Tools...)
				tools[0].Digest = "sha256:ADULTERADO"
				c.Spec.Tools = tools
			},
			wantStepID: "step-000001",
			wantReason: "prompt_hash",
		},
		{
			name:       "seed do modelo trocado (invisível ao prompt_hash)",
			tamper:     func(c *Case) { c.Spec.Model.Seed = 999 },
			wantStepID: "step-000001",
			wantReason: "model",
		},
		{
			name:       "versão do assembler subida (invisível ao prompt_hash)",
			tamper:     func(c *Case) { c.Spec.AssemblyVersion = "2.0.0" },
			wantStepID: "step-000001",
			wantReason: "assembly_version",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := BuildEchoGolden("golden_tamper_" + sanitize(tc.name))
			if err != nil {
				t.Fatalf("BuildEchoGolden: %v", err)
			}
			defer f.Close()

			c := f.Case()
			tc.tamper(&c)

			rep, err := Verify(context.Background(), c)
			if err != nil {
				t.Fatalf("Verify (operacional): %v", err)
			}
			// O harness TEM de reprovar a trajectória adulterada.
			if rep.Pass {
				t.Fatalf("harness aceitou trajectória adulterada:\n%s", rep.JSON())
			}
			if rep.Err() == nil {
				t.Fatalf("Err() devia ser não-nil numa trajectória adulterada")
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
		})
	}
}

// TestHarnessDetectsMidTrajectoryTamper prova a LOCALIZAÇÃO num turno POSTERIOR: só
// o turno 2 diverge (o 1 coincide) e o harness reporta fidelidade PARCIAL. Adultera
// o prompt_hash gravado do turno 2 via um reader que reescreve esse manifesto.
func TestHarnessDetectsMidTrajectoryTamper(t *testing.T) {
	f, err := BuildEchoGolden("golden_tamper_mid")
	if err != nil {
		t.Fatalf("BuildEchoGolden: %v", err)
	}
	defer f.Close()

	c := f.Case()
	const tampered = "sha256:TURNO-2-ADULTERADO"
	c.Reader = &manifestMutatingReader{
		inner: f.trajectory,
		mutate: func(turn int, m *agentruntime.Manifest) {
			if turn == 2 {
				m.PromptHash = tampered
			}
		},
	}

	rep, err := Verify(context.Background(), c)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.Pass || !rep.Diverged {
		t.Fatalf("esperava reprovação por divergência a meio:\n%s", rep.JSON())
	}
	if rep.DivergenceStepID != "step-000002" || rep.DivergenceReason != "prompt_hash" {
		t.Fatalf("divergência localizada errada: step=%q reason=%q", rep.DivergenceStepID, rep.DivergenceReason)
	}
	// Fidelidade PARCIAL: 1 turno coincidiu de 2 verificados até à divergência.
	if rep.ReplayFidelity != 0.5 {
		t.Fatalf("fidelidade = %v, esperava 0.5", rep.ReplayFidelity)
	}
}

// ---------------------------------------------------------------------------
// META-TESTE 2 — efeito DUPLICADO injectado → o harness FALHA.
// A injecção é uma idempotency key NÃO-DETERMINÍSTICA (varia com a tentativa): o
// ledger não consegue deduplicar e o efeito corre mais do que uma vez.
// ---------------------------------------------------------------------------

func TestHarnessDetectsDuplicatedEffect(t *testing.T) {
	f, err := BuildEchoGolden("golden_dup_effect")
	if err != nil {
		t.Fatalf("BuildEchoGolden: %v", err)
	}
	defer f.Close()

	c := f.Case()
	// Substitui o primeiro efeito por um com BUG de idempotência: a chave inclui o
	// nº da tentativa (não-determinística) ⇒ o ledger não deduplica. Mantém o
	// segundo efeito idempotente (controlo).
	broken, observed := brokenKeyEffect(c.RunID, 1, 1)
	c.Effects[0] = broken

	rep, err := Verify(context.Background(), c)
	if err != nil {
		t.Fatalf("Verify (operacional): %v", err)
	}
	if rep.Pass {
		t.Fatalf("harness aceitou efeito duplicado:\n%s", rep.JSON())
	}
	if rep.Err() == nil {
		t.Fatalf("Err() devia ser não-nil com efeitos duplicados")
	}
	if rep.DuplicatedEffects == 0 {
		t.Fatalf("esperava DuplicatedEffects > 0, obtive %d", rep.DuplicatedEffects)
	}
	// O efeito com bug correu 3 vezes (3 tentativas, chave sempre diferente) ⇒ 2
	// duplicados observáveis; o replay em si continua fiel (a divergência é de
	// idempotência, não de replay).
	if *observed != 3 {
		t.Fatalf("efeito com bug devia ter corrido 3 vezes, correu %d", *observed)
	}
	if rep.DuplicatedEffects != 2 {
		t.Fatalf("esperava exactamente 2 efeitos duplicados, obtive %d", rep.DuplicatedEffects)
	}
	if rep.Diverged {
		t.Fatalf("o replay não devia divergir — a falha é de idempotência")
	}
}

// TestIdempotentEffectRunsOnce isola a garantia positiva: um efeito com chave
// estável corre UMA vez sob o calendário at-least-once com crash intercalado.
func TestIdempotentEffectRunsOnce(t *testing.T) {
	f, err := BuildEchoGolden("golden_idem_once")
	if err != nil {
		t.Fatalf("BuildEchoGolden: %v", err)
	}
	defer f.Close()

	seq := durable.NewStepSequencer()
	eff := StableEffect(f.RunID, seq, 1, 1)
	if err := DriveEffectSchedule(context.Background(), f.RunID, f.ledgerStore, eff); err != nil {
		t.Fatalf("DriveEffectSchedule: %v", err)
	}
	if got := eff.Observed(); got != 1 {
		t.Fatalf("efeito idempotente devia correr 1 vez, correu %d", got)
	}
}

// ---------------------------------------------------------------------------
// META-TESTE 3 — reprodutibilidade: as fixtures produzem resultados ESTÁVEIS entre
// execuções. Construções independentes da mesma golden dão relatórios byte-idênticos.
// Corre com -count alto para reforçar a estabilidade.
// ---------------------------------------------------------------------------

func TestFixturesReproducible(t *testing.T) {
	ctx := context.Background()

	build := func() FidelityReport {
		f, err := BuildEchoGolden("golden_repro")
		if err != nil {
			t.Fatalf("BuildEchoGolden: %v", err)
		}
		defer f.Close()
		rep, err := Verify(ctx, f.Case())
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		return rep
	}

	a := build()
	b := build()
	if !bytes.Equal(a.JSON(), b.JSON()) {
		t.Fatalf("relatórios divergem entre construções independentes:\nA=%s\nB=%s", a.JSON(), b.JSON())
	}
	if !a.Pass || a.ReplayFidelity != 1.0 {
		t.Fatalf("golden reproduzível devia passar a 100%%: %+v", a)
	}

	// O golden set inteiro também é estável entre execuções.
	agg1 := goldenAggregate(t)
	agg2 := goldenAggregate(t)
	if !bytes.Equal(agg1, agg2) {
		t.Fatalf("agregado do golden set não é estável entre execuções:\n1=%s\n2=%s", agg1, agg2)
	}
}

// ---------------------------------------------------------------------------
// FAULT-INJECTION — pontos de crash → retoma correcta (mesmo estado que o completo).
// ---------------------------------------------------------------------------

func TestFaultInjectionResume(t *testing.T) {
	f, err := BuildEchoGolden("golden_fault")
	if err != nil {
		t.Fatalf("BuildEchoGolden: %v", err)
	}
	defer f.Close()

	rep, err := Verify(context.Background(), f.Case())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.ResumePoints != 2 {
		t.Fatalf("esperava 2 pontos de crash exercitados, obtive %d", rep.ResumePoints)
	}
	if rep.ResumeMismatches != 0 {
		t.Fatalf("retoma devia ser correcta (0 divergências), obtive %d", rep.ResumeMismatches)
	}
	if !rep.Pass {
		t.Fatalf("retoma correcta devia passar:\n%s", rep.JSON())
	}

	// Um ponto de crash em step_id INEXISTENTE é reportado como retoma divergente
	// (fail-closed), não como erro operacional.
	c := f.Case()
	c.Faults = []FaultPoint{{AtStepID: "step-999999"}}
	c.Effects = nil // isola a fault-injection
	bad, err := Verify(context.Background(), c)
	if err != nil {
		t.Fatalf("Verify (step inexistente): %v", err)
	}
	if bad.ResumeMismatches != 1 || bad.Pass {
		t.Fatalf("crash em step inexistente devia dar 1 mismatch e reprovar: %+v", bad)
	}
}

// ---------------------------------------------------------------------------
// RELATÓRIO de fidelidade — emitido, estável e consumível. Emite a linha marcada
// AOS_REPLAY_REPORT que o gate de CI (scripts/ci/replay.sh) capta.
// ---------------------------------------------------------------------------

func TestFidelityReportEmitted(t *testing.T) {
	agg, closer, err := GoldenReport(context.Background())
	if err != nil {
		t.Fatalf("GoldenReport: %v", err)
	}
	defer closer()

	if !agg.Pass {
		t.Fatalf("relatório devia indicar Pass=true:\n%s", agg.JSON())
	}
	if agg.MeanReplayFidelity != 1.0 || agg.TotalDuplicatedEffects != 0 {
		t.Fatalf("relatório com métricas inesperadas:\n%s", agg.JSON())
	}
	compact := agg.CompactJSON()
	// Contrato de emissão consumido pelo gate: linha única marcada + "pass":true.
	if !bytes.Contains(compact, []byte(`"pass":true`)) {
		t.Fatalf("relatório compacto sem \"pass\":true: %s", compact)
	}
	if !bytes.Contains(compact, []byte(`"mean_replay_fidelity":1`)) {
		t.Fatalf("relatório compacto sem replay-fidelity: %s", compact)
	}
	// EMISSÃO: uma única linha em stdout, captada por 'go test -v' no gate.
	fmt.Printf("AOS_REPLAY_REPORT %s\n", compact)
}

// ---------------------------------------------------------------------------
// Erros operacionais (setup inválido) — para cobertura e contrato.
// ---------------------------------------------------------------------------

func TestVerifyOperationalErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := Verify(ctx, Case{}); !errors.Is(err, ErrNoRunID) {
		t.Fatalf("esperava ErrNoRunID, obtive %v", err)
	}
	if _, err := Verify(ctx, Case{RunID: "x"}); !errors.Is(err, ErrNoReader) {
		t.Fatalf("esperava ErrNoReader, obtive %v", err)
	}
	// Efeitos sem LedgerStore ⇒ ErrNoLedgerStore.
	f, err := BuildEchoGolden("golden_op_err")
	if err != nil {
		t.Fatalf("BuildEchoGolden: %v", err)
	}
	defer f.Close()
	c := f.Case()
	c.LedgerStore = nil
	if _, err := Verify(ctx, c); !errors.Is(err, ErrNoLedgerStore) {
		t.Fatalf("esperava ErrNoLedgerStore, obtive %v", err)
	}
	// Trajectória inexistente ⇒ erro operacional do replay propagado.
	if _, err := Verify(ctx, Case{RunID: "inexistente", Reader: f.trajectory}); err == nil {
		t.Fatalf("esperava erro operacional para run inexistente")
	}
}

// TestWithTracerEmitsMarker garante que a opção WithTracer é honrada: o motor de
// replay emite o marcador eval (ADR-010) ligado ao run.
func TestWithTracerEmitsMarker(t *testing.T) {
	f, err := BuildEchoGolden("golden_tracer")
	if err != nil {
		t.Fatalf("BuildEchoGolden: %v", err)
	}
	defer f.Close()

	tr := &agentruntime.RecordingTracer{}
	rep, err := Verify(context.Background(), f.Case(), WithTracer(tr))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.Pass {
		t.Fatalf("golden com tracer devia passar")
	}
	// O replay completo + as duas retomas emitem marcadores de replay.
	if len(tr.SpansByOperation("replay")) == 0 {
		t.Fatalf("esperava marcadores de replay emitidos")
	}
}

// ---------------------------------------------------------------------------
// SELF-TEST do gate 8 — prova (via scripts/ci/selftest.sh) que uma trajectória
// DIVERGENTE torna o harness VERMELHO. Normalmente SKIPPED; só corre com
// AOS_REPLAY_SELFTEST=1. Quando activo, adultera a golden e ASSEVERA (falsamente)
// que ela é fiel — a asserção FALHA de propósito, provando o fail-closed do gate.
// ---------------------------------------------------------------------------

func TestSelftestTamperReddensGate(t *testing.T) {
	if os.Getenv("AOS_REPLAY_SELFTEST") != "1" {
		t.Skip("self-test do gate 8 desligado (defina AOS_REPLAY_SELFTEST=1)")
	}
	f, err := BuildEchoGolden("golden_selftest_tamper")
	if err != nil {
		t.Fatalf("BuildEchoGolden: %v", err)
	}
	defer f.Close()

	c := f.Case()
	c.Spec.System = "SYSTEM ADULTERADO (self-test do gate 8)"
	rep, err := Verify(context.Background(), c)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// Afirmação PROPOSITADAMENTE FALSA: a golden adulterada NÃO é fiel, logo Err()
	// é não-nil e esta asserção falha — é isso que torna o gate 8 vermelho.
	if err := rep.Err(); err != nil {
		t.Fatalf("SELF-TEST: harness apanhou a adulteração — gate 8 fica vermelho (esperado): %v", err)
	}
}

// ---------------------------------------------------------------------------
// Auxiliares de teste.
// ---------------------------------------------------------------------------

// brokenKeyEffect constrói um [Effect] com BUG de idempotência: a idempotency key
// inclui o nº da tentativa (não-determinística) ⇒ o ledger não deduplica. Devolve
// o efeito e um ponteiro para o contador de execuções observadas.
func brokenKeyEffect(runID string, turn, index int) (Effect, *int) {
	seq := durable.NewStepSequencer()
	base := seq.SubStepID(runID, turn, index)
	count := 0
	eff := Effect{
		StepID: base,
		KeyAt: func(attempt int) (string, error) {
			// BUG: o step_id varia com a tentativa (como um UUID/carimbo por retry).
			return durable.IdempotencyKey(runID, fmt.Sprintf("%s-try-%d", base, attempt))
		},
		Run: func(context.Context) (durable.Result, error) {
			count++
			return durable.Result{Status: "ok", Payload: []byte("efeito:" + base)}, nil
		},
		Observed: func() int { return count },
	}
	return eff, &count
}

func goldenAggregate(t *testing.T) []byte {
	t.Helper()
	cases, closer, err := GoldenSet()
	if err != nil {
		t.Fatalf("GoldenSet: %v", err)
	}
	defer closer()
	agg, err := VerifyAll(context.Background(), cases)
	if err != nil {
		t.Fatalf("VerifyAll: %v", err)
	}
	return agg.JSON()
}

func findCase(t *testing.T, agg AggregateReport, name string) FidelityReport {
	t.Helper()
	for _, c := range agg.Cases {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("caso %q ausente do agregado", name)
	return FidelityReport{}
}
