package eventstore

// AOS-349 — `desfazer` SOBRE UM WAL ENCOLHIDO TORNAVA DURÁVEL UM APPEND QUE FALHOU.
//
// # O DEFEITO, executado
//
// [wal.desfazer] repunha o WAL chamando `os.Truncate(path, w.tamanho)` com o tamanho que
// tinha ANTES do append falhado. Se o ficheiro tivesse entretanto ENCOLHIDO por baixo — o
// que AOS-346 composto com AOS-347 tornava alcançável, um comando de INSPECÇÃO a levar o
// log de 1480 para 592 bytes —, `w.tamanho` ficava à frente do ficheiro real, e
// `os.Truncate` para um tamanho MAIOR não trunca: ESTENDE, com zeros.
//
//	ficheiro real=592 ; A.wal.tamanho (em memória)=1480 ; DESSINCRONIZADO
//	append com fsync falhado -> desfazer chama os.Truncate(path, 1480) sobre 888 bytes
//	tamanho após desfazer = 1480 (o ficheiro CRESCEU)
//	bytes nulos no ficheiro = 606 de 1480
//	replay final: seq=1, seq=2, seq=6      (3, 4 e 5 desaparecidos)
//	**o append FALHADO (s9) ficou DURÁVEL**
//
// A invariante central — «erro devolvido ⇒ nada ficou durável» — violada pelo próprio
// código que existe para a repor.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestAOS349_DesfazerSobreFicheiroEncolhidoNaoTornaDuravelOAppendFalhado é o teste que
// nasceu VERMELHO: o append falhado ficava no ficheiro, num buraco de bytes nulos.
func TestAOS349_DesfazerSobreFicheiroEncolhidoNaoTornaDuravelOAppendFalhado(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")

	s, ff := abreComFsyncFalhado(t, path, 1<<30, false)
	defer s.Close()
	for i := 1; i <= 5; i++ {
		appendEv(t, s, "run-A", "s"+string(rune('0'+i)), "t", `{"n":1}`)
	}
	memoria := s.wal.tamanho

	// O FICHEIRO ENCOLHE POR BAIXO: é o que um inspector fazia ao truncar a cauda.
	offs, _ := offsetsDeRegistos(t, path)
	if len(offs) != 5 {
		t.Fatalf("esperava 5 registos, li %d", len(offs))
	}
	encolhido := offs[2] // fica com os 2 primeiros registos
	if err := os.Truncate(path, encolhido); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if encolhido >= memoria {
		t.Fatalf("a dessincronização não aplicou: memória=%d, ficheiro=%d", memoria, encolhido)
	}

	// Agora um append cujo fsync FALHA — o caminho que chama `desfazer`.
	ff.falharApós = 0
	ff.syncs = 0
	_, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":9}`), RunID: "run-A", StepID: "s9"})
	if err == nil {
		t.Fatal("esperava erro do fsync")
	}

	// (1) O ERRO NOMEIA A CAUSA CERTA, e não «disco cheio».
	if !errors.Is(err, ErrWALDesincronizado) {
		t.Fatalf("erro = %v, esperava ErrWALDesincronizado — o operador tem de saber que o "+
			"ficheiro encolheu por baixo, não que o disco falhou", err)
	}

	// (2) O FICHEIRO NÃO CRESCEU. Era o crescimento que enterrava o append falhado.
	fi, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("stat: %v", statErr)
	}
	if fi.Size() > encolhido {
		t.Fatalf("a «reposição» ESTENDEU o ficheiro: %d -> %d bytes (memória dizia %d)",
			encolhido, fi.Size(), memoria)
	}

	// (3) O APPEND FALHADO NÃO FICOU DURÁVEL — a invariante central.
	for _, ev := range registosNoFicheiro(t, path) {
		if ev.StepID == "s9" {
			t.Fatal("o append FALHADO ficou DURÁVEL — «erro devolvido ⇒ nada ficou durável» violado")
		}
	}

	// (4) E o WAL para de aceitar escritas: a invariante não é reponível por este
	// mecanismo, e continuar seria construir um log que o Open seguinte recusa.
	ff.falharApós = 1 << 30
	if _, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":10}`), RunID: "run-A", StepID: "s10"}); err == nil {
		t.Fatal("o WAL dessincronizado NÃO podia voltar a aceitar escritas")
	}
}

// TestAOS349_ReposicaoNormalContinuaAFuncionar guarda a metade que a correcção não pode
// estropiar: com o ficheiro COERENTE, um append falhado continua a ser reposto por
// truncatura e o WAL continua utilizável.
func TestAOS349_ReposicaoNormalContinuaAFuncionar(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.wal")

	s, ff := abreComFsyncFalhado(t, path, 1<<30, false)
	defer s.Close()
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)
	antes := s.wal.tamanho

	ff.falharApós = 0
	ff.syncs = 0
	_, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":2}`), RunID: "run-A", StepID: "s2"})
	if err == nil {
		t.Fatal("esperava erro do fsync")
	}
	if errors.Is(err, ErrWALDesincronizado) {
		t.Fatalf("um ficheiro COERENTE foi lido como dessincronizado: %v", err)
	}

	fi, _ := os.Stat(path)
	if fi.Size() != antes {
		t.Fatalf("a reposição normal não repôs: %d -> %d bytes", antes, fi.Size())
	}

	// E o WAL continua utilizável quando a avaria passa.
	ff.falharApós = 1 << 30
	res, err := s.Append(ctx, "run-A", EventInput{Type: "t", Payload: []byte(`{"n":3}`), RunID: "run-A", StepID: "s3"})
	if err != nil {
		t.Fatalf("retry depois de uma reposição NORMAL: %v", err)
	}
	if res.Seq != 2 {
		t.Fatalf("seq = %d, quero 2", res.Seq)
	}
}
