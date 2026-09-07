package audit

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openWORM(t *testing.T, path string) *FileStore {
	t.Helper()
	s, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore(%q): %v", path, err)
	}
	return s
}

// TestWORM_SurvivesRestart — (d): a hash-chain do audit persiste e VERIFICA após
// restart. Escreve registos, fecha, reabre sobre o MESMO ficheiro e Verify fecha; os
// registos reconstruídos são byte-a-byte os originais (seq/PrevHash/EntryHash).
func TestWORM_SurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	var orig []AuditRecord
	for i := 0; i < 4; i++ {
		rec, err := s.Append(ctx, sampleRecord("run-1", DecisionAllow))
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		orig = append(orig, rec)
	}
	// Uma segunda partição para provar o isolamento de cadeias no replay.
	if _, err := s.Append(ctx, sampleRecord("run-2", DecisionDeny)); err != nil {
		t.Fatalf("append run-2: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// REABRE.
	s2 := openWORM(t, path)
	defer s2.Close()

	if h, _ := s2.Head(ctx, "run-1"); h != 4 {
		t.Fatalf("head run-1 = %d, quero 4", h)
	}
	if h, _ := s2.Head(ctx, "run-2"); h != 1 {
		t.Fatalf("head run-2 = %d, quero 1", h)
	}
	// A hash-chain VERIFICA após restart (tamper-evidence sobreviveu).
	if err := Verify(ctx, s2, "run-1", 1, 4); err != nil {
		t.Fatalf("Verify run-1 pós-restart: %v", err)
	}
	if err := Verify(ctx, s2, "run-2", 1, 1); err != nil {
		t.Fatalf("Verify run-2 pós-restart: %v", err)
	}
	// Registos idênticos byte-a-byte.
	for i, want := range orig {
		got, ok, _ := s2.At(ctx, "run-1", uint64(i+1))
		if !ok {
			t.Fatalf("registo %d ausente após restart", i+1)
		}
		if got.AuditSeq != want.AuditSeq ||
			!bytes.Equal(got.PrevHash, want.PrevHash) ||
			!bytes.Equal(got.EntryHash, want.EntryHash) {
			t.Fatalf("registo %d divergiu após restart", i+1)
		}
	}

	// A cadeia CONTINUA após restart: um novo Append encadeia no último EntryHash.
	next, err := s2.Append(ctx, sampleRecord("run-1", DecisionAllow))
	if err != nil {
		t.Fatalf("append pós-restart: %v", err)
	}
	if next.AuditSeq != 5 || !bytes.Equal(next.PrevHash, orig[3].EntryHash) {
		t.Fatalf("cadeia nao continuou: seq=%d prev-encadeia=%v", next.AuditSeq, bytes.Equal(next.PrevHash, orig[3].EntryHash))
	}
	if err := Verify(ctx, s2, "run-1", 1, 5); err != nil {
		t.Fatalf("Verify run-1 pós-append: %v", err)
	}
}

// TestWORM_CrashTruncatedTail — um crash a meio do write do último registo deixa um
// tail truncado; o reopen ignora-o e a cadeia até ao penúltimo verifica. O store
// continua a partir daí sem quebrar a cadeia.
func TestWORM_CrashTruncatedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	full, _ := os.ReadFile(path)
	// Corta o fim do último registo (crash a meio do write).
	if err := os.WriteFile(path, full[:len(full)-5], 0o600); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	s2 := openWORM(t, path)
	defer s2.Close()
	if h, _ := s2.Head(ctx, "p"); h != 2 {
		t.Fatalf("head pós-crash = %d, quero 2 (último truncado ignorado)", h)
	}
	if err := Verify(ctx, s2, "p", 1, 2); err != nil {
		t.Fatalf("Verify pós-crash: %v", err)
	}
	// Continua a cadeia: seq 3 encadeia no EntryHash de seq 2.
	prev, _, _ := s2.At(ctx, "p", 2)
	next, err := s2.Append(ctx, sampleRecord("p", DecisionAllow))
	if err != nil {
		t.Fatalf("append pós-crash: %v", err)
	}
	if next.AuditSeq != 3 || !bytes.Equal(next.PrevHash, prev.EntryHash) {
		t.Fatalf("cadeia nao continuou após crash: seq=%d", next.AuditSeq)
	}
	if err := Verify(ctx, s2, "p", 1, 3); err != nil {
		t.Fatalf("Verify final: %v", err)
	}
}

// TestWORM_TamperDetectedOnDisk — adulterar um registo no ficheiro é detectado por
// Verify após restart (a tamper-evidence é a do conteúdo selado, não do ficheiro,
// mas o restart não a mascara). Aqui corrompemos o checksum do ÚLTIMO registo: o
// replay pára antes dele, reduzindo o head — o registo adulterado nunca entra na
// cadeia servida (fail-safe: nunca serve um registo corrompido como íntegro).
// TestWORM_CorruptRecordDropped — REESCRITO por AOS-364. Antes, corromper o CRC do ÚLTIMO
// registo (fisicamente completo) fazia o Open descartá-lo em SILÊNCIO (head=1). Isso codificava
// o defeito como golden: um registo completo com CRC mau não é uma cauda rasgada de crash — é
// bit-rot ou adulteração —, e apagá-lo em silêncio destrói a detecção. A regra estrita de
// AOS-364 recusa a reabertura com DanoInteriorError, sem tocar no ficheiro. (O caso de crash
// legítimo — bytes em FALTA no fim — continua a truncar; ver TestWORM_CrashTruncatedTail.)
func TestWORM_CorruptRecordDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	for i := 0; i < 2; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_ = s.Close()

	full, _ := os.ReadFile(path)
	corrupt := make([]byte, len(full))
	copy(corrupt, full)
	corrupt[len(corrupt)-1] ^= 0xFF // corrompe o checksum do 2º (último) registo, framing INTACTO
	_ = os.WriteFile(path, corrupt, 0o600)
	sizeAntes := int64(len(corrupt))

	_, err := OpenFileStore(path)
	if err == nil {
		t.Fatal("um registo COMPLETO com CRC corrompido devia RECUSAR o Open (dano, não cauda rasgada)")
	}
	if !errors.Is(err, ErrWORMDanoInterior) {
		t.Fatalf("erro devia desembrulhar para ErrWORMDanoInterior, veio: %v", err)
	}
	if fi, _ := os.Stat(path); fi != nil && fi.Size() != sizeAntes {
		t.Fatalf("o ficheiro NÃO pode ser alterado num dano recusado: size antes=%d depois=%d", sizeAntes, fi.Size())
	}
}

// TestWORM_ImplementsStore — garantia de tipo: FileStore satisfaz Store.
func TestWORM_ImplementsStore(t *testing.T) {
	var _ Store = (*FileStore)(nil)
	s := openWORM(t, filepath.Join(t.TempDir(), "x.wal"))
	t.Cleanup(func() { _ = s.Close() })
	var _ Store = s
}
