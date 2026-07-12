package liveness

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// fakeClock é um relógio determinístico (sem sleeps). Advance move o "agora".
type fakeClock struct{ t time.Time }

func newClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0).UTC()} }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// countingObserver conta as classificações vistas — prova que o observer é chamado.
type countingObserver struct {
	last  Classification
	count int
}

func (o *countingObserver) Classified(_ state.State, c Classification) {
	o.last = c
	o.count++
}

// ---------------------------------------------------------------------------
// ZombieClassifier: matriz table-driven
// ---------------------------------------------------------------------------

func TestClassify(t *testing.T) {
	t.Parallel()
	c := NewZombieClassifier()
	ctx := context.Background()

	cases := []struct {
		name string
		run  RunLiveness
		want Classification
	}{
		// --- Invariante 1: espera legítima NUNCA é zombi, mesmo com lease expirado ---
		{
			name: "waiting_on_human com lease de trabalho expirado NAO e zombi",
			run:  RunLiveness{State: state.WaitingOnHuman, WorkLeaseExpired: true},
			want: WaitingLegitimate,
		},
		{
			name: "waiting_on_human lease vivo",
			run:  RunLiveness{State: state.WaitingOnHuman, WorkLeaseExpired: false},
			want: WaitingLegitimate,
		},
		{
			name: "waiting_on_tool com lease expirado NAO e zombi",
			run:  RunLiveness{State: state.WaitingOnTool, WorkLeaseExpired: true},
			want: WaitingLegitimate,
		},
		{
			name: "paused com lease expirado NAO e zombi",
			run:  RunLiveness{State: state.Paused, WorkLeaseExpired: true},
			want: WaitingLegitimate,
		},
		// --- Gate humano fail-closed: TTL do gate excedido → GateExpired (não zombi) ---
		{
			name: "waiting_on_human com gate excedido e GateExpired",
			run:  RunLiveness{State: state.WaitingOnHuman, GateDeadlineExceeded: true},
			want: GateExpired,
		},
		{
			name: "waiting_on_human com gate excedido E lease expirado ainda e GateExpired (gate vence)",
			run:  RunLiveness{State: state.WaitingOnHuman, GateDeadlineExceeded: true, WorkLeaseExpired: true},
			want: GateExpired,
		},
		{
			name: "gate excedido em waiting_on_tool e IGNORADO (gate e do humano)",
			run:  RunLiveness{State: state.WaitingOnTool, GateDeadlineExceeded: true},
			want: WaitingLegitimate,
		},
		{
			name: "gate excedido em paused e IGNORADO",
			run:  RunLiveness{State: state.Paused, GateDeadlineExceeded: true},
			want: WaitingLegitimate,
		},
		// --- Invariante 2 (não-regressão): running com lease expirado É zombi ---
		{
			name: "running com lease de trabalho expirado E zombi",
			run:  RunLiveness{State: state.Running, WorkLeaseExpired: true},
			want: Zombie,
		},
		{
			name: "running com lease vivo e Alive",
			run:  RunLiveness{State: state.Running, WorkLeaseExpired: false},
			want: Alive,
		},
		{
			name: "running com gate flag espurio ignorado, lease vivo e Alive",
			run:  RunLiveness{State: state.Running, GateDeadlineExceeded: true},
			want: Alive,
		},
		// --- Terminais ---
		{name: "complete e Terminal", run: RunLiveness{State: state.Complete}, want: Terminal},
		{name: "killed e Terminal", run: RunLiveness{State: state.Killed}, want: Terminal},
		{name: "timed_out e Terminal", run: RunLiveness{State: state.TimedOut}, want: Terminal},
		{
			name: "terminal com lease expirado continua Terminal (nao zombi)",
			run:  RunLiveness{State: state.TimedOut, WorkLeaseExpired: true},
			want: Terminal,
		},
		// --- Activos / recuperação ---
		{name: "ready e Alive", run: RunLiveness{State: state.Ready}, want: Alive},
		{name: "failed e Alive", run: RunLiveness{State: state.Failed}, want: Alive},
		{name: "compensating e Alive", run: RunLiveness{State: state.Compensating}, want: Alive},
		{
			name: "ready com lease expirado NAO e zombi (so running e)",
			run:  RunLiveness{State: state.Ready, WorkLeaseExpired: true},
			want: Alive,
		},
		// --- Estado não-canónico (defensivo): conservador, nunca zombi ---
		{
			name: "estado desconhecido cai em Alive (conservador, nunca morto por zombi)",
			run:  RunLiveness{State: state.State("gibberish"), WorkLeaseExpired: true},
			want: Alive,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := c.Classify(ctx, tc.run); got != tc.want {
				t.Fatalf("Classify(%+v) = %q, quer %q", tc.run, got, tc.want)
			}
		})
	}
}

