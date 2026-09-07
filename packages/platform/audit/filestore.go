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
// CRASH-SAFETY vs DANO (AOS-364): a reabertura distingue dois casos que a heurística antiga
// confundia, e que têm consequências opostas.
//   - CAUDA RASGADA — o ÚLTIMO registo está INCOMPLETO (crash a meio de um write: faltam-lhe
//     bytes, o short read esgota o ficheiro). A cadeia é um prefixo, logo continua VÁLIDA até
//     ao último registo íntegro; o tail parcial é truncado antes de reabrir em append, e
//     Verify(from, Head()) fecha. É recuperação de crash legítima.
//   - DANO — corrupção que deixa registos íntegros DEPOIS do ponto de quebra. A reabertura
//     RECUSA com [DanoInteriorError], SEM tocar no ficheiro — truncar aqui apagaria em silêncio
//     esses registos e reemitiria audit_seq já atribuídos, destruindo a detecção que é a única
//     garantia deste armazém. O que distingue dano de cauda NÃO é a posição do leitor (um
//     comprimento corrompido desalinha-o e falha de forma indistinguível de um crash), mas a
//     RESSINCRONIZAÇÃO: há frames íntegros para lá da quebra? (ver contaOrfaos, portado de
//     AOS-346). Um frame FISICAMENTE COMPLETO com CRC/JSON inválido recusa mesmo sem ressincronizar
//     — um registo persistido corrompido nunca é artefacto de crash. A adulteração de CONTEÚDO com
//     CRC recalculado (framing intacto) continua a ser apanhada mais adiante por
//     verifyReplayedChain (AOS-221); esta verificação cobre o vector COMPLEMENTAR, o do framing físico.

const auditMaxRecordBytes = 64 << 20

var auditCRCTable = crc32.MakeTable(crc32.IEEE)

// FileStore é o [Store] WORM persistente em disco. Guarda os registos selados por
// partição em memória (para Read/Head/At O(1)-amortizado) E persiste cada Append no
// WAL antes de devolver (fsync). Seguro para concorrência.
type FileStore struct {
	mu    sync.RWMutex
	parts map[string][]AuditRecord

	wmu sync.Mutex // serializa os writes ao ficheiro único

	// posse arbitra a EXCLUSIVIDADE de escrita por partição entre PROCESSOS — o que o
	// mutex acima não faz. nil mantém o comportamento anterior. Ver posse.go.
	posse   PosseDeParticao
	recusas recusasDePosse
	f       *os.File
	w       *bufio.Writer
	closed  bool
}

