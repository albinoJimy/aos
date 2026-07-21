package autonomysurface_test

import (
	"context"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	autonomysurface "github.com/aos-ref/control-plane/governance/autonomy-surface"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// ---------------------------------------------------------------------------
// Fixtures deterministas
// ---------------------------------------------------------------------------

const (
	testAgent  = "agent-alpha"
	testDomain = "billing"
	testActor  = "operator-1"
	testRunID  = "run-xyz"
)

// fixedClock devolve um relógio determinista para datar as transições no registo.
func fixedClock() func() time.Time {
	t := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// relFunc adapta uma função a [autonomy.ReliabilitySource]/ReliabilityReader.
type relFunc func(agent, domain string, window time.Duration) autonomy.Reliability

func (f relFunc) Reliability(agent, domain string, window time.Duration) autonomy.Reliability {
	return f(agent, domain, window)
}

// spyReviewer regista as chamadas a RequestReview e devolve um veredicto fixo — prova
// que a superfície DELEGA a decisão (não a toma).
type spyReviewer struct {
	calls  int
	change autonomy.LevelChange
	ok     bool
	err    error
}

func (s *spyReviewer) RequestReview(ctx context.Context, agent, domain string) (autonomy.LevelChange, bool, error) {
	s.calls++
	return s.change, s.ok, s.err
}

// newRegistryAt cria um registo e fixa o nível inicial do par de teste (com motivo/actor,
// como AOS-089 exige), devolvendo o registo pronto a LER.
func newRegistryAt(t *testing.T, level autonomy.Level) *autonomy.LevelRegistry {
	t.Helper()
	reg := autonomy.NewLevelRegistry(autonomy.WithClock(fixedClock()))
	if _, err := reg.SetLevel(context.Background(), testAgent, testDomain, level, "seed", testActor); err != nil {
		t.Fatalf("seed SetLevel: %v", err)
	}
	return reg
}

// cfgWith devolve uma config válida com o override_rate_max dado (o limiar headline do
// progresso) e tecto L5.
func cfgWith(t *testing.T, overrideMax float64) autonomy.AutonomyControlConfig {
	t.Helper()
	cfg, err := autonomy.NewAutonomyControlConfig("1.0.0", 0.02, overrideMax, 30*24*time.Hour, 2, autonomy.L1, autonomy.L5)
	if err != nil {
		t.Fatalf("NewAutonomyControlConfig: %v", err)
	}
	return cfg
}

// ---------------------------------------------------------------------------
// (a) LEITURA DE NÍVEL — AC1: Current == LevelReader.LevelFor (== AOS-089)
// ---------------------------------------------------------------------------

func TestBuildLevelView_CurrentReadsRegistry(t *testing.T) {
	reg := newRegistryAt(t, autonomy.L3)
	s, err := autonomysurface.New(reg, cfgWith(t, 0.40))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	v := s.BuildLevelView(context.Background(), testAgent, testDomain)

	if v.Current != reg.LevelFor(testAgent, testDomain) {
		t.Fatalf("Current=%s, quero == LevelReader.LevelFor=%s", v.Current, reg.LevelFor(testAgent, testDomain))
	}
	if v.Current != autonomy.L3 {
		t.Fatalf("Current=%s, quero L3 (o nível semeado em AOS-089)", v.Current)
	}
	if v.NextLevel != autonomy.L4 {
		t.Fatalf("NextLevel=%s, quero L4 (Current+1 abaixo do tecto)", v.NextLevel)
	}
}

// Um par SEM registo é fail-closed em L0 (contrato de AOS-089), lido tal e qual.
func TestBuildLevelView_UnregisteredIsL0(t *testing.T) {
	reg := autonomy.NewLevelRegistry(autonomy.WithClock(fixedClock()))
	s, err := autonomysurface.New(reg, cfgWith(t, 0.40))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	v := s.BuildLevelView(context.Background(), "ghost", "unknown")
	if v.Current != autonomy.L0 {
		t.Fatalf("Current=%s, quero L0 fail-closed para um par sem registo", v.Current)
	}
}

// L5 (tecto): não há próximo nível — NextLevel == Current e o progresso di-lo.
func TestBuildLevelView_AtCeilingNoNext(t *testing.T) {
	reg := newRegistryAt(t, autonomy.L5)
	s, err := autonomysurface.New(reg, cfgWith(t, 0.40))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	v := s.BuildLevelView(context.Background(), testAgent, testDomain)
	if v.NextLevel != autonomy.L5 {
		t.Fatalf("NextLevel=%s, quero L5 (== Current, sem próxima promoção)", v.NextLevel)
	}
	if v.Progress.Fraction != 0 || v.Progress.WindowOK {
		t.Fatalf("no tecto o progresso deve ser vazio, tenho %+v", v.Progress)
	}
	if autonomysurface.Eligible(v) {
		t.Fatalf("no tecto o par NÃO deve ser elegível a mais autonomia")
	}
}

// ---------------------------------------------------------------------------
// (b) PROGRESSO — AC2: reflecte a fiabilidade medida (ReliabilityReader) vs o limiar;
// a Fraction MUDA com a métrica.
// ---------------------------------------------------------------------------

// TestProgress_ErrorRateIsBindingConstraint prova que o progresso reflecte o gate
// MULTI-DIMENSIONAL de AOS-090: um override-rate excelente NÃO basta se o error-rate
// exceder o limiar. A Fraction é governada pela restrição vinculativa (o mínimo), pelo
// que a superfície não sinaliza elegibilidade optimista que a política negaria.
func TestProgress_ErrorRateIsBindingConstraint(t *testing.T) {
	reg := newRegistryAt(t, autonomy.L3)
	cfg := cfgWith(t, 0.40) // override_rate_max=0.40 (threshold 0.60), error_rate_max=0.02 (threshold 0.98)

	// Override EXCELENTE (measured 0.95, fraction override 1.58) MAS error-rate ALTO
	// (0.30 ⇒ errMeasured 0.70, fraction erro 0.70/0.98=0.71): o gate exige AMBOS.
	rel := relFunc(func(_, _ string, _ time.Duration) autonomy.Reliability {
		return autonomy.Reliability{ErrorRate: 0.30, OverrideRate: 0.05, WindowOK: true}
	})
	s, err := autonomysurface.New(reg, cfg, autonomysurface.WithReliabilityReader(rel))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	v := s.BuildLevelView(context.Background(), testAgent, testDomain)

	if v.Progress.Fraction >= 1.0 {
		t.Fatalf("Fraction=%.3f: o error-rate alto devia limitar o progresso abaixo de 1.0 (gate multi-dimensional)", v.Progress.Fraction)
	}
	if autonomysurface.Eligible(v) {
		t.Fatal("elegivel com error-rate acima do limiar: a superficie sinalizou uma elegibilidade que a politica negaria")
	}
	// A dimensão de erro está exposta na apresentação.
	if v.Progress.ErrorThreshold <= 0 || v.Progress.ErrorMeasured >= v.Progress.ErrorThreshold {
		t.Fatalf("dimensao de erro nao reflectida: measured=%.3f threshold=%.3f", v.Progress.ErrorMeasured, v.Progress.ErrorThreshold)
	}
}

func TestProgress_ReflectsMeasuredReliability(t *testing.T) {
	reg := newRegistryAt(t, autonomy.L3)
	cfg := cfgWith(t, 0.40) // limiar: override_rate_max=0.40 ⇒ threshold=0.60

	// Fiabilidade ALTA (override baixo): measured=0.90, fraction=0.90/0.60=1.5 (>=1).
	relHigh := relFunc(func(_, _ string, _ time.Duration) autonomy.Reliability {
		return autonomy.Reliability{ErrorRate: 0.01, OverrideRate: 0.10, WindowOK: true}
	})
	sHigh, err := autonomysurface.New(reg, cfg, autonomysurface.WithReliabilityReader(relHigh))
	if err != nil {
		t.Fatalf("New(high): %v", err)
	}
	vHigh := sHigh.BuildLevelView(context.Background(), testAgent, testDomain)

	// Fiabilidade BAIXA (override alto): measured=0.50, fraction=0.50/0.60=0.83 (<1).
	relLow := relFunc(func(_, _ string, _ time.Duration) autonomy.Reliability {
		return autonomy.Reliability{ErrorRate: 0.01, OverrideRate: 0.50, WindowOK: true}
	})
	sLow, err := autonomysurface.New(reg, cfg, autonomysurface.WithReliabilityReader(relLow))
	if err != nil {
		t.Fatalf("New(low): %v", err)
	}
	vLow := sLow.BuildLevelView(context.Background(), testAgent, testDomain)

	if !(vHigh.Progress.Fraction > vLow.Progress.Fraction) {
		t.Fatalf("a Fraction deve MUDAR com a métrica: high=%.3f não > low=%.3f",
			vHigh.Progress.Fraction, vLow.Progress.Fraction)
	}
	if vHigh.Progress.Measured <= vLow.Progress.Measured {
		t.Fatalf("measured deve subir quando o override-rate desce: high=%.3f low=%.3f",
			vHigh.Progress.Measured, vLow.Progress.Measured)
	}
	if vHigh.Progress.Threshold != 0.60 {
		t.Fatalf("threshold=%.3f, quero 0.60 (1 − override_rate_max)", vHigh.Progress.Threshold)
	}
	// A fiabilidade alta cumpre o critério headline; a baixa não.
	if vHigh.Progress.Fraction < 1.0 {
		t.Fatalf("high fraction=%.3f, quero >= 1.0", vHigh.Progress.Fraction)
	}
	if vLow.Progress.Fraction >= 1.0 {
		t.Fatalf("low fraction=%.3f, quero < 1.0", vLow.Progress.Fraction)
	}
}

// Janela sem cobertura (WindowOK=false) ⇒ nunca elegível, por muito boa que a métrica
// pareça num instante.
func TestProgress_WindowNotOKNotEligible(t *testing.T) {
	reg := newRegistryAt(t, autonomy.L3)
	rel := relFunc(func(_, _ string, _ time.Duration) autonomy.Reliability {
		return autonomy.Reliability{ErrorRate: 0.0, OverrideRate: 0.0, WindowOK: false}
	})
	s, err := autonomysurface.New(reg, cfgWith(t, 0.40), autonomysurface.WithReliabilityReader(rel))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	v := s.BuildLevelView(context.Background(), testAgent, testDomain)
	if autonomysurface.Eligible(v) {
		t.Fatalf("com WindowOK=false o par NÃO deve ser elegível (janela incompleta)")
	}
}

// Sem ReliabilityReader o progresso é indisponível (não inventa um valor).
func TestProgress_NoSignalUnavailable(t *testing.T) {
	reg := newRegistryAt(t, autonomy.L2)
	s, err := autonomysurface.New(reg, cfgWith(t, 0.40))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	v := s.BuildLevelView(context.Background(), testAgent, testDomain)
	if v.Progress.WindowOK || v.Progress.Measured != 0 {
		t.Fatalf("sem sinal o progresso deve ser vazio, tenho %+v", v.Progress)
	}
	if v.Progress.Criteria == "" {
		t.Fatalf("o progresso deve explicar a indisponibilidade nos Criteria")
	}
}

// ---------------------------------------------------------------------------
// (c) TRANSIÇÃO — AC3: TransitionView mostra From/To/Reason de HistoryFor SEM a
// superfície decidir (só LÊ).
// ---------------------------------------------------------------------------

func TestBuildLevelView_TransitionsExplainHistory(t *testing.T) {
	reg := autonomy.NewLevelRegistry(autonomy.WithClock(fixedClock()))
	ctx := context.Background()
	// Uma promoção e depois uma demoção seladas por AOS-089/090 (com motivo/actor).
	if _, err := reg.SetLevel(ctx, testAgent, testDomain, autonomy.L3, "promocao sustentada", autonomy.ControllerActor); err != nil {
		t.Fatalf("SetLevel L3: %v", err)
	}
	if _, err := reg.SetLevel(ctx, testAgent, testDomain, autonomy.L1, "anomalia override_rate_spike", autonomy.ControllerActor); err != nil {
		t.Fatalf("SetLevel L1: %v", err)
	}

	s, err := autonomysurface.New(reg, cfgWith(t, 0.40))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	v := s.BuildLevelView(ctx, testAgent, testDomain)

	if len(v.Transitions) != 2 {
		t.Fatalf("nº de transições=%d, quero 2 (LIDAS de HistoryFor)", len(v.Transitions))
	}
	// Elo 1: promoção L0->L3 com o seu motivo.
	if v.Transitions[0].From != autonomy.L0 || v.Transitions[0].To != autonomy.L3 {
		t.Fatalf("transição[0] = %s->%s, quero L0->L3", v.Transitions[0].From, v.Transitions[0].To)
	}
	if v.Transitions[0].Reason != "promocao sustentada" {
		t.Fatalf("transição[0].Reason=%q, quero o motivo selado por AOS-090", v.Transitions[0].Reason)
	}
	// Elo 2: demoção L3->L1 — a superfície EXPLICA-a, marcada como demoção.
	if !v.Transitions[1].IsDemotion() {
		t.Fatalf("transição[1] deveria ser marcada como demoção (To<From)")
	}
	if v.Transitions[1].Reason != "anomalia override_rate_spike" {
		t.Fatalf("transição[1].Reason=%q, quero o motivo da demoção", v.Transitions[1].Reason)
	}

	// PROVA POR AUSÊNCIA: a superfície LÊ, não decide. O nível no registo é EXACTAMENTE
	// o que AOS-090 fixou (L1) — a construção da vista não o alterou.
	if got := reg.LevelFor(testAgent, testDomain); got != autonomy.L1 {
		t.Fatalf("nível no registo=%s após BuildLevelView, quero L1 inalterado (a superfície não decide)", got)
	}
	if n := len(reg.HistoryFor(testAgent, testDomain)); n != 2 {
		t.Fatalf("histórico=%d após BuildLevelView, quero 2 (a superfície não escreveu)", n)
	}
}

// ---------------------------------------------------------------------------
// (d) SOLICITAÇÃO — AC4: RequestMoreAutonomy DELEGA a LevelReviewer.RequestReview e
// RESPEITA a decisão (aprovado/negado); a superfície não altera o nível se negado.
// ---------------------------------------------------------------------------

func TestRequestMoreAutonomy_DelegatesAndRespectsApproval(t *testing.T) {
	reg := newRegistryAt(t, autonomy.L3)
	spy := &spyReviewer{
		change: autonomy.LevelChange{Agent: testAgent, Domain: testDomain, Old: autonomy.L3, New: autonomy.L4, Reason: "promocao sustentada", Actor: autonomy.ControllerActor},
		ok:     true,
	}
	s, err := autonomysurface.New(reg, cfgWith(t, 0.40), autonomysurface.WithReviewer(spy))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ch, changed, err := s.RequestMoreAutonomy(context.Background(), testAgent, testDomain)
	if err != nil {
		t.Fatalf("RequestMoreAutonomy: %v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("RequestReview chamado %d vezes, quero 1 (a superfície DELEGA)", spy.calls)
	}
	if !changed || ch.New != autonomy.L4 {
		t.Fatalf("veredicto=%v/%s, quero a decisão da política (aprovado L4)", changed, ch.New)
	}
}

func TestRequestMoreAutonomy_RespectsDenial(t *testing.T) {
	reg := newRegistryAt(t, autonomy.L3)
	spy := &spyReviewer{ok: false} // política NEGA: mantém
	s, err := autonomysurface.New(reg, cfgWith(t, 0.40), autonomysurface.WithReviewer(spy))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, changed, err := s.RequestMoreAutonomy(context.Background(), testAgent, testDomain)
	if err != nil {
		t.Fatalf("RequestMoreAutonomy: %v", err)
	}
	if spy.calls != 1 {
		t.Fatalf("RequestReview chamado %d vezes, quero 1", spy.calls)
	}
	if changed {
		t.Fatalf("a política NEGOU — changed deve ser false")
	}
	// A superfície NÃO altera o nível quando negado: o registo fica em L3.
	if got := reg.LevelFor(testAgent, testDomain); got != autonomy.L3 {
		t.Fatalf("nível=%s após pedido NEGADO, quero L3 inalterado (a superfície não decide)", got)
	}
}

// Sem reviewer configurado o pedido é recusado fail-closed — nunca auto-promoção.
func TestRequestMoreAutonomy_NoReviewerFailsClosed(t *testing.T) {
	reg := newRegistryAt(t, autonomy.L3)
	s, err := autonomysurface.New(reg, cfgWith(t, 0.40))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, changed, err := s.RequestMoreAutonomy(context.Background(), testAgent, testDomain)
	if err != autonomysurface.ErrNoReviewer {
		t.Fatalf("erro=%v, quero ErrNoReviewer", err)
	}
	if changed {
		t.Fatalf("changed deve ser false sem reviewer")
	}
	if got := reg.LevelFor(testAgent, testDomain); got != autonomy.L3 {
		t.Fatalf("nível=%s, quero L3 inalterado", got)
	}
}

// ---------------------------------------------------------------------------
// (e) DEMOÇÃO — AC5: NotifyLevelChange de uma demoção (To<From) produz a DemotionNotice
// IMEDIATA com o Reason (não esconde).
// ---------------------------------------------------------------------------

func TestNotifyLevelChange_DemotionSurfacedImmediately(t *testing.T) {
	reg := newRegistryAt(t, autonomy.L1)
	s, err := autonomysurface.New(reg, cfgWith(t, 0.40))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	now := fixedClock()()
	demotion := autonomy.LevelChange{
		Agent:  testAgent,
		Domain: testDomain,
		Old:    autonomy.L4,
		New:    autonomy.L2,
		Reason: "anomalia unsafe_action: democao determinista L4->L2",
		Actor:  autonomy.ControllerActor,
		At:     now,
	}

	notice, ok := s.NotifyLevelChange(context.Background(), demotion)
	if !ok {
		t.Fatalf("NotifyLevelChange deveria reconhecer a demoção (To<From)")
	}
	if notice.From != autonomy.L4 || notice.To != autonomy.L2 {
		t.Fatalf("aviso=%s->%s, quero L4->L2 sem ocultação", notice.From, notice.To)
	}
	if notice.Reason != demotion.Reason {
		t.Fatalf("Reason=%q, quero o motivo selado (não escondido)", notice.Reason)
	}
	if notice.At != now {
		t.Fatalf("At=%v, quero o instante da demoção (imediato)", notice.At)
	}
	if !autonomysurface.IsDemotion(demotion) {
		t.Fatalf("IsDemotion deveria ser true para L4->L2")
	}
}

// Uma PROMOÇÃO não gera aviso de demoção (o caminho de promoção é a vista, não um aviso).
func TestNotifyLevelChange_PromotionNoNotice(t *testing.T) {
	reg := newRegistryAt(t, autonomy.L1)
	s, err := autonomysurface.New(reg, cfgWith(t, 0.40))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	promo := autonomy.LevelChange{Agent: testAgent, Domain: testDomain, Old: autonomy.L2, New: autonomy.L3, Reason: "promocao"}
	if _, ok := s.NotifyLevelChange(context.Background(), promo); ok {
		t.Fatalf("uma promoção NÃO deve produzir DemotionNotice")
	}
	if autonomysurface.IsDemotion(promo) {
		t.Fatalf("IsDemotion deveria ser false para L2->L3")
	}
}

// ---------------------------------------------------------------------------
// SPAN — DoD: cada interacção emite um span aos.autonomy.level ligado ao trace, com o
// tipo de interacção e o run_id, sem segredos.
// ---------------------------------------------------------------------------

func TestSpans_InteractionsEmitted(t *testing.T) {
	reg := newRegistryAt(t, autonomy.L3)
	rec := &agentruntime.RecordingTracer{}
	rel := relFunc(func(_, _ string, _ time.Duration) autonomy.Reliability {
		return autonomy.Reliability{OverrideRate: 0.10, WindowOK: true}
	})
	spy := &spyReviewer{ok: false}
	s, err := autonomysurface.New(reg, cfgWith(t, 0.40),
		autonomysurface.WithReliabilityReader(rel),
		autonomysurface.WithReviewer(spy),
		autonomysurface.WithTracer(rec),
		autonomysurface.WithRunID(testRunID),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	s.BuildLevelView(ctx, testAgent, testDomain)
	if _, _, err := s.RequestMoreAutonomy(ctx, testAgent, testDomain); err != nil {
		t.Fatalf("RequestMoreAutonomy: %v", err)
	}
	s.NotifyLevelChange(ctx, autonomy.LevelChange{Agent: testAgent, Domain: testDomain, Old: autonomy.L4, New: autonomy.L2, Reason: "anomalia"})

	spans := rec.SpansByOperation(autonomy.OpAutonomyLevel)
	if len(spans) != 3 {
		t.Fatalf("nº de spans de interacção=%d, quero 3 (view, request, demotion)", len(spans))
	}
	wantKinds := []string{autonomysurface.SurfaceKindView, autonomysurface.SurfaceKindRequest, autonomysurface.SurfaceKindDemotion}
	for i, sp := range spans {
		if !sp.Ended {
			t.Errorf("span %d não foi fechado", i)
		}
		if got := sp.Attributes[autonomysurface.AttrAutonomySurfaceKind]; got != wantKinds[i] {
			t.Errorf("span %d kind=%v, quero %q", i, got, wantKinds[i])
		}
		if got := sp.Attributes[agentruntime.AttrRunID]; got != testRunID {
			t.Errorf("span %d run_id=%v, quero %q", i, got, testRunID)
		}
		if got := sp.Attributes[autonomy.AttrAutonomyAgent]; got != testAgent {
			t.Errorf("span %d agent=%v, quero %q", i, got, testAgent)
		}
	}
	// O aviso de demoção carrega o motivo no span (transparência da métrica selada).
	if got := spans[2].Attributes[autonomy.AttrAutonomyReason]; got != "anomalia" {
		t.Errorf("span de demoção reason=%v, quero \"anomalia\"", got)
	}
}
