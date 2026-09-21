package state

// AOS-419 — BACKSTOP DE WALL-CLOCK DAS ESPERAS NÃO-HUMANAS (eixo do DEF-906).
//
// O defeito medido: o switch de [Machine.CheckDeadlines] só tratava `waiting_on_human` e
// `running`. Um run em `waiting_on_tool` (activity externa que nunca responde) ou em
// `paused` (steer aceite e esquecido) não tinha prazo NENHUM — e a segunda via também não
// o apanhava, porque o disjuntor de EPIC-08 é no-op fora de `running`.
//
// Estes testes são o oráculo do fecho. Todos usam RELÓGIO MANUAL: nenhuma asserção
// depende de `time.Now()` nem de sleeps.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// razaoBackstop é o rótulo de auditoria esperado, escrito à MÃO e não como referência a
// [ReasonSuspensionBackstop]. É deliberado: assim este ficheiro COMPILA contra o código da
// base (sem a constante nova), e a prova de falha-antes é uma falha de ASSERÇÃO com saída
// legível — não um erro de compilação, que não distingue "o backstop não existe" de
// "escrevi mal o nome". Escrito à mão, é também o ORÁCULO do rótulo: o valor é contrato de
// auditoria (é o que o operador procura no log), pelo que mudá-lo em [ReasonSuspensionBackstop]
// tem de partir um teste, e não passar em silêncio por a asserção seguir a constante.
const razaoBackstop = "suspension_wall_clock_exceeded"

// TestAOS419_BackstopMataWaitingOnToolPendurado é o caso do DEF-906 à letra: uma activity
// externa que nunca responde. Antes de AOS-419 este teste falha — CheckDeadlines devolve
// (waiting_on_tool, false) por muito que o relógio avance.
func TestAOS419_BackstopMataWaitingOnToolPendurado(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	clk := newManualClock()
	m := mustMachine(t, st, "run-419-tool", WithClock(clk), WithRunWallClock(time.Minute))

	mustSeq(t, ctx, m, []step{{Running, tok}, {WaitingOnTool, TransitionEvent{Reason: "activity"}}})

	// Dentro do tecto: a espera é LEGÍTIMA e não morre.
	clk.Advance(59 * time.Second)
	if s, fired, err := m.CheckDeadlines(ctx); err != nil || fired || s != WaitingOnTool {
		t.Fatalf("dentro do tecto a espera não pode morrer: s=%q fired=%v err=%v", s, fired, err)
	}

	// Fronteira INCLUSIVA (o mesmo critério de running/waiting_on_human).
	clk.Advance(time.Second)
	s, fired, err := m.CheckDeadlines(ctx)
	if err != nil || !fired || s != TimedOut {
		t.Fatalf("no tecto o backstop TEM de disparar: s=%q fired=%v err=%v; quero timed_out/true", s, fired, err)
	}
	if r := lastReason(t, st, "run-419-tool"); r != razaoBackstop {
		t.Fatalf("razão=%q; quero %q (a auditoria tem de distinguir morrer-pendurado de morrer-a-trabalhar)", r, razaoBackstop)
	}
	// E é DURÁVEL: um worker novo que releia o log adopta o terminal.
	m2 := mustMachine(t, st, "run-419-tool", WithClock(clk))
	if got, err := m2.Rebuild(ctx); err != nil || got != TimedOut {
		t.Fatalf("Rebuild=%q err=%v; quero timed_out (o backstop tem de sobreviver a crash)", got, err)
	}
}

