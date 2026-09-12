package eventstore

// AOS-353 — `IngestStream` DO STORE DE REFERÊNCIA NÃO ESCREVIA NO WAL.
//
// # O DEFEITO
//
// `backup.go` aplicava o lote restaurado apenas às réplicas EM MEMÓRIA
// (`r.store(ev.clone())`) e elevava o commit index. Não havia uma única chamada a
// `s.wal`. O contraste com o caminho normal é directo: [Store.Append] persiste ANTES de
// aplicar. Reiniciava-se o nó e o restauro tinha EVAPORADO.
//
// Uma subtileza reduz o alcance sem anular o defeito: no replay de ARRANQUE a omissão é
// CORRECTA — `durable.go` faz `restoreInto(s, events)` ANTES de `s.wal = w`, e reescrever
// duplicaria. O problema é o SEGUNDO uso da mesma porta, com `s.wal != nil`.
//
// # PORQUE SOBREVIVEU
//
// TODOS os testes de restauro do store de referência constroem o destino com
// `mustNew(t)` — `New()` in-memory, NUNCA `Open(path)`. Um restauro para um store com WAL
// seguido de reinício não era exercitado em lado nenhum. Este ficheiro fecha essa lacuna
// pelos dois lados: o que passou a persistir, e o que tem de continuar a NÃO duplicar.

import (
	"context"
	"path/filepath"
	"testing"
)

// eventosParaRestaurar produz um lote com envelope próprio, no molde do que um backup
// devolveria: seq contíguo a partir de 1 e EventID não-vazio.
func eventosParaRestaurar(streamID string, n int) []Event {
	evs := make([]Event, 0, n)
	for i := 1; i <= n; i++ {
		evs = append(evs, Event{
			EventID:  "ev-" + streamID + "-" + string(rune('0'+i)),
			StreamID: streamID,
			Seq:      uint64(i),
			Type:     "t",
			Payload:  []byte(`{"restaurado":true}`),
			RunID:    streamID,
			StepID:   "s" + string(rune('0'+i)),
		})
	}
	return evs
}

// TestAOS353_RestauroSobreStoreDuravelSobreviveAoReinicio é o teste que nasceu VERMELHO:
// depois de reabrir, o stream restaurado não existia.
func TestAOS353_RestauroSobreStoreDuravelSobreviveAoReinicio(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")

	s, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	quero := eventosParaRestaurar("run-restaurado", 3)
	if err := s.IngestStream(ctx, "run-restaurado", quero); err != nil {
		t.Fatalf("IngestStream: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// REINÍCIO. É aqui que o restauro evaporava.
	s2, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("reabrir: %v", err)
	}
	defer s2.Close()
	got, err := s2.Read(ctx, "run-restaurado", 1)
	if err != nil {
		t.Fatalf("Read após reinício = %v — o restauro não sobreviveu", err)
	}
	if len(got) != len(quero) {
		t.Fatalf("após reinício: %d eventos, quero %d — o restauro EVAPOROU", len(got), len(quero))
	}
	// O ENVELOPE é o do backup: um restauro que reatribuísse seria outra história.
	for i := range quero {
		if got[i].EventID != quero[i].EventID || got[i].Seq != quero[i].Seq {
			t.Fatalf("envelope %d: got (%q,%d) quero (%q,%d)",
				i, got[i].EventID, got[i].Seq, quero[i].EventID, quero[i].Seq)
		}
	}
}

// TestAOS353_ReplayDeArranqueNaoDuplica guarda a metade oposta, e é a que a correcção
// podia estropiar: no arranque, `restoreInto` chama a MESMA porta com os eventos que
// acabou de ler do ficheiro. Se persistisse, o WAL duplicaria a cada reinício e o Open
// seguinte recusaria com E_RESTORE_ORDER.
func TestAOS353_ReplayDeArranqueNaoDuplica(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")

	s := openDurable(t, path)
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)
	appendEv(t, s, "run-A", "s2", "t", `{"n":2}`)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	registosApos1 := len(registosNoFicheiro(t, path))

	// Três reinícios seguidos: se o replay reescrevesse, o ficheiro crescia de cada vez.
	for i := 0; i < 3; i++ {
		si, err := Open(path, WithReplicas(1), WithQuorum(1))
		if err != nil {
			t.Fatalf("reinício %d: %v", i, err)
		}
		if err := si.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
		if n := len(registosNoFicheiro(t, path)); n != registosApos1 {
			t.Fatalf("reinício %d: o ficheiro tem %d registos, tinha %d — o replay de arranque DUPLICOU", i, n, registosApos1)
		}
	}

	// E o store continua correcto e escrevível a seguir.
	s2, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("reabrir: %v", err)
	}
	defer s2.Close()
	got, err := s2.Read(ctx, "run-A", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("após três reinícios: %d eventos, quero 2", len(got))
	}
	res := appendEv(t, s2, "run-A", "s3", "t", `{"n":3}`)
	if res.Seq != 3 {
		t.Fatalf("seq = %d, quero 3", res.Seq)
	}
}

// TestAOS353_StoreSemWALContinuaAAceitarRestauro fixa a retro-compatibilidade: um store
// puramente in-memory (`New`) não tem WAL e o restauro tem de continuar a funcionar sem
// tocar em disco nenhum.
func TestAOS353_StoreSemWALContinuaAAceitarRestauro(t *testing.T) {
	ctx := context.Background()
	s, err := New(WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()
	if err := s.IngestStream(ctx, "run-mem", eventosParaRestaurar("run-mem", 2)); err != nil {
		t.Fatalf("IngestStream num store in-memory: %v", err)
	}
	got, err := s.Read(ctx, "run-mem", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d eventos, quero 2", len(got))
	}
}

// TestAOS353_LoteFalhadoNaoDeixaMeioRestauroDuravel prova a reposição ao nível do LOTE.
// Sem ela, um restauro que devolve erro deixaria um prefixo durável — a mesma classe de
// defeito que AOS-348/349 fecham para o append singular.
func TestAOS353_LoteFalhadoNaoDeixaMeioRestauroDuravel(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")

	s, ff := abreComFsyncFalhado(t, path, 2, false) // o 3.º fsync do lote falha
	defer s.Close()

	err := s.IngestStream(ctx, "run-lote", eventosParaRestaurar("run-lote", 4))
	if err == nil {
		t.Fatal("esperava erro do lote")
	}
	if ff.syncs < 3 {
		t.Fatalf("a sonda só viu %d fsync — o lote não chegou ao registo que devia falhar", ff.syncs)
	}
	if evs := registosNoFicheiro(t, path); len(evs) != 0 {
		t.Fatalf("o ficheiro ficou com %d registo(s) de um lote que FALHOU — a reposição do lote não repôs", len(evs))
	}
}
