package progresssurface

// AOS-261/AOS-262 — a fonte real do burn-down e o AVISO (sem opções de decisão).
//
// O que estes testes selam, por ordem de importância:
//
//  1. O CRITÉRIO DURO: sem fonte, a leitura devolve ERRO — nunca 0%. Um burn-down que
//     devolve 0% por não ter dados é pior do que não existir.
//  2. A política MULTI-INCARNAÇÃO: o consumo é cumulativo sobre o prefixo T1 e a
//     reprodução T2 não duplica (a fonte é chaveada por run_id, não por trace_id).
//  3. O LATCH: o span de aviso é emitido UMA VEZ por run, não uma vez por turno.
//  4. A ABSTINÊNCIA: EvaluateRun NÃO apresenta extend/summarize_stop/abort.

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/control-plane/budget"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// fakeSource é uma [BurndownSource] controlada: devolve o consumo pedido, ou o erro pedido.
type fakeSource struct {
	consumption RunConsumption
	err         error
	calls       int
}

func (f *fakeSource) ConsumedByRun(_ context.Context, _ string) (RunConsumption, error) {
	f.calls++
	if f.err != nil {
		return RunConsumption{}, f.err
	}
	return f.consumption, nil
}

// --- (1) O critério duro: sem fonte / com fonte em erro ⇒ ERRO, nunca zero ----------

func TestAOS261_EvaluateRun_SemFonte_ErroExplicito(t *testing.T) {
	s := New(&spyBudgetReader{limit: budget.Amount{Tokens: 1000}}, nil, nil, stubReflector{}, nil)

	ev, err := s.EvaluateRun(context.Background(), "run-1", 1, "run-1")
	if !errors.Is(err, ErrNilBurndownSource) {
		t.Fatalf("sem BurndownSource devia ser fail-closed com ErrNilBurndownSource, got err=%v ev=%+v", err, ev)
	}
	// NÃO-VACUOSIDADE do critério: o defeito que se remove é precisamente devolver um
	// burn-down de 0% com err nil. Se a fracção viesse 0 SEM erro, o teste acima passaria
	// à mesma numa implementação errada que só não tivesse a guarda — por isso confirma-se
	// que a avaliação devolvida é o zero-value e não uma leitura apresentável.
	if ev.Burndown.Fraction != 0 || ev.Turns != 0 || ev.Warning != nil {
		t.Fatalf("a avaliação sem fonte não pode trazer leitura nenhuma: %+v", ev)
	}
}

func TestAOS261_EvaluateRun_FonteEmErro_PropagaSemInventarZero(t *testing.T) {
	semDados := errors.New("sem ledger para este run")
	s := New(&spyBudgetReader{limit: budget.Amount{Tokens: 1000}}, nil, nil, stubReflector{}, nil,
		WithBurndownSource(&fakeSource{err: semDados}))

	if _, err := s.EvaluateRun(context.Background(), "run-1", 1, "run-1"); !errors.Is(err, semDados) {
		t.Fatalf("o erro da fonte tem de subir tal-qual (nunca virar 0%%), got %v", err)
	}
}

func TestAOS261_EvaluateRun_SemReader_FailClosed(t *testing.T) {
	s := New(nil, nil, nil, stubReflector{}, nil, WithBurndownSource(&fakeSource{}))
	if _, err := s.EvaluateRun(context.Background(), "run-1", 1, "run-1"); !errors.Is(err, ErrNilBudgetReader) {
		t.Fatalf("sem tecto não há denominador — esperava ErrNilBudgetReader, got %v", err)
	}
}

// A via ANTIGA (por spans) deixou de devolver 0% quando não há spans nenhuns: nenhum nó os
// produz/retém, logo `nil` era o caso NORMAL e a superfície mentia em todas as leituras.
func TestAOS261_EvaluatePorSpans_NilEErro_NaoZero(t *testing.T) {
	s := New(&spyBudgetReader{limit: budget.Amount{Tokens: 1000}}, nil, nil, stubReflector{}, nil)
	if _, err := s.Evaluate(context.Background(), nil, fixedTraceHex(), "tree-1"); !errors.Is(err, ErrNoBurndownSpans) {
		t.Fatalf("Evaluate com spans nil devia ser fail-closed, got %v", err)
	}
	// Uma fatia VAZIA mas não-nil continua a ser uma leitura legítima (o chamador afirma
	// ter olhado): não se quebra quem tenha spans e nenhum seja `chat`.
	if _, err := s.Evaluate(context.Background(), []otelgenai.SpanData{}, fixedTraceHex(), "tree-1"); err != nil {
		t.Fatalf("fatia vazia não-nil é leitura legítima, got %v", err)
	}
}

