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
//
// O CABEÇALHO DE COMPRIMENTO NÃO É COBERTO PELO CHECKSUM, e isso é deliberado depois de
// AOS-346: cobri-lo não fecharia nada — para verificar o CRC é preciso localizar o
// trailer, e localizá-lo exige confiar no `len` que se quereria verificar. Quem torna um
// `len` corrompido detectável é [contaOrfaos], que RESSINCRONIZA o enquadramento em vez
// de confiar na posição do leitor. O formato mantém-se, e um WAL escrito por qualquer
// versão anterior continua legível sem migração.

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
	// tamanhoReal lê o tamanho ACTUAL do ficheiro em disco. Vive ao lado de `truncar` e
	// pela mesma razão — vai por caminho, não pelo descritor O_APPEND — e existe porque
	// [wal.desfazer] tem de saber se o ficheiro ainda tem o tamanho que julga (AOS-349).
	tamanhoReal func() (int64, error)
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
		tamanhoReal: func() (int64, error) {
			fi, err := os.Stat(path)
			if err != nil {
				return 0, err
			}
			return fi.Size(), nil
		},
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
	// AOS-349 — A DESSINCRONIZAÇÃO VERIFICA-SE ANTES DE ESCREVER, NÃO DEPOIS.
	//
	// `w.tamanho` é o tamanho que ESTA goroutine julga que o ficheiro tem. O comentário do
	// campo justificava não fazer `Stat` por append com «o valor é exacto — só esta
	// goroutine escreve». Deixou de ser verdade: o ficheiro pode encolher POR BAIXO (outro
	// processo a truncar; AOS-346 composto com AOS-347 levou um WAL de 1480 para 592 bytes
	// por um comando de LEITURA), e a partir daí `w.tamanho` mente.
	//
	// Verificar só em [wal.desfazer] não chega, e a diferença foi medida ao escrever este
	// teste: o WAL é aberto em O_APPEND, logo o registo aterra no FIM REAL do ficheiro
	// ANTES de o `desfazer` sequer correr. Recusar nessa altura impede a extensão com
	// zeros mas já não desescreve o registo — que, por aterrar exactamente na fronteira
	// onde um registo antigo começava, fica bem-formado e RESSUSCITA no replay seguinte.
	// A única barreira que impede um append falhado de ficar durável é a que corre ANTES
	// da primeira escrita.
	//
	// O custo é um `Stat` por append, contra um `fsync` no mesmo caminho: ruído.
	//
	// SÓ O ENCOLHIMENTO, e a restrição é deliberada. Um ficheiro MAIOR do que a memória é o
	// outro escritor a acrescentar — DEF-282, que este epic não reabre. Recusá-lo aqui
	// faria o substrato PARECER que arbitra entre processos sem lhe dar a garantia que
	// falta (o `expected_seq` atómico), que é exactamente a armadilha que o pacote
	// `conformance` existe para desarmar; e avermelharia
	// TestDefeito_DoisEscritoresTornamOWALInabrivel, o sensor que mede essa ausência.
	// Medido ao escrever isto: com `real != antes` o sensor ficou vermelho.
	if real, err := w.tamanhoReal(); err != nil {
		w.envenenado = fmt.Errorf("eventstore/wal: nao foi possivel medir o ficheiro antes do append (%w) — "+
			"sem essa medida nao se pode garantir que um erro nao deixa nada duravel; o WAL nao aceita mais escritas", err)
		return w.envenenado
	} else if real < antes {
		w.envenenado = fmt.Errorf("eventstore/wal: %w: memoria=%d bytes, ficheiro=%d bytes — "+
			"o WAL nao aceita mais escritas. Pare o no, reconcilie com a copia de seguranca e verifique "+
			"quem mais escreve neste ficheiro", ErrWALDesincronizado, antes, real)
		return w.envenenado
	}
	// AOS-348: os Write também vão por [wal.desfazer]. Não é simetria decorativa. O
	// `bufio.Writer` descarrega sozinho quando o buffer enche, pelo que um `Write` pode
	// já ter posto bytes no ficheiro; e um `Write` que falhe deixa o writer com um erro
	// PEGAJOSO que envenena todos os seguintes. Sair por `return err` — como se fazia —
	// não repunha o ficheiro NEM o writer, e o append seguinte morria aqui mesmo, sem
	// passar por lado nenhum que soubesse dizer que aquilo já não era transitório.
	if _, err := w.w.Write(hdr[:]); err != nil {
		return w.desfazer(antes, err)
	}
	if _, err := w.w.Write(payload); err != nil {
		return w.desfazer(antes, err)
	}
	if _, err := w.w.Write(tr[:]); err != nil {
		return w.desfazer(antes, err)
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
	// AOS-349 — NÃO SE REPÕE UM FICHEIRO ESTENDENDO-O.
	//
	// [os.Truncate] para um tamanho MAIOR do que o ficheiro não trunca: ESTENDE, com
	// zeros. Se o WAL tiver encolhido por baixo — o que AOS-346 composto com AOS-347
	// tornava alcançável, um comando de INSPECÇÃO a truncar o log de 1480 para 592 bytes
	// —, `w.tamanho` fica à frente do ficheiro real e a «reposição» faz o contrário do que
	// promete. Executado:
	//
	//	ficheiro real=592 ; A.wal.tamanho (em memória)=1480 ; DESSINCRONIZADO
	//	append com fsync falhado -> desfazer chama os.Truncate(path, 1480) sobre 888 bytes
	//	tamanho após desfazer = 1480 (o ficheiro CRESCEU)
	//	bytes nulos no ficheiro = 606 de 1480
	//	replay final: seq=1, seq=2, seq=6      (3, 4 e 5 desaparecidos)
	//	**o append FALHADO (s9) ficou DURÁVEL**
	//
	// A invariante central — «erro devolvido ⇒ nada ficou durável» — era violada PELO
	// PRÓPRIO CÓDIGO QUE EXISTE PARA A REPOR.
	//
	// Um ficheiro mais curto do que a memória não é reponível por este mecanismo, e
	// fingir que é seria pior do que parar: é condição TERMINAL, com sentinela próprio
	// ([ErrWALDesincronizado]) para que o operador saiba que o problema não é o disco
	// cheio — é que alguém mexeu no ficheiro por baixo do nó.
	if real, err := w.tamanhoReal(); err != nil {
		w.envenenado = fmt.Errorf("eventstore/wal: %w; nao foi possivel medir o ficheiro para a reposicao (%v) — "+
			"o WAL pode conter um registo NAO confirmado e nao aceita mais escritas", causa, err)
		return w.envenenado
	} else if real < antes {
		w.envenenado = fmt.Errorf("eventstore/wal: %w; %w: memoria=%d bytes, ficheiro=%d bytes — "+
			"truncar para %d ESTENDERIA o ficheiro com zeros e tornaria DURAVEL um append que falhou; "+
			"o WAL nao aceita mais escritas. Pare o no, reconcilie com a copia de seguranca e verifique "+
			"quem mais escreve neste ficheiro",
			causa, ErrWALDesincronizado, antes, real, antes)
		return w.envenenado
	}
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
	// AOS-348 — REPOR TAMBÉM O WRITER, não só o ficheiro.
	//
	// O `bufio.Writer` guarda o primeiro erro que encontra e devolve-o em TODAS as
	// operações seguintes, para sempre. Sem este `Reset`, um `Flush` falhado matava o WAL
	// de forma definitiva enquanto o anunciava como transitório: o append seguinte morria
	// já no `w.w.Write`, com a mensagem do erro ORIGINAL, sem passar por aqui e sem marcar
	// `envenenado`. O `Store.Append` embrulhava-o em «persistir evento committed» e o
	// chamador lia «ENOSPC», concluía «disco cheio, volto a tentar» — e nunca mais escrevia
	// nada. Medido lado a lado com o caminho gémeo:
	//
	//	[Flush]  2.º append falha = "persistir evento committed: sonda: write falhou (ENOSPC)"
	//	         RETRY com a falha REMOVIDA = mesma mensagem ENOSPC   ****  MORREU
	//	[fsync]  falha = "sonda: fsync falhou (EIO)"
	//	         RETRY = <nil>                                        ****  RECUPERA
	//
	// Mesma falha transitória, mesmo retry, desfechos opostos. O `Reset` fecha a
	// assimetria: descarta o que ficou no buffer (o registo que não foi confirmado a
	// ninguém — é isso mesmo que se quer deitar fora), limpa o erro pegajoso e volta a
	// apontar ao ficheiro, que o `truncar` acima acabou de repor. Se a avaria for
	// persistente, o append seguinte falha OUTRA VEZ e volta a passar por aqui — que é o
	// comportamento honesto; o que não pode é morrer calado a fingir-se retentável.
	w.w.Reset(w.f)
	// Reposto: o ficheiro e o writer voltaram ao estado anterior a este registo.
	return causa
}

