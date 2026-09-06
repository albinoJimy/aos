package eventstore

// CORRUPÇÃO A MEIO DO WAL ≠ CAUDA RASGADA.
//
// # O DEFEITO, medido antes da guarda
//
// O [replayWAL] parava no primeiro registo com checksum inválido e o [Open] truncava o
// ficheiro nesse ponto. Para uma cauda rasgada por um crash isso é a recuperação certa —
// os bytes parciais não são dados de ninguém. Aplicado a uma quebra no MEIO, o mesmo
// gesto apagava do disco tudo o que vinha depois.
//
// Medição de 2026-08-28, com 5 eventos e UM byte corrompido no segundo:
//
//	apos reabrir: 1 evento(s) legiveis (err=<nil>); WAL passou de 1765 para 353 bytes
//	=> os eventos 3, 4 e 5 estavam INTEGROS e desapareceram
//
// Sem erro, sem aviso, sem sinal: o `Read` devolvia `err=nil` e o log parecia apenas
// mais curto. É a diferença entre perda DETECTADA e perda SILENCIOSA.
//
// # A DISTINÇÃO, e porque ela é decidível
//
// Depois de um registo com checksum inválido o enquadramento continua bom — o
// comprimento foi lido, o frame foi consumido por inteiro — pelo que a fronteira do
// registo SEGUINTE é alcançável. Basta procurar lá: se houver registos que ainda
// desserializam, a quebra não está na cauda e truncar destrói dados bons.
//
// AOS-357(b) — O ARGUMENTO CERTO É TEMPORAL, NÃO ESTRUTURAL.
//
// Esta secção dizia que um write rasgado «perde o próprio enquadramento … por
// construção, zero». É FALSO. O `append` faz `Flush` (write(2)) e só depois `Sync`;
// entre os dois os bytes estão na page cache, e numa queda de MÁQUINA a escrita de volta
// é por PÁGINA enquanto o dispositivo persiste por SECTOR. Um registo que atravesse
// fronteiras de sector pode ficar com header e trailer persistidos e o miolo não — isto
// é, um frame COMPLETO com CRC inválido, exactamente o que se dizia impossível.
//
// O argumento verdadeiro é temporal: um write rasgado só pode ser o ÚLTIMO registo,
// porque cada `append` anterior fez `fsync` com sucesso antes de devolver `committed`
// (durable.go, [wal.append]). Logo `orfaos > 0` prova que a quebra NÃO é de um crash —
// há dados confirmados depois dela — e é essa a inferência que autoriza a recusa.
//
// A distinção não é cosmética: a versão estrutural tornar-se-ia perigosa no dia em que
// alguém acrescentasse group-commit, hipótese que store.go antecipa explicitamente. Aí
// vários registos partilhariam um só fsync e o «só o último» deixaria de valer — e quem
// lesse o argumento estrutural não veria que a premissa tinha caído.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// escreveCinco grava cinco eventos marcados e devolve o caminho e o tamanho do WAL.
func escreveCinco(t *testing.T) (path string, tamanho int64) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "events.wal")
	s := openDurable(t, path)
	for i := 1; i <= 5; i++ {
		appendEv(t, s, "run-A", "s"+strconv.Itoa(i), "t", `{"marca":"EVENTO-`+strconv.Itoa(i)+`"}`)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return path, fi.Size()
}

// corrompeMarca troca a marca do evento n por outra do MESMO comprimento, sem tocar no
// checksum — o registo passa a ter crc inválido e o enquadramento mantém-se. Falha o
// teste se a troca não chegar a aplicar-se: uma mutação que não aplica lê-se como
// robustez do sistema e nunca como teste fraco.
func corrompeMarca(t *testing.T, path string, n int) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	de := []byte("EVENTO-" + strconv.Itoa(n))
	para := []byte("EVENTO-X")
	if len(de) != len(para) {
		t.Fatalf("as marcas têm de ter o mesmo comprimento (%q vs %q)", de, para)
	}
	idx := indexOf(b, de)
	if idx < 0 {
		t.Fatal(">>> A CORRUPÇÃO NÃO APLICOU <<< a marca não está no WAL")
	}
	copy(b[idx:], para)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func indexOf(hay, needle []byte) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if string(hay[i:i+len(needle)]) == string(needle) {
			return i
		}
	}
	return -1
}

