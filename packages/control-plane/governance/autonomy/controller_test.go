package autonomy

import (
	"context"
	"strings"
	"testing"
	"time"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// fakeReliability é uma [ReliabilitySource] determinista para os testes: devolve a
// mesma [Reliability] para qualquer par (o controlador é puro; a fonte concreta é
// wiring diferido).
type fakeReliability struct{ rel Reliability }

func (f fakeReliability) Reliability(_, _ string, _ time.Duration) Reliability { return f.rel }

// capturingSink implementa [Sink] e guarda cada [LevelChange] selada — prova que a
// transição chega ao audit COM a métrica e o motivo (AC4).
type capturingSink struct{ sealed []LevelChange }

func (s *capturingSink) SealLevelChange(_ context.Context, ch LevelChange) error {
	s.sealed = append(s.sealed, ch)
	return nil
}

// newFixture constrói um registo (com sink capturador) já a um nível de partida e um
// controlador com a config dada e a fiabilidade dada.
func newFixture(t *testing.T, start Level, cfg AutonomyControlConfig, rel Reliability) (*LevelRegistry, *Controller, *capturingSink, *otelgenai.RecordingTracer) {
	t.Helper()
	sink := &capturingSink{}
	reg := NewLevelRegistry(WithSink(sink))
	if start != L0 {
		if _, err := reg.SetLevel(context.Background(), "agent-1", "http", start, "setup", "test"); err != nil {
			t.Fatalf("setup SetLevel: %v", err)
		}
		sink.sealed = nil // descarta a selagem de setup
	}
	tr := otelgenai.NewRecordingTracer(nil)
	ctl, err := NewController(reg, fakeReliability{rel}, cfg, WithControllerTracer(tr))
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	return reg, ctl, sink, tr
}

// TestPromotionSustainedReliabilityPromotes — AC1: fiabilidade sustentada acima do
// limiar (WindowOK + taxas abaixo dos máximos) PROMOVE UM nível.
func TestPromotionSustainedReliabilityPromotes(t *testing.T) {
	cfg := DefaultAutonomyControlConfig()
	reg, ctl, _, _ := newFixture(t, L2, cfg, Reliability{ErrorRate: 0.01, OverrideRate: 0.10, WindowOK: true})

	ch, changed, err := ctl.Evaluate(context.Background(), "agent-1", "http")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !changed {
		t.Fatal("esperava promocao; nada mudou")
	}
	if ch.Old != L2 || ch.New != L3 {
		t.Errorf("transicao = %s->%s; quer L2->L3", ch.Old, ch.New)
	}
	if got := reg.LevelFor("agent-1", "http"); got != L3 {
		t.Errorf("nivel corrente = %s; quer L3", got)
	}
	if ch.Actor != ControllerActor {
		t.Errorf("actor = %q; quer %q", ch.Actor, ControllerActor)
	}
}

// TestPromotionRisesExactlyOneLevel — AC1 (conservador): mesmo com fiabilidade
// perfeita, a promoção sobe SÓ um nível de cada vez.
func TestPromotionRisesExactlyOneLevel(t *testing.T) {
	cfg := DefaultAutonomyControlConfig()
	_, ctl, _, _ := newFixture(t, L1, cfg, Reliability{ErrorRate: 0, OverrideRate: 0, WindowOK: true})

	ch, changed, err := ctl.Evaluate(context.Background(), "agent-1", "http")
	if err != nil || !changed {
		t.Fatalf("Evaluate: changed=%v err=%v", changed, err)
	}
	if ch.New != L2 {
		t.Errorf("promoveu para %s; quer L2 (um nivel só)", ch.New)
	}
}

// TestPromotionNotSustainedHolds — AC1: fiabilidade NÃO sustentada (janela sem
// cobertura) MANTÉM o nível, ainda que as taxas instantâneas estejam abaixo.
func TestPromotionNotSustainedHolds(t *testing.T) {
	cfg := DefaultAutonomyControlConfig()
	reg, ctl, sink, _ := newFixture(t, L2, cfg, Reliability{ErrorRate: 0.001, OverrideRate: 0.01, WindowOK: false})

	_, changed, err := ctl.Evaluate(context.Background(), "agent-1", "http")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if changed {
		t.Error("promoveu com janela incompleta; devia manter")
	}
	if got := reg.LevelFor("agent-1", "http"); got != L2 {
		t.Errorf("nivel = %s; quer L2 (mantido)", got)
	}
	if len(sink.sealed) != 0 {
		t.Errorf("selou %d eventos; nao devia selar nada ao manter", len(sink.sealed))
	}
}

// TestPromotionAboveThresholdHolds — AC1: taxas ACIMA do limiar mantêm o nível.
func TestPromotionAboveThresholdHolds(t *testing.T) {
	cfg := DefaultAutonomyControlConfig()
	// error_rate ok mas override_rate acima de 0.40 ⇒ mantém.
	_, ctl, _, _ := newFixture(t, L2, cfg, Reliability{ErrorRate: 0.005, OverrideRate: 0.55, WindowOK: true})

	_, changed, err := ctl.Evaluate(context.Background(), "agent-1", "http")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if changed {
		t.Error("promoveu com override_rate acima do limiar; devia manter")
	}
}

// TestPromotionRespectsCeiling — AC1: no tecto configurado, não promove.
func TestPromotionRespectsCeiling(t *testing.T) {
	cfg, err := NewAutonomyControlConfig("1.0.0", 0.02, 0.40, 720*time.Hour, 2, L1, L3)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	_, ctl, _, _ := newFixture(t, L3, cfg, Reliability{ErrorRate: 0, OverrideRate: 0, WindowOK: true})

	_, changed, err := ctl.Evaluate(context.Background(), "agent-1", "http")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if changed {
		t.Error("promoveu acima do tecto L3; devia manter")
	}
}

// TestDemotionOnAnomalyDemotesImmediately — AC2/AC5: uma anomalia rebaixa JÁ para um
// nível mais supervisionado, de forma determinística (L4 -salto2-> L2).
func TestDemotionOnAnomalyDemotesImmediately(t *testing.T) {
	cfg := DefaultAutonomyControlConfig()
	reg, ctl, _, _ := newFixture(t, L4, cfg, Reliability{})

	ch, changed, err := ctl.OnAnomaly(context.Background(), "agent-1", "http", AnomalyOverrideRateSpike)
	if err != nil {
		t.Fatalf("OnAnomaly: %v", err)
	}
	if !changed {
		t.Fatal("esperava democao; nada mudou")
	}
	if ch.Old != L4 || ch.New != L2 {
		t.Errorf("transicao = %s->%s; quer L4->L2", ch.Old, ch.New)
	}
	if got := reg.LevelFor("agent-1", "http"); got != L2 {
		t.Errorf("nivel corrente = %s; quer L2", got)
	}
}

// TestDemotionDeterministicMap — AC5: o mapa de demoção é determinístico e converge
// para o piso (nunca abaixo). Cobre a escada da tecnica/09 §7 (L5->L3, L4->L2,
// L3->L1) e o clamp no piso (L2->L1).
func TestDemotionDeterministicMap(t *testing.T) {
	cfg := DefaultAutonomyControlConfig() // salto=2, piso=L1
	cases := []struct{ from, want Level }{
		{L5, L3}, {L4, L2}, {L3, L1}, {L2, L1},
	}
	for _, tc := range cases {
		reg, ctl, _, _ := newFixture(t, tc.from, cfg, Reliability{})
		ch, changed, err := ctl.OnAnomaly(context.Background(), "agent-1", "http", AnomalyUnsafeAction)
		if err != nil {
			t.Fatalf("OnAnomaly %s: %v", tc.from, err)
		}
		if !changed || ch.New != tc.want {
			t.Errorf("democao de %s = %s (changed=%v); quer %s", tc.from, ch.New, changed, tc.want)
		}
		_ = reg
	}
}

// TestAnomalyNeverPromotes — determinismo/fail-safe: uma anomalia num par já no piso
// NUNCA sobe o nível; mantém-se e não sela nada.
func TestAnomalyNeverPromotes(t *testing.T) {
	cfg := DefaultAutonomyControlConfig() // piso=L1
	reg, ctl, sink, _ := newFixture(t, L1, cfg, Reliability{})

	_, changed, err := ctl.OnAnomaly(context.Background(), "agent-1", "http", AnomalyDrift)
	if err != nil {
		t.Fatalf("OnAnomaly: %v", err)
	}
	if changed {
		t.Error("anomalia mudou o nivel no piso; devia manter (nunca promover)")
	}
	if got := reg.LevelFor("agent-1", "http"); got != L1 {
		t.Errorf("nivel = %s; quer L1 (mantido)", got)
	}
	if len(sink.sealed) != 0 {
		t.Errorf("selou %d eventos numa anomalia sem transicao", len(sink.sealed))
	}
}

// TestAnomalyOnUnregisteredPairFailClosed — um par sem nível registado está em L0
// (fail-closed); uma anomalia não o pode "promover" para o piso L1.
func TestAnomalyOnUnregisteredPairFailClosed(t *testing.T) {
	cfg := DefaultAutonomyControlConfig() // piso=L1
	reg := NewLevelRegistry()
	ctl, err := NewController(reg, nil, cfg)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	_, changed, err := ctl.OnAnomaly(context.Background(), "novo", "db", AnomalyUnsafeAction)
	if err != nil {
		t.Fatalf("OnAnomaly: %v", err)
	}
	if changed {
		t.Error("anomalia promoveu um par L0 para o piso; fail-safe violado")
	}
	if got := reg.LevelFor("novo", "db"); got != L0 {
		t.Errorf("nivel = %s; quer L0 (fail-closed, mantido)", got)
	}
}

// TestConfigurabilityChangesBehavior — AC3: alterar a policy-as-code altera o
// comportamento. Uma fiabilidade que NÃO promovia (override 0.30 > limiar apertado
// 0.20) passa a promover quando o limiar relaxa para 0.40; e uma demoção de salto 1
// passa a salto 3.
func TestConfigurabilityChangesBehavior(t *testing.T) {
	strict, err := NewAutonomyControlConfig("1.0.0", 0.02, 0.20, 720*time.Hour, 1, L1, L5)
	if err != nil {
		t.Fatalf("strict: %v", err)
	}
	reg, ctl, _, _ := newFixture(t, L2, strict, Reliability{ErrorRate: 0.01, OverrideRate: 0.30, WindowOK: true})

	// Com o limiar apertado (0.20), override 0.30 NÃO promove.
	if _, changed, _ := ctl.Evaluate(context.Background(), "agent-1", "http"); changed {
		t.Fatal("promoveu sob politica estrita; nao devia")
	}

	// Relaxa a politica: override_rate_max sobe para 0.40 ⇒ agora promove.
	relaxed, err := NewAutonomyControlConfig("1.1.0", 0.02, 0.40, 720*time.Hour, 3, L1, L5)
	if err != nil {
		t.Fatalf("relaxed: %v", err)
	}
	if err := ctl.SetConfig(relaxed); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}
	ch, changed, err := ctl.Evaluate(context.Background(), "agent-1", "http")
	if err != nil || !changed {
		t.Fatalf("apos relaxar: changed=%v err=%v", changed, err)
	}
	if ch.New != L3 {
		t.Errorf("promoveu para %s; quer L3", ch.New)
	}

	// E o salto de democao passou a 3: de L3 desce para o piso L1 (3 niveis).
	dch, dchanged, err := ctl.OnAnomaly(context.Background(), "agent-1", "http", AnomalyDrift)
	if err != nil || !dchanged {
		t.Fatalf("OnAnomaly: changed=%v err=%v", dchanged, err)
	}
	if dch.New != L1 {
		t.Errorf("democao salto-3 de L3 = %s; quer L1 (piso)", dch.New)
	}
	_ = reg
}

