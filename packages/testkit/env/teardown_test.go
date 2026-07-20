package env_test

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/testkit/env"
)

// fakeTB implementa [env.TB] capturando os cleanups registados por env.New em vez
// de os deixar à mercê do runtime de testes. Permite provar directamente a
// GARANTIA de teardown em falha: registamos as deps, simulamos uma falha a meio
// (não corremos o resto do teste) e disparamos os cleanups na MESMA ordem LIFO
// que o testing.T usa — exactamente o que o framework faria após um t.Fatal ou um
// panic recuperado. Assim o teste-pai não precisa de falhar para exercitar a via.
type fakeTB struct {
	cleanups []func()
	fatal    bool
	fatalMsg string
}

func (f *fakeTB) Helper() {}

func (f *fakeTB) Cleanup(fn func()) { f.cleanups = append(f.cleanups, fn) }

func (f *fakeTB) Fatalf(format string, args ...any) {
	f.fatal = true
	f.fatalMsg = fmt.Sprintf(format, args...)
}

// runCleanups dispara os cleanups em ordem LIFO — a mesma semântica de
// testing.common.runCleanup, que corre SEMPRE (sucesso, t.Fatal ou panic).
func (f *fakeTB) runCleanups() {
	for i := len(f.cleanups) - 1; i >= 0; i-- {
		f.cleanups[i]()
	}
}

// waitGoroutines espera (até d) que a contagem de goroutines desça a <= target e
// devolve a contagem final. Substitui um goleak externo por um check in-process
// (zero-dep): as goroutines de subscrição push do Env têm de fechar no teardown.
func waitGoroutines(target int, d time.Duration) int {
	deadline := time.Now().Add(d)
	for {
		runtime.GC()
		n := runtime.NumGoroutine()
		if n <= target || time.Now().After(deadline) {
			return n
		}
		time.Sleep(time.Millisecond)
	}
}

// TestTeardown_GuaranteedOnFailure cobre AC3: mesmo quando o teste FALHA a meio, o
// teardown registado em Cleanup corre — fecha o Store e cancela as subscrições
// push, sem deixar recursos órfãos. Simula-se a falha com um [fakeTB]: provisiona,
// semeia, interrompe (sem correr o resto) e dispara os cleanups como o framework
// faria após um t.Fatal.
func TestTeardown_GuaranteedOnFailure(t *testing.T) {
	base := waitGoroutines(0, 0) // baseline de goroutines antes do Env

	ftb := &fakeTB{}
	e := env.New(ftb, env.WithEventStore(), env.WithBus(), env.WithVault())
	e.SeedTrajectory("run-teardown", 4) // trajectória em curso...
	// Subscrições push adicionais vivas no momento da falha (goroutines a fechar).
	if _, err := e.Bus.Subscribe(eventstore.Filter{}, func(eventstore.Event) {}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if ftb.fatal {
		t.Fatalf("provisionamento falhou inesperadamente: %s", ftb.fatalMsg)
	}

	// >>> FALHA A MEIO <<< : não corremos o resto. O framework, após um t.Fatal,
	// dispara os cleanups — é o que fazemos aqui.
	ftb.runCleanups()

	// O Store TEM de estar fechado: um segundo Close (idempotente) devolve ErrClosed.
	if err := e.EventStore.Close(); err != eventstore.ErrClosed {
		t.Fatalf("teardown nao fechou o Store em falha: Close() = %v (esperava ErrClosed)", err)
	}
	// Ler o Store fechado tem de falhar (sem handle órfão a servir leituras).
	if _, err := e.EventStore.Read(context.Background(), "run-teardown", 1); err != eventstore.ErrClosed {
		t.Fatalf("Store devia estar fechado, Read = %v", err)
	}
	// Nenhuma goroutine de subscrição pendurada: a contagem volta ao baseline.
	if n := waitGoroutines(base, 2*time.Second); n > base {
		t.Fatalf("goroutines orfas apos teardown em falha: base=%d agora=%d", base, n)
	}
}

// TestTeardown_GuaranteedViaRealCleanup complementa a via anterior exercitando o
// t.Cleanup REAL de um subteste que termina normalmente: ao sair do t.Run os
// cleanups do subteste correm e o Store fica fechado — prova que a integração com
// o runtime de testes (não só o fakeTB) liberta os recursos.
func TestTeardown_GuaranteedViaRealCleanup(t *testing.T) {
	base := waitGoroutines(0, 0)
	var captured *env.EphemeralEnv
	t.Run("inner", func(st *testing.T) {
		e := env.New(st, env.WithEventStore(), env.WithBus())
		captured = e
		e.SeedTrajectory("run-real", 2)
	})
	// Fora do subteste: os seus t.Cleanup já correram.
	if err := captured.EventStore.Close(); err != eventstore.ErrClosed {
		t.Fatalf("t.Cleanup real nao fechou o Store: Close() = %v", err)
	}
	if n := waitGoroutines(base, 2*time.Second); n > base {
		t.Fatalf("goroutines orfas apos cleanup real: base=%d agora=%d", base, n)
	}
}

// TestTeardown_Idempotent cobre a idempotência do teardown (AC3): chamá-lo
// explicitamente múltiplas vezes NÃO entra em panic nem em erro (sync.Once).
func TestTeardown_Idempotent(t *testing.T) {
	t.Parallel()
	e := env.New(t, env.WithEventStore(), env.WithBus(), env.WithVault())
	e.SeedTrajectory("run-idem", 1)

	e.Teardown()
	e.Teardown() // 2ª chamada à mão: continua sem panic
	// (o t.Cleanup registado em New fará a 3ª chamada no fim; sync.Once garante no-op)

	if err := e.EventStore.Close(); err != eventstore.ErrClosed {
		t.Fatalf("apos Teardown o Store devia estar fechado: %v", err)
	}
}

// TestTeardown_NoOrphansHappyPath confirma o caminho feliz: sem falha, o teardown
// também não deixa goroutines órfãs (baseline restaurado).
func TestTeardown_NoOrphansHappyPath(t *testing.T) {
	base := waitGoroutines(0, 0)
	func() {
		e := env.New(t, env.WithEventStore(), env.WithBus())
		e.SeedTrajectory("run-happy", 3)
		if _, err := e.Bus.Subscribe(eventstore.Filter{Types: []string{"turn.recorded"}}, func(eventstore.Event) {}); err != nil {
			t.Fatalf("Subscribe: %v", err)
		}
		e.Teardown()
	}()
	if n := waitGoroutines(base, 2*time.Second); n > base {
		t.Fatalf("goroutines orfas no caminho feliz: base=%d agora=%d", base, n)
	}
}
