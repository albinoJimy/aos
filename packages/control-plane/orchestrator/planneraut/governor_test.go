package planneraut

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// ---------------------------------------------------------------------------
// Mocks das portas.
// ---------------------------------------------------------------------------

// stubOverride serve uma taxa de override AUTORITATIVA (AOS-095) fixa (ou um erro).
// Thread-safe para os testes -race.
type stubOverride struct {
	mu   sync.Mutex
	rate float64
	err  error
}

func (s *stubOverride) set(rate float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rate = rate
}

func (s *stubOverride) OverrideRate(_ context.Context, _ DomainKey) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rate, s.err
}

// stubEval é o eval-gate de decomposição (AOS-241). Conta as chamadas (para provar
// que a pré-condição CORRE mesmo a L4/L5).
type stubEval struct {
	mu     sync.Mutex
	passed bool
	err    error
	calls  int
}

func (e *stubEval) Evaluate(_ context.Context, _ EvalRequest) (EvalOutcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	return EvalOutcome{Passed: e.passed}, e.err
}

func (e *stubEval) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

func testConfig() Config {
	return Config{
		Envelope:        DefaultEnvelope(),
		MinSample:       1,
		MinRecurrence:   2,
		MaxLevel:        L5,
		AutoApproveFrom: L4,
		SampleEveryN:    1,
	}
}

// healthyCounters é uma janela PERFEITAMENTE sã: tudo aprovado sem edição, sem
// re-planos, sem inválidos, custo calibrado, fracção de planeamento 1%.
func healthyCounters() Counters {
	return Counters{
		Plans:               100,
		ApprovedNoEdit:      100,
		Replans:             0,
		InvalidProposals:    0,
		CostSamples:         20,
		CostWithinTolerance: 20,
		PlanningUnits:       1,
		ExecutionUnits:      99,
	}
}

func newTestGovernor(t *testing.T, ov *stubOverride, ev *stubEval) *Governor {
	t.Helper()
	g, err := NewGovernor(testConfig(), ov, ev)
	if err != nil {
		t.Fatalf("NewGovernor: %v", err)
	}
	return g
}

// driveToLevel observa janelas sãs até o domínio atingir target (ou falha).
func driveToLevel(t *testing.T, g *Governor, dom DomainKey, target Level) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if g.Level(dom) >= target {
			return
		}
		if _, err := g.ObserveWindow(context.Background(), dom, healthyCounters()); err != nil {
			t.Fatalf("ObserveWindow: %v", err)
		}
	}
	if g.Level(dom) < target {
		t.Fatalf("não atingiu %s (ficou em %s)", target, g.Level(dom))
	}
}

// ---------------------------------------------------------------------------
// TESTE REQUERIDO: domínio ad-hoc NÃO promove (fica L0); recorrente promove.
// ---------------------------------------------------------------------------

func TestObserveWindow_AdHocStaysL0_RecurrentPromotes(t *testing.T) {
	ov := &stubOverride{rate: 0.0}
	g := newTestGovernor(t, ov, &stubEval{passed: true})

	adhoc := NewDomainKey("tenantA", "unique_role_only_once")

	// UMA única janela sã (objectivo ad-hoc, domínio não recorre).
	out, err := g.ObserveWindow(context.Background(), adhoc, healthyCounters())
	if err != nil {
		t.Fatalf("ObserveWindow: %v", err)
	}
	if out.Promoted {
		t.Fatalf("uma janela sã NÃO devia promover (ad-hoc); out=%+v", out)
	}
	if lvl := g.Level(adhoc); lvl != L0 {
		t.Fatalf("domínio ad-hoc devia ficar em L0, ficou %s", lvl)
	}

	// Domínio RECORRENTE: MinRecurrence(=2) janelas sãs sustentadas ⇒ promove a L1.
	rec := NewDomainKey("tenantA", "billing", "reconciliation")
	if _, err := g.ObserveWindow(context.Background(), rec, healthyCounters()); err != nil {
		t.Fatalf("ObserveWindow: %v", err)
	}
	if g.Level(rec) != L0 {
		t.Fatalf("após 1 janela o recorrente ainda devia estar em L0")
	}
	out2, err := g.ObserveWindow(context.Background(), rec, healthyCounters())
	if err != nil {
		t.Fatalf("ObserveWindow: %v", err)
	}
	if !out2.Promoted || g.Level(rec) != L1 {
		t.Fatalf("2 janelas sãs sustentadas deviam promover a L1; out=%+v level=%s", out2, g.Level(rec))
	}
}

