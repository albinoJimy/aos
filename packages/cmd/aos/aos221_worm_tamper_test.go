package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"testing"

	audit "github.com/aos-ref/platform/audit"
)

// AOS-221 — IMPOSIÇÃO da tamper-evidence do WORM, AO NÍVEL DO NÓ. Prova FALSIFICÁVEL (dois
// sentidos, -race) de que o arranque do nó re-encadeia e verifica a hash-chain do WORM
// DURÁVEL no load (não só o CRC de framing) e que uma cadeia adulterada IMPEDE o arranque
// fail-closed. FALHA-ANTES: sem o re-encadeamento, o load passava com o CRC intacto e o nó
// subia a servir um WORM cuja cadeia estava partida.

const aos221Marker = "AOS221-TAMPER-MARKER"

// tamperWALMarker localiza o registo do WAL cujo payload contém marker, adultera-o
// preservando o comprimento (flip de um bit numa letra ASCII ⇒ JSON continua válido, o
// registo continua a desserializar) e RECALCULA o CRC do frame — de modo a que a camada de
// CRC/framing ACEITE o registo adulterado (é o defeito que AOS-221 fecha: CRC intacto,
// hash-chain partida).
func tamperWALMarker(t *testing.T, path, marker string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wal: %v", err)
	}
	tbl := crc32.MakeTable(crc32.IEEE)
	off := 0
	for off+8 <= len(data) {
		n := int(binary.BigEndian.Uint32(data[off : off+4]))
		if n == 0 || off+4+n+4 > len(data) {
			break
		}
		payload := data[off+4 : off+4+n]
		if i := bytes.Index(payload, []byte(marker)); i >= 0 {
			payload[i] ^= 0x20 // 'M' -> 'm': muda o conteúdo, mantém ASCII/JSON válido
			binary.BigEndian.PutUint32(data[off+4+n:off+4+n+4], crc32.Checksum(payload, tbl))
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("write wal: %v", err)
			}
			return
		}
		off += 4 + n + 4
	}
	t.Fatalf("marcador %q nao encontrado no WAL", marker)
}

// sealMarkerRecord sela um registo com o marcador conhecido na cadeia WORM do nó.
func sealMarkerRecord(t *testing.T, ctx context.Context, node *Node) {
	t.Helper()
	if _, err := node.WORM.Append(ctx, audit.AuditRecord{
		Partition:  "aos221.p",
		Decision:   audit.DecisionAllow,
		Capability: aos221Marker,
	}); err != nil {
		t.Fatalf("append registo marcador: %v", err)
	}
}

// TestNode_RestartRejectsTamperedWORM — o arranque com um WORM DURÁVEL cuja cadeia foi
// adulterada (CRC recalculado ⇒ framing intacto) é RECUSADO fail-closed. Dois sentidos:
//
//	(A) RETRO-COMPAT: um WORM íntegro reabre e o nó arranca; VerifyWORM fecha.
//	(B) FALHA-ANTES/DEPOIS: adulterado o conteúdo de um registo com CRC recalculado, o
//	    re-Bootstrap ABORTA (audit.ErrTampered). Sem o re-encadeamento no load, o Open
//	    passaria (o teste (A) prova que não é vácuo — um store íntegro arranca).
func TestNode_RestartRejectsTamperedWORM(t *testing.T) {
	ctx := context.Background()

	// (A) RETRO-COMPAT: íntegro reabre.
	t.Run("integro_reabre", func(t *testing.T) {
		wormPath := filepath.Join(t.TempDir(), "worm.wal")
		cfg := tnBaseConfig()
		cfg.WORMPath = wormPath

		n1, err := Bootstrap(ctx, cfg, io.Discard)
		if err != nil {
			t.Fatalf("bootstrap 1: %v", err)
		}
		sealMarkerRecord(t, ctx, n1)
		if err := n1.Close(); err != nil {
			t.Fatalf("close 1: %v", err)
		}

		n2, err := Bootstrap(ctx, cfg, io.Discard)
		if err != nil {
			t.Fatalf("re-bootstrap de um WORM integro NAO devia falhar: %v", err)
		}
		defer n2.Close()
		if err := n2.VerifyWORM(ctx); err != nil {
			t.Fatalf("VerifyWORM de um WORM integro: %v", err)
		}
		if h, _ := n2.WORM.Head(ctx, "aos221.p"); h != 1 {
			t.Fatalf("registo marcador ausente apos restart integro: head=%d", h)
		}
	})

	// (B) TAMPER: adulterado com CRC recalculado ⇒ o arranque ABORTA.
	t.Run("adulterado_aborta", func(t *testing.T) {
		wormPath := filepath.Join(t.TempDir(), "worm.wal")
		cfg := tnBaseConfig()
		cfg.WORMPath = wormPath

		n1, err := Bootstrap(ctx, cfg, io.Discard)
		if err != nil {
			t.Fatalf("bootstrap 1: %v", err)
		}
		sealMarkerRecord(t, ctx, n1)
		if err := n1.Close(); err != nil {
			t.Fatalf("close 1: %v", err)
		}

		tamperWALMarker(t, wormPath, aos221Marker)

		n2, err := Bootstrap(ctx, cfg, io.Discard)
		if err == nil {
			_ = n2.Close()
			t.Fatalf("o arranque devia ABORTAR fail-closed com o WORM adulterado (CRC valido)")
		}
		if !errors.Is(err, audit.ErrTampered) {
			t.Fatalf("erro de arranque devia desembrulhar para audit.ErrTampered, veio: %v", err)
		}
	})
}

// TestNode_VerifyWORM_PostShredPositive — a via PÓS-SHRED (VerifyWORM) fecha sobre a cadeia
// do nó depois de registos selados. É o método que o handler /dsar/erase corre pós-shred; o
// caminho de DETECÇÃO é provado no audit (TestVerifyStore_DetectsTamperedPartition) e na
// sub-prova (B) acima. Aqui prova-se que a via NÃO é vácua no sentido positivo: um WORM
// integro do nó verifica sem falso-positivo.
func TestNode_VerifyWORM_PostShredPositive(t *testing.T) {
	ctx := context.Background()
	node, err := Bootstrap(ctx, tnBaseConfig(), io.Discard)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	defer node.Close()
	sealMarkerRecord(t, ctx, node)
	sealMarkerRecord(t, ctx, node)
	if err := node.VerifyWORM(ctx); err != nil {
		t.Fatalf("VerifyWORM apos selar registos integros: %v", err)
	}
}
