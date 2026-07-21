package authoringsurface

import (
	"context"
	"errors"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// ---------------------------------------------------------------------------
// Spies/fakes deterministas das portas — nenhum comete efeitos nem ratifica.
// ---------------------------------------------------------------------------

// dryRunSpy é um [DryRunner] que CAPTURA efeitos (untrusted) e NUNCA os comete. O
// campo committed permite forçar a violação capital (Committed=true) para provar a
// validação fail-closed. O campo committedWorld prova, por ausência de mutação, que o
// spy nunca "comete" nada no mundo (mantém-se sempre false).
type dryRunSpy struct {
	calls          int
	committed      bool // o valor a devolver em DryRunResult.Committed
	egressBlocked  bool
	effects        []CapturedEffect
	committedWorld bool // NUNCA passa a true — prova que nada é cometido
	err            error
}

func (d *dryRunSpy) DryRun(_ context.Context, _ CandidateRef) (DryRunResult, error) {
	d.calls++
	if d.err != nil {
		return DryRunResult{}, d.err
	}
	// Um dry-run NUNCA comete efeitos no mundo: committedWorld fica intocado.
	return DryRunResult{
		Committed:     d.committed,
		EgressBlocked: d.egressBlocked,
		Effects:       d.effects,
		Output:        "simulated-output",
	}, nil
}

type attributionStub struct {
	att Attribution
	err error
}

func (a attributionStub) Attribution(_ context.Context, _, _ string) (Attribution, error) {
	return a.att, a.err
}

type evalStub struct {
	res    otelgenai.EvaluationResult
	canary bool
	err    error
}

func (e evalStub) EvalOutcome(_ context.Context, _, _ string) (otelgenai.EvaluationResult, bool, error) {
	return e.res, e.canary, e.err
}

// submitSpy prova a DELEGAÇÃO ao gate de AOS-096 e devolve o RatificationID. NÃO tem
// método Ratify — estruturalmente a superfície não pode ratificar através dele.
type submitSpy struct {
	calls int
	ratID string
	err   error
	got   CandidateRef
}

func (s *submitSpy) Submit(_ context.Context, c CandidateRef) (string, error) {
	s.calls++
	s.got = c
	return s.ratID, s.err
}

func recordingLoop(t *testing.T, dr DryRunner, opts ...Option) (*AuthoringLoop, *otelgenai.RecordingTracer) {
	t.Helper()
	tr := otelgenai.NewRecordingTracer(&otelgenai.SequentialIDGenerator{})
	opts = append(opts, WithTracer(tr), WithRunID("run-abc"))
	l, err := New(dr, opts...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l, tr
}

var validCandidate = CandidateRef{Skill: "summarize", Version: "1.2.3"}

// ---------------------------------------------------------------------------
// (a) dry-run — efeitos capturados untrusted, nada cometido, egress bloqueado.
// ---------------------------------------------------------------------------

func TestDryRun_CapturesEffects_NothingCommitted(t *testing.T) {
	spy := &dryRunSpy{
		committed:     false,
		egressBlocked: true,
		effects: []CapturedEffect{
			{Kind: "egress", Descriptor: "api.example.com:443"},
			{Kind: "fs_write", Descriptor: "/overlay/out.txt", Taint: "trusted"}, // será normalizado
		},
	}
	loop, tr := recordingLoop(t, spy)

	res, err := loop.DryRun(context.Background(), validCandidate)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if res.Committed {
		t.Fatal("Committed deve ser false — nada pode ser cometido no dry-run")
	}
	if !res.EgressBlocked {
		t.Fatal("EgressBlocked deve ser true (default-deny)")
	}
	if spy.committedWorld {
		t.Fatal("o spy nunca comete efeitos no mundo — committedWorld deve manter-se false")
	}
	if len(res.Effects) != 2 {
		t.Fatalf("esperados 2 efeitos capturados, obtidos %d", len(res.Effects))
	}
	// Todos os efeitos capturados são untrusted (normalizado pela superfície).
	for i, e := range res.Effects {
		if e.Taint != EffectTaintUntrusted {
			t.Fatalf("efeito %d: taint=%q, esperado untrusted", i, e.Taint)
		}
	}
	// Span dry_run emitido com committed=false e egress_blocked=true.
	spans := tr.SpansByOperation(OpAuthoringSurface)
	if len(spans) != 1 {
		t.Fatalf("esperado 1 span, obtidos %d", len(spans))
	}
	s := spans[0]
	if s.Attributes[AttrAuthoringKind] != SurfaceKindDryRun {
		t.Fatalf("kind=%v, esperado dry_run", s.Attributes[AttrAuthoringKind])
	}
	if s.Attributes[AttrCommitted] != false {
		t.Fatalf("span committed=%v, esperado false", s.Attributes[AttrCommitted])
	}
	if s.Attributes[AttrEgressBlocked] != true {
		t.Fatalf("span egress_blocked=%v, esperado true", s.Attributes[AttrEgressBlocked])
	}
	if !s.Ended {
		t.Fatal("span deve ser fechado")
	}
}

func TestDryRun_CommittedTrue_Rejected(t *testing.T) {
	// A violação capital: um DryRunResult com Committed=true é rejeitado fail-closed.
	spy := &dryRunSpy{committed: true, egressBlocked: true}
	loop, _ := recordingLoop(t, spy)

	_, err := loop.DryRun(context.Background(), validCandidate)
	if !errors.Is(err, ErrEffectCommitted) {
		t.Fatalf("esperado ErrEffectCommitted, obtido %v", err)
	}
}

func TestDryRun_InvalidCandidate_And_PropagatesError(t *testing.T) {
	loop, _ := recordingLoop(t, &dryRunSpy{})
	if _, err := loop.DryRun(context.Background(), CandidateRef{Skill: "x"}); !errors.Is(err, ErrInvalidCandidate) {
		t.Fatalf("esperado ErrInvalidCandidate, obtido %v", err)
	}
	sentinel := errors.New("sandbox boom")
	loop2, _ := recordingLoop(t, &dryRunSpy{err: sentinel})
	if _, err := loop2.DryRun(context.Background(), validCandidate); !errors.Is(err, sentinel) {
		t.Fatalf("esperado erro do DryRunner propagado, obtido %v", err)
	}
}

// ---------------------------------------------------------------------------
// (b) atribuição — autor, versão SemVer e proveniência apresentados correctamente.
// ---------------------------------------------------------------------------

func TestAttribution_PresentsAuthorVersionProvenance(t *testing.T) {
	want := Attribution{
		Author:         "agent://planner-7",
		Version:        "1.2.3",
		OriginRunID:    "run-2026-07-19-xyz",
		ContentHashHex: "deadbeef",
		Provenance:     ProvenanceView{Origin: "self", Publisher: "planner-7", Trust: "first_seen"},
	}
	loop, tr := recordingLoop(t, &dryRunSpy{}, WithAttributionReader(attributionStub{att: want}))

	got, err := loop.Attribution(context.Background(), validCandidate)
	if err != nil {
		t.Fatalf("Attribution: %v", err)
	}
	if got != want {
		t.Fatalf("atribuicao=%+v, esperada %+v", got, want)
	}
	// Versão SemVer bem-formada (X.Y.Z).
	if got.Version != "1.2.3" {
		t.Fatalf("versao SemVer=%q, esperada 1.2.3", got.Version)
	}
	spans := tr.SpansByOperation(OpAuthoringSurface)
	if len(spans) != 1 || spans[0].Attributes[AttrAuthoringKind] != SurfaceKindAttribution {
		t.Fatalf("esperado 1 span attribution_view, obtidos %+v", spans)
	}
	if spans[0].Attributes[AttrAuthor] != want.Author {
		t.Fatalf("span author=%v, esperado %q", spans[0].Attributes[AttrAuthor], want.Author)
	}
}

func TestAttribution_NoReader_FailClosed(t *testing.T) {
	loop, _ := recordingLoop(t, &dryRunSpy{})
	if _, err := loop.Attribution(context.Background(), validCandidate); !errors.Is(err, ErrNoAttributionReader) {
		t.Fatalf("esperado ErrNoAttributionReader, obtido %v", err)
	}
}

// ---------------------------------------------------------------------------
// (c) encaminhamento — delega ao RatificationSubmitter, devolve o RatificationID,
//     e a superfície NUNCA ratifica (prova por ausência — sem caminho de Ratify).
// ---------------------------------------------------------------------------

func TestSubmitForRatification_Delegates_ReturnsRatID(t *testing.T) {
	spy := &submitSpy{ratID: "ratid-abc123"}
	loop, tr := recordingLoop(t, &dryRunSpy{}, WithRatificationSubmitter(spy))

	ratID, err := loop.SubmitForRatification(context.Background(), validCandidate)
	if err != nil {
		t.Fatalf("SubmitForRatification: %v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("esperada 1 delegacao ao Submit, obtidas %d", spy.calls)
	}
	if ratID != "ratid-abc123" {
		t.Fatalf("ratID=%q, esperado ratid-abc123", ratID)
	}
	if spy.got != validCandidate {
		t.Fatalf("candidata delegada=%+v, esperada %+v", spy.got, validCandidate)
	}
	// Prova por AUSÊNCIA: a superfície não expõe nenhum método de ratificação. Não há
	// caminho de Ratify — a única acção sobre o gate é submeter.
	if _, ok := any(loop).(interface {
		Ratify(context.Context, CandidateRef) (bool, error)
	}); ok {
		t.Fatal("a superficie NAO pode ter um caminho de Ratify")
	}
	spans := tr.SpansByOperation(OpAuthoringSurface)
	if len(spans) != 1 || spans[0].Attributes[AttrAuthoringKind] != SurfaceKindSubmit {
		t.Fatalf("esperado 1 span submit, obtidos %+v", spans)
	}
	if spans[0].Attributes[AttrRatificationID] != "ratid-abc123" {
		t.Fatalf("span ratification_id=%v", spans[0].Attributes[AttrRatificationID])
	}
}

func TestSubmitForRatification_NoSubmitter_FailClosed(t *testing.T) {
	loop, _ := recordingLoop(t, &dryRunSpy{})
	if _, err := loop.SubmitForRatification(context.Background(), validCandidate); !errors.Is(err, ErrNoSubmitter) {
		t.Fatalf("esperado ErrNoSubmitter, obtido %v", err)
	}
}

// ---------------------------------------------------------------------------
// (d) eval — Verdict/Score/CanaryPassed apresentados ANTES da submissão/decisão.
// ---------------------------------------------------------------------------

func TestEvalOutcome_PresentsVerdictScoreCanary(t *testing.T) {
	res := otelgenai.EvaluationResult{
		Suite:   "skill.summarize",
		EvalID:  "eval-42",
		Dataset: otelgenai.EvalDatasetGolden,
		Verdict: otelgenai.EvalPass,
		Score:   0.97,
	}
	loop, _ := recordingLoop(t, &dryRunSpy{}, WithEvalResultReader(evalStub{res: res, canary: true}))

	view, err := loop.EvalOutcome(context.Background(), validCandidate)
	if err != nil {
		t.Fatalf("EvalOutcome: %v", err)
	}
	if view.Verdict != "pass" || view.Score != 0.97 || !view.CanaryPassed {
		t.Fatalf("view=%+v, esperado pass/0.97/canary", view)
	}
	if !view.Admissible() {
		t.Fatal("pass + canary passado deve ser admissivel")
	}
	if view.Detail != "skill.summarize/eval-42" {
		t.Fatalf("detail=%q", view.Detail)
	}
}

func TestEvalOutcome_FailOrNoCanary_NotAdmissible(t *testing.T) {
	// Fail no eval.
	loop, _ := recordingLoop(t, &dryRunSpy{}, WithEvalResultReader(evalStub{
		res:    otelgenai.EvaluationResult{Verdict: otelgenai.EvalFail, Score: 0.4},
		canary: true,
	}))
	view, err := loop.EvalOutcome(context.Background(), validCandidate)
	if err != nil {
		t.Fatalf("EvalOutcome: %v", err)
	}
	if view.Admissible() {
		t.Fatal("verdict=fail nao deve ser admissivel")
	}
	// Pass mas canary falhou.
	loop2, _ := recordingLoop(t, &dryRunSpy{}, WithEvalResultReader(evalStub{
		res:    otelgenai.EvaluationResult{Verdict: otelgenai.EvalPass, Score: 0.9},
		canary: false,
	}))
	view2, _ := loop2.EvalOutcome(context.Background(), validCandidate)
	if view2.Admissible() {
		t.Fatal("canary falhado nao deve ser admissivel")
	}
}

func TestEvalOutcome_NoResult_And_NoReader_FailClosed(t *testing.T) {
	// Sem reader.
	loop, _ := recordingLoop(t, &dryRunSpy{})
	if _, err := loop.EvalOutcome(context.Background(), validCandidate); !errors.Is(err, ErrNoEvalReader) {
		t.Fatalf("esperado ErrNoEvalReader, obtido %v", err)
	}
	// Reader sem resultado (value-zero + canary false).
	loop2, _ := recordingLoop(t, &dryRunSpy{}, WithEvalResultReader(evalStub{}))
	if _, err := loop2.EvalOutcome(context.Background(), validCandidate); !errors.Is(err, ErrNoEvalResult) {
		t.Fatalf("esperado ErrNoEvalResult, obtido %v", err)
	}
}

// TestLoopOrder_EvalBeforeSubmit prova a ordem: o eval é apresentado ANTES da decisão
// de submeter (AC4). Corre o loop completo e verifica a sequência de leituras.
func TestLoopOrder_EvalBeforeSubmit(t *testing.T) {
	res := otelgenai.EvaluationResult{Verdict: otelgenai.EvalPass, Score: 0.95}
	submit := &submitSpy{ratID: "rid-1"}
	loop, _ := recordingLoop(t, &dryRunSpy{egressBlocked: true},
		WithAttributionReader(attributionStub{att: Attribution{Author: "a", Version: "1.2.3"}}),
		WithEvalResultReader(evalStub{res: res, canary: true}),
		WithRatificationSubmitter(submit),
	)
	ctx := context.Background()

	if _, err := loop.DryRun(ctx, validCandidate); err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if _, err := loop.Attribution(ctx, validCandidate); err != nil {
		t.Fatalf("Attribution: %v", err)
	}
	view, err := loop.EvalOutcome(ctx, validCandidate)
	if err != nil {
		t.Fatalf("EvalOutcome: %v", err)
	}
	// A decisão de submeter é do humano; a superfície só encaminha DEPOIS de o eval
	// estar visível. Aqui provamos que o eval está disponível antes de Submit ser chamado.
	if submit.calls != 0 {
		t.Fatal("Submit nao deve ter sido chamado antes da apresentacao do eval")
	}
	if !view.Admissible() {
		t.Fatal("eval deve estar visivel e admissivel antes da submissao")
	}
	if _, err := loop.SubmitForRatification(ctx, validCandidate); err != nil {
		t.Fatalf("SubmitForRatification: %v", err)
	}
	if submit.calls != 1 {
		t.Fatalf("esperada 1 submissao, obtidas %d", submit.calls)
	}
}

// ---------------------------------------------------------------------------
// (e) sem efeito/segredo — o dry-run nada comete e os spans/views não têm segredos.
// ---------------------------------------------------------------------------

func TestNoSecretsInSpans(t *testing.T) {
	res := otelgenai.EvaluationResult{Verdict: otelgenai.EvalPass, Score: 0.9}
	loop, tr := recordingLoop(t, &dryRunSpy{egressBlocked: true, effects: []CapturedEffect{{Kind: "egress", Descriptor: "blocked"}}},
		WithAttributionReader(attributionStub{att: Attribution{Author: "agent://x", Version: "1.2.3", ContentHashHex: "abcd"}}),
		WithEvalResultReader(evalStub{res: res, canary: true}),
		WithRatificationSubmitter(&submitSpy{ratID: "rid"}),
	)
	ctx := context.Background()
	_, _ = loop.DryRun(ctx, validCandidate)
	_, _ = loop.Attribution(ctx, validCandidate)
	_, _ = loop.SubmitForRatification(ctx, validCandidate)

	// Eixos permitidos (não-secretos) nos spans do loop. Qualquer outra chave é suspeita.
	allowed := map[string]bool{
		AttrAuthoringKind: true, AttrVersion: true, agentruntime.AttrRunID: true,
		AttrCommitted: true, AttrEgressBlocked: true, AttrEffectCount: true, AttrDryRunError: true,
		AttrAuthor: true, AttrTrust: true, AttrRatificationID: true, AttrSubmitError: true,
	}
	for _, s := range tr.SpansByOperation(OpAuthoringSurface) {
		for k := range s.Attributes {
			if !allowed[k] {
				t.Fatalf("atributo de span inesperado (possivel segredo): %q", k)
			}
		}
		// committed nunca é true.
		if v, ok := s.Attributes[AttrCommitted]; ok && v == true {
			t.Fatal("nenhum span pode reportar committed=true")
		}
	}
}

// TestNew_NilDryRunner_FailClosed cobre a construção fail-closed.
func TestNew_NilDryRunner_FailClosed(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrNilDryRunner) {
		t.Fatalf("esperado ErrNilDryRunner, obtido %v", err)
	}
	// WithTracer(nil) mantém o Noop (não deve entrar em pânico ao emitir).
	loop, err := New(&dryRunSpy{}, WithTracer(nil))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := loop.DryRun(context.Background(), validCandidate); err != nil {
		t.Fatalf("DryRun com Noop: %v", err)
	}
}
