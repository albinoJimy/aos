package broker

import (
	"context"
	"testing"
	"time"
)

// TestAOS478_TrocaENegacaoLevamOPrincipal (AOS-478): `credential.exchange.issued` e
// `credential.exchange.denied` levam no envelope o principal da troca.
func TestAOS478_TrocaENegacaoLevamOPrincipal(t *testing.T) {
	st := newStack(t, time.Minute)
	if _, err := st.broker.Exchange(context.Background(), request("run-478", provInScopeCap)); err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	b, es := semGateStack(t, enforcedClassProviders())
	if _, err := b.Exchange(context.Background(), requestForProvider("run-478-neg", providerOther, provInScopeCap)); err == nil {
		t.Fatal("a troca fora da autoridade devia ser negada")
	}
	vistos := 0
	for _, ev := range append(readStream(t, st.es, "run-478"), readStream(t, es, "run-478-neg")...) {
		if ev.Type != exchangeEventType && ev.Type != exchangeDeniedEventType {
			continue
		}
		vistos++
		if ev.Producer.NHIID != nhiID {
			t.Errorf("%s producer=%+v, quer %q", ev.Type, ev.Producer, nhiID)
		}
	}
	if vistos != 2 {
		t.Fatalf("eventos de troca vistos=%d, quer 2 (issued + denied)", vistos)
	}
}
