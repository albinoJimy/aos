package eventstore

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
	"sort"
	"sync"
)

// AOS-170 — SUBSTRATO DURÁVEL do Event Store. Uma camada de persistência em disco
// ADITIVA ao Store in-memory: um WAL (write-ahead log) append-only que grava cada
// evento COMMITADO e um Open/Reopen que no arranque RECONSTRÓI o store byte-por-byte
// via IngestStream (o mesmo caminho de restauro de AOS-101 que preserva o envelope).
// Zero dependências externas — só stdlib (os, bufio, encoding/json, encoding/binary,
// hash/crc32, sync, sort).
//
// GARANTIAS (o reinício NÃO perde nem DUPLICA trabalho):
//
//   - DURABILIDADE: cada evento committed é escrito e fsync'd (File.Sync) ANTES de
//     Append devolver sucesso. Um evento cujo Append retornou committed sobrevive a
//     um crash.
//   - FIDELIDADE: o replay reconstrói exactamente os mesmos eventos/seq/heads. O
//     envelope (EventID/Ts/Seq/IdempotencyKey) é reinserido intacto via IngestStream
//     — nada é reatribuído.
//   - IDEMPOTÊNCIA/CAS após restart: IngestStream reconstrói o índice de dedup por
//     stream (idempotency_key→seq) e o head (lastSeq), pelo que a deduplicação e a
//     concorrência optimista (WithExpectedSeq) barram colisões tal como antes.
//   - CRASH-SAFETY: uma escrita PARCIAL/truncada no fim do ficheiro (crash a meio de
//     um write) ou um registo com checksum inválido é DETECTADO e IGNORADO no replay
//     (pára no último registo íntegro) — nunca corrompe o store nem duplica.
//
// FORMATO DE REGISTO (framing que detecta truncamento):
//
//	uint32(len)  big-endian   — comprimento do payload
//	payload      len bytes    — json(Event)
//	uint32(crc)  big-endian   — crc32(IEEE) do payload
//
// O comprimento permite saber quantos bytes ler; se o ficheiro acabar antes de um
// registo completo (header, payload ou checksum truncados), o registo é descartado.
// O checksum apanha corrupção silenciosa dentro de um registo de comprimento válido.

// maxRecordBytes limita o tamanho de um registo lido do WAL. Um comprimento acima
// deste (provável lixo de um header corrompido) é tratado como fim-de-log íntegro:
// paramos, em vez de tentar alocar gigabytes. 64 MiB é folgado para um envelope.
const maxRecordBytes = 64 << 20

// crcTable é a tabela IEEE reutilizada (evita realocar por registo).
var crcTable = crc32.MakeTable(crc32.IEEE)

// wal é o escritor append-only do log em disco. Serializa os writes com o seu
// próprio mutex (appends de streams distintos correm concorrentes no Store, mas o
// ficheiro é único) e faz fsync após cada registo para durabilidade real.
type wal struct {
	mu     sync.Mutex
	f      *os.File
	w      *bufio.Writer
	closed bool
}

// openWALAppend abre (criando se necessário) o ficheiro do WAL em modo append para
// escrita de novos eventos committed.
func openWALAppend(path string) (*wal, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	// DURABILIDADE: em POSIX a entrada de directório de um ficheiro RECÉM-CRIADO só
	// é durável após fsync do directório pai — sem isto, um crash logo após criar o
	// WAL e gravar o 1º registo (já com File.Sync) pode perder a própria entrada de
	// directório, deixando um ficheiro de tamanho 0 ou nenhum. Torna a criação durável.
	fsyncDir(filepath.Dir(path))
	return &wal{f: f, w: bufio.NewWriter(f)}, nil
}

// fsyncDir torna durável a entrada de directório (criação/truncatura de ficheiros
// nele contidos), como exige o POSIX para a durabilidade real de um ficheiro novo.
// Best-effort e sem dependências externas: em plataformas onde sincronizar um handle
// de directório não é suportado (ex.: Windows) o erro é ignorado — a durabilidade do
// CONTEÚDO mantém-se via File.Sync por registo.
func fsyncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// append escreve um registo framed do evento e faz fsync. Só regressa após o dado
// estar durável no disco (flush do buffer + File.Sync).
func (w *wal) append(ev Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("eventstore/wal: marshal: %w", err)
	}
	if len(payload) > maxRecordBytes {
		return fmt.Errorf("eventstore/wal: registo demasiado grande (%d bytes)", len(payload))
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	var tr [4]byte
	binary.BigEndian.PutUint32(tr[:], crc32.Checksum(payload, crcTable))

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrClosed
	}
	if _, err := w.w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := w.w.Write(payload); err != nil {
		return err
	}
	if _, err := w.w.Write(tr[:]); err != nil {
		return err
	}
	if err := w.w.Flush(); err != nil {
		return err
	}
	// fsync: durabilidade real — o evento committed sobrevive a um crash do processo/SO.
	return w.f.Sync()
}

// close descarrega e fecha o ficheiro do WAL (idempotente).
func (w *wal) close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	ferr := w.w.Flush()
	serr := w.f.Sync()
	cerr := w.f.Close()
	if ferr != nil {
		return ferr
	}
	if serr != nil {
		return serr
	}
	return cerr
}

