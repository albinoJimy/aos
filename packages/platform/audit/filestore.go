package audit

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// AOS-170 — WORM DURÁVEL. FileStore é uma implementação persistente de [Store] que
// sobrevive ao restart do nó: cada registo SELADO da hash-chain é gravado num WAL
// append-only (mesma mecânica de framing+crc+fsync do Event Store durável) e o
// arranque RECONSTRÓI a cadeia lendo o ficheiro. A hash-chain tamper-evident
// atravessa o reinício intacta — [Verify] continua a fechar após o restart.
//
// Zero dependências externas (só stdlib). O framing é DELIBERADAMENTE idêntico ao do
// Event Store durável (packages/substrate/eventstore/durable.go); a duplicação é
// intencional para manter os dois módulos independentes (sem um módulo partilhado de
// WAL e respectivos replace directives) — cada um tem ~50 linhas de log framed.
//
// FORMATO DE REGISTO:
//
//	uint32(len) BE || json(AuditRecord selado) len bytes || uint32(crc32 IEEE) BE
//
// CRASH-SAFETY: um registo final truncado (crash a meio de um write) ou com checksum
// inválido é detectado e ignorado no replay (pára no último registo íntegro). Como a
// cadeia é um prefixo, uma cadeia truncada continua a ser uma cadeia VÁLIDA até ao
// último registo íntegro — Verify(from, Head()) fecha. No Open, um tail parcial é
// truncado do ficheiro antes de reabrir em append (writes novos ficam contíguos).

const auditMaxRecordBytes = 64 << 20

var auditCRCTable = crc32.MakeTable(crc32.IEEE)

// FileStore é o [Store] WORM persistente em disco. Guarda os registos selados por
// partição em memória (para Read/Head/At O(1)-amortizado) E persiste cada Append no
// WAL antes de devolver (fsync). Seguro para concorrência.
type FileStore struct {
	mu    sync.RWMutex
	parts map[string][]AuditRecord

	wmu    sync.Mutex // serializa os writes ao ficheiro único
	f      *os.File
	w      *bufio.Writer
	closed bool
}

// OpenFileStore cria OU reabre um WORM durável respaldado pelo WAL em path. No
// arranque faz replay do ficheiro (crash-safe), reconstruindo as cadeias por
// partição na ordem de escrita, e reabre o ficheiro em append (truncando um tail
// parcial). Um path inexistente cria um WORM durável novo. Chame Close para fechar.
func OpenFileStore(path string) (*FileStore, error) {
	recs, validEnd, err := replayAuditWAL(path)
	if err != nil {
		return nil, fmt.Errorf("audit: replay do WAL %q: %w", path, err)
	}
	if fi, statErr := os.Stat(path); statErr == nil && fi.Size() > validEnd {
		if err := os.Truncate(path, validEnd); err != nil {
			return nil, fmt.Errorf("audit: truncar tail parcial do WAL %q: %w", path, err)
		}
		fsyncDir(filepath.Dir(path))
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: abrir WAL %q para append: %w", path, err)
	}
	// DURABILIDADE: em POSIX a entrada de directório de um ficheiro recém-criado só é
	// durável após fsync do directório pai; sem isto um crash logo após criar o WAL
	// poderia perder a entrada de directório apesar do File.Sync por registo. Best-effort.
	fsyncDir(filepath.Dir(path))
	s := &FileStore{
		parts: make(map[string][]AuditRecord),
		f:     f,
		w:     bufio.NewWriter(f),
	}
	// Reconstrói as cadeias por partição na ordem de escrita (a ordem no ficheiro é a
	// ordem de Append, que dentro de cada partição é a ordem de audit_seq).
	for _, rec := range recs {
		s.parts[rec.Partition] = append(s.parts[rec.Partition], rec)
	}

	// AOS-221 — RE-ENCADEAR NO LOAD (não só CRC). O replay acima só garantiu o
	// FRAMING (CRC de cada registo) do WAL; NÃO garante que o CONTEÚDO selado
	// ENCADEIA. Um WAL cujo registo foi adulterado e o CRC recalculado passa o replay
	// mas parte a hash-chain. Aqui re-encadeia-se cada partição reconstruída a partir da
	// GÉNESE (sem chave privada — é um encadeamento de hashes): audit_seq contíguo,
	// PrevHash encadeado, EntryHash recomputável. Uma cadeia adulterada RECUSA o Open
	// (fail-closed) — o WORM nunca serve como íntegro um WAL cuja cadeia está partida.
	// Um WORM intacto abre exactamente como antes (a verificação passa em silêncio).
	for _, part := range sortedPartitions(s.parts) {
		if err := verifyReplayedChain(part, s.parts[part]); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("audit: hash-chain adulterada no WAL %q: %w", path, err)
		}
	}
	return s, nil
}

// Partitions implementa [PartitionLister]: os nomes de todas as partições com
// registos, ordenados (determinismo ⇒ verificação reproduzível em [VerifyStore]).
func (s *FileStore) Partitions() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return sortedPartitions(s.parts)
}

