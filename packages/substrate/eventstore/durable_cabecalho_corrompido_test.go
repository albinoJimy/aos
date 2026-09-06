package eventstore

// AOS-346 — O CABEÇALHO DE COMPRIMENTO ERA ZONA CEGA.
//
// # O DEFEITO, medido
//
// O enquadramento do WAL é `len` (BE32) + payload + `crc32` (BE32), e o CRC cobre só o
// payload. A consequência não é a que o nome sugere. Um `len` corrompido faz o leitor
// consumir um número ERRADO de bytes; o CRC falha — o que está certo —, mas a contagem
// de órfãos, que era feita a partir da posição em que o leitor ficou, arrancava
// DESALINHADA, não reconhecia nada e devolvia zero. Com zero órfãos o [Open] conclui
// «cauda rasgada» e TRUNCA.
//
// Medido com 5 eventos (1480 bytes) e UM byte trocado no `len` do 2.º registo:
//
//	len maior  @299 0x20->0xFF   Open err=nil, leu 1 de 5, ficheiro 1480 -> 296
//	len menor  @299 0x20->0x0A   Open err=nil,             ficheiro 1480 -> 296
//	len +256   @298 0x01->0x02   Open err=nil,             ficheiro 1480 -> 296
//	CONTROLO: payload @306       E_WAL_CORRUPTED_MID_LOG, orfaos=3, ficheiro INTACTO
//
// O controlo isola a variável: mesma zona, um byte. Corromper o payload disparava o
// fail-closed e não tocava no ficheiro; corromper o cabeçalho apagava três eventos
// CONFIRMADOS e devolvia `err=nil`. Quatro bytes em cada 296 — ≈1,4% do ficheiro — eram
// zona cega, e bit rot ou um write parcial de página aterram lá sem atacante nenhum.
//
// # O QUE ESTE FICHEIRO FIXA
//
// Que as três variantes medidas passam a recusar com [ErrWALCorruptedMidLog] e o
// ficheiro INTACTO. O controlo positivo (corrupção de payload) vive em
// durable_corrupcao_a_meio_test.go e continua verde — a guarda não se tornou
// indiscriminada. E [TestDurable_CaudaRasgadaContinuaATruncar] continua a provar a
// metade oposta: um crash genuíno continua a truncar.

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"
)

// offsetsDeRegistos percorre o enquadramento do WAL e devolve o offset de cada registo.
// Falha o teste se o ficheiro não for interpretável — um teste de corrupção que não
// consegue localizar o que vai corromper não mede nada.
func offsetsDeRegistos(t *testing.T, path string) ([]int64, []byte) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var offs []int64
	for off := int64(0); off+8 <= int64(len(b)); {
		n := int64(binary.BigEndian.Uint32(b[off : off+4]))
		if n == 0 || off+8+n > int64(len(b)) {
			t.Fatalf("enquadramento ilegível ao byte %d (len=%d, ficheiro=%d)", off, n, len(b))
		}
		offs = append(offs, off)
		off += 4 + n + 4
	}
	return offs, b
}

