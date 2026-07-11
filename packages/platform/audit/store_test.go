package audit

import (
	"bytes"
	"context"
	"testing"
)

// appendN acrescenta n registos allow à partição e devolve os registos selados.
func appendN(t *testing.T, store Store, partition string, n int) []AuditRecord {
	t.Helper()
	ctx := context.Background()
	out := make([]AuditRecord, 0, n)
	for i := 0; i < n; i++ {
		rec, err := store.Append(ctx, sampleRecord(partition, DecisionAllow))
		if err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
		out = append(out, rec)
	}
	return out
}

// TestAppendSealsChain — Append atribui seq gapless a partir de 1, encadeia
// PrevHash=EntryHash anterior (génese no primeiro) e calcula o EntryHash.
func TestAppendSealsChain(t *testing.T) {
	store := NewMemStore()
	recs := appendN(t, store, "p", 3)

	if recs[0].AuditSeq != 1 || recs[1].AuditSeq != 2 || recs[2].AuditSeq != 3 {
		t.Fatalf("audit_seq nao gapless a partir de 1: %d,%d,%d",
			recs[0].AuditSeq, recs[1].AuditSeq, recs[2].AuditSeq)
	}
	if !bytes.Equal(recs[0].PrevHash, GenesisHash("p")) {
		t.Fatal("primeiro PrevHash nao e a genese da particao")
	}
	if !bytes.Equal(recs[1].PrevHash, recs[0].EntryHash) {
		t.Fatal("PrevHash[2] != EntryHash[1]")
	}
	if !bytes.Equal(recs[2].PrevHash, recs[1].EntryHash) {
		t.Fatal("PrevHash[3] != EntryHash[2]")
	}
	for i, r := range recs {
		if !bytes.Equal(ComputeEntryHash(r.PrevHash, r), r.EntryHash) {
			t.Fatalf("EntryHash do registo %d nao fecha", i+1)
		}
	}
}

// TestPartitionsIndependent — cada partição tem a sua própria cadeia gapless.
func TestPartitionsIndependent(t *testing.T) {
	store := NewMemStore()
	a := appendN(t, store, "a", 2)
	b := appendN(t, store, "b", 2)

	if a[0].AuditSeq != 1 || b[0].AuditSeq != 1 {
		t.Fatal("cada particao deve comecar em audit_seq=1")
	}
	if !bytes.Equal(b[0].PrevHash, GenesisHash("b")) {
		t.Fatal("particao b nao ancora na sua propria genese")
	}
	if bytes.Equal(a[0].EntryHash, b[0].EntryHash) {
		t.Fatal("particoes distintas nao devem colidir no EntryHash")
	}
}

// TestHeadAndAt — Head devolve o último seq; At localiza por seq exacto.
func TestHeadAndAt(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 5)

	head, err := store.Head(ctx, "p")
	if err != nil || head != 5 {
		t.Fatalf("Head=%d err=%v, esperado 5", head, err)
	}
	if h, _ := store.Head(ctx, "vazia"); h != 0 {
		t.Fatal("Head de particao vazia deve ser 0")
	}
	rec, ok, err := store.At(ctx, "p", 3)
	if err != nil || !ok || rec.AuditSeq != 3 {
		t.Fatalf("At(3)=%+v ok=%v err=%v", rec, ok, err)
	}
	if _, ok, _ := store.At(ctx, "p", 99); ok {
		t.Fatal("At de seq inexistente deve devolver ok=false")
	}
}

// TestReadIsolation — mutar a slice devolvida por Read NÃO altera o estado selado.
func TestReadIsolation(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 1)

	got, _ := store.Read(ctx, "p", 1, 1)
	got[0].EntryHash[0] ^= 0xFF // tentar corromper via referência

	if err := Verify(ctx, store, "p", 1, 1); err != nil {
		t.Fatalf("mutar a copia lida corrompeu o estado selado: %v", err)
	}
}