// TestClassifyExhaustiveSuspendedNeverZombie varre TODOS os estados de suspensão sob
// TODAS as combinações de flags e prova a invariante: NUNCA Zombie.
func TestClassifyExhaustiveSuspendedNeverZombie(t *testing.T) {
	t.Parallel()
	c := NewZombieClassifier()
	ctx := context.Background()
	for _, s := range []state.State{state.WaitingOnHuman, state.WaitingOnTool, state.Paused} {
		for _, leaseExp := range []bool{false, true} {
			for _, gateExc := range []bool{false, true} {
				got := c.Classify(ctx, RunLiveness{State: s, WorkLeaseExpired: leaseExp, GateDeadlineExceeded: gateExc})
				if got == Zombie {
					t.Fatalf("estado de espera %q (lease_exp=%v gate_exc=%v) foi classificado ZOMBIE — viola a invariante", s, leaseExp, gateExc)
				}
			}
		}
	}
}

// TestClassifyRunningStuckIsZombie prova a não-regressão: o único caminho para Zombie
// é running + lease expirado, e ele DISPARA.
func TestClassifyRunningStuckIsZombie(t *testing.T) {
	t.Parallel()
	c := NewZombieClassifier()
	ctx := context.Background()

	if got := c.Classify(ctx, RunLiveness{State: state.Running, WorkLeaseExpired: true}); got != Zombie {
		t.Fatalf("running preso deveria ser Zombie, veio %q", got)
	}
	// Nenhum outro estado, mesmo com lease expirado, produz Zombie.
	for _, s := range state.AllStates {
		if s == state.Running {
			continue
		}
		if got := c.Classify(ctx, RunLiveness{State: s, WorkLeaseExpired: true}); got == Zombie {
			t.Fatalf("estado %q com lease expirado NAO deveria ser Zombie, veio %q", s, got)
		}
	}
}

func TestClassifierObserverInvoked(t *testing.T) {
	t.Parallel()
	obs := &countingObserver{}
	c := NewZombieClassifier(WithClassifierObserver(obs))
	c.Classify(context.Background(), RunLiveness{State: state.Running, WorkLeaseExpired: true})
	if obs.count != 1 || obs.last != Zombie {
		t.Fatalf("observer: count=%d last=%q, quer 1/%q", obs.count, obs.last, Zombie)
	}
}

func TestClassificationHelpers(t *testing.T) {
	t.Parallel()
	if !Zombie.IsZombie() || WaitingLegitimate.IsZombie() {
		t.Fatal("IsZombie incorrecto")
	}
	if !WaitingLegitimate.IsLegitimateWait() || Zombie.IsLegitimateWait() {
		t.Fatal("IsLegitimateWait incorrecto")
	}
	if !GateExpired.RequiresKill() {
		t.Fatal("GateExpired deveria RequiresKill")
	}
	if Zombie.RequiresKill() {
		t.Fatal("Zombie NAO deveria RequiresKill (e reatribuicao, nao morte do run)")
	}
}

// ---------------------------------------------------------------------------
// WaitingGate: TTL fail-closed do relógio de espera
// ---------------------------------------------------------------------------