// TestAOS419_PausaNaoMorrePeloTectoDoTrabalho é a decisão que a revisão adversarial
// forçou, e é uma GUARDA, não uma prova do backstop.
//
// `paused` tem dois produtores com razões duráveis distintas: o disjuntor de orçamento
// (`budget_breaker_tripped`) e a PAUSA GRACIOSA DE UM OPERADOR (`steer_graceful_pause`).
// Esta máquina não retém a razão de ENTRADA, pelo que um backstop em `paused` mataria os
// dois pelo tecto do TRABALHO — e um `/pause` de operador morreria aos 30 min por omissão,
// irrecuperável, porque `timed_out` é absorvente e o `Resume` exige `paused`.
//
// É o mesmo argumento com que este ticket isenta `waiting_on_human`. Estender o backstop a
// `paused` exige reter a razão e isentar a pausa humana — desenho novo, decisão do dono.
func TestAOS419_PausaNaoMorrePeloTectoDoTrabalho(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	clk := newManualClock()
	m := mustMachine(t, st, "run-419-paused", WithClock(clk), WithRunWallClock(time.Minute))

	mustSeq(t, ctx, m, []step{{Running, tok}, {Paused, TransitionEvent{Reason: "steer_graceful_pause"}}})

	clk.Advance(10 * time.Minute)
	s, fired, err := m.CheckDeadlines(ctx)
	if err != nil {
		t.Fatalf("CheckDeadlines: %v", err)
	}
	if fired || s != Paused {
		t.Fatalf("uma pausa de operador morreu pelo tecto do TRABALHO: s=%q fired=%v — o backstop não distingue a pausa humana do esquecimento (AOS-419)", s, fired)
	}
}

// TestAOS419_ArestasDeBackstopNaTabela fixa as DUAS arestas novas na tabela declarativa —
// e, no mesmo varrimento, que nenhuma outra saída apareceu de contrabando nesses dois
// estados (uma espera não ganha caminho para complete, failed ou killed).
func TestAOS419_ArestasDeBackstopNaTabela(t *testing.T) {
	for _, from := range []State{WaitingOnTool, Paused} {
		if !IsValidTransition(from, TimedOut) {
			t.Errorf("%q→timed_out TEM de ser válida (é o backstop do DEF-906)", from)
		}
		var saidas []State
		for _, to := range AllStates {
			if IsValidTransition(from, to) {
				saidas = append(saidas, to)
			}
		}
		if len(saidas) != 2 {
			t.Errorf("%q deve ter exactamente 2 saídas (esta asserção CONTA-AS; quem fixa QUAIS é o oráculo de transitions_test.go), tem %v", from, saidas)
		}
	}
	// waiting_on_human NÃO ganha aresta nova: a sua saída fail-closed é → killed (ADR-013).
	if IsValidTransition(WaitingOnHuman, TimedOut) {
		t.Error("waiting_on_human→timed_out não existe: a deliberação humana morre por TTL para killed (ADR-013)")
	}
}

// TestAOS419_RetomaLevaOTectoInteiro prova a semântica POR-SEGMENTO no lado da espera: o
// backstop mede o tempo desde a ENTRADA na espera, pelo que uma espera que retoma a tempo
// não fica "meio morta" — nem acumula entre segmentos.
func TestAOS419_RetomaLevaOTectoInteiro(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	clk := newManualClock()
	m := mustMachine(t, st, "run-419-segmento", WithClock(clk), WithRunWallClock(time.Minute))
	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := m.Transition(ctx, WaitingOnTool, TransitionEvent{}); err != nil {
			t.Fatal(err)
		}
		clk.Advance(59 * time.Second)
		if s, fired, _ := m.CheckDeadlines(ctx); fired {
			t.Fatalf("ciclo %d: a espera dentro do tecto não pode morrer (s=%q)", i, s)
		}
		if err := m.Transition(ctx, Running, TransitionEvent{}); err != nil {
			t.Fatal(err)
		}
		clk.Advance(30 * time.Second)
		if s, fired, _ := m.CheckDeadlines(ctx); fired {
			t.Fatalf("ciclo %d: o segmento de running também recomeça (s=%q)", i, s)
		}
	}
	// Mais de 7 minutos decorridos no total e o run está vivo: o tecto é por-segmento.
	if s := m.Current(); s != Running {
		t.Fatalf("estado=%q; quero running (nenhum segmento excedeu o tecto)", s)
	}
}

