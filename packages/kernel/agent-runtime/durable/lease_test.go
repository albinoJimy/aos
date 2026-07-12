package durable

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
)

// conflictOnceStore envolve o Event Store e, na PRIMEIRA escrita a um stream de
// lease, injecta antes uma escrita concorrente (o "outro worker") no MESMO
// expected_seq — forçando de forma DETERMINÍSTICA a rejeição por concorrência
// optimista (ErrAppendOnlyViolation) do Append original, e com ela o caminho de
// re-tentativa do claim. É o cross-host de contenção sem depender de escalonamento.
type conflictOnceStore struct {
	*eventstore.Store
	competing eventstore.EventInput
	fired     bool
}

func (s *conflictOnceStore) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if !s.fired && strings.HasPrefix(streamID, leaseStreamPrefix) {
		s.fired = true
		// O concorrente vence o slot com o MESMO expected_seq (opts) que o chamador ia usar.
		if _, err := s.Store.Append(ctx, streamID, s.competing, opts...); err != nil {
			return eventstore.AppendResult{}, err
		}
	}
	return s.Store.Append(ctx, streamID, in, opts...)
}

// testClock é um relógio manual thread-safe: os testes avançam o tempo
// explicitamente (sem sleeps), o que torna a expiração de TTL determinística e o
// -race limpo mesmo quando várias goroutines lêem o relógio em simultâneo.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

const ttl = 30 * time.Second

func newManager(t *testing.T, store EventStore, clk Clock, opts ...LeaseOption) *LeaseManager {
	t.Helper()
	all := append([]LeaseOption{WithLeaseClock(clk)}, opts...)
	m, err := NewLeaseManager(store, ttl, all...)
	if err != nil {
		t.Fatalf("NewLeaseManager: %v", err)
	}
	return m
}

func TestNewLeaseManagerValidation(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	tests := []struct {
		name  string
		store EventStore
		ttl   time.Duration
		want  error
	}{
		{"nil store", nil, ttl, ErrNilStore},
		{"zero ttl", store, 0, ErrInvalidTTL},
		{"negative ttl", store, -time.Second, ErrInvalidTTL},
		{"ok", store, ttl, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewLeaseManager(tc.store, tc.ttl)
			if !errors.Is(err, tc.want) {
				t.Fatalf("erro = %v, quer %v", err, tc.want)
			}
		})
	}
}

// TestClaimHeartbeatRenewExpire cobre o ciclo de vida: claim → heartbeat renova →
// (sem heartbeat) expira → run reclamável com token MAIOR.
func TestClaimHeartbeatRenewExpire(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	store := newStore(t)
	m := newManager(t, store, clk, WithWorkerID("A"))
	ctx := context.Background()
	const run = "run-lifecycle"

	// (1) Claim inicial → token 1, expira em now+ttl.
	l1, err := m.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if l1.Token.Value() != 1 {
		t.Fatalf("token inicial = %d, quer 1", l1.Token.Value())
	}
	wantExp := clk.Now().Add(ttl)
	if !l1.ExpiresAt.Equal(wantExp) {
		t.Fatalf("ExpiresAt = %v, quer %v", l1.ExpiresAt, wantExp)
	}

	// (2) Heartbeat antes do TTL → renova a expiração, mantém o token.
	clk.Advance(ttl / 2)
	l2, err := m.Heartbeat(ctx, l1)
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if l2.Token.Value() != 1 {
		t.Fatalf("heartbeat mudou o token: %d", l2.Token.Value())
	}
	if !l2.ExpiresAt.After(l1.ExpiresAt) {
		t.Fatalf("heartbeat não estendeu a expiração: %v <= %v", l2.ExpiresAt, l1.ExpiresAt)
	}

	// (3) Claim enquanto o lease está VIVO → ErrLeaseHeld.
	if _, err := m.Claim(ctx, run); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("Claim com lease vivo = %v, quer ErrLeaseHeld", err)
	}

	// (4) Sem heartbeat, avança para lá da nova expiração → o lease expira.
	clk.Advance(ttl + time.Nanosecond)

	// Heartbeat de um lease expirado → ErrLeaseExpired.
	if _, err := m.Heartbeat(ctx, l2); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("Heartbeat expirado = %v, quer ErrLeaseExpired", err)
	}

	// (5) Novo claim reclama o run e minta um token ESTRITAMENTE MAIOR.
	l3, err := m.Claim(ctx, run)
	if err != nil {
		t.Fatalf("re-Claim após expiração: %v", err)
	}
	if l3.Token.Value() != 2 {
		t.Fatalf("token após reclamação = %d, quer 2", l3.Token.Value())
	}

	// (6) Heartbeat do lease antigo (token 1), agora superado → ErrLeaseSuperseded.
	if _, err := m.Heartbeat(ctx, l1); !errors.Is(err, ErrLeaseSuperseded) {
		t.Fatalf("Heartbeat superado = %v, quer ErrLeaseSuperseded", err)
	}
}

