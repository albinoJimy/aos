package audit

import (
	"context"
	"errors"
	"testing"
)

// TestVerifyIntactChain — uma cadeia bem-formada verifica sem erro (full e sub-range).
func TestVerifyIntactChain(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 6)

	if err := Verify(ctx, store, "p", 1, 6); err != nil {
		t.Fatalf("cadeia integra devia verificar: %v", err)
	}
	// Sub-range ancorado no registo anterior (from>1).
	if err := Verify(ctx, store, "p", 3, 5); err != nil {
		t.Fatalf("sub-range integro devia verificar: %v", err)
	}
}

// TestVerifyDetectsMutation — mutar 1 registo faz o verify falhar e identifica-o.
func TestVerifyDetectsMutation(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 5)

	// Mutar in-place o conteúdo do registo seq=3 SEM recalcular o hash (adulteração
	// ingénua): a Capability muda mas o EntryHash armazenado fica o antigo.
	store.parts["p"][2].Capability = "fs:write:/etc/passwd"

	err := Verify(ctx, store, "p", 1, 5)
	assertTamper(t, err, TamperMutation, 3)

	if !errors.Is(err, ErrTampered) {
		t.Fatal("VerifyError deve desembrulhar para ErrTampered")
	}
}

// TestVerifyDetectsMutationWithRecomputedHash — mutar o conteúdo E recalcular o
// EntryHash do próprio registo é apanhado pela quebra de encadeamento no seguinte.
func TestVerifyDetectsMutationRecomputedLocally(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 5)

	// Adversário muta seq=3 e recalcula o SEU EntryHash para ser auto-consistente,
	// mas não propaga para a frente: o PrevHash de seq=4 deixa de encadear.
	store.parts["p"][2].Capability = "fs:write:/etc/passwd"
	store.parts["p"][2].EntryHash = ComputeEntryHash(store.parts["p"][2].PrevHash, store.parts["p"][2])

	err := Verify(ctx, store, "p", 1, 5)
	assertTamper(t, err, TamperChainBroken, 4)
}

// TestVerifyDetectsRemovalMiddle — remover um registo do meio quebra a contiguidade.
func TestVerifyDetectsRemovalMiddle(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 5)

	// Remover seq=3 (índice 2).
	p := store.parts["p"]
	store.parts["p"] = append(p[:2:2], p[3:]...)

	err := Verify(ctx, store, "p", 1, 5)
	assertTamper(t, err, TamperRemoval, 0) // seq esperado 3; Type é o que importa
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("esperado ErrTampered, veio %v", err)
	}
}

// TestVerifyDetectsRemovalTail — remover o último registo é detectado (falta no fim).
func TestVerifyDetectsRemovalTail(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 5)

	// Head continua a "prometer" 5, mas removemos o registo seq=5 do slice e
	// verificamos explicitamente 1..5. Simulamos remoção do tail dentro do range.
	store.parts["p"] = store.parts["p"][:4]

	// Verify(1,4) passa; a remoção do tail deteta-se ao verificar até ao seq que
	// existia. Usamos verifyRange directamente até 5 para o cenário determinístico.
	err := verifyRange(ctx, store, "p", 1, 5, GenesisHash("p"))
	assertTamper(t, err, TamperRemoval, 5)
}

// TestVerifyDetectsInsertion — inserir um registo forjado quebra a cadeia.
func TestVerifyDetectsInsertion(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 5)

	// Forjar um registo e inseri-lo entre seq=2 e seq=3, roubando o seq=3.
	forged := sampleRecord("p", DecisionAllow)
	forged.Capability = "cap:forged"
	forged.AuditSeq = 3
	forged.PrevHash = store.parts["p"][1].EntryHash
	forged.EntryHash = ComputeEntryHash(forged.PrevHash, forged)

	p := store.parts["p"]
	// [1,2, forged(3), 3(real, seq=3), 4, 5]
	newp := make([]AuditRecord, 0, len(p)+1)
	newp = append(newp, p[0], p[1], forged)
	newp = append(newp, p[2:]...)
	store.parts["p"] = newp

	err := Verify(ctx, store, "p", 1, 5)
	// O registo real seq=3 reaparece a seguir ao forjado (seq 3 duplicado) →
	// inserção; ou a quebra de encadeamento é detectada. Basta que falhe e seja
	// tamper.
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("insercao nao detectada: %v", err)
	}
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("esperado *VerifyError, veio %T", err)
	}
	if ve.Type != TamperInsertion && ve.Type != TamperChainBroken {
		t.Fatalf("tipo inesperado para insercao: %s", ve.Type)
	}
}

// TestVerifyDetectsInsertionAtTail — anexar um registo forjado com PrevHash errado.
func TestVerifyDetectsInsertionForgedLink(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 3)

	// Registo seq=4 com PrevHash que NÃO encadeia no EntryHash de seq=3.
	forged := sampleRecord("p", DecisionDeny)
	forged.AuditSeq = 4
	forged.PrevHash = GenesisHash("p") // errado de propósito
	forged.EntryHash = ComputeEntryHash(forged.PrevHash, forged)
	store.parts["p"] = append(store.parts["p"], forged)

	err := Verify(ctx, store, "p", 1, 4)
	assertTamper(t, err, TamperChainBroken, 4)
}

// TestVerifyInvalidRange — intervalos inválidos e além do head.
func TestVerifyInvalidRange(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 3)

	cases := []struct {
		name     string
		from, to uint64
		want     error
	}{
		{"from-zero", 0, 2, ErrInvalidRange},
		{"to-lt-from", 3, 1, ErrInvalidRange},
		{"beyond-head", 1, 9, ErrRangeBeyondHead},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := Verify(ctx, store, "p", c.from, c.to); !errors.Is(err, c.want) {
				t.Fatalf("Verify(%d,%d)=%v, esperado %v", c.from, c.to, err, c.want)
			}
		})
	}
	if err := Verify(ctx, store, "vazia", 1, 1); !errors.Is(err, ErrUnknownPartition) {
		t.Fatalf("particao vazia: %v, esperado ErrUnknownPartition", err)
	}
}

// TestVerifyErrorString — a mensagem do VerifyError identifica tipo, partição e seq.
func TestVerifyErrorString(t *testing.T) {
	e := tamper(TamperMutation, "run-1", 7, "detalhe")
	msg := e.Error()
	for _, want := range []string{"mutation", "run-1", "7", "detalhe"} {
		if !contains(msg, want) {
			t.Fatalf("mensagem %q nao contem %q", msg, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// assertTamper valida que err é um *VerifyError do tipo esperado (seq opcional:
// wantSeq==0 ignora o seq).
func assertTamper(t *testing.T, err error, wantType TamperType, wantSeq uint64) {
	t.Helper()
	if err == nil {
		t.Fatal("esperado erro de adulteracao, veio nil")
	}
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("esperado *VerifyError, veio %T (%v)", err, err)
	}
	if ve.Type != wantType {
		t.Fatalf("tipo=%s, esperado %s (%s)", ve.Type, wantType, ve.Detail)
	}
	if wantSeq != 0 && ve.Seq != wantSeq {
		t.Fatalf("seq=%d, esperado %d", ve.Seq, wantSeq)
	}
}