// corrompeCabecalho troca o comprimento declarado do registo n (1-based) por novo,
// deixando todo o resto do ficheiro intacto. Falha se a troca não alterar nada: uma
// mutação que não aplica lê-se como robustez do sistema e nunca como teste fraco.
func corrompeCabecalho(t *testing.T, path string, n int, novo uint32) {
	t.Helper()
	offs, b := offsetsDeRegistos(t, path)
	if n < 1 || n > len(offs) {
		t.Fatalf("registo %d fora do WAL (tem %d)", n, len(offs))
	}
	off := offs[n-1]
	antigo := binary.BigEndian.Uint32(b[off : off+4])
	if antigo == novo {
		t.Fatalf(">>> A CORRUPÇÃO NÃO APLICOU <<< len já era %d", novo)
	}
	binary.BigEndian.PutUint32(b[off:off+4], novo)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestAOS346_CabecalhoCorrompidoAMeioRecusaEmVezDeApagar é o teste que nasceu VERMELHO
// nas três variantes: antes da ressincronização, o Open devolvia err=nil e o ficheiro
// encolhia de 1480 para 296 bytes, apagando três eventos confirmados.
func TestAOS346_CabecalhoCorrompidoAMeioRecusaEmVezDeApagar(t *testing.T) {
	// Os deltas replicam as três variantes medidas. «maior mas cabe» é o caso perigoso:
	// o frame torto ainda assenta dentro do ficheiro, pelo que nada no enquadramento
	// denuncia que o comprimento está errado.
	casos := []struct {
		nome  string
		delta int64
	}{
		{"len maior", +223},
		{"len menor", -22},
		{"len maior mas cabe (+256)", +256},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			path, antes := escreveCinco(t)
			offs, b := offsetsDeRegistos(t, path)
			if len(offs) != 5 {
				t.Fatalf("esperava 5 registos no WAL, li %d", len(offs))
			}
			atual := int64(binary.BigEndian.Uint32(b[offs[1] : offs[1]+4]))
			novo := atual + c.delta
			if novo <= 0 {
				t.Fatalf("delta %d inutilizável sobre len=%d", c.delta, atual)
			}
			corrompeCabecalho(t, path, 2, uint32(novo))

			s, err := Open(path, WithReplicas(1), WithQuorum(1))
			if err == nil {
				_ = s.Close()
				fi, _ := os.Stat(path)
				t.Fatalf("Open ACEITOU um cabeçalho corrompido a meio (len %d -> %d); "+
					"ficheiro %d -> %d bytes — os registos 3..5 seriam apagados",
					atual, novo, antes, fi.Size())
			}
			if !errors.Is(err, ErrWALCorruptedMidLog) {
				t.Fatalf("erro = %v, esperava ErrWALCorruptedMidLog", err)
			}
			// A RECUSA NÃO PODE TER EFEITOS: a cópia de segurança tem de continuar a
			// ter contra o que reconciliar.
			fi, statErr := os.Stat(path)
			if statErr != nil {
				t.Fatalf("stat: %v", statErr)
			}
			if fi.Size() != antes {
				t.Fatalf("o ficheiro foi alterado pela recusa: %d -> %d bytes", antes, fi.Size())
			}
		})
	}
}

// TestAOS346_CabecalhoCorrompidoRecuperaComTruncaturaDeliberada prova que a via de saída
// explícita cobre também esta classe — sem ela a guarda nova seria um beco para quem tem
// o log partido e não tem cópia.
func TestAOS346_CabecalhoCorrompidoRecuperaComTruncaturaDeliberada(t *testing.T) {
	path, antes := escreveCinco(t)
	offs, b := offsetsDeRegistos(t, path)
	atual := binary.BigEndian.Uint32(b[offs[1] : offs[1]+4])
	corrompeCabecalho(t, path, 2, atual+223)

	s, err := Open(path, WithReplicas(1), WithQuorum(1), WithWALTruncateOnCorruption())
	if err != nil {
		t.Fatalf("Open com truncatura deliberada: %v", err)
	}
	defer s.Close()
	fi, _ := os.Stat(path)
	if fi.Size() >= antes {
		t.Fatalf("a opção não truncou: %d -> %d bytes", antes, fi.Size())
	}
}

// TestAOS346_FicheiroIntegroNaoPagaRessincronizacao guarda a propriedade oposta: um WAL
// íntegro não tem nada depois do último registo, e a ressincronização não pode inventar
// órfãos nem recusar um arranque limpo.
func TestAOS346_FicheiroIntegroNaoPagaRessincronizacao(t *testing.T) {
	path, antes := escreveCinco(t)
	s, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("Open sobre WAL íntegro: %v", err)
	}
	defer s.Close()
	fi, _ := os.Stat(path)
	if fi.Size() != antes {
		t.Fatalf("Open mexeu num WAL íntegro: %d -> %d bytes", antes, fi.Size())
	}
}
