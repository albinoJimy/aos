package natsjs

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Erros do cliente. São sentinelas para que o chamador possa distinguir «o servidor
// recusou» de «a ligação partiu» — distinção de que o Event Store depende para saber
// se um retry é seguro.
var (
	// ErrClosed — a ligação já foi fechada por quem a detém.
	ErrClosed = errors.New("natsjs: ligação fechada")
	// ErrProtocol — o servidor enviou algo que este cliente não sabe interpretar, ou
	// o chamador pediu algo que não é representável no protocolo. É um defeito de
	// software ou uma incompatibilidade de versão — NÃO é uma condição operacional.
	ErrProtocol = errors.New("natsjs: violação de protocolo")
	// ErrTimeout — o pedido não teve resposta dentro do prazo.
	ErrTimeout = errors.New("natsjs: sem resposta dentro do prazo")
	// ErrDesligado — não há socket vivo NESTE momento; o cliente está a reconectar.
	//
	// É a promessa FORTE: uma operação que falha assim nem chegou a sair, logo nada ficou
	// durável. Distingue-se de [ErrIndeterminate] exactamente por isso — e a distinção
	// não é cosmética, porque uma é seguro repetir sem pensar e a outra exige o CAS.
	ErrDesligado = errors.New("natsjs: sem ligação neste momento (a reconectar)")

	// ErrNoResponders — ninguém está a servir o subject (status 503). É a condição
	// OPERACIONAL mais comum: JetStream desligado, stream inexistente, subject fora do
	// stream, ou sem permissões. A acção é do operador, não do programador — por isso
	// é sentinela própria e não ErrProtocol.
	ErrNoResponders = errors.New("natsjs: ninguém serve este subject (503)")

	// ErrIndeterminate — NÃO SE SABE se a escrita ficou durável.
	//
	// # Porque existe, e o que corrige
	//
	// O contrato deste pacote prometia «ERRO ⇒ NADA FICOU DURÁVEL» com UMA excepção
	// nomeada (o timeout). A auditoria adversarial de 2026-08-31 MEDIU uma segunda: se
	// a ligação parte DEPOIS de o comando ter sido escoado por inteiro, o pedido pode
	// ter sido aplicado e a resposta perdida — mas o erro devolvido era um EOF genérico,
	// indistinguível de uma falha que nada escreveu. Um chamador que siga a promessa à
	// letra (reverter em memória, como o GraphBuilder faz) DIVERGE do log.
	//
	// A regra correcta não é «timeout é especial»: é que TODO o erro posterior ao
	// escoamento do comando é indeterminado. É isso que esta sentinela marca, e o
	// timeout passa a ser um caso dela.
	//
	// Um indeterminado é RECUPERÁVEL sem duplicar, e é a razão de o CAS ser a
	// primitiva: o retry repete o mesmo expected_seq e o servidor responde «committed»
	// se foi o nosso que passou, ou «wrong last sequence» se já lá está.
	ErrIndeterminate = errors.New("natsjs: indeterminado — a escrita pode ter sido aplicada")
)

// maxFrameBytes é o tecto absoluto de um quadro recebido, usado quando o servidor não
// anuncia max_payload. Sem tecto, um `total` forjado no fio é um OOM: MEDIDO a
// 2026-08-31 com `MSG <i> <s> 100000000`, cem vezes acima do max_payload anunciado,
// aceite e alocado. Alinhado com maxRecordBytes do WAL (64 MiB).
const maxFrameBytes = 64 << 20

// defaultWriteTimeout limita o tempo que uma escrita pode passar bloqueada no
// socket. Sem ele, um socket que deixa de drenar bloqueia o Flush INDEFINIDAMENTE com o
// lock de escrita na mão — MEDIDO: um Publish com timeout de 200 ms continuava
// bloqueado 5 s depois, porque o prazo do chamador só era avaliado DEPOIS da escrita.
const defaultWriteTimeout = 30 * time.Second

// Msg é uma mensagem entregue pelo servidor.
type Msg struct {
	Subject string
	Reply   string
	Header  Header
	// Status é o código da linha de estado do bloco de cabeçalhos (ex.: 503), ou 0 se
	// a mensagem não for de estado. Sem isto, um 503 é indistinguível de uma resposta
	// sem cabeçalhos e chega ao chamador como JSON ilegível.
	Status int
	Data   []byte
}