// TestCurrentTokenAndCurrentLease verifica a autoridade de token e a leitura do lease.
func TestCurrentTokenAndCurrentLease(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	store := newStore(t)
	m := newManager(t, store, clk)
	ctx := context.Background()
	const run = "run-current"

	// Sem lease: token 0, sem lease corrente.
	tok, err := m.CurrentToken(ctx, run)
	if err != nil || tok.Value() != 0 {
		t.Fatalf("CurrentToken inicial = %d, %v; quer 0, nil", tok.Value(), err)
	}
	if _, ok, err := m.Current(ctx, run); err != nil || ok {
		t.Fatalf("Current inicial ok=%v err=%v; quer false, nil", ok, err)
	}

	l, err := m.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	tok, _ = m.CurrentToken(ctx, run)
	if tok.Value() != l.Token.Value() {
		t.Fatalf("CurrentToken = %d, quer %d", tok.Value(), l.Token.Value())
	}
	cur, ok, err := m.Current(ctx, run)
	if err != nil || !ok {
		t.Fatalf("Current após claim ok=%v err=%v", ok, err)
	}
	if cur.Token.Value() != l.Token.Value() || !cur.ExpiresAt.Equal(l.ExpiresAt) {
		t.Fatalf("Current lease = %+v, quer token/exp de %+v", cur, l)
	}
}

// TestTrivialHelpers cobre os utilitários finos: relógio de sistema (default),
// ClockFunc, Lease.Expired, e o rótulo de produtor — sem depender de sleeps.
func TestTrivialHelpers(t *testing.T) {
	t.Parallel()
	store := newStore(t)
	// Sem WithLeaseClock → usa o systemClock por omissão; e com produtor rotulado.
	m, err := NewLeaseManager(store, ttl,
		WithLeaseProducer(eventstore.Producer{NHIID: "nhi:worker"}),
		WithWorkerID("A"))
	if err != nil {
		t.Fatalf("NewLeaseManager: %v", err)
	}
	ctx := context.Background()
	l, err := m.Claim(ctx, "run-trivial")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// Expired: antes da expiração é falso; muito depois é verdadeiro (fronteira inclusiva).
	if l.Expired(l.ExpiresAt.Add(-time.Nanosecond)) {
		t.Fatal("lease não devia estar expirado antes de ExpiresAt")
	}
	if !l.Expired(l.ExpiresAt) {
		t.Fatal("lease devia estar expirado em ExpiresAt (fronteira inclusiva)")
	}

	// ClockFunc adapta uma função a Clock.
	fixed := time.Unix(1000, 0)
	var c Clock = ClockFunc(func() time.Time { return fixed })
	if !c.Now().Equal(fixed) {
		t.Fatalf("ClockFunc.Now = %v, quer %v", c.Now(), fixed)
	}
}

