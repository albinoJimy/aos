package durable

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestAOS432_RecusaDoLeaseNomeiaODono — quem perde a posse tem de saber A QUEM a perdeu e
// até quando. A recusa continua a ser [ErrLeaseHeld] na cadeia (é por errors.Is que o
// `aos-orq` a traduz para o código 3), e a mensagem passa a nomear o worker detentor, o
// token e a expiração.
func TestAOS432_RecusaDoLeaseNomeiaODono(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	store := newStore(t)
	dono := newManager(t, store, clk, WithWorkerID("replica-dona"))
	intruso := newManager(t, store, clk, WithWorkerID("replica-intrusa"))
	ctx := context.Background()
	const run = "run-aos432"

	l, err := dono.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim do dono: %v", err)
	}
	_, err = intruso.Claim(ctx, run)
	if !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("Claim do intruso = %v, quer ErrLeaseHeld na cadeia", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, `"replica-dona"`) {
		t.Fatalf("a recusa não nomeia o dono: %q", msg)
	}
	if !strings.Contains(msg, "token 1") {
		t.Fatalf("a recusa não nomeia o token do dono: %q", msg)
	}
	if !strings.Contains(msg, l.ExpiresAt.UTC().Format("2006-01-02T15:04:05")) {
		t.Fatalf("a recusa não diz até quando o lease é válido (%s): %q", l.ExpiresAt.UTC(), msg)
	}
}
