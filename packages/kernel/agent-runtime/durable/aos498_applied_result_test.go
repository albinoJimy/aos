package durable

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// AOS-498 — [ReadAppliedResult]: a leitura, do LOG, do resultado que o step-ledger gravou para um
// passo. É a fonte dos bytes da saída por referência (ADR-038 §2.3).

// leitorQueFalha devolve sempre o mesmo erro de leitura.
type leitorQueFalha struct{ err error }

func (l leitorQueFalha) Read(context.Context, string, uint64) ([]eventstore.Event, error) {
	return nil, l.err
}

func TestAOS498_ReadAppliedResult(t *testing.T) {
	ctx := context.Background()
	const (
		subject = "nhi:titular-498"
		runID   = "run-498"
		selado  = "documento do titular"
	)
	cipher := newFakeCipher()
	store := newStore(t)
	ledger, err := NewStepLedger(store, WithProducer(eventstore.Producer{NHIID: subject}), WithContentSealer(cipher))
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	aplicar := func(passo string, payload []byte) {
		t.Helper()
		if _, _, err := ledger.Apply(ctx, runID+":"+passo, func(context.Context) (Result, error) {
			return Result{Status: "ok", Payload: payload}, nil
		}); err != nil {
			t.Fatalf("Apply(%s): %v", passo, err)
		}
	}
	aplicar("step-000001-tool-1", []byte("outro passo"))
	aplicar("step-000001-tool-2", []byte(selado))
	aplicar("step-000001-tool-3", nil) // payload vazio: não é selado

	// O passo pedido, e só ele, decifrado.
	got, err := ReadAppliedResult(ctx, store, cipher, runID, "step-000001-tool-2")
	if err != nil || string(got) != selado {
		t.Fatalf("ReadAppliedResult = %q, %v; quero %q", got, err, selado)
	}
	// Um resultado vazio lê-se vazio, sem erro e sem opener.
	if got, err := ReadAppliedResult(ctx, store, nil, runID, "step-000001-tool-3"); err != nil || len(got) != 0 {
		t.Fatalf("resultado vazio = %q, %v; quero vazio e sem erro", got, err)
	}

	// A LEITURA NÃO TEM ESTADO: um ledger novo sobre o mesmo store continua sem nada em memória.
	outro, err := NewStepLedger(store, WithContentSealer(cipher))
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	if _, emMemoria := outro.Applied(runID + ":step-000001-tool-2"); emMemoria {
		t.Fatal("pre-condicao: um ledger novo nasce vazio")
	}
	antes, _ := store.Read(ctx, runID, 1)
	if _, err := ReadAppliedResult(ctx, store, cipher, runID, "step-000001-tool-2"); err != nil {
		t.Fatalf("segunda leitura: %v", err)
	}
	if depois, _ := store.Read(ctx, runID, 1); len(depois) != len(antes) {
		t.Fatalf("a leitura escreveu no Event Store: %d eventos antes, %d depois", len(antes), len(depois))
	}

	// Passo que o ledger não tem, e run que não existe: o mesmo erro definitivo.
	for nome, alvo := range map[string][2]string{
		"passo em falta": {runID, "step-000009-tool-1"},
		"run em falta":   {"run-que-nao-existe", "step-000001-tool-2"},
	} {
		if _, err := ReadAppliedResult(ctx, store, cipher, alvo[0], alvo[1]); !errors.Is(err, ErrAppliedResultNotFound) {
			t.Fatalf("%s: erro = %v; quero ErrAppliedResultNotFound", nome, err)
		}
	}
	// Registo selado sem opener: fail-closed, nunca o ciphertext.
	if got, err := ReadAppliedResult(ctx, store, nil, runID, "step-000001-tool-2"); !errors.Is(err, ErrSealedResultNoCipher) || got != nil {
		t.Fatalf("selado sem opener = %q, %v; quero ErrSealedResultNoCipher e nada", got, err)
	}
	// Erro de leitura do Event Store: sai tal qual (quem chama decide se é transitório).
	falha := errors.New("event store sem quorum (teste)")
	if _, err := ReadAppliedResult(ctx, leitorQueFalha{falha}, cipher, runID, "step-000001-tool-2"); !errors.Is(err, falha) {
		t.Fatalf("erro de leitura = %v; quero o do Event Store", err)
	}
	// KEK destruída: o erro do opener, tal qual, e nenhum byte.
	cipher.shred(subject)
	got, err = ReadAppliedResult(ctx, store, cipher, runID, "step-000001-tool-2")
	if !errors.Is(err, errShredded) || bytes.Contains(got, []byte(selado)) {
		t.Fatalf("depois do apagamento = %q, %v; quero o erro do opener e nada", got, err)
	}
}