// --- (2) A leitura REAL: a fracção vem da fonte, não de spans -----------------------

func TestAOS261_EvaluateRun_FraccaoDerivaDaFonteEDoTecto(t *testing.T) {
	reader := &spyBudgetReader{limit: budget.Amount{Tokens: 1000}}
	src := &fakeSource{consumption: RunConsumption{
		Consumed: budget.Amount{Tokens: 400},
		Turns:    4,
		LastTurn: 4,
	}}
	s := New(reader, nil, nil, stubReflector{snap: ProgressSnapshot{State: "running", Step: "chat#4"}}, nil,
		WithBurndownSource(src))

	ev, err := s.EvaluateRun(context.Background(), "run-1", 4, "run-1")
	if err != nil {
		t.Fatalf("EvaluateRun: %v", err)
	}
	if ev.Burndown.Consumed.Tokens != 400 || ev.Burndown.Limit.Tokens != 1000 {
		t.Fatalf("burn-down não deriva da fonte/tecto: %+v", ev.Burndown)
	}
	if ev.Burndown.Fraction < 0.3999 || ev.Burndown.Fraction > 0.4001 {
		t.Fatalf("fracção esperada 0.40, got %v", ev.Burndown.Fraction)
	}
	if ev.Turns != 4 {
		t.Fatalf("Turns devia vir da fonte (não-vacuosidade), got %d", ev.Turns)
	}
	if ev.Warning != nil || ev.State != PromptIdle {
		t.Fatalf("40%% está abaixo do limiar (~80%%) — não devia avisar: %+v", ev)
	}
	// O progresso vem da porta, não é inventado.
	if ev.Progress.State != "running" || ev.Progress.Step != "chat#4" {
		t.Fatalf("progresso não veio do ProgressReflector: %+v", ev.Progress)
	}
	if reader.limitCalls != 1 || src.calls != 1 {
		t.Fatalf("leitura pura: 1 Limit + 1 ConsumedByRun; got %d/%d", reader.limitCalls, src.calls)
	}
	// A superfície NUNCA lê Available para o burn-down: são consumos disjuntos do mesmo
	// tecto (tool calls vs turnos de modelo) e somá-los seria uma contabilidade nova.
	if reader.availCalls != 0 {
		t.Fatalf("EvaluateRun não pode ler Available (%d chamadas)", reader.availCalls)
	}
}

// --- (3) O aviso: dispara ao limiar, e o span é UMA VEZ por run (latch) -------------

func TestAOS262_Aviso_SpanUmaVezPorRun_Latch(t *testing.T) {
	tr := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	src := &fakeSource{consumption: RunConsumption{Consumed: budget.Amount{Tokens: 850}, Turns: 8, LastTurn: 8}}
	s := New(&spyBudgetReader{limit: budget.Amount{Tokens: 1000}}, nil, nil, stubReflector{}, tr,
		WithBurndownSource(src))

	// Três fronteiras de fim-de-turno consecutivas acima do limiar.
	for turn := 8; turn <= 10; turn++ {
		ev, err := s.EvaluateRun(context.Background(), "run-A", turn, "run-A")
		if err != nil {
			t.Fatalf("EvaluateRun turno %d: %v", turn, err)
		}
		if ev.Warning == nil || ev.State != PromptWarned {
			t.Fatalf("85%% >= limiar: esperava aviso no turno %d, got %+v", turn, ev)
		}
		if want := turn == 8; ev.Warning.SpanEmitted != want {
			t.Fatalf("turno %d: SpanEmitted=%v, esperado %v (o latch emite só à primeira)", turn, ev.Warning.SpanEmitted, want)
		}
		if ev.Warning.Turn != turn || ev.Warning.RunID != "run-A" {
			t.Fatalf("aviso sem a correlação (run, turno): %+v", ev.Warning)
		}
	}
	spans := tr.SpansByOperation(OpBudgetWarning)
	if len(spans) != 1 {
		t.Fatalf("o span de aviso tem de ser emitido UMA VEZ por run, got %d", len(spans))
	}
	if spans[0].Attributes[otelgenai.AttrRunID] != "run-A" {
		t.Fatalf("span de aviso sem run_id: %+v", spans[0].Attributes)
	}
	if spans[0].Attributes[AttrWarningTurn] != int64(8) {
		t.Fatalf("span de aviso sem o turno da correlação (AOS-261): %+v", spans[0].Attributes)
	}
	if spans[0].Attributes[AttrConsumedTokens] != int64(850) || spans[0].Attributes[AttrLimitTokens] != int64(1000) {
		t.Fatalf("span de aviso sem a dimensão que DECIDE (tokens): %+v", spans[0].Attributes)
	}

	// Um run DIFERENTE não é calado pelo latch do primeiro.
	ev, err := s.EvaluateRun(context.Background(), "run-B", 1, "run-B")
	if err != nil {
		t.Fatalf("EvaluateRun run-B: %v", err)
	}
	if ev.Warning == nil || !ev.Warning.SpanEmitted {
		t.Fatalf("o latch é POR RUN — run-B devia avisar e emitir: %+v", ev.Warning)
	}
	if got := len(tr.SpansByOperation(OpBudgetWarning)); got != 2 {
		t.Fatalf("esperava 2 spans (um por run), got %d", got)
	}

	// ForgetRun liberta o latch (o composition root chama-o no fim do run).
	s.ForgetRun("run-A")
	ev, err = s.EvaluateRun(context.Background(), "run-A", 11, "run-A")
	if err != nil {
		t.Fatalf("EvaluateRun após ForgetRun: %v", err)
	}
	if !ev.Warning.SpanEmitted {
		t.Fatal("ForgetRun devia libertar o latch do run")
	}
}

