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
//   - CORRUPÇÃO A MEIO ≠ CAUDA RASGADA: se houver registos ÍNTEGROS DEPOIS do ponto de
//     quebra, não é a cauda de um crash e truncar apagaria dados bons. [Open] RECUSA
//     ([ErrWALCorruptedMidLog]) em vez de truncar, e não toca no ficheiro — a cópia de
//     segurança continua a ter contra o que reconciliar. Ver [WithWALTruncateOnCorruption]
//     para a recuperação deliberada.
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

// ficheiroWAL é a fatia de *os.File de que o [wal] precisa. Existe por uma razão de
// TESTABILIDADE, e a razão é um defeito real: o ramo em que o `Flush` passa e o `Sync`
// falha não era exercitável com um *os.File, e por isso o teste de regressão que devia
// cobri-lo (TestDurable_PersistErrorNoPhantom) fecha o descritor — o que faz falhar o
// `Write`, NÃO o `Sync`. O ramo perigoso ficou por testar precisamente por não haver
// costura. Ver [wal.desfazer].
type ficheiroWAL interface {
	io.Writer
	Sync() error
	Close() error
}

// wal é o escritor append-only do log em disco. Serializa os writes com o seu
// próprio mutex (appends de streams distintos correm concorrentes no Store, mas o
// ficheiro é único) e faz fsync após cada registo para durabilidade real.
type wal struct {
	mu sync.Mutex
	f  ficheiroWAL
	w  *bufio.Writer
	// tamanho são os bytes CONFIRMADOS no ficheiro (só sobe depois de um registo
	// ficar durável). É o ponto de reposição de [wal.desfazer]. Mantido em memória em
	// vez de um Stat por append: o append é caminho quente e o valor é exacto — só
	// esta goroutine escreve, sob w.mu.
	tamanho int64
	// truncar repõe o ficheiro a um tamanho dado. NÃO é um método do descritor de
	// escrita DE PROPÓSITO: o WAL é aberto com O_APPEND, e em Windows esse modo concede
	// FILE_APPEND_DATA, que NÃO autoriza truncar — medido, `Acesso negado`. A truncatura
	// vai por caminho ([os.Truncate]), que abre o seu próprio descritor. Manter o
	// O_APPEND é o que garante que a escrita seguinte aterra no novo fim do ficheiro.
	truncar func(int64) error
	// envenenado != nil ⇒ o WAL ficou num estado que não se conseguiu repor e NÃO pode
	// aceitar mais escritas. Ver [wal.desfazer].
	envenenado error
	closed     bool
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
	// O tamanho de partida é o ponto de reposição do PRIMEIRO append ([wal.desfazer]).
	// O Open já truncou qualquer cauda parcial antes de chegar aqui, pelo que este valor
	// é o fim do último registo íntegro.
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &wal{
		f:       f,
		w:       bufio.NewWriter(f),
		tamanho: fi.Size(),
		truncar: func(n int64) error { return os.Truncate(path, n) },
	}, nil
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
	if w.envenenado != nil {
		return w.envenenado
	}
	antes := w.tamanho
	if _, err := w.w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := w.w.Write(payload); err != nil {
		return err
	}
	if _, err := w.w.Write(tr[:]); err != nil {
		return err
	}
	// A PARTIR DAQUI OS BYTES PODEM JÁ ESTAR NO FICHEIRO, e é isso que o revert do
	// chamador assume que não acontece. Qualquer falha destas duas chamadas passa por
	// [wal.desfazer], que repõe a invariante «erro devolvido ⇒ nada ficou durável».
	//
	// O `Flush` conta tanto como o `Sync`: o `bufio.Writer` propaga o erro do Write
	// subjacente mesmo quando ESCREVEU TUDO (só acrescenta io.ErrShortWrite quando
	// n < len), pelo que um Flush falhado também pode deixar o registo inteiro em disco.
	if err := w.w.Flush(); err != nil {
		return w.desfazer(antes, err)
	}
	// fsync: durabilidade real — o evento committed sobrevive a um crash do processo/SO.
	if err := w.f.Sync(); err != nil {
		return w.desfazer(antes, err)
	}
	w.tamanho = antes + int64(len(hdr)+len(payload)+len(tr))
	return nil
}

