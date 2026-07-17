package risk_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// spyChannel é uma [risk.ConfirmationChannel] de teste, determinista: devolve uma
// resposta fixa e conta/regista os pedidos (para provar quantas confirmações
// houve e que o preview é concreto).
type spyChannel struct {
	mu       sync.Mutex
	approve  bool
	err      error
	block    bool // se true, bloqueia até o ctx expirar (simula HITL sem resposta)
	requests []risk.ConfirmationRequest
}

func (c *spyChannel) Confirm(ctx context.Context, req risk.ConfirmationRequest) (risk.ConfirmationResponse, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	block, approve, err := c.block, c.approve, c.err
	c.mu.Unlock()
	if block {
		<-ctx.Done() // espera o timeout: prova o fail-closed por ausência de resposta
		return risk.ConfirmationResponse{}, ctx.Err()
	}
	if err != nil {
		return risk.ConfirmationResponse{}, err
	}
	return risk.ConfirmationResponse{Approved: approve, Approver: "human:tester"}, nil
}

func (c *spyChannel) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

func (c *spyChannel) last() risk.ConfirmationRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests[len(c.requests)-1]
}

func classified(class risk.Class, rev risk.Reversibility) risk.Classification {
	return risk.Classification{Class: class, Reversibility: rev, PolicyVersion: "test#deadbeef0000"}
}

// --- (1) SAFE corre sem gate (sem fricção, sem HITL) ------------------------

func TestGate_Safe_CorreSemGate(t *testing.T) {
	t.Parallel()
	ch := &spyChannel{approve: false} // mesmo negando, safe não consulta o canal
	g, err := risk.NewGate(ch)
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	res := g.Evaluate(context.Background(), risk.Request{Classification: classified(risk.ClassSafe, risk.Reversible)})
	if res.Outcome != risk.OutcomeAllow {
		t.Fatalf("safe: outcome = %v, quer Allow", res.Outcome)
	}
	if ch.count() != 0 {
		t.Errorf("safe consultou o canal %d vezes, não devia consultar nenhuma", ch.count())
	}
	if p, _, _, _, _, _ := g.Metrics().Snapshot(); p != 0 {
		t.Errorf("safe contabilizou %d prompts, não devia contar nenhum", p)
	}
}

// --- (2) GRAY agrupa: uma confirmação para o LOTE, não uma por acção --------

func TestGate_Gray_AgrupaEmLote(t *testing.T) {
	t.Parallel()
	ch := &spyChannel{approve: true}
	g, err := risk.NewGate(ch, risk.WithMaturity(risk.MaturityNovice)) // novice não auto-aprova
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	// Três acções gray no MESMO lote (mesma BatchKey).
	for i := 0; i < 3; i++ {
		res := g.Evaluate(context.Background(), risk.Request{
			Classification: classified(risk.ClassGray, risk.Reversible),
			BatchKey:       "run-42",
		})
		if res.Outcome != risk.OutcomeAllow {
			t.Fatalf("gray[%d]: outcome = %v, quer Allow", i, res.Outcome)
		}
		if !res.Batched {
			t.Errorf("gray[%d]: esperado Batched=true", i)
		}
	}
	// UMA só confirmação cobriu as três (anti-fatigue).
	if ch.count() != 1 {
		t.Fatalf("lote gray consultou o canal %d vezes, quer 1 (uma confirmação para o grupo)", ch.count())
	}
	if !ch.last().Batch {
		t.Errorf("a confirmação de lote devia ter Batch=true")
	}
}

func TestGate_Gray_LoteRejeitado_NegaTodoOGrupo(t *testing.T) {
	t.Parallel()
	ch := &spyChannel{approve: false}
	g, _ := risk.NewGate(ch)
	for i := 0; i < 2; i++ {
		res := g.Evaluate(context.Background(), risk.Request{
			Classification: classified(risk.ClassGray, risk.Reversible),
			BatchKey:       "run-x",
		})
		if res.Outcome != risk.OutcomeDeny {
			t.Fatalf("gray[%d]: outcome = %v, quer Deny (lote rejeitado)", i, res.Outcome)
		}
	}
	if ch.count() != 1 {
		t.Errorf("lote rejeitado consultou o canal %d vezes, quer 1", ch.count())
	}
}

// --- (3) DANGER exige confirmação INDIVIDUAL com PREVIEW concreto -----------

