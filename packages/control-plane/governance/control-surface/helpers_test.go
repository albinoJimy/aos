package controlsurface_test

import (
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// Identidades de teste. O emissor NÃO registado e o autenticador HMAC saíram daqui com
// a ControlSurface (AOS-292 AC4): quem prova hoje a recusa de um sinal não autenticado é
// control.TestUnauthenticatedSteerRejected, ao nível do canal — que é o nível que corre
// em produção, e cobre mais casos (assinatura ausente, forjada, emissor desconhecido e
// replay cross-run) do que os testes que aqui existiam através da superfície.
const (
	testEmitter = "operator-42"
	testRunID   = "run-119-abc"
)

func newStore(t testing.TB) *eventstore.Store {
	t.Helper()
	st, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}
