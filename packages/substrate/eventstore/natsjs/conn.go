package natsjs

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	// ErrFechado — a ligação já foi fechada por quem a detém.
	ErrFechado = errors.New("natsjs: ligação fechada")
	// ErrProtocolo — o servidor enviou algo que este cliente não sabe interpretar.
	// É sempre um defeito nosso ou uma incompatibilidade de versão, nunca do operador.
	ErrProtocolo = errors.New("natsjs: violação de protocolo")
	// ErrTimeout — o pedido não teve resposta dentro do prazo. NÃO diz que a escrita
	// não aconteceu: diz que não sabemos. Ver Conn.Request.
	ErrTimeout = errors.New("natsjs: sem resposta dentro do prazo")
)

// Msg é uma mensagem entregue pelo servidor.
type Msg struct {
	Subject string
	Reply   string
	Header  Header
	Data    []byte
}

// Conn é uma ligação a um servidor NATS. É segura para uso concorrente.
type Conn struct {
	c  net.Conn
	bw *bufio.Writer

	escrita sync.Mutex // serializa a escrita de comandos completos no socket

	mu     sync.Mutex
	subs   map[string]chan Msg
	sid    uint64
	fechou bool
	falha  error // primeira falha do leitor; entregue a quem espera
}

// Ligar abre uma ligação a addr ("host:porta") e faz o handshake.
//
// O handshake é INFO (do servidor) seguido de CONNECT (nosso). Declaramos
// headers:true porque TODAS as garantias que o AOS pede ao substrato viajam em
// cabeçalhos — sem isto o servidor nunca envia HMSG e o CAS fica mudo.
func Ligar(addr string, timeout time.Duration) (*Conn, error) {
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
		return nil, fmt.Errorf("%w: esperava INFO, veio %q", ErrProtocolo, primeiraLinha(linha))
	}
	if err := c.SetReadDeadline(time.Time{}); err != nil {
		_ = c.Close()
		return nil, err
	}

	cn := &Conn{c: c, bw: bufio.NewWriterSize(c, 64*1024), subs: map[string]chan Msg{}}
	if err := cn.enviar(`CONNECT {"verbose":false,"pedantic":false,"tls_required":false,"headers":true,"no_responders":true,"name":"aos-eventstore","lang":"go","version":"stdlib"}` + "\r\n"); err != nil {
		_ = c.Close()
		return nil, err
	}
	go cn.ler(br)
	return cn, nil
}

// Close fecha a ligação. Idempotente.
func (cn *Conn) Close() error {
	cn.mu.Lock()
	if cn.fechou {
		cn.mu.Unlock()
		return nil
	}
	cn.fechou = true
	for _, ch := range cn.subs {
		close(ch)
	}
	cn.subs = map[string]chan Msg{}
	cn.mu.Unlock()
	return cn.c.Close()
}

// Request publica em subject (com cabeçalhos, se houver) e espera UMA resposta.
//
// # O que um ErrTimeout aqui significa — e o que NÃO significa
//
// Significa que não houve resposta a tempo. NÃO significa que a escrita não aconteceu:
// o pedido pode ter sido aplicado e a resposta perdida. Quem chama tem de tratar o
// timeout como INDETERMINADO.
//
// No Event Store isso é seguro e é a razão de o CAS ser a primitiva: o retry repete o
// mesmo Nats-Expected-Last-Subject-Sequence, e o servidor responde «committed» se foi
// o nosso que passou ou «wrong last sequence» se já lá está — em nenhum dos casos
// duplica. É assim que a garantia «ERRO ⇒ NADA FICOU DURÁVEL» do contrato C2 sobrevive
// a um substrato remoto.
func (cn *Conn) Request(subject string, h Header, dados []byte, timeout time.Duration) (Msg, error) {
	inbox, err := novoInbox()
	if err != nil {
		return Msg{}, err
	}
	respostas, sid, err := cn.subscrever(inbox)
	if err != nil {
		return Msg{}, err
	}
	defer cn.dessubscrever(sid)

	if err := cn.publicar(subject, inbox, h, dados); err != nil {
		return Msg{}, err
	}

	temporizador := time.NewTimer(timeout)
	defer temporizador.Stop()
	select {
	case m, ok := <-respostas:
		if !ok {
			return Msg{}, cn.erroDeFecho()
		}
		return m, nil
	case <-temporizador.C:
		return Msg{}, fmt.Errorf("%w (%s, %s)", ErrTimeout, subject, timeout)
	}
}

// --- Protocolo -------------------------------------------------------------

func (cn *Conn) subscrever(subject string) (<-chan Msg, string, error) {
	cn.mu.Lock()
	if cn.fechou {
		cn.mu.Unlock()
		return nil, "", ErrFechado
	}
	cn.sid++
	sid := strconv.FormatUint(cn.sid, 10)
	ch := make(chan Msg, 8)
	cn.subs[sid] = ch
	cn.mu.Unlock()

	if err := cn.enviar("SUB " + subject + " " + sid + "\r\n"); err != nil {
		cn.dessubscrever(sid)
		return nil, "", err
	}
	return ch, sid, nil
}

func (cn *Conn) dessubscrever(sid string) {
	cn.mu.Lock()
	ch, ok := cn.subs[sid]
	if ok {
		delete(cn.subs, sid)
		close(ch)
	}
	fechou := cn.fechou
	cn.mu.Unlock()
	if ok && !fechou {
		_ = cn.enviar("UNSUB " + sid + "\r\n")
	}
}

