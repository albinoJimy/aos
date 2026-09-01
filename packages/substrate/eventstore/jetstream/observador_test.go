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

// rastreadorEspiao regista os spans abertos e os seus atributos.
type rastreadorEspiao struct {
	mu     sync.Mutex
	spans  []*spanEspiao
	fechos int
}

type spanEspiao struct {
	operacao string
	attrs    map[string]any
	dono     *rastreadorEspiao
}

func (r *rastreadorEspiao) Iniciar(ctx context.Context, operacao string) (context.Context, eventstore.Rastro) {
	s := &spanEspiao{operacao: operacao, attrs: map[string]any{}, dono: r}
	r.mu.Lock()
	r.spans = append(r.spans, s)
	r.mu.Unlock()
	return ctx, s
}

func (s *spanEspiao) Atributo(k string, v any) {
	s.dono.mu.Lock()
	s.attrs[k] = v
	s.dono.mu.Unlock()
}

func (s *spanEspiao) Fim() {
	s.dono.mu.Lock()
	s.dono.fechos++
	s.dono.mu.Unlock()
}

// TestRastreio_CadaDesfechoDeixaSpanComACausa — um span que só dissesse «rejected»
// mandaria toda a gente ler os logs. O desfecho E a causa vão no span, e é isso que o
// torna útil numa vista de query-time.
func TestRastreio_CadaDesfechoDeixaSpanComACausa(t *testing.T) {
	addr := servidor(t)
	espiao := &rastreadorEspiao{}
	st, err := abrirComOpcoes(t, addr, append(opcoesBase(t, "RASTRO_"),
		jetstream.ComRastreador(espiao))...)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	ctx := context.Background()
	const stream = "run-rastreado"
	facto := func(tipo string) eventstore.EventInput {
		return eventstore.EventInput{Type: tipo, Payload: json.RawMessage(`{}`)}
	}

	if _, err := st.Append(ctx, stream, facto("rastro.ok"), eventstore.WithExpectedSeq(0)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := st.Append(ctx, stream, facto("rastro.passado"), eventstore.WithExpectedSeq(0)); !errors.Is(err, eventstore.ErrAppendOnlyViolation) {
		t.Fatalf("quero E_APPEND_ONLY_VIOLATION, veio %v", err)
	}
	if _, err := st.Read(ctx, stream, 1); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, err := st.Read(ctx, "run-que-nao-existe", 1); !errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Fatalf("quero E_STREAM_NOT_FOUND, veio %v", err)
	}

	espiao.mu.Lock()
	defer espiao.mu.Unlock()
	if len(espiao.spans) != 4 {
		t.Fatalf("spans abertos = %d, quer 4 (2 appends + 2 reads)", len(espiao.spans))
	}
	if espiao.fechos != len(espiao.spans) {
		t.Errorf("abertos %d spans e fechados %d — um span que não fecha nunca é exportado",
			len(espiao.spans), espiao.fechos)
	}

	esperado := []struct {
		operacao, desfecho, erro string
	}{
		{eventstore.OperacaoAppend, "committed", ""},
		{eventstore.OperacaoAppend, "rejected", "E_APPEND_ONLY_VIOLATION"},
		{eventstore.OperacaoRead, "ok", ""},
		{eventstore.OperacaoRead, "rejected", "E_STREAM_NOT_FOUND"},
	}
	for i, e := range esperado {
		s := espiao.spans[i]
		if s.operacao != e.operacao {
			t.Errorf("span[%d] operação = %q, quer %q", i, s.operacao, e.operacao)
		}
		if got := s.attrs[eventstore.AtributoDesfecho]; got != e.desfecho {
			t.Errorf("span[%d] desfecho = %v, quer %q", i, got, e.desfecho)
		}
		if e.erro != "" {
			if got := s.attrs[eventstore.AtributoErro]; got != e.erro {
				t.Errorf("span[%d] erro = %v, quer %q — sem a CAUSA o span não diz ao operador o que fazer", i, got, e.erro)
			}
		}
		if s.attrs[eventstore.AtributoStream] == nil {
			t.Errorf("span[%d] sem stream_id — não se sabe de que run é", i)
		}
	}
}

// TestRastreio_LigarDepoisDeUsarERECUSADO — a invariante «liga-se antes de usar» é
// IMPOSTA, não recomendada. Um rastreador ligado a meio produziria spans para umas
// operações e não para outras, sem nada a dizê-lo.
func TestRastreio_LigarDepoisDeUsarERECUSADO(t *testing.T) {
	addr := servidor(t)
	st, err := abrirComOpcoes(t, addr, opcoesBase(t, "RASTROTARDE_")...)
	if err != nil {
		t.Fatalf("abrir: %v", err)
	}
	// Antes de usar: aceite.
	if err := st.LigarRastreador(&rastreadorEspiao{}); err != nil {
		t.Fatalf("ligar antes de usar devia ser aceite: %v", err)
	}
	if _, err := st.Append(context.Background(), "run-tarde",
		eventstore.EventInput{Type: "rastro.tarde", Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Depois de usar: recusado.
	if err := st.LigarRastreador(&rastreadorEspiao{}); err == nil {
		t.Fatal("ligar o rastreador DEPOIS do primeiro uso foi aceite — produziria observabilidade " +
			"com buracos silenciosos, que é pior do que nenhuma")
	}
}
