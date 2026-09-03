package main

// AOS-290 no NÓ — o fluxo DSAR real alcança a projecção em memória do step-ledger.
//
// O `durable` já tem os testes de unidade do apagamento por titular e da poda. O que ELES não
// conseguem provar é a CABLAGEM: que o `dsar.Flow` do nó recebeu o ledger como store. Foi
// exactamente essa a forma do defeito original — o mecanismo de cifra por-titular existia e
// funcionava, e o que faltava era o mapa em memória estar ligado ao apagamento. Um teste que
// prove só o mecanismo deixa o defeito passar outra vez.
//
// Segue o molde de `TestOFluxoDoNoRECEBEUOConfirmador` (shred_restauro_test.go), que existe
// pela mesma razão: verificar que o composition-root ligou o que diz ter ligado.

import (
	"context"
	"testing"

	dsar "github.com/aos-ref/control-plane/governance/dsar"
	durable "github.com/aos-ref/kernel/agent-runtime/durable"
)

// TestAOS290_OFluxoDoNoAPAGAOLedgerEmMemoria prova que `node.DSAR.Receive` — o caminho real do
// Art. 17 — remove o texto claro que o step-ledger do nó retinha.
//
// Sem a linha em `bootstrap.go` que acrescenta [dsar.StepLedgerStore] à lista de stores, o
// `Applied` continua a devolver o payload depois de a KEK ter sido destruída: o apagamento
// real no disco e ficção em memória, que é a medição da auditoria.
func TestAOS290_OFluxoDoNoAPAGAOLedgerEmMemoria(t *testing.T) {
	ctx := context.Background()
	node, _ := newDurableNode(t)

	if node.Ledger == nil {
		t.Fatal("premissa: o no com execucao duravel tem de compor o step-ledger")
	}

	const subject = "nhi:titular-aos290-no"
	const runID = "run-aos290-no"
	const claro = "resultado-sintetico: SUJEITO-AOS290 caso SYNTH-290"

	// O titular chega ao ledger pelo CONTEXTO — é o caminho de produção (o dispatcher durável
	// faz `durable.ContextWithTitular(ctx, act.Principal.NHIID)`), e não o fallback do produtor.
	ctxTitular := durable.ContextWithTitular(ctx, subject)
	key, err := durable.IdempotencyKey(runID, "step-000001")
	if err != nil {
		t.Fatalf("IdempotencyKey: %v", err)
	}
	if _, _, err := node.Ledger.Apply(ctxTitular, key, func(context.Context) (durable.Result, error) {
		return durable.Result{Status: "ok", Payload: []byte(claro)}, nil
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// PREMISSA: antes do apagamento o ledger devolve o claro. Sem esta metade, um ledger que
	// nunca tivesse guardado nada passaria a asserção de baixo.
	if res, ok := node.Ledger.Applied(key); !ok || string(res.Payload) != claro {
		t.Fatalf("premissa: o ledger devia ter o claro antes do apagamento; ok=%v payload=%q", ok, res.Payload)
	}

	res, err := node.DSAR.Receive(ctx, dsar.Request{RequestID: "req-aos290", SubjectID: subject})
	if err != nil {
		t.Fatalf("DSAR.Receive: %v", err)
	}
	if res.Partial {
		t.Fatalf("erasure PARCIAL sem legal hold: %+v", res)
	}

	if res, ok := node.Ledger.Applied(key); ok {
		t.Fatalf("apos o DSAR do no, o step-ledger AINDA devolve o claro: payload=%q — o ledger nao esta na lista de stores do fluxo", res.Payload)
	}
}

// TestAOS290_OFluxoDoNoNaoApagaOutroTitular fixa o isolamento no caminho real. Um store de
// apagamento que levasse tudo à frente seria um defeito maior do que o que corrige — e este é
// o nível onde isso se veria, porque o fluxo é o mesmo para todos os titulares.
func TestAOS290_OFluxoDoNoNaoApagaOutroTitular(t *testing.T) {
	ctx := context.Background()
	node, _ := newDurableNode(t)

	aplica := func(subject, runID, payload string) string {
		t.Helper()
		key, err := durable.IdempotencyKey(runID, "step-000001")
		if err != nil {
			t.Fatalf("IdempotencyKey: %v", err)
		}
		if _, _, err := node.Ledger.Apply(durable.ContextWithTitular(ctx, subject), key,
			func(context.Context) (durable.Result, error) {
				return durable.Result{Status: "ok", Payload: []byte(payload)}, nil
			}); err != nil {
			t.Fatalf("Apply(%s): %v", runID, err)
		}
		return key
	}

	keyA := aplica("nhi:aos290-A", "run-aos290-A", "claro-de-A")
	keyB := aplica("nhi:aos290-B", "run-aos290-B", "claro-de-B")

	if _, err := node.DSAR.Receive(ctx, dsar.Request{RequestID: "req-A", SubjectID: "nhi:aos290-A"}); err != nil {
		t.Fatalf("DSAR.Receive(A): %v", err)
	}

	if _, ok := node.Ledger.Applied(keyA); ok {
		t.Fatal("A devia ter sido apagado do ledger")
	}
	if res, ok := node.Ledger.Applied(keyB); !ok || string(res.Payload) != "claro-de-B" {
		t.Fatalf("o apagamento de A levou B a frente; ok=%v payload=%q", ok, res.Payload)
	}
}