// serverInfo é o subconjunto do INFO que este cliente usa.
type serverInfo struct {
	Headers    bool `json:"headers"`
	MaxPayload int  `json:"max_payload"`
}

// Conn é uma ligação a um servidor NATS. É segura para uso concorrente.
type Conn struct {
	servidores   []string // por onde se tenta reconectar, por ordem
	prazoLigacao time.Duration

	c            net.Conn
	bw           *bufio.Writer
	writeTimeout time.Duration
	maxFrame     int
	ligada       bool // ha socket vivo AGORA

	writeMu sync.Mutex // serializa a escrita de comandos completos no socket

	mu           sync.Mutex
	subs         map[string]chan Msg
	sid          uint64
	closed       bool
	reconectando bool
	failure      error // primeira falha do leitor; entregue a quem espera
}

// Connect abre uma ligação a addr ("host:porta") e faz o handshake.
//
// O handshake é INFO (do servidor) seguido de CONNECT (nosso). O INFO é PARSEADO, não
// apenas reconhecido pelo prefixo: dele saem duas coisas de que a correcção depende —
// se o servidor suporta cabeçalhos (sem eles o CAS é mudo, e a ligação é recusada
// fail-closed em vez de morrer mais tarde sem explicação) e o max_payload, que limita
// o que aceitamos alocar a partir do fio.
// Connect liga-se a UM servidor. Para reconexão automática, ver [ConnectServers]: com um
// só endereço, a morte desse nó deixa o cliente a tentar sempre o mesmo — o que é
// correcto, mas menos útil do que ter para onde ir.
func Connect(addr string, timeout time.Duration) (*Conn, error) {
	return ConnectServers([]string{addr}, timeout)
}

// ConnectServers liga-se ao PRIMEIRO servidor que aceitar, e guarda a lista para as
// reconexões.
//
// # Porque a reconexão existe, e o que ela corrige
//
// MEDIDO a 2026-09-01: matar o nó a que o cliente estava ligado deixava-o a devolver
// «ligação fechada» PARA SEMPRE — 20 s depois, e indefinidamente. A morte de UM nó do
// cluster virava um incidente do NÓ INTEIRO, que é o oposto do que um Event Store
// replicado existe para dar. O AC1 do AOS-100 («a perda de uma réplica não interrompe
// escritas») só era verdade se o nó morto não fosse o nosso.
func ConnectServers(addrs []string, timeout time.Duration) (*Conn, error) {
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%w: sem servidores", ErrProtocol)
	}
	cn := &Conn{
		servidores:   append([]string(nil), addrs...),
		prazoLigacao: timeout,
		writeTimeout: defaultWriteTimeout,
		subs:         map[string]chan Msg{},
	}
	var ultimo error
	for _, addr := range cn.servidores {
		if err := cn.ligarA(addr); err != nil {
			ultimo = err
			continue
		}
		return cn, nil
	}
	return nil, ultimo
}

// ligarA estabelece o socket e o handshake com UM servidor, e arranca o leitor.
func (cn *Conn) ligarA(addr string) error {
	timeout := cn.prazoLigacao
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return fmt.Errorf("natsjs: ligar a %s: %w", addr, err)
	}
	br := bufio.NewReaderSize(c, 64*1024)

	if err := c.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		_ = c.Close()
		return err
	}
	linha, err := br.ReadString('\n')
	if err != nil {
		_ = c.Close()
		return fmt.Errorf("natsjs: ler INFO: %w", err)
	}
	if !strings.HasPrefix(linha, "INFO ") {
		_ = c.Close()
		return fmt.Errorf("%w: esperava INFO, veio %q", ErrProtocol, firstLine(linha))
	}
	var info serverInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(linha[len("INFO "):])), &info); err != nil {
		_ = c.Close()
		return fmt.Errorf("%w: INFO ilegível: %v", ErrProtocol, err)
	}
	if !info.Headers {
		_ = c.Close()
		return fmt.Errorf("%w: o servidor não anuncia suporte de cabeçalhos, e é neles que "+
			"viajam o expected_seq e a chave de deduplicação — recusado fail-closed", ErrProtocol)
	}
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		_ = c.Close()
		return err
	}

	maxFrame := maxFrameBytes
	if info.MaxPayload > 0 && info.MaxPayload < maxFrameBytes {
		maxFrame = info.MaxPayload + (1 << 20) // folga para o bloco de cabeçalhos
	}

	cn.mu.Lock()
	cn.c, cn.bw, cn.maxFrame, cn.ligada = c, bufio.NewWriterSize(c, 64*1024), maxFrame, true
	cn.mu.Unlock()

	if err := cn.send(`CONNECT {"verbose":false,"pedantic":false,"tls_required":false,"headers":true,"no_responders":true,"name":"aos-eventstore","lang":"go","version":"stdlib"}` + "\r\n"); err != nil {
		cn.mu.Lock()
		cn.ligada = false
		cn.mu.Unlock()
		_ = c.Close()
		return err
	}
	go cn.readLoop(br)
	return nil
}

