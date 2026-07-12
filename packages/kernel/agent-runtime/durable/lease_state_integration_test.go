package durable_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/durable"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// intClock é um relógio manual para este pacote de teste externo (durable_test não
// partilha o testClock interno).
type intClock struct{ t time.Time }

func (c *intClock) Now() time.Time          { return c.t }
func (c *intClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// TestClaimTokenDrivesReadyToRunning demonstra a INTEGRAÇÃO ponta-a-ponta do contrato
// de AOS-018 com a máquina de estados de AOS-017 e o Event Store de AOS-002:
//
//	Claim → fencing token → Machine.Transition(ready → running) → escrita FENCED.
//
// O token mintado pelo LeaseManager (durable.FencingToken) satisfaz directamente o
// contrato state.FencingToken (Valid()/Value()), sem conversão nem acoplamento de
// pacotes — é a ligação partilhada entre o claim e o claim ready → running.
func TestClaimTokenDrivesReadyToRunning(t *testing.T) {
	clk := &intClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	const run = "run-integration"

	lm, err := durable.NewLeaseManager(store, 30*time.Second, durable.WithLeaseClock(clk))
	if err != nil {
		t.Fatalf("NewLeaseManager: %v", err)
	}

	// (1) Claim minta o fencing token.
	lease, err := lm.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	// (2) O token entra directamente no claim ready → running da máquina de estados.
	var _ state.FencingToken = lease.Token // o token satisfaz o contrato de AOS-017
	m, err := state.NewMachine(store, run, state.WithClock(state.ClockFunc(clk.Now)))
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{
		Reason: "claim",
		Token:  lease.Token,
	}); err != nil {
		t.Fatalf("Transition ready→running com token do claim: %v", err)
	}
	if m.Current() != state.Running {
		t.Fatalf("estado = %q, quer running", m.Current())
	}

	// (3) Escrita de negócio FENCED sob o token corrente → aceite.
	fa, err := durable.NewFencedAppender(store, lm)
	if err != nil {
		t.Fatalf("NewFencedAppender: %v", err)
	}
	if _, err := fa.Append(ctx, run, lease.Token, eventstore.EventInput{
		Type: "work.effect", Payload: []byte(`{"k":1}`), RunID: run, StepID: "w1",
	}); err != nil {
		t.Fatalf("escrita fenced sob o token corrente: %v", err)
	}

	// (4) Reatribuição: o lease de A expira, B reclama (token maior). AQUI é a ESCRITA
	// FENCED (não a transição da máquina) que exclui A: a máquina de AOS-017, sem uma
	// FencingAuthority ligada, valida só a PRESENÇA do token — a staleness da PRÓPRIA
	// re-transição ready→running é exercida em TestMachineRejectsStaleClaimWithAuthority.
	clk.advance(31 * time.Second)
	leaseB, err := lm.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim B: %v", err)
	}
	if leaseB.Token.Value() <= lease.Token.Value() {
		t.Fatalf("token de B (%d) não é > token de A (%d)", leaseB.Token.Value(), lease.Token.Value())
	}
	// A escrita obsoleta de A (token antigo) é fenced-out mesmo após a transição.
	if _, err := fa.Append(ctx, run, lease.Token, eventstore.EventInput{
		Type: "work.effect", Payload: []byte(`{"k":2}`), RunID: run, StepID: "w2-stale",
	}); !errors.Is(err, durable.ErrStaleFencingToken) {
		t.Fatalf("escrita obsoleta pós-transição = %v, quer ErrStaleFencingToken", err)
	}

	// (5) Uma tentativa de claim ready→running SEM token válido é recusada pela máquina
	// (contrato de AOS-017) — confirma que o token é a pré-condição do claim.
	m2, _ := state.NewMachine(store, "run-no-token", state.WithClock(state.ClockFunc(clk.Now)))
	if err := m2.Transition(ctx, state.Running, state.TransitionEvent{Reason: "no-token"}); !errors.Is(err, state.ErrMissingFencingToken) {
		t.Fatalf("Transition sem token = %v, quer ErrMissingFencingToken", err)
	}
}

// leaseFencingAuthority adapta o LeaseManager (CurrentToken → FencingToken) ao contrato
// state.FencingAuthority (CurrentTokenValue → uint64), SEM o pacote state importar
// durable — a ligação por interface implícita que AOS-018 partilha com AOS-017.
type leaseFencingAuthority struct{ lm *durable.LeaseManager }

func (a leaseFencingAuthority) CurrentTokenValue(ctx context.Context, runID string) (uint64, error) {
	tok, err := a.lm.CurrentToken(ctx, runID)
	if err != nil {
		return 0, err
	}
	return tok.Value(), nil
}

// TestMachineRejectsStaleClaimWithAuthority exercita a STALENESS da própria transição
// ready→running (AOS-018-Q2): com uma state.FencingAuthority ligada (o LeaseManager,
// via adaptador), a máquina recusa um claim cujo token seja INFERIOR ao corrente —
// não basta a PRESENÇA do token. Sem a autoridade a transição passaria (um worker
// obsoleto materializava-a); com ela, é fail-closed com state.ErrStaleFencingToken.
func TestMachineRejectsStaleClaimWithAuthority(t *testing.T) {
	clk := &intClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
	store, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	const run = "run-stale-claim"

	lm, err := durable.NewLeaseManager(store, 30*time.Second, durable.WithLeaseClock(clk))
	if err != nil {
		t.Fatalf("NewLeaseManager: %v", err)
	}

	// A reclama (token 1); o lease expira; B reclama (token 2). Corrente = 2.
	a, err := lm.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim A: %v", err)
	}
	clk.advance(31 * time.Second)
	b, err := lm.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim B: %v", err)
	}
	if b.Token.Value() <= a.Token.Value() {
		t.Fatalf("token de B (%d) não é > token de A (%d)", b.Token.Value(), a.Token.Value())
	}

	// Máquina ligada à autoridade de fencing (o LeaseManager).
	m, err := state.NewMachine(store, run,
		state.WithClock(state.ClockFunc(clk.Now)),
		state.WithFencingAuthority(leaseFencingAuthority{lm}),
	)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}

	// Claim ready→running com o token OBSOLETO de A (1 < corrente 2) → recusado.
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Reason: "stale-claim", Token: a.Token}); !errors.Is(err, state.ErrStaleFencingToken) {
		t.Fatalf("claim obsoleto = %v, quer state.ErrStaleFencingToken", err)
	}
	if m.Current() != state.Ready {
		t.Fatalf("estado após claim recusado = %q, quer ready (transição não materializada)", m.Current())
	}

	// Claim com o token CORRENTE de B (2) → aceite.
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Reason: "claim", Token: b.Token}); err != nil {
		t.Fatalf("claim corrente de B = %v, quer nil", err)
	}
	if m.Current() != state.Running {
		t.Fatalf("estado = %q, quer running", m.Current())
	}
}