func TestNewWaitingGateValidation(t *testing.T) {
	t.Parallel()
	for _, ttl := range []time.Duration{0, -time.Second} {
		if _, err := NewWaitingGate(ttl); !errors.Is(err, ErrInvalidGateTTL) {
			t.Fatalf("NewWaitingGate(%v) err=%v, quer ErrInvalidGateTTL", ttl, err)
		}
	}
	if _, err := NewWaitingGate(time.Minute); err != nil {
		t.Fatalf("NewWaitingGate(1m) err inesperado: %v", err)
	}
}

func TestWaitingGateExceededBoundary(t *testing.T) {
	t.Parallel()
	clk := newClock()
	entered := clk.Now()
	gate, err := NewWaitingGate(30*time.Minute, WithGateClock(clk))
	if err != nil {
		t.Fatal(err)
	}

	// Antes do TTL: não excedido.
	clk.Advance(29 * time.Minute)
	if gate.Exceeded(entered) {
		t.Fatal("gate excedido antes do TTL")
	}
	if rem := gate.Remaining(entered); rem != time.Minute {
		t.Fatalf("Remaining = %v, quer 1m", rem)
	}

	// Fronteira EXACTA (now == deadline): INCLUSIVA ⇒ excedido (fail-closed), igual ao
	// CheckDeadlines de AOS-017.
	clk.Advance(time.Minute)
	if !gate.Exceeded(entered) {
		t.Fatal("gate NAO excedido na fronteira exacta (deveria ser inclusiva/fail-closed)")
	}
	if rem := gate.Remaining(entered); rem != 0 {
		t.Fatalf("Remaining na fronteira = %v, quer 0", rem)
	}

	// Depois do TTL: excedido.
	clk.Advance(time.Hour)
	if !gate.Exceeded(entered) {
		t.Fatal("gate NAO excedido bem depois do TTL")
	}
}

func TestRunLivenessFrom(t *testing.T) {
	t.Parallel()
	clk := newClock()
	entered := clk.Now()
	gate, err := NewWaitingGate(10*time.Minute, WithGateClock(clk))
	if err != nil {
		t.Fatal(err)
	}

	// waiting_on_human dentro do TTL: gate não excedido.
	rl := RunLivenessFrom(state.WaitingOnHuman, true, gate, entered)
	if rl.GateDeadlineExceeded {
		t.Fatal("gate nao deveria estar excedido dentro do TTL")
	}
	// Avança para lá do TTL.
	clk.Advance(11 * time.Minute)
	rl = RunLivenessFrom(state.WaitingOnHuman, true, gate, entered)
	if !rl.GateDeadlineExceeded {
		t.Fatal("gate deveria estar excedido depois do TTL")
	}
	// Estado não-humano: o gate é ignorado mesmo excedido no tempo.
	rl = RunLivenessFrom(state.WaitingOnTool, true, gate, entered)
	if rl.GateDeadlineExceeded {
		t.Fatal("GateDeadlineExceeded so se aplica a waiting_on_human")
	}
	// Gate nil: sem avaliação, false.
	rl = RunLivenessFrom(state.WaitingOnHuman, false, nil, entered)
	if rl.GateDeadlineExceeded {
		t.Fatal("gate nil deveria dar GateDeadlineExceeded=false")
	}
}

