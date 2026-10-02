package durable

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// aos478_producer_test.go — AOS-478: o `producer` do envelope dos eventos do substrato
// durável. Os de ciclo de vida (lease, checkpoint) levam a identidade de componente; o
// `step.ledger.applied` leva o principal de quem causou o efeito, SEM mexer no titular da
// cifra — que continua a ser o do contexto.

// TestAOS478_LedgerProducerDoEfeitoNaoMudaOTitular: com a cifra por-titular composta e o
// modo estrito ligado (a configuração do nó), o envelope leva o principal declarado por
// [WithEffectProducer] e o payload continua selado sob o titular do CONTEXTO.
func TestAOS478_LedgerProducerDoEfeitoNaoMudaOTitular(t *testing.T) {
	ctx := ContextWithTitular(context.Background(), "titular-ana")
	store := newStore(t)
	l, err := NewStepLedger(store, WithContentSealer(newFakeCipher()), WithRequireTitular())
	if err != nil {
		t.Fatal(err)
	}
	autor := eventstore.Producer{
		NHIID:           "agt-1",
		DelegationChain: []eventstore.DelegationHop{{Sub: "human:ana", ActAs: "agt-1"}},
	}
	key, _ := IdempotencyKey("run-478", "s1")
	var lido eventstore.Producer
	_, applied, err := l.Apply(ctx, key, func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte(`{"x":1}`)}, nil
	}, WithEffectProducer(func() eventstore.Producer { lido = autor; return autor }))
	if err != nil || !applied {
		t.Fatalf("Apply: applied=%t err=%v", applied, err)
	}
	if lido.NHIID == "" {
		t.Fatal("o ledger não leu o produtor do efeito")
	}
	evs, err := store.Read(context.Background(), "run-478", 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("Read: err=%v n=%d", err, len(evs))
	}
	if evs[0].Producer.NHIID != "agt-1" || len(evs[0].Producer.DelegationChain) != 1 {
		t.Fatalf("producer=%+v, quer o autor do efeito", evs[0].Producer)
	}
	var rec struct {
		Sealed  bool   `json:"sealed"`
		Subject string `json:"subject"`
	}
	if err := json.Unmarshal(evs[0].Payload, &rec); err != nil {
		t.Fatal(err)
	}
	if !rec.Sealed || rec.Subject != "titular-ana" {
		t.Fatalf("o titular da cifra mudou: %+v (quer selado sob titular-ana)", rec)
	}
}

// TestAOS478_LedgerSemTitularContinuaARecusar: o produtor do efeito NÃO é fallback do
// titular — sem [ContextWithTitular], o modo estrito recusa como antes, mesmo com um autor
// declarado. É o que impede este ticket de reabrir a degradação silenciosa do AOS-245.
func TestAOS478_LedgerSemTitularContinuaARecusar(t *testing.T) {
	l, err := NewStepLedger(newStore(t), WithContentSealer(newFakeCipher()), WithRequireTitular())
	if err != nil {
		t.Fatal(err)
	}
	key, _ := IdempotencyKey("run-478", "s2")
	_, _, err = l.Apply(context.Background(), key, func(context.Context) (Result, error) {
		return Result{Status: "ok", Payload: []byte("y")}, nil
	}, WithEffectProducer(func() eventstore.Producer { return eventstore.Producer{NHIID: "agt-1"} }))
	if !errors.Is(err, ErrNoTitular) {
		t.Fatalf("err=%v, quer ErrNoTitular", err)
	}
}

// TestAOS478_LeaseECheckpointLevamIdentidadeDeComponente: os defaults nunca deixam o
// envelope vazio.
func TestAOS478_LeaseECheckpointLevamIdentidadeDeComponente(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	lm, err := NewLeaseManager(store, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lm.Claim(ctx, "run-478"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	cp, err := NewCheckpointer(store)
	if err != nil {
		t.Fatal(err)
	}
	if cp.producer.NHIID != DefaultCheckpointProducerNHI {
		t.Fatalf("checkpointer producer=%q, quer %q", cp.producer.NHIID, DefaultCheckpointProducerNHI)
	}
	streams, err := store.Streams()
	if err != nil {
		t.Fatal(err)
	}
	var vistos int
	for _, s := range streams {
		evs, _ := store.Read(ctx, s, 1)
		for _, ev := range evs {
			if ev.Type == EventTypeLeaseClaimed {
				vistos++
				if ev.Producer.NHIID != DefaultLeaseProducerNHI {
					t.Fatalf("lease.claimed producer=%q, quer %q", ev.Producer.NHIID, DefaultLeaseProducerNHI)
				}
			}
		}
	}
	if vistos != 1 {
		t.Fatalf("lease.claimed vistos=%d", vistos)
	}
}
