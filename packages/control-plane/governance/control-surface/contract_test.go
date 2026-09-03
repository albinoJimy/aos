package controlsurface_test

// AOS-303 — O QUE SOBRA DO TESTE DE CONTRATO.
//
// Este ficheiro tinha cinco testes do contrato de mensagens: schema válido, fail-closed por
// campo, MAJOR incompatível, SemVer, e round-trip JSON. Quatro deles exercitavam a
// `ControlMessage` e saíram com ela em AOS-303 — não foram reaproveitados, e a razão é que
// provam a validação de um payload que deixou de existir; reaproveitá-los exigiria manter o
// payload, que é a via que a decisão recusou.
//
// FICA o teste de SemVer, porque o que ele exercita SOBREVIVEU: `ControlSchemaVersion`,
// `ParseControlSchemaVersion`, a ordenação total, a classificação de mudança e o
// `SchemaDomain` — tudo em `version.go`, consumido por `governance/approval-card`.

import (
	"errors"
	"testing"

	controlsurface "github.com/aos-ref/control-plane/governance/control-surface"
)

// TestContract_SemVerParseCompareClassify — AC5: parse fail-closed, ordenação total,
// classificação de mudança (MAJOR/MINOR/PATCH) e o domínio versionado.
func TestContract_SemVerParseCompareClassify(t *testing.T) {
	if controlsurface.SchemaDomain != "aos.control.surface.v1" {
		t.Fatalf("SchemaDomain=%q, quero aos.control.surface.v1", controlsurface.SchemaDomain)
	}

	// Parse fail-closed estrito.
	for _, bad := range []string{"", "1", "1.2", "1.2.3.4", "1.x.0", "-1.0.0", "v1.0.0", "1..3"} {
		if _, err := controlsurface.ParseControlSchemaVersion(bad); !errors.Is(err, controlsurface.ErrInvalidSchemaVersion) {
			t.Errorf("ParseControlSchemaVersion(%q) devia falhar fail-closed, err=%v", bad, err)
		}
	}
	v, err := controlsurface.ParseControlSchemaVersion(" 1.2.3 ")
	if err != nil || v.String() != "1.2.3" {
		t.Fatalf("ParseControlSchemaVersion(1.2.3)=%v,%v", v, err)
	}

	// Ordenação total.
	a := controlsurface.ControlSchemaVersion{Major: 1, Minor: 2, Patch: 3}
	b := controlsurface.ControlSchemaVersion{Major: 1, Minor: 3, Patch: 0}
	if !a.Less(b) || a.Equal(b) || a.Compare(a) != 0 {
		t.Fatalf("ordenação: a<b=%v, a==b=%v", a.Less(b), a.Equal(b))
	}

	// Classificação de mudança.
	cases := []struct {
		from, to controlsurface.ControlSchemaVersion
		want     controlsurface.ChangeKind
	}{
		{a, a, controlsurface.ChangeNone},
		{controlsurface.ControlSchemaVersion{Major: 1}, controlsurface.ControlSchemaVersion{Major: 2}, controlsurface.ChangeMajor},
		{controlsurface.ControlSchemaVersion{Major: 1, Minor: 1}, controlsurface.ControlSchemaVersion{Major: 1, Minor: 2}, controlsurface.ChangeMinor},
		{controlsurface.ControlSchemaVersion{Major: 1, Minor: 1, Patch: 1}, controlsurface.ControlSchemaVersion{Major: 1, Minor: 1, Patch: 2}, controlsurface.ChangePatch},
		// Downgrade de MAJOR é na mesma uma quebra (fail-closed simétrico).
		{controlsurface.ControlSchemaVersion{Major: 2}, controlsurface.ControlSchemaVersion{Major: 1}, controlsurface.ChangeMajor},
	}
	for _, tc := range cases {
		if got := controlsurface.Classify(tc.from, tc.to); got != tc.want {
			t.Errorf("Classify(%s→%s)=%s, quero %s", tc.from, tc.to, got, tc.want)
		}
	}
}

// TestContract_JSONRoundTrip — AC1: o codec JSON é estável (round-trip preserva todos
