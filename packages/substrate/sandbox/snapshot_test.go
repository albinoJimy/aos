package sandbox

import (
	"testing"
	"time"
)

// baseImage é uma imagem base determinista para os testes de snapshot/overlay.
func baseImage() map[string][]byte {
	return map[string][]byte{
		"etc/os-release": []byte("AOS-guest 1.0"),
		"bin/tool":       []byte("#!fake"),
	}
}

func newSnap(t *testing.T, opts ...SnapshotOption) *Snapshot {
	t.Helper()
	s, err := NewSnapshot("img-v1", baseImage(), opts...)
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	return s
}

func TestNewSnapshot_EmptyVersionFailsClosed(t *testing.T) {
	if _, err := NewSnapshot("", baseImage()); err != ErrEmptyImageVersion {
		t.Fatalf("esperava ErrEmptyImageVersion, obtive %v", err)
	}
}

// TestSnapshot_RestoreDurationInRange prova que a duração de restore está SEMPRE em
// [5,30] ms — inclusive quando um modelo injectado devolve valores fora de gama (o
// clamp impõe a invariante estruturalmente).
func TestSnapshot_RestoreDurationInRange(t *testing.T) {
	tests := []struct {
		name  string
		model RestoreModel
	}{
		{"default", DefaultRestoreModel},
		{"below-floor", func(uint64) time.Duration { return 1 * time.Millisecond }},
		{"above-ceiling", func(uint64) time.Duration { return 500 * time.Millisecond }},
		{"negative", func(uint64) time.Duration { return -3 * time.Millisecond }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newSnap(t, WithRestoreModel(tc.model))
			for i := 0; i < 100; i++ {
				_, d := s.Restore()
				if d < MinRestore || d > MaxRestore {
					t.Fatalf("restore #%d = %v fora de [%v,%v]", i, d, MinRestore, MaxRestore)
				}
			}
		})
	}
}

// TestSnapshot_DefaultModelSpansRange confirma que o modelo default cobre toda a
// gama (5 e 30 ms aparecem), garantindo cold-starts realistas nos testes de p95.
func TestSnapshot_DefaultModelSpansRange(t *testing.T) {
	s := newSnap(t)
	seen := map[time.Duration]bool{}
	for i := 0; i < 26; i++ {
		_, d := s.Restore()
		seen[d] = true
	}
	if !seen[MinRestore] || !seen[MaxRestore] {
		t.Fatalf("modelo default nao cobriu os extremos: min=%v max=%v seen=%v", seen[MinRestore], seen[MaxRestore], len(seen))
	}
}

// TestOverlay_BaseImmutableAcrossRestores prova a IMUTABILIDADE do base: escrever em
// muitos overlays nunca altera o digest do base nem contamina o base lido por outro
// overlay.
func TestOverlay_BaseImmutableAcrossRestores(t *testing.T) {
	s := newSnap(t)
	before := s.Digest()

	ov1, _ := s.Restore()
	if err := ov1.Write("etc/os-release", []byte("TAMPERED")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := ov1.Write("scratch/n.txt", []byte("dirt-1")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// O digest do base não muda: a escrita ficou no overlay, não no base.
	if after := s.Digest(); after != before {
		t.Fatalf("digest do base mudou apos escrita no overlay: %s != %s", after, before)
	}

	// Um overlay NOVO lê o base ORIGINAL — nunca a escrita de ov1.
	ov2, _ := s.Restore()
	got, ok := ov2.Read("etc/os-release")
	if !ok || string(got) != "AOS-guest 1.0" {
		t.Fatalf("overlay novo leu base contaminado: %q (ok=%v)", got, ok)
	}
	if _, ok := ov2.Read("scratch/n.txt"); ok {
		t.Fatalf("overlay novo observou escrita de outro overlay (estado reciclado)")
	}
	if ov2.BaseDigest() != before {
		t.Fatalf("BaseDigest do overlay novo != digest original")
	}
}

// TestOverlay_ReadReturnsCopy prova que Read devolve uma cópia — mutar o resultado
// não corrompe o base nem a escrita interna.
func TestOverlay_ReadReturnsCopy(t *testing.T) {
	s := newSnap(t)
	ov, _ := s.Restore()
	got, ok := ov.Read("etc/os-release")
	if !ok {
		t.Fatal("Read falhou")
	}
	got[0] = 'X'
	again, _ := ov.Read("etc/os-release")
	if string(again) != "AOS-guest 1.0" {
		t.Fatalf("Read expos o buffer interno: %q", again)
	}
}

// TestOverlay_DiscardDropsDirtyState prova que Discard deita fora o estado sujo e o
// overlay não ressuscita (fail-closed em write/read).
func TestOverlay_DiscardDropsDirtyState(t *testing.T) {
	s := newSnap(t)
	ov, _ := s.Restore()
	_ = ov.Write("scratch/x", []byte("dirt"))
	if !ov.Dirty() {
		t.Fatal("esperava overlay sujo")
	}
	ov.Discard()
	if !ov.Discarded() {
		t.Fatal("esperava overlay descartado")
	}
	if ov.Dirty() {
		t.Fatal("overlay descartado nao devia reportar-se sujo")
	}
	if _, ok := ov.Read("scratch/x"); ok {
		t.Fatal("leu estado sujo apos Discard")
	}
	if err := ov.Write("scratch/y", []byte("z")); err != ErrOverlayDiscarded {
		t.Fatalf("esperava ErrOverlayDiscarded, obtive %v", err)
	}
	// Discard é idempotente.
	ov.Discard()
}

// TestOverlay_FreshRestoreIsClean prova que cada restore começa limpo (sem escritas).
func TestOverlay_FreshRestoreIsClean(t *testing.T) {
	s := newSnap(t)
	ov, _ := s.Restore()
	if ov.Dirty() {
		t.Fatal("overlay recem-restaurado devia estar limpo")
	}
}
