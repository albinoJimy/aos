package state

import (
	"context"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// aos478_producer_test.go — AOS-478: `run.state.transition` passa a levar a identidade de
// componente da máquina no envelope, e um log ANTIGO (escrito com o envelope vazio) continua
// a reconstruir-se e a aceitar transições novas.

// storeLegado simula o binário anterior ao AOS-478: grava cada transição com o `producer`
// vazio, que é a forma de todos os eventos de transição já gravados em produção.
type storeLegado struct{ *eventstore.Store }

func (s storeLegado) Append(ctx context.Context, stream string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	in.Producer = eventstore.Producer{}
	return s.Store.Append(ctx, stream, in, opts...)
}

func TestAOS478_TransicaoLevaIdentidadeDeComponente(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	m := mustMachine(t, st, "run-478")
	if err := m.Transition(ctx, Running, tok); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	evs, err := st.Read(ctx, "run-478", 1)
	if err != nil || len(evs) != 1 {
		t.Fatalf("Read: err=%v n=%d", err, len(evs))
	}
	if evs[0].Producer.NHIID != DefaultProducerNHI {
		t.Fatalf("producer.nhi_id=%q, quer %q", evs[0].Producer.NHIID, DefaultProducerNHI)
	}
}

// TestAOS478_LogLegadoSemProducerReconstroi é a retro-compatibilidade (AC6): o estado
// reconstruído de um log de envelope vazio é o mesmo, e a transição seguinte, já escrita
// pelo código novo, encadeia por cima sem conflito de seq nem de dedup.
func TestAOS478_LogLegadoSemProducerReconstroi(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	velha := mustMachine(t, storeLegado{st}, "run-legado")
	if err := velha.Transition(ctx, Running, tok); err != nil {
		t.Fatalf("Transition (legado): %v", err)
	}
	if err := velha.Pause(ctx, TransitionEvent{Reason: "operador"}); err != nil {
		t.Fatalf("Pause (legado): %v", err)
	}

	nova := mustMachine(t, st, "run-legado")
	got, err := nova.Rebuild(ctx)
	if err != nil {
		t.Fatalf("Rebuild sobre log legado: %v", err)
	}
	if got != velha.Current() {
		t.Fatalf("estado reconstruído %q != %q do binário anterior", got, velha.Current())
	}
	if err := nova.Resume(ctx, TransitionEvent{Reason: "operador"}); err != nil {
		t.Fatalf("Resume sobre log legado: %v", err)
	}
	evs, err := st.Read(ctx, "run-legado", 1)
	if err != nil || len(evs) != 3 {
		t.Fatalf("Read: err=%v n=%d", err, len(evs))
	}
	if evs[0].Producer.NHIID != "" || evs[1].Producer.NHIID != "" {
		t.Fatalf("o log legado foi reescrito: %+v %+v", evs[0].Producer, evs[1].Producer)
	}
	if evs[2].Producer.NHIID != DefaultProducerNHI {
		t.Fatalf("a transição nova devia levar %q, levou %q", DefaultProducerNHI, evs[2].Producer.NHIID)
	}
}