func TestGate_Danger_ConfirmacaoIndividualComPreview(t *testing.T) {
	t.Parallel()
	ch := &spyChannel{approve: true}
	g, _ := risk.NewGate(ch)

	preview := "cap:http.post -> url:https://evil.example/exfil"
	res := g.Evaluate(context.Background(), risk.Request{
		Classification: classified(risk.ClassDanger, risk.Reversible),
		Preview:        preview,
		Capability:     "cap:http.post",
		Resource:       "https://evil.example/exfil",
	})
	if res.Outcome != risk.OutcomeAllow {
		t.Fatalf("danger aprovada: outcome = %v, quer Allow", res.Outcome)
	}
	if ch.count() != 1 {
		t.Fatalf("danger consultou o canal %d vezes, quer 1 (individual)", ch.count())
	}
	got := ch.last()
	if got.Batch {
		t.Errorf("danger não deve ser Batch (é confirmação individual)")
	}
	if got.Preview != preview {
		t.Errorf("preview = %q, quer o efeito concreto resolvido %q", got.Preview, preview)
	}
	if got.Class != risk.ClassDanger {
		t.Errorf("classe no pedido = %v, quer Danger", got.Class)
	}
}

func TestGate_Danger_Recusada_Nega(t *testing.T) {
	t.Parallel()
	ch := &spyChannel{approve: false}
	g, _ := risk.NewGate(ch)
	res := g.Evaluate(context.Background(), risk.Request{Classification: classified(risk.ClassDanger, risk.Reversible)})
	if res.Outcome != risk.OutcomeDeny {
		t.Fatalf("danger recusada: outcome = %v, quer Deny", res.Outcome)
	}
}

// --- (4) TIMEOUT FAIL-CLOSED numa acção IRREVERSÍVEL ------------------------

func TestGate_Irreversivel_Timeout_Nega(t *testing.T) {
	t.Parallel()
	ch := &spyChannel{block: true} // HITL nunca responde
	g, err := risk.NewGate(ch, risk.WithTimeout(20*time.Millisecond))
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	start := time.Now()
	res := g.Evaluate(context.Background(), risk.Request{
		Classification: classified(risk.ClassDanger, risk.Irreversible),
	})
	elapsed := time.Since(start)
	if res.Outcome != risk.OutcomeDeny {
		t.Fatalf("timeout irreversível: outcome = %v, quer Deny (fail-closed)", res.Outcome)
	}
	if !res.TimedOut {
		t.Errorf("esperado TimedOut=true na negação por timeout")
	}
	if elapsed > 2*time.Second {
		t.Errorf("timeout demorou %v, esperado ~20ms (não deve bloquear indefinidamente)", elapsed)
	}
	if _, _, _, _, timeouts, _ := g.Metrics().Snapshot(); timeouts != 1 {
		t.Errorf("métrica Timeouts = %d, quer 1", timeouts)
	}
}

// Timeout DETERMINISTA via relógio injectável: um relógio no passado faz a
// deadline expirar de imediato, sem sleeps reais.
func TestGate_Irreversivel_Timeout_RelogioInjectavel(t *testing.T) {
	t.Parallel()
	past := time.Unix(0, 0)
	ch := &spyChannel{block: true}
	g, err := risk.NewGate(ch,
		risk.WithTimeout(time.Millisecond),
		risk.WithClock(func() time.Time { return past }), // deadline = 1970 + 1ms ⇒ já passou
	)
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}
	res := g.Evaluate(context.Background(), risk.Request{Classification: classified(risk.ClassDanger, risk.Irreversible)})
	if res.Outcome != risk.OutcomeDeny || !res.TimedOut {
		t.Fatalf("relógio no passado: outcome=%v timedOut=%v, quer Deny+TimedOut", res.Outcome, res.TimedOut)
	}
}

// Prova adicional do fail-closed: um canal que ERRA numa acção irreversível NEGA.
func TestGate_Irreversivel_ErroCanal_Nega(t *testing.T) {
	t.Parallel()
	ch := &spyChannel{err: errors.New("canal indisponivel")}
	g, _ := risk.NewGate(ch, risk.WithTimeout(time.Second))
	res := g.Evaluate(context.Background(), risk.Request{Classification: classified(risk.ClassDanger, risk.Irreversible)})
	if res.Outcome != risk.OutcomeDeny {
		t.Fatalf("erro do canal: outcome = %v, quer Deny (fail-closed)", res.Outcome)
	}
}

// --- (5) AUTO-APPROVE por classe/maturidade; danger NUNCA (prova estrutural) --

