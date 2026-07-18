package hitl

import (
	"context"
	"testing"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/kernel/reference-monitor/risk"
	"github.com/aos-ref/platform/audit"
)

// TestGateComposition_EscalatesAndFailsClosedToKilled comprova a integração ponta-a-
// ponta exigida por AC1/AC3: o gate de risco (AOS-074) aceita este Channel HITL como
// a sua porta ConfirmationChannel (substituindo o DenyChannel base), uma acção danger/
// irreversível ESCALA (o Channel recebe o preview concreto) e o TIMEOUT/silêncio NEGA
// fail-closed. As transições duráveis correspondentes — Running→WaitingOnHuman
// (escalada) e WaitingOnHuman→Killed (timeout) — são as válidas da máquina de estados
// (EPIC-01/02).
func TestGateComposition_EscalatesAndFailsClosedToKilled(t *testing.T) {
	t.Parallel()

	// O estado durável do Art. 14: a escalada pausa em waiting_on_human; o timeout
	// fail-closed transita para killed. Ambas são transições canónicas da máquina.
	if !state.IsValidTransition(state.Running, state.WaitingOnHuman) {
		t.Fatalf("escalada danger deve pausar em waiting_on_human (transicao invalida)")
	}
	if !state.IsValidTransition(state.WaitingOnHuman, state.Killed) {
		t.Fatalf("timeout fail-closed deve transitar waiting_on_human -> killed")
	}
	if state.IsValidTransition(state.WaitingOnHuman, state.Complete) {
		t.Fatalf("waiting_on_human NAO deve poder saltar para complete (silencio nunca permite)")
	}

	var gotPreview string
	src := scriptedSource{fn: func(ctx context.Context, p Presentation) (SignedApproval, error) {
		gotPreview = p.Preview // a escalada apresentou o preview concreto
		<-ctx.Done()           // aprovador silencioso → o gate impõe a deadline fail-closed
		return SignedApproval{}, ctx.Err()
	}}
	reg := NewMemApproverRegistry()
	store := audit.NewMemStore()
	ch, err := NewChannel(reg, src, store, WithClock(fixedClock()))
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}

	// O gate de risco compõe o nosso Channel como ConfirmationChannel (não o DenyChannel).
	gate, err := risk.NewGate(ch, risk.WithTimeout(10*time.Millisecond))
	if err != nil {
		t.Fatalf("NewGate: %v", err)
	}

	req := risk.Request{
		Classification: risk.Classification{Class: risk.ClassDanger, Reversibility: risk.Irreversible, PolicyVersion: "test#deadbeef0000"},
		Preview:        "cap:fs.delete -> /data/prod",
		Principal:      "run-7",
		Capability:     "cap:fs.delete",
		Resource:       "/data/prod",
	}
	res := gate.Evaluate(context.Background(), req)
	if res.Outcome != risk.OutcomeDeny {
		t.Fatalf("silencio numa accao irreversivel devia NEGAR, obtive %v", res.Outcome)
	}
	if !res.TimedOut {
		t.Fatalf("a negacao devia resultar do timeout fail-closed")
	}
	if gotPreview != "cap:fs.delete -> /data/prod" {
		t.Fatalf("a escalada nao apresentou o preview concreto: %q", gotPreview)
	}
	// O timeout ficou registado nos contadores de ambos (gate e channel).
	if to := gate.Metrics().Timeouts.Load(); to != 1 {
		t.Fatalf("gate: esperava Timeouts=1, obtive %d", to)
	}
	if _, _, _, chTimeouts, _ := ch.Metrics().Snapshot(); chTimeouts != 1 {
		t.Fatalf("channel: esperava Timeouts=1, obtive %d", chTimeouts)
	}
}