// TestAuditRecordsMetricAndReason — AC4: cada transição (promoção E demoção) regista
// a MÉTRICA e o MOTIVO no evento selado, com o actor do controlador.
func TestAuditRecordsMetricAndReason(t *testing.T) {
	cfg := DefaultAutonomyControlConfig()
	_, ctl, sink, _ := newFixture(t, L2, cfg, Reliability{ErrorRate: 0.01, OverrideRate: 0.10, WindowOK: true})

	if _, changed, err := ctl.Evaluate(context.Background(), "agent-1", "http"); err != nil || !changed {
		t.Fatalf("Evaluate: changed=%v err=%v", changed, err)
	}
	if len(sink.sealed) != 1 {
		t.Fatalf("selou %d eventos; quer 1 (promocao)", len(sink.sealed))
	}
	promo := sink.sealed[0]
	if promo.Actor != ControllerActor {
		t.Errorf("actor selado = %q; quer %q", promo.Actor, ControllerActor)
	}
	for _, want := range []string{"error_rate=", "override_rate=", "0.0100", "0.1000"} {
		if !strings.Contains(promo.Reason, want) {
			t.Errorf("motivo de promocao %q nao contem a metrica %q", promo.Reason, want)
		}
	}

	// Agora uma anomalia: o motivo tem de conter o tipo e o mapa determinístico.
	sink.sealed = nil
	if _, changed, err := ctl.OnAnomaly(context.Background(), "agent-1", "http", AnomalyOverrideRateSpike); err != nil || !changed {
		t.Fatalf("OnAnomaly: changed=%v err=%v", changed, err)
	}
	if len(sink.sealed) != 1 {
		t.Fatalf("selou %d eventos; quer 1 (democao)", len(sink.sealed))
	}
	demo := sink.sealed[0]
	for _, want := range []string{"anomalia", string(AnomalyOverrideRateSpike), "->"} {
		if !strings.Contains(demo.Reason, want) {
			t.Errorf("motivo de democao %q nao contem %q", demo.Reason, want)
		}
	}
}

