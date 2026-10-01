package main

// aos360_desligado_traduzido_test.go — os dois consumidores do nó que ramificam no sentinela de
// indisponibilidade, exercitados com a forma que o substrato REPLICADO produz (AOS-360).
//
// O AOS-354 fez o backend JetStream traduzir a desligação para [eventstore.ErrNoQuorum], e os
// testes do nó continuaram a alimentar os consumidores com o sentinela CRU. Um consumidor que
// comparasse por igualdade (`err == eventstore.ErrNoQuorum`) passava nesses testes e falhava
// exactamente no substrato que o AOS-354 veio corrigir. A forma traduzida tem de ser a que se
// testa.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// erroDesligadoTraduzido é o erro que `jetstream.Store.Read` devolve com o cliente sem socket:
// a causa do caminho de leitura, embrulhada no sentinela canónico. A forma vem de
// `indisponibilidadeTransitoria` (`substrate/eventstore/jetstream/store.go`), e o
// `TestAOS360_AsQuatroPortasTraduzemADesligacao` desse pacote prova que é ela que sai das
// quatro portas.
var erroDesligadoTraduzido = fmt.Errorf("%w: %w", eventstore.ErrNoQuorum,
	fmt.Errorf("jetstream: leitura de %q: %w", "aos.es.run-1", natsjs.ErrDesligado))

// TestAOS360_ODesligadoTraduzidoETransitorioNoBurndown — o burn-down tolera a desligação do
// substrato replicado como tolera a perda de quórum, e ao passar da tolerância o erro que mata
// o run continua a nomear as DUAS causas.
func TestAOS360_ODesligadoTraduzidoETransitorioNoBurndown(t *testing.T) {
	h := novoAOS262HarnessComStore(t, 1000, 0.80, func(inner turnLedgerStore) turnLedgerStore {
		return &storeInstavel{inner: inner, falhas: maxLeiturasTransitoriasToleradas + 1, erro: erroDesligadoTraduzido}
	})
	ctx := context.Background()
	const run = "run-360-desligado"
	gravaTurno(t, h.rec, run, "step-1", 1, 150, 50, 0)

	for turno := 1; turno <= maxLeiturasTransitoriasToleradas; turno++ {
		if err := h.prog.ObserveProgress(ctx, run, turno); err != nil {
			t.Fatalf("a %da leitura com o substrato replicado desligado NAO pode matar o run — e o defeito "+
				"de AOS-354, que sobre JetStream matava a primeira: %v", turno, err)
		}
	}
	err := h.prog.ObserveProgress(ctx, run, maxLeiturasTransitoriasToleradas+1)
	if err == nil {
		t.Fatal("passada a tolerancia a desligacao deixa de ser transitoria: o run tem de abortar")
	}
	if !errors.Is(err, eventstore.ErrNoQuorum) || !errors.Is(err, natsjs.ErrDesligado) {
		t.Fatalf("o erro fatal tem de nomear o sentinela canonico E a causa do substrato: %v", err)
	}
}

// TestAOS360_StreamSetupErrorStatus — o mapa de erro do setup de um stream SSE, que até aqui não
// era referido por teste nenhum: o AC3 do AOS-354 foi dado por cumprido só pelo código.
func TestAOS360_StreamSetupErrorStatus(t *testing.T) {
	casos := []struct {
		nome string
		err  error
		quer int
	}{
		{"store fechado", eventstore.ErrClosed, http.StatusServiceUnavailable},
		{"store fechado, embrulhado", fmt.Errorf("subscribe: %w", eventstore.ErrClosed), http.StatusServiceUnavailable},
		{"sem quorum, cru (store de referencia)", eventstore.ErrNoQuorum, http.StatusServiceUnavailable},
		{"desligado, traduzido (JetStream)", erroDesligadoTraduzido, http.StatusServiceUnavailable},
		// O que o AOS-354 corrigiu: o backend deixou de devolver isto CRU. Se voltar a devolver,
		// o mapa não o reconhece — e é o teste das quatro portas no `jetstream` que avermelha.
		{"desligado, cru (sem a traducao do backend)", natsjs.ErrDesligado, http.StatusInternalServerError},
		{"outro erro", errors.New("payload ilegivel"), http.StatusInternalServerError},
	}
	for _, c := range casos {
		if got := streamSetupErrorStatus(c.err); got != c.quer {
			t.Errorf("%s: streamSetupErrorStatus = %d, quero %d", c.nome, got, c.quer)
		}
	}
}
