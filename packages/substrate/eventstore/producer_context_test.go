package eventstore

import (
	"context"
	"testing"
)

// TestAOS478_ProducerNoContexto: o producer anexado volta igual, é uma CÓPIA (mutar o
// original não o altera), e um contexto sem ele devolve o valor zero.
func TestAOS478_ProducerNoContexto(t *testing.T) {
	p := Producer{NHIID: "agt-1", DelegationChain: []DelegationHop{{Sub: "human:ana", ActAs: "agt-1"}}, Scope: []string{"cap:x"}}
	ctx := ContextWithProducer(context.Background(), p)
	p.DelegationChain[0].Sub = "human:outro"
	p.Scope[0] = "cap:y"
	got := ProducerFromContext(ctx)
	if got.NHIID != "agt-1" || got.DelegationChain[0].Sub != "human:ana" || got.Scope[0] != "cap:x" {
		t.Fatalf("producer partilhado com o chamador: %+v", got)
	}
	got.Scope[0] = "cap:z"
	if again := ProducerFromContext(ctx); again.Scope[0] != "cap:x" {
		t.Fatalf("a leitura devolve o estado guardado por referência: %+v", again)
	}
	if z := ProducerFromContext(context.Background()); z.NHIID != "" || z.DelegationChain != nil || z.Scope != nil {
		t.Fatalf("sem producer devia ser o valor zero: %+v", z)
	}
}