func TestAutoApprove_SafeGrayPorMaturidade_DangerNunca(t *testing.T) {
	t.Parallel()
	p := risk.DefaultAutoApprove() // GrayFrom = Trusted
	cases := []struct {
		class    risk.Class
		maturity risk.Maturity
		want     bool
	}{
		{risk.ClassSafe, risk.MaturityNovice, true}, // safe sempre auto-aprovável
		{risk.ClassGray, risk.MaturityNovice, false},
		{risk.ClassGray, risk.MaturityExperienced, false},
		{risk.ClassGray, risk.MaturityTrusted, true}, // gray a partir de trusted
		// DANGER: NUNCA, em nenhuma maturidade (prova estrutural do anti-bypass).
		{risk.ClassDanger, risk.MaturityNovice, false},
		{risk.ClassDanger, risk.MaturityExperienced, false},
		{risk.ClassDanger, risk.MaturityTrusted, false},
	}
	for _, tc := range cases {
		if got := p.Allows(tc.class, tc.maturity); got != tc.want {
			t.Errorf("Allows(%v, %v) = %v, quer %v", tc.class, tc.maturity, got, tc.want)
		}
	}
	// Prova reforçada: mesmo uma política que TENTE ser permissiva (GrayFrom=novice)
	// nunca auto-aprova danger.
	permissive := risk.AutoApprovePolicy{GrayFrom: risk.MaturityNovice}
	for m := risk.MaturityNovice; m <= risk.MaturityTrusted; m++ {
		if permissive.Allows(risk.ClassDanger, m) {
			t.Fatalf("danger foi auto-aprovada com maturidade %v — bypass estrutural!", m)
		}
	}
}

// Fluxo: gray auto-aprovada por maturidade NÃO consulta o canal (nem conta override).
func TestGate_Gray_AutoAprovada_SemHITL(t *testing.T) {
	t.Parallel()
	ch := &spyChannel{approve: false} // canal negaria, mas nem é consultado
	g, _ := risk.NewGate(ch, risk.WithMaturity(risk.MaturityTrusted))
	res := g.Evaluate(context.Background(), risk.Request{
		Classification: classified(risk.ClassGray, risk.Reversible),
		BatchKey:       "run-auto",
	})
	if res.Outcome != risk.OutcomeAllow || !res.AutoApproved {
		t.Fatalf("gray madura: outcome = %v, autoApproved = %v; quer Allow+auto", res.Outcome, res.AutoApproved)
	}
	if ch.count() != 0 {
		t.Errorf("auto-aprovação consultou o canal %d vezes, não devia", ch.count())
	}
	prompted, _, auto, _, _, rate := g.Metrics().Snapshot()
	if prompted != 0 {
		t.Errorf("auto-aprovação contou %d prompts (não é override, não conta)", prompted)
	}
	if auto != 1 {
		t.Errorf("AutoApproved = %d, quer 1", auto)
	}
	if rate != 0 {
		t.Errorf("override-rate = %v, quer 0 (sem prompts)", rate)
	}
}

// Danger nunca é auto-aprovada mesmo com a maturidade máxima: consulta SEMPRE o canal.
func TestGate_Danger_MaturidadeMaxima_ContinuaAConfirmar(t *testing.T) {
	t.Parallel()
	ch := &spyChannel{approve: false}
	g, _ := risk.NewGate(ch,
		risk.WithMaturity(risk.MaturityTrusted),
		risk.WithAutoApprove(risk.AutoApprovePolicy{GrayFrom: risk.MaturityNovice}),
	)
	res := g.Evaluate(context.Background(), risk.Request{Classification: classified(risk.ClassDanger, risk.Reversible)})
	if res.Outcome != risk.OutcomeDeny {
		t.Fatalf("danger com maturidade máxima: outcome = %v, quer Deny (recusada, não auto-aprovada)", res.Outcome)
	}
	if ch.count() != 1 {
		t.Errorf("danger deve confirmar sempre; canal consultado %d vezes, quer 1", ch.count())
	}
}

// --- (6) OVERRIDE-RATE contabilizado e exposto (anti rubber-stamping) -------

