package backup

import (
	"context"
	"errors"
	"testing"
)

func TestVerify_CleanBackupPasses(t *testing.T) {
	exp, dst := exportSeeded(t)
	rst, err := NewRestorer(dst, exp.Vault(), exp.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	if err := rst.VerifyManifest(exp.Manifest(), exp.Checkpoint(), 0); err != nil {
		t.Fatalf("backup íntegro devia verificar: %v", err)
	}
}

// tamperImmutable substitui, à força, o blob de uma ref no store imutável (simula
// uma adulteração do backup em repouso, contornando o write-once).
func tamperImmutable(s *InMemoryImmutableStore, ref string, blob []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.objects[ref]
	rec.blob = blob
	s.objects[ref] = rec
}

func TestVerify_DetectsSegmentTamper(t *testing.T) {
	exp, dst := exportSeeded(t)
	rst, err := NewRestorer(dst, exp.Vault(), exp.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	m := exp.Manifest()
	ref := m.Segments[0].Ref
	orig, _ := dst.Get(ref)
	// Adultera um byte do segmento em repouso.
	bad := make([]byte, len(orig))
	copy(bad, orig)
	bad[len(bad)/2] ^= 0xFF
	tamperImmutable(dst, ref, bad)

	if err := rst.VerifyManifest(m, exp.Checkpoint(), 0); !errors.Is(err, ErrSegmentTampered) {
		t.Fatalf("adulteração de segmento não detectada: %v", err)
	}
	// E o restauro tem de ABORTAR (fail-closed) sem escrever nada.
	out := freshDest(t, "board-eu", "eu-west")
	if _, err := rst.RestoreTo(context.Background(), m, exp.Checkpoint(), 0, nil, out); !errors.Is(err, ErrSegmentTampered) {
		t.Fatalf("restauro sobre backup adulterado devia abortar: %v", err)
	}
	if len(out.Streams()) != 0 {
		t.Fatalf("restauro abortado não devia ter escrito eventos")
	}
}

func TestVerify_DetectsManifestTamper(t *testing.T) {
	exp, dst := exportSeeded(t)
	rst, err := NewRestorer(dst, exp.Vault(), exp.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	// Adultera um campo semântico do manifesto (StreamHeads) sem tocar no blob.
	m := exp.Manifest()
	m.Segments[0].StreamHeads["run-a"] = 999
	if err := rst.VerifyManifest(m, exp.Checkpoint(), 0); !errors.Is(err, ErrChainBroken) {
		t.Fatalf("adulteração do manifesto não detectada: %v", err)
	}
}

func TestVerify_RejectsForgedCheckpoint(t *testing.T) {
	exp, dst := exportSeeded(t)
	// Um verificador com OUTRA chave pública (âncora de confiança errada) rejeita.
	other := newSigner(t)
	rst, err := NewRestorer(dst, exp.Vault(), other.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	if err := rst.VerifyManifest(exp.Manifest(), exp.Checkpoint(), 0); !errors.Is(err, ErrCheckpointSignature) {
		t.Fatalf("checkpoint forjado/assinado por outra chave devia falhar: %v", err)
	}
}

func TestVerify_RejectsStaleCheckpoint(t *testing.T) {
	ctx := context.Background()
	src := newSourceStore(t, "board-eu", "eu-west")
	exp, dst := newExporter(t, src, "eu-west")
	seed(t, src, "run-a", 1, "m")
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("Export#1: %v", err)
	}
	staleCp := exp.Checkpoint() // sela o ciclo 1
	staleManifest := exp.Manifest()

	seed(t, src, "run-a", 1, "m")
	if _, err := exp.Export(ctx); err != nil {
		t.Fatalf("Export#2: %v", err)
	}

	rst, err := NewRestorer(dst, exp.Vault(), exp.Public())
	if err != nil {
		t.Fatalf("NewRestorer: %v", err)
	}
	// expectedHead=2 (head conhecido) mas o checkpoint apresentado sela só o ciclo 1
	// → rollback detectado, fail-closed.
	if err := rst.VerifyManifest(staleManifest, staleCp, 2); !errors.Is(err, ErrCheckpointStale) {
		t.Fatalf("checkpoint stale (rollback) não detectado: %v", err)
	}
}
