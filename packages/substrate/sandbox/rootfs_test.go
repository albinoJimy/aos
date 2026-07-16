package sandbox

import (
	"errors"
	"sync"
	"testing"
)

// newTestSnapshot constrói um snapshot base determinista com um ficheiro read-only.
func newTestSnapshot(t *testing.T) *Snapshot {
	t.Helper()
	snap, err := NewSnapshot("img/v1", map[string][]byte{
		"etc/config": []byte("base-config"),
		"bin/tool":   []byte("base-binary"),
	})
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	return snap
}

func mountFresh(t *testing.T, snap *Snapshot) *RootFS {
	t.Helper()
	ov, _ := snap.Restore()
	fs, err := MountReadOnly(ov)
	if err != nil {
		t.Fatalf("MountReadOnly: %v", err)
	}
	return fs
}

// TestRootFS_MountNilOverlay confirma fail-closed sem overlay.
func TestRootFS_MountNilOverlay(t *testing.T) {
	if _, err := MountReadOnly(nil); !errors.Is(err, ErrNilOverlay) {
		t.Fatalf("MountReadOnly(nil) = %v, quero ErrNilOverlay", err)
	}
}

// TestSecurity_WriteRootRejected é o critério AOS-066: uma escrita na RAIZ
// read-only é rejeitada de forma controlada (ErrReadOnlyRoot), sem panic, e o base
// NUNCA é mutado.
func TestSecurity_WriteRootRejected(t *testing.T) {
	snap := newTestSnapshot(t)
	digestBefore := snap.Digest()
	fs := mountFresh(t, snap)

	err := fs.WriteRoot("etc/config", []byte("attacker-owned"))
	if !errors.Is(err, ErrReadOnlyRoot) {
		t.Fatalf("WriteRoot = %v, quero ErrReadOnlyRoot", err)
	}
	// A raiz read-only não foi tocada: o ficheiro base mantém o conteúdo original.
	got, ok := fs.Read("etc/config")
	if !ok || string(got) != "base-config" {
		t.Fatalf("após WriteRoot rejeitada, Read = (%q,%v), quero (base-config,true)", got, ok)
	}
	// O digest do base é estável: prova estrutural de que a raiz é imutável.
	if snap.Digest() != digestBefore {
		t.Fatal("digest do base mudou: a raiz read-only foi mutada")
	}
	if fs.Dirty() {
		t.Fatal("overlay ficou sujo após uma escrita na raiz rejeitada")
	}
}

// TestSecurity_OverlayWriteWorksAndDisappears é o critério AOS-066: a escrita no
// OVERLAY funciona e DESAPARECE após destroy (Discard).
func TestSecurity_OverlayWriteWorksAndDisappears(t *testing.T) {
	snap := newTestSnapshot(t)
	fs := mountFresh(t, snap)

	if err := fs.WriteOverlay("tmp/scratch", []byte("ephemeral")); err != nil {
		t.Fatalf("WriteOverlay: %v", err)
	}
	got, ok := fs.Read("tmp/scratch")
	if !ok || string(got) != "ephemeral" {
		t.Fatalf("Read(overlay) = (%q,%v), quero (ephemeral,true)", got, ok)
	}
	if !fs.Dirty() {
		t.Fatal("overlay devia estar sujo após WriteOverlay")
	}

	// DESTROY: descarta o overlay efémero.
	fs.Discard()
	if !fs.Discarded() {
		t.Fatal("overlay devia estar descartado após Discard")
	}
	// A escrita desapareceu: um overlay descartado não devolve o ficheiro.
	if _, ok := fs.Read("tmp/scratch"); ok {
		t.Fatal("ficheiro do overlay sobreviveu ao Discard")
	}
	// Escrever num overlay descartado falha fail-closed.
	if err := fs.WriteOverlay("tmp/again", []byte("x")); !errors.Is(err, ErrOverlayDiscarded) {
		t.Fatalf("WriteOverlay pós-Discard = %v, quero ErrOverlayDiscarded", err)
	}
}

// TestIsolation_NPlusOneDoesNotSeeN é o critério AOS-066: um ficheiro criado na
// execução N está AUSENTE na execução N+1 (overlay novo do mesmo base imutável).
func TestIsolation_NPlusOneDoesNotSeeN(t *testing.T) {
	snap := newTestSnapshot(t)

	// Execução N: escreve no overlay e descarta no fim (destroy).
	fsN := mountFresh(t, snap)
	if err := fsN.WriteOverlay("run/secret", []byte("data-de-N")); err != nil {
		t.Fatalf("N WriteOverlay: %v", err)
	}
	if _, ok := fsN.Read("run/secret"); !ok {
		t.Fatal("N não vê o próprio ficheiro")
	}
	fsN.Discard()

	// Execução N+1: restore NOVO do mesmo base imutável.
	fsN1 := mountFresh(t, snap)
	if _, ok := fsN1.Read("run/secret"); ok {
		t.Fatal("ISOLAMENTO QUEBRADO: N+1 observa o ficheiro escrito por N")
	}
	// N+1 continua a ver a raiz read-only intacta.
	if got, ok := fsN1.Read("etc/config"); !ok || string(got) != "base-config" {
		t.Fatalf("N+1 Read(raiz) = (%q,%v), quero (base-config,true)", got, ok)
	}
	// Os overlays são distintos (nunca reciclados).
	if fsN.OverlayID() == fsN1.OverlayID() {
		t.Fatal("overlay de N+1 reutilizou o id de N")
	}
	// A mesma imagem base entre execuções (prova de imutabilidade partilhada).
	if fsN.BaseDigest() != fsN1.BaseDigest() {
		t.Fatal("base digest divergiu entre execuções da mesma imagem")
	}
	if fsN1.ImageVersion() != "img/v1" {
		t.Fatalf("ImageVersion = %q, quero img/v1", fsN1.ImageVersion())
	}
	if !fsN1.ReadOnlyRoot() {
		t.Fatal("ReadOnlyRoot devia ser true")
	}
}

// TestRootFS_ConcurrentAccess exercita Read/Write/Discard em paralelo (o -race
// guarda a delegação no Overlay).
func TestRootFS_ConcurrentAccess(t *testing.T) {
	snap := newTestSnapshot(t)
	fs := mountFresh(t, snap)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = fs.WriteOverlay("k", []byte{byte(n)})
			_, _ = fs.Read("k")
			_, _ = fs.Read("etc/config")
			_ = fs.WriteRoot("etc/config", []byte("x"))
			_ = fs.Dirty()
		}(i)
	}
	wg.Wait()
}