// TestTransitionSpanEmitted — AC4/DoD: cada transição emite um span aos.autonomy.
// transition com os níveis, o sentido e o motivo (sem segredos).
func TestTransitionSpanEmitted(t *testing.T) {
	cfg := DefaultAutonomyControlConfig()
	_, ctl, _, tr := newFixture(t, L4, cfg, Reliability{ErrorRate: 0.01, OverrideRate: 0.10, WindowOK: true})

	// Promoção L4->L5.
	if _, changed, err := ctl.Evaluate(context.Background(), "agent-1", "http"); err != nil || !changed {
		t.Fatalf("Evaluate: changed=%v err=%v", changed, err)
	}
	// Demoção por anomalia L5->L3.
	if _, changed, err := ctl.OnAnomaly(context.Background(), "agent-1", "http", AnomalyUnsafeAction); err != nil || !changed {
		t.Fatalf("OnAnomaly: changed=%v err=%v", changed, err)
	}

	spans := tr.SpansByOperation(OpAutonomyTransition)
	if len(spans) != 2 {
		t.Fatalf("spans de transicao = %d; quer 2", len(spans))
	}
	promo := spans[0]
	assertAttr(t, promo, AttrAutonomyDirection, directionPromotion)
	assertAttr(t, promo, AttrAutonomyOldLevel, "L4")
	assertAttr(t, promo, AttrAutonomyNewLevel, "L5")
	if !promo.Ended {
		t.Error("span de promocao nao foi fechado")
	}
	demo := spans[1]
	assertAttr(t, demo, AttrAutonomyDirection, directionDemotion)
	assertAttr(t, demo, AttrAutonomyOldLevel, "L5")
	assertAttr(t, demo, AttrAutonomyNewLevel, "L3")
	assertAttr(t, demo, AttrAutonomyAnomalyKind, string(AnomalyUnsafeAction))
}