// Close fecha a ligação. Idempotente.
func (cn *Conn) Close() error {
	cn.mu.Lock()
	if cn.closed {
		cn.mu.Unlock()
		return nil
	}
	cn.closed = true
	for _, ch := range cn.subs {
		close(ch)
	}
	cn.subs = map[string]chan Msg{}
	c := cn.c
	cn.ligada = false
	cn.mu.Unlock()
	if c == nil {
		return nil
	}
	return c.Close()
}

// Request publica em subject (com cabeçalhos, se houver) e espera UMA resposta.
//
// # Uma falha DEPOIS de o comando sair é INDETERMINADA
//
// Assim que o comando é escoado para o socket, deixamos de poder afirmar que nada ficou
// durável: o pedido pode ter sido aplicado e a resposta perdida. Todo o erro a partir
// desse ponto — timeout, ligação partida, fecho concorrente — é embrulhado em
// [ErrIndeterminate]. Só as falhas ANTERIORES ao escoamento mantêm a promessa forte
// «nada ficou durável».
//
// No Event Store o indeterminado é seguro, e é a razão de o CAS ser a primitiva: o
// retry repete o mesmo expected_seq e o servidor distingue os dois casos por nós.
func (cn *Conn) Request(subject string, h Header, data []byte, timeout time.Duration) (Msg, error) {
	inbox, err := newInbox()
	if err != nil {
		return Msg{}, err
	}
	respostas, sid, err := cn.subscribe(inbox)
	if err != nil {
		return Msg{}, err
	}
	defer cn.unsubscribe(sid)

	// Falha ANTES do escoamento: a promessa forte mantém-se, sem embrulho.
	if err := cn.publish(subject, inbox, h, data); err != nil {
		return Msg{}, err
	}

	temporizador := time.NewTimer(timeout)
	defer temporizador.Stop()
	select {
	case m, ok := <-respostas:
		if !ok {
			return Msg{}, fmt.Errorf("%w: %w", ErrIndeterminate, cn.closeErr())
		}
		if m.Status == 503 {
			// 503 é resposta DO SERVIDOR: ele processou o pedido e disse que ninguém
			// serve o subject. Nada ficou durável, e não é indeterminado.
			return m, fmt.Errorf("%w (%s)", ErrNoResponders, subject)
		}
		return m, nil
	case <-temporizador.C:
		return Msg{}, fmt.Errorf("%w: %w (%s, %s)", ErrIndeterminate, ErrTimeout, subject, timeout)
	}
}

// --- Protocolo -------------------------------------------------------------

func (cn *Conn) subscribe(subject string) (<-chan Msg, string, error) {
	return cn.subscribeBuffered(subject, 8)
}

// subscribeBuffered subscreve com a profundidade de fila dada. Ver
// [Conn.SubscribeSubjectBuffered] para porque a profundidade e do CHAMADOR.
func (cn *Conn) subscribeBuffered(subject string, buf int) (<-chan Msg, string, error) {
	if buf < 1 {
		buf = 1
	}
	cn.mu.Lock()
	if cn.closed {
		cn.mu.Unlock()
		return nil, "", ErrClosed
	}
	cn.sid++
	sid := strconv.FormatUint(cn.sid, 10)
	ch := make(chan Msg, buf)
	cn.subs[sid] = ch
	cn.mu.Unlock()

	if err := cn.send("SUB " + subject + " " + sid + "\r\n"); err != nil {
		cn.unsubscribe(sid)
		return nil, "", err
	}
	return ch, sid, nil
}