func TestGate_OverrideRate_ContabilizadoEExposto(t *testing.T) {
	t.Parallel()
	ch := &spyChannel{approve: true}
	g, _ := risk.NewGate(ch)

	// Quatro acções danger, todas aprovadas pelo utilizador (rubber-stamping).
	for i := 0; i < 4; i++ {
		g.Evaluate(context.Background(), risk.Request{Classification: classified(risk.ClassDanger, risk.Reversible)})
	}
	prompted, overrides, _, _, _, rate := g.Metrics().Snapshot()
	if prompted != 4 || overrides != 4 {
		t.Fatalf("prompted=%d overrides=%d, quer 4/4", prompted, overrides)
	}
	if rate != 1.0 {
		t.Errorf("override-rate = %v, quer 1.0 (100%% aprovado = rubber-stamping)", rate)
	}
	if g.Metrics().OverrideRate() != 1.0 {
		t.Errorf("OverrideRate() = %v, quer 1.0", g.Metrics().OverrideRate())
	}
}

func TestGate_OverrideRate_Parcial(t *testing.T) {
	t.Parallel()
	// Um canal que aprova só as ímpares para dar uma taxa parcial.
	approvals := []bool{true, false, true, false}
	idx := 0
	var mu sync.Mutex
	ch := &togglingChannel{fn: func() bool {
		mu.Lock()
		defer mu.Unlock()
		v := approvals[idx%len(approvals)]
		idx++
		return v
	}}
	g, _ := risk.NewGate(ch)
	for i := 0; i < 4; i++ {
		g.Evaluate(context.Background(), risk.Request{Classification: classified(risk.ClassDanger, risk.Reversible)})
	}
	prompted, overrides, _, _, _, rate := g.Metrics().Snapshot()
	if prompted != 4 || overrides != 2 {
		t.Fatalf("prompted=%d overrides=%d, quer 4/2", prompted, overrides)
	}
	if rate != 0.5 {
		t.Errorf("override-rate = %v, quer 0.5", rate)
	}
}

// --- SAROC-05: modo de decisão atribuível ------------------------------------

func TestGateResult_DecisionMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		res  risk.GateResult
		want string
	}{
		{"auto", risk.GateResult{Outcome: risk.OutcomeAllow, AutoApproved: true}, "auto"},
		{"timeout", risk.GateResult{Outcome: risk.OutcomeDeny, TimedOut: true}, "timeout"},
		{"batch", risk.GateResult{Outcome: risk.OutcomeAllow, Batched: true}, "batch"},
		{"human", risk.GateResult{Outcome: risk.OutcomeAllow}, "human"},
		{"denied", risk.GateResult{Outcome: risk.OutcomeDeny}, "denied"},
	}
	for _, tc := range cases {
		if got := tc.res.DecisionMode(); got != tc.want {
			t.Errorf("%s: DecisionMode() = %q, quer %q", tc.name, got, tc.want)
		}
	}
}

// --- SAROC-06: amplificação de lote medida (BatchCovered) -------------------

func TestGate_BatchCovered_MedeAmplificacao(t *testing.T) {
	t.Parallel()
	ch := &spyChannel{approve: true}
	g, _ := risk.NewGate(ch, risk.WithMaturity(risk.MaturityNovice))
	// 3 acções gray no MESMO lote: 1 prompt cobre as 3.
	for i := 0; i < 3; i++ {
		g.Evaluate(context.Background(), risk.Request{
			Classification: classified(risk.ClassGray, risk.Reversible),
			BatchKey:       "run-amp",
		})
	}
	prompted, overrides, _, _, _, _ := g.Metrics().Snapshot()
	covered := g.Metrics().BatchCovered.Load()
	if prompted != 1 || overrides != 1 {
		t.Fatalf("prompted=%d overrides=%d, quer 1/1 (uma aprovação)", prompted, overrides)
	}
	if covered != 2 {
		t.Errorf("BatchCovered = %d, quer 2 (N-1 acções cobertas por 1 aprovação)", covered)
	}
}

// --- SAROC-07: timeout de guarda para TODA a classe danger ------------------

// Uma danger REVERSÍVEL também tem timeout de guarda (não só a irreversível): um HITL
// que nunca responde NEGA fail-closed, não pende indefinidamente.
func TestGate_DangerReversivel_TimeoutDeGuarda_Nega(t *testing.T) {
	t.Parallel()
	ch := &spyChannel{block: true}
	g, _ := risk.NewGate(ch, risk.WithTimeout(20*time.Millisecond))
	start := time.Now()
	res := g.Evaluate(context.Background(), risk.Request{
		Classification: classified(risk.ClassDanger, risk.Reversible),
	})
	if res.Outcome != risk.OutcomeDeny || !res.TimedOut {
		t.Fatalf("danger reversível bloqueada: outcome=%v timedOut=%v, quer Deny+TimedOut (guarda)", res.Outcome, res.TimedOut)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("guarda não disparou: demorou demasiado")
	}
}