// --- (4) A ABSTINÊNCIA: a primeira entrega NÃO apresenta opções de decisão ----------

func TestAOS262_AvisoNaoApresentaOpcoesDeDecisao(t *testing.T) {
	tr := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	src := &fakeSource{consumption: RunConsumption{Consumed: budget.Amount{Tokens: 999}, Turns: 9, LastTurn: 9}}
	s := New(&spyBudgetReader{limit: budget.Amount{Tokens: 1000}}, nil, nil, stubReflector{}, tr,
		WithBurndownSource(src))

	ev, err := s.EvaluateRun(context.Background(), "run-1", 9, "run-1")
	if err != nil {
		t.Fatalf("EvaluateRun: %v", err)
	}
	// O tipo do resultado NÃO TEM campo de opções — a prova estrutural está na
	// compilação. O que se sela aqui é o comportamento observável: o estado é `warned`
	// (avisado) e nunca `prompting` (à espera de escolha), e NENHUM span de PROMPT é
	// emitido — só o de aviso. Um consumidor do canal de leitura que visse
	// aos.control.exhaustion_prompt inferiria, correctamente, que há uma escolha a fazer;
	// e não há: extend/summarize_stop/abort não têm executor nem autoridade (AOS-263).
	if ev.State != PromptWarned {
		t.Fatalf("estado esperado warned (não prompting), got %v", ev.State)
	}
	if n := len(tr.SpansByOperation(OpExhaustionPrompt)); n != 0 {
		t.Fatalf("a primeira entrega NÃO apresenta prompt de exaustão: %d spans %s", n, OpExhaustionPrompt)
	}
	if n := len(tr.SpansByOperation(OpExhaustionDecision)); n != 0 {
		t.Fatalf("a primeira entrega NÃO decide nada: %d spans %s", n, OpExhaustionDecision)
	}
	if n := len(tr.SpansByOperation(OpBudgetWarning)); n != 1 {
		t.Fatalf("esperava exactamente 1 span de aviso, got %d", n)
	}
}

// --- (5) ValidThreshold é a MESMA noção que WithThreshold usa -----------------------

func TestAOS262_ValidThreshold_MesmaNocaoDoWithThreshold(t *testing.T) {
	for _, bad := range []float64{0, -0.1, 1, 1.5} {
		if ValidThreshold(bad) {
			t.Fatalf("%v devia ser inválido", bad)
		}
		if s := New(&spyBudgetReader{}, nil, nil, stubReflector{}, nil, WithThreshold(bad)); s.Threshold() != DefaultThreshold {
			t.Fatalf("WithThreshold(%v) devia cair no default", bad)
		}
	}
	for _, ok := range []float64{0.01, 0.5, 0.8, 0.99} {
		if !ValidThreshold(ok) {
			t.Fatalf("%v devia ser válido", ok)
		}
		if s := New(&spyBudgetReader{}, nil, nil, stubReflector{}, nil, WithThreshold(ok)); s.Threshold() != ok {
			t.Fatalf("WithThreshold(%v) não foi aplicado", ok)
		}
	}
}
