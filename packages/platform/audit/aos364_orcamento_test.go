package audit

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAOS364_Orcamento_RessincronizacaoNaoPrende cobre o achado F1 da revisão adversarial v2: sem
// tecto, o varrimento byte-a-byte da ressincronização é O(n²) sobre 64MB, e um adversário com
// escrita no WAL prende o arranque minutos ao inflar o comprimento de um frame num WAL grande
// (medido antes do tecto: 1MB→10s, 2MB→>300s). Com o orçamento fail-closed, o arranque termina
// depressa — RECUSANDO (nunca truncando). Constrói um WAL de ~1,5MB, infla o comprimento do 2º
// frame, e exige que OpenFileStore devolva erro em muito menos tempo do que os 10s medidos.
func TestAOS364_Orcamento_RessincronizacaoNaoPrende(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	// ~1,5MB de registos (cada sampleRecord ~algumas centenas de bytes).
	for i := 0; i < 4000; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_ = s.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data) < 512*1024 {
		t.Fatalf("WAL demasiado pequeno para o teste de orçamento: %d bytes", len(data))
	}
	sizeAntes := int64(len(data))
	// Infla o comprimento do frame de índice 1 (byte alto) — desalinha o leitor e força a
	// ressincronização a varrer.
	n0 := int(binary.BigEndian.Uint32(data[0:4]))
	off1 := 4 + n0 + 4
	data[off1] = 0xFF // comprimento enorme mas ≤ 64MB? 0xFF... byte alto ⇒ pode exceder max
	// Garante um comprimento dentro de (remaining, 64MB]: põe o valor a ~10MB.
	binary.BigEndian.PutUint32(data[off1:off1+4], 10<<20)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, e := OpenFileStore(path)
		done <- e
	}()

	select {
	case e := <-done:
		// Tem de RECUSAR (dano interior: há frames íntegros a seguir, ou a ressincronização é
		// inconclusiva por orçamento — ambos fail-closed).
		if !errors.Is(e, ErrWORMDanoInterior) {
			t.Fatalf("devia RECUSAR (ErrWORMDanoInterior), veio: %v", e)
		}
		if fi, _ := os.Stat(path); fi != nil && fi.Size() != sizeAntes {
			t.Fatalf("ficheiro AMPUTADO num dano recusado: %d -> %d", sizeAntes, fi.Size())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("OpenFileStore prendeu >30s — o orçamento de ressincronização (F1) não está a limitar o varrimento")
	}
}
