package durable

import (
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// AOS-436 (hipótese H-a da segunda revisão adversarial) — INDISPONÍVEL não é APAGADO.
//
// O [StepLedger.Rebuild] salta um passo cifrado cujo conteúdo não abre: é o comportamento certo
// depois de um crypto-shred (o run está a ser apagado; a re-execução é irrelevante). Mas o portão da
// custódia fechado — ou um Vault sem resposta — também faz o conteúdo não abrir, e aí saltar o passo
// é tirá-lo do ledger: a retoma re-executa um efeito externo JÁ aplicado (ADR-015). Com
// [ErrConteudoIndisponivel] o Rebuild falha fechado; com um apagamento continua a saltar.

// cipherIndisponivel é o double de um cifrador cuja custódia está fechada: sela, e não abre.
type cipherIndisponivel struct {
	*fakeCipher
	fechado bool
}

func (c *cipherIndisponivel) OpenContent(ctx context.Context, subject string, sealed []byte) ([]byte, error) {
	if c.fechado {
		return nil, ErrConteudoIndisponivel
	}
	return c.fakeCipher.OpenContent(ctx, subject, sealed)
}

func TestAOS436_RebuildFalhaFechadoComConteudoIndisponivel(t *testing.T) {
	ctx := context.Background()
	const subject = "nhi:agent-h-a"
	store := newStore(t)
	cipher := &cipherIndisponivel{fakeCipher: newFakeCipher()}
	producer := eventstore.Producer{NHIID: subject}
	ledger, err := NewStepLedger(store, WithProducer(producer), WithContentSealer(cipher))
	if err != nil {
		t.Fatal(err)
	}
	const key = "run-h-a:step-1"
	if _, applied, err := ledger.Apply(ctx, key, func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte("efeito externo aplicado")}, nil
	}); err != nil || !applied {
		t.Fatalf("Apply: applied=%v err=%v", applied, err)
	}

	// A custódia fecha: o Rebuild NÃO pode esquecer o passo.
	cipher.fechado = true
	ledger2, _ := NewStepLedger(store, WithProducer(producer), WithContentSealer(cipher))
	if err := ledger2.Rebuild(ctx, "run-h-a"); !errors.Is(err, ErrConteudoIndisponivel) {
		t.Fatalf("com o conteudo INDISPONIVEL o Rebuild devia falhar fechado; veio %v — um passo ja "+
			"aplicado desaparecia do ledger e a retoma re-executava o efeito externo", err)
	}

	// CONTROLO: um APAGAMENTO continua a saltar o passo, sem erro.
	cipher.fechado = false
	cipher.shred(subject)
	ledger3, _ := NewStepLedger(store, WithProducer(producer), WithContentSealer(cipher))
	if err := ledger3.Rebuild(ctx, "run-h-a"); err != nil {
		t.Fatalf("depois do crypto-shred o Rebuild salta o passo sem erro (AOS-093); veio %v", err)
	}
	if _, ok := ledger3.Applied(key); ok {
		t.Error("depois do crypto-shred o passo nao devia ser re-hidratado")
	}
}
