package audit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// AOS-373 — LER A TRILHA NÃO PODE ENCURTAR A PROVA.
//
// `aos audit-trail` abria o WORM por [OpenFileStore], que sobre uma cauda rasgada TRUNCA o
// ficheiro a validEnd antes de o reabrir em append. Correr a ferramenta de LEITURA sobre a
// prova forense apagava o registo em voo — o próprio artefacto de uma investigação — e
// falhava num mount `:ro`. [OpenFileStoreReadOnly] não anexa nem trunca; estes testes
// provam-no, e provam que o fail-closed do DANO INTERIOR não regride.

// TestAOS373_ReadOnly_NaoTruncaCaudaRasgada é o AC4 (metade positiva) e a prova STRUTURAL da
// tolerância a `:ro` (no molde de eventstore TestAOS347_InspeccaoNaoTrunca, por tamanho — não
// por chmod, frágil no win32): um WORM com cauda GENUINAMENTE rasgada, aberto por
// [OpenFileStoreReadOnly], não muda de tamanho E serve o prefixo íntegro por Read.
func TestAOS373_ReadOnly_NaoTruncaCaudaRasgada(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_ = s.Close()

	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// Corta os últimos 5 bytes do 3.º registo: perde trailer + 1 byte de payload ⇒ short read
	// ⇒ cauda rasgada (bytes em FALTA, não frame completo corrompido). O prefixo íntegro são
	// os 2 primeiros registos.
	rasgado := full[:len(full)-5]
	if err := os.WriteFile(path, rasgado, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	sizeAntes := int64(len(rasgado))

	ro, err := OpenFileStoreReadOnly(path)
	if err != nil {
		t.Fatalf("OpenFileStoreReadOnly sobre cauda rasgada devia ABRIR, veio erro: %v", err)
	}
	defer ro.Close()

	// (a) o ficheiro NÃO foi encurtado — a prova em voo continua no disco intacta.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat depois: %v", err)
	}
	if fi.Size() != sizeAntes {
		t.Fatalf("a LEITURA truncou a prova: size antes=%d depois=%d — OpenFileStoreReadOnly não pode escrever", sizeAntes, fi.Size())
	}

	// (b) e serve o prefixo íntegro (2 registos): a leitura útil não regride.
	recs, err := ro.Read(ctx, "p", 1, 1<<40)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("o prefixo íntegro tem 2 registos (o 3.º foi rasgado), Read devolveu %d", len(recs))
	}
	if h, _ := ro.Head(ctx, "p"); h != 2 {
		t.Fatalf("Head devia ser 2 (prefixo íntegro), veio %d", h)
	}
}

// TestAOS373_WritePath_AindaTrunca é o CONTROLO NEGATIVO exigido pela CA4: a MESMA cauda
// rasgada, aberta pelo caminho de ESCRITA ([OpenFileStore]), CONTINUA a truncar a validEnd.
// Sem isto, a asserção «tamanho inalterado» do teste read-only poderia estar a medir a
// ausência de dano, e não a supressão da truncatura pelo caminho novo.
func TestAOS373_WritePath_AindaTrunca(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_ = s.Close()

	full, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	rasgado := full[:len(full)-5]
	if err := os.WriteFile(path, rasgado, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	sizeCorrompido := int64(len(rasgado))

	wr, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore sobre cauda rasgada devia ABRIR (crash-safety): %v", err)
	}
	defer wr.Close()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() >= sizeCorrompido {
		t.Fatalf("o caminho de ESCRITA devia TRUNCAR a cauda parcial (size < %d), veio %d — o controlo negativo caiu", sizeCorrompido, fi.Size())
	}
	// E a truncatura reflecte-se no head: o 3.º registo rasgado desapareceu, sobram 2.
	if h, _ := wr.Head(ctx, "p"); h != 2 {
		t.Fatalf("head pós-truncatura devia ser 2, veio %d", h)
	}
}

// TestAOS373_ReadOnly_DanoInteriorFalhaFechado: o fail-closed do DANO INTERIOR não regride em
// leitura. Um WORM com corrupção de CRC num frame INTERIOR (framing intacto), aberto por
// [OpenFileStoreReadOnly], RECUSA com [DanoInteriorError] e NÃO toca no ficheiro — um WORM
// corrompido nunca se serve como íntegro, nem sequer a uma leitura.
func TestAOS373_ReadOnly_DanoInteriorFalhaFechado(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	for i := 0; i < 4; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_ = s.Close()

	fiAntes, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat antes: %v", err)
	}
	corromperTrailerCRCdoFrame(t, path, 1) // frame interior, framing intacto

	_, openErr := OpenFileStoreReadOnly(path)
	if !errors.Is(openErr, ErrWORMDanoInterior) {
		t.Fatalf("dano interior devia RECUSAR também a leitura com ErrWORMDanoInterior, veio: %v", openErr)
	}

	fiDepois, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat depois: %v", err)
	}
	if fiDepois.Size() != fiAntes.Size() {
		t.Fatalf("a recusa não pode tocar no ficheiro: size antes=%d depois=%d", fiAntes.Size(), fiDepois.Size())
	}
	_ = ctx
}

// TestAOS373_Append_RecusaReadOnly: uma escrita num store de inspecção recusa com
// [ErrAuditReadOnly] ANTES de qualquer efeito (o gémeo de eventstore ErrReadOnly). Prova
// também que Read serve o WORM íntegro e que Close tolera os handles nil (não faz nil-deref).
func TestAOS373_Append_RecusaReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	_ = s.Close()

	ro, err := OpenFileStoreReadOnly(path)
	if err != nil {
		t.Fatalf("OpenFileStoreReadOnly: %v", err)
	}

	// Read funciona sobre o handle nil (serve de s.parts sob RLock).
	recs, err := ro.Read(ctx, "p", 1, 1<<40)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("Read devia servir os 3 registos íntegros, veio %d", len(recs))
	}

	// Append recusa fail-closed, sem tocar em s.f/s.w (que são nil).
	if _, err := ro.Append(ctx, sampleRecord("p", DecisionAllow)); !errors.Is(err, ErrAuditReadOnly) {
		t.Fatalf("Append num store de inspecção = %v, quero ErrAuditReadOnly", err)
	}

	// Close tolera f/w nil (sem nil-deref em Flush/Sync).
	if err := ro.Close(); err != nil {
		t.Fatalf("Close de um store read-only devia ser no-op sem erro, veio: %v", err)
	}
}
