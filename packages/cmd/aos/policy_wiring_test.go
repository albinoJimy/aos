package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------
// AOS-087 — níveis de autonomia (o que torna o `escalate` ALCANÇÁVEL)
// ---------------------------------------------------------------------------

// TestAutonomy_NaoConfiguradoNaoLigaOOraculo: sem a variável, o oráculo não é ligado e o
// comportamento é exactamente o de antes (nada escala). É a razão de ser opt-in — ligar
// com registo vazio faria CADA tool call exigir aprovação (L0 ⇒ suggest).
func TestAutonomy_NaoConfiguradoNaoLigaOOraculo(t *testing.T) {
	t.Setenv("AOS_AUTONOMY_LEVELS", "")
	specs, err := parseAutonomyLevels()
	if err != nil || specs != nil {
		t.Fatalf("não configurado devia dar (nil,nil); specs=%+v err=%v", specs, err)
	}
	if w := buildAutonomyOracle(specs); w != nil {
		t.Fatalf("sem specs não devia haver cablagem de oráculo; veio %v", w)
	}
}

// TestAutonomy_ParseEConstroiORegisto: o formato `agente:dominio=Ln` resolve-se em níveis
// consultáveis, e um par NÃO registado continua fail-closed em L0.
func TestAutonomy_ParseEConstroiORegisto(t *testing.T) {
	t.Setenv("AOS_AUTONOMY_LEVELS", "agt-1:http=L4, agt-1:fs=L5")
	specs, err := parseAutonomyLevels()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("esperava 2 entradas, veio %d", len(specs))
	}
	// AOS-248: a cablagem é em DUAS FASES — o registo nasce vazio (com o sink ligado) e os níveis
	// só entram em vigor no `provision`, com o WORM composto. É por isso que este teste sela num
	// [audit.NewMemStore]: sem store não haveria selo e o provisionamento RECUSARIA.
	wiring := buildAutonomyOracle(specs)
	if wiring == nil {
		t.Fatal("buildAutonomyOracle: com specs devia devolver cablagem")
	}
	if err := wiring.provision(context.Background(), audit.NewMemStore()); err != nil {
		t.Fatalf("provision: %v", err)
	}
	oracle := wiring.oracle()
	if got := oracle.LevelFor("agt-1", "http"); got != autonomy.L4 {
		t.Fatalf("agt-1:http devia ser L4, veio %v", got)
	}
	if got := oracle.LevelFor("agt-1", "fs"); got != autonomy.L5 {
		t.Fatalf("agt-1:fs devia ser L5, veio %v", got)
	}
	// FAIL-CLOSED: par não registado ⇒ L0 (o mais restritivo).
	if got := oracle.LevelFor("agt-desconhecido", "http"); got != autonomy.L0 {
		t.Fatalf("par não registado devia ser L0 (fail-closed), veio %v", got)
	}
}

// TestAutonomy_NivelEscolhidoDeterminaOEscalate liga as duas pontas: é a composição
// nível × classe que decide se uma acção escala. Prova que L4 (a escolha documentada para
// um agente autónomo) corre acções seguras e ESCALA as perigosas.
func TestAutonomy_NivelEscolhidoDeterminaOEscalate(t *testing.T) {
	casos := []struct {
		nivel        autonomy.Level
		classe       risk.Class
		querEscalate bool
	}{
		{autonomy.L4, risk.ClassSafe, false},   // corre
		{autonomy.L4, risk.ClassDanger, true},  // a EXCEPÇÃO que exige humano
		{autonomy.L0, risk.ClassSafe, true},    // sugestão: tudo escala
		{autonomy.L5, risk.ClassDanger, false}, // amostragem post-hoc, não bloqueia
	}
	for _, c := range casos {
		modo := autonomy.Oversight(c.nivel, c.classe)
		if got := modo.RequiresHumanGate(); got != c.querEscalate {
			t.Fatalf("%v x %v ⇒ modo %v, gate humano=%t (esperado %t)", c.nivel, c.classe, modo, got, c.querEscalate)
		}
	}
}

// TestAutonomy_ConfigInvalidaAborta: fail-closed na configuração. Uma entrada mal escrita
// ignorada em silêncio deixaria o agente sob um nível diferente do pretendido.
func TestAutonomy_ConfigInvalidaAborta(t *testing.T) {
	for _, mau := range []string{"sem-igual", "agt-1=L4", "agt-1:http=L9", "agt-1:http=", ":http=L4"} {
		t.Setenv("AOS_AUTONOMY_LEVELS", mau)
		if _, err := parseAutonomyLevels(); !errors.Is(err, ErrBadAutonomyLevels) {
			t.Fatalf("%q devia dar ErrBadAutonomyLevels; err=%v", mau, err)
		}
	}
}