// ---------------------------------------------------------------------------
// TESTE REQUERIDO: envelope L4/L5 usa RISCO DERIVADO — um nó danger DERIVADO força
// revisão apesar de rótulo safe e nível L5.
// ---------------------------------------------------------------------------

func TestAuthorizeAutoApproval_DerivedDangerForcesReviewEvenAtL5(t *testing.T) {
	ov := &stubOverride{rate: 0.0}
	ev := &stubEval{passed: true}
	g := newTestGovernor(t, ov, ev)

	dom := NewDomainKey("tenantA", "role_x")
	driveToLevel(t, g, dom, L5)
	if g.Level(dom) != L5 {
		t.Fatalf("pré-condição: domínio devia estar a L5, está %s", g.Level(dom))
	}

	// Risco DERIVADO = danger, mas o LLM rotulou safe. A L5, TEM de forçar revisão.
	d, err := g.AuthorizeAutoApproval(context.Background(), AutoApproveInput{
		PlanID:   "p1",
		Domain:   dom,
		Derived:  plan.RiskDanger,
		Declared: plan.RiskSafe,
	})
	if err != nil {
		t.Fatalf("AuthorizeAutoApproval: %v", err)
	}
	if d.AutoApproved || !d.RequireHuman {
		t.Fatalf("danger DERIVADO devia forçar revisão mesmo a L5; d=%+v", d)
	}
	if d.Reason != ReasonDangerForcesReview {
		t.Fatalf("razão = %q, esperado %q", d.Reason, ReasonDangerForcesReview)
	}

	// Contraste falsificável: MESMO nível, risco derivado safe ⇒ auto-aprova. Se a
	// decisão lesse o rótulo do LLM (safe) no caso danger acima, este par não
	// distinguiria — é a leitura do DERIVADO que separa os dois desfechos.
	d2, err := g.AuthorizeAutoApproval(context.Background(), AutoApproveInput{
		PlanID:   "p2",
		Domain:   dom,
		Derived:  plan.RiskSafe,
		Declared: plan.RiskSafe,
	})
	if err != nil {
		t.Fatalf("AuthorizeAutoApproval: %v", err)
	}
	if !d2.AutoApproved || d2.RequireHuman {
		t.Fatalf("risco derivado safe a L5 devia auto-aprovar; d2=%+v", d2)
	}
}

// O advisory do LLM só ELEVA: derived safe + declared danger ⇒ revisão (nunca baixa,
// mas honra a cautela do modelo para cima).
func TestAuthorizeAutoApproval_AdvisoryElevatesOnly(t *testing.T) {
	g := newTestGovernor(t, &stubOverride{}, &stubEval{passed: true})
	dom := NewDomainKey("tenantA", "role_y")
	driveToLevel(t, g, dom, L5)

	d, _ := g.AuthorizeAutoApproval(context.Background(), AutoApproveInput{
		Domain: dom, Derived: plan.RiskSafe, Declared: plan.RiskDanger,
	})
	if !d.RequireHuman || d.AutoApproved {
		t.Fatalf("declared danger devia elevar para revisão; d=%+v", d)
	}
}

// capability_gap força SEMPRE revisão, mesmo a L5 com risco derivado safe.
func TestAuthorizeAutoApproval_CapabilityGapForcesReviewEvenAtL5(t *testing.T) {
	g := newTestGovernor(t, &stubOverride{}, &stubEval{passed: true})
	dom := NewDomainKey("tenantA", "role_z")
	driveToLevel(t, g, dom, L5)

	d, err := g.AuthorizeAutoApproval(context.Background(), AutoApproveInput{
		Domain: dom, Derived: plan.RiskSafe, Declared: plan.RiskSafe, HasCapabilityGap: true,
	})
	if err != nil {
		t.Fatalf("AuthorizeAutoApproval: %v", err)
	}
	if d.AutoApproved || !d.RequireHuman || d.Reason != ReasonCapabilityGapForcesReview {
		t.Fatalf("capability_gap devia forçar revisão a L5; d=%+v", d)
	}
}

