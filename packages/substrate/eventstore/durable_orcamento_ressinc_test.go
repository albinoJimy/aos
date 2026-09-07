package eventstore

// AOS-385 (porte do WORM de auditoria irmão AOS-364) — A RESSINCRONIZAÇÃO TEM ORÇAMENTO.
//
// # O DoS, medido na versão gémea do audit ANTES da correcção
//
// Quando o enquadramento se perde (AOS-346), [contaOrfaos] varre o ficheiro byte-a-byte à
// procura da próxima fronteira de registo válida. Sem tecto, esse varrimento é O(n²) sobre
// a janela de [maxRecordBytes] (64 MiB): cada offset onde quatro bytes formam um comprimento
// plausível custa uma leitura e um crc32 de `tam` bytes. Um adversário com escrita no WAL —
// o modelo de ameaça deste substrato — fabrica PADDING após uma quebra de comprimento em que
// quase todos os offsets são candidatos de `tam` grande, e prende o arranque do Event Store.
// Medido no WORM gémeo: WAL de 1 MiB → ~10 s; 2 MiB → não termina em 300 s.
//
// # O QUE ESTE FICHEIRO FIXA
//
// Que [Open] sobre esse WAL RECUSA depressa (fail-closed) em vez de prender (AOS-385). O
// orçamento [ressincOrcamentoBytes] trava o varrimento: esgotá-lo devolve «inconclusivo», que [abrir]
// trata como DANO (recusa com [ErrWALCorruptedMidLog]) — NUNCA como cauda rasgada (truncar).
// A recusa não toca no ficheiro: a cópia de segurança continua a ter contra o que reconciliar.
//
// Sem o orçamento este teste PRENDE (o varrimento testaria centenas de milhar de candidatos
// de 256 KiB cada, com leitura+CRC em cada um) e estoira o limite de tempo — é essa a
// regressão que guarda.

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAOS385_Orcamento_RessincronizacaoNaoPrende — molde: o teste homónimo do WORM de
// auditoria (AOS-364, aos364_orcamento_test.go). Constrói um WAL com um prefixo VÁLIDO, infla o
// comprimento do registo seguinte (a «quebra»), e enche a cauda com padding adversarial
// desenhado para maximizar candidatos de `tam` grande. Exige que [Open] devolva erro em
// muito menos tempo do que o hang medido, RECUSANDO e sem amputar o ficheiro.
func TestAOS385_Orcamento_RessincronizacaoNaoPrende(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")

	// Prefixo VÁLIDO: dois eventos reais. O primeiro sobrevive ao replay (validEnd fica na
	// sua fronteira), o que prova que a recusa é de DANO A MEIO e não de ficheiro vazio.
	s := openDurable(t, path)
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)
	appendEv(t, s, "run-A", "s2", "t", `{"n":2}`)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	offs, b := offsetsDeRegistos(t, path)
	if len(offs) != 2 {
		t.Fatalf("esperava 2 registos no WAL, li %d", len(offs))
	}
	// Reconstrói o ficheiro: frame 0 intacto || cabeçalho de comprimento INFLADO (10 MiB, a
	// quebra) || padding adversarial. O leitor sequencial pára na quebra (short read do
	// payload de 10 MiB) e a ressincronização tem de varrer o padding para decidir o remédio.
	quebra := int(offs[1]) // início do 2.º registo
	alvo := quebra + 4 + (2 << 20)
	novo := make([]byte, 0, alvo)
	novo = append(novo, b[:quebra]...) // prefixo válido (frame 0)

	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 10<<20) // comprimento inflado, ≤ maxRecordBytes
	novo = append(novo, hdr[:]...)

	// PADDING ADVERSARIAL: o padrão big-endian [00 04 00 00] = 256 KiB. Em cada fronteira de
	// 4 bytes o varrimento vê um candidato de `tam`=256 KiB (0<tam≤64 MiB e cabe no ficheiro),
	// gasta 256 KiB de orçamento e lê+CRC 256 KiB — sem NUNCA validar (o CRC de padding não
	// fecha, o JSON não desserializa num Event). ~1024 candidatos esgotam os 256 MiB do tecto
	// nos primeiros ~4 KiB do padding; sem o tecto, o varrimento continuaria por todo o padding.
	padrao := []byte{0x00, 0x04, 0x00, 0x00}
	for len(novo) < alvo {
		novo = append(novo, padrao...)
	}
	if err := os.WriteFile(path, novo, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	sizeAntes := int64(len(novo))

	// Abre num goroutine com limite de tempo. Um hang O(n²) demoraria minutos-a-horas; 30 s
	// distingue com folga larga «recusou depressa» de «prendeu».
	done := make(chan error, 1)
	go func() {
		st, e := Open(path, WithReplicas(1), WithQuorum(1))
		if st != nil {
			_ = st.Close()
		}
		done <- e
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Open ACEITOU um WAL com comprimento inflado e padding adversarial — " +
				"devia recusar (ressincronização inconclusiva por orçamento)")
		}
		if !errors.Is(err, ErrWALCorruptedMidLog) {
			t.Fatalf("erro = %v, esperava ErrWALCorruptedMidLog", err)
		}
		// A RECUSA NÃO TOCA NO FICHEIRO: nada foi amputado.
		if got := tamanhoDe(t, path); got != sizeAntes {
			t.Fatalf("o ficheiro foi alterado por um dano recusado: %d -> %d bytes", sizeAntes, got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Open prendeu >30 s — o orçamento de ressincronização (ressincOrcamentoBytes) " +
			"não está a travar o varrimento O(n²)")
	}
}

