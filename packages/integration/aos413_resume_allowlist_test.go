package integration

// AOS-413 (ADR-027) — a lista-branca de tools de um run sobrevive à retoma. Sem isto, um run
// re-hospedado depois de um reinício do nó ganhava as tools de todo o token.

import (
	"context"
	"reflect"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

func TestAOS413_RetomaPreservaAListaBrancaDoRun(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	r, err := NewResumeRecords(es, nil)
	if err != nil {
		t.Fatalf("NewResumeRecords: %v", err)
	}
	ctx := context.Background()
	rec := sampleResume("run-413.n1")
	rec.AllowedTools = []string{"fs.read"}
	if err := r.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := r.Get(ctx, "run-413.n1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if goal := got.GoalWith("cred-fresca"); !reflect.DeepEqual(goal.AllowedTools, []string{"fs.read"}) {
		t.Fatalf("a retoma perdeu a lista-branca: %v", goal.AllowedTools)
	}
}

// A lista vazia (nega tudo) não pode voltar da retoma como nil (sem restrição).
func TestAOS413_RetomaDistingueListaVaziaDeAusente(t *testing.T) {
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	r, err := NewResumeRecords(es, nil)
	if err != nil {
		t.Fatalf("NewResumeRecords: %v", err)
	}
	ctx := context.Background()
	rec := sampleResume("run-413.n2")
	rec.AllowedTools = []string{}
	if err := r.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := r.Get(ctx, "run-413.n2")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if goal := got.GoalWith("cred"); goal.AllowedTools == nil || len(goal.AllowedTools) != 0 {
		t.Fatalf("a lista vazia voltou da retoma como %#v — nil abria todas as tools", goal.AllowedTools)
	}
}
