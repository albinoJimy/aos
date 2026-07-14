package procedural

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/aos-ref/platform/memory/schema"
)

func TestThresholdEvalGate(t *testing.T) {
	tests := []struct {
		name       string
		min        float64
		maxRegr    int
		score      float64
		regr       int
		wantPassed bool
	}{
		{"verde", 0.90, 0, 0.95, 0, true},
		{"score abaixo do limiar", 0.90, 0, 0.80, 0, false},
		{"regressao acima do limiar", 0.90, 0, 0.99, 1, false},
		{"limiar exacto passa", 0.90, 1, 0.90, 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := ThresholdEvalGate{
				MinGoldenSetScore:       tc.min,
				MaxTraceDiffRegressions: tc.maxRegr,
				Metrics:                 func(string, schema.Version) (float64, int) { return tc.score, tc.regr },
			}
			res, err := g.Evaluate(context.Background(), EvalRequest{SkillName: "s", Version: ver(1, 0, 0)})
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if res.Passed != tc.wantPassed {
				t.Fatalf("Passed = %v, quer %v", res.Passed, tc.wantPassed)
			}
		})
	}
}

func TestThresholdEvalGate_NilMetricsFailClosed(t *testing.T) {
	g := ThresholdEvalGate{MinGoldenSetScore: 0}
	res, err := g.Evaluate(context.Background(), EvalRequest{})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if res.Passed {
		t.Fatalf("gate sem métricas devia falhar-fechar")
	}
}

func TestThresholdCanaryGate(t *testing.T) {
	tests := []struct {
		name       string
		minSucc    float64
		maxUnsafe  float64
		succ       float64
		unsafe     float64
		wantPassed bool
	}{
		{"verde", 0.95, 0.01, 0.99, 0.0, true},
		{"success abaixo", 0.95, 0.01, 0.90, 0.0, false},
		{"unsafe acima", 0.95, 0.01, 0.99, 0.05, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := ThresholdCanaryGate{
				MinSuccessRate:      tc.minSucc,
				MaxUnsafeActionRate: tc.maxUnsafe,
				Metrics:             func(string, schema.Version) (float64, float64) { return tc.succ, tc.unsafe },
			}
			res, err := g.Evaluate(context.Background(), CanaryRequest{SkillName: "s", Version: ver(1, 0, 0)})
			if err != nil {
				t.Fatalf("Evaluate: %v", err)
			}
			if res.Passed != tc.wantPassed {
				t.Fatalf("Passed = %v, quer %v", res.Passed, tc.wantPassed)
			}
		})
	}
}

func TestThresholdCanaryGate_NilMetricsFailClosed(t *testing.T) {
	g := ThresholdCanaryGate{}
	res, _ := g.Evaluate(context.Background(), CanaryRequest{})
	if res.Passed {
		t.Fatalf("canary sem métricas devia falhar-fechar")
	}
}

func TestInMemorySkillRegistry(t *testing.T) {
	r := NewInMemorySkillRegistry()
	ctx := context.Background()
	req := RegistrationRequest{
		SkillName:   "s",
		Version:     ver(1, 0, 0),
		ContentHash: []byte{1, 2, 3},
		Signature:   []byte{4, 5, 6},
		SignerKID:   "k1",
	}
	pin, err := r.Register(ctx, req)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if pin.Ref != "registry://s@1.0.0" {
		t.Fatalf("Ref = %q", pin.Ref)
	}
	// Repin da mesma versão é rejeitado (imutabilidade).
	if _, err := r.Register(ctx, req); !errors.Is(err, ErrDuplicateVersion) {
		t.Fatalf("repin err = %v, quer ErrDuplicateVersion", err)
	}
	// Resolve devolve cópia independente (mutar não afecta o pin guardado).
	got, ok, err := r.Resolve(ctx, "s", ver(1, 0, 0))
	if err != nil || !ok {
		t.Fatalf("Resolve: ok=%v err=%v", ok, err)
	}
	got.ContentHash[0] = 99
	again, _, _ := r.Resolve(ctx, "s", ver(1, 0, 0))
	if again.ContentHash[0] != 1 {
		t.Fatalf("Resolve não isolou o blob (mutação vazou)")
	}
	// Versão inexistente.
	if _, ok, _ := r.Resolve(ctx, "s", ver(2, 0, 0)); ok {
		t.Fatalf("Resolve de versão inexistente devolveu ok")
	}
}

func TestEd25519SignerAndRatification(t *testing.T) {
	priv := keyFromSeed(3)
	s := NewEd25519Signer(priv, "kid-x")
	if s.KID() != "kid-x" {
		t.Fatalf("KID = %q", s.KID())
	}
	msg := []byte("mensagem")
	sig := s.Sign(msg)
	if !ed25519.Verify(s.PublicKey(), msg, sig) {
		t.Fatalf("assinatura não verifica")
	}
	// Determinismo: assinar a mesma mensagem duas vezes dá a mesma assinatura.
	if !bytes.Equal(sig, s.Sign(msg)) {
		t.Fatalf("assinatura ed25519 não determinística")
	}

	// Ratificação: assinar liga (ratifier,skill,versão,contentHash); replay noutro
	// alvo falha.
	hashA := []byte{1, 2, 3}
	rat := SignRatification(priv, "human:a", "skillA", ver(1, 0, 0), hashA)
	if !ed25519.Verify(s.PublicKey(), CanonicalRatification("human:a", "skillA", ver(1, 0, 0), hashA), rat.Signature) {
		t.Fatalf("ratificação não verifica no alvo correcto")
	}
	if ed25519.Verify(s.PublicKey(), CanonicalRatification("human:a", "skillB", ver(1, 0, 0), hashA), rat.Signature) {
		t.Fatalf("ratificação verificou num alvo diferente (replay)")
	}
	// PROC-003: a assinatura liga o CONTEÚDO — a mesma (skill,versão) com outro
	// ContentHash NÃO verifica (a assinatura humana cobre os bytes revistos, não só
	// o rótulo SemVer).
	if ed25519.Verify(s.PublicKey(), CanonicalRatification("human:a", "skillA", ver(1, 0, 0), []byte{9, 9, 9}), rat.Signature) {
		t.Fatalf("ratificação verificou com ContentHash diferente (não liga ao conteúdo)")
	}
}

func TestCanonicalDeterminism(t *testing.T) {
	man := NewManifest("s", ver(1, 2, 3), "agent:x", "run", []byte("c"))
	if !bytes.Equal(canonicalManifest(man), canonicalManifest(man)) {
		t.Fatalf("canonicalManifest não determinístico")
	}
	extra := map[string]string{"b": "2", "a": "1", "c": "3"}
	p1 := canonicalTransition("s", ver(1, 0, 0), StageStaging, StageEvalGate, "agent", 100, extra)
	p2 := canonicalTransition("s", ver(1, 0, 0), StageStaging, StageEvalGate, "agent", 100, extra)
	if !bytes.Equal(p1, p2) {
		t.Fatalf("canonicalTransition não determinístico")
	}
	// Ordem de inserção do mapa não muda o payload (chaves ordenadas).
	extra2 := map[string]string{"c": "3", "a": "1", "b": "2"}
	p3 := canonicalTransition("s", ver(1, 0, 0), StageStaging, StageEvalGate, "agent", 100, extra2)
	if !bytes.Equal(p1, p3) {
		t.Fatalf("canonicalTransition depende da ordem de iteração do mapa")
	}
	// Conteúdo diferente → hash diferente.
	if bytes.Equal(computeContentHash([]byte("a")), computeContentHash([]byte("b"))) {
		t.Fatalf("computeContentHash colidiu")
	}
}
