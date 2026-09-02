package control_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/aos-ref/kernel/agent-runtime/control"
	"github.com/aos-ref/kernel/agent-runtime/state"
	"github.com/aos-ref/substrate/eventstore"
)

// ---------------------------------------------------------------------------
// faultStore — embrulha um Event Store real e injecta falhas de Append SELECTIVAS
// (por tipo de evento) para exercitar os ramos de falha/divergência do canal sem
// perturbar as escritas da máquina de estados (que usa o store subjacente directo).
// ---------------------------------------------------------------------------

type faultStore struct {
	inner  control.EventStore
	mu     sync.Mutex
	failOn map[string]error
}

func newFaultStore(inner control.EventStore) *faultStore {
	return &faultStore{inner: inner, failOn: map[string]error{}}
}

func (f *faultStore) failType(t string, err error) {
	f.mu.Lock()
	f.failOn[t] = err
	f.mu.Unlock()
}

func (f *faultStore) clear(t string) {
	f.mu.Lock()
	delete(f.failOn, t)
	f.mu.Unlock()
}

func (f *faultStore) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	f.mu.Lock()
	err := f.failOn[in.Type]
	f.mu.Unlock()
	if err != nil {
		return eventstore.AppendResult{}, err
	}
	return f.inner.Append(ctx, streamID, in, opts...)
}

func (f *faultStore) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	return f.inner.Read(ctx, streamID, fromSeq)
}

// errGate é um [control.StateGate] falso que devolve erros injectados — usado para
// isolar os ramos em que a MATERIALIZAÇÃO da transição falha (independente da máquina
// real de AOS-017).
type errGate struct{ pauseErr, resumeErr error }

func (g errGate) Pause(context.Context, string) error  { return g.pauseErr }
func (g errGate) Resume(context.Context, string) error { return g.resumeErr }

// ---------------------------------------------------------------------------
// Q1 — AUDIT-FIRST: falha do audit control.resume deixa máquina E projecção
// consistentes através de Rebuild (sem re-pausa espúria nem re-aplicação).
// ---------------------------------------------------------------------------

func TestResume_AuditFailureKeepsMachineAndProjectionConsistent(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	runID := "run-audit-fail"

	fs := newFaultStore(st)
	ch, err := control.NewChannel(fs, a, control.WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}
	// A máquina usa o store REAL (não falha) — só o canal vê a falha injectada.
	m, gate := runningMachine(t, st, runID)

	if err := ch.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.GracefulPause(ctx, runID, gate); err != nil {
		t.Fatal(err)
	}
	correction := []byte("não percas isto quando o audit falhar")
	if err := ch.Steer(ctx, runID, correction, signed(t, a, runID, control.SignalSteer, correction)); err != nil {
		t.Fatal(err)
	}
	if m.Current() != state.Paused {
		t.Fatalf("pré-condição: máquina %s, quer paused", m.Current())
	}

	// Injecta a falha do audit control.resume.
	boom := errors.New("event store indisponível")
	fs.failType(control.EventTypeControlResume, boom)

	if _, err := ch.Resume(ctx, runID, signed(t, a, runID, control.SignalResume, nil), gate); !errors.Is(err, boom) {
		t.Fatalf("Resume err = %v, quer a falha do audit", err)
	}

	// AUDIT-FIRST: a transição NUNCA foi tentada — a máquina fica paused.
	if m.Current() != state.Paused {
		t.Fatalf("máquina = %s, quer paused (audit-first: transição não materializada)", m.Current())
	}
	// Projecção intacta: pausa e correcção pendentes.
	if !ch.PendingPause(runID) {
		t.Fatal("pausa pendente perdida após falha do audit")
	}
	if got, ok := ch.PendingCorrection(runID); !ok || string(got) != string(correction) {
		t.Fatalf("correcção = %q (ok=%v), quer intacta %q", got, ok, correction)
	}

	// CONSISTÊNCIA POR REBUILD: um canal fresco reconstrói a MESMA projecção (paused +
	// correcção) e a máquina reconstrói-se para paused a partir do MESMO log.
	fresh := newChannel(t, st, a)
	if err := fresh.Rebuild(ctx, runID); err != nil {
		t.Fatal(err)
	}
	if !fresh.PendingPause(runID) {
		t.Fatal("Rebuild: pausa pendente perdida")
	}
	if got, ok := fresh.PendingCorrection(runID); !ok || string(got) != string(correction) {
		t.Fatalf("Rebuild: correcção = %q (ok=%v), quer %q", got, ok, correction)
	}
	m2, err := state.NewMachine(st, runID)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := m2.Rebuild(ctx); err != nil || s != state.Paused {
		t.Fatalf("Machine.Rebuild = %s, %v; quer paused", s, err)
	}

	// Recuperado o Event Store, a retoma tem sucesso e aplica a correcção intacta.
	fs.clear(control.EventTypeControlResume)
	corr, err := fresh.Resume(ctx, runID, signed(t, a, runID, control.SignalResume, nil), control.NewMachineGate(m2))
	if err != nil {
		t.Fatalf("Resume pós-recuperação: %v", err)
	}
	if !corr.Present || string(corr.Value) != string(correction) {
		t.Fatalf("correcção aplicada = %+v, quer %q", corr, correction)
	}
	if m2.Current() != state.Running {
		t.Fatalf("estado = %s, quer running", m2.Current())
	}
}

