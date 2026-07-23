package audit

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// tempWAL devolve um caminho de WAL novo numa directoria temporária do teste.
func tempWAL(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "worm.wal")
}

// AOS-164b — CA de SINGLE-WRITER do WORM sob N runs concorrentes. O [FileStore.Append]
// sela (AuditSeq/PrevHash/EntryHash) E persiste sob o MESMO s.mu, tornando o writer
// serializado o DONO NOMEADO da ordenação da hash-chain por-partição (ver o comentário de
// Append). Estes testes provam a propriedade sob concorrência real (-race), cobrindo os
// dois casos: (a) N runs na MESMA partição e (b) N runs em partições DIFERENTES.

// TestFileStore_SingleWriterSamePartition — N goroutines fazem Append CONCORRENTE à MESMA
// partição. Prova a SERIALIZAÇÃO: a cadeia resultante tem AuditSeq contíguo 1..N (gapless),
// cada PrevHash encadeia no EntryHash anterior, cada EntryHash é único (SEM fork) e Verify
// fecha. Não-vacuoso: sem o single-writer, dois appends veriam o mesmo `last` e produziriam
// um FORK (AuditSeq/PrevHash duplicados) — detectado abaixo e por Verify.
func TestFileStore_SingleWriterSamePartition(t *testing.T) {
	t.Parallel()
	path := tempWAL(t)
	ctx := context.Background()

	s := openWORM(t, path)
	const n = 64
	const part = "run-shared"

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Append(ctx, sampleRecord(part, DecisionAllow)); err != nil {
				t.Errorf("append concorrente: %v", err)
			}
		}()
	}
	wg.Wait()

	// Head reflecte exactamente N appends (nem a menos — perda — nem a mais — duplicação).
	if h, _ := s.Head(ctx, part); h != n {
		t.Fatalf("head = %d, quero %d (single-writer contou todos os appends sem perda/dup)", h, n)
	}

	recs, err := s.Read(ctx, part, 1, n)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(recs) != n {
		t.Fatalf("len(recs) = %d, quero %d", len(recs), n)
	}
	// Contiguidade (1..N, sem gap/dup/reordenação), encadeamento e ausência de FORK.
	seenSeq := make(map[uint64]bool, n)
	seenHash := make(map[string]bool, n)
	var prevHash []byte
	for i, r := range recs {
		wantSeq := uint64(i + 1)
		if r.AuditSeq != wantSeq {
			t.Fatalf("posição %d: AuditSeq = %d, quero %d (gap/dup/reordenação — serialização falhou)", i, r.AuditSeq, wantSeq)
		}
		if seenSeq[r.AuditSeq] {
			t.Fatalf("AuditSeq %d duplicado (FORK)", r.AuditSeq)
		}
		seenSeq[r.AuditSeq] = true

		hk := string(r.EntryHash)
		if seenHash[hk] {
			t.Fatalf("EntryHash repetido no seq %d (FORK — dois registos partilham o mesmo elo)", r.AuditSeq)
		}
		seenHash[hk] = true

		// Encadeamento: PrevHash == EntryHash do anterior (génese no primeiro).
		wantPrev := prevHash
		if i == 0 {
			wantPrev = GenesisHash(part)
		}
		if string(r.PrevHash) != string(wantPrev) {
			t.Fatalf("seq %d: PrevHash não encadeia no EntryHash anterior (FORK/reordenação)", r.AuditSeq)
		}
		// Integridade do selo: recomputar bate o armazenado.
		if string(ComputeEntryHash(r.PrevHash, r)) != string(r.EntryHash) {
			t.Fatalf("seq %d: EntryHash recomputado diverge (selo corrompido)", r.AuditSeq)
		}
		prevHash = r.EntryHash
	}

	// A cadeia inteira VERIFICA (tamper-evidence fecha sobre 1..N).
	if err := Verify(ctx, s, part, 1, n); err != nil {
		t.Fatalf("Verify pós-concorrência: %v", err)
	}

	// A serialização é DURÁVEL: reabrir do WAL reconstrói a mesma cadeia e Verify fecha.
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s2 := openWORM(t, path)
	defer s2.Close()
	if h, _ := s2.Head(ctx, part); h != n {
		t.Fatalf("head pós-restart = %d, quero %d", h, n)
	}
	if err := Verify(ctx, s2, part, 1, n); err != nil {
		t.Fatalf("Verify pós-restart: %v", err)
	}
}

// TestFileStore_PerPartitionIsolationConcurrent — N runs em partições DIFERENTES fazem
// Append concorrente. Prova o ISOLAMENTO por-partição: cada cadeia é independentemente
// contígua (1..perPart) e VÁLIDA, sem contaminação entre partições (a ordenação de uma não
// depende nem interfere com a de outra). Runs em partições distintas nunca contendem na
// MESMA cadeia (o modelo por-particao de revalhook.go / cada despacho no seu partition).
func TestFileStore_PerPartitionIsolationConcurrent(t *testing.T) {
	t.Parallel()
	path := tempWAL(t)
	ctx := context.Background()

	s := openWORM(t, path)
	defer s.Close()

	const parts = 16
	const perPart = 16

	var wg sync.WaitGroup
	for p := 0; p < parts; p++ {
		part := fmt.Sprintf("run-%02d", p)
		for k := 0; k < perPart; k++ {
			wg.Add(1)
			go func(part string) {
				defer wg.Done()
				if _, err := s.Append(ctx, sampleRecord(part, DecisionAllow)); err != nil {
					t.Errorf("append concorrente (%s): %v", part, err)
				}
			}(part)
		}
	}
	wg.Wait()

	// Cada partição tem exactamente perPart registos, contíguos e a cadeia fecha.
	for p := 0; p < parts; p++ {
		part := fmt.Sprintf("run-%02d", p)
		if h, _ := s.Head(ctx, part); h != perPart {
			t.Fatalf("partição %q head = %d, quero %d (isolamento: cada cadeia é completa)", part, h, perPart)
		}
		recs, err := s.Read(ctx, part, 1, perPart)
		if err != nil {
			t.Fatalf("read %q: %v", part, err)
		}
		for i, r := range recs {
			if r.AuditSeq != uint64(i+1) {
				t.Fatalf("partição %q posição %d: AuditSeq = %d, quero %d", part, i, r.AuditSeq, i+1)
			}
		}
		if err := Verify(ctx, s, part, 1, perPart); err != nil {
			t.Fatalf("Verify %q: %v (uma partição não deve ser afectada pela concorrência noutra)", part, err)
		}
	}
}
