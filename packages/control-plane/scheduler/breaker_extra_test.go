package scheduler_test

// Testes complementares do breaker (AOS-029): resolução de limiares por
// especificidade, opção de nó de orçamento distinto do tree_id, opções/no-ops do
// parker e Reevaluate. Deterministas.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/budget"
	"github.com/aos-ref/control-plane/scheduler"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// A resolução de limiares segue a especificidade (classe,tenant) > classe > tenant >
// default.
func TestStaticThresholdProvider_Specificity(t *testing.T) {
	t.Parallel()
	def := scheduler.Thresholds{VelocityTokens: 1}
	cls := scheduler.Thresholds{VelocityTokens: 2}
	ten := scheduler.Thresholds{VelocityTokens: 3}
	pair := scheduler.Thresholds{VelocityTokens: 4}
	tp := scheduler.NewStaticThresholdProvider(def).
		SetClass("c", cls).
		SetTenant("t", ten).
		SetClassTenant("c", "t", pair)

	cases := []struct {
		class, tenant string
		want          int64
	}{
		{"c", "t", 4}, // par exacto
		{"c", "z", 2}, // classe
		{"x", "t", 3}, // tenant
		{"x", "z", 1}, // default
	}
	for _, c := range cases {
		if got := tp.Thresholds(c.class, c.tenant).VelocityTokens; got != c.want {
			t.Errorf("Thresholds(%q,%q).VelocityTokens = %d, quero %d", c.class, c.tenant, got, c.want)
		}
	}
}

// WithBreakerNode aponta o breaker a um nó de orçamento distinto do tree_id (ex.: a
// raiz da árvore com um id próprio), lido para o sinal de esgotamento.
func TestBreaker_NodeOptionReadsDistinctBudgetNode(t *testing.T) {
	t.Parallel()
	base := time.Unix(1_000_000, 0)
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	// Árvore com raiz "root" e um sub-nó "leaf"; o breaker observa "leaf".
	b, err := budget.New("root", budget.Amount{Tokens: 10_000, CostMicroUSD: 10_000})
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	if err := b.AddNode("leaf", "root", budget.Amount{Tokens: 1000, CostMicroUSD: 1000}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	br, err := scheduler.NewBreaker(es, b, "tree-node",
		scheduler.Thresholds{ExhaustionMargin: budget.Amount{Tokens: 100, CostMicroUSD: 100}},
		scheduler.WithBreakerNode("leaf"),
		scheduler.WithBreakerClock(fixedClock(base)),
		scheduler.WithBreakerProducer(eventstore.Producer{NHIID: "nhi:test"}),
	)
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}
	ctx := context.Background()

	// Esgota o sub-nó "leaf" até 50/50 (<= margem 100) ⇒ trip por esgotamento.
	r, _ := b.Reserve(ctx, "leaf", budget.Amount{Tokens: 950, CostMicroUSD: 950})
	if err := b.Commit(ctx, r); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	st, err := br.Observe(ctx, budget.Amount{})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if st != scheduler.BreakerOpen {
		t.Fatalf("estado = %s, quero open (esgotamento do nó leaf)", st)
	}
}

// O parker é no-op quando resolve é nil ou a árvore não bate; a opção de motivo é
// aplicada na transição de paragem.
func TestMachineParker_NoopAndReason(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// resolve nil ⇒ ParkTree é no-op sem erro.
	pNil := scheduler.NewMachineParker(nil)
	if err := pNil.ParkTree(ctx, "whatever"); err != nil {
		t.Fatalf("ParkTree(nil resolve): %v", err)
	}

	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	m, _ := state.NewMachine(es, "run-Z")
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	p := scheduler.NewMachineParker(func(string) []*state.Machine {
		return []*state.Machine{nil, m} // inclui um nil (deve ser saltado)
	}, scheduler.WithParkReason("custom_reason"))
	if err := p.ParkTree(ctx, "tree"); err != nil {
		t.Fatalf("ParkTree: %v", err)
	}
	if m.Current() != state.Paused {
		t.Fatalf("estado = %s, quero paused", m.Current())
	}
	// O motivo custom foi gravado na transição running→paused.
	evs, _ := es.Read(ctx, "run-Z", 1)
	found := false
	for _, ev := range evs {
		if ev.Type == state.EventTypeTransition {
			var rec struct {
				To     string `json:"to"`
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(ev.Payload, &rec)
			if rec.To == string(state.Paused) && rec.Reason == "custom_reason" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("transição paused com motivo custom_reason não encontrada")
	}
}

// Reevaluate delega em Allow: transita open→half_open após o cooldown.
func TestBreaker_Reevaluate(t *testing.T) {
	t.Parallel()
	clk := &mutClock{}
	clk.set(time.Unix(9_000_000, 0))
	es, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	b, err := budget.New("tree-re", bigLimit)
	if err != nil {
		t.Fatalf("budget.New: %v", err)
	}
	br, err := scheduler.NewBreaker(es, b, "tree-re",
		scheduler.Thresholds{VelocityTokens: 1000, Window: time.Minute, Cooldown: 30 * time.Second},
		scheduler.WithBreakerClock(clk.now))
	if err != nil {
		t.Fatalf("NewBreaker: %v", err)
	}
	ctx := context.Background()

	if _, err := br.Observe(ctx, budget.Amount{Tokens: 1500}); err != nil {
		t.Fatalf("trip: %v", err)
	}
	// Antes do cooldown: Reevaluate mantém open.
	if st, _ := br.Reevaluate(ctx); st != scheduler.BreakerOpen {
		t.Fatalf("Reevaluate pré-cooldown = %s, quero open", st)
	}
	clk.advance(30 * time.Second)
	if st, err := br.Reevaluate(ctx); err != nil || st != scheduler.BreakerHalfOpen {
		t.Fatalf("Reevaluate após cooldown = (%s,%v), quero (half_open,nil)", st, err)
	}
}
