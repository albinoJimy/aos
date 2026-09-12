package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// aos090_promocao_test.go — AOS-090/ADR-025 Fase D: a ReliabilitySource agregada + a promoção
// automática. Prova que a promoção dispara com fiabilidade sustentada, NUNCA cruza L4
// (dual-control), e recusa com observação insuficiente ou erro alto.

func eventoDesfecho(classe, capability, outcome string) eventstore.Event {
	raw, _ := json.Marshal(map[string]any{
		"agent_class": classe,
		"capability":  capability,
		"resource":    map[string]any{"value": "/x"},
		"outcome":     outcome,
	})
	return eventstore.Event{Type: referencemonitor.EventTypeOutcome, Payload: raw}
}

func eventoDecisao(tipo, classe, capability string) eventstore.Event {
	raw, _ := json.Marshal(map[string]any{
		"capability": capability,
		"resource":   map[string]any{"value": "/x"},
		"principal":  map[string]any{"agent_class": classe},
	})
	return eventstore.Event{Type: tipo, Payload: raw}
}

// aggComHistoria constrói um agregador cuja observação JÁ cobre a janela, alimentado com
// `ok` desfechos ok, `erro` desfechos com erro e `dec` decisões (todas mediated, zero escalada).
func aggComHistoria(t *testing.T, janela time.Duration, ok, erro, dec int) *fiabilidadeAgregada {
	t.Helper()
	base := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	agora := base
	relogio := func() time.Time { return agora }
	agg := novaFiabilidade(&esFake{}, relogio, janela, time.Hour)
	// Avança para além da janela para a observação a cobrir (WindowOK honesto).
	agora = base.Add(janela + time.Minute)
	for i := 0; i < ok; i++ {
		agg.observar(eventoDesfecho("worker", "cap:fs.write", referencemonitor.OutcomeOK))
	}
	for i := 0; i < erro; i++ {
		agg.observar(eventoDesfecho("worker", "cap:fs.write", referencemonitor.OutcomeError))
	}
	for i := 0; i < dec; i++ {
		agg.observar(eventoDecisao(referencemonitor.EventTypeMediated, "worker", "cap:fs.write"))
	}
	return agg
}