func (cn *Conn) unsubscribe(sid string) {
	cn.mu.Lock()
	ch, ok := cn.subs[sid]
	if ok {
		delete(cn.subs, sid)
		close(ch)
	}
	closed := cn.closed
	cn.mu.Unlock()
	if ok && !closed {
		_ = cn.send("UNSUB " + sid + "\r\n")
	}
}

// publicar emite PUB (sem cabeçalhos) ou HPUB (com). O comando é escrito sob um só
// lock: um PUB partido a meio por outro escritor corromperia o fluxo do socket.
//
// # Três defeitos que esta função já teve, e que o formato agora impede
//
// 1. PAYLOAD VAZIO SEM TERMINADOR. `enviarPartes` só escrevia o CRLF final quando o
// corpo era não-nil, e `publicar` passava o `data` do chamador — nil quando não há
// corpo. Um PUB de comprimento zero (o que toda a API do JetStream sem corpo faz, ex.
// STREAM.INFO) saía sem terminador, o servidor lia o comando seguinte no sítio errado e
// respondia `-ERR 'Unknown Protocol Operation'`. MEDIDO a 2026-08-31.
//
// 2. ESPAÇO A DOBRAR SEM REPLY. Com reply vazio o formato dava `PUB subj  0`.
//
// 3. INJECÇÃO NO SUBJECT. Um subject com espaço forjava argumentos extra; com CRLF,
// comandos inteiros. MEDIDO: `Publish("aos.outro _INBOX.forjado", …)` emitia quatro
// argumentos e matava a ligação. Os tokens são agora validados.
func (cn *Conn) publish(subject, reply string, h Header, data []byte) error {
	if err := validateToken("subject", subject); err != nil {
		return err
	}
	if reply != "" {
		if err := validateToken("reply", reply); err != nil {
			return err
		}
	}
	if data == nil {
		data = []byte{}
	}
	args := []string{subject}
	if reply != "" {
		args = append(args, reply)
	}

	if len(h) == 0 {
		args = append(args, strconv.Itoa(len(data)))
		return cn.sendFrame("PUB "+strings.Join(args, " ")+"\r\n", data)
	}
	bloco, err := h.encode()
	if err != nil {
		return err
	}
	args = append(args, strconv.Itoa(len(bloco)), strconv.Itoa(len(bloco)+len(data)))
	corpo := make([]byte, 0, len(bloco)+len(data))
	corpo = append(corpo, bloco...)
	corpo = append(corpo, data...)
	return cn.sendFrame("HPUB "+strings.Join(args, " ")+"\r\n", corpo)
}

func (cn *Conn) send(s string) error { return cn.sendFrame(s, nil) }

// enviarPartes escreve um comando completo. `corpo` nil significa comando SEM payload
// (SUB, UNSUB, PONG, CONNECT); um slice vazio mas não-nil significa payload de
// comprimento zero, que LEVA terminador — a distinção é o defeito 1 de [Conn.publicar].
func (cn *Conn) sendFrame(head string, body []byte) error {
	cn.writeMu.Lock()
	defer cn.writeMu.Unlock()
	cn.mu.Lock()
	closed, ligada, c := cn.closed, cn.ligada, cn.c
	cn.mu.Unlock()
	if closed {
		return ErrClosed
	}
	// Sem socket vivo a operação NEM CHEGA A SAIR: é a promessa forte, e é seguro
	// repetir assim que a reconexão pegar. Distinta de [ErrIndeterminate], que é o que
	// se devolve quando o comando JÁ saiu.
	if !ligada || c == nil {
		return ErrDesligado
	}
	// Sem prazo de escrita, um socket que não drena bloqueia aqui para sempre com o
	// lock na mão — e arrasta consigo o PONG do leitor, que também escreve.
	if err := c.SetWriteDeadline(time.Now().Add(cn.writeTimeout)); err != nil {
		return err
	}
	err := cn.writeOut(head, body)
	if err != nil {
		// Uma escrita falhada deixa o fluxo do socket num ponto desconhecido: o que
		// se seguisse seria interpretado a partir do sítio errado. A ligação morre.
		cn.die(fmt.Errorf("natsjs: escrita falhou: %w", err))
	}
	return err
}

