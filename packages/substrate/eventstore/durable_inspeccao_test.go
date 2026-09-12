package eventstore

// AOS-347 — DOIS `Open` CONCORRENTES ATRIBUÍAM O MESMO `seq`, E O NÓ DEIXAVA DE ARRANCAR.
//
// # O DEFEITO, medido
//
// [Open] abre sempre em `O_CREATE|O_WRONLY|O_APPEND` e não pede posse nenhuma —
// [LockWAL] é deliberadamente separado, e a tranca vive no ficheiro IRMÃO para não
// bloquear quem lê. Três subcomandos (`wal-inspect`, `wal-summary`, `wal-count`) abriam
// por [Open]; os comentários diziam «com o contentor principal PARADO», o que é uma
// convenção documentada e não uma restrição imposta.
//
// Com o escritor A vivo, um segundo [Open] tinha sucesso e reconstruía a SUA cabeça:
//
//	seq atribuído por A = 4 ; seq atribuído por B = 4 ; COLIDEM
//	segunda ronda:  A=5  B=5  COLIDEM
//	ficheiro final: 7 registos, seqs=[1 2 3 4 4 5 5]
//	ARRANQUE SEGUINTE FALHOU = E_RESTORE_ORDER: lote de restauro nao e gapless
//
// E, composto com AOS-346, um comando de LEITURA levou um WAL de 1480 para 592 bytes:
// três eventos confirmados apagados por um inspector.
//
// # O QUE ESTE FICHEIRO FIXA
//
// Que o abridor de inspecção ([OpenReadOnly]) não tem por onde causar nenhuma das duas
// coisas — não atribui seq e não toca no ficheiro — e que a leitura legítima com o nó
// PARADO continua a não precisar de posse.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestAOS347_InspeccaoComEscritorVivoNaoGanhaCabecaConcorrente é o cenário que o
// `wal-inspect` nomeia e que nenhum teste media: o escritor está VIVO.
func TestAOS347_InspeccaoComEscritorVivoNaoGanhaCabecaConcorrente(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")

	escritor := openDurable(t, path)
	defer escritor.Close()
	appendEv(t, escritor, "run-A", "s1", "t", `{"n":1}`)
	appendEv(t, escritor, "run-A", "s2", "t", `{"n":2}`)
	appendEv(t, escritor, "run-A", "s3", "t", `{"n":3}`)

	// O inspector abre o MESMO WAL, com o escritor vivo. Tem de conseguir LER…
	inspector, err := OpenReadOnly(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("OpenReadOnly com o escritor vivo = %v — o inspector tem de poder olhar", err)
	}
	defer inspector.Close()
	evs, err := inspector.Read(t.Context(), "run-A", 1)
	if err != nil {
		t.Fatalf("Read do inspector: %v", err)
	}
	if len(evs) != 3 {
		t.Fatalf("inspector leu %d eventos, quero 3", len(evs))
	}

	// …e NÃO tem de conseguir atribuir um seq. Era a segunda cabeça que produzia os
	// seqs [1 2 3 4 4 5 5] e o E_RESTORE_ORDER do arranque seguinte.
	_, err = inspector.Append(t.Context(), "run-A", EventInput{
		Type: "probe", Payload: []byte(`{}`), RunID: "run-A", StepID: "x",
	})
	if !errors.Is(err, ErrReadOnly) {
		t.Fatalf("Append do inspector = %v, quero ErrReadOnly — uma segunda cabeça de escrita "+
			"colide no seq e o nó deixa de arrancar", err)
	}

	// O escritor continua a ser o único a atribuir seq, e continua gapless.
	res := appendEv(t, escritor, "run-A", "s4", "t", `{"n":4}`)
	if res.Seq != 4 {
		t.Fatalf("seq do escritor = %d, quero 4", res.Seq)
	}
}

// TestAOS347_InspeccaoNaoTrunca é a metade da destruição. Composto com AOS-346, um
// [Open] de leitura sobre um WAL com cauda parcial encolhia o ficheiro. O abridor de
// inspecção não pode tocar-lhe — a cópia de segurança tem de continuar a ter contra o
// que reconciliar.
func TestAOS347_InspeccaoNaoTrunca(t *testing.T) {
	path, _ := escreveCinco(t)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Cauda parcial: um crash a meio do write do 6.º registo.
	comCauda := append(append([]byte{}, b...), 0x00, 0x00, 0x01, 0x20, 0x7B, 0x22)
	if err := os.WriteFile(path, comCauda, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	antes := int64(len(comCauda))

	inspector, err := OpenReadOnly(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer inspector.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() != antes {
		t.Fatalf("o INSPECTOR truncou o WAL: %d -> %d bytes", antes, fi.Size())
	}
	// E continua a servir a leitura útil: os cinco registos íntegros.
	evs, err := inspector.Read(t.Context(), "run-A", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 5 {
		t.Fatalf("inspector leu %d eventos, quero 5", len(evs))
	}
}

// TestAOS347_LeituraLegitimaComONoParadoContinuaSemPosse guarda a metade que a correcção
// não pode estropiar: o operador com o nó parado não tem de pedir posse a ninguém.
func TestAOS347_LeituraLegitimaComONoParadoContinuaSemPosse(t *testing.T) {
	path, antes := escreveCinco(t)

	inspector, err := OpenReadOnly(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("OpenReadOnly com o nó parado: %v", err)
	}
	defer inspector.Close()
	evs, err := inspector.Read(t.Context(), "run-A", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 5 {
		t.Fatalf("leu %d eventos, quero 5", len(evs))
	}
	fi, _ := os.Stat(path)
	if fi.Size() != antes {
		t.Fatalf("a leitura mexeu no ficheiro: %d -> %d bytes", antes, fi.Size())
	}
}

// TestAOS347_InspeccaoRecusaIngestStream fecha a outra porta de escrita. [IngestStream]
// é o caminho de restauro, e um store de inspecção que o aceitasse escreveria na mesma —
// só que pela porta das traseiras.
func TestAOS347_InspeccaoRecusaIngestStream(t *testing.T) {
	path, _ := escreveCinco(t)
	inspector, err := OpenReadOnly(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer inspector.Close()

	evs, err := inspector.Read(t.Context(), "run-A", 1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	err = inspector.IngestStream(t.Context(), "run-B", evs)
	if !errors.Is(err, ErrReadOnly) {
		t.Fatalf("IngestStream do inspector = %v, quero ErrReadOnly", err)
	}
}