// desfazer repõe a invariante «Append devolveu erro ⇒ nada ficou durável» truncando o
// ficheiro ao tamanho que tinha antes deste registo.
//
// # O DEFEITO QUE FECHA, medido
//
// `wal.append` escrevia, fazia `Flush` e só depois `Sync`. Um `Flush` bem-sucedido
// significa que os bytes JÁ passaram por write(2); se o `fsync` seguinte falhar com o
// processo vivo — EIO, ENOSPC sob delayed allocation, EDQUOT, volume thin-provisioned
// ou em rede — o registo fica COMPLETO e com CRC VÁLIDO no ficheiro e o `Append`
// devolve erro. Medido a 2026-08-30: `head` vivo=1, WAL em disco=2 registos; e como os
// bytes estão na page cache independentemente do fsync, bastava um REINÍCIO DE PROCESSO
// para o evento dado como falhado ressuscitar.
//
// A consequência aguda não era sequer essa. O índice de deduplicação do Store só é
// povoado DEPOIS da escrita no WAL, pelo que um retry não recebia `StatusDuplicate` —
// recebia o MESMO seq do órfão. O WAL ficava com seqs [1 2 2] e o `Open` seguinte
// recusava com `E_RESTORE_ORDER: lote de restauro nao e gapless`: o nó deixava de
// arrancar. O comentário de `restoreInto` antecipa esse caso como «que um WAL
// bem-formado nunca produz» — e este era o caminho que o produzia.
//
// # PORQUE TRUNCAR, E O QUE FAZER QUANDO NEM ISSO SE CONSEGUE
//
// O registo NUNCA foi confirmado ao chamador, logo removê-lo não perde dado de ninguém:
// repõe exactamente a invariante que o `Store.Append` e o revert do `GraphBuilder`
// assumem. Se o fsync falhou porque o kernel já largou as páginas sujas, tanto melhor —
// aqui o objectivo é justamente que os bytes NÃO fiquem.
//
// Se a truncatura (ou o fsync dela) também falhar, a invariante não é reponível e o WAL
// fica ENVENENADO: qualquer append seguinte é recusado. É deliberado e é o mal menor —
// um Store que recusa escritas em voz alta é um problema visível; um que continua a
// aceitar acaba com um seq duplicado no ficheiro e um nó que não volta a arrancar.
func (w *wal) desfazer(antes int64, causa error) error {
	if err := w.truncar(antes); err != nil {
		w.envenenado = fmt.Errorf("eventstore/wal: %w; a truncatura de reposicao para %d bytes FALHOU (%v) — "+
			"o WAL pode conter um registo NAO confirmado e nao aceita mais escritas", causa, antes, err)
		return w.envenenado
	}
	// O fsync da truncatura é BEST-EFFORT, e a assimetria face ao caso de cima é
	// deliberada. Se a truncatura FALHOU, o registo órfão está lá e a invariante está
	// quebrada agora — envenenar é a resposta. Se a truncatura PASSOU e só o seu fsync
	// falhou, o ficheiro já está no tamanho certo para este processo: a invariante
	// vale, e o que fica incerto é apenas a durabilidade da mudança de tamanho num
	// crash subsequente. Parar de aceitar escritas por causa disso seria trocar um
	// risco condicional por uma indisponibilidade certa — e as escritas seguintes
	// reescrevem essa região de qualquer forma.
	_ = w.f.Sync()
	// Reposto: o ficheiro voltou ao estado anterior a este registo.
	return causa
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
// orfaos é o nº de registos ÍNTEGROS encontrados DEPOIS do ponto onde o replay parou.
// Zero ⇒ cauda rasgada (o caso do crash), e truncar é a recuperação correcta. Maior
// que zero ⇒ corrupção a MEIO do log, e truncar apagaria dados bons — ver [Open].
func replayWAL(path string) (_ []Event, validEnd int64, orfaos int, _ error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, 0, nil
		}
		return nil, 0, 0, err
	}
	defer f.Close()

	r := bufio.NewReader(f)
	var out []Event
	// enquadramentoIntacto: o registo que fez parar foi lido POR INTEIRO (len+payload+crc),
	// pelo que o leitor ficou posicionado na fronteira do registo SEGUINTE e dá para
	// procurar o que vem depois. Quando o próprio enquadramento se perdeu (header/payload/
	// checksum truncados, comprimento absurdo), não há fronteira seguinte que se possa
	// achar — e, por construção, estamos no fim do que é interpretável.
	enquadramentoIntacto := false
	for {
		payload, ok, enquadrado := leRegisto(r)
		if !ok {
			enquadramentoIntacto = enquadrado
			break
		}
		var ev Event
		if err := json.Unmarshal(payload, &ev); err != nil {
			// Payload não desserializa: os bytes são os que foram escritos (o crc bate),
			// logo o enquadramento está bom e o registo seguinte é alcançável.
			enquadramentoIntacto = true
			break
		}
		out = append(out, ev)
		validEnd += int64(4 + len(payload) + 4)
	}
	if enquadramentoIntacto {
		orfaos = contaRegistosIntegros(r)
	}
	return out, validEnd, orfaos, nil
}

