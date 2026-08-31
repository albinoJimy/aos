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
	c            net.Conn
	bw           *bufio.Writer
	writeTimeout time.Duration
	maxFrame     int

	writeMu sync.Mutex // serializa a escrita de comandos completos no socket

	mu      sync.Mutex
	subs    map[string]chan Msg
	sid     uint64
	closed  bool
	failure error // primeira falha do leitor; entregue a quem espera
}

// Connect abre uma ligação a addr ("host:porta") e faz o handshake.
//
// O handshake é INFO (do servidor) seguido de CONNECT (nosso). O INFO é PARSEADO, não
// apenas reconhecido pelo prefixo: dele saem duas coisas de que a correcção depende —
// se o servidor suporta cabeçalhos (sem eles o CAS é mudo, e a ligação é recusada
// fail-closed em vez de morrer mais tarde sem explicação) e o max_payload, que limita
// o que aceitamos alocar a partir do fio.
func Connect(addr string, timeout time.Duration) (*Conn, error) {
	c, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("natsjs: ligar a %s: %w", addr, err)
	}
	br := bufio.NewReaderSize(c, 64*1024)

	if err := c.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		_ = c.Close()
		return nil, err
	}
	linha, err := br.ReadString('\n')
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("natsjs: ler INFO: %w", err)
	}
	if !strings.HasPrefix(linha, "INFO ") {
		_ = c.Close()
		return nil, fmt.Errorf("%w: esperava INFO, veio %q", ErrProtocol, firstLine(linha))
	}
	var info serverInfo
	if err := json.Unmarshal([]byte(strings.TrimSpace(linha[len("INFO "):])), &info); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("%w: INFO ilegível: %v", ErrProtocol, err)
	}
	if !info.Headers {
		_ = c.Close()
		return nil, fmt.Errorf("%w: o servidor não anuncia suporte de cabeçalhos, e é neles que "+
			"viajam o expected_seq e a chave de deduplicação — recusado fail-closed", ErrProtocol)
	}
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		_ = c.Close()
		return nil, err
	}

	maxFrame := maxFrameBytes
	if info.MaxPayload > 0 && info.MaxPayload < maxFrameBytes {
		maxFrame = info.MaxPayload + (1 << 20) // folga para o bloco de cabeçalhos
	}

	cn := &Conn{
		c:            c,
		bw:           bufio.NewWriterSize(c, 64*1024),
		writeTimeout: defaultWriteTimeout,
		maxFrame:     maxFrame,
		subs:         map[string]chan Msg{},
	}
	if err := cn.send(`CONNECT {"verbose":false,"pedantic":false,"tls_required":false,"headers":true,"no_responders":true,"name":"aos-eventstore","lang":"go","version":"stdlib"}` + "\r\n"); err != nil {
		_ = c.Close()
		return nil, err
	}
	go cn.readLoop(br)
	return cn, nil
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
	cn.mu.Unlock()
	return cn.c.Close()
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
	cn.mu.Lock()
	if cn.closed {
		cn.mu.Unlock()
		return nil, "", ErrClosed
	}
	cn.sid++
	sid := strconv.FormatUint(cn.sid, 10)
	ch := make(chan Msg, 8)
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
	closed := cn.closed
	cn.mu.Unlock()
	if closed {
		return ErrClosed
	}
	// Sem prazo de escrita, um socket que não drena bloqueia aqui para sempre com o
	// lock na mão — e arrasta consigo o PONG do leitor, que também escreve.
	if err := cn.c.SetWriteDeadline(time.Now().Add(cn.writeTimeout)); err != nil {
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

func (cn *Conn) die(err error) {
	cn.mu.Lock()
	if cn.failure == nil {
		cn.failure = err
	}
	if !cn.closed {
		cn.closed = true
		for _, ch := range cn.subs {
			close(ch)
		}
		cn.subs = map[string]chan Msg{}
	}
	cn.mu.Unlock()
	_ = cn.c.Close()
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
