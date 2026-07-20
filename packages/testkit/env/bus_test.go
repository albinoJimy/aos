package env_test

import (
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/testkit/env"
)

// waitFor aguarda (até 2s) que uma condição se verifique. Substitui um sleep fixo
// por espera activa — determinista, sem flakiness no push assíncrono.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout a aguardar a condicao")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestBus_PushDeliversToSubscriber cobre o transporte push: um subscritor
// registado no bus recebe os eventos committed (event-driven, não polling).
func TestBus_PushDeliversToSubscriber(t *testing.T) {
	t.Parallel()
	e := env.New(t, env.WithBus())

	var mu sync.Mutex
	var seen []string
	_, err := e.Bus.Subscribe(eventstore.Filter{Types: []string{"turn.recorded"}}, func(ev eventstore.Event) {
		mu.Lock()
		seen = append(seen, ev.Type)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	e.SeedTrajectory("run-push", 3) // 3 turn.recorded + 3 replay.captured

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) >= 3
	})
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 3 {
		t.Fatalf("o subscritor filtrado devia ver 3 turn.recorded, viu %d", len(seen))
	}
}

// TestBus_TapCapturesAll confirma que o TAP interno captura TODOS os eventos
// (sem filtro), para asserção de entrega.
func TestBus_TapCapturesAll(t *testing.T) {
	t.Parallel()
	e := env.New(t, env.WithBus())
	e.SeedEvent("s", "a.b", 1, nil)
	e.SeedEvent("s", "c.d", 2, nil)

	waitFor(t, func() bool { return e.Bus.Count() >= 2 })
	if got := e.Bus.Count(); got != 2 {
		t.Fatalf("TAP devia capturar 2, capturou %d", got)
	}
	if len(e.Bus.Received()) != 2 {
		t.Fatalf("Received devia devolver 2 eventos")
	}
}

// TestBus_SubscribeAfterClose confirma que subscrever depois do teardown falha
// (o Store está fechado) — sem criar uma goroutine órfã.
func TestBus_SubscribeAfterClose(t *testing.T) {
	t.Parallel()
	e := env.New(t, env.WithBus())
	e.Teardown()

	if _, err := e.Bus.Subscribe(eventstore.Filter{}, func(eventstore.Event) {}); err == nil {
		t.Fatal("Subscribe apos teardown devia falhar (Store fechado)")
	}
}