// TestResume_TransitionFailureAfterAudit documenta o RESIDUAL fail-closed: se o audit
// tem sucesso mas a materialização da transição falha, o control.resume já é durável e
// a projecção já consumiu a correcção, MAS a máquina permanece no lado seguro (paused).
func TestResume_TransitionFailureAfterAuditIsFailClosed(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	runID := "run-transition-fail"

	ch := newChannel(t, st, a)
	m, gate := runningMachine(t, st, runID)
	if err := ch.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.GracefulPause(ctx, runID, gate); err != nil {
		t.Fatal(err)
	}
	correction := []byte("c")
	if err := ch.Steer(ctx, runID, correction, signed(t, a, runID, control.SignalSteer, correction)); err != nil {
		t.Fatal(err)
	}

	// A transição falha (gate falso), mas o audit já correu com o store real.
	boom := errors.New("transição de estado indisponível")
	if _, err := ch.Resume(ctx, runID, signed(t, a, runID, control.SignalResume, nil), errGate{resumeErr: boom}); !errors.Is(err, boom) {
		t.Fatalf("Resume err = %v, quer a falha da transição", err)
	}
	// A máquina real NÃO foi tocada pelo gate falso — fica paused (fail-closed).
	if m.Current() != state.Paused {
		t.Fatalf("máquina = %s, quer paused (lado seguro)", m.Current())
	}
	// O control.resume é durável: um canal fresco reconstrói a projecção coerente com o LOG.
	// O invariante testado é esse — projecção == Rebuild —, e não um valor em concreto.
	fresh := newChannel(t, st, a)
	if err := fresh.Rebuild(ctx, runID); err != nil {
		t.Fatal(err)
	}
	if fresh.PendingPause(runID) {
		t.Fatal("Rebuild: pausa ainda pendente apesar do control.resume durável")
	}
	// A CORRECÇÃO FICA PENDENTE, e desde AOS-292 é isso o correcto. O `Resume` deixou de a
	// consumir — quem a consome é a ENTREGA ao loop
	// ([SteerChannel.ConsumeCorrection]/`control.correction_consumed`), e aqui não houve
	// entrega nenhuma: a transição falhou e o run nem sequer voltou a correr.
	//
	// Antes, o resume consumia-a, e uma retoma falhada ficava com a máquina em paused e a
	// correcção do operador PERDIDA — a reconciliação futura de que o comentário do Resume
	// fala retomaria o run sem a correcção que alguém escreveu. Manter pendente é o lado
	// seguro nos dois eixos, não só no da máquina.
	if _, ok := fresh.PendingCorrection(runID); !ok {
		t.Fatal("Rebuild: a correcção devia continuar PENDENTE — a retoma nao a consome (AOS-292), e esta nem transitou")
	}
}

// ---------------------------------------------------------------------------
// Q2 — reconciliação fail-closed do dedup (duplicado ctrl-N não corresponde ao pedido).
// ---------------------------------------------------------------------------

func TestAppendControl_DuplicateStepIDFailsClosed(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	runID := "run-dup-diverge"

	// ch1 grava legitimamente ctrl-1 = pause.
	ch1 := newChannel(t, st, a)
	if err := ch1.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
		t.Fatal(err)
	}

	// ch2 é FRESCO (nControls=0, sem Rebuild) ⇒ o seu primeiro sinal reusa ctrl-1. Um
	// steer colide com o pause já persistido: o ES devolve StatusDuplicate com o evento
	// ORIGINAL (pause) e descarta o payload novo. A reconciliação tem de recusar.
	ch2 := newChannel(t, st, a)
	correction := []byte("payload novo que o dedup descarta")
	err := ch2.Steer(ctx, runID, correction, signed(t, a, runID, control.SignalSteer, correction))
	if !errors.Is(err, control.ErrControlLogDivergence) {
		t.Fatalf("Steer err = %v, quer ErrControlLogDivergence", err)
	}
	// A projecção NÃO foi mutada a partir do payload descartado.
	if _, ok := ch2.PendingCorrection(runID); ok {
		t.Fatal("projecção mutada a partir de um payload descartado (divergência silenciosa)")
	}
}