// TestDurable_CorrupcaoAMeioRecusaEmVezDeApagar é o teste que nasceu VERMELHO: antes da
// guarda, o Open devolvia um store e o ficheiro encolhia.
func TestDurable_CorrupcaoAMeioRecusaEmVezDeApagar(t *testing.T) {
	path, antes := escreveCinco(t)
	corrompeMarca(t, path, 2)

	s, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err == nil {
		_ = s.Close()
		t.Fatal("Open ACEITOU um WAL com corrupção a meio — os registos 3..5 seriam apagados")
	}
	if !errors.Is(err, ErrWALCorruptedMidLog) {
		t.Fatalf("erro = %v, esperava ErrWALCorruptedMidLog", err)
	}

	// A RECUSA NÃO PODE TER EFEITOS. Se o Open truncasse antes de recusar, a cópia de
	// segurança deixaria de ter contra o que reconciliar — e a recusa passaria a ser
	// só um aviso depois do facto.
	fi, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("stat: %v", statErr)
	}
	if fi.Size() != antes {
		t.Fatalf("o ficheiro foi alterado pela recusa: %d -> %d bytes (tinha de ficar intacto)", antes, fi.Size())
	}
}

// TestDurable_CaudaRasgadaContinuaATruncar guarda a metade que a correcção NÃO pode
// estropiar. Sem este teste, a guarda podia recusar também o caso do crash — e um nó
// que não arranca depois de uma falha de energia seria uma regressão pior do que o
// defeito corrigido.
func TestDurable_CaudaRasgadaContinuaATruncar(t *testing.T) {
	path, _ := escreveCinco(t)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Crash a meio do write do 5º registo: corta 3 bytes do fim.
	if err := os.WriteFile(path, b[:len(b)-3], 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	s, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("Open recusou uma CAUDA RASGADA, que é o caso legítimo de truncatura: %v", err)
	}
	defer s.Close()

	got, err := s.Read(context.Background(), "run-A", 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("cauda rasgada: restaurou %d eventos, quero 4", len(got))
	}
}

// TestDurable_TruncaturaDeliberadaRecupera prova que a via de recuperação existe e que
// é EXPLÍCITA. Sem ela, um operador com o log partido e sem cópia ficaria sem saída
// nenhuma — e a guarda passaria de protecção a beco.
func TestDurable_TruncaturaDeliberadaRecupera(t *testing.T) {
	path, antes := escreveCinco(t)
	corrompeMarca(t, path, 2)

	s, err := Open(path, WithReplicas(1), WithQuorum(1), WithWALTruncateOnCorruption())
	if err != nil {
		t.Fatalf("Open com truncatura deliberada: %v", err)
	}
	defer s.Close()

	got, err := s.Read(context.Background(), "run-A", 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("truncatura deliberada: restaurou %d eventos, quero 1", len(got))
	}
	fi, _ := os.Stat(path)
	if fi.Size() >= antes {
		t.Fatalf("a opção não truncou: %d -> %d bytes", antes, fi.Size())
	}
}

// TestDurable_CorrupcaoNoULTIMORegistoEhCauda fixa a fronteira. Um checksum inválido no
// ÚLTIMO registo não tem nada íntegro a seguir: é indistinguível de um write rasgado, e
// tem de continuar a truncar. Sem este teste, a guarda podia ser escrita como «qualquer
// checksum inválido recusa» — o que avermelharia o caso do crash que já era coberto.
func TestDurable_CorrupcaoNoULTIMORegistoEhCauda(t *testing.T) {
	path, _ := escreveCinco(t)
	corrompeMarca(t, path, 5)

	s, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("Open recusou corrupção no ÚLTIMO registo, que é a cauda: %v", err)
	}
	defer s.Close()

	got, err := s.Read(context.Background(), "run-A", 1)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("corrupção na cauda: restaurou %d eventos, quero 4", len(got))
	}
}