// TestAOS419_BackstopFailClosedNaFalhaDoEventStore: o backstop é uma transição durável
// como as outras — se o Event Store recusar, NADA muda (nem o estado persistido nem o
// in-memory) e o erro SOBE para quem varre (que re-tenta no tick seguinte). Um backstop
// que engolisse o erro deixaria o operador a ler `paused` sobre um run já condenado.
func TestAOS419_BackstopFailClosedNaFalhaDoEventStore(t *testing.T) {
	ctx := context.Background()
	base := newStore(t)
	st := &failingStore{EventStore: base}
	clk := newManualClock()
	m := mustMachine(t, st, "run-419-failclosed", WithClock(clk), WithRunWallClock(time.Minute))
	mustSeq(t, ctx, m, []step{{Running, tok}, {WaitingOnTool, TransitionEvent{}}})

	boom := errors.New("quórum perdido")
	st.failNext, st.failErr = true, boom
	clk.Advance(2 * time.Minute)

	s, fired, err := m.CheckDeadlines(ctx)
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v; quero o erro do Event Store (fail-closed, não engolido)", err)
	}
	if fired || s != WaitingOnTool {
		t.Fatalf("com o ES a recusar nada transita: s=%q fired=%v", s, fired)
	}
	if m.Current() != WaitingOnTool {
		t.Fatalf("estado in-memory=%q; quero waiting_on_tool intacto", m.Current())
	}
	// Re-tentativa no tick seguinte (o ES já aceita): agora sim.
	if s, fired, err := m.CheckDeadlines(ctx); err != nil || !fired || s != TimedOut {
		t.Fatalf("re-tentativa: s=%q fired=%v err=%v; quero timed_out/true", s, fired, err)
	}
}

// TestAOS419_SemTectoNaoHaBackstop é CONTROLO NEGATIVO (passa DOS DOIS LADOS da correcção,
// de propósito): com o tecto desligado (0, o default) o comportamento de antes de AOS-419
// mantém-se intacto — uma espera atravessa dez anos sem morrer. O que ele guarda é a
// compatibilidade: o backstop não se auto-arma em quem não o configurou.
func TestAOS419_SemTectoNaoHaBackstop(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	clk := newManualClock()
	m := mustMachine(t, st, "run-419-sem-tecto", WithClock(clk)) // sem WithRunWallClock
	mustSeq(t, ctx, m, []step{{Running, tok}, {Paused, TransitionEvent{}}})
	clk.Advance(87600 * time.Hour) // dez anos, a medição do DEF-906
	if s, fired, err := m.CheckDeadlines(ctx); err != nil || fired || s != Paused {
		t.Fatalf("sem tecto configurado nada dispara: s=%q fired=%v err=%v", s, fired, err)
	}
}

// TestAOS419_DeliberacaoHumanaNaoMorrePeloTectoDeMaquina é CONTROLO NEGATIVO (passa dos
// dois lados): o tecto de wall-clock NÃO se estende a `waiting_on_human`. É a fronteira
// que AOS-263 fixou no nó — a deliberação humana não paga o tecto do trabalho — e que este
// ticket não pode quebrar ao alargar o âmbito do mesmo tecto às esperas NÃO-humanas.
func TestAOS419_DeliberacaoHumanaNaoMorrePeloTectoDeMaquina(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	clk := newManualClock()
	m := mustMachine(t, st, "run-419-humano", WithClock(clk), WithRunWallClock(time.Minute))
	mustSeq(t, ctx, m, []step{{Running, tok}, {WaitingOnHuman, TransitionEvent{Reason: "gate"}}})
	clk.Advance(24 * time.Hour)
	if s, fired, err := m.CheckDeadlines(ctx); err != nil || fired || s != WaitingOnHuman {
		t.Fatalf("o gate humano não morre pelo tecto de máquina: s=%q fired=%v err=%v", s, fired, err)
	}
	// E o TTL próprio continua a matar fail-closed para killed (ADR-013), não para timed_out.
	m2 := mustMachine(t, st, "run-419-humano-ttl", WithClock(clk),
		WithRunWallClock(time.Minute), WithHumanApprovalTTL(time.Hour))
	mustSeq(t, ctx, m2, []step{{Running, tok}, {WaitingOnHuman, TransitionEvent{}}})
	clk.Advance(2 * time.Hour)
	if s, fired, err := m2.CheckDeadlines(ctx); err != nil || !fired || s != Killed {
		t.Fatalf("com TTL o gate humano vai para killed: s=%q fired=%v err=%v", s, fired, err)
	}
	if r := lastReason(t, st, "run-419-humano-ttl"); r != ReasonHumanTimeout {
		t.Fatalf("razão=%q; quero %q", r, ReasonHumanTimeout)
	}
}