func (cn *Conn) writeOut(head string, body []byte) error {
	if _, err := cn.bw.WriteString(head); err != nil {
		return err
	}
	if body != nil {
		if _, err := cn.bw.Write(body); err != nil {
			return err
		}
		if _, err := cn.bw.WriteString("\r\n"); err != nil {
			return err
		}
	}
	return cn.bw.Flush()
}

// ler é o laço do leitor. Corre numa goroutine própria até a ligação fechar.
func (cn *Conn) readLoop(br *bufio.Reader) {
	for {
		linha, err := br.ReadString('\n')
		if err != nil {
			cn.die(err)
			return
		}
		campos := strings.Fields(linha)
		if len(campos) == 0 {
			continue
		}
		switch strings.ToUpper(campos[0]) {
		case "PING":
			_ = cn.send("PONG\r\n")
		case "PONG", "+OK":
			// nada a fazer
		case "-ERR":
			cn.die(fmt.Errorf("natsjs: servidor recusou: %s", firstLine(linha)))
			return
		case "INFO":
			// re-anúncio de topologia; este cliente não faz descoberta de cluster
		case "MSG", "HMSG":
			if err := cn.deliver(br, campos); err != nil {
				cn.die(err)
				return
			}
		default:
			cn.die(fmt.Errorf("%w: comando desconhecido %q", ErrProtocol, campos[0]))
			return
		}
	}
}

// entregar lê o corpo de um MSG/HMSG e encaminha-o para o subscritor.
//
//	MSG  <subject> <sid> [reply] <bytes>
//	HMSG <subject> <sid> [reply] <hdr_bytes> <total_bytes>
func (cn *Conn) deliver(br *bufio.Reader, campos []string) error {
	comHeader := strings.EqualFold(campos[0], "HMSG")
	minimo := 4
	if comHeader {
		minimo = 5
	}
	if len(campos) < minimo {
		return fmt.Errorf("%w: %s com %d campos", ErrProtocol, campos[0], len(campos))
	}

	m := Msg{Subject: campos[1]}
	sid := campos[2]
	resto := campos[3:]
	if len(resto) == minimo-2 { // há reply antes dos tamanhos
		m.Reply = resto[0]
		resto = resto[1:]
	}

	var hdrLen int
	totalIdx := 0
	if comHeader {
		n, err := strconv.Atoi(resto[0])
		if err != nil {
			return fmt.Errorf("%w: hdr_len %q", ErrProtocol, resto[0])
		}
		hdrLen = n
		totalIdx = 1
	}
	total, err := strconv.Atoi(resto[totalIdx])
	if err != nil {
		return fmt.Errorf("%w: total %q", ErrProtocol, resto[totalIdx])
	}
	// Tamanhos negativos vêm do FIO e chegavam a `make` e a um slice — dois panics
	// distintos, ambos a abortar o processo do nó. MEDIDO a 2026-08-31.
	if hdrLen < 0 || total < 0 {
		return fmt.Errorf("%w: tamanhos negativos (hdr=%d total=%d)", ErrProtocol, hdrLen, total)
	}
	if hdrLen > total {
		return fmt.Errorf("%w: hdr_len %d > total %d", ErrProtocol, hdrLen, total)
	}
	if total > cn.maxFrame {
		return fmt.Errorf("%w: quadro de %d bytes acima do tecto de %d", ErrProtocol, total, cn.maxFrame)
	}

	corpo := make([]byte, total+2) // +2 pelo CRLF final
	if _, err := io.ReadFull(br, corpo); err != nil {
		return err
	}
	corpo = corpo[:total]
	if comHeader {
		m.Header, m.Status = decodeHeader(corpo[:hdrLen])
		m.Data = corpo[hdrLen:]
	} else {
		m.Data = corpo
	}

	// A procura e o envio são feitos SOB O MESMO LOCK que fecha os canais.
	//
	// O DEFEITO QUE FECHA, medido: o ponteiro do canal era obtido sob `mu`, o lock era
	// LARGADO, e só depois se enviava. Entre as duas instruções, o `defer
	// dessubscrever` de um Request expirado fechava o canal — `panic: send on closed
	// channel`, na goroutine do leitor, sem recover: o processo do nó MORRE. Reproduzido
	// 3/3 em ~2s com 8 goroutines a fazer Request com prazo mínimo, que é exactamente a
	// situação normal de um cluster sob carga.
	//
	// Manter o lock durante o envio é seguro porque o envio é NÃO-BLOQUEANTE.
	cn.mu.Lock()
	defer cn.mu.Unlock()
	ch, ok := cn.subs[sid]
	if !ok {
		return nil // subscrição já removida; a mensagem é descartada
	}
	select {
	case ch <- m:
	default:
		// Subscritor lento: descartar é preferível a bloquear o leitor, que serve
		// TODAS as subscrições. Este cliente só faz request-reply (fila de 8), pelo
		// que aqui só chega quem já não está a ouvir.
	}
	return nil
}