// TestNewWaitingGateFromDerivesFromMachine prova que o gate derivado reusa o MESMO TTL
// e o MESMO relógio da Machine (anti-drift, AOS019-Q1): a fronteira do gate coincide
// EXACTAMENTE com o kill fail-closed de CheckDeadlines — sem divergência no tempo.
func TestNewWaitingGateFromDerivesFromMachine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const ttl = 45 * time.Minute
	clk := newClock()
	m := newMachineWaitingOnHuman(t, clk, ttl)

	gate, err := NewWaitingGateFrom(m)
	if err != nil {
		t.Fatalf("NewWaitingGateFrom: %v", err)
	}
	if gate.TTL() != ttl {
		t.Fatalf("gate derivado TTL = %v, quer %v (reusar HumanApprovalTTL)", gate.TTL(), ttl)
	}

	entered := m.EnteredAt()
	// Um instante ANTES da fronteira: nem o gate excede nem CheckDeadlines mata.
	clk.Advance(ttl - time.Nanosecond)
	if gate.Exceeded(entered) {
		t.Fatal("gate derivado excedido antes do TTL")
	}
	if s, fired, err := m.CheckDeadlines(ctx); err != nil || fired || s != state.WaitingOnHuman {
		t.Fatalf("CheckDeadlines dentro do TTL: s=%q fired=%v err=%v", s, fired, err)
	}

	// Fronteira EXACTA: gate e Machine disparam no MESMO instante (partilham relógio+TTL).
	clk.Advance(time.Nanosecond)
	if !gate.Exceeded(entered) {
		t.Fatal("gate derivado NAO excedido na fronteira (deveria coincidir com CheckDeadlines)")
	}
	s, fired, err := m.CheckDeadlines(ctx)
	if err != nil || !fired || s != state.Killed {
		t.Fatalf("CheckDeadlines na fronteira: s=%q fired=%v err=%v, quer killed/true", s, fired, err)
	}
}

// TestNewWaitingGateFromGuards cobre os fail-closed do construtor derivado: máquina nil
// e máquina sem TTL humano configurado (não fabricar um gate permissivo).
func TestNewWaitingGateFromGuards(t *testing.T) {
	t.Parallel()

	if _, err := NewWaitingGateFrom(nil); !errors.Is(err, ErrNilMachine) {
		t.Fatalf("NewWaitingGateFrom(nil) err=%v, quer ErrNilMachine", err)
	}

	// Máquina SEM WithHumanApprovalTTL (humanTTL == 0): recusa fail-closed.
	st, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	m, err := state.NewMachine(st, "run-no-ttl", state.WithClock(newClock()))
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	if _, err := NewWaitingGateFrom(m); !errors.Is(err, ErrInvalidGateTTL) {
		t.Fatalf("NewWaitingGateFrom sem TTL humano err=%v, quer ErrInvalidGateTTL", err)
	}
}

// ---------------------------------------------------------------------------
// WorkClock: exclusão do tempo de espera (contrato do breaker)
// ---------------------------------------------------------------------------

func TestWorkClockExcludesWaitingTime(t *testing.T) {
	t.Parallel()
	clk := newClock()
	wc := NewWorkClock(WithWorkClockClock(clk))

	// Nada acumulado no arranque.
	if got := wc.ActiveWork(); got != 0 {
		t.Fatalf("ActiveWork inicial = %v, quer 0", got)
	}

	// Entra em running e trabalha 5m.
	wc.Observe(state.Running)
	clk.Advance(5 * time.Minute)
	if got := wc.ActiveWork(); got != 5*time.Minute {
		t.Fatalf("ActiveWork apos 5m running = %v, quer 5m", got)
	}

	// Entra em waiting_on_human e espera 2h — NÃO conta.
	wc.Observe(state.WaitingOnHuman)
	clk.Advance(2 * time.Hour)
	if got := wc.ActiveWork(); got != 5*time.Minute {
		t.Fatalf("ActiveWork apos 2h de espera = %v, quer continuar 5m (espera excluida)", got)
	}
	if wc.Running() {
		t.Fatal("WorkClock nao deveria estar running em waiting_on_human")
	}

	// Retoma running e trabalha mais 3m.
	wc.Observe(state.Running)
	clk.Advance(3 * time.Minute)
	if got := wc.ActiveWork(); got != 8*time.Minute {
		t.Fatalf("ActiveWork apos retoma = %v, quer 8m", got)
	}

	// Pausa e paused também não conta.
	wc.Observe(state.Paused)
	clk.Advance(time.Hour)
	if got := wc.ActiveWork(); got != 8*time.Minute {
		t.Fatalf("ActiveWork em paused = %v, quer 8m", got)
	}

	// Terminal: fecha o span, não acumula.
	wc.Observe(state.Complete)
	clk.Advance(time.Hour)
	if got := wc.ActiveWork(); got != 8*time.Minute {
		t.Fatalf("ActiveWork terminal = %v, quer 8m", got)
	}
}