// trocarFicheiro substitui o descritor do WAL e RECONSTRÓI o `bufio.Writer` sobre ele.
//
// # AOS-348 — PORQUE A COSTURA TINHA DE CRESCER
//
// [ficheiroWAL] existe para tornar exercitável o ramo «Flush passa, Sync falha». Mas o
// `bufio.Writer` é construído em [openWALAppend] sobre o `*os.File` ORIGINAL, pelo que
// trocar só `s.wal.f` — como os testes faziam — intercepta `Sync` e `Close` e NÃO o
// caminho de ESCRITA. Medido: `writes=0`. O `Write` da sonda era código morto, e foi por
// isso que o defeito do erro pegajoso do `Flush` viveu tanto tempo sem ser visto.
//
// Trocar as duas coisas à mão, no teste, é o que um teste já fazia — e é precisamente o
// tipo de detalhe que o teste seguinte esquece. Aqui a troca é atómica e é uma só chamada.
//
// Só faz sentido com o buffer VAZIO (a seguir a um append bem-sucedido, que fez Flush):
// substituir o writer descarta o que lá estivesse. Uso restrito a testes deste pacote.
func (w *wal) trocarFicheiro(f ficheiroWAL) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.f = f
	w.w = bufio.NewWriter(f)
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
// AOS-346: a contagem é feita por RESSINCRONIZAÇÃO do enquadramento, e é feita SEMPRE —
// já não depende de o leitor ter ficado numa fronteira, que era a suposição que um `len`
// corrompido invalidava sem que nada o denunciasse.
func replayWAL(path string) (_ []Event, validEnd int64, orfaos int, _ error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, 0, nil
		}
		return nil, 0, 0, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, 0, 0, err
	}
	fim := fi.Size()

	r := bufio.NewReader(f)
	var out []Event
	for {
		payload, ok := leRegisto(r)
		if !ok {
			break
		}
		var ev Event
		if err := json.Unmarshal(payload, &ev); err != nil {
			break
		}
		out = append(out, ev)
		validEnd += int64(4 + len(payload) + 4)
	}
	// validEnd é agora o offset onde COMEÇA o registo que fez parar o replay. Contar o
	// que existe depois dele é a única pergunta cuja resposta muda o remédio, e é feita
	// por RESSINCRONIZAÇÃO — não pela posição em que o leitor ficou. Ver [contaOrfaos].
	orfaos = contaOrfaos(f, validEnd, fim)
	return out, validEnd, orfaos, nil
}