// die trata a QUEBRA da ligação: fecha o socket, avisa os subscritores e manda reconectar.
//
// # Porque os canais dos subscritores são FECHADOS e não silenciosamente retomados
//
// Reconectar o socket não ressuscita um consumidor push do JetStream: ele era efémero e
// morreu com a ligação. Se aqui se reconectasse e se ficasse calado, uma subscrição
// deixaria de entregar sem que ninguém soubesse — e uma subscrição morta em silêncio é
// pior do que uma que falha, porque quem depende dela nunca desconfia.
//
// Fechando o canal, quem subscreveu É INFORMADO e pode recriar o consumidor. É o que o
// Store faz.
func (cn *Conn) die(err error) {
	cn.mu.Lock()
	if cn.failure == nil {
		cn.failure = err
	}
	fechadoPeloDono := cn.closed
	c := cn.c
	cn.ligada = false
	for _, ch := range cn.subs {
		close(ch)
	}
	cn.subs = map[string]chan Msg{}
	arrancar := !fechadoPeloDono && !cn.reconectando
	if arrancar {
		cn.reconectando = true
	}
	cn.mu.Unlock()

	if c != nil {
		_ = c.Close()
	}
	if arrancar {
		go cn.reconectar()
	}
}

// reconectar tenta os servidores por ordem, com recuo exponencial limitado, até haver
// ligação ou o dono fechar.
//
// NUNCA desiste sozinho: desistir seria transformar uma falha transitória do cluster numa
// falha PERMANENTE do nó — que foi exactamente o defeito medido a 2026-09-01, quando
// matar o nó da ligação deixava o cliente a devolver «ligação fechada» indefinidamente.
func (cn *Conn) reconectar() {
	espera := 100 * time.Millisecond
	const tecto = 5 * time.Second
	for {
		cn.mu.Lock()
		fechado, ligada := cn.closed, cn.ligada
		if fechado || ligada {
			cn.reconectando = false
			cn.mu.Unlock()
			return
		}
		cn.mu.Unlock()

		for _, addr := range cn.servidores {
			if err := cn.ligarA(addr); err == nil {
				cn.mu.Lock()
				cn.failure, cn.reconectando = nil, false
				cn.mu.Unlock()
				return
			}
		}
		time.Sleep(espera)
		if espera < tecto {
			espera *= 2
		}
	}
}

func (cn *Conn) closeErr() error {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	if cn.failure != nil {
		return cn.failure
	}
	return ErrClosed
}

// validateToken recusa um subject/reply que não seja representável num comando: o
// protocolo separa argumentos por espaços e termina linhas em CRLF, pelo que um token
// com qualquer um deles forja argumentos — ou comandos inteiros.
func validateToken(nome, v string) error {
	if v == "" {
		return fmt.Errorf("%w: %s vazio", ErrProtocol, nome)
	}
	if strings.ContainsAny(v, " \t\r\n") {
		return fmt.Errorf("%w: %s %q contém espaço ou fim-de-linha", ErrProtocol, nome, v)
	}
	return nil
}

func newInbox() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("natsjs: gerar inbox: %w", err)
	}
	return "_INBOX." + hex.EncodeToString(b[:]), nil
}

func firstLine(s string) string {
	return strings.TrimRight(s, "\r\n")
}