// leRegisto lê UM registo framed do WAL.
//
//	ok=true                      — registo íntegro; payload devolvido.
//	ok=false, enquadrado=false   — fim-de-log limpo, ou enquadramento perdido (write
//	                               rasgado): não há fronteira seguinte a partir daqui.
//	ok=false, enquadrado=true    — checksum inválido, mas o frame foi consumido por
//	                               inteiro: o leitor está na fronteira do registo seguinte.
func leRegisto(r *bufio.Reader) (payload []byte, ok bool, enquadrado bool) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		// EOF limpo (fim do log) OU header truncado (crash a meio).
		return nil, false, false
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > maxRecordBytes {
		return nil, false, false // comprimento absurdo ⇒ header corrompido/lixo.
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, false, false // payload truncado ⇒ registo incompleto.
	}
	var tr [4]byte
	if _, err := io.ReadFull(r, tr[:]); err != nil {
		return nil, false, false // checksum truncado ⇒ registo incompleto.
	}
	if binary.BigEndian.Uint32(tr[:]) != crc32.Checksum(payload, crcTable) {
		return nil, false, true // corrupção DENTRO de um frame bem-formado.
	}
	return payload, true, true
}

// contaRegistosIntegros conta os registos que ainda desserializam a partir da posição
// corrente do leitor. É o que distingue «cauda rasgada» (zero) de «corrupção a meio»
// (mais que zero) — a única pergunta cuja resposta muda o remédio.
func contaRegistosIntegros(r *bufio.Reader) int {
	n := 0
	for {
		payload, ok, _ := leRegisto(r)
		if !ok {
			return n
		}
		var ev Event
		if err := json.Unmarshal(payload, &ev); err != nil {
			return n
		}
		n++
	}
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
	events, validEnd, orfaos, err := replayWAL(path)
	if err != nil {
		return nil, fmt.Errorf("eventstore: replay do WAL %q: %w", path, err)
	}
	// CORRUPÇÃO A MEIO ≠ CAUDA RASGADA, e a diferença decide se se apaga ou se se pára.
	//
	// Truncar a validEnd é a recuperação CORRECTA para um write interrompido no fim do
	// ficheiro: os bytes parciais não são dados de ninguém. Aplicado a uma corrupção no
	// MEIO, o mesmo gesto apaga do disco todos os registos que vinham a seguir — que
	// podem estar byte-a-byte íntegros. Medido antes desta guarda: 5 eventos, um byte
	// corrompido no 2º, e o ficheiro passou de 1765 para 353 bytes ao reabrir; os
	// eventos 3, 4 e 5 estavam intactos e desapareceram, com Read a devolver err=nil.
	//
	// Fail-closed: com registos íntegros a seguir ao ponto de quebra, o Open RECUSA.
	// Um nó que não arranca é um problema visível; um log que encolheu em silêncio não é.
	if orfaos > 0 && !walTruncaCorrompido(opts) {
		return nil, fmt.Errorf(
			"eventstore: WAL %q: %w — quebra ao byte %d, com %d registo(s) integro(s) depois dela; "+
				"truncar aqui apagaria esses registos. Restaure de uma copia de seguranca, ou "+
				"— se decidir DELIBERADAMENTE perder o troco — reabra com WithWALTruncateOnCorruption()",
			path, ErrWALCorruptedMidLog, validEnd, orfaos)
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

// walTruncaCorrompido lê APENAS o interruptor de [WithWALTruncateOnCorruption] das
// Option, sem construir um Store: a decisão de truncar tem de ser tomada ANTES de
// mexer no ficheiro, e o [New] só corre depois. Não replica defaults com significado
// — o único campo lido tem zero-value fail-closed (não truncar).
func walTruncaCorrompido(opts []Option) bool {
	c := &config{}
	for _, o := range opts {
		o(c)
	}
	return c.walTruncaCorr
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