// TestControllerNilRegistry — construção sem registo é rejeitada fail-closed.
func TestControllerNilRegistry(t *testing.T) {
	if _, err := NewController(nil, nil, DefaultAutonomyControlConfig()); err != ErrNilRegistry {
		t.Errorf("erro = %v; quer ErrNilRegistry", err)
	}
}

// TestControllerRejectsInvalidConfig — uma config inválida na construção é rejeitada.
func TestControllerRejectsInvalidConfig(t *testing.T) {
	bad := AutonomyControlConfig{version: "nope"}
	if _, err := NewController(NewLevelRegistry(), nil, bad); err == nil {
		t.Error("NewController aceitou config invalida")
	}
}

// TestSetConfigRejectsInvalidKeepsOld — AC3 fail-closed: uma config inválida em
// SetConfig é rejeitada e a política anterior mantém-se.
func TestSetConfigRejectsInvalidKeepsOld(t *testing.T) {
	cfg := DefaultAutonomyControlConfig()
	_, ctl, _, _ := newFixture(t, L2, cfg, Reliability{ErrorRate: 0.01, OverrideRate: 0.10, WindowOK: true})

	bad := AutonomyControlConfig{version: "x"}
	if err := ctl.SetConfig(bad); err == nil {
		t.Fatal("SetConfig aceitou config invalida")
	}
	if got := ctl.Config().Version(); got != "1.0.0" {
		t.Errorf("versao apos rejeicao = %q; quer 1.0.0 (mantida)", got)
	}
}