// Numa acção IRREVERSÍVEL, WithTimeout(<=0) NÃO desactiva o bound: o piso
// [DefaultTimeout] aplica-se na mesma (relógio injectado no passado ⇒ expira já).
func TestGate_Irreversivel_TimeoutDesactivado_AindaTemPiso(t *testing.T) {
	t.Parallel()
	past := time.Unix(0, 0)
	ch := &spyChannel{block: true}
	g, _ := risk.NewGate(ch,
		risk.WithTimeout(0), // "desactivado" — mas irreversível tem piso não-desactivável
		risk.WithClock(func() time.Time { return past }),
	)
	res := g.Evaluate(context.Background(), risk.Request{
		Classification: classified(risk.ClassDanger, risk.Irreversible),
	})
	if res.Outcome != risk.OutcomeDeny || !res.TimedOut {
		t.Fatalf("irreversível com timeout<=0: outcome=%v timedOut=%v, quer Deny+TimedOut (piso não-desactivável)", res.Outcome, res.TimedOut)
	}
}

type togglingChannel struct{ fn func() bool }

func (c *togglingChannel) Confirm(_ context.Context, _ risk.ConfirmationRequest) (risk.ConfirmationResponse, error) {
	return risk.ConfirmationResponse{Approved: c.fn()}, nil
}

// --- Construção fail-closed -------------------------------------------------

func TestNewGate_CanalNil_Erro(t *testing.T) {
	t.Parallel()
	if _, err := risk.NewGate(nil); !errors.Is(err, risk.ErrNoChannel) {
		t.Fatalf("NewGate(nil) err = %v, quer ErrNoChannel", err)
	}
}

// DenyChannel de referência nega tudo (fail-closed por omissão).
func TestDenyChannel_NegaTudo(t *testing.T) {
	t.Parallel()
	g, _ := risk.NewGate(risk.DenyChannel{})
	res := g.Evaluate(context.Background(), risk.Request{Classification: classified(risk.ClassDanger, risk.Reversible)})
	if res.Outcome != risk.OutcomeDeny {
		t.Fatalf("DenyChannel: outcome = %v, quer Deny", res.Outcome)
	}
}

// Um canal que entra em PANIC é tratado como fail-closed (deny), não propaga.
func TestGate_CanalPanic_FailClosed(t *testing.T) {
	t.Parallel()
	g, _ := risk.NewGate(panicChannel{})
	res := g.Evaluate(context.Background(), risk.Request{Classification: classified(risk.ClassDanger, risk.Reversible)})
	if res.Outcome != risk.OutcomeDeny {
		t.Fatalf("canal em panic: outcome = %v, quer Deny (fail-closed)", res.Outcome)
	}
}

type panicChannel struct{}

func (panicChannel) Confirm(context.Context, risk.ConfirmationRequest) (risk.ConfirmationResponse, error) {
	panic("canal rebentou")
}

// Formas textuais canónicas (seladas no audit) — inclui os fail-closed.
func TestStrings_Canonicas(t *testing.T) {
	t.Parallel()
	if risk.ClassSafe.String() != "safe" || risk.ClassGray.String() != "gray" || risk.ClassDanger.String() != "danger" {
		t.Errorf("Class.String inesperado")
	}
	if risk.SensitivityPublic.String() != "public" || risk.SensitivityInternal.String() != "internal" || risk.SensitivitySensitive.String() != "sensitive" {
		t.Errorf("Sensitivity.String inesperado")
	}
	if risk.SensitivityUnknown.String() != "sensitive" { // fail-closed
		t.Errorf("Sensitivity desconhecida devia serializar 'sensitive'")
	}
	if risk.EgressNone.String() != "none" || risk.EgressInternal.String() != "internal" || risk.EgressExternal.String() != "external" {
		t.Errorf("Egress.String inesperado")
	}
	if risk.EgressUnknown.String() != "external" { // fail-closed
		t.Errorf("Egress desconhecido devia serializar 'external'")
	}
	if risk.Reversible.String() != "reversible" || risk.Irreversible.String() != "irreversible" {
		t.Errorf("Reversibility.String inesperado")
	}
	if risk.ReversibilityUnknown.String() != "irreversible" { // fail-closed
		t.Errorf("Reversibility desconhecida devia serializar 'irreversible'")
	}
}