// Append implementa [Store.Append]: sela o registo na cadeia da partição (idêntico a
// MemStore) e PERSISTE o registo selado no WAL com fsync ANTES de devolver — um
// registo cujo Append retornou está durável. O selo (seq/PrevHash/EntryHash) é
// idêntico ao in-memory, pelo que a cadeia reconstruída após restart é byte-a-byte a
// mesma.
//
// DONO DA CADEIA — SINGLE-WRITER (AOS-164b, CA de serialização sob N runs concorrentes).
// A secção crítica abaixo (s.mu.Lock … s.mu.Unlock) é o ÚNICO escritor da hash-chain, e é
// o DONO NOMEADO da ordenação por-partição: TODO o selo (leitura do último registo da
// partição → AuditSeq = last+1, PrevHash = last.EntryHash, EntryHash = ComputeEntryHash)
// E a persistência acontecem sob o MESMO s.mu, indivisíveis. Logo, com N goroutines (N
// runs) a fazer Append concorrente:
//   - na MESMA partição serializam-se aqui: AuditSeq fica contíguo (1..k, gapless), cada
//     PrevHash encadeia no EntryHash anterior e não há FORK (dois registos a partilhar o
//     mesmo AuditSeq/PrevHash) — a ordem total por-partição é a ordem de entrada no lock;
//   - em partições DIFERENTES não contendem na MESMA cadeia (o estado é `parts[Partition]`),
//     mas continuam serializadas pelo mesmo s.mu (a hash-chain global do ficheiro é uma só)
//     — cada cadeia por-partição é independentemente contígua e válida.
//
// A prova está em filestore_concurrency_test.go (-race). O wmu de [persist] é uma segunda
// linha defensiva para o ficheiro; o dono da ORDENAÇÃO da cadeia é este s.mu.
func (s *FileStore) Append(_ context.Context, rec AuditRecord) (AuditRecord, error) {
	s.mu.Lock()
	part := s.parts[rec.Partition]
	var prev []byte
	if len(part) == 0 {
		prev = GenesisHash(rec.Partition)
		rec.AuditSeq = 1
	} else {
		last := part[len(part)-1]
		prev = last.EntryHash
		rec.AuditSeq = last.AuditSeq + 1
	}
	rec.PrevHash = prev
	rec.EntryHash = ComputeEntryHash(prev, rec)
	sealed := cloneRecord(rec)

	// Persiste ANTES de publicar o registo em memória: se o fsync falhar, o registo
	// NÃO entra na cadeia in-memory (fail-closed) e o audit_seq não é consumido — a
	// próxima tentativa reusa a mesma posição. Evita divergência memória-vs-disco.
	if err := s.persist(sealed); err != nil {
		s.mu.Unlock()
		return AuditRecord{}, fmt.Errorf("audit: persistir registo selado: %w", err)
	}
	s.parts[rec.Partition] = append(part, sealed)
	s.mu.Unlock()
	return cloneRecord(sealed), nil
}

// persist grava um registo framed e faz fsync. Chamado com s.mu detido; usa o seu
// próprio wmu para o caso (defensivo) de writes concorrentes ao ficheiro.
func (s *FileStore) persist(rec AuditRecord) error {
	payload, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if len(payload) > auditMaxRecordBytes {
		return fmt.Errorf("registo demasiado grande (%d bytes)", len(payload))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	var tr [4]byte
	binary.BigEndian.PutUint32(tr[:], crc32.Checksum(payload, auditCRCTable))

	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.closed {
		return errors.New("audit: file store fechado")
	}
	if _, err := s.w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := s.w.Write(payload); err != nil {
		return err
	}
	if _, err := s.w.Write(tr[:]); err != nil {
		return err
	}
	if err := s.w.Flush(); err != nil {
		return err
	}
	return s.f.Sync()
}

// Read implementa [Store.Read].
func (s *FileStore) Read(_ context.Context, partition string, from, to uint64) ([]AuditRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	part := s.parts[partition]
	out := make([]AuditRecord, 0, len(part))
	for _, r := range part {
		if r.AuditSeq >= from && r.AuditSeq <= to {
			out = append(out, cloneRecord(r))
		}
	}
	return out, nil
}

// Head implementa [Store.Head].
func (s *FileStore) Head(_ context.Context, partition string) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	part := s.parts[partition]
	if len(part) == 0 {
		return 0, nil
	}
	return part[len(part)-1].AuditSeq, nil
}

// At implementa [Store.At].
func (s *FileStore) At(_ context.Context, partition string, seq uint64) (AuditRecord, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.parts[partition] {
		if r.AuditSeq == seq {
			return cloneRecord(r), true, nil
		}
	}
	return AuditRecord{}, false, nil
}

// Close descarrega e fecha o ficheiro do WAL (idempotente). Registos já Append'd
// foram fsync'd individualmente; este close garante o descarregamento final.
func (s *FileStore) Close() error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	ferr := s.w.Flush()
	serr := s.f.Sync()
	cerr := s.f.Close()
	if ferr != nil {
		return ferr
	}
	if serr != nil {
		return serr
	}
	return cerr
}

// fsyncDir torna durável a entrada de directório (criação/truncatura de ficheiros nele
// contidos), como exige o POSIX para a durabilidade real de um ficheiro novo. Best-effort
// e zero-dep: em plataformas onde sincronizar um handle de directório não é suportado
// (ex.: Windows) o erro é ignorado — a durabilidade do CONTEÚDO mantém-se via File.Sync.
func fsyncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// replayAuditWAL lê os registos íntegros do WAL e o offset do fim do último íntegro.
// Crash-safe: pára no primeiro registo truncado/corrompido, sem erro.
func replayAuditWAL(path string) ([]AuditRecord, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	var out []AuditRecord
	var validEnd int64
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			break // EOF limpo ou header truncado.
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 || n > auditMaxRecordBytes {
			break
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			break
		}
		var tr [4]byte
		if _, err := io.ReadFull(r, tr[:]); err != nil {
			break
		}
		if binary.BigEndian.Uint32(tr[:]) != crc32.Checksum(payload, auditCRCTable) {
			break
		}
		var rec AuditRecord
		if err := json.Unmarshal(payload, &rec); err != nil {
			break
		}
		out = append(out, rec)
		validEnd += int64(4 + int(n) + 4)
	}
	return out, validEnd, nil
}