// leRegisto lê UM registo framed do WAL a partir da posição corrente do leitor.
//
//	ok=true    — registo íntegro; payload devolvido.
//	ok=false   — fim-de-log limpo, registo incompleto (write rasgado), comprimento
//	             absurdo, ou checksum inválido.
//
// AOS-346: já NÃO distingue «enquadramento intacto» de «enquadramento perdido». Essa
// distinção era usada para decidir se valia a pena procurar registos a seguir, e era
// enganadora: um `len` corrompido consome um número errado de bytes SEM que o leitor o
// saiba, pelo que «o frame foi consumido por inteiro» não implica «o leitor está numa
// fronteira». Quem procura o que vem depois é [contaOrfaos], por ressincronização, e
// procura sempre.
func leRegisto(r *bufio.Reader) (payload []byte, ok bool) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		// EOF limpo (fim do log) OU header truncado (crash a meio).
		return nil, false
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > maxRecordBytes {
		return nil, false // comprimento absurdo ⇒ header corrompido/lixo.
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, false // payload truncado ⇒ registo incompleto.
	}
	var tr [4]byte
	if _, err := io.ReadFull(r, tr[:]); err != nil {
		return nil, false // checksum truncado ⇒ registo incompleto.
	}
	if binary.BigEndian.Uint32(tr[:]) != crc32.Checksum(payload, crcTable) {
		return nil, false // corrupção DENTRO de um frame bem-formado.
	}
	return payload, true
}

// janelaDeRessincronizacao é o buffer de varrimento byte-a-byte da ressincronização.
// NÃO limita o tamanho de um registo: o payload de um candidato é lido do ficheiro por
// [os.File.ReadAt], fora da janela.
const janelaDeRessincronizacao = 64 << 10

