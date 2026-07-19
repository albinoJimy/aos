package backup

import (
	"errors"
	"testing"
)

func TestSovereignty_AllowsSameRegion(t *testing.T) {
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("EU-West") // caixa/espacos normalizados
	if _, err := NewExporter(src, dst, newSigner(t), WithRandSource(detRand())); err != nil {
		t.Fatalf("mesma região (normalizada) devia ser permitida: %v", err)
	}
}

func TestSovereignty_RejectsCrossBorder(t *testing.T) {
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("us-east") // fora da fronteira do board
	_, err := NewExporter(src, dst, newSigner(t), WithRandSource(detRand()))
	if !errors.Is(err, ErrSovereigntyViolation) {
		t.Fatalf("destino cross-border devia ser recusado fail-closed: %v", err)
	}
}

func TestSovereignty_RejectsMissingRegion(t *testing.T) {
	src := newSourceStore(t, "board-eu", "eu-west")
	dst := NewInMemoryImmutableStore("") // região do destino ausente/desconhecida
	_, err := NewExporter(src, dst, newSigner(t), WithRandSource(detRand()))
	if !errors.Is(err, ErrSovereigntyViolation) {
		t.Fatalf("destino sem região devia ser recusado fail-closed: %v", err)
	}
}

func TestSovereignty_CheckHelper(t *testing.T) {
	cases := []struct {
		src, dst string
		wantErr  bool
	}{
		{"eu-west", "eu-west", false},
		{"eu-west", "EU-West", false},
		{"eu-west", "us-east", true},
		{"eu-west", "", true},
		{"", "", true},         // destino sem região sempre recusado
		{"", "eu-west", false}, // sem fronteira no board, destino declarado é aceite
	}
	for _, c := range cases {
		err := checkSovereignty(c.src, c.dst)
		if (err != nil) != c.wantErr {
			t.Fatalf("checkSovereignty(%q,%q)=%v, wantErr=%v", c.src, c.dst, err, c.wantErr)
		}
	}
}
