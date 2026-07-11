package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

func newSigner(t *testing.T) *Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gerar par ed25519: %v", err)
	}
	s, err := NewSigner(priv, withSignerClock(func() time.Time { return fixedTime }))
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

// TestNewSignerInvalidKey — chave com dimensão errada é rejeitada.
func TestNewSignerInvalidKey(t *testing.T) {
	if _, err := NewSigner(ed25519.PrivateKey{1, 2, 3}); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("esperado ErrInvalidKey, veio %v", err)
	}
}

// TestCheckpointRoundTrip — selar e verificar a assinatura de um checkpoint.
func TestCheckpointRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 5)
	signer := newSigner(t)

	cp, err := signer.Seal(ctx, store, "p", 3)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if cp.AuditSeq != 3 || len(cp.Signature) == 0 {
		t.Fatalf("checkpoint mal formado: %+v", cp)
	}
	if err := VerifyCheckpoint(signer.Public(), cp); err != nil {
		t.Fatalf("assinatura valida rejeitada: %v", err)
	}
}

// TestVerifyFromCheckpointHappy — verifica eficientemente cp+1..to.
func TestVerifyFromCheckpointHappy(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 8)
	signer := newSigner(t)

	cp, _ := signer.Seal(ctx, store, "p", 4)
	if err := VerifyFromCheckpoint(ctx, store, signer.Public(), cp, 8); err != nil {
		t.Fatalf("verificacao ancorada devia passar: %v", err)
	}
	// to == cp.AuditSeq: nada para verificar além da âncora.
	if err := VerifyFromCheckpoint(ctx, store, signer.Public(), cp, 4); err != nil {
		t.Fatalf("to==cp devia passar: %v", err)
	}
}

// TestVerifyFromCheckpointBadSignature — assinatura inválida é rejeitada.
func TestVerifyFromCheckpointBadSignature(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 5)
	signer := newSigner(t)

	cp, _ := signer.Seal(ctx, store, "p", 3)
	cp.Signature[0] ^= 0xFF // adulterar a assinatura

	err := VerifyFromCheckpoint(ctx, store, signer.Public(), cp, 5)
	if !errors.Is(err, ErrCheckpointSignature) {
		t.Fatalf("esperado ErrCheckpointSignature, veio %v", err)
	}
}

// TestVerifyFromCheckpointWrongKey — chave pública diferente rejeita o checkpoint.
func TestVerifyFromCheckpointWrongKey(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 5)
	signer := newSigner(t)
	other := newSigner(t)

	cp, _ := signer.Seal(ctx, store, "p", 3)
	if err := VerifyFromCheckpoint(ctx, store, other.Public(), cp, 5); !errors.Is(err, ErrCheckpointSignature) {
		t.Fatalf("chave errada devia rejeitar: %v", err)
	}
}

// TestVerifyFromCheckpointTamperAfter — adulteração após a âncora é detectada.
func TestVerifyFromCheckpointTamperAfter(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 6)
	signer := newSigner(t)

	cp, _ := signer.Seal(ctx, store, "p", 3)
	// Mutar seq=5 (depois da âncora, dentro do intervalo verificado).
	store.parts["p"][4].PolicyVersion = "0.0.0-forjada"

	err := VerifyFromCheckpoint(ctx, store, signer.Public(), cp, 6)
	assertTamper(t, err, TamperMutation, 5)
}

// TestVerifyFromCheckpointAnchorMismatch — se o registo na âncora for adulterado,
// o EntryHash deixa de corresponder ao checkpoint assinado.
func TestVerifyFromCheckpointAnchorMismatch(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 6)
	signer := newSigner(t)

	cp, _ := signer.Seal(ctx, store, "p", 3)
	// Corromper o EntryHash do registo âncora seq=3.
	store.parts["p"][2].EntryHash[0] ^= 0xFF

	err := VerifyFromCheckpoint(ctx, store, signer.Public(), cp, 6)
	if !errors.Is(err, ErrCheckpointAnchor) {
		t.Fatalf("esperado ErrCheckpointAnchor, veio %v", err)
	}
}

// TestVerifyFromCheckpointAnchorContentTamper — AOS-011-Q1: adulterar o CONTEÚDO
// do registo-âncora (Decision, Capability) mantendo o campo EntryHash armazenado
// byte-a-byte igual ao checkpoint assinado tem de ser detectado. O verificador
// recomputa H(prevHash||conteúdo) da âncora e compara-o com o valor ASSINADO —
// confiar só no campo EntryHash armazenado deixaria passar a forja.
func TestVerifyFromCheckpointAnchorContentTamper(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 6)
	signer := newSigner(t)

	cp, _ := signer.Seal(ctx, store, "p", 3)

	// Guardar o EntryHash original do registo-âncora e mutar SÓ o conteúdo,
	// restaurando o campo EntryHash armazenado para o valor original (o atacante
	// mantém-no idêntico ao checkpoint assinado).
	orig := make([]byte, len(store.parts["p"][2].EntryHash))
	copy(orig, store.parts["p"][2].EntryHash)
	store.parts["p"][2].Decision = DecisionDeny // allow -> deny
	store.parts["p"][2].Capability = "TAMPERED" // conteúdo forjado
	copy(store.parts["p"][2].EntryHash, orig)   // campo EntryHash intacto

	// Sanidade: o campo EntryHash armazenado continua igual ao checkpoint.
	if !bytes.Equal(store.parts["p"][2].EntryHash, cp.EntryHash) {
		t.Fatal("pre-condicao invalida: EntryHash armazenado deveria continuar == cp.EntryHash")
	}

	err := VerifyFromCheckpoint(ctx, store, signer.Public(), cp, 6)
	if !errors.Is(err, ErrCheckpointAnchor) {
		t.Fatalf("conteudo da ancora adulterado com EntryHash intacto nao detectado: %v", err)
	}
}

// TestVerifyFromCheckpointScope — a verificação ancorada cobre SÓ cp+1..to; uma
// adulteração ANTES da âncora não é examinada por este caminho (é papel de um
// checkpoint anterior). Documenta a eficiência: não se reprocessa desde a génese.
func TestVerifyFromCheckpointScope(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	appendN(t, store, "p", 6)
	signer := newSigner(t)

	cp, _ := signer.Seal(ctx, store, "p", 4)
	// Mutar seq=2 (ANTES da âncora, sem tocar no EntryHash de seq=4).
	store.parts["p"][1].Capability = "cap:forjada-antes-da-ancora"

	// VerifyFromCheckpoint(cp=4, to=6) só olha 5..6 → não deteta a mutação em 2.
	if err := VerifyFromCheckpoint(ctx, store, signer.Public(), cp, 6); err != nil {
		t.Fatalf("verificacao ancorada nao devia examinar o prefixo: %v", err)
	}
	// A verificação completa desde a génese, essa, deteta.
	if err := Verify(ctx, store, "p", 1, 6); !errors.Is(err, ErrTampered) {
		t.Fatalf("Verify completo devia detetar a mutacao no prefixo: %v", err)
	}
}