// Risco derivado GRAY e UNSET forçam revisão mesmo a L5 — só o piso SAFE dispensa
// humano. Fecha o contrato de [plan.RiskGray] ("revisão item-a-item no gate"): um
// plano gray/unset nunca auto-aprova sem humano.
//
// Falha-antes: com a regra antiga (revisão SÓ quando elevate(...)==RiskDanger),
// gray e unset caíam no ramo de auto-aprovação e este teste veria AutoApproved=true.
// A condição actual (só RiskSafe prossegue; qualquer outro ⇒ ReasonNonSafeForcesReview)
// é o que separa os dois desfechos.
func TestAuthorizeAutoApproval_GrayAndUnsetForceReviewEvenAtL5(t *testing.T) {
	ev := &stubEval{passed: true}
	g := newTestGovernor(t, &stubOverride{}, ev)
	dom := NewDomainKey("tenantA", "role_gray")
	driveToLevel(t, g, dom, L5)
	if g.Level(dom) != L5 {
		t.Fatalf("pré-condição: domínio devia estar a L5, está %s", g.Level(dom))
	}

	for _, derived := range []plan.RiskClass{plan.RiskGray, plan.RiskUnset} {
		// Declared=safe: o advisory só ELEVA, logo não altera o piso derivado — é o
		// próprio DERIVADO gray/unset que tem de forçar revisão.
		d, err := g.AuthorizeAutoApproval(context.Background(), AutoApproveInput{
			PlanID: "p", Domain: dom, Derived: derived, Declared: plan.RiskSafe,
		})
		if err != nil {
			t.Fatalf("AuthorizeAutoApproval(%v): %v", derived, err)
		}
		if d.AutoApproved || !d.RequireHuman {
			t.Fatalf("risco derivado %v devia forçar revisão mesmo a L5; d=%+v", derived, d)
		}
		if d.Reason != ReasonNonSafeForcesReview {
			t.Fatalf("risco %v: razão = %q, esperado %q", derived, d.Reason, ReasonNonSafeForcesReview)
		}
	}

	// O eval-gate NÃO deve ser alcançado: gray/unset barram ANTES da pré-condição de
	// runtime (a revisão é forçada na etapa 1, a montante do travão da etapa 4).
	if ev.callCount() != 0 {
		t.Fatalf("gray/unset deviam barrar antes do eval-gate; calls=%d", ev.callCount())
	}
}

// ---------------------------------------------------------------------------
// TESTE REQUERIDO: SLI de fracção visível (exposto como métrica/sinal).
// ---------------------------------------------------------------------------

func TestGovernor_PlanningFractionSLIVisible(t *testing.T) {
	g := newTestGovernor(t, &stubOverride{}, &stubEval{passed: true})
	dom := NewDomainKey("tenantA", "role_sli")

	// 3 unidades de planeamento em 100 ⇒ 3% (dentro do SLI de 5%).
	c := healthyCounters()
	c.PlanningUnits = 3
	c.ExecutionUnits = 97
	if _, err := g.ObserveWindow(context.Background(), dom, c); err != nil {
		t.Fatalf("ObserveWindow: %v", err)
	}

	frac, within, ok := g.PlanningFractionSLI(dom)
	if !ok {
		t.Fatalf("SLI devia estar visível após observação")
	}
	if frac != 0.03 || !within {
		t.Fatalf("SLI = %v (within=%v), esperado 0.03 dentro do tecto", frac, within)
	}

	// E o sinal completo também é visível.
	sig, ok := g.LastSignals(dom)
	if !ok || sig.PlanningFraction != 0.03 {
		t.Fatalf("LastSignals.PlanningFraction = %v (ok=%v), esperado 0.03", sig.PlanningFraction, ok)
	}
}

// ---------------------------------------------------------------------------
// TESTE REQUERIDO: anomalia num sinal DEMOVE.
// ---------------------------------------------------------------------------