// OpenFileStore cria OU reabre um WORM durável respaldado pelo WAL em path. No
// arranque faz replay do ficheiro (crash-safe), reconstruindo as cadeias por
// partição na ordem de escrita, e reabre o ficheiro em append (truncando um tail
// parcial). Um path inexistente cria um WORM durável novo. Chame Close para fechar.
func OpenFileStore(path string, opts ...FileStoreOption) (*FileStore, error) {
	recs, validEnd, stop, orfaos, err := replayAuditWAL(path)
	if err != nil {
		return nil, fmt.Errorf("audit: replay do WAL %q: %w", path, err)
	}
	// AOS-364 — DISTINGUIR CAUDA RASGADA DE DANO INTERIOR, ANTES de qualquer escrita. A
	// heurística antiga «trunca tudo o que vier depois do último frame íntegro» amputava
	// registos VÁLIDOS quando o dano era interior — e fazia-o de forma disparável tanto pelo CRC
	// como pelo COMPRIMENTO (que desalinha o leitor e parece uma cauda rasgada). A decisão certa
	// não é o tipo de falha na posição do leitor, mas «há frames íntegros DEPOIS da quebra?»:
	//   - walStopComplete (frame fisicamente completo, CRC/JSON inválido) ⇒ RECUSA sempre: um
	//     registo persistido corrompido é bit-rot/adulteração, nunca artefacto de crash.
	//   - walStopIncomplete (short read ou comprimento lixo) ⇒ AMBÍGUO, decide-se por orfaos:
	//       orfaos > 0 (há registos íntegros para lá da quebra) ⇒ RECUSA (dano interior);
	//       orfaos == 0 (nada íntegro a seguir) ⇒ trunca o tail parcial (crash-safety legítima).
	//   - walStopClean ⇒ nada a fazer.
	// A RECUSA corre ANTES de qualquer os.Truncate/OpenFile(O_WRONLY)/fsync — nenhum byte é
	// tocado, nenhum Append reemite audit_seq.
	incompletoEDano := stop.kind == walStopIncomplete && (!stop.conclusive || orfaos > 0)
	switch {
	case stop.kind == walStopComplete || incompletoEDano:
		detail := stop.detail
		switch {
		case stop.kind == walStopIncomplete && !stop.conclusive:
			detail = fmt.Sprintf("%s, ressincronizacao inconclusiva (orcamento esgotado) — recusado por seguranca", detail)
		case stop.kind == walStopIncomplete:
			detail = fmt.Sprintf("%s, com %d registo(s) integro(s) depois da quebra", detail, orfaos)
		}
		return nil, &DanoInteriorError{
			Path:      path,
			Offset:    stop.offset,
			Partition: stop.partition,
			AuditSeq:  stop.auditSeq,
			HasSeq:    stop.hasSeq,
			Detail:    detail,
		}
	case stop.kind == walStopIncomplete: // conclusivo e orfaos == 0 ⇒ cauda rasgada
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
	// Opções aplicadas ANTES do replay: uma porta de posse armada aqui já vale para
	// qualquer escrita que o chamador faça a seguir.
	for _, o := range opts {
		o(s)
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
			// O INVÓLUCRO TAMBÉM TEM DE DIZER A VERDADE. Classificar a causa lá dentro e
			// embrulhá-la em «hash-chain adulterada» não corrigiria a leitura de ninguém:
			// é esta a primeira linha que o operador vê quando o nó se recusa a arrancar.
			if errors.Is(err, ErrChainForked) {
				return nil, fmt.Errorf("audit: hash-chain BIFURCADA no WAL %q (dois escritores na mesma particao, nao adulteracao — ver AOS-284): %w", path, err)
			}
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
func (s *FileStore) Append(ctx context.Context, rec AuditRecord) (AuditRecord, error) {
	// POSSE ANTES DE TUDO (AC1/AC3 do AOS-284). Fora do s.mu de propósito: a porta pode ir
	// à rede, e serializar todas as escritas atrás de uma chamada remota trocaria um
	// defeito de correcção por um de desempenho. A recusa acontece ANTES de haver efeito:
	// nada selado, nada persistido, audit_seq não consumido.
	if err := s.autorizadoAEscrever(ctx, rec.Partition); err != nil {
		return AuditRecord{}, err
	}
	// AOS-311 — CONTEXTO MORTO NÃO SELA. A ordem é posse → ctx → lock → selo → ctx →
	// persist, e a ordem não é acidental: a posse consulta-se PRIMEIRO porque é ela que
	// pode ir à rede e consumir o prazo, e o ctx verifica-se logo a seguir, ainda fora do
	// s.mu, para que um prazo esgotado na porta de posse não entre sequer na secção
	// crítica. Não há corrida com autorizadoAEscrever: os dois passos são sequenciais na
	// mesma goroutine e nenhum deles muta estado do store — uma posse afirmativa seguida
	// de um ctx morto resolve em recusa sem efeito, tal como uma posse negada. O erro
	// devolvido é o próprio ctx.Err() (não ErrParticaoAlheia), para o chamador distinguir
	// «prazo esgotado» de «partição alheia».
	if err := ctx.Err(); err != nil {
		return AuditRecord{}, err
	}
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
	stampSchema(&rec)
	rec.EntryHash = ComputeEntryHash(prev, rec)
	sealed := cloneRecord(rec)

	// AOS-311 — segunda verificação, imediatamente antes do efeito durável. Entre a
	// primeira e esta só houve hashing em memória, mas é ESTE o último ponto sem efeito:
	// um prazo que morra aqui não escreve um byte, não publica na cadeia in-memory e não
	// consome o audit_seq — a próxima tentativa reusa a mesma posição.
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return AuditRecord{}, err
	}

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
// walStopKind classifica PORQUE o replay parou. A distinção que AOS-364 exige — cauda rasgada
// (truncável) vs dano (recusa) — NÃO se decide pelo tipo de falha na posição corrente do leitor:
// um comprimento corrompido leva o leitor a consumir o número errado de bytes e a falhar de forma
// indistinguível de um crash. É por isso que a decisão final consulta a RESSINCRONIZAÇÃO
// (contaOrfaos): «há frames íntegros DEPOIS da quebra?». Esta classificação só distingue o caso
// que NÃO precisa de ressincronizar — um frame FISICAMENTE COMPLETO cujo CRC/JSON falha — dos
// que precisam. É o mesmo discriminador que o Event Store irmão adoptou em AOS-346.
type walStopKind int

const (
	// walStopClean — EOF numa fronteira de frame: o WAL acaba num registo íntegro (fim normal).
	walStopClean walStopKind = iota
	// walStopComplete — frame FISICAMENTE COMPLETO (header+payload+trailer todos lidos) cujo CRC
	// ou JSON não valida. NUNCA é artefacto de crash: `persist` escreve os três num só Flush, logo
	// um crash deixa um PREFIXO (short read), nunca um trailer completo errado. É bit-rot ou
	// adulteração de um registo já persistido ⇒ RECUSA sempre, sem consultar a ressincronização.
	// (Divergência DELIBERADA do Event Store irmão, que trunca este caso quando é o último registo:
	// a CA1 de AOS-364 só permite truncar quando o dano «esgota os bytes restantes» — um frame
	// completo não os esgota —, e um registo de auditoria persistido não se apaga em silêncio.)
	walStopComplete
	// walStopIncomplete — o frame na quebra está FISICAMENTE INCOMPLETO (short read) OU o seu
	// comprimento é lixo (n==0 ou n>max, extensão desconhecida). Ambíguo entre cauda rasgada e dano
	// interior; a decisão consulta a ressincronização (orfaos>0 ⇒ dano, ==0 ⇒ cauda).
	walStopIncomplete
)

// walStop reporta o motivo e a localização da paragem do replay. offset é o início do frame
// problemático (== validEnd). partition/auditSeq são best-effort: preenchem-se quando o payload
// ainda desserializa (o caso em que só o trailer de CRC de 4 bytes foi corrompido, medido em O-11).
type walStop struct {
	kind      walStopKind
	offset    int64
	partition string
	auditSeq  uint64
	hasSeq    bool
	detail    string
	// conclusive é false quando a ressincronização esgotou o orçamento sem decidir se há registos
	// íntegros a seguir. O chamador trata inconclusivo como DANO (fail-closed) — nunca como cauda.
	conclusive bool
}

// replayAuditWAL lê o WAL frame a frame, devolve os registos íntegros lidos, o offset até onde a
// cadeia é válida (validEnd), a classificação da paragem, e — quando a paragem é ambígua
// (walStopIncomplete) — o número de registos ÍNTEGROS que existem DEPOIS da quebra (orfaos), obtido
// por RESSINCRONIZAÇÃO do enquadramento. É orfaos, e não a posição do leitor, que distingue uma
// cauda rasgada (orfaos==0) de dano interior (orfaos>0). Ver AOS-364 / AOS-346.
func replayAuditWAL(path string) (_ []AuditRecord, validEnd int64, _ walStop, orfaos int, _ error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, walStop{kind: walStopClean}, 0, nil
		}
		return nil, 0, walStop{}, 0, err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	var out []AuditRecord
	var stop walStop
	for {
		var hdr [4]byte
		if _, rerr := io.ReadFull(r, hdr[:]); rerr != nil {
			if errors.Is(rerr, io.EOF) {
				stop = walStop{kind: walStopClean, offset: validEnd} // fim limpo numa fronteira
			} else {
				stop = walStop{kind: walStopIncomplete, offset: validEnd, detail: "header parcial"}
			}
			break
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 || n > auditMaxRecordBytes {
			// Comprimento lixo: a extensão do frame é desconhecida ⇒ ambíguo, decide-se por orfaos.
			stop = walStop{kind: walStopIncomplete, offset: validEnd, detail: "comprimento malformado"}
			break
		}
		payload := make([]byte, n)
		if _, rerr := io.ReadFull(r, payload); rerr != nil {
			stop = walStop{kind: walStopIncomplete, offset: validEnd, detail: "payload incompleto"}
			break
		}
		var tr [4]byte
		if _, rerr := io.ReadFull(r, tr[:]); rerr != nil {
			stop = walStop{kind: walStopIncomplete, offset: validEnd, detail: "trailer incompleto"}
			break
		}
		if binary.BigEndian.Uint32(tr[:]) != crc32.Checksum(payload, auditCRCTable) {
			// Frame COMPLETO com CRC errado ⇒ dano, sem consultar orfaos. Best-effort: quando só o
			// trailer foi corrompido, o payload ainda desserializa e dá partição/audit_seq (O-11).
			s := walStop{kind: walStopComplete, offset: validEnd, detail: "CRC nao fecha"}
			var rec AuditRecord
			if json.Unmarshal(payload, &rec) == nil {
				s.partition, s.auditSeq, s.hasSeq = rec.Partition, rec.AuditSeq, true
			}
			stop = s
			break
		}
		var rec AuditRecord
		if jerr := json.Unmarshal(payload, &rec); jerr != nil {
			stop = walStop{kind: walStopComplete, offset: validEnd, detail: "JSON invalido apesar de CRC valido"}
			break
		}
		out = append(out, rec)
		validEnd += int64(4 + int(n) + 4)
	}
	// Só a paragem ambígua precisa de saber o que há para lá da quebra.
	if stop.kind == walStopIncomplete {
		fim := fileSizeOrZero(f)
		orfaos, stop.conclusive = contaOrfaos(f, validEnd, fim)
	} else {
		stop.conclusive = true
	}
	return out, validEnd, stop, orfaos, nil
}

func fileSizeOrZero(f *os.File) int64 {
	if fi, err := f.Stat(); err == nil {
		return fi.Size()
	}
	return 0
}

// janelaDeRessincronizacao é o buffer de varrimento byte-a-byte da ressincronização.
const janelaDeRessincronizacao = 64 << 10

// ressincOrcamentoBytes limita o trabalho (bytes lidos+CRC) que a ressincronização pode gastar a
// procurar a fronteira do registo seguinte. Sem este tecto o varrimento byte-a-byte é O(n²) sobre
// uma janela de 64MB, e um adversário com escrita no WAL (o modelo de ameaça deste armazém)
// fabrica padding após uma quebra de comprimento para prender o arranque minutos-a-horas em vez de
// o fazer recusar depressa (achado F1 da revisão adversarial v2, medido: 1MB→10s, 2MB→>300s). O
// tecto é FAIL-CLOSED: esgotá-lo sem concluir devolve «inconclusivo», que o [OpenFileStore] trata
// como DANO (recusa) — nunca como cauda (truncar). É seguro porque uma cauda rasgada legítima tem
// janela pequena (a quebra está no fim) e nunca o atinge; só o dano interior real ou o abuso o
// atingem, e aí recusar é o remédio correcto. 256MB ⇒ arranque limitado a fracção de segundo.
const ressincOrcamentoBytes = 256 << 20

// contaOrfaos conta os registos ÍNTEGROS que existem DEPOIS do ponto de quebra (AOS-364, portado
// de AOS-346). É o que distingue «cauda rasgada» (zero) de «dano interior» (>0) — a única pergunta
// cuja resposta muda o remédio. Não se pode continuar da posição do leitor: um comprimento
// corrompido desalinha tudo o que venha a seguir, pelo que se RESSINCRONIZA o enquadramento
// varrendo o ficheiro à procura da próxima fronteira de registo válida.
//
// Devolve (contagem, conclusivo). conclusivo==false ⇒ o orçamento de ressincronização esgotou-se
// antes de decidir; o chamador trata isso como DANO (fail-closed), não como cauda rasgada.
func contaOrfaos(f *os.File, quebra, fim int64) (int, bool) {
	inicio, ok, esgotou := ressincroniza(f, quebra+1, fim)
	if esgotou {
		return 0, false // inconclusivo ⇒ o chamador recusa (fail-closed)
	}
	if !ok {
		return 0, true // varreu toda a janela sem achar fronteira ⇒ nada íntegro a seguir (cauda)
	}
	r := bufio.NewReader(io.NewSectionReader(f, inicio, fim-inicio))
	n := 0
	for {
		payload, ok := leRegistoValido(r)
		if !ok {
			return n, true
		}
		var rec AuditRecord
		if json.Unmarshal(payload, &rec) != nil {
			return n, true
		}
		n++
	}
}

// leRegistoValido lê UM frame bem-formado (comprimento válido, payload completo, CRC a fechar) e
// devolve o payload. Não desserializa — isso é do chamador.
func leRegistoValido(r *bufio.Reader) (payload []byte, ok bool) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, false
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > auditMaxRecordBytes {
		return nil, false
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, false
	}
	var tr [4]byte
	if _, err := io.ReadFull(r, tr[:]); err != nil {
		return nil, false
	}
	if binary.BigEndian.Uint32(tr[:]) != crc32.Checksum(payload, auditCRCTable) {
		return nil, false
	}
	return payload, true
}

// ressincroniza procura o primeiro offset em [depois, fim) onde começa um registo COMPLETO, com
// CRC válido e payload que desserializa num AuditRecord. É o varrimento que repõe a fronteira
// quando o enquadramento se perdeu. O limite do varrimento é auditMaxRecordBytes+8 a partir de
// `depois` — DEMONSTRADO, não arbitrado: o registo corrompido não pode ocupar legitimamente mais do
// que isso, logo o registo íntegro seguinte, se existir, começa dentro dessa janela. Um falso
// positivo exigiria quatro bytes arbitrários a formar um comprimento plausível, os bytes seguintes
// a ter um crc32 que bate, e o resultado a desserializar num AuditRecord — desprezável, e erra para
// o lado fail-closed (recusar).
// ressincroniza devolve (offset, encontrado, esgotou). encontrado==true ⇒ há uma fronteira válida
// em offset. encontrado==false && esgotou==false ⇒ varreu toda a janela e não há fronteira (nada
// íntegro a seguir). esgotou==true ⇒ o orçamento [ressincOrcamentoBytes] acabou antes de decidir ⇒
// o chamador trata como DANO (fail-closed), nunca como cauda.
func ressincroniza(f *os.File, depois, fim int64) (offset int64, encontrado bool, esgotou bool) {
	if depois < 0 {
		depois = 0
	}
	limite := depois + int64(auditMaxRecordBytes) + 8
	if limite > fim {
		limite = fim
	}
	buf := make([]byte, janelaDeRessincronizacao)
	var rec []byte
	var gasto int64 // bytes lidos+CRC gastos a testar candidatos; tecto FAIL-CLOSED (F1)
	for base := depois; base < limite; {
		nlido, err := f.ReadAt(buf, base)
		if nlido < 4 {
			return 0, false, false
		}
		for i := 0; i+4 <= nlido; i++ {
			off := base + int64(i)
			if off >= limite {
				return 0, false, false
			}
			tam := int64(binary.BigEndian.Uint32(buf[i : i+4]))
			if tam == 0 || tam > auditMaxRecordBytes || off+4+tam+4 > fim {
				continue
			}
			// Cada candidato plausível custa uma leitura + um CRC de `tam` bytes. Sobre lixo há
			// muitos candidatos com `tam` grande; o orçamento impede o O(n²) de prender o arranque.
			gasto += tam
			if gasto > ressincOrcamentoBytes {
				return 0, false, true // esgotado ⇒ inconclusivo ⇒ fail-closed
			}
			if int64(cap(rec)) < tam+4 {
				rec = make([]byte, tam+4)
			}
			cand := rec[:tam+4]
			if _, rerr := f.ReadAt(cand, off+4); rerr != nil {
				continue
			}
			if binary.BigEndian.Uint32(cand[tam:]) != crc32.Checksum(cand[:tam], auditCRCTable) {
				continue
			}
			var rc AuditRecord
			if json.Unmarshal(cand[:tam], &rc) != nil {
				continue
			}
			return off, true, false
		}
		if err != nil {
			return 0, false, false // fim do ficheiro sem encontrar fronteira
		}
		base += int64(nlido) - 3 // sobreposição de 3: um header a cavalo da janela é visto na seguinte
	}
	return 0, false, false
}