// ---------------------------------------------------------------------------
// AOS-080/081 — limiares do circuit breaker (o que o torna EFICAZ)
// ---------------------------------------------------------------------------

// TestBreakerThresholds_DefaultsLigamOsDoisSinaisJustificaveis: por omissão ficam ligados
// no-progress e wall-clock; as velocidades de queima ficam DESLIGADAS — uma escolha
// deliberada (um tecto de velocidade errado mata runs saudáveis e o valor certo depende
// da carga, que este nó não conhece).
func TestBreakerThresholds_DefaultsLigamOsDoisSinaisJustificaveis(t *testing.T) {
	for _, k := range []string{"AOS_BREAKER_MAX_STALE_ITERATIONS", "AOS_BREAKER_MAX_WALL_CLOCK",
		"AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC", "AOS_BREAKER_MAX_TOKENS_PER_SEC"} {
		t.Setenv(k, "")
	}
	prov, err := breakerThresholdsFromEnv()
	if err != nil {
		t.Fatalf("breakerThresholdsFromEnv: %v", err)
	}
	if prov == nil {
		t.Fatal("com defaults o disjuntor devia ser composto")
	}
	th := prov.Thresholds("qualquer-classe")
	if th.MaxStaleIterations != DefaultBreakerMaxStaleIterations {
		t.Fatalf("no-progress devia vir ligado a %d, veio %d", DefaultBreakerMaxStaleIterations, th.MaxStaleIterations)
	}
	if th.MaxWallClock != DefaultBreakerMaxWallClock {
		t.Fatalf("wall-clock devia vir ligado a %s, veio %s", DefaultBreakerMaxWallClock, th.MaxWallClock)
	}
	if th.MaxCostMicroUSDPerSecond != 0 || th.MaxTokensPerSecond != 0 {
		t.Fatalf("as velocidades de queima devem vir DESLIGADAS por omissão: %+v", th)
	}
}

// TestBreakerThresholds_TudoDesligadoNaoCompoe: com todos os sinais a 0 não se compõe um
// disjuntor — um disjuntor cego daria a ilusão de protecção.
func TestBreakerThresholds_TudoDesligadoNaoCompoe(t *testing.T) {
	t.Setenv("AOS_BREAKER_MAX_STALE_ITERATIONS", "0")
	t.Setenv("AOS_BREAKER_MAX_WALL_CLOCK", "0")
	t.Setenv("AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC", "0")
	t.Setenv("AOS_BREAKER_MAX_TOKENS_PER_SEC", "0")
	prov, err := breakerThresholdsFromEnv()
	if err != nil {
		t.Fatalf("breakerThresholdsFromEnv: %v", err)
	}
	if prov != nil {
		t.Fatal("com todos os sinais desligados NÃO se deve compor disjuntor")
	}
}

// TestBreakerThresholds_ValoresExplicitos: a configuração sobrepõe os defaults.
func TestBreakerThresholds_ValoresExplicitos(t *testing.T) {
	t.Setenv("AOS_BREAKER_MAX_STALE_ITERATIONS", "7")
	t.Setenv("AOS_BREAKER_MAX_WALL_CLOCK", "90s")
	t.Setenv("AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC", "250.5")
	t.Setenv("AOS_BREAKER_MAX_TOKENS_PER_SEC", "1000")
	prov, err := breakerThresholdsFromEnv()
	if err != nil {
		t.Fatalf("breakerThresholdsFromEnv: %v", err)
	}
	th := prov.Thresholds("c")
	if th.MaxStaleIterations != 7 || th.MaxWallClock != 90*time.Second ||
		th.MaxCostMicroUSDPerSecond != 250.5 || th.MaxTokensPerSecond != 1000 {
		t.Fatalf("valores explícitos não aplicados: %+v", th)
	}
}

// TestBreakerThresholds_ConfigInvalidaAborta: fail-closed na CONFIGURAÇÃO. Um limiar mal
// escrito que fosse ignorado deixaria o operador convencido de que está protegido.
func TestBreakerThresholds_ConfigInvalidaAborta(t *testing.T) {
	casos := map[string]string{
		"AOS_BREAKER_MAX_STALE_ITERATIONS":       "tres",
		"AOS_BREAKER_MAX_WALL_CLOCK":             "30 minutos",
		"AOS_BREAKER_MAX_COST_MICRO_USD_PER_SEC": "-1",
		"AOS_BREAKER_MAX_TOKENS_PER_SEC":         "muitos",
	}
	for k, v := range casos {
		t.Run(k, func(t *testing.T) {
			for kk := range casos {
				t.Setenv(kk, "")
			}
			t.Setenv(k, v)
			if _, err := breakerThresholdsFromEnv(); !errors.Is(err, ErrBadBreakerThresholds) {
				t.Fatalf("%s=%q devia dar ErrBadBreakerThresholds; err=%v", k, v, err)
			}
		})
	}
}