// TestAppendControl_BenignDuplicateAccepted cobre o ramo benigno: um duplicado cujo
// sinal bate EXACTAMENTE o pedido (retry idempotente) é aceite e dobrado sem erro.
func TestAppendControl_BenignDuplicateAccepted(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	runID := "run-dup-benign"

	ch1 := newChannel(t, st, a)
	if err := ch1.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
		t.Fatal(err)
	}
	// ch2 fresco reemite o MESMO pause (mesmo kind/emissor/payload) ⇒ ctrl-1 duplicado
	// idêntico ⇒ aceite, projecção actualizada.
	ch2 := newChannel(t, st, a)
	if err := ch2.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
		t.Fatalf("retry benigno recusado: %v", err)
	}
	if !ch2.PendingPause(runID) {
		t.Fatal("duplicado benigno não dobrado na projecção")
	}
}

// ---------------------------------------------------------------------------
// Ramos de falha do Event Store no caminho record (pause/steer).
// ---------------------------------------------------------------------------

func TestRecord_AppendFailureLeavesNoProjection(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	runID := "run-append-fail"
	fs := newFaultStore(st)
	ch, err := control.NewChannel(fs, a, control.WithClock(fixedClock()))
	if err != nil {
		t.Fatal(err)
	}

	boom := errors.New("quórum perdido")
	fs.failType(control.EventTypeControlPause, boom)
	if err := ch.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); !errors.Is(err, boom) {
		t.Fatalf("Pause err = %v, quer a falha do append", err)
	}
	if ch.PendingPause(runID) {
		t.Fatal("pausa pendente apesar da falha do append")
	}
}

// TestGracefulPause_GateError cobre o ramo em que a materialização running→paused falha.
func TestGracefulPause_GateError(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	ch := newChannel(t, st, a)
	runID := "run-gate-err"

	if err := ch.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("máquina recusou a pausa")
	did, err := ch.GracefulPause(ctx, runID, errGate{pauseErr: boom})
	if !errors.Is(err, boom) {
		t.Fatalf("GracefulPause err = %v, quer a falha do gate", err)
	}
	if did {
		t.Fatal("GracefulPause reportou pausa apesar do erro do gate")
	}
}

// ---------------------------------------------------------------------------
// Q4 — CONCORRÊNCIA: o canal sob contenção (-race). Exactamente uma retoma vence;
// o log e a projecção mantêm-se consistentes.
// ---------------------------------------------------------------------------

// TestConcurrent_ResumeExactlyOneWins lança N goroutines a retomar o MESMO run paused
// em simultâneo: exactamente UMA consome a correcção e materializa a transição; as
// restantes são recusadas (state.ErrInvalidTransition) sem re-consumir a correcção.
func TestConcurrent_ResumeExactlyOneWins(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	runID := "run-concurrent-resume"
	ch := newChannel(t, st, a)
	m, gate := runningMachine(t, st, runID)

	if err := ch.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := ch.GracefulPause(ctx, runID, gate); err != nil {
		t.Fatal(err)
	}
	correction := []byte("a única correcção que vence a corrida")
	if err := ch.Steer(ctx, runID, correction, signed(t, a, runID, control.SignalSteer, correction)); err != nil {
		t.Fatal(err)
	}

	const goroutines = 8
	resume := signed(t, a, runID, control.SignalResume, nil)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var wins int
	var otherErrs []error
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			corr, err := ch.Resume(ctx, runID, resume, gate)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
				if !corr.Present || string(corr.Value) != string(correction) {
					otherErrs = append(otherErrs, fmt.Errorf("vencedor sem a correcção: %+v", corr))
				}
			case errors.Is(err, state.ErrInvalidTransition):
				// perdedor esperado
			default:
				otherErrs = append(otherErrs, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Fatalf("retomas vencedoras = %d, quer exactamente 1", wins)
	}
	for _, e := range otherErrs {
		t.Errorf("erro inesperado numa retoma perdedora: %v", e)
	}
	if m.Current() != state.Running {
		t.Fatalf("estado final = %s, quer running", m.Current())
	}
	// A ENTREGA consome a correcção (AOS-292): o resume levanta só a pausa. Fecha-se o ciclo
	// aqui para que a asserção de consistência abaixo continue a ser sobre um ciclo COMPLETO.
	if _, err := ch.ConsumeCorrection(ctx, runID); err != nil {
		t.Fatalf("ConsumeCorrection: %v", err)
	}
	// Consistência final: um canal fresco reconstrói a mesma projecção (ciclo consumido).
	fresh := newChannel(t, st, a)
	if err := fresh.Rebuild(ctx, runID); err != nil {
		t.Fatal(err)
	}
	if fresh.PendingPause(runID) {
		t.Fatal("Rebuild: pausa pendente após o ciclo completo")
	}
	if _, ok := fresh.PendingCorrection(runID); ok {
		t.Fatal("Rebuild: correcção pendente após o ciclo completo")
	}
}