// TestAOS385_Orcamento_CaudaRasgadaLegitimaNaoAtingeOTecto guarda a propriedade oposta: uma
// cauda rasgada genuína (um write interrompido no fim do ficheiro) tem janela pequena — os
// bytes a seguir à quebra não chegam para um registo — e NUNCA esgota o orçamento. Continua a
// truncar (crash-safety), não a recusar. Sem esta metade, o teste de cima seria compatível com
// uma guarda que recusa tudo.
func TestAOS385_Orcamento_CaudaRasgadaLegitimaNaoAtingeOTecto(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.wal")
	s := openDurable(t, path)
	appendEv(t, s, "run-A", "s1", "t", `{"n":1}`)
	appendEv(t, s, "run-A", "s2", "t", `{"n":2}`)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// Fim do último registo íntegro (os dois eventos), medido ANTES de sujar a cauda — é a
	// validEnd a que o Open tem de truncar.
	validEnd := tamanhoDe(t, path)

	// Simula um crash a meio do 3.º write: acrescenta um cabeçalho e meio payload, sem
	// trailer. É uma cauda parcial — não há nada íntegro depois dela.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], 64) // diz 64 bytes de payload…
	if _, err := f.Write(hdr[:]); err != nil {
		t.Fatalf("write hdr: %v", err)
	}
	if _, err := f.Write([]byte(`{"n":3,"parci`)); err != nil { // …mas só escreve um bocado
		t.Fatalf("write payload parcial: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close append: %v", err)
	}

	s2, err := Open(path, WithReplicas(1), WithQuorum(1))
	if err != nil {
		t.Fatalf("Open recusou uma cauda rasgada legítima (%v) — devia truncar, não recusar", err)
	}
	defer s2.Close()

	// A cauda parcial foi truncada; os dois eventos íntegros sobrevivem.
	if got := tamanhoDe(t, path); got != validEnd {
		t.Fatalf("cauda parcial não foi truncada a %d: ficheiro tem %d bytes", validEnd, got)
	}
	evs, err := s2.Read(context.Background(), "run-A", 0)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("esperava 2 eventos íntegros após truncatura da cauda, li %d", len(evs))
	}
}