// replayWAL lê o ficheiro do WAL e devolve os eventos íntegros por ordem de escrita
// e o OFFSET de bytes do fim do último registo íntegro (validEnd). CRASH-SAFETY:
// pára no PRIMEIRO registo incompleto (header/payload/checksum truncados por um crash
// a meio de um write) ou com checksum inválido — descarta-o e devolve tudo o que o
// precede, SEM erro. O validEnd permite ao Open TRUNCAR um tail parcial antes de
// reabrir em append, para que os writes novos fiquem contíguos e replayáveis (sem
// isto, uns bytes parciais no meio tornariam registos posteriores inalcançáveis). Um
// ficheiro inexistente devolve zero eventos e offset 0 (arranque a frio).
func replayWAL(path string) ([]Event, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	var out []Event
	var validEnd int64
	for {
		var hdr [4]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			// EOF limpo (fim do log) OU header truncado (crash a meio) — em ambos
			// paramos no último registo íntegro. Nunca é um erro de integridade.
			break
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 || n > maxRecordBytes {
			// Comprimento absurdo ⇒ header corrompido/lixo de um write parcial: pára.
			break
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			break // payload truncado ⇒ registo final incompleto: descarta.
		}
		var tr [4]byte
		if _, err := io.ReadFull(r, tr[:]); err != nil {
			break // checksum truncado ⇒ registo final incompleto: descarta.
		}
		if binary.BigEndian.Uint32(tr[:]) != crc32.Checksum(payload, crcTable) {
			break // checksum inválido ⇒ corrupção: pára no último registo íntegro.
		}
		var ev Event
		if err := json.Unmarshal(payload, &ev); err != nil {
			break // payload não desserializa ⇒ trata como corrupção: pára.
		}
		out = append(out, ev)
		validEnd += int64(4 + int(n) + 4)
	}
	return out, validEnd, nil
}

// Open cria OU reabre um Event Store DURÁVEL respaldado pelo WAL em path. No
// arranque:
//
//  1. constrói o Store in-memory (mesmas Option de New: réplicas, quórum, soberania);
//  2. faz replay do WAL (crash-safe: ignora um registo final truncado/corrompido);
//  3. reconstrói o store via IngestStream por stream, na ordem de seq — envelope
//     intacto, dedup e CAS reconstruídos;
//  4. anexa o WAL em modo append para que os Append FUTUROS persistam (fsync) antes
//     de devolver committed.
//
// Reabrir sobre o MESMO path restaura exactamente os eventos/seq/heads anteriores
// (fidelidade byte-a-byte do envelope). Um path inexistente cria um store durável
// novo (WAL vazio). Chame Close para libertar o Store e fechar o WAL.
func Open(path string, opts ...Option) (*Store, error) {
	events, validEnd, err := replayWAL(path)
	if err != nil {
		return nil, fmt.Errorf("eventstore: replay do WAL %q: %w", path, err)
	}
	// CRASH-SAFETY: se o ficheiro tinha um tail parcial/corrompido para lá do último
	// registo íntegro, trunca-o a validEnd ANTES de reabrir em append — assim os
	// eventos novos ficam contíguos e replayáveis (bytes parciais no meio tornariam
	// registos posteriores inalcançáveis). Idempotente quando não há tail parcial.
	if fi, statErr := os.Stat(path); statErr == nil && fi.Size() > validEnd {
		if err := os.Truncate(path, validEnd); err != nil {
			return nil, fmt.Errorf("eventstore: truncar tail parcial do WAL %q: %w", path, err)
		}
		// Torna durável a nova dimensão do ficheiro (metadados do directório) após a
		// truncatura do tail parcial, pela mesma razão POSIX (best-effort).
		fsyncDir(filepath.Dir(path))
	}
	s, err := New(opts...)
	if err != nil {
		return nil, err
	}
	if err := restoreInto(s, events); err != nil {
		_ = s.Close()
		return nil, err
	}
	w, err := openWALAppend(path)
	if err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("eventstore: abrir WAL %q para append: %w", path, err)
	}
	s.wal = w
	return s, nil
}

// Reopen é um alias explícito de Open para o caminho de ARRANQUE do nó (reconstrói o
// store a partir do disco). Semântica idêntica: cria-or-reabre.
func Reopen(path string, opts ...Option) (*Store, error) { return Open(path, opts...) }

// restoreInto reinsere no store, PRESERVANDO o envelope, os eventos lidos do WAL.
// Agrupa por stream e ordena por seq ascendente (a ordem no ficheiro pode intercalar
// streams distintos, mas a ordem-por-stream é a de seq), depois chama IngestStream
// por stream — o mesmo caminho de restauro de AOS-101 que valida gapless, reinsere o
// envelope intacto e reconstrói dedup/CAS. Registos duplicados do mesmo (stream,seq)
// — que um WAL bem-formado nunca produz — fariam IngestStream falhar por não-gapless,
// sinal de corrupção que preferimos expor a mascarar.
func restoreInto(s *Store, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	byStream := make(map[string][]Event)
	for _, ev := range events {
		byStream[ev.StreamID] = append(byStream[ev.StreamID], ev)
	}
	// Ordem determinística dos streams (não afecta a correcção — cada stream é
	// independente — mas torna o replay reproduzível).
	streams := make([]string, 0, len(byStream))
	for id := range byStream {
		streams = append(streams, id)
	}
	sort.Strings(streams)

	ctx := context.Background()
	for _, id := range streams {
		evs := byStream[id]
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].Seq < evs[j].Seq })
		if err := s.IngestStream(ctx, id, evs); err != nil {
			return fmt.Errorf("eventstore: reconstruir stream %q do WAL: %w", id, err)
		}
	}
	return nil
}