// publicar emite PUB (sem cabeçalhos) ou HPUB (com). O comando é escrito sob um só
// lock: um PUB partido a meio por outro escritor corromperia o fluxo do socket.
func (cn *Conn) publicar(subject, reply string, h Header, dados []byte) error {
	if len(h) == 0 {
		cab := fmt.Sprintf("PUB %s %s %d\r\n", subject, reply, len(dados))
		return cn.enviarPartes(strings.TrimRight(cab, "\r\n")+"\r\n", dados)
	}
	bloco := h.codificar()
	cab := fmt.Sprintf("HPUB %s %s %d %d\r\n", subject, reply, len(bloco), len(bloco)+len(dados))
	return cn.enviarPartes(cab, append(append([]byte{}, bloco...), dados...))
}

func (cn *Conn) enviar(s string) error { return cn.enviarPartes(s, nil) }

func (cn *Conn) enviarPartes(cab string, corpo []byte) error {
	cn.escrita.Lock()
	defer cn.escrita.Unlock()
	cn.mu.Lock()
	fechou := cn.fechou
	cn.mu.Unlock()
	if fechou {
		return ErrFechado
	}
	if _, err := cn.bw.WriteString(cab); err != nil {
		return err
	}
	if corpo != nil {
		if _, err := cn.bw.Write(corpo); err != nil {
			return err
		}
		if _, err := cn.bw.WriteString("\r\n"); err != nil {
			return err
		}
	}
	return cn.bw.Flush()
}

// ler é o laço do leitor. Corre numa goroutine própria até a ligação fechar.
func (cn *Conn) ler(br *bufio.Reader) {
	for {
		linha, err := br.ReadString('\n')
		if err != nil {
			cn.morrer(err)
			return
		}
		campos := strings.Fields(linha)
		if len(campos) == 0 {
			continue
		}
		switch strings.ToUpper(campos[0]) {
		case "PING":
			_ = cn.enviar("PONG\r\n")
		case "PONG", "+OK":
			// nada a fazer
		case "-ERR":
			cn.morrer(fmt.Errorf("natsjs: servidor recusou: %s", primeiraLinha(linha)))
			return
		case "INFO":
			// re-anúncio de topologia; este cliente não faz descoberta de cluster
		case "MSG", "HMSG":
			if err := cn.entregar(br, campos); err != nil {
				cn.morrer(err)
				return
			}
		default:
			cn.morrer(fmt.Errorf("%w: comando desconhecido %q", ErrProtocolo, campos[0]))
			return
		}
	}
}

// entregar lê o corpo de um MSG/HMSG e encaminha-o para o subscritor.
//
//	MSG  <subject> <sid> [reply] <bytes>
//	HMSG <subject> <sid> [reply] <hdr_bytes> <total_bytes>
func (cn *Conn) entregar(br *bufio.Reader, campos []string) error {
	comHeader := strings.EqualFold(campos[0], "HMSG")
	minimo := 4
	if comHeader {
		minimo = 5
	}
	if len(campos) < minimo {
		return fmt.Errorf("%w: %s com %d campos", ErrProtocolo, campos[0], len(campos))
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
			return fmt.Errorf("%w: hdr_len %q", ErrProtocolo, resto[0])
		}
		hdrLen = n
		totalIdx = 1
	}
	total, err := strconv.Atoi(resto[totalIdx])
	if err != nil {
		return fmt.Errorf("%w: total %q", ErrProtocolo, resto[totalIdx])
	}
	if hdrLen > total {
		return fmt.Errorf("%w: hdr_len %d > total %d", ErrProtocolo, hdrLen, total)
	}

	corpo := make([]byte, total+2) // +2 pelo CRLF final
	if _, err := ioReadFull(br, corpo); err != nil {
		return err
	}
	corpo = corpo[:total]
	if comHeader {
		m.Header = descodificarHeader(corpo[:hdrLen])
		m.Data = corpo[hdrLen:]
	} else {
		m.Data = corpo
	}

	cn.mu.Lock()
	ch, ok := cn.subs[sid]
	cn.mu.Unlock()
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

func (cn *Conn) morrer(err error) {
	cn.mu.Lock()
	if cn.falha == nil {
		cn.falha = err
	}
	if !cn.fechou {
		cn.fechou = true
		for _, ch := range cn.subs {
			close(ch)
		}
		cn.subs = map[string]chan Msg{}
	}
	cn.mu.Unlock()
	_ = cn.c.Close()
}

func (cn *Conn) erroDeFecho() error {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	if cn.falha != nil {
		return cn.falha
	}
	return ErrFechado
}

func novoInbox() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("natsjs: gerar inbox: %w", err)
	}
	return "_INBOX." + hex.EncodeToString(b[:]), nil
}

func primeiraLinha(s string) string {
	return strings.TrimRight(s, "\r\n")
}

// ioReadFull evita importar io só por uma função; mantém o pacote com o mínimo.
func ioReadFull(br *bufio.Reader, p []byte) (int, error) {
	n := 0
	for n < len(p) {
		k, err := br.Read(p[n:])
		n += k
		if err != nil {
			return n, err
		}
	}
	return n, nil
}