// contaOrfaos conta os registos ÍNTEGROS que existem DEPOIS do ponto de quebra. É o que
// distingue «cauda rasgada» (zero) de «corrupção a meio» (mais que zero) — a única
// pergunta cuja resposta muda o remédio.
//
// # AOS-346 — PORQUE NÃO SE PODE CONTINUAR DE ONDE O LEITOR FICOU
//
// A versão anterior contava a partir da posição corrente do leitor sequencial, e essa
// posição SÓ é uma fronteira de registo quando o comprimento lido estava certo. Um byte
// trocado no CABEÇALHO de comprimento leva o leitor a consumir um número errado de
// bytes: o CRC falha (bem), mas a contagem seguinte arranca DESALINHADA, não reconhece
// nada, devolve zero — e o [Open] conclui «cauda rasgada» e trunca. Medido a 2026-09-06,
// com 5 eventos (1480 bytes) e UM byte trocado no `len` do 2.º registo:
//
//	len maior  (@299 0x20->0xFF)  Open err=nil, leu 1 de 5, ficheiro 1480 -> 296
//	len menor  (@299 0x20->0x0A)  Open err=nil,             ficheiro 1480 -> 296
//	len +256   (@298 0x01->0x02)  Open err=nil,             ficheiro 1480 -> 296
//	controlo: payload (@306)      E_WAL_CORRUPTED_MID_LOG, orfaos=3, ficheiro INTACTO
//
// Quatro bytes em cada 296 (≈1,4% do ficheiro) eram zona cega: bit rot, um sector
// rasgado ou um write parcial de página aterram lá sem precisar de atacante.
//
// # PORQUE A RESSINCRONIZAÇÃO, E NÃO UM CRC SOBRE O CABEÇALHO
//
// Estender o `crc32` ao cabeçalho — a formulação directa do defeito — NÃO o fecha: para
// verificar o CRC é preciso localizar o trailer, e localizá-lo exige confiar no `len`
// que se quer verificar. Um `len` corrompido continua a fazer ler o número errado de
// bytes, e continua a desalinhar tudo o que venha a seguir. O que fecha o defeito é
// deixar de confiar na posição do leitor: procurar a fronteira do registo seguinte
// varrendo o ficheiro. Vantagem lateral, e não é pequena: o FORMATO NÃO MUDA — um WAL
// escrito por qualquer versão anterior continua legível, sem migração.
//
// Fica um residual DECLARADO: um `len` corrompido no ÚLTIMO registo, inflado para além
// dos bytes que restam, é indistinguível de um write rasgado sem um checksum do
// cabeçalho verificável de forma independente (o que seria mudança de formato). Cai no
// mesmo lado que [TestDurable_CorrupcaoNoULTIMORegistoEhCauda] já fixa: trunca.
func contaOrfaos(f *os.File, quebra, fim int64) int {
	inicio, ok := ressincroniza(f, quebra+1, fim)
	if !ok {
		return 0
	}
	r := bufio.NewReader(io.NewSectionReader(f, inicio, fim-inicio))
	n := 0
	for {
		payload, ok := leRegisto(r)
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

// ressincroniza procura o primeiro offset em [depois, fim) onde começa um registo
// COMPLETO, com CRC válido e payload que desserializa num [Event]. É o varrimento que
// repõe a fronteira quando o enquadramento se perdeu.
//
// O limite do varrimento é `maxRecordBytes+8` bytes a partir de `depois`, e o limite é
// DEMONSTRADO, não arbitrado: o registo corrompido não pode ocupar legitimamente mais do
// que isso, logo o registo íntegro seguinte — se existir — começa dentro dessa janela.
//
// Falso positivo exige que quatro bytes arbitrários formem um comprimento plausível, que
// os `len` bytes seguintes tenham um crc32 que bata com os quatro a seguir, e que o
// resultado desserialize num Event. É desprezável, e erra para o lado fail-closed.
func ressincroniza(f *os.File, depois, fim int64) (int64, bool) {
	if depois < 0 {
		depois = 0
	}
	limite := depois + int64(maxRecordBytes) + 8
	if limite > fim {
		limite = fim
	}
	buf := make([]byte, janelaDeRessincronizacao)
	var rec []byte
	for base := depois; base < limite; {
		n, err := f.ReadAt(buf, base)
		if n < 4 {
			return 0, false
		}
		for i := 0; i+4 <= n; i++ {
			off := base + int64(i)
			if off >= limite {
				return 0, false
			}
			tam := int64(binary.BigEndian.Uint32(buf[i : i+4]))
			if tam == 0 || tam > maxRecordBytes || off+4+tam+4 > fim {
				continue
			}
			if int64(cap(rec)) < tam+4 {
				rec = make([]byte, tam+4)
			}
			cand := rec[:tam+4]
			if _, rerr := f.ReadAt(cand, off+4); rerr != nil {
				continue
			}
			if binary.BigEndian.Uint32(cand[tam:]) != crc32.Checksum(cand[:tam], crcTable) {
				continue
			}
			var ev Event
			if json.Unmarshal(cand[:tam], &ev) != nil {
				continue
			}
			return off, true
		}
		if err != nil {
			return 0, false // fim do ficheiro alcançado sem encontrar fronteira
		}
		// Sobreposição de 3 bytes: um cabeçalho a cavalo da fronteira da janela tem de
		// ser visto na janela seguinte.
		base += int64(n) - 3
	}
	return 0, false
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
	return abrir(path, false, opts...)
}

// OpenReadOnly reabre um Event Store durável para INSPECÇÃO: faz o replay do WAL e
// devolve um [Store] que serve [Store.Read] normalmente, mas que NÃO anexa o WAL para
// append e NÃO toca no ficheiro. Qualquer escrita devolve [ErrReadOnly].
//
// # AOS-347 — PORQUE ISTO TINHA DE EXISTIR
//
// `aos wal-inspect`, `aos wal-summary` e `aos wal-count` chamavam [Open]. Os comentários
// diziam «com o contentor principal PARADO», o que é uma CONVENÇÃO DOCUMENTADA e não uma
// restrição imposta — e [Open] não pede posse nenhuma (a tranca de [LockWAL] é sobre o
// ficheiro irmão, deliberadamente, para não bloquear quem lê). Com o escritor vivo, um
// segundo [Open] tinha sucesso, reconstruía a SUA cabeça em memória, e:
//
//   - se escrevesse, atribuía seqs em colisão com os do escritor. Medido: seqs
//     [1 2 3 4 4 5 5] no ficheiro, e o arranque seguinte a recusar com
//     `E_RESTORE_ORDER: lote de restauro nao e gapless` — o nó não voltava a arrancar;
//   - mesmo sem escrever, TRUNCAVA: [Open] repõe a cauda parcial, e composto com o
//     defeito de enquadramento de AOS-346 um comando de LEITURA levou um WAL de 1480
//     para 592 bytes, apagando três eventos confirmados.
//
// Um abridor que não pode escrever nem truncar não tem por onde causar nenhuma das
// duas coisas. A convenção documentada passa a propriedade do tipo.
//
// A leitura continua a NÃO precisar de posse, que é a garantia que [LockWAL] promete a
// quem investiga um incidente com o nó a correr.
func OpenReadOnly(path string, opts ...Option) (*Store, error) {
	return abrir(path, true, opts...)
}

func abrir(path string, soLeitura bool, opts ...Option) (*Store, error) {
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
	//
	// AOS-347: só o abridor de ESCRITA o faz. Um abridor de inspecção não tem cauda
	// para preparar — não vai escrever nada — e o único efeito que a truncatura teria
	// aí seria destruir aquilo que o operador foi lá ver.
	if !soLeitura {
		if fi, statErr := os.Stat(path); statErr == nil && fi.Size() > validEnd {
			if err := os.Truncate(path, validEnd); err != nil {
				return nil, fmt.Errorf("eventstore: truncar tail parcial do WAL %q: %w", path, err)
			}
			// Torna durável a nova dimensão do ficheiro (metadados do directório) após a
			// truncatura do tail parcial, pela mesma razão POSIX (best-effort).
			fsyncDir(filepath.Dir(path))
		}
	}
	s, err := New(opts...)
	if err != nil {
		return nil, err
	}
	// O restauro corre ANTES de a postura de leitura ser selada: [restoreInto] usa
	// [Store.IngestStream], que um store já marcado só-leitura recusaria.
	if err := restoreInto(s, events); err != nil {
		_ = s.Close()
		return nil, err
	}
	if soLeitura {
		s.soLeitura = true
		return s, nil
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
