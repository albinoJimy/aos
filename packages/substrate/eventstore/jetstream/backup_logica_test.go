package jetstream

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// backup_logica_test.go — a validação do lote de restauro, SEM cluster.
//
// A forma do lote é decidida antes de qualquer rede, e de propósito: um lote malformado
// nunca chega a tocar no log. Como no substrato replicado um restauro falhado a meio NÃO
// SE LIMPA (deny_purge), a validação prévia é a única parte do fail-closed que sobra —
// tudo o resto se resolve por retoma. Testá-la aqui é testá-la onde a CI corre sempre.

// evRestauro fabrica um evento de restauro com envelope preenchido.
func evRestauro(stream string, seq uint64, id string) eventstore.Event {
	return eventstore.Event{
		EventID:  id,
		StreamID: stream,
		Seq:      seq,
		Type:     "aos.teste.v1",
		Ts:       "2026-09-01T00:00:00Z",
	}
}

func TestIngestStream_RecusaAFormaDoLoteAntesDeTocarNaRede(t *testing.T) {
	// Store SEM ligação: se qualquer destes casos chegasse ao servidor, o teste
	// entrava em pânico no `s.streams` nil em vez de passar. É essa a prova de que a
	// validação acontece ANTES da rede, e não uma afirmação sobre ela.
	s := &Store{prefixo: "aos.es.teste"}
	ctx := context.Background()

	casos := []struct {
		nome  string
		lote  []eventstore.Event
		querE error
	}{
		{
			"stream_id do evento não é o do alvo",
			[]eventstore.Event{evRestauro("outro", 1, "e1")},
			eventstore.ErrRestoreEnvelope,
		},
		{
			"event_id vazio",
			[]eventstore.Event{evRestauro("run-a", 1, "")},
			eventstore.ErrRestoreEnvelope,
		},
		{
			"buraco no meio do lote",
			[]eventstore.Event{evRestauro("run-a", 1, "e1"), evRestauro("run-a", 3, "e3")},
			eventstore.ErrRestoreOrder,
		},
		{
			"lote fora de ordem",
			[]eventstore.Event{evRestauro("run-a", 2, "e2"), evRestauro("run-a", 1, "e1")},
			eventstore.ErrRestoreOrder,
		},
		{
			"seq repetido",
			[]eventstore.Event{evRestauro("run-a", 1, "e1"), evRestauro("run-a", 1, "e1")},
			eventstore.ErrRestoreOrder,
		},
		{
			"começa em 0 — o seq do AOS é gapless desde 1",
			[]eventstore.Event{evRestauro("run-a", 0, "e0")},
			eventstore.ErrRestoreOrder,
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			err := s.IngestStream(ctx, "run-a", c.lote)
			if !errors.Is(err, c.querE) {
				t.Fatalf("IngestStream = %v, quer %v", err, c.querE)
			}
		})
	}
}

func TestIngestStream_LoteVazioNaoEErro(t *testing.T) {
	// Um restauro de um stream sem eventos até ao alvo não é uma falha — é um
	// não-acontecimento. O restaurador salta-o, e a porta tem de o tolerar.
	s := &Store{prefixo: "aos.es.teste"}
	if err := s.IngestStream(context.Background(), "run-a", nil); err != nil {
		t.Fatalf("lote vazio devia ser no-op: %v", err)
	}
}

func TestIngestStream_RecusaFechadoECancelado(t *testing.T) {
	lote := []eventstore.Event{evRestauro("run-a", 1, "e1")}

	fechado := &Store{prefixo: "aos.es.teste"}
	fechado.fechado = true
	if err := fechado.IngestStream(context.Background(), "run-a", lote); !errors.Is(err, eventstore.ErrClosed) {
		t.Errorf("store fechado: %v, quer ErrClosed", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &Store{prefixo: "aos.es.teste"}
	if err := s.IngestStream(ctx, "run-a", lote); !errors.Is(err, context.Canceled) {
		t.Errorf("ctx cancelado: %v, quer context.Canceled", err)
	}
	// O MESMO para as duas portas de leitura: um ctx já cancelado não vira ida à rede.
	if _, err := s.StreamHead(ctx, "run-a"); !errors.Is(err, context.Canceled) {
		t.Errorf("StreamHead com ctx cancelado: %v, quer context.Canceled", err)
	}
}

func TestIngestStream_StreamIDNaoRepresentavelERecusado(t *testing.T) {
	// Um stream_id que escapa para um subject vizinho não pode ser alvo de restauro —
	// os eventos aterrariam noutro stream, com o envelope de origem intacto, o que é a
	// forma mais convincente possível de corromper um log.
	s := &Store{prefixo: "aos.es.teste"}
	err := s.IngestStream(context.Background(), "run.com.ponto",
		[]eventstore.Event{evRestauro("run.com.ponto", 1, "e1")})
	if err == nil || !strings.Contains(err.Error(), "stream") {
		t.Fatalf("stream_id não representável devia ser recusado, veio %v", err)
	}
}
