package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	// Um resultado vazio lê-se vazio e sem erro: o ledger não o sela, e não é conteúdo.
	if got, err := ReadAppliedResult(ctx, store, cipher, runID, "step-000001-tool-3"); err != nil || len(got) != 0 {
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
	// SEM OPENER NÃO SE LÊ NADA (revisão, M2): nem o registo selado (nunca o ciphertext), nem o
	// vazio, nem um passo que não existe — a recusa é anterior à leitura do log.
	for _, passo := range []string{"step-000001-tool-2", "step-000001-tool-3", "step-000009-tool-1"} {
		if got, err := ReadAppliedResult(ctx, store, nil, runID, passo); !errors.Is(err, ErrSealedResultNoCipher) || got != nil {
			t.Fatalf("%s sem opener = %q, %v; quero ErrSealedResultNoCipher e nada", passo, got, err)
		}
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

// aos498RegistoAMao escreve um `step.ledger.applied` com o payload dado, sem passar pelo ledger: o
// que um log de outra composição, ou danificado, tem.
func aos498RegistoAMao(t *testing.T, store *eventstore.Store, stream, passoDoEvento string, payload []byte) {
	t.Helper()
	if _, err := store.Append(context.Background(), stream, eventstore.EventInput{
		Type: EventTypeLedgerApplied, Payload: payload, RunID: stream, StepID: passoDoEvento,
		Producer: eventstore.Producer{NHIID: "nhi:titular-498"},
	}); err != nil {
		t.Fatalf("Append(%s/%s): %v", stream, passoDoEvento, err)
	}
}

// TestAOS498_ReadAppliedResult_AChaveEInteira (mutação R2 da revisão): a chave do registo
// compara-se INTEIRA, run e passo. Um registo de OUTRO run escrito no stream deste, com o mesmo
// passo, não é o resultado pedido — nem quando é o único do stream.
func TestAOS498_ReadAppliedResult_AChaveEInteira(t *testing.T) {
	ctx := context.Background()
	const passo = "step-000001-tool-1"
	cipher := newFakeCipher()
	selado, err := cipher.SealContent(ctx, "nhi:titular-498", "", []byte("documento de OUTRO run"))
	if err != nil {
		t.Fatalf("SealContent: %v", err)
	}
	registo := func(chave string) []byte {
		cru, err := json.Marshal(map[string]any{"key": chave, "status": "ok", "result": selado, "sealed": true, "subject": "nhi:titular-498"})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		return cru
	}
	store := newStore(t)
	aos498RegistoAMao(t, store, "run-A", "ledger-"+passo, registo("run-B:"+passo))
	if got, err := ReadAppliedResult(ctx, store, cipher, "run-A", passo); !errors.Is(err, ErrAppliedResultNotFound) || got != nil {
		t.Fatalf("registo de run-B no stream de run-A = %q, %v; quero ErrAppliedResultNotFound e nada", got, err)
	}
	// Com o registo certo ao lado, sai o certo.
	certo, err := cipher.SealContent(ctx, "nhi:titular-498", "", []byte("documento de run-A"))
	if err != nil {
		t.Fatalf("SealContent: %v", err)
	}
	selado = certo
	aos498RegistoAMao(t, store, "run-A", "ledger-"+passo+"-certo", registo("run-A:"+passo))
	selado, _ = cipher.SealContent(ctx, "nhi:titular-498", "", []byte("documento de OUTRO run, depois"))
	aos498RegistoAMao(t, store, "run-A", "ledger-"+passo+"-depois", registo("run-B:"+passo))
	if got, err := ReadAppliedResult(ctx, store, cipher, "run-A", passo); err != nil || string(got) != "documento de run-A" {
		t.Fatalf("ReadAppliedResult = %q, %v; quero o registo de run-A", got, err)
	}
}

// TestAOS498_ReadAppliedResult_IlegivelEDefinitivo (revisão, M1): um registo do ledger que não
// descodifica — em QUALQUER passo do stream — e um passo que não forma uma chave de idempotência
// dão [ErrAppliedResultUnreadable], distinto de «não há registo» e de um erro de leitura do log.
func TestAOS498_ReadAppliedResult_IlegivelEDefinitivo(t *testing.T) {
	ctx := context.Background()
	const (
		subject = "nhi:titular-498"
		runID   = "run-498-ilegivel"
		passo   = "step-000001-tool-1"
	)
	cipher := newFakeCipher()
	store := newStore(t)
	ledger, err := NewStepLedger(store, WithProducer(eventstore.Producer{NHIID: subject}), WithContentSealer(cipher))
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	if _, _, err := ledger.Apply(ctx, runID+":"+passo, func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte("documento")}, nil
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// O passo que não forma chave: decide-se antes de ler, com o log inteiro.
	for _, mau := range []string{"x:" + passo, ""} {
		_, err := ReadAppliedResult(ctx, store, cipher, runID, mau)
		if !errors.Is(err, ErrAppliedResultUnreadable) || errors.Is(err, ErrAppliedResultNotFound) {
			t.Fatalf("passo %q: erro = %v; quero ErrAppliedResultUnreadable", mau, err)
		}
	}
	if _, err := ReadAppliedResult(ctx, store, cipher, runID, "x:"+passo); !errors.Is(err, ErrDelimiterInInput) {
		t.Fatalf("a causa (ErrDelimiterInInput) tem de ir embrulhada ao lado; veio %v", err)
	}
	// Um registo de OUTRO passo que não descodifica (um objecto onde a chave devia ser texto).
	aos498RegistoAMao(t, store, runID, "ledger-partido", []byte(`{"key":{"nao":"e texto"}}`))
	got, err := ReadAppliedResult(ctx, store, cipher, runID, passo)
	if !errors.Is(err, ErrAppliedResultUnreadable) || got != nil {
		t.Fatalf("stream com um registo ilegivel = %q, %v; quero ErrAppliedResultUnreadable e nada", got, err)
	}
	if strings.Contains(err.Error(), "documento") {
		t.Fatalf("o erro leva conteudo: %v", err)
	}
}

// TestAOS498_ReadAppliedResult_EmClaroNaoSai (revisão, M2): um registo com conteúdo que não está
// selado por-titular não é devolvido — com ou sem opener, e com o opener sem nunca ser chamado.
// O opener é a única porta por onde sai conteúdo desta função.
func TestAOS498_ReadAppliedResult_EmClaroNaoSai(t *testing.T) {
	ctx := context.Background()
	const (
		runID = "run-498-em-claro"
		passo = "step-000001-tool-1"
	)
	store := newStore(t)
	// Um ledger SEM cifra escreve o resultado em claro.
	ledger, err := NewStepLedger(store)
	if err != nil {
		t.Fatalf("NewStepLedger: %v", err)
	}
	if _, _, err := ledger.Apply(ctx, runID+":"+passo, func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte("documento em claro")}, nil
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	cipher := newFakeCipher()
	got, err := ReadAppliedResult(ctx, store, cipher, runID, passo)
	if !errors.Is(err, ErrAppliedResultInClear) || got != nil {
		t.Fatalf("registo em claro = %q, %v; quero ErrAppliedResultInClear e nada", got, err)
	}
	if got, err := ReadAppliedResult(ctx, store, nil, runID, passo); !errors.Is(err, ErrSealedResultNoCipher) || got != nil {
		t.Fatalf("registo em claro sem opener = %q, %v; quero ErrSealedResultNoCipher e nada", got, err)
	}
}
