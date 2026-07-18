package hitl

import (
	"testing"

	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// AC6 — tiering policy-as-code: safe corre (ModeRun), gray agrupa (ModeBatch), danger
// confirma individual (ModeConfirm).
func TestTiering_SAROCMapping(t *testing.T) {
	t.Parallel()
	p := DefaultTieringPolicy()
	cases := []struct {
		class risk.Class
		want  Mode
	}{
		{risk.ClassSafe, ModeRun},
		{risk.ClassGray, ModeBatch},
		{risk.ClassDanger, ModeConfirm},
	}
	for _, tc := range cases {
		if got := p.ModeFor(tc.class); got != tc.want {
			t.Fatalf("ModeFor(%v) = %v, esperava %v", tc.class, got, tc.want)
		}
	}
}

// Fail-closed: uma política vazia (sem regras) devolve ModeConfirm para qualquer
// classe — nunca corre sem gate por omissão.
func TestTiering_EmptyPolicyFailClosed(t *testing.T) {
	t.Parallel()
	empty := TieringPolicy{VersionTag: "empty"}
	for _, c := range []risk.Class{risk.ClassSafe, risk.ClassGray, risk.ClassDanger} {
		if got := empty.ModeFor(c); got != ModeConfirm {
			t.Fatalf("politica vazia: ModeFor(%v) = %v, esperava ModeConfirm (fail-closed)", c, got)
		}
	}
}

// O digest é tamper-evident: alterar o mapa de tiering muda o digest/version.
func TestTiering_DigestTamperEvident(t *testing.T) {
	t.Parallel()
	base := DefaultTieringPolicy()
	tampered := DefaultTieringPolicy()
	// Rebaixa danger para lote (o ataque clássico de diluir a fricção).
	for i := range tampered.Rules {
		if tampered.Rules[i].Class == risk.ClassDanger {
			tampered.Rules[i].Mode = ModeBatch
		}
	}
	if base.Digest() == tampered.Digest() {
		t.Fatalf("digest nao detectou a alteracao do mapa de tiering")
	}
	if base.Version() == tampered.Version() {
		t.Fatalf("version devia diferir apos adulteracao")
	}
	// Estável: recomputar dá o mesmo.
	if base.Digest() != DefaultTieringPolicy().Digest() {
		t.Fatalf("digest nao determinista")
	}
}

func TestMode_String(t *testing.T) {
	t.Parallel()
	if ModeRun.String() != "run" || ModeBatch.String() != "batch" || ModeConfirm.String() != "confirm" {
		t.Fatalf("String() dos modos inconsistente")
	}
}