func TestEmptyRunIDRejected(t *testing.T) {
	t.Parallel()
	m := newManager(t, newStore(t), newTestClock())
	ctx := context.Background()
	if _, err := m.Claim(ctx, ""); !errors.Is(err, ErrEmptyRunID) {
		t.Fatalf("Claim(\"\") = %v, quer ErrEmptyRunID", err)
	}
	if _, err := m.CurrentToken(ctx, ""); !errors.Is(err, ErrEmptyRunID) {
		t.Fatalf("CurrentToken(\"\") = %v, quer ErrEmptyRunID", err)
	}
	if _, err := m.Heartbeat(ctx, Lease{}); !errors.Is(err, ErrEmptyRunID) {
		t.Fatalf("Heartbeat(zero) = %v, quer ErrEmptyRunID", err)
	}
}

// TestTokensStrictlyMonotonic prova que reclamações sucessivas (cada uma após a
// expiração da anterior) produzem tokens ESTRITAMENTE crescentes 1,2,3,4.
func TestTokensStrictlyMonotonic(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	store := newStore(t)
	m := newManager(t, store, clk)
	ctx := context.Background()
	const run = "run-monotonic"

	var prev uint64
	for i := 0; i < 4; i++ {
		l, err := m.Claim(ctx, run)
		if err != nil {
			t.Fatalf("Claim #%d: %v", i, err)
		}
		if l.Token.Value() <= prev {
			t.Fatalf("token #%d = %d não é > anterior %d", i, l.Token.Value(), prev)
		}
		prev = l.Token.Value()
		clk.Advance(ttl + time.Nanosecond) // deixa expirar para o próximo claim
	}
	if prev != 4 {
		t.Fatalf("último token = %d, quer 4", prev)
	}
}

// TestClaimRetriesOnConcurrencyConflictYieldsHigherToken força DETERMINISTICAMENTE a
// contenção de concorrência optimista: um concorrente vence o slot de expected_seq do
// primeiro claim (deixando um lease JÁ EXPIRADO com token 1). O claim vê a rejeição,
// RELÊ e minta um token ESTRITAMENTE MAIOR (2) — a monotonicidade sob contenção que
// deriva do CAS do Event Store, não de coordenação in-memory.
func TestClaimRetriesOnConcurrencyConflictYieldsHigherToken(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	base := newStore(t)
	ctx := context.Background()
	const run = "run-cas-conflict"

	now := clk.Now()
	// Concorrente: claim de token 1 JÁ EXPIRADO (expira exactamente em now, inclusivo).
	competitor := leaseRecord{
		RunID:           run,
		Token:           1,
		Worker:          "competitor",
		Kind:            "claimed",
		TTLNanos:        int64(ttl),
		AtUnixNano:      now.Add(-2 * ttl).UnixNano(),
		ExpiresUnixNano: now.UnixNano(),
	}
	payload, err := json.Marshal(competitor)
	if err != nil {
		t.Fatalf("marshal competitor: %v", err)
	}
	cs := &conflictOnceStore{
		Store: base,
		competing: eventstore.EventInput{
			Type:    EventTypeLeaseClaimed,
			Payload: payload,
			RunID:   run,
			StepID:  leaseClaimStepPrefix + "competitor",
		},
	}
	m := newManager(t, cs, clk)

	l, err := m.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim sob conflito: %v", err)
	}
	if l.Token.Value() != 2 {
		t.Fatalf("token após conflito = %d, quer 2 (estritamente > 1 do concorrente)", l.Token.Value())
	}
	if !cs.fired {
		t.Fatal("o concorrente não chegou a ser injectado (conflito não exercido)")
	}
}