func TestWorkClockSignalPredicates(t *testing.T) {
	t.Parallel()
	if !CountsAsActiveWork(state.Running) {
		t.Fatal("running deveria contar como trabalho activo")
	}
	for _, s := range []state.State{state.WaitingOnHuman, state.WaitingOnTool, state.Paused, state.Ready, state.Complete} {
		if CountsAsActiveWork(s) {
			t.Fatalf("%q NAO deveria contar como trabalho activo", s)
		}
	}
	for _, s := range []state.State{state.WaitingOnHuman, state.WaitingOnTool, state.Paused} {
		if !IsWorkPaused(s) {
			t.Fatalf("%q deveria ter o relogio de trabalho pausado", s)
		}
	}
	for _, s := range []state.State{state.Running, state.Ready, state.Complete, state.Failed} {
		if IsWorkPaused(s) {
			t.Fatalf("%q NAO deveria ter o relogio de trabalho pausado", s)
		}
	}
}

// TestConstructorDefaultsAndAdapters cobre os guardas nil (default fail-safe) e o
// adaptador ClockFunc / o getter TTL.
func TestConstructorDefaultsAndAdapters(t *testing.T) {
	t.Parallel()

	// ClockFunc adapta uma função a Clock.
	fixed := time.Unix(1000, 0).UTC()
	var clk Clock = ClockFunc(func() time.Time { return fixed })
	if !clk.Now().Equal(fixed) {
		t.Fatal("ClockFunc.Now nao devolveu o instante fixo")
	}

	// WithClassifierObserver(nil) mantém o Nop default (não entra em pânico).
	c := NewZombieClassifier(WithClassifierObserver(nil))
	if got := c.Classify(context.Background(), RunLiveness{State: state.Running}); got != Alive {
		t.Fatalf("classify com observer nil = %q, quer Alive", got)
	}

	// WithGateClock(nil) mantém o systemClock default; TTL() devolve o configurado.
	gate, err := NewWaitingGate(42*time.Second, WithGateClock(nil))
	if err != nil {
		t.Fatal(err)
	}
	if gate.TTL() != 42*time.Second {
		t.Fatalf("TTL() = %v, quer 42s", gate.TTL())
	}

	// WithWorkClockClock(nil) mantém o systemClock default (não entra em pânico).
	wc := NewWorkClock(WithWorkClockClock(nil))
	wc.Observe(state.Running)
	if !wc.Running() {
		t.Fatal("WorkClock deveria estar running apos Observe(Running)")
	}
}

// ---------------------------------------------------------------------------
// Integração com a Machine real (AOS-017): critério combinado 4 de AOS-019
// ---------------------------------------------------------------------------

func newMachineWaitingOnHuman(t *testing.T, clk *fakeClock, ttl time.Duration) *state.Machine {
	t.Helper()
	st, err := eventstore.New()
	if err != nil {
		t.Fatalf("eventstore.New: %v", err)
	}
	m, err := state.NewMachine(st, "run-aos019",
		state.WithClock(clk),
		state.WithHumanApprovalTTL(ttl),
	)
	if err != nil {
		t.Fatalf("NewMachine: %v", err)
	}
	ctx := context.Background()
	// ready → running (claim exige fencing token) → waiting_on_human.
	if err := m.Transition(ctx, state.Running, state.TransitionEvent{Token: state.Uint64Token(1)}); err != nil {
		t.Fatalf("ready->running: %v", err)
	}
	if err := m.Transition(ctx, state.WaitingOnHuman, state.TransitionEvent{Reason: "risk_gate"}); err != nil {
		t.Fatalf("running->waiting_on_human: %v", err)
	}
	return m
}

