package sandbox

import (
	"context"
	"testing"

	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/substrate/eventstore"
)

// TestAOS478_CicloDeVidaLevaOPrincipalDaToolCall: o EventStoreSink grava no envelope o
// principal que o RM anexou ao contexto do despacho; sem ele, o envelope fica vazio — e é
// o teste do nó (cmd/aos, TestAOS478_ProducerPorFamilia) que garante que o despacho real o
// anexa sempre.
func TestAOS478_CicloDeVidaLevaOPrincipalDaToolCall(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatal(err)
	}
	sink := NewEventStoreSink(es)
	p := referencemonitor.Principal{
		NHIID: "agt-1", Authority: []string{"cap:fs.read"},
		DelegationChain: []referencemonitor.DelegationHop{{Sub: "human:ana", ActAs: "agt-1"}},
	}
	ctx := referencemonitor.ContextWithMediatedPrincipal(context.Background(), p)
	if _, err := sink.RecordLifecycle(ctx, LifecycleEvent{RunID: "run-478", StepID: "s1", Phase: PhaseCreated}); err != nil {
		t.Fatal(err)
	}
	evs, err := es.Read(context.Background(), "run-478", 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("Read: err=%v n=%d", err, len(evs))
	}
	got := evs[0].Producer
	if got.NHIID != "agt-1" || len(got.DelegationChain) != 1 || got.DelegationChain[0].Sub != "human:ana" || len(got.Scope) != 1 {
		t.Fatalf("producer=%+v, quer o principal da tool call", got)
	}
}
