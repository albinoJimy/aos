package audit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

// AOS-221 — IMPOSIÇÃO da tamper-evidence do WORM. Prova FALSIFICÁVEL (dois sentidos) de
// que o load re-encadeia a hash-chain e NÃO só valida o CRC de framing do WAL.
//
// O DEFEITO que fecha: [OpenFileStore] só validava o CRC de cada registo do WAL. Um WAL
// cujo CONTEÚDO de um registo foi adulterado e o CRC recalculado passa o replay (CRC
// intacto) mas parte a hash-chain — e o load servia-o como íntegro. FALHA-ANTES: sem o
// re-encadeamento, [OpenFileStore] devolvia o store sem erro.

// tamperFirstFrameContent adultera o CONTEÚDO do PRIMEIRO registo do WAL preservando o
// comprimento (troca uma letra dentro de uma string do JSON) e RECALCULA o CRC de framing,
// de modo a que a camada de CRC aceite o registo adulterado. Devolve o offset tocado.
func tamperFirstFrameContent(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read wal: %v", err)
	}
	if len(data) < 8 {
		t.Fatalf("wal demasiado curto: %d bytes", len(data))
	}
	n := int(binary.BigEndian.Uint32(data[0:4]))
	payload := data[4 : 4+n]
	// A Capability do sampleRecord é "fs:write:/reports/*": trocar uma letra mantém o
	// JSON válido (o registo continua a desserializar e a ENTRAR na cadeia), muda o
	// conteúdo canónico (⇒ EntryHash recomputado diverge) e preserva o comprimento.
	idx := bytes.Index(payload, []byte("reports"))
	if idx < 0 {
		t.Fatalf("marcador de conteudo nao encontrado no payload do WAL")
	}
	payload[idx] = 'X' // "reports" -> "Xeports"
	// RECALCULA o CRC sobre o payload adulterado — a camada de framing passa a aceitá-lo.
	binary.BigEndian.PutUint32(data[4+n:4+n+4], crc32.Checksum(payload, crc32.MakeTable(crc32.IEEE)))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write wal: %v", err)
	}
}

// TestWORM_LoadRejectsTamperedChain_CRCValid — o cerne de AOS-221. Escreve dois registos,
// adultera o conteúdo do 1º com CRC RECALCULADO, e prova DOIS SENTIDOS:
//
//	(1) FALHA-ANTES explícita: a camada de CRC/framing (replayAuditWAL) ACEITA o WAL
//	    adulterado sem erro — é exactamente o que o load antigo fazia e porque o defeito
//	    passava despercebido;
//	(2) DEPOIS: OpenFileStore RECUSA o mesmo WAL (a hash-chain re-encadeada parte no seq 1).
func TestWORM_LoadRejectsTamperedChain_CRCValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	for i := 0; i < 2; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	tamperFirstFrameContent(t, path)

	// (1) A camada de CRC/framing ACEITA o WAL adulterado — falha-antes provada: sem o
	// re-encadeamento, o load passava com CRC intacto.
	recs, validEnd, err := replayAuditWAL(path)
	if err != nil {
		t.Fatalf("replayAuditWAL nao devia falhar com CRC recalculado: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("CRC/framing devia aceitar ambos os registos adulterados: len=%d", len(recs))
	}
	if fi, _ := os.Stat(path); fi != nil && validEnd != fi.Size() {
		t.Fatalf("CRC valido devia cobrir o ficheiro inteiro: validEnd=%d size=%d", validEnd, fi.Size())
	}

	// (2) DEPOIS: OpenFileStore re-encadeia e RECUSA (fail-closed).
	s2, err := OpenFileStore(path)
	if err == nil {
		_ = s2.Close()
		t.Fatalf("OpenFileStore devia RECUSAR um WAL com hash-chain adulterada (CRC valido)")
	}
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("erro devia desembrulhar para ErrTampered, veio: %v", err)
	}
}

// TestWORM_LoadAcceptsIntactChain — RETRO-COMPAT (o outro sentido): um WAL ÍNTEGRO abre na
// mesma e VerifyStore fecha em todas as partições. Sem isto, o teste acima seria vácuo
// (podia estar a recusar QUALQUER store).
func TestWORM_LoadAcceptsIntactChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worm.wal")
	ctx := context.Background()

	s := openWORM(t, path)
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, sampleRecord("run-a", DecisionAllow)); err != nil {
			t.Fatalf("append run-a %d: %v", i, err)
		}
	}
	if _, err := s.Append(ctx, sampleRecord("run-b", DecisionDeny)); err != nil {
		t.Fatalf("append run-b: %v", err)
	}
	_ = s.Close()

	s2, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("OpenFileStore de um WAL integro NAO devia falhar: %v", err)
	}
	defer s2.Close()

	n, err := VerifyStore(ctx, s2)
	if err != nil {
		t.Fatalf("VerifyStore de um store integro: %v", err)
	}
	if n != 2 {
		t.Fatalf("VerifyStore devia verificar 2 particoes, veio %d", n)
	}
}

// TestVerifyStore_DetectsTamperedPartition — VerifyStore (a via SEM chave privada usada
// no restart/pós-shred) detecta uma partição adulterada num MemStore. Adultera-se o
// EntryHash armazenado de um registo por reflexão do estado interno (white-box): o
// re-encadeamento parte. Prova que VerifyStore não é vácuo.
func TestVerifyStore_DetectsTamperedPartition(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	for i := 0; i < 3; i++ {
		if _, err := s.Append(ctx, sampleRecord("p", DecisionAllow)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	// Integro: passa.
	if n, err := VerifyStore(ctx, s); err != nil || n != 1 {
		t.Fatalf("VerifyStore integro: n=%d err=%v", n, err)
	}

	// Adultera o EntryHash do 2º registo em memória (white-box): a cadeia parte no seq 2.
	s.mu.Lock()
	s.parts["p"][1].EntryHash[0] ^= 0xFF
	s.mu.Unlock()

	_, err := VerifyStore(ctx, s)
	if err == nil {
		t.Fatalf("VerifyStore devia detectar a particao adulterada")
	}
	if !errors.Is(err, ErrTampered) {
		t.Fatalf("erro devia desembrulhar para ErrTampered, veio: %v", err)
	}
}

// TestVerifyStore_FailClosedWithoutPartitionLister — um Store que não expõe PartitionLister
// não pode ser percorrido: VerifyStore devolve ErrPartitionsUnavailable (fail-closed, não
// declara verificado o que não percorreu).
func TestVerifyStore_FailClosedWithoutPartitionLister(t *testing.T) {
	ctx := context.Background()
	_, err := VerifyStore(ctx, opaqueStore{NewMemStore()})
	if !errors.Is(err, ErrPartitionsUnavailable) {
		t.Fatalf("esperado ErrPartitionsUnavailable, veio: %v", err)
	}
}

// opaqueStore embrulha um Store escondendo o método Partitions (não promove PartitionLister
// porque só reexporta os métodos da interface Store).
type opaqueStore struct{ inner Store }

func (o opaqueStore) Append(ctx context.Context, rec AuditRecord) (AuditRecord, error) {
	return o.inner.Append(ctx, rec)
}
func (o opaqueStore) Read(ctx context.Context, p string, from, to uint64) ([]AuditRecord, error) {
	return o.inner.Read(ctx, p, from, to)
}
func (o opaqueStore) Head(ctx context.Context, p string) (uint64, error) {
	return o.inner.Head(ctx, p)
}
func (o opaqueStore) At(ctx context.Context, p string, seq uint64) (AuditRecord, bool, error) {
	return o.inner.At(ctx, p, seq)
}