// TestCombinedWaitLongThenGateKill é o teste do critério 4: um run parado 100% do
// wall-clock de aprovação em waiting_on_human NÃO é morto por falso-positivo de zombi,
// MAS É morto ao exceder o TTL do gate.
func TestCombinedWaitLongThenGateKill(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const ttl = time.Hour

	clk := newClock()
	m := newMachineWaitingOnHuman(t, clk, ttl)
	gate, err := NewWaitingGate(ttl, WithGateClock(clk))
	if err != nil {
		t.Fatal(err)
	}
	classifier := NewZombieClassifier()

	// O lease de TRABALHO está expirado o tempo todo (o worker está parado à espera do
	// humano — o heartbeat de trabalho pausou). É a condição que, em running, faria
	// zombi — aqui NÃO deve.
	const workLeaseExpired = true

	// Avança 100% do wall-clock de aprovação MENOS um instante: ainda dentro do gate.
	clk.Advance(ttl - time.Nanosecond)

	entered := m.EnteredAt()
	rl := RunLivenessFrom(m.Current(), workLeaseExpired, gate, entered)
	if got := classifier.Classify(ctx, rl); got != WaitingLegitimate {
		t.Fatalf("espera longa (99.9%% do TTL, lease expirado) classificada %q, quer WaitingLegitimate — falso-positivo de zombi", got)
	}
	// CheckDeadlines ainda NÃO mata (dentro do TTL).
	if s, fired, err := m.CheckDeadlines(ctx); err != nil || fired || s != state.WaitingOnHuman {
		t.Fatalf("CheckDeadlines dentro do TTL: s=%q fired=%v err=%v, quer waiting_on_human/false/nil", s, fired, err)
	}

	// Cruza a fronteira do TTL do gate (100%): fail-closed.
	clk.Advance(time.Nanosecond)

	rl = RunLivenessFrom(m.Current(), workLeaseExpired, gate, entered)
	if got := classifier.Classify(ctx, rl); got != GateExpired {
		t.Fatalf("ao exceder o TTL do gate, classificacao %q, quer GateExpired", got)
	}
	// E a Machine mata fail-closed: waiting_on_human → killed.
	s, fired, err := m.CheckDeadlines(ctx)
	if err != nil {
		t.Fatalf("CheckDeadlines apos TTL: err=%v", err)
	}
	if !fired || s != state.Killed {
		t.Fatalf("CheckDeadlines apos TTL: s=%q fired=%v, quer killed/true (fail-closed ADR-013)", s, fired)
	}

	// Pós-morte: o classificador vê Terminal (nunca zombi).
	rl = RunLivenessFrom(m.Current(), workLeaseExpired, gate, m.EnteredAt())
	if got := classifier.Classify(ctx, rl); got != Terminal {
		t.Fatalf("run killed classificado %q, quer Terminal", got)
	}
}

// TestWaitFullTTLNeverZombieWhileGateEnforced reforça: durante toda a janela de
// aprovação (amostragem densa), enquanto dentro do gate a classificação é sempre
// WaitingLegitimate — nunca Zombie — apesar do lease de trabalho expirado.
func TestWaitFullTTLNeverZombieWhileGateEnforced(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const ttl = time.Hour
	clk := newClock()
	entered := clk.Now()
	gate, err := NewWaitingGate(ttl, WithGateClock(clk))
	if err != nil {
		t.Fatal(err)
	}
	c := NewZombieClassifier()

	for elapsed := time.Duration(0); elapsed < ttl; elapsed += ttl / 20 {
		rl := RunLivenessFrom(state.WaitingOnHuman, true, gate, entered)
		if got := c.Classify(ctx, rl); got != WaitingLegitimate {
			t.Fatalf("aos %v de espera: %q, quer WaitingLegitimate", elapsed, got)
		}
		clk.Advance(ttl / 20)
	}
	// Agora >= TTL.
	rl := RunLivenessFrom(state.WaitingOnHuman, true, gate, entered)
	if got := c.Classify(ctx, rl); got != GateExpired {
		t.Fatalf("ao/apos o TTL: %q, quer GateExpired", got)
	}
}
