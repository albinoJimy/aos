package autonomy

import (
	"errors"
	"testing"
	"time"
)

// TestLoadControlConfigRoundTrip — a policy-as-code carrega de JSON, valida e faz
// round-trip via Marshal (autorável/revisável como código).
func TestLoadControlConfigRoundTrip(t *testing.T) {
	raw := []byte(`{
		"version": "1.2.0",
		"error_rate_max": 0.02,
		"override_rate_max": 0.40,
		"window": "720h",
		"demotion_drop": 2,
		"demotion_floor": "L1",
		"promotion_ceil": "L5"
	}`)
	cfg, err := LoadAutonomyControlConfig(raw)
	if err != nil {
		t.Fatalf("LoadAutonomyControlConfig: %v", err)
	}
	if cfg.Version() != "1.2.0" || cfg.Window() != 720*time.Hour {
		t.Errorf("carga = v%s janela=%s; quer v1.2.0 720h0m0s", cfg.Version(), cfg.Window())
	}
	if cfg.DemotionDrop() != 2 || cfg.DemotionFloor() != L1 || cfg.PromotionCeil() != L5 {
		t.Errorf("democao/piso/tecto = %d/%s/%s", cfg.DemotionDrop(), cfg.DemotionFloor(), cfg.PromotionCeil())
	}

	out, err := cfg.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := LoadAutonomyControlConfig(out)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if back.ContentHash() != cfg.ContentHash() {
		t.Errorf("hash apos round-trip diverge: %s != %s", back.ContentHash(), cfg.ContentHash())
	}
}

// TestControlConfigContentHashChangesWithThreshold — AC3: uma alteração de limiar
// muda o ContentHash (a transição de política é detectável/auditável).
func TestControlConfigContentHashChangesWithThreshold(t *testing.T) {
	a := DefaultAutonomyControlConfig()
	b, err := NewAutonomyControlConfig(a.Version(), 0.05, a.OverrideRateMax(), a.Window(), a.DemotionDrop(), a.DemotionFloor(), a.PromotionCeil())
	if err != nil {
		t.Fatalf("config b: %v", err)
	}
	if a.ContentHash() == b.ContentHash() {
		t.Error("hash nao mudou com error_rate_max diferente")
	}
}

// TestControlConfigValidateFailClosed — cada invariante fail-closed é recusada.
func TestControlConfigValidateFailClosed(t *testing.T) {
	cases := []struct {
		name string
		cfg  AutonomyControlConfig
	}{
		{"versao invalida", AutonomyControlConfig{version: "1.0", window: time.Hour, demotionDrop: 1, demotionFloor: L1, promotionCeil: L5}},
		{"error_rate fora", AutonomyControlConfig{version: "1.0.0", errorRateMax: 1.5, window: time.Hour, demotionDrop: 1, demotionFloor: L1, promotionCeil: L5}},
		{"override_rate fora", AutonomyControlConfig{version: "1.0.0", overrideRateMax: -0.1, window: time.Hour, demotionDrop: 1, demotionFloor: L1, promotionCeil: L5}},
		{"janela <= 0", AutonomyControlConfig{version: "1.0.0", window: 0, demotionDrop: 1, demotionFloor: L1, promotionCeil: L5}},
		{"salto < 1", AutonomyControlConfig{version: "1.0.0", window: time.Hour, demotionDrop: 0, demotionFloor: L1, promotionCeil: L5}},
		{"piso invalido", AutonomyControlConfig{version: "1.0.0", window: time.Hour, demotionDrop: 1, demotionFloor: Level(9), promotionCeil: L5}},
		{"tecto abaixo do piso", AutonomyControlConfig{version: "1.0.0", window: time.Hour, demotionDrop: 1, demotionFloor: L4, promotionCeil: L2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); !errors.Is(err, ErrInvalidControlConfig) {
				t.Errorf("Validate() = %v; quer ErrInvalidControlConfig", err)
			}
		})
	}
}

// TestLoadControlConfigRejectsMalformed — JSON/duração/nível malformados são
// recusados fail-closed.
func TestLoadControlConfigRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"json invalido":   `{`,
		"janela invalida": `{"version":"1.0.0","window":"nope","demotion_floor":"L1","promotion_ceil":"L5","demotion_drop":1}`,
		"piso invalido":   `{"version":"1.0.0","window":"1h","demotion_floor":"L9","promotion_ceil":"L5","demotion_drop":1}`,
		"tecto invalido":  `{"version":"1.0.0","window":"1h","demotion_floor":"L1","promotion_ceil":"LX","demotion_drop":1}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadAutonomyControlConfig([]byte(raw)); !errors.Is(err, ErrInvalidControlConfig) {
				t.Errorf("Load(%q) err = %v; quer ErrInvalidControlConfig", raw, err)
			}
		})
	}
}

// TestDefaultControlConfigValid — a config de referência do blueprint é válida e
// espelha a tecnica/09 §7 (erro <2%, override <40%, salto 2, piso L1).
func TestDefaultControlConfigValid(t *testing.T) {
	cfg := DefaultAutonomyControlConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default invalida: %v", err)
	}
	if cfg.ErrorRateMax() != 0.02 || cfg.OverrideRateMax() != 0.40 {
		t.Errorf("limiares = %v/%v; quer 0.02/0.40", cfg.ErrorRateMax(), cfg.OverrideRateMax())
	}
	if cfg.DemotionDrop() != 2 || cfg.DemotionFloor() != L1 {
		t.Errorf("democao = salto %d piso %s; quer salto 2 piso L1", cfg.DemotionDrop(), cfg.DemotionFloor())
	}
}

// TestParseLevelFailClosed — o parser de nível recusa rótulos desconhecidos.
func TestParseLevelFailClosed(t *testing.T) {
	if _, ok := parseLevel("L9"); ok {
		t.Error("parseLevel aceitou L9")
	}
	if lvl, ok := parseLevel("L3"); !ok || lvl != L3 {
		t.Errorf("parseLevel(L3) = %s,%v; quer L3,true", lvl, ok)
	}
}