func controladorComFonte(t *testing.T, src autonomy.ReliabilitySource, janela time.Duration, nivelInicial autonomy.Level) (*autonomy.Controller, *autonomy.LevelRegistry) {
	t.Helper()
	reg := autonomy.NewLevelRegistry(autonomy.WithSink(autonomy.NewAuditSink(audit.NewMemStore(), "")))
	if _, err := reg.SetLevel(context.Background(), autonomy.ClassPrefix+"worker", "fs", nivelInicial, "base", "gov-admin"); err != nil {
		t.Fatal(err)
	}
	// errorRateMax 2%, overrideRateMax 40%, tecto L3 (já travado abaixo de dual-control).
	cfg, err := autonomy.NewAutonomyControlConfig("1.0.0", 0.02, 0.40, janela, 2, autonomy.L1, autonomy.L3)
	if err != nil {
		t.Fatal(err)
	}
	ctrl, err := autonomy.NewController(reg, src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ctrl, reg
}

// TestAOS090_Promocao_FiabilidadeSustentadaPromoveAbaixoDeL4 — com erro 0% e override 0%
// sustentados, a classe sobe L1→L2→L3 e PÁRA em L3 (nunca L4, o limiar de dual-control).
func TestAOS090_Promocao_FiabilidadeSustentadaPromoveAbaixoDeL4(t *testing.T) {
	janela := time.Hour
	agg := aggComHistoria(t, janela, 50, 0, 50) // 0% erro, 0% override, amostras suficientes
	ctrl, reg := controladorComFonte(t, agg, janela, autonomy.L1)
	ctx := context.Background()

	// Três avaliações: L1→L2, L2→L3, depois mantém L3 (tecto).
	niveisEsperados := []autonomy.Level{autonomy.L2, autonomy.L3, autonomy.L3}
	for i, quer := range niveisEsperados {
		ctrl.Evaluate(ctx, autonomy.ClassPrefix+"worker", "fs")
		if got := reg.LevelFor(autonomy.ClassPrefix+"worker", "fs"); got != quer {
			t.Fatalf("apos avaliacao %d: nivel=%s, quer %s", i+1, got, quer)
		}
	}
	// Nunca cruzou para L4.
	if got := reg.LevelFor(autonomy.ClassPrefix+"worker", "fs"); got >= autonomy.L4 {
		t.Fatalf("a promocao automatica NUNCA pode cruzar L4; ficou em %s", got)
	}
}

// TestAOS090_Promocao_ErroAltoNaoPromove — taxa de erro acima do limiar (aqui ~9%) mantém o
// nível: não se promove um agente que falha.
func TestAOS090_Promocao_ErroAltoNaoPromove(t *testing.T) {
	janela := time.Hour
	agg := aggComHistoria(t, janela, 50, 5, 50) // 5/55 ≈ 9% erro > 2%
	ctrl, reg := controladorComFonte(t, agg, janela, autonomy.L1)
	ctrl.Evaluate(context.Background(), autonomy.ClassPrefix+"worker", "fs")
	if got := reg.LevelFor(autonomy.ClassPrefix+"worker", "fs"); got != autonomy.L1 {
		t.Fatalf("erro alto não devia promover; nivel=%s (quer L1)", got)
	}
}

// TestAOS090_Promocao_ObservacaoInsuficienteNaoPromove — sem cobrir a janela, WindowOK=false e
// não se promove (não se afirma fiabilidade sustentada sem história).
func TestAOS090_Promocao_ObservacaoInsuficienteNaoPromove(t *testing.T) {
	janela := time.Hour
	base := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	agora := base
	agg := novaFiabilidade(&esFake{}, func() time.Time { return agora }, janela, time.Hour)
	// Só 10 minutos de observação (< janela de 1h): muitos eventos bons, mas cedo demais.
	agora = base.Add(10 * time.Minute)
	for i := 0; i < 100; i++ {
		agg.observar(eventoDesfecho("worker", "cap:fs.write", referencemonitor.OutcomeOK))
		agg.observar(eventoDecisao(referencemonitor.EventTypeMediated, "worker", "cap:fs.write"))
	}
	if rel := agg.Reliability(autonomy.ClassPrefix+"worker", "fs", janela); rel.WindowOK {
		t.Fatalf("observacao de 10min < janela 1h nao pode ser WindowOK: %+v", rel)
	}
	ctrl, reg := controladorComFonte(t, agg, janela, autonomy.L1)
	ctrl.Evaluate(context.Background(), autonomy.ClassPrefix+"worker", "fs")
	if got := reg.LevelFor(autonomy.ClassPrefix+"worker", "fs"); got != autonomy.L1 {
		t.Fatalf("sem janela coberta nao promove; nivel=%s (quer L1)", got)
	}
}

// TestAOS090_Promocao_OverrideProxyDaEscalada — o override-rate vem da proxy de escalada:
// muitas escaladas ⇒ override alto ⇒ não promove, mesmo com erro 0%.
func TestAOS090_Promocao_OverrideProxyDaEscalada(t *testing.T) {
	janela := time.Hour
	base := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	agora := base
	agg := novaFiabilidade(&esFake{}, func() time.Time { return agora }, janela, time.Hour)
	agora = base.Add(janela + time.Minute)
	for i := 0; i < 50; i++ {
		agg.observar(eventoDesfecho("worker", "cap:fs.write", referencemonitor.OutcomeOK)) // 0% erro
	}
	// Metade das decisões são escaladas ⇒ override ~50% > 40%.
	for i := 0; i < 25; i++ {
		agg.observar(eventoDecisao(referencemonitor.EventTypeMediated, "worker", "cap:fs.write"))
		agg.observar(eventoDecisao(referencemonitor.EventTypeEscalated, "worker", "cap:fs.write"))
	}
	rel := agg.Reliability(autonomy.ClassPrefix+"worker", "fs", janela)
	if !rel.WindowOK || rel.OverrideRate < 0.4 {
		t.Fatalf("override-rate devia ser alto e a janela OK: %+v", rel)
	}
	ctrl, reg := controladorComFonte(t, agg, janela, autonomy.L1)
	ctrl.Evaluate(context.Background(), autonomy.ClassPrefix+"worker", "fs")
	if got := reg.LevelFor(autonomy.ClassPrefix+"worker", "fs"); got != autonomy.L1 {
		t.Fatalf("override alto (proxy de escalada) nao devia promover; nivel=%s (quer L1)", got)
	}
}