// TestReliabilityFuncAndOptions — cobre os adaptadores/opções de conveniência: a
// [ReliabilityFunc], o [WithControllerClock] e o String de uma anomalia não
// especificada.
func TestReliabilityFuncAndOptions(t *testing.T) {
	var src ReliabilitySource = ReliabilityFunc(func(_, _ string, _ time.Duration) Reliability {
		return Reliability{ErrorRate: 0.01, OverrideRate: 0.10, WindowOK: true}
	})
	reg := NewLevelRegistry()
	if _, err := reg.SetLevel(context.Background(), "a", "d", L2, "setup", "test"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	fixed := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	ctl, err := NewController(reg, src, DefaultAutonomyControlConfig(), WithControllerClock(func() time.Time { return fixed }))
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if _, changed, err := ctl.Evaluate(context.Background(), "a", "d"); err != nil || !changed {
		t.Fatalf("Evaluate via ReliabilityFunc: changed=%v err=%v", changed, err)
	}
	if got := AnomalyKind("").String(); got != "unspecified" {
		t.Errorf("AnomalyKind(\"\").String() = %q; quer unspecified", got)
	}
}

// blockingReliability é uma [ReliabilitySource] que BLOQUEIA dentro de Reliability
// até o teste a libertar — permite injectar deterministicamente uma anomalia na
// janela entre a amostragem da fiabilidade e a escrita da promoção, reproduzindo a
// corrida promoção-vs-demoção.
type blockingReliability struct {
	rel     Reliability
	entered chan struct{} // sinaliza que Reliability foi chamado (base já lida)
	release chan struct{} // Reliability retorna quando fechado
}

func (b *blockingReliability) Reliability(_, _ string, _ time.Duration) Reliability {
	b.entered <- struct{}{}
	<-b.release
	return b.rel
}

// TestAnomalyWinsOverRacingPromotion — CONCORRÊNCIA/fail-safe (anti lost-update): uma
// demoção por anomalia que dispara ENQUANTO uma promoção amostra a janela tem de
// vencer — o agente inseguro NÃO pode acabar restaurado a um nível elevado. Sem a
// serialização e o re-read, a promoção calcularia next a partir de uma base obsoleta
// (L4) e escreveria L5, esmagando a demoção L4->L2.
func TestAnomalyWinsOverRacingPromotion(t *testing.T) {
	cfg := DefaultAutonomyControlConfig()
	sink := &capturingSink{}
	reg := NewLevelRegistry(WithSink(sink))
	if _, err := reg.SetLevel(context.Background(), "agent-1", "http", L4, "setup", "test"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	src := &blockingReliability{
		rel:     Reliability{ErrorRate: 0, OverrideRate: 0, WindowOK: true},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	ctl, err := NewController(reg, src, cfg)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Esperado: ABANDONA (a base mudou de L4 para L2 durante a amostragem).
		if _, changed, err := ctl.Evaluate(context.Background(), "agent-1", "http"); err != nil || changed {
			t.Errorf("Evaluate em corrida: changed=%v err=%v; queria abandonar", changed, err)
		}
	}()

	<-src.entered // a promoção já leu a base L4 e está a amostrar a janela (sem lock)

	// Dispara a anomalia AGORA: demove L4->L2 e sela, de forma síncrona.
	dch, changed, err := ctl.OnAnomaly(context.Background(), "agent-1", "http", AnomalyUnsafeAction)
	if err != nil || !changed {
		t.Fatalf("OnAnomaly: changed=%v err=%v", changed, err)
	}
	if dch.New != L2 {
		t.Fatalf("democao = %s; quer L2", dch.New)
	}

	close(src.release) // deixa a promoção prosseguir para a secção crítica
	<-done

	if got := reg.LevelFor("agent-1", "http"); got != L2 {
		t.Fatalf("nivel final = %s; quer L2 (a anomalia tem de vencer a promocao em corrida)", got)
	}
}

// TestPromotionRollsBackOnSealFailure — AC4/fail-closed: se a selagem no audit falhar,
// a promoção NÃO se mantém em vigor — a concessão é revertida em memória para o nível
// anterior (o PDP nunca lê autonomia elevada sem registo selado) e o erro é devolvido.
// Reutiliza a failingSink de registry_test.go (falha sempre a selagem).
func TestPromotionRollsBackOnSealFailure(t *testing.T) {
	cfg := DefaultAutonomyControlConfig()
	reg := NewLevelRegistry(WithSink(failingSink{}))
	// Estabelece L2 (a selagem de setup falha, mas o nível em memória fica L2).
	if _, err := reg.SetLevel(context.Background(), "agent-1", "http", L2, "setup", "test"); err == nil {
		t.Fatal("setup: esperava erro de selagem da failingSink")
	}

	ctl, err := NewController(reg, fakeReliability{Reliability{ErrorRate: 0, OverrideRate: 0, WindowOK: true}}, cfg)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}

	_, changed, err := ctl.Evaluate(context.Background(), "agent-1", "http")
	if err == nil {
		t.Fatal("Evaluate devia propagar o erro de selagem")
	}
	if changed {
		t.Error("promocao reportou changed=true apesar da selagem falhada; devia reverter")
	}
	if got := reg.LevelFor("agent-1", "http"); got != L2 {
		t.Errorf("nivel = %s; quer L2 (concessao revertida, fail-safe)", got)
	}
}

// TestEvaluateNilSourceNeverPromotes — fail-safe: sem [ReliabilitySource] a promoção
// nunca dispara (mas a demoção continua a funcionar).
func TestEvaluateNilSourceNeverPromotes(t *testing.T) {
	reg := NewLevelRegistry()
	if _, err := reg.SetLevel(context.Background(), "a", "d", L3, "setup", "test"); err != nil {
		t.Fatalf("setup: %v", err)
	}
	ctl, err := NewController(reg, nil, DefaultAutonomyControlConfig())
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if _, changed, _ := ctl.Evaluate(context.Background(), "a", "d"); changed {
		t.Error("promoveu sem fonte de fiabilidade")
	}
	// Demoção continua a funcionar.
	if _, changed, err := ctl.OnAnomaly(context.Background(), "a", "d", AnomalyDrift); err != nil || !changed {
		t.Fatalf("OnAnomaly: changed=%v err=%v", changed, err)
	}
}