// TestConcurrentClaimSingleWinner: N workers competem pelo MESMO run livre em
// simultâneo. A concorrência optimista do Event Store serializa os claims — exactamente
// UM vence (token 1) e os restantes vêem o lease vivo e recebem ErrLeaseHeld. Prova
// que só um escritor obtém o token corrente (critério de concorrência AOS-018).
func TestConcurrentClaimSingleWinner(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	store := newStore(t)
	ctx := context.Background()
	const run = "run-concurrent"
	const workers = 16

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		wins     int
		winToken uint64
		held     int
		others   []error
	)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			// Cada worker tem o seu manager (cross-host simulado), mesmo store/relógio.
			m := newManager(t, store, clk)
			<-start
			l, err := m.Claim(ctx, run)
			mu.Lock()
			switch {
			case err == nil:
				wins++
				winToken = l.Token.Value()
			case errors.Is(err, ErrLeaseHeld):
				held++
			default:
				others = append(others, err)
			}
			mu.Unlock()
		}(i)
	}
	close(start)
	wg.Wait()

	if len(others) != 0 {
		t.Fatalf("erros inesperados nos claims concorrentes: %v", others)
	}
	if wins != 1 {
		t.Fatalf("vencedores = %d, quer exactamente 1", wins)
	}
	if winToken != 1 {
		t.Fatalf("token vencedor = %d, quer 1", winToken)
	}
	if held != workers-1 {
		t.Fatalf("ErrLeaseHeld = %d, quer %d", held, workers-1)
	}
}

// TestLeaseEventsObservableInStream fecha o item de DoD de OBSERVABILIDADE (AOS-018):
// os eventos de lease/heartbeat são auditáveis lendo directamente o stream de lease.
// Assevera a sequência e o conteúdo: lease.claimed (token N, worker, expiração) seguido
// de lease.renewed (MESMO token, NOVA expiração maior) — em vez de a inferir só do
// comportamento de readLeaseState/FenceObserver.
func TestLeaseEventsObservableInStream(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	store := newStore(t)
	m := newManager(t, store, clk, WithWorkerID("w1"))
	ctx := context.Background()
	const run = "run-obs"

	lease, err := m.Claim(ctx, run)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// Heartbeat dentro do TTL → renova (mesmo token, nova expiração).
	clk.Advance(ttl / 2)
	renewed, err := m.Heartbeat(ctx, lease)
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	// Lê o STREAM DE LEASE e recolhe os eventos de lease observáveis.
	evs, err := store.Read(ctx, leaseStream(run), 1)
	if err != nil {
		t.Fatalf("Read stream de lease: %v", err)
	}
	type obsEvent struct {
		typ     string
		token   uint64
		worker  string
		expires int64
	}
	var got []obsEvent
	for _, e := range evs {
		if e.Type != EventTypeLeaseClaimed && e.Type != EventTypeLeaseRenewed {
			continue
		}
		var rec leaseRecord
		if uerr := json.Unmarshal(e.Payload, &rec); uerr != nil {
			t.Fatalf("descodifica evento de lease: %v", uerr)
		}
		got = append(got, obsEvent{e.Type, rec.Token, rec.Worker, rec.ExpiresUnixNano})
	}

	if len(got) != 2 {
		t.Fatalf("eventos de lease observáveis = %d, quer 2 (claimed + renewed)", len(got))
	}
	if got[0].typ != EventTypeLeaseClaimed {
		t.Fatalf("1.º evento = %q, quer %q", got[0].typ, EventTypeLeaseClaimed)
	}
	if got[1].typ != EventTypeLeaseRenewed {
		t.Fatalf("2.º evento = %q, quer %q", got[1].typ, EventTypeLeaseRenewed)
	}
	// Mesmo token entre claim e heartbeat (o heartbeat não minta novo token).
	if got[0].token != lease.Token.Value() || got[1].token != lease.Token.Value() {
		t.Fatalf("tokens observados = %d/%d, quer ambos %d", got[0].token, got[1].token, lease.Token.Value())
	}
	// O worker é observável/auditável em ambos.
	if got[0].worker != "w1" || got[1].worker != "w1" {
		t.Fatalf("worker observado = %q/%q, quer w1/w1", got[0].worker, got[1].worker)
	}
	// O heartbeat ESTENDE a expiração (nova > anterior) e casa com o lease devolvido.
	if got[1].expires <= got[0].expires {
		t.Fatalf("expiração renovada = %d, quer > %d (heartbeat estende o TTL)", got[1].expires, got[0].expires)
	}
	if got[1].expires != renewed.ExpiresAt.UnixNano() {
		t.Fatalf("expiração observada = %d, quer %d (lease renovado)", got[1].expires, renewed.ExpiresAt.UnixNano())
	}
}