func TestObserveWindow_AnomalyDemotes(t *testing.T) {
	ov := &stubOverride{rate: 0.0}
	g := newTestGovernor(t, ov, &stubEval{passed: true})
	dom := NewDomainKey("tenantA", "role_demote")

	driveToLevel(t, g, dom, L3)
	if g.Level(dom) != L3 {
		t.Fatalf("pré-condição: devia estar a L3, está %s", g.Level(dom))
	}

	// Janela anómala: taxa de propostas inválidas muito acima do tecto.
	bad := healthyCounters()
	bad.InvalidProposals = 50 // 50% >> 5%
	out, err := g.ObserveWindow(context.Background(), dom, bad)
	if err != nil {
		t.Fatalf("ObserveWindow: %v", err)
	}
	if !out.Demoted || out.Level != L0 {
		t.Fatalf("anomalia devia demover a L0; out=%+v", out)
	}
	if len(out.Breaches) == 0 || out.Breaches[0].Signal != SignalInvalid {
		t.Fatalf("brecha esperada em %q; brechas=%+v", SignalInvalid, out.Breaches)
	}
	if g.Level(dom) != L0 {
		t.Fatalf("nível após anomalia = %s, esperado L0", g.Level(dom))
	}
}

// ---------------------------------------------------------------------------
// DoD: promoção NÃO-GAMEÁVEL — contadores próprios perfeitos não promovem se a
// fonte de override AUTORITATIVA (AOS-095) reportar overrides a mais.
// ---------------------------------------------------------------------------

func TestObserveWindow_OverridePortVetoesPromotion(t *testing.T) {
	ov := &stubOverride{rate: 0.50} // 50% de override — muito acima do tecto de 5%
	g := newTestGovernor(t, ov, &stubEval{passed: true})
	dom := NewDomainKey("tenantA", "role_gaming")

	// Contadores auto-reportados PERFEITOS, repetidos várias janelas.
	for i := 0; i < 5; i++ {
		out, err := g.ObserveWindow(context.Background(), dom, healthyCounters())
		if err != nil {
			t.Fatalf("ObserveWindow: %v", err)
		}
		if out.Promoted {
			t.Fatalf("override autoritativo alto devia VETAR a promoção; out=%+v", out)
		}
	}
	if g.Level(dom) != L0 {
		t.Fatalf("com override alto o domínio devia ficar em L0, ficou %s", g.Level(dom))
	}
}

// ---------------------------------------------------------------------------
// Travão de runtime: o eval-gate de decomposição é PRÉ-CONDIÇÃO e corre mesmo a L5.
// ---------------------------------------------------------------------------

func TestAuthorizeAutoApproval_EvalGateBrakeIndependentOfHuman(t *testing.T) {
	ev := &stubEval{passed: false} // eval-gate reprova
	g := newTestGovernor(t, &stubOverride{}, ev)
	dom := NewDomainKey("tenantA", "role_brake")
	driveToLevel(t, g, dom, L5)

	d, err := g.AuthorizeAutoApproval(context.Background(), AutoApproveInput{
		Domain: dom, Derived: plan.RiskSafe, Declared: plan.RiskSafe,
	})
	if err != nil {
		t.Fatalf("AuthorizeAutoApproval: %v", err)
	}
	if d.AutoApproved || !d.RequireHuman || d.Reason != ReasonEvalGateBrake {
		t.Fatalf("eval-gate reprovado devia travar a auto-aprovação a L5; d=%+v", d)
	}
	if ev.callCount() == 0 {
		t.Fatalf("o eval-gate devia ter sido CHAMADO como pré-condição (mesmo a L5)")
	}
}

// Amostragem post-hoc marcada mesmo a L4/L5 (SampleEveryN=1 ⇒ toda auto-aprovação).
func TestAuthorizeAutoApproval_PostHocSamplingEvenAtHighLevel(t *testing.T) {
	ev := &stubEval{passed: true}
	g := newTestGovernor(t, &stubOverride{}, ev)
	dom := NewDomainKey("tenantA", "role_sample")
	driveToLevel(t, g, dom, L5)

	d, err := g.AuthorizeAutoApproval(context.Background(), AutoApproveInput{
		Domain: dom, Derived: plan.RiskSafe, Declared: plan.RiskSafe,
	})
	if err != nil {
		t.Fatalf("AuthorizeAutoApproval: %v", err)
	}
	if !d.AutoApproved || !d.EvalSampled {
		t.Fatalf("auto-aprovação a L5 devia ser amostrada post-hoc; d=%+v", d)
	}
}

