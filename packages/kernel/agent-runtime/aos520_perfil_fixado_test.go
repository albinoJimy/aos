package agentruntime

import (
	"reflect"
	"testing"
)

// AOS-520 (PR 0) — o perfil da rota fixado por run, do lado do kernel: SÓ TRANSPORTE. O digest em
// que o run está fixado segue do Goal para a vista de cada turno, tal e qual, e não muda um byte
// do que o run grava nem do prompt.

// SEM DIGEST FIXADO a vista não leva nada. COM ele — um digest, a marca de «não fixado», ou um
// valor sem forma — a vista leva-o TAL E QUAL em todos os turnos, e os `turn.recorded` são byte a
// byte os do run sem ele: o kernel não o grava, não o interpreta e não o descarta.
func TestAOS520_Loop_ODigestFixadoSegueNaVistaEONucleoNaoOGrava(t *testing.T) {
	base, vistasBase := aos513Run(t, sampleGoal(), nil)
	if len(base) != 2 {
		t.Fatalf("queria 2 turnos, vieram %d", len(base))
	}
	for _, v := range vistasBase {
		if v.RouteProfileDigest != "" {
			t.Fatalf("a vista de um run sem perfil fixado leva %q", v.RouteProfileDigest)
		}
	}
	for _, fixado := range []string{
		"sha256:0cbc100e54038f013a6c84867ff08510095f47dd17b2dd07f68b5dc74f090d0f",
		RouteProfileDigestUnpinned,
		// Um valor sem forma NÃO é reduzido a vazio: vazio quer dizer «não fixado», e um run com
		// um valor que não se percebe tem de chegar a quem confere para ser recusado.
		"texto que nao e um digest",
	} {
		goal := sampleGoal()
		goal.RouteProfileDigest = fixado
		turnos, vistas := aos513Run(t, goal, nil)
		if !reflect.DeepEqual(turnos, base) {
			t.Fatalf("digest %q: os turn.recorded mudaram com o perfil fixado no Goal", fixado)
		}
		if len(vistas) != 2 {
			t.Fatalf("digest %q: vistas = %d", fixado, len(vistas))
		}
		for i, v := range vistas {
			if v.RouteProfileDigest != fixado {
				t.Fatalf("digest %q, turno %d: a vista leva %q", fixado, i+1, v.RouteProfileDigest)
			}
			if string(v.Materialized) != string(vistasBase[i].Materialized) || v.PromptHash != vistasBase[i].PromptHash {
				t.Fatalf("digest %q, turno %d: o prompt mudou com o perfil fixado", fixado, i+1)
			}
		}
	}
}