// TestConcurrent_SignalsAndReads martela Pause/Steer e leituras concorrentes no mesmo
// run: o mutex do canal serializa a projecção e a atribuição de ctrl-N, e a projecção
// in-memory final coincide com a reconstruída do log (fidelidade sob contenção).
func TestConcurrent_SignalsAndReads(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	a := authWith(t)
	runID := "run-concurrent-signals"
	ch := newChannel(t, st, a)

	const writers = 6
	var wg sync.WaitGroup
	start := make(chan struct{})

	// Escritores de steer com correcções distintas.
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			corr := []byte(fmt.Sprintf("correcção-%d", n))
			if err := ch.Steer(ctx, runID, corr, signed(t, a, runID, control.SignalSteer, corr)); err != nil {
				t.Errorf("Steer concorrente: %v", err)
			}
		}(i)
	}
	// Um escritor de pause concorrente.
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		if err := ch.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
			t.Errorf("Pause concorrente: %v", err)
		}
	}()
	// Leitores concorrentes (exercitam o mutex partilhado sob -race).
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = ch.PendingPause(runID)
			_, _ = ch.PendingCorrection(runID)
		}()
	}

	close(start)
	wg.Wait()

	// A projecção in-memory final == a reconstruída do log (mesma última correcção).
	inMemCorr, inMemOK := ch.PendingCorrection(runID)
	fresh := newChannel(t, st, a)
	if err := fresh.Rebuild(ctx, runID); err != nil {
		t.Fatal(err)
	}
	rebuiltCorr, rebuiltOK := fresh.PendingCorrection(runID)
	if inMemOK != rebuiltOK || string(inMemCorr) != string(rebuiltCorr) {
		t.Fatalf("projecção in-memory (%q,%v) diverge da reconstruída (%q,%v)",
			inMemCorr, inMemOK, rebuiltCorr, rebuiltOK)
	}
	if ch.PendingPause(runID) != fresh.PendingPause(runID) {
		t.Fatal("pausa pendente in-memory diverge da reconstruída")
	}
}

// leituraTruncada devolve o stream SEM o último evento — simula a janela em que o `Rebuild`
// lê, e um sinal é aceite antes de a projecção ser instalada.
type leituraTruncada struct {
	*eventstore.Store
	truncar bool
}

func (s *leituraTruncada) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	evs, err := s.Store.Read(ctx, streamID, fromSeq)
	if err != nil || !s.truncar || len(evs) == 0 {
		return evs, err
	}
	return evs[:len(evs)-1], nil
}

// TestAOS293_RebuildNaoRegridiUmaProjeccaoMaisAVANCADA fixa a guarda que AOS-293 acrescentou.
//
// O `Rebuild` lê o stream FORA do lock — de propósito, porque prender um mutex durante I/O é o
// defeito que AOS-291 removeu do disjuntor. Isso abre uma janela: um sinal aceite entre a
// leitura e a instalação ficaria de fora da projecção substituída, com o evento durável no log
// e a memória atrasada.
//
// A janela existia antes e era inalcançável — `Rebuild` não tinha chamador de produção. AOS-293
// passou a chamá-lo em cada hospedagem e tornou-a real, por isso a guarda entra com ele.
func TestAOS293_RebuildNaoRegridiUmaProjeccaoMaisAVANCADA(t *testing.T) {
	ctx := context.Background()
	const runID = "run-rebuild-corrida"
	a := authWith(t)
	st := newStore(t)
	truncado := &leituraTruncada{Store: st}
	ch, err := control.NewChannel(truncado, a)
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}

	// Dois sinais reais: a projecção em memória fica com nControls == 2.
	if err := ch.Pause(ctx, runID, signed(t, a, runID, control.SignalPause, nil)); err != nil {
		t.Fatal(err)
	}
	corr := []byte("correccao a preservar")
	if err := ch.Steer(ctx, runID, corr, signed(t, a, runID, control.SignalSteer, corr)); err != nil {
		t.Fatal(err)
	}

	// O Rebuild vê o stream SEM o último evento — exactamente o que uma corrida produz.
	truncado.truncar = true
	if err := ch.Rebuild(ctx, runID); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// A projecção NÃO pode ter regredido: o steer continua lá.
	got, ok := ch.PendingCorrection(runID)
	if !ok || string(got) != string(corr) {
		t.Fatalf("o Rebuild instalou uma projeccao ATRASADA e perdeu o sinal concorrente; PendingCorrection=(%q,%v)", got, ok)
	}
	if !ch.PendingPause(runID) {
		t.Fatal("a pausa tambem se perdeu")
	}
}
