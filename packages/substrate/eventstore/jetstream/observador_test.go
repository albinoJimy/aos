package jetstream_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/jetstream"
)

// observadorEspiao regista o que lhe chega. É seguro para uso concorrente porque o
// Append do adaptador é.
type observadorEspiao struct {
	mu         sync.Mutex
	committed  []uint64
	duplicados []uint64
	rejeitados []error
	latencias  []time.Duration
}

func (o *observadorEspiao) AppendCommitted(_ string, seq uint64, lat time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.committed = append(o.committed, seq)
	o.latencias = append(o.latencias, lat)
}
func (o *observadorEspiao) AppendDuplicate(_ string, seq uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.duplicados = append(o.duplicados, seq)
}
func (o *observadorEspiao) AppendRejected(_ string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.rejeitados = append(o.rejeitados, err)
}
func (o *observadorEspiao) Published(string, uint64, int) {}

// TestObservador_TodaTentativaRejeitadaESinalizada é o que o contrato C2 exige: «toda a
// tentativa REJEITADA — incluindo a violação append-only — é sinalizada via
// AppendRejected», porque é o Observer que torna a rejeição AUDITÁVEL de forma durável.
//
// Sem isto, trocar o substrato para o replicado apagava essa via em silêncio: as
// rejeições continuariam a ser devolvidas ao chamador e deixariam de ser auditadas.
func TestObservador_TodaTentativaRejeitadaESinalizada(t *testing.T) {
	addr := servidor(t)
	espiao := &observadorEspiao{}
	st, err := abrirComOpcoes(t, addr, append(opcoesBase(t, "OBS_"),
		jetstream.ComObservador(espiao))...)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	ctx := context.Background()
	const stream = "run-observado"
	facto := func(tipo string) eventstore.EventInput {
		return eventstore.EventInput{Type: tipo, Payload: json.RawMessage(`{}`)}
	}

	// (1) COMMITTED, com latência.
	if _, err := st.Append(ctx, stream, facto("obs.committed"), eventstore.WithExpectedSeq(0)); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// (2) REJEITADO por afirmar o passado.
	if _, err := st.Append(ctx, stream, facto("obs.passado"), eventstore.WithExpectedSeq(0)); !errors.Is(err, eventstore.ErrAppendOnlyViolation) {
		t.Fatalf("quero E_APPEND_ONLY_VIOLATION, veio %v", err)
	}
	// (3) REJEITADO por afirmar à frente do log.
	if _, err := st.Append(ctx, stream, facto("obs.futuro"), eventstore.WithExpectedSeq(99)); !errors.Is(err, eventstore.ErrSeqConflict) {
		t.Fatalf("quero E_SEQ_CONFLICT, veio %v", err)
	}
	// (4) DUPLICADO.
	dup := facto("obs.dup")
	dup.RunID, dup.StepID = stream, "passo-1"
	if _, err := st.Append(ctx, stream, dup); err != nil {
		t.Fatalf("1.º com chave: %v", err)
	}
	if r, err := st.Append(ctx, stream, dup); err != nil || r.Status != eventstore.StatusDuplicate {
		t.Fatalf("2.º com a mesma chave: status=%v err=%v", r.Status, err)
	}

	espiao.mu.Lock()
	defer espiao.mu.Unlock()
	if len(espiao.committed) != 2 {
		t.Errorf("committed sinalizados = %v, quer 2 (o CAS e o da chave)", espiao.committed)
	}
	if len(espiao.duplicados) != 1 {
		t.Errorf("duplicados sinalizados = %v, quer 1", espiao.duplicados)
	}
	if len(espiao.rejeitados) != 2 {
		t.Fatalf("rejeições sinalizadas = %d (%v), quer 2 — uma rejeição não sinalizada não é auditável",
			len(espiao.rejeitados), espiao.rejeitados)
	}
	if !errors.Is(espiao.rejeitados[0], eventstore.ErrAppendOnlyViolation) ||
		!errors.Is(espiao.rejeitados[1], eventstore.ErrSeqConflict) {
		t.Errorf("as rejeições sinalizadas não distinguem as causas: %v", espiao.rejeitados)
	}
	for i, l := range espiao.latencias {
		if l <= 0 {
			t.Errorf("latência[%d] = %v — uma latência não-positiva torna o sinal inútil", i, l)
		}
	}
}

// TestObservador_SemObservadorNaoRebenta — o default é nop, e o caminho quente não pode
// ter um `if obs != nil` por chamada. Esquecê-lo uma vez seria um panic no caminho mais
// crítico que existe.
func TestObservador_SemObservadorNaoRebenta(t *testing.T) {
	addr := servidor(t)
	st, err := abrirComOpcoes(t, addr, opcoesBase(t, "OBSNIL_")...)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	if _, err := st.Append(context.Background(), "run-sem-obs",
		eventstore.EventInput{Type: "obs.nenhum", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Append sem observador: %v", err)
	}
	// E injectar nil não pode desarmar o default.
	st2, err := abrirComOpcoes(t, addr, append(opcoesBase(t, "OBSNIL2_"), jetstream.ComObservador(nil))...)
	if err != nil {
		t.Fatalf("abrir com observador nil: %v", err)
	}
	if _, err := st2.Append(context.Background(), "run-obs-nil",
		eventstore.EventInput{Type: "obs.nil", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Append com observador nil: %v", err)
	}
}