// Fora do envelope de autonomia (nível < AutoApproveFrom) ⇒ revisão humana, e o
// eval-gate NÃO é chamado (só é pré-condição do caminho de auto-aprovação).
func TestAuthorizeAutoApproval_BelowEnvelopeRequiresHuman(t *testing.T) {
	ev := &stubEval{passed: true}
	g := newTestGovernor(t, &stubOverride{}, ev)
	dom := NewDomainKey("tenantA", "role_low") // nunca promovido ⇒ L0

	d, err := g.AuthorizeAutoApproval(context.Background(), AutoApproveInput{
		Domain: dom, Derived: plan.RiskSafe, Declared: plan.RiskSafe,
	})
	if err != nil {
		t.Fatalf("AuthorizeAutoApproval: %v", err)
	}
	if d.AutoApproved || !d.RequireHuman || d.Reason != ReasonBelowAutoApprove {
		t.Fatalf("L0 devia exigir revisão humana; d=%+v", d)
	}
	if ev.callCount() != 0 {
		t.Fatalf("eval-gate não devia ser chamado abaixo do envelope; calls=%d", ev.callCount())
	}
}

// ---------------------------------------------------------------------------
// Fail-closed de construção e de fonte.
// ---------------------------------------------------------------------------

func TestNewGovernor_FailClosed(t *testing.T) {
	cfg := testConfig()
	if _, err := NewGovernor(cfg, nil, &stubEval{}); !errors.Is(err, ErrNilPort) {
		t.Fatalf("porta override nil devia dar ErrNilPort, deu %v", err)
	}
	if _, err := NewGovernor(cfg, &stubOverride{}, nil); !errors.Is(err, ErrNilPort) {
		t.Fatalf("porta eval nil devia dar ErrNilPort, deu %v", err)
	}
	bad := cfg
	bad.MinRecurrence = 0
	if _, err := NewGovernor(bad, &stubOverride{}, &stubEval{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("MinRecurrence=0 devia dar ErrInvalidConfig, deu %v", err)
	}
	bad2 := cfg
	bad2.Envelope.MaxPlanningFraction = 0
	if _, err := NewGovernor(bad2, &stubOverride{}, &stubEval{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("SLI=0 devia dar ErrInvalidConfig, deu %v", err)
	}
}

// Um erro da fonte de override propaga-se e a janela não altera o nível (fail-closed).
func TestObserveWindow_OverrideSourceErrorFailsClosed(t *testing.T) {
	ov := &stubOverride{err: errors.New("AOS-095 indisponível")}
	g := newTestGovernor(t, ov, &stubEval{passed: true})
	dom := NewDomainKey("tenantA", "role_err")

	if _, err := g.ObserveWindow(context.Background(), dom, healthyCounters()); err == nil {
		t.Fatalf("erro da fonte de override devia propagar-se")
	}
	if g.Level(dom) != L0 {
		t.Fatalf("nível não devia mudar em erro de fonte; ficou %s", g.Level(dom))
	}
}

// Insuficiência de amostra (Plans < MinSample) não julga: nem promove nem demove.
func TestObserveWindow_InsufficientSampleNoJudgement(t *testing.T) {
	g := newTestGovernor(t, &stubOverride{}, &stubEval{passed: true})
	dom := NewDomainKey("tenantA", "role_small")
	driveToLevel(t, g, dom, L2)

	small := healthyCounters()
	small.Plans = 0 // abaixo de MinSample(=1)
	// Ainda que os contadores fossem anómalos, sem amostra não há juízo.
	small.InvalidProposals = 99
	out, err := g.ObserveWindow(context.Background(), dom, small)
	if err != nil {
		t.Fatalf("ObserveWindow: %v", err)
	}
	if out.Evaluated || out.Demoted || out.Promoted {
		t.Fatalf("amostra insuficiente não devia julgar; out=%+v", out)
	}
	if g.Level(dom) != L2 {
		t.Fatalf("nível devia manter-se em L2, ficou %s", g.Level(dom))
	}
}

// Concorrência: observações e decisões em paralelo não fazem corrida (-race).
func TestGovernor_ConcurrentSafe(t *testing.T) {
	g := newTestGovernor(t, &stubOverride{}, &stubEval{passed: true})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			dom := NewDomainKey("tenantA", "role_conc")
			for j := 0; j < 20; j++ {
				_, _ = g.ObserveWindow(context.Background(), dom, healthyCounters())
				_, _ = g.AuthorizeAutoApproval(context.Background(), AutoApproveInput{
					Domain: dom, Derived: plan.RiskSafe, Declared: plan.RiskSafe,
				})
				_ = g.Level(dom)
				_, _, _ = g.PlanningFractionSLI(dom)
			}
		}(i)
	}
	wg.Wait()
}
